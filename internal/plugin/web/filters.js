// A filter narrows a view by one field: a choice among options(account), an on/off toggle, or a search term.
// Choices and toggles persist with the role's view preferences; search terms last for the session.
// A filter may also declare the query its options come from (source) and how to read it (load).
const filterValues = { admin: new Map(), account: new Map() };
// Selects records whose field is empty, such as requests without an API key.
const EMPTY_FIELD = "__empty__";

function searchFilter(id) { return { id, label: m("ui.keyword"), icon: "search", search: true }; }

function statusFilter(id) {
  return {
    id,
    label: m("ui.status"),
    icon: "toggle-right",
    initial: "enabled",
    options: () => [{ value: "enabled", label: m("ui.enabled") }, { value: "disabled", label: m("ui.disabled") }]
  };
}

function matchesStatus(status, disabled) { return !status || disabled === (status === "disabled"); }

function filterValue(account, filter) {
  const role = account ? "account" : "admin";
  const values = filterValues[role];
  if (!values.has(filter.id)) {
    const fallback = filter.initial ?? (filter.toggle ? false : "");
    values.set(filter.id, filter.search ? fallback : viewPreference(role, "filter." + filter.id, fallback));
  }
  return values.get(filter.id);
}

function filterActive(account, filter) {
  const value = filterValue(account, filter);
  return filter.toggle ? value : value !== "";
}

function filterSnapshot(account, filters) { return JSON.stringify(filters.map((filter) => filterValue(account, filter))); }

// Maps query fields to the filters that set them; an empty-field choice sets field_empty.
function filterQuery(account, fields) {
  const query = {};
  for (const [field, filter] of Object.entries(fields)) {
    const value = filterValue(account, filter);
    if (value === EMPTY_FIELD) query[field + "_empty"] = "true"; else if (value) query[field] = value;
  }
  return query;
}

// The current choice stays listed after the data that offered it changes.
function filterOptions(account, filter) {
  const value = filterValue(account, filter);
  const options = filter.options(account);
  if (value && !options.some((option) => option.value === value)) options.push({ value, label: filter.describe?.(value) ?? value });
  return options;
}

// A bar shows each active filter as a chip and offers the rest from its add button; apply(filter) runs after a change.
// Its state lives on its element: the filters it shows, the chips it drew, and the search filter being edited.
function renderFilterBar(target, account, filters, apply) {
  const bar = (target.filterBar ||= { target, chips: "", editing: null });
  Object.assign(bar, { account, filters, apply });
  drawFilterBar(bar);
}

function drawFilterBar(bar) {
  const { target, account, filters } = bar;
  const active = filters.filter((filter) => bar.editing === filter.id || filterActive(account, filter));
  // Rebuilding only when the chips change keeps a search field and its focus while the user types.
  const chips = JSON.stringify([account, filters.map((filter) => filter.id), active.map((filter) => filter.id)]);
  if (bar.chips !== chips) {
    bar.chips = chips;
    const inactive = filters.filter((filter) => !active.includes(filter));
    target.replaceChildren(
      ...active.map((filter) => filterChip(bar, filter)),
      ...(inactive.length ? [addFilterButton(bar, inactive)] : [])
    );
  }
  for (const chip of target.children) chip.refresh?.();
}

function changeFilter(bar, filter, value) {
  if (value === filterValue(bar.account, filter)) return;
  const role = bar.account ? "account" : "admin";
  filterValues[role].set(filter.id, value);
  if (!filter.search) saveViewPreference(role, "filter." + filter.id, value);
  drawFilterBar(bar);
  bar.apply(filter);
}

function filterChip(bar, filter) {
  const remove = el(
    "button",
    {
      type: "button",
      class: "icon-action filter-chip-remove",
      "aria-label": m("ui.remove_filter_value", { v0: filter.label }),
      onclick: () => {
        bar.editing = null;
        changeFilter(bar, filter, filter.toggle ? false : "");
      }
    },
    actionIcon("x")
  );
  if (filter.toggle) return el("span", { class: "filter-chip" }, el("span", { class: "filter-chip-body", text: filter.label }), remove);
  if (filter.search) return searchFilterChip(bar, filter, remove);
  const value = el("span", { class: "filter-chip-value" + (filter.masked ? " mask-blur" : "") });
  const button = el(
    "button",
    { type: "button", class: "filter-chip-body", onclick: () => openFilterChoices(bar, filter, button) },
    el("span", { class: "filter-chip-label", text: filter.label }),
    value
  );
  const chip = el("span", { class: "filter-chip" }, button, remove);
  chip.refresh = () => {
    const current = filterValue(bar.account, filter);
    setText(value, displayLabel(filterOptions(bar.account, filter).find((option) => option.value === current).label));
  };
  return chip;
}

// An emptied search chip stays while its field has focus, and leaves once the field loses it.
function searchFilterChip(bar, filter, remove) {
  const input = el("input", {
    type: "search",
    class: "filter-chip-input",
    autocomplete: "off",
    "aria-label": filter.label,
    onfocus: () => { bar.editing = filter.id; },
    onblur: () => {
      bar.editing = null;
      drawFilterBar(bar);
    },
    oninput: () => changeFilter(bar, filter, input.value),
    onkeydown: (event) => {
      if (event.key !== "Escape") return;
      event.stopPropagation();
      input.value = "";
      changeFilter(bar, filter, "");
      input.blur();
    }
  });
  const chip = el(
    "span",
    { class: "filter-chip" },
    el("label", { class: "filter-chip-body" }, el("span", { class: "filter-chip-label", text: filter.label }), input),
    remove
  );
  chip.refresh = () => { if (document.activeElement !== input) input.value = filterValue(bar.account, filter); };
  return chip;
}

function openFilterChoices(bar, filter, anchor) {
  toggleChoiceMenu(anchor, {
    label: filter.label,
    searchable: filter.searchable,
    choices: () =>
      filterOptions(bar.account, filter).map((option) => ({
        ...option,
        selected: option.value === filterValue(bar.account, filter),
        masked: filter.masked
      })),
    select: (value) => changeFilter(bar, filter, value),
    onToggle: (open) => anchor.setAttribute("aria-expanded", String(open))
  });
}

function addFilterButton(bar, filters) {
  const button = el(
    "button",
    { type: "button", class: "filter-add", "aria-haspopup": "menu", "aria-expanded": "false" },
    actionIcon("plus"),
    el("span", { text: m("ui.filter") })
  );
  button.onclick = () =>
    toggleChoiceMenu(button, {
      label: m("ui.add_filter"),
      menu: true,
      choices: () => filters.map((filter) => ({ value: filter, label: filter.label, icon: filter.icon, submenu: !!filter.options })),
      select: (filter) => {
        if (filter.options) openFilterChoices(bar, filter, button);
        else if (filter.toggle) changeFilter(bar, filter, true);
        else {
          bar.editing = filter.id;
          drawFilterBar(bar);
          bar.target.querySelector(".filter-chip-input").focus();
        }
      },
      onToggle: (open) => button.setAttribute("aria-expanded", String(open))
    });
  return button;
}
