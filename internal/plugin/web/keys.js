const CONCURRENCY_OPTIONS = [0, 1, 2, 3, 5, 10, 20, 50];
const CUSTOM_CONCURRENCY = "__custom__";
const CALLER_SCOPE_SALT = "cli-proxy-api:caller-scope:v1\u0000";

const keySelection = createTableSelection({
  id: (key) => key.scope,
  label: (key) => apiKeyIdentity(key).primary,
  eligible: (key) => !key.deleted_at,
  context: () => [$("key-filter").value.trim(), $("keys-include-deleted").checked],
  render: renderKeys,
  actions: renderKeySelectionActions,
  busy: () => adminUIState.keySubmissions.size > 0
});

function callerScope(key) { return sha256Hex(CALLER_SCOPE_SALT + key.trim()); }

// A complete API key finds its row through its scope.
const keySearch = { term: "", scope: "" };

async function configuredAPIKey(view) {
  for (const value of await fetchConfiguredAPIKeys()) { if ((await callerScope(value)) === view.scope) return value.trim(); }
  throw new UIError(m("ui.this_api_key_no_longer_exists_refresh_the_list"));
}

async function saveConcurrency(view, raw) {
  const limit = concurrencyLimit(raw);
  await submitKeyChange(view.scope, async () => {
    await mutateAdmin("POST", "/keys/concurrency", { scope: view.scope, concurrency_limit: limit });
    notify(limit ? m("ui.concurrency_limit_updated_to_value", { v0: limit }) : m("ui.concurrency_limit_removed"));
  });
}

function concurrencyLimit(raw) {
  const value = String(raw ?? "").trim();
  const limit = Number(value);
  if (!/^\d+$/.test(value) || !Number.isSafeInteger(limit) || limit > 10000)
    throw new UIError(m("ui.concurrency_limit_must_be_an_integer_from_0_to_10000"));
  return limit;
}

function writeKeyPlan(scope, planID) {
  return writeAdmin("POST", planID ? "/keys/bind" : "/keys/unbind", { scope, ...(planID ? { plan_id: planID } : {}) });
}

function renderKeySelectionActions(keys) {
  const plans = resources.admin.plans.value;
  return [
    keySelection.action(m("ui.reset_quota"), keys.filter((key) => !key.unlimited), resetSelectedKeys),
    keySelection.select(
      m("ui.subscription_plans"),
      [
        el("option", { value: "", text: m("ui.unlimited") }),
        ...(plans || []).map((plan) => el("option", { value: plan.id, text: plan.name || plan.id }))
      ],
      (planID) =>
        keySelection.run(keys, (key) => writeKeyPlan(key.scope, planID), {
          keepSelection: true,
          success: m("ui.subscription_plan_updated")
        }),
      !plans
    ),
    keySelection.select(
      m("ui.concurrency_limit"),
      [
        ...CONCURRENCY_OPTIONS.map((limit) => el("option", { value: String(limit), text: limit === 0 ? m("ui.unlimited") : String(limit) })
        ),
        el("option", { value: CUSTOM_CONCURRENCY, text: m("ui.enter_manually") })
      ],
      (value) =>
        value === CUSTOM_CONCURRENCY
          ? openActionDialog(m("ui.set_concurrency_limit"), m("ui.enter_an_integer_from_0_to_10000_use_0_for_unlimited"), "0", (raw) =>
              setSelectedConcurrency(keys, raw)
            )
          : setSelectedConcurrency(keys, value)
    ),
    keySelection.action(m("ui.routing_rules"), keys, (selected) =>
      editKeyRoutes(selected, (bindings) =>
        keySelection.run(selected, (key) => writeAdmin("PUT", "/keys/routes", { scope: key.scope, bindings }), {
          success: m("ui.api_key_routing_rules_saved")
        })
      )
    )
  ];
}

function setSelectedConcurrency(keys, raw) {
  const limit = concurrencyLimit(raw);
  return keySelection.run(keys, (key) => writeAdmin("POST", "/keys/concurrency", { scope: key.scope, concurrency_limit: limit }), {
    keepSelection: true,
    success: limit ? m("ui.concurrency_limit_updated_to_value", { v0: limit }) : m("ui.concurrency_limit_removed")
  });
}

function resetSelectedKeys(keys) {
  const shared = keys.filter((key) => key.windows[0]?.cycle_anchor_at).length;
  const note =
    shared === keys.length
      ? m("ui.clear_usage_in_all_windows_and_keep_the_scheduled_reset_times")
      : shared === 0
        ? m("ui.all_quota_cycles_will_end_new_cycles_start_when_the_next_request_is_admitted")
        : m("ui.batch_reset_mixed_cycles");
  return openActionDialog(m("ui.batch_reset_subscription_quota", { count: keys.length }), note, undefined, () =>
    keySelection.run(keys, (_, group) => writeAdmin("POST", "/keys/reset", { mode: "all", scopes: group.map((key) => key.scope) }), {
      groupBy: () => "reset",
      success: m("ui.subscription_quota_reset")
    })
  );
}

