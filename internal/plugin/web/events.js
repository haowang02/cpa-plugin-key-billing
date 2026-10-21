const EVENT_EXPORT_BATCH_SIZE = 1000;

const EVENT_CSV_COLUMNS = {
  events: [
    "at",
    "preview",
    "label",
    "billing_model",
    "upstream_model",
    "executor_type",
    "request_id",
    "trace_id",
    "session_id",
    "parent_session_id",
    "access_provider",
    "is_native_key",
    "base_url",
    "credential_ref",
    "endpoint",
    "transport",
    "stream",
    "source",
    "credential_name",
    "user_agent",
    "reasoning_effort",
    "service_tier",
    "response_service_tier",
    "failed",
    "ttft_ms",
    "latency_ms",
    "output_tps",
    "input_tokens",
    "billed_output_tokens",
    "reasoning_tokens",
    "cache_read_tokens",
    "cache_write_tokens",
    "multiplier",
    "total_usd"
  ],
  errors: ["at", "preview", "label", "billing_model", "upstream_model", "source", "status_code", "error_type", "body"]
};

// The upstream credential name is preferred over the bare provider source:
// it names the account that served the request, like the routing rules do.
function sourceLabel(entry) { return entry.credential_name || entry.source || ""; }

function renderExecutionTooltip(execution) {
  const labels = {
    request_id: "ui.execution_id",
    trace_id: "ui.trace_id",
    session_id: "ui.session_id",
    parent_session_id: "ui.parent_session_id",
    access_provider: "ui.access_provider",
    is_native_key: "ui.native_key",
    base_url: "ui.upstream_origin",
    credential_ref: "ui.credential_reference"
  };
  const fields = Object.entries(labels).filter(([key]) => execution[key] != null && execution[key] !== "");
  return fields.map(([key, label]) =>
    el(
      "div",
      {},
      el("div", { class: "hover-tooltip-heading", text: m(label) }),
      el("div", { class: "hover-tooltip-body mono", text: String(execution[key]) })
    )
  );
}

function apiKeyFilter(account) {
  const key = account ? "" : $("analysis-key").value;
  if (!key) return {};
  return key === UNASSIGNED_KEY ? { api_key_empty: "true" } : { api_key: key };
}

function requestEventFilters(account) {
  const prefix = account ? "account-request-event" : "request-event";
  const filters = apiKeyFilter(account);
  for (const [field, control] of [["model", "model"], ["source", "source"], ["failed", "status"]]) {
    const value = $(prefix + "-" + control).value;
    if (value) filters[field] = value;
  }
  return filters;
}

function errorEventFilters(account) {
  const prefix = account ? "account-" : "";
  const filters = apiKeyFilter(account);
  for (const field of ["model", "source"]) {
    const value = $(prefix + "error-" + field).value;
    if (value) filters[field] = value;
  }
  const typeChoice = $(prefix + "error-type").value;
  if (typeChoice) {
    const errorType = typeChoice.slice(5);
    if (errorType) filters.error_type = errorType; else filters.error_type_empty = "true";
  }
  return filters;
}

const REQUEST_EVENT_STATUS_OPTIONS = {
  "": { label: m("ui.all_results"), count: "all" },
  false: { label: m("ui.succeeded"), count: "normal" },
  true: { label: m("ui.failed"), count: "failed" }
};

// Counts cover the whole field/time selection and ignore the status choice,
// so each status option can show how many entries selecting it would reveal.
function labelRequestEventStatuses(select, counts) {
  for (const option of select.options) {
    const status = REQUEST_EVENT_STATUS_OPTIONS[option.value];
    const label = status.label + DISPLAY_SEPARATOR + int((counts || {})[status.count]);
    if (option.textContent !== label) setText(option, label);
  }
}

function renderSelectOptions(select, options) {
  const selected = select.value;
  const previous = new Map([...select.options].map((option) => [option.value, option]));
  let position = select.firstChild;
  for (const { value, text } of options) {
    const option = previous.get(value) || el("option", { value });
    const label = displayLabel(text);
    if (option.textContent !== label) setText(option, label);
    if (option !== position) select.insertBefore(option, position);
    position = option.nextSibling;
    previous.delete(value);
  }
  for (const option of previous.values()) option.remove();
  select.value = selected;
  if (select.selectedIndex < 0) select.value = "";
}

