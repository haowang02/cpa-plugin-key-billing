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

function renderPlans() {
  if (renderDataPending($("plans-body"), [resources.admin.plans, resources.admin.keys], m("ui.loading"), "empty section-loading")) return;
  const plans = resources.admin.plans.value.map((plan) => ({
    plan,
    boundKeys: resources.admin.keys.value.filter((key) => key.plan_id === plan.id)
  }));
  renderCollection($("plans-body"), plans, {
    attrs: { class: "event-list settings-card-list", role: "list" },
    empty: m("ui.no_subscription_plans"),
    render: ({ plan, boundKeys }) => {
      const name = plan.name || plan.id;
      const windows = plan.windows.map((window) => {
        const quotas = settingsChipList(
          QUOTA_DIMENSIONS.filter((metric) => window[metric.key] > 0).map((metric) => ({
            text: metric.planValue(window[metric.key], plan.currency),
            hint: metric.metric === "tokens" ? m("ui.metric_value", { v0: metric.label, v1: metric.exact(window[metric.key]) }) : null
          }))
        );
        if (window.cycle_anchor_at)
          quotas.append(el("span", { class: "small muted", text: m("ui.next_reset_value", { v0: when(nextCycleStart(window)) }) }));
        const field = settingsEntryField(window.name + DISPLAY_SEPARATOR + periodLabel(window.period_seconds), quotas);
        field.classList.add("plan-window-summary");
        return field;
      });
      return settingsCard(
        name,
        [
          settingsEntryField(
            m("ui.cycle_mode"),
            el("span", { text: plan.windows[0]?.cycle_anchor_at ? m("ui.shared_cycles") : m("ui.independent_cycles") })
          ),
          ...windows,
          boundKeyField(boundKeys)
        ],
        () => editPlan(plan),
        () =>
          openActionDialog(
            m("ui.delete_subscription_plan_value", { v0: name }),
            boundKeys.length
              ? m("ui.unbind_the_plan_from_value_subscription_quotas_will_no_longer_apply_value", {
                  v0: bindingSummary(boundKeys),
                  v1: boundKeys.some((key) => key.deleted_at) ? m("ui.this_also_applies_when_deleted_keys_are_added_again") : ""
                })
              : "",
            undefined,
            async () => {
              await mutateAdmin("DELETE", "/plans?id=" + encodeURIComponent(plan.id));
              notify(m("ui.subscription_plan_deleted"));
            }
          )
      );
    }
  });
}

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
              el("i", { class: "quota-fill " + quota.level, style: "width:" + quota.percent + "%" })
            )
          );
        })
      );
    })
  );
}

function nextCycleStart(window, now = Date.now()) {
  const anchor = Date.parse(window.cycle_anchor_at);
  const period = window.period_seconds * 1000;
  return new Date(anchor + (Math.floor((now - anchor) / period) + 1) * period);
}

function sameWindowSchedule(left, right) {
  if (left.period_seconds !== right.period_seconds || !!left.cycle_anchor_at !== !!right.cycle_anchor_at) return false;
  return (
    !left.cycle_anchor_at || (Date.parse(left.cycle_anchor_at) - Date.parse(right.cycle_anchor_at)) % (left.period_seconds * 1000) === 0
  );
}

function syncPlanWindows() {
  const unified = $("plan-cycle-mode").value === "unified";
  closeDateTimePickers();
  const rows = Array.from($("plan-windows").children);
  rows.forEach((row) => {
    row.classList.toggle("unified", unified);
    row.querySelector(".window-delete").disabled = rows.length === 1;
    if (row.querySelector('[name="window_period"]').hasAttribute("aria-invalid")) validatePlanPeriod(row);
    const field = row.querySelector(".plan-window-schedule");
    field.classList.toggle("hidden", !unified);
    field.querySelector('[name="window_anchor"]').disabled = !unified;
    field.querySelector(".icon-action").disabled = !unified;
    prefillPlanAnchor(row);
    validatePlanAnchor(row, false);
  });
}

