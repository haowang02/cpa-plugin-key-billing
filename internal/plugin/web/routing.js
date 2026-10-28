let keyRouteSelection = null;

function editKeyRoutes(views, save) {
  openEditor("route-picker", "route", (data) => {
    const keys = views.map((view) => data.keys.find((key) => key.scope === view.scope));
    if (keys.some((key) => !key || key.deleted_at)) throw new UIError(m("ui.this_api_key_no_longer_exists_refresh_the_list"));
    const key = keys.length === 1 ? keys[0] : null;
    keyRouteSelection = {
      routeIDs: new Set(key?.route_bindings.route_ids || []),
      routeNames: key?.route_names || {},
      ...routingEditorState(key?.route_bindings, "route-picker-enabled-only")
    };
    $("route-picker-search").value = "";
    $("route-picker-identity").classList.toggle("mask-blur", !!key);
    setText($("route-picker-identity"), key ? apiKeyIdentity(key).text : m("ui.batch_routes_replace", { count: keys.length }));
    renderKeyRouteList();
    return save;
  });
}

function routeBindingChoice(selection, id, label, rerender) {
  return el(
    "label",
    { class: "routing-option", "data-routing-binding": id },
    checkboxControl({
      input: el("input", {
        type: "checkbox",
        value: id,
        "data-routing-focus": "route:" + id,
        checked: selection.routeIDs.has(id),
        onchange: (event) => {
          if (event.target.checked) selection.routeIDs.add(id); else selection.routeIDs.delete(id);
          rerender();
        }
      })
    }),
    actionIcon("route"),
    el("span", {}, displayText(label))
  );
}

function renderKeyRouteList() {
  renderRoutingList(
    $("route-picker-list"),
    keyRouteSelection,
    editors.get($("route-picker")).data,
    $("route-picker-search").value,
    renderKeyRouteList
  );
}

function renderRouteRuleList() {
  renderRoutingList(
    $("route-rule-list"),
    routeRuleSelection,
    editors.get($("route-dialog")).data,
    $("route-rule-search").value,
    renderRouteRuleList
  );
}

function routingChoiceGroups(selection, data, term) {
  const groups = [];
  if (selection.routeIDs) {
    const routes = data.routes.slice();
    // The route list is a separate read; a binding it missed still carries its name on the key.
    for (const id of selection.routeIDs) {
      if (!routes.some((route) => route.id === id)) routes.push({ id, name: selection.routeNames[id] });
    }
    const choices = routes.filter((route) => matchesTerm(route.name, term))
      .map((route) => ({ kind: "route", value: route.id, label: route.name }));
    groups.push(["routes", m("ui.routing_rules"), choices]);
  }
  groups.push(
    ["credentials", m("ui.upstream_credentials"), routeCredentialChoices(selection, data.credentials, term)],
    ["models", m("ui.model"), routeModelChoices(selection, data.models, term)]
  );
  return groups;
}

function renderRoutingList(list, selection, data, search, rerender) {
  const term = search.trim().toLowerCase();
  const scrollTop = list.scrollTop;
  const focusKey = list.contains(document.activeElement) ? document.activeElement.dataset.routingFocus : null;
  if (term && !selection.beforeSearch) {
    selection.beforeSearch = selection.expanded;
    selection.expanded = { routes: true, credentials: true, models: true };
  } else if (!term && selection.beforeSearch) {
    selection.expanded = selection.beforeSearch;
    selection.beforeSearch = null;
  }
  const sections = routingChoiceGroups(selection, data, term).map(([kind, title, choices]) => {
    const options = choices.map((item) =>
      item.kind === "route" ? routeBindingChoice(selection, item.value, item.label, rerender) : routeChoiceRow(selection, item, rerender)
    );
    return routingSection(selection, kind, title, options, rerender);
  });
  list.replaceChildren(...sections);
  list.scrollTop = scrollTop;
  if (focusKey)
    [...list.querySelectorAll("[data-routing-focus]")].find((control) => control.dataset.routingFocus === focusKey)
      ?.focus({ preventScroll: true });
}

