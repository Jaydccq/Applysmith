-- LLM routing: settings, the per-call ledger, prices, the embedding cache and
-- the daily cost-cap alert claim. Timestamps are RFC 3339 UTC text with
-- millisecond precision so they sort as text.

CREATE TABLE llm_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE llm_ledger (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    call_id             TEXT NOT NULL,
    started_at          TEXT NOT NULL,
    task                TEXT NOT NULL,
    provider_id         TEXT NOT NULL,
    provider_kind       TEXT NOT NULL,
    model               TEXT NOT NULL,
    attempt             INTEGER NOT NULL,
    replaced            TEXT,
    candidate_source    TEXT NOT NULL CHECK (candidate_source IN ('primary', 'fallback', 'override')),
    outcome             TEXT NOT NULL,
    http_status         INTEGER,
    input_tokens        INTEGER NOT NULL DEFAULT 0,
    output_tokens       INTEGER NOT NULL DEFAULT 0,
    cached_input_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens    INTEGER NOT NULL DEFAULT 0,
    cost_usd            REAL,
    billing             TEXT NOT NULL CHECK (billing IN ('metered', 'subscription', 'free', 'unpriced')),
    latency_ms          INTEGER NOT NULL DEFAULT 0,
    ttft_ms             INTEGER,
    finish_reason       TEXT NOT NULL DEFAULT '',
    clamped             INTEGER NOT NULL DEFAULT 0 CHECK (clamped IN (0, 1)),
    downgraded          INTEGER NOT NULL DEFAULT 0 CHECK (downgraded IN (0, 1)),
    run_id              TEXT NOT NULL DEFAULT '',
    error_text          TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX llm_ledger_started ON llm_ledger (started_at);
CREATE INDEX llm_ledger_task ON llm_ledger (task, started_at);

-- Prompt and completion text, only while debug capture is on.
CREATE TABLE llm_capture (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ledger_id   INTEGER NOT NULL,
    captured_at TEXT NOT NULL,
    prompt      TEXT NOT NULL,
    completion  TEXT NOT NULL
) STRICT;

CREATE INDEX llm_capture_at ON llm_capture (captured_at);

-- Prices refreshed from the OpenRouter catalog; they win over the embedded table.
CREATE TABLE llm_prices (
    key          TEXT PRIMARY KEY,
    input_per_m  REAL NOT NULL,
    cached_per_m REAL NOT NULL,
    output_per_m REAL NOT NULL,
    source       TEXT NOT NULL,
    fetched_at   TEXT NOT NULL
) STRICT;

-- One row per price-check run: the drift between the embedded table and the
-- live catalog.
CREATE TABLE llm_price_checks (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    checked_at TEXT NOT NULL,
    drift_json TEXT NOT NULL
) STRICT;

CREATE TABLE llm_embeddings (
    model     TEXT NOT NULL,
    text_hash TEXT NOT NULL,
    vector    BLOB NOT NULL,
    used_at   TEXT NOT NULL,
    PRIMARY KEY (model, text_hash)
) STRICT;

CREATE INDEX llm_embeddings_used ON llm_embeddings (used_at);

-- One cost-cap alert per UTC day. A claim that no channel accepted becomes
-- claimable again ten minutes after it was made.
CREATE TABLE llm_cap_alerts (
    day        TEXT PRIMARY KEY,
    claimed_at TEXT NOT NULL,
    delivered  INTEGER NOT NULL DEFAULT 0 CHECK (delivered IN (0, 1))
) STRICT;