function populateEventFilterSelect(select, defaultLabel, values, valueOf = (item) => item, labelOf = valueOf) {
  const selected = select.value;
  const selectedLabel = select.selectedOptions[0]?.textContent || selected;
  const options = [{ value: "", text: defaultLabel }];
  let found = !selected;
  for (const item of values || []) {
    const value = String(valueOf(item) || "");
    if (!value) continue;
    if (value === selected) found = true;
    options.push({ value, text: String(labelOf(item)) });
  }
  if (selected && !found) options.push({ value: selected, text: selectedLabel });
  renderSelectOptions(select, options);
}

function populateSourceFilterSelect(select, filters) {
  const values = disambiguateSourceFilterOptions(filters.source_options || []);
  populateEventFilterSelect(select, m("ui.all_sources"), values, (option) => option.value, (option) => option.label);
}

function disambiguateSourceFilterOptions(options) {
  const prepared = options.map((option) => ({ option, label: String(displayLabel(option.label)), digest: String(option.value || "") }));
  const groups = new Map();
  for (const item of prepared) {
    if (!groups.has(item.label)) groups.set(item.label, []);
    groups.get(item.label).push(item);
  }
  return prepared.map((item) => {
    const group = groups.get(item.label);
    if (group.length < 2) return item.option;
    let length = Math.min(6, item.digest.length);
    while (length < item.digest.length && group.some((other) => other !== item && other.digest.startsWith(item.digest.slice(0, length))))
      length++;
    return { ...item.option, label: item.label + DISPLAY_SEPARATOR + "#" + item.digest.slice(0, length).toUpperCase() };
  });
}

function populateRequestEventFilters(filters, account) {
  if (!filters) return;
  const prefix = account ? "account-request-event" : "request-event";
  populateEventFilterSelect($(prefix + "-model"), m("ui.all_models"), filters.models);
  populateSourceFilterSelect($(prefix + "-source"), filters);
}

function readEventKeys(reload) {
  const range = selectedRange(false);
  return eventKeysQuery.read(
    JSON.stringify(sharedTimeRanges.admin),
    async () => {
      const revision = keyDirectory.beginRead();
      const keys = requireArray(await plugin("GET", "/events/keys?" + new URLSearchParams(range)), m("ui.event_api_keys"));
      return keyDirectory.receive(keys, revision);
    },
    reload
  );
}

function renderEventKeys() {
  const keys = eventKeysQuery.value;
  if (!keys) return;
  const select = $("analysis-key");
  const selected = select.value;
  renderSelectOptions(select, [
    { value: "", text: m("ui.all_api_keys") },
    ...keys.filter((key) => key.scope)
      .map((key) => ({ value: key.scope, text: apiKeyIdentity(key).text + (key.deleted_at ? m("ui.deleted_2") : "") })),
    ...(selected && selected !== UNASSIGNED_KEY && !keys.some((key) => key.scope === selected)
      ? [
          {
            value: selected,
            text: keyDirectory.value.has(selected)
              ? apiKeyIdentity({ scope: selected }).text
              : select.selectedOptions[0]?.textContent || selected
          }
        ]
      : []),
    ...(selected === UNASSIGNED_KEY || keys.some((key) => !key.scope) ? [{ value: UNASSIGNED_KEY, text: m("ui.unassigned") }] : [])
  ]);
}

function updateFilterClearButton(button) {
  const selects = button.parentElement.querySelectorAll("select");
  button.disabled = !Array.from(selects).some((select) => select.value);
}

