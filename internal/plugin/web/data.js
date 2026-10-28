// Data groups publish independently; saves replace their returned entries.
// Each entry retains its last successful value when a read fails.
function dataEntries(labels) {
  return Object.fromEntries(Object.entries(labels).map(([name, label]) => [name, { label, value: null, error: "", readAt: 0 }]));
}

const resources = {
  admin: dataEntries({
    keys: "API Key",
    plans: m("ui.subscription_plans"),
    routes: m("ui.routing_rules"),
    credentials: m("ui.credential_catalog"),
    models: m("ui.model_catalog"),
    catalog: m("ui.model_prices"),
    priceStatus: m("ui.reference_price_status"),
    currency: m("ui.exchange_rate")
  }),
  account: dataEntries({
    profile: m("ui.account_profile"),
    subscription: m("ui.subscription"),
    routing: m("ui.access_permissions"),
    models: m("ui.model_catalog"),
    catalog: m("ui.model_prices"),
    currency: m("ui.exchange_rate")
  })
};
// Key metadata is shared by configuration, event queries, and analysis.
// Their cached rows keep scope references; labels and previews live here once.
const keyDirectory = {
  value: new Map(),
  revisions: new Map(),
  revision: 0,
  beginRead() { return ++keyDirectory.revision; },
  clear() {
    keyDirectory.value = new Map();
    keyDirectory.revisions.clear();
    keyDirectory.revision++;
  },
  receive(rows, revision = ++keyDirectory.revision, { field = "scope", fallback = false } = {}) {
    let identities = keyDirectory.value;
    const references = rows.map((row) => {
      const scope = row[field];
      if (!scope) return row;
      const { label = "", preview = "", ...reference } = row;
      const previous = identities.get(scope);
      if (fallback && previous) return reference;
      if (revision >= (keyDirectory.revisions.get(scope) || 0)) {
        // Analysis provides a display name; exact labels come from Key records.
        const identity = { label: fallback && (label === preview || label === scope) ? "" : label, preview };
        if (!fallback) keyDirectory.revisions.set(scope, revision);
        if (!previous || previous.label !== identity.label || previous.preview !== preview) {
          if (identities === keyDirectory.value) identities = new Map(identities);
          identities.set(scope, identity);
        }
      }
      return reference;
    });
    keyDirectory.value = identities;
    return references;
  },
  resolve(reference) { return { ...reference, ...keyDirectory.value.get(reference.scope || reference.key) }; }
};

const resourceGroups = {
  admin: {
    configuration: { names: ["keys", "plans", "routes", "credentials", "models"], syncErrors: [] },
    pricing: { names: ["catalog", "priceStatus"], after: "configuration" },
    currency: { names: ["currency"] }
  },
  account: {
    ...Object.fromEntries(Object.keys(resources.account).map((name) => [name, { names: [name] }])),
    catalog: { names: ["catalog"], after: "models" }
  }
};
// These are also page dependencies, even when the editor is still closed.
const EDITOR_RESOURCES = { plan: ["keys", "plans"], route: ["keys", "routes", "credentials", "models"] };

// A query caches one explicit selection. Changing that selection or starting
// a new session discards the result; elapsed time never invalidates it.
function createQuery(label, receive = (value) => value) {
  const query = {
    label,
    value: null,
    error: "",
    readAt: 0,
    key: "",
    task: null,
    request: null,
    clear() { Object.assign(query, { value: null, error: "", readAt: 0, key: "", task: null, request: null }); },
    read(key, fetch, reload = false) {
      if (key !== query.key) query.clear();
      if (!reload && query.task) return query.task;
      if (!reload && (query.value !== null || query.error)) return Promise.resolve(query.value);
      const request = {};
      query.key = key;
      query.request = request;
      query.error = "";
      const task = Promise.resolve().then(() => {
        if (query.request !== request) throw new StaleRequestError();
        return fetch();
      })
        .then((value) => {
          if (query.request !== request) throw new StaleRequestError();
          query.value = receive(value);
          return query.value;
        })
        .catch((error) => {
          if (query.request !== request) throw new StaleRequestError();
          if (error instanceof AuthError || error instanceof StaleRequestError) throw error;
          // Ordinary read failures belong to the query's inline status.
          query.error = error.message || String(error);
          return query.value;
        })
        .finally(() => {
          if (query.request === request) {
            query.task = null;
            query.readAt = Date.now();
            renderPage();
          }
        });
      query.task = task;
      updateRefreshControls();
      return task;
    }
  };
  return query;
}

