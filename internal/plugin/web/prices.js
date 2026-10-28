const priceSelection = createTableSelection({
  id: (price) => price.custom_price_model_id || price.model_id,
  label: (price) => price.model_id,
  eligible: (price) => price.source === "custom",
  disabledTip: m("ui.batch_custom_prices_only"),
  context: () => $("price-filter").value.trim().toLowerCase(),
  render: renderPriceTable,
  actions: (prices) => [priceSelection.action(m("ui.delete_custom_prices"), prices, deleteSelectedPrices, true)]
});

function deleteSelectedPrices(prices) {
  return openActionDialog(m("ui.batch_delete_custom_prices", { count: prices.length }), m("ui.batch_delete_prices_note"), undefined, () =>
    priceSelection.run(prices, (price) => writeAdmin("DELETE", "/prices?model_id=" + encodeURIComponent(priceSelection.id(price))), {
      success: m("ui.custom_price_deleted")
    })
  );
}

function priceSourceTag(source) {
  switch (source) {
    case "custom":
      return el("span", { class: "tag ok", text: m("ui.custom") });
    case "builtin":
      return el("span", { class: "tag plain", text: m("ui.built_in") });
    case "reference":
      return el("span", { class: "tag info", text: m("ui.reference") });
    default:
      return el("span", { class: "tag warn", text: m("ui.unpriced") });
  }
}

const PRICE_FIELDS = [
  ["input_per_1m", "ui.input"],
  ["output_per_1m", "ui.output"],
  ["cache_read_per_1m", "ui.cache_read"],
  ["cache_write_per_1m", "ui.cache_write"]
];

function priceValue(value, currency) {
  return value === null || value === undefined ? el("span", { class: "muted", text: "—" }) : money(value, currency);
}

function longContextPrices(row) {
  const tier = row.long_context;
  const threshold = tokenThreshold(tier.threshold_input_tokens);
  const tooltip = () => [
    el(
      "table",
      { class: "compact-table" },
      el(
        "thead",
        {},
        el(
          "tr",
          {},
          el("th"),
          el("th", { class: "num", text: m("ui.standard_context_value", { v0: threshold }) }),
          el("th", { class: "num", text: m("ui.long_context_value", { v0: threshold }) })
        )
      ),
      el(
        "tbody",
        {},
        PRICE_FIELDS.map(([field, label]) =>
          el(
            "tr",
            {},
            el("td", { text: m(label) }),
            el("td", { class: "num" }, priceValue(row[field], row.currency)),
            el("td", { class: "num" }, priceValue(tier[field], row.currency))
          )
        )
      )
    )
  ];
  return el(
    "span",
    { class: "breakdown-value", ...hoverTooltipProps(tooltip) },
    el("span", { class: "long-context-threshold", text: ">" + threshold }),
    " ",
    PRICE_FIELDS.flatMap(([field], index) => [
      index > 0 && el("span", { class: "muted", text: DISPLAY_SLASH }),
      priceValue(tier[field], row.currency)
    ])
  );
}

function renderPriceTable() {
  if (renderDataPending($("prices-body"), [resources.admin.catalog], m("ui.loading_model_prices"))) return;
  const filter = $("price-filter").value.trim().toLowerCase();
  const rows = resources.admin.catalog.value.prices.filter((row) => !filter || row.model_id.toLowerCase().includes(filter))
    .sort((a, b) => compareModelId(a.model_id, b.model_id));
  const renderRow = (row) => {
    const priced = row.source !== "none";
    return el(
      "tr",
      {},
      el(
        "td",
        { class: "mono" },
        row.model_id,
        row.in_models === false ? el("div", { class: "muted small", text: m("ui.not_in_the_cpa_model_list") }) : null,
        row.custom_price_model_id && row.custom_price_model_id !== row.model_id
          ? el("div", { class: "muted small", text: m("ui.using_the_custom_price_for_value", { v0: row.custom_price_model_id }) })
          : null
      ),
      el("td", {}, priced ? row.currency : priceValue(null)),
      PRICE_FIELDS.map(([field]) => el("td", { class: "num" }, priceValue(priced ? row[field] : null, row.currency))),
      el("td", {}, row.long_context ? longContextPrices(row) : priceValue(null)),
      el("td", {}, priceSourceTag(row.source)),
      el(
        "td",
        { class: "small" },
        el("button", { class: "link", text: m("ui.edit"), onclick: () => editPrice(row) }),
        row.source === "custom" && el("button", { class: "link danger", text: m("ui.delete"), onclick: () => deleteCustomPrice(row) })
      )
    );
  };
  const headers = [
    m("ui.model"),
    m("ui.currency"),
    ...PRICE_FIELDS.map(([, label]) => ({ label: m(label), num: true })),
    m("ui.long_context"),
    m("ui.source"),
    m("ui.actions")
  ];
  renderCollection($("prices-body"), rows, { headers, render: renderRow, selection: priceSelection });
}

