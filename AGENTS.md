# Repository Guidelines

## Related Source Repositories

Source paths are relative to this repository's root:

- **CLIProxyAPI:** `../CLIProxyAPI`
- **CPAMC:** `../Cli-Proxy-API-Management-Center`
- **CPAMP:** `../CPA-Manager-Plus`

## Required Checks

- **Before committing:** Run `gofmt -l .`; format any listed files and rerun until the output is empty.
- **Go changes:** Run `go vet ./...` and `go test -race ./...`.
- **Frontend changes:** Start `python3 scripts/frontend_dummy_backend.py` on a port other than the default 8765, such as `--port 18765`, and verify affected desktop and narrow-screen layouts with Playwright, beyond static checks. Add `--host cpamc` or `--host cpamp` to check host-specific styles inside those shells.
- **Frontend regression scripts:** Store JavaScript scripts used with `playwright-cli` for browser regression testing in a temporary directory, never in the project's `scripts/` directory.
- **Billing changes:** Run `scripts/e2e_cpa_billing.sh v7.3.7` after modifying any billing behavior, including usage parsing, pricing, quota enforcement, or failure reporting. Keep this baseline aligned with the minimum supported CLIProxyAPI version in `README.md` and `README.en.md`.

## Frontend Layout

- The management page lives in `internal/plugin/web/`. `ui.html` is the page template; `internal/plugin/ui.go` replaces each `/*name.css*/` or `/*name.js*/` marker with that sibling file, and the browser receives one self-contained HTML resource. `scripts/frontend_dummy_backend.py` mirrors this assembly.
- `standalone.js`, `i18n.js`, `style.css`, and `theme.js` load in `<head>`. The feature files (`core.js` through `app.js`) are inlined together into one strict script at the end of `<body>`: top-level declarations are shared, function declarations hoist across files, and top-level statements run in template order. Keep load-time code (constants, bindings, bootstrap) after anything it reads, and keep `app.js` last.
- Add new feature files to the template with their own marker; every `.js` and `.css` file in `web/` must be included exactly once.

## UI Formatting

- Use `scripts/format_ui.mjs` for the template, stylesheet, and scripts in `internal/plugin/web/`; do not run plain Prettier on them, since it expands intentionally compact code. `i18n.js` keeps its own layout and is not formatted.
- Requires Node.js 20+ and npm. Install the pinned development-only dependencies with `npm ci --prefix scripts` after checkout or a lockfile change.
- Format with `node scripts/format_ui.mjs`. Check without writing with `node scripts/format_ui.mjs --check` (exit status 1 means changes are needed or validation failed). By default it covers `internal/plugin/web/*.{html,css,js}`, resolved relative to the script independently of the working directory; pass file paths to format only those files.
- Keep 2-space indentation, useful blank lines, and multi-statement blocks. Short CSS rules, HTML elements, and JavaScript expressions or single-statement blocks are kept on one line where practical, using roughly 140 characters as a guide rather than minifying.
- The formatter checks JavaScript ASTs, CSS structure, and HTML display text before writing. If validation fails, inspect the unsupported formatting case; do not bypass the check or change application behavior just to make formatting pass.
- After changing the formatter, run `npm test --prefix scripts` and verify that a second format pass leaves the files in `internal/plugin/web/` unchanged. The desktop/narrow-screen browser checks above still apply to UI formatting changes.

## Architecture Invariants

- Implement every plugin feature within CLIProxyAPI's existing capabilities. Do not propose or rely on CLIProxyAPI modifications as part of the plugin implementation.
- `usage.handle` is the only source of provider usage, billing data, latency, and upstream failure details. Do not reconstruct usage from raw responses or request/response lifecycle hooks.
- Use `request.intercept_before` only for admission controls such as model, quota, and concurrency enforcement. Use `request.complete` only for lifecycle bookkeeping such as releasing concurrency slots.
- Use `response.intercept_after` only to filter model catalogs by routing rules, and declare it only on hosts that answer `host.auth.models`. Return every other response unchanged without decoding its bodies. Catalog filtering is a display convenience, not access control.
- If `UsageRecord` does not expose required information, degrade the feature honestly. Never correlate concurrent records heuristically by model, credential, timestamps, or route.
- Failure events use `UsageRecord.Failed` and `UsageRecord.Failure`. Log only fields present in the record; do not invent a downstream request path or request ID.
- Preserve the host's TTFT value as reported for both streaming and non-streaming requests. Do not infer streaming mode from TTFT, response headers, or approximate latency equality.
- Keep provider token semantics aligned with CLIProxyAPI. In particular, Claude's raw `OutputTokens` includes reasoning tokens; do not charge reasoning twice.
- Do not introduce plugin-owned background goroutines, timers, or flushers. Complete work synchronously within host calls so the embedded Go runtime remains inactive between calls.

