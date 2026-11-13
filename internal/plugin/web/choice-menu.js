const choiceSearch = el("input", {
  type: "search",
  class: "choice-search",
  autocomplete: "off",
  placeholder: m("ui.search"),
  "aria-label": m("ui.search"),
  oninput: () => renderChoiceMenu()
});
const choiceList = el("div", { class: "choice-options", role: "listbox" });
const choiceCategories = el("div", { class: "choice-categories hidden", role: "menu" });
const choiceBack = el(
  "button",
  { type: "button", class: "choice-back", "aria-label": m("ui.back"), onclick: leaveChoiceCategory },
  actionIcon("chevron", "choice-back-icon"),
  el("span")
);
const choicePane = el("div", { class: "choice-pane" }, choiceBack, choiceSearch, choiceList);
// Manual dismissal keeps a second anchor click from reopening the menu.
const choiceMenu = el("div", { class: "choice-menu", popover: "manual" }, choiceCategories, choicePane);
let choiceSource = null;
let choiceTyped = "", choiceTypedAt = 0;
document.body.append(choiceMenu);

// source: { label, choices(), select(value), onToggle(open), searchable, align, menu } or, in place of choices and select,
// { categories(), category }, where category names the one to open on. A category is { value, label, icon } with either
// choices(), select(value) and searchable, or run() and, when it toggles, checked.
// A choice is { value, label, icon, detail, selected, disabled, masked }; a change of group starts a new section.
// Menu choices run as actions and may be checked or dangerous, or be a control: { label, icon, options, current, change(value), note }.
function toggleChoiceMenu(anchor, source) {
  const open = choiceMenu.matches(":popover-open");
  if (open) choiceMenu.hidePopover();
  if (open && choiceSource?.anchor === anchor) return;
  choiceSource = { ...source, anchor };
  // A modal dialog makes the rest of the document inert, so the menu opens inside the anchor's dialog.
  (anchor.closest("dialog") || document.body).append(choiceMenu);
  const categories = source.categories?.() || [];
  const requested = categories.find((category) => category.value === source.category);
  choiceSource.category = requested || categories.find((category) => category.choices);
  choiceMenu.classList.toggle("choice-columns", categories.length > 0);
  choiceMenu.classList.toggle("choice-drilled", !!requested);
  choiceMenu.style.minWidth = categories.length ? "" : anchor.offsetWidth + "px";
  choiceCategories.classList.toggle("hidden", !categories.length);
  setTextAttribute(choiceCategories, "aria-label", source.label);
  renderChoiceCategories(categories);
  choiceSearch.value = "";
  renderChoiceMenu();
  choiceMenu.showPopover();
  placeChoiceMenu();
  const selected = choiceList.querySelector('[aria-selected="true"]');
  selected?.scrollIntoView({ block: "nearest" });
  const categoryFirst = categories.length && !requested && (!choiceSource.category || matchMedia("(max-width: 520px)").matches);
  if (categoryFirst)
    (choiceCategories.querySelector('[aria-current="true"]') || choiceCategories.firstElementChild).focus({ preventScroll: true });
  else focusChoiceValues();
}

function leaveChoiceCategory() {
  choiceMenu.classList.remove("choice-drilled");
  choiceCategories.querySelector('[aria-current="true"]')?.focus({ preventScroll: true });
  placeChoiceMenu();
}

function activeChoiceSource() { return choiceSource.category || choiceSource; }

const SHORT_CHOICE_LIST_LENGTH = 5;
function choiceSearchable(source, choices = source.choices?.() || []) {
  return !!source.searchable && choices.length > SHORT_CHOICE_LIST_LENGTH;
}

function focusChoiceValues() {
  const source = activeChoiceSource();
  const typing = choiceSearchable(source) && !matchMedia("(pointer: coarse)").matches;
  const row =
    (!source.menu && choiceList.querySelector('[aria-selected="true"]')) ||
    choiceList.querySelector(".choice-option:not(:disabled), .choice-control button");
  (typing ? choiceSearch : row || choiceSearch).focus({ preventScroll: true });
}

