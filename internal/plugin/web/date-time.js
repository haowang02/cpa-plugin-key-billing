function localDay(date) { return new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime(); }

function shiftLocalDay(value, days) {
  const date = new Date(value);
  date.setDate(date.getDate() + days);
  return date.getTime();
}

function renderMonthCalendar({ month, start, end = start, min, max, onSelect, onMonthChange }) {
  const monthStart = new Date(month.getFullYear(), month.getMonth(), 1);
  const nextMonth = new Date(month.getFullYear(), month.getMonth() + 1, 1);
  const grid = el("div", { class: "calendar-grid" });
  for (const name of [m("ui.sun"), m("ui.mon"), m("ui.tue"), m("ui.wed"), m("ui.thu"), m("ui.fri"), m("ui.sat")])
    grid.append(el("span", { class: "small muted", text: name }));
  const first = shiftLocalDay(monthStart.getTime(), -monthStart.getDay());
  for (let i = 0; i < 42; i++) {
    const day = shiftLocalDay(first, i);
    const selected = day === start || day === end;
    grid.append(
      el("button", {
        type: "button",
        "data-calendar-day": String(day),
        class: [
          selected ? "active" : "",
          day === start ? "range-start" : "",
          day === end ? "range-end" : "",
          day >= start && day <= end ? "in-range" : "",
          new Date(day).getMonth() !== month.getMonth() ? "outside-month" : ""
        ]
          .filter(Boolean)
          .join(" "),
        disabled: day < min || day > max,
        "aria-label": window.billingI18n.date(day),
        "aria-pressed": String(selected),
        text: String(new Date(day).getDate()),
        onclick: () => onSelect(day)
      })
    );
  }
  return el(
    "div",
    { class: "calendar-picker" },
    el(
      "div",
      { class: "row spread" },
      el("button", {
        type: "button",
        class: "link small",
        text: m("ui.previous_month"),
        disabled: monthStart.getTime() <= min,
        onclick: () => onMonthChange(new Date(month.getFullYear(), month.getMonth() - 1, 1))
      }),
      el("span", { class: "small", text: window.billingI18n.date(month, { year: "numeric", month: "long" }) }),
      el("button", {
        type: "button",
        class: "link small",
        text: m("ui.next_month"),
        disabled: nextMonth.getTime() > max,
        onclick: () => onMonthChange(nextMonth)
      })
    ),
    grid
  );
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

addEventListener("resize", closeDateTimePickers);