function inputDate(date) { return date.getFullYear() + "/" + (date.getMonth() + 1) + "/" + date.getDate(); }
function parseLocalDateTime(value, reference) {
  const match = /^(\d{4})\/(\d{1,2})\/(\d{1,2}) (\d{2}):(\d{2}):(\d{2})$/.exec(value.trim());
  if (!match) return null;
  const parts = match.slice(1).map(Number);
  const [year, month, day, hour, minute, second] = parts;
  // Keep the chosen occurrence when a clock change repeats a local hour.
  if (
    reference && inputDate(reference) === year + "/" + month + "/" + day && reference.getHours() === hour &&
    reference.getMinutes() === minute &&
    reference.getSeconds() === second
  )
    return reference;
  const date = new Date(year, month - 1, day, hour, minute, second);
  const actual = [date.getFullYear(), date.getMonth() + 1, date.getDate(), date.getHours(), date.getMinutes(), date.getSeconds()];
  return year >= 1970 && actual.every((part, index) => part === parts[index]) ? date : null;
}

function dateTimeValue(input) {
  return parseLocalDateTime(input.value, input.dataset.timestamp ? new Date(input.dataset.timestamp) : null);
}

function setDateTimeValue(input, date) {
  input.value = date
    ? inputDate(date) + " " + [date.getHours(), date.getMinutes(), date.getSeconds()].map((part) => String(part).padStart(2, "0")).join(":")
    : "";
  if (date) input.dataset.timestamp = date.toISOString(); else delete input.dataset.timestamp;
}

function suggestedCycleStart(seconds, now = Date.now()) {
  if (!seconds) return null;
  const period = seconds * 1000;
  const date = new Date(now);
  const days = seconds / 86400;
  if (days >= 365) date.setFullYear(date.getFullYear() + 1, 0, 1);
  else if (days >= 90) date.setMonth((Math.floor(date.getMonth() / 3) + 1) * 3, 1);
  else if (days >= 28) date.setMonth(date.getMonth() + 1, 1);
  else if (days >= 7) date.setDate(date.getDate() + ((8 - date.getDay()) % 7 || 7));
  else if (days >= 1) date.setDate(date.getDate() + 1);
  else {
    const start = localDay(date);
    return new Date(start + (Math.floor((now - start) / period) + 1) * period);
  }
  date.setHours(0, 0, 0, 0);
  // Calendar boundaries can exceed a fixed-length window across months or DST.
  return new Date(Math.min(date.getTime(), Math.floor((now + period) / 1000) * 1000));
}

function prefillPlanAnchor(row) {
  const input = row.querySelector('[name="window_anchor"]');
  if (input.disabled || (input.value && !input.dataset.automatic)) return;
  const date = suggestedCycleStart(planWindowPeriod(row));
  setDateTimeValue(input, date);
  if (date) input.dataset.automatic = "true"; else delete input.dataset.automatic;
}

function closeDateTimePickers() {
  document.querySelectorAll(".date-time-popover:popover-open").forEach((popover) => popover.hidePopover());
}