function renderChoiceCategories(categories = choiceSource.categories?.() || []) {
  choiceCategories.replaceChildren(
    ...categories.map((category) => {
      const button = el(
        "button",
        {
          type: "button",
          class: "choice-category",
          role: category.checked === undefined ? "menuitem" : "menuitemcheckbox",
          "aria-checked": category.checked === undefined ? null : String(category.checked),
          tabindex: "-1",
          "aria-current": String(category.value === choiceSource.category?.value),
          "aria-haspopup": category.choices ? "listbox" : null,
          "aria-expanded": category.choices ? String(category.value === choiceSource.category?.value) : null,
          onclick: () => selectChoiceCategory(category)
        },
        category.icon && actionIcon(category.icon, "choice-icon"),
        el("span", { class: "choice-label", text: category.label }),
        category.choices
          ? actionIcon("chevron", "choice-category-chevron")
          : category.checked !== undefined && actionIcon("check", "choice-check")
      );
      button.category = category;
      return button;
    })
  );
}

function selectChoiceCategory(category) {
  if (category.run) {
    const anchor = choiceSource.anchor;
    choiceMenu.hidePopover();
    anchor.focus({ preventScroll: true });
    category.run();
    return;
  }
  choiceSource.category = category;
  choiceSearch.value = "";
  choiceMenu.classList.add("choice-drilled");
  renderChoiceCategories();
  renderChoiceMenu();
  placeChoiceMenu();
  focusChoiceValues();
}

function choiceControl(choice) {
  return el(
    "div",
    { class: "choice-control" },
    el("div", { class: "choice-control-label" }, choice.icon && actionIcon(choice.icon, "choice-icon"), el("span", { text: choice.label })),
    segmentedControl(choice.label, choice.options, choice.current, choice.change, { fill: true }),
    choice.note && el("div", { class: "choice-note", text: choice.note })
  );
}

function attachChoiceMenu(button, source) {
  button.setAttribute("aria-haspopup", source.menu ? "menu" : "listbox");
  button.setAttribute("aria-expanded", "false");
  button.onclick = () =>
    toggleChoiceMenu(button, { align: "end", ...source, onToggle: (open) => button.setAttribute("aria-expanded", String(open)) });
}

function choiceRow(choice, menu) {
  const checkable = menu && choice.checked !== undefined;
  const row = el(
    "button",
    {
      type: "button",
      class: "choice-option" + (choice.danger ? " choice-danger" : ""),
      role: menu ? (checkable ? "menuitemcheckbox" : "menuitem") : "option",
      tabindex: "-1",
      "aria-selected": menu ? null : String(!!choice.selected),
      "aria-checked": checkable ? String(choice.checked) : null,
      disabled: choice.disabled,
      onclick: () => pickChoice(choice.value)
    },
    choice.icon && actionIcon(choice.icon, "choice-icon"),
    el("span", {
      class: "choice-label" + (choice.masked ? " mask-blur" : ""),
      "data-tip": choice.label,
      "data-tip-overflow": "",
      text: choice.label
    }),
    choice.detail != null && el("span", { class: "choice-detail", text: choice.detail }),
    (!menu || checkable) && actionIcon("check", "choice-check")
  );
  row.choice = choice.value;
  return row;
}

function renderChoiceMenu() {
  const focused = choiceList.contains(document.activeElement) ? document.activeElement.choice : undefined;
  const source = activeChoiceSource();
  const all = source.choices?.() || [];
  const searchable = choiceSearchable(source, all);
  choicePane.classList.toggle("hidden", !!choiceSource.categories && !choiceSource.category);
  choiceSearch.classList.toggle("hidden", !searchable);
  setText(choiceBack.lastElementChild, source.label || "");
  choiceList.setAttribute("role", source.menu ? "menu" : "listbox");
  setTextAttribute(choiceList, "aria-label", source.label);
  const query = searchable ? choiceSearch.value.trim().toLocaleLowerCase() : "";
  const menu = !!source.menu;
  const choices = all.filter((choice) => !query || String(choice.label).toLocaleLowerCase().includes(query));
  const rows = [];
  choices.forEach((choice, index) => {
    if (index > 0 && choice.group !== choices[index - 1].group) rows.push(el("div", { class: "choice-separator", role: "separator" }));
    rows.push(choice.options ? choiceControl(choice) : choiceRow(choice, menu));
  });
  choiceList.replaceChildren(...rows);
  if (!choices.length) choiceList.append(el("div", { class: "choice-empty", text: m("ui.no_matches") }));
  if (focused !== undefined) [...choiceList.children].find((row) => row.choice === focused)?.focus({ preventScroll: true });
}

