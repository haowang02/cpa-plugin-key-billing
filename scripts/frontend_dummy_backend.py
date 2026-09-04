#!/usr/bin/env python3

import argparse
import hashlib
import json
from datetime import datetime, timedelta, timezone
from zoneinfo import ZoneInfo
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlparse


ROOT = Path(__file__).resolve().parents[1]
UI_PATH = ROOT / "internal" / "plugin" / "ui.html"
API_BASE = "/v0/management/plugins/cpa-key-billing"
RESOURCE_BASE = "/v0/resource/plugins/cpa-key-billing"
NOW = datetime.now(timezone.utc).replace(minute=0, second=0, microsecond=0)
CALLER_SCOPE_SALT = b"cli-proxy-api:caller-scope:v1\0"


HOST_SHELL = r"""<!doctype html>
<html lang="zh-CN" data-host="__HOST_MODE__">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>__HOST_LABEL__ · API Key 计费样式预览</title>
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
  <div class="nav-placeholder"><span>仪表盘</span><span>AI 提供商</span><span>插件管理</span><span class="active">API Key 计费</span></div>
</aside>
<header class="navbar">
  <div class="navbar-left"><button class="mobile-menu" title="菜单">☰</button><span>API Key 计费</span></div>
  <div class="theme-controls" aria-label="预览主题">
    <button type="button" data-action="refresh" title="刷新">↻</button>
    <button type="button" data-theme-choice="light" title="浅色主题">◐</button>
    <button type="button" class="theme-white" data-theme-choice="white" title="白色主题">○</button>
    <button type="button" data-theme-choice="dark" title="深色主题">●</button>
  </div>
</header>
<main class="content"><iframe id="plugin-frame" src="/ui" title="API Key 计费插件"></iframe></main>
<script>
"use strict";
const HOST_MODE="__HOST_MODE__";
const INITIAL_THEME="__INITIAL_THEME__";
const root=document.documentElement;
const frame=document.getElementById("plugin-frame");
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


PLANS = [
    {
        "id": "engineering-monthly",
        "name": "研发团队",
        "amount_usd": 300,
        "period_seconds": 2592000,
    },
    {
        "id": "production-monthly",
        "name": "生产服务",
        "amount_usd": 1000,
        "period_seconds": 2592000,
    },
    {
        "id": "project-credit",
        "name": "项目额度",
        "amount_usd": 100,
        "period_seconds": 0,
    },
]


CREDENTIALS = [
    {"ref": "sha256:" + "a" * 64, "source": "auth-files", "provider": "codex", "display_name": "dev-team@example.com", "status": "active", "disabled": False, "unavailable": False},
    {"ref": "sha256:" + "b" * 64, "source": "auth-files", "provider": "claude", "display_name": "platform@example.com", "status": "active", "disabled": False, "unavailable": False},
    {"ref": "sha256:" + "c" * 64, "source": "ai-providers", "provider": "codex", "display_name": "sk-proxy…7f3a", "status": "active", "disabled": False, "unavailable": False},
    {"ref": "sha256:" + "d" * 64, "source": "ai-providers", "provider": "deepseek", "display_name": "sk-live…91b2", "status": "disabled", "disabled": True, "unavailable": False},
    {"ref": "sha256:" + "e" * 64, "source": "auth-files", "provider": "xai", "display_name": "disabled@example.com", "status": "disabled", "disabled": True, "unavailable": False},
]
SYNCED_CREDENTIAL_REFS = set()

ROUTES = [
    {"id": "coding", "name": "代码开发", "rule": {"models": ["gpt-5.6-sol", "gpt-5.5", "codex/deepseek-v4-flash-vision-exp"], "credential_ids": [], "credential_providers": [{"source": "auth-files", "provider": "codex"}]}},
    {"id": "analytics", "name": "数据分析", "rule": {"models": ["claude/deepseek-v4-pro", "claude/deepseek-v4-flash"], "credential_ids": ["sha256:" + "b" * 64], "credential_providers": []}},
    {"id": "economy", "name": "轻量任务", "rule": {"models": ["gpt-5.6-luna"], "credential_ids": [], "credential_providers": [{"source": "auth-files", "provider": "codex"}]}},
]


AUTH_FILES = [
    {
        "auth_index": "auth-demo-codex-plus",
        "name": "codex-dev-team@example.com.json",
        "category": "codex",
        "email": "dev-team@example.com",
        "disabled": False,
        "unavailable": False,
        "quota_supported": True,
    },
    {
        "auth_index": "auth-demo-claude",
        "name": "claude-platform@example.com.json",
        "category": "claude",
        "email": "platform@example.com",
        "disabled": False,
        "unavailable": False,
        "quota_supported": True,
    },
    {
        "auth_index": "auth-demo-codex-pro",
        "name": "codex-automation@example.com.json",
        "category": "codex",
        "email": "automation@example.com",
        "disabled": False,
        "unavailable": False,
        "quota_supported": True,
    },
    {
        "auth_index": "auth-demo-antigravity",
        "name": "antigravity-ai-lab@example.com.json",
        "category": "antigravity",
        "email": "ai-lab@example.com",
        "disabled": False,
        "unavailable": False,
        "quota_supported": True,
    },
    {
        "auth_index": "auth-demo-kimi",
        "name": "kimi-research@example.com.json",
        "category": "kimi",
        "email": "research@example.com",
        "disabled": False,
        "unavailable": True,
        "quota_supported": True,
    },
    {
        "auth_index": "auth-demo-xai-active",
        "name": "xai-research@example.com.json",
        "category": "xai",
        "email": "research@example.com",
        "disabled": False,
        "unavailable": False,
        "quota_supported": True,
    },
]

AUTH_FILE_CREDENTIAL_REFS = {
    "auth-demo-codex-plus": "sha256:" + "a" * 64,
    "auth-demo-claude": "sha256:" + "b" * 64,
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


def quota_row(label, remaining_percent, reset_seconds, **extra):
    return {
        "label": label,
        "remaining_percent": remaining_percent,
        "reset_at": iso(NOW + timedelta(seconds=reset_seconds)),
        **extra,
    }


AUTH_FILE_QUOTAS = {
    "auth-demo-codex-pro": {
        "plan": "pro-20x",
        "rate_limit_reset_credits_available_count": 1,
        "quota": [
            quota_row("周限额", 62, 432000),
            quota_row(
                "GPT-5.3-Codex-Spark 5 小时限额",
                100,
                18000,
            ),
            quota_row(
                "GPT-5.3-Codex-Spark 周限额",
                100,
                604800,
            ),
        ],
    },
    "auth-demo-codex-plus": {
        "plan": "plus",
        "rate_limit_reset_credits_available_count": 1,
        "quota": [
            quota_row("5 小时限额", 35, 14400),
            quota_row("周限额", 90, 518400),
        ],
    },
    "auth-demo-claude": {
        "plan": "Team",
        "quota": [
            quota_row("5 小时限额", 76, 12600),
            quota_row("周限额", 59, 388800),
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
            quota_row(
                "5 小时限额",
                82,
                64800,
                group_label="Gemini Models",
            ),
            quota_row(
                "周限额",
                93,
                64800,
                group_label="Gemini Models",
            ),
        ],
    },
    "auth-demo-kimi": {
        "quota": [
            quota_row("5 小时限额", 48, 7200),
            quota_row("周限额", 69, 345600),
        ],
    },
    "auth-demo-xai-active": {
        "quota": [
            quota_row("周限额", 78, 410400),
            {
                "label": "月度额度",
                "used": 8.5,
                "limit": 50,
                "remaining_percent": 83,
                "currency": "USD",
                "reset_at": iso(NOW + timedelta(seconds=1814400)),
            },
        ],
    },
}


def auth_file_quota(query):
    auth_index = query.get("auth_index", [""])[0]
    quota = AUTH_FILE_QUOTAS.get(auth_index)
    if quota is None:
        return None
    auth_file = next(item for item in AUTH_FILES if item["auth_index"] == auth_index)
    return {
        "auth_revision": auth_file["cache_revision"],
        "fetched_at": iso(NOW),
        **quota,
    }


KEY_PROFILES = [
    {"label": "代码审查机器人", "plan_id": "engineering-monthly", "spent_usd": 128.64, "concurrency_limit": 5, "current_concurrency": 2, "cycle_days": 18, "route_bindings": {"route_ids": ["coding"], "models": [], "credential_ids": [], "credential_providers": []}},
    {"label": "CI 构建服务", "plan_id": "engineering-monthly", "spent_usd": 84.27, "concurrency_limit": 10, "current_concurrency": 3, "cycle_days": 24, "route_bindings": {"route_ids": ["coding"], "models": [], "credential_ids": [], "credential_providers": []}},
    {"label": "数据分析平台", "plan_id": "production-monthly", "spent_usd": 368.91, "concurrency_limit": 5, "current_concurrency": 1, "cycle_days": 12, "route_bindings": {"route_ids": ["analytics"], "models": [], "credential_ids": [], "credential_providers": []}},
    {"label": "客服助手", "plan_id": "production-monthly", "spent_usd": 241.36, "concurrency_limit": 8, "current_concurrency": 2, "cycle_days": 7, "route_bindings": {"route_ids": ["analytics"], "models": ["gpt-5.5"], "credential_ids": [], "credential_providers": []}},
    {"label": "文档生成", "plan_id": "engineering-monthly", "spent_usd": 56.48, "concurrency_limit": 3, "current_concurrency": 0, "cycle_days": 21, "route_bindings": {"route_ids": ["coding"], "models": [], "credential_ids": [], "credential_providers": []}},
    {"label": "预发布环境", "plan_id": "project-credit", "spent_usd": 43.72, "concurrency_limit": 2, "current_concurrency": 1, "route_bindings": {"route_ids": ["economy"], "models": [], "credential_ids": [], "credential_providers": []}},
    {"label": "内部工具", "plan_id": "", "spent_usd": 0, "concurrency_limit": 0, "current_concurrency": 1, "route_bindings": {"route_ids": [], "models": [], "credential_ids": [], "credential_providers": []}},
    {"label": "临时测试", "plan_id": "project-credit", "spent_usd": 87.19, "concurrency_limit": 1, "current_concurrency": 0, "route_bindings": {"route_ids": [], "models": ["gpt-5.5"], "credential_ids": ["sha256:" + "c" * 64], "credential_providers": []}},
]


def make_key(index):
    profile = KEY_PROFILES[index - 1]
    plan = next((item for item in PLANS if item["id"] == profile["plan_id"]), None)
    limit = plan["amount_usd"] if plan else 0
    result = {
        "scope": hashlib.sha256(CALLER_SCOPE_SALT + f"sk-demo-{index:04d}".encode()).hexdigest(),
        "preview": f"sk-demo…{index:04d}",
        "label": profile["label"],
        "in_config": True,
        "plan_id": profile["plan_id"],
        "plan_name": plan["name"] if plan else "",
        "concurrency_limit": profile["concurrency_limit"],
        "current_concurrency": profile["current_concurrency"],
        "route_bindings": profile["route_bindings"],
        "unlimited": plan is None,
        "blocked": False,
        "limit_usd": limit,
        "spent_usd": profile["spent_usd"],
        "used_percent": profile["spent_usd"] / limit * 100 if limit else 0,
    }
    if plan and plan["period_seconds"] > 0:
        result["cycle_end_at"] = iso(NOW + timedelta(days=profile["cycle_days"]))
    return result


KEYS = [make_key(index) for index in range(1, len(KEY_PROFILES) + 1)]
LIVE_KEYS = KEYS

PRICES = [
    {
        "pattern": "gpt-5.6-sol",
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
        "pattern": "gpt-5.5",
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
        "pattern": "gpt-5.6-luna",
        "input_per_1m": 0.2,
        "output_per_1m": 1.2,
        "cache_read_per_1m": 0.02,
        "source": "custom",
    },
    {
        "pattern": "gpt-5.6-terra",
        "input_per_1m": 2,
        "output_per_1m": 12,
        "cache_read_per_1m": 0.2,
        "source": "custom",
    },
    {
        "pattern": "gpt-image-2",
        "input_per_1m": 5,
        "output_per_1m": 30,
        "cache_read_per_1m": 1.25,
        "source": "custom",
    },
    {
        "pattern": "claude/deepseek-v4-pro",
        "input_per_1m": 0.435,
        "output_per_1m": 0.87,
        "cache_read_per_1m": 0.003625,
        "source": "custom",
    },
    {
        "pattern": "claude/deepseek-v4-flash",
        "input_per_1m": 0.28,
        "output_per_1m": 0.42,
        "source": "custom",
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
    long_context=False,
    failed=False,
):
    uncached, cache_read, cache_write, output = tokens
    if failed:
        uncached = cache_read = cache_write = output = 0
    return {
        "key_index": key_index,
        "source": source,
        "provider": provider,
        "executor_type": executor,
        "reasoning_effort": effort,
        "service_tier": tier,
        "upstream_model": model,
        "billing_model": billing_model or model,
        "failed": failed,
        "latency_ms": latency_ms,
        "ttft_ms": ttft_ms,
        "accounting_quality": "" if failed else "complete",
        "price_source": "override",
        "cost": make_cost(
            uncached,
            cache_read,
            cache_write,
            output,
            rates,
            model.startswith("gpt-5."),
            long_context,
        ),
        "reasoning_tokens": 0 if failed else reasoning_tokens,
    }


# Numeric usage and timing values are sampled from a real export. All identities
# below are synthetic and intentionally unrelated to the source records.
SUCCESS_EVENT_SAMPLES = [
    event_sample(0, "codex · dev-team@example.com", "codex", "gpt-5.6-sol", "CodexExecutor", "high", "auto", 11513, 8209, 266, (712, 91648, 4096, 425), (4, 0.4, 5, 20)),
    event_sample(5, "codex · dev-team@example.com", "codex", "gpt-5.6-luna", "CodexWebsocketsExecutor", "low", "auto", 2516, 1431, 10, (1030, 49920, 0, 75), (0.2, 0.02, 0.25, 1.2)),
    event_sample(1, "codex · dev-team@example.com", "codex", "gpt-5.5", "CodexWebsocketsExecutor", "medium", "auto", 2417, 1103, 0, (1194, 95616, 0, 73), (5, 0.5, 5, 30)),
    event_sample(1, "codex · dev-team@example.com", "codex", "gpt-5.6-sol", "CodexWebsocketsExecutor", "high", "auto", 5306, 2121, 21, (798, 169984, 0, 201), (4, 0.4, 5, 20)),
    event_sample(2, "claude · platform@example.com", "claude", "deepseek-v4-pro", "ClaudeExecutor", "high", "auto", 10061, 637, 0, (38049, 0, 0, 474), (0.435, 0.003625, 0.435, 0.87), billing_model="claude/deepseek-v4-pro"),
    event_sample(7, "codex · sk-proxy…7f3a", "codex", "gpt-5.5", "CodexWebsocketsExecutor", "high", "auto", 7288, 4130, 188, (8502, 45440, 0, 309), (5, 0.5, 5, 30)),
    event_sample(6, "codex · dev-team@example.com", "codex", "gpt-5.6-terra", "CodexWebsocketsExecutor", "medium", "auto", 10120, 3379, 81, (1300, 69376, 0, 477), (2, 0.2, 2.5, 12)),
    event_sample(4, "codex · dev-team@example.com", "codex", "gpt-5.6-sol", "CodexWebsocketsExecutor", "high", "auto", 2609, 1881, 6, (1368, 237824, 0, 46), (4, 0.4, 5, 20)),
    event_sample(5, "codex · dev-team@example.com", "codex", "gpt-5.6-luna", "CodexWebsocketsExecutor", "low", "auto", 5002, 2464, 61, (1261, 149248, 0, 147), (0.2, 0.02, 0.25, 1.2)),
    event_sample(7, "codex · sk-proxy…7f3a", "codex", "gpt-5.5", "CodexWebsocketsExecutor", "high", "auto", 7634, 4412, 94, (24196, 19840, 0, 275), (5, 0.5, 5, 30)),
    event_sample(6, "codex · dev-team@example.com", "codex", "gpt-image-2", "CodexExecutor", "", "auto", 43679, 43476, 0, (1658, 0, 0, 915), (5, 1.25, 5, 30)),
    event_sample(3, "claude · platform@example.com", "claude", "deepseek-v4-pro", "ClaudeExecutor", "high", "auto", 16431, 909, 0, (66079, 32768, 0, 535), (0.435, 0.003625, 0.435, 0.87), billing_model="claude/deepseek-v4-pro"),
    event_sample(0, "codex · dev-team@example.com", "codex", "gpt-5.6-sol", "CodexExecutor", "high", "auto", 19542, 17836, 681, (835, 282752, 0, 802), (8, 0.8, 10, 30), long_context=True),
    event_sample(6, "codex · dev-team@example.com", "codex", "gpt-5.6-luna", "CodexWebsocketsExecutor", "low", "auto", 5074, 3149, 49, (28615, 31488, 0, 124), (0.2, 0.02, 0.25, 1.2)),
]

FAILURE_EVENT_SAMPLES = [
    event_sample(1, "codex · dev-team@example.com", "codex", "gpt-5.5", "CodexWebsocketsExecutor", "high", "auto", 4338, 237, 0, (0, 0, 0, 0), (5, 0.5, 5, 30), failed=True),
]

EVENT_SAMPLES = SUCCESS_EVENT_SAMPLES * 2 + FAILURE_EVENT_SAMPLES


def make_request_events():
    entries = []
    for index, sample in enumerate(EVENT_SAMPLES):
        key = LIVE_KEYS[sample["key_index"]]
        entries.append({
            "at": iso(NOW - timedelta(hours=index * 22, minutes=(index % 4) * 11)),
            "scope": key["scope"],
            "preview": key["preview"],
            "label": key["label"],
            **{name: value for name, value in sample.items() if name != "key_index"},
        })
    return entries


REQUEST_EVENTS = make_request_events()

def request_event_view(query, scope=""):
    selected_key = "" if scope else query.get("api_key", [""])[0]
    selected_model = query.get("model", [""])[0]
    selected_source = query.get("source", [""])[0]
    selected_provider = query.get("provider", [""])[0]
    selected_executor = query.get("executor", [""])[0]
    selected_failed = query.get("failed", [""])[0]
    offset = max(0, int(query.get("offset", ["0"])[0] or 0))
    limit = max(0, int(query.get("limit", ["0"])[0] or 0))
    time_matched = filter_event_time([entry for entry in REQUEST_EVENTS if not scope or entry["scope"] == scope], query)
    filter_options = {
        "models": sorted({entry.get("billing_model") or entry.get("upstream_model", "")
                          for entry in time_matched} - {""}, key=str.lower),
        "sources": sorted({entry.get("source", "") for entry in time_matched} - {""}, key=str.lower),
        "providers": sorted({entry.get("provider", "") for entry in time_matched} - {""}, key=str.lower),
        "executors": sorted({entry.get("executor_type", "") for entry in time_matched} - {""}, key=str.lower),
    }
    counts = {"all": 0, "normal": 0, "failed": 0}
    matched = []
    for entry in time_matched:
        if selected_key and entry.get("scope") != selected_key:
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
    result = {"entries": page, "total": len(matched), "offset": offset, "status_counts": counts}
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
    key = LIVE_KEYS[index]
    bindings = key["route_bindings"]
    models = set(bindings["models"])
    credential_refs = set(bindings["credential_ids"])
    credential_providers = {
        (item["source"], item["provider"])
        for item in bindings["credential_providers"]
    }
    for route_id in bindings["route_ids"]:
        route = next((item for item in ROUTES if item["id"] == route_id), None)
        if route:
            models.update(route["rule"]["models"])
            credential_refs.update(route["rule"]["credential_ids"])
            credential_providers.update(
                (item["source"], item["provider"])
                for item in route["rule"]["credential_providers"]
            )

    return models, credential_refs, credential_providers


def account_access(index):
    key = LIVE_KEYS[index]
    models, credential_refs, credential_providers = account_routing(index)

    credentials = [
        {
            "source": credential["source"],
            "provider": credential["provider"],
            "name": credential["display_name"],
            "status": credential["status"],
        }
        for credential in CREDENTIALS
        if credential["ref"] in credential_refs
        or (credential["source"], credential["provider"]) in credential_providers
    ]
    return {
        "tracked": True,
        "identity": {"preview": key["preview"], "label": key["label"]},
        "subscription": {
            "name": key["plan_name"], "unlimited": key["unlimited"], "blocked": key["blocked"],
            "limit_usd": key["limit_usd"], "spent_usd": key["spent_usd"],
            "remaining_usd": max(0, key["limit_usd"] - key["spent_usd"]),
            "used_percent": key["used_percent"], "cycle_end_at": key.get("cycle_end_at"),
        },
        "concurrency": {"limit": key["concurrency_limit"], "current": key["current_concurrency"]},
        "models": sorted(models),
        "credentials": credentials,
        "routing_valid": True,
        "warnings": [],
    }


def account_auth_files(index):
    _, credential_refs, credential_providers = account_routing(index)
    if not credential_refs and not credential_providers:
        return AUTH_FILES
    return [
        auth_file
        for auth_file in AUTH_FILES
        if AUTH_FILE_CREDENTIAL_REFS.get(auth_file["auth_index"]) in credential_refs
        or ("auth-files", auth_file["category"]) in credential_providers
    ]


def refresh_route_counts():
    for route in ROUTES:
        bound = [key for key in KEYS if route["id"] in key["route_bindings"]["route_ids"]]
        route["bound_key_count"] = len(bound)
        route["fully_unrestricted_keys"] = sum(
            len(key["route_bindings"]["route_ids"]) == 1
            and not key["route_bindings"]["models"]
            and not key["route_bindings"]["credential_ids"]
            and not key["route_bindings"]["credential_providers"]
            for key in bound
        )


def request_error(event_index, message, status=0, error_type="", code=""):
    event = REQUEST_EVENTS[event_index]
    event["failed"] = True
    error = {"message": message}
    if error_type:
        error["type"] = error_type
    if code:
        error["code"] = code
    if 400 <= status <= 599:
        error["status"] = status
    reason = (f"HTTP {status}：" if status else "") + message
    if error_type:
        reason += f"（{error_type}）"
    return {
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
        "error_type": error_type,
        "reason": reason,
        "body": json.dumps({"error": error}, ensure_ascii=False, separators=(",", ":")),
    }


ERRORS = [
    request_error(
        28,
        "upstream request timed out",
        status=504,
        error_type="timeout_error",
        code="upstream_timeout",
    ),
]

PLUGIN_LOGS = [
    {
        "id": 3,
        "at": iso(NOW - timedelta(minutes=2)),
        "level": "debug",
        "message": "route " + json.dumps({"key": "代码审查机器人 · sk-demo…0001", "model": "gpt-5.6-sol", "model_policy": "restricted", "model_result": "allow", "credential_policy": "restricted", "credential_result": "selected", "selected_credential": "codex · dev-team@example.com", "outcome": "succeeded", "status": 200}, ensure_ascii=False, separators=(",", ":")),
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


def error_view(query, scope=""):
    rows = filter_event_time([entry for entry in ERRORS if not scope or entry["scope"] == scope], query)
    rows.sort(key=lambda entry: entry["at"], reverse=True)
    selected = {
        "api_key": "" if scope else query.get("api_key", [""])[0],
        "model": query.get("model", [""])[0],
        "source": query.get("source", [""])[0],
        "provider": query.get("provider", [""])[0],
        "executor": query.get("executor", [""])[0],
        "status_code": query.get("status_code", [""])[0],
        "error_type": query.get("error_type", [""])[0],
    }
    filtered = []
    for entry in rows:
        if selected["api_key"] and entry["scope"] != selected["api_key"]:
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
        if selected["error_type"] and entry["error_type"] != selected["error_type"]:
            continue
        filtered.append(entry)
    offset = max(0, int(query.get("offset", ["0"])[0] or 0))
    limit = max(0, int(query.get("limit", ["0"])[0] or 0))
    page = filtered[offset:offset + limit] if limit else filtered[offset:]
    if scope:
        page = [{key: value for key, value in entry.items()
                 if key not in {"scope", "preview", "label", "auth_index"}} for entry in page]
    result = {"entries": page, "total": len(filtered)}
    if offset == 0:
        result["filter_options"] = {
            "models": sorted({entry["billing_model"] for entry in rows}),
            "sources": sorted({entry["source"] for entry in rows}),
            "providers": sorted({entry["provider"] for entry in rows}),
            "executors": sorted({entry["executor_type"] for entry in rows}),
            "status_codes": sorted({entry["status_code"] for entry in rows if entry["status_code"]}),
            "error_types": sorted({entry["error_type"] for entry in rows if entry["error_type"]}),
        }
    return result


def analysis_view(query, scope=""):
    rows = filter_event_time([entry for entry in REQUEST_EVENTS if not scope or entry["scope"] == scope], query)
    selected = query.get("api_key", [""])[0]
    if selected and not scope:
        rows = [entry for entry in rows if entry["scope"] == selected]

    def distribution(field, label_field=None, unknown="未知"):
        grouped = {}
        for entry in rows:
            key = entry.get(field, "") or unknown
            label = entry.get(label_field, "") if label_field else key
            if not label and field == "scope":
                label = entry.get("preview", "")
            item = grouped.setdefault(key, {"key": key, "label": label or key,
                                            "total_tokens": 0, "requests": 0,
                                            "cost_usd": 0, "cost_available": True})
            cost = entry.get("cost", {})
            item["total_tokens"] += sum(cost.get(name, 0) for name in (
                "uncached_input_tokens", "cache_read_tokens", "cache_write_tokens", "billed_output_tokens"))
            item["requests"] += 1
            item["cost_usd"] += cost.get("total_usd", 0)
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
    cost = {
        "available": all(
            entry.get("price_source") != "none" or
            sum(entry.get("cost", {}).get(name, 0) for name in (
                "uncached_input_tokens", "cache_read_tokens", "cache_write_tokens", "billed_output_tokens"
            )) == 0
            for entry in rows
        ),
        "input_usd": sum(entry.get("cost", {}).get("uncached_input_usd", 0) for entry in rows),
        "cache_read_usd": sum(entry.get("cost", {}).get("cache_read_usd", 0) for entry in rows),
        "cache_write_usd": sum(entry.get("cost", {}).get("cache_write_usd", 0) for entry in rows),
        "output_usd": sum(entry.get("cost", {}).get("output_usd", 0) for entry in rows),
    }
    cost["total_usd"] = sum(cost[field] for field in (
        "input_usd", "cache_read_usd", "cache_write_usd", "output_usd"
    ))

    from_time = datetime.fromisoformat(
        query.get("from", [iso(NOW - timedelta(days=30))])[0].replace("Z", "+00:00")
    )
    to_time = datetime.fromisoformat(
        query.get("to", [iso(NOW)])[0].replace("Z", "+00:00")
    )
    bucket_size = timedelta(hours=1)
    bucket_start = from_time
    if to_time - from_time > timedelta(days=1):
        bucket_size = timedelta(days=1)
        browser_zone = ZoneInfo(query.get("timezone", ["UTC"])[0])
        local_from = from_time.astimezone(browser_zone)
        bucket_start = local_from.replace(hour=0, minute=0, second=0, microsecond=0)
    buckets = []
    cursor = bucket_start
    while cursor < to_time:
        buckets.append({"time": cursor, "requests": 0,
                        "input_tokens": 0, "output_tokens": 0,
                        "cache_read_tokens": 0, "cache_write_tokens": 0,
                        "total_cost": 0})
        cursor += bucket_size
    for entry in rows:
        at = datetime.fromisoformat(entry["at"].replace("Z", "+00:00"))
        if at < bucket_start or not buckets:
            continue
        index = len(buckets) - 1
        for candidate in range(1, len(buckets)):
            if at < buckets[candidate]["time"]:
                index = candidate - 1
                break
        item = buckets[index]
        item["requests"] += 1
        entry_cost = entry.get("cost", {})
        item["input_tokens"] += entry_cost.get("uncached_input_tokens", 0)
        item["output_tokens"] += entry_cost.get("billed_output_tokens", 0)
        item["cache_read_tokens"] += entry_cost.get("cache_read_tokens", 0)
        item["cache_write_tokens"] += entry_cost.get("cache_write_tokens", 0)
        item["total_cost"] += entry_cost.get("total_usd", 0)

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
        "total_cost": trend(lambda item: item["total_cost"]) if cost["available"] else [],
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
            "api_keys": [] if scope or selected else distribution("scope", "label"),
            "models": distribution("billing_model", unknown="未知模型"),
            "sources": distribution("source", unknown="未知来源"),
        },
    }


def payload_for(path, query):
    if path == f"{API_BASE}/access":
        refresh_route_counts()
        return {"keys": KEYS, "plans": PLANS, "routes": ROUTES, "credentials": CREDENTIALS}
    if path == f"{API_BASE}/prices":
        return PRICES
    if path == f"{API_BASE}/events":
        return request_event_view(query)
    if path == f"{API_BASE}/errors":
        return error_view(query)
    if path == f"{API_BASE}/analysis":
        return analysis_view(query)
    if path == f"{API_BASE}/plugin-logs":
        return {"entries": PLUGIN_LOGS}
    if path == f"{API_BASE}/auth-files":
        return {"files": AUTH_FILES}
    if path == f"{API_BASE}/auth-files/quota":
        return auth_file_quota(query)
    if path == f"{API_BASE}/prices/catalog":
        term = query.get("q", [""])[0].lower()
        return {"models": [row for row in PRICES if term in row["pattern"].lower()]}
    if path == "/v0/management/api-keys":
        return {"api-keys": [f"sk-demo-{index:04d}" for index in range(1, len(LIVE_KEYS) + 1)]}
    if path in {
        "/v0/management/gemini-api-key",
        "/v0/management/interactions-api-key",
        "/v0/management/xai-api-key",
        "/v0/management/vertex-api-key",
    }:
        field = path.rsplit("/", 1)[-1]
        return {field: []}
    if path == "/v0/management/codex-api-key":
        return {"codex-api-key": [{"api-key": "sk-dummy-codex", "prefix": "codex"}]}
    if path == "/v0/management/claude-api-key":
        return {"claude-api-key": [{"api-key": "sk-dummy-claude", "prefix": "claude"}]}
    if path == "/v0/management/openai-compatibility":
        return {"openai-compatibility": [{
            "name": "DeepSeek",
            "disabled": False,
            "api-key-entries": [{"api-key": "sk-dummy-deepseek"}],
        }]}
    if path == "/v1/models":
        return {"data": [{"id": row["pattern"]} for row in PRICES] + [
            {"id": "codex/deepseek-v4-flash-vision-exp"},
        ]}
    return None


class Handler(BaseHTTPRequestHandler):
    host_mode = "standalone"
    initial_theme = "auto"

    def send_html(self, body):
        encoded = body.encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def send_json(self, status, payload):
        body = json.dumps(payload, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        parsed = urlparse(self.path)
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
            body = UI_PATH.read_text()
            if self.host_mode != "standalone":
                body = body.replace(
                    "</head>",
                    '<script>localStorage.setItem("managementKey", JSON.stringify("dummy"));</script>\n</head>',
                    1,
                )
            self.send_html(body)
            return
        authorization = self.headers.get("Authorization", "")
        api_keys = [f"sk-demo-{index:04d}" for index in range(1, len(LIVE_KEYS) + 1)]
        if parsed.path == "/v1/models":
            if not authorization.startswith("Bearer ") or authorization[7:] not in api_keys:
                self.send_json(401, {"error": {"message": "API Key 无效"}})
                return
        resource_paths = {
            f"{RESOURCE_BASE}/access",
            f"{RESOURCE_BASE}/prices",
            f"{RESOURCE_BASE}/events",
            f"{RESOURCE_BASE}/errors",
            f"{RESOURCE_BASE}/analysis",
            f"{RESOURCE_BASE}/auth-files",
            f"{RESOURCE_BASE}/auth-files/quota",
        }
        if parsed.path in resource_paths:
            if not authorization.startswith("Bearer ") or authorization[7:] not in api_keys:
                self.send_json(401, {"error": {"message": "API Key 无效"}})
                return
            index = api_keys.index(authorization[7:])
            if parsed.path.endswith("/access"):
                self.send_json(200, account_access(index))
            elif parsed.path.endswith("/prices"):
                self.send_json(200, PRICES)
            elif parsed.path.endswith("/analysis"):
                self.send_json(200, analysis_view(parse_qs(parsed.query), LIVE_KEYS[index]["scope"]))
            elif parsed.path.endswith("/errors"):
                self.send_json(200, error_view(parse_qs(parsed.query), LIVE_KEYS[index]["scope"]))
            elif parsed.path.endswith("/auth-files"):
                self.send_json(200, {"files": account_auth_files(index)})
            elif parsed.path.endswith("/auth-files/quota"):
                query = parse_qs(parsed.query)
                allowed = {
                    item["auth_index"] for item in account_auth_files(index)
                }
                auth_index = query.get("auth_index", [""])[0]
                payload = auth_file_quota(query) if auth_index in allowed else None
                if payload is None:
                    self.send_json(404, {"error": {"message": "认证文件不存在或不支持限额查询"}})
                else:
                    self.send_json(200, payload)
            elif parsed.path.endswith("/events"):
                self.send_json(200, request_event_view(parse_qs(parsed.query), LIVE_KEYS[index]["scope"]))
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
        request_body = b""
        if length:
            request_body = self.rfile.read(length)
        route = self.command, parsed.path
        if route == ("DELETE", f"{API_BASE}/plugin-logs"):
            cleared = len(PLUGIN_LOGS)
            PLUGIN_LOGS.clear()
            self.send_json(200, {"cleared": cleared})
        elif route == ("POST", f"{API_BASE}/prices/catalog/refresh"):
            self.send_json(200, {"catalog": {"models": len(PRICES)}, "updated_models": 2})
        elif route == ("POST", f"{API_BASE}/prices/reset"):
            self.send_json(200, {"restored": len(PRICES)})
        elif route == ("POST", f"{API_BASE}/keys/reset"):
            scopes = json.loads(request_body or b"[]")
            self.send_json(200, {"reset": len(set(scopes))})
        elif route == ("POST", f"{API_BASE}/keys/concurrency"):
            body = json.loads(request_body or b"{}")
            for key in KEYS:
                if key["scope"] == body.get("scope"):
                    key["concurrency_limit"] = body.get("concurrency_limit", 0)
                    break
            self.send_json(200, {"ok": True})
        elif route == ("POST", f"{API_BASE}/keys/sync"):
            self.send_json(200, {"added": 0, "removed": 0})
        elif route == ("POST", f"{API_BASE}/credentials/sync"):
            body = json.loads(request_body or b"{}")
            CREDENTIALS[:] = [
                item for item in CREDENTIALS
                if item["ref"] not in SYNCED_CREDENTIAL_REFS
            ]
            SYNCED_CREDENTIAL_REFS.clear()
            for item in body.get("credentials", []):
                ref = item["ref"]
                SYNCED_CREDENTIAL_REFS.add(ref)
                CREDENTIALS.append({
                    "ref": ref,
                    "source": "ai-providers",
                    "provider": item["provider"],
                    "display_name": item["display_name"],
                    "status": "disabled" if item.get("disabled") else "active",
                    "disabled": bool(item.get("disabled")),
                    "unavailable": False,
                })
            self.send_json(200, {"credentials": CREDENTIALS})
        elif route == ("POST", f"{API_BASE}/prices/sync"):
            self.send_json(200, {"added": 0, "removed": 0, "priced": len(PRICES)})
        elif route == ("DELETE", f"{API_BASE}/routes"):
            route_id = parse_qs(parsed.query).get("id", [""])[0]
            affected = 0
            unrestricted = 0
            ROUTES[:] = [item for item in ROUTES if item["id"] != route_id]
            for key in KEYS:
                route_ids = key["route_bindings"]["route_ids"]
                if route_id in route_ids:
                    key["route_bindings"]["route_ids"] = [item for item in route_ids if item != route_id]
                    affected += 1
                    unrestricted += not any(key["route_bindings"].values())
            self.send_json(200, {"deleted": route_id, "affected_keys": affected, "fully_unrestricted_keys": unrestricted})
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
                    }
                    break
            refresh_route_counts()
            self.send_json(200, {"ok": True})
        elif route in {
            ("POST", f"{API_BASE}/keys/bind"),
            ("POST", f"{API_BASE}/keys/unbind"),
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
    args = parser.parse_args()
    Handler.host_mode = args.host
    Handler.initial_theme = args.theme
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
