let chartLoadPromise;
let chartError = "";
function loadCharts() {
  if (typeof Chart === "function") return Promise.resolve();
  if (chartLoadPromise) return chartLoadPromise;
  chartError = "";
  chartLoadPromise = new Promise((resolve, reject) => {
    const script = document.createElement("script");
    const failed = (message) => {
      clearTimeout(timer);
      script.onload = script.onerror = null;
      script.remove();
      chartLoadPromise = null;
      chartError = message;
      reject(new UIError(message));
    };
    const timer = setTimeout(() => failed(m("ui.chart_loading_timed_out")), 10000);
    script.src = "https://cdn.jsdelivr.net/npm/chart.js@4.4.7/dist/chart.umd.min.js";
    script.onload = () => {
      if (typeof Chart !== "function") return failed(m("ui.chart_library_failed_to_initialize"));
      clearTimeout(timer);
      script.onload = script.onerror = null;
      resolve();
    };
    script.onerror = () => failed(m("ui.failed_to_load_charts"));
    document.head.append(script);
  }).finally(renderLoadedCharts);
  return chartLoadPromise;
}

function renderLoadedCharts() {
  const account = currentRole === "account";
  if (currentRole && activeTab(account) === "analysis" && analysisQueries[currentRole].value) renderAnalysis(account);
}

function loadAnalysis(account, reload) {
  const tasks = [readAnalysis(account, reload).then(() => readSpeed(account, reload))];
  if (typeof Chart !== "function" && (!chartError || reload)) tasks.push(loadCharts());
  return settleLoads(tasks);
}

function analysisRequest(account) {
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  const filters = apiKeyFilter(account);
  const usage = { ...selectedRange(account), ...filters };
  return {
    params: new URLSearchParams({ ...usage, timezone }),
    selection: JSON.stringify([sharedTimeRanges[account ? "account" : "admin"], filters, timezone]),
    usage
  };
}

function readAnalysis(account, reload) {
  const role = account ? "account" : "admin";
  const { params, selection, usage } = analysisRequest(account);
  return analysisQueries[role].read(
    selection,
    async () => {
      const revision = keyDirectory.beginRead();
      const value = await (account ? accountAPI("/analysis?" + params) : plugin("GET", "/analysis?" + params));
      value.usage_query = usage;
      if (!account) {
        const distribution = value.usage_distribution;
        distribution.api_keys = keyDirectory.receive(distribution.api_keys, revision, { field: "key", fallback: true });
      }
      return value;
    },
    reload
  );
}

const speedModels = { admin: "", account: "" };

function speedModelRows(analysis) {
  return analysis.usage_distribution.models.filter((row) => row.key).sort((a, b) => b.requests - a.requests || a.key.localeCompare(b.key));
}

// An explicit choice applies while the model has requests in the selected
// range; otherwise the most requested model is shown.
function selectedSpeedModel(account) {
  const role = account ? "account" : "admin";
  const analysis = analysisQueries[role].value;
  const rows = analysis ? speedModelRows(analysis) : [];
  return (rows.find((row) => row.key === speedModels[role]) || rows[0])?.key || "";
}

// Speed belongs to the loaded analysis and the model selected from it.
function speedKey(role, model) { return JSON.stringify([analysisQueries[role].key, model]); }

function readSpeed(account, reload) {
  const role = account ? "account" : "admin";
  const model = selectedSpeedModel(account);
  if (!model) {
    speedQueries[role].clear();
    return Promise.resolve(null);
  }
  const { params } = analysisRequest(account);
  params.set("model", model);
  return speedQueries[role].read(
    speedKey(role, model),
    () => (account ? accountAPI("/analysis/speed?" + params) : plugin("GET", "/analysis/speed?" + params)),
    reload
  );
}

const ANALYSIS_COLOR_PROPERTIES = [
  "--analysis-color-1",
  "--analysis-color-2",
  "--analysis-color-3",
  "--analysis-color-4",
  "--analysis-color-5",
  "--analysis-color-6"
];
const analysisDimensions = { admin: "api_keys", account: "models" };
const analysisMetrics = { admin: "total_tokens", account: "total_tokens" };
const DISTRIBUTION_METRICS = {
  total_tokens: { label: m("ui.total_tokens"), option: m("ui.token_share"), format: (row) => tokens(row.total_tokens) },
  requests: { label: m("ui.requests_4"), option: m("ui.request_share"), format: (row) => int(row.requests) },
  cost_usd: { label: m("ui.total_cost"), option: m("ui.cost_share"), format: (row) => usageMoney(row.cost_usd, row.charged_in) }
};
function emptyAnalysisCharts() { return { trend: null, distribution: null, speed: null, usageRendered: null, speedRendered: null }; }
const analysisCharts = { admin: emptyAnalysisCharts(), account: emptyAnalysisCharts() };

