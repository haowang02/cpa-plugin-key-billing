function credentialColumnHeader(label, actions = []) {
  return el(
    "div",
    { class: "credential-column-heading" },
    el("span", {}, label),
    actions.length > 0 && el("div", { class: "credential-column-actions" }, actions.map(credentialColumnButton))
  );
}

function credentialColumnButton(action) {
  const toggle = action.checked !== undefined;
  return el(
    "button",
    {
      type: "button",
      class: "credential-column-action" + (action.loading ? " loading" : ""),
      disabled: action.disabled,
      "aria-pressed": toggle ? String(action.checked) : null,
      "aria-busy": action.loading ? "true" : null,
      onclick: (event) => {
        const list = event.currentTarget.closest(".credential-list");
        const focused = document.activeElement === event.currentTarget;
        action.run();
        if (toggle && focused) list.querySelector(".credential-column-action[aria-pressed]")?.focus({ preventScroll: true });
      }
    },
    !toggle && actionIcon(action.loading ? "loader-circle" : action.icon),
    el("span", { class: "credential-column-action-label" }, action.label),
    toggle && el("span", { class: "credential-column-switch", "aria-hidden": "true" })
  );
}

function updateCredentialBatchProgress(id, label) {
  const target = $(id).querySelector('.credential-column-action[aria-busy="true"] .credential-column-action-label');
  if (target) setText(target, label);
}

function credentialMetricHeaders() { return ["ui.in_flight_requests", "ui.historical_usage"].map((key) => m(key)); }

function renderCredentialMetrics({ usage, in_flight: inFlight }, breakdown) {
  const inFlightCell = el(
    "td",
    { class: "credential-inflight-cell" },
    el(
      "div",
      { class: "credential-metric" },
      el("span", { class: "credential-metric-label", text: m("ui.in_flight_requests") }),
      el("span", { class: "credential-metric-value", text: int(inFlight) })
    )
  );
  return [inFlightCell, renderCredentialUsage(usage, breakdown)];
}

function renderCredentialUsage(usage, breakdown) {
  const summary = usage && credentialUsageSummary(usage);
  const requests = [summary ? tokens(summary.succeeded, 2) : "—"];
  if (summary?.failed > 0) {
    requests.push(DISPLAY_SLASH, el("span", { class: "credential-request-failed", text: tokens(summary.failed, 2) }));
  }
  const metrics = [
    ["arrow-left-right", m("ui.successful_failed_requests"), requests],
    ["square-text", m("ui.auth_total_tokens"), summary ? tokens(summary.totalTokens) : "—"],
    ["circle-check", m("ui.success_rate"), summary ? summary.successRate : "—"],
    ["circle-dollar-sign", m("ui.cost"), usage ? usageMoney(usage.cost_usd, usage.charged_in, 2) : "—"]
  ];
  return el(
    "td",
    { class: "credential-usage-cell" },
    el("span", { class: "credential-metric-label", text: m("ui.historical_usage") }),
    el(
      "div",
      { class: "credential-usage", ...hoverTooltipProps(breakdown, breakdown && summary?.requests > 0) },
      metrics.map(([icon, label, value]) =>
        el(
          "span",
          { class: "credential-usage-item" },
          actionIcon(icon),
          el("span", { class: "visually-hidden" }, label, ": "),
          el("span", {}, value)
        )
      )
    )
  );
}
