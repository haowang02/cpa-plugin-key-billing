const AUTH_QUOTA_GROUP_NAMES = new Map([
  ["gemini", "Gemini"],
  ["claude gpt", "Claude"],
  ["claude and gpt", "Claude"],
  ["gemini 4 argon", "Argon"]
]);

// An elapsed window reports nothing of its next period, which, like an unused window, resets one length after its first request.
function quotaResetMeta(row) {
  const resetAt = Date.parse(row.reset_at);
  if (!Number.isFinite(resetAt)) return null;
  const left = Math.floor((resetAt - Date.now()) / 1000);
  if (left > 0) return { elapsed: false, at: compactResetDate(new Date(resetAt)), after: compactTimeUntil(left) };
  if (!row.window_seconds) return { elapsed: true, at: compactResetDate(new Date(resetAt)), after: m("ui.quota_reset_elapsed") };
  return {
    elapsed: true,
    at: compactResetDate(new Date(Date.now() + row.window_seconds * 1000)),
    after: compactTimeUntil(row.window_seconds)
  };
}

function quotaAmount(row) {
  const hasUsed = row.used !== undefined;
  const hasLimit = row.limit !== undefined;
  if (!hasUsed && !hasLimit) return "";
  const format = (value) => quotaNumber(value, row.currency);
  if (hasUsed && hasLimit) return format(row.used) + DISPLAY_SLASH + format(row.limit);
  return hasUsed ? m("ui.used_value", { v0: format(row.used) }) : m("ui.quota_value", { v0: format(row.limit) });
}

function quotaNumber(value, currency) {
  return CURRENCY_SYMBOLS[currency] ? money(value, currency) : Number(value).toLocaleString(locale(), { maximumFractionDigits: 2 });
}

function quotaLabel(row) {
  const label = window.billingI18n.serverMessage(row.label_message, row.label || "");
  if (row.label_prefix) return m("ui.join", { v0: row.label_prefix, v1: "", v2: label });
  const group = window.billingI18n.serverMessage(row.group_message, row.group_label || "");
  const key = String(group).toLowerCase().replace(/[^a-z0-9]+/g, " ").trim().replace(/ models?$/, "");
  const name = AUTH_QUOTA_GROUP_NAMES.get(key) || group;
  return name && String(name).toLowerCase() !== String(label).toLowerCase()
    ? m("ui.join", { v0: name, v1: " ", v2: label })
    : name || label;
}

// Scaling to the whole window needs both a used share and recorded usage.
function quotaEstimateScale(usedPercent, usage) { return usedPercent > 0 && usage.tokens > 0 ? 100 / usedPercent : 0; }

function renderQuotaMetrics(usage, scale = 1, tooltip = null) {
  const known = usage && scale;
  const metric = (icon, label, value) =>
    el("span", { class: "auth-quota-metric" }, el("span", { role: "img", "aria-label": label }, actionIcon(icon)), value);
  return el(
    "span",
    {
      class: "auth-quota-metrics",
      "data-tip": known ? null : m(usage ? "ui.quota_estimate_unavailable" : "ui.quota_usage_not_collected"),
      tooltip
    },
    metric("square-text", m("ui.tokens"), known ? tokens(Math.round(usage.tokens * scale)) : "—"),
    metric("circle-dollar-sign", m("ui.amount"), known ? usageMoney(usage.cost_usd * scale, usage.charged_in, 2) : "—")
  );
}

function renderQuotaBar(percent, label) {
  const known = percent != null;
  const level = percent >= 70 ? "high" : percent >= 30 ? "medium" : "low";
  return el(
    "div",
    {
      class: "bar",
      role: "progressbar",
      "aria-label": label,
      "aria-valuemin": "0",
      "aria-valuemax": "100",
      "aria-valuenow": known ? Math.round(percent) : null
    },
    el("i", { class: "bar-fill " + (known ? level : "high"), style: "width:" + (known ? percent : 0) + "%" })
  );
}