function destroyAnalysisCharts(account) {
  const role = account ? "account" : "admin";
  for (const kind of ["trend", "distribution", "speed"]) analysisCharts[role][kind]?.destroy();
  analysisCharts[role] = emptyAnalysisCharts();
  $(account ? "account-analysis-body" : "analysis-body").replaceChildren();
}

function analysisLegendItem(color, label) {
  return el(
    "span",
    { class: "analysis-legend-item", style: "--legend-color:" + color },
    el("i", { class: "analysis-legend-dot", "aria-hidden": "true" }),
    label
  );
}

function sameRender(previous, next) { return !!previous && Object.keys(next).every((key) => previous[key] === next[key]); }

function analysisChartPanel(className, id, label) {
  return el("div", { class: className }, el("canvas", { id, role: "img", "aria-label": label }), el("div", { class: "empty hidden" }));
}

// The canvas and Chart instance belong to the page, not to a response.
// Replacing options also replaces tooltip callbacks with the current data.
function updateAnalysisChart(account, kind, canvas, config, { empty = "", animate = false } = {}) {
  config = window.billingI18n.chartValue(config);
  const charts = analysisCharts[account ? "account" : "admin"];
  const message = empty || (typeof Chart !== "function" ? chartError || m("ui.loading_charts") : "");
  const status = canvas.parentElement.querySelector(".empty");
  canvas.classList.toggle("hidden", !!message);
  status.classList.toggle("hidden", !message);
  setText(status, message);
  if (message) return;
  if (charts[kind]) {
    charts[kind].data = config.data;
    charts[kind].options = config.options;
    charts[kind].update(animate ? undefined : "none");
  } else { charts[kind] = new Chart(canvas, config); }
}

function themeColor(property, fallback) {
  const probe = document.createElement("span");
  probe.style.color = "var(" + property + ", " + fallback + ")";
  probe.style.display = "none";
  document.body.appendChild(probe);
  const color = getComputedStyle(probe).color || fallback;
  probe.remove();
  return color;
}

function analysisColors() {
  const fallbacks = ["#3478f6", "#e5a311", "#22a85a", "#8b5cf6", "#d84a4a", "#23a6c7"];
  return ANALYSIS_COLOR_PROPERTIES.map((property, index) => themeColor(property, fallbacks[index]));
}

function chartTooltip(options) {
  return {
    backgroundColor: themeColor("--floating-surface", "#2a2723"),
    borderColor: themeColor("--border-primary", "#4a453f"),
    borderWidth: 1,
    titleColor: themeColor("--text-primary", "#f6f4f1"),
    bodyColor: themeColor("--text-secondary", "#c9c3bb"),
    footerColor: themeColor("--text-primary", "#f6f4f1"),
    ...options
  };
}

function chartAxisColors() {
  return { text: themeColor("--text-tertiary", "#8b95a6"), grid: themeColor("--analysis-grid-color", "rgba(45, 42, 38, 0.08)") };
}

function compactChart(width) { return width <= 520; }
function timeTickLimit(width) { return compactChart(width) ? 4 : 8; }

function timeAxis(axis, width) {
  return {
    grid: { display: false, drawTicks: false },
    border: { color: axis.grid },
    ticks: { color: axis.text, maxRotation: 0, autoSkip: true, maxTicksLimit: timeTickLimit(width) }
  };
}

function isDailyTrend(points) { return points.length > 1 && new Date(points[1].time) - new Date(points[0].time) > 12 * 3600000; }

function trendLabels(points) {
  const format = new Intl.DateTimeFormat(
    locale(),
    isDailyTrend(points) ? { month: "numeric", day: "numeric" } : { hour: "2-digit", minute: "2-digit", hour12: false }
  );
  return points.map((point) => format.format(new Date(point.time)));
}

