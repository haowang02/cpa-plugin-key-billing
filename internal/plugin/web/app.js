const PAGE_REFRESH_INTERVALS = [0, 5, 10, 30, 60, 300];
const PAGE_STALE_MS = 5 * 60 * 1000;
let pageRefreshTimer = 0;
let pageRefreshTarget = null;

function pendingPageLoads(page) {
  // Filters and pagination can read sources outside a full-page load.
  return page ? [page.task, ...page.sources.map((source) => source.task)].filter(Boolean) : [];
}

function clearPageRefresh() {
  clearTimeout(pageRefreshTimer);
  pageRefreshTimer = 0;
  pageRefreshTarget = null;
}

// One deadline belongs to the visible page. Loads restart it on completion;
// hiding the document pauses the timer while retaining an overdue deadline.
function schedulePageRefresh(page, loading) {
  const seconds = currentRole ? Number($((currentRole === "account" ? "account-" : "") + "auto-refresh").value) : 0;
  if (!page?.autoRefresh || !seconds || loading) {
    clearPageRefresh();
    return;
  }
  if (pageRefreshTarget?.page !== page || pageRefreshTarget.generation !== sessionGeneration || pageRefreshTarget.seconds !== seconds) {
    clearPageRefresh();
    pageRefreshTarget = { page, generation: sessionGeneration, seconds, dueAt: Date.now() + seconds * 1000 };
  }
  if (document.visibilityState !== "visible") {
    clearTimeout(pageRefreshTimer);
    pageRefreshTimer = 0;
    return;
  }
  if (pageRefreshTimer) return;
  const target = pageRefreshTarget;
  pageRefreshTimer = setTimeout(
    () => {
      clearPageRefresh();
      if (currentPage() !== target.page || sessionGeneration !== target.generation || document.visibilityState !== "visible") {
        updateRefreshControls();
        return;
      }
      refreshPage();
    },
    Math.max(0, target.dueAt - Date.now())
  );
}

function updateRefreshControls() {
  const page = currentPage();
  const loading = pendingPageLoads(page).length > 0;
  const label = currentRole
    ? document.querySelector(currentRole === "account" ? "[data-account-tab].active" : "[data-tab].active")?.textContent.trim()
    : "";
  for (const button of document.querySelectorAll('[data-page-action="refresh"]')) {
    button.disabled = loading;
    setTextAttribute(
      button,
      "data-tip",
      setTextAttribute(button, "aria-label", label ? m("ui.refresh_value", { v0: label }) : m("ui.refresh"))
    );
  }
  schedulePageRefresh(page, loading);
}

function refreshPage() {
  const page = currentPage();
  if (!page) return Promise.resolve();
  const pending = pendingPageLoads(page);
  return guard(() => (pending.length ? settleLoads(pending) : loadPage(currentRole === "account", { reload: true })));
}

function refreshStalePage() {
  if (currentPage()?.sources.some((source) => source.readAt && Date.now() - source.readAt >= PAGE_STALE_MS)) return refreshPage();
}

for (const role of ["admin", "account"]) {
  const select = $((role === "account" ? "account-" : "") + "auto-refresh");
  select.replaceChildren(
    ...PAGE_REFRESH_INTERVALS.map((seconds) =>
      el("option", {
        value: String(seconds),
        text: seconds ? m("ui.auto_refresh_every", { v0: seconds < 60 ? seconds + "s" : seconds / 60 + "m" }) : m("ui.auto_refresh_off")
      })
    )
  );
  select.onchange = () => {
    saveChoice(role, select.id);
    updateRefreshControls();
  };
  const currency = $((role === "account" ? "account-" : "") + "display-currency");
  currency.replaceChildren(el("option", { value: "auto", text: m("ui.currency_auto") }), ...currencyOptions());
  currency.onchange = () => {
    saveChoice(role, currency.id);
    renderPage();
  };
}

function renderDisplayCurrency() {
  const { rate } = moneyChoice();
  const control = $((currentRole === "account" ? "account-" : "") + "display-currency-control");
  setTextAttribute(control, "data-tip", rate ? m("ui.exchange_rate_value", { v0: rate }) : "");
}

for (const button of document.querySelectorAll('[data-page-action="refresh"]')) {
  setActionIcon(button, "refresh", m("ui.refresh"));
  button.onclick = () => refreshPage();
}