function routingSection(selection, kind, title, options, rerender) {
  const selected =
    kind === "routes"
      ? selection.routeIDs.size
      : kind === "credentials" ? selection.credential.size + selection.provider.size : selection.model.size;
  const expanded = !!selection.expanded[kind];
  return el(
    "div",
    { class: "routing-section", "data-routing-section": kind },
    el(
      "div",
      {
        class: "routing-section-header",
        onclick: (event) => {
          if (event.target.closest(".compact-toggle")) return;
          event.currentTarget.querySelector(".routing-section-toggle").focus({ preventScroll: true });
          selection.expanded[kind] = !expanded;
          rerender();
        }
      },
      el(
        "button",
        {
          type: "button",
          class: "routing-section-toggle",
          "aria-label": m(expanded ? "ui.collapse_section" : "ui.expand_section", { section: title }),
          "aria-expanded": String(expanded),
          "data-routing-focus": "section:" + kind
        },
        el("span", { class: "routing-chevron", "aria-hidden": "true" }, actionIcon("chevron")),
        el("span", { text: title })
      ),
      kind === "credentials"
        ? compactEnabledToggle(selection.enabledOnly, (checked) => {
            selection.enabledOnly = checked;
            saveViewPreference("admin", selection.preference, checked);
            rerender();
          })
        : null,
      el("span", { class: "routing-section-count", text: selected + "/" + options.length })
    ),
    expanded
      ? el(
          "div",
          {},
          ...(options.length ? options : [el("div", { class: "routing-empty", text: m("ui.no_value_available", { v0: title }) })])
        )
      : null
  );
}

function compactEnabledToggle(checked, onChange) {
  return el(
    "label",
    { class: "toggle-filter compact-toggle" },
    switchControl({
      input: el("input", {
        type: "checkbox",
        role: "switch",
        "data-routing-focus": "enabled-only",
        checked,
        onchange: (event) => onChange(event.target.checked)
      })
    }),
    el("span", { text: m("ui.enabled_only") })
  );
}

function routingDimensionChips(rule, kind, labels) {
  const items = [];
  for (const denied of [false, true]) {
    const prefix = denied ? "denied_" : "";
    const state = denied ? "deny" : "allow";
    if (kind === "model") items.push(...(rule[prefix + "models"] || []).map((text) => ({ text, state, iconName: "astroid" })));
    else {
      items.push(
        ...(rule[prefix + "credential_providers"] || []).map((item) => ({
          text: credentialProviderLabel(item),
          state,
          tag: m("ui.entire_category"),
          iconName: credentialIcon(item)
        }))
      );
      items.push(
        ...(rule[prefix + "credential_ids"] || []).map((ref) => {
          const credential = (resources.admin.credentials.value || []).find((item) => item.ref === ref);
          return {
            text: credential
              ? maskedTailText(credential.provider_name, credentialName(credential))
              : labels?.[ref]
                ? maskedLabelText(labels[ref])
                : maskedTailText(m("ui.selected_upstream_credentialvaluevalue", { v0: "", v1: "" }), ref.slice(-8)),
            state,
            iconName: credential ? credentialIcon(credential) : "key"
          };
        })
      );
    }
  }
  return items;
}

function renderRoutes() {
  if (renderDataPending($("routes-body"), [resources.admin.routes, resources.admin.keys], m("ui.loading"), "empty section-loading")) return;
  const routes = resources.admin.routes.value.map((route) => ({
    route,
    boundKeys: resources.admin.keys.value.filter((key) => key.route_bindings.route_ids.includes(route.id))
  }));
  renderCollection($("routes-body"), routes, {
    attrs: { class: "event-list settings-card-list", role: "list" },
    empty: m("ui.no_routing_rules"),
    render: ({ route, boundKeys }) => {
      const fields = [
        ["model", m("ui.model_access"), m("ui.all_models"), "astroid"],
        ["credential", m("ui.credential_access"), m("ui.all_credentials"), "key"]
      ].map(([kind, label, fallback, iconName]) => {
        const items = routingDimensionChips(route.rule, kind, route.credential_labels);
        if (items.length && !items.some((item) => item.state === "allow")) items.unshift({ text: fallback, iconName });
        return settingsEntryField(label, settingsChipList(items, fallback));
      });
      return settingsCard(
        route.name,
        [...fields, boundKeyField(boundKeys)],
        () => editRoute(route),
        () =>
          openActionDialog(
            m("ui.delete_routing_rule_value", { v0: route.name }),
            boundKeys.length
              ? m("ui.remove_this_rule_from_value_of_these_value_non_deleted_keys_will_have_unrestricted_mo", {
                  v0: bindingSummary(boundKeys),
                  v1: route.fully_unrestricted_keys || 0
                })
              : m("ui.this_action_cannot_be_undone"),
            undefined,
            async () => {
              await mutateAdmin("DELETE", "/routes?id=" + encodeURIComponent(route.id));
              notify(m("ui.routing_rule_deleted"));
            }
          )
      );
    }
  });
}

