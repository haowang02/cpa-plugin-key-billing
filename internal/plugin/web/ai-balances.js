// Upstreams whose API keys can read their own account balance. A key's base URL must name one of
// these hosts exactly, so a key is only ever sent to the upstream that issued it.
const AI_BALANCE_UPSTREAMS = [
  {
    hosts: ["api.deepseek.com"],
    async read(call) {
      const body = await call("/user/balance");
      return requireArray(body.balance_infos, m("ui.balance")).map((info) => ({
        currency: balanceCurrency(requireObject(info, m("ui.balance")).currency),
        amount: balanceAmount(info.total_balance),
        depleted: body.is_available === false,
        parts: [["ui.balance_topped_up_value", info.topped_up_balance], ["ui.balance_granted_value", info.granted_balance]]
      }));
    }
  },
  {
    hosts: ["openrouter.ai"],
    async read(call) {
      // The key's own spending limit is optional detail; the account credits are the balance.
      const [credits, key] = await Promise.all([
        call("/api/v1/credits"),
        call("/api/v1/key").catch((error) => {
          if (error instanceof AuthError || error instanceof StaleRequestError) throw error;
          return null;
        })
      ]);
      const data = requireObject(credits.data, m("ui.balance"));
      const total = balanceAmount(data.total_credits), used = balanceAmount(data.total_usage);
      const limit = key?.data;
      const limited = Number.isFinite(limit?.limit) && Number.isFinite(limit?.limit_remaining);
      return [
        {
          currency: "USD",
          amount: total - used,
          // A spent key limit stops the key while the account still has credits.
          depleted: total - used <= 0 || (limited && limit.limit_remaining <= 0),
          parts: [
            ["ui.balance_used_value", used, total],
            ...(limited ? [["ui.key_limit_used_value", limit.limit - limit.limit_remaining, limit.limit]] : [])
          ]
        }
      ];
    }
  },
  {
    hosts: ["api.moonshot.cn", "api.moonshot.ai"],
    async read(call, host) {
      const data = requireObject((await call("/v1/users/me/balance")).data, m("ui.balance"));
      const amount = balanceAmount(data.available_balance);
      return [
        {
          currency: host.endsWith(".cn") ? "CNY" : "USD",
          amount,
          depleted: amount <= 0,
          parts: [["ui.balance_cash_value", data.cash_balance], ["ui.balance_voucher_value", data.voucher_balance]]
        }
      ];
    }
  },
  {
    hosts: ["api.siliconflow.cn", "api.siliconflow.com"],
    async read(call, host) {
      const data = requireObject((await call("/v1/user/info")).data, m("ui.balance"));
      const amount = balanceAmount(data.totalBalance);
      return [
        {
          currency: host.endsWith(".cn") ? "CNY" : "USD",
          amount,
          depleted: amount <= 0,
          parts: [["ui.balance_topped_up_value", data.chargeBalance], ["ui.balance_granted_value", data.balance]]
        }
      ];
    }
  },
  {
    hosts: ["api.stepfun.com", "api.stepfun.ai"],
    async read(call, host) {
      const body = await call("/v1/accounts");
      const amount = balanceAmount(body.balance);
      // Postpaid accounts keep calling below zero.
      return [{ currency: host.endsWith(".com") ? "CNY" : "USD", amount, depleted: body.type !== "postpaid" && amount <= 0 }];
    }
  }
];

// CPA ends a proxied call after 60 seconds.
const AI_BALANCE_TIMEOUT_MS = 65000;
const AI_BALANCE_BATCH_CONCURRENCY = 3;

function aiBalanceUpstream(provider) {
  if (!provider.auth_index || !provider.key_preview) return null;
  let host;
  try { host = new URL(provider.base_url).hostname.toLowerCase(); } catch (_) { return null; }
  const upstream = AI_BALANCE_UPSTREAMS.find((item) => item.hosts.includes(host));
  return upstream ? { read: upstream.read, host } : null;
}

function requireObject(value, label) {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new UIError(m("ui.invalid_response_for_value", { v0: label }));
  return value;
}

function requireArray(value, label) {
  if (!Array.isArray(value)) throw new UIError(m("ui.invalid_response_for_value", { v0: label }));
  return value;
}

function balanceAmount(value) {
  const amount = typeof value === "string" && value.trim() ? Number(value) : value;
  if (typeof amount !== "number" || !Number.isFinite(amount))
    throw new UIError(m("ui.invalid_response_for_value", { v0: m("ui.balance") }));
  return amount;
}

function balanceCurrency(value) {
  if (typeof value !== "string" || !/^[A-Z]{3}$/.test(value))
    throw new UIError(m("ui.invalid_response_for_value", { v0: m("ui.balance") }));
  return value;
}

function formatBalance(value, currency) {
  return Number(value).toLocaleString(locale(), {
    style: "currency",
    currency,
    currencyDisplay: "narrowSymbol",
    minimumFractionDigits: 2,
    maximumFractionDigits: 2
  });
}

// CPA substitutes the selected credential's key for $TOKEN$.
async function upstreamBalanceJSON(provider, url) {
  const request = {
    auth_index: provider.auth_index,
    method: "GET",
    url,
    header: { Authorization: "Bearer $TOKEN$", Accept: "application/json" }
  };
  const result = await api("POST", "/v0/management/api-call", request, { timeoutMS: AI_BALANCE_TIMEOUT_MS });
  if (!(result.status_code >= 200 && result.status_code < 300)) {
    throw new UIError(m("ui.upstream_returned_http_value", { v0: result.status_code }));
  }
  let body = null;
  try { body = JSON.parse(result.body); } catch (_) {}
  return requireObject(body, m("ui.balance"));
}

