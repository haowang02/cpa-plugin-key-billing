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

// A query caches one selection until the selection or session changes; elapsed time never invalidates it.
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

const LIST_PAGE_SIZE = 50;

const EVENT_LOAD_MORE_THRESHOLD_PX = 320;

const PAGED_LIST_VIEWS = {
  events: {
    tab: "request-events",
    label: m("ui.request_events"),
    scroller: "request-events-body",
    timed: true,
    keyed: true,
    endpoint: "/events",
    query: requestEventQuery,
    render: renderRequestEvents
  },
  errors: {
    tab: "errors",
    label: m("ui.error_events"),
    scroller: "errors-body",
    timed: true,
    keyed: true,
    endpoint: "/errors",
    query: errorEventQuery,
    render: renderErrors
  },
  logs: {
    tab: "settings",
    label: m("ui.plugin_logs"),
    scroller: "plugin-logs-body",
    endpoint: "/plugin-logs",
    query: pluginLogQuery,
    render: renderPluginLogs
  }
};

// Rolling ranges advance only when a fresh traversal starts.
function pagedListSelection(kind, account) {
  const view = PAGED_LIST_VIEWS[kind];
  const query = view.query(account);
  const range = view.timed ? selectedRange(account) : {};
  if (view.timed) range.to = new Date(Math.min(Date.now(), Date.parse(range.to))).toISOString();
  return {
    key: JSON.stringify([view.timed ? sharedTimeRanges[account ? "account" : "admin"] : null, query]),
    params: { ...query, ...range, limit: LIST_PAGE_SIZE }
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

// Called only for accepted responses, so stale reads cannot touch key metadata or a newer selection.
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
    get scroller() { return $(view.scroller); },
    active() { return currentRole === (account ? "account" : "admin") && currentTab === view.tab; },
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
      list.scroller.replaceChildren(
        query.error
          ? notice(
              m("ui.failed_to_load_value_value", { v0: view.label, v1: query.error }),
              "error",
              !query.task && el("button", { type: "button", onclick: () => guard(() => list.load(true)) }, m("ui.retry"))
            )
          : el("div", { class: "empty", text: m("ui.loading") })
      );
      return true;
    },
    loadState() {
      if (query.task)
        return el("div", { class: "event-load-state", role: "status", text: list.append ? m("ui.loading_more") : m("ui.loading") });
      if (!query.error) return null;
      const retry = el("button", { type: "button", onclick: () => list.loadMore(true) }, m("ui.retry"));
      return el(
        "div",
        { class: "event-load-state", role: "alert" },
        notice(m("ui.failed_to_load_more", { v0: query.error }), "error", retry)
      );
    },
    renderStatus() {
      const scroller = list.scroller;
      const top = scroller.scrollTop, left = scroller.scrollLeft;
      const status = list.loadState();
      const previous = scroller.querySelector(":scope > .event-load-state");
      if ((previous?.textContent || "") === (status?.textContent || "")) return;
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
  // Editors keep these values until they close, even when resources are replaced.
  return Object.fromEntries(names.map((name) => [name, resources.admin[name].value]));
}

// Configuration and pricing serialize their reads and writes; clearing logs waits for neither.
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

// The plugin's /v1/models applies the key's routing rules; management lists every model.
async function fetchModels(account, key) {
  const headers = account ? {} : { "X-CPA-Key-Billing-Models": "all" };
  const result = await api("GET", "/v1/models", null, { key, headers, auxiliaryCredential: !account });
  const rows = result.data;
  const models = [...new Set(rows.map((row) => row.id))].sort(compareModelId);
  const previous = resources[account ? "account" : "admin"].models.value;
  // Keep the previous array when the IDs are unchanged, even if reordered.
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

function resetEventHistory() {
  eventKeysQuery.clear();
  for (const role of ["admin", "account"]) {
    for (const kind of ["events", "errors"]) pagedLists[role][kind].reset();
    for (const query of [analysisQueries[role], speedQueries[role], authFilesQueries[role], aiProvidersQueries[role]]) query.clear();
    modelUsages[role].clear();
  }
}