function bindPagedListFilters(list, role, ids, clear) {
  const reload = () => {
    updateFilterClearButton(clear);
    guard(() => list.load(true));
  };
  for (const id of ids) {
    $(id).addEventListener("change", () => {
      saveChoice(role, id);
      reload();
    });
  }
  clear.onclick = () => {
    for (const id of ids) {
      $(id).value = "";
      saveChoice(role, id);
    }
    reload();
  };
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
  setText(note, loadedCountText(entries.length, view.total, list.complete));
  const renderRow = (entry) => {
    const cost = entry.cost;
    const costNote = [
      cost.tiered ? (cost.long_context ? m("ui.long_context") : m("ui.standard")) : "",
      cost.multiplier === 2.5 ? "x2.5" : ""
    ]
      .filter(Boolean)
      .join(DISPLAY_SEPARATOR);
    const input = requestInputTokens(cost);
    const output = cost.billed_output_tokens;
    // Missing usage details mean unknown counts, not zero usage.
    const measured = entry.accounting_quality === "complete";
    const tps = outputTPS(cost, entry.latency_ms, measured);
    const tokens = (value) => (measured ? exactTokens(value) : "—");
    const hasTTFT = Number(entry.ttft_ms || 0) > 0;

    const billingModel = entry.billing_model || entry.upstream_model || "—";
    const upstreamModel = entry.upstream_model || "—";
    const showUpstreamModel = entry.upstream_model && entry.upstream_model !== billingModel;
    const mismatchedModel = entry.response_model && entry.response_model !== entry.upstream_model ? entry.response_model : "";
    const mismatchLabel = m("ui.upstream_served_a_model_other_than_the_one_requested");
    const serviceTiers = [entry.service_tier, entry.response_service_tier].filter(Boolean).join("\u2009/\u2009") || "—";
    const requestType =
      entry.transport === "websocket"
        ? "WS"
        : entry.transport === "http" && typeof entry.stream === "boolean" ? entry.stream ? "SSE" : "HTTP" : "—";
    const modelNote = mismatchedModel
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
      : showUpstreamModel ? el("div", { class: "request-event-cell-line muted" }, actionIcon("move-right"), upstreamModel) : null;

    return el(
      "tr",
      {},
      el("td", {}, dateTimeLines(entry.at)),
      account ? null : el("td", {}, renderAPIKeyIdentity(entry, { stacked: true })),
      el("td", { class: "small" }, el("div", { class: "request-event-cell-line mono" }, billingModel), modelNote),
      el(
        "td",
        { class: "small" },
        el("div", { class: "request-event-cell-line mono" }, entry.executor_type || m("ui.unknown_executor")),
        el(
          "div",
          { class: "request-event-detail muted", "data-tip": displayLabel(sourceLabel(entry)) || null, "data-tip-overflow": "" },
          sourceLabel(entry) ? maskedLabelText(sourceLabel(entry)) : m("ui.unknown_source")
        )
      ),
      el(
        "td",
        { class: "small" },
        el(
          "div",
          { class: "request-event-detail mono", "data-tip": entry.user_agent || null, "data-tip-overflow": "" },
          entry.user_agent || "—"
        )
      ),
      el(
        "td",
        { class: "small" },
        el("div", { class: "request-event-cell-line mono" }, entry.reasoning_effort || "—"),
        el("div", { class: "request-event-cell-line muted" }, serviceTiers)
      ),
      el(
        "td",
        { class: "small request-event-request" },
        el("div", {
          class: "request-event-cell-line mono",
          text: requestType,
          ...(entry.execution ? hoverTooltipProps(() => renderExecutionTooltip(entry.execution)) : {})
        }),
        el("div", { class: "request-event-detail muted mono", text: entry.endpoint || "—" })
      ),
      el("td", {}, failureTag(entry)),
      el(
        "td",
        { class: "num" },
        el("div", {}, tps === null ? "—" : tokenRate(tps)),
        el(
          "div",
          {
            class: "muted small",
            "data-tip": hasTTFT ? m("ui.time_to_first_tokenvaluetotal_latency", { v0: DISPLAY_SEPARATOR }) : m("ui.total_latency")
          },
          hasTTFT ? latency(entry.ttft_ms) + DISPLAY_SEPARATOR + latency(entry.latency_ms) : latency(entry.latency_ms)
        )
      ),
      el("td", { class: "num" }, tokens(input)),
      el(
        "td",
        { class: "num" },
        el("div", {}, tokens(cost.cache_read_tokens)),
        measured ? el("div", { class: "muted small" }, cacheReadRate(cost.cache_read_tokens, input)) : null
      ),
      hasCacheWrite ? el("td", { class: "num" }, tokens(cost.cache_write_tokens)) : null,
      el(
        "td",
        { class: "num" },
        el("div", {}, tokens(output)),
        measured && entry.reasoning_tokens
          ? el("div", { class: "muted small" }, m("ui.reasoning_value", { v0: exactTokens(entry.reasoning_tokens) }))
          : null
      ),
      el(
        "td",
        { class: "num" },
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
                usd(cost.total_usd)
              )
            : usd(cost.total_usd)
        ),
        costNote ? el("div", { class: "muted small" }, costNote) : null
      )
    );
  };

  list.renderItems(entries, {
    render: renderRow,
    headers: [
      m("ui.time"),
      ...(account ? [] : ["API Key"]),
      m("ui.model"),
      m("ui.executor_source"),
      m("ui.user_agent"),
      m("ui.reasoning_speed"),
      m("ui.request"),
      m("ui.result"),
      { label: m("ui.tps_latency"), num: true },
      { label: m("ui.input"), num: true },
      { label: m("ui.cache_read"), num: true },
      ...(hasCacheWrite ? [{ label: m("ui.cache_write"), num: true }] : []),
      { label: m("ui.output"), num: true },
      { label: m("ui.cost"), num: true }
    ]
  });
}

