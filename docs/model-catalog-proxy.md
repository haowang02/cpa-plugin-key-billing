# Per-key model discovery

The optional `cpa-model-catalog-proxy` companion filters CPA model catalogs using
the caller's Key Billing routing permissions. It runs as a separate HTTP
process and uses the existing `/v0/resource/plugins/cpa-key-billing/routing`
endpoint in v1.3.3 and later. The plugin binary and CPA require no changes.

## Run

```sh
go build -o cpa-model-catalog-proxy ./cmd/cpa-model-catalog-proxy
./cpa-model-catalog-proxy -listen 127.0.0.1:18318 -upstream http://127.0.0.1:8317
```

Use the original CPA listener as the upstream, not this proxy or an address
whose model routes lead back to it. Both HTTP and HTTPS upstream origins are
supported. The default listener is loopback.

Clients can use the companion as their CPA base URL. Alternatively, send only
model-list requests through it in an existing reverse proxy. For Nginx, inside
the API server block:

```nginx
location ~ ^/(v1/models|models|backend-api/codex/models|codex/models)$ {
    proxy_pass http://127.0.0.1:18318;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_cache off;
}
```

Keep the administrator's model-pricing discovery connected directly to CPA so
that its complete catalog is available. Direct CPA connections bypass this
display filter; Key Billing's request-time enforcement remains authoritative.

## Filtering rules

- Each successful model-list response is intersected with the current caller's
  route model scope. An empty scope means unrestricted models; matching is
  case-insensitive and exact, as in Key Billing. No model names are hardcoded.
- Credential-bound routes also restrict catalog ownership to the credential
  providers reported by Key Billing. Codex corresponds to `owned_by: openai`.
  Native Codex catalogs omit ownership and are treated as Codex-only. Unknown
  owners are hidden when a credential-provider restriction applies, and entries
  reported as missing or disabled do not grant a provider.
- OpenAI `data` and native Codex `models` arrays are supported. Retained model
  metadata, including instructions, capabilities, and speed choices, is preserved.
- The catalog and routing permissions are fetched on every request. Filtered
  responses use `Cache-Control: private, no-store`, and shared cache validators
  are removed. Clients must still refresh any catalog they already hold.
- CPA authenticates the catalog request first. The routing lookup uses the same
  downstream key, normalized to Bearer authentication, without a management key.
  Conflicting credentials are rejected. Permission redirects are not followed.
- Missing, invalid, or unavailable routing permissions produce HTTP 502 instead
  of an unfiltered model list. The Key must be tracked in Key Billing, for example
  by synchronizing configured keys and assigning its route in the management UI.
- Other requests and upstream HTTP errors pass through. Inference payloads,
  service tiers, billing, SSE, and standard reverse-proxy WebSocket upgrades keep
  their existing behavior. No inference requests are issued to probe models.

Provider ownership is a coarse compatibility check. The routing response does
not enumerate the models supported by each individual credential. This filter
therefore cannot guarantee immediate callability or predict temporary quotas,
cooldowns, network failures, missing model prices, or per-credential model
differences. These remain request-time checks. Clients that merge server models
with their own built-in lists may still show additional local entries.

## Tests

```sh
go test -race ./internal/catalogproxy ./cmd/cpa-model-catalog-proxy
```

Tests cover concurrent per-key isolation, live routing changes, both catalog
formats and all supported paths, provider intersection, metadata preservation,
cache isolation, malformed responses, credential ambiguity, redirects, body
limits, inference streams, and upstream authentication errors.
