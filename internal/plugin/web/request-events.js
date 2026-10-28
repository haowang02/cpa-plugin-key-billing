const REQUEST_EVENT_COLUMN_LAYOUT = "request-event-columns";

const REQUEST_EVENT_STATUS_OPTIONS = {
  "": { label: m("ui.all_results"), count: "all" },
  false: { label: m("ui.succeeded"), count: "normal" },
  true: { label: m("ui.failed"), count: "failed" }
};

// Table columns in their default order. Time anchors each row, so it cannot be hidden.
const REQUEST_EVENT_COLUMNS = [
  { id: "time", label: m("ui.time"), required: true, cell: ({ entry }) => dateTimeLines(entry.at) },
  { id: "api_key", label: "API Key", admin: true, cell: ({ entry }) => renderAPIKeyIdentity(entry, { stacked: true }) },
  { id: "model", label: m("ui.model"), class: "small", cell: requestEventModel },
  {
    id: "executor_source",
    label: m("ui.executor_source"),
    class: "small",
    cell: ({ entry }) => [
      el("div", { class: "request-event-cell-line mono" }, entry.executor_type || m("ui.unknown_executor")),
      el(
        "div",
        { class: "request-event-detail muted", "data-tip": displayLabel(entry.source) || null, "data-tip-overflow": "" },
        entry.source ? maskedLabelText(entry.source) : m("ui.unknown_source")
      )
    ]
  },
  {
    id: "reasoning_speed",
    label: m("ui.reasoning_speed"),
    class: "small",
    cell: ({ entry }) => [
      el("div", { class: "request-event-cell-line mono" }, entry.reasoning_effort || "—"),
      el(
        "div",
        { class: "request-event-cell-line muted" },
        [entry.service_tier, entry.response_service_tier].filter(Boolean).join(DISPLAY_SLASH) || "—"
      )
    ]
  },
  {
    id: "request",
    label: m("ui.request"),
    class: "small",
    cell: ({ entry }) => [
      el("div", {
        class: "request-event-cell-line mono",
        text:
          entry.transport === "websocket" ? "WS" : entry.transport === "http" && entry.stream != null ? entry.stream ? "SSE" : "HTTP" : "—"
      }),
      el("div", { class: "request-event-detail muted mono", text: entry.endpoint || "—" })
    ]
  },
  { id: "result", label: m("ui.result"), cell: ({ entry }) => failureTag(entry) },
  { id: "tps_latency", label: m("ui.tps_latency"), class: "num", cell: requestEventSpeed },
  { id: "input", label: m("ui.input"), class: "num", cell: ({ input, tokens }) => tokens(input) },
  {
    id: "cache_read",
    label: m("ui.cache_read"),
    class: "num",
    cell: ({ cost, input, measured, tokens }) => [
      el("div", {}, tokens(cost.cache_read_tokens)),
      measured ? el("div", { class: "muted small" }, cacheReadRate(cost.cache_read_tokens, input)) : null
    ]
  },
  { id: "cache_write", label: m("ui.cache_write"), class: "num", cell: ({ cost, tokens }) => tokens(cost.cache_write_tokens) },
  {
    id: "output",
    label: m("ui.output"),
    class: "num",
    cell: ({ entry, cost, measured, tokens }) => [
      el("div", {}, tokens(cost.billed_output_tokens)),
      measured && entry.reasoning_tokens
        ? el("div", { class: "muted small" }, m("ui.reasoning_value", { v0: exactTokens(entry.reasoning_tokens) }))
        : null
    ]
  },
  { id: "cost", label: m("ui.cost"), class: "num", cell: requestEventCost },
  { id: "session_id", label: m("ui.session_id"), class: "small", cell: requestEventSession },
  {
    id: "user_agent",
    label: "User-Agent",
    class: "small",
    cell: ({ entry }) =>
      entry.user_agent
        ? el(
            "div",
            { class: "request-event-cell-line" },
            el("span", { class: "request-event-detail", "data-tip": entry.user_agent, "data-tip-overflow": "" }, entry.user_agent),
            copyButton(m("ui.copy_user_agent"), () => entry.user_agent)
          )
        : "—"
  },
  {
    id: "x_forwarded_for",
    label: "X-Forwarded-For",
    class: "small",
    cell: ({ entry }) =>
      el(
        "div",
        { class: "request-event-detail mono", "data-tip": entry.x_forwarded_for || null, "data-tip-overflow": "" },
        entry.x_forwarded_for || "—"
      )
  }
];

