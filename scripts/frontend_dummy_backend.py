#!/usr/bin/env python3

import argparse
import bisect
import hashlib
import json
import math
import random
import re
import time
from datetime import datetime, timedelta, timezone
from zoneinfo import ZoneInfo
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlparse


ROOT = Path(__file__).resolve().parents[1]
UI_DIR = ROOT / "internal" / "plugin" / "web"
# Matches the placeholders internal/plugin/ui.go replaces with sibling files.
UI_INCLUDE = re.compile(r"/\*([a-z0-9-]+\.(?:css|js))\*/")
API_BASE = "/v0/management/plugins/cpa-key-billing"
RESOURCE_BASE = "/v0/resource/plugins/cpa-key-billing"
NOW = datetime.now(timezone.utc).replace(minute=0, second=0, microsecond=0)
CALLER_SCOPE_SALT = b"cli-proxy-api:caller-scope:v1\0"


HOST_SHELL = r"""<!doctype html>
<html lang="en" data-host="__HOST_MODE__">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>__HOST_LABEL__ · CPA Key Billing 样式预览</title>
<script>
{
  const version="__HOST_MODE__"==="cpamp"?"v2":"v1";
  const scope=version==="v2"?"v2|"+location.host:location.host+"|"+navigator.userAgent;
  const secret=new TextEncoder().encode("cli-proxy-api-webui::secure-storage|"+scope);
  const data=new TextEncoder().encode(JSON.stringify({state:{apiBase:location.origin,managementKey:"dummy",rememberPassword:true},version:0}));
  const encoded=data.map((byte,index)=>byte^secret[index%secret.length]);
  localStorage.setItem("cli-proxy-auth","enc::"+version+"::"+btoa(String.fromCharCode(...encoded)));
}
</script>
<style>
:root{
  --bg-secondary:#faf9f5;--bg-primary:#f0eee8;--bg-tertiary:#e9e6df;--bg-hover:var(--bg-tertiary);
  --text-primary:#2d2a26;--text-secondary:#6d6760;--text-tertiary:#a29c95;
  --border-color:#e3e1db;--border-primary:#d5d2cb;--border-hover:#cecac4;
  --primary-color:#8b8680;--primary-hover:#7f7a74;--primary-active:#726d67;--primary-contrast:#fff;
  --success-badge-bg:#d1fae5;--success-badge-text:#065f46;--success-badge-border:#6ee7b7;
  --failure-badge-bg:#c6574624;--failure-badge-text:#8a3a30;--failure-badge-border:#c6574659;
  color-scheme:light;
}
:root[data-theme=white]{
  --bg-secondary:#fff;--bg-primary:#fff;--bg-tertiary:#f6f6f6;
  --border-color:#e5e5e5;--border-primary:#d9d9d9;--border-hover:#ccc;
}
:root[data-theme=dark]{
  --bg-secondary:#151412;--bg-primary:#1d1b18;--bg-tertiary:#262320;--bg-hover:#2e2a26;
  --text-primary:#f6f4f1;--text-secondary:#c9c3bb;--text-tertiary:#9c958d;
  --border-color:#3a3530;--border-primary:#4a453f;--border-hover:#5a544d;
  --primary-hover:#9a948e;--primary-active:#a6a099;
  --success-badge-bg:#064e3b4d;--success-badge-text:#6ee7b7;--success-badge-border:#059669;
  --failure-badge-bg:#c657463d;--failure-badge-text:#f1b0a6;--failure-badge-border:#c6574680;
  color-scheme:dark;
}
html[data-host=cpamp]{
  --app-bg:#eff2f7;--app-bg-gradient:linear-gradient(120deg,#f0f7ff 0%,#e7f2ff 50%,#edf7ff 100%);
  --app-bg-blob-1-start:#7aa2ff;--app-bg-blob-1-end:#b6d4ff;--app-bg-blob-2-start:#6bc5ff;--app-bg-blob-2-end:#7debdc;
  --app-bg-blob-1-opacity:.5;--app-bg-blob-2-opacity:.5;--app-bg-blob-blur:60px;--glass-backdrop-filter:blur(20px);
  --app-surface:rgba(255,255,255,.94);--app-surface-strong:#fff;--app-surface-muted:rgba(255,255,255,.68);
  --app-border:rgba(15,23,42,.08);--app-border-strong:rgba(15,23,42,.12);
  --app-text-primary:#2c3e50;--app-text-regular:#5f6c7b;--app-text-muted:#8b95a6;
  --app-accent-soft:rgba(59,130,246,.12);--surface-subtle:#f6faff;
  --app-radius-lg:20px;--app-radius-md:12px;--app-radius-sm:8px;
  --glass-bg:#fff;--glass-border:rgba(255,255,255,.6);--glass-shadow:none;
  --app-input-bg:rgba(255,255,255,.62);--app-input-bg-focus:#fff;
  --app-input-border:var(--app-border-strong);--app-input-border-focus:#3b82f6;
  --color-primary:#3b82f6;--color-primary-light-3:#60a5fa;--color-primary-dark-2:#2563eb;
  --color-success:#22c55e;--success-color:var(--color-success);
  --primary-color:var(--color-primary);--primary-hover:var(--color-primary-light-3);
  --primary-active:var(--color-primary-dark-2);--primary-solid:#2563eb;--primary-solid-hover:#3b82f6;
  --primary-ring:rgba(59,130,246,.22);--primary-contrast:#fff;
  --color-warning:#f59e0b;--color-danger:#ef4444;
  --data-blue-base:#3b82f6;--data-green-base:#22c55e;--data-amber-base:#f59e0b;
  --data-red-base:#ef4444;--data-violet-base:#8b5cf6;--data-cyan-base:#06b6d4;
  --data-badge-success-bg:#f0fdf4;--data-badge-success-text:#16a34a;--data-badge-success-border:#bbf7d0;
  --data-badge-warning-bg:#fffbeb;--data-badge-warning-text:#d97706;--data-badge-warning-border:#fde68a;
  --data-badge-danger-bg:#fef2f2;--data-badge-danger-text:#dc2626;--data-badge-danger-border:#fecaca;
  --data-badge-info-bg:#eff6ff;--data-badge-info-text:#2563eb;--data-badge-info-border:#bfdbfe;
  --data-badge-neutral-bg:#f8fafc;--data-badge-neutral-text:#475569;--data-badge-neutral-border:#cbd5e1;
  --bg-secondary:var(--app-bg);--bg-primary:var(--app-surface);--bg-tertiary:var(--app-surface-muted);
  --bg-hover:var(--app-accent-soft);--text-primary:var(--app-text-primary);
  --text-secondary:var(--app-text-regular);--text-tertiary:var(--app-text-muted);
  --border-color:var(--app-border);--border-primary:var(--app-border-strong);
  --border-hover:rgba(59,130,246,.28);
}
html[data-host=cpamp][data-theme=dark]{
  --app-bg:#0a0a0a;--app-bg-gradient:linear-gradient(120deg,#0b1324 0%,#0a1426 50%,#091521 100%);
  --app-bg-blob-1-start:#1b2a55;--app-bg-blob-1-end:#27325c;--app-bg-blob-2-start:#0f3d59;--app-bg-blob-2-end:#0c4a4f;
  --app-bg-blob-1-opacity:.35;--app-bg-blob-2-opacity:.3;--app-bg-blob-blur:70px;
  --app-surface:rgba(24,28,40,.9);--app-surface-strong:#1b1f2a;--app-surface-muted:rgba(255,255,255,.08);
  --app-border:rgba(255,255,255,.08);--app-border-strong:rgba(255,255,255,.12);
  --app-text-primary:#e5e5e5;--app-text-regular:#a3a3a3;--app-text-muted:#7a7a7a;
  --app-accent-soft:rgba(96,165,250,.18);--surface-subtle:rgba(255,255,255,.06);
  --glass-bg:rgba(24,28,40,.72);--glass-border:rgba(255,255,255,.1);
  --app-input-bg:#1b1f2a;--app-input-bg-focus:#1b1f2a;--app-input-border-focus:#60a5fa;
  --color-primary:#60a5fa;--color-primary-light-3:#93c5fd;--color-primary-dark-2:#3b82f6;
  --color-success:#4ade80;--success-color:var(--color-success);
  --primary-solid:#60a5fa;--primary-solid-hover:#3b82f6;--primary-ring:rgba(96,165,250,.22);
  --data-blue-base:#60a5fa;--data-green-base:#4ade80;--data-amber-base:#fbbf24;
  --data-red-base:#f87171;--data-violet-base:#a78bfa;--data-cyan-base:#22d3ee;
  --data-badge-success-bg:rgba(74,222,128,.14);--data-badge-success-text:#4ade80;--data-badge-success-border:rgba(74,222,128,.24);
  --data-badge-warning-bg:rgba(251,191,36,.14);--data-badge-warning-text:#fbbf24;--data-badge-warning-border:rgba(251,191,36,.24);
  --data-badge-danger-bg:rgba(248,113,113,.14);--data-badge-danger-text:#f87171;--data-badge-danger-border:rgba(248,113,113,.24);
  --data-badge-info-bg:rgba(96,165,250,.14);--data-badge-info-text:#60a5fa;--data-badge-info-border:rgba(96,165,250,.24);
  --data-badge-neutral-bg:rgba(148,163,184,.12);--data-badge-neutral-text:#94a3b8;--data-badge-neutral-border:rgba(148,163,184,.2);
}
*{box-sizing:border-box}
html,body{width:100%;height:100%;margin:0;overflow:hidden}
body{font:14px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:var(--bg-secondary);color:var(--text-primary)}
html[data-host=cpamp] body{background-color:var(--app-bg);background-image:var(--app-bg-gradient)}
.sidebar{position:fixed;inset:0 auto 0 0;z-index:20;width:var(--sidebar-width);padding:14px 10px;
  background:var(--bg-primary);border-right:1px solid var(--border-color)}
.brand{display:flex;align-items:center;gap:10px;height:48px;padding:0 10px;font-size:17px;font-weight:750}
.brand-mark{display:grid;place-items:center;width:34px;height:34px;border-radius:9px;background:var(--primary-color);color:#fff}
.nav-placeholder{display:grid;gap:8px;margin-top:30px}
.nav-placeholder span{height:38px;padding:9px 12px;border-radius:9px;color:var(--text-secondary)}
.nav-placeholder span.active{background:var(--bg-tertiary);color:var(--text-primary);font-weight:650}
.navbar{position:fixed;z-index:30;display:flex;align-items:center;justify-content:space-between;height:var(--header-height);
  color:var(--text-secondary)}
.navbar-left{display:flex;align-items:center;gap:10px;min-width:0}
.mobile-menu,.theme-controls button{appearance:none;display:grid;place-items:center;width:36px;height:36px;padding:0;
  border:1px solid transparent;border-radius:10px;background:transparent;color:inherit;font:inherit;cursor:pointer}
.theme-controls{display:flex;gap:3px;padding:5px;border:1px solid var(--border-color);border-radius:14px;background:var(--bg-primary)}
.theme-controls button:hover,.theme-controls button.active{background:var(--bg-tertiary);color:var(--text-primary)}
.content{position:fixed;overflow:hidden}
#plugin-frame{display:block;width:100%;height:100%;border:0;background:var(--bg-secondary)}
html[data-host=cpamc]{--sidebar-width:216px;--header-height:80px}
html[data-host=cpamc] .navbar{inset:0 0 auto var(--sidebar-width);pointer-events:none}
html[data-host=cpamc] .navbar-left{display:none}
html[data-host=cpamc] .navbar-left>span{display:none}
html[data-host=cpamc] .theme-controls{position:absolute;top:24px;right:24px;pointer-events:auto;box-shadow:0 18px 44px #0000002b}
html[data-host=cpamc] .mobile-menu{display:none}
html[data-host=cpamc] .content{inset:0 0 0 var(--sidebar-width)}
html[data-host=cpamp]{--sidebar-width:210px;--header-height:50px}
html[data-host=cpamp] .sidebar{background:color-mix(in srgb,var(--app-surface) 70%,transparent)}
html[data-host=cpamp] .navbar{inset:0 0 auto var(--sidebar-width);padding:0 20px 0 8px;background:var(--app-surface);border-bottom:1px solid var(--app-border)}
html[data-host=cpamp] .theme-controls{padding:2px;border:0;background:transparent}
html[data-host=cpamp] .theme-white{display:none}
html[data-host=cpamp] .content{inset:var(--header-height) 0 0 var(--sidebar-width)}
html[data-host=cpamp] #plugin-frame{background:var(--bg-primary)}
@media(max-width:768px){
  .sidebar{display:none}
  html[data-host] .navbar{left:0}
  html[data-host] .content{left:0}
  html[data-host=cpamc] .navbar-left{display:block;position:absolute;top:12px;left:12px;pointer-events:auto}
  html[data-host=cpamc] .mobile-menu{display:grid;background:var(--bg-primary);border-color:var(--border-color);box-shadow:0 18px 44px #0000002b}
  html[data-host=cpamc] .theme-controls{top:12px;right:12px}
  html[data-host=cpamp] .navbar{padding-right:8px}
}
</style>
</head>
<body>
<aside class="sidebar">
  <div class="brand"><span class="brand-mark">◈</span><span>__HOST_LABEL__</span></div>
  <div class="nav-placeholder"><span>仪表盘</span><span>AI 提供商</span><span>插件管理</span><span class="active">CPA Key Billing</span></div>
</aside>
<header class="navbar">
  <div class="navbar-left"><button class="mobile-menu" title="菜单">☰</button><span>CPA Key Billing</span></div>
  <div class="theme-controls" aria-label="预览主题">
    <select id="host-language" aria-label="Language"><option value="en">English</option><option value="zh-CN">简体中文</option><option value="zh-TW">繁體中文</option><option value="ru">Русский</option></select>
    <button type="button" data-action="refresh" title="刷新">↻</button>
    <button type="button" data-theme-choice="light" title="浅色主题">◐</button>
    <button type="button" class="theme-white" data-theme-choice="white" title="白色主题">○</button>
    <button type="button" data-theme-choice="dark" title="深色主题">●</button>
  </div>
</header>
<main class="content"><iframe id="plugin-frame" src="/ui" title="CPA Key Billing 插件"></iframe></main>
<script>
"use strict";
const HOST_MODE="__HOST_MODE__";
const INITIAL_THEME="__INITIAL_THEME__";
const root=document.documentElement;
const frame=document.getElementById("plugin-frame");
const language=document.getElementById("host-language");
language.value=root.lang;
language.onchange=()=>{root.lang=language.value;};
const systemDark=()=>!!matchMedia("(prefers-color-scheme:dark)").matches;
let selectedTheme=INITIAL_THEME;

function resolveTheme(choice){
  if(choice==="auto")return systemDark()?"dark":"white";
  if(HOST_MODE==="cpamp"&&choice==="light")return "white";
  return choice;
}

function cpampBridgeCSS(theme){
  const computed=getComputedStyle(root);
  const declarations=[];
  for(let index=0;index<computed.length;index++){
    const name=computed.item(index);
    if(!name.startsWith("--"))continue;
    const value=computed.getPropertyValue(name).trim();
    if(value)declarations.push("  "+name+":"+value+";");
  }
  const scope=":where(html[data-cpamp-plugin-host='true'])";
  return scope+"{\n"+declarations.join("\n")+"\ncolor-scheme:"+(theme==="dark"?"dark":"light")+";min-height:100%;background:var(--bg-primary);color:var(--text-primary)}\n"+
    scope+" :where(body){min-height:100vh;margin:0;background:var(--bg-primary);color:var(--text-primary);font-family:Inter,-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;font-size:14px;line-height:1.5}\n"+
    scope+" :where(body,button,input,select,textarea){font-family:Inter,-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif}\n"+
    scope+" :where(input:not([type=checkbox]):not([type=radio]),select,textarea){min-height:34px;border:1px solid var(--app-input-border);border-radius:var(--app-radius-sm);background:var(--app-input-bg);color:var(--text-primary);box-shadow:none}\n"+
    scope+" :where(button,[role=button]){min-height:34px;border:1px solid var(--border-color);border-radius:var(--app-radius-md);background:var(--app-surface-muted);color:var(--text-primary);font-weight:600;line-height:1.2}\n"+
    scope+" :where(thead,th){background:color-mix(in srgb,var(--bg-tertiary) 72%,var(--bg-primary));color:var(--text-secondary)}\n"+
    scope+" :where(.card,[class*=card],[class*=panel]){border-color:var(--border-color);background:var(--bg-primary);color:var(--text-primary)}";
}

function syncCPAMPFrame(theme){
  if(HOST_MODE!=="cpamp")return;
  let doc;
  try{doc=frame.contentDocument}catch(_){return}
  if(!doc||!doc.documentElement||!doc.head)return;
  const childRoot=doc.documentElement;
  childRoot.setAttribute("data-cpamp-plugin-host","true");
  childRoot.setAttribute("data-theme",theme==="dark"?"dark":"white");
  childRoot.classList.toggle("theme-dark",theme==="dark");
  childRoot.classList.toggle("theme-light",theme!=="dark");
  let style=doc.getElementById("cpamp-dummy-host-style");
  if(!style){
    style=doc.createElement("style");
    style.id="cpamp-dummy-host-style";
    const pluginStyle=doc.head.querySelector("style,link[rel~=stylesheet]");
    doc.head.insertBefore(style,pluginStyle);
  }
  style.textContent=cpampBridgeCSS(theme);
}

function applyTheme(choice){
  selectedTheme=choice;
  const theme=resolveTheme(choice);
  const activeChoice=choice==="auto"?(HOST_MODE==="cpamp"&&theme==="white"?"light":theme):choice;
  if(theme==="light")root.removeAttribute("data-theme");else root.setAttribute("data-theme",theme);
  document.querySelectorAll("[data-theme-choice]").forEach(button=>{
    button.classList.toggle("active",button.dataset.themeChoice===activeChoice);
  });
  syncCPAMPFrame(theme);
}

document.querySelectorAll("[data-theme-choice]").forEach(button=>{
  button.addEventListener("click",()=>applyTheme(button.dataset.themeChoice));
});
document.querySelector("[data-action=refresh]").addEventListener("click",()=>frame.contentWindow.location.reload());
frame.addEventListener("load",()=>applyTheme(selectedTheme));
matchMedia("(prefers-color-scheme:dark)").addEventListener("change",()=>{
  if(selectedTheme==="auto")applyTheme("auto");
});
applyTheme(INITIAL_THEME);
</script>
</body>
</html>
"""