const analysisQueries = Object.fromEntries(["admin", "account"].map((role) => [role, createQuery(m("ui.analysis"))]));
const speedQueries = Object.fromEntries(["admin", "account"].map((role) => [role, createQuery(m("ui.speed_trend"))]));
const authFilesQueries = Object.fromEntries(
  ["admin", "account"].map((role) => [
    role,
    createQuery(m("ui.auth_files"), (files) => {
      updateAuthFiles(role === "account", files);
      return files;
    })
  ])
);
const aiProvidersQueries = Object.fromEntries(["admin", "account"].map((role) => [role, createQuery(m("ui.ai_providers"))]));
const eventKeysQuery = createQuery(m("ui.event_api_key_filter"));
const UNASSIGNED_KEY = "__unassigned__";

const LIST_PAGE_SIZE = 50;

const EVENT_LOAD_MORE_THRESHOLD_PX = 320;

const PAGED_LIST_VIEWS = {
  events: {
    tab: "request-events",
    label: m("ui.request_events"),
    scrollers: ["request-events-body", "account-request-events-body"],
    timed: true,
    keyed: true,
    endpoint: "/events",
    filters: requestEventFilters,
    render: renderRequestEvents
  },
  errors: {
    tab: "errors",
    label: m("ui.error_events"),
    scrollers: ["errors-body", "account-errors-body"],
    timed: true,
    keyed: true,
    endpoint: "/errors",
    filters: errorEventFilters,
    render: renderErrors
  },
  logs: {
    tab: "settings",
    label: m("ui.plugin_logs"),
    scrollers: ["plugin-logs-body"],
    endpoint: "/plugin-logs",
    filters: () => ({ level: $("plugin-log-level").value }),
    render: renderPluginLogs
  }
};

// A selection separates its cache identity from the concrete time window.
// Rolling ranges advance only when starting a fresh traversal.
function pagedListSelection(kind, account) {
  const view = PAGED_LIST_VIEWS[kind];
  const filters = view.filters(account);
  const range = view.timed ? selectedRange(account) : {};
  if (view.timed) range.to = new Date(Math.min(Date.now(), Date.parse(range.to))).toISOString();
  return {
    key: JSON.stringify([view.timed ? sharedTimeRanges[account ? "account" : "admin"] : null, filters]),
    params: { ...filters, ...range, limit: LIST_PAGE_SIZE }
  };
}

async function fetchListPage(kind, account, params, previous = null) {
  const view = PAGED_LIST_VIEWS[kind];
  const query = new URLSearchParams({ ...params, ...previous?.cursor });
  const path = view.endpoint + "?" + query;
  const revision = keyDirectory.beginRead();
  const page = await (account ? accountAPI(path) : plugin("GET", path));
  const entries = page.entries;
  let cursor = null;
  if (view.timed) {
    // Retention may change totals or leave short/empty pages.
    const offset = (previous?.cursor?.offset || 0) + entries.length;
    if (entries.length && offset < page.total) cursor = { offset, snapshot_id: String(page.snapshot_id) };
  } else if (page.next_before_id) { cursor = { before_id: String(page.next_before_id) }; }
  return { page, params, cursor, previous, revision };
}

