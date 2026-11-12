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
    "credential",
    "reasoning_effort",
    "service_tier",
    "effective_service_tier",
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
  errors: ["at", "preview", "label", "billing_model", "upstream_model", "credential", "status_code", "error_type", "body"]
};

// Analysis and event lists share one API key choice, offered from the keys with events in the time range.
const API_KEY_FILTER = {
  id: "api-key",
  label: "API Key",
  icon: "key",
  searchable: true,
  masked: true,
  source: eventKeysQuery,
  load: readEventKeys,
  options: (account) => {
    const keys = eventKeysQuery.value || [];
    // Only the event lists count their rows per key.
    const list = { "request-events": "events", errors: "errors" }[currentTab];
    const counts = list && pagedList(list, account).data?.filter_options?.key_counts;
    const detail = (scope) => (counts ? int(counts[scope] || 0) : null);
    return [
      ...keys.filter((key) => key.scope).map((key) => ({
        value: key.scope,
        label: apiKeyIdentity(key).text + (key.deleted_at ? m("ui.deleted_2") : ""),
        detail: detail(key.scope)
      })),
      ...(keys.some((key) => !key.scope) ? [{ value: EMPTY_FIELD, label: m("ui.unassigned"), detail: detail("") }] : [])
    ];
  },
  describe: (value) => (value === EMPTY_FIELD ? m("ui.unassigned") : apiKeyIdentity({ scope: value }).text)
};

function apiKeyQuery(account) { return account ? {} : filterQuery(false, { api_key: API_KEY_FILTER }); }

// Event lists offer the models and upstream credentials found in the whole time range.
function modelFilter(id, kind) {
  return {
    id,
    label: m("ui.model"),
    icon: "astroid",
    searchable: true,
    options: (account) => {
      const filters = pagedList(kind, account).data?.filter_options;
      return (filters?.models || []).map((model) => ({ value: model, label: model, detail: int(filters.model_counts?.[model] || 0) }));
    }
  };
}

function credentialFilter(id, kind) {
  return {
    id,
    label: m("ui.credential"),
    icon: "file-key",
    searchable: true,
    masked: true,
    options: (account) => disambiguateCredentialOptions(pagedList(kind, account).data?.filter_options?.credential_options || [])
  };
}

// Credentials sharing a display name gain the shortest digest prefix that tells them apart.
function disambiguateCredentialOptions(options) {
  const prepared = options.map((option) => ({
    value: option.value,
    label: String(displayLabel(option.label)),
    detail: int(option.count || 0),
    digest: String(option.value || "")
  }));
  const groups = new Map();
  for (const item of prepared) {
    if (!groups.has(item.label)) groups.set(item.label, []);
    groups.get(item.label).push(item);
  }
  return prepared.map((item) => {
    const group = groups.get(item.label);
    let label = item.label;
    if (group.length > 1) {
      let length = Math.min(6, item.digest.length);
      while (length < item.digest.length && group.some((other) => other !== item && other.digest.startsWith(item.digest.slice(0, length))))
        length++;
      label += DISPLAY_SEPARATOR + "#" + item.digest.slice(0, length).toUpperCase();
    }
    return { value: item.value, label, detail: item.detail };
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

let eventsExporting = false;

function eventExportAction(kind, account) {
  return {
    label: eventsExporting ? m("ui.exporting") : m("ui.export"),
    icon: "download",
    disabled: eventsExporting,
    run: () => guard(() => exportEvents(kind, account))
  };
}

async function exportEvents(kind, account) {
  if (eventsExporting) return;
  eventsExporting = true;
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
    notify(m("ui.exported_records", { count: int(entries.length) }));
  } finally {
    eventsExporting = false;
    renderPage();
  }
}

function updateEventCleanupRange() {
  const custom = $("events-clear-range").value === "custom";
  $("events-clear-days-field").classList.toggle("hidden", !custom);
  $("events-clear-days").disabled = !custom;
  if (custom) $("events-clear-days").focus();
}

function eventCleanupAction() {
  return {
    label: m("ui.data_cleanup"),
    icon: "trash-2",
    danger: true,
    run: () =>
      openEditor("events-clear-dialog", null, () => {
        $("events-clear-range").value = "all";
        $("events-clear-days").value = "30";
        updateEventCleanupRange();
      })
  };
}

$("events-clear-range").replaceChildren(
  el("option", { value: "all" }, m("ui.all_request_events")),
  ...[1, 7, 30, 90, 180, 365].map((days) => el("option", { value: days }, m("ui.events_older_than_days", { count: days }))),
  el("option", { value: "custom" }, m("ui.custom"))
);
$("events-clear-range").onchange = updateEventCleanupRange;
$("events-clear-confirm").onclick = () => {
  const range = $("events-clear-range").value;
  const input = $("events-clear-days");
  if (range === "custom" && !input.reportValidity()) return;
  const days = range === "all" ? 0 : Number(range === "custom" ? input.value : range);
  return submitEditor(
    "events-clear-dialog",
    async () => {
      const result = await plugin("DELETE", "/events", { older_than_days: days }, { command: true });
      resetEventHistory();
      notify(m("ui.cleared_request_events", { count: int(result.cleared) }));
      await loadPage(false);
    },
    m("ui.cleaning")
  );
};
