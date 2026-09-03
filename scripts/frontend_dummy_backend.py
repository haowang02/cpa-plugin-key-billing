#!/usr/bin/env python3

import argparse
import hashlib
import json
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlparse


ROOT = Path(__file__).resolve().parents[1]
UI_PATH = ROOT / "internal" / "plugin" / "ui.html"
API_BASE = "/v0/management/plugins/cpa-key-billing"
RESOURCE_BASE = "/v0/resource/plugins/cpa-key-billing"
NOW = datetime.now().astimezone().replace(hour=20, minute=0, second=0, microsecond=0).astimezone(timezone.utc)
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
    {
        "id": "long-name-plan",
        "name": "面向跨区域研发测试与持续集成团队的超长名称订阅计划",
        "amount_usd": 1200,
        "period": {"kind": "monthly"},
    },
]


CREDENTIALS = [
    {"ref": "sha256:" + "a" * 64, "source": "auth-files", "provider": "codex", "display_name": "xxxyyyy4455@gmail.com", "status": "active", "disabled": False, "unavailable": False},
    {"ref": "sha256:" + "b" * 64, "source": "auth-files", "provider": "claude", "display_name": "claude-team@example.com", "status": "active", "disabled": False, "unavailable": False},
    {"ref": "sha256:" + "c" * 64, "source": "ai-providers", "provider": "codex", "display_name": "sk-dem…1234", "status": "active", "disabled": False, "unavailable": False},
]

ROUTES = [
    {"id": "system:all", "name": "默认", "rule": {"models": [], "credential_ids": [], "credential_providers": []}, "bound_key_count": 5},
    {"id": "codex-auth-files", "name": "Codex 认证文件", "rule": {"models": ["gpt-5.6-sol"], "credential_ids": [], "credential_providers": [{"source": "auth-files", "provider": "codex"}]}, "bound_key_count": 6, "fully_unrestricted_keys": 1},
    {"id": "codex-ai-provider", "name": "Codex AI 供应商", "rule": {"models": ["gpt-5.5"], "credential_ids": ["sha256:" + "c" * 64], "credential_providers": []}, "bound_key_count": 4, "fully_unrestricted_keys": 0},
    {"id": "long-name-route", "name": "跨区域研发测试团队使用的超长名称 Codex 认证文件路由规则", "rule": {"models": ["claude/deepseek-v4-flash-vision-exp"], "credential_ids": [], "credential_providers": [{"source": "auth-files", "provider": "codex"}]}, "bound_key_count": 0, "fully_unrestricted_keys": 0},
]


AUTH_FILES = [
    {
        "auth_index": "auth-demo-xai-disabled",
        "name": "xai-disabled@example.com.json",
        "category": "xai",
        "email": "xai-disabled@example.com",
        "disabled": True,
        "unavailable": False,
        "quota_supported": False,
        "quota_unavailable_reason": "认证文件已停用",
    },
    {
        "auth_index": "auth-demo-codex-plus",
        "name": "codex-1dfbc38b-xxxyyyy4455@gmail.com-pro.json",
        "category": "codex",
        "email": "xxxyyyy4455@gmail.com",
        "disabled": False,
        "unavailable": False,
        "quota_supported": True,
    },
    {
        "auth_index": "auth-demo-claude",
        "name": "claude-team@example.com.json",
        "category": "claude",
        "email": "claude-team@example.com",
        "disabled": False,
        "unavailable": False,
        "quota_supported": True,
    },
    {
        "auth_index": "auth-demo-gemini",
        "name": "gemini-cli@example.com.json",
        "category": "gemini-cli",
        "email": "gemini-cli@example.com",
        "disabled": False,
        "unavailable": False,
        "quota_supported": False,
    },
    {
        "auth_index": "auth-demo-codex-pro",
        "name": "codex-pro@example.com.json",
        "category": "codex",
        "email": "pro@example.com",
        "disabled": False,
        "unavailable": False,
        "quota_supported": True,
    },
    {
        "auth_index": "auth-demo-codex-pro5x",
        "name": "codex-pro-lite@example.com.json",
        "category": "codex",
        "email": "pro-lite@example.com",
        "disabled": False,
        "unavailable": False,
        "quota_supported": True,
    },
    {
        "auth_index": "auth-demo-codex-runtime",
        "name": "codex-runtime-only.json",
        "category": "codex",
        "email": "runtime-only@example.com",
        "disabled": False,
        "unavailable": False,
        "runtime_only": True,
        "quota_supported": False,
        "quota_unavailable_reason": "运行时认证文件没有可读取的物理凭据",
    },
    {
        "auth_index": "auth-demo-antigravity",
        "name": "antigravity@example.com.json",
        "category": "antigravity",
        "email": "antigravity@example.com",
        "disabled": False,
        "unavailable": False,
        "quota_supported": True,
    },
    {
        "auth_index": "auth-demo-kimi",
        "name": "kimi@example.com.json",
        "category": "kimi",
        "email": "kimi@example.com",
        "disabled": False,
        "unavailable": True,
        "quota_supported": True,
    },
    {
        "auth_index": "auth-demo-xai-active",
        "name": "xai-active@example.com.json",
        "category": "xai",
        "email": "xai-active@example.com",
        "disabled": False,
        "unavailable": False,
        "quota_supported": True,
    },
]

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


