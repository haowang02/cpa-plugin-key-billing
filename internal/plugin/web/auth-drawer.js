const authDrawer = { account: false, file: null, history: null, error: "", window: 0, request: 0 };
const AUTH_RESET_PROVIDERS = ["codex", "claude"];
const QUOTA_PERIOD_ENDS = { scheduled: ["plain", "ui.quota_period_scheduled"], early_reset: ["warn", "ui.quota_period_early_reset"] };

function openAuthDrawer(file, account) {
  closeHoverTooltip();
  Object.assign(authDrawer, { account, file, history: null, error: "", window: 0 });
  renderAuthDrawerFile();
  renderAuthDrawerHistory();
  $("auth-drawer").showModal();
  guard(loadAuthDrawerHistory);
  // Manual resets come only from a quota query.
  const ui = authUI(account);
  if (
    AUTH_RESET_PROVIDERS.includes(file.category) && canRefreshAuthQuota(file) && !ui.owner.authQuotas.has(file.auth_index) &&
    ui.bulk.dataset.loading !== "true"
  )
    guard(() => refreshAuthQuota(file, account));
}

async function loadAuthDrawerHistory() {
  const { account, file } = authDrawer;
  const request = ++authDrawer.request;
  try {
    const path = "/auth-files/history?auth_index=" + encodeURIComponent(file.auth_index);
    const history = account ? await accountAPI(path) : await plugin("GET", path);
    if (request === authDrawer.request) Object.assign(authDrawer, { history, error: "" });
  } catch (error) {
    if (error instanceof AuthError || error instanceof StaleRequestError) throw error;
    if (request === authDrawer.request) authDrawer.error = error.message || String(error);
  } finally { if (request === authDrawer.request) renderAuthDrawerHistory(); }
}

function renderAuthDrawerFile() {
  const ui = authUI(authDrawer.account);
  const file = (ui.files.value || []).find((item) => item.auth_index === authDrawer.file.auth_index) || authDrawer.file;
  authDrawer.file = file;
  $("auth-drawer-identity").replaceChildren(
    authProviderIcon(file.category),
    el(
      "div",
      { class: "drawer-title" },
      el("h2", {
        id: "auth-drawer-title",
        class: "mask-blur",
        "data-tip": file.note || file.title,
        "data-tip-overflow": "",
        text: file.note || file.title
      }),
      authFileMeta(file)
    )
  );
  renderAuthDrawerResets(file, authDrawer.account);
}

function renderAuthDrawerResets(file, account) {
  $("auth-drawer-reset-section").classList.toggle("hidden", !AUTH_RESET_PROVIDERS.includes(file.category));
  const owner = authUI(account).owner;
  const result = owner.authQuotas.get(file.auth_index);
  const error = owner.authQuotaErrors.get(file.auth_index);
  const count = result?.rate_limit_reset_credits_available_count;
  setText($("auth-drawer-reset-count"), count != null ? int(count) : "");
  const reset = $("auth-drawer-reset");
  reset.classList.toggle("hidden", account && resources.account.profile.value?.can_reset_auth_quota !== true);
  reset.disabled = !canResetAuthQuota(file, account);
  reset.onclick = () => guard(() => resetAuthQuota(file, account));
  const status = (text) => el("div", { class: "muted small", text });
  const content = owner.authQuotaLoading.has(file.auth_index)
    ? status(m("ui.updating_quotas"))
    : error && !result
      ? el("div", { class: "credential-error", text: error })
      : !result
        ? status(authQuotaRefreshTitle(file) || m("ui.refresh_to_query_quota"))
        : (renderAuthResetCredits(file, result) ?? (count === 0 ? status(m("ui.no_manual_resets_available")) : null));
  $("auth-drawer-resets").replaceChildren(...(content ? [content] : []));
}

function renderAuthDrawerHistory() {
  const { history, error } = authDrawer;
  const switcher = $("auth-drawer-window-switch");
  switcher.classList.toggle("hidden", !history?.windows.length);
  if (!history) {
    $("auth-drawer-usage").replaceChildren(
      ...(error
        ? [
            el("div", { class: "credential-error", text: error }),
            el("button", { class: "retry-button", onclick: () => retryAuthDrawerHistory() }, m("ui.retry"))
          ]
        : [el("div", { class: "muted small", text: m("ui.loading") })])
    );
    $("auth-drawer-models").replaceChildren();
    switcher.replaceChildren();
    $("auth-drawer-periods").replaceChildren();
    return;
  }
  $("auth-drawer-usage").replaceChildren(renderAuthDrawerUsage(history.usage));
  renderAuthDrawerModels();
  if (authDrawer.window >= history.windows.length) authDrawer.window = 0;
  switcher.replaceChildren(
    ...history.windows.map((window, index) =>
      el(
        "button",
        {
          type: "button",
          role: "tab",
          onclick: () => {
            authDrawer.window = index;
            renderAuthDrawerPeriods();
          }
        },
        quotaLabels(window).full
      )
    )
  );
  renderAuthDrawerPeriods();
}

function retryAuthDrawerHistory() {
  authDrawer.error = "";
  renderAuthDrawerHistory();
  guard(loadAuthDrawerHistory);
}

