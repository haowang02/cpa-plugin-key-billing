const THEME_MODES = {
  "": { icon: "sun-moon", label: () => m("ui.theme_follows_system") },
  white: { icon: "sun", label: () => m("ui.theme_light") },
  dark: { icon: "moon", label: () => m("ui.theme_dark") }
};

function themeChoices() {
  return Object.entries(THEME_MODES).map(([value, mode]) => ({
    value,
    label: mode.label(),
    selected: value === window.billingTheme.preference()
  }));
}

function languageChoices() {
  return window.billingI18n.languages().map((item) => ({ ...item, selected: item.value === window.billingI18n.current() }));
}

function selectTheme(value) {
  window.billingTheme.select(value);
  updateThemeButtons();
}

function updateThemeButtons() {
  const icon = THEME_MODES[window.billingTheme.preference()].icon;
  for (const button of document.querySelectorAll('[data-page-action="theme"]')) button.replaceChildren(actionIcon(icon));
}

async function setLoginMode(mode, preserveError) {
  const ticket = ++loginModeRequest;
  loginMode = mode === "account" ? "account" : "admin";
  const route = pageRoute();
  writePageRoute(loginMode, route.role === loginMode ? route.tab : roleTab(loginMode));
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
  if (!preserveError) $("gate-error").replaceChildren();
  const saved = await recalledCredential(loginMode);
  if (ticket !== loginModeRequest) return;
  if (saved) setTextAttribute($("gate-key"), "placeholder", m("ui.key_remembered"));
  $("gate-remember").checked = !!saved;
}

function showGate(mode, error) {
  sessionGeneration++;
  clearTableSelection();
  clearToasts();
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
  currentTab = "";
  updateRefreshControls();
  $("app").classList.add("hidden");
  $("gate").classList.remove("hidden");
  $("gate-error").replaceChildren(...(error ? [notice(error, "error")] : []));
  setLoginMode(mode, true);
}

async function submitLogin() {
  const role = loginMode;
  const generation = sessionGeneration;
  let key = $("gate-key").value.trim();
  if (!key) key = await recalledCredential(role);
  if (!key) {
    $("gate-error").replaceChildren(notice(role === "account" ? m("ui.enter_an_api_key") : m("ui.enter_a_management_key"), "error"));
    return;
  }
  $("gate-submit").disabled = true;
  try {
    if (role === "account") await startAccount(key); else await startAdmin(key);
    if (generation !== sessionGeneration) return;
    try { await saveCredential(role, key, $("gate-remember").checked); } catch (err) {
      if (generation !== sessionGeneration) return;
      notify(m("ui.signed_in_but_the_session_was_not_saved_value", { v0: err.message || err }), "warning");
    }
    $("gate-key").value = "";
  } catch (err) {
    if (generation !== sessionGeneration || err instanceof StaleRequestError) return;
    if (err instanceof AuthError) await fail(err); else showGate(role, m("ui.sign_in_failed_value", { v0: err.message || err }));
  } finally { $("gate-submit").disabled = false; }
}

function signOut() {
  return guard(async () => {
    const role = currentRole || loginMode;
    await deleteCredential(role);
    const preference = viewPreferenceKey(role, "privacy-mask");
    if (preference) { try { localStorage.removeItem(preference); } catch (_) {} }
    showGate(role, "");
  });
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
  showSession();
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
  setText($("account-identity"), previewCredential(key));
  showSession();
  await loadPage(true).catch(fail);
}

function showSession() {
  restoreRolePreferences(currentRole);
  renderPageTabs();
  $("app").dataset.role = currentRole;
  $("gate").classList.add("hidden");
  $("app").classList.remove("hidden", "untracked");
  const route = pageRoute();
  setTab(route.role === currentRole ? route.tab : "");
}

function restoreRolePreferences(role) {
  const account = role === "account";
  restoreDisplaySettings(role);
  setPrivacyMask(viewPreference(role, "privacy-mask", false));
  sharedTimeRanges[role] = storedTimeRange(role);
  const range = sharedTimeRanges[role];
  const rolling = rollingTimeMode(range.mode);
  sharedTimeChoices[role] = { hour: 8, day: 7, custom: range.mode === "custom" ? range : null };
  if (rolling) sharedTimeChoices[role][rolling.unit] = rolling.value;
  const defaultDimension = account ? "models" : "api_keys";
  const dimension = viewPreference(role, "analysis-dimension", defaultDimension);
  const allowedDimensions = account ? ["models", "credentials"] : ["api_keys", "models", "credentials"];
  analysisDimensions[role] = allowedDimensions.includes(dimension) ? dimension : defaultDimension;
  const metric = viewPreference(role, "analysis-metric", "total_tokens");
  analysisMetrics[role] = Object.hasOwn(DISTRIBUTION_METRICS, metric) ? metric : "total_tokens";
  updateSharedTimeControl();
}

function resetRoleData(role) {
  const account = role === "account";
  closeSharedTimePopover();
  setPrivacyMask(false);
  destroyAnalysisCharts(account);
  speedModels[role] = "";
  if (account) accountUIState = emptyAuthQuotaState();
  else {
    $("route-picker").close();
    adminUIState = emptyAdminUIState();
    expandedPluginLogIDs.clear();
    resourceGroups.admin.configuration.syncErrors = [];
    keyDirectory.clear();
    eventKeysQuery.clear();
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
  filterValues[role].clear();
  for (const list of Object.values(pagedLists[role])) list.reset();
}

for (const button of document.querySelectorAll('[data-page-action="language"]')) {
  button.append(actionIcon("globe"));
  attachChoiceMenu(button, { label: m("language.select"), choices: languageChoices, select: window.billingI18n.select });
}
for (const button of document.querySelectorAll('[data-page-action="theme"]'))
  attachChoiceMenu(button, { label: m("ui.switch_theme"), choices: themeChoices, select: selectTheme });
for (const button of document.querySelectorAll('[data-page-action="logout"]')) {
  setActionIcon(button, "logout", m("ui.sign_out"));
  button.onclick = signOut;
}

window.addEventListener("billing-theme-change", () => {
  updateThemeButtons();
  if (currentRole && currentTab === "analysis") renderAnalysis(currentRole === "account");
});
window.addEventListener("billing-language-change", () => {
  dateFormatter = new Intl.DateTimeFormat(locale());
  timeFormatter = new Intl.DateTimeFormat(locale(), { hour: "numeric", minute: "numeric", second: "numeric", hour12: false });
  if (currentRole) updateSharedTimeControl();
  renderPage();
});

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