def quota_row(label, used_percent, reset_after_seconds, **extra):
    return {
        "label": label,
        "used_percent": used_percent,
        "reset_after_seconds": reset_after_seconds,
        **extra,
    }


AUTH_FILE_QUOTAS = {
    "auth-demo-codex-pro": {
        "plan": "pro-20x",
        "rate_limit_reset_credits_available_count": 1,
        "quota": [
            quota_row("周限额", 38, 432000),
            quota_row(
                "GPT-5.3-Codex-Spark 5 小时限额",
                0,
                18000,
            ),
            quota_row(
                "GPT-5.3-Codex-Spark 周限额",
                0,
                604800,
            ),
        ],
    },
    "auth-demo-codex-plus": {
        "plan": "plus",
        "rate_limit_reset_credits_available_count": 1,
        "quota": [
            quota_row("5 小时限额", 65, 14400),
            quota_row("周限额", 10, 518400),
        ],
    },
    "auth-demo-codex-pro5x": {
        "plan": "pro-5x",
        "rate_limit_reset_credits_available_count": 1,
        "quota": [
            quota_row("5 小时限额", 44, 10800),
            quota_row("周限额", 27, 259200),
        ],
    },
    "auth-demo-claude": {
        "plan": "Team",
        "quota": [
            quota_row("5 小时限额", 24, 12600),
            quota_row("周限额", 41, 388800),
            {
                "label": "额外用量",
                "used": 1250,
                "limit": 10000,
                "remaining": 8750,
                "used_percent": 12.5,
                "money_cents": True,
            },
        ],
    },
    "auth-demo-antigravity": {
        "plan": "Google AI Pro",
        "quota": [
            quota_row(
                "5 小时限额",
                18,
                64800,
                group_label="Gemini Models",
            ),
            quota_row(
                "周限额",
                7,
                64800,
                group_label="Gemini Models",
            ),
        ],
    },
    "auth-demo-kimi": {
        "quota": [
            quota_row("5 小时限额", 52, 7200),
            quota_row("周限额", 31, 345600),
        ],
    },
    "auth-demo-xai-active": {
        "quota": [
            quota_row("周限额", 22, 410400),
            {
                "label": "月度额度",
                "used": 850,
                "limit": 5000,
                "remaining": 4150,
                "used_percent": 17,
                "money_cents": True,
                "reset_after_seconds": 1814400,
            },
        ],
    },
    "auth-demo-xai-disabled": {
        "plan": "Free",
        "quota": [quota_row("周限额", 100, 86400)],
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


def make_key(index):
    plan_id = "team-monthly" if index <= 14 else "one-time" if index == 15 else ""
    plan_name = "研发团队" if plan_id == "team-monthly" else "一次性额度" if plan_id else ""
    cost = round(7.5 + index * 0.83, 4)
    bindings = [] if index % 4 == 0 else ([{"kind": "route", "value": "codex-auth-files"}] if index % 4 == 1 else ([{"kind": "credential", "value": "sha256:" + "c" * 64}, {"kind": "model", "value": "gpt-5.6-sol"}] if index % 4 == 2 else [{"kind": "route", "value": "codex-ai-provider"}, {"kind": "credential_provider", "value": "auth-files\u0000codex"}]))
    result = {
        "scope": hashlib.sha256(CALLER_SCOPE_SALT + f"sk-demo-{index:04d}".encode()).hexdigest(),
        "preview": f"sk-demo…{index:04d}",
        "label": f"成员 {index:02d}" if index % 3 else "",
        "in_config": True,
        "plan_id": plan_id,
        "plan_name": plan_name,
        "concurrency_limit": [0, 1, 2, 5, 10][index % 5],
        "current_concurrency": index % 3,
        "route_bindings": bindings,
        "unlimited": not plan_id,
        "blocked": False,
        "limit_usd": 500 if plan_id == "team-monthly" else 50 if plan_id else 0,
        "spent_usd": cost if plan_id else 0,
        "used_percent": cost / (500 if plan_id == "team-monthly" else 50) * 100 if plan_id else 0,
    }
    if plan_id == "team-monthly":
        result["cycle_end_at"] = iso(NOW + timedelta(days=25))
    return result


# A deleted Key remains available to historical request events.
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
        "provider": "codex",
        "source": "codex · xxxyyyy4455@gmail.com",
        "executor_type": "CodexExecutor",
        "reasoning_effort": "high",
        "service_tier": "auto",
        "upstream_model": "gpt-5.6-sol",
        "billing_model": "team/gpt-5.6-sol",
        "reasoning_tokens": 2100,
        "cost": make_cost(140000, 19857, 1032, 4852, (5, 0.5, 5, 30), True, False),
    },
    {
        "provider": "xai",
        "source": "xai · demo@example.com",
        "executor_type": "XAIWebsocketsExecutor",
        "reasoning_effort": "xhigh",
        "service_tier": "priority",
        "upstream_model": "gpt-5.6-sol",
        "billing_model": "gpt-5.6-sol",
        "reasoning_tokens": 8000,
        "cost": make_cost(280000, 19000, 1001, 20000, (10, 1, 10, 45), True, True),
    },
    {
        "provider": "deepseek",
        # A provider configured in config.yaml, named by the masked API key
        # that separates it from the other keys of that provider.
        "source": "deepseek · sk-ups…0001",
        "executor_type": "OpenAICompatExecutor",
        "reasoning_effort": "low",
        "service_tier": "default",
        "upstream_model": "deepseek-v4-flash",
        "billing_model": "deepseek-v4-flash",
        "reasoning_tokens": 0,
        "cost": make_cost(160889, 0, 0, 12001, (0.28, 0.28, 0.28, 0.42)),
    },
    {
        "provider": "claude",
        "source": "claude · sk-ups…0001",
        "executor_type": "ClaudeExecutor",
        "reasoning_effort": "medium",
        "service_tier": "standard",
        "upstream_model": "claude-sonnet-4-5",
        "billing_model": "claude-sonnet-4-5",
        "reasoning_tokens": 0,
        "cost": make_cost(38122, 9931, 2048, 7240, (3, 0.3, 3.75, 15)),
    },
    {
        "provider": "codex",
        # A credential no usage record has named yet, so its source falls back
        # to the placeholder.
        "source": "",
        "executor_type": "CodexExecutor",
        "reasoning_effort": "",
        "service_tier": "",
        "upstream_model": "gpt-5.5",
        "billing_model": "gpt-5.5",
        "reasoning_tokens": 400,
        "cost": make_cost(160889, 0, 0, 4096, (2.5, 2.5, 2.5, 15), True, False),
    },
    {
        "provider": "codex",
        # A normal usage event whose provider supplied no token detail.
        "source": "codex · ops@example.com",
        "executor_type": "CodexExecutor",
        "reasoning_effort": "high",
        "service_tier": "auto",
        "upstream_model": "gpt-5.5",
        "billing_model": "gpt-5.5",
        "reasoning_tokens": 0,
        "cost": make_cost(0, 0, 0, 0, (0, 0, 0, 0)),
        "accounting_quality": "",
    },
    {
        "provider": "future-provider",
        # The host reported a total that could not be split into billable
        # buckets. Zero-valued cost fields must render as unknown, not measured.
        "source": "future-provider",
        "executor_type": "FutureExecutor",
        "reasoning_effort": "",
        "service_tier": "",
        "upstream_model": "future-model",
        "billing_model": "future-model",
        "reasoning_tokens": 120,
        "cost": make_cost(0, 0, 0, 0, (0, 0, 0, 0)),
        "accounting_quality": "unclassified",
    },
    {
        "provider": "codex",
        "source": "codex · ops@example.com",
        "executor_type": "CodexExecutor",
        "reasoning_effort": "medium",
        "service_tier": "flex",
        "upstream_model": "gpt-5.5",
        "billing_model": "gpt-5.5",
        "reasoning_tokens": 0,
        "cost": make_cost(0, 0, 0, 0, (0, 0, 0, 0)),
        "accounting_quality": "inconsistent",
    },
    {
        "provider": "xai",
        "source": "xai · ops@example.com",
        "executor_type": "XAIExecutor",
        "reasoning_effort": "high",
        "service_tier": "priority",
        "upstream_model": "grok-4",
        "billing_model": "grok-4",
        "reasoning_tokens": 128,
        "cost": make_cost(4096, 0, 0, 256, (3, 0.75, 3, 15)),
    },
]


