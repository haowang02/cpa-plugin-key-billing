const AI_PROVIDER_TYPES = [
  { type: "claude", provider: "claude", list: "claude-api-key", brand: "claude" },
  { type: "codex", provider: "codex", list: "codex-api-key", brand: "codex" },
  { type: "gemini", provider: "gemini", list: "gemini-api-key", brand: "gemini" },
  { type: "interactions", provider: "gemini-interactions", list: "interactions-api-key", brand: "gemini" },
  { type: "vertex", provider: "vertex", list: "vertex-api-key", brand: "vertex" },
  { type: "xai", provider: "xai", list: "xai-api-key", brand: "xai" },
  { type: "meta", provider: "meta", list: "meta-api-key", brand: "meta" },
  { type: "openai-compatibility", list: "openai-compatibility", brand: "openai" }
];

const aiSelection = createTableSelection({
  id: (provider) => provider.selection_id,
  label: (provider) => [provider.name || provider.type, provider.key_preview || provider.base_url].join(" "),
  context: () => [$("ai-provider-type").value, $("ai-provider-search").value, $("ai-provider-enabled-only").checked],
  render: () => renderAIProviders(false),
  actions: renderAISelectionActions,
  busy: () => aiProviderStatusSaving
});

function renderAISelectionActions(providers) {
  return [
    aiSelection.action(
      m("ui.query_balance"),
      providers.filter(
        (provider) =>
          aiBalanceUpstream(provider) && !adminUIState.aiBalanceLoading.has(provider.auth_index) &&
          $("ai-provider-query-balances").dataset.loading !== "true"
      ),
      querySelectedAIBalances
    ),
    aiSelection.action(
      m("ui.enable"),
      providers.filter((provider) => provider.disabled),
      (selected) => setAIProvidersEnabled(selected, true, aiSelection)
    ),
    aiSelection.action(
      m("ui.disable"),
      providers.filter((provider) => !provider.disabled),
      (selected) => setAIProvidersEnabled(selected, false, aiSelection)
    )
  ];
}

function sortedConfiguredHeaders(headers) {
  if (!headers) return "";
  return Object.keys(headers).sort().map((key) => key + "\0" + headers[key] + "\0").join("");
}

async function configuredCredentialRef(kind, parts, counters) {
  // These hashes must stay byte-identical to CPA's StableIDGenerator and
  // the plugin's CredentialFingerprint so exact bindings match candidates.
  const digest = await sha256Hex(kind + parts.map((part) => "\0" + String(part || "").trim()).join(""));
  const base = kind + ":" + digest.slice(0, 12);
  const collision = counters.get(base) || 0;
  counters.set(base, collision + 1);
  const id = collision ? base + "-" + collision : base;
  return "sha256:" + (await sha256Hex("cpa-key-billing:credential:v1\0" + id));
}

function configuredProviderDetails(entry, type) {
  return {
    type,
    name: (entry.name || "").trim(),
    auth_index: entry["auth-index"] || "",
    base_url: (entry["base-url"] || "").trim(),
    priority: entry.priority ?? 0
  };
}

async function readConfiguredProviderConfig() {
  return Object.assign({}, ...(await Promise.all(AI_PROVIDER_TYPES.map((kind) => api("GET", "/v0/management/" + kind.list, null)))));
}

async function syncConfiguredProviders() {
  const { credentials, targets } = await parseConfiguredProviders(await readConfiguredProviderConfig());
  await plugin("POST", "/credentials/sync", { credentials }, { command: true });
  return targets;
}

