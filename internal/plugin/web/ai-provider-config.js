const AI_PROVIDER_TYPES = [
  { type: "claude", provider: "claude", list: "claude-api-key", brand: "claude" },
  { type: "codex", provider: "codex", list: "codex-api-key", brand: "codex" },
  { type: "gemini", provider: "gemini", list: "gemini-api-key", brand: "gemini" },
  { type: "interactions", provider: "gemini-interactions", list: "interactions-api-key", brand: "gemini" },
  { type: "vertex", provider: "vertex", list: "vertex-api-key", brand: "vertex" },
  { type: "xai", provider: "xai", list: "xai-api-key", brand: "xai" },
  { type: "meta", provider: "meta", list: "meta-api-key", brand: "meta" },
  { type: "openai-compatibility", list: "openai-compatibility", brand: "openai" }
];

function sortedConfiguredHeaders(headers) {
  if (!headers) return "";
  return Object.keys(headers).sort().map((key) => key + "\0" + headers[key] + "\0").join("");
}

// Hashing the distinguishing fields, unlike the host's runtime credential, survives disabling the entry.
async function configuredEntryRef(kind, parts, counters) {
  const digest = await sha256Hex(kind + parts.map((part) => "\0" + String(part || "").trim()).join(""));
  const base = kind + ":" + digest.slice(0, 12);
  const collision = counters.get(base) || 0;
  counters.set(base, collision + 1);
  const id = collision ? base + "-" + collision : base;
  return "sha256:" + (await sha256Hex("cpa-key-billing:credential:v1\0" + id));
}

function configuredProviderDetails(entry, type, name) {
  return {
    type,
    name: String(name ?? "").trim(),
    auth_index: entry["auth-index"] || "",
    base_url: (entry["base-url"] || "").trim(),
    priority: entry.priority ?? 0
  };
}

// v0 lists only OpenAI-compatible providers and v8 every key group; hosts without providers or v0-only panels answer 404.
async function readProviderGroupNames() {
  let groups;
  try { groups = await api("GET", "/v8/management/config/api-keys", null); } catch (error) {
    if (error.status === 404) return new Map();
    throw error;
  }
  const names = new Map();
  for (const group of Object.values(groups).flat()) for (const key of group.keys) if (key.auth_index) names.set(key.auth_index, group.name);
  return names;
}

async function readProviderConfiguration() {
  const [lists, groupNames] = await Promise.all([
    Promise.all(AI_PROVIDER_TYPES.map((kind) => api("GET", "/v0/management/" + kind.list, null))),
    readProviderGroupNames()
  ]);
  const config = Object.assign({}, ...lists);
  return { config, ...(await parseConfiguredProviders(config, groupNames)) };
}

async function syncConfiguredProviders() {
  const { credentials, targets } = await readProviderConfiguration();
  await plugin("POST", "/credentials/sync", { credentials }, { command: true });
  return targets;
}