// createQuery calls this only after accepting the response. Stale reads
// cannot publish Key metadata or append rows to a newer selection.
function mergeListPage(kind, account, { page, params, cursor, previous, revision }) {
  const rows = PAGED_LIST_VIEWS[kind].keyed && !account ? keyDirectory.receive(page.entries, revision) : page.entries;
  return {
    page: {
      ...page,
      entries: (previous?.page.entries || []).concat(rows),
      filter_options: page.filter_options || previous?.page.filter_options
    },
    params,
    cursor
  };
}

function createPagedList(kind, account) {
  const view = PAGED_LIST_VIEWS[kind];
  const query = createQuery(view.label, (result) => mergeListPage(kind, account, result));
  const list = {
    query,
    append: false,
    rendered: null,
    get data() { return query.value?.page || null; },
    get complete() { return !!query.value && !query.value.cursor; },
    get scroller() { return $(view.scrollers[account ? 1 : 0]); },
    active() { return currentRole === (account ? "account" : "admin") && activeTab(account) === view.tab; },
    replace(page) {
      list.reset();
      query.key = pagedListSelection(kind, account).key;
      query.value = { page, cursor: null };
    },
    reset() {
      query.clear();
      list.append = false;
      list.rendered = null;
      list.scroller.scrollTop = 0;
      list.scroller.scrollLeft = 0;
    },
    renderItems(items, options) {
      const previous = list.rendered;
      const identities = view.keyed && !account ? keyDirectory.value : null;
      const shape = JSON.stringify([options.headers || null, moneyDisplayKey()]);
      // Appended pages retain the same entry objects. Rebuild when the identity
      // metadata, table shape or displayed currency changes, or rows are no longer a prefix.
      const append =
        previous?.rows?.isConnected && previous.shape === shape && previous.identities === identities &&
        previous.items.length <= items.length &&
        previous.items.every((item, index) => item === items[index]);
      const rows = append ? previous.rows : renderCollection(list.scroller, items, options);
      if (append) rows.append(...items.slice(previous.items.length).map(options.render));
      list.rendered = { items, shape, rows, identities };
    },
    load(reload = false) {
      const selection = pagedListSelection(kind, account);
      if (selection.key !== query.key) list.reset();
      if (query.task && (!reload || !list.append)) return query.task;
      if (!reload && (query.value || query.error)) return Promise.resolve(query.value);
      list.append = false;
      const task = query.read(selection.key, () => fetchListPage(kind, account, selection.params), true);
      if (list.active()) { if (list.data) list.renderStatus(); else view.render(account); }
      return task;
    },
    loadMore(retry = false) {
      if (!list.active() || query.task || !query.value?.cursor || !isNearScrollEnd(list.scroller)) return;
      if (query.error && !(retry && list.append)) return;
      const previous = query.value;
      list.append = true;
      const task = query.read(query.key, () => fetchListPage(kind, account, previous.params, previous), true);
      list.renderStatus();
      guard(() => task);
    },
    renderEmpty() {
      if (list.data) return false;
      list.rendered = null;
      const message = query.error ? m("ui.failed_to_load_value_value", { v0: view.label, v1: query.error }) : m("ui.loading");
      list.scroller.replaceChildren(
        el(
          "div",
          { class: "empty" },
          el("div", { text: message }),
          query.error && !query.task
            ? el("button", { class: "retry-button", onclick: () => guard(() => list.load(true)) }, m("ui.retry"))
            : null
        )
      );
      return true;
    },
    renderStatus() {
      const scroller = list.scroller;
      const top = scroller.scrollTop, left = scroller.scrollLeft;
      const error = query.task ? "" : query.error;
      let message = error ? m("ui.failed_to_load_value", { v0: error }) : "";
      if (query.task) message = list.append ? m("ui.loading_more") : m("ui.loading");
      const previous = scroller.querySelector(":scope > .event-load-state");
      if ((previous?.textContent || "") === message) return;
      const status = message ? el("div", { class: "event-load-state", role: error ? "alert" : "status", text: message }) : null;
      if (status && previous) previous.replaceWith(status); else if (status) scroller.append(status); else previous?.remove();
      scroller.scrollTop = top;
      scroller.scrollLeft = left;
    }
  };
  bindPagedListScroll(list);
  return list;
}