async function parseConfiguredProviders(config) {
  const generation = sessionGeneration;
  const counters = new Map();
  const disabledCounters = new Map();
  // Selection identities must survive provider enable/disable changes.
  const selectionCounters = new Map();
  const credentials = [];
  const targets = new Map();
  const add = (credential, list, index, selectionID = credential.ref) => {
    credentials.push(credential);
    targets.set(credential.ref, { list, index, selectionID, excludedModels: config[list][index]["excluded-models"] || [] });
  };

  for (const { type, list, provider } of AI_PROVIDER_TYPES) {
    if (type === "openai-compatibility") continue;
    for (const [index, entry] of (config[list] || []).entries()) {
      const apiKey = (entry["api-key"] || "").trim();
      const baseURL = (entry["base-url"] || "").trim();
      if (!apiKey && !baseURL) continue;
      const parts = [apiKey, baseURL, entry["proxy-url"]];
      if (type !== "vertex") parts.push(entry.prefix, sortedConfiguredHeaders(entry.headers));
      add(
        {
          ref: await configuredCredentialRef(provider + ":apikey", parts, counters),
          ...configuredProviderDetails(entry, type),
          provider,
          key_preview: previewCredential(apiKey),
          weight: Math.max(0, entry.weight ?? 1),
          disabled: (entry["excluded-models"] || []).some((model) => model.trim() === "*")
        },
        list,
        index
      );
    }
  }

  for (const [index, entry] of (config["openai-compatibility"] || []).entries()) {
    // Disabled providers have no runtime identity and must not consume the live ID counters.
    const disabled = entry.disabled === true;
    const kindPrefix = disabled ? "disabled:" : "";
    const providerCounters = disabled ? disabledCounters : counters;
    const providerName = (entry.name || "").trim().toLowerCase() || "openai-compatibility";
    const provider =
      providerName === "openai-compatibility" || providerName.startsWith("openai-compatible-")
        ? providerName
        : "openai-compatible-" + providerName;
    const apiKeys = entry["api-key-entries"] || [];
    if (!apiKeys.length) {
      add(
        {
          ref: await configuredCredentialRef(kindPrefix + "openai-compatibility:" + providerName, [entry["base-url"]], providerCounters),
          ...configuredProviderDetails(entry, "openai-compatibility"),
          provider,
          key_preview: "",
          weight: 1,
          disabled
        },
        "openai-compatibility",
        index,
        await configuredCredentialRef("selection:openai-compatibility:" + providerName, [entry["base-url"]], selectionCounters)
      );
      continue;
    }
    for (const apiKeyEntry of apiKeys) {
      const apiKey = (apiKeyEntry["api-key"] || "").trim();
      add(
        {
          ref: await configuredCredentialRef(
            kindPrefix + "openai-compatibility:" + providerName,
            [apiKey, entry["base-url"], apiKeyEntry["proxy-url"]],
            providerCounters
          ),
          ...configuredProviderDetails(entry, "openai-compatibility"),
          auth_index: apiKeyEntry["auth-index"] || "",
          provider,
          key_preview: previewCredential(apiKey),
          weight: Math.max(0, apiKeyEntry.weight ?? 1),
          disabled
        },
        "openai-compatibility",
        index,
        await configuredCredentialRef(
          "selection:openai-compatibility:" + providerName,
          [apiKey, entry["base-url"], apiKeyEntry["proxy-url"]],
          selectionCounters
        )
      );
    }
  }

  if (generation !== sessionGeneration) throw new StaleRequestError();
  return { credentials, targets };
}

async function fetchAIProviders(account) {
  const targets = account ? null : await queueAdminData("configuration", syncConfiguredProviders);
  const range = selectedRange(account);
  const path = "/ai-providers?" + new URLSearchParams(range);
  const result = account ? await accountAPI(path) : await plugin("GET", path);
  return result.providers.map((provider) => ({
    ...provider,
    selection_id: targets?.get(provider.ref)?.selectionID || provider.ref,
    usage_query: provider.auth_index ? { credential_ref: provider.ref, ...range } : null
  }));
}

function readAIProviders(account, reload = false) {
  const role = account ? "account" : "admin";
  return aiProvidersQueries[role].read(JSON.stringify(sharedTimeRanges[role]), () => fetchAIProviders(account), reload);
}

function filteredAIProviders(account) {
  const prefix = account ? "account-" : "";
  const type = $(prefix + "ai-provider-type").value;
  const search = $(prefix + "ai-provider-search").value.trim().toLocaleLowerCase();
  return (aiProvidersQueries[account ? "account" : "admin"].value || []).filter((provider) => {
    if ($(prefix + "ai-provider-enabled-only").checked && provider.disabled) return false;
    if (type && provider.type !== type) return false;
    if (!search) return true;
    return [provider.base_url, provider.type, provider.name, provider.key_preview].some((value) =>
      value.toLocaleLowerCase().includes(search)
    );
  });
}

function renderAIProviders(account) {
  const body = $((account ? "account-" : "") + "ai-providers-body");
  const bulk = account ? null : $("ai-provider-query-balances");
  if (renderDataPending(body, [aiProvidersQueries[account ? "account" : "admin"]])) {
    if (bulk) bulk.disabled = true;
    return;
  }
  const providers = filteredAIProviders(account);
  if (bulk)
    bulk.disabled = bulk.dataset.loading === "true" || aiSelection.running || aiProviderStatusSaving || !providers.some(aiBalanceUpstream);
  renderCollection(body, providers, {
    selection: account ? null : aiSelection,
    headers: [
      m("ui.ai_providers"),
      ...credentialMetricHeaders(),
      { label: m("ui.balance"), class: "credential-quota-column" },
      ...(!account ? [m("ui.actions")] : [])
    ],
    attrs: { class: "credential-table" },
    empty: m("ui.no_ai_providers_match_the_filters"),
    render: (provider) => renderAIProviderRow(provider, account)
  });
}

