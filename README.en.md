<div align="center">
  <h1>CPA Key Billing</h1>
  <p><strong>Per-key billing, subscription quotas, and routing for <a href="https://github.com/router-for-me/CLIProxyAPI">CLIProxyAPI</a>.</strong></p>
  <p>
    <a href="https://github.com/haowang02/cpa-plugin-key-billing/releases/latest"><img src="https://img.shields.io/github/v/release/haowang02/cpa-plugin-key-billing?label=release" alt="Latest release"></a>
    <a href="https://github.com/haowang02/cpa-plugin-key-billing/releases"><img src="https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/haowang02/cpa-plugin-key-billing/badges/downloads.json" alt="Plugin package downloads"></a>
    <img src="https://img.shields.io/badge/platforms-Windows%20%7C%20macOS%20%7C%20Linux-blue" alt="Platforms: Windows, macOS, and Linux">
    <a href="./LICENSE"><img src="https://img.shields.io/badge/license-MIT-green" alt="MIT License"></a>
  </p>
  <p><strong>English</strong> · <a href="./README.md">简体中文</a></p>
</div>
<img src="images/example.png" alt="CPA Key Billing dashboard" width="100%" />

## Features

- Limit spending, tokens, requests, and concurrency per API key, or share quotas across keys
- Use USD or CNY pricing, long-context tiers, and reference prices from [models.dev](https://models.dev/)
- Control model and upstream credential access through routing rules
- Admin and self-service dashboards with separate usage statistics for each key

## Installation

Requires **CLIProxyAPI 8.0.44 or later** with plugin support.

Search for `cpa-key-billing` in the CPAMC or CPAMP plugin store, or run the installer from your CLIProxyAPI directory to install it into `plugins/`.

macOS / Linux:

```sh
curl -LsSf https://raw.githubusercontent.com/haowang02/cpa-plugin-key-billing/main/install.sh | sh
```

Windows: stop CLIProxyAPI first, then run in PowerShell:

```powershell
irm https://raw.githubusercontent.com/haowang02/cpa-plugin-key-billing/main/install.ps1 | iex
```

## Configuration

Add the following to your CLIProxyAPI configuration:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa-key-billing:
      enabled: true
      priority: 100                      # Scheduling priority; higher values take precedence
      debug: false                       # Debug logging

      # Data
      state_file: "plugins/cpa-key-billing-state-v1.db"
      request_event_retention_days: 365  # History retention in days; shortening deletes older records

      # Billing
      allow_unpriced_models: false       # Admit unpriced models and record usage at zero cost
      codex_fast_mode_billing: false     # Charge 2.5× for Codex requests sent upstream at the priority tier
      cny_per_usd: 7                     # CNY per USD

      # API key account view
      mask_api_key_view_emails: false    # Mask full email addresses
      allow_api_key_quota_reset: false   # Let users reset accessible Codex / Claude auth file quotas

      # Credential scheduling; applies only to requests with credential restrictions
      routing:
        strategy: "round-robin"          # round-robin, weighted-round-robin, fill-first
        session-affinity: false          # Reuse an available credential for the same session
        session-affinity-ttl: "1h"       # Idle timeout for session bindings
```

Restart CLIProxyAPI after installing or upgrading.

> [!WARNING]
> Back up `state_file` before upgrading. Databases from v1.0.0 onward migrate automatically. Data from v0.8.4 or earlier is incompatible; use a new data file.

## Access

Open the plugin from the management panel or visit:

```text
http(s)://<CLIProxyAPI address>/v0/resource/plugins/cpa-key-billing/ui
```

## Billing and quotas

### Model pricing

Custom prices override reference prices; unpriced models are rejected by default. Quotas are charged in the plan's currency, converting other currencies at `cny_per_usd`.

### Subscription plans

A plan can have multiple quota windows combining spending, token, and request limits.

| Type | Quota allocation | Reset schedule |
| --- | --- | --- |
| Per-key schedule | Separate quota for each key | Starts with each key's first admitted request |
| Shared schedule | Separate quota for each key | All keys reset at the configured time |
| Shared quota | One pool for all bound keys | All keys reset at the configured time |

Clearing history does not reset quotas. Unbinding or deleting a member key does not refund shared quota usage.

## Routing rules

Routing rules limit the models an API key can call and the upstream credentials it can use. Credentials can be selected one by one or by category, such as "Auth Files · codex".

All rules on a key apply together: allowlists are merged, denials take precedence, and an empty allowlist means no limit. Models and credentials are merged separately, not paired by rule.

```mermaid
---
config:
  themeVariables:
    fontSize: "13px"
  flowchart:
    diagramPadding: 4
    nodeSpacing: 20
    rankSpacing: 28
    padding: 3
---
flowchart TB
    D{{Do model rules allow the model?}} -- Yes --> E{{Served by an allowed credential?}}
    E -- Yes --> P{{Allowed credential usable now?}}
    P -- Yes --> U[Call the upstream provider]
    D -- No --> R1[HTTP 403]
    E -- No --> R2[HTTP 403]
    P -- No --> S[HTTP 503]
```

- A disabled credential serves no models. A credential in cooldown still counts as serving its models, so the request gets a 503.
- `/v1/models` lists only the models the key can call. The CPAMC and CPAMP management panels still see the full list.
- Credential rules apply only to credentials this CPA instance loads. In CPA Home mode the plugin cannot see Home-dispatched credentials, so keys with credential rules are refused.

## Rejected requests

| Status | What to check |
| --- | --- |
| `403` | Whether the key's routing rules allow the model and an allowed credential serves it |
| `429` | Whether subscription quota or concurrency limits have been reached |
| `503` | Whether the model is priced and allowed credentials are available; check plugin logs for details |

## Acknowledgments

- [LINUX DO](https://linux.do/) community
