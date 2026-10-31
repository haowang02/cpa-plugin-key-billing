const PLUGIN_BASE = "/v0/management/plugins/cpa-key-billing";
const ACCOUNT_BASE = "/v0/resource/plugins/cpa-key-billing";
const PRODUCT_NAME = "CPA Key Billing";
const DISPLAY_SEPARATOR = "\u2009·\u2009";
const DISPLAY_SLASH = "\u2009/\u2009";
const $ = (id) => document.getElementById(id);
const { message: m, setText, setAttribute: setTextAttribute, UIError } = window.billingI18n;
const locale = () => window.billingI18n.locale();
window.billingI18n.initialize();

const CREDENTIAL_DB = "cpa-key-billing-credentials";
const CREDENTIAL_STORE = "vault";
const CREDENTIAL_KEY = "wrapping-key";
const SESSION_CREDENTIAL_PREFIX = "cpa-key-billing.session.v1.";
const VIEW_PREFERENCE_PREFIX = "cpa-key-billing.view.v1.";

const embedded = window.parent !== window;
document.body.classList.toggle("embedded", embedded);
const ADMIN_TAB_IDS = ["keys", "analysis", "request-events", "errors", "auth-files", "ai-providers", "settings"];
const ACCOUNT_TAB_IDS = ["subscription", "analysis", "request-events", "errors", "auth-files", "ai-providers"];
const PAGE_ROUTE_KEY = "cpa-key-billing.route.v1." + (embedded ? "embedded" : "standalone");

function parsePageRoute(value) {
  const [role, tab] = value.split("/");
  if (role === "account") return { role, tab: ACCOUNT_TAB_IDS.includes(tab) ? tab : "subscription" };
  if (role === "admin") return { role, tab: ADMIN_TAB_IDS.includes(tab) ? tab : "keys" };
  return null;
}

function pageRoute() {
  const direct = parsePageRoute(location.hash.slice(1));
  if (direct) return direct;
  try {
    const stored = parsePageRoute(sessionStorage.getItem(PAGE_ROUTE_KEY) || "");
    if (stored) return stored;
  } catch (_) {}
  return { role: "admin", tab: "keys" };
}

function writePageRoute(role, tab) {
  try { sessionStorage.setItem(PAGE_ROUTE_KEY, role + "/" + tab); } catch (_) {}
  const hash = "#" + role + "/" + tab;
  if (location.hash !== hash) history.replaceState(null, "", hash);
}

const initialRoute = pageRoute();

let authKey = "";
let currentRole = "";
let sessionGeneration = 0;
const activeRequests = new Set();
let loginMode = initialRoute.role;
let loginModeRequest = 0;
function emptyAuthQuotaState() {
  return { authQuotas: new Map(), authQuotaLoading: new Set(), authQuotaErrors: new Map(), authFileRevisions: new Map() };
}
function emptyAdminUIState() {
  return {
    ...emptyAuthQuotaState(),
    keySubmissions: new Set(),
    authStatusSubmissions: new Map(),
    aiBalanceLoading: new Set(),
    aiBalanceErrors: new Map()
  };
}
let adminUIState = emptyAdminUIState();
let accountUIState = emptyAuthQuotaState();
const authQuotaGeneration = { admin: 0, account: 0 };
const credentialScope = { admin: "", account: "" };
let accountTab = initialRoute.role === "account" ? initialRoute.tab : "subscription";

function openCredentialDB() {
  return new Promise((resolve, reject) => {
    if (!window.indexedDB || !window.crypto?.subtle) {
      reject(new UIError(m("ui.secure_key_storage_is_not_supported_in_this_environment")));
      return;
    }
    const request = indexedDB.open(CREDENTIAL_DB, 1);
    request.onupgradeneeded = () => request.result.createObjectStore(CREDENTIAL_STORE);
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error || new UIError(m("ui.browser_credential_storage_failed")));
  });
}

async function credentialTransaction(mode, operation) {
  const db = await openCredentialDB();
  try {
    return await new Promise((resolve, reject) => {
      const transaction = db.transaction(CREDENTIAL_STORE, mode);
      const request = operation(transaction.objectStore(CREDENTIAL_STORE));
      transaction.oncomplete = () => resolve(request?.result);
      transaction.onerror = transaction.onabort = () => reject(transaction.error || new UIError(m("ui.browser_credential_storage_failed")));
    });
  } finally { db.close(); }
}

async function deleteCredential(role) { try { await credentialTransaction("readwrite", (store) => store.delete(role)); } catch (_) {} }

