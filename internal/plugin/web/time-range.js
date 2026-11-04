const SHARED_TIME_LABELS = {
  hour: m("ui.hours"),
  day: m("ui.days"),
  today: m("ui.today"),
  yesterday: m("ui.yesterday"),
  custom: m("ui.custom")
};
const sharedTimeDrafts = { admin: null, account: null };
const sharedTimeChoices = { admin: { hour: 8, day: 7, custom: null }, account: { hour: 8, day: 7, custom: null } };

const sharedTimeRanges = { admin: { mode: "30d" }, account: { mode: "30d" } };

function recentTimeRange(mode) {
  const to = new Date();
  const value = Number.parseInt(mode, 10);
  const duration = value * (mode.endsWith("h") ? 3600000 : 86400000);
  return { from: new Date(to.getTime() - duration).toISOString(), to: to.toISOString() };
}

function naturalTimeRange(mode) {
  const now = new Date();
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  if (mode === "yesterday") {
    const from = new Date(today);
    from.setDate(from.getDate() - 1);
    return { from: from.toISOString(), to: today.toISOString() };
  }
  return { from: today.toISOString(), to: now.toISOString() };
}

function selectedRange(account) {
  const range = sharedTimeRanges[account ? "account" : "admin"];
  if (range.mode === "custom") { return { from: range.from, to: range.to }; }
  if (range.mode === "today" || range.mode === "yesterday") { return naturalTimeRange(range.mode); }
  return recentTimeRange(range.mode);
}

function storedTimeRange(role) {
  const range = viewPreference(role, "time-range", { mode: "30d" });
  if (!range || typeof range.mode !== "string" || !(rollingTimeMode(range.mode) || ["today", "yesterday", "custom"].includes(range.mode)))
    return { mode: "30d" };
  if (range.mode !== "custom") return { mode: range.mode };
  if (!["day", "hour"].includes(range.unit) || typeof range.from !== "string" || typeof range.to !== "string") return { mode: "30d" };
  const from = new Date(range.from);
  const to = new Date(range.to);
  if (
    !Number.isFinite(from.getTime()) || !Number.isFinite(to.getTime()) || from >= to ||
    (range.unit === "day"
      ? localDay(new Date(to.getTime() - 1)) > shiftLocalDay(localDay(from), 364)
      : to.getTime() - from.getTime() > 365 * 86400000)
  ) { return { mode: "30d" }; }
  return { mode: "custom", unit: range.unit, from: from.toISOString(), to: to.toISOString() };
}

function sharedTimeID(account, suffix) { return (account ? "account-shared" : "shared") + "-time-" + suffix; }

function rollingTimeMode(mode) {
  const match = /^(\d+)([hd])$/.exec(mode || "");
  if (!match) return null;
  const value = Number(match[1]);
  const unit = match[2] === "h" ? "hour" : "day";
  return value >= (unit === "hour" ? 5 : 1) && value <= (unit === "hour" ? 24 : 30) ? { unit, value } : null;
}

function timeRangeLabel(range) {
  const rolling = rollingTimeMode(range.mode);
  if (rolling) return m("time.last_" + rolling.unit, { count: rolling.value });
  if (range.mode !== "custom") return SHARED_TIME_LABELS[range.mode];
  const options = { year: "numeric", month: "2-digit", day: "2-digit" };
  if (range.unit !== "day") Object.assign(options, { hour: "2-digit", minute: "2-digit", hour12: false });
  const format = (value) => new Date(value).toLocaleString(locale(), options);
  const end = new Date(new Date(range.to).getTime() - 1);
  if (range.unit === "hour") end.setMinutes(0, 0, 0);
  return format(range.from) + " – " + format(end);
}

function updateSharedTimeControl(account, range = sharedTimeRanges[account ? "account" : "admin"]) {
  const label = timeRangeLabel(range);
  setText($(sharedTimeID(account, "label")), label);
  const current = $(sharedTimeID(account, "popover")).querySelector(".time-range-current");
  if (current) setText(current, label);
}

