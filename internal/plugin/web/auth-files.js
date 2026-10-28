const authSelection = createTableSelection({
  id: (file) => file.auth_index,
  label: (file) => file.note || file.title,
  eligible: (file) => !!file.auth_index,
  context: () => [$("auth-provider").value, $("auth-search").value, $("auth-enabled-only").checked],
  render: () => renderAuthFiles(false),
  actions: renderAuthSelectionActions
});

function renderAuthSelectionActions(files) {
  const owner = adminUIState;
  return [
    authSelection.action(
      m("ui.update_quotas"),
      files.filter(
        (file) => canRefreshAuthQuota(file) && !owner.authQuotaLoading.has(file.auth_index) && authUI(false).bulk.dataset.loading !== "true"
      ),
      refreshSelectedAuthQuotas
    ),
    authSelection.action(
      m("ui.enable"),
      files.filter((file) => file.disabled && !owner.authStatusSubmissions.has(file.auth_index)),
      (selected) => setSelectedAuthFilesEnabled(selected, true)
    ),
    authSelection.action(
      m("ui.disable"),
      files.filter((file) => !file.disabled && !owner.authStatusSubmissions.has(file.auth_index)),
      (selected) => setSelectedAuthFilesEnabled(selected, false)
    )
  ];
}

function refreshSelectedAuthQuotas(files) {
  const owner = adminUIState;
  return authSelection.run(
    files,
    async (file) => {
      await loadAuthQuota(file, false);
      const error = owner.authQuotaErrors.get(file.auth_index);
      if (error) throw new UIError(error);
    },
    { concurrency: AUTH_QUOTA_BATCH_CONCURRENCY, refresh: () => persistAuthQuotaCache(false) }
  );
}

function authFileQuota(file, owner) {
  const manual = owner.authQuotas.get(file.auth_index);
  const observed = file.quota;
  if (!manual) return observed;
  if (!observed) return manual;
  // A request reports only the windows of the limit it used, so a window observed after the manual query replaces
  // just that window, in place. A queried window drops out once its limit is observed after its reset time without it.
  const newer = observed.quota.filter((row) => Date.parse(row.observed_at) > Date.parse(manual.fetched_at));
  const reportedAt = new Map();
  for (const row of newer) reportedAt.set(row.limit_key, Math.max(reportedAt.get(row.limit_key) ?? 0, Date.parse(row.observed_at)));
  const windows = new Map(manual.quota.map((row) => [authQuotaWindowKey(row), row]));
  for (const row of newer) windows.set(authQuotaWindowKey(row), row);
  const quota = [...windows.values()].filter((row) => newer.includes(row) || !(reportedAt.get(row.limit_key) >= Date.parse(row.reset_at)));
  const result = { ...observed, ...manual, quota };
  for (const field of ["plan", "credit_balance", "credits_unlimited"])
    if (Date.parse(observed[field + "_observed_at"]) > Date.parse(manual.fetched_at)) result[field] = observed[field];
  return result;
}

// The label carries a window's length, so it tells the windows of one limit apart.
function authQuotaWindowKey(row) {
  return JSON.stringify([row.limit_key || "", row.group_label || "", row.label_prefix || "", row.label || ""]);
}

function authFileMeta(file) {
  return el(
    "div",
    { class: "credential-meta" },
    credentialRoutingTag(file),
    file.unavailable && el("span", { class: "tag bad", text: m("ui.unavailable") })
  );
}

// Wide tables show one form of a plan's expiry; switching it applies to every row.
let authPlanShowsEndTime = false;

function renderAuthFilePlan(file, quota) {
  const left = (file.subscription_until - Date.now()) / 1000;
  const toggle = m("ui.switch_between_time_left_and_end_time");
  const expiry =
    left > 0 &&
    el(
      "span",
      { class: "auth-plan-expiry" + (authPlanShowsEndTime ? " end-time" : "") },
      el("span", { class: "auth-plan-time-left", text: compactTimeUntil(left, "left") }),
      el("span", { class: "auth-plan-expiry-separator", text: DISPLAY_SEPARATOR }),
      el("span", { class: "auth-plan-end-time", text: compactResetDate(new Date(file.subscription_until)) }),
      el(
        "button",
        { class: "inline-icon-button link", type: "button", "data-tip": toggle, "aria-label": toggle, onclick: toggleAuthPlanTime },
        actionIcon("arrow-left-right")
      )
    );
  const cell = credentialMetric("plan", m("ui.plan"), quota?.plan?.trim() || "—");
  if (expiry) cell.append(expiry);
  return cell;
}

