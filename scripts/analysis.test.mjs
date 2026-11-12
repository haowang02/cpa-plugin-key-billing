import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";
import vm from "node:vm";

const source = fs.readFileSync(new URL("../internal/plugin/web/analysis-trend.js", import.meta.url), "utf8");
const usageTrendSeries = vm.runInNewContext(source + "; usageTrendSeries", { m: String });

test("daily trends follow the browser's calendar days across DST", () => {
  process.env.TZ = "America/New_York";
  for (const [midnight, hours] of [["2026-03-08T00:00:00-05:00", 23], ["2026-11-01T00:00:00-04:00", 25]]) {
    const hourly = Array.from({ length: hours + 1 }, () => 1);
    const zero = hourly.map(() => 0);
    const days = usageTrendSeries(
      {
        start: midnight,
        requests: hourly,
        input_tokens: hourly.map((_, hour) => (hour ? 0 : 90)),
        cache_read_tokens: hourly.map((_, hour) => (hour < 2 ? 5 : 0)),
        cache_write_tokens: zero
      },
      true
    );
    assert.deepEqual([...days.times], [Date.parse(midnight), Date.parse(midnight) + hours * 3600000]);
    assert.deepEqual([...days.requests], [hours, 1]);
    // The day's cache share weighs its hours by their input instead of averaging hourly shares.
    assert.deepEqual([...days.cache_rate], [10, 0]);
  }
});