function compactDistribution(rows, metric) {
  const sorted = [...rows].sort((a, b) => Number(b[metric] || 0) - Number(a[metric] || 0));
  const total = rows.reduce((sum, row) => sum + Number(row[metric] || 0), 0);
  const visible = sorted.slice(0, 5).map((row) => ({ ...row }));
  const rest = sorted.slice(5);
  if (rest.length) {
    const other = { grouped: true, label: m("ui.other"), rows: rest };
    for (const field of [
      "total_tokens",
      "input_tokens",
      "output_tokens",
      "cache_read_tokens",
      "cache_write_tokens",
      "requests",
      "cost_usd"
    ]) { other[field] = rest.reduce((sum, row) => sum + Number(row[field] || 0), 0); }
    other.charged_in = commonChargedIn(rest);
    visible.push(other);
  }
  for (const row of visible) { row.percent = total > 0 ? (Number(row[metric] || 0) * 100) / total : 0; }
  return visible;
}

function namesIdentity(dimension, row) { return ["api_keys", "sources"].includes(dimension) && !!row?.key; }

function renderUsageDistributionChart(account, rows, dimension, dimensionLabel, metric, colors, animate) {
  const canvas = $(account ? "account-usage-chart" : "usage-chart");
  canvas.setAttribute("aria-label", m("ui.value_value_doughnut_chart", { v0: dimensionLabel, v1: DISTRIBUTION_METRICS[metric].option }));
  const empty = rows.some((row) => row.percent > 0)
    ? ""
    : m("ui.value_is_zero_in_this_time_range", { v0: DISTRIBUTION_METRICS[metric].label });
  updateAnalysisChart(
    account,
    "distribution",
    canvas,
    {
      type: "doughnut",
      data: {
        labels: rows.map((row) =>
          dimension === "api_keys" && !row.grouped ? apiKeyIdentity(row).text : row.label || row.key || m("ui.unknown_2")
        ),
        datasets: [
          {
            data: rows.map((row) => row.percent),
            backgroundColor: rows.map((_, index) => colors[index % colors.length]),
            borderColor: themeColor("--analysis-segment-border", "#f0eee8"),
            borderWidth: 3,
            borderRadius: 7,
            spacing: 1,
            hoverBorderWidth: 3,
            hoverOffset: 8
          }
        ]
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        cutout: "62%",
        layout: { padding: 12 },
        plugins: {
          legend: { display: false },
          tooltip: chartTooltip({
            callbacks: {
              // Canvas text cannot be blurred, so a masked identity is left out of the tooltip entirely.
              title: (items) => {
                const row = items.length ? rows[items[0].dataIndex] : null;
                return row && !(privacyMasked() && namesIdentity(dimension, row))
                  ? dimensionLabel + DISPLAY_SEPARATOR + items[0].label
                  : dimensionLabel;
              },
              label: (context) => {
                const row = rows[context.dataIndex];
                return [
                  DISTRIBUTION_METRICS[metric].option + " " + row.percent.toFixed(2) + "%",
                  ...Object.values(DISTRIBUTION_METRICS).map((item) => item.label + " " + item.format(row))
                ];
              }
            }
          })
        }
      }
    },
    { empty, animate }
  );
}