function renderReferencePriceStatus() {
  const load = resources.admin.priceStatus;
  const metadata = load.value?.metadata;
  setText(
    $("price-status"),
    [
      metadata?.usable && metadata?.fetched_at
        ? m("ui.reference_prices_updated_value", {
            v0: window.billingI18n.date(metadata.fetched_at, { dateStyle: "short", timeStyle: "medium", hour12: false })
          })
        : load.value ? m("ui.no_reference_prices") : load.error ? "" : m("ui.loading_reference_price_status"),
      metadata?.last_error
    ]
      .filter(Boolean)
      .join("；")
  );
}

let priceReferenceTimer = 0;
let priceReferenceRequest = 0;

function priceNumber(id, emptyValue = 0) {
  const input = $(id);
  // Check badInput before treating an empty value as an omitted field.
  // Browsers can expose an invalid numeric entry as an empty string.
  if (!input.reportValidity()) throw new UIError(m("ui.check_the_values_in_model_pricing"));
  if (input.value.trim() === "") return emptyValue;
  const value = input.valueAsNumber;
  if (!Number.isFinite(value) || value < 0) {
    input.focus();
    throw new UIError(m("ui.the_price_must_be_a_valid_non_negative_number"));
  }
  return value;
}

// Reference prices carry no currency; they are in USD.
function fillPriceFields(row) {
  $("price-currency").value = row.currency || "USD";
  $("price-in").value = row.input_per_1m ?? 0;
  $("price-out").value = row.output_per_1m ?? 0;
  $("price-cr").value = row.cache_read_per_1m ?? "";
  $("price-cw").value = row.cache_write_per_1m ?? "";
  const tier = row.long_context;
  $("price-tiered").checked = !!tier;
  $("price-tier-threshold").value = tier?.threshold_input_tokens ?? 272000;
  $("price-tier-in").value = tier?.input_per_1m ?? 0;
  $("price-tier-out").value = tier?.output_per_1m ?? 0;
  $("price-tier-cr").value = tier?.cache_read_per_1m ?? "";
  $("price-tier-cw").value = tier?.cache_write_per_1m ?? "";
  syncPriceTierFields();
}

function referencePriceText(row) {
  const usd = (value) => money(value, "USD");
  const prices = [m("ui.input_value", { v0: usd(row.input_per_1m) }), m("ui.output_value", { v0: usd(row.output_per_1m) })];
  if (row.cache_read_per_1m !== null && row.cache_read_per_1m !== undefined) {
    prices.push(m("ui.cache_read_value", { v0: usd(row.cache_read_per_1m) }));
  }
  if (row.cache_write_per_1m !== null && row.cache_write_per_1m !== undefined) {
    prices.push(m("ui.cache_write_value", { v0: usd(row.cache_write_per_1m) }));
  }
  if (row.long_context) {
    prices.push(
      m("ui.value_tier_input_valuevalueoutput_value", {
        v0: tokenThreshold(row.long_context.threshold_input_tokens),
        v1: usd(row.long_context.input_per_1m),
        v2: DISPLAY_SEPARATOR,
        v3: usd(row.long_context.output_per_1m)
      })
    );
  }
  return prices.join(DISPLAY_SEPARATOR);
}

function syncPriceTierFields() { $("price-tier-fields").classList.toggle("hidden", !$("price-tiered").checked); }

function renderPriceReferences(prices) {
  const results = $("price-reference-results");
  if (!prices.length) {
    results.replaceChildren(el("div", { class: "price-reference-empty", text: m("ui.no_reference_prices_found") }));
    return;
  }
  results.replaceChildren(
    ...prices.map((row) =>
      el(
        "button",
        {
          class: "price-reference-option",
          type: "button",
          onclick: (event) => {
            fillPriceFields(row);
            for (const option of results.querySelectorAll(".price-reference-option")) option.classList.remove("selected");
            event.currentTarget.classList.add("selected");
          }
        },
        el("span", { class: "mono price-reference-name", text: row.provider_id + "/" + row.model_id }),
        el("span", { class: "price-reference-prices", text: referencePriceText(row) })
      )
    )
  );
  results.scrollTop = 0;
}

