function renderAccountProfile() {
  const profile = resources.account.profile.value;
  const tracked = profile?.tracked === true;
  $("account-untracked").classList.toggle("hidden", !profile || tracked);
  $("account-content").classList.toggle("hidden", !tracked);
  if (tracked) $("account-identity").replaceChildren(displayText(apiKeyIdentity(profile.identity || {}).text));
  else closeSharedTimePopover(true);
  return tracked;
}

function renderAccountSubscription() {
  const data = resources.account.subscription;
  const subscription = data.value?.subscription || {};
  const concurrency = data.value?.concurrency || {};
  setText(
    $("account-concurrency"),
    !data.value ? "—" : int(concurrency.current) + " / " + (Number(concurrency.limit || 0) > 0 ? int(concurrency.limit) : "∞")
  );
  setText(
    $("account-plan-name"),
    !data.value
      ? m("ui.subscription_quota")
      : displayLabel(subscription.name || (subscription.unlimited ? m("ui.no_subscription_plan") : m("ui.subscription_quota")))
  );
  $("account-plan-status").className = "tag " + (subscription.blocked ? "bad" : "plain");
  setText(
    $("account-plan-status"),
    !data.value ? "" : subscription.unlimited ? m("ui.unlimited") : subscription.blocked ? m("ui.quota_exhausted") : m("ui.active")
  );
  const target = $("account-windows");
  if (renderDataPending(target, [data], m("ui.loading_subscription_status"))) return;
  target.replaceChildren(renderQuotaWindows(subscription, true));
}

function renderAccountModels() {
  const data = resources.account;
  const target = $("account-models");
  if (renderDataPending(target, [data.models, data.catalog, data.routing], m("ui.loading_model_prices"))) return;
  const access = data.routing.value;
  const directory = data.models.value;
  const modelScope = new Set(access.models.map((model) => model.toLowerCase()));
  const deniedModels = new Set((access.denied_models || []).map((model) => model.toLowerCase()));
  // A host that filters model catalogs has already applied every rule to the key's directory.
  const models = (
    access.catalog_filtered
      ? directory
      : !modelScope.size ? directory : directory.length ? directory.filter((model) => modelScope.has(model.toLowerCase())) : access.models
  )
    .filter((model) => !deniedModels.has(model.toLowerCase()))
    .slice()
    .sort(compareModelId);
  renderCollection(target, models, { attrs: { class: "display-contents" }, empty: m("ui.no_model_prices"), render: renderAccountModel });
}

function renderAccountRouting() {
  const data = resources.account.routing;
  const target = $("account-access");
  if (renderDataPending(target, [data], m("ui.loading_routing_permissions"))) return;
  target.replaceChildren(renderAccountAccess(data.value));
}

function renderAccountAccess(access) {
  const accountCredentialLabel = (item, state) => {
    const name = window.billingI18n.serverMessage(item.name_message, item.name);
    return {
      text: item.provider_wide
        ? credentialProviderLabel(item)
        : item.status === "missing"
          ? name ? maskedLabelText(name) : m("ui.the_selected_upstream_credential_is_currently_unavailable")
          : maskedTailText(item.provider_name, name || m("ui.credential_unavailable")),
      tag: item.provider_wide ? m("ui.entire_category") : "",
      iconName: credentialIcon(item),
      state: item.denied ? "deny" : state
    };
  };
  const deniedModels = new Set((access.denied_models || []).map((model) => model.toLowerCase()));
  const modelItems = access.models.map((text) => ({
    text,
    state: deniedModels.has(text.toLowerCase()) ? "deny" : "allow",
    iconName: "astroid"
  }));
  const allModels =
    access.credentials.length || access.denied_credentials.length ? m("ui.all_models_within_credential_access") : m("ui.all_models");
  if (!modelItems.length) modelItems.push({ text: allModels, iconName: "astroid" });
  for (const text of access.denied_models || [])
    if (!access.models.some((model) => model.toLowerCase() === text.toLowerCase()))
      modelItems.push({ text, state: "deny", iconName: "astroid" });
  const credentialItems = access.credentials.map((item) => accountCredentialLabel(item, "allow"));
  if (!credentialItems.length) credentialItems.push({ text: m("ui.all_credentials") });
  const credentialKey = (item) => JSON.stringify([item.source, item.provider, !!item.provider_wide, item.name]);
  const deniedCredentials = new Set(access.credentials.filter((item) => item.denied).map(credentialKey));
  for (const item of access.denied_credentials || []) {
    const key = credentialKey(item);
    if (deniedCredentials.has(key)) continue;
    deniedCredentials.add(key);
    credentialItems.push(accountCredentialLabel(item, "deny"));
  }
  const nodes = [
    el(
      "div",
      { class: "account-access-summary" },
      el(
        "div",
        { class: "inset-card" },
        el("div", { class: "account-access-scope-label", text: m("ui.model_access") }),
        settingsChipList(modelItems)
      ),
      el(
        "div",
        { class: "inset-card" },
        el("div", { class: "account-access-scope-label", text: m("ui.credential_access") }),
        settingsChipList(credentialItems)
      )
    )
  ];
  if (access.warnings?.length)
    nodes.push(
      el(
        "div",
        { class: "account-access-warnings" },
        access.warnings.map((warning, index) =>
          el("span", { class: "tag warn", text: window.billingI18n.serverMessage(access.warning_messages?.[index], warning) })
        )
      )
    );
  return el("div", { class: "display-contents" }, nodes);
}

function renderAccountModel(model) {
  const candidate = resources.account.catalog.value.prices.find((price) => price.model_id.toLowerCase() === model.toLowerCase());
  const price = candidate?.source === "none" ? null : candidate;
  const fields = [[m("ui.input"), "input_per_1m"], [m("ui.output"), "output_per_1m"]];
  const longContext = price?.long_context;
  for (const [label, field] of [[m("ui.cache_read"), "cache_read_per_1m"], [m("ui.cache_write"), "cache_write_per_1m"]]) {
    if (price?.[field] != null || longContext?.[field] != null) fields.push([label, field]);
  }
  const threshold = longContext ? ">" + tokenThreshold(longContext.threshold_input_tokens) : "";
  const priceRows = price
    ? fields.map(([label, field]) => ({
        label,
        base: price[field] == null ? null : Number(price[field]),
        long: longContext?.[field] == null ? null : Number(longContext[field])
      }))
    : [];
  const priceRow = (item) =>
    el(
      "div",
      { class: "account-model-price-row" + (longContext ? " tiered" : "") },
      el("span", { class: "account-model-price-label" }, item.label),
      el("strong", { class: "mono account-model-base-price" }, item.base == null ? "—" : money(item.base, price.currency)),
      longContext
        ? el(
            "span",
            { class: "account-model-long-price" },
            item.long != null
              ? [
                  el("span", { class: "account-model-threshold" }, threshold),
                  el("strong", { class: "mono" }, money(item.long, price.currency))
                ]
              : el("span", { class: "muted" }, "—")
          )
        : null
    );
  return el(
    "div",
    { class: "account-model-card" },
    el("b", { class: "mono account-model-name", "data-tip": model, "data-tip-overflow": "" }, model),
    price
      ? el("div", { class: "account-model-price-list" }, priceRows.map(priceRow))
      : el("span", {
          class: "muted small",
          text: resources.account.profile.value.can_use_unpriced_models
            ? m("ui.no_price_configured_not_billed")
            : m("ui.no_price_configured_currently_unavailable")
        })
  );
}
