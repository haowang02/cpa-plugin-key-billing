const MODEL_USAGE_SUMS = ["requests", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "cost_usd"];

// More than one model adds a row of their totals.
function renderModelUsageTable(models) {
  const columns = [
    [m("ui.requests"), (row) => tokens(row.requests, 2)],
    [m("ui.input"), (row) => tokens(row.input_tokens)],
    [m("ui.output"), (row) => tokens(row.output_tokens)],
    [m("ui.cache_read"), (row) => tokens(row.cache_read_tokens)],
    models.some((row) => row.cache_write_tokens > 0) && [m("ui.cache_write"), (row) => tokens(row.cache_write_tokens)],
    [m("ui.cache_rate"), (row) => cacheReadRate(row.cache_read_tokens, row.input_tokens)],
    [m("ui.cost"), (row) => usageMoney(row.cost_usd, row.charged_in, 2)]
  ].filter(Boolean);
  const row = (label, usage) =>
    el("tr", {}, el("td", { text: label }), columns.map(([, value]) => el("td", { class: "num", text: value(usage) })));
  const total = models.length > 1 && {
    ...Object.fromEntries(MODEL_USAGE_SUMS.map((field) => [field, models.reduce((sum, model) => sum + (model[field] || 0), 0)])),
    charged_in: commonChargedIn(models)
  };
  return el(
    "table",
    { class: "compact-table" },
    el("thead", {}, el("tr", {}, el("th", { text: m("ui.model") }), columns.map(([label]) => el("th", { class: "num", text: label })))),
    el("tbody", {}, models.map((model) => row(model.model || m("ui.unknown_model"), model))),
    total && el("tfoot", {}, row(m("ui.total"), total))
  );
}

// Breakdowns by model load when first shown, once per role and query. A
// failed load shows its error once and is tried again the next time.
const modelUsages = { admin: new Map(), account: new Map() };

function modelUsage(account, query) {
  const cache = modelUsages[account ? "account" : "admin"];
  const params = new URLSearchParams();
  for (const [name, value] of Object.entries(query)) for (const item of [value].flat()) params.append(name, item);
  const search = params.toString();
  const cached = cache.get(search);
  if (cached?.error) cache.delete(search);
  if (cached) return cached;
  const state = {};
  cache.set(search, state);
  state.task = guard(async () => {
    try {
      const path = "/usage/models?" + search;
      const result = account ? await accountAPI(path) : await plugin("GET", path);
      state.models = result.models;
    } catch (error) {
      if (error instanceof AuthError || error instanceof StaleRequestError) {
        cache.delete(search);
        throw error;
      }
      state.error = error.message || String(error);
    } finally { refreshHoverTooltip(); }
  });
  return state;
}

// The tooltip of a usage total breaks down the requests query selects by model.
function modelUsageTooltip(account, query) {
  if (!query) return null;
  return () => {
    const state = modelUsage(account, query);
    if (state.models?.length) return [renderModelUsageTable(state.models)];
    return [el("div", { class: "hover-tooltip-body", text: state.error || (state.models ? m("ui.no_data") : m("ui.loading")) })];
  };
}