function closeSharedTimePopover(account, restoreFocus = false) {
  const popover = $(sharedTimeID(account, "popover"));
  if (popover.classList.contains("hidden")) return;
  popover.classList.add("hidden");
  $(sharedTimeID(account, "backdrop")).classList.add("hidden");
  $(sharedTimeID(account, "trigger")).setAttribute("aria-expanded", "false");
  sharedTimeDrafts[account ? "account" : "admin"] = null;
  updateSharedTimeControl(account);
  if (restoreFocus) $(sharedTimeID(account, "trigger")).focus();
}

function positionSharedTimePopover(account) {
  const popover = $(sharedTimeID(account, "popover"));
  if (popover.classList.contains("hidden")) return;
  if (!matchMedia("(max-width: 768px)").matches) {
    popover.style.cssText = "";
    return;
  }
  positionAnchoredPopover(popover, $(sharedTimeID(account, "trigger")), { align: "end" });
}

function defaultCustomTimeRange(draft, unit) {
  return unit === "hour"
    ? { unit, start: draft.hours[16], end: draft.hours[23] }
    : { unit, start: shiftLocalDay(draft.today, -6), end: draft.today };
}

function renderCustomTimeRange(account, draft) {
  const custom = draft.custom;
  const content = el("div", { class: "time-range-custom" });
  const redraw = () => {
    const popover = $(sharedTimeID(account, "popover"));
    const focused = document.activeElement;
    const label = focused.getAttribute("aria-label") || focused.textContent;
    const matchingButtons = () =>
      Array.from(popover.querySelectorAll("button:not(:disabled)")).filter(
        (button) => (button.getAttribute("aria-label") || button.textContent) === label
      );
    const index = matchingButtons().indexOf(focused);
    renderSharedTimePopover(account);
    const next = matchingButtons()[index];
    (next || popover.querySelector(".time-range-modes .active")).focus({ preventScroll: true });
  };
  const format = (value) =>
    new Date(value).toLocaleString(
      locale(),
      custom.unit === "day"
        ? { year: "numeric", month: "short", day: "numeric" }
        : { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false }
    );
  const pickerCancel = () => {
    draft.custom = draft.snapshot;
    draft.view = "summary";
    redraw();
  };
  const apply = () => {
    const to = Math.min(Date.now(), custom.unit === "day" ? shiftLocalDay(custom.end, 1) : custom.end + 3600000);
    guard(() =>
      applyTimeRange(account, {
        mode: "custom",
        unit: custom.unit,
        from: new Date(custom.start).toISOString(),
        to: new Date(to).toISOString()
      })
    );
  };
  if (draft.view === "summary") {
    content.append(
      el(
        "div",
        { class: "time-range-options time-range-units", role: "group", "aria-label": m("ui.custom_range_unit") },
        ["hour", "day"].map((unit) =>
          el("button", {
            type: "button",
            class: unit === custom.unit ? "active" : "",
            "aria-pressed": String(unit === custom.unit),
            text: unit === "hour" ? m("ui.hours") : m("ui.days"),
            onclick: () => {
              if (unit === custom.unit) return;
              draft.custom = defaultCustomTimeRange(draft, unit);
              redraw();
            }
          })
        )
      )
    );
  } else {
    content.append(
      el(
        "div",
        { class: "row spread" },
        el("button", { type: "button", class: "link small", text: m("ui.back"), onclick: pickerCancel }),
        el("span", { class: "small", text: custom.unit === "day" ? m("ui.select_date") : m("ui.select_hour") }),
        el("span", { class: "small muted", text: Intl.DateTimeFormat().resolvedOptions().timeZone })
      )
    );
  }
  const endpoints = el(
    "div",
    { class: "time-range-options time-range-endpoints" + (draft.view === "summary" ? " summary" : "") },
    ["start", "end"].map((endpoint) =>
      el(
        "button",
        {
          type: "button",
          class: draft.view !== "summary" && draft.endpoint === endpoint ? "active" : "",
          "aria-label": endpoint === "start" ? m("ui.select_start_time") : m("ui.select_end_time"),
          onclick: () => {
            if (draft.view === "summary") draft.snapshot = { ...custom };
            draft.endpoint = endpoint;
            draft.view = custom.unit;
            draft.month = new Date(custom[endpoint]);
            draft.month.setDate(1);
            redraw();
          }
        },
        el("span", { class: "small muted", text: endpoint === "start" ? m("ui.start_time") : m("ui.end_time") }),
        el("span", { text: format(custom[endpoint]) }),
        draft.view === "summary"
          ? el("span", {
              class: "small muted",
              text: endpoint === "end" && custom.start === custom.end ? m("ui.same_as_start_time") : m("ui.click_to_edit")
            })
          : null
      )
    )
  );
  if (draft.view === "summary")
    endpoints.insertBefore(el("span", { class: "muted", "aria-hidden": "true", text: "→" }), endpoints.lastChild);
  content.append(endpoints);
  if (custom.unit === "day") content.append(el("div", { class: "small muted", text: m("ui.select_dates_within_the_last_365_days") }));
  if (draft.view === "day") {
    content.append(
      renderMonthCalendar({
        month: draft.month,
        start: custom.start,
        end: custom.end,
        min: draft.firstDay,
        max: draft.today,
        onMonthChange: (month) => {
          draft.month = month;
          redraw();
        },
        onSelect: (day) => {
          if (draft.endpoint === "start") {
            custom.start = day;
            custom.end = Math.max(custom.end, day);
            draft.endpoint = "end";
            draft.month = new Date(custom.end);
            draft.month.setDate(1);
          } else {
            custom.end = day;
            custom.start = Math.min(custom.start, day);
          }
          redraw();
        }
      })
    );
  } else if (draft.view === "hour") {
    content.append(
      el(
        "div",
        { class: "time-range-hours" },
        ["start", "end"].map((endpoint) =>
          el(
            "div",
            {},
            el("div", { class: "small muted", text: endpoint === "start" ? m("ui.start_time") : m("ui.end_time") }),
            el(
              "div",
              { class: "time-range-hour-list time-range-options" },
              draft.hours.map((hour, index) =>
                el(
                  "button",
                  {
                    type: "button",
                    class: custom[endpoint] === hour ? "active" : "",
                    "aria-pressed": String(custom[endpoint] === hour),
                    disabled: endpoint === "start" ? index > 19 : hour < custom.start + 4 * 3600000,
                    onclick: () => {
                      custom[endpoint] = hour;
                      if (endpoint === "start") custom.end = Math.max(custom.end, hour + 4 * 3600000);
                      redraw();
                    }
                  },
                  el("span", { text: window.billingI18n.date(hour, { month: "short", day: "numeric" }) }),
                  el("span", {
                    text:
                      endpoint === "end" && index === 23
                        ? m("ui.now")
                        : window.billingI18n.date(hour, { hour: "2-digit", minute: "2-digit", hour12: false })
                  })
                )
              )
            )
          )
        )
      )
    );
  }
  const count = custom.unit === "hour" ? (custom.end - custom.start) / 3600000 + 1 : Math.round((custom.end - custom.start) / 86400000) + 1;
  if (draft.view === "summary") {
    content.append(
      el(
        "div",
        { class: "row spread small muted" },
        el("span", { text: m("time.total_" + custom.unit, { count }) }),
        el("span", { text: Intl.DateTimeFormat().resolvedOptions().timeZone })
      )
    );
  }
  content.append(
    el(
      "div",
      { class: "row spread" },
      draft.view === "summary" ? null : el("span", { class: "small muted", text: format(custom.start) + " – " + format(custom.end) }),
      el(
        "div",
        { class: "row picker-actions" },
        el("button", {
          type: "button",
          text: m("ui.cancel"),
          onclick: draft.view === "summary" ? () => closeSharedTimePopover(account, true) : pickerCancel
        }),
        el("button", { type: "button", class: "primary", text: m("ui.apply"), onclick: apply })
      )
    )
  );
  return content;
}