function dateTimeControl(input, getLimits) {
  input.addEventListener("input", () => {
    delete input.dataset.timestamp;
    delete input.dataset.automatic;
  });
  const popover = el("div", { class: "date-time-popover", popover: "auto", role: "dialog", "aria-label": m("ui.select_date_and_time") });
  const button = el(
    "button",
    {
      type: "button",
      class: "icon-action",
      "aria-label": m("ui.select_date_and_time"),
      "aria-haspopup": "dialog",
      "aria-expanded": "false",
      onclick: () => {
        if (popover.matches(":popover-open")) return popover.hidePopover();
        const limits = getLimits();
        if (!limits) return;
        const parsed = dateTimeValue(input);
        const date = parsed && parsed.getTime() > limits.min && parsed.getTime() <= limits.max ? parsed : limits.initial;
        let day = localDay(date), month = new Date(date.getFullYear(), date.getMonth(), 1);
        const calendar = el("div");
        const redraw = () => {
          const label = document.activeElement.getAttribute("aria-label") || document.activeElement.textContent;
          calendar.replaceChildren(
            renderMonthCalendar({
              month,
              start: day,
              min: localDay(new Date(limits.min)),
              max: localDay(new Date(limits.max)),
              onSelect: (value) => {
                day = value;
                redraw();
              },
              onMonthChange: (value) => {
                month = value;
                redraw();
              }
            })
          );
          const focused = Array.from(calendar.querySelectorAll("button:not(:disabled)")).find(
            (item) => (item.getAttribute("aria-label") || item.textContent) === label
          );
          (focused || calendar.querySelector('[aria-pressed="true"]'))?.focus({ preventScroll: true });
        };
        const clock = [date.getHours(), date.getMinutes(), date.getSeconds()].map((value, index) =>
          el("input", {
            type: "number",
            min: "0",
            max: index ? "59" : "23",
            step: "1",
            required: true,
            value: String(value).padStart(2, "0"),
            "aria-label": [m("ui.hour"), m("ui.minute"), m("ui.seconds")][index]
          })
        );
        const error = el("div", { class: "field-error small", role: "alert" });
        popover.replaceChildren(
          calendar,
          el(
            "div",
            { class: "date-time-clock" },
            clock.map((field, index) =>
              el("div", { class: "field" }, el("span", { text: [m("ui.hour"), m("ui.minute"), m("ui.seconds")][index] }), field)
            )
          ),
          error,
          el(
            "div",
            { class: "row picker-actions" },
            el("button", {
              type: "button",
              text: m("ui.cancel"),
              onclick: () => {
                popover.hidePopover();
                button.focus();
              }
            }),
            el("button", {
              type: "button",
              class: "primary",
              text: m("ui.confirm"),
              onclick: () => {
                for (const field of clock) field.removeAttribute("aria-invalid");
                const invalid = clock.find((field) => !field.validity.valid);
                if (invalid) {
                  invalid.setAttribute("aria-invalid", "true");
                  setText(error, m("ui.hours_must_be_0_23_minutes_and_seconds_must_be_0_59"));
                  invalid.focus();
                  return;
                }
                input.value = inputDate(new Date(day)) + " " + clock.map((field) => String(field.valueAsNumber).padStart(2, "0")).join(":");
                delete input.dataset.automatic;
                delete input.dataset.timestamp;
                const selected = parseLocalDateTime(input.value, date);
                if (selected) setDateTimeValue(input, selected);
                input.dispatchEvent(new Event("change", { bubbles: true }));
                popover.hidePopover();
                input.focus();
              }
            })
          )
        );
        redraw();
        popover.showPopover();
        if (matchMedia("(max-width: 640px)").matches) popover.style.cssText = "";
        else positionAnchoredPopover(popover, input, { align: "end" });
        calendar.querySelector('[aria-pressed="true"]')?.focus({ preventScroll: true });
      }
    },
    actionIcon("clock")
  );
  popover.addEventListener("toggle", (event) => button.setAttribute("aria-expanded", String(event.newState === "open")));
  popover.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      popover.hidePopover();
      button.focus();
    }
  });
  return el("div", { class: "date-time-control" }, input, button, popover);
}

function planWindowPeriod(row) {
  const value = row.querySelector('[name="window_period"]').value;
  const seconds = Number(value) * Number(row.querySelector('[name="window_unit"]').value);
  return /^[1-9][0-9]*$/.test(value) && Number.isInteger(seconds) && seconds <= 9223372036 ? seconds : 0;
}

function validatePlanPeriod(row) {
  const seconds = planWindowPeriod(row);
  let message = "";
  if (!seconds) message = m("ui.the_period_must_be_a_positive_integer");
  else if (Array.from($("plan-windows").children).some((other) => other !== row && planWindowPeriod(other) === seconds))
    message = m("ui.another_window_has_the_same_period");
  setPlanFieldError(row.querySelector('[name="window_period"]'), message);
  return !message;
}

