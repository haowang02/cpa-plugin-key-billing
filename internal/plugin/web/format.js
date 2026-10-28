const CURRENCY_SYMBOLS = { USD: "$", CNY: "¥" };

function currencyOptions() { return Object.keys(CURRENCY_SYMBOLS).map((code) => el("option", { value: code, text: code })); }

function money(value, currency, fixedDigits) {
  const symbol = CURRENCY_SYMBOLS[currency];
  const amount = Number(value || 0);
  const absolute = Math.abs(amount);
  const digits = fixedDigits ?? (absolute >= 1 ? 2 : absolute >= 0.01 ? 4 : 6);
  const compact = amount.toLocaleString("en-US", { minimumFractionDigits: fixedDigits ?? 0, maximumFractionDigits: digits });
  if (amount === 0 || fixedDigits !== undefined) return symbol + compact;
  const rounded = Number(compact.replaceAll(",", ""));
  if (rounded === 0) return "<" + symbol + "0.000001";
  const tolerance = Number.EPSILON * Math.max(1, absolute) * 8;
  if (Math.abs(amount - rounded) <= tolerance) return symbol + compact;
  return symbol + amount.toLocaleString("en-US", { minimumFractionDigits: digits, maximumFractionDigits: digits });
}

function moneyChoice() {
  const account = currentRole === "account";
  return {
    selected: $((account ? "account-" : "") + "display-currency").value,
    rate: resources[account ? "account" : "admin"].currency.value?.cny_per_usd
  };
}

// Renders compare this key to know when displayed amounts change.
function moneyDisplayKey() {
  const { selected, rate } = moneyChoice();
  return selected + ":" + (rate || "");
}

// Amounts show in the selected currency, or under Auto in the one they were
// charged in, and in USD for a mix. Converting waits for the rate.
function displayMoney(amount, from, chargedIn, fixedDigits) {
  const { selected, rate } = moneyChoice();
  const target = selected === "auto" ? (chargedIn === "CNY" ? "CNY" : "USD") : selected;
  const value = Number(amount || 0);
  if (target === from || !rate) return money(value, from, fixedDigits);
  return money(target === "CNY" ? value * rate : value / rate, target, fixedDigits);
}

// Usage totals arrive in USD with the one currency they were charged in, if any.
function usageMoney(valueUSD, chargedIn, fixedDigits) { return displayMoney(valueUSD, "USD", chargedIn, fixedDigits); }

// A request's cost and rates are in the currency of its price.
function costMoney(amount, currency, fixedDigits) { return displayMoney(amount, currency, currency, fixedDigits); }

function commonChargedIn(totals) {
  const currencies = new Set(totals.filter((total) => total.cost_usd > 0).map((total) => total.charged_in || ""));
  return currencies.size === 1 ? [...currencies][0] : "";
}

function loadedCountText(loaded, total, complete) {
  return loaded > total || (complete && loaded !== total)
    ? m("ui.loaded_value_records_value_currently_match", { v0: int(loaded), v1: int(total) })
    : m("ui.loaded_value_value_records", { v0: int(loaded), v1: int(total) });
}

function int(value) { return Number(value || 0).toLocaleString(locale()); }
function tokens(value, thousandsDigits = 1) {
  const n = Number(value || 0);
  if (n >= 1e9) return (n / 1e9).toFixed(2) + "B";
  if (n >= 1e6) return (n / 1e6).toFixed(2) + "M";
  if (n >= 1e3) return (n / 1e3).toFixed(thousandsDigits) + "K";
  return String(n);
}
function quotaTokens(n) {
  const [scale, suffix] = [[1e12, "T"], [1e9, "B"], [1e6, "M"], [1e3, "K"]].find(([scale]) => n >= scale) || [1e3, "K"];
  return Number((n / scale).toFixed(n < 1000 ? 3 : 2)) + suffix;
}
function exactTokens(value) { return Math.trunc(Number(value || 0)).toLocaleString("en-US"); }
function latency(value) {
  const milliseconds = Number(value || 0);
  if (milliseconds <= 0) return "—";
  if (milliseconds < 1000) return Math.round(milliseconds) + " ms";
  return (milliseconds / 1000).toFixed(2) + " s";
}
function tokenRate(value) { return value.toFixed(1) + " t/s"; }
function tokenRateRange(from, to) { return from.toFixed(1) + "–" + tokenRate(to); }
function outputTPS(cost, latencyMS, measured) { return measured && latencyMS > 0 ? (cost.billed_output_tokens * 1000) / latencyMS : null; }
function requestInputTokens(cost) { return cost.uncached_input_tokens + cost.cache_read_tokens + cost.cache_write_tokens; }
function cacheReadRate(cacheRead, totalInput) {
  const input = Number(totalInput || 0);
  if (input <= 0) return "0.00%";
  return ((Number(cacheRead || 0) / input) * 100).toFixed(2) + "%";
}
function tokenThreshold(value) {
  const n = Number(value || 0);
  if (n >= 1e9) return String(Number((n / 1e9).toFixed(3))) + "B";
  if (n >= 1e6) return String(Number((n / 1e6).toFixed(3))) + "M";
  if (n >= 1e3) return String(Number((n / 1e3).toFixed(3))) + "K";
  return exactTokens(n);
}
function when(value) {
  const parts = dateTimeParts(value);
  return parts ? parts.date + " " + parts.time : "—";
}
let dateFormatter = new Intl.DateTimeFormat(locale());
let timeFormatter = new Intl.DateTimeFormat(locale(), { hour: "numeric", minute: "numeric", second: "numeric", hour12: false });
function dateTimeParts(value) {
  if (!value) return null;
  const date = new Date(value);
  if (isNaN(date.getTime()) || date.getFullYear() < 2000) return null;
  return { time: timeFormatter.format(date), date: dateFormatter.format(date) };
}
function dateTimeLines(value) {
  const parts = dateTimeParts(value);
  if (!parts) return el("div", {}, "—");
  return [el("div", {}, parts.time), el("div", { class: "muted small mono" }, parts.date)];
}