## Data and Compatibility

- Do not bump the SQLite schema version for an idempotent repair or code cleanup. A real format change requires an explicit migration and review.
- Preserve historical data during SQLite and legacy JSON migrations, including failed or all-zero usage rows. If a legacy schema is incompatible, fail and roll back instead of dropping or silently hiding its table.
- Never persist or log plaintext downstream or upstream API keys. Mask API-key credentials, omit uncertain account values, and use dummy credentials in tests; do not copy real credentials into the workspace.

## Release and Changelog

- Before tagging, increment the patch version unless the user explicitly requests a major or minor change, and create an annotated tag with `git tag -a` and a message. Whenever changing `Version` in `internal/plugin/types.go`, update the footer version in `internal/plugin/web/ui.html` to match.
- Edit `Changelog.md` only when the user explicitly requests preparation for a tag or release.
- Prepend one `## vX.Y.Z` section directly below `# Changelog`; never append releases or add an unreleased placeholder.
- Treat sections for tags that already exist as immutable history. Do not edit, move, merge, or delete them unless the user explicitly requests changes to that tag's entry.
- Use concise Chinese bullets that describe released behavior, not the development process. Include only relevant sections, ordered as `### 升级须知`, `### 后端`, then `### 前端`.
- Put breaking changes, migration requirements, and operator actions in `### 升级须知`. Omit implementation details unless they affect users or operators.

## Commit Messages

- Use Conventional Commits: `<type>(<scope>): <imperative summary>`, in concise English. Include `scope` only when it clearly identifies the affected module.
- Use one of these common types: `feat`, `fix`, `refactor`, `perf`, `docs`, `test`, `build`, `ci`, or `chore`.
- Keep each commit focused on one logical change.
- Omit the body only for simple, narrowly scoped changes fully explained by the subject. Otherwise, add a blank line and a body explaining motivation, important implementation details, and behavioral impact; wrap at approximately 72 columns.
- Describe the final change, not the development process or implementation history.

## Local Build and Private Configuration

- Release builds follow `.github/workflows/release.yml`. Locally, `scripts/deploy.sh` cross-builds the Linux/amd64 CGO library with Zig's C compiler (`brew install zig` on macOS) against the same glibc 2.17 baseline. Keep artifacts in ignored `dist/`, and set cross-compilation variables per command rather than globally.
- Private deployment values live in ignored `.env`, created from the placeholder `.env.example` and kept at mode `600`. Never expose credentials or private deployment values in logs, screenshots, commits, uploads, or reports.
- `CPA_SSH_HOST` selects the deployment SSH host. `CPA_REMOTE_PLUGIN_DIR` (inside a `plugins/` directory) and `CPA_REMOTE_COMPOSE_FILE` are resolved on the server relative to the SSH user's home. `CPA_BASE_URL` and `CPA_MANAGEMENT_PASSWORD` provide the verification origin and management login.

## Deployment and End-to-End Verification

After plugin code, UI, build, or installation changes, complete the required local checks only. Do not deploy or run `scripts/deploy.sh` unless the user explicitly requests deployment. When deployment is explicitly requested, complete the steps below without routine reconfirmation; documentation and local setup changes are exempt.

1. Before deploying a new `Version`, update this plugin's CPA `store.version` and `store.release-tag` pins when present; the loader skips files that do not match them. Run `scripts/deploy.sh`: it builds and uploads the library with a checksum check, moves existing `cpa-key-billing-v*.so` files to `plugin-backups/` beside the remote `plugins/` directory, installs `cpa-key-billing-v<Version>.so`, restarts only the `cli-proxy-api` Compose service, and fails unless the management API reports that exact file registered and enabled. Plugin configuration, billing state, and history are preserved. Inspect the server by hand only when the script fails or the deployment layout changes.
2. Open the deployed plugin UI under `CPA_BASE_URL` and sign in with the management password. Verify the changed flow and its authenticated API calls, including desktop and narrow layouts for UI changes, and check existing billing views and history after a refresh. Do not generate billable upstream requests or change real keys, quotas, plans, routes, or credential state for a smoke test; billing behavior changes still require the local end-to-end check above.
3. If deployment breaks CPA or the plugin, restore the backup printed by the script under its original name, remove the new file, and restart through the same Compose file. Report checks, artifact version and checksum, and verification results, naming any blockers and incomplete checks.
