// One toolbar serves both roles: the role's views, the current page's filters, its time range, refresh,
// privacy and a menu holding the less frequent page actions and display settings.
const TAB_LABELS = {
  keys: "API Key",
  subscription: m("ui.subscription"),
  analysis: m("ui.analysis"),
  "request-events": m("ui.request_events"),
  errors: m("ui.error_events"),
  "auth-files": m("ui.auth_files"),
  "ai-providers": m("ui.ai_providers"),
  settings: m("ui.settings")
};
const PAGE_STALE_MS = 5 * 60 * 1000;
// Each role keeps its own display settings, chosen from the page menu. The first value of each is its default.
const DISPLAY_SETTINGS = {
  currency: ["auto", ...Object.keys(CURRENCY_SYMBOLS)],
  refresh: [0, 5, 10, 30, 60, 300],
  quotaEstimate: [false, true]
};
const displaySettings = { admin: {}, account: {} };
let pageRefreshTimer = 0;
let pageRefreshTarget = null;
let toolbarLayoutFrame = 0;

function restoreDisplaySettings(role) {
  for (const [name, values] of Object.entries(DISPLAY_SETTINGS)) {
    const value = viewPreference(role, name, values[0]);
    displaySettings[role][name] = values.includes(value) ? value : values[0];
  }
}

function setDisplaySetting(name, value) {
  displaySettings[currentRole][name] = value;
  saveViewPreference(currentRole, name, value);
}

function renderPageTabs() {
  const tabs = ROLE_TABS[currentRole];
  $("page-tabs").replaceChildren(
    ...tabs.map((tab) =>
      el("button", { type: "button", "data-tab": tab, onclick: () => openTab(tab) }, TAB_LABELS[tab], el("span", { class: "count-badge" }))
    )
  );
  setTextAttribute($("toolbar"), "aria-label", currentRole === "account" ? m("ui.api_key_views") : m("ui.management_views"));
}

// An open editor or a running batch keeps the current view.
function setTab(tab) {
  if (editors.get($("route-picker"))?.saving || activeTableSelection?.busy) return false;
  const selected = roleTab(currentRole, tab);
  if (selected !== currentTab) clearTableSelection();
  if (selected !== "keys") $("route-picker").close();
  currentTab = selected;
  writePageRoute(currentRole, selected);
  for (const view of document.querySelectorAll(".page-view")) view.classList.toggle("hidden", view.id !== "tab-" + selected);
  if (!currentPage().timeRange) closeSharedTimePopover();
  renderToolbar();
  return true;
}

function openTab(tab) {
  if (tab === currentTab || !setTab(tab)) return;
  if (!refreshStalePage()) guard(() => loadPage(currentRole === "account"));
}

function renderToolbar() {
  const page = currentPage();
  if (!page) return;
  const account = currentRole === "account";
  const count = page.count?.() ?? null;
  const countText = count === null ? "" : int(count);
  for (const button of $("page-tabs").children) {
    const active = button.dataset.tab === currentTab;
    button.classList.toggle("active", active);
    button.setAttribute("aria-current", active ? "page" : "false");
    setText(button.lastChild, active ? countText : "");
  }
  const menu = $("page-tab-menu");
  setText(menu.querySelector(".page-tab-menu-label"), TAB_LABELS[currentTab]);
  setText(menu.querySelector(".count-badge"), countText);
  renderFilterBar($("page-filters"), account, page.filters, refilterPage);
  $("time-range").classList.toggle("hidden", !page.timeRange);
  $("page-menu").classList.toggle("hidden", !pageMenuItems().length);
  updateRefreshControls();
  scheduleToolbarLayout();
}

function pendingPageLoads(page) {
  // Filters and pagination can read sources outside a full-page load.
  return page ? [page.task, ...page.sources.map((source) => source.task)].filter(Boolean) : [];
}

function autoRefreshSeconds(page) { return page?.autoRefresh ? displaySettings[currentRole].refresh : 0; }

function updateRefreshControls() {
  const page = currentPage();
  const loading = pendingPageLoads(page).length > 0;
  const button = $("page-refresh");
  const seconds = autoRefreshSeconds(page);
  button.disabled = loading;
  const label = page ? m("ui.refresh_value", { v0: TAB_LABELS[currentTab] }) : m("ui.refresh");
  setTextAttribute(button, "data-tip", setTextAttribute(button, "aria-label", label));
  $("auto-refresh").classList.toggle("hidden", !page?.autoRefresh);
  setText($("auto-refresh").querySelector(".refresh-interval"), !seconds ? "" : seconds < 60 ? seconds + "s" : seconds / 60 + "min");
  schedulePageRefresh(page, loading);
}

function clearPageRefresh() {
  clearTimeout(pageRefreshTimer);
  pageRefreshTimer = 0;
  pageRefreshTarget = null;
}

