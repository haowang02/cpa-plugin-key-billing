function credentialEditCell(edit, disabled) {
  return el(
    "td",
    { class: "credential-edit-cell" },
    el(
      "button",
      { type: "button", class: "inline-icon-button link", "aria-label": m("ui.edit"), disabled, onclick: () => guard(edit) },
      actionIcon("pencil")
    )
  );
}

function openCredentialEditor(title, values, save, { weightEditable = true, hint = "" } = {}) {
  openEditor("credential-dialog", null, () => {
    const initial = { note: values.note || "", priority: values.priority ?? 0, weight: values.weight ?? 1 };
    setText($("credential-dialog-title"), title);
    $("credential-note").value = initial.note;
    $("credential-priority").value = initial.priority;
    $("credential-weight").value = weightEditable ? initial.weight : "";
    $("credential-weight").disabled = !weightEditable;
    $("credential-weight").placeholder = weightEditable ? "1" : "";
    setText($("credential-editor-hint"), hint);
    $("credential-editor-hint").classList.toggle("hidden", !hint);
    return { save, values: initial };
  });
}

for (const [id, message] of [["credential-priority-info", "ui.priority_hint"], ["credential-weight-info", "ui.weight_hint"]]) {
  $(id).replaceChildren(actionIcon("info"));
  setTextAttribute($(id), "data-tip", m(message));
}

$("credential-save").onclick = () =>
  submitEditor("credential-dialog", async (editor) => {
    const priority = $("credential-priority").valueAsNumber;
    const input = $("credential-weight");
    let weight;
    if (!input.disabled) {
      weight = input.value.trim() === "" && !input.validity.badInput ? 1 : input.valueAsNumber;
      if (!Number.isSafeInteger(weight) || weight > 1000000) throw new UIError(m("ui.weight_invalid"));
      weight = Math.max(0, weight);
    }
    const values = { note: $("credential-note").value.trim(), priority: Number.isSafeInteger(priority) ? priority : undefined, weight };
    const changes = Object.fromEntries(
      Object.entries(values).filter(([field, value]) => value !== undefined && value !== editor.initial.values[field])
    );
    if (Object.keys(changes).length) {
      await editor.initial.save(changes);
      notify(m("ui.credential_saved"));
    }
  });
