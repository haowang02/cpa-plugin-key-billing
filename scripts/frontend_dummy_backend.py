#!/usr/bin/env python3

import argparse
import json
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlparse


ROOT = Path(__file__).resolve().parents[1]
UI_PATH = ROOT / "internal" / "plugin" / "ui.html"
API_BASE = "/v0/management/plugins/cpa-key-billing"
NOW = datetime(2026, 8, 9, 12, 0, tzinfo=timezone.utc)


def iso(value):
    return value.isoformat().replace("+00:00", "Z")


PLANS = [
    {
        "id": "team-monthly",
        "name": "研发团队",
        "amount_usd": 500,
        "period": {"kind": "monthly"},
    },
    {
        "id": "one-time",
        "name": "一次性额度",
        "amount_usd": 50,
        "period": {"kind": "never"},
    },
]


def make_key(index):
    plan_id = "team-monthly" if index <= 14 else "one-time" if index == 15 else ""
    plan_name = "研发团队" if plan_id == "team-monthly" else "一次性额度" if plan_id else ""
    requests = 1200 + index * 37
    cost = round(7.5 + index * 0.83, 4)
    result = {
        "scope": f"{index:064x}",
        "preview": f"sk-demo…{index:04d}",
        "label": f"成员 {index:02d}" if index % 3 else "",
        "in_config": True,
        "plan_id": plan_id,
        "plan_name": plan_name,
        "unlimited": not plan_id,
        "blocked": False,
        "limit_usd": 500 if plan_id == "team-monthly" else 50 if plan_id else 0,
        "spent_usd": cost if plan_id else 0,
        "used_percent": cost / (500 if plan_id == "team-monthly" else 50) * 100 if plan_id else 0,
        "lifetime": {"requests": requests, "cost_usd": cost},
    }
    if plan_id == "team-monthly":
        result["cycle_end_at"] = iso(NOW + timedelta(days=25))
    return result


# A Key deleted from CPA: the panel keeps it out of the Key list and the plan
# bindings, while its billing log rows still name it.
DELETED_KEY = {
    **make_key(19),
    "in_config": False,
    "deleted_at": iso(NOW - timedelta(days=2)),
    "plan_id": "team-monthly",
    "plan_name": "研发团队",
    "unlimited": False,
    "limit_usd": 500,
}

KEYS = [make_key(index) for index in range(1, 19)] + [DELETED_KEY]
LIVE_KEYS = [key for key in KEYS if not key.get("deleted_at")]

PRICES = [
    {
        "pattern": "gpt-5.6-sol",
        "input_per_1m": 5,
        "output_per_1m": 30,
        "cache_read_per_1m": 0.5,
        "source": "builtin",
        "long_context": {
            "threshold_input_tokens": 272000,
            "input_per_1m": 10,
            "output_per_1m": 45,
            "cache_read_per_1m": 1,
        },
    },
    {
        "pattern": "gpt-5.5",
        "input_per_1m": 2.5,
        "output_per_1m": 15,
        "source": "custom",
        "long_context": {
            "threshold_input_tokens": 272000,
            "input_per_1m": 5,
            "output_per_1m": 22.5,
        },
    },
    {
        "pattern": "claude-sonnet-4-5",
        "input_per_1m": 3,
        "output_per_1m": 15,
        "cache_read_per_1m": 0.3,
        "cache_write_per_1m": 3.75,
        "source": "builtin",
    },
    {
        "pattern": "deepseek-v4-flash",
        "input_per_1m": 0.28,
        "output_per_1m": 0.42,
        "source": "custom",
    },
]