function renderUsageTrendChart(account, trends, chargedIn, colors) {
  const canvas = $(account ? "account-usage-trend-chart" : "usage-trend-chart");
  const points = trends.total_tokens;
  if (!points.length) {
    canvas.closest(".usage-trend-card").querySelector(".analysis-legend").replaceChildren();
    updateAnalysisChart(account, "trend", canvas, null, { empty: m("ui.no_trend_data_in_this_time_range") });
    return;
  }
  const values = (name) => trends[name].map((point) => Number(point.value || 0));
  const totalTokens = values("total_tokens");
  const cacheWriteTokens = values("cache_write_tokens");
  const average = totalTokens.reduce((sum, value) => sum + value, 0) / totalTokens.length;
  const axis = chartAxisColors();
  const tokenDataset = (label, data, color) => ({
    type: "bar",
    label,
    data,
    yAxisID: "tokens",
    stack: "tokens",
    metric: "tokens",
    backgroundColor: color,
    borderColor: color,
    borderWidth: 0,
    borderRadius: 0,
    maxBarThickness: 42,
    categoryPercentage: 0.76,
    barPercentage: 0.9
  });
  const datasets = [
    tokenDataset(m("ui.input"), values("input_tokens"), colors[0]),
    tokenDataset(m("ui.output"), values("output_tokens"), colors[2]),
    tokenDataset(m("ui.cache_read"), values("cache_read_tokens"), colors[1])
  ];
  if (cacheWriteTokens.some((value) => value > 0)) { datasets.push(tokenDataset(m("ui.cache_write"), cacheWriteTokens, colors[4])); }
  datasets.push({
    type: "line",
    label: m("ui.requests_4"),
    data: values("requests"),
    yAxisID: "requests",
    metric: "requests",
    borderColor: "#ff6048",
    backgroundColor: "#ff6048",
    borderWidth: 2.5,
    borderDash: [7, 5],
    pointRadius: 2,
    pointHoverRadius: 5,
    tension: 0.38
  });
  const costs = values("total_cost");
  if (costs.length) {
    datasets.push({
      type: "line",
      label: m("ui.total_cost"),
      data: costs,
      yAxisID: "cost",
      metric: "cost",
      borderColor: colors[5],
      backgroundColor: colors[5],
      borderWidth: 2.5,
      pointRadius: 2,
      pointHoverRadius: 5,
      tension: 0.38
    });
  }
  datasets.push({
    type: "line",
    label: m("ui.average_value", { v0: tokens(average) }),
    data: totalTokens.map(() => average),
    yAxisID: "tokens",
    stack: "average",
    metric: "average",
    borderColor: axis.text,
    borderWidth: 1.5,
    borderDash: [5, 5],
    pointRadius: 0,
    pointHoverRadius: 0
  });
  const width = canvas.parentElement.clientWidth;
  canvas.closest(".usage-trend-card").querySelector(".analysis-legend")
    .replaceChildren(...datasets.map((dataset) => analysisLegendItem(dataset.borderColor, dataset.label)));
  updateAnalysisChart(account, "trend", canvas, {
    type: "bar",
    data: { labels: trendLabels(points), datasets },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      animation: { duration: 300 },
      onResize: (chart, size) => {
        chart.options.scales.x.ticks.maxTicksLimit = timeTickLimit(size.width);
        chart.options.scales.requests.display = !compactChart(size.width);
        chart.options.scales.cost.display = !compactChart(size.width) && costs.length > 0;
      },
      interaction: { mode: "index", intersect: false },
      plugins: {
        legend: { display: false },
        tooltip: chartTooltip({
          usePointStyle: true,
          boxWidth: 8,
          boxHeight: 8,
          boxPadding: 6,
          filter: (context) => context.dataset.metric !== "average",
          callbacks: {
            labelPointStyle: () => ({ pointStyle: "circle", rotation: 0 }),
            label: (context) => {
              const value = Number(context.raw || 0);
              if (context.dataset.metric === "requests") { return context.dataset.label + ": " + int(value); }
              if (context.dataset.metric === "cost") { return context.dataset.label + ": " + usageMoney(value, chargedIn); }
              return context.dataset.label + ": " + tokens(value);
            },
            footer: (items) => (items.length ? m("ui.total_tokens_value", { v0: tokens(totalTokens[items[0].dataIndex]) }) : "")
          }
        })
      },
      scales: {
        x: { ...timeAxis(axis, width), stacked: true },
        tokens: {
          stacked: true,
          beginAtZero: true,
          grid: { color: axis.grid },
          border: { color: axis.grid },
          ticks: { color: axis.text, maxTicksLimit: 5, callback: (value) => tokens(value) }
        },
        requests: {
          beginAtZero: true,
          position: "right",
          display: !compactChart(width),
          grid: { drawOnChartArea: false },
          ticks: { color: axis.text, precision: 0 }
        },
        cost: {
          beginAtZero: true,
          position: "right",
          display: !compactChart(width) && costs.length > 0,
          grid: { drawOnChartArea: false },
          ticks: { color: axis.text, callback: (value) => usageMoney(value, chargedIn) }
        }
      }
    }
  });
}

