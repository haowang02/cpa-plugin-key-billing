# Repository Guidelines

## Required Checks

- **Release tags:** Update `Version` in `internal/plugin/types.go` before creating a tag.
- **Frontend changes:** Start `python3 scripts/frontend_dummy_backend.py`, then test the affected desktop and narrow-screen layouts with Playwright. Static checks alone are insufficient.
- **Billing changes:** Run `scripts/e2e_cpa_billing.sh /Users/wh/Workspace/2api/CLIProxyAPI/cli-proxy-api` after modifying usage parsing, pricing calculations, quota enforcement, failure reporting, or other billing behavior. Use this patched local CLIProxyAPI binary rather than a downloaded release. This suite is not required for frontend-only, documentation-only, or other non-billing changes.

## Architecture Invariants

- `usage.handle` is the only source of provider usage, billing data, latency, and upstream failure details. Implement new billing and failure-diagnostic behavior from `UsageRecord` without parsing raw responses.
- Do not restore `response.normalize_before`, response interceptors, stream-chunk interceptors, request-completion billing, raw-response usage parsers, or the old request/response usage tracker. `request.intercept_before` remains for model and quota enforcement, not usage reconstruction.
- If `UsageRecord` does not expose required information, degrade the feature honestly or extend the CLIProxyAPI usage contract explicitly. Never correlate concurrent records heuristically by model, credential, timestamps, or route.
- Failure events use `UsageRecord.Failed` and `UsageRecord.Failure`. The current usage contract does not include the downstream request path or request ID, so plugin failure logs must not invent them.
- Preserve the host's TTFT value as reported for both streaming and non-streaming requests. Do not infer streaming mode from TTFT, response headers, or approximate latency equality.
- Keep provider token semantics aligned with CLIProxyAPI. In particular, Claude's raw `OutputTokens` includes reasoning tokens; do not charge reasoning twice.

## Data and Compatibility

- Do not bump the SQLite schema version for an idempotent repair or code cleanup. A real format change requires an explicit migration and review.
- Preserve historical data during SQLite and legacy JSON migrations, including failed or all-zero usage rows. If a legacy schema is incompatible, fail and roll back instead of dropping or silently hiding its table.
- Never persist plaintext downstream or upstream API keys. Mask API-key credentials, omit uncertain account values, and keep copied real credentials in a private temporary directory that is removed after testing.
- Do not perform catalog downloads or large catalog parsing while holding the billing store lock. Catalog loading may retry on the usage worker, but request admission must not block on network I/O.

## Commit Messages

Follow Conventional Commits:

```text
<type>(<scope>): <imperative summary>
```

- Write concise, imperative English.
- Keep each commit focused on one logical change.
- Include `scope` only when it clearly identifies the affected module.
- Use one of these common types: `feat`, `fix`, `refactor`, `perf`, `docs`, `test`, `build`, `ci`, or `chore`.

For simple changes, use only a subject line:

```text
fix(viewport): prevent scrolling past content bounds
```

Add a body only when it provides useful context beyond the subject. Separate it with a blank line and describe the motivation, important implementation details, or behavioral impact:

```text
refactor(tui): replace custom viewport handling

Use the standard viewport implementation as the single scrolling path.
Remove the legacy scroll state and compatibility logic.
```

Describe the final change, not the development process or implementation history.