function bindPagedListScroll(list) {
  const scroller = list.scroller;
  const on = (type, handler) => scroller.addEventListener(type, handler, { passive: true });
  // Failed pages retry on user input; layout scrolls cannot trigger retries.
  on("wheel", (event) => { if (event.deltaY > 0) list.loadMore(true); });
  let touchY = null;
  on("touchstart", (event) => { touchY = event.touches[0]?.clientY ?? null; });
  on("touchmove", (event) => {
    const nextY = event.touches[0]?.clientY ?? null;
    if (touchY !== null && nextY !== null && nextY < touchY) list.loadMore(true);
    touchY = nextY;
  });
  on("keydown", (event) => {
    if (event.target === scroller && ["ArrowDown", "PageDown", "End", " "].includes(event.key) && !event.shiftKey) list.loadMore(true);
  });
  let dragTop = null;
  on("pointerdown", (event) => { dragTop = event.target === scroller ? scroller.scrollTop : null; });
  on("pointerup", (event) => {
    if (event.target === scroller && dragTop !== null && scroller.scrollTop > dragTop) list.loadMore(true);
    dragTop = null;
  });
  on("scroll", () => { list.loadMore(); });
}

const pagedLists = {
  admin: { events: createPagedList("events", false), errors: createPagedList("errors", false), logs: createPagedList("logs", false) },
  account: { events: createPagedList("events", true), errors: createPagedList("errors", true) }
};

function pagedList(kind, account = false) { return pagedLists[account ? "account" : "admin"][kind]; }

function isNearScrollEnd(scroller) {
  return scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight <= EVENT_LOAD_MORE_THRESHOLD_PX;
}

function viewPreferenceKey(role, name) {
  const scope = credentialScope[role];
  return scope ? VIEW_PREFERENCE_PREFIX + role + "." + scope + "." + name : "";
}

function viewPreference(role, name, fallback) {
  const key = viewPreferenceKey(role, name);
  if (!key) return fallback;
  try {
    const raw = localStorage.getItem(key);
    const value = raw === null ? fallback : JSON.parse(raw);
    return typeof value === typeof fallback ? value : fallback;
  } catch (_) { return fallback; }
}

function saveViewPreference(role, name, value) {
  const key = viewPreferenceKey(role, name);
  if (!key) return;
  try { localStorage.setItem(key, JSON.stringify(value)); } catch (_) {}
}

function restoreChoice(role, id, fallback, allowMissing = false) {
  const control = $(id);
  let value = viewPreference(role, id, fallback);
  if (control.type === "checkbox") {
    control.checked = value;
    return;
  }
  if (!Array.from(control.options).some((option) => option.value === value)) {
    if (allowMissing && value) control.add(el("option", { value, text: value })); else value = fallback;
  }
  control.value = value;
}

function saveChoice(role, id) {
  const control = $(id);
  saveViewPreference(role, id, control.type === "checkbox" ? control.checked : control.value);
}

async function fetchAdminCollection(name) {
  const result = await plugin("GET", "/" + name);
  return result[name];
}

function editorData(kind) {
  const names = EDITOR_RESOURCES[kind];
  for (const name of names) {
    const resource = resources.admin[name];
    if (resource.value === null) throw new UIError(resource.error || m("ui.value_has_not_loaded_yet", { v0: resource.label }));
  }
  // Resources are replaced on reads and writes. Holding these values gives
  // an editor stable options until it closes, including search and toggles.
  return Object.fromEntries(names.map((name) => [name, resources.admin[name].value]));
}

// Each write family has its own order. Configuration and pricing also
// serialize their reads; clearing logs never waits for either family.
const adminDataQueues = { configuration: Promise.resolve(), pricing: Promise.resolve(), logs: Promise.resolve() };
function queueAdminData(group, operation) {
  const generation = sessionGeneration;
  const current = () => { if (generation !== sessionGeneration || currentRole !== "admin") throw new StaleRequestError(); };
  const task = adminDataQueues[group].catch(() => {}).then(async () => {
    current();
    return operation(current);
  });
  adminDataQueues[group] = task;
  return task;
}

