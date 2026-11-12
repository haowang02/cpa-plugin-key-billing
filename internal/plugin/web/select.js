// One popover lists the choices of whichever control opened it: values to pick, or, as a menu, actions to run.
const choiceSearch = el("input", {
  type: "search",
  class: "choice-search",
  autocomplete: "off",
  placeholder: m("ui.search"),
  "aria-label": m("ui.search"),
  oninput: () => renderChoiceMenu()
});
const choiceList = el("div", { class: "choice-options", role: "listbox" });
// Manual, so pressing the anchor of an open menu closes it instead of light dismiss closing and the click reopening it.
const choiceMenu = el("div", { class: "choice-menu", popover: "manual" }, choiceSearch, choiceList);
let choiceSource = null;
let choiceTyped = "", choiceTypedAt = 0;
document.body.append(choiceMenu);

// source: { label, choices(), select(value), onToggle(open), searchable, align, menu }
// A choice is { value, label, icon, detail, selected, disabled, masked }. Menu choices may instead be checked,
// open a submenu, be dangerous, or be a caption; a change of group starts a new section.
function toggleChoiceMenu(anchor, source) {
  const open = choiceMenu.matches(":popover-open");
  if (open) choiceMenu.hidePopover();
  if (open && choiceSource?.anchor === anchor) return;
  choiceSource = { ...source, anchor };
  // A modal dialog makes the rest of the document inert, so the menu opens inside the anchor's dialog.
  (anchor.closest("dialog") || document.body).append(choiceMenu);
  choiceSearch.value = "";
  choiceSearch.classList.toggle("hidden", !source.searchable);
  choiceList.setAttribute("role", source.menu ? "menu" : "listbox");
  setTextAttribute(choiceList, "aria-label", source.label);
  renderChoiceMenu();
  choiceMenu.style.minWidth = anchor.offsetWidth + "px";
  choiceMenu.showPopover();
  placeChoiceMenu();
  const selected = choiceList.querySelector('[aria-selected="true"]');
  selected?.scrollIntoView({ block: "nearest" });
  // Touch screens keep their keyboard closed until the search field is tapped.
  const typing = source.searchable && !matchMedia("(pointer: coarse)").matches;
  (typing ? choiceSearch : selected || choiceList.querySelector(".choice-option:not(:disabled)") || choiceSearch).focus({
    preventScroll: true
  });
}

// The button's menu opens from its end edge; it lists values, or actions when source.menu is set.
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
      class: "choice-option" + (choice.danger ? " danger" : ""),
      role: menu ? (checkable ? "menuitemcheckbox" : "menuitem") : "option",
      tabindex: "-1",
      "aria-selected": menu ? null : String(!!choice.selected),
      "aria-checked": checkable ? String(choice.checked) : null,
      "aria-haspopup": choice.submenu ? "listbox" : null,
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
    choice.submenu ? actionIcon("chevron", "choice-submenu") : (!menu || checkable) && actionIcon("check", "choice-check")
  );
  row.choice = choice.value;
  return row;
}

function renderChoiceMenu() {
  const focused = choiceList.contains(document.activeElement) ? document.activeElement.choice : undefined;
  const query = choiceSearch.value.trim().toLocaleLowerCase();
  const menu = !!choiceSource.menu;
  const choices = choiceSource.choices().filter((choice) => !query || String(choice.label).toLocaleLowerCase().includes(query));
  const rows = [];
  choices.forEach((choice, index) => {
    if (index > 0 && choice.group !== choices[index - 1].group) rows.push(el("div", { class: "choice-separator", role: "separator" }));
    rows.push(choice.caption ? el("div", { class: "choice-caption", role: "presentation", text: choice.label }) : choiceRow(choice, menu));
  });
  choiceList.replaceChildren(...rows);
  if (!choices.length) choiceList.append(el("div", { class: "choice-empty", text: m("ui.no_matches") }));
  if (focused !== undefined) [...choiceList.children].find((row) => row.choice === focused)?.focus({ preventScroll: true });
}

function placeChoiceMenu() {
  positionAnchoredPopover(choiceMenu, choiceSource.anchor, { align: choiceSource.align, maxHeight: "min(360px, 60dvh)" });
}

function pickChoice(value) {
  const { anchor, select } = choiceSource;
  choiceMenu.hidePopover();
  anchor.focus({ preventScroll: true });
  select(value);
}

function focusChoice(row) {
  row?.focus({ preventScroll: true });
  row?.scrollIntoView({ block: "nearest" });
}