let routeRuleSelection;

function routeProviderChoices(credentials, selected) {
  const providers = [];
  for (const item of credentials) {
    const existing = providers.find((provider) => sameCredentialProvider(provider, item));
    if (existing) { existing.available ||= !item.disabled && !item.unavailable; } else {
      providers.push({ source: item.source, provider: item.provider, available: !item.disabled && !item.unavailable });
    }
  }
  for (const item of selected) {
    if (providers.some((provider) => sameCredentialProvider(provider, item))) continue;
    providers.push({ ...item, available: false });
  }
  return providers.sort(compareCredentialProviders);
}

const ROUTING_FIELDS = { model: "models", credential: "credential_ids", provider: "credential_providers" };

function routingSelection(rule = {}) {
  const selection = Object.fromEntries(Object.keys(ROUTING_FIELDS).map((kind) => [kind, new Map()]));
  for (const [kind, field] of Object.entries(ROUTING_FIELDS)) {
    for (const [prefix, state] of [["", "allow"], ["denied_", "deny"]]) {
      for (const value of rule[prefix + field] || []) setRouteChoice(selection, kind, value, state);
    }
  }
  return selection;
}

function routingPayload(selection) {
  const rule = {};
  for (const [kind, field] of Object.entries(ROUTING_FIELDS)) {
    rule[field] = [];
    rule["denied_" + field] = [];
    for (const { value, state } of selection[kind].values()) { rule[(state === "deny" ? "denied_" : "") + field].push(value); }
  }
  return rule;
}

function routingEditorState(rule, preference) {
  return {
    ...routingSelection(rule),
    expanded: { routes: true, credentials: true, models: true },
    enabledOnly: viewPreference("admin", preference, true),
    preference,
    beforeSearch: null
  };
}

function routeChoiceKey(kind, value) { return (kind === "provider" ? value.source + ":" + value.provider : value).toLowerCase(); }

function selectedRouteValues(selection, kind) { return [...selection[kind].values()].map((item) => item.value); }

function routeChoiceState(selection, kind, value) { return selection[kind].get(routeChoiceKey(kind, value))?.state || "none"; }

function setRouteChoice(selection, kind, value, state) {
  const key = routeChoiceKey(kind, value);
  if (state === "none") selection[kind].delete(key);
  else selection[kind].set(key, { value: kind === "provider" ? { source: value.source, provider: value.provider } : value, state });
}