async function wrappingKey() {
  const key = await credentialTransaction("readonly", (store) => store.get(CREDENTIAL_KEY));
  if (key) return key;
  const created = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
  const existing = await credentialTransaction("readwrite", (store) => {
    // Another tab may have created the shared key while generation was pending.
    const request = store.get(CREDENTIAL_KEY);
    request.onsuccess = () => { if (!request.result) store.put(created, CREDENTIAL_KEY); };
    return request;
  });
  return existing || created;
}

async function saveCredential(role, credential, remember) {
  const generation = sessionGeneration;
  const key = await wrappingKey();
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const data = await crypto.subtle.encrypt({ name: "AES-GCM", iv }, key, new TextEncoder().encode(credential));
  const saved = { iv: Array.from(iv), data: Array.from(new Uint8Array(data)) };
  await credentialTransaction("readwrite", (store) => {
    if (generation !== sessionGeneration) return;
    return remember ? store.put(saved, role) : store.delete(role);
  });
  if (generation !== sessionGeneration) return;
  sessionStorage.setItem(SESSION_CREDENTIAL_PREFIX + role, JSON.stringify(saved));
}

async function recalledCredential(role, includeSession = false) {
  try {
    const session = includeSession ? sessionStorage.getItem(SESSION_CREDENTIAL_PREFIX + role) : null;
    const saved = session !== null ? JSON.parse(session) : await credentialTransaction("readonly", (store) => store.get(role));
    if (!saved?.iv || !saved?.data) return "";
    const key = await credentialTransaction("readonly", (store) => store.get(CREDENTIAL_KEY));
    const data = await crypto.subtle.decrypt({ name: "AES-GCM", iv: new Uint8Array(saved.iv) }, key, new Uint8Array(saved.data));
    return new TextDecoder().decode(data).trim();
  } catch (_) { return ""; }
}

class AuthError extends UIError {}
class StaleRequestError extends Error {}

async function api(method, path, body, opts) {
  const generation = sessionGeneration;
  const init = { method, cache: "no-store", headers: { ...opts?.headers, Authorization: "Bearer " + (opts?.key ?? authKey) } };
  const timeoutMS = Number(opts?.timeoutMS ?? 30000);
  const controller = new AbortController();
  if (!opts?.command) activeRequests.add(controller);
  const timeout = setTimeout(() => controller.abort(), timeoutMS);
  init.signal = controller.signal;
  if (body !== undefined && body !== null) {
    init.headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }
  let resp, text;
  try {
    resp = await fetch(location.origin + path, init);
    text = await resp.text();
    if (generation !== sessionGeneration) throw new StaleRequestError();
  } catch (error) {
    if (generation !== sessionGeneration) throw new StaleRequestError();
    if (controller.signal.aborted) throw new UIError(m("ui.request_timed_out"));
    if (error instanceof TypeError) throw new UIError(m("ui.unable_to_connect_to_the_server"));
    throw error;
  } finally {
    clearTimeout(timeout);
    activeRequests.delete(controller);
  }
  let payload = null;
  if (text) { try { payload = JSON.parse(text); } catch (_) { if (resp.ok) throw new UIError(m("ui.invalid_server_response")); } }
  if (!resp.ok) {
    const detail = payload?.error?.message ?? payload?.error;
    const messageText = typeof detail === "string" ? detail.replace(/\s+/g, " ").trim() : "";
    // Proxy error pages and response dumps are not user-facing messages.
    const fallback = messageText && !/<[^>]+>/.test(messageText) ? messageText.slice(0, 200) + (messageText.length > 200 ? "…" : "") : "";
    const message = window.billingI18n.serverMessage(payload?.error, fallback);
    if ((resp.status === 401 || (resp.status === 403 && !opts?.keepSessionOnForbidden)) && !opts?.auxiliaryCredential) {
      throw new AuthError(
        message || (currentRole === "account" ? m("ui.invalid_api_key_or_access_denied") : m("ui.invalid_management_key_or_access_denied"))
      );
    }
    const error = new UIError(
      message ||
        (resp.status >= 500
          ? m("ui.service_unavailable_http_value", { v0: resp.status })
          : m("ui.server_error_http_value", { v0: resp.status }))
    );
    error.status = resp.status;
    throw error;
  }
  return payload;
}

const plugin = (method, path, body, opts) => api(method, PLUGIN_BASE + path, body, opts);
const accountAPI = (path, opts) => api("GET", ACCOUNT_BASE + path, null, opts);

async function settleLoads(tasks) {
  const results = await Promise.allSettled(tasks);
  const failures = results.filter((result) => result.status === "rejected").map((result) => result.reason);
  // Let every independent read finish, but never swallow unexpected errors.
  // Authentication takes precedence over ordinary failures and cancellation.
  const failure =
    failures.find((error) => error instanceof AuthError) || failures.find((error) => !(error instanceof StaleRequestError)) || failures[0];
  if (failure) throw failure;
}