def make_request_events(count=120):
    entries = []
    for index in range(count):
        case = LOG_CASES[index % len(LOG_CASES)]
        key = KEYS[index % len(KEYS)]
        entries.append(
            {
                "at": iso(NOW - timedelta(hours=index * 6)),
                "scope": key["scope"],
                "preview": key["preview"],
                "label": key["label"],
                "source": case["source"],
                "provider": case["provider"],
                "executor_type": case["executor_type"],
                "reasoning_effort": case["reasoning_effort"],
                "service_tier": case["service_tier"],
                "upstream_model": case["upstream_model"],
                "billing_model": case["billing_model"],
                "failed": case.get("failed", False),
                "latency_ms": 780 + (index % 5) * 610,
                "ttft_ms": 120 + (index % 4) * 95,
                "accounting_quality": case.get("accounting_quality", "complete"),
                "price_source": "builtin" if index % 4 else "override",
                "cost": case["cost"],
                "reasoning_tokens": case["reasoning_tokens"],
            }
        )
    return entries


REQUEST_EVENTS = make_request_events()

def request_event_view(query, scope=""):
    selected_key = "" if scope else query.get("api_key", [""])[0]
    selected_model = query.get("model", [""])[0]
    selected_source = query.get("source", [""])[0]
    selected_provider = query.get("provider", [""])[0]
    selected_executor = query.get("executor", [""])[0]
    selected_status = query.get("status", [""])[0]
    offset = max(0, int(query.get("offset", ["0"])[0] or 0))
    limit = max(0, int(query.get("limit", ["0"])[0] or 0))
    time_matched = filter_event_time([entry for entry in REQUEST_EVENTS if not scope or entry["scope"] == scope], query)
    keys = {}
    for entry in time_matched:
        keys[entry["scope"]] = {
            "scope": entry["scope"], "preview": entry.get("preview", ""), "label": entry.get("label", ""),
        }
    filter_options = {
        "api_keys": sorted(keys.values(), key=lambda item: (not item["label"], item["label"].lower(),
                                                             item["preview"].lower())),
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
        status = "failed" if entry.get("failed") else "normal"
        counts["all"] += 1
        counts[status] = counts.get(status, 0) + 1
        if selected_status and status != selected_status:
            continue
        matched.append(entry)
    page = matched[offset:offset + limit] if limit else matched[offset:]
    if scope:
        page = [{key: value for key, value in entry.items()
                 if key not in {"scope", "auth_index", "preview", "label"}} for entry in page]
        filter_options["api_keys"] = []
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


def account_status(index):
    key = LIVE_KEYS[index]
    return {
        "role": "api_key",
        "tracked": True,
        "identity": {"preview": key["preview"], "label": key["label"]},
        "subscription": {
            "name": key["plan_name"], "unlimited": key["unlimited"], "blocked": key["blocked"],
            "limit_usd": key["limit_usd"], "spent_usd": key["spent_usd"],
            "remaining_usd": max(0, key["limit_usd"] - key["spent_usd"]),
            "used_percent": key["used_percent"], "cycle_end_at": key.get("cycle_end_at"),
        },
        "concurrency": {"limit": key["concurrency_limit"], "current": key["current_concurrency"]},
    }

def account_access(index):
    key = LIVE_KEYS[index]
    unrestricted = not key["route_bindings"]
    return {
        "role": "api_key",
        "models": [] if unrestricted else ["gpt-5.6-sol"],
        "credentials": [] if unrestricted else [{
            "source": "auth-files", "provider": "codex",
            "name": "xxxyyyy4455@gmail.com", "status": "active",
        }],
        "routing_valid": True,
        "warnings": [],
    }


def refresh_route_counts():
    for route in ROUTES:
        if route["id"] == "system:all":
            route["bound_key_count"] = sum(not key.get("route_bindings") for key in KEYS)
            route["fully_unrestricted_keys"] = 0
            continue
        binding = {"kind": "route", "value": route["id"]}
        bound = [key for key in KEYS if binding in key.get("route_bindings", [])]
        route["bound_key_count"] = len(bound)
        route["fully_unrestricted_keys"] = sum(
            not [item for item in key.get("route_bindings", []) if item != binding]
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
        0,
        (
            "This content was flagged for possible cybersecurity risk. "
            "If this seems wrong, try rephrasing your request. To get authorized for security work, "
            "join the Trusted Access for Cyber program: https://chatgpt.com/cyber"
        ),
        status=400,
        error_type="invalid_request",
    ),
    request_error(
        19,
        "websocket: close 1006 (abnormal closure): unexpected EOF",
    ),
    request_error(
        1,
        "websocket: close 1006 (abnormal closure): unexpected EOF",
    ),
    request_error(
        38,
        "upstream transport requires full HTTP replay",
        status=426,
        error_type="server_error",
        code="upstream_http_replay_required",
    ),
    request_error(
        2,
        "read tcp 192.0.2.10:41740->192.0.2.20:1080: i/o timeout",
    ),
    request_error(
        3,
        "websocket: close 1006 (abnormal closure): unexpected EOF",
    ),
    request_error(
        4,
        "Our servers are currently overloaded. Please try again later.",
        status=502,
        error_type="service_unavailable_error",
    ),
]