document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible") refreshStalePage();
  updateRefreshControls();
});

async function setLoginMode(mode, preserveError) {
  const ticket = ++loginModeRequest;
  loginMode = mode === "account" ? "account" : "admin";
  const route = pageRoute();
  const tab = route.role === loginMode ? route.tab : loginMode === "account" ? "subscription" : "keys";
  writePageRoute(loginMode, tab);
  const account = loginMode === "account";
  setText(document.querySelector("title"), account ? m("ui.api_key_usage") : PRODUCT_NAME);
  $("gate-mode-admin").classList.toggle("active", !account);
  $("gate-mode-admin").setAttribute("aria-selected", String(!account));
  $("gate-mode-account").classList.toggle("active", account);
  $("gate-mode-account").setAttribute("aria-selected", String(account));
  setText($("gate-key-label"), account ? "API Key" : m("ui.management_key"));
  $("gate-username").value = account ? "api-key" : "administrator";
  setTextAttribute($("gate-key"), "placeholder", account ? m("ui.enter_api_key") : m("ui.enter_management_key"));
  setText($("gate-submit"), account ? m("ui.view_my_usage") : m("ui.open_management_console"));
  $("gate-key").value = "";
  if (!preserveError) {
    setText($("gate-error"), "");
    $("gate-error").className = "notice err";
  }
  const saved = await recalledCredential(loginMode);
  if (ticket !== loginModeRequest) return;
  if (saved) setTextAttribute($("gate-key"), "placeholder", m("ui.key_remembered"));
  $("gate-remember").checked = !!saved;
}

function showGate(mode, error) {
  sessionGeneration++;
  clearTableSelection();
  hideNotice();
  closeHoverTooltip();
  try { sessionStorage.removeItem(SESSION_CREDENTIAL_PREFIX + mode); } catch (_) {}
  for (const dialog of document.querySelectorAll("dialog[open]")) dialog.close();
  for (const controller of activeRequests) controller.abort();
  if (currentRole) {
    clearCredentialScope(currentRole === "account");
    resetRoleData(currentRole);
  }
  authKey = "";
  currentRole = "";
  updateRefreshControls();
  document.body.classList.remove("privacy-masked");
  $("app").classList.add("hidden");
  $("account-app").classList.add("hidden");
  $("gate").classList.remove("hidden");
  const box = $("gate-error");
  setText(box, error || "");
  box.className = error ? "notice err show" : "notice err";
  setLoginMode(mode, true);
}

async function submitLogin() {
  const role = loginMode;
  const generation = sessionGeneration;
  let key = $("gate-key").value.trim();
  if (!key) key = await recalledCredential(role);
  if (!key) {
    setText($("gate-error"), role === "account" ? m("ui.enter_an_api_key") : m("ui.enter_a_management_key"));
    $("gate-error").className = "notice err show";
    return;
  }
  $("gate-submit").disabled = true;
  try {
    if (role === "account") await startAccount(key); else await startAdmin(key);
    if (generation !== sessionGeneration) return;
    try { await saveCredential(role, key, $("gate-remember").checked); } catch (err) {
      if (generation !== sessionGeneration) return;
      const message = m("ui.signed_in_but_the_session_was_not_saved_value", { v0: err.message || err });
      notify(message, "err");
    }
    $("gate-key").value = "";
  } catch (err) {
    if (generation !== sessionGeneration || err instanceof StaleRequestError) return;
    if (err instanceof AuthError) await fail(err); else showGate(role, m("ui.sign_in_failed_value", { v0: err.message || err }));
  } finally { $("gate-submit").disabled = false; }
}

$("gate-form").onsubmit = (event) => {
  event.preventDefault();
  submitLogin();
};
$("gate-mode-admin").onclick = () => setLoginMode("admin");
$("gate-mode-account").onclick = () => setLoginMode("account");

async function startAdmin(key) {
  setText(document.querySelector("title"), PRODUCT_NAME);
  const generation = sessionGeneration;
  currentRole = "admin";
  authKey = key;
  await activateCredentialScope(false, key);
  if (generation !== sessionGeneration) throw new StaleRequestError();
  restoreRolePreferences("admin");
  const route = pageRoute();
  setAdminTab(route.role === "admin" ? route.tab : "keys");
  $("gate").classList.add("hidden");
  $("account-app").classList.add("hidden");
  $("app").classList.remove("hidden");
  schedulePageTabLayout();
  await loadPage(false).catch(fail);
}