function renderSharedTimePopover(account) {
  const role = account ? "account" : "admin";
  const draft = sharedTimeDrafts[role];
  const popover = $(sharedTimeID(account, "popover"));
  popover.replaceChildren(
    el(
      "div",
      { class: "row spread time-range-heading" },
      el("span", { text: m("ui.time_range") }),
      el("span", { class: "time-range-current", text: timeRangeLabel(sharedTimeRanges[role]) })
    ),
    el(
      "div",
      { class: "time-range-options time-range-modes", role: "group", "aria-label": m("ui.time_range_mode") },
      Object.entries(SHARED_TIME_LABELS).map(([mode, label]) =>
        el("button", {
          type: "button",
          text: label,
          "aria-pressed": String(mode === draft.mode),
          class: mode === draft.mode ? "active" : "",
          onclick: () => {
            if (mode === "custom") resetCustomTimeDraft(account, draft);
            draft.mode = mode;
            draft.view = "summary";
            renderSharedTimePopover(account);
            popover.querySelector(".time-range-modes .active").focus();
            if (mode !== "custom")
              guard(() =>
                applyTimeRange(
                  account,
                  { mode: mode === "hour" || mode === "day" ? draft[mode] + (mode === "hour" ? "h" : "d") : mode },
                  false
                )
              );
          }
        })
      )
    )
  );
  if (draft.mode === "custom") {
    popover.append(renderCustomTimeRange(account, draft));
    for (const list of popover.querySelectorAll(".time-range-hour-list")) {
      const selected = list.querySelector(".active");
      if (selected) list.scrollTop = Math.max(0, selected.offsetTop - (list.clientHeight - selected.clientHeight) / 2);
    }
  } else if (draft.mode === "hour" || draft.mode === "day") {
    const unit = draft.mode;
    const min = unit === "hour" ? 5 : 1, max = unit === "hour" ? 24 : 30;
    const duration = () => m("time.duration_" + unit, { count: draft[unit] });
    const value = el("b", { text: duration() });
    let activePointer = null;
    const commit = () => {
      activePointer = null;
      if (sharedTimeDrafts[role] === draft && draft.mode === unit)
        guard(() => applyTimeRange(account, { mode: draft[unit] + (unit === "hour" ? "h" : "d") }, false));
    };
    const finishPointer = (event) => { if (activePointer === event.pointerId) commit(); };
    const slider = el("input", {
      type: "range",
      class: "range-input",
      min,
      max,
      step: 1,
      value: draft[unit],
      style: "--range-progress:" + ((draft[unit] - min) / (max - min)) * 100 + "%",
      "aria-label": m("ui.recent_time_range"),
      oninput: (event) => {
        draft[unit] = Number(event.target.value);
        setText(value, duration());
        updateSharedTimeControl(account, { mode: draft[unit] + (unit === "hour" ? "h" : "d") });
        event.currentTarget.style.setProperty("--range-progress", ((draft[unit] - min) / (max - min)) * 100 + "%");
      },
      onpointerdown: (event) => {
        if (activePointer !== null && activePointer !== event.pointerId) return event.preventDefault();
        activePointer = event.pointerId;
        event.currentTarget.setPointerCapture(event.pointerId);
      },
      onpointerup: finishPointer,
      onpointercancel: finishPointer,
      onkeyup: (event) => { if (/^(Arrow(Left|Right|Up|Down)|Home|End|PageUp|PageDown)$/.test(event.key)) commit(); },
      onblur: commit
    });
    const ticks = unit === "hour" ? [5, 8, 12, 18, 24] : [1, 7, 14, 21, 30];
    popover.append(
      el(
        "div",
        { class: "time-range-rolling" },
        el("div", { class: "row spread small" }, el("span", { class: "muted", text: m("ui.recent_time_range") }), value),
        el(
          "div",
          { class: "range-control" },
          slider,
          el(
            "div",
            { class: "range-ticks small muted", "aria-hidden": "true" },
            ticks.map((tick) => el("span", { text: String(tick), style: "left:" + ((tick - min) / (max - min)) * 100 + "%" }))
          )
        )
      )
    );
  } else {
    popover.append(
      el(
        "div",
        { class: "time-range-rolling" },
        el("span", { class: "small", text: SHARED_TIME_LABELS[draft.mode] }),
        el("span", { class: "small muted", text: draft.mode === "today" ? m("ui.00_00_now") : "00:00 → 24:00" })
      )
    );
  }
  positionSharedTimePopover(account);
}

