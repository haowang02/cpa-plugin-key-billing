const CREDENTIAL_TEST_TIMEOUT_MS = 65000;
const credentialTests = new Set();

function credentialActionButton(icon, label, { tip = label, disabled = false, className = "", run, ...attrs }) {
  return el(
    "button",
    {
      type: "button",
      class: "icon-action" + (className ? " " + className : ""),
      "aria-label": label,
      "data-tip": tip,
      disabled,
      onclick: () => guard(run),
      ...attrs
    },
    actionIcon(icon)
  );
}

function credentialPowerButton(credential, { disabled, group = false, toggle }) {
  const enabled = !credential.disabled;
  const [enable, disable] = group ? [m("ui.enable_provider_group"), m("ui.disable_provider_group")] : [m("ui.enable"), m("ui.disable")];
  return credentialActionButton(enabled ? "power" : "power-off", enable, {
    tip: enabled ? disable : enable,
    disabled,
    className: "credential-power",
    "aria-pressed": String(enabled),
    run: () => toggle(!enabled)
  });
}

function credentialTestButton(credential, render) {
  const testing = credentialTests.has(credential.auth_index);
  const unavailable = credential.disabled
    ? m("ui.enable_the_credential_to_test_its_connection")
    : !credential.auth_index && m("ui.credential_not_loaded");
  return credentialActionButton(testing ? "loader-circle" : "square-activity", m("ui.test_connection"), {
    tip: unavailable || m("ui.test_connection"),
    disabled: !!unavailable || testing,
    className: testing ? "loading" : "",
    run: () => testCredentialConnection(credential.auth_index, render)
  });
}

async function testCredentialConnection(authIndex, render) {
  if (credentialTests.has(authIndex)) return;
  const generation = sessionGeneration;
  credentialTests.add(authIndex);
  render();
  try {
    const result = await plugin(
      "POST",
      "/credentials/test",
      { auth_index: authIndex },
      { command: true, timeoutMS: CREDENTIAL_TEST_TIMEOUT_MS }
    );
    notify(m("ui.connection_test_passed_value_value", { v0: result.model, v1: latency(result.latency_ms) }));
  } finally {
    credentialTests.delete(authIndex);
    if (generation === sessionGeneration) render();
  }
}