function setPlanFieldError(input, message) {
  if (message) input.setAttribute("aria-invalid", "true"); else input.removeAttribute("aria-invalid");
  setText(input.closest(".field").querySelector(":scope > .field-error"), message);
}

function validatePlanAnchor(row, required = true) {
  const input = row.querySelector('[name="window_anchor"]');
  let message = "", anchor;
  if (!input.disabled && (required || input.value.trim())) {
    const seconds = planWindowPeriod(row);
    if (row.dataset.anchor && seconds === Number(row.dataset.period) && input.value === input.defaultValue) {
      anchor = row.dataset.anchor;
    } else {
      const date = dateTimeValue(input);
      const now = Date.now();
      if (!date) message = m("ui.enter_a_valid_time_yyyy_mm_dd_hh_mm_ss");
      else if (!seconds) message = m("ui.set_a_valid_reset_period_first");
      else if (date.getTime() <= now) message = m("ui.the_next_cycle_must_start_after_the_current_time");
      else if (date.getTime() > now + seconds * 1000) message = m("ui.the_next_cycle_must_start_within_one_period_from_now");
      else anchor = date.toISOString().replace(".000Z", "Z");
    }
  }
  setPlanFieldError(input, message);
  return anchor;
}

function addPlanWindow(quotaWindow = {}) {
  const seconds = quotaWindow.period_seconds;
  const unit = seconds ? PERIOD_UNITS.find(([value]) => seconds % value === 0)[0] : 3600;
  const row = el("div", {
    class: "plan-window",
    "data-id": quotaWindow.id || "",
    "data-anchor": quotaWindow.cycle_anchor_at || "",
    "data-period": String(seconds || "")
  });
  const field = (label, input, className = "") =>
    el("label", { class: "field " + className }, label, input, el("span", { class: "field-error small" }));
  const anchorInput = el("input", {
    name: "window_anchor",
    type: "text",
    placeholder: "yyyy/MM/dd HH:mm:ss",
    autocomplete: "off",
    spellcheck: false,
    "data-tip": Intl.DateTimeFormat().resolvedOptions().timeZone,
    onchange: () => validatePlanAnchor(row, false),
    onblur: () => {
      prefillPlanAnchor(row);
      validatePlanAnchor(row, false);
    }
  });
  setDateTimeValue(anchorInput, quotaWindow.cycle_anchor_at ? nextCycleStart(quotaWindow) : null);
  anchorInput.defaultValue = anchorInput.value;
  const scheduleField = field(
    m("ui.next_cycle_start"),
    dateTimeControl(anchorInput, () => {
      const seconds = planWindowPeriod(row);
      if (!validatePlanPeriod(row)) {
        row.querySelector('[name="window_period"]').focus();
        return null;
      }
      const min = Date.now();
      return { min, max: min + seconds * 1000, initial: suggestedCycleStart(seconds, min) };
    }),
    "plan-window-schedule"
  );
  row.append(
    field(m("ui.window_name"), el("input", { name: "window_name", value: quotaWindow.name || "", required: true })),
    field(
      m("ui.reset_period"),
      el(
        "div",
        { class: "period-input" },
        el("input", {
          name: "window_period",
          inputmode: "numeric",
          pattern: "[1-9][0-9]*",
          value: seconds ? seconds / unit : "",
          required: true,
          oninput: (event) => {
            const input = event.currentTarget;
            const cursor = input.value.slice(0, input.selectionStart).replace(/\D/g, "").replace(/^0+/, "").length;
            const value = input.value.replace(/\D/g, "").replace(/^0+/, "");
            if (input.value !== value) {
              input.value = value;
              input.setSelectionRange(cursor, cursor);
            }
          }
        }),
        el(
          "span",
          { class: "native-select" },
          el(
            "select",
            { name: "window_unit", "aria-label": m("ui.period_unit") },
            PERIOD_UNITS.map(([value, label]) => el("option", { value: String(value), selected: value === unit, text: label }))
          )
        )
      )
    ),
    scheduleField,
    el(
      "button",
      {
        type: "button",
        class: "icon-action window-delete",
        "aria-label": m("ui.delete_window"),
        onclick: () => {
          row.remove();
          syncPlanWindows();
        }
      },
      actionIcon("x")
    )
  );
  row.addEventListener("input", (event) => {
    if (["window_period", "window_unit"].includes(event.target.name)) syncPlanWindows();
    else if (event.target.name === "window_anchor") validatePlanAnchor(row, false);
  });
  row.append(
    el(
      "div",
      { class: "plan-window-limits" },
      QUOTA_DIMENSIONS.map((metric) => {
        const input = el("input", {
          name: metric.key,
          "aria-label": metric.inputLabel,
          type: "number",
          min: "0",
          step: metric.key === "amount" ? "any" : "1",
          value: quotaWindow[metric.key] || "",
          placeholder: m("ui.unlimited")
        });
        return field(metric.inputLabel, input);
      })
    )
  );
  $("plan-windows").append(row);
  syncPlanWindows();
  return row;
}