function drawAnalysisTrend(canvas, points) {
  const context = canvas.getContext("2d");
  const width = canvas.width;
  const height = canvas.height;
  context.clearRect(0, 0, width, height);
  const values = points.map((point) => Number(point.value || 0));
  if (!values.length) return;
  if (values.length === 1) values.push(values[0]);
  const padding = 12;
  const minimum = Math.min(...values);
  const maximum = Math.max(...values);
  const span = maximum - minimum;
  const coordinates = values.map((value, index) => ({
    x: padding + (index * (width - padding * 2)) / (values.length - 1),
    y: span ? padding + ((maximum - value) * (height - padding * 2)) / span : maximum === 0 ? height - padding : height / 2
  }));
  const line = new Path2D();
  line.moveTo(coordinates[0].x, coordinates[0].y);
  for (let index = 1; index < coordinates.length; index++) {
    const previous = coordinates[index - 1];
    const current = coordinates[index];
    const middle = (previous.x + current.x) / 2;
    line.bezierCurveTo(middle, previous.y, middle, current.y, current.x, current.y);
  }
  const fill = new Path2D(line);
  fill.lineTo(coordinates[coordinates.length - 1].x, height - padding);
  fill.lineTo(coordinates[0].x, height - padding);
  fill.closePath();
  const style = getComputedStyle(canvas);
  context.fillStyle = style.getPropertyValue("--analysis-trend-fill").trim() || "rgba(52, 120, 246, 0.16)";
  context.fill(fill);
  context.strokeStyle = style.getPropertyValue("--analysis-accent").trim() || "#3478f6";
  context.lineWidth = 4;
  context.lineCap = "round";
  context.lineJoin = "round";
  context.stroke(line);
}

function summaryLine(label, value) {
  return el("div", { class: "analysis-summary-line" }, el("span", {}, label), el("strong", { class: "mono" }, value));
}

function tokenBreakdown(usage) {
  const rows = [
    [m("ui.input"), tokens(usage.input_tokens)],
    [m("ui.output"), tokens(usage.output_tokens)],
    [m("ui.cache_read"), tokens(usage.cache_read_tokens)]
  ];
  if (Number(usage.cache_write_tokens || 0) > 0) rows.push([m("ui.cache_write"), tokens(usage.cache_write_tokens)]);
  return rows;
}

function renderAnalysisSummary(summary, trends, daily) {
  const cost = summary.cost;
  const averageLabel = daily ? m("ui.daily_average") : m("ui.hourly_average");
  const average = (total) => (trends.requests.length ? Number(total || 0) / trends.requests.length : 0);
  const peakCacheRate = Math.max(0, ...trends.cache_rate.map((point) => Number(point.value || 0)));
  const card = (title, value, statistic, rows, trend) =>
    el(
      "section",
      { class: "card analysis-summary-card", "aria-label": title },
      el("div", { class: "analysis-summary-title" }, title),
      el("div", { class: "analysis-summary-value mono" }, value),
      statistic,
      el("div", { class: "analysis-summary-details" }, rows),
      el("canvas", {
        class: "analysis-summary-trend",
        width: "600",
        height: "128",
        role: "img",
        "aria-label": m("ui.value_trend", { v0: title }),
        "data-analysis-trend": trend
      })
    );
  const costDetail = (label, amount) => summaryLine(label, usageMoney(amount, cost.charged_in, Number(amount || 0) === 0 ? 4 : undefined));
  const hasCacheWriteCost = Number(cost.cache_write_usd || 0) > 0;
  const costRows = [
    costDetail(m("ui.input"), cost.input_usd),
    costDetail(m("ui.output"), cost.output_usd),
    costDetail(m("ui.cache_read"), cost.cache_read_usd)
  ];
  if (hasCacheWriteCost) costRows.push(costDetail(m("ui.cache_write"), cost.cache_write_usd));
  const tokenRows = tokenBreakdown(summary).map(([label, value]) => summaryLine(label, value));
  return el(
    "div",
    { class: "analysis-summary-grid" },
    card(
      m("ui.total_requests"),
      int(summary.requests),
      summaryLine(averageLabel, int(Math.ceil(average(summary.requests)))),
      [
        summaryLine(m("ui.succeeded"), int(summary.succeeded)),
        summaryLine(m("ui.failed"), int(summary.failed)),
        summaryLine(m("ui.success_rate"), Number(summary.success_rate || 0).toFixed(2) + "%")
      ],
      "requests"
    ),
    card(
      m("ui.total_tokens"),
      tokens(summary.total_tokens),
      summaryLine(averageLabel, tokens(Number(average(summary.total_tokens).toFixed(2)))),
      tokenRows,
      "total_tokens"
    ),
    card(
      m("ui.cache_hit_rate"),
      Number(summary.cache_rate || 0).toFixed(2) + "%",
      summaryLine(daily ? m("ui.daily_peak") : m("ui.hourly_peak"), peakCacheRate.toFixed(2) + "%"),
      [summaryLine(m("ui.cache_read"), tokens(summary.cache_read_tokens)), summaryLine(m("ui.input"), tokens(summary.input_tokens))],
      "cache_rate"
    ),
    card(
      m("ui.total_cost"),
      usageMoney(cost.total_usd, cost.charged_in),
      summaryLine(averageLabel, usageMoney(average(cost.total_usd), cost.charged_in)),
      costRows,
      "total_cost"
    )
  );
}

