package sqlite

type index struct {
	name, columns string
}

// Non-unique indexes named <table>_<name> belong to this module.
var indexes = map[string][]index{
	// Names the sources of configured credentials' events.
	"config_credentials": {
		{"auth_index", "auth_index"},
	},
	"reference_prices": {
		{"match_key", "match_key"},
	},
	"request_events": {
		{"at", "at, id, failed"},
		{"scope_at", "scope, at, id, failed"},
		// Covers credential usage, so its sums never read the wide rows.
		{"auth_usage", "auth_index, at, provider, billing_model, upstream_model, failed, " +
			"uncached_input_tokens, cache_read_tokens, cache_write_tokens, billed_output_tokens, currency, total_cost"},
		{"model_at", eventModelSQL + ", at"},
	},
	"quota_reports": {
		// Covers the newest values and the history of a credential's reports.
		{"auth", "auth_index, provider, at, request_event_id, plan, credit_balance, credits_unlimited"},
	},
	"quota_report_windows": {
		// Covers each window's reports in order, and so its newest one.
		{"window", "auth_index, provider, limit_key, seconds, at, request_event_id, used_percent, reset_at, plan"},
	},
	"request_errors": {
		{"status", "status_code, error_type"},
		{"type", "error_type, status_code"},
		{"event_type_status", "request_event_id, error_type, status_code"},
	},
	"plugin_logs": {
		{"at", "at"},
		{"level_id_at", "level, id, at"},
	},
}

// quotaReportTables hold what each request's response reported about its
// credential's quota. A window copies its report's credential, time and plan so
// that each window's reports are an index range.
const quotaReportTables = `
CREATE TABLE quota_reports (
	request_event_id  INTEGER PRIMARY KEY REFERENCES request_events(id) ON DELETE CASCADE,
	auth_index        TEXT    NOT NULL,
	provider          TEXT    NOT NULL,
	at                INTEGER NOT NULL,
	plan              TEXT    NOT NULL DEFAULT '',
	credit_balance    TEXT    NOT NULL DEFAULT '',
	credits_unlimited INTEGER
);

CREATE TABLE quota_report_windows (
	request_event_id INTEGER NOT NULL REFERENCES quota_reports(request_event_id) ON DELETE CASCADE,
	limit_key        TEXT    NOT NULL,
	seconds          INTEGER NOT NULL,
	auth_index       TEXT    NOT NULL,
	provider         TEXT    NOT NULL,
	at               INTEGER NOT NULL,
	plan             TEXT    NOT NULL DEFAULT '',
	used_percent     REAL    NOT NULL,
	reset_at         INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (request_event_id, limit_key, seconds)
) WITHOUT ROWID;
`