function toggleAuthPlanTime() {
  authPlanShowsEndTime = !authPlanShowsEndTime;
  for (const expiry of document.querySelectorAll(".auth-plan-expiry")) expiry.classList.toggle("end-time", authPlanShowsEndTime);
}

function authProviderIcon(category) {
  return providerIcon(category === "gemini-cli" || category === "gemini-interactions" ? "gemini" : category);
}

function authUI(account) {
  const prefix = account ? "account-auth" : "auth";
  return {
    owner: account ? accountUIState : adminUIState,
    files: authFilesQueries[account ? "account" : "admin"],
    body: $(prefix + "-files-body"),
    enabledOnly: $(prefix + "-enabled-only"),
    // Account usage covers only the presenting key, so it cannot be projected to the whole window.
    estimate: account ? null : $("auth-quota-estimate"),
    provider: $(prefix + "-provider"),
    search: $(prefix + "-search"),
    bulk: $(prefix + "-refresh-filtered")
  };
}

const AUTH_QUOTA_CACHE_PREFIX = "cpa-key-billing.auth-quotas.v7.";

function resetAuthQuotaMemory(account) {
  const role = account ? "account" : "admin";
  const owner = account ? accountUIState : adminUIState;
  authQuotaGeneration[role]++;
  owner.authQuotas.clear();
  owner.authQuotaLoading.clear();
  owner.authQuotaErrors.clear();
  owner.authFileRevisions.clear();
  resetAuthQuotaBatch(account);
}

function resetAuthQuotaBatch(account) {
  const bulk = authUI(account).bulk;
  bulk.dataset.loading = "false";
  setLabeledActionIcon(bulk, "refresh", m("ui.update_quotas"));
}

function authQuotaStorageKey(role) {
  const scope = credentialScope[role];
  return scope ? AUTH_QUOTA_CACHE_PREFIX + role + "." + scope : "";
}

function persistAuthQuotaCache(account) {
  const role = account ? "account" : "admin";
  const key = authQuotaStorageKey(role);
  if (!key) return;
  const owner = account ? accountUIState : adminUIState;
  try { sessionStorage.setItem(key, JSON.stringify({ entries: [...owner.authQuotas] })); } catch (_) {}
}

function restoreAuthQuotaCache(account) {
  const role = account ? "account" : "admin";
  let cached;
  try { cached = JSON.parse(sessionStorage.getItem(authQuotaStorageKey(role)) || "null"); } catch (_) { return; }
  if (!Array.isArray(cached?.entries)) return;
  const owner = account ? accountUIState : adminUIState;
  for (const entry of cached.entries) {
    if (Array.isArray(entry) && typeof entry[0] === "string" && Array.isArray(entry[1]?.quota)) {
      owner.authQuotas.set(entry[0], entry[1]);
    }
  }
}

async function readCPAAuthSubscriptions() {
  try {
    const result = await api("GET", "/v0/management/auth-files", null, { keepSessionOnForbidden: true });
    return new Map(result.files.map((file) => [file.auth_index, Date.parse(file.id_token?.chatgpt_subscription_active_until)]));
  } catch (err) {
    if (err instanceof AuthError || err instanceof StaleRequestError) throw err;
    return new Map();
  }
}

function readAuthFiles(account, reload = false) {
  const role = account ? "account" : "admin";
  return authFilesQueries[role].read(
    JSON.stringify(sharedTimeRanges[role]),
    async () => {
      const range = selectedRange(account);
      const path = "/auth-files?" + new URLSearchParams(range);
      const [result, subscriptions] = await Promise.all([
        account ? accountAPI(path) : plugin("GET", path),
        account ? null : readCPAAuthSubscriptions()
      ]);
      // A breakdown selects the same requests as its total.
      return result.files.map((file) => ({
        ...file,
        subscription_until: subscriptions?.get(file.auth_index),
        usage_query: { auth_index: file.auth_index, ...range }
      }));
    },
    reload
  );
}

