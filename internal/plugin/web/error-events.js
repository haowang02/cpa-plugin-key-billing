function errorEventFilters(account) {
  const prefix = account ? "account-" : "";
  const filters = apiKeyFilter(account);
  for (const field of ["model", "credential"]) {
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
  populateCredentialFilterSelect($(prefix + "error-credential"), filters);
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
            entry.credential ? errorEventField(m("ui.credential"), maskedLabelText(entry.credential)) : null
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