// Time columns use Unix nanoseconds; JSON cycles use UTC RFC3339Nano. Amounts
// are in the currency of their row.
const schema = `
CREATE TABLE api_keys (
	scope                 TEXT    PRIMARY KEY,
	preview               TEXT    NOT NULL DEFAULT '',
	label                 TEXT    NOT NULL DEFAULT '',
	in_config             INTEGER NOT NULL DEFAULT 0,
	deleted_at            INTEGER NOT NULL DEFAULT 0,
	plan_id               TEXT    NOT NULL DEFAULT '',
	concurrency_limit     INTEGER NOT NULL DEFAULT 0,
	cycles_json           TEXT    NOT NULL DEFAULT '{}',
	route_bindings_json   TEXT    NOT NULL DEFAULT '{}'
);

CREATE TABLE routes (
	position INTEGER PRIMARY KEY,
	id       TEXT    NOT NULL UNIQUE,
	name     TEXT    NOT NULL DEFAULT '',
	rule_json TEXT   NOT NULL DEFAULT '{}'
);

CREATE TABLE config_credentials (
	ref          TEXT PRIMARY KEY,
	provider     TEXT NOT NULL,
	key_preview  TEXT NOT NULL DEFAULT '',
	disabled     INTEGER NOT NULL DEFAULT 0,
	auth_index   TEXT NOT NULL DEFAULT '',
	type         TEXT NOT NULL DEFAULT '',
	name         TEXT NOT NULL DEFAULT '',
	base_url     TEXT NOT NULL DEFAULT '',
	priority     INTEGER NOT NULL DEFAULT 0,
	weight       INTEGER NOT NULL DEFAULT 1,
	balance_json TEXT NOT NULL DEFAULT 'null',
	config_ref   TEXT NOT NULL DEFAULT '',
	note         TEXT NOT NULL DEFAULT ''
);

CREATE TABLE plans (
	position        INTEGER PRIMARY KEY,
	id              TEXT    NOT NULL UNIQUE,
	name            TEXT    NOT NULL DEFAULT '',
	windows_json    TEXT    NOT NULL,
	currency        TEXT    NOT NULL DEFAULT 'USD'
);

CREATE TABLE prices (
	position                        INTEGER PRIMARY KEY,
	model_id                        TEXT    NOT NULL COLLATE NOCASE UNIQUE,
	input_per_1m                    REAL    NOT NULL DEFAULT 0,
	output_per_1m                   REAL    NOT NULL DEFAULT 0,
	cache_read_per_1m               REAL,
	cache_write_per_1m              REAL,
	long_context_threshold          INTEGER,
	long_context_input_per_1m       REAL,
	long_context_output_per_1m      REAL,
	long_context_cache_read_per_1m  REAL,
	long_context_cache_write_per_1m REAL,
	currency                        TEXT    NOT NULL DEFAULT 'USD'
);

CREATE TABLE reference_prices_metadata (
	id                   INTEGER PRIMARY KEY CHECK (id = 1),
	source_url           TEXT    NOT NULL,
	content_hash         TEXT    NOT NULL DEFAULT '',
	version              INTEGER NOT NULL DEFAULT 0,
	fetched_at           INTEGER NOT NULL DEFAULT 0,
	model_count          INTEGER NOT NULL DEFAULT 0,
	last_attempt_at      INTEGER NOT NULL DEFAULT 0,
	retry_after          INTEGER NOT NULL DEFAULT 0,
	last_error           TEXT    NOT NULL DEFAULT '',
	consecutive_failures INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE reference_prices (
    provider_id  TEXT    NOT NULL,
    model_id     TEXT    NOT NULL,
    match_key    TEXT    NOT NULL,
    is_canonical INTEGER NOT NULL,
    rates_json   TEXT,
    PRIMARY KEY (provider_id, model_id)
);

CREATE TABLE request_events (
	id                          INTEGER PRIMARY KEY AUTOINCREMENT,
	at                          INTEGER NOT NULL,
	scope                       TEXT    NOT NULL,
	auth_index                  TEXT    NOT NULL DEFAULT '',
	provider                    TEXT    NOT NULL DEFAULT '',
	account                     TEXT    NOT NULL DEFAULT '',
	executor_type               TEXT    NOT NULL DEFAULT '',
	reasoning_effort            TEXT    NOT NULL DEFAULT '',
	service_tier                TEXT    NOT NULL DEFAULT '',
	response_service_tier       TEXT    NOT NULL DEFAULT '',
	upstream_model              TEXT    NOT NULL DEFAULT '',
	response_model              TEXT    NOT NULL DEFAULT '',
	billing_model               TEXT    NOT NULL DEFAULT '',
	failed                      INTEGER NOT NULL DEFAULT 0,
	latency_ms                  INTEGER NOT NULL DEFAULT 0,
	ttft_ms                     INTEGER NOT NULL DEFAULT 0,
	accounting_quality          TEXT    NOT NULL DEFAULT '',
	price_source                TEXT    NOT NULL DEFAULT '',
	reasoning_tokens            INTEGER NOT NULL DEFAULT 0,
	total_cost                  REAL    NOT NULL DEFAULT 0,
	uncached_input_cost         REAL    NOT NULL DEFAULT 0,
	cache_read_cost             REAL    NOT NULL DEFAULT 0,
	cache_write_cost            REAL    NOT NULL DEFAULT 0,
	output_cost                 REAL    NOT NULL DEFAULT 0,
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
	applied_cache_write_per_1m  REAL    NOT NULL DEFAULT 0,
	endpoint                    TEXT    NOT NULL DEFAULT '',
	transport                   TEXT    NOT NULL DEFAULT '',
	stream                      INTEGER,
	execution_json              TEXT    NOT NULL DEFAULT 'null',
	user_agent                  TEXT    NOT NULL DEFAULT '',
	x_forwarded_for             TEXT    NOT NULL DEFAULT '',
	currency                    TEXT    NOT NULL DEFAULT 'USD'
);

CREATE TABLE request_errors (
	request_event_id INTEGER PRIMARY KEY REFERENCES request_events(id) ON DELETE CASCADE,
	status_code      INTEGER NOT NULL DEFAULT 0,
	error_type       TEXT    NOT NULL DEFAULT '',
	body             TEXT    NOT NULL DEFAULT ''
);

` + quotaReportTables + `
CREATE TABLE plugin_logs (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	at      INTEGER NOT NULL,
	level   TEXT    NOT NULL DEFAULT '',
	message TEXT    NOT NULL DEFAULT ''
);
`