function renderQuotaRow(row, account, estimate) {
  const label = quotaLabel(row);
  const reset = quotaResetMeta(row);
  const shown = reset?.elapsed ? { ...row, remaining_percent: 100, used: row.used === undefined ? undefined : 0, usage: null } : row;
  const percent = shown.remaining_percent;
  // The row sits inside the button that opens the drawer, so its tooltips are hover-only.
  const usage =
    row.meter &&
    (!shown.usage
      ? renderQuotaMetrics(null)
      : estimate
        ? renderQuotaMetrics(shown.usage, quotaEstimateScale(100 - percent, shown.usage))
        : renderQuotaMetrics(shown.usage, 1, shown.usage.tokens > 0 ? modelUsageTooltip(account, shown.usage_query) : null));
  const amount = quotaAmount(shown);
  const observed = row.observed_at && m("ui.quota_observed_at_value", { v0: compactResetDate(new Date(row.observed_at)) });
  return el(
    "div",
    { class: "auth-quota-row" },
    el(
      "div",
      { class: "auth-quota-head" },
      el("span", { class: "auth-quota-label", "data-tip": label, "data-tip-overflow": "", text: label }),
      reset && el("span", { class: "auth-quota-reset-after", text: reset.after }),
      el("span", { class: "auth-quota-value", "data-tip": observed, text: percent != null ? percent.toFixed(0) + "%" : "—" })
    ),
    renderQuotaBar(percent, label),
    (usage || amount || reset) &&
      el(
        "div",
        { class: "auth-quota-foot" },
        usage,
        amount && el("span", { text: amount }),
        reset && el("span", { class: "auth-quota-reset-time", text: reset.at })
      )
  );
}

function authQuotaResets(quota) {
  const listed = quota?.rate_limit_reset_credits || [];
  const credits = listed.filter((credit) => !(Date.parse(credit.expires_at) <= Date.now()));
  const count = quota?.rate_limit_reset_credits_available_count;
  return { count: count == null ? null : Math.max(0, count - listed.length + credits.length), credits };
}

function renderAuthResetCredits(file, quota) {
  if (file.category !== "codex") return null;
  const { count, credits } = authQuotaResets(quota);
  if (count === 0) return el("div", { class: "muted small", text: m("ui.no_manual_resets_available") });
  if (!credits.length) { return el("div", { class: "muted small", text: m("ui.reset_credits_expiry_unavailable") }); }
  const timezone =
    new Intl.DateTimeFormat("en", { timeZoneName: "shortOffset" }).formatToParts(new Date()).find((part) => part.type === "timeZoneName")
      ?.value || "";
  return el(
    "div",
    { class: "auth-reset-credits" },
    el("div", { class: "auth-reset-credits-title", text: m("ui.reset_credits_expiry", { v0: timezone }) }),
    credits.map((credit, index) => {
      const expiresAt = new Date(credit.expires_at);
      const left = (expiresAt.getTime() - Date.now()) / 1000;
      const time = Number.isFinite(left)
        ? [compactTimeUntil(left, "left"), DISPLAY_SEPARATOR, compactResetDate(expiresAt)]
        : [credit.expires_at || "—"];
      return el(
        "div",
        { class: "auth-reset-credit" },
        el("span", { text: m("ui.reset_credit_number", { v0: index + 1 }) }),
        el("span", { class: "auth-reset-credit-time" }, time)
      );
    })
  );
}

function canRefreshAuthQuota(file) { return file.quota_supported && !file.disabled; }

function authQuotaRefreshTitle(file) {
  if (file.quota_supported) return null;
  return (
    window.billingI18n.serverMessage(file.quota_unavailable_message, file.quota_unavailable_reason) ||
    m("ui.quota_queries_are_not_supported_for_this_auth_file_type")
  );
}

function renderAuthFileQuota(file, account) {
  const ui = authUI(account);
  const owner = ui.owner;
  const index = file.auth_index;
  const quota = file.quota;
  if (owner.authQuotaLoading.has(index) && !quota) {
    return el("div", { class: "auth-file-quota" }, el("div", { class: "muted small", text: m("ui.updating_quotas") }));
  }
  const error = owner.authQuotaErrors.get(index);
  if (error && !quota) return el("div", { class: "auth-file-quota" }, el("div", { class: "credential-error", text: error }));
  if (!quota) return el("div", { class: "muted small", text: authQuotaRefreshTitle(file) || m("ui.refresh_to_query_quota") });
  const summary = [];
  const balance = Number(quota.credit_balance);
  if (quota.credits_unlimited || (Number.isFinite(balance) && balance !== 0)) {
    const credits = quota.credits_unlimited ? m("ui.unlimited") : quotaNumber(balance, quota.credit_currency);
    summary.push(el("span", { text: m("ui.credits_value", { v0: credits }) }));
  }
  const resets = authQuotaResets(quota).count;
  if (resets != null) summary.push(el("span", { text: m("ui.available_resets_value", { v0: int(resets) }) }));
  const rows = quota.quota;
  return el(
    "div",
    { class: "auth-file-quota" },
    summary.length ? el("div", { class: "auth-quota-summary" }, summary) : null,
    error && el("div", { class: "credential-error", text: error }),
    rows.length
      ? el(
          "div",
          { class: "auth-quota-list" },
          rows.map((row) => renderQuotaRow(row, account, !account && displaySettings.admin.quotaEstimate))
        )
      : el("div", { class: "muted small", text: m("ui.the_upstream_returned_no_displayable_quota") })
  );
}