async function readAdminConfiguration(read) {
  const configuredKeys = fetchConfiguredAPIKeys();
  const errors = [];
  const sync = async (label, operation) => {
    try { await operation(); } catch (error) {
      if (error instanceof AuthError || error instanceof StaleRequestError) throw error;
      errors.push(m("ui.failed_to_sync_value_value", { v0: label, v1: error.message }));
    }
  };
  const synced = settleLoads([
    sync("API Key", async () => plugin("POST", "/keys/sync", { keys: await configuredKeys, allow_empty: true }, { command: true })),
    sync(m("ui.credentials"), syncConfiguredProviders)
  ]);
  await settleLoads([
    read("plans", () => fetchAdminCollection("plans")),
    read("models", async () => fetchModels(false, (await configuredKeys).find((key) => key.trim())?.trim() || "")),
    synced.then(() => settleLoads(["keys", "routes", "credentials"].map((name) => read(name, () => fetchAdminCollection(name)))))
  ]);
  return errors;
}

function writeAdmin(method, path, data = null, options) {
  const payload = structuredClone(data);
  const group = path.startsWith("/prices") ? "pricing" : path === "/plugin-logs" ? "logs" : "configuration";
  return queueAdminData(group, async (current) => {
    const body = { data: payload };
    const models = resources.admin.models.value;
    if (path.startsWith("/prices")) body.models = models || [];
    const result = await plugin(method, path + (path.includes("?") ? "&" : "?") + "view=1", body, { ...options, command: true });
    current();
    applyAdminView(result.view, models);
    return result;
  });
}

function applyAdminView(view, models) {
  for (const name of ["keys", "plans", "routes"]) {
    if (!Object.hasOwn(view, name)) continue;
    const resource = resources.admin[name];
    const rows = view[name];
    // A committed view supersedes metadata from any read already in flight.
    Object.assign(resource, { value: name === "keys" ? keyDirectory.receive(rows) : rows, error: "" });
  }
  const catalog = resources.admin.catalog;
  if (Object.hasOwn(view, "prices")) {
    const prices = view.prices;
    catalog.value = { models, prices: prices.map((price) => ({ ...price, in_models: models === null ? undefined : price.in_models })) };
    catalog.error = "";
  }
  if (view.error) catalog.error = view.error;
  if (Object.hasOwn(view, "metadata")) { Object.assign(resources.admin.priceStatus, { value: { metadata: view.metadata }, error: "" }); }
  if (view.logs_cleared) {
    pagedList("logs").replace({ entries: [], level_counts: {}, next_before_id: 0 });
    expandedPluginLogIDs.clear();
  }
}

async function mutateAdmin(method, path, data = null, options) {
  const result = await writeAdmin(method, path, data, options);
  renderPage();
  return result;
}

// Long selections split across requests, so that each URL stays within common proxy limits.
const QUERY_BATCH_LENGTH = 3300;

function batchedSearchParams(name, values) {
  const batches = [];
  let query = new URLSearchParams();
  for (const value of values) {
    const parameter = new URLSearchParams({ [name]: value }).toString();
    if (query.size && query.toString().length + parameter.length + 1 > QUERY_BATCH_LENGTH) {
      batches.push(query);
      query = new URLSearchParams();
    }
    query.append(name, value);
  }
  if (query.size || !batches.length) batches.push(query);
  return batches;
}

async function fetchModelPrices(models, account) {
  if (models.some((model) => new URLSearchParams({ model }).toString().length > QUERY_BATCH_LENGTH))
    throw new UIError(m("ui.model_id_is_too_long"));
  const batches = batchedSearchParams("model", models);
  const prices = new Map();
  for (const [index, batch] of batches.entries()) {
    batch.set("include_custom", String(!account && index === 0));
    const path = "/prices?" + batch.toString();
    const rows = await (account ? accountAPI(path) : plugin("GET", path));
    for (const row of rows) {
      const key = row.model_id.toLowerCase();
      const previous = prices.get(key);
      prices.set(key, { ...row, in_models: row.in_models || previous?.in_models || false });
    }
  }
  return [...prices.values()];
}