MODEL_TOTALS = [
    {
        "billing_model": "gpt-5.6-sol",
        "requests": 1832,
        "uncached_input_tokens": 140000,
        "cache_read_tokens": 19857,
        "cache_creation_tokens": 1032,
        "output_tokens": 4852,
        "cost_usd": 1.050121,
    },
    {
        "billing_model": "claude-sonnet-4-5",
        "requests": 924,
        "uncached_input_tokens": 4921000,
        "cache_read_tokens": 2814000,
        "cache_creation_tokens": 64000,
        "output_tokens": 781000,
        "cost_usd": 27.3904,
    },
    {
        "billing_model": "deepseek-v4-flash",
        "requests": 618,
        "uncached_input_tokens": 882000,
        "cache_read_tokens": 0,
        "cache_creation_tokens": 0,
        "output_tokens": 99000,
        "cost_usd": 0.28854,
    },
]


def make_cost(uncached, cache_read, cache_write, output, rates, tiered=False, long_context=False):
    input_price, read_price, write_price, output_price = rates
    parts = {
        "uncached_input_usd": uncached * input_price / 1_000_000,
        "cache_read_usd": cache_read * read_price / 1_000_000,
        "cache_write_usd": cache_write * write_price / 1_000_000,
        "output_usd": output * output_price / 1_000_000,
    }
    return {
        **parts,
        "total_usd": sum(parts.values()),
        "uncached_input_tokens": uncached,
        "cache_read_tokens": cache_read,
        "cache_write_tokens": cache_write,
        "billed_output_tokens": output,
        "tiered": tiered,
        "long_context": long_context,
        "threshold_input_tokens": 272000 if tiered else 0,
        "applied_input_per_1m": input_price,
        "applied_cache_read_per_1m": read_price,
        "applied_cache_write_per_1m": write_price,
        "applied_output_per_1m": output_price,
    }


LOG_CASES = [
    {
        "endpoint": "/v1/responses",
        "source": "codex · haowang4455@gmail.com",
        "upstream_model": "gpt-5.6-sol",
        "billing_model": "team/gpt-5.6-sol",
        "reasoning_tokens": 2100,
        "cost": make_cost(140000, 19857, 1032, 4852, (5, 0.5, 5, 30), True, False),
    },
    {
        "endpoint": "/v1/messages",
        "source": "xai · 00f7ghqi90@haowang.im",
        "upstream_model": "gpt-5.6-sol",
        "billing_model": "gpt-5.6-sol",
        "reasoning_tokens": 8000,
        "cost": make_cost(280000, 19000, 1001, 20000, (10, 1, 10, 45), True, True),
    },
    {
        # A provider configured in config.yaml, named by the masked API key
        # that separates it from the other keys of that provider.
        "endpoint": "/v1/chat/completions",
        "source": "deepseek · sk-ups…0001",
        "upstream_model": "deepseek-v4-flash",
        "billing_model": "deepseek-v4-flash",
        "reasoning_tokens": 0,
        "cost": make_cost(160889, 0, 0, 12001, (0.28, 0.28, 0.28, 0.42)),
    },
    {
        "endpoint": "/v1beta/models/*action",
        "source": "claude · sk-ups…0001",
        "upstream_model": "claude-sonnet-4-5",
        "billing_model": "claude-sonnet-4-5",
        "reasoning_tokens": 0,
        "cost": make_cost(38122, 9931, 2048, 7240, (3, 0.3, 3.75, 15)),
    },
    {
        # An execution with no route, and a credential no usage record has
        # named yet, so both fields fall back to their placeholders.
        "endpoint": "",
        "source": "",
        "upstream_model": "gpt-5.5",
        "billing_model": "gpt-5.5",
        "reasoning_tokens": 400,
        "cost": make_cost(160889, 0, 0, 4096, (2.5, 2.5, 2.5, 15), True, False),
    },
    {
        # A client that disconnected mid-generation. The provider never reported
        # usage, so every token column is unknown rather than zero.
        "endpoint": "/v1/messages",
        "source": "codex · ops@example.com",
        "upstream_model": "gpt-5.5",
        "billing_model": "gpt-5.5",
        "reasoning_tokens": 0,
        "cost": make_cost(0, 0, 0, 0, (0, 0, 0, 0)),
        "outcome": "canceled",
        "accounting_quality": "",
    },
    {
        # An upstream error after the provider had already reported its usage.
        "endpoint": "/v1/responses",
        "source": "xai · ops@example.com",
        "upstream_model": "grok-4",
        "billing_model": "grok-4",
        "reasoning_tokens": 128,
        "cost": make_cost(4096, 0, 0, 256, (3, 0.75, 3, 15)),
        "outcome": "failed",
    },
]