async function parseConfiguredProviders(config, groupNames) {
  const generation = sessionGeneration;
  const counters = new Map();
  const credentials = [];
  const targets = new Map();
  const add = (credential, list, index, keyIndex = null) => {
    credentials.push(credential);
    targets.set(credential.config_ref, {
      list,
      index,
      keyIndex,
      group: list + ":" + index,
      editor: {
        weightEditable: list !== "openai-compatibility" || keyIndex !== null,
        sharedPriority: list === "openai-compatibility" && (config[list][index]["api-key-entries"] || []).length > 1
      },
      excludedModels: config[list][index]["excluded-models"] || []
    });
  };

  for (const { type, list, provider } of AI_PROVIDER_TYPES) {
    if (type === "openai-compatibility") continue;
    for (const [index, entry] of (config[list] || []).entries()) {
      const apiKey = (entry["api-key"] || "").trim();
      const baseURL = (entry["base-url"] || "").trim();
      if (!apiKey && !baseURL) continue;
      const parts = [apiKey, baseURL, entry["proxy-url"]];
      if (type !== "vertex") parts.push(entry.prefix, sortedConfiguredHeaders(entry.headers));
      add(
        {
          config_ref: await configuredEntryRef(provider + ":apikey", parts, counters),
          ...configuredProviderDetails(entry, type, groupNames.get(entry["auth-index"])),
          provider,
          key_preview: previewCredential(apiKey),
          weight: Math.max(0, entry.weight ?? 1),
          disabled: (entry["excluded-models"] || []).some((model) => model.trim() === "*")
        },
        list,
        index
      );
    }
  }

  for (const [index, entry] of (config["openai-compatibility"] || []).entries()) {
    const providerName = (entry.name || "").trim().toLowerCase() || "openai-compatibility";
    const provider =
      providerName === "openai-compatibility" || providerName.startsWith("openai-compatible-")
        ? providerName
        : "openai-compatible-" + providerName;
    const kind = "selection:openai-compatibility:" + providerName;
    const details = {
      ...configuredProviderDetails(entry, "openai-compatibility", entry.name),
      provider,
      disabled: entry.disabled === true
    };
    const apiKeys = entry["api-key-entries"] || [];
    if (!apiKeys.length) {
      add(
        { config_ref: await configuredEntryRef(kind, [entry["base-url"]], counters), ...details, key_preview: "", weight: 1 },
        "openai-compatibility",
        index
      );
      continue;
    }
    for (const [keyIndex, apiKeyEntry] of apiKeys.entries()) {
      const apiKey = (apiKeyEntry["api-key"] || "").trim();
      add(
        {
          config_ref: await configuredEntryRef(kind, [apiKey, entry["base-url"], apiKeyEntry["proxy-url"]], counters),
          ...details,
          auth_index: apiKeyEntry["auth-index"] || "",
          key_preview: previewCredential(apiKey),
          weight: Math.max(0, apiKeyEntry.weight ?? 1)
        },
        "openai-compatibility",
        index,
        keyIndex
      );
    }
  }

  if (generation !== sessionGeneration) throw new StaleRequestError();
  return { credentials, targets };
}

function configuredProviderTarget(provider, targets) {
  const target = targets.get(provider.config_ref);
  if (!target) throw new UIError(m("ui.provider_config_changed"));
  return target;
}

async function readConfiguredProviderTargets(providers) {
  const { targets } = await readProviderConfiguration();
  for (const provider of providers) configuredProviderTarget(provider, targets);
  return targets;
}

function writeConfiguredProvider(provider, changes) {
  return queueAdminData("configuration", async () => {
    const { config, targets } = await readProviderConfiguration();
    const target = configuredProviderTarget(provider, targets);
    const entry = config[target.list][target.index];
    const patch = {};
    if (changes.priority !== undefined) patch.priority = changes.priority;
    if (changes.weight !== undefined) {
      if (target.list === "openai-compatibility") {
        if (target.keyIndex === null || !entry["api-key-entries"]?.[target.keyIndex]) throw new UIError(m("ui.provider_config_changed"));
        patch["api-key-entries"] = entry["api-key-entries"].map(({ "auth-index": _, ...item }, index) => ({
          ...item,
          ...(index === target.keyIndex ? { weight: changes.weight } : {})
        }));
      } else patch.weight = changes.weight;
    }
    if (Object.keys(patch).length) await api("PATCH", "/v0/management/" + target.list, { index: target.index, value: patch });
    if (changes.note !== undefined)
      await plugin("POST", "/ai-providers/note", { ref: provider.ref, note: changes.note }, { command: true });
  });
}

function setConfiguredProviderEnabled(target, enabled) {
  const value =
    target.list === "openai-compatibility"
      ? { disabled: !enabled }
      : {
          "excluded-models": enabled
            ? target.excludedModels.filter((model) => model.trim() !== "*")
            : [...new Set([...target.excludedModels, "*"])]
        };
  return queueAdminData("configuration", () => api("PATCH", "/v0/management/" + target.list, { index: target.index, value }));
}