// A row's breakdown selects the requests of the analysis that the row, or each row it groups, covers.
function distributionUsageQuery(analysis, dimension, row) {
  if (!(row.requests > 0)) return null;
  const rows = row.grouped ? row.rows : [row];
  return { ...analysis.usage_query, dimension, value: rows.map((item) => (dimension === "sources" ? item.filter : item.key)) };
}

function renderAnalysisDistribution(account, { colors = analysisColors(), animate = false } = {}) {
  const role = account ? "account" : "admin";
  const analysis = analysisQueries[role].value;
  const target = $(account ? "account-usage-distribution" : "usage-distribution");
  const labels = { api_keys: "API Key", models: m("ui.model"), sources: m("ui.source") };
  if (account || $("analysis-key").value) delete labels.api_keys;
  const dimension = labels[analysisDimensions[role]] ? analysisDimensions[role] : "models";
  const metric = analysisMetrics[role];
  const rows = compactDistribution(analysis.usage_distribution[dimension], metric);
  for (const row of rows) {
    if (row.grouped) continue;
    if (!row.key)
      row.label = m(dimension === "models" ? "ui.unknown_model" : dimension === "sources" ? "ui.unknown_source" : "ui.unassigned");
    else if (dimension === "sources") row.label = displayLabel(row.label || row.key);
  }
  const metricSelect = el(
    "select",
    {
      "aria-label": m("ui.usage_distribution_metric"),
      onchange: (event) => {
        analysisMetrics[role] = event.target.value;
        saveViewPreference(role, "analysis-metric", event.target.value);
        renderAnalysisDistribution(account, { animate: true });
      }
    },
    Object.entries(DISTRIBUTION_METRICS).map(([key, item]) => el("option", { value: key, text: item.option, selected: key === metric }))
  );
  const tabs = Object.entries(labels).map(([key, label]) =>
    el("button", {
      type: "button",
      class: key === dimension ? "active" : "",
      text: label,
      onclick: () => {
        analysisDimensions[role] = key;
        saveViewPreference(role, "analysis-dimension", key);
        if (key !== dimension) renderAnalysisDistribution(account, { animate: true });
      }
    })
  );
  const list = rows.map((row, index) => {
    const breakdown = modelUsageTooltip(account, distributionUsageQuery(analysis, dimension, row));
    return el(
      "div",
      { class: "usage-distribution-row", style: "--distribution-color:" + colors[index % colors.length] },
      el(
        "div",
        { class: "usage-distribution-heading" },
        dimension === "api_keys" && !row.grouped
          ? el("span", { class: namesIdentity(dimension, row) ? "mask-blur" : "", text: apiKeyIdentity(row).text })
          : el("b", {}, namesIdentity(dimension, row) ? maskedLabelText(row.label) : row.label || row.key || m("ui.unknown_2")),
        el("b", { class: "mono" }, row.percent.toFixed(2) + "%")
      ),
      el("div", { class: "usage-distribution-track" }, el("i", { style: "width:" + row.percent + "%" })),
      el(
        "div",
        { class: "usage-distribution-metrics" },
        Object.entries(DISTRIBUTION_METRICS).map(([key, item]) =>
          el("span", { class: key === metric ? "active" : "", ...hoverTooltipProps(breakdown) }, item.label + " " + item.format(row))
        )
      )
    );
  });
  target.querySelector(".analysis-card-header").replaceChildren(
    el("h2", {}, m("ui.usage_distribution")),
    el(
      "div",
      { class: "row analysis-card-controls" },
      el("div", { class: "usage-distribution-tabs" }, tabs),
      el("span", { class: "native-select" }, metricSelect)
    )
  );
  target.querySelector(".usage-distribution-list").replaceChildren(...list);
  target.querySelector(".usage-distribution-layout").classList.toggle("hidden", !rows.length);
  target.querySelector(".usage-distribution-empty").classList.toggle("hidden", !!rows.length);
  renderUsageDistributionChart(account, rows, dimension, labels[dimension], metric, colors, animate);
  refreshHoverTooltip();
}