function updateAuthFiles(account, files) {
  const role = account ? "account" : "admin";
  const owner = account ? accountUIState : adminUIState;
  // Status changes awaiting confirmation outlast reads that started before them.
  for (const file of files)
    if (owner.authStatusSubmissions?.has(file.auth_index)) file.disabled = owner.authStatusSubmissions.get(file.auth_index);
  const byIndex = new Map(files.map((file) => [file.auth_index, file]));
  const previous = owner.authFileRevisions;
  const changed = new Set(
    files.filter((file) => previous.has(file.auth_index) && previous.get(file.auth_index) !== (file.cache_revision || ""))
      .map((file) => file.auth_index)
  );
  const staleIndex = (index) => {
    const file = byIndex.get(index);
    if (!file || changed.has(index)) return true;
    const result = owner.authQuotas.get(index);
    return !!file.cache_revision && !!result && result.auth_revision !== file.cache_revision;
  };
  const stale = [...owner.authQuotas.keys(), ...owner.authQuotaErrors.keys(), ...owner.authQuotaLoading].some(staleIndex);
  owner.authFileRevisions = new Map(files.map((file) => [file.auth_index, file.cache_revision || ""]));
  if (!stale) return;
  authQuotaGeneration[role]++;
  resetAuthQuotaBatch(account);
  for (const index of owner.authQuotas.keys()) if (staleIndex(index)) owner.authQuotas.delete(index);
  for (const index of owner.authQuotaErrors.keys()) if (staleIndex(index)) owner.authQuotaErrors.delete(index);
  owner.authQuotaLoading.clear();
  persistAuthQuotaCache(account);
}

function authQuotaCacheCurrent(account, owner, generation, index) {
  const role = account ? "account" : "admin";
  const currentOwner = account ? accountUIState : adminUIState;
  return owner === currentOwner && generation === authQuotaGeneration[role] && owner.authFileRevisions.has(index);
}

function filteredAuthFiles(account) {
  const ui = authUI(account);
  const provider = ui.provider.value;
  const search = ui.search.value.trim().toLocaleLowerCase();
  return (ui.files.value || []).filter((file) => {
    if (ui.enabledOnly.checked && file.disabled) return false;
    if (provider && file.category !== provider) return false;
    if (!search) return true;
    const currentPlan = authFileQuota(file, ui.owner)?.plan;
    return [file.note, file.name, file.title, currentPlan, file.category].some((value) =>
      String(value || "").toLocaleLowerCase().includes(search)
    );
  });
}

function quotaResetMeta(row) {
  const resetAt = row.reset_at ? new Date(row.reset_at) : null;
  if (!resetAt || isNaN(resetAt.getTime())) return { at: "", after: "" };
  const after = Math.max(0, Math.floor((resetAt.getTime() - Date.now()) / 1000));
  return { at: compactResetDate(resetAt), after: compactTimeUntil(after) };
}

function quotaAmount(row) {
  const hasUsed = row.used !== undefined;
  const hasLimit = row.limit !== undefined;
  if (!hasUsed && !hasLimit) return "";
  const format = (value) => quotaNumber(value, row.currency);
  if (hasUsed && hasLimit) return format(row.used) + DISPLAY_SLASH + format(row.limit);
  return hasUsed ? m("ui.used_value", { v0: format(row.used) }) : m("ui.quota_value", { v0: format(row.limit) });
}

function quotaNumber(value, currency) {
  return CURRENCY_SYMBOLS[currency] ? money(value, currency) : Number(value).toLocaleString(locale(), { maximumFractionDigits: 2 });
}

function quotaLabels(row) {
  const label = window.billingI18n.serverMessage(row.label_message, row.label || "");
  const group = window.billingI18n.serverMessage(row.group_message, row.group_label || "");
  const windowLabel = row.label_prefix ? m("ui.join", { v0: row.label_prefix, v1: "", v2: label }) : label;
  const full =
    group && String(group).toLowerCase() !== String(windowLabel).toLowerCase()
      ? m("ui.join", { v0: group, v1: DISPLAY_SEPARATOR, v2: windowLabel })
      : group || windowLabel;
  return { group, label: windowLabel, full };
}