function compactResetDate(date) {
  const pad = (part) => String(part).padStart(2, "0");
  return pad(date.getMonth() + 1) + "/" + pad(date.getDate()) + " " + pad(date.getHours()) + ":" + pad(date.getMinutes());
}

// Time until a moment in its largest whole unit, as "in 3 hours" or, with the left form, "3 hours left".
function compactTimeUntil(seconds, form = "in") {
  if (seconds < 60) return m(form === "left" ? "ui.less_than_a_minute_left" : "ui.in_less_than_a_minute");
  const [unit, size] = seconds >= 86400 ? ["day", 86400] : seconds >= 3600 ? ["hour", 3600] : ["minute", 60];
  return m("time." + form + "_" + unit, { count: Math.floor(seconds / size) });
}

function previewCredential(value) {
  value = String(value || "").trim();
  if (value.length <= 12) return "*".repeat(value.length);
  return value.slice(0, 6) + "…" + value.slice(-4);
}

// Format display labels without changing API values or editable text.
function displayLabel(value) {
  return window.billingI18n.isMessage(value) ? value : String(value ?? "").replace(/\s*·\s*/g, DISPLAY_SEPARATOR);
}

function displayText(value) {
  if (window.billingI18n.isMessage(value)) return el("span", {}, value);
  if (typeof value !== "string" || !value.includes("·")) return value;
  const fragment = document.createDocumentFragment();
  displayLabel(value).split(DISPLAY_SEPARATOR).forEach((part, index) => {
    if (index) fragment.append(el("span", { class: "text-separator", text: DISPLAY_SEPARATOR }));
    fragment.append(part);
  });
  return fragment;
}

function maskedTailText(provider, account) {
  return [provider, el("span", { class: "text-separator", text: DISPLAY_SEPARATOR }), maskedText(account)];
}

function maskedText(value) {
  return window.billingI18n.isMessage(value) ? displayText(value) : el("span", { class: "mask-blur" }, displayText(value));
}

function splitCredentialLabel(value) {
  const label = displayLabel(value);
  const separator = typeof label === "string" ? label.indexOf(DISPLAY_SEPARATOR) : -1;
  return separator <= 0 ? ["", value] : [label.slice(0, separator), label.slice(separator + DISPLAY_SEPARATOR.length)];
}

function maskedLabelText(value) {
  const [provider, account] = splitCredentialLabel(value);
  return provider ? maskedTailText(provider, account) : maskedText(account);
}

function apiKeyIdentity(view) {
  if (currentRole === "admin") view = keyDirectory.resolve(view);
  const scope = String(view.scope || view.key || "");
  const preview = String(view.preview || "");
  const candidate = view.label || "";
  const name = String(candidate);
  // Account views identify the caller by label and preview alone, without a scope.
  const label = name && name !== preview && name !== scope ? candidate : "";
  const primary = label || preview || (scope ? "unknown" : m("ui.unassigned"));
  const secondary = label ? preview : "";
  return { primary, secondary, text: displayLabel(secondary ? primary + DISPLAY_SEPARATOR + secondary : primary) };
}

function renderAPIKeyIdentity(view, options = {}) {
  const identity = apiKeyIdentity(view);
  const trailing = options.trailing || [];
  const line = (text, className, children = []) => el("span", { class: "api-key-identity-line" + className }, maskedText(text), children);
  return el(
    "span",
    { class: "api-key-identity" + (options.stacked ? " api-key-identity-stacked" : "") },
    line(identity.primary, "", identity.secondary ? [] : trailing),
    identity.secondary ? line(identity.secondary, " api-key-identity-secondary", trailing) : null
  );
}

function matchesTerm(text, term) {
  return (!term || String(displayLabel(text)).toLowerCase().includes(String(displayLabel(term)).toLowerCase()));
}

// Group prefixed and unprefixed variants by base model name.
function modelIdParts(modelID) {
  const slash = modelID.indexOf("/");
  if (slash < 0) return { name: modelID, prefix: "" };
  return { name: modelID.slice(slash + 1), prefix: modelID.slice(0, slash) };
}

function compareModelId(a, b) {
  const ka = modelIdParts(a);
  const kb = modelIdParts(b);
  if (ka.name !== kb.name) return ka.name < kb.name ? -1 : 1;
  if (ka.prefix !== kb.prefix) return ka.prefix < kb.prefix ? -1 : 1;
  return 0;
}