function renderSpeedTrend(account, view) {
  const role = account ? "account" : "admin";
  const prefix = account ? "account-" : "";
  const charts = analysisCharts[role];
  const analysis = analysisQueries[role].value;
  const query = speedQueries[role];
  const model = selectedSpeedModel(account);
  // Until its read starts, the speed query still holds the previous analysis' data.
  const current = !!model && query.key === speedKey(role, model);
  const state = { ...view, analysis, model, speed: current ? query.value : null, error: current && !query.value ? query.error : "" };
  if (sameRender(charts.speedRendered, state)) return;
  const card = $(prefix + "speed-trend");
  const rows = speedModelRows(analysis);
  const select = el(
    "select",
    {
      "aria-label": m("ui.speed_trend_model"),
      onchange: (event) => {
        speedModels[role] = event.target.value;
        guard(() => loadPage(account));
      }
    },
    rows.map((row) => el("option", { value: row.key, text: row.key + DISPLAY_SEPARATOR + int(row.requests), selected: row.key === model }))
  );
  card.querySelector(".analysis-card-controls").replaceChildren(...(rows.length ? [el("span", { class: "native-select" }, select)] : []));
  const speed = state.speed;
  const stats = speed?.summary;
  const colors = analysisColors();
  const palette = { median: colors[1], box: colors[0], whisker: chartAxisColors().text };
  const legend = card.querySelector(".analysis-legend");
  legend.replaceChildren();
  if (stats?.samples) {
    const [median, box, whisker] = speedLines(stats);
    legend.append(
      analysisLegendItem(palette.median, median),
      analysisLegendItem(palette.box, box),
      analysisLegendItem(palette.whisker, whisker)
    );
  }
  let empty = "";
  if (!model) empty = m("ui.no_request_events_in_this_time_range");
  else if (!speed) empty = state.error ? m("ui.failed_to_load_speed_trend") : m("ui.loading");
  else if (!stats.samples) empty = m("ui.no_successful_requests_for_this_model");
  renderSpeedChart(account, $(prefix + "speed-trend-chart"), speed, palette, empty);
  charts.speedRendered = state;
}

function speedLines(stats) {
  return [
    m("ui.metric_value", { v0: m("ui.median_tps"), v1: tokenRate(stats.p50) }),
    m("ui.metric_value", { v0: "P25–P75", v1: tokenRateRange(stats.p25, stats.p75) }),
    m("ui.metric_value", { v0: "P10–P90", v1: tokenRateRange(stats.p10, stats.p90) })
  ];
}

function renderSpeedChart(account, canvas, speed, palette, empty) {
  if (empty) {
    updateAnalysisChart(account, "speed", canvas, null, { empty });
    return;
  }
  const points = speed.trend;
  const axis = chartAxisColors();
  const width = canvas.parentElement.clientWidth;
  const surface = themeColor("--analysis-segment-border", "#f0eee8");
  const boxFill = "color-mix(in srgb, " + palette.box + " 22%, " + surface + ")";
  const range = (from, to) => points.map((point) => (point.samples ? [point[from], point[to]] : null));
  // The median line draws over its box (P25–P75) and whiskers (P10–P90).
  const datasets = [
    {
      type: "line",
      data: points.map((point) => (point.samples ? point.p50 : null)),
      borderColor: palette.median,
      backgroundColor: palette.median,
      borderWidth: 2.5,
      cubicInterpolationMode: "monotone",
      pointRadius: 3.5,
      pointHoverRadius: 5,
      pointBorderColor: surface,
      pointBorderWidth: 1.5
    },
    {
      type: "bar",
      data: range("p25", "p75"),
      grouped: false,
      backgroundColor: boxFill,
      // Chart.js cannot derive a hover color from color-mix().
      hoverBackgroundColor: boxFill,
      borderColor: palette.box,
      borderWidth: 1.5,
      borderRadius: 2,
      borderSkipped: false,
      maxBarThickness: 16,
      barPercentage: 0.6
    },
    ...[range("p10", "p25"), range("p75", "p90")].map((data) => ({
      type: "bar",
      data,
      grouped: false,
      backgroundColor: palette.whisker,
      barThickness: 1.5
    }))
  ];
  updateAnalysisChart(account, "speed", canvas, {
    type: "bar",
    data: { labels: trendLabels(points), datasets },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      animation: { duration: 300 },
      onResize: (chart, size) => { chart.options.scales.x.ticks.maxTicksLimit = timeTickLimit(size.width); },
      interaction: { mode: "index", intersect: false },
      plugins: {
        legend: { display: false },
        tooltip: chartTooltip({
          usePointStyle: true,
          boxWidth: 8,
          boxHeight: 8,
          boxPadding: 6,
          filter: (item) => item.datasetIndex === 0,
          callbacks: {
            labelPointStyle: () => ({ pointStyle: "circle", rotation: 0 }),
            label: (item) => speedLines(points[item.dataIndex]),
            footer: (items) => (items.length ? m("ui.successful_requests_value", { v0: int(points[items[0].dataIndex].samples) }) : "")
          }
        })
      },
      scales: {
        x: timeAxis(axis, width),
        // Medians and boxes encode position, not length, so the axis fits the data.
        y: {
          beginAtZero: false,
          grace: "5%",
          grid: { color: axis.grid },
          border: { color: axis.grid },
          ticks: { color: axis.text, maxTicksLimit: 6, callback: (value) => Number(value.toFixed(1)) + " t/s" }
        }
      }
    }
  });
}