function resetCustomTimeDraft(account, draft) {
  const range = sharedTimeChoices[account ? "account" : "admin"].custom;
  const now = new Date();
  const hour = new Date(now);
  hour.setMinutes(0, 0, 0);
  draft.today = localDay(now);
  draft.firstDay = shiftLocalDay(draft.today, -364);
  draft.hours = Array.from({ length: 24 }, (_, i) => hour.getTime() - (23 - i) * 3600000);
  draft.custom = defaultCustomTimeRange(draft, range?.unit === "hour" ? "hour" : "day");
  if (!range) return;
  const start = new Date(range.from).getTime(), end = new Date(range.to).getTime() - 1;
  if (range.unit === "hour") {
    const first = draft.hours.find((value) => value >= start);
    const last = draft.hours.findLast((value) => value <= end);
    if (first !== undefined && last - first >= 4 * 3600000) draft.custom = { unit: "hour", start: first, end: last };
  } else if (end >= draft.firstDay && start <= now.getTime()) {
    draft.custom = {
      unit: "day",
      start: Math.max(draft.firstDay, localDay(new Date(start))),
      end: Math.min(draft.today, localDay(new Date(end)))
    };
  } else { draft.custom = { unit: "day", start: draft.today, end: draft.today }; }
}

function openSharedTimePopover(account) {
  const popover = $(sharedTimeID(account, "popover"));
  if (!popover.classList.contains("hidden")) return closeSharedTimePopover(account, true);
  closeSharedTimePopover(!account);
  const role = account ? "account" : "admin";
  const range = sharedTimeRanges[role];
  const rolling = rollingTimeMode(range.mode);
  const draft = { ...sharedTimeChoices[role], mode: rolling?.unit || range.mode, view: "summary", endpoint: "start" };
  if (rolling) draft[rolling.unit] = rolling.value;
  resetCustomTimeDraft(account, draft);
  sharedTimeDrafts[role] = draft;
  renderSharedTimePopover(account);
  popover.classList.remove("hidden");
  $(sharedTimeID(account, "backdrop")).classList.remove("hidden");
  $(sharedTimeID(account, "trigger")).setAttribute("aria-expanded", "true");
  positionSharedTimePopover(account);
  popover.querySelector(".time-range-modes .active").focus();
}

