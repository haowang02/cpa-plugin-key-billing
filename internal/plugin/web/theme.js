"use strict";

/* Apply the host or stored theme before painting the body. */
(function () {
  const STORE_KEY = "cli-proxy-theme";
  const PLUGIN_STORE_KEY = "cpa-key-billing:theme";

  function parentDocument() {
    if (window.parent === window) return null;
    try { return window.parent.document; } catch (_) { return null; }
  }

  function storedTheme() {
    const theme = panelStoredValue(STORE_KEY)?.state?.theme;
    return typeof theme === "string" ? theme : "";
  }

  const systemDark = () => !!(window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches);

  function resolve(theme) {
    if (theme === "dark" || theme === "white") return theme;
    if (theme === "" || theme === "auto") return systemDark() ? "dark" : "white";
    return "";
  }

  function readPluginTheme() {
    try {
      const own = localStorage.getItem(PLUGIN_STORE_KEY);
      if (own === "dark" || own === "white") return own;
    } catch (_) {}
    return "";
  }

  let selectedTheme = readPluginTheme();
  function currentTheme() {
    const doc = parentDocument();
    if (doc) return doc.documentElement.getAttribute("data-theme") || "";
    return selectedTheme || resolve(storedTheme());
  }

  let appliedTheme = null;
  function apply(theme) {
    const root = document.documentElement;
    const next = theme === "dark" || theme === "white" ? theme : "";
    if (next) root.setAttribute("data-theme", next); else root.removeAttribute("data-theme");
    root.style.colorScheme = next === "dark" ? "dark" : "light";
    const changed = appliedTheme !== null && appliedTheme !== next;
    appliedTheme = next;
    if (changed) { window.dispatchEvent(new CustomEvent("billing-theme-change", { detail: next })); }
  }

  const sync = () => apply(currentTheme());
  sync();

  const doc = parentDocument();
  if (doc) { new MutationObserver(sync).observe(doc.documentElement, { attributes: true, attributeFilter: ["data-theme"] }); } else {
    window.addEventListener("storage", (event) => {
      if (!event.key || event.key === STORE_KEY || event.key === PLUGIN_STORE_KEY) {
        selectedTheme = readPluginTheme();
        sync();
      }
    });
  }
  if (window.matchMedia) { window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", sync); }
  window.billingTheme = {
    current: () => appliedTheme,
    preference: () => selectedTheme,
    select(value) {
      selectedTheme = value;
      try {
        if (selectedTheme) localStorage.setItem(PLUGIN_STORE_KEY, selectedTheme); else localStorage.removeItem(PLUGIN_STORE_KEY);
      } catch (_) {}
      sync();
    }
  };
})();