def make_logs(count=120):
    entries = []
    for index in range(count):
        case = LOG_CASES[index % len(LOG_CASES)]
        key = KEYS[index % len(KEYS)]
        entries.append(
            {
                "at": iso(NOW - timedelta(seconds=index * 17)),
                "scope": key["scope"],
                "preview": key["preview"],
                "label": key["label"],
                "request_id": f"req-dummy-{index + 1:04d}",
                "endpoint": case["endpoint"],
                "source": case["source"],
                "upstream_model": case["upstream_model"],
                "billing_model": case["billing_model"],
                "outcome": case.get("outcome", ""),
                "accounting_quality": case.get("accounting_quality", "complete"),
                "price_source": "builtin" if index % 4 else "override",
                "cost": case["cost"],
                "reasoning_tokens": case["reasoning_tokens"],
            }
        )
    return entries


LOGS = make_logs()

# The billing log is served one page at a time, and the counts beside the status
# filter come with it, so the dummy has to answer the same query the plugin does.
LOG_SEARCH_FIELDS = (
    "label", "preview", "scope", "upstream_model", "billing_model", "endpoint", "source", "request_id",
)


def log_view(query):
    search = query.get("q", [""])[0].strip().lower()
    outcome = query.get("outcome", [""])[0]
    offset = max(0, int(query.get("offset", ["0"])[0] or 0))
    limit = max(0, int(query.get("limit", ["0"])[0] or 0))
    counts = {"all": 0, "succeeded": 0, "failed": 0, "canceled": 0}
    matched = []
    for entry in LOGS:
        haystack = " ".join(str(entry.get(field, "")) for field in LOG_SEARCH_FIELDS).lower()
        if search and search not in haystack:
            continue
        status = entry.get("outcome") or "succeeded"
        counts["all"] += 1
        counts[status] = counts.get(status, 0) + 1
        if outcome and status != outcome:
            continue
        matched.append(entry)
    page = matched[offset:offset + limit] if limit else matched[offset:]
    return {"entries": page, "total": len(matched), "offset": offset, "outcome_counts": counts}

EVENTS = [
    {
        "at": iso(NOW - timedelta(minutes=1)),
        "level": "error",
        "message": "保存状态文件失败：替换文件 /srv/cli-proxy-api/plugins/cpa-key-billing-state.json：read-only file system",
    },
    {
        "at": iso(NOW - timedelta(minutes=12)),
        "level": "error",
        "message": "更新 models.dev 参考价目录失败：Get \"https://models.dev/api.json\": dial tcp: i/o timeout",
    },
    {
        "at": iso(NOW - timedelta(hours=2)),
        "level": "info",
        "message": "已同步 CLIProxyAPI 的 API Key 列表：新增 3 个，移除 1 个。",
    },
    {
        "at": iso(NOW - timedelta(days=1)),
        "level": "info",
        "message": (
            "已加载状态文件 /srv/cli-proxy-api/plugins/cpa-key-billing-state.json："
            "16 个 API Key、2 个订阅计划、120 条计费日志。已启用。"
        ),
    },
]