function renderAnalysisUsage(account, content, analysis, view) {
  const charts = analysisCharts[account ? "account" : "admin"];
  const state = { ...view, analysis, identities: account ? null : keyDirectory.value };
  if (sameRender(charts.usageRendered, state)) return;
  const trends = analysis.trends;
  const colors = analysisColors();
  content.querySelector(".analysis-summary-grid")
    .replaceWith(renderAnalysisSummary(analysis.summary, trends, isDailyTrend(trends.total_tokens)));
  for (const canvas of content.querySelectorAll("[data-analysis-trend]")) {
    drawAnalysisTrend(canvas, trends[canvas.dataset.analysisTrend]);
  }
  renderUsageTrendChart(account, trends, analysis.summary.cost.charged_in, colors);
  renderAnalysisDistribution(account, { colors });
  charts.usageRendered = state;
}

function renderAnalysis(account) {
  const target = $(account ? "account-analysis-body" : "analysis-body");
  const load = account ? analysisQueries.account : analysisQueries.admin;
  let content = target.querySelector(".analysis-content");
  if (!content) {
    const prefix = account ? "account-" : "";
    content = el(
      "div",
      { class: "analysis-content" },
      el("div", { class: "analysis-summary-grid" }),
      el(
        "section",
        { class: "card usage-trend-card" },
        el("div", { class: "row spread analysis-card-header" }, el("h2", {}, m("ui.usage_trend"))),
        el("div", { class: "analysis-legend" }),
        analysisChartPanel("analysis-trend-chart", prefix + "usage-trend-chart", m("ui.usage_trend_chart"))
      ),
      el(
        "section",
        { id: prefix + "usage-distribution", class: "card usage-distribution-card" },
        el("div", { class: "row spread analysis-card-header" }),
        el(
          "div",
          { class: "usage-distribution-layout" },
          analysisChartPanel("usage-distribution-chart", prefix + "usage-chart", m("ui.usage_distribution_chart")),
          el("div", { class: "usage-distribution-list" })
        ),
        el("div", { class: "empty usage-distribution-empty", text: m("ui.no_request_events_in_this_time_range") })
      ),
      el(
        "section",
        { id: prefix + "speed-trend", class: "card" },
        el(
          "div",
          { class: "row spread analysis-card-header" },
          el("h2", {}, m("ui.speed_trend")),
          el("div", { class: "row analysis-card-controls" })
        ),
        el("div", { class: "analysis-legend" }),
        analysisChartPanel("analysis-trend-chart", prefix + "speed-trend-chart", m("ui.speed_trend_chart"))
      )
    );
    target.replaceChildren(el("div", { class: "card empty analysis-state" }), content);
  }
  const analysis = load.value;
  const status = target.querySelector(".analysis-state");
  status.classList.toggle("hidden", !!analysis);
  content.classList.toggle("hidden", !analysis);
  if (!analysis) {
    setText(status, load.error ? m("ui.failed_to_load_analysis_data") : m("ui.loading"));
    return;
  }
  const view = {
    theme: window.billingTheme.current(),
    library: typeof Chart === "function" ? "ready" : chartError || "loading",
    language: locale(),
    money: moneyDisplayKey()
  };
  renderAnalysisUsage(account, content, analysis, view);
  renderSpeedTrend(account, view);
}
