const TREND_METRICS = {
  total_tokens: { label: m("ui.tokens"), format: (value) => tokens(value) },
  requests: { label: m("ui.requests"), color: 4, format: (value) => int(value) },
  total_cost: { label: m("ui.cost"), color: 5, format: (value, chargedIn) => usageMoney(value, chargedIn) }
};
const analysisTrendMetrics = { admin: "total_tokens", account: "total_tokens" };
const analysisTrendBuckets = { admin: "hour", account: "hour" };

// The server counts usage per hour from trends.start. Days are the browser's calendar days, which DST makes 23 or 25 hours long.
function usageTrendSeries(trends, daily) {
  const { start, ...hourly } = trends;
  const first = new Date(start).getTime();
  const times = [];
  const buckets = hourly.requests.map((_, hour) => {
    const time = first + hour * 3600000;
    const bucket = daily ? new Date(time).setHours(0, 0, 0, 0) : time;
    if (times.at(-1) !== bucket) times.push(bucket);
    return times.length - 1;
  });
  const series = { times };
  for (const [name, values] of Object.entries(hourly)) {
    series[name] = times.map(() => 0);
    values.forEach((value, hour) => { series[name][buckets[hour]] += value; });
  }
  series.cache_rate = times.map((_, index) => {
    const read = series.cache_read_tokens[index];
    const input = series.input_tokens[index] + read + series.cache_write_tokens[index];
    return input > 0 ? (read * 100) / input : 0;
  });
  return series;
}

function renderUsageTrendControls(account) {
  const role = account ? "account" : "admin";
  const choose = (choices, preference) => (value) => {
    choices[role] = value;
    saveViewPreference(role, preference, value);
    renderUsageTrendChart(account);
  };
  const metric = segmentedControl(
    m("ui.trend_metric"),
    Object.entries(TREND_METRICS).map(([value, { label }]) => ({ value, label })),
    analysisTrendMetrics[role],
    choose(analysisTrendMetrics, "trend-metric")
  );
  const chooseBucket = choose(analysisTrendBuckets, "trend-bucket");
  const bucket = el(
    "ui-select",
    { "aria-label": m("ui.trend_bucket"), onchange: (event) => chooseBucket(event.target.value) },
    Object.entries({ hour: m("ui.by_hour"), day: m("ui.by_day") }).map(([value, text]) =>
      el("option", { value, text, selected: value === analysisTrendBuckets[role] })
    )
  );
  $("usage-trend-chart").closest(".usage-trend-card").querySelector(".analysis-controls").replaceChildren(metric, bucket);
}

function renderUsageTrendChart(account, colors = analysisColors()) {
  const role = account ? "account" : "admin";
  const analysis = analysisQueries[role].value;
  const metric = analysisTrendMetrics[role];
  const daily = analysisTrendBuckets[role] === "day";
  const series = usageTrendSeries(analysis.trends, daily);
  const canvas = $("usage-trend-chart");
  const legend = canvas.closest(".usage-trend-card").querySelector(".analysis-legend");
  if (!series.times.length) {
    legend.replaceChildren();
    updateAnalysisChart(account, "trend", canvas, null, { empty: m("ui.no_trend_data_in_this_time_range") });
    return;
  }
  const format = (value) => TREND_METRICS[metric].format(value, analysis.summary.cost.charged_in);
  const totals = series[metric];
  const average = totals.reduce((sum, value) => sum + value, 0) / totals.length;
  const axis = chartAxisColors();
  const bars = (label, data, color) => ({
    type: "bar",
    label,
    data,
    yAxisID: "usage",
    stack: "usage",
    backgroundColor: color,
    borderColor: color,
    borderWidth: 0,
    borderRadius: 0,
    maxBarThickness: 42,
    categoryPercentage: 0.76,
    barPercentage: 0.9
  });
  const datasets = [];
  if (metric === "total_tokens") {
    datasets.push(
      bars(m("ui.input"), series.input_tokens, colors[0]),
      bars(m("ui.output"), series.output_tokens, colors[2]),
      bars(m("ui.cache_read"), series.cache_read_tokens, colors[1])
    );
    if (series.cache_write_tokens.some((value) => value > 0))
      datasets.push(bars(m("ui.cache_write"), series.cache_write_tokens, colors[4]));
  } else { datasets.push(bars(TREND_METRICS[metric].label, totals, colors[TREND_METRICS[metric].color])); }
  datasets.push({
    type: "line",
    label: m("ui.average_value", { v0: format(average) }),
    data: totals.map(() => average),
    yAxisID: "usage",
    stack: "average",
    borderColor: axis.text,
    borderWidth: 1.5,
    borderDash: [5, 5],
    pointRadius: 0,
    pointHoverRadius: 0
  });
  legend.replaceChildren(...datasets.map((dataset) => analysisLegendItem(dataset.borderColor, dataset.label)));
  updateAnalysisChart(account, "trend", canvas, {
    type: "bar",
    data: { labels: trendLabels(series.times, daily), datasets },
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
          filter: (context) => context.dataset.type === "bar",
          callbacks: {
            labelPointStyle: () => ({ pointStyle: "circle", rotation: 0 }),
            label: (context) => context.dataset.label + ": " + format(context.raw),
            footer: (items) =>
              metric === "total_tokens" && items.length ? m("ui.total_tokens_value", { v0: format(totals[items[0].dataIndex]) }) : ""
          }
        })
      },
      scales: {
        x: { ...timeAxis(axis, canvas.parentElement.clientWidth), stacked: true },
        usage: {
          stacked: true,
          beginAtZero: true,
          grid: { color: axis.grid },
          border: { color: axis.grid },
          ticks: {
            color: axis.text,
            maxTicksLimit: 5,
            precision: metric === "requests" ? 0 : undefined,
            callback: (value) => format(value)
          }
        }
      }
    }
  });
}
