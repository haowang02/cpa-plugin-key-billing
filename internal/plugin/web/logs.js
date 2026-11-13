const expandedPluginLogIDs = new Set();

const pluginLogLevels = {
  debug: { label: m("ui.debug"), class: "tag plain" },
  info: { label: m("ui.info"), class: "tag info" },
  error: { label: m("ui.error"), class: "tag bad" }
};

const PLUGIN_LOG_FILTERS = {
  level: {
    id: "log-level",
    label: m("ui.level"),
    icon: "logs",
    options: () => {
      const counts = pagedList("logs").data?.level_counts || {};
      return Object.entries(pluginLogLevels).map(([level, { label }]) => ({ value: level, label, detail: int(counts[level]) }));
    }
  },
  search: searchFilter("log-search")
};

function pluginLogQuery() { return filterQuery(false, { level: PLUGIN_LOG_FILTERS.level }); }

function renderPluginLogs() {
  const list = pagedList("logs");
  renderFilterBar($("plugin-log-filters"), false, Object.values(PLUGIN_LOG_FILTERS), (filter) => {
    if (filter === PLUGIN_LOG_FILTERS.level) guard(() => list.load(true));
    else {
      renderPluginLogs();
      if (list.data) list.renderStatus();
    }
  });
  const count = $("plugin-log-count");
  if (list.renderEmpty()) {
    setText(count, "");
    return;
  }
  const pluginLogs = list.data.entries;
  const filter = filterValue(false, PLUGIN_LOG_FILTERS.search).trim().toLowerCase();
  const currentIDs = new Set(pluginLogs.map((entry) => String(entry.id)));
  for (const id of expandedPluginLogIDs) if (!currentIDs.has(id)) expandedPluginLogIDs.delete(id);
  const searched = pluginLogs.filter((pluginLog) => matchesTerm(pluginLog.message || "", filter));
  const counts = list.data.level_counts || {};
  const selected = filterValue(false, PLUGIN_LOG_FILTERS.level);
  setText(count, int(selected ? counts[selected] : Object.values(counts).reduce((sum, value) => sum + value, 0)));
  list.renderItems(searched, {
    attrs: { class: "event-list", role: "list", "aria-label": m("ui.plugin_log_list") },
    empty: pluginLogs.length ? m("ui.no_matching_entries_in_loaded_logs") : m("ui.no_plugin_logs"),
    render: (pluginLog) => {
      const level = pluginLogLevels[pluginLog.level] || pluginLogLevels.info;
      return el(
        "article",
        { class: "event-entry", role: "listitem" },
        el(
          "div",
          { class: "event-entry-head" },
          el("time", { class: "event-time", datetime: pluginLog.at || null }, when(pluginLog.at)),
          el("span", { class: level.class, text: level.label })
        ),
        renderPluginLogMessage(pluginLog)
      );
    }
  });
}

function routeLogDetails(row) {
  const details = document.createDocumentFragment();
  JSON.stringify(row, null, 2).split("\n").forEach((line, index) => {
    if (index) details.append("\n");
    const field = line.match(/^  "(key|selected_credential)": "/);
    if (!field) {
      details.append(line);
      return;
    }
    const separator = field[1] === "selected_credential" ? line.indexOf("·", field[0].length) : -1;
    const start = separator < 0 ? field[0].length : separator + 1;
    const end = line.lastIndexOf('"');
    // displayText would normalize the middle dots inside JSON.
    details.append(line.slice(0, start), el("span", { class: "mask-blur", text: line.slice(start, end) }), line.slice(end));
  });
  return details;
}

function upstreamCredentialText(value) {
  const label = (name) => m("ui.upstream_credential_value", { v0: name });
  if (!value) return label(m("ui.unknown_2"));
  const [provider, account] = splitCredentialLabel(value);
  return provider ? maskedTailText(label(provider), account) : [label(""), maskedText(account)];
}

function renderPluginLogMessage(pluginLog) {
  const message = pluginLog.message || "";
  if (!message.startsWith("route ")) return el("div", { class: "event-detail" }, message);
  try {
    const row = JSON.parse(message.slice(6));
    const model = row.model_result === "deny" ? m("ui.denied") : row.model_result === "allow" ? m("ui.allowed") : m("ui.unknown_2");
    const check = {
      allow: { label: m("ui.credential_route_check_passed"), class: "tag ok" },
      deny: { label: m("ui.credential_route_check_denied"), class: "tag bad" },
      unverified: { label: m("ui.credential_route_check_unverified"), class: "tag bad" },
      selected: { label: m("ui.credential_route_check_not_recorded"), class: "tag plain" }
    }[row.credential_result];
    const credential =
      {
        unavailable: m("ui.upstream_credential_allowed_unavailable"),
        no_credential: m("ui.upstream_credential_none_allowed_serves_model"),
        no_match: m("ui.upstream_credential_no_available_match"),
        not_reached: m("ui.upstream_credential_selection_not_reached"),
        not_observed: m("ui.upstream_credential_selection_not_observed"),
        unrestricted: m("ui.upstream_credential_unrestricted")
      }[row.credential_result] || upstreamCredentialText(row.selected_credential);
    const outcome =
      { succeeded: m("ui.succeeded"), failed: m("ui.failed"), rejected: m("ui.rejected"), canceled: m("ui.canceled") }[row.outcome] ||
      row.outcome ||
      m("ui.unknown_2");
    const id = String(pluginLog.id);
    return el(
      "details",
      {
        open: expandedPluginLogIDs.has(id),
        ontoggle: (event) => {
          if (!event.currentTarget.isConnected) return;
          if (event.currentTarget.open) expandedPluginLogIDs.add(id); else expandedPluginLogIDs.delete(id);
        }
      },
      el(
        "summary",
        { class: "details-summary" },
        el("span", { class: "details-summary-icon", "aria-hidden": "true" }, actionIcon("chevron")),
        el(
          "span",
          { class: "row" },
          el("span", {}, m("ui.api_key_value", { v0: "" }), row.key ? maskedText(row.key) : m("ui.unknown_2")),
          el("span", { text: m("ui.model_value_value", { v0: row.model || m("ui.unknown_2"), v1: model }) }),
          el("span", {}, credential),
          check && el("span", { class: check.class, text: check.label }),
          el("span", { text: m("ui.request_result_value", { v0: outcome }) })
        )
      ),
      el("pre", { class: "event-detail mono small" }, routeLogDetails(row))
    );
  } catch (_) { return el("div", { class: "event-detail" }, message); }
}

$("plugin-logs-clear").onclick = () =>
  openActionDialog(
    m("ui.clear_all_plugin_logs"),
    m("ui.this_permanently_clears_the_plugin_s_operational_logs_billing_data_is_unaffected"),
    undefined,
    async () => {
      const result = await mutateAdmin("DELETE", "/plugin-logs");
      notify(m("ui.cleared_plugin_log_entries", { count: int(result.cleared) }));
    }
  );
