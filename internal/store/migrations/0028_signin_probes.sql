-- The last sign-in probe result per site: the state the classifier gave and
-- when the page was read. The probe is served from this row while it is less
-- than five minutes old. Timestamps are RFC 3339 UTC text with millisecond
-- precision. No row holds a page, a cookie or a URL beyond the site's domain.
CREATE TABLE signin_probes (
    domain     TEXT PRIMARY KEY CHECK (domain <> ''),
    state      TEXT NOT NULL CHECK (state IN ('signed_in', 'signed_out', 'checkpoint', 'captcha', 'rate_limited', 'unknown')),
    checked_at TEXT NOT NULL
) STRICT;
