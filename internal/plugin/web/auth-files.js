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
    file.quota?.plan && authPlanTag(file.category, file.quota.plan),
    authPlanExpiryTag(file),
    credentialRoutingTag(file),
    file.unavailable && el("span", { class: "tag bad", text: m("ui.unavailable") })
  );
}

// Plan tones are per provider: a Claude Pro is not a Codex Pro 200.
const AUTH_PLAN_TONES = {
  codex: {
    elite: ["pro500", "pro200"],
    premium: ["pro100", "businesspremium"],
    info: ["plus"],
    ok: ["go"],
    business: ["business", "enterprise", "enterpriseautomation"]
  },
  claude: { premium: ["max"], info: ["pro"], business: ["team"] },
  antigravity: { elite: ["ultra20"], premium: ["ultra5"], info: ["pro"], ok: ["plus"] },
  xai: { premium: ["paid"] }
};

function authPlanTag(category, plan) {
  const key = plan.toLowerCase().replace(/[^a-z0-9]/g, "");
  const tone = Object.entries(AUTH_PLAN_TONES[category] || {}).find(([, plans]) => plans.includes(key))?.[0] || "plain";
  return el("span", { class: "tag " + tone, text: plan });
}

let authPlanShowsEndTime = false;

function authPlanExpiryTag(file) {
  const left = (file.subscription_until - Date.now()) / 1000;
  if (!(left > 0)) return null;
  const toggle = m("ui.switch_between_time_left_and_end_time");
  return el(
    "span",
    { class: "tag plain auth-plan-expiry" + (authPlanShowsEndTime ? " end-time" : "") },
    el("span", { class: "auth-plan-time-left", text: compactTimeUntil(left, "left") }),
    el("span", { class: "auth-plan-end-time", text: compactResetDate(new Date(file.subscription_until)) }),
    el(
      "button",
      { class: "inline-icon-button link", type: "button", "data-tip": toggle, "aria-label": toggle, onclick: toggleAuthPlanTime },
      actionIcon("arrow-left-right")
    )
  );
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
      loading: !!batch,
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

function renderAuthFiles(account) {
  if ($("auth-drawer").open && authDrawer.account === account) renderAuthDrawerFile();
  const body = $("auth-files-body");
  if (renderDataPending(body, [authUI(account).files])) return;
  renderCollection(body, filteredAuthFiles(account), {
    selection: account ? null : authSelection,
    headers: [
      { label: m("ui.auth_files"), class: "credential-identity-column" },
      ...credentialMetricHeaders(),
      { label: credentialColumnHeader(m("ui.quota"), authFileActions(account)), class: "credential-quota-column" },
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
  const statusBusy = adminUIState.authStatusSubmissions.has(file.auth_index) || authSelection.running;
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
        resettable &&
          credentialActionButton("rotate-cw-clock", m("ui.reset_quota"), {
            disabled: !canResetAuthQuota(file, account),
            run: () => resetAuthQuota(file, account)
          }),
        credentialActionButton("refresh", m("ui.update_quotas"), {
          tip: authQuotaRefreshTitle(file) || m("ui.update_quotas"),
          disabled: !canRefreshAuthQuota(file) || loading || !!ui.owner.authQuotaBatch || (!account && authSelection.running),
          className: loading ? "loading" : "",
          run: () => refreshAuthQuota(file, account)
        }),
        !account && [
          credentialTestButton(file, () => renderAuthFiles(false)),
          credentialActionButton("pencil", m("ui.edit"), { disabled: statusBusy, run: () => editAuthFile(file) }),
          credentialPowerButton(file, { disabled: statusBusy, toggle: (enabled) => setAuthFileEnabled(file, enabled) })
        ]
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
        el("span", { class: "count-badge", text: int(authQuotaResets(file.quota).count) }),
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
        if (generation === authQuotaGeneration[role])
          updateCredentialBatchProgress("auth-files-body", m("ui.updating_value_value", { v0: batch.done, v1: batch.total }));
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
