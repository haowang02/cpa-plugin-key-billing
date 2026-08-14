# Repository Guidelines

## Required Checks

- **Release tags:** Update `Version` in `internal/plugin/types.go` before creating a tag.
- **Frontend changes:** Start `python3 scripts/frontend_dummy_backend.py`, then test the affected desktop and narrow-screen layouts with Playwright. Static checks alone are insufficient.
- **Billing changes:** Run `scripts/e2e_cpa_billing.sh` after modifying usage parsing, pricing calculations, quota enforcement, or other billing behavior. This suite is not required for frontend-only, documentation-only, or other non-billing changes.

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
