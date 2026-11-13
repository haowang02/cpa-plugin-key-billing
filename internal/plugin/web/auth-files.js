const AUTH_FILE_CATEGORIES = { claude: "Claude", antigravity: "Antigravity", codex: "Codex", xai: "xAI", kimi: "Kimi" };
const AUTH_FILE_FILTERS = {
  status: statusFilter("auth-status"),
  category: {
    id: "auth-category",
    label: m("ui.category"),
    icon: "shapes",
    options: () => Object.entries(AUTH_FILE_CATEGORIES).map(([value, label]) => ({ value, label }))
  },
  search: searchFilter("auth-search")
};

const authSelection = createTableSelection({
  id: (file) => file.auth_index,
  label: (file) => file.note || file.title,
  eligible: (file) => !!file.auth_index,
  context: () => filterSnapshot(false, Object.values(AUTH_FILE_FILTERS)),
  render: () => renderAuthFiles(false),
  actions: renderAuthSelectionActions
});

function renderAuthSelectionActions(files) {
  const owner = adminUIState;
  return [
    authSelection.action(
      m("ui.update_quotas"),
      files.filter((file) => canRefreshAuthQuota(file) && !owner.authQuotaLoading.has(file.auth_index) && !owner.authQuotaBatch),
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
    { concurrency: AUTH_QUOTA_BATCH_CONCURRENCY, success: m("ui.quotas_refreshed") }
  );
}

function authFileMeta(file) {
  return el(
    "div",
    { class: "credential-meta" },
    credentialRoutingTag(file),
    file.unavailable && el("span", { class: "tag bad", text: m("ui.unavailable") })
  );
}

let authPlanShowsEndTime = false;

function renderAuthFilePlan(file) {
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
  const cell = credentialMetric("plan", m("ui.plan"), file.quota?.plan?.trim() || "—");
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
  return { owner: account ? accountUIState : adminUIState, files: authFilesQueries[account ? "account" : "admin"] };
}

function resetAuthQuotaMemory(account) {
  const owner = account ? accountUIState : adminUIState;
  authQuotaGeneration[account ? "account" : "admin"]++;
  owner.authQuotaLoading.clear();
  owner.authQuotaErrors.clear();
  owner.authQuotaBatch = null;
}

// Account usage covers only the presenting key, so only administrators can project it to the whole window.
function authFileActions(account) {
  const { authQuotaBatch: batch } = authUI(account).owner;
  return [
    {
      label: batch ? m("ui.updating_value_value", { v0: batch.done, v1: batch.total }) : m("ui.update_quotas"),
      icon: "refresh",
      disabled: !!batch || (!account && authSelection.running) || !filteredAuthFiles(account).some(canRefreshAuthQuota),
      run: () => guard(() => refreshFilteredAuthQuotas(account))
    },
    ...(account
      ? []
      : [
          {
            label: m("ui.quota_estimate"),
            icon: "gauge",
            checked: displaySettings.admin.quotaEstimate,
            run: () => {
              setDisplaySetting("quotaEstimate", !displaySettings.admin.quotaEstimate);
              renderAuthFiles(false);
            }
          }
        ])
  ];
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
  const owner = account ? accountUIState : adminUIState;
  // Status changes awaiting confirmation outlast reads that started before them.
  for (const file of files)
    if (owner.authStatusSubmissions?.has(file.auth_index)) file.disabled = owner.authStatusSubmissions.get(file.auth_index);
}

function authQuotaSessionCurrent(account, owner, generation) {
  return owner === (account ? accountUIState : adminUIState) && generation === authQuotaGeneration[account ? "account" : "admin"];
}

// A list read that began before a query was answered would bring back the quota the query replaced.
function receiveAuthQuota(account, index, quota) {
  const files = authUI(account).files;
  const file = files.value?.find((item) => item.auth_index === index);
  if (file) file.quota = quota;
  if (files.task) guard(() => readAuthFiles(account, true));
}

function filteredAuthFiles(account) {
  const status = filterValue(account, AUTH_FILE_FILTERS.status);
  const category = filterValue(account, AUTH_FILE_FILTERS.category);
  const term = filterValue(account, AUTH_FILE_FILTERS.search).trim().toLocaleLowerCase();
  return (authUI(account).files.value || []).filter((file) => {
    if (!matchesStatus(status, file.disabled)) return false;
    if (category && file.category !== category) return false;
    if (!term) return true;
    return [file.note, file.name, file.title, file.quota?.plan, file.category].some((value) =>
      String(value || "").toLocaleLowerCase().includes(term)
    );
  });
}

// An elapsed window reports nothing of its next period, which, like an unused window, resets one length after its first request.
function quotaResetMeta(row) {
  const resetAt = Date.parse(row.reset_at);
  if (!Number.isFinite(resetAt)) return null;
  const left = Math.floor((resetAt - Date.now()) / 1000);
  if (left > 0) return { elapsed: false, at: compactResetDate(new Date(resetAt)), after: compactTimeUntil(left) };
  if (!row.window_seconds) return { elapsed: true, at: compactResetDate(new Date(resetAt)), after: m("ui.quota_reset_elapsed") };
  return {
    elapsed: true,
    at: compactResetDate(new Date(Date.now() + row.window_seconds * 1000)),
    after: compactTimeUntil(row.window_seconds)
  };
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

// Scaling to the whole window needs both a used share and recorded usage.
function quotaEstimateScale(usedPercent, usage) { return usedPercent > 0 && usage.tokens > 0 ? 100 / usedPercent : 0; }

function renderQuotaMetrics(usage, scale = 1, tooltip = null) {
  const known = usage && scale;
  const metric = (icon, label, value) =>
    el("span", { class: "auth-quota-metric" }, el("span", { role: "img", "aria-label": label }, actionIcon(icon)), value);
  return el(
    "span",
    {
      class: "auth-quota-metrics",
      "data-tip": known ? null : m(usage ? "ui.quota_estimate_unavailable" : "ui.quota_usage_not_collected"),
      tooltip
    },
    metric("square-text", m("ui.tokens"), known ? tokens(Math.round(usage.tokens * scale)) : "—"),
    metric("circle-dollar-sign", m("ui.amount"), known ? usageMoney(usage.cost_usd * scale, usage.charged_in, 2) : "—")
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
  const reset = quotaResetMeta(row);
  const shown = reset?.elapsed ? { ...row, remaining_percent: 100, used: row.used === undefined ? undefined : 0, usage: null } : row;
  const percent = shown.remaining_percent;
  // The row sits inside the button that opens the drawer, so its tooltips are hover-only.
  const usage =
    row.meter &&
    (!shown.usage
      ? renderQuotaMetrics(null)
      : estimate
        ? renderQuotaMetrics(shown.usage, quotaEstimateScale(100 - percent, shown.usage))
        : renderQuotaMetrics(shown.usage, 1, shown.usage.tokens > 0 ? modelUsageTooltip(account, shown.usage_query) : null));
  const amount = quotaAmount(shown);
  const observed = row.observed_at && m("ui.quota_observed_at_value", { v0: compactResetDate(new Date(row.observed_at)) });
  return el(
    "div",
    { class: "auth-quota-row" },
    el(
      "div",
      { class: "auth-quota-head" },
      el("span", { class: "auth-quota-label", "data-tip": full, "data-tip-overflow": "", text: label }),
      reset && el("span", { class: "auth-quota-reset-after", text: reset.after }),
      el("span", { class: "auth-quota-value", "data-tip": observed, text: percent != null ? percent.toFixed(0) + "%" : "—" })
    ),
    renderQuotaBar(percent, full),
    (usage || amount || reset) &&
      el(
        "div",
        { class: "auth-quota-foot" },
        usage,
        amount && el("span", { text: amount }),
        reset && el("span", { class: "auth-quota-reset-time", text: reset.at })
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

function authQuotaResets(quota) {
  const listed = quota?.rate_limit_reset_credits || [];
  const credits = listed.filter((credit) => !(Date.parse(credit.expires_at) <= Date.now()));
  const count = quota?.rate_limit_reset_credits_available_count;
  return { count: count == null ? null : Math.max(0, count - listed.length + credits.length), credits };
}

function renderAuthResetCredits(file, quota) {
  if (file.category !== "codex") return null;
  const { count, credits } = authQuotaResets(quota);
  if (count === 0) return el("div", { class: "muted small", text: m("ui.no_manual_resets_available") });
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
        ? [compactTimeUntil(left, "left"), DISPLAY_SEPARATOR, compactResetDate(expiresAt)]
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
  const quota = file.quota;
  if (owner.authQuotaLoading.has(index) && !quota) {
    return el("div", { class: "auth-file-quota" }, el("div", { class: "muted small", text: m("ui.updating_quotas") }));
  }
  const error = owner.authQuotaErrors.get(index);
  if (error && !quota) return el("div", { class: "auth-file-quota" }, el("div", { class: "credential-error", text: error }));
  if (!quota) return el("div", { class: "muted small", text: authQuotaRefreshTitle(file) || m("ui.refresh_to_query_quota") });
  const summary = [];
  const balance = Number(quota.credit_balance);
  if (quota.credits_unlimited || (Number.isFinite(balance) && balance !== 0)) {
    const credits = quota.credits_unlimited ? m("ui.unlimited") : quotaNumber(balance, quota.credit_currency);
    summary.push(el("span", { text: m("ui.credits_value", { v0: credits }) }));
  }
  const resets = authQuotaResets(quota).count;
  if (resets != null) summary.push(el("span", { text: m("ui.available_resets_value", { v0: int(resets) }) }));
  const rows = quota.quota;
  return el(
    "div",
    { class: "auth-file-quota" },
    summary.length ? el("div", { class: "auth-quota-summary" }, summary) : null,
    error && el("div", { class: "credential-error", text: error }),
    rows.length
      ? renderAuthQuotaRows(rows, account, !account && displaySettings.admin.quotaEstimate)
      : el("div", { class: "muted small", text: m("ui.the_upstream_returned_no_displayable_quota") })
  );
}

function renderAuthFiles(account) {
  if ($("auth-drawer").open && authDrawer.account === account) renderAuthDrawerFile();
  const body = $("auth-files-body");
  if (renderDataPending(body, [authUI(account).files])) return;
  renderCollection(body, filteredAuthFiles(account), {
    selection: account ? null : authSelection,
    headers: [
      m("ui.auth_files"),
      ...(!account ? [""] : []),
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
  const loading = ui.owner.authQuotaLoading.has(file.auth_index);
  const resettable = (!account || resources.account.profile.value?.can_reset_auth_quota === true) && authQuotaResets(file.quota).count > 0;
  const quotaActions = el(
    "div",
    { class: "auth-quota-actions" },
    resettable &&
      el(
        "button",
        {
          type: "button",
          class: "icon-action",
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
        class: "icon-action" + (loading ? " loading" : ""),
        disabled: !canRefreshAuthQuota(file) || loading || !!ui.owner.authQuotaBatch || (!account && authSelection.running),
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
    renderAuthFilePlan(file),
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
  return authSelection.run(files, (file) => patchAuthFileStatus(file, enabled), {
    refresh: () => refreshAuthFileStatuses(files, owner),
    success: enabled ? m("ui.auth_files_enabled") : m("ui.auth_files_disabled")
  });
}

const AUTH_QUOTA_TIMEOUT_MS = 65000;
const AUTH_QUOTA_BATCH_CONCURRENCY = 3;

function canResetAuthQuota(file, account) {
  if (currentRole !== (account ? "account" : "admin") || (account && resources.account.profile.value?.can_reset_auth_quota !== true))
    return false;
  const owner = account ? accountUIState : adminUIState;
  return file.quota_supported && !file.disabled && !owner.authQuotaLoading.has(file.auth_index) && authQuotaResets(file.quota).count > 0;
}

async function resetAuthQuota(file, account) {
  if (!canResetAuthQuota(file, account)) return;
  const owner = account ? accountUIState : adminUIState;
  const generation = authQuotaGeneration[account ? "account" : "admin"], index = file.auth_index;
  await openActionDialog(
    m("ui.reset_provider_quota", { v0: PROVIDER_LABELS[file.category] || file.category }),
    el(
      "div",
      { class: "auth-reset-confirmation" },
      el(
        "div",
        { class: "auth-reset-available" },
        el("span", { class: "auth-reset-count-badge", text: int(authQuotaResets(file.quota).count) }),
        m("ui.available_reset_count")
      ),
      el("div", { text: m("ui.consume_one_reset_now") }),
      renderAuthResetCredits(file, file.quota)
    ),
    undefined,
    async () => {
      if (!authQuotaSessionCurrent(account, owner, generation) || !canResetAuthQuota(file, account)) {
        throw new UIError(m("ui.the_auth_file_or_quota_changed_close_this_dialog_update_the_quota_and_try_again"));
      }
      owner.authQuotaLoading.add(index);
      owner.authQuotaErrors.delete(index);
      renderAuthFiles(account);
      let resetConfirmed = false, cooldownCleared = true;
      try {
        const params = new URLSearchParams({ auth_index: file.auth_index, auth_name: file.name, auth_revision: file.revision || "" });
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
        if (authQuotaSessionCurrent(account, owner, generation)) {
          owner.authQuotaLoading.delete(index);
          if (!resetConfirmed)
            owner.authQuotaErrors.set(index, m("ui.reset_was_not_confirmed_update_the_quota_to_check_its_current_status"));
          renderAuthFiles(account);
        }
      }
      if (!authQuotaSessionCurrent(account, owner, generation)) return;
      await refreshAuthQuota(file, account);
      if (authQuotaSessionCurrent(account, owner, generation)) {
        if (!cooldownCleared) notify(m("ui.quota_reset_but_cooldown_clear_failed"), "warning");
        else if (owner.authQuotaErrors.has(index))
          notify(m("ui.quota_reset_but_refresh_failed_please_update_the_quota_manually"), "warning");
        else notify(m("ui.auth_quota_reset"));
      }
    },
    m("ui.confirm_reset")
  );
}

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
    if (authQuotaSessionCurrent(account, owner, generation)) receiveAuthQuota(account, file.auth_index, result);
  } catch (error) {
    if (error instanceof AuthError || error instanceof StaleRequestError) throw error;
    if (authQuotaSessionCurrent(account, owner, generation)) owner.authQuotaErrors.set(file.auth_index, error.message || String(error));
  } finally { if (authQuotaSessionCurrent(account, owner, generation)) owner.authQuotaLoading.delete(file.auth_index); }
}

async function refreshAuthQuota(file, account) {
  if (!canRefreshAuthQuota(file)) return;
  const role = account ? "account" : "admin";
  const generation = authQuotaGeneration[role];
  const task = loadAuthQuota(file, account);
  renderAuthFiles(account);
  try { await task; } finally { if (generation === authQuotaGeneration[role]) renderAuthFiles(account); }
}

async function refreshFilteredAuthQuotas(account) {
  const owner = authUI(account).owner;
  const role = account ? "account" : "admin";
  const generation = authQuotaGeneration[role];
  const files = filteredAuthFiles(account).filter((file) => canRefreshAuthQuota(file) && !owner.authQuotaLoading.has(file.auth_index));
  if (!files.length || owner.authQuotaBatch) return;
  const batch = (owner.authQuotaBatch = { done: 0, total: files.length });
  renderAuthFiles(account);
  let next = 0;
  try {
    const worker = async () => {
      while (next < files.length && generation === authQuotaGeneration[role]) {
        await loadAuthQuota(files[next++], account);
        batch.done++;
      }
    };
    await settleLoads(Array.from({ length: Math.min(AUTH_QUOTA_BATCH_CONCURRENCY, files.length) }, worker));
  } finally {
    if (owner.authQuotaBatch === batch) owner.authQuotaBatch = null;
    if (generation === authQuotaGeneration[role]) renderAuthFiles(account);
  }
  if (generation === authQuotaGeneration[role] && !files.some((file) => owner.authQuotaErrors.has(file.auth_index)))
    notify(m("ui.quotas_refreshed"));
}
