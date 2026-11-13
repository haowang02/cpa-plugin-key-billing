function createPages(role) {
  const account = role === "account";
  const data = {
    ...resources[role],
    analysis: analysisQueries[role],
    speed: speedQueries[role],
    authFiles: authFilesQueries[role],
    aiProviders: aiProvidersQueries[role],
    keyDirectory,
    ...Object.fromEntries(Object.entries(pagedLists[role]).map(([name, list]) => [name, list.query]))
  };
  const section = (names, render) => ({ sources: names.map((name) => data[name]), render, displayed: null });
  const keyedSection = (names, render) => section(account ? names : [...names, "keyDirectory"], render);
  const keyFilter = account ? [] : [API_KEY_FILTER];
  const result = {
    analysis: {
      timeRange: true,
      currency: true,
      autoRefresh: true,
      filters: keyFilter,
      queries: [data.analysis, data.speed],
      load: (reload) => loadAnalysis(account, reload),
      renderStatus: () => renderAnalysis(account),
      sections: []
    },
    "request-events": {
      timeRange: true,
      currency: true,
      autoRefresh: true,
      filters: [...keyFilter, ...Object.values(REQUEST_EVENT_FILTERS)],
      actions: () => requestEventActions(account),
      count: () => pagedList("events", account).data?.total,
      list: pagedLists[role].events,
      sections: [keyedSection(["events"], () => renderRequestEvents(account))]
    },
    errors: {
      timeRange: true,
      autoRefresh: true,
      filters: [...keyFilter, ...Object.values(ERROR_EVENT_FILTERS)],
      actions: () => [eventExportAction("errors", account)],
      count: () => pagedList("errors", account).data?.total,
      list: pagedLists[role].errors,
      sections: [keyedSection(["errors"], () => renderErrors(account))]
    },
    "ai-providers": {
      timeRange: true,
      currency: true,
      autoRefresh: true,
      filters: Object.values(AI_PROVIDER_FILTERS),
      actions: () => (account ? [] : [aiBalanceAction()]),
      count: () => aiProvidersQueries[role].value && filteredAIProviders(account).length,
      queries: [data.aiProviders],
      load: (reload) => readAIProviders(account, reload),
      sections: [section(["aiProviders"], () => renderAIProviders(account))]
    },
    "auth-files": {
      timeRange: true,
      currency: true,
      autoRefresh: true,
      filters: Object.values(AUTH_FILE_FILTERS),
      actions: () => authFileActions(account),
      count: () => authFilesQueries[role].value && filteredAuthFiles(account).length,
      queries: [data.authFiles],
      load: (reload) => readAuthFiles(account, reload),
      sections: [section(["authFiles"], () => renderAuthFiles(account))]
    }
  };
  if (account) {
    result.subscription = {
      sections: [
        section(["subscription"], renderAccountSubscription),
        section(["models", "catalog", "profile"], renderAccountModels),
        section(["routing"], renderAccountRouting)
      ]
    };
  } else {
    result.keys = {
      editors: ["route"],
      filters: Object.values(KEY_FILTERS),
      actions: keyActions,
      count: () => resources.admin.keys.value && filteredKeys().length,
      sections: [keyedSection(["keys", "plans", "routes", "credentials"], renderKeys)]
    };
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
    page.filters ||= [];
    if (page.list) {
      page.queries = [page.list.query];
      page.load = page.list.load;
      page.renderStatus = () => { if (page.list.data) page.list.renderStatus(); };
    }
    const rate = page.currency ? [data.currency] : [];
    const filterSources = page.filters.map((filter) => filter.source).filter(Boolean);
    page.sources = [
      ...new Set([...page.sections.flatMap((section) => section.sources), ...(page.queries || []), ...filterSources, ...rate])
    ];
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

function currentPage() { return currentRole ? pages[currentRole][currentTab] : null; }

function renderPage() {
  const page = currentPage();
  if (!page) return;
  const account = currentRole === "account";
  renderToolbar();
  renderDataNotice();
  if (account && !renderAccountProfile()) return;
  const filters = filterSnapshot(account, page.filters);
  for (const section of page.sections) {
    const values = [
      locale(),
      moneyDisplayKey(),
      filters,
      ...section.sources.flatMap((source) => [source.value, source.value === null ? source.error : ""])
    ];
    if (section.displayed?.every((value, index) => value === values[index])) continue;
    if (section.render() !== false) section.displayed = values;
  }
  page.renderStatus?.();
}

// Start the read before repainting so stale rows never show.
function refilterPage() {
  const task = currentPage().load?.(false);
  renderPage();
  if (task) guard(() => task);
}

function loadPage(account, { reload = false } = {}) {
  const role = account ? "account" : "admin";
  const page = pages[role][currentTab];
  const generation = sessionGeneration;
  const task = (async () => {
    if (account) await loadDataGroup(true, "profile", reload);
    if (generation !== sessionGeneration || currentPage() !== page) return;
    const tasks = [];
    if (!account || resources.account.profile.value?.tracked) {
      tasks.push(...page.groups.map((name) => loadDataGroup(account, name, reload)));
      tasks.push(...page.filters.filter((filter) => filter.load).map((filter) => filter.load(reload)));
      if (page.load) tasks.push(page.load(reload));
    }
    renderPage();
    try { await settleLoads(tasks); } catch (error) {
      // Hidden pages keep their errors; only authentication failures concern the session.
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
  const listQuery = page.list?.query;
  const visible = page.sources.filter((source) => source !== listQuery);
  const errors = visible.filter((source) => source.error).map((source) =>
    m("ui.resource_error", {
      v0: source.label,
      v1: source.error,
      v2: source.value !== null ? m("ui.showing_the_last_successful_data") : ""
    })
  );
  if (currentRole === "admin" && page.groups.includes("configuration")) errors.push(...resourceGroups.admin.configuration.syncErrors);
  const status = $("data-status");
  if (!errors.length) return status.replaceChildren();
  const retry = el("button", { type: "button", disabled: !!page.task, onclick: refreshPage }, page.task ? m("ui.retrying") : m("ui.retry"));
  status.replaceChildren(notice(errors.join("\n"), "error", retry));
}