def payload_for(path, query):
    # The panel reads everything but the two logs through this one route.
    if path == f"{API_BASE}/overview":
        return {
            "status": {"enabled": True},
            "keys": KEYS,
            "plans": PLANS,
            "prices": PRICES,
            "stats": {
                "keys": len(LIVE_KEYS),
                "blocked_keys": 0,
                "lifetime": {
                    "requests": sum(item["requests"] for item in MODEL_TOTALS),
                    "cost_usd": sum(item["cost_usd"] for item in MODEL_TOTALS),
                },
                "by_model": MODEL_TOTALS,
            },
        }
    if path == f"{API_BASE}/status":
        return {"enabled": True}
    if path == f"{API_BASE}/logs":
        return log_view(query)
    if path == f"{API_BASE}/events":
        return {"events": EVENTS}
    if path == f"{API_BASE}/prices/catalog":
        term = query.get("q", [""])[0].lower()
        return {"models": [row for row in PRICES if term in row["pattern"].lower()]}
    if path == "/v0/management/api-keys":
        return {"api-keys": [f"sk-demo-{index:04d}" for index in range(1, len(LIVE_KEYS) + 1)]}
    if path == "/v1/models":
        return {"data": [{"id": row["pattern"]} for row in PRICES]}
    return None


class Handler(BaseHTTPRequestHandler):
    def send_json(self, status, payload):
        body = json.dumps(payload, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        parsed = urlparse(self.path)
        if parsed.path in ("/", "/ui"):
            body = UI_PATH.read_text().replace(
                "</head>",
                '<script>sessionStorage.setItem("cpa-key-billing:mgmt-key", "dummy");</script>\n</head>',
                1,
            ).encode()
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Cache-Control", "no-store")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        payload = payload_for(parsed.path, parse_qs(parsed.query))
        if payload is None:
            self.send_json(404, {"error": {"message": "dummy backend: route not found"}})
            return
        self.send_json(200, payload)

    def do_POST(self):
        self.handle_mutation()

    def do_PATCH(self):
        self.handle_mutation()

    def do_PUT(self):
        self.handle_mutation()

    def do_DELETE(self):
        self.handle_mutation()

    def handle_mutation(self):
        parsed = urlparse(self.path)
        length = int(self.headers.get("Content-Length", "0"))
        if length:
            self.rfile.read(length)
        route = self.command, parsed.path
        if route == ("DELETE", f"{API_BASE}/logs"):
            self.send_json(200, {"cleared": len(LOGS)})
        elif route == ("DELETE", f"{API_BASE}/events"):
            self.send_json(200, {"cleared": len(EVENTS)})
        elif route == ("POST", f"{API_BASE}/prices/catalog/refresh"):
            self.send_json(200, {"catalog": {"models": len(PRICES)}, "updated_models": 2})
        elif route == ("POST", f"{API_BASE}/prices/reset"):
            self.send_json(200, {"restored": len(PRICES)})
        elif route == ("POST", f"{API_BASE}/keys/reset-all"):
            self.send_json(200, {"reset": 14})
        elif route in {
            ("POST", f"{API_BASE}/keys/sync"),
            ("POST", f"{API_BASE}/prices/sync"),
        }:
            self.send_json(200, {"added": 0, "removed": 0, "matched": len(LIVE_KEYS), "priced": len(PRICES)})
        elif route in {
            ("POST", f"{API_BASE}/keys/bind"),
            ("POST", f"{API_BASE}/keys/unbind"),
            ("POST", f"{API_BASE}/keys/reset"),
            ("POST", f"{API_BASE}/keys/label"),
            ("POST", f"{API_BASE}/plans"),
            ("PATCH", f"{API_BASE}/plans"),
            ("DELETE", f"{API_BASE}/plans"),
            ("PUT", f"{API_BASE}/prices"),
        }:
            self.send_json(200, {"ok": True})
        else:
            self.send_json(404, {"error": {"message": "dummy backend: route not found"}})

    def log_message(self, message, *args):
        print(f"{self.address_string()} - {message % args}")


def main():
    parser = argparse.ArgumentParser(description="Serve the billing UI with deterministic dummy data.")
    parser.add_argument("--port", type=int, default=8765)
    args = parser.parse_args()
    server = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    print(f"Frontend dummy backend: http://127.0.0.1:{server.server_port}/ui", flush=True)
    print("Data is reset on every restart. Press Ctrl-C to stop.", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
