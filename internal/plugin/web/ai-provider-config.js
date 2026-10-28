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

async function configuredCredentialRef(kind, parts, counters) {
  // These hashes must stay byte-identical to CPA's StableIDGenerator and
  // the plugin's CredentialFingerprint so exact bindings match candidates.
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

// The v0 lists name only OpenAI-compatible providers, while CLIProxyAPI v8 names
// every group of keys. Hosts before v8, or without providers, answer 404.
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
  const disabledCounters = new Map();
  // Config identities stay stable when disabled providers lose their runtime identity.
  const configCounters = new Map();
  const credentials = [];
  const targets = new Map();
  const add = (credential, list, index, configRef = credential.ref, keyIndex = null) => {
    credentials.push({ ...credential, config_ref: configRef });
    targets.set(credential.ref, {
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
          ref: await configuredCredentialRef(provider + ":apikey", parts, counters),
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
    // Disabled providers have no runtime identity and must not consume the live ID counters.
    const disabled = entry.disabled === true;
    const kindPrefix = disabled ? "disabled:" : "";
    const providerCounters = disabled ? disabledCounters : counters;
    const providerName = (entry.name || "").trim().toLowerCase() || "openai-compatibility";
    const provider =
      providerName === "openai-compatibility" || providerName.startsWith("openai-compatible-")
        ? providerName
        : "openai-compatible-" + providerName;
    const apiKeys = entry["api-key-entries"] || [];
    if (!apiKeys.length) {
      add(
        {
          ref: await configuredCredentialRef(kindPrefix + "openai-compatibility:" + providerName, [entry["base-url"]], providerCounters),
          ...configuredProviderDetails(entry, "openai-compatibility", entry.name),
          provider,
          key_preview: "",
          weight: 1,
          disabled
        },
        "openai-compatibility",
        index,
        await configuredCredentialRef("selection:openai-compatibility:" + providerName, [entry["base-url"]], configCounters)
      );
      continue;
    }
    for (const [keyIndex, apiKeyEntry] of apiKeys.entries()) {
      const apiKey = (apiKeyEntry["api-key"] || "").trim();
      add(
        {
          ref: await configuredCredentialRef(
            kindPrefix + "openai-compatibility:" + providerName,
            [apiKey, entry["base-url"], apiKeyEntry["proxy-url"]],
            providerCounters
          ),
          ...configuredProviderDetails(entry, "openai-compatibility", entry.name),
          auth_index: apiKeyEntry["auth-index"] || "",
          provider,
          key_preview: previewCredential(apiKey),
          weight: Math.max(0, apiKeyEntry.weight ?? 1),
          disabled
        },
        "openai-compatibility",
        index,
        await configuredCredentialRef(
          "selection:openai-compatibility:" + providerName,
          [apiKey, entry["base-url"], apiKeyEntry["proxy-url"]],
          configCounters
        ),
        keyIndex
      );
    }
  }

  if (generation !== sessionGeneration) throw new StaleRequestError();
  return { credentials, targets };
}

function configuredProviderTarget(provider, targets) {
  const target = targets.get(provider.ref);
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
