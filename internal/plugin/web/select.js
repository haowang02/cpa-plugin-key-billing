// A select whose option children stay the data, so callers keep the select API: value, options,
// selectedIndex, selectedOptions, disabled and change events. It sizes to the selected label.
class UISelect extends HTMLElement {
  static formAssociated = true;
  static observedAttributes = ["disabled", "placeholder"];
  #internals = this.attachInternals();
  #label = el("span", { part: "value" });

  constructor() {
    super();
    const chevron = actionIcon("chevron-down");
    chevron.setAttribute("part", "chevron");
    this.attachShadow({ mode: "open" }).append(this.#label, chevron);
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
    const selected = this.#selected();
    // A fallback choice becomes selected, so option:checked matches what the control shows.
    if (selected) selected.selected = true;
    setText(this.#label, selected?.label ?? this.getAttribute("placeholder") ?? "");
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
