package sqlite

// Times are Unix nanoseconds. Ordered configuration uses explicit positions.
const schema = `
CREATE TABLE api_keys (
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
	route_bindings_json   TEXT    NOT NULL DEFAULT '[]'
);

CREATE TABLE routes (
	position INTEGER PRIMARY KEY,
	id       TEXT    NOT NULL UNIQUE,
	name     TEXT    NOT NULL DEFAULT '',
	rule_json TEXT   NOT NULL DEFAULT '{}'
);

INSERT INTO routes (position, id, name, rule_json)
VALUES (0, 'system:all', '默认', '{"models":[],"credential_ids":[],"credential_providers":[]}');

CREATE TABLE plans (
	position       INTEGER PRIMARY KEY,
	id             TEXT    NOT NULL UNIQUE,
	name           TEXT    NOT NULL DEFAULT '',
	amount_usd     REAL    NOT NULL DEFAULT 0,
	period_seconds INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE prices (
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

CREATE TABLE credentials (
	auth_index TEXT PRIMARY KEY,
	provider   TEXT NOT NULL DEFAULT '',
	account    TEXT NOT NULL DEFAULT '',
	name       TEXT NOT NULL DEFAULT ''
);

CREATE TABLE request_events (
	id                          INTEGER PRIMARY KEY AUTOINCREMENT,
	at                          INTEGER NOT NULL,
	scope                       TEXT    NOT NULL,
	auth_index                  TEXT    NOT NULL DEFAULT '',
	provider                    TEXT    NOT NULL DEFAULT '',
	executor_type               TEXT    NOT NULL DEFAULT '',
	reasoning_effort            TEXT    NOT NULL DEFAULT '',
	service_tier                TEXT    NOT NULL DEFAULT '',
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

CREATE INDEX request_events_at ON request_events(at);
CREATE INDEX request_events_scope_at ON request_events(scope, at);
CREATE INDEX request_events_model_at ON request_events(billing_model, at);
CREATE INDEX request_events_auth_at ON request_events(auth_index, at);

CREATE TABLE request_errors (
	request_event_id INTEGER PRIMARY KEY REFERENCES request_events(id) ON DELETE CASCADE,
	status_code      INTEGER NOT NULL DEFAULT 0,
	error_type       TEXT    NOT NULL DEFAULT '',
	reason           TEXT    NOT NULL DEFAULT '',
	body             TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX request_errors_status ON request_errors(status_code);
CREATE INDEX request_errors_type ON request_errors(error_type);

CREATE TABLE plugin_logs (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	at      INTEGER NOT NULL,
	level   TEXT    NOT NULL DEFAULT '',
	message TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX plugin_logs_at ON plugin_logs(at);

`