async function submitKeyChange(scope, save) {
  const owner = adminUIState;
  if (owner.keySubmissions.has(scope)) return;
  owner.keySubmissions.add(scope);
  renderKeys();
  try { await save(); } finally {
    owner.keySubmissions.delete(scope);
    if (owner === adminUIState) renderKeys();
  }
}

function renderKeys() {
  if (currentRole !== "admin" || activeTab(false) !== "keys") return false;
  closeHoverTooltip();
  if (renderDataPending($("keys-body"), [resources.admin.keys], m("ui.loading"), "empty section-loading")) return;
  const plans = resources.admin.plans.value;
  const term = $("key-filter").value.trim();
  const scope = keySearch.term === term ? keySearch.scope : "";
  const keys = resources.admin.keys.value.filter(
    (view) =>
      (!view.deleted_at || $("keys-include-deleted").checked) &&
      (view.scope === scope || matchesTerm([apiKeyIdentity(view).text, view.plan_name, view.scope].filter(Boolean).join(" "), term))
  );
  renderCollection($("keys-body"), keys, {
    selection: keySelection,
    headers: [
      "",
      "API Key",
      m("ui.subscription_plans"),
      m("ui.routing_rules"),
      m("ui.concurrency_limit"),
      m("ui.subscription_quota"),
      m("ui.status"),
      m("ui.actions")
    ],
    render: (view, index) => {
      const deleted = !!view.deleted_at;
      const saving = adminUIState.keySubmissions.has(view.scope) || keySelection.running;
      const name = apiKeyIdentity(view).primary;
      const select = el(
        "select",
        {
          name: "plan_id",
          disabled: deleted || saving || !plans,
          "data-tip": !plans ? resources.admin.plans.error || m("ui.loading_subscription_plans") : null,
          "aria-label": m("ui.subscription_plan_for_value", { v0: name }),
          onchange: (event) =>
            guard(async () => {
              const planID = event.target.value;
              event.target.value = view.plan_id || "";
              await submitKeyChange(view.scope, async () => {
                await writeKeyPlan(view.scope, planID);
                notify(m("ui.subscription_plan_updated"));
              });
            })
        },
        el("option", { value: "", text: m("ui.unlimited") }),
        view.plan_id && !(plans || []).some((plan) => plan.id === view.plan_id)
          ? el("option", { value: view.plan_id, selected: true, text: displayLabel(view.plan_name || view.plan_id) })
          : null,
        (plans || []).map((plan) => el("option", { value: plan.id, selected: plan.id === view.plan_id }, displayLabel(plan.name || plan.id))
        )
      );

      const usage = renderQuotaWindows(view);

      const currentConcurrencyLimit = Number(view.concurrency_limit || 0);
      const concurrencyValues = CONCURRENCY_OPTIONS.includes(currentConcurrencyLimit)
        ? CONCURRENCY_OPTIONS
        : [...CONCURRENCY_OPTIONS, currentConcurrencyLimit].sort((a, b) => a - b);
      const concurrencySelect = el(
        "select",
        {
          "aria-label": m("ui.maximum_concurrent_requests_for_value", { v0: name }),
          disabled: deleted || saving,
          onchange: (event) =>
            guard(async () => {
              const value = event.target.value;
              event.target.value = String(currentConcurrencyLimit);
              if (value === CUSTOM_CONCURRENCY) {
                openActionDialog(
                  m("ui.set_concurrency_limit"),
                  m("ui.enter_an_integer_from_0_to_10000_use_0_for_unlimited"),
                  String(currentConcurrencyLimit),
                  (raw) => saveConcurrency(view, raw)
                );
                return;
              }
              await saveConcurrency(view, value);
            })
        },
        concurrencyValues.map((limit) =>
          el("option", {
            value: String(limit),
            selected: limit === currentConcurrencyLimit,
            text: limit === 0 ? m("ui.unlimited") : String(limit)
          })
        ),
        el("option", { value: CUSTOM_CONCURRENCY, text: m("ui.enter_manually") })
      );
      const concurrency = el(
        "div",
        { class: "concurrency-editor" },
        el("span", { class: "native-select" }, concurrencySelect),
        el("span", { class: "small muted", text: m("ui.current_value", { v0: int(view.current_concurrency) }) })
      );

      return el(
        "tr",
        { class: deleted ? "key-deleted" : "" },
        el("td", { class: "key-index", text: "#" + (index + 1) }),
        el(
          "td",
          {},
          renderAPIKeyIdentity(view, {
            stacked: true,
            trailing: [
              copyButton(m("ui.copy_api_key"), () => configuredAPIKey(view), { disabled: deleted || !view.in_config }),
              !deleted && !view.in_config ? el("span", {}, displayText(m("ui.valueusage_tracking_only", { v0: DISPLAY_SEPARATOR }))) : null
            ]
          })
        ),
        el("td", {}, el("span", { class: "native-select key-plan-select" }, select)),
        el("td", {}, renderKeyRoutes(view)),
        el("td", {}, concurrency),
        el("td", {}, usage),
        el(
          "td",
          {},
          deleted
            ? el("span", {
                class: "tag plain",
                text: m("ui.deleted"),
                "data-tip": m("ui.removed_from_cpa_configuration_on_value", { v0: new Date(view.deleted_at).toLocaleString(locale()) })
              })
            : view.blocked
              ? el("span", { class: "tag bad", text: m("ui.quota_exhausted") })
              : el("span", { class: "tag " + (view.unlimited ? "plain" : "ok"), text: view.unlimited ? m("ui.unlimited") : m("ui.active") })
        ),
        el(
          "td",
          { class: "small" },
          el("button", {
            class: "link",
            text: m("ui.reset"),
            disabled: deleted || view.unlimited || saving,
            onclick: () =>
              openActionDialog(
                m("ui.reset_subscription_quota"),
                view.windows[0]?.cycle_anchor_at
                  ? m("ui.clear_usage_in_all_windows_and_keep_the_scheduled_reset_times")
                  : m("ui.all_quota_cycles_will_end_new_cycles_start_when_the_next_request_is_admitted"),
                undefined,
                async () => {
                  await mutateAdmin("POST", "/keys/reset", { mode: "all", scopes: [view.scope] });
                  notify(m("ui.subscription_quota_reset"));
                }
              )
          }),
          el("button", {
            class: "link",
            text: m("ui.label"),
            onclick: () =>
              openActionDialog(m("ui.label"), name, keyDirectory.resolve(view).label || "", async (label) => {
                await mutateAdmin("POST", "/keys/label", { scope: view.scope, label });
                notify(m("ui.label_saved"));
              })
          })
        )
      );
    }
  });
}