async function startAccount(key) {
  setText(document.querySelector("title"), m("ui.api_key_usage"));
  const generation = sessionGeneration;
  currentRole = "account";
  authKey = key;
  // CLIProxyAPI remains authoritative for whether this downstream key is
  // currently valid. The plugin endpoint only uses the key to select its hash.
  resources.account.models.value = await fetchModels(true, key);
  await activateCredentialScope(true, key);
  if (generation !== sessionGeneration) throw new StaleRequestError();
  restoreRolePreferences("account");
  $("gate").classList.add("hidden");
  $("app").classList.add("hidden");
  $("account-app").classList.remove("hidden");
  setText($("account-identity"), previewCredential(key));
  const route = pageRoute();
  setAccountTab(route.role === "account" ? route.tab : "subscription");
  await loadPage(true).catch(fail);
  schedulePageTabLayout();
}

function restoreRolePreferences(role) {
  const account = role === "account";
  const prefix = account ? "account-" : "";
  // Dynamic choices must exist before the first filtered request, even
  // when the current result no longer contains the stored selection.
  for (const id of ["request-event-model", "request-event-source", "error-model", "error-source", "error-type"])
    restoreChoice(role, prefix + id, "", true);
  restoreChoice(role, prefix + "request-event-status", "");
  restoreChoice(role, prefix + "auth-enabled-only", true);
  restoreChoice(role, prefix + "auth-provider", "");
  restoreChoice(role, prefix + "ai-provider-enabled-only", true);
  restoreChoice(role, prefix + "ai-provider-type", "");
  restoreChoice(role, prefix + "auto-refresh", "0");
  restoreChoice(role, prefix + "display-currency", "auto");
  setPrivacyMask(role, viewPreference(role, prefix + "privacy-mask", false));
  if (!account) {
    restoreChoice(role, "analysis-key", "", true);
    if ($("analysis-key").value === UNASSIGNED_KEY) $("analysis-key").selectedOptions[0].textContent = m("ui.unassigned");
    restoreChoice(role, "keys-include-deleted", false);
    restoreChoice(role, "auth-quota-estimate", false);
    restoreChoice(role, "plugin-log-level", "all");
  }
  sharedTimeRanges[role] = storedTimeRange(role);
  const range = sharedTimeRanges[role];
  const rolling = rollingTimeMode(range.mode);
  sharedTimeChoices[role] = { hour: 8, day: 7, custom: range.mode === "custom" ? range : null };
  if (rolling) sharedTimeChoices[role][rolling.unit] = rolling.value;
  const defaultDimension = account ? "models" : "api_keys";
  const dimension = viewPreference(role, "analysis-dimension", defaultDimension);
  const allowedDimensions = account ? ["models", "sources"] : ["api_keys", "models", "sources"];
  analysisDimensions[role] = allowedDimensions.includes(dimension) ? dimension : defaultDimension;
  const metric = viewPreference(role, "analysis-metric", "total_tokens");
  analysisMetrics[role] = Object.hasOwn(DISTRIBUTION_METRICS, metric) ? metric : "total_tokens";
  updateSharedTimeControl(account);
  updateFilterClearButton($(prefix + "request-events-filter-clear"));
  updateFilterClearButton($(prefix + "errors-filter-clear"));
}

function resetRoleControls(role) {
  const account = role === "account";
  const prefix = account ? "account-" : "";
  closeSharedTimePopover(account);
  setPrivacyMask(role, false);
  $(prefix + "auth-search").value = "";
  $(prefix + "ai-provider-search").value = "";
  if (!account) {
    $("key-filter").value = "";
    $("keys-include-deleted").checked = false;
    $("price-filter").value = "";
    $("plugin-log-filter").value = "";
  }
}