function requestEventFilters(account) {
  const prefix = account ? "account-request-event" : "request-event";
  const filters = apiKeyFilter(account);
  for (const [field, control] of [["model", "model"], ["source", "source"], ["failed", "status"]]) {
    const value = $(prefix + "-" + control).value;
    if (value) filters[field] = value;
  }
  return filters;
}

// Counts cover the whole field/time selection and ignore the status choice,
// so each status option can show how many entries selecting it would reveal.
function labelRequestEventStatuses(select, counts) {
  for (const option of select.options) {
    const status = REQUEST_EVENT_STATUS_OPTIONS[option.value];
    const label = status.label + DISPLAY_SEPARATOR + int((counts || {})[status.count]);
    if (option.textContent !== label) setText(option, label);
  }
}

function populateRequestEventFilters(filters, account) {
  if (!filters) return;
  const prefix = account ? "account-request-event" : "request-event";
  populateEventFilterSelect($(prefix + "-model"), m("ui.all_models"), filters.models);
  populateSourceFilterSelect($(prefix + "-source"), filters);
}

// Keep the saved order, inserting new columns before their next default neighbor.
function requestEventColumnLayout(account, preference = viewPreference(account ? "account" : "admin", REQUEST_EVENT_COLUMN_LAYOUT, {})) {
  const { order = [], hidden = [] } = preference;
  const columns = REQUEST_EVENT_COLUMNS.filter((column) => !account || !column.admin);
  const ordered = [...new Set(order)].map((id) => columns.find((column) => column.id === id)).filter(Boolean);
  for (const [index, column] of columns.entries()) {
    if (ordered.includes(column)) continue;
    const next = columns.slice(index + 1).find((candidate) => ordered.includes(candidate));
    ordered.splice(next ? ordered.indexOf(next) : ordered.length, 0, column);
  }
  return ordered.map((column) => ({ column, visible: column.required || !hidden.includes(column.id) }));
}

function renderRequestEvents(account = false) {
  closeHoverTooltip();
  const list = pagedList("events", account);
  const note = $(account ? "account-request-event-note" : "request-event-note");
  if (list.renderEmpty()) {
    setText(note, "");
    return;
  }
  const view = list.data;
  const prefix = account ? "account-request-event" : "request-event";
  populateRequestEventFilters(view.filter_options, account);
  labelRequestEventStatuses($(prefix + "-status"), view.status_counts);
  updateFilterClearButton($(account ? "account-request-events-filter-clear" : "request-events-filter-clear"));
  const entries = view.entries;
  const hasCacheWrite = entries.some((entry) => entry.cost.cache_write_tokens !== 0);
  const columns = requestEventColumnLayout(account)
    .filter(({ column, visible }) => visible && (column.id !== "cache_write" || hasCacheWrite))
    .map(({ column }) => column);
  setText(note, loadedCountText(entries.length, view.total, list.complete));
  list.renderItems(entries, {
    render: (entry) => {
      // Missing usage details mean unknown counts, not zero usage.
      const measured = entry.accounting_quality === "complete";
      const row = {
        entry,
        cost: entry.cost,
        measured,
        hasCacheWrite,
        input: requestInputTokens(entry.cost),
        tokens: (value) => (measured ? exactTokens(value) : "—")
      };
      return el("tr", {}, columns.map((column) => el("td", { class: column.class || "" }, column.cell(row))));
    },
    headers: columns.map((column) => ({ label: column.label, num: column.class === "num" }))
  });
}

