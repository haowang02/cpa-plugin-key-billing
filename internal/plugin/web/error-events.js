// Keyed by query field; each error type shows how many entries it has under the other filters.
const ERROR_EVENT_FILTERS = {
  model: modelFilter("error-model", "errors"),
  credential: credentialFilter("error-credential", "errors"),
  error_type: {
    id: "error-type",
    label: m("ui.error_type"),
    icon: "triangle-alert",
    searchable: true,
    options: (account) => {
      const counts = pagedList("errors", account).data?.error_type_counts || {};
      return Object.keys(counts).sort()
        .map((type) => ({ value: type || EMPTY_FIELD, label: type || m("ui.uncategorized"), detail: int(counts[type]) }));
    },
    describe: (value) => (value === EMPTY_FIELD ? m("ui.uncategorized") : value)
  }
};

function errorEventQuery(account) { return { ...apiKeyQuery(account), ...filterQuery(account, ERROR_EVENT_FILTERS) }; }

function renderErrors(account) {
  const list = pagedList("errors", account);
  if (list.renderEmpty()) return;
  list.renderItems(list.data.entries, {
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
