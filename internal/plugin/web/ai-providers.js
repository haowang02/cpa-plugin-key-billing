const aiSelection = createTableSelection({
  id: (provider) => provider.config_ref,
  label: (provider) => [provider.note || provider.name || provider.type, provider.key_preview || provider.base_url].join(" "),
  context: () => [$("ai-provider-type").value, $("ai-provider-search").value, $("ai-provider-enabled-only").checked],
  render: () => renderAIProviders(false),
  actions: renderAISelectionActions,
  busy: () => aiProviderStatusSaving
});

function renderAISelectionActions(providers) {
  return [
    aiSelection.action(
      m("ui.query_balance"),
      providers.filter(
        (provider) =>
          aiBalanceUpstream(provider) && !adminUIState.aiBalanceLoading.has(provider.auth_index) &&
          $("ai-provider-query-balances").dataset.loading !== "true"
      ),
      querySelectedAIBalances
    ),
    aiSelection.action(
      m("ui.enable"),
      providers.filter((provider) => provider.disabled),
      (selected) => setAIProvidersEnabled(selected, true, aiSelection)
    ),
    aiSelection.action(
      m("ui.disable"),
      providers.filter((provider) => !provider.disabled),
      (selected) => setAIProvidersEnabled(selected, false, aiSelection)
    )
  ];
}

async function fetchAIProviders(account) {
  const targets = account ? null : await queueAdminData("configuration", syncConfiguredProviders);
  const range = selectedRange(account);
  const path = "/ai-providers?" + new URLSearchParams(range);
  const result = account ? await accountAPI(path) : await plugin("GET", path);
  return result.providers.map((provider) => {
    const target = targets?.get(provider.ref);
    return {
      ...provider,
      editor: target?.editor || { weightEditable: false, sharedPriority: false },
      usage_query: provider.auth_index ? { credential_ref: provider.ref, ...range } : null
    };
  });
}

function readAIProviders(account, reload = false) {
  const role = account ? "account" : "admin";
  return aiProvidersQueries[role].read(JSON.stringify(sharedTimeRanges[role]), () => fetchAIProviders(account), reload);
}

function filteredAIProviders(account) {
  const prefix = account ? "account-" : "";
  const type = $(prefix + "ai-provider-type").value;
  const search = $(prefix + "ai-provider-search").value.trim().toLocaleLowerCase();
  return (aiProvidersQueries[account ? "account" : "admin"].value || []).filter((provider) => {
    if ($(prefix + "ai-provider-enabled-only").checked && provider.disabled) return false;
    if (type && provider.type !== type) return false;
    if (!search) return true;
    return [provider.note, provider.base_url, provider.type, provider.name, provider.key_preview].some((value) =>
      value.toLocaleLowerCase().includes(search)
    );
  });
}

function renderAIProviders(account) {
  const body = $((account ? "account-" : "") + "ai-providers-body");
  const bulk = account ? null : $("ai-provider-query-balances");
  if (renderDataPending(body, [aiProvidersQueries[account ? "account" : "admin"]])) {
    if (bulk) bulk.disabled = true;
    return;
  }
  const providers = filteredAIProviders(account);
  if (bulk)
    bulk.disabled = bulk.dataset.loading === "true" || aiSelection.running || aiProviderStatusSaving || !providers.some(aiBalanceUpstream);
  renderCollection(body, providers, {
    selection: account ? null : aiSelection,
    headers: [
      m("ui.ai_providers"),
      ...(!account ? [{ label: "", class: "credential-edit-column" }] : []),
      ...credentialMetricHeaders(),
      { label: m("ui.balance"), class: "credential-quota-column" },
      ...(!account ? [m("ui.actions")] : [])
    ],
    attrs: { class: "credential-table" },
    empty: m("ui.no_ai_providers_match_the_filters"),
    render: (provider) => renderAIProviderRow(provider, account)
  });
}

