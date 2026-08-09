# Repository guidelines

- Before creating a tag, update `Version` in `internal/plugin/types.go`.
- For frontend changes, start `python3 scripts/frontend_dummy_backend.py` and use `chrome-devtools` MCP. Check the affected desktop and narrow-screen layouts; static checks alone are not sufficient.
- Run `scripts/e2e_cpa_billing.sh` after changes to usage parsing, pricing calculations, quota enforcement, or other billing behavior. Pure frontend, documentation, and similarly non-billing changes do not require this end-to-end suite.

# Commit messages

Use Conventional Commits:

```text
<type>(<scope>): <imperative summary>
```

Use concise imperative English. Keep each commit focused on one logical change. `scope` is optional and should only be used when it clearly identifies the affected module.

Common types: `feat`, `fix`, `refactor`, `perf`, `docs`, `test`, `build`, `ci`, `chore`.

For simple changes, use only the subject line:

```text
fix(viewport): prevent scrolling past content bounds
```

For changes that need context, add a blank line followed by a concise body describing the motivation, important implementation details, or behavioral impact:

```text
refactor(tui): replace custom viewport handling

Use the standard viewport implementation as the single scrolling path.
Remove the legacy scroll state and compatibility logic.
```

Do not add a body unless it provides useful information beyond the subject. Describe the final change, not the development process or implementation history.

