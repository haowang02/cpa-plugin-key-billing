const QUOTA_DIMENSIONS = [
  { key: "amount", metric: "amount", label: m("ui.amount"), inputLabel: m("quota.amount_limit"), planValue: money, format: money },
  {
    key: "token_limit",
    metric: "tokens",
    label: m("ui.tokens"),
    inputLabel: m("quota.token_limit"),
    planValue: (value) => m("quota.token_count", { value: quotaTokens(value) }),
    format: quotaTokens,
    exact: exactTokens
  },
  {
    key: "request_limit",
    metric: "requests",
    label: m("ui.requests"),
    inputLabel: m("quota.request_limit"),
    planValue: (value) => m("quota.request_count", { count: value, value: int(value) }),
    format: int
  }
];
const PERIOD_UNITS = [[86400, m("ui.days")], [3600, m("ui.hours")], [60, m("ui.minutes")], [1, m("ui.seconds")]];

function remainingQuota(usedPercent) {
  const percent = 100 - Math.max(0, Math.min(100, usedPercent || 0));
  return { percent, level: percent >= 70 ? "high" : percent >= 30 ? "medium" : "low" };
}

function periodLabel(seconds) {
  const [unit] = PERIOD_UNITS.find(([unit]) => seconds % unit === 0);
  return m("time.duration_" + { 86400: "day", 3600: "hour", 60: "minute", 1: "second" }[unit], { count: seconds / unit });
}

function quotaWindowResetLabel(row, showCountdown) {
  if (!row.started) return m("ui.not_started");
  const resetAt = new Date(row.end_at);
  const remaining = (resetAt.getTime() - Date.now()) / 1000;
  return (
    compactResetDate(resetAt) + (showCountdown ? DISPLAY_SEPARATOR + (remaining <= 0 ? m("ui.expired") : compactTimeUntil(remaining)) : "")
  );
}

function renderQuotaWindows(view, account = false) {
  if (view.unlimited) return el("span", { class: "muted", text: m("ui.unlimited") });
  return el(
    "div",
    { class: "quota-windows" },
    view.shared_quota ? el("span", { class: "small muted", text: m("ui.shared_quota_usage_note") }) : null,
    view.windows.map((window) => {
      const dimensions = window.dimensions;
      const single = dimensions.length === 1;
      const reset = el("span", { class: "muted", text: quotaWindowResetLabel(window, account) });
      return el(
        "div",
        { class: "quota-window" + (single ? " quota-single" : "") },
        single ? null : el("div", { class: "quota-title" }, el("b", { text: displayLabel(window.name) }), reset),
        dimensions.map((balance) => {
          const metric = QUOTA_DIMENSIONS.find((item) => item.metric === balance.metric);
          const quota = remainingQuota(balance.used_percent);
          const label = displayLabel(single ? window.name + (account ? DISPLAY_SEPARATOR + metric.label : "") : metric.label);
          return el(
            "div",
            { class: "quota-metric" },
            el(
              "div",
              { class: "quota-heading" },
              el(single ? "b" : "span", { class: single ? "" : "muted", text: label }),
              el("span", {
                class: "mono" + (balance.blocked ? " blocked" : ""),
                text: metric.format(balance.remaining, balance.currency) + DISPLAY_SLASH + metric.format(balance.limit, balance.currency)
              }),
              single ? reset : null
            ),
            el(
              "div",
              {
                class: "bar",
                role: "progressbar",
                "aria-label": m("ui.valuevaluevalue_remaining_quota", { v0: window.name, v1: DISPLAY_SEPARATOR, v2: metric.label }),
                "aria-valuemin": "0",
                "aria-valuemax": "100",
                "aria-valuenow": String(Math.round(quota.percent))
              },
              el("i", { class: "bar-fill " + quota.level, style: "width:" + quota.percent + "%" })
            )
          );
        })
      );
    })
  );
}

function quotaResetNote(keys) {
  const scheduled = keys.filter((key) => key.windows[0]?.cycle_anchor_at).length;
  const schedule =
    scheduled === keys.length
      ? m("ui.clear_usage_in_all_windows_and_keep_the_scheduled_reset_times")
      : scheduled === 0
        ? m("ui.all_quota_cycles_will_end_new_cycles_start_when_the_next_request_is_admitted")
        : m("ui.batch_reset_mixed_cycles");
  return keys.some((key) => key.shared_quota) ? m("ui.reset_shared_quota_note") + " " + schedule : schedule;
}