function placeChoiceMenu() {
  positionAnchoredPopover(choiceMenu, choiceSource.anchor, { align: choiceSource.align, maxHeight: "min(360px, 60dvh)" });
}

function pickChoice(value) {
  const { anchor } = choiceSource;
  const { select } = activeChoiceSource();
  choiceMenu.hidePopover();
  anchor.focus({ preventScroll: true });
  select(value);
}

function focusChoice(row) {
  row?.focus({ preventScroll: true });
  row?.scrollIntoView({ block: "nearest" });
}

function followChoiceAnchor() {
  if (!choiceMenu.matches(":popover-open")) return;
  const box = choiceSource.anchor.getBoundingClientRect();
  if (box.bottom < 0 || box.top > innerHeight) choiceMenu.hidePopover(); else placeChoiceMenu();
}

choiceMenu.addEventListener("beforetoggle", (event) => choiceSource?.onToggle?.(event.newState === "open"));
choiceMenu.addEventListener("keydown", (event) => {
  if (event.key === "Escape" || event.key === "Tab") {
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
    }
    choiceMenu.hidePopover();
    choiceSource.anchor.focus({ preventScroll: true });
    return;
  }
  const categoryRows = [...choiceCategories.querySelectorAll(".choice-category")];
  if (choiceCategories.contains(event.target)) {
    const index = categoryRows.indexOf(document.activeElement);
    if (event.key === "ArrowRight") {
      event.preventDefault();
      selectChoiceCategory(document.activeElement.category);
    } else if (["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) {
      event.preventDefault();
      const next = event.key === "Home" ? 0 : event.key === "End" ? categoryRows.length - 1 : index + (event.key === "ArrowDown" ? 1 : -1);
      focusChoice(categoryRows[(next + categoryRows.length) % categoryRows.length]);
    }
    return;
  }
  if (choiceSource.categories && event.key === "ArrowLeft" && event.target !== choiceSearch) {
    event.preventDefault();
    leaveChoiceCategory();
    return;
  }
  const control = event.target.closest(".choice-control > .tabs");
  if (control && ["ArrowLeft", "ArrowRight"].includes(event.key)) {
    event.preventDefault();
    const buttons = [...control.children];
    const next = buttons.indexOf(document.activeElement) + (event.key === "ArrowRight" ? 1 : -1);
    focusChoice(buttons[(next + buttons.length) % buttons.length]);
    return;
  }
  const rows = [...choiceList.querySelectorAll(".choice-option:not(:disabled), .choice-control button")];
  if (!rows.length) return;
  const index = rows.indexOf(document.activeElement);
  const searching = event.target === choiceSearch;
  if (searching && event.key === "Enter") {
    event.preventDefault();
    rows[0].click();
    return;
  }
  const next = {
    ArrowDown: index + 1,
    ArrowUp: (index < 0 ? rows.length : index) - 1,
    ...(searching ? {} : { Home: 0, End: rows.length - 1 })
  }[event.key];
  if (next !== undefined) {
    event.preventDefault();
    focusChoice(rows[(next + rows.length) % rows.length]);
  } else if (!searching && event.key.length === 1 && event.key !== " " && !event.ctrlKey && !event.metaKey && !event.altKey) {
    choiceTyped = (Date.now() - choiceTypedAt < 600 ? choiceTyped : "") + event.key.toLocaleLowerCase();
    choiceTypedAt = Date.now();
    focusChoice(rows.find((row) => row.textContent.toLocaleLowerCase().startsWith(choiceTyped)));
  }
});
document.addEventListener(
  "pointerdown",
  (event) => {
    if (choiceMenu.matches(":popover-open") && !choiceMenu.contains(event.target) && !choiceSource.anchor.contains(event.target))
      choiceMenu.hidePopover();
  },
  { capture: true }
);
document.addEventListener(
  "scroll",
  (event) => { if (!choiceMenu.contains(event.target)) followChoiceAnchor(); },
  { capture: true, passive: true }
);
addEventListener("resize", followChoiceAnchor);