function routeChoiceRow(selection, item, rerender) {
  const { kind, value, label, text, iconName, badge, note } = item;
  const state = routeChoiceState(selection, kind, value);
  const next = { none: "allow", allow: "deny", deny: "none" }[state];
  const names = { none: m("ui.not_selected"), allow: m("ui.allowlist"), deny: m("ui.denylist") };
  const key = kind + ":" + (kind === "provider" ? value.source + ":" + value.provider : value);
  const source = kind === "credential" && item.source ? credentialSourceLabel(item.source) : null;
  const ariaLabel = m("ui.valuevalue_value_click_to_switch_to_value", {
    v0: source ? m("ui.join", { v0: source, v1: DISPLAY_SEPARATOR, v2: "" }) : "",
    v1: label,
    v2: names[state],
    v3: names[next]
  });
  return el(
    "button",
    {
      type: "button",
      class: "routing-option route-choice",
      "data-state": state,
      "data-routing-choice": key,
      "data-routing-focus": key,
      tooltip: () => [
        el(
          "div",
          { class: "hover-tooltip-body" },
          source ? m("ui.join", { v0: source, v1: DISPLAY_SEPARATOR, v2: "" }) : null,
          chipText(text || label),
          "\n",
          m("ui.metric_value", { v0: m("ui.current_state"), v1: names[state] }),
          "\n",
          m("ui.metric_value", { v0: m("ui.click_to_switch_to"), v1: names[next] })
        )
      ],
      "aria-label": ariaLabel,
      onclick: () => {
        setRouteChoice(selection, kind, value, next);
        rerender();
      }
    },
    checkboxControl({ state }),
    iconName ? actionIcon(iconName) : null,
    el("span", { class: "route-choice-name" + (kind === "model" ? " mono" : "") }, text || displayText(label)),
    kind === "provider" ? el("span", { class: "tag warn", text: m("ui.entire_category") }) : null,
    badge ? el("span", { class: "tag plain", text: badge }) : null,
    note ? el("span", { class: "route-choice-note", text: note }) : null
  );
}

function routeCredentialChoices(selection, credentials, term) {
  const selectedProviders = selectedRouteValues(selection, "provider");
  const options = routeProviderChoices(credentials, selectedProviders).filter(
    (item) =>
      (!selection.enabledOnly || item.available || routeChoiceState(selection, "provider", item) !== "none") &&
      matchesTerm(m("ui.value_entire_category", { v0: credentialProviderLabel(item) }), term)
  )
    .map((item) => ({
      kind: "provider",
      value: item,
      label: credentialProviderLabel(item),
      iconName: credentialIcon(item),
      badge: item.available ? "" : m("ui.none_available")
    }));
  for (const item of credentials.filter(
    (item) =>
      (!selection.enabledOnly || !item.disabled || routeChoiceState(selection, "credential", item.ref) !== "none") &&
      matchesTerm(credentialLabel(item), term)
  )
    .sort(compareCredentials)) {
    options.push({
      kind: "credential",
      value: item.ref,
      label: credentialLabel(item),
      text: maskedTailText(item.provider_name, credentialName(item)),
      source: item.source,
      iconName: credentialIcon(item),
      badge: item.disabled ? m("ui.disabled") : item.unavailable ? m("ui.unavailable") : ""
    });
  }
  for (const ref of selectedRouteValues(selection, "credential")) {
    if (!credentials.some((item) => item.ref === ref) && matchesTerm(ref, term))
      options.push({
        kind: "credential",
        value: ref,
        label: m("ui.credential_no_longer_exists"),
        iconName: "key",
        badge: m("ui.unavailable")
      });
  }
  return options;
}

function routeModelChoices(selection, available, term) {
  // Keep retired selections so a temporary provider outage cannot rewrite access.
  const known = new Set(available.map((model) => model.toLowerCase()));
  const retired = selectedRouteValues(selection, "model").filter((model) => !known.has(model.toLowerCase())).sort(compareModelId);
  return available.concat(retired).filter((model) => matchesTerm(model, term)).map((model) => ({
    kind: "model",
    value: model,
    label: model,
    iconName: "astroid",
    note: retired.includes(model) ? m("ui.not_in_the_cpa_model_list") : ""
  }));
}

function renderRouteKeyList(keys, routeID) {
  const list = $("route-key-list");
  keys = keys.filter((key) => !key.deleted_at || (routeID && key.route_bindings.route_ids.includes(routeID)));
  if (!keys.length) {
    list.replaceChildren(el("div", { class: "empty", text: m("ui.no_api_keys") }));
    return;
  }
  list.replaceChildren(
    ...keys.map((key) => {
      const checked = !!routeID && key.route_bindings.route_ids.includes(routeID);
      return el(
        "label",
        {},
        checkboxControl({ input: el("input", { type: "checkbox", value: key.scope, checked }) }),
        renderAPIKeyIdentity(key),
        key.deleted_at ? el("span", { class: "tag plain", text: m("ui.deleted"), "data-tip": m("ui.uncheck_to_remove_the_binding") }) : null
      );
    })
  );
}

