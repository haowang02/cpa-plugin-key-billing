let activeTableSelection = null;

function clearTableSelection() {
  const previous = activeTableSelection;
  activeTableSelection = null;
  previous?.clear();
  renderSelectionBar();
}

function createTableSelection({ id, label, eligible = () => true, disabledTip, context, render, actions, busy = () => false }) {
  return {
    id,
    render,
    actions,
    target: null,
    items: new Map(),
    selected: new Set(),
    context: "",
    job: null,
    get running() { return this.job?.generation === sessionGeneration; },
    get busy() { return this.running || busy(); },
    update(target, items) {
      const nextContext = JSON.stringify(context());
      if (!this.running && this.context !== nextContext) this.selected.clear();
      this.context = nextContext;
      this.target = target;
      this.items = new Map(items.filter(eligible).map((item) => [id(item), item]));
      if (!this.running) for (const value of this.selected) if (!this.items.has(value)) this.selected.delete(value);
    },
    values() { return [...this.items.values()].filter((item) => this.selected.has(id(item))); },
    clear() {
      this.selected.clear();
      this.sync();
    },
    choose(values, checked) {
      if (activeTableSelection !== this) {
        clearTableSelection();
        activeTableSelection = this;
      }
      for (const value of values) { if (checked) this.selected.add(value); else this.selected.delete(value); }
      this.sync();
    },
    checkbox(item) {
      const all = item === undefined;
      const value = all ? null : id(item);
      const input = el("input", {
        type: "checkbox",
        "data-selection-id": value,
        "data-selection-all": all ? "" : null,
        "aria-label": all ? m("ui.select_all_visible") : m("ui.select_row", { name: label(item) }),
        "data-tip": !all && !eligible(item) ? disabledTip : null,
        onchange: (event) => this.choose(all ? this.items.keys() : [value], event.target.checked)
      });
      return checkboxControl({ input });
    },
    sync() {
      for (const input of this.target?.querySelectorAll("[data-selection-id]") || []) {
        input.checked = this.selected.has(input.dataset.selectionId) && this.items.has(input.dataset.selectionId);
        input.disabled = this.busy || !this.items.has(input.dataset.selectionId);
        input.closest("tr").classList.toggle("selected-row", input.checked);
      }
      for (const input of this.target?.querySelectorAll("[data-selection-all]") || []) {
        const count = [...this.items.keys()].filter((value) => this.selected.has(value)).length;
        input.checked = count > 0 && count === this.items.size;
        input.indeterminate = count > 0 && count < this.items.size;
        input.disabled = this.busy || !this.items.size;
      }
      if (activeTableSelection === this) renderSelectionBar();
    },
    action(label, items, apply, danger = false) {
      return el("button", {
        type: "button",
        class: "batch-action" + (danger ? " danger" : ""),
        disabled: this.busy || !items.length,
        "data-tip": !items.length ? m("ui.batch_unavailable") : null,
        text:
          items.length && items.length < this.selected.size ? m("ui.batch_action_count", { action: label, count: items.length }) : label,
        onclick: () => guard(() => apply(items))
      });
    },
    select(label, options, apply, disabled = false) {
      return el(
        "span",
        { class: "native-select batch-select" },
        el(
          "select",
          {
            "aria-label": label,
            disabled: this.busy || disabled,
            onchange: (event) => {
              const value = event.target.value;
              event.target.selectedIndex = 0;
              guard(() => apply(value));
            }
          },
          el("option", { selected: true, disabled: true, text: label }),
          options
        )
      );
    },
    async run(items, apply, { groupBy = id, concurrency = 1, refresh, keepSelection = false, success } = {}) {
      if (this.running) return false;
      const groups = new Map();
      for (const item of items) {
        if (!this.selected.has(id(item))) continue;
        const key = groupBy(item);
        if (!groups.has(key)) groups.set(key, []);
        groups.get(key).push(item);
      }
      const pending = [...groups.values()];
      if (!pending.length) return;
      const job = { generation: sessionGeneration, done: 0, total: pending.reduce((count, group) => count + group.length, 0) };
      this.job = job;
      this.render();
      const failures = [];
      let next = 0, succeeded = 0, stopped = null;
      const current = () => job.generation === sessionGeneration && currentRole === "admin";
      try {
        const worker = async () => {
          while (next < pending.length && current() && !stopped) {
            const group = pending[next++];
            try {
              await apply(group[0], group);
              if (!current()) return;
              succeeded += group.length;
              for (const item of group) this.selected.delete(id(item));
            } catch (error) {
              if (error instanceof AuthError || error instanceof StaleRequestError) {
                stopped = error;
                return;
              }
              failures.push({ count: group.length, error });
            }
            job.done += group.length;
            renderSelectionBar();
          }
        };
        await Promise.all(Array.from({ length: Math.min(concurrency, pending.length) }, worker));
        if (stopped) throw stopped;
        if (!current()) throw new StaleRequestError();
        await refresh?.();
        if (keepSelection && !failures.length) for (const group of pending) for (const item of group) this.selected.add(id(item));
      } finally {
        if (this.job === job) this.job = null;
        if (current()) {
          this.render();
          this.sync();
        }
      }
      if (failures.length)
        throw new UIError(
          m("ui.batch_failure", {
            done: succeeded,
            failed: failures.reduce((count, failure) => count + failure.count, 0),
            error: failures[0].error.message || String(failures[0].error)
          })
        );
      notify(success || m("ui.batch_success"));
    }
  };
}

function renderSelectionBar() {
  const bar = $("batch-bar");
  const selection = activeTableSelection;
  const visible = currentRole === "admin" && selection && (selection.selected.size > 0 || selection.running);
  bar.classList.toggle("hidden", !visible);
  document.body.classList.toggle("has-batch-selection", !!visible);
  if (!visible) {
    bar.replaceChildren();
    return;
  }
  bar.replaceChildren(
    el(
      "div",
      { class: "batch-summary" },
      el("span", {
        class: "batch-count",
        role: "status",
        "aria-live": "polite",
        text: selection.running
          ? m("ui.batch_progress", { done: selection.job.done, total: selection.job.total })
          : m("ui.batch_selected", { count: selection.selected.size })
      }),
      el("button", {
        type: "button",
        class: "link",
        disabled: selection.busy,
        text: m("ui.clear_selection"),
        onclick: () => selection.clear()
      })
    ),
    el("div", { class: "batch-actions" }, selection.actions(selection.values()))
  );
}