function renderAIProviderRow(provider, account) {
  const upstream = aiBalanceUpstream(provider);
  const loading = adminUIState.aiBalanceLoading.has(provider.auth_index);
  const baseURL = provider.base_url.replace(/^https:\/\//i, "");
  return el(
    "tr",
    { class: "credential-row" + (provider.disabled ? " disabled" : "") },
    el(
      "td",
      { class: "credential-identity-cell" },
      el(
        "div",
        { class: "credential-head" },
        providerIcon(AI_PROVIDER_TYPES.find((kind) => kind.type === provider.type)?.brand),
        el(
          "div",
          { class: "credential-identity" },
          el("div", {
            class: "credential-title" + (baseURL ? "" : " muted"),
            "data-tip": baseURL || null,
            "data-tip-overflow": "",
            text: baseURL || m("ui.default_base_url")
          }),
          el(
            "div",
            { class: "credential-meta ai-provider-meta" },
            el(
              "span",
              { class: "tag plain ai-provider-key" },
              el("span", {
                class: provider.key_preview ? "mono mask-blur" : "",
                text: provider.key_preview || m("ui.no_api_key_configured")
              })
            ),
            credentialRoutingTag(provider)
          )
        )
      )
    ),
    renderCredentialMetrics(provider, modelUsageTooltip(account, provider.usage_query)),
    el("td", { class: "credential-quota-cell" }, renderAIBalance(provider, account)),
    !account &&
      el(
        "td",
        { class: "credential-actions-cell" },
        el(
          "div",
          { class: "credential-actions" },
          el(
            "button",
            {
              type: "button",
              class: "labeled-icon-button round-icon-button" + (loading ? " loading" : ""),
              disabled:
                !upstream || loading || aiSelection.running || aiProviderStatusSaving ||
                $("ai-provider-query-balances").dataset.loading === "true",
              "aria-label": m("ui.query_balance"),
              "data-tip": upstream ? m("ui.query_balance") : m("ui.balance_queries_are_not_supported_for_this_provider"),
              onclick: () => guard(() => queryAIBalance(provider))
            },
            actionIcon("refresh")
          ),
          switchControl({
            input: el("input", {
              type: "checkbox",
              role: "switch",
              checked: !provider.disabled,
              "aria-label": m(provider.type === "openai-compatibility" ? "ui.provider_group_switch" : "ui.enable"),
              "data-tip": m(provider.type === "openai-compatibility" ? "ui.provider_group_switch" : "ui.enable"),
              disabled: aiProviderStatusSaving || aiSelection.running,
              onchange: (event) => {
                const enabled = event.target.checked;
                event.target.checked = !provider.disabled;
                guard(() => setAIProvidersEnabled([provider], enabled));
              }
            })
          })
        )
      )
  );
}

let aiProviderStatusSaving = false;

async function setAIProvidersEnabled(providers, enabled, selection = null) {
  if (aiProviderStatusSaving) return;
  const generation = sessionGeneration;
  const context = selection?.context;
  aiProviderStatusSaving = true;
  renderAIProviders(false);
  try {
    const { targets } = await parseConfiguredProviders(await readConfiguredProviderConfig());
    if (generation !== sessionGeneration || (selection && (activeTableSelection !== selection || selection.context !== context)))
      throw new StaleRequestError();
    if (providers.some((provider) => !targets.has(provider.ref))) throw new UIError(m("ui.provider_config_changed"));
    const group = (provider) => {
      const target = targets.get(provider.ref);
      return target.list + ":" + target.index;
    };
    const save = (provider) => {
      const target = targets.get(provider.ref);
      const value =
        target.list === "openai-compatibility"
          ? { disabled: !enabled }
          : {
              "excluded-models": enabled
                ? target.excludedModels.filter((model) => model.trim() !== "*")
                : [...new Set([...target.excludedModels, "*"])]
            };
      return queueAdminData("configuration", () => api("PATCH", "/v0/management/" + target.list, { index: target.index, value }));
    };
    if (selection) await selection.run(providers, save, { groupBy: group, refresh: () => readAIProviders(false, true) });
    else {
      try { for (const provider of new Map(providers.map((item) => [group(item), item])).values()) await save(provider); } finally {
        if (generation === sessionGeneration) await readAIProviders(false, true);
      }
    }
  } finally {
    aiProviderStatusSaving = false;
    if (generation === sessionGeneration) renderAIProviders(false);
  }
}

for (const account of [false, true]) {
  const prefix = account ? "account-" : "";
  $(prefix + "ai-provider-type").append(...AI_PROVIDER_TYPES.map((kind) => el("option", { value: kind.type, text: kind.type })));
  for (const filter of [$(prefix + "ai-provider-enabled-only"), $(prefix + "ai-provider-type")]) {
    filter.addEventListener("change", () => {
      saveChoice(account ? "account" : "admin", filter.id);
      renderAIProviders(account);
    });
  }
  $(prefix + "ai-provider-search").addEventListener("input", () => renderAIProviders(account));
}
resetAIBalanceBatch();
$("ai-provider-query-balances").onclick = () => guard(queryFilteredAIBalances);
