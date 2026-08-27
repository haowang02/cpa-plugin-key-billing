package sqlite

// Money is REAL because the domain computes in float64 USD; rounding it on the
// way to disk would make the stored totals disagree with the ones the plugin
// enforces against. Instants are INTEGER Unix nanoseconds.
//
// Plans, prices and model groups carry an explicit position: all three are
// ordered lists an operator reads back, and prices are additionally consulted in
// order, globs last. Model membership keeps its position for the same reason.
const schema = `
CREATE TABLE IF NOT EXISTS api_keys (
	scope                 TEXT    PRIMARY KEY,
	preview               TEXT    NOT NULL DEFAULT '',
	label                 TEXT    NOT NULL DEFAULT '',
	in_config             INTEGER NOT NULL DEFAULT 0,
	deleted_at            INTEGER NOT NULL DEFAULT 0,
	plan_id               TEXT    NOT NULL DEFAULT '',
	concurrency_limit      INTEGER NOT NULL DEFAULT 0,
	cycle_plan_id         TEXT    NOT NULL DEFAULT '',
	cycle_start_at        INTEGER NOT NULL DEFAULT 0,
	cycle_end_at          INTEGER NOT NULL DEFAULT 0,
	cycle_spent_usd       REAL    NOT NULL DEFAULT 0,
	cost_usd              REAL    NOT NULL DEFAULT 0,
	requests              INTEGER NOT NULL DEFAULT 0,
	uncached_input_tokens INTEGER NOT NULL DEFAULT 0,
	output_tokens         INTEGER NOT NULL DEFAULT 0,
	reasoning_tokens      INTEGER NOT NULL DEFAULT 0,
	cache_read_tokens     INTEGER NOT NULL DEFAULT 0,
	cache_creation_tokens INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS key_models (
	scope                 TEXT    NOT NULL REFERENCES api_keys(scope) ON DELETE CASCADE,
	billing_model         TEXT    NOT NULL,
	cost_usd              REAL    NOT NULL DEFAULT 0,
	requests              INTEGER NOT NULL DEFAULT 0,
	uncached_input_tokens INTEGER NOT NULL DEFAULT 0,
	output_tokens         INTEGER NOT NULL DEFAULT 0,
	reasoning_tokens      INTEGER NOT NULL DEFAULT 0,
	cache_read_tokens     INTEGER NOT NULL DEFAULT 0,
	cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (scope, billing_model)
);

-- The two tables a key's model grant lives in cascade with the key itself, the
-- way its per-model usage does. Neither they nor model_group_models reference
-- model_groups: that table is rewritten whole on every edit, so a cascade from
-- it would take every key's grant with it. A binding whose group is gone is
-- healed where the panel reads the key list instead.
CREATE TABLE IF NOT EXISTS key_model_groups (
	scope    TEXT    NOT NULL REFERENCES api_keys(scope) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	group_id TEXT    NOT NULL,
	PRIMARY KEY (scope, group_id)
);

CREATE TABLE IF NOT EXISTS key_allowed_models (
	scope    TEXT    NOT NULL REFERENCES api_keys(scope) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	model    TEXT    NOT NULL,
	PRIMARY KEY (scope, model)
);

CREATE TABLE IF NOT EXISTS model_groups (
	position INTEGER PRIMARY KEY,
	id       TEXT    NOT NULL UNIQUE,
	name     TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS model_group_models (
	group_id TEXT    NOT NULL,
	position INTEGER NOT NULL,
	model    TEXT    NOT NULL,
	PRIMARY KEY (group_id, model)
);

CREATE TABLE IF NOT EXISTS plans (
	position       INTEGER PRIMARY KEY,
	id             TEXT    NOT NULL UNIQUE,
	name           TEXT    NOT NULL DEFAULT '',
	amount_usd     REAL    NOT NULL DEFAULT 0,
	period_kind    TEXT    NOT NULL DEFAULT '',
	period_seconds INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS prices (
	position                        INTEGER PRIMARY KEY,
	pattern                         TEXT    NOT NULL,
	input_per_1m                    REAL    NOT NULL DEFAULT 0,
	output_per_1m                   REAL    NOT NULL DEFAULT 0,
	cache_read_per_1m               REAL,
	cache_write_per_1m              REAL,
	long_context_threshold          INTEGER,
	long_context_input_per_1m       REAL,
	long_context_output_per_1m      REAL,
	long_context_cache_read_per_1m  REAL,
	long_context_cache_write_per_1m REAL
);

CREATE TABLE IF NOT EXISTS credentials (
	auth_index TEXT PRIMARY KEY,
	provider   TEXT NOT NULL DEFAULT '',
	account    TEXT NOT NULL DEFAULT '',
	name       TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS usage_log (
	id                          INTEGER PRIMARY KEY AUTOINCREMENT,
	at                          INTEGER NOT NULL,
	scope                       TEXT    NOT NULL,
	auth_index                  TEXT    NOT NULL DEFAULT '',
	upstream_model              TEXT    NOT NULL DEFAULT '',
	billing_model               TEXT    NOT NULL DEFAULT '',
	failed                      INTEGER NOT NULL DEFAULT 0,
	latency_ms                  INTEGER NOT NULL DEFAULT 0,
	ttft_ms                     INTEGER NOT NULL DEFAULT 0,
	accounting_quality          TEXT    NOT NULL DEFAULT '',
	price_source                TEXT    NOT NULL DEFAULT '',
	reasoning_tokens            INTEGER NOT NULL DEFAULT 0,
	total_usd                   REAL    NOT NULL DEFAULT 0,
	uncached_input_usd          REAL    NOT NULL DEFAULT 0,
	cache_read_usd              REAL    NOT NULL DEFAULT 0,
	cache_write_usd             REAL    NOT NULL DEFAULT 0,
	output_usd                  REAL    NOT NULL DEFAULT 0,
	uncached_input_tokens       INTEGER NOT NULL DEFAULT 0,
	cache_read_tokens           INTEGER NOT NULL DEFAULT 0,
	cache_write_tokens          INTEGER NOT NULL DEFAULT 0,
	billed_output_tokens        INTEGER NOT NULL DEFAULT 0,
	tiered                      INTEGER NOT NULL DEFAULT 0,
	long_context                INTEGER NOT NULL DEFAULT 0,
	threshold_input_tokens      INTEGER NOT NULL DEFAULT 0,
	applied_input_per_1m        REAL    NOT NULL DEFAULT 0,
	applied_output_per_1m       REAL    NOT NULL DEFAULT 0,
	applied_cache_read_per_1m   REAL    NOT NULL DEFAULT 0,
	applied_cache_write_per_1m  REAL    NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS usage_log_at ON usage_log(at);
CREATE INDEX IF NOT EXISTS usage_log_scope ON usage_log(scope);

CREATE TABLE IF NOT EXISTS plugin_log (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	at      INTEGER NOT NULL,
	level   TEXT    NOT NULL DEFAULT '',
	message TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS plugin_log_at ON plugin_log(at);
`