function requestEventModel({ entry }) {
  const billingModel = entry.billing_model || entry.upstream_model || "—";
  const mismatchedModel = entry.response_model && entry.response_model !== entry.upstream_model ? entry.response_model : "";
  const mismatchLabel = m("ui.upstream_served_a_model_other_than_the_one_requested");
  const note = mismatchedModel
    ? el(
        "div",
        { class: "request-event-cell-line muted" },
        actionIcon("corner-down-right"),
        mismatchedModel,
        el(
          "span",
          {
            class: "tag warn icon",
            role: "img",
            "aria-label": mismatchLabel,
            ...hoverTooltipProps(() => [el("div", { class: "hover-tooltip-body", text: mismatchLabel })], true)
          },
          actionIcon("equal-not")
        )
      )
    : entry.upstream_model && entry.upstream_model !== billingModel
      ? el("div", { class: "request-event-cell-line muted" }, actionIcon("move-right"), entry.upstream_model)
      : null;
  return [el("div", { class: "request-event-cell-line mono" }, billingModel), note];
}

function requestEventSession({ entry }) {
  const { session_id: sessionID, parent_session_id: parentSessionID } = entry.execution || {};
  const identity = (value) =>
    el(
      "span",
      { class: "request-event-detail request-event-session-id mono", "data-tip": value || null, "data-tip-overflow": "" },
      value || "—"
    );
  return [
    el("div", { class: "request-event-cell-line" }, identity(sessionID)),
    parentSessionID &&
      el(
        "div",
        { class: "request-event-cell-line muted" },
        el("span", { role: "img", "aria-label": m("ui.parent_session_id") }, actionIcon("corner-left-up", "request-event-parent-icon")),
        identity(parentSessionID)
      )
  ];
}

function failureTag(entry) {
  const body = entry.failed ? entry.error_body : "";
  const content = () => [el("pre", { class: "hover-tooltip-body mono", text: body })];
  return el("span", {
    class: "tag " + (entry.failed ? "bad" : "ok"),
    text: entry.failed ? m("ui.failed") : m("ui.succeeded"),
    ...hoverTooltipProps(content, body)
  });
}

function requestEventSpeed({ entry, cost, measured }) {
  const tps = outputTPS(cost, entry.latency_ms, measured);
  const hasTTFT = Number(entry.ttft_ms || 0) > 0;
  return [
    el("div", {}, tps === null ? "—" : tokenRate(tps)),
    el(
      "div",
      {
        class: "muted small",
        "data-tip": hasTTFT ? m("ui.time_to_first_tokenvaluetotal_latency", { v0: DISPLAY_SEPARATOR }) : m("ui.total_latency")
      },
      hasTTFT ? latency(entry.ttft_ms) + DISPLAY_SEPARATOR + latency(entry.latency_ms) : latency(entry.latency_ms)
    )
  ];
}

function requestEventCost({ cost, measured, hasCacheWrite }) {
  const note = [cost.tiered ? (cost.long_context ? m("ui.long_context") : m("ui.standard")) : "", cost.multiplier === 2.5 ? "x2.5" : ""]
    .filter(Boolean)
    .join(DISPLAY_SEPARATOR);
  return [
    el(
      "div",
      {},
      measured
        ? el(
            "span",
            {
              class: "breakdown-value",
              tabindex: "0",
              "aria-label": m("ui.view_cost_breakdown"),
              ...hoverTooltipProps(() => renderCostTooltip(cost, hasCacheWrite))
            },
            costMoney(cost.total, cost.currency)
          )
        : costMoney(cost.total, cost.currency)
    ),
    note ? el("div", { class: "muted small" }, note) : null
  ];
}

function costTierText(cost) {
  const threshold = tokenThreshold(cost.threshold_input_tokens);
  return cost.long_context ? m("ui.long_context_value", { v0: threshold }) : m("ui.standard_context_value", { v0: threshold });
}

