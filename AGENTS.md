# Repository guidelines

- Prefix commit messages with a type, for example `feat:`, `fix:`, `ci:`, or `chore:`.
- Before creating a tag, update `Version` in `internal/plugin/types.go`.
- For frontend changes, start `python3 scripts/frontend_dummy_backend.py` and use Chrome DevTools against the printed URL. Check the affected desktop and narrow-screen layouts; static checks alone are not sufficient.
- Run `scripts/e2e_cpa_billing.sh` after changes to usage parsing, pricing calculations, quota enforcement, or other billing behavior. Pure frontend, documentation, and similarly non-billing changes do not require this end-to-end suite.