// Usage covers a window's period. An estimate scales it to the whole window by the share already used,
// so it needs both a used share and recorded usage.
function quotaEstimateScale(usedPercent, usage) { return usedPercent > 0 && usage.tokens > 0 ? 100 / usedPercent : 0; }

function renderQuotaMetrics(usage, scale = 1, tooltip = null) {
  const metric = (icon, label, value) =>
    el("span", { class: "auth-quota-metric" }, el("span", { role: "img", "aria-label": label }, actionIcon(icon)), value);
  return el(
    "span",
    { class: "auth-quota-metrics", "data-tip": scale ? null : m("ui.quota_estimate_unavailable"), tooltip },
    metric("square-text", m("ui.tokens"), scale ? tokens(Math.round(usage.tokens * scale)) : "—"),
    metric("circle-dollar-sign", m("ui.amount"), scale ? usageMoney(usage.cost_usd * scale, usage.charged_in, 2) : "—")
  );
}

function renderQuotaBar(percent, label) {
  const known = percent != null;
  const level = percent >= 70 ? "high" : percent >= 30 ? "medium" : "low";
  return el(
    "div",
    {
      class: "bar",
      role: "progressbar",
      "aria-label": label,
      "aria-valuemin": "0",
      "aria-valuemax": "100",
      "aria-valuenow": known ? Math.round(percent) : null
    },
    el("i", { class: "quota-fill " + (known ? level : "high"), style: "width:" + (known ? percent : 0) + "%" })
  );
}

function renderQuotaRow(row, account, estimate) {
  const { label, full } = quotaLabels(row);
  const percent = row.remaining_percent;
  const reset = quotaResetMeta(row);
  const elapsed = row.reset_at && new Date(row.reset_at).getTime() <= Date.now();
  // The row sits inside the button that opens the drawer, so its tooltip is hover-only.
  const usage =
    row.usage &&
    (estimate
      ? renderQuotaMetrics(row.usage, quotaEstimateScale(100 - percent, row.usage))
      : renderQuotaMetrics(row.usage, 1, row.usage.tokens > 0 ? modelUsageTooltip(account, row.usage_query) : null));
  const amount = quotaAmount(row);
  return el(
    "div",
    { class: "auth-quota-row" + (elapsed ? " elapsed" : "") },
    el(
      "div",
      { class: "auth-quota-head" },
      el("span", { class: "auth-quota-label", "data-tip": full, "data-tip-overflow": "", text: label }),
      reset.at && el("span", { class: "auth-quota-reset-after", text: elapsed ? m("ui.quota_reset_elapsed") : reset.after }),
      el("span", { class: "auth-quota-value", text: percent != null ? percent.toFixed(0) + "%" : "—" })
    ),
    renderQuotaBar(percent, full),
    (usage || amount || reset.at) &&
      el(
        "div",
        { class: "auth-quota-foot" },
        usage,
        amount && el("span", { text: amount }),
        reset.at && el("span", { class: "auth-quota-reset-time", text: reset.at })
      )
  );
}

function renderAuthQuotaRows(rows, account, estimate) {
  const groups = new Map();
  for (const row of rows) {
    const key = JSON.stringify([row.group_label || "", row.group_message || null]);
    if (!groups.has(key)) groups.set(key, { label: quotaLabels(row).group, rows: [] });
    groups.get(key).rows.push(row);
  }
  return el(
    "div",
    { class: "auth-quota-groups" },
    [...groups.values()].map(({ label, rows }) =>
      el(
        "div",
        { class: "auth-quota-group" },
        label && el("div", { class: "auth-quota-group-label", text: label }),
        el("div", { class: "auth-quota-list" }, rows.map((row) => renderQuotaRow(row, account, estimate)))
      )
    )
  );
}