function costTierText(cost) {
  const threshold = tokenThreshold(cost.threshold_input_tokens);
  return cost.long_context ? m("ui.long_context_value", { v0: threshold }) : m("ui.standard_context_value", { v0: threshold });
}

function renderCostTooltip(cost, hasCacheWrite) {
  const rows = [
    [m("ui.uncached_input"), cost.uncached_input_tokens, cost.applied_input_per_1m, cost.uncached_input_usd],
    [m("ui.cache_read"), cost.cache_read_tokens, cost.applied_cache_read_per_1m, cost.cache_read_usd],
    ...(hasCacheWrite ? [[m("ui.cache_write"), cost.cache_write_tokens, cost.applied_cache_write_per_1m, cost.cache_write_usd]] : []),
    [m("ui.output"), cost.billed_output_tokens, cost.applied_output_per_1m, cost.output_usd]
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
        el("span", { class: "num" }, exactTokens(tokenCount) + " × " + usd(price) + DISPLAY_SLASH + "1M"),
        el("span", { class: "num" }, usd(amount))
      ])
    ),
    el("div", { class: "cost-tooltip-total" }, el("span", {}, m("ui.total_amount")), el("span", {}, usd(cost.total_usd)))
  ];
}

function populateErrorFilters(view, account) {
  const prefix = account ? "account-" : "";
  const filters = view.filter_options || {};
  const counts = view.error_type_counts || {};
  const total = Object.values(counts).reduce((sum, count) => sum + count, 0);
  const typeSelect = $(prefix + "error-type");
  const types = Object.keys(counts);
  if (typeSelect.value) types.push(typeSelect.value.slice(5));
  populateEventFilterSelect(
    typeSelect,
    m("ui.all_error_typesvaluevalue", { v0: DISPLAY_SEPARATOR, v1: int(total) }),
    [...new Set(types)].sort(),
    (value) => "type:" + value,
    (value) => (value || m("ui.uncategorized")) + DISPLAY_SEPARATOR + int(counts[value])
  );
  populateEventFilterSelect($(prefix + "error-model"), m("ui.all_models"), filters.models);
  populateSourceFilterSelect($(prefix + "error-source"), filters);
}

function renderErrors(account) {
  const note = $(account ? "account-error-note" : "error-note");
  const list = pagedList("errors", account);
  if (list.renderEmpty()) {
    setText(note, "");
    return;
  }
  const view = list.data;
  populateErrorFilters(view, account);
  updateFilterClearButton($(account ? "account-errors-filter-clear" : "errors-filter-clear"));
  list.renderItems(view.entries, {
    attrs: { class: "event-list", role: "list", "aria-label": m("ui.error_event_list") },
    empty: m("ui.no_error_events"),
    render: (entry) => {
      const tags = [el("span", { class: "tag bad", text: entry.status_code ? "HTTP " + entry.status_code : m("ui.failed") })];
      if (entry.error_type) { tags.push(el("span", { class: "tag bad", text: entry.error_type })); }
      return el(
        "article",
        { class: "event-entry request-failure", role: "listitem" },
        el(
          "div",
          { class: "event-entry-head" },
          el("time", { class: "event-time", datetime: entry.at || null }, when(entry.at)),
          el(
            "div",
            { class: "event-request-meta" },
            account ? null : errorEventField("API Key", maskedText(apiKeyIdentity(entry).text), false),
            errorEventField(m("ui.model"), entry.billing_model || entry.upstream_model),
            entry.source ? errorEventField(m("ui.source"), maskedLabelText(entry.source)) : null
          ),
          el("div", { class: "event-entry-tags" }, tags)
        ),
        el("pre", { class: "event-error-body mono", text: entry.body || m("ui.no_error_details") })
      );
    }
  });
  setText(note, loadedCountText(view.entries.length, view.total, list.complete));
}