function renderPlanKeyList(planID, data) {
  const list = $("plan-key-list");
  const keys = data.keys.filter((key) => !key.deleted_at || (planID && key.plan_id === planID));
  if (!keys.length) {
    list.replaceChildren(el("div", { class: "empty", text: m("ui.no_api_keys") }));
    return;
  }
  list.replaceChildren(
    ...keys.map((key) => {
      const ownedByOther = !!key.plan_id && key.plan_id !== planID;
      const owner = ownedByOther ? data.plans.find((plan) => plan.id === key.plan_id) : null;
      return el(
        "label",
        { class: ownedByOther ? "disabled" : "" },
        checkboxControl({
          input: el("input", { type: "checkbox", value: key.scope, checked: key.plan_id === planID && !!planID, disabled: ownedByOther })
        }),
        renderAPIKeyIdentity(key),
        key.deleted_at
          ? el("span", { class: "tag plain", text: m("ui.deleted"), "data-tip": m("ui.uncheck_to_remove_the_binding") })
          : null,
        owner ? el("span", { class: "muted small", text: m("ui.bound_to_value", { v0: owner.name || owner.id }) }) : null
      );
    })
  );
}

function editPlan(plan) {
  const id = plan?.id;
  openEditor("plan-dialog", "plan", (data) => {
    const current = id ? data.plans.find((entry) => entry.id === id) : null;
    if (id && !current) throw new UIError(m("ui.this_subscription_plan_no_longer_exists_close_the_dialog_and_refresh_the_list"));
    setText($("plan-dialog-title"), current ? m("ui.edit_plan_value", { v0: current.name || id }) : m("ui.create_plan"));
    $("plan-id").value = id || "";
    setText($("plan-save"), current ? m("ui.save") : m("ui.create"));
    $("plan-name").value = current?.name || "";
    $("plan-currency").value = current?.currency || "USD";
    $("plan-cycle-mode").value = current?.windows[0]?.cycle_anchor_at ? "unified" : "independent";
    $("plan-windows").replaceChildren();
    (current ? current.windows : [{}]).forEach(addPlanWindow);
    renderPlanKeyList(id || "", data);
    return {
      plan: current ? { ...structuredClone(current), windows: planFromForm().windows } : null,
      scopes: selectedKeyScopes("plan-key-list")
    };
  });
}