function renderKeyRoutes(view) {
  const bindings = view.route_bindings;
  const names = bindings.route_ids.map((id) => ({ iconName: "route", text: view.route_names[id] }))
    .concat(routingDimensionChips(bindings, "credential", view.credential_labels))
    .concat(routingDimensionChips(bindings, "model", view.credential_labels));
  const disabled = !!view.deleted_at || adminUIState.keySubmissions.has(view.scope) || keySelection.running;
  const tooltip = () => [
    el("div", {
      class: "hover-tooltip-heading",
      text: m("ui.full_routing_rulesvaluevalue_items", { v0: DISPLAY_SEPARATOR, v1: names.length })
    }),
    ...names.map((entry) => settingsChipTooltip(entry))
  ];
  return el(
    "div",
    { class: "key-route-cell" },
    el(
      "div",
      { class: "key-route-lines", ...hoverTooltipProps(tooltip, names.length > 3) },
      ...(names.length
        ? names.slice(0, 3).map((entry) => el("div", { class: "key-route-line" }, settingsChipList([entry])))
        : [el("div", { class: "key-route-line muted", text: m("ui.all_routes") })]),
      names.length > 3
        ? el("div", { class: "key-route-more", text: "…", "aria-label": m("ui.value_more", { v0: names.length - 3 }) })
        : null
    ),
    el(
      "div",
      { class: "key-route-actions compact-actions" },
      el("button", {
        type: "button",
        class: "link key-route-clear",
        text: m("ui.clear"),
        disabled: disabled || !names.length,
        "aria-label": m("ui.clear_routing_rules_for_value", { v0: apiKeyIdentity(view).primary }),
        onclick: () =>
          guard(() => {
            closeHoverTooltip();
            return openActionDialog(
              m("ui.clear_routing_rules_for_value_2", { v0: apiKeyIdentity(view).primary }),
              m("ui.remove_all_routing_rule_bindings_and_direct_model_and_credential_allowlists_and_denyl"),
              undefined,
              () =>
                submitKeyChange(view.scope, async () => {
                  await mutateAdmin("PUT", "/keys/routes", { scope: view.scope, bindings: {} });
                  notify(m("ui.api_key_routing_rules_cleared"));
                })
            );
          })
      }),
      el("button", {
        type: "button",
        class: "link key-route-edit",
        text: m("ui.edit"),
        disabled,
        "aria-label": m("ui.edit_routing_rules_for_value", { v0: apiKeyIdentity(view).primary }),
        onclick: () =>
          guard(() =>
            editKeyRoutes([view], async (bindings) => {
              await mutateAdmin("PUT", "/keys/routes", { scope: view.scope, bindings });
              notify(m("ui.api_key_routing_rules_saved"));
            })
          )
      })
    )
  );
}