def iso(value):
    return value.isoformat().replace("+00:00", "Z")


CNY_PER_USD = 7


def usd_amount(amount, currency):
    return amount / CNY_PER_USD if currency == "CNY" else amount


def charged_in(rows):
    """The one currency the charged requests share, or "" when they mix currencies or cost nothing."""
    currencies = {row["cost"].get("currency") for row in rows if row.get("cost", {}).get("total")}
    return currencies.pop() if len(currencies) == 1 else ""


PLANS = [
    {"id": "engineering", "name": "研发团队", "currency": "USD", "windows": [
        {"id": "short", "name": "短时额度", "amount": 15, "request_limit": 100, "token_limit": 1000000, "period_seconds": 18000},
        {"id": "budget", "name": "团队预算", "amount": 300, "period_seconds": 2592000},
    ]},
    {"id": "production", "name": "生产服务", "currency": "USD", "windows": [
        {"id": "short", "name": "峰值保护", "amount": 0, "request_limit": 200, "token_limit": 0,
         "period_seconds": 7200, "cycle_anchor_at": iso(NOW + timedelta(hours=2))},
        {"id": "medium", "name": "服务额度", "amount": 0, "request_limit": 0, "token_limit": 2000000,
         "period_seconds": 86400, "cycle_anchor_at": iso((NOW + timedelta(days=1)).replace(hour=0))},
        {"id": "budget", "name": "生产预算", "amount": 1000,
         "period_seconds": 2592000, "cycle_anchor_at": iso((NOW + timedelta(days=15)).replace(hour=0))},
    ]},
    {"id": "project-credit", "name": "项目额度", "currency": "CNY", "windows": [
        {"id": "budget", "name": "项目预算", "amount": 700, "period_seconds": 864000},
    ]},
]


QUOTA_CYCLES = {}