function editRoute(route) {
  const id = route?.id;
  openEditor("route-dialog", "route", (data) => {
    const current = id ? data.routes.find((entry) => entry.id === id) : null;
    if (id && !current) throw new UIError(m("ui.this_routing_rule_no_longer_exists_close_the_dialog_and_refresh_the_list"));
    setText($("route-dialog-title"), current ? m("ui.edit_rule_value", { v0: current.name || id }) : m("ui.create_rule"));
    $("route-id").value = id || "";
    setText($("route-save"), current ? m("ui.save") : m("ui.create"));
    $("route-name").value = current?.name || "";
    $("route-rule-search").value = "";
    routeRuleSelection = routingEditorState(current?.rule, "route-credential-enabled-only");
    renderRouteRuleList();
    renderRouteKeyList(data.keys, id || "");
    return { route: structuredClone(current), scopes: selectedKeyScopes("route-key-list") };
  });
}

$("route-picker-search").addEventListener("input", renderKeyRouteList);
$("route-picker-all").onclick = () => setVisibleRouteChoices("route-picker", keyRouteSelection, "allow", renderKeyRouteList);
$("route-picker-deny-all").onclick = () => setVisibleRouteChoices("route-picker", keyRouteSelection, "deny", renderKeyRouteList);
$("route-picker-save").onclick = () =>
  submitEditor("route-picker", (editor) =>
    editor.initial({ route_ids: [...keyRouteSelection.routeIDs], ...routingPayload(keyRouteSelection) })
  );
$("route-picker-clear").onclick = () => {
  keyRouteSelection.routeIDs.clear();
  Object.assign(keyRouteSelection, routingSelection());
  renderKeyRouteList();
};
$("route-picker").addEventListener("close", () => { if (!$("route-picker").open) keyRouteSelection = null; });
$("route-rule-search").addEventListener("input", renderRouteRuleList);
function setVisibleRouteChoices(id, selection, state, rerender) {
  const dialog = $(id);
  const data = editors.get(dialog).data;
  const term = dialog.querySelector('input[type="search"]').value.trim().toLowerCase();
  for (const [, , choices] of routingChoiceGroups(selection, data, term)) {
    for (const item of choices) {
      if (item.kind === "route") selection.routeIDs.add(item.value); else setRouteChoice(selection, item.kind, item.value, state);
    }
  }
  rerender();
}
$("route-rule-all").onclick = () => setVisibleRouteChoices("route-dialog", routeRuleSelection, "allow", renderRouteRuleList);
$("route-rule-deny-all").onclick = () => setVisibleRouteChoices("route-dialog", routeRuleSelection, "deny", renderRouteRuleList);
$("route-rule-none").onclick = () => {
  Object.assign(routeRuleSelection, routingSelection());
  renderRouteRuleList();
};
$("route-new").onclick = () => editRoute();
$("route-save").onclick = () =>
  submitEditor("route-dialog", async (editor) => {
    const name = $("route-name").value.trim();
    if (!name) throw new UIError(m("ui.enter_a_rule_name"));
    const id = $("route-id").value;
    const rule = routingPayload(routeRuleSelection);
    const scopes = selectedKeyScopes("route-key-list");
    const data = { name, rule, scopes };
    if (id) {
      const route = editor.initial.route;
      if (
        route?.bound_key_count &&
        !(await openActionDialog(
          m("ui.save_routing_rule"),
          m("ui.update_the_selected_keys_bindings_and_rules_value", {
            v0: route.deleted_key_count ? m("ui.deleted_keys_will_also_retain_or_lose_bindings_according_to_this_selection_retained_b") : ""
          })
        ))
      )
        return false;
      if (!editorCurrent(editor)) return false;
      if (sameScopes(scopes, editor.initial.scopes)) delete data.scopes;
      if (name === route.name) delete data.name;
      if (JSON.stringify(rule) === JSON.stringify(routingPayload(routingSelection(route.rule)))) delete data.rule;
      if (!Object.keys(data).length) return;
      await mutateAdmin("PATCH", "/routes", { id, ...data });
    } else await mutateAdmin("POST", "/routes", data);
    notify(id ? m("ui.routing_rule_saved") : m("ui.routing_rule_created"));
  });