function resetRoleData(role) {
  const account = role === "account";
  destroyAnalysisCharts(account);
  speedModels[role] = "";
  if (role === "account") accountUIState = emptyAuthQuotaState();
  else {
    $("route-picker").close();
    adminUIState = emptyAdminUIState();
    expandedPluginLogIDs.clear();
    resourceGroups.admin.configuration.syncErrors = [];
    keyDirectory.clear();
  }
  for (const entry of Object.values(resources[role])) Object.assign(entry, { value: null, error: "", readAt: 0 });
  for (const group of Object.values(resourceGroups[role])) group.task = null;
  for (const page of Object.values(pages[role])) {
    page.task = null;
    for (const section of page.sections) section.displayed = null;
  }
  analysisQueries[role].clear();
  speedQueries[role].clear();
  authFilesQueries[role].clear();
  aiProvidersQueries[role].clear();
  modelUsages[role].clear();
  if (!account) { eventKeysQuery.clear(); }
  for (const list of Object.values(pagedLists[role])) list.reset();
  resetRoleControls(role);
}

function privacyMasked() { return document.body.classList.contains("privacy-masked"); }

function privacyMaskButton(role) { return $((role === "account" ? "account-" : "") + "privacy-mask"); }

function setPrivacyMask(role, masked) {
  const button = privacyMaskButton(role);
  button.setAttribute("aria-pressed", String(masked));
  button.replaceChildren(actionIcon(masked ? "eye-off" : "eye"));
  setTextAttribute(button, "aria-label", m("ui.mask"));
  document.body.classList.toggle("privacy-masked", masked);
}

function activeTab(account) {
  return account ? accountTab : document.querySelector(".tabs button[data-tab].active")?.dataset.tab || "keys";
}

const PAGE_TAB_ROWS = Array.from(document.querySelectorAll(".page-tabs-row"));
let tabLayoutFrame = 0;

// The select stands in for the tab bar when the tabs do not fit; it mirrors their labels and active tab.
for (const row of PAGE_TAB_ROWS) {
  const select = row.querySelector(".page-tab-select > select");
  const tabs = Array.from(row.querySelectorAll(".tabs > button"));
  const tabName = (button) => button.dataset.tab || button.dataset.accountTab;
  select.replaceChildren(...tabs.map((button) => el("option", { value: tabName(button) }, window.billingI18n.boundText(button))));
  select.onchange = () => {
    tabs.find((button) => tabName(button) === select.value).click();
    // A refused switch leaves the previous tab active.
    select.value = tabName(tabs.find((button) => button.classList.contains("active")));
  };
}

function updatePageTabLayouts() {
  tabLayoutFrame = 0;
  for (const row of PAGE_TAB_ROWS) {
    if (!row.getClientRects().length) continue;
    const switcher = row.querySelector(".page-tab-switcher");
    const tabBar = switcher.querySelector(".tabs");
    const select = switcher.querySelector("select");
    const compact = tabBar.scrollWidth > switcher.clientWidth + 1;
    const focusedTab = tabBar.contains(document.activeElement);
    const focusedSelect = document.activeElement === select;
    row.querySelector(".page-navigation").classList.toggle("compact", compact);
    // Keyboard focus moves to whichever control is now shown.
    if (compact && focusedTab) select.focus({ preventScroll: true });
    if (!compact && focusedSelect) tabBar.querySelector(".active").focus({ preventScroll: true });
    const actions = row.querySelector(".page-tab-actions");
    const hasControls = Array.from(actions.children).some((node) => !node.classList.contains("hidden"));
    actions.classList.toggle("hidden", !hasControls);
    const card = Array.from(row.parentElement.querySelectorAll(".event-list-card")).find((card) => card.getClientRects().length);
    if (card) row.parentElement.style.setProperty("--event-list-top", card.getBoundingClientRect().top + scrollY + "px");
    positionSharedTimePopover(!!row.closest("#account-app"));
  }
}

function schedulePageTabLayout() {
  if (tabLayoutFrame) return;
  tabLayoutFrame = requestAnimationFrame(updatePageTabLayouts);
}

addEventListener("resize", schedulePageTabLayout);
const pageTabResizeObserver = new ResizeObserver(schedulePageTabLayout);
for (const element of document.querySelectorAll(".head, #admin-data-status, #account-data-status, .page-tabs-row, .page-tabs-row .tabs")) {
  pageTabResizeObserver.observe(element, { box: "border-box" });
}