/* Host panels know API keys only by hash, so each label source returns [hash, label] pairs with the hash it gives a key. */
async function importPanelLabels(load) {
  const generation = sessionGeneration;
  const { labels, fingerprint } = await load();
  const keys = await fetchConfiguredAPIKeys();
  const scopes = new Map();
  for (const key of keys) {
    const value = key.trim();
    if (value) scopes.set(await fingerprint(value), await callerScope(value));
  }
  if (generation !== sessionGeneration) throw new StaleRequestError();
  let imported = 0;
  let unchanged = 0;
  try {
    for (const [hash, alias] of labels) {
      if (generation !== sessionGeneration) throw new StaleRequestError();
      const scope = scopes.get(hash);
      if (!scope) continue;
      const label = alias.trim();
      const key = (resources.admin.keys.value || []).find((key) => key.scope === scope);
      if (key && (keyDirectory.resolve(key).label || "") === label) {
        unchanged++;
        continue;
      }
      await writeAdmin("POST", "/keys/label", { scope, label });
      imported++;
    }
  } catch (err) {
    if (err instanceof AuthError || err instanceof StaleRequestError) throw err;
    throw new UIError(m("ui.imported_value_labels_import_interrupted_value", { v0: imported, v1: err.message || err }));
  } finally { if (imported && generation === sessionGeneration) renderPage(); }
  const skipped = labels.length - imported - unchanged;
  notify(
    m("ui.imported_value_labelsvaluevalue", {
      v0: imported,
      v1: unchanged ? m("ui.value_unchanged", { v0: unchanged }) : "",
      v2: skipped ? m("ui.value_unmatched_labels_skipped", { v0: skipped }) : ""
    })
  );
}

async function cpampLabels() {
  const result = await api("GET", "/v0/management/api-key-aliases", null);
  return { labels: result.items.map((item) => [item.apiKeyHash.toLowerCase(), item.alias]), fingerprint: sha256Hex };
}

/* CPAMC keeps labels in browser storage under the API base it is connected to, which must be this server. */
function cpamcLabels() {
  const apiBase = panelStoredValue("cli-proxy-auth")?.state?.apiBase;
  let origin = "";
  try { origin = new URL(apiBase).origin; } catch (_) {}
  if (origin !== location.origin) throw new UIError(m("ui.cannot_read_cpamc_labels_open_cpamc_from_this_cpa_s_address_and_try_again"));
  const stored = panelStoredValue("api-key-names:v1:" + apiBase);
  const labels = Object.entries(stored && typeof stored === "object" ? stored : {}).filter(([, label]) => typeof label === "string");
  return { labels, fingerprint: (key) => sha256Hex(JSON.stringify(["api-key-name", apiBase, key])) };
}

$("key-filter").addEventListener("input", async () => {
  const term = $("key-filter").value.trim();
  const scope = await callerScope(term);
  if ($("key-filter").value.trim() !== term) return;
  Object.assign(keySearch, { term, scope });
  renderKeys();
});
$("keys-include-deleted").addEventListener("change", () => {
  saveChoice("admin", "keys-include-deleted");
  renderKeys();
});

$("keys-import-labels").onclick = () => {
  const [panel, load] =
    document.documentElement.getAttribute("data-cpamp-plugin-host") === "true" ? ["CPAMP", cpampLabels] : ["CPAMC", cpamcLabels];
  openActionDialog(
    m("ui.import_value_labels", { v0: panel }),
    m("ui.import_value_api_key_labels_and_overwrite_existing_labels_for_matching_keys_unmatched", { v0: panel }),
    undefined,
    () => importPanelLabels(load)
  );
};
$("keys-reset-all").onclick = () =>
  openActionDialog(
    m("ui.reset_all_subscription_quotas"),
    m("ui.clear_usage_for_all_non_deleted_api_keys_bound_to_a_plan_shared_cycles_keep_their_res"),
    undefined,
    async () => {
      const result = await mutateAdmin("POST", "/keys/reset", { mode: "global" });
      notify(result.keys ? m("ui.reset_quotas_for_value_api_keys", { v0: result.keys }) : m("ui.no_quotas_to_reset"));
    }
  );