// One deadline belongs to the visible page. Loads restart it on completion;
// hiding the document pauses the timer while retaining an overdue deadline.
function schedulePageRefresh(page, loading) {
  const seconds = autoRefreshSeconds(page);
  if (!seconds || loading) {
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

function refreshPage() {
  const page = currentPage();
  if (!page) return Promise.resolve();
  const pending = pendingPageLoads(page);
  return guard(() => (pending.length ? settleLoads(pending) : loadPage(currentRole === "account", { reload: true })));
}

function refreshStalePage() {
  if (currentPage()?.sources.some((source) => source.readAt && Date.now() - source.readAt >= PAGE_STALE_MS)) return refreshPage();
}

function privacyMasked() { return document.body.classList.contains("privacy-masked"); }

function setPrivacyMask(masked) {
  const button = $("privacy-mask");
  button.setAttribute("aria-pressed", String(masked));
  button.replaceChildren(actionIcon(masked ? "eye-off" : "eye"));
  setTextAttribute(button, "aria-label", m("ui.mask"));
  document.body.classList.toggle("privacy-masked", masked);
}

function pageMenuItems() {
  const page = currentPage();
  const items = (page.actions?.() || []).map((item) => ({ ...item, group: "page" }));
  if (page.currency) items.push(currencyMenuItem());
  return items;
}

function currencyMenuItem() {
  const rate = resources[currentRole].currency.value?.cny_per_usd;
  return {
    group: "display",
    label: m("ui.display_currency"),
    icon: "circle-dollar-sign",
    options: DISPLAY_SETTINGS.currency.map((value) => ({ value, label: value === "auto" ? m("ui.currency_auto") : value })),
    current: displaySettings[currentRole].currency,
    note: rate ? m("ui.exchange_rate_value", { v0: rate }) : null,
    change: (value) => {
      setDisplaySetting("currency", value);
      renderPage();
    }
  };
}

function refreshChoices() {
  return DISPLAY_SETTINGS.refresh.map((seconds) => ({
    value: seconds,
    label: !seconds
      ? m("ui.auto_refresh_off")
      : seconds < 60 ? m("time.refresh_every_second", { count: seconds }) : m("time.refresh_every_minute", { count: seconds / 60 }),
    selected: seconds === displaySettings[currentRole].refresh
  }));
}

// The tab bar gives way to a menu button when the views no longer fit beside the tools.
function updateToolbarLayout() {
  toolbarLayoutFrame = 0;
  const toolbar = $("toolbar");
  if (!toolbar.getClientRects().length) return;
  const tabs = $("page-tabs");
  const menu = $("page-tab-menu");
  const compact = tabs.scrollWidth > toolbar.querySelector(".toolbar-main").clientWidth + 1;
  const focusedTab = tabs.contains(document.activeElement);
  const focusedMenu = document.activeElement === menu;
  toolbar.classList.toggle("compact", compact);
  // Keyboard focus moves to whichever control is now shown.
  if (compact && focusedTab) menu.focus({ preventScroll: true });
  if (!compact && focusedMenu) tabs.querySelector(".active").focus({ preventScroll: true });
  const card = Array.from(document.querySelectorAll("#app .event-list-card")).find((card) => card.getClientRects().length);
  if (card) $("app").style.setProperty("--event-list-top", card.getBoundingClientRect().top + scrollY + "px");
  positionSharedTimePopover();
}

function scheduleToolbarLayout() { if (!toolbarLayoutFrame) toolbarLayoutFrame = requestAnimationFrame(updateToolbarLayout); }

$("page-refresh").append(actionIcon("refresh"));
$("page-refresh").onclick = () => refreshPage();
$("auto-refresh").append(el("span", { class: "refresh-interval" }), actionIcon("chevron-down"));
attachChoiceMenu($("auto-refresh"), {
  label: m("ui.auto_refresh_interval"),
  choices: refreshChoices,
  select: (seconds) => {
    setDisplaySetting("refresh", seconds);
    updateRefreshControls();
  }
});
$("page-menu").append(actionIcon("ellipsis"));
attachChoiceMenu($("page-menu"), {
  label: m("ui.more_actions"),
  menu: true,
  choices: () => pageMenuItems().map((item) => ({ ...item, value: item })),
  select: (item) => item.run()
});
$("privacy-mask").onclick = () => {
  setPrivacyMask(!privacyMasked());
  saveViewPreference(currentRole, "privacy-mask", privacyMasked());
};
$("page-tab-menu").append(
  el("span", { class: "page-tab-menu-label" }),
  el("span", { class: "count-badge" }),
  actionIcon("chevron-down", "page-tab-menu-chevron")
);
attachChoiceMenu($("page-tab-menu"), {
  label: m("ui.select_view"),
  align: "start",
  choices: () => ROLE_TABS[currentRole].map((tab) => ({ value: tab, label: TAB_LABELS[tab], selected: tab === currentTab })),
  select: openTab
});
setPrivacyMask(false);
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible") refreshStalePage();
  updateRefreshControls();
});
addEventListener("resize", scheduleToolbarLayout);
const toolbarResizeObserver = new ResizeObserver(scheduleToolbarLayout);
for (const element of [$("toolbar"), $("page-tabs"), $("data-status")]) toolbarResizeObserver.observe(element, { box: "border-box" });