function renderCostTooltip(cost, hasCacheWrite) {
  const rows = [
    [m("ui.uncached_input"), cost.uncached_input_tokens, cost.applied_input_per_1m, cost.uncached_input],
    [m("ui.cache_read"), cost.cache_read_tokens, cost.applied_cache_read_per_1m, cost.cache_read],
    ...(hasCacheWrite ? [[m("ui.cache_write"), cost.cache_write_tokens, cost.applied_cache_write_per_1m, cost.cache_write]] : []),
    [m("ui.output"), cost.billed_output_tokens, cost.applied_output_per_1m, cost.output]
  ];
  return [
    el("div", { class: "cost-tooltip-title", text: m("ui.cost_breakdown") }),
    ...(cost.tiered ? [el("div", { class: "cost-tooltip-summary" }, el("span", {}, m("ui.tier")), el("b", {}, costTierText(cost)))] : []),
    ...(cost.multiplier === 2.5
      ? [
          el(
            "div",
            { class: "cost-tooltip-summary" },
            el("span", {}, m("ui.billing_multiplier")),
            el("b", {}, "2.5×" + DISPLAY_SEPARATOR, m("ui.codex_fast_mode"))
          ),
          el("div", { class: "cost-tooltip-summary" }, m("ui.the_prices_and_amounts_below_include_the_multiplier"))
        ]
      : []),
    el(
      "div",
      { class: "cost-tooltip-grid" },
      el("span", { class: "cost-tooltip-head" }, m("ui.item")),
      el("span", { class: "cost-tooltip-head num" }, m("ui.tokens_unit_price")),
      el("span", { class: "cost-tooltip-head num" }, m("ui.amount")),
      rows.flatMap(([name, tokenCount, price, amount]) => [
        el("span", {}, name),
        el("span", { class: "num" }, exactTokens(tokenCount) + " × " + costMoney(price, cost.currency) + DISPLAY_SLASH + "1M"),
        el("span", { class: "num" }, costMoney(amount, cost.currency))
      ])
    ),
    el("div", { class: "cost-tooltip-total" }, el("span", {}, m("ui.total_amount")), el("span", {}, costMoney(cost.total, cost.currency)))
  ];
}

function renderRequestEventColumnSettings(layout) {
  $("request-event-columns-list").replaceChildren(
    ...layout.map(({ column, visible }) =>
      el(
        "div",
        { class: "column-setting", "data-column": column.id },
        el(
          "button",
          {
            class: "inline-icon-button link column-setting-handle",
            type: "button",
            "aria-label": m("ui.move_column_value", { v0: column.label })
          },
          actionIcon("grip-vertical")
        ),
        el(
          "label",
          {},
          el("span", { text: column.label }),
          switchControl({ input: el("input", { type: "checkbox", role: "switch", checked: visible, disabled: column.required }) })
        )
      )
    )
  );
}

function openRequestEventColumns(account) {
  openEditor("request-event-columns-dialog", null, () => {
    renderRequestEventColumnSettings(requestEventColumnLayout(account));
    $("request-event-columns-reset").onclick = () => renderRequestEventColumnSettings(requestEventColumnLayout(account, {}));
    $("request-event-columns-save").onclick = () =>
      submitEditor("request-event-columns-dialog", () => {
        const rows = [...$("request-event-columns-list").children];
        saveViewPreference(account ? "account" : "admin", REQUEST_EVENT_COLUMN_LAYOUT, {
          order: rows.map((row) => row.dataset.column),
          hidden: rows.filter((row) => !row.querySelector("input").checked).map((row) => row.dataset.column)
        });
        renderRequestEvents(account);
      });
  });
}

makeSortable($("request-event-columns-list"), ".column-setting-handle", (row) => {
  const rows = [...row.parentElement.children];
  setText(
    $("request-event-columns-status"),
    m("ui.column_position_value", { v0: row.querySelector("label > span").textContent, v1: rows.indexOf(row) + 1, v2: rows.length })
  );
});
for (const account of [false, true]) {
  const button = $(account ? "account-request-event-columns" : "request-event-columns");
  setLabeledActionIcon(button, "columns-3-cog", m("ui.columns"));
  button.onclick = () => openRequestEventColumns(account);
}
