function planType(plan) { return plan?.shared_quota ? "shared" : plan?.windows[0]?.cycle_anchor_at ? "unified" : "independent"; }

function planTypeLabel(type) {
  return m({ independent: "ui.independent_cycles", unified: "ui.shared_cycles", shared: "ui.shared_quota" }[type]);
}

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
      const windows = plan.shared_quota
        ? [settingsEntryField(m("ui.remaining_subscription_quota"), renderQuotaWindows(plan.quota))]
        : plan.windows.map((window) => {
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
        [settingsEntryField(m("ui.plan_type"), el("span", { text: planTypeLabel(planType(plan)) })), ...windows, boundKeyField(boundKeys)],
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
          ),
        plan.shared_quota
          ? [
              el("button", {
                class: "link",
                text: m("ui.reset"),
                onclick: () =>
                  openActionDialog(m("ui.reset_shared_quota"), m("ui.reset_shared_quota_note"), undefined, async () => {
                    await mutateAdmin("POST", "/plans/reset", { id: plan.id });
                    notify(m("ui.subscription_quota_reset"));
                  })
              })
            ]
          : []
      );
    }
  });
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
  const unified = $("plan-type").value !== "independent";
  setText($("plan-type-note"), m("ui.plan_type_" + $("plan-type").value + "_note"));
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
  setText(input.closest(".field").querySelector(":scope > .field-control > .field-error"), message);
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
  const field = (label, control, className = "") =>
    el(
      "label",
      { class: "field " + className },
      label,
      el("span", { class: "field-control" }, control, el("span", { class: "field-error small" }))
    );
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
          "ui-select",
          { name: "window_unit", "aria-label": m("ui.period_unit") },
          PERIOD_UNITS.map(([value, label]) => el("option", { value: String(value), selected: value === unit, text: label }))
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
    $("plan-type").value = planType(current);
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
  return {
    name: $("plan-name").value.trim(),
    currency: $("plan-currency").value,
    shared_quota: $("plan-type").value === "shared",
    windows,
    scopes: selectedKeyScopes("plan-key-list")
  };
}

$("plan-dialog").addEventListener("close", closeDateTimePickers);
$("plan-dialog").addEventListener("scroll", closeDateTimePickers);
$("plan-currency").replaceChildren(...currencyOptions());
$("plan-type").onchange = syncPlanWindows;
$("plan-window-add").onclick = () => addPlanWindow().querySelector("input").focus();
$("plan-new").onclick = () => editPlan();
$("plan-save").onclick = () =>
  submitEditor("plan-dialog", async (editor) => {
    const plan = planFromForm();
    if (!plan) return false;
    const id = $("plan-id").value;
    const existing = editor.initial.plan;
    const affected = (existing?.windows || []).filter(
      (old) =>
        !!existing.shared_quota !== plan.shared_quota ||
        !plan.windows.some((quotaWindow) => quotaWindow.id === old.id && sameWindowSchedule(quotaWindow, old))
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
    if (id && plan.shared_quota === !!existing.shared_quota) delete plan.shared_quota;
    if (id && JSON.stringify(plan.windows) === JSON.stringify(existing.windows)) delete plan.windows;
    if (id && !Object.keys(plan).length) return;
    if (id) await mutateAdmin("PATCH", "/plans", { id, ...plan }); else await mutateAdmin("POST", "/plans", plan);
    notify(id ? m("ui.subscription_plan_saved") : m("ui.subscription_plan_created"));
  });