function errorEventField(label, value, mono = true) {
  if (!value) return null;
  return el(
    "div",
    { class: "error-event-field" },
    el("span", { class: "error-event-field-label", text: label }),
    el(
      "span",
      {
        class: "error-event-field-value" + (mono ? " mono" : ""),
        "data-tip": typeof value === "string" ? displayLabel(value) : null,
        "data-tip-overflow": ""
      },
      displayText(value)
    )
  );
}

function csvCell(value) {
  if (value === undefined || value === null) return "";
  return '"' + String(value).replaceAll('"', '""') + '"';
}

function eventCSV(entries, kind, account) {
  const columns = EVENT_CSV_COLUMNS[kind].filter(
    (column) => !account || !["preview", "label", "base_url", "credential_ref"].includes(column)
  );
  const lines = [columns.map(csvCell).join(",")];
  for (const record of entries) {
    const entry = account ? record : keyDirectory.resolve(record);
    const measured = entry.accounting_quality === "complete";
    const row =
      kind === "events"
        ? {
            ...entry,
            ...entry.cost,
            ...entry.execution,
            multiplier: entry.cost.multiplier || 1,
            input_tokens: requestInputTokens(entry.cost),
            output_tps: outputTPS(entry.cost, entry.latency_ms, measured)
          }
        : entry;
    lines.push(
      columns.map((column) => csvCell(kind === "events" && column.endsWith("_tokens") && !measured ? null : row[column])).join(",")
    );
  }
  return "\ufeff" + lines.join("\r\n") + "\r\n";
}

function downloadCSV(filename, content) {
  const url = URL.createObjectURL(new Blob([content], { type: "text/csv;charset=utf-8" }));
  const anchor = el("a", { href: url, download: filename });
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

async function exportEvents(kind, account) {
  const button = $(account ? "account-events-export" : "events-export");
  if (button.disabled) return;
  button.disabled = true;
  setLabeledActionIcon(button, "download", m("ui.exporting"));
  const view = PAGED_LIST_VIEWS[kind];
  try {
    const selection = pagedListSelection(kind, account);
    const params = { ...selection.params, limit: EVENT_EXPORT_BATCH_SIZE };
    const generation = sessionGeneration;
    let result = null;
    do {
      const response = await fetchListPage(kind, account, params, result);
      if (generation !== sessionGeneration) throw new StaleRequestError();
      result = mergeListPage(kind, account, response);
    } while (result.cursor);
    const entries = result.page.entries;
    const stamp = new Date().toISOString().replaceAll(":", "-").slice(0, 19);
    const name = "cpa-key-billing-" + (account ? "account-" : "") + view.tab + "-";
    downloadCSV(name + stamp + ".csv", eventCSV(entries, kind, account));
    notify(m("ui.exported_value_value_records", { v0: int(entries.length), v1: view.label }));
  } finally {
    button.disabled = false;
    setLabeledActionIcon(button, "download", m("ui.export"));
    renderPage();
  }
}

for (const account of [false, true]) {
  const role = account ? "account" : "admin";
  const prefix = account ? "account-" : "";
  bindPagedListFilters(
    pagedList("events", account),
    role,
    ["request-event-model", "request-event-source", "request-event-status"].map((id) => prefix + id),
    $(prefix + "request-events-filter-clear")
  );
  bindPagedListFilters(
    pagedList("errors", account),
    role,
    ["error-model", "error-source", "error-type"].map((id) => prefix + id),
    $(prefix + "errors-filter-clear")
  );
  const exportButton = $(account ? "account-events-export" : "events-export");
  setLabeledActionIcon(exportButton, "download", m("ui.export"));
  exportButton.onclick = () => guard(() => exportEvents(activeTab(account) === "errors" ? "errors" : "events", account));
  bindSharedTimeControls(account);
  updateSharedTimeControl(account);
}