function renderAuthResetCredits(file, result) {
  if (file.category !== "codex") return null;
  if (result.rate_limit_reset_credits_available_count === 0) {
    return el("div", { class: "muted small", text: m("ui.no_manual_resets_available") });
  }
  const credits = result.rate_limit_reset_credits || [];
  if (!credits.length) { return el("div", { class: "muted small", text: m("ui.reset_credits_expiry_unavailable") }); }
  const timezone =
    new Intl.DateTimeFormat("en", { timeZoneName: "shortOffset" }).formatToParts(new Date()).find((part) => part.type === "timeZoneName")
      ?.value || "";
  return el(
    "div",
    { class: "auth-reset-credits" },
    el("div", { class: "auth-reset-credits-title", text: m("ui.reset_credits_expiry", { v0: timezone }) }),
    credits.map((credit, index) => {
      const expiresAt = new Date(credit.expires_at);
      const left = (expiresAt.getTime() - Date.now()) / 1000;
      const time = Number.isFinite(left)
        ? [left > 0 ? compactTimeUntil(left, "left") : m("ui.expired"), DISPLAY_SEPARATOR, compactResetDate(expiresAt)]
        : [credit.expires_at || "—"];
      return el(
        "div",
        { class: "auth-reset-credit" },
        el("span", { text: m("ui.reset_credit_number", { v0: index + 1 }) }),
        el("span", { class: "auth-reset-credit-time" }, time)
      );
    })
  );
}

function canRefreshAuthQuota(file) { return file.quota_supported && !file.disabled; }

function authQuotaRefreshTitle(file) {
  if (file.quota_supported) return null;
  return (
    window.billingI18n.serverMessage(file.quota_unavailable_message, file.quota_unavailable_reason) ||
    m("ui.quota_queries_are_not_supported_for_this_auth_file_type")
  );
}

function renderAuthFileQuota(file, account) {
  const ui = authUI(account);
  const owner = ui.owner;
  const index = file.auth_index;
  const result = authFileQuota(file, owner);
  if (owner.authQuotaLoading.has(index) && !result) {
    return el("div", { class: "auth-file-quota" }, el("div", { class: "muted small", text: m("ui.updating_quotas") }));
  }
  const error = owner.authQuotaErrors.get(index);
  if (error && !result) return el("div", { class: "auth-file-quota" }, el("div", { class: "credential-error", text: error }));
  if (!result) return el("div", { class: "muted small", text: authQuotaRefreshTitle(file) || m("ui.refresh_to_query_quota") });
  const summary = [];
  const balance = Number(result.credit_balance);
  if (result.credits_unlimited || (Number.isFinite(balance) && balance !== 0)) {
    const credits = result.credits_unlimited ? m("ui.unlimited") : quotaNumber(balance, result.credit_currency);
    summary.push(el("span", { text: m("ui.credits_value", { v0: credits }) }));
  }
  if (result.rate_limit_reset_credits_available_count != null) {
    summary.push(el("span", { text: m("ui.available_resets_value", { v0: int(result.rate_limit_reset_credits_available_count) }) }));
  }
  const rows = result.quota;
  return el(
    "div",
    { class: "auth-file-quota" },
    summary.length ? el("div", { class: "auth-quota-summary" }, summary) : null,
    error && el("div", { class: "credential-error", text: error }),
    rows.length
      ? renderAuthQuotaRows(rows, account, ui.estimate?.checked === true)
      : el("div", { class: "muted small", text: m("ui.the_upstream_returned_no_displayable_quota") })
  );
}

function renderAuthFiles(account) {
  const ui = authUI(account);
  if ($("auth-drawer").open && authDrawer.account === account) renderAuthDrawerFile();
  if (renderDataPending(ui.body, [ui.files])) {
    ui.bulk.disabled = true;
    return;
  }
  const files = filteredAuthFiles(account);
  const refreshable = files.filter(canRefreshAuthQuota).length;
  ui.bulk.disabled = refreshable === 0 || ui.bulk.dataset.loading === "true" || (!account && authSelection.running);
  renderCollection(ui.body, files, {
    selection: account ? null : authSelection,
    headers: [
      m("ui.auth_files"),
      ...(!account ? [{ label: "", class: "credential-edit-column" }] : []),
      m("ui.plan"),
      ...credentialMetricHeaders(),
      { label: m("ui.quota"), class: "credential-quota-column" },
      m("ui.actions")
    ],
    attrs: { class: "credential-table" },
    empty: m("ui.no_auth_files_match_the_filters"),
    render: (file) => renderAuthFileRow(file, account)
  });
}