function planFromForm() {
  let firstInvalid = null;
  const names = new Set();
  const invalid = (input, message) => {
    setPlanFieldError(input, message);
    firstInvalid ||= input;
  };
  const windows = Array.from($("plan-windows").children, (row) => {
    prefillPlanAnchor(row);
    row.querySelectorAll("[aria-invalid]").forEach((input) => input.removeAttribute("aria-invalid"));
    row.querySelectorAll(".field-error").forEach((error) => setText(error, ""));
    const nameInput = row.querySelector('[name="window_name"]');
    const limits = {};
    const periodInput = row.querySelector('[name="window_period"]');
    const name = nameInput.value.trim();

    const seconds = planWindowPeriod(row);
    if (!name || new TextEncoder().encode(name).length > 128)
      invalid(nameInput, m("ui.the_name_is_required_and_must_not_exceed_128_bytes"));
    else if (names.has(name.toLowerCase())) invalid(nameInput, m("ui.duplicate_window_name"));
    names.add(name.toLowerCase());
    for (const metric of QUOTA_DIMENSIONS) {
      const input = row.querySelector('[name="' + metric.key + '"]');
      const value = input.value === "" && !input.validity.badInput ? 0 : input.valueAsNumber;
      if (!Number.isFinite(value) || value < 0) invalid(input, m("ui.enter_a_valid_quota"));
      else if (metric.key !== "amount" && !Number.isSafeInteger(value)) invalid(input, m("ui.enter_a_valid_integer"));
      limits[metric.key] = value;
    }
    if (Object.values(limits).every((value) => value === 0)) invalid(row.querySelector('[name="amount"]'), m("ui.set_at_least_one_quota"));
    if (!validatePlanPeriod(row)) firstInvalid ||= periodInput;
    const anchor = validatePlanAnchor(row);
    const anchorInput = row.querySelector('[name="window_anchor"]');
    if (anchorInput.hasAttribute("aria-invalid")) firstInvalid ||= anchorInput;
    return {
      ...(row.dataset.id ? { id: row.dataset.id } : {}),
      name,
      ...limits,
      period_seconds: seconds,
      ...(anchor ? { cycle_anchor_at: anchor } : {})
    };
  });
  if (firstInvalid) {
    firstInvalid.scrollIntoView({ block: "center" });
    firstInvalid.focus();
    return null;
  }
  return { name: $("plan-name").value.trim(), currency: $("plan-currency").value, windows, scopes: selectedKeyScopes("plan-key-list") };
}

$("plan-dialog").addEventListener("close", closeDateTimePickers);
$("plan-dialog").addEventListener("scroll", closeDateTimePickers);
addEventListener("resize", closeDateTimePickers);
$("plan-currency").replaceChildren(...currencyOptions());
$("plan-cycle-mode").onchange = syncPlanWindows;
$("plan-window-add").onclick = () => addPlanWindow().querySelector("input").focus();
$("plan-new").onclick = () => editPlan();
$("plan-save").onclick = () =>
  submitEditor("plan-dialog", async (editor) => {
    const plan = planFromForm();
    if (!plan) return false;
    const id = $("plan-id").value;
    const existing = editor.initial.plan;
    const affected = (existing?.windows || []).filter(
      (old) => !plan.windows.some((quotaWindow) => quotaWindow.id === old.id && sameWindowSchedule(quotaWindow, old))
    );
    if (
      affected.length &&
      !(await openActionDialog(
        m("ui.update_quota_cycles"),
        m("ui.usage_will_be_cleared_for_these_windows_and_their_new_schedules_will_take_effect_valu", {
          v0: affected.map((quotaWindow) => quotaWindow.name).join(", ")
        })
      ))
    )
      return false;
    if (!editorCurrent(editor)) return false;
    if (id && sameScopes(plan.scopes, editor.initial.scopes)) delete plan.scopes;
    if (id && plan.name === existing.name) delete plan.name;
    if (id && plan.currency === existing.currency) delete plan.currency;
    if (id && JSON.stringify(plan.windows) === JSON.stringify(existing.windows)) delete plan.windows;
    if (id && !Object.keys(plan).length) return;
    if (id) await mutateAdmin("PATCH", "/plans", { id, ...plan }); else await mutateAdmin("POST", "/plans", plan);
    notify(id ? m("ui.subscription_plan_saved") : m("ui.subscription_plan_created"));
  });
