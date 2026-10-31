"use strict";

function panelStoredValue(name) {
  try {
    const raw = localStorage.getItem(name);
    if (!raw) return null;
    const prefix = /^enc::v([12])::/.exec(raw);
    if (!prefix) return JSON.parse(raw);

    // CPAMC and CPAMP share the v2 prefix but use different host scopes.
    const scopes = prefix[1] === "2" ? [location.host, "v2|" + location.host] : [location.host + "|" + navigator.userAgent];
    const bytes = Uint8Array.from(atob(raw.slice(prefix[0].length)), (char) => char.charCodeAt(0));
    for (const scope of scopes) {
      const secret = new TextEncoder().encode("cli-proxy-api-webui::secure-storage|" + scope);
      const decoded = bytes.map((byte, index) => byte ^ secret[index % secret.length]);
      try { return JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(decoded)); } catch (_) {}
    }
  } catch (_) {}
  return null;
}

function readPanelKey() {
  const fromStore = panelStoredValue("cli-proxy-auth")?.state?.managementKey;
  if (typeof fromStore === "string" && fromStore.trim()) return fromStore.trim();

  const direct = panelStoredValue("managementKey");
  return typeof direct === "string" ? direct.trim() : "";
}