function renderAuthFileRow(file, account) {
  const ui = authUI(account);
  const quota = authFileQuota(file, ui.owner);
  const loading = ui.owner.authQuotaLoading.has(file.auth_index);
  const resettable =
    (!account || resources.account.profile.value?.can_reset_auth_quota === true) &&
    Number(quota?.rate_limit_reset_credits_available_count) > 0;
  const quotaActions = el(
    "div",
    { class: "auth-quota-actions" },
    resettable &&
      el(
        "button",
        {
          type: "button",
          class: "labeled-icon-button round-icon-button",
          "aria-label": m("ui.reset_quota"),
          "data-tip": m("ui.reset_quota"),
          disabled: !canResetAuthQuota(file, account),
          onclick: () => guard(() => resetAuthQuota(file, account))
        },
        actionIcon("rotate-cw-clock")
      ),
    el(
      "button",
      {
        type: "button",
        class: "labeled-icon-button round-icon-button" + (loading ? " loading" : ""),
        disabled: !canRefreshAuthQuota(file) || loading || ui.bulk.dataset.loading === "true" || (!account && authSelection.running),
        "aria-label": m("ui.update_quotas"),
        "data-tip": authQuotaRefreshTitle(file) || m("ui.update_quotas"),
        onclick: () => guard(() => refreshAuthQuota(file, account))
      },
      actionIcon("refresh")
    )
  );
  return el(
    "tr",
    { class: "credential-row" + (file.disabled ? " disabled" : ""), "data-auth-index": file.auth_index },

    el(
      "td",
      { class: "credential-identity-cell" },
      el(
        "div",
        { class: "credential-head" },
        authProviderIcon(file.category),
        el(
          "div",
          { class: "credential-identity" },
          el("div", {
            class: "credential-title mask-blur",
            "data-tip": file.note || file.title,
            "data-tip-overflow": "",
            text: file.note || file.title
          }),
          authFileMeta(file)
        )
      )
    ),
    !account &&
      credentialEditCell(() => editAuthFile(file), authSelection.running || adminUIState.authStatusSubmissions.has(file.auth_index)),
    renderAuthFilePlan(file, quota),
    renderCredentialMetrics(file, modelUsageTooltip(account, file.usage_query)),
    el(
      "td",
      { class: "credential-quota-cell" },
      el(
        "div",
        {
          class: "auth-quota-open",
          role: "button",
          tabindex: "0",
          "aria-haspopup": "dialog",
          "aria-label": m("ui.view_quota_history"),
          onclick: () => openAuthDrawer(file, account),
          onkeydown: (event) => {
            if (event.key !== "Enter" && event.key !== " ") return;
            event.preventDefault();
            openAuthDrawer(file, account);
          }
        },
        renderAuthFileQuota(file, account)
      )
    ),
    el(
      "td",
      { class: "credential-actions-cell" },
      el(
        "div",
        { class: "credential-actions" },
        quotaActions,
        !account &&
          switchControl({
            input: el("input", {
              type: "checkbox",
              role: "switch",
              checked: !file.disabled,
              "aria-label": m("ui.enable"),
              disabled: adminUIState.authStatusSubmissions.has(file.auth_index) || authSelection.running,
              onchange: (event) => guard(() => setAuthFileEnabled(file, event.target.checked))
            })
          })
      )
    )
  );
}

function editAuthFile(file) {
  openCredentialEditor(
    m("ui.edit_auth_file"),
    file,
    async (changes) => {
      try { await api("PATCH", "/v0/management/auth-files/fields", { name: file.name, ...changes }); } finally {
        await readAuthFiles(false, true);
      }
    },
    { weightEditable: file.weight != null }
  );
}

async function patchAuthFileStatus(file, enabled) {
  const owner = adminUIState;
  if (owner.authStatusSubmissions.has(file.auth_index)) return;
  owner.authStatusSubmissions.set(file.auth_index, !enabled);
  file.disabled = !enabled;
  renderAuthFiles(false);
  try {
    const body = { name: file.name, auth_index: file.auth_index, disabled: !enabled };
    await api("PATCH", "/v0/management/auth-files/status", body);
  } catch (error) {
    owner.authStatusSubmissions.delete(file.auth_index);
    file.disabled = enabled;
    if (owner === adminUIState) renderAuthFiles(false);
    if (error instanceof AuthError || error instanceof StaleRequestError) throw error;
    throw new UIError(m(enabled ? "ui.enable_auth_file_failed_value" : "ui.disable_auth_file_failed_value", { v0: error.message }));
  }
}