function renderAuthDrawerUsage(usage) {
  const { requests, successRate, totalTokens } = credentialUsageSummary(usage);
  const stat = (label, value, tooltip) =>
    el(
      "div",
      { class: "inset-card drawer-stat" },
      el("span", { class: "drawer-stat-label", text: label }),
      el("span", { class: "drawer-stat-value", "data-tip": tooltip }, value)
    );
  return el(
    "div",
    { class: "drawer-stats" },
    stat(m("ui.requests"), tokens(requests, 2), requests >= 1000 ? int(requests) : null),
    stat(m("ui.success_rate"), successRate),
    stat(m("ui.auth_total_tokens"), tokens(totalTokens), totalTokens >= 1000 ? exactTokens(totalTokens) : null),
    stat(m("ui.cost"), usageMoney(usage.cost_usd, usage.charged_in, 2))
  );
}

function renderAuthDrawerModels() {
  const { history, account, file } = authDrawer;
  if (!(history.usage.requests > 0)) {
    $("auth-drawer-models").replaceChildren();
    return;
  }
  const state = modelUsage(account, { auth_index: file.auth_index, from: history.from, to: history.to });
  if (!state.models && !state.error) state.task.then(() => { if (authDrawer.history === history) renderAuthDrawerModels(); });
  $("auth-drawer-models").replaceChildren(
    state.models
      ? renderModelUsageTable(state.models)
      : el("div", { class: state.error ? "credential-error" : "muted small", text: state.error || m("ui.loading") })
  );
}

function renderAuthDrawerPeriods() {
  const { history, window: selected, account, file } = authDrawer;
  for (const [index, button] of Array.from($("auth-drawer-window-switch").children).entries()) {
    button.classList.toggle("active", index === selected);
    button.setAttribute("aria-selected", String(index === selected));
  }
  const window = history.windows[selected];
  $("auth-drawer-periods").replaceChildren(
    window
      ? el("div", { class: "quota-period-list" }, window.periods.map((period) => renderQuotaPeriod(period, file.category, account)))
      : el("div", { class: "muted small", text: m("ui.no_quota_windows_reported") })
  );
}

// Tag tones for the plan labels the backend reports, per provider: a Claude Pro is not a Codex Pro 200.
// Antigravity reports its upstream tier name as is.
const AUTH_PLAN_TONES = {
  codex: {
    elite: ["pro500", "pro200"],
    premium: ["pro100", "businesspremium"],
    info: ["plus"],
    ok: ["go"],
    business: ["business", "enterprise", "enterpriseautomation"]
  },
  claude: { premium: ["max"], info: ["pro"], business: ["team"] },
  antigravity: { elite: ["ultra", "googleaiultra"], premium: ["ultralite", "googleaiultralite"], info: ["pro", "googleaipro"] },
  xai: { premium: ["paid"] }
};

function authPlanTone(category, plan) {
  const key = plan.toLowerCase().replace(/[^a-z0-9]/g, "");
  return Object.entries(AUTH_PLAN_TONES[category] || {}).find(([, plans]) => plans.includes(key))?.[0] || "plain";
}

// Estimates need the usage of every request, which API key views do not have.
function renderQuotaPeriod(period, category, account) {
  const remaining = Math.max(0, 100 - period.used_percent);
  const end = period.end || period.reset_at;
  const [tone, reason] = QUOTA_PERIOD_ENDS[period.end_reason] || ["ok", "ui.quota_period_running"];
  const range = quotaPeriodRange(new Date(period.start), end && new Date(end));
  const usage = (label, metrics, tooltip) =>
    el("span", { class: "quota-period-usage", ...hoverTooltipProps(tooltip) }, el("span", { text: label }), metrics);
  return el(
    "div",
    { class: "auth-quota-row" },
    el(
      "div",
      { class: "auth-quota-head" },
      el("span", { class: "auth-quota-label", text: range }),
      el("span", { class: "tag " + tone, text: m(reason) }),
      el("span", { class: "auth-quota-value", text: remaining.toFixed(0) + "%" })
    ),
    renderQuotaBar(remaining, range),
    el(
      "div",
      { class: "auth-quota-foot" },
      usage(
        m("ui.quota_used"),
        renderQuotaMetrics(period.usage),
        period.usage.tokens > 0 ? modelUsageTooltip(account, period.usage_query) : null
      ),
      !account && usage(m("ui.quota_projected"), renderQuotaMetrics(period.usage, quotaEstimateScale(period.used_percent, period.usage))),
      period.plans?.length > 0 &&
        el(
          "span",
          { class: "quota-period-plans" },
          period.plans.flatMap((plan, index) => [
            index > 0 && "→",
            el("span", { class: "tag " + authPlanTone(category, plan), text: plan })
          ])
        )
    )
  );
}

function quotaPeriodRange(start, end) {
  const from = compactResetDate(start);
  if (!end) return from + " – …";
  const to = compactResetDate(end);
  return from + " – " + (to.slice(0, 5) === from.slice(0, 5) ? to.slice(6) : to);
}

$("auth-drawer-close").replaceChildren(actionIcon("x"));
setLabeledActionIcon($("auth-drawer-reset"), "rotate-cw-clock", m("ui.reset_quota"));
$("auth-drawer-close").onclick = () => $("auth-drawer").close();
// A drag between children also clicks the drawer itself, so only a press that starts on the backdrop closes it.
let authDrawerBackdropPress = false;
$("auth-drawer").addEventListener("pointerdown", (event) => { authDrawerBackdropPress = event.target === event.currentTarget; });
$("auth-drawer").addEventListener("click", (event) => {
  if (authDrawerBackdropPress && event.target === event.currentTarget) event.currentTarget.close();
});
$("auth-drawer").addEventListener("close", () => authDrawer.request++);