// The menu follows its anchor and closes once the anchor leaves the viewport.
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
  const rows = [...choiceList.querySelectorAll(".choice-option:not(:disabled)")];
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

// A select whose option children stay the data, so callers keep the select API: value, options,
// selectedIndex, selectedOptions, disabled and change events. It sizes to its widest option.
class UISelect extends HTMLElement {
  static formAssociated = true;
  static observedAttributes = ["disabled", "placeholder"];
  #internals = this.attachInternals();
  #labels = el("span", { part: "value" });

  constructor() {
    super();
    const chevron = actionIcon("chevron-down");
    chevron.setAttribute("part", "chevron");
    this.attachShadow({ mode: "open" }).append(this.#labels, chevron);
    this.#internals.role = "combobox";
    this.#internals.ariaHasPopup = "listbox";
    this.#internals.ariaExpanded = "false";
    new MutationObserver(() => this.#render()).observe(this, {
      subtree: true,
      childList: true,
      characterData: true,
      attributes: true,
      attributeFilter: ["label", "value", "selected", "disabled"]
    });
    this.addEventListener("click", (event) => {
      // An enclosing label would otherwise pass the click on to its first control.
      event.preventDefault();
      this.#toggle();
    });
    this.addEventListener("keydown", (event) => {
      if (!["ArrowDown", "ArrowUp", "Enter", " "].includes(event.key)) return;
      event.preventDefault();
      this.#toggle();
    });
  }

  connectedCallback() { this.#render(); }

  // Renders replace controls, so a menu never outlives the control that opened it.
  disconnectedCallback() { if (this.#menuOpen) choiceMenu.hidePopover(); }

  attributeChangedCallback(name) {
    if (name === "disabled" && this.disabled && this.#menuOpen) choiceMenu.hidePopover();
    this.#render();
  }

  get #menuOpen() { return choiceSource?.anchor === this && choiceMenu.matches(":popover-open"); }

  get options() { return Array.from(this.querySelectorAll(":scope > option")); }
  get selectedOptions() { return [this.#selected()].filter(Boolean); }
  get selectedIndex() { return this.options.indexOf(this.#selected()); }
  set selectedIndex(index) { this.#choose(this.options[index] ?? null); }
  get value() { return this.#selected()?.value ?? ""; }
  set value(value) { this.#choose(this.options.find((option) => option.value === String(value)) ?? null); }
  get disabled() { return this.hasAttribute("disabled"); }
  set disabled(value) { this.toggleAttribute("disabled", !!value); }

  // Without a placeholder, an unmatched value falls back to the first enabled option.
  #selected() {
    const options = this.options;
    return (
      options.findLast((option) => option.selected) ??
      (this.hasAttribute("placeholder") ? null : (options.find((option) => !option.disabled) ?? null))
    );
  }

  #choose(option) {
    for (const item of this.options) item.selected = item === option;
    this.#render();
  }

  #render() {
    this.tabIndex = this.disabled ? -1 : 0;
    this.#internals.ariaDisabled = String(this.disabled);
    const options = this.options;
    const selected = this.#selected();
    // A fallback choice becomes selected, so option:checked matches what the control shows.
    if (selected) selected.selected = true;
    const labels = options.map((option) => [option.label, option === selected]);
    if (this.hasAttribute("placeholder")) labels.push([this.getAttribute("placeholder"), !selected]);
    this.#labels.replaceChildren(
      ...labels.map(([label, current]) => el("span", { part: current ? "label current" : "label", text: label }))
    );
    if (selected) this.#internals.states.delete("placeholder"); else this.#internals.states.add("placeholder");
    if (this.#menuOpen) renderChoiceMenu();
  }

  #toggle() {
    if (this.disabled) return;
    toggleChoiceMenu(this, {
      label: this.getAttribute("aria-label") || "",
      searchable: this.hasAttribute("searchable"),
      choices: () =>
        this.options.map((option) => ({ value: option, label: option.label, selected: option.selected, disabled: option.disabled })),
      select: (option) => this.#pick(option),
      onToggle: (open) => {
        this.#internals.ariaExpanded = String(open);
        if (open) this.#internals.states.add("open"); else this.#internals.states.delete("open");
      }
    });
  }

  #pick(option) {
    if (option.selected) return;
    this.#choose(option);
    this.dispatchEvent(new Event("input", { bubbles: true }));
    this.dispatchEvent(new Event("change", { bubbles: true }));
  }
}

customElements.define("ui-select", UISelect);