async function refreshAuthFileStatuses(files, owner) {
  try { await readAuthFiles(false, true); } finally {
    for (const file of files) owner.authStatusSubmissions.delete(file.auth_index);
    if (owner === adminUIState) renderAuthFiles(false);
  }
}

async function setAuthFileEnabled(file, enabled) {
  const owner = adminUIState;
  await patchAuthFileStatus(file, enabled);
  await refreshAuthFileStatuses([file], owner);
}

function setSelectedAuthFilesEnabled(files, enabled) {
  const owner = adminUIState;
  return authSelection.run(files, (file) => patchAuthFileStatus(file, enabled), { refresh: () => refreshAuthFileStatuses(files, owner) });
}

const AUTH_QUOTA_TIMEOUT_MS = 65000;
const AUTH_QUOTA_BATCH_CONCURRENCY = 3;

function canResetAuthQuota(file, account) {
  if (currentRole !== (account ? "account" : "admin") || (account && resources.account.profile.value?.can_reset_auth_quota !== true))
    return false;
  const owner = account ? accountUIState : adminUIState;
  return (
    file.quota_supported && !file.disabled && !owner.authQuotaLoading.has(file.auth_index) &&
    Number(owner.authQuotas.get(file.auth_index)?.rate_limit_reset_credits_available_count) > 0
  );
}

async function resetAuthQuota(file, account) {
  if (!canResetAuthQuota(file, account)) return;
  const owner = account ? accountUIState : adminUIState;
  const generation = authQuotaGeneration[account ? "account" : "admin"], index = file.auth_index;
  const quota = owner.authQuotas.get(index);
  await openActionDialog(
    m("ui.reset_provider_quota", { v0: PROVIDER_LABELS[file.category] || file.category }),
    el(
      "div",
      { class: "auth-reset-confirmation" },
      el(
        "div",
        { class: "auth-reset-available" },
        el("span", { class: "auth-reset-count-badge", text: int(quota.rate_limit_reset_credits_available_count) }),
        m("ui.available_reset_count")
      ),
      el("div", { text: m("ui.consume_one_reset_now") }),
      renderAuthResetCredits(file, quota)
    ),
    undefined,
    async () => {
      if (!authQuotaCacheCurrent(account, owner, generation, index) || !canResetAuthQuota(file, account)) {
        throw new UIError(m("ui.the_auth_file_or_quota_changed_close_this_dialog_update_the_quota_and_try_again"));
      }
      owner.authQuotaLoading.add(index);
      owner.authQuotas.delete(index);
      owner.authQuotaErrors.delete(index);
      persistAuthQuotaCache(account);
      renderAuthFiles(account);
      let resetConfirmed = false, cooldownCleared = true;
      try {
        const params = new URLSearchParams({ auth_index: file.auth_index, auth_name: file.name, auth_revision: file.cache_revision || "" });
        const path = "/auth-files/quota/reset?" + params;
        // getRandomValues also works on HTTP installations where randomUUID is unavailable.
        const bytes = crypto.getRandomValues(new Uint8Array(16));
        bytes[6] = (bytes[6] & 0x0f) | 0x40;
        bytes[8] = (bytes[8] & 0x3f) | 0x80;
        const resetID = Array.from(
          bytes,
          (byte, index) => ([4, 6, 8, 10].includes(index) ? "-" : "") + byte.toString(16).padStart(2, "0")
        ).join("");
        const opts = { timeoutMS: AUTH_QUOTA_TIMEOUT_MS, headers: { "X-Quota-Reset-ID": resetID }, keepSessionOnForbidden: account };
        const result = account ? await accountAPI(path, opts) : await plugin("POST", path, null, opts);
        if (result?.reset !== true) throw new UIError(m("ui.reset_was_not_confirmed_update_the_quota_to_check_its_current_status"));
        resetConfirmed = true;
        cooldownCleared = result.cooldown_cleared === true;
      } finally {
        if (authQuotaCacheCurrent(account, owner, generation, index)) {
          owner.authQuotaLoading.delete(index);
          if (!resetConfirmed)
            owner.authQuotaErrors.set(index, m("ui.reset_was_not_confirmed_update_the_quota_to_check_its_current_status"));
          renderAuthFiles(account);
        }
      }
      if (!authQuotaCacheCurrent(account, owner, generation, index)) return;
      await refreshAuthQuota(file, account);
      if (authQuotaCacheCurrent(account, owner, generation, index)) {
        const failed = owner.authQuotaErrors.has(index);
        if (!cooldownCleared) {
          notify(m("ui.quota_reset_but_cooldown_clear_failed"), "err");
          return;
        }
        notify(
          failed
            ? m("ui.quota_reset_but_refresh_failed_please_update_the_quota_manually")
            : m("ui.reset_quota_for_value", { v0: file.name }),
          failed ? "err" : "ok"
        );
      }
    },
    m("ui.confirm_reset")
  );
}