function renderAIProviderRow(provider, account) {
  const upstream = aiBalanceUpstream(provider);
  const loading = adminUIState.aiBalanceLoading.has(provider.auth_index);
  const baseURL = provider.base_url.replace(/^https:\/\//i, "");
  const title = provider.note || baseURL || m("ui.default_base_url");
  return el(
    "tr",
    { class: "credential-row" + (provider.disabled ? " disabled" : "") },
    el(
      "td",
      { class: "credential-identity-cell" },
      el(
        "div",
        { class: "credential-head" },
        providerIcon(AI_PROVIDER_TYPES.find((kind) => kind.type === provider.type)?.brand),
        el(
          "div",
          { class: "credential-identity" },
          el("div", {
            class: "credential-title" + (provider.note ? " mask-blur" : baseURL ? "" : " muted"),
            "data-tip": title,
            "data-tip-overflow": "",
            text: title
          }),
          el(
            "div",
            { class: "credential-meta ai-provider-meta" },
            provider.name &&
              el("span", {
                class: "tag plain credential-name-tag",
                "data-tip": provider.name,
                "data-tip-overflow": "",
                text: provider.name
              }),
            el(
              "span",
              { class: "tag plain ai-provider-key" },
              el("span", {
                class: provider.key_preview ? "mono mask-blur" : "",
                text: provider.key_preview || m("ui.no_api_key_configured")
              })
            ),
            credentialRoutingTag(provider)
          )
        )
      )
    ),
    !account && credentialEditCell(() => editAIProvider(provider), aiProviderStatusSaving || aiSelection.running),
    renderCredentialMetrics(provider, modelUsageTooltip(account, provider.usage_query)),
    el("td", { class: "credential-quota-cell" }, renderAIBalance(provider, account)),
    !account &&
      el(
        "td",
        { class: "credential-actions-cell" },
        el(
          "div",
          { class: "credential-actions" },
          el(
            "button",
            {
              type: "button",
              class: "labeled-icon-button round-icon-button" + (loading ? " loading" : ""),
              disabled:
                !upstream || loading || aiSelection.running || aiProviderStatusSaving ||
                $("ai-provider-query-balances").dataset.loading === "true",
              "aria-label": m("ui.query_balance"),
              "data-tip": upstream ? m("ui.query_balance") : m("ui.balance_queries_are_not_supported_for_this_provider"),
              onclick: () => guard(() => queryAIBalance(provider))
            },
            actionIcon("refresh")
          ),
          switchControl({
            input: el("input", {
              type: "checkbox",
              role: "switch",
              checked: !provider.disabled,
              "aria-label": m(provider.type === "openai-compatibility" ? "ui.provider_group_switch" : "ui.enable"),
              "data-tip": m(provider.type === "openai-compatibility" ? "ui.provider_group_switch" : "ui.enable"),
              disabled: aiProviderStatusSaving || aiSelection.running,
              onchange: (event) => {
                const enabled = event.target.checked;
                event.target.checked = !provider.disabled;
                guard(() => setAIProvidersEnabled([provider], enabled));
              }
            })
          })
        )
      )
  );
}

function editAIProvider(provider) {
  openCredentialEditor(
    m("ui.edit_ai_provider"),
    provider,
    async (changes) => { try { await writeConfiguredProvider(provider, changes); } finally { await readAIProviders(false, true); } },
    { weightEditable: provider.editor.weightEditable, hint: provider.editor.sharedPriority ? m("ui.provider_priority_shared") : "" }
  );
}

let aiProviderStatusSaving = false;

async function setAIProvidersEnabled(providers, enabled, selection = null) {
  if (aiProviderStatusSaving) return;
  const generation = sessionGeneration;
  const context = selection?.context;
  aiProviderStatusSaving = true;
  renderAIProviders(false);
  try {
    const targets = await readConfiguredProviderTargets(providers);
    if (generation !== sessionGeneration || (selection && (activeTableSelection !== selection || selection.context !== context)))
      throw new StaleRequestError();
    const group = (provider) => targets.get(provider.ref).group;
    const save = (provider) => setConfiguredProviderEnabled(targets.get(provider.ref), enabled);
    if (selection) await selection.run(providers, save, { groupBy: group, refresh: () => readAIProviders(false, true) });
    else {
      try { for (const provider of new Map(providers.map((item) => [group(item), item])).values()) await save(provider); } finally {
        if (generation === sessionGeneration) await readAIProviders(false, true);
      }
    }
  } finally {
    aiProviderStatusSaving = false;
    if (generation === sessionGeneration) renderAIProviders(false);
  }
}

for (const account of [false, true]) {
  const prefix = account ? "account-" : "";
  $(prefix + "ai-provider-type").append(...AI_PROVIDER_TYPES.map((kind) => el("option", { value: kind.type, text: kind.type })));
  for (const filter of [$(prefix + "ai-provider-enabled-only"), $(prefix + "ai-provider-type")]) {
    filter.addEventListener("change", () => {
      saveChoice(account ? "account" : "admin", filter.id);
      renderAIProviders(account);
    });
  }
  $(prefix + "ai-provider-search").addEventListener("input", () => renderAIProviders(account));
}
resetAIBalanceBatch();
$("ai-provider-query-balances").onclick = () => guard(queryFilteredAIBalances);