async function fetchConfiguredAPIKeys() {
  const result = await api("GET", "/v0/management/api-keys", null);
  return result["api-keys"] || [];
}

// The plugin filters /v1/models by the key's routing rules; management lists every model.
async function fetchModels(account, key) {
  const headers = account ? {} : { "X-CPA-Key-Billing-Models": "all" };
  const result = await api("GET", "/v1/models", null, { key, headers, auxiliaryCredential: !account });
  const rows = result.data;
  const models = [...new Set(rows.map((row) => row.id))].sort(compareModelId);
  const previous = resources[account ? "account" : "admin"].models.value;
  // The model list is the catalog's input. Preserve its identity when
  // discovery returns the same IDs, even if their server order changed.
  return previous?.length === models.length && previous.every((model, index) => model === models[index]) ? previous : models;
}

async function fetchCatalog(account) {
  const models = resources[account ? "account" : "admin"].models.value;
  if (account && models === null) throw new UIError(m("ui.model_catalog_is_not_available_yet"));
  const prices = await fetchModelPrices(models || [], account);
  return { models, prices: prices.map((price) => ({ ...price, in_models: models === null ? undefined : price.in_models })) };
}

function dataGroupCached(account, group) {
  const data = resources[account ? "account" : "admin"];
  return (
    group.names.every((name) => data[name].value !== null || data[name].error) &&
    (!group.names.includes("catalog") || !data.catalog.value || data.catalog.value.models === data.models.value)
  );
}

// Groups own complete loading flows, including their upstream inputs.
// A catalog waits for its model source and records the list it priced;
// a changed directory does not clear a hidden page's existing prices.
function loadDataGroup(account, name, reload = false) {
  const role = account ? "account" : "admin";
  const group = resourceGroups[role][name];
  if (group.task) return group.task;
  const prerequisite = group.after ? loadDataGroup(account, group.after, reload).catch((error) => error) : null;
  if (!reload && dataGroupCached(account, group) && (!group.after || !resourceGroups[role][group.after].task)) return Promise.resolve();
  const generation = sessionGeneration;
  const load = async () => {
    const error = await prerequisite;
    if (error instanceof AuthError || error instanceof StaleRequestError) throw error;
    if (generation !== sessionGeneration) throw new StaleRequestError();
    if (!reload && dataGroupCached(account, group)) return;
    for (const key of group.names) resources[role][key].error = "";
    renderPage();
    const identityRevision = keyDirectory.beginRead();
    const values = {}, failures = [];
    const read = async (key, fetch) => {
      try { values[key] = await fetch(); } catch (error) {
        if (error instanceof AuthError || error instanceof StaleRequestError) throw error;
        failures.push({ key, error });
      }
    };
    if (account) {
      await read(name, () =>
        name === "models" ? fetchModels(true, authKey) : name === "catalog" ? fetchCatalog(true) : accountAPI("/" + name)
      );
    } else if (name === "configuration") { group.syncErrors = await readAdminConfiguration(read); } else if (name === "pricing") {
      await settleLoads([read("catalog", () => fetchCatalog(false)), read("priceStatus", () => plugin("GET", "/prices/reference/status"))]);
    } else { await read(name, () => plugin("GET", "/" + name)); }
    if (generation !== sessionGeneration) throw new StaleRequestError();
    if (account && name !== "profile" && !resources.account.profile.value?.tracked) return;
    if (values.keys) values.keys = keyDirectory.receive(values.keys, identityRevision);
    for (const [key, value] of Object.entries(values)) Object.assign(resources[role][key], { value, error: "" });
    for (const { key, error } of failures) resources[role][key].error = error.message || String(error);
    for (const key of group.names) resources[role][key].readAt = Date.now();
    if (values.profile?.tracked === false) {
      for (const [key, resource] of Object.entries(resources.account)) {
        if (key !== "profile") Object.assign(resource, { value: null, error: "", readAt: 0 });
      }
      analysisQueries.account.clear();
      speedQueries.account.clear();
      authFilesQueries.account.clear();
      for (const list of Object.values(pagedLists.account)) list.reset();
      resetAuthQuotaMemory(true);
      persistAuthQuotaCache(true);
    }
  };
  const task = !account && ["configuration", "pricing"].includes(name) ? queueAdminData(name, load) : Promise.resolve().then(load);
  group.task = task.finally(() => {
    if (generation === sessionGeneration) {
      group.task = null;
      renderPage();
    }
  });
  return group.task;
}