async function searchPriceReferences() {
  const query = $("price-reference-search").value.trim();
  const request = ++priceReferenceRequest;
  if (!query) {
    $("price-reference-results").replaceChildren(el("div", { class: "price-reference-empty", text: m("ui.enter_a_model_name_to_search") }));
    return;
  }
  $("price-reference-results").replaceChildren(el("div", { class: "price-reference-empty", text: m("ui.searching") }));
  try {
    const result = await plugin("GET", "/prices/reference?q=" + encodeURIComponent(query) + "&limit=20");
    if (request === priceReferenceRequest) renderPriceReferences(result.prices || []);
  } catch (err) {
    if (request === priceReferenceRequest) {
      if (err instanceof AuthError) {
        $("price-dialog").close();
        fail(err);
        return;
      }
      $("price-reference-results").replaceChildren(
        el("div", { class: "price-reference-empty", text: m("ui.failed_to_search_reference_prices_value", { v0: err.message || err }) })
      );
    }
  }
}

function editPrice(row) {
  const modelID = row.model_id;
  openEditor("price-dialog", null, () => {
    setText($("price-dialog-title"), modelID ? m("ui.edit_model_pricing") : m("ui.add_model_pricing"));
    setText($("price-save"), m("ui.save"));
    $("price-model").value = row.custom_price_model_id || modelID;
    $("price-model").readOnly = !!modelID;
    fillPriceFields(row);
    $("price-reference-search").value = modelID;
    searchPriceReferences();
  });
}

$("price-currency").replaceChildren(...currencyOptions());
$("price-filter").addEventListener("input", renderPriceTable);
$("price-reference-search").addEventListener("input", () => {
  priceReferenceRequest++;
  $("price-reference-results").replaceChildren();
  clearTimeout(priceReferenceTimer);
  priceReferenceTimer = setTimeout(() => {
    priceReferenceTimer = 0;
    searchPriceReferences();
  }, 180);
});
$("price-tiered").addEventListener("change", syncPriceTierFields);
$("price-dialog").addEventListener("close", () => {
  clearTimeout(priceReferenceTimer);
  priceReferenceTimer = 0;
  priceReferenceRequest++;
});
function deleteCustomPrice(row) {
  const model = row.custom_price_model_id || row.model_id;
  const message = m("ui.deleting_the_custom_price_for_value_switches_affected_models_to_reference_pricing_mod", { v0: model });
  openActionDialog(m("ui.delete_custom_price"), message, undefined, async () => {
    await mutateAdmin("DELETE", "/prices?model_id=" + encodeURIComponent(model));
    notify(m("ui.custom_price_deleted"));
  });
}

$("price-new").onclick = () => editPrice({ model_id: "", input_per_1m: 0, output_per_1m: 0 });
$("price-save").onclick = () =>
  submitEditor("price-dialog", async () => {
    const model = $("price-model").value.trim();
    if (!model) throw new UIError(m("ui.enter_a_model_id"));
    const price = {
      model_id: model,
      currency: $("price-currency").value,
      input_per_1m: priceNumber("price-in"),
      output_per_1m: priceNumber("price-out"),
      cache_read_per_1m: priceNumber("price-cr", null),
      cache_write_per_1m: priceNumber("price-cw", null)
    };
    if ($("price-tiered").checked) {
      const threshold = priceNumber("price-tier-threshold");
      if (!Number.isSafeInteger(threshold) || threshold <= 0)
        throw new UIError(m("ui.the_long_context_threshold_must_be_a_valid_positive_integer"));
      price.long_context = {
        threshold_input_tokens: threshold,
        input_per_1m: priceNumber("price-tier-in"),
        output_per_1m: priceNumber("price-tier-out"),
        cache_read_per_1m: priceNumber("price-tier-cr", null),
        cache_write_per_1m: priceNumber("price-tier-cw", null)
      };
    }
    await mutateAdmin("PUT", "/prices", price);
    notify(m("ui.saved_pricing_for_value", { v0: model }));
  });
$("price-reference-refresh").onclick = () =>
  guard(async () => {
    const button = $("price-reference-refresh");
    button.disabled = true;
    try {
      const result = await mutateAdmin("POST", "/prices/reference/refresh", null, { timeoutMS: 45000 });
      notify(result.changed ? m("ui.reference_prices_updated") : m("ui.reference_prices_are_up_to_date"));
    } finally { button.disabled = false; }
  });