function updatePageControls(account, page) {
  const prefix = account ? "account-" : "";
  const actions = $(prefix + "page-tab-actions");
  const keyed = !account && page.sources.includes(eventKeysQuery);
  actions.dataset.page = activeTab(account);
  actions.classList.toggle("has-key-filter", keyed);
  $(prefix + "shared-time-filter").classList.toggle("hidden", !page.timeRange);
  $(prefix + "display-currency-control").classList.toggle("hidden", !page.currency);
  $(prefix + "auto-refresh-control").classList.toggle("hidden", !page.autoRefresh);
  $(prefix + "request-event-columns").classList.toggle("hidden", !page.eventColumns);
  $(prefix + "events-export").classList.toggle("hidden", !page.exportEvents);
  if (!account) $("shared-key-filter").classList.toggle("hidden", !keyed);
  if (!page.timeRange) closeSharedTimePopover(account);
  updateRefreshControls();
  schedulePageTabLayout();
}

function setAccountTab(tab) {
  accountTab = ACCOUNT_TAB_IDS.includes(tab) ? tab : "subscription";
  writePageRoute("account", accountTab);
  document.querySelectorAll("[data-account-tab]").forEach((button) => {
    const active = button.dataset.accountTab === accountTab;
    button.classList.toggle("active", active);
    button.setAttribute("aria-current", active ? "page" : "false");
  });
  $("account-page-tab-select").value = accountTab;
  for (const name of ACCOUNT_TAB_IDS) { $("account-tab-" + name).classList.toggle("hidden", name !== accountTab); }
  updatePageControls(true, pages.account[accountTab]);
}

const TABS = Array.from(document.querySelectorAll(".tabs button[data-tab]"));
function setAdminTab(name) {
  if (editors.get($("route-picker"))?.saving || activeTableSelection?.busy) return;
  const selected = ADMIN_TAB_IDS.includes(name) ? name : "keys";
  if (selected !== activeTab(false)) clearTableSelection();
  if (selected !== "keys") $("route-picker").close();
  writePageRoute("admin", selected);
  for (const tab of TABS) {
    const active = tab.dataset.tab === selected;
    tab.classList.toggle("active", active);
    tab.setAttribute("aria-current", active ? "page" : "false");
  }
  $("page-tab-select").value = selected;
  const visible = selected === "settings" ? new Set(["plans", "routes", "prices", "plugin-logs"]) : new Set([selected]);
  for (const section of document.querySelectorAll('#app > section[id^="tab-"]')) {
    section.classList.toggle("hidden", !visible.has(section.id.slice("tab-".length)));
  }
  updatePageControls(false, pages.admin[selected]);
}
for (const tab of TABS)
  tab.onclick = () => {
    if (activeTab(false) === tab.dataset.tab) return;
    setAdminTab(tab.dataset.tab);
    if (!refreshStalePage()) guard(() => loadPage(false));
  };

document.querySelectorAll("[data-account-tab]").forEach((button) => {
  button.onclick = () => {
    if (activeTab(true) === button.dataset.accountTab) return;
    setAccountTab(button.dataset.accountTab);
    if (!refreshStalePage()) guard(() => loadPage(true));
  };
});
$("analysis-key").addEventListener("change", () =>
  guard(() => {
    saveChoice("admin", "analysis-key");
    return loadPage(false);
  })
);

const THEME_MODES = {
  "": { icon: "sun-moon", label: () => m("ui.theme_follows_system") },
  white: { icon: "sun", label: () => m("ui.theme_light") },
  dark: { icon: "moon", label: () => m("ui.theme_dark") }
};
function updateThemeButtons() {
  const mode = THEME_MODES[window.billingTheme.preference()];
  document.querySelectorAll('[data-page-action="theme"]').forEach((button) => button.replaceChildren(actionIcon(mode.icon)));
}

function updateThemedVisuals() {
  updateThemeButtons();
  if (currentRole && activeTab(currentRole === "account") === "analysis") renderAnalysis(currentRole === "account");
}