PLUGIN_LOGS = [
    {
        "id": 3,
        "at": iso(NOW - timedelta(minutes=2)),
        "level": "debug",
        "message": "route " + json.dumps({"key": "成员 01 · sk-demo…0001", "model": "gpt-5.6-sol", "model_policy": "restricted", "model_result": "allow", "credential_policy": "restricted", "credential_result": "selected", "selected_credential": "codex · xxxyyyy4455@gmail.com", "outcome": "succeeded", "status": 200}, ensure_ascii=False, separators=(",", ":")),
    },
    {
        "id": 2,
        "at": iso(NOW - timedelta(minutes=11)),
        "level": "info",
        "message": (
            "已加载计费数据库 /srv/cli-proxy-api/plugins/cpa-key-billing-state-v1.db："
            "9 个 API Key、1 个订阅计划、24097 条请求事件。已启用。"
        ),
    },
    {
        "id": 1,
        "at": iso(NOW - timedelta(hours=12, minutes=40)),
        "level": "info",
        "message": "已同步 CLIProxyAPI 的 API Key 列表：新增 2 个，移除 1 个。",
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
        keys = {}
        for entry in rows:
            keys[entry["scope"]] = {
                "scope": entry["scope"],
                "preview": entry["preview"],
                "label": entry["label"],
            }
        result["filter_options"] = {
            "api_keys": [] if scope else sorted(
                keys.values(),
                key=lambda item: (not item["label"], item["label"].lower(), item["preview"].lower()),
            ),
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
    cost_fields = {
        "input": "uncached_input_usd",
        "cache_read": "cache_read_usd",
        "cache_write": "cache_write_usd",
        "output": "output_usd",
    }
    cost_values = {
        name: sum(entry.get("cost", {}).get(field, 0) for entry in rows)
        for name, field in cost_fields.items()
    }
    total_usd = sum(cost_values.values())
    cost = {
        "available": all(
            entry.get("price_source") != "none" or
            sum(entry.get("cost", {}).get(name, 0) for name in (
                "uncached_input_tokens", "cache_read_tokens", "cache_write_tokens", "billed_output_tokens"
            )) == 0
            for entry in rows
        ),
        "total_usd": total_usd,
    }
    for name, value in cost_values.items():
        cost[name] = {"usd": value}

    return {
        "range": {
            "from": query.get("from", [iso(NOW - timedelta(days=30))])[0],
            "to": query.get("to", [iso(NOW)])[0],
        },
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
        "usage_distribution": {
            "api_keys": [] if scope or selected else distribution("scope", "label"),
            "models": distribution("billing_model", unknown="未知模型"),
            "sources": distribution("source", unknown="未知来源"),
        },
    }


def payload_for(path, query):
    if path == f"{API_BASE}/status":
        return {"role": "management", "enabled": True}
    if path == f"{API_BASE}/access":
        refresh_route_counts()
        return {"role": "management", "keys": KEYS, "plans": PLANS, "routes": ROUTES, "credentials": CREDENTIALS}
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
    if path == "/v1/models":
        return {"data": [{"id": row["pattern"]} for row in PRICES]}
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
            f"{RESOURCE_BASE}/status",
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
            if parsed.path.endswith("/status"):
                self.send_json(200, account_status(index))
            elif parsed.path.endswith("/access"):
                self.send_json(200, account_access(index))
            elif parsed.path.endswith("/prices"):
                self.send_json(200, PRICES)
            elif parsed.path.endswith("/analysis"):
                self.send_json(200, analysis_view(parse_qs(parsed.query), LIVE_KEYS[index]["scope"]))
            elif parsed.path.endswith("/errors"):
                self.send_json(200, error_view(parse_qs(parsed.query), LIVE_KEYS[index]["scope"]))
            elif parsed.path.endswith("/auth-files"):
                self.send_json(200, {"files": AUTH_FILES})
            elif parsed.path.endswith("/auth-files/quota"):
                payload = auth_file_quota(parse_qs(parsed.query))
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
        elif route == ("POST", f"{API_BASE}/keys/reset-all"):
            self.send_json(200, {"reset": 14})
        elif route == ("POST", f"{API_BASE}/keys/concurrency"):
            body = json.loads(request_body or b"{}")
            for key in KEYS:
                if key["scope"] == body.get("scope"):
                    key["concurrency_limit"] = body.get("concurrency_limit", 0)
                    break
            self.send_json(200, {"ok": True})
        elif route == ("POST", f"{API_BASE}/keys/sync"):
            self.send_json(200, {"added": 0, "removed": 0})
        elif route == ("POST", f"{API_BASE}/prices/sync"):
            self.send_json(200, {"added": 0, "removed": 0, "priced": len(PRICES)})
        elif route == ("DELETE", f"{API_BASE}/routes"):
            route_id = parse_qs(parsed.query).get("id", [""])[0]
            affected = 0
            unrestricted = 0
            ROUTES[:] = [item for item in ROUTES if item["id"] != route_id]
            for key in KEYS:
                before = len(key.get("route_bindings", []))
                key["route_bindings"] = [binding for binding in key.get("route_bindings", []) if not (binding["kind"] == "route" and binding["value"] == route_id)]
                if before != len(key["route_bindings"]):
                    affected += 1
                    unrestricted += not key["route_bindings"]
            self.send_json(200, {"deleted": route_id, "affected_keys": affected, "fully_unrestricted_keys": unrestricted})
        elif route == ("POST", f"{API_BASE}/routes"):
            body = json.loads(request_body or b"{}")
            route_id = f"route-dummy-{len(ROUTES)}"
            scopes = set(body.get("scopes", []))
            stored = {"id": route_id, "name": body.get("name", "新路由"), "rule": body.get("rule", {}), "bound_key_count": len(scopes)}
            ROUTES.append(stored)
            for key in KEYS:
                if key["scope"] in scopes:
                    key.setdefault("route_bindings", []).append({"kind": "route", "value": route_id})
            self.send_json(201, {"route": stored})
        elif route == ("PATCH", f"{API_BASE}/routes"):
            body = json.loads(request_body or b"{}")
            route_id = body.get("id", "")
            stored = next((item for item in ROUTES if item["id"] == route_id), ROUTES[1])
            stored.update({key: body[key] for key in ("name", "rule") if key in body})
            if "scopes" in body:
                scopes = set(body["scopes"])
                for key in KEYS:
                    bindings = [binding for binding in key.get("route_bindings", []) if not (binding["kind"] == "route" and binding["value"] == route_id)]
                    if key["scope"] in scopes:
                        bindings.append({"kind": "route", "value": route_id})
                    key["route_bindings"] = bindings
            refresh_route_counts()
            self.send_json(200, {"route": stored})
        elif route == ("PUT", f"{API_BASE}/keys/routes"):
            body = json.loads(request_body or b"{}")
            bindings = body.get("bindings", [])
            if bindings == [{"kind": "route", "value": "system:all"}]:
                bindings = []
            for key in KEYS:
                if key["scope"] == body.get("scope"):
                    key["route_bindings"] = bindings
                    break
            refresh_route_counts()
            self.send_json(200, {"ok": True})
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
