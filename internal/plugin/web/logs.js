const expandedPluginLogIDs = new Set();

const pluginLogLevels = {
  debug: { label: m("ui.debug"), class: "tag plain" },
  error: { label: m("ui.error"), class: "tag bad" },
  info: { label: m("ui.info"), class: "tag info" }
};

function renderPluginLogs() {
  const list = pagedList("logs");
  const note = $("plugin-log-note");
  if (list.renderEmpty()) {
    setText(note, "");
    return;
  }
  const pluginLogs = list.data.entries;
  const filter = $("plugin-log-filter").value.trim().toLowerCase();
  const currentIDs = new Set(pluginLogs.map((entry) => String(entry.id)));
  for (const id of expandedPluginLogIDs) if (!currentIDs.has(id)) expandedPluginLogIDs.delete(id);
  const searched = pluginLogs.filter((pluginLog) => {
    if (!filter) return true;
    return matchesTerm(pluginLog.message || "", filter);
  });
  const counts = { ...list.data.level_counts, all: Object.values(list.data.level_counts || {}).reduce((sum, count) => sum + count, 0) };
  const levelSelect = $("plugin-log-level");
  for (const option of levelSelect.options) {
    const label = option.value === "all" ? m("ui.all_levels") : pluginLogLevels[option.value].label;
    const text = label + DISPLAY_SEPARATOR + int(counts[option.value]);
    if (option.textContent !== text) setText(option, text);
  }
  const total = counts[levelSelect.value] || 0;
  setText(note, loadedCountText(pluginLogs.length, total, list.complete));
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
    // Preserve JSON whitespace and escapes; displayText would normalize middle-dot separators.
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
  if (!message.startsWith("route ")) return el("div", { class: "plugin-log-message" }, message);
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
      el("pre", { class: "event-error-body mono" }, routeLogDetails(row))
    );
  } catch (_) { return el("div", { class: "plugin-log-message" }, message); }
}

$("plugin-log-filter").addEventListener("input", () => {
  renderPluginLogs();
  const list = pagedList("logs");
  if (list.data) list.renderStatus();
});
$("plugin-log-level").addEventListener("change", () => {
  saveChoice("admin", "plugin-log-level");
  guard(() => pagedList("logs").load(true));
});
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