// A page owns both its displays and its editors. Loading and refresh use
// their union; rendering uses only each display's own dependencies.
function createPages(role) {
  const account = role === "account";
  const data = {
    ...resources[role],
    analysis: analysisQueries[role],
    speed: speedQueries[role],
    authFiles: authFilesQueries[role],
    aiProviders: aiProvidersQueries[role],
    eventKeys: eventKeysQuery,
    keyDirectory,
    ...Object.fromEntries(Object.entries(pagedLists[role]).map(([name, list]) => [name, list.query]))
  };
  const section = (names, render) => ({ sources: names.map((name) => data[name]), render, displayed: null });
  const keyedSection = (names, render) => section(account ? names : [...names, "keyDirectory"], render);
  const result = {
    analysis: {
      timeRange: true,
      currency: true,
      autoRefresh: true,
      queries: [data.analysis, data.speed],
      load: (reload) => loadAnalysis(account, reload),
      renderStatus: () => renderAnalysis(account),
      sections: []
    },
    "request-events": {
      timeRange: true,
      currency: true,
      autoRefresh: true,
      eventColumns: true,
      exportEvents: true,
      list: pagedLists[role].events,
      sections: [keyedSection(["events"], () => renderRequestEvents(account))]
    },
    errors: {
      timeRange: true,
      autoRefresh: true,
      exportEvents: true,
      list: pagedLists[role].errors,
      sections: [keyedSection(["errors"], () => renderErrors(account))]
    },
    "ai-providers": {
      timeRange: true,
      currency: true,
      autoRefresh: true,
      queries: [data.aiProviders],
      load: (reload) => readAIProviders(account, reload),
      sections: [section(["aiProviders"], () => renderAIProviders(account))]
    },
    "auth-files": {
      timeRange: true,
      currency: true,
      autoRefresh: true,
      queries: [data.authFiles],
      load: (reload) => readAuthFiles(account, reload),
      sections: [section(["authFiles"], () => renderAuthFiles(account))]
    }
  };
  if (account) {
    result.subscription = {
      sections: [
        section(["subscription"], renderAccountSubscription),
        section(["models", "catalog", "routing", "profile"], renderAccountModels),
        section(["routing"], renderAccountRouting)
      ]
    };
  } else {
    const keyFilter = keyedSection(["eventKeys"], renderEventKeys);
    for (const tab of ["analysis", "request-events", "errors"]) result[tab].sections.unshift(keyFilter);
    result.keys = { editors: ["route"], sections: [keyedSection(["keys", "plans", "routes", "credentials"], renderKeys)] };
    result.settings = {
      editors: ["plan", "route"],
      list: pagedLists[role].logs,
      sections: [
        keyedSection(["plans", "keys"], renderPlans),
        keyedSection(["routes", "keys", "credentials"], renderRoutes),
        section(["catalog"], renderPriceTable),
        section(["priceStatus", "catalog"], renderReferencePriceStatus),
        section(["logs"], renderPluginLogs)
      ]
    };
  }
  for (const page of Object.values(result)) {
    if (page.list) {
      page.queries = [page.list.query];
      page.load = page.list.load;
      page.renderStatus = () => { if (page.list.data) page.list.renderStatus(); };
    }
    // Pages that show amounts load the exchange rate to convert them.
    const rate = page.currency ? [data.currency] : [];
    page.sources = [...new Set([...page.sections.flatMap((section) => section.sources), ...(page.queries || []), ...rate])];
    for (const editor of page.editors || []) {
      for (const name of EDITOR_RESOURCES[editor]) if (!page.sources.includes(data[name])) page.sources.push(data[name]);
    }
    if (page.sources.includes(data.catalog) && !page.sources.includes(data.models)) page.sources.push(data.models);
    page.groups = Object.entries(resourceGroups[role]).filter(([, group]) => group.names.some((name) => page.sources.includes(data[name])))
      .map(([name]) => name);
    if (account) page.sources.unshift(data.profile);
    page.task = null;
  }
  if (account) result["auth-files"].sections[0].sources.push(data.profile);
  return result;
}
const pages = { admin: createPages("admin"), account: createPages("account") };