document.querySelectorAll('[data-page-action="logout"]').forEach((button) => {
  setActionIcon(button, "logout", m("ui.sign_out"));
  button.onclick = () =>
    guard(async () => {
      const role = currentRole || loginMode;
      await deleteCredential(role);
      const preference = viewPreferenceKey(role, (role === "account" ? "account-" : "") + "privacy-mask");
      if (preference) { try { localStorage.removeItem(preference); } catch (_) {} }
      showGate(role, "");
    });
});
function attachSelectionMenu(button, label, choices, select) {
  const menu = el("div", { class: "selection-menu", popover: "auto", role: "menu", "aria-label": label });
  button.setAttribute("aria-haspopup", "menu");
  button.setAttribute("aria-expanded", "false");
  button.onclick = () => {
    if (menu.matches(":popover-open")) return menu.hidePopover();
    menu.replaceChildren(
      ...choices().map((item) =>
        el("button", {
          type: "button",
          class: "selection-option",
          role: "menuitemradio",
          "aria-checked": String(item.selected),
          text: item.label,
          onclick: () => {
            menu.hidePopover();
            button.focus();
            select(item.value);
          }
        })
      )
    );
    menu.showPopover();
    positionAnchoredPopover(menu, button, { align: "end" });
    menu.querySelector('[aria-checked="true"]').focus({ preventScroll: true });
  };
  menu.addEventListener("toggle", (event) => button.setAttribute("aria-expanded", String(event.newState === "open")));
  menu.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      menu.hidePopover();
      button.focus();
      return;
    }
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
    event.preventDefault();
    const items = Array.from(menu.querySelectorAll("button"));
    const index = items.indexOf(document.activeElement);
    items[(index + (event.key === "ArrowDown" ? 1 : items.length - 1)) % items.length].focus();
  });
  button.after(menu);
}
document.querySelectorAll(".standalone-actions").forEach((group) => {
  const language = el(
    "button",
    { type: "button", class: "icon-action", "data-page-action": "language", "aria-label": m("language.select") },
    actionIcon("globe")
  );
  group.prepend(language);
  attachSelectionMenu(
    language,
    m("language.select"),
    () => window.billingI18n.languages().map((item) => ({ ...item, selected: item.value === window.billingI18n.current() })),
    (value) => window.billingI18n.select(value)
  );
  attachSelectionMenu(
    group.querySelector('[data-page-action="theme"]'),
    m("ui.switch_theme"),
    () =>
      Object.entries(THEME_MODES).map(([value, mode]) => ({
        value,
        label: mode.label(),
        selected: value === window.billingTheme.preference()
      })),
    (value) => {
      window.billingTheme.select(value);
      updateThemeButtons();
    }
  );
});

window.addEventListener("billing-theme-change", updateThemedVisuals);
window.addEventListener("billing-language-change", () => {
  dateFormatter = new Intl.DateTimeFormat(locale());
  timeFormatter = new Intl.DateTimeFormat(locale(), { hour: "numeric", minute: "numeric", second: "numeric", hour12: false });
  updateThemeButtons();
  updateSharedTimeControl(false);
  updateSharedTimeControl(true);
  renderPage();
  schedulePageTabLayout();
});

for (const role of ["admin", "account"]) {
  const button = privacyMaskButton(role);
  setPrivacyMask(role, false);
  button.onclick = () => {
    setPrivacyMask(role, !privacyMasked());
    saveViewPreference(role, button.id, privacyMasked());
  };
}

(async function () {
  const panelKey = readPanelKey();
  document.querySelectorAll('input[type="checkbox"]').forEach((input) => {
    const placeholder = document.createTextNode("");
    input.replaceWith(placeholder);
    placeholder.replaceWith(input.getAttribute("role") === "switch" ? switchControl({ input }) : checkboxControl({ input }));
  });
  document.querySelectorAll("[data-lucide-icon]")
    .forEach((placeholder) => placeholder.replaceWith(actionIcon(placeholder.dataset.lucideIcon, placeholder.className)));
  updateThemeButtons();
  try {
    if (embedded && panelKey && initialRoute.role !== "account") {
      await startAdmin(panelKey);
      return;
    }
    const saved = await recalledCredential(loginMode, true);
    if (!saved) {
      showGate(loginMode, "");
      return;
    }
    if (loginMode === "account") await startAccount(saved); else await startAdmin(saved);
  } catch (err) {
    if (err instanceof AuthError) await fail(err); else showGate(loginMode, m("ui.sign_in_failed_value", { v0: err.message || err }));
  }
})();