async function loadAIBalance(provider) {
  const owner = adminUIState;
  const index = provider.auth_index;
  const upstream = aiBalanceUpstream(provider);
  if (!upstream || owner.aiBalanceLoading.has(index)) return;
  owner.aiBalanceLoading.add(index);
  owner.aiBalanceErrors.delete(index);
  try {
    const balances = await upstream.read((path) => upstreamBalanceJSON(provider, "https://" + upstream.host + path), upstream.host);
    const result = {
      fetched_at: new Date().toISOString(),
      balances: balances.map((balance) => ({
        ...balance,
        parts: (balance.parts || []).filter(([, value]) => value != null)
          .map(([label, ...amounts]) => ({ label, amounts: amounts.map(balanceAmount) }))
      }))
    };
    const saved = await queueAdminData("configuration", () =>
      plugin("POST", "/ai-providers/balance", { auth_index: index, balance: result }, { command: true })
    );
    if (owner === adminUIState) {
      for (const item of aiProvidersQueries.admin.value || []) if (item.auth_index === index) item.balance = saved;
    }
  } catch (error) {
    if (error instanceof AuthError || error instanceof StaleRequestError) throw error;
    if (owner === adminUIState && aiProvidersQueries.admin.value?.some((item) => item.auth_index === index))
      owner.aiBalanceErrors.set(index, error.message || String(error));
  } finally { owner.aiBalanceLoading.delete(index); }
}

async function queryAIBalance(provider) {
  const owner = adminUIState;
  const task = loadAIBalance(provider);
  renderAIProviders(false);
  try { await task; } finally { if (owner === adminUIState) renderAIProviders(false); }
}

function querySelectedAIBalances(providers) {
  const owner = adminUIState;
  return aiSelection.run(
    providers,
    async (provider) => {
      await loadAIBalance(provider);
      const error = owner.aiBalanceErrors.get(provider.auth_index);
      if (error) throw new UIError(error);
    },
    { groupBy: (provider) => provider.auth_index, concurrency: AI_BALANCE_BATCH_CONCURRENCY }
  );
}

function resetAIBalanceBatch() {
  const bulk = $("ai-provider-query-balances");
  bulk.dataset.loading = "false";
  setLabeledActionIcon(bulk, "refresh", m("ui.query_balance"));
}

async function queryFilteredAIBalances() {
  const owner = adminUIState;
  const bulk = $("ai-provider-query-balances");
  // A key listed twice shares one auth index, and so one query.
  const providers = [
    ...new Map(
      filteredAIProviders(false).filter((provider) => aiBalanceUpstream(provider) && !owner.aiBalanceLoading.has(provider.auth_index))
        .map((provider) => [provider.auth_index, provider])
    ).values()
  ];
  if (!providers.length || bulk.dataset.loading === "true") return;
  bulk.dataset.loading = "true";
  const progress = el("span", { text: m("ui.querying_0_value", { v0: providers.length }) });
  bulk.removeAttribute("aria-label");
  bulk.replaceChildren(actionIcon("refresh"), progress);
  renderAIProviders(false);
  let next = 0, completed = 0;
  try {
    const worker = async () => {
      while (next < providers.length && owner === adminUIState) {
        await loadAIBalance(providers[next++]);
        setText(progress, m("ui.querying_value_value", { v0: ++completed, v1: providers.length }));
      }
    };
    await settleLoads(Array.from({ length: Math.min(AI_BALANCE_BATCH_CONCURRENCY, providers.length) }, worker));
  } finally {
    if (owner === adminUIState) {
      resetAIBalanceBatch();
      renderAIProviders(false);
    }
  }
}

function renderAIBalanceRow(balance, fetchedAt) {
  const details = [
    ...balance.parts.map(({ label, amounts }) =>
      el("span", { text: m(label, { v0: amounts.map((amount) => formatBalance(amount, balance.currency)).join(DISPLAY_SLASH) }) })
    ),
    fetchedAt && el("time", { datetime: fetchedAt, text: m("ui.balance_queried_at_value", { v0: compactResetDate(new Date(fetchedAt)) }) })
  ].filter(Boolean);
  return el(
    "div",
    { class: "ai-balance-row" + (balance.depleted ? " depleted" : "") },
    el("span", { class: "credential-metric-value", text: formatBalance(balance.amount, balance.currency) }),
    details.length > 0 && el("div", { class: "ai-balance-details" }, details)
  );
}

function renderAIBalance(provider, account) {
  const owner = adminUIState;
  const status = (text) => el("div", { class: "muted small", text });
  const result = provider.balance;
  const error = account ? "" : owner.aiBalanceErrors.get(provider.auth_index);
  if (!result) {
    if (!aiBalanceUpstream(provider)) return status(m("ui.balance_queries_are_not_supported_for_this_provider"));
    if (account) return status(m("ui.balance_not_queried"));
    if (owner.aiBalanceLoading.has(provider.auth_index)) return status(m("ui.querying_balance"));
    return error ? el("div", { class: "credential-error", text: error }) : status(m("ui.query_to_check_balance"));
  }
  return el(
    "div",
    { class: "ai-balance" },
    error && el("div", { class: "credential-error", text: error }),
    result.balances.map((balance, index) => renderAIBalanceRow(balance, index === result.balances.length - 1 ? result.fetched_at : null))
  );
}