function applyTimeRange(account, range, close = true) {
  const role = account ? "account" : "admin";
  const previous = sharedTimeRanges[role];
  sharedTimeRanges[role] = range;
  const rolling = rollingTimeMode(range.mode);
  if (rolling) sharedTimeChoices[role][rolling.unit] = rolling.value;
  if (range.mode === "custom") sharedTimeChoices[role].custom = range;
  saveViewPreference(role, "time-range", range);
  updateSharedTimeControl(account);
  if (close) closeSharedTimePopover(account, true);
  if (JSON.stringify(previous) !== JSON.stringify(range)) { return loadPage(account); }
}

function bindSharedTimeControls(account) {
  $(sharedTimeID(account, "trigger")).onclick = () => openSharedTimePopover(account);
  $(sharedTimeID(account, "backdrop")).onclick = () => closeSharedTimePopover(account, true);
  $(sharedTimeID(account, "popover")).onkeydown = (event) => {
    if (event.key !== "Tab") return;
    const controls = Array.from(event.currentTarget.querySelectorAll("button:not(:disabled), input:not(:disabled)"));
    const target = event.shiftKey ? controls.at(-1) : controls[0];
    if (
      !event.currentTarget.contains(document.activeElement) || document.activeElement === (event.shiftKey ? controls[0] : controls.at(-1))
    ) {
      event.preventDefault();
      target.focus();
    }
  };
}

addEventListener(
  "pointerdown",
  (event) => {
    for (const account of [false, true]) {
      const popover = $(sharedTimeID(account, "popover"));
      if (
        !popover.classList.contains("hidden") && !popover.contains(event.target) &&
        !$(sharedTimeID(account, "trigger")).contains(event.target)
      ) { closeSharedTimePopover(account); }
    }
  },
  true
);
addEventListener("keydown", (event) => {
  if (event.key !== "Escape") return;
  closeSharedTimePopover(false, true);
  closeSharedTimePopover(true, true);
});
matchMedia("(max-width: 768px)").addEventListener("change", () => {
  closeSharedTimePopover(false);
  closeSharedTimePopover(true);
});