// Callers own rendering and persistence of queried quotas.
async function loadAuthQuota(file, account) {
  const owner = account ? accountUIState : adminUIState;
  const role = account ? "account" : "admin";
  const generation = authQuotaGeneration[role];
  if (owner.authQuotaLoading.has(file.auth_index)) return;
  owner.authQuotaLoading.add(file.auth_index);
  owner.authQuotaErrors.delete(file.auth_index);
  try {
    const path = "/auth-files/quota?auth_index=" + encodeURIComponent(file.auth_index);
    const opts = { timeoutMS: AUTH_QUOTA_TIMEOUT_MS };
    const result = account ? await accountAPI(path, opts) : await plugin("GET", path, null, opts);
    if (authQuotaCacheCurrent(account, owner, generation, file.auth_index)) owner.authQuotas.set(file.auth_index, result);
  } catch (error) {
    if (error instanceof AuthError || error instanceof StaleRequestError) throw error;
    if (authQuotaCacheCurrent(account, owner, generation, file.auth_index))
      owner.authQuotaErrors.set(file.auth_index, error.message || String(error));
  } finally { if (authQuotaCacheCurrent(account, owner, generation, file.auth_index)) owner.authQuotaLoading.delete(file.auth_index); }
}

async function refreshAuthQuota(file, account) {
  if (!canRefreshAuthQuota(file)) return;
  const role = account ? "account" : "admin";
  const generation = authQuotaGeneration[role];
  const task = loadAuthQuota(file, account);
  renderAuthFiles(account);
  try { await task; } finally {
    if (generation === authQuotaGeneration[role]) {
      persistAuthQuotaCache(account);
      renderAuthFiles(account);
    }
  }
}

async function refreshFilteredAuthQuotas(account) {
  const ui = authUI(account);
  const role = account ? "account" : "admin";
  const generation = authQuotaGeneration[role];
  const files = filteredAuthFiles(account).filter((file) => canRefreshAuthQuota(file) && !ui.owner.authQuotaLoading.has(file.auth_index));
  if (!files.length || ui.bulk.dataset.loading === "true") return;
  ui.bulk.dataset.loading = "true";
  const progress = el("span", { text: m("ui.updating_0_value", { v0: files.length }) });
  ui.bulk.removeAttribute("aria-label");
  ui.bulk.replaceChildren(actionIcon("refresh"), progress);
  renderAuthFiles(account);
  let next = 0, completed = 0;
  try {
    const worker = async () => {
      while (next < files.length && generation === authQuotaGeneration[role]) {
        await loadAuthQuota(files[next++], account);
        setText(progress, m("ui.updating_value_value", { v0: ++completed, v1: files.length }));
      }
    };
    await settleLoads(Array.from({ length: Math.min(AUTH_QUOTA_BATCH_CONCURRENCY, files.length) }, worker));
  } finally {
    if (generation === authQuotaGeneration[role]) {
      persistAuthQuotaCache(account);
      resetAuthQuotaBatch(account);
      renderAuthFiles(account);
    }
  }
}

for (const account of [false, true]) {
  const ui = authUI(account);
  const role = account ? "account" : "admin";
  setLabeledActionIcon(ui.bulk, "refresh", m("ui.update_quotas"));
  for (const filter of [ui.enabledOnly, ui.estimate, ui.provider].filter(Boolean)) {
    filter.addEventListener("change", () => {
      saveChoice(role, filter.id);
      renderAuthFiles(account);
    });
  }
  ui.search.addEventListener("input", () => renderAuthFiles(account));
  ui.bulk.onclick = () => guard(() => refreshFilteredAuthQuotas(account));
}
