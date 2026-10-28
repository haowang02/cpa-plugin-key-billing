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
    "currency",
    "cost",
    "user_agent",
    "x_forwarded_for"
  ],
  errors: ["at", "preview", "label", "billing_model", "upstream_model", "source", "status_code", "error_type", "body"]
};

function apiKeyFilter(account) {
  const key = account ? "" : $("analysis-key").value;
  if (!key) return {};
  return key === UNASSIGNED_KEY ? { api_key_empty: "true" } : { api_key: key };
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

function readEventKeys(reload) {
  const range = selectedRange(false);
  return eventKeysQuery.read(
    JSON.stringify(sharedTimeRanges.admin),
    async () => {
      const revision = keyDirectory.beginRead();
      const keys = await plugin("GET", "/events/keys?" + new URLSearchParams(range));
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
            cost: entry.cost.total,
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