function currentPage() { return currentRole ? pages[currentRole][activeTab(currentRole === "account")] : null; }

// Cached navigation only paints changed sections. A refresh retains the
// existing DOM until new data arrives; hidden pages are painted on entry.
function renderPage() {
  const page = currentPage();
  if (!page) return;
  const account = currentRole === "account";
  updateRefreshControls();
  renderDisplayCurrency();
  renderDataNotice();
  if (account && !renderAccountProfile()) return;
  for (const section of page.sections) {
    const values = [
      locale(),
      moneyDisplayKey(),
      ...section.sources.flatMap((source) => [source.value, source.value === null ? source.error : ""])
    ];
    if (section.displayed?.every((value, index) => value === values[index])) continue;
    if (section.render() !== false) section.displayed = values;
  }
  page.renderStatus?.();
}

function loadPage(account, { reload = false } = {}) {
  const role = account ? "account" : "admin";
  const page = pages[role][activeTab(account)];
  const generation = sessionGeneration;
  const task = (async () => {
    if (account) await loadDataGroup(true, "profile", reload);
    if (generation !== sessionGeneration || currentPage() !== page) return;
    const tasks = [];
    if (!account || resources.account.profile.value?.tracked) {
      tasks.push(...page.groups.map((name) => loadDataGroup(account, name, reload)));
      if (page.sources.includes(eventKeysQuery)) tasks.push(readEventKeys(reload));
      if (page.load) tasks.push(page.load(reload));
    }
    renderPage();
    try { await settleLoads(tasks); } catch (error) {
      // Hidden pages keep their errors for the next visit. Only an
      // authentication failure concerns the whole session.
      if (currentPage() === page || error instanceof AuthError) throw error;
    }
  })().finally(() => {
    if (generation !== sessionGeneration) return;
    if (page.task === task) page.task = null;
    renderPage();
  });
  page.task = task;
  renderPage();
  return task;
}

function renderDataNotice() {
  const page = currentPage();
  if (!page) return;
  const account = currentRole === "account";
  // Paged lists display their own loading and error states.
  const listQuery = page.list?.query;
  const visible = page.sources.filter((source) => source !== listQuery);
  const errors = visible.filter((source) => source.error).map((source) =>
    m("ui.resource_error", {
      v0: source.label,
      v1: source.error,
      v2: source.value !== null ? m("ui.showing_the_last_successful_data") : ""
    })
  );
  if (!account && page.groups.includes("configuration")) errors.push(...resourceGroups.admin.configuration.syncErrors);
  const target = $(account ? "account-data-status" : "admin-data-status");
  target.classList.toggle("show", !!errors.length);
  target.replaceChildren(
    ...(errors.length
      ? [
          el("div", { text: errors.join("；") }),
          el("button", { class: "retry-button", disabled: !!page.task, onclick: refreshPage }, page.task ? m("ui.retrying") : m("ui.retry"))
        ]
      : [])
  );
}
