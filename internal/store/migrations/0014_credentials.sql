-- The non-secret credential index (credentials-vault): what exists, never a
-- value. A password, token or key lives only in the keychain or the
-- environment. Timestamps are RFC 3339 UTC text with millisecond precision.
CREATE TABLE credential_index (
    name               TEXT PRIMARY KEY,
    kind               TEXT NOT NULL CHECK (kind IN ('signin_default', 'signin_alternate', 'signin_site', 'tenant', 'api_key', 'token')),
    label              TEXT NOT NULL DEFAULT '',
    host               TEXT,
    system             TEXT,
    tenant             TEXT,
    email              TEXT,
    position           INTEGER,
    has_secret         INTEGER NOT NULL DEFAULT 0 CHECK (has_secret IN (0, 1)),
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    account_created_at TEXT,
    last_signin_at     TEXT,
    last_outcome       TEXT CHECK (last_outcome IS NULL OR last_outcome IN ('created', 'signed-in'))
) STRICT;

CREATE INDEX credential_index_host ON credential_index (host) WHERE host IS NOT NULL;

-- credentials_changed_at: the latest change to a shared password.
CREATE TABLE credential_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;

-- One row per host whose passwords the apply chain recorded exhausted: the
-- hold on submitting passwords there, read by the chain and by the
-- extension's sign-in resolve. day is the local calendar day of the record.
CREATE TABLE signin_holds (
    host        TEXT PRIMARY KEY CHECK (host <> '' AND host = lower(host)),
    recorded_at TEXT NOT NULL,
    day         TEXT NOT NULL,
    lockout     INTEGER NOT NULL CHECK (lockout IN (0, 1)),
    run_id      TEXT NOT NULL DEFAULT ''
) STRICT;