def refresh_key_quota(key):
    plan = next((item for item in PLANS if item["id"] == key["plan_id"]), None)
    previous = QUOTA_CYCLES.get(key["scope"], {})
    cycles = {}
    now = datetime.now(timezone.utc)
    key.update(plan_name=plan["name"] if plan else "", unlimited=plan is None, blocked=False, windows=[])
    key.pop("retry_at", None)
    for window in plan["windows"] if plan else []:
        cycle = previous.get(window["id"], {})
        started = (cycle.get("plan_id") == key["plan_id"]
                   and cycle.get("period_seconds") == window["period_seconds"]
                   and cycle.get("cycle_anchor_at") == window.get("cycle_anchor_at")
                   and (key.get("deleted_at") or datetime.fromisoformat(cycle["end_at"].replace("Z", "+00:00")) > now))
        if started:
            cycles[window["id"]] = cycle
        else:
            cycle = {}
        if not started and window.get("cycle_anchor_at"):
            anchor = datetime.fromisoformat(window["cycle_anchor_at"].replace("Z", "+00:00"))
            period = timedelta(seconds=window["period_seconds"])
            start = anchor + ((now - anchor) // period) * period
            cycle = dict(start_at=iso(start), end_at=iso(start+period))
            started = True
        dimensions = [dict(metric=metric, limit=limit, used=used, remaining=max(0, limit-used),
                           used_percent=min(100, used / limit * 100), blocked=used >= limit,
                           **({"currency": plan["currency"]} if metric == "amount" else {}))
                      for metric, limit, used in [
                          ("amount", window.get("amount", 0), cycle.get("spent", 0)),
                          ("tokens", window.get("token_limit", 0), cycle.get("used_tokens", 0)),
                          ("requests", window.get("request_limit", 0), cycle.get("used_requests", 0))] if limit > 0]
        view = dict(id=window["id"], name=window["name"], period_seconds=window["period_seconds"],
                    started=started, blocked=any(dimension["blocked"] for dimension in dimensions),
                    dimensions=dimensions)
        if window.get("cycle_anchor_at"):
            view["cycle_anchor_at"] = window["cycle_anchor_at"]
        if started:
            view.update(start_at=cycle["start_at"], end_at=cycle["end_at"])
        if view["blocked"]:
            key["blocked"] = True
            key["retry_at"] = max(key.get("retry_at", ""), view["end_at"])
        key["windows"].append(view)
    QUOTA_CYCLES[key["scope"]] = cycles


AUTOMATION_CREDENTIAL_REF = "sha256:" + "f" * 64
UNAVAILABLE_CREDENTIAL_REF = "sha256:" + "1" * 64

CREDENTIALS = [
    {"ref": "sha256:" + "a" * 64, "source": "auth-files", "provider": "codex", "provider_name": "codex", "display_name": "dev-team@example.com", "status": "active", "disabled": False, "unavailable": False},
    {"ref": "sha256:" + "b" * 64, "source": "auth-files", "provider": "claude", "provider_name": "claude", "display_name": "platform@example.com", "status": "active", "disabled": False, "unavailable": False},
    {"ref": "sha256:" + "c" * 64, "source": "ai-providers", "provider": "codex", "provider_name": "Codex Relay", "display_name": "sk-proxy…7f3a", "status": "active", "disabled": False, "unavailable": False},
    {"ref": "sha256:" + "d" * 64, "source": "ai-providers", "provider": "deepseek", "provider_name": "deepseek", "display_name": "sk-live…91b2", "status": "disabled", "disabled": True, "unavailable": False},
    {"ref": "sha256:" + "e" * 64, "source": "auth-files", "provider": "xai", "provider_name": "xai", "display_name": "disabled@example.com", "status": "disabled", "disabled": True, "unavailable": False},
    {"ref": AUTOMATION_CREDENTIAL_REF, "source": "auth-files", "provider": "codex", "provider_name": "codex", "display_name": "automation@example.com", "status": "active", "disabled": False, "unavailable": False},
    {"ref": UNAVAILABLE_CREDENTIAL_REF, "source": "auth-files", "provider": "kimi", "provider_name": "kimi", "display_name": "kimi-research", "status": "error", "disabled": False, "unavailable": True},
]
SYNCED_CREDENTIAL_REFS = set()
CONFIG_CREDENTIALS = {}

ROUTES = [
    {"id": "coding", "name": "代码开发", "rule": {"models": ["gpt-5.6-sol", "gpt-5.5", "codex/deepseek-v4-flash-vision-exp"], "credential_ids": [], "credential_providers": [{"source": "auth-files", "provider": "codex"}]}},
    {"id": "analytics", "name": "数据分析", "rule": {"models": ["claude/deepseek-v4-pro", "claude/deepseek-v4-flash"], "credential_ids": ["sha256:" + "b" * 64], "credential_providers": []}},
    {"id": "economy", "name": "轻量任务", "rule": {"models": ["gpt-5.6-luna"], "credential_ids": [], "credential_providers": [{"source": "auth-files", "provider": "codex"}]}},
    {
        "id": "ci",
        "name": "持续集成",
        "rule": {
            "models": ["gpt-5.6-luna", "gpt-5.6-terra"],
            "credential_ids": [],
            "credential_providers": [{"source": "auth-files", "provider": "codex"}],
            "denied_models": ["gpt-5.6-sol"],
            "denied_credential_ids": ["sha256:" + "a" * 64],
            "denied_credential_providers": [{"source": "ai-providers", "provider": "deepseek"}],
        },
    },
    {
        "id": "text-only",
        "name": "文本服务",
        "rule": {
            "models": [], "credential_ids": [], "credential_providers": [],
            "denied_models": ["gpt-image-2"],
            "denied_credential_ids": ["sha256:" + "d" * 64, "sha256:" + "e" * 64],
            "denied_credential_providers": [],
        },
    },
]

AUTH_FILES = [
    {
        "auth_index": "auth-demo-codex-plus",
        "name": "codex-dev-team@example.com.json",
        "category": "codex",
        "email": "dev-team@example.com",
        "disabled": False,
        "unavailable": False,
        "priority": 10,
        "weight": 10,
        "id_token": {"chatgpt_subscription_active_until": iso(NOW + timedelta(days=12, hours=5))},
        "quota_supported": True,
    },
    {
        "auth_index": "auth-demo-claude",
        "name": "claude-platform@example.com.json",
        "category": "claude",
        "email": "platform@example.com",
        "disabled": False,
        "unavailable": False,
        "priority": 0,
        "quota_supported": True,
    },
    {
        "auth_index": "auth-demo-codex-pro",
        "name": "codex-automation@example.com.json",
        "category": "codex",
        "email": "automation@example.com",
        "disabled": False,
        "unavailable": False,
        "priority": 99,
        "weight": 1000000,
        "id_token": {"chatgpt_subscription_active_until": iso(NOW + timedelta(hours=20))},
        "quota_supported": True,
    },
    {
        "auth_index": "auth-demo-antigravity",
        "name": "antigravity-ai-lab@example.com.json",
        "category": "antigravity",
        "email": "ai-lab@example.com",
        "disabled": False,
        "unavailable": False,
        "priority": 5,
        "weight": 5,
        "quota_supported": True,
    },
    {
        "auth_index": "auth-demo-kimi",
        "name": "kimi-research.json",
        "category": "kimi",
        "disabled": False,
        "unavailable": True,
        "priority": 0,
        "weight": 0,
        "quota_supported": True,
    },
    {
        "auth_index": "auth-demo-xai-active",
        "name": "xai-research@example.com.json",
        "category": "xai",
        "email": "research@example.com",
        "disabled": False,
        "unavailable": False,
        "priority": 1,
        "quota_supported": True,
    },
]

# CPA's per-type lists of configured upstream API keys, with the auth index CPA reports for each
# loaded credential. Keys are synthetic; a disabled compatible provider is not loaded, so it has none.
AI_PROVIDER_LISTS = {
    "claude-api-key": [
        {"api-key": "sk-ant-dummy-claude-0000000001", "auth-index": "ai-demo-claude"},
        {"api-key": "sk-dummy-deepseek-anthropic-0001", "base-url": "https://api.deepseek.com/anthropic",
         "priority": 5, "weight": 3, "auth-index": "ai-demo-claude-deepseek"},
    ],
    "codex-api-key": [
        {"api-key": "sk-proxy-dummy-codex-relay-7f3a", "base-url": "https://relay.example.com/v1", "prefix": "codex",
         "priority": 10, "weight": 10, "auth-index": "ai-demo-codex-relay"},
    ],
    "gemini-api-key": [
        {"api-key": "AIzaDummyGeminiKey0000000000001", "excluded-models": ["*"], "auth-index": "ai-demo-gemini"},
    ],
    "interactions-api-key": [],
    "vertex-api-key": [
        {"api-key": "vertex-dummy-key-0000000000001", "base-url": "https://aiplatform.googleapis.com", "auth-index": "ai-demo-vertex"},
    ],
    "xai-api-key": [
        {"api-key": "xai-dummy-key-00000000000000001", "base-url": "https://api.x.ai/v1", "auth-index": "ai-demo-xai"},
    ],
    "meta-api-key": [
        {"api-key": "meta-dummy-key-0000000000000001", "base-url": "https://api.llama.com/compat/v1", "auth-index": "ai-demo-meta"},
    ],
    "openai-compatibility": [
        {"name": "openrouter", "base-url": "https://openrouter.ai/api/v1", "priority": 3,
         "api-key-entries": [{"api-key": "sk-or-v1-dummy-000000000000000001", "weight": 2, "auth-index": "ai-demo-openrouter"}]},
        {"name": "deepseek", "base-url": "https://api.deepseek.com/v1", "api-key-entries": [
            {"api-key": "sk-dummy-deepseek-00000000000001", "auth-index": "ai-demo-deepseek-1"},
            {"api-key": "sk-dummy-deepseek-00000000000002", "weight": 0, "auth-index": "ai-demo-deepseek-2"},
        ]},
        {"name": "kimi", "base-url": "https://api.moonshot.cn/v1",
         "api-key-entries": [{"api-key": "sk-dummy-kimi-00000000000000001", "auth-index": "ai-demo-kimi"}]},
        {"name": "siliconflow", "base-url": "https://api.siliconflow.cn/v1",
         "api-key-entries": [{"api-key": "sk-dummy-siliconflow-expired-01", "auth-index": "ai-demo-siliconflow"}]},
        {"name": "stepfun", "base-url": "https://api.stepfun.ai/v1",
         "api-key-entries": [{"api-key": "dummy-stepfun-key-000000000001", "auth-index": "ai-demo-stepfun"}]},
        {"name": "local", "base-url": "http://127.0.0.1:11434/v1", "auth-index": "ai-demo-local"},
        {"name": "legacy", "base-url": "https://legacy.example.com/v1", "disabled": True,
         "api-key-entries": [{"api-key": "sk-dummy-legacy-000000000000001"}]},
    ],
}

# CPA v8 names every group of keys; each entry above forms its own group, as in a migrated legacy configuration.
# The page reads OpenAI-compatible provider names from the v0 lists.
AI_PROVIDER_GROUP_NAMES = {
    "ai-demo-claude": "Anthropic", "ai-demo-claude-deepseek": "DeepSeek", "ai-demo-codex-relay": "Codex Relay",
    "ai-demo-gemini": "gemini-1", "ai-demo-vertex": "Vertex AI", "ai-demo-xai": "xAI", "ai-demo-meta": "Llama API",
}

# Balance responses of the upstreams the page queries, keyed by the request URL.
AI_BALANCE_RESPONSES = {
    "https://api.deepseek.com/user/balance": (200, {"is_available": True, "balance_infos": [
        {"currency": "CNY", "total_balance": "37.07", "granted_balance": "2.29", "topped_up_balance": "34.78"}]}),
    "https://openrouter.ai/api/v1/credits": (200, {"data": {"total_credits": 50, "total_usage": 38.4127}}),
    "https://openrouter.ai/api/v1/key": (200, {"data": {"label": "sk-or-v1-dum...001", "limit": 20, "limit_remaining": 6.25, "usage": 13.75}}),
    "https://api.moonshot.cn/v1/users/me/balance": (200, {"code": 0, "data": {
        "available_balance": 49.58894, "voucher_balance": 46.58893, "cash_balance": 3.00001}, "scode": "0x0", "status": True}),
    "https://api.siliconflow.cn/v1/user/info": (401, {"code": 30014, "data": None, "message": "Token is invalid."}),
    "https://api.stepfun.ai/v1/accounts": (200, {"object": "account", "type": "prepaid", "balance": 0, "total_cash_balance": 10, "total_voucher_balance": 5}),
}


def ai_provider_credentials():
    for field, entries in AI_PROVIDER_LISTS.items():
        for entry in entries:
            for credential in entry.get("api-key-entries") or [entry]:
                if credential.get("auth-index"):
                    yield credential


def ai_provider_groups():
    return {field.removesuffix("-api-key"): [
        {"name": AI_PROVIDER_GROUP_NAMES[entry["auth-index"]], "keys": [{"api-key": entry["api-key"], "auth_index": entry["auth-index"]}]}
        for entry in entries] for field, entries in AI_PROVIDER_LISTS.items() if field != "openai-compatibility"}


def without_auth_indexes(value):
    if isinstance(value, list):
        return [without_auth_indexes(item) for item in value]
    if isinstance(value, dict):
        return {key: without_auth_indexes(item) for key, item in value.items() if key != "auth-index"}
    return value


AUTH_FILE_CREDENTIAL_REFS = {
    "auth-demo-codex-plus": "sha256:" + "a" * 64,
    "auth-demo-claude": "sha256:" + "b" * 64,
    "auth-demo-codex-pro": AUTOMATION_CREDENTIAL_REF,
    "auth-demo-kimi": UNAVAILABLE_CREDENTIAL_REF,
    "auth-demo-xai-active": "sha256:" + "e" * 64,
}

for auth_file in AUTH_FILES:
    auth_file["cache_revision"] = iso(NOW - timedelta(minutes=5))

AUTH_CATEGORY_ORDER = {"claude": 0, "antigravity": 1, "codex": 2, "xai": 3, "kimi": 4}
AUTH_FILES.sort(
    key=lambda item: (
        AUTH_CATEGORY_ORDER.get(item["category"], 5),
        item["category"].lower(),
        item["name"].lower(),
        item["auth_index"],
    )
)


def auth_file_views(files, query, scope=""):
    entries = filter_event_time([entry for entry in REQUEST_EVENTS if not scope or entry["scope"] == scope], query)
    views = []
    for index, item in enumerate(files):
        rows = [entry for entry in entries if entry["auth_index"] == item["auth_index"]]
        view = {field: value for field, value in item.items() if field not in ("id_token", "email")}
        view["weight"] = item.get("weight", 1)
        view["title"] = item.get("email") or item["name"].removesuffix(".json")
        view["usage"] = credential_usage(rows)
        view["in_flight"] = 0 if item["disabled"] else (1 if scope else 3)
        if item["category"] in ("codex", "claude"):
            quota = auth_file_quota({"auth_index": [item["auth_index"]]})
            observed_at = iso(NOW - timedelta(minutes=6 + index))
            view["quota"] = {
                "fetched_at": observed_at,
                "plan": quota.get("plan", ""), "plan_observed_at": observed_at,
                "quota": [{**row, "observed_at": observed_at} for row in quota["quota"] if "reset_at" in row],
            }
            if item["category"] == "codex":
                view["quota"].update(
                    credit_balance=quota.get("credit_balance", ""), credit_balance_observed_at=observed_at,
                    credits_unlimited=quota.get("credits_unlimited", False), credits_unlimited_observed_at=observed_at,
                )
        if item["disabled"]:
            view.update(quota_supported=False, quota_unavailable_reason="Auth file is disabled",
                        quota_unavailable_message={"message_key": "backend.auth_file_is_disabled"})
        views.append(view)
    return views


def model_usage(rows):
    models, grouped = {}, {}
    for row in rows:
        cost = row["cost"]
        grouped.setdefault(row["billing_model"] or row["upstream_model"], []).append(row)
        usage = models.setdefault(row["billing_model"] or row["upstream_model"], dict.fromkeys(
            ("requests", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "cost_usd"), 0))
        usage["requests"] += 1
        usage["input_tokens"] += sum(cost.get(field, 0) for field in ("uncached_input_tokens", "cache_read_tokens", "cache_write_tokens"))
        usage["output_tokens"] += cost.get("billed_output_tokens", 0)
        usage["cache_read_tokens"] += cost.get("cache_read_tokens", 0)
        usage["cache_write_tokens"] += cost.get("cache_write_tokens", 0)
        usage["cost_usd"] += usd_amount(cost.get("total", 0), cost.get("currency"))
    return sorted(({"model": model, **usage, "charged_in": charged_in(grouped[model])} for model, usage in models.items()),
                  key=lambda usage: (-usage["input_tokens"] - usage["output_tokens"], usage["model"]))


def credential_usage(rows):
    models = model_usage(rows)
    return {
        "requests": len(rows), "failed": sum(bool(row.get("failed")) for row in rows),
        **{field: sum(usage[field] for usage in models) for field in ("input_tokens", "cache_read_tokens", "output_tokens", "cost_usd")},
        "charged_in": charged_in(rows),
    }


CODEX_MODELS = ("gpt-5.6-sol", "gpt-5.6-luna", "codex-auto-review")
CLAUDE_MODELS = ("claude-sonnet-5-5", "claude-opus-5-5")
SPARK_MODELS = ("gpt-5.3-codex-spark",)


def model_split(tokens, cost_usd, models):
    """Splits a usage total across models, largest first; Claude models also write cache."""
    weights = (7, 3, 1)[:len(models)]
    split = []
    for model, weight in zip(models, weights):
        share = weight / sum(weights)
        total = round(tokens * share)
        input_tokens = round(total * 0.92)
        split.append({
            "model": model, "requests": max(1, total // 40_000), "input_tokens": input_tokens, "output_tokens": total - input_tokens,
            "cache_read_tokens": round(input_tokens * 0.81),
            "cache_write_tokens": round(input_tokens * 0.04) if model.startswith("claude") else 0,
            "cost_usd": round(cost_usd * share, 4), "charged_in": "USD",
        })
    return [usage for usage in split if usage["input_tokens"] + usage["output_tokens"] > 0]


def quota_usage(tokens, cost_usd, models=CODEX_MODELS):
    """A window's usage lists totals; _models splits them when a breakdown is asked for."""
    return {"usage": {"tokens": tokens, "cost_usd": cost_usd, "charged_in": "USD"}, "_models": models}


QUOTA_WINDOW_LENGTHS = {"5 小时限额": timedelta(hours=5), "周限额": timedelta(days=7), "月度额度": timedelta(days=30)}


def quota_row(label, remaining_percent, reset_seconds, **extra):
    reset_at = NOW + timedelta(seconds=reset_seconds)
    row = {"label": label, "remaining_percent": remaining_percent, "reset_at": iso(reset_at), **extra}
    if "usage" in row:
        row["usage_query"] = {"from": iso(reset_at - QUOTA_WINDOW_LENGTHS[label]), "to": iso(NOW)}
    return row


def public_fields(row, auth_file):
    """Drops private fields; a usage query also names the auth file it selects."""
    row = {key: value for key, value in row.items() if not key.startswith("_")}
    if "usage_query" in row:
        row["usage_query"] = {**row["usage_query"], "auth_index": auth_file["auth_index"], "provider": auth_file["category"]}
    return row


AUTH_FILE_QUOTAS = {
    "auth-demo-codex-pro": {
        "plan": "Pro 200",
        "credit_balance": "62500.0000000000",
        "rate_limit_reset_credits_available_count": 2,
        "rate_limit_reset_credits": [
            {"expires_at": iso(NOW + timedelta(days=13, hours=14))},
            {"expires_at": iso(NOW + timedelta(days=14, hours=10))},
        ],
        "quota": [
            quota_row("周限额", 62, 432000, limit_key="codex", **quota_usage(48_612_340, 36.2)),
            quota_row(
                "5 小时限额",
                100,
                18000,
                label_prefix="GPT-5.3-Codex-Spark ",
                limit_key="GPT-5.3-Codex-Spark",
                **quota_usage(0, 0),
            ),
            quota_row(
                "周限额",
                100,
                604800,
                label_prefix="GPT-5.3-Codex-Spark ",
                limit_key="GPT-5.3-Codex-Spark",
                **quota_usage(0, 0),
            ),
        ],
    },
    "auth-demo-codex-plus": {
        "plan": "Plus",
        "credit_balance": "0",
        "rate_limit_reset_credits_available_count": 1,
        "rate_limit_reset_credits": [{"expires_at": iso(NOW + timedelta(days=7))}],
        "quota": [
            quota_row("5 小时限额", 35, 14400, limit_key="codex", **quota_usage(12_408_115, 9.86)),
            quota_row("周限额", 90, 518400, limit_key="codex", **quota_usage(18_930_552, 15.31)),
        ],
    },
    "auth-demo-claude": {
        "plan": "Team",
        "rate_limit_reset_credits_available_count": 1,
        "quota": [
            quota_row("5 小时限额", 76, 12600, limit_key="unified", **quota_usage(3_104_822, 8.42, CLAUDE_MODELS)),
            quota_row("周限额", 59, 388800, limit_key="unified", **quota_usage(21_733_906, 57.8, CLAUDE_MODELS)),
            {
                "label": "额外用量",
                "used": 12.5,
                "limit": 100,
                "remaining_percent": 87.5,
                "currency": "USD",
            },
        ],
    },
    "auth-demo-antigravity": {
        "plan": "Google AI Pro",
        "quota": [
            quota_row("5 小时限额", 99, 10800, group_label="Claude and GPT Models"),
            quota_row("周限额", 99, 518400, group_label="Claude and GPT Models"),
            quota_row("5 小时限额", 82, 14400, group_label="Gemini Models"),
            quota_row("周限额", 93, 432000, group_label="Gemini Models"),
        ],
    },
    "auth-demo-kimi": {
        "quota": [
            quota_row("5 小时限额", 48, 7200, **quota_usage(2_405_118, 1.27, ("kimi-k3",))),
            quota_row("周限额", 69, 345600, **quota_usage(4_822_391, 2.61, ("kimi-k3",))),
        ],
    },
    "auth-demo-xai-active": {
        "credit_balance": "12.34",
        "credit_currency": "USD",
        "quota": [
            quota_row("周限额", 78, 410400, **quota_usage(5_310_027, 6.94, ("grok-4.7", "grok-build-0.1"))),
            quota_row("月度额度", 83, 1814400, used=8.5, limit=50, currency="USD",
                      **quota_usage(7_102_448, 9.12, ("grok-4.7", "grok-build-0.1"))),
        ],
    },
}


def auth_file_quota(query):
    auth_index = query.get("auth_index", [""])[0]
    quota = AUTH_FILE_QUOTAS.get(auth_index)
    if quota is None:
        return None
    auth_file = next(item for item in AUTH_FILES if item["auth_index"] == auth_index)
    result = {
        "auth_revision": auth_file["cache_revision"],
        "fetched_at": iso(NOW),
        **quota,
    }
    result["quota"] = [public_fields(row, auth_file) for row in localized_quota_labels(result["quota"])]
    return result


def localized_quota_labels(rows):
    english = json.loads((UI_DIR / "locales/en.json").read_text())
    chinese = json.loads((UI_DIR / "locales/zh-CN.json").read_text())
    labels = {value: key for key, value in chinese.items() if key.startswith("backend.") and "{" not in value}
    rows = [dict(row) for row in rows]
    for row in rows:
        key = labels.get(row.get("label", ""))
        if key:
            row["label"] = row.get("label_prefix", "") + english[key]
            row["label_message"] = {"message_key": key}
    return rows


def quota_period(start, length, used, tokens, cost_usd, plans=(), end=None, reason="", models=CODEX_MODELS):
    """A history period lists usage totals; _models splits them when the drawer asks for a breakdown."""
    period = {"start": iso(start), "reset_at": iso(start + length), "used_percent": used, "plans": list(plans),
              "usage": {"tokens": tokens, "cost_usd": cost_usd, "charged_in": "USD"},
              "usage_query": {"from": iso(start), "to": iso(min(end or start + length, NOW))},
              "_models": models}
    if reason:
        period.update(end=iso(end or start + length), end_reason=reason)
    return period


def daily_quota_periods(days, length, plans=(), skip=(), models=CODEX_MODELS):
    """Past periods, one a day, with deterministic usage."""
    periods = []
    for day in days:
        if day in skip:
            continue
        used = (day * 37) % 88 + 9
        tokens = used * 187_431 + day * 1_237
        periods.append(quota_period(NOW - timedelta(days=day, hours=9), length, used, tokens, round(tokens * 7.9e-7, 2),
                                    plans, reason="scheduled", models=models))
    return periods


FIVE_HOURS, WEEK = timedelta(hours=5), timedelta(days=7)
# Plus until 17 days ago, Pro for a week without a 5-hour window, then Plus again with a reset card used 5 days ago.
AUTH_FILE_HISTORY = {
    "auth-demo-codex-plus": [
        {"label": "5 小时限额", "periods": [
            quota_period(NOW - timedelta(hours=1), FIVE_HOURS, 65, 12_408_115, 9.86, ["Plus"]),
            *daily_quota_periods(range(1, 5), FIVE_HOURS, ["Plus"]),
            quota_period(NOW - timedelta(days=5, hours=2), FIVE_HOURS, 31, 5_104_220, 4.02, ["Plus"], reason="scheduled"),
            quota_period(NOW - timedelta(days=5, hours=6), FIVE_HOURS, 100, 15_220_418, 12.07, ["Plus"],
                         end=NOW - timedelta(days=5, hours=2), reason="early_reset"),
            *daily_quota_periods(range(6, 10), FIVE_HOURS, ["Plus"], skip={7}),
            quota_period(NOW - timedelta(days=17, hours=2), FIVE_HOURS, 48, 6_920_331, 5.48, ["Plus", "Pro 100"], reason="scheduled"),
            *daily_quota_periods(range(18, 31), FIVE_HOURS, ["Plus"], skip={21, 26}),
        ]},
        {"label": "周限额", "periods": [
            quota_period(NOW - timedelta(days=1), WEEK, 10, 18_930_552, 15.31, ["Plus"]),
            quota_period(NOW - timedelta(days=8, hours=6), WEEK, 87, 71_402_216, 56.48, ["Pro 100", "Plus"], reason="scheduled"),
            quota_period(NOW - timedelta(days=17), WEEK, 64, 182_005_904, 143.88, ["Pro 100"],
                         end=NOW - timedelta(days=9, hours=6), reason="early_reset"),
            quota_period(NOW - timedelta(days=19, hours=4), WEEK, 72, 35_118_027, 27.76, ["Plus"],
                         end=NOW - timedelta(days=17), reason="early_reset"),
            quota_period(NOW - timedelta(days=26, hours=4), WEEK, 96, 47_906_315, 37.85, ["Plus"], reason="scheduled"),
            quota_period(NOW - timedelta(days=33, hours=4), WEEK, 41, 20_455_108, 16.16, ["Plus"], reason="scheduled"),
        ]},
    ],
    "auth-demo-codex-pro": [
        {"label": "周限额", "periods": [
            quota_period(NOW - timedelta(days=2), WEEK, 38, 48_612_340, 36.2, ["Pro 200"]),
            *[quota_period(NOW - timedelta(days=2 + 7 * week), WEEK, 70 + week * 6, 90_402_118 + week * 7_301_224,
                           round(67.31 + week * 5.44, 2), ["Pro 200"], reason="scheduled") for week in range(1, 5)],
        ]},
        {"label": "5 小时限额", "label_prefix": "GPT-5.3-Codex-Spark ", "periods": [
            quota_period(NOW - timedelta(days=3, hours=4), FIVE_HOURS, 12, 1_204_877, 0.94, ["Pro 200"], reason="scheduled", models=SPARK_MODELS),
            quota_period(NOW - timedelta(days=11, hours=8), FIVE_HOURS, 4, 380_105, 0.3, ["Pro 200"], reason="scheduled", models=SPARK_MODELS),
        ]},
        {"label": "周限额", "label_prefix": "GPT-5.3-Codex-Spark ", "periods": [
            quota_period(NOW - timedelta(days=4), WEEK, 12, 1_584_982, 1.24, ["Pro 200"], models=SPARK_MODELS),
        ]},
    ],
    "auth-demo-claude": [
        {"label": "5 小时限额", "periods": [
            quota_period(NOW - timedelta(hours=1, minutes=30), FIVE_HOURS, 24, 3_104_822, 8.42, models=CLAUDE_MODELS),
            *daily_quota_periods(range(1, 31), FIVE_HOURS, skip={4, 5, 11, 12, 18, 19, 25, 26}, models=CLAUDE_MODELS),
        ]},
        {"label": "周限额", "periods": [
            quota_period(NOW - timedelta(days=2, hours=12), WEEK, 41, 21_733_906, 57.8, models=CLAUDE_MODELS),
            *[quota_period(NOW - timedelta(days=2 + 7 * week, hours=12), WEEK, 55 + week * 9, 30_118_440 + week * 2_008_310,
                           round(80.06 + week * 5.34, 2), reason="scheduled", models=CLAUDE_MODELS) for week in range(1, 5)],
        ]},
    ],
}


def auth_file_history(query, scope=""):
    auth_index = query.get("auth_index", [""])[0]
    auth_file = next((item for item in AUTH_FILES if item["auth_index"] == auth_index), None)
    if auth_file is None:
        return None
    since = NOW - timedelta(days=30)
    rows = [entry for entry in filter_event_time(REQUEST_EVENTS, {"from": [iso(since)], "to": [iso(NOW)]})
            if entry["auth_index"] == auth_index and (not scope or entry["scope"] == scope)]
    windows = []
    for window in AUTH_FILE_HISTORY.get(auth_index, []):
        localized = localized_quota_labels([window])[0]
        localized["periods"] = sorted((public_fields(period, auth_file) for period in window["periods"]), key=lambda period: period["start"], reverse=True)
        windows.append(localized)
    return {"from": iso(since), "to": iso(NOW), "usage": credential_usage(rows), "windows": windows}


USAGE_DIMENSIONS = {"api_keys": ("scope", ""), "models": ("billing_model", "未知模型"), "sources": ("source", "未知来源")}


def usage_models(query, scope=""):
    """Break down credential, quota-period, or distribution usage by model."""
    if "credential_ref" in query:
        credential = CONFIG_CREDENTIALS.get(query["credential_ref"][0], {})
        query = {**query, "auth_index": [credential.get("auth_index", "")]}

    auth_index = query.get("auth_index", [None])[0]
    if auth_index is not None:
        bounds = (query.get("from", [""])[0], query.get("to", [""])[0])
        periods = [*AUTH_FILE_QUOTAS.get(auth_index, {}).get("quota", []),
                   *(period for window in AUTH_FILE_HISTORY.get(auth_index, []) for period in window["periods"])]
        for period in periods:
            if "usage_query" in period and (period["usage_query"]["from"], period["usage_query"]["to"]) == bounds:
                return {"models": model_split(period["usage"]["tokens"], period["usage"]["cost_usd"], period["_models"])}
        return {"models": model_usage([entry for entry in filter_event_time(REQUEST_EVENTS, query)
                                       if entry["auth_index"] == auth_index and (not scope or entry["scope"] == scope)])}
    field, unknown = USAGE_DIMENSIONS[query.get("dimension", ["models"])[0]]
    values = set(query.get("value", []))
    rows = analysis_rows(query, scope)
    if field == "source":
        rows = [entry for entry in rows if source_filter_token(scope, entry.get(field, "") or unknown) in values]
    else:
        rows = [entry for entry in rows if (entry.get(field, "") or unknown) in values]
    return {"models": model_usage(rows)}


KEY_PROFILES = [
    {"label": "代码审查机器人", "plan_id": "engineering", "spent": 128.64, "concurrency_limit": 5, "current_concurrency": 2,
     "route_bindings": {"route_ids": ["coding", "analytics", "economy", "text-only"], "models": [], "credential_ids": [], "credential_providers": []}},
    {"label": "CI 构建服务", "plan_id": "engineering", "spent": 84.27, "concurrency_limit": 10, "current_concurrency": 3,
     "route_bindings": {"route_ids": ["ci"], "models": ["gpt-5.6-terra", "gpt-5.5"],
                        "credential_ids": [AUTOMATION_CREDENTIAL_REF], "credential_providers": [],
                        "denied_models": ["gpt-5.6-sol", "gpt-image-2"], "denied_credential_ids": ["sha256:" + "a" * 64], "denied_credential_providers": []}},
    {"label": "数据分析平台", "plan_id": "production", "spent": 368.91, "concurrency_limit": 5, "current_concurrency": 1,
     "route_bindings": {"route_ids": ["analytics"], "models": [], "credential_ids": [], "credential_providers": []}},
    {"label": "客服助手", "plan_id": "production", "spent": 241.36, "concurrency_limit": 8, "current_concurrency": 2,
     "route_bindings": {"route_ids": ["analytics"], "models": ["gpt-5.5"], "denied_models": ["gpt-image-2"], "credential_ids": [], "credential_providers": []}},
    {"label": "文档生成", "plan_id": "engineering", "spent": 56.48, "concurrency_limit": 3, "current_concurrency": 0,
     "route_bindings": {"route_ids": ["text-only"], "models": [], "credential_ids": [], "credential_providers": []}},
    {"label": "预发布环境", "plan_id": "project-credit", "spent": 306.04, "concurrency_limit": 2, "current_concurrency": 1,
     "route_bindings": {"route_ids": ["economy"], "models": [], "credential_ids": [], "credential_providers": []}},
    {"label": "内部工具", "plan_id": "", "spent": 0, "concurrency_limit": 0, "current_concurrency": 1,
     "route_bindings": {"route_ids": [], "models": [], "credential_ids": [], "credential_providers": []}},
    {"label": "临时测试", "plan_id": "project-credit", "spent": 610.33, "concurrency_limit": 1, "current_concurrency": 0,
     "route_bindings": {"route_ids": [], "models": ["gpt-5.5"], "credential_ids": ["sha256:" + "c" * 64], "credential_providers": []}},
]


def make_key(index):
    profile = KEY_PROFILES[index - 1]
    plan = next((item for item in PLANS if item["id"] == profile["plan_id"]), None)
    result = {
        "scope": hashlib.sha256(CALLER_SCOPE_SALT + f"sk-demo-{index:04d}".encode()).hexdigest(),
        "preview": f"sk-demo…{index:04d}",
        "label": profile["label"],
        "in_config": True,
        "plan_id": profile["plan_id"],
        "concurrency_limit": profile["concurrency_limit"],
        "current_concurrency": profile["current_concurrency"],
        "route_bindings": profile["route_bindings"],
    }
    cycles = {}
    for position, window in enumerate(plan["windows"] if plan and index != 4 else []):
        ratio = profile["spent"] / plan["windows"][-1]["amount"]
        if index == 2 and position == 0 or index == 3 and position < 2:
            ratio = 1.05
        end = (datetime.fromisoformat(window["cycle_anchor_at"].replace("Z", "+00:00"))
               if window.get("cycle_anchor_at") else NOW + timedelta(seconds=window["period_seconds"] * (0.3 + index * 0.04)))
        cycles[window["id"]] = dict(plan_id=result["plan_id"], period_seconds=window["period_seconds"],
            cycle_anchor_at=window.get("cycle_anchor_at"),
            spent=window["amount"]*ratio,
            used_requests=int(window.get("request_limit", 1000)*ratio),
            used_tokens=int(window.get("token_limit", 10000000)*ratio),
            start_at=iso(end-timedelta(seconds=window["period_seconds"])), end_at=iso(end))
    QUOTA_CYCLES[result["scope"]] = cycles
    refresh_key_quota(result)
    return result


KEYS = [make_key(index) for index in range(1, len(KEY_PROFILES) + 1)]
KEYS[-2]["in_config"] = False
KEYS[-2]["deleted_at"] = iso(NOW - timedelta(days=2))
KEYS[-1]["in_config"] = False
KEYS[-1]["deleted_at"] = iso(NOW - timedelta(days=1))
KEYS[-1]["route_bindings"]["route_ids"] = ["economy"]
LIVE_KEYS = [key for key in KEYS if not key.get("deleted_at")]

PRICES = [
    {
        "model_id": "gpt-5.6-sol",
        "currency": "USD",
        "input_per_1m": 4,
        "output_per_1m": 20,
        "cache_read_per_1m": 0.4,
        "source": "custom",
        "long_context": {
            "threshold_input_tokens": 272000,
            "input_per_1m": 8,
            "output_per_1m": 30,
            "cache_read_per_1m": 0.8,
        },
    },
    {
        "model_id": "gpt-5.5",
        "currency": "USD",
        "input_per_1m": 5,
        "output_per_1m": 30,
        "cache_read_per_1m": 0.5,
        "source": "custom",
        "long_context": {
            "threshold_input_tokens": 272000,
            "input_per_1m": 10,
            "output_per_1m": 45,
            "cache_read_per_1m": 1,
        },
    },
    {
        "model_id": "gpt-5.6-luna",
        "currency": "USD",
        "input_per_1m": 0.2,
        "output_per_1m": 1.2,
        "cache_read_per_1m": 0.02,
        "source": "custom",
    },
    {
        "model_id": "gpt-5.6-terra",
        "currency": "USD",
        "input_per_1m": 2,
        "output_per_1m": 12,
        "cache_read_per_1m": 0.2,
        "source": "custom",
    },
    {
        "model_id": "gpt-image-2",
        "currency": "USD",
        "input_per_1m": 5,
        "output_per_1m": 30,
        "cache_read_per_1m": 1.25,
        "source": "custom",
    },
    {
        "model_id": "claude/deepseek-v4-pro",
        "currency": "CNY",
        "input_per_1m": 3,
        "output_per_1m": 6,
        "cache_read_per_1m": 0.025,
        "source": "custom",
    },
    {
        "model_id": "claude/deepseek-v4-flash",
        "currency": "CNY",
        "input_per_1m": 2,
        "output_per_1m": 3,
        "source": "custom",
    },
]

def make_cost(uncached, cache_read, cache_write, output, rates, tiered=False, long_context=False, multiplier=1, currency="USD"):
    input_price, read_price, write_price, output_price = (rate * multiplier for rate in rates)
    parts = {
        "uncached_input": uncached * input_price / 1_000_000,
        "cache_read": cache_read * read_price / 1_000_000,
        "cache_write": cache_write * write_price / 1_000_000,
        "output": output * output_price / 1_000_000,
    }
    return {
        **parts,
        "multiplier": multiplier,
        "currency": currency,
        "total": sum(parts.values()),
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


def event_sample(
    key_index,
    source,
    provider,
    model,
    executor,
    effort,
    tier,
    latency_ms,
    ttft_ms,
    reasoning_tokens,
    tokens,
    rates,
    *,
    billing_model="",
    response_model="",
    response_tier="",
    long_context=False,
    failed=False,
    multiplier=1,
    auth_index="",
    currency="USD",
):
    uncached, cache_read, cache_write, output = tokens
    if failed:
        uncached = cache_read = cache_write = output = 0
    return {
        "key_index": key_index,
        "auth_index": auth_index,
        "source": source,
        "provider": provider,
        "executor_type": executor,
        "reasoning_effort": effort,
        "service_tier": tier,
        "response_service_tier": response_tier,
        "upstream_model": model,
        "response_model": response_model,
        "billing_model": billing_model or model,
        "failed": failed,
        "latency_ms": latency_ms,
        "ttft_ms": ttft_ms,
        "accounting_quality": "" if failed else "complete",
        "price_source": "custom",
        "cost": make_cost(
            uncached,
            cache_read,
            cache_write,
            output,
            rates,
            model.startswith("gpt-5."),
            long_context,
            multiplier if not failed else 1,
            currency,
        ),
        "reasoning_tokens": 0 if failed else reasoning_tokens,
    }


# Numeric usage and timing values are sampled from a real export. All identities
# below are synthetic and intentionally unrelated to the source records.
SUCCESS_EVENT_SAMPLES = [
    event_sample(0, "codex · dev-team@example.com", "codex", "gpt-5.6-sol", "CodexExecutor", "high", "priority", 11513, 8209, 266, (712, 91648, 4096, 425), (4, 0.4, 5, 20), multiplier=2.5, response_tier="priority", auth_index="auth-demo-codex-plus"),
    event_sample(5, "codex · dev-team@example.com", "codex", "codex-auto-review", "CodexWebsocketsExecutor", "low", "auto", 2516, 1431, 10, (1030, 49920, 0, 75), (0.2, 0.02, 0.25, 1.2), response_model="gpt-5.6-luna", response_tier="default", auth_index="auth-demo-codex-plus"),
    event_sample(1, "codex · dev-team@example.com", "codex", "gpt-5.5", "CodexWebsocketsExecutor", "medium", "auto", 2417, 1103, 0, (1194, 95616, 0, 73), (5, 0.5, 5, 30), auth_index="auth-demo-codex-plus"),
    event_sample(1, "codex · dev-team@example.com", "codex", "gpt-5.6-sol", "CodexWebsocketsExecutor", "high", "auto", 5306, 2121, 21, (798, 169984, 0, 201), (4, 0.4, 5, 20), auth_index="auth-demo-codex-plus"),
    event_sample(2, "claude · platform@example.com", "claude", "deepseek-v4-pro", "ClaudeExecutor", "high", "auto", 10061, 637, 0, (38049, 0, 0, 474), (3, 0.025, 3, 6), billing_model="claude/deepseek-v4-pro", response_model="deepseek-v4-pro", auth_index="auth-demo-claude", currency="CNY"),
    event_sample(7, "Codex Relay · sk-proxy…7f3a", "codex", "gpt-5.5", "CodexWebsocketsExecutor", "high", "auto", 7288, 4130, 188, (8502, 45440, 0, 309), (5, 0.5, 5, 30), auth_index="ai-demo-codex-relay"),
    event_sample(6, "codex · dev-team@example.com", "codex", "gpt-5.6-terra", "CodexWebsocketsExecutor", "medium", "auto", 10120, 3379, 81, (1300, 69376, 0, 477), (2, 0.2, 2.5, 12), auth_index="auth-demo-codex-plus"),
    event_sample(4, "codex · dev-team@example.com", "codex", "gpt-5.6-sol", "CodexWebsocketsExecutor", "high", "auto", 2609, 1881, 6, (1368, 237824, 0, 46), (4, 0.4, 5, 20), auth_index="auth-demo-codex-plus"),
    event_sample(5, "codex · dev-team@example.com", "codex", "gpt-5.6-luna", "CodexWebsocketsExecutor", "low", "auto", 5002, 2464, 61, (1261, 149248, 0, 147), (0.2, 0.02, 0.25, 1.2), auth_index="auth-demo-codex-plus"),
    event_sample(7, "Codex Relay · sk-proxy…7f3a", "codex", "gpt-5.5", "CodexWebsocketsExecutor", "high", "auto", 7634, 4412, 94, (24196, 19840, 0, 275), (5, 0.5, 5, 30), auth_index="ai-demo-codex-relay"),
    event_sample(6, "codex · dev-team@example.com", "codex", "gpt-image-2", "CodexExecutor", "", "auto", 43679, 43476, 0, (1658, 0, 0, 915), (5, 1.25, 5, 30), auth_index="auth-demo-codex-plus"),
    event_sample(3, "claude · platform@example.com", "claude", "deepseek-v4-pro", "ClaudeExecutor", "high", "auto", 16431, 909, 0, (66079, 32768, 0, 535), (3, 0.025, 3, 6), billing_model="claude/deepseek-v4-pro", auth_index="auth-demo-claude", currency="CNY"),
    event_sample(0, "codex · dev-team@example.com", "codex", "gpt-5.6-sol", "CodexExecutor", "high", "auto", 19542, 17836, 681, (835, 282752, 0, 802), (8, 0.8, 10, 30), long_context=True, auth_index="auth-demo-codex-plus"),
    event_sample(6, "codex · dev-team@example.com", "codex", "gpt-5.6-luna", "CodexWebsocketsExecutor", "low", "auto", 5074, 3149, 49, (28615, 31488, 0, 124), (0.2, 0.02, 0.25, 1.2), auth_index="auth-demo-codex-plus"),
]

FAILURE_EVENT_SAMPLES = [
    event_sample(1, "codex · dev-team@example.com", "codex", "gpt-5.5", "CodexWebsocketsExecutor", "high", "auto", 4338, 237, 0, (0, 0, 0, 0), (5, 0.5, 5, 30), failed=True, auth_index="auth-demo-codex-plus"),
]

UNASSIGNED_EVENT_SAMPLES = [
    event_sample(None, "codex · dev-team@example.com", "codex", "gpt-5.5", "CodexWebsocketsExecutor", "medium", "auto", 3120, 1288, 12, (2048, 31744, 0, 96), (5, 0.5, 5, 30), auth_index="auth-demo-codex-plus"),
    event_sample(None, "codex · dev-team@example.com", "codex", "gpt-5.5", "CodexWebsocketsExecutor", "medium", "auto", 1810, 0, 0, (0, 0, 0, 0), (5, 0.5, 5, 30), failed=True, auth_index="auth-demo-codex-plus"),
]

EVENT_SAMPLES = SUCCESS_EVENT_SAMPLES * 2 + FAILURE_EVENT_SAMPLES + UNASSIGNED_EVENT_SAMPLES


def make_request_events():
    entries = []
    session_id = "codex:01a16989-602b-7f81-83a1-964cd3cce8d9"
    request_variants = [
        {"endpoint": "/v1/chat/completions", "transport": "http", "stream": True, "user_agent": "OpenAI/Python 2.8.1", "x_forwarded_for": "203.0.113.24"},
        {
            "endpoint": "/v1/responses",
            "transport": "websocket",
            "stream": True,
            "user_agent": "codex_cli_rs/0.144.0 (Mac OS 26.1.0; arm64) iTerm.app/3.6.6",
            "x_forwarded_for": "198.51.100.7, 192.0.2.44",
        },
        {"endpoint": "/v1/messages", "transport": "http", "stream": False, "user_agent": "claude-cli/2.1.30 (external, cli)"},
        {},
        {
            "endpoint": "/v1/responses",
            "transport": "http",
            "stream": True,
            "user_agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36",
            "x_forwarded_for": "2001:db8:85a3::8a2e:370:7334",
        },
        {"endpoint": "/v1/responses", "transport": "http"},
    ]
    for index, sample in enumerate(EVENT_SAMPLES):
        key = {"scope": "", "preview": "", "label": ""} if sample["key_index"] is None else KEYS[sample["key_index"]]
        entries.append({
            "id": str(index + 1),
            "at": iso(NOW - timedelta(hours=index * 22, minutes=(index % 4) * 11)),
            "scope": key["scope"],
            "preview": key["preview"],
            "label": key["label"],
            **{name: value for name, value in sample.items() if name != "key_index"},
            "execution": {
                "request_id": f"dummy-execution-{index + 1}",
                "trace_id": "demo0001",
                "session_id": session_id,
                "parent_session_id": session_id if index % 2 else "",
                "access_provider": "config-inline",
                "is_native_key": True,
            },
            **request_variants[index % len(request_variants)],
        })
    return entries


REQUEST_EVENTS = make_request_events()

def source_filter_token(scope, source):
    payload = "filter:v1\0source\0" + scope + "\0" + source
    return hashlib.sha256(payload.encode()).hexdigest()

def source_filter_options(scope, sources):
    return [{"value": source_filter_token(scope, source), "label": source} for source in sources]

# None selects every key; an empty scope selects unassigned events.
def selected_scope(query, scope):
    if scope:
        return None
    if query.get("api_key_empty", [""])[0] == "true":
        return ""
    return query.get("api_key", [""])[0] or None

def event_snapshot(query):
    return int(query.get("snapshot_id", [str(max((int(entry["id"]) for entry in REQUEST_EVENTS), default=0))])[0])

def request_event_view(query, scope=""):
    snapshot = event_snapshot(query)
    selected_key = selected_scope(query, scope)
    selected_model = query.get("model", [""])[0]
    selected_source = query.get("source", [""])[0]
    selected_provider = query.get("provider", [""])[0]
    selected_executor = query.get("executor", [""])[0]
    selected_failed = query.get("failed", [""])[0]
    offset = max(0, int(query.get("offset", ["0"])[0] or 0))
    limit = max(0, int(query.get("limit", ["0"])[0] or 0))
    time_matched = filter_event_time([entry for entry in REQUEST_EVENTS
                                     if int(entry["id"]) <= snapshot and (not scope or entry["scope"] == scope)], query)
    time_matched.sort(key=lambda entry: (entry["at"], int(entry["id"])), reverse=True)
    source_values = sorted({entry.get("source", "") for entry in time_matched} - {""}, key=str.lower)
    filter_options = {
        "models": sorted({entry.get("billing_model") or entry.get("upstream_model", "")
                          for entry in time_matched} - {""}, key=str.lower),
        "source_options": source_filter_options(scope, source_values),
        "providers": sorted({entry.get("provider", "") for entry in time_matched} - {""}, key=str.lower),
        "executors": sorted({entry.get("executor_type", "") for entry in time_matched} - {""}, key=str.lower),
    }
    if selected_source:
        selected_source = next((source for source in source_values
                                if source_filter_token(scope, source) == selected_source), "\0")
    counts = {"all": 0, "normal": 0, "failed": 0}
    matched = []
    for entry in time_matched:
        if selected_key is not None and entry.get("scope") != selected_key:
            continue
        if selected_model and (entry.get("billing_model") or entry.get("upstream_model")) != selected_model:
            continue
        if selected_source and entry.get("source") != selected_source:
            continue
        if selected_provider and entry.get("provider") != selected_provider:
            continue
        if selected_executor and entry.get("executor_type") != selected_executor:
            continue
        failed = bool(entry.get("failed"))
        counts["all"] += 1
        counts["failed" if failed else "normal"] += 1
        if selected_failed and failed != (selected_failed == "true"):
            continue
        matched.append(entry)
    page = matched[offset:offset + limit] if limit else matched[offset:]
    if scope:
        page = [{key: value for key, value in entry.items()
                 if key not in {"scope", "auth_index", "preview", "label"}} for entry in page]
    result = {"entries": page, "total": len(matched), "snapshot_id": str(snapshot), "status_counts": counts}
    if offset == 0:
        result["filter_options"] = filter_options
    return result


def filter_event_time(entries, query):
    from_raw = query.get("from", [""])[0]
    to_raw = query.get("to", [""])[0]
    from_time = datetime.fromisoformat(from_raw.replace("Z", "+00:00")) if from_raw else None
    to_time = datetime.fromisoformat(to_raw.replace("Z", "+00:00")) if to_raw else None
    return [entry for entry in entries if
            (not from_time or datetime.fromisoformat(entry["at"].replace("Z", "+00:00")) >= from_time) and
            (not to_time or datetime.fromisoformat(entry["at"].replace("Z", "+00:00")) < to_time)]


def account_routing(index):
    bindings = LIVE_KEYS[index]["route_bindings"]
    rules = [bindings] + [route["rule"] for route in ROUTES if route["id"] in bindings["route_ids"]]
    result = []
    for prefix in ("", "denied_"):
        models, refs, providers = set(), set(), set()
        for rule in rules:
            models.update(rule.get(prefix + "models", []))
            refs.update(rule.get(prefix + "credential_ids", []))
            providers.update((item["source"], item["provider"]) for item in rule.get(prefix + "credential_providers", []))
        result.extend((models, refs, providers))
    return result


def account_routing_view(index):
    models, refs, providers, denied_models, denied_refs, denied_providers = account_routing(index)
    def credential_view(item):
        return {"source": item["source"], "provider": item["provider"], "provider_name": item["provider_name"],
                "name": item["display_name"], "status": item["status"],
                "denied": item["ref"] in denied_refs or (item["source"], item["provider"]) in denied_providers}
    return {
        "models": sorted(models), "denied_models": sorted(denied_models),
        "credentials": [credential_view(item) for item in CREDENTIALS if item["ref"] in refs or (item["source"], item["provider"]) in providers],
        "denied_credentials": [credential_view(item) for item in CREDENTIALS if item["ref"] in denied_refs] +
            [{"source": source, "provider": provider, "provider_wide": True, "denied": True} for source, provider in sorted(denied_providers)],
        "catalog_filtered": True, "warnings": [],
    }


def account_auth_files(index):
    _, refs, providers, _, denied_refs, denied_providers = account_routing(index)
    return [item for item in AUTH_FILES
            if (not refs and not providers or AUTH_FILE_CREDENTIAL_REFS.get(item["auth_index"]) in refs or ("auth-files", item["category"]) in providers)
            and AUTH_FILE_CREDENTIAL_REFS.get(item["auth_index"]) not in denied_refs
            and ("auth-files", item["category"]) not in denied_providers]


def configured_provider_views(query, account_index=None):
    entries = filter_event_time(REQUEST_EVENTS, query)
    credentials = list(CONFIG_CREDENTIALS.values())
    if account_index is not None:
        _, refs, providers, _, denied_refs, denied_providers = account_routing(account_index)
        credentials = [item for item in credentials
                       if (not refs and not providers or item["ref"] in refs or ("ai-providers", item["provider"]) in providers)
                       and item["ref"] not in denied_refs and ("ai-providers", item["provider"]) not in denied_providers]
        entries = [row for row in entries if row["scope"] == LIVE_KEYS[account_index]["scope"]]
    return [{**item, "in_flight": 0 if item["disabled"] else (1 if account_index is not None else 3),
             "usage": credential_usage([row for row in entries if row["auth_index"] == item["auth_index"]])
             if item["auth_index"] else None} for item in credentials]


def refresh_route_counts():
    for route in ROUTES:
        bound = [key for key in KEYS if route["id"] in key["route_bindings"]["route_ids"]]
        route["bound_key_count"] = len(bound)
        route["deleted_key_count"] = sum(bool(key.get("deleted_at")) for key in bound)
        route["fully_unrestricted_keys"] = sum(
            not key.get("deleted_at")
            and len(key["route_bindings"]["route_ids"]) == 1
            and not key["route_bindings"]["models"]
            and not key["route_bindings"]["credential_ids"]
            and not key["route_bindings"]["credential_providers"]
            and not any(key["route_bindings"].get(field) for field in ("denied_models", "denied_credential_ids", "denied_credential_providers"))
            for key in bound
        )


# transport=True is a bare executor error: the body is the message itself
# rather than an upstream payload.
def request_error(event_index, message, status=0, error_type="", code="", transport=False):
    event = REQUEST_EVENTS[event_index]
    event["failed"] = True
    body = message
    if not transport:
        error = {"message": message}
        if error_type:
            error["type"] = error_type
        if code:
            error["code"] = code
        if 400 <= status <= 599:
            error["status"] = status
        body = json.dumps({"error": error}, ensure_ascii=False, separators=(",", ":"))
    event["error_body"] = body
    return {
        "id": event["id"],
        "at": event["at"],
        "scope": event["scope"],
        "preview": event["preview"],
        "label": event["label"],
        "source": event["source"],
        "provider": event["provider"],
        "executor_type": event["executor_type"],
        "upstream_model": event["upstream_model"],
        "billing_model": event["billing_model"],
        "latency_ms": event["latency_ms"],
        "ttft_ms": event["ttft_ms"],
        "status_code": status,
        "error_type": code or error_type,
        "body": body,
    }


ERRORS = [
    request_error(30, "Rate limit reached for requests", status=429, error_type="rate_limit_error"),
    request_error(
        28,
        "Responses websocket connection limit reached (60 minutes). Create a new websocket connection to continue.",
        status=400,
        error_type="invalid_request_error",
        code="websocket_connection_limit_reached",
    ),
    request_error(27, "The model is not supported.", status=400, error_type="invalid_request_error"),
    request_error(26, "websocket: close 1012"),
    request_error(
        25,
        "upstream request timed out",
        status=504,
        error_type="timeout_error",
        code="upstream_timeout",
    ),
    request_error(
        24,
        "websocket: close 1006 (abnormal closure): unexpected EOF",
        code="websocket_abnormal_closure",
        transport=True,
    ),
    request_error(
        23,
        'Post "https://api.deepseek.com/anthropic/v1/messages?beta=true": context canceled',
        code="context_canceled",
        transport=True,
    ),
]

PLUGIN_LOGS = [
    {
        "id": 4,
        "at": iso(NOW - timedelta(minutes=1)),
        "level": "info",
        "message": "route " + json.dumps({"key": "代码审查机器人 · sk-demo…0001", "model": "gpt-5.6-luna", "model_policy": "restricted", "model_result": "allow", "credential_policy": "restricted", "credential_result": "unavailable", "outcome": "failed", "status": 503}, ensure_ascii=False, separators=(",", ":")),
    },
    {
        "id": 3,
        "at": iso(NOW - timedelta(minutes=2)),
        "level": "debug",
        "message": "route " + json.dumps({"key": "代码审查机器人 · sk-demo…0001", "model": "gpt-5.6-sol", "model_policy": "restricted", "model_result": "allow", "credential_policy": "restricted", "credential_result": "allow", "selected_credential": "codex · dev-team@example.com", "outcome": "succeeded", "status": 200}, ensure_ascii=False, separators=(",", ":")),
    },
    {
        "id": 2,
        "at": iso(NOW - timedelta(minutes=11)),
        "level": "info",
        "message": (
            "已加载计费数据库 /srv/cli-proxy-api/plugins/cpa-key-billing-state-v1.db："
            "8 个 API Key、3 个订阅计划、29 条请求事件。已启用。"
        ),
    },
    {
        "id": 1,
        "at": iso(NOW - timedelta(hours=12, minutes=40)),
        "level": "info",
        "message": "已同步 CLIProxyAPI 的 API Key 列表：新增 1 个。",
    },
]


def seed_paginated_history():
    """Provide multiple real pages of synthetic history in the default preview."""
    event_samples = list(REQUEST_EVENTS)
    error_samples = {entry["id"]: entry for entry in ERRORS}
    log_samples = list(PLUGIN_LOGS)
    REQUEST_EVENTS.clear()
    ERRORS.clear()
    PLUGIN_LOGS.clear()
    for batch in range(40):
        for index, sample in enumerate(event_samples):
            sequence = batch * len(event_samples) + index
            # Upstream load drifts slowly, giving speed trends some shape.
            load = 1 + 0.3 * math.sin(sequence / 90)
            identity = {
                "id": str(sequence + 1),
                "at": iso(NOW - timedelta(minutes=sequence)),
                "latency_ms": round(sample["latency_ms"] * load),
                "ttft_ms": round(sample["ttft_ms"] * load),
            }
            REQUEST_EVENTS.append({**sample, **identity})
            if sample["id"] in error_samples:
                ERRORS.append({**error_samples[sample["id"]], **identity})
        for index, sample in enumerate(log_samples):
            sequence = batch * len(log_samples) + index
            PLUGIN_LOGS.append({
                **sample,
                "id": 40 * len(log_samples) - sequence,
                "at": iso(NOW - timedelta(minutes=sequence)),
            })


def error_view(query, scope=""):
    snapshot = event_snapshot(query)
    rows = filter_event_time([entry for entry in ERRORS
                             if int(entry["id"]) <= snapshot and (not scope or entry["scope"] == scope)], query)
    rows.sort(key=lambda entry: (entry["at"], int(entry["id"])), reverse=True)
    selected = {
        "api_key": selected_scope(query, scope),
        "model": query.get("model", [""])[0],
        "source": query.get("source", [""])[0],
        "provider": query.get("provider", [""])[0],
        "executor": query.get("executor", [""])[0],
        "status_code": query.get("status_code", [""])[0],
        "error_type": query.get("error_type", [""])[0],
    }
    source_values = sorted({entry["source"] for entry in rows})
    if selected["source"]:
        selected["source"] = next((source for source in source_values
                                   if source_filter_token(scope, source) == selected["source"]), "\0")
    filtered = []
    counts = {}
    empty_type = query.get("error_type_empty", [""])[0] == "true"
    for entry in rows:
        if selected["api_key"] is not None and entry["scope"] != selected["api_key"]:
            continue
        if selected["model"] and entry["billing_model"] != selected["model"]:
            continue
        if selected["source"] and entry["source"] != selected["source"]:
            continue
        if selected["provider"] and entry["provider"] != selected["provider"]:
            continue
        if selected["executor"] and entry["executor_type"] != selected["executor"]:
            continue
        if selected["status_code"] and str(entry["status_code"]) != selected["status_code"]:
            continue
        error_type = entry["error_type"]
        counts[error_type] = counts.get(error_type, 0) + 1
        if empty_type:
            if error_type:
                continue
        elif selected["error_type"] and error_type != selected["error_type"]:
            continue
        filtered.append(entry)
    offset = max(0, int(query.get("offset", ["0"])[0] or 0))
    limit = max(0, int(query.get("limit", ["0"])[0] or 0))
    page = filtered[offset:offset + limit] if limit else filtered[offset:]
    if scope:
        page = [{key: value for key, value in entry.items()
                 if key not in {"scope", "preview", "label", "auth_index"}} for entry in page]
    result = {"entries": page, "total": len(filtered), "snapshot_id": str(snapshot), "error_type_counts": counts}
    if offset == 0:
        result["filter_options"] = {
            "models": sorted({entry["billing_model"] for entry in rows}),
            "source_options": source_filter_options(scope, source_values),
            "providers": sorted({entry["provider"] for entry in rows}),
            "executors": sorted({entry["executor_type"] for entry in rows}),
            "status_codes": sorted({entry["status_code"] for entry in rows if entry["status_code"]}),
            "error_types": sorted({entry["error_type"] for entry in rows if entry["error_type"]}),
        }
    return result


def analysis_rows(query, scope):
    rows = filter_event_time([entry for entry in REQUEST_EVENTS if not scope or entry["scope"] == scope], query)
    selected = selected_scope(query, scope)
    return rows if selected is None else [entry for entry in rows if entry["scope"] == selected]


# Hourly ranges start at "from"; longer ranges use browser-local days.
def trend_starts(query):
    from_time = datetime.fromisoformat(
        query.get("from", [iso(NOW - timedelta(days=30))])[0].replace("Z", "+00:00")
    )
    to_time = datetime.fromisoformat(
        query.get("to", [iso(NOW)])[0].replace("Z", "+00:00")
    )
    bucket_size = timedelta(hours=1)
    cursor = from_time
    if to_time - from_time > timedelta(days=1):
        bucket_size = timedelta(days=1)
        local_from = from_time.astimezone(ZoneInfo(query.get("timezone", ["UTC"])[0]))
        cursor = local_from.replace(hour=0, minute=0, second=0, microsecond=0)
    starts = []
    while cursor < to_time:
        starts.append(cursor)
        cursor += bucket_size
    return starts


def trend_index(starts, entry):
    return bisect.bisect_right(starts, datetime.fromisoformat(entry["at"].replace("Z", "+00:00"))) - 1


def analysis_view(query, scope=""):
    rows = analysis_rows(query, scope)
    selected = selected_scope(query, scope)

    def distribution(field, label_field=None, unknown="未知"):
        grouped, members = {}, {}
        for entry in rows:
            key = entry.get(field, "") or unknown
            label = entry.get(label_field, "") if label_field else key
            if not label and field == "scope":
                label = entry.get("preview", "")
            item = grouped.setdefault(key, {"key": key, "label": label or key,
                                            "total_tokens": 0, "requests": 0,
                                            "input_tokens": 0, "output_tokens": 0,
                                            "cache_read_tokens": 0, "cache_write_tokens": 0,
                                            "cost_usd": 0})
            if field == "scope":
                item["preview"] = entry.get("preview", "")
            if field == "source":
                item["filter"] = source_filter_token(scope, key)
            cost = entry.get("cost", {})
            input_tokens = sum(cost.get(name, 0) for name in (
                "uncached_input_tokens", "cache_read_tokens", "cache_write_tokens"))
            output_tokens = cost.get("billed_output_tokens", 0)
            item["input_tokens"] += input_tokens
            item["output_tokens"] += output_tokens
            item["cache_read_tokens"] += cost.get("cache_read_tokens", 0)
            item["cache_write_tokens"] += cost.get("cache_write_tokens", 0)
            item["total_tokens"] += input_tokens + output_tokens
            item["requests"] += 1
            item["cost_usd"] += usd_amount(cost.get("total", 0), cost.get("currency"))
            members.setdefault(key, []).append(entry)
        for key, item in grouped.items():
            item["charged_in"] = charged_in(members[key])
        token_total = sum(item["total_tokens"] for item in grouped.values())
        request_total = sum(item["requests"] for item in grouped.values())
        for item in grouped.values():
            denominator = token_total if token_total else request_total
            numerator = item["total_tokens"] if token_total else item["requests"]
            item["percent"] = numerator * 100 / max(1, denominator)
        return sorted(grouped.values(), key=lambda item: (-item["total_tokens"], -item["requests"], item["label"]))

    requests = len(rows)
    failed = sum(int(entry.get("failed", False)) for entry in rows)
    input_tokens = sum(
        sum(entry.get("cost", {}).get(name, 0) for name in (
            "uncached_input_tokens", "cache_read_tokens", "cache_write_tokens"
        )) for entry in rows
    )
    cache_read_tokens = sum(entry.get("cost", {}).get("cache_read_tokens", 0) for entry in rows)
    cache_write_tokens = sum(entry.get("cost", {}).get("cache_write_tokens", 0) for entry in rows)
    output_tokens = sum(entry.get("cost", {}).get("billed_output_tokens", 0) for entry in rows)
    def usd_sum(field):
        return sum(usd_amount(entry.get("cost", {}).get(field, 0), entry.get("cost", {}).get("currency")) for entry in rows)

    cost = {
        "input_usd": usd_sum("uncached_input"),
        "cache_read_usd": usd_sum("cache_read"),
        "cache_write_usd": usd_sum("cache_write"),
        "output_usd": usd_sum("output"),
    }
    cost["total_usd"] = sum(cost[field] for field in (
        "input_usd", "cache_read_usd", "cache_write_usd", "output_usd"
    ))
    cost["charged_in"] = charged_in(rows)

    starts = trend_starts(query)
    buckets = [{"time": start, "requests": 0,
                "input_tokens": 0, "output_tokens": 0,
                "cache_read_tokens": 0, "cache_write_tokens": 0,
                "total_cost": 0} for start in starts]
    for entry in rows:
        index = trend_index(starts, entry)
        if index < 0:
            continue
        item = buckets[index]
        item["requests"] += 1
        entry_cost = entry.get("cost", {})
        item["input_tokens"] += entry_cost.get("uncached_input_tokens", 0)
        item["output_tokens"] += entry_cost.get("billed_output_tokens", 0)
        item["cache_read_tokens"] += entry_cost.get("cache_read_tokens", 0)
        item["cache_write_tokens"] += entry_cost.get("cache_write_tokens", 0)
        item["total_cost"] += usd_amount(entry_cost.get("total", 0), entry_cost.get("currency"))

    def trend(value):
        return [{"time": iso(item["time"]), "value": value(item)} for item in buckets]

    def total_input(item):
        return item["input_tokens"] + item["cache_read_tokens"] + item["cache_write_tokens"]

    trends = {
        "requests": trend(lambda item: item["requests"]),
        "total_tokens": trend(lambda item: total_input(item) + item["output_tokens"]),
        "input_tokens": trend(lambda item: item["input_tokens"]),
        "output_tokens": trend(lambda item: item["output_tokens"]),
        "cache_read_tokens": trend(lambda item: item["cache_read_tokens"]),
        "cache_write_tokens": trend(lambda item: item["cache_write_tokens"]),
        "cache_rate": trend(lambda item: item["cache_read_tokens"] * 100 / total_input(item)
                            if total_input(item) else 0),
        "total_cost": trend(lambda item: item["total_cost"]),
    }

    return {
        "summary": {
            "requests": requests,
            "succeeded": requests - failed,
            "failed": failed,
            "success_rate": (requests - failed) * 100 / requests if requests else 0,
            "total_tokens": input_tokens + output_tokens,
            "input_tokens": input_tokens,
            "output_tokens": output_tokens,
            "cache_read_tokens": cache_read_tokens,
            "cache_write_tokens": cache_write_tokens,
            "cache_rate": cache_read_tokens * 100 / input_tokens if input_tokens else 0,
            "cost": cost,
        },
        "trends": trends,
        "usage_distribution": {
            "api_keys": [] if scope or selected is not None else distribution("scope", "label", unknown=""),
            "models": distribution("billing_model", unknown="未知模型"),
            "sources": distribution("source", unknown="未知来源"),
        },
    }


def speed_stats(values):
    if not values:
        return {"samples": 0, "p10": 0, "p25": 0, "p50": 0, "p75": 0, "p90": 0}
    values = sorted(values)

    def quantile(q):
        position = q * (len(values) - 1)
        lower = int(position)
        if lower == len(values) - 1:
            return values[lower]
        return values[lower] + (values[lower + 1] - values[lower]) * (position - lower)

    return {"samples": len(values), "p10": quantile(0.1), "p25": quantile(0.25), "p50": quantile(0.5),
            "p75": quantile(0.75), "p90": quantile(0.9)}


def analysis_speed_view(query, scope=""):
    model = query.get("model", [""])[0].strip()
    rows = [entry for entry in analysis_rows(query, scope)
            if entry["billing_model"] == model and not entry["failed"] and entry["accounting_quality"] == "complete"
            and entry["cost"]["billed_output_tokens"] > 0 and entry["latency_ms"] > 0]
    starts = trend_starts(query)
    buckets = [[] for _ in starts]
    total = []
    for entry in rows:
        tps = entry["cost"]["billed_output_tokens"] * 1000 / entry["latency_ms"]
        total.append(tps)
        index = trend_index(starts, entry)
        if index >= 0:
            buckets[index].append(tps)
    return {
        "summary": speed_stats(total),
        "trend": [{"time": iso(start), **speed_stats(bucket)} for start, bucket in zip(starts, buckets)],
    }


for price in PRICES:
    price["in_models"] = True
PRICES.extend([
    {
        "model_id": "demo-reference", "source": "reference", "currency": "USD",
        "input_per_1m": 1.5, "output_per_1m": 3, "in_models": True,
    },
    {
        "model_id": "demo-free", "source": "custom", "currency": "USD",
        "input_per_1m": 0, "output_per_1m": 0, "in_models": True,
    },
    {
        "model_id": "demo-unpriced", "source": "none",
        "input_per_1m": 0, "output_per_1m": 0, "in_models": True,
    },
    {
        "model_id": "demo-retired-custom", "source": "custom", "currency": "CNY",
        "input_per_1m": 1, "output_per_1m": 2, "in_models": False,
    },
])
REFERENCE_PRICES = {
    price["model_id"]: dict(price)
    for price in PRICES if price["source"] == "reference"
}


def price_status():
    return {
        "metadata": {
            "source_url": "https://models.dev/catalog.json",
            "content_hash": "dummy-ui-reference-prices-hash",
            "version": 1,
            "model_count": len(REFERENCE_PRICES),
            "fetched_at": "2026-09-05T00:00:00Z",
            "usable": True,
        },
    }


def model_prices(query, include_custom):
    models = set(query.get("model", []))
    rows = {row["model_id"]: row for row in PRICES}
    names = models | ({row["model_id"] for row in PRICES if row["source"] == "custom"} if include_custom else set())
    return [dict(rows.get(model, {"model_id": model, "source": "none", "input_per_1m": 0, "output_per_1m": 0}),
                 in_models=model in models) for model in sorted(names)]


def credential_labels(refs):
    return {item["ref"]: item["provider_name"] + " · " + item["display_name"]
            for item in CREDENTIALS if item["ref"] in refs}


def key_rows():
    for key in LIVE_KEYS:
        refresh_key_quota(key)
    return [dict(key, route_names={route["id"]: route["name"] for route in ROUTES
                                  if route["id"] in key["route_bindings"]["route_ids"]},
                 credential_labels=credential_labels(key["route_bindings"]["credential_ids"] + key["route_bindings"].get("denied_credential_ids", []))) for key in KEYS]


def route_rows():
    return [dict(route, credential_labels=credential_labels(route["rule"].get("credential_ids", []) + route["rule"].get("denied_credential_ids", []))) for route in ROUTES]


def payload_for(path, query):
    if path == f"{API_BASE}/keys":
        refresh_route_counts()
        return {"keys": key_rows()}
    if path == f"{API_BASE}/plans":
        return {"plans": PLANS}
    if path == f"{API_BASE}/routes":
        refresh_route_counts()
        return {"routes": route_rows()}
    if path == f"{API_BASE}/credentials":
        return {"credentials": CREDENTIALS}
    if path == f"{API_BASE}/currency":
        return {"cny_per_usd": CNY_PER_USD}
    if path == f"{API_BASE}/prices/reference/status":
        return price_status()
    if path == f"{API_BASE}/prices":
        return model_prices(query, include_custom=query.get("include_custom", ["true"])[0] == "true")
    if path == f"{API_BASE}/events/keys":
        scopes = {event["scope"] for event in filter_event_time(REQUEST_EVENTS, query)}
        keys = [{field: key[field] for field in ("scope", "preview", "label", "deleted_at") if field in key}
                for key in KEYS if key["scope"] in scopes]
        if "" in scopes:
            keys.append({"scope": "", "preview": ""})
        return keys
    if path == f"{API_BASE}/events":
        return request_event_view(query)
    if path == f"{API_BASE}/errors":
        return error_view(query)
    if path == f"{API_BASE}/analysis":
        return analysis_view(query)
    if path == f"{API_BASE}/analysis/speed":
        return analysis_speed_view(query)
    if path == f"{API_BASE}/plugin-logs":
        counts = {level: sum(entry["level"] == level for entry in PLUGIN_LOGS)
                  for level in ("debug", "info", "error")}
        levels = query.get("level", ["all"])[0].split(",")
        before = int(query.get("before_id", ["0"])[0])
        limit = int(query.get("limit", ["100"])[0])
        entries = [entry for entry in PLUGIN_LOGS
                   if ("all" in levels or entry["level"] in levels)
                   and (not before or entry["id"] < before)]
        return {"entries": entries[:limit], "level_counts": counts,
                "next_before_id": entries[limit - 1]["id"] if len(entries) > limit else 0}
    if path == "/v0/management/auth-files":
        return {"files": [{field: item[field] for field in ("auth_index", "weight", "id_token") if field in item}
                          for item in AUTH_FILES]}
    if path == f"{API_BASE}/auth-files":
        return {"files": auth_file_views(AUTH_FILES, query)}
    if path == f"{API_BASE}/auth-files/quota":
        return auth_file_quota(query)
    if path == f"{API_BASE}/auth-files/history":
        return auth_file_history(query)
    if path == f"{API_BASE}/usage/models":
        return usage_models(query)
    if path == f"{API_BASE}/ai-providers":
        return {"providers": configured_provider_views(query)}
    if path.startswith("/v0/management/") and path.removeprefix("/v0/management/") in AI_PROVIDER_LISTS:
        field = path.removeprefix("/v0/management/")
        return {field: AI_PROVIDER_LISTS[field]}
    if path == "/v8/management/config/api-keys":
        return ai_provider_groups()
    if path == f"{API_BASE}/prices/reference":
        term = query.get("q", [""])[0].lower()
        return {"prices": [
            {**price, "provider_id": "demo", "model_id": model, "is_canonical": True}
            for model, price in REFERENCE_PRICES.items() if term in model.lower()
        ]}
    if path in {"/v0/management/config", "/v0/management/api-keys"}:
        config = {"api-keys": [f"sk-demo-{index:04d}" for index in range(1, len(LIVE_KEYS) + 1)]}
        if path == "/v0/management/config":
            config.update(without_auth_indexes(AI_PROVIDER_LISTS))
        return config
    if path == "/v1/models":
        return {"data": [{"id": row["model_id"]} for row in PRICES if row.get("in_models")] + [
            {"id": "codex/deepseek-v4-flash-vision-exp"},
        ]}
    return None


class Handler(BaseHTTPRequestHandler):
    host_mode = "standalone"
    initial_theme = "auto"
    allow_api_key_quota_reset = False

    def send_response(self, code, message=None):
        time.sleep(random.uniform(0.4, 0.6))
        super().send_response(code, message)

    def send_html(self, body):
        encoded = body.encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def send_json(self, status, payload):
        if 200 <= status < 300 and getattr(self, "mutation_view", None) is not None:
            path = urlparse(self.path).path
            view = {}
            if path in {f"{API_BASE}/plans", f"{API_BASE}/routes"} or path.startswith(f"{API_BASE}/keys/"):
                refresh_route_counts()
                view["keys"] = key_rows()
                if path == f"{API_BASE}/plans":
                    view["plans"] = PLANS
                if path in {f"{API_BASE}/routes", f"{API_BASE}/keys/routes"}:
                    view["routes"] = route_rows()
            elif path in {f"{API_BASE}/prices", f"{API_BASE}/prices/reference/refresh"}:
                view["prices"] = model_prices({"model": self.mutation_view.get("models", [])}, include_custom=True)
                view["metadata"] = price_status()["metadata"]
            elif path == f"{API_BASE}/plugin-logs":
                view["logs_cleared"] = True
            payload = dict(payload, view=view)
        body = json.dumps(payload, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        self.mutation_view = None
        parsed = urlparse(self.path)
        # Repeated parameters may be empty, as distribution rows name the unassigned key.
        query = parse_qs(parsed.query, keep_blank_values=True)
        if parsed.path == "/favicon.ico":
            self.send_response(204)
            self.end_headers()
            return
        if parsed.path == "/" and self.host_mode != "standalone":
            label = "CPAMC" if self.host_mode == "cpamc" else "CPAMP"
            body = (
                HOST_SHELL.replace("__HOST_MODE__", self.host_mode)
                .replace("__HOST_LABEL__", label)
                .replace("__INITIAL_THEME__", self.initial_theme)
            )
            self.send_html(body)
            return
        if parsed.path in ("/", "/ui"):
            catalogs = {language: json.loads((UI_DIR / "locales" / f"{language}.json").read_text())
                        for language in ("en", "zh-CN")}
            messages = "const BILLING_MESSAGES = " + json.dumps(catalogs).replace("<", "\\u003c") + ";\n"

            def include(match):
                name = match.group(1)
                return (messages if name == "i18n.js" else "") + (UI_DIR / name).read_text()

            body = UI_INCLUDE.sub(include, (UI_DIR / "ui.html").read_text())
            self.send_html(body)
            return
        authorization = self.headers.get("Authorization", "")
        api_keys = [f"sk-demo-{index:04d}" for index in range(1, len(LIVE_KEYS) + 1)]
        if parsed.path == "/v1/models":
            if not authorization.startswith("Bearer ") or authorization[7:] not in api_keys:
                self.send_json(401, {"error": {"message": "API Key 无效"}})
                return
            # Mirrors the plugin's catalog filtering for model rules; management asks for every model.
            payload = payload_for(parsed.path, query)
            if self.headers.get("X-CPA-Key-Billing-Models", "").lower() != "all":
                models, _, _, denied_models, _, _ = account_routing(api_keys.index(authorization[7:]))
                payload["data"] = [row for row in payload["data"] if (not models or row["id"] in models) and row["id"] not in denied_models]
            self.send_json(200, payload)
            return
        resource_paths = {
            f"{RESOURCE_BASE}/profile",
            f"{RESOURCE_BASE}/subscription",
            f"{RESOURCE_BASE}/routing",
            f"{RESOURCE_BASE}/currency",
            f"{RESOURCE_BASE}/prices",
            f"{RESOURCE_BASE}/events",
            f"{RESOURCE_BASE}/errors",
            f"{RESOURCE_BASE}/analysis",
            f"{RESOURCE_BASE}/analysis/speed",
            f"{RESOURCE_BASE}/auth-files",
            f"{RESOURCE_BASE}/auth-files/quota",
            f"{RESOURCE_BASE}/auth-files/quota/reset",
            f"{RESOURCE_BASE}/auth-files/history",
            f"{RESOURCE_BASE}/usage/models",
            f"{RESOURCE_BASE}/ai-providers",
        }
        if parsed.path in resource_paths:
            if not authorization.startswith("Bearer ") or authorization[7:] not in api_keys:
                self.send_json(401, {"error": {"message": "API Key 无效"}})
                return
            index = api_keys.index(authorization[7:])
            if parsed.path.endswith("/profile"):
                key = LIVE_KEYS[index]
                self.send_json(200, {"tracked": True, "identity": {"preview": key["preview"], "label": key["label"]},
                                     "can_reset_auth_quota": self.allow_api_key_quota_reset, "can_use_unpriced_models": False})
            elif parsed.path.endswith("/subscription"):
                key = LIVE_KEYS[index]
                refresh_key_quota(key)
                self.send_json(200, {"subscription": {"name": key["plan_name"], "unlimited": key["unlimited"], "blocked": key["blocked"], "windows": key["windows"], "retry_at": key.get("retry_at")}, "concurrency": {"limit": key["concurrency_limit"], "current": key["current_concurrency"]}})
            elif parsed.path.endswith("/routing"):
                self.send_json(200, account_routing_view(index))
            elif parsed.path.endswith("/currency"):
                self.send_json(200, {"cny_per_usd": CNY_PER_USD})
            elif parsed.path.endswith("/prices"):
                self.send_json(200, model_prices(query, include_custom=False))
            elif parsed.path.endswith("/analysis"):
                self.send_json(200, analysis_view(query, LIVE_KEYS[index]["scope"]))
            elif parsed.path.endswith("/analysis/speed"):
                self.send_json(200, analysis_speed_view(query, LIVE_KEYS[index]["scope"]))
            elif parsed.path.endswith("/errors"):
                self.send_json(200, error_view(query, LIVE_KEYS[index]["scope"]))
            elif parsed.path.endswith("/ai-providers"):
                self.send_json(200, {"providers": configured_provider_views(query, index)})
            elif parsed.path.endswith("/auth-files"):
                self.send_json(200, {"files": auth_file_views(account_auth_files(index), query, LIVE_KEYS[index]["scope"])})
            elif parsed.path.endswith("/auth-files/quota"):
                allowed = {
                    item["auth_index"] for item in account_auth_files(index)
                }
                auth_index = query.get("auth_index", [""])[0]
                payload = auth_file_quota(query) if auth_index in allowed else None
                if payload is None:
                    self.send_json(404, {"error": {"message": "认证文件不存在或不支持限额查询"}})
                else:
                    self.send_json(200, payload)
            elif parsed.path.endswith("/usage/models"):
                allowed = {item["auth_index"] for item in account_auth_files(index)}
                provider_refs = {item["ref"] for item in configured_provider_views(query, index) if item["auth_index"]}
                if ("credential_ref" in query and query["credential_ref"][0] not in provider_refs or
                        "credential_ref" not in query and query.get("auth_index", [None])[0] not in allowed | {None}):
                    self.send_json(404, {"error": {"message": "Credential not found"}})
                else:
                    self.send_json(200, usage_models(query, LIVE_KEYS[index]["scope"]))
            elif parsed.path.endswith("/auth-files/history"):
                allowed = {item["auth_index"] for item in account_auth_files(index)}
                payload = auth_file_history(query, LIVE_KEYS[index]["scope"]) if query.get("auth_index", [""])[0] in allowed else None
                if payload is None:
                    self.send_json(404, {"error": {"message": "认证文件不存在"}})
                else:
                    self.send_json(200, payload)
            elif parsed.path.endswith("/auth-files/quota/reset"):
                if not self.allow_api_key_quota_reset:
                    self.send_json(403, {"error": {"message": "Quota resets are disabled for API key users"}})
                else:
                    self.reset_auth_quota(parsed, account_auth_files(index))
            elif parsed.path.endswith("/events"):
                self.send_json(200, request_event_view(query, LIVE_KEYS[index]["scope"]))
            return
        payload = payload_for(parsed.path, query)
        if payload is None:
            self.send_json(404, {"error": {"message": "dummy backend: route not found"}})
            return
        self.send_json(200, payload)

    def do_POST(self):
        self.handle_mutation()

    def reset_auth_quota(self, parsed, files):
        query = parse_qs(parsed.query, keep_blank_values=True)
        auth_index = query.get("auth_index", [""])[0]
        auth_file = next((item for item in files if item["auth_index"] == auth_index), None)
        quota = AUTH_FILE_QUOTAS.get(auth_index)
        if not re.fullmatch(r"[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}", self.headers.get("X-Quota-Reset-ID", "")):
            self.send_json(400, {"error": {"message": "Invalid quota reset request ID"}})
        elif auth_file is None or quota is None:
            self.send_json(404, {"error": {"message": "Auth file does not exist"}})
        elif auth_file["category"] not in {"codex", "claude"} or auth_file.get("disabled"):
            self.send_json(422, {"error": {"message": "This auth file cannot reset quotas"}})
        elif query.get("auth_revision") != [auth_file["cache_revision"]] or query.get("auth_name") != [auth_file["name"]]:
            self.send_json(409, {"error": {"message": "Auth file changed; refresh the auth file list and try again"}})
        elif quota.get("rate_limit_reset_credits_available_count", 0) <= 0:
            self.send_json(502, {"error": {"message": "No reset credits available"}})
        else:
            quota["rate_limit_reset_credits_available_count"] -= 1
            if quota.get("rate_limit_reset_credits"):
                quota["rate_limit_reset_credits"].pop(0)
            for row in quota["quota"]:
                row["remaining_percent"] = 100
                if "usage" in row:
                    row.update(quota_usage(0, 0))
            self.send_json(200, {"reset": True, "cooldown_cleared": True})

    def do_PATCH(self):
        self.handle_mutation()

    def do_PUT(self):
        self.handle_mutation()

    def do_DELETE(self):
        self.handle_mutation()

    def handle_mutation(self):
        parsed = urlparse(self.path)
        self.mutation_view = None
        length = int(self.headers.get("Content-Length", "0"))
        request_body = b""
        if length:
            request_body = self.rfile.read(length)
        if parse_qs(parsed.query).get("view") == ["1"]:
            self.mutation_view = json.loads(request_body or b"{}")
            request_body = json.dumps(self.mutation_view.get("data") or {}).encode()
        route = self.command, parsed.path
        if route == ("POST", f"{API_BASE}/auth-files/quota/reset"):
            self.reset_auth_quota(parsed, AUTH_FILES)
        elif route == ("POST", "/v0/management/api-call"):
            body = json.loads(request_body or b"{}")
            credential = next((item for item in ai_provider_credentials() if item["auth-index"] == body.get("auth_index")), None)
            if credential is None:
                self.send_json(400, {"error": "auth credential not found for auth_index"})
                return
            if body.get("header", {}).get("Authorization") != "Bearer $TOKEN$":
                self.send_json(400, {"error": "dummy backend: balance queries must use the credential token"})
                return
            status, upstream = AI_BALANCE_RESPONSES.get(body.get("url"), (404, {"error": {"message": "Not Found"}}))
            self.send_json(200, {"status_code": status, "header": {"Content-Type": ["application/json"]}, "body": json.dumps(upstream)})
        elif self.command == "PATCH" and parsed.path.removeprefix("/v0/management/") in AI_PROVIDER_LISTS:
            field = parsed.path.removeprefix("/v0/management/")
            body = json.loads(request_body or b"{}")
            entries = AI_PROVIDER_LISTS[field]
            index = body.get("index")
            value = body.get("value")
            if not isinstance(index, int) or not 0 <= index < len(entries) or not isinstance(value, dict):
                self.send_json(400, {"error": "invalid provider patch"})
                return
            allowed = {"disabled", "priority", "api-key-entries"} if field == "openai-compatibility" else {"excluded-models", "priority", "weight"}
            if not value or not set(value) <= allowed:
                self.send_json(400, {"error": "dummy backend: unsupported provider patch"})
                return
            if "api-key-entries" in value:
                previous = {(item.get("api-key"), item.get("proxy-url")): item.get("auth-index")
                            for item in entries[index].get("api-key-entries", [])}
                for item in value["api-key-entries"]:
                    auth_index = previous.get((item.get("api-key"), item.get("proxy-url")))
                    if auth_index:
                        item["auth-index"] = auth_index
            entries[index].update(value)
            self.send_json(200, {"status": "ok"})
        elif route == ("PATCH", "/v0/management/auth-files/fields"):
            body = json.loads(request_body or b"{}")
            item = next((item for item in AUTH_FILES if item["name"] == body.get("name")), None)
            if item is None:
                self.send_json(404, {"error": "auth file not found"})
                return
            item.update({field: body[field] for field in ("note", "priority", "weight") if field in body})
            self.send_json(200, {"status": "ok"})
        elif route == ("PATCH", "/v0/management/auth-files/status"):
            body = json.loads(request_body or b"{}")
            item = next((item for item in AUTH_FILES if item["name"] == body.get("name")), None)
            if item is None:
                self.send_json(404, {"error": "auth file not found"})
                return
            item["disabled"] = bool(body.get("disabled"))
            self.send_json(200, {"status": "ok", "disabled": item["disabled"]})
        elif route == ("DELETE", f"{API_BASE}/plugin-logs"):
            cleared = len(PLUGIN_LOGS)
            PLUGIN_LOGS.clear()
            self.send_json(200, {"cleared": cleared})
        elif route == ("POST", f"{API_BASE}/prices/reference/refresh"):
            self.send_json(200, {"metadata": price_status()["metadata"], "changed": False})
        elif route == ("PUT", f"{API_BASE}/prices"):
            body = json.loads(request_body or b"{}")
            row = next((price for price in PRICES if price["model_id"] == body.get("model_id")), None)
            if row is None:
                row = {"in_models": False}
                PRICES.append(row)
            row.update(body)
            row["source"] = "custom"
            self.send_json(200, {"price": row})
        elif route == ("DELETE", f"{API_BASE}/prices"):
            model = parse_qs(parsed.query).get("model_id", [""])[0]
            row = next((price for price in PRICES if price["model_id"] == model), None)
            if row is None or row["source"] != "custom":
                self.send_json(404, {"error": {"message": "自定义价不存在"}})
                return
            if not row["in_models"]:
                PRICES.remove(row)
            else:
                row.update(REFERENCE_PRICES.get(model) or {
                    "source": "none", "input_per_1m": 0, "output_per_1m": 0,
                    "cache_read_per_1m": None, "cache_write_per_1m": None,
                    "long_context": None,
                })
            self.send_json(200, {"deleted": model})
        elif route == ("POST", f"{API_BASE}/keys/reset"):
            body = json.loads(request_body or b"{}")
            targets = [key for key in KEYS if not key.get("deleted_at") and key["plan_id"]
                       and (body.get("mode") == "global" or key["scope"] in body.get("scopes", []))]
            counts = {"keys": 0, "windows": 0}
            for key in targets:
                count = len(QUOTA_CYCLES.get(key["scope"], {}))
                counts["keys"] += bool(count)
                counts["windows"] += count
                QUOTA_CYCLES.pop(key["scope"], None)
                refresh_key_quota(key)
            self.send_json(200, counts)
        elif route == ("POST", f"{API_BASE}/keys/concurrency"):
            body = json.loads(request_body or b"{}")
            for key in KEYS:
                if key["scope"] == body.get("scope"):
                    key["concurrency_limit"] = body.get("concurrency_limit", 0)
                    break
            self.send_json(200, {"ok": True})
        elif route == ("POST", f"{API_BASE}/keys/sync"):
            self.send_json(200, {"added": 0, "deleted": 0})
        elif route == ("POST", f"{API_BASE}/credentials/sync"):
            body = json.loads(request_body or b"{}")
            CREDENTIALS[:] = [
                item for item in CREDENTIALS
                if item["ref"] not in SYNCED_CREDENTIAL_REFS
            ]
            SYNCED_CREDENTIAL_REFS.clear()
            balances = {item["auth_index"]: item["balance"] for item in CONFIG_CREDENTIALS.values() if item["balance"] is not None}
            notes = {item.get("config_ref", ref): item.get("note", "") for ref, item in CONFIG_CREDENTIALS.items()}
            CONFIG_CREDENTIALS.clear()
            for item in body.get("credentials", []):
                ref = item["ref"]
                SYNCED_CREDENTIAL_REFS.add(ref)
                CONFIG_CREDENTIALS[ref] = {**item, "balance": balances.get(item["auth_index"]), "note": notes.get(item.get("config_ref", ref), "")}
                name = {"display_name": item["key_preview"]} if item["key_preview"] else {
                    "display_name": "No API key configured", "display_name_message": {"message_key": "backend.no_api_key_configured"}}
                CREDENTIALS.append({
                    "ref": ref,
                    "source": "ai-providers",
                    "provider": item["provider"],
                    "provider_name": item["name"] or item["provider"].removeprefix("openai-compatible-"),
                    **name,
                    "status": "disabled" if item.get("disabled") else "active",
                    "disabled": bool(item.get("disabled")),
                    "unavailable": False,
                })
            self.send_json(200, {"credentials": CREDENTIALS})
        elif route == ("POST", f"{API_BASE}/ai-providers/note"):
            body = json.loads(request_body or b"{}")
            item = CONFIG_CREDENTIALS.get(body.get("ref"))
            if item is None:
                self.send_json(404, {"error": "Credential not found"})
                return
            item["note"] = body.get("note", "").strip()
            self.send_json(200, {})
        elif route == ("POST", f"{API_BASE}/ai-providers/balance"):
            body = json.loads(request_body or b"{}")
            credentials = [item for item in CONFIG_CREDENTIALS.values() if item["auth_index"] and item["auth_index"] == body["auth_index"]]
            if not credentials:
                self.send_json(404, {"error": {"message": "Credential not found"}})
            else:
                for credential in credentials:
                    credential["balance"] = body["balance"]
                self.send_json(200, body["balance"])
        elif route == ("DELETE", f"{API_BASE}/routes"):
            route_id = parse_qs(parsed.query).get("id", [""])[0]
            affected = 0
            unrestricted = 0
            deleted = 0
            ROUTES[:] = [item for item in ROUTES if item["id"] != route_id]
            for key in KEYS:
                route_ids = key["route_bindings"]["route_ids"]
                if route_id in route_ids:
                    key["route_bindings"]["route_ids"] = [item for item in route_ids if item != route_id]
                    affected += 1
                    deleted += bool(key.get("deleted_at"))
                    unrestricted += not key.get("deleted_at") and not any(key["route_bindings"].values())
            self.send_json(200, {"deleted": route_id, "affected_keys": affected, "deleted_keys": deleted, "fully_unrestricted_keys": unrestricted})
        elif route == ("POST", f"{API_BASE}/routes"):
            body = json.loads(request_body or b"{}")
            route_id = f"route-dummy-{len(ROUTES)}"
            scopes = set(body.get("scopes", []))
            stored = {"id": route_id, "name": body.get("name", "新路由"), "rule": body.get("rule", {})}
            ROUTES.append(stored)
            for key in KEYS:
                if key["scope"] in scopes:
                    key["route_bindings"]["route_ids"].append(route_id)
            self.send_json(201, {"route": stored})
        elif route == ("PATCH", f"{API_BASE}/routes"):
            body = json.loads(request_body or b"{}")
            route_id = body.get("id", "")
            stored = next((item for item in ROUTES if item["id"] == route_id), None)
            if stored is None:
                self.send_json(404, {"error": {"message": "dummy backend: route not found"}})
                return
            stored.update({key: body[key] for key in ("name", "rule") if key in body})
            if "scopes" in body:
                scopes = set(body["scopes"])
                for key in KEYS:
                    route_ids = [item for item in key["route_bindings"]["route_ids"] if item != route_id]
                    if key["scope"] in scopes:
                        route_ids.append(route_id)
                    key["route_bindings"]["route_ids"] = route_ids
            refresh_route_counts()
            self.send_json(200, {"route": stored})
        elif route in {("POST", f"{API_BASE}/plans"), ("PATCH", f"{API_BASE}/plans")}:
            body = json.loads(request_body or b"{}")
            plan_id = body.get("id") or "plan-" + str(time.time_ns())
            stored = next((item for item in PLANS if item["id"] == plan_id), None)
            if self.command == "POST":
                stored = {"id": plan_id}
                PLANS.append(stored)
            if stored is None:
                self.send_json(404, {"error": {"message": "dummy backend: plan not found"}})
                return
            stored.update({key: body[key] for key in ("name", "currency", "windows") if key in body})
            for index, window in enumerate(stored["windows"]):
                window.setdefault("id", str(time.time_ns()) + "-" + str(index))
            stored["windows"].sort(key=lambda window: window["period_seconds"])
            if "scopes" in body:
                scopes = set(body["scopes"])
                for key in KEYS:
                    if key["scope"] in scopes and key["plan_id"] != plan_id:
                        key["plan_id"] = plan_id
                        QUOTA_CYCLES.pop(key["scope"], None)
                    elif key["scope"] not in scopes and key["plan_id"] == plan_id:
                        key["plan_id"] = ""
                        QUOTA_CYCLES.pop(key["scope"], None)
            for key in KEYS:
                refresh_key_quota(key)
            self.send_json(201 if self.command == "POST" else 200, {"plan": stored})
        elif route == ("PUT", f"{API_BASE}/keys/routes"):
            body = json.loads(request_body or b"{}")
            bindings = body.get("bindings", {})
            for key in KEYS:
                if key["scope"] == body.get("scope"):
                    key["route_bindings"] = {
                        "route_ids": bindings.get("route_ids", []),
                        "models": bindings.get("models", []),
                        "credential_ids": bindings.get("credential_ids", []),
                        "credential_providers": bindings.get("credential_providers", []),
                        "denied_models": bindings.get("denied_models", []),
                        "denied_credential_ids": bindings.get("denied_credential_ids", []),
                        "denied_credential_providers": bindings.get("denied_credential_providers", []),
                    }
                    break
            refresh_route_counts()
            self.send_json(200, {"ok": True})
        elif route == ("DELETE", f"{API_BASE}/plans"):
            plan_id = parse_qs(parsed.query).get("id", [""])[0]
            PLANS[:] = [plan for plan in PLANS if plan["id"] != plan_id]
            for key in KEYS:
                if key["plan_id"] == plan_id:
                    key["plan_id"] = ""
                    refresh_key_quota(key)
            self.send_json(200, {"deleted": plan_id})
        elif route in {("POST", f"{API_BASE}/keys/bind"), ("POST", f"{API_BASE}/keys/unbind")}:
            body = json.loads(request_body or b"{}")
            for key in KEYS:
                if key["scope"] == body.get("scope"):
                    plan_id = body.get("plan_id", "")
                    if key["plan_id"] != plan_id:
                        key["plan_id"] = plan_id
                        QUOTA_CYCLES.pop(key["scope"], None)
                        refresh_key_quota(key)
            self.send_json(200, {"ok": True})
        elif route == ("POST", f"{API_BASE}/keys/label"):
            body = json.loads(request_body or b"{}")
            for key in KEYS:
                if key["scope"] == body.get("scope"):
                    key["label"] = body.get("label", "").strip()
                    break
            self.send_json(200, {"ok": True})
        else:
            self.send_json(404, {"error": {"message": "dummy backend: route not found"}})

    def log_message(self, message, *args):
        print(f"{self.address_string()} - {message % args}")


def main():
    parser = argparse.ArgumentParser(description="Serve the billing UI with deterministic dummy data.")
    parser.add_argument("--port", type=int, default=8765)
    parser.add_argument(
        "--host",
        choices=("standalone", "cpamc", "cpamp"),
        default="standalone",
        help="Wrap the plugin UI in a lightweight CPAMC or CPAMP host shell.",
    )
    parser.add_argument(
        "--theme",
        choices=("auto", "light", "white", "dark"),
        default="auto",
        help="Initial host theme; the preview shell can switch themes after startup.",
    )
    parser.add_argument(
        "--allow-api-key-quota-reset",
        action="store_true",
        help="Allow API key users to reset Codex auth file quotas.",
    )
    args = parser.parse_args()
    seed_paginated_history()
    Handler.host_mode = args.host
    Handler.initial_theme = args.theme
    Handler.allow_api_key_quota_reset = args.allow_api_key_quota_reset
    server = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    entry_path = "/ui" if args.host == "standalone" else "/"
    print(
        f"Frontend dummy backend ({args.host}): http://127.0.0.1:{server.server_port}{entry_path}",
        flush=True,
    )
    if args.host != "standalone":
        print(f"Direct plugin document: http://127.0.0.1:{server.server_port}/ui", flush=True)
    print(f"API Key account page: http://127.0.0.1:{server.server_port}/ui#account", flush=True)
    print("Data is reset on every restart. Press Ctrl-C to stop.", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
