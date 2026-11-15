function openCredentialEditor(title, values, save, { weightEditable = true, hint = "" } = {}) {
  openEditor("credential-dialog", null, () => {
    const initial = { note: values.note || "", test_model: "", priority: values.priority ?? 0, weight: values.weight ?? 1 };
    setText($("credential-dialog-title"), title);
    $("credential-note").value = initial.note;
    renderCredentialTestModels("", null);
    $("credential-priority").value = initial.priority;
    $("credential-weight").value = weightEditable ? initial.weight : "";
    $("credential-weight").disabled = !weightEditable;
    $("credential-weight").placeholder = weightEditable ? "1" : "";
    setText($("credential-editor-hint"), hint);
    $("credential-editor-hint").classList.toggle("hidden", !hint);
    return { save, authIndex: values.auth_index, values: initial };
  });
  const editor = editors.get($("credential-dialog"));
  if (editor.initial.authIndex) guard(() => loadCredentialTestModel(editor));
}

async function loadCredentialTestModel(editor) {
  const query = new URLSearchParams({ auth_index: editor.initial.authIndex });
  const { model, models } = await plugin("GET", "/credentials/test-model?" + query);
  if (!editorCurrent(editor) || editor.saving) return;
  editor.initial.values.test_model = model;
  renderCredentialTestModels(model, models);
}

// A saved model the credential no longer serves stays selectable.
function renderCredentialTestModels(selected, models) {
  const options = [
    el("option", { value: "", text: models?.length ? m("ui.default_test_model_value", { v0: models[0] }) : m("ui.default_test_model") })
  ];
  if (selected && !models.includes(selected))
    options.push(el("option", { value: selected, text: m("ui.test_model_not_served_value", { v0: selected }) }));
  for (const model of models || []) options.push(el("option", { value: model, text: model }));
  $("credential-test-model").replaceChildren(...options);
  $("credential-test-model").value = selected;
  $("credential-test-model").disabled = !models;
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
    const values = {
      note: $("credential-note").value.trim(),
      test_model: $("credential-test-model").value,
      priority: Number.isSafeInteger(priority) ? priority : undefined,
      weight
    };
    const changes = Object.fromEntries(
      Object.entries(values).filter(([field, value]) => value !== undefined && value !== editor.initial.values[field])
    );
    if (!Object.keys(changes).length) return;
    const { test_model: model, ...fields } = changes;
    if (model !== undefined)
      await plugin("POST", "/credentials/test-model", { auth_index: editor.initial.authIndex, model }, { command: true });
    if (Object.keys(fields).length) await editor.initial.save(fields);
    notify(m("ui.credential_saved"));
  });
