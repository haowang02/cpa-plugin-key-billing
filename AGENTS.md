# Repository Guidelines

## Required Checks

- **Release tags:** Update `Version` in `internal/plugin/types.go` before creating a tag.
- **Tag version increments:** When creating a tag, increment the patch version unless the user explicitly requests a major or minor version change.
- **Frontend changes:** Start `python3 scripts/frontend_dummy_backend.py --port 18765`, then test the affected desktop and narrow-screen layouts with Playwright. Static checks alone are insufficient; do not use the script's default port.
- **Billing changes:** Run `scripts/e2e_cpa_billing.sh v7.2.143` after modifying usage parsing, pricing calculations, quota enforcement, failure reporting, or other billing behavior. This suite is not required for frontend-only, documentation-only, or other non-billing changes.

## Architecture Invariants

- Implement every plugin feature within CLIProxyAPI's existing capabilities. Do not propose or rely on CLIProxyAPI modifications as part of the plugin implementation.
- `usage.handle` is the only source of provider usage, billing data, latency, and upstream failure details. Do not reconstruct usage from raw responses or request/response lifecycle hooks.
- Use `request.intercept_before` only for admission controls such as model, quota, and concurrency enforcement. Use `request.complete` only for lifecycle bookkeeping such as releasing concurrency slots.
- If `UsageRecord` does not expose required information, degrade the feature honestly. Never correlate concurrent records heuristically by model, credential, timestamps, or route.
- Failure events use `UsageRecord.Failed` and `UsageRecord.Failure`. Log only fields present in the record; do not invent a downstream request path or request ID.
- Preserve the host's TTFT value as reported for both streaming and non-streaming requests. Do not infer streaming mode from TTFT, response headers, or approximate latency equality.
- Keep provider token semantics aligned with CLIProxyAPI. In particular, Claude's raw `OutputTokens` includes reasoning tokens; do not charge reasoning twice.
- Do not introduce plugin-owned background goroutines, timers, or flushers. Complete work synchronously within host calls so the embedded Go runtime remains inactive between calls.

## Data and Compatibility

- Do not bump the SQLite schema version for an idempotent repair or code cleanup. A real format change requires an explicit migration and review.
- Preserve historical data during SQLite and legacy JSON migrations, including failed or all-zero usage rows. If a legacy schema is incompatible, fail and roll back instead of dropping or silently hiding its table.
- Never persist or log plaintext downstream or upstream API keys. Mask API-key credentials, omit uncertain account values, and use dummy credentials in tests; do not copy real credentials into the workspace.
- Perform catalog downloads and parsing outside the billing store lock and the request-admission path. Request admission must never block on network I/O.

## Commit Messages

Follow Conventional Commits:

```text
<type>(<scope>): <imperative summary>
```

- Write concise, imperative English.
- Keep each commit focused on one logical change.
- Include `scope` only when it clearly identifies the affected module.
- Use one of these common types: `feat`, `fix`, `refactor`, `perf`, `docs`, `test`, `build`, `ci`, or `chore`.

Use only a subject line when the change is genuinely simple, narrowly scoped, and fully explained by the summary:

```text
fix(viewport): prevent scrolling past content bounds
```

For every other commit, include a body. Separate it from the subject with a blank line and describe the motivation, important implementation details, and behavioral impact:

- Wrap body text at approximately 72 columns so it remains readable with Git's standard log indentation.
- Prefer one cohesive body paragraph. Use additional paragraphs only when the commit contains genuinely distinct concerns.

```text
refactor(tui): replace custom viewport handling

Use the standard viewport implementation as the single scrolling path.
Remove the legacy scroll state and compatibility logic.
```

Describe the final change, not the development process or implementation history.
