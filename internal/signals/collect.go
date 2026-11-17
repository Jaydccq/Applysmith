package signals

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"applysmith/internal/discovery"
	"applysmith/internal/workspace"
)

// Endpoints are the base URLs the collectors call; the zero value is the
// public hosts and tests point it at local servers.
type Endpoints struct {
	EDGAR string
	HN    string
	Exa   string
	X     string
	ATS   discovery.Endpoints
}

func (e Endpoints) edgar() string { return orDefault(e.EDGAR, "https://www.sec.gov") }
func (e Endpoints) hn() string    { return orDefault(e.HN, "https://hn.algolia.com") }
func (e Endpoints) exa() string   { return orDefault(e.Exa, "https://api.exa.ai") }
func (e Endpoints) x() string     { return orDefault(e.X, "https://api.twitterapi.io") }

func orDefault(v, d string) string {
	if v = strings.TrimRight(v, "/"); v != "" {
		return v
	}
	return d
}

// Collected is what one collector returns. Commit advances the cursors and
// validators, and runs only after the documents were stored.
type Collected struct {
	Docs     []Doc
	Deferred []string
	// Notes are failures of single feeds or queries that did not stop the collector.
	Notes  []string
	Commit func(ctx context.Context) error
}

// SkipError ends a collector that cannot run, with the status text.
type SkipError struct{ Reason string }

func (e *SkipError) Error() string { return e.Reason }

// CollectError is a failure with the action it needs and whether a later tick
// can succeed without anyone doing it.
type CollectError struct {
	Reason         string
	RequiredAction string
	Retryable      bool
}

func (e *CollectError) Error() string { return e.Reason }

// collectEnv is everything a collector reads.
type collectEnv struct {
	Fetch     *Fetcher
	Repo      *Repo
	Now       func() time.Time
	Endpoints Endpoints
	Secret    func(string) string
	Profile   workspace.Profile
	Queries   []yaml.Node
	Search    SearchProvider
	// Days is the number of business days a backfill collects; zero is a normal run.
	Days int
}

type collector struct {
	id  string
	run func(ctx context.Context, env *collectEnv) (Collected, error)
}

func collectors() []collector {
	return []collector{
		{SourceEdgar, collectEdgar}, {SourceAlerts, collectAlerts}, {SourceHN, collectHN},
		{SourceHNHiring, collectHNHiring}, {SourceWeb, collectWeb}, {SourceX, collectX},
	}
}

// SourceEnabled reads the enabled flag of a source; the hiring thread is on by
// default and every other source is off until the user turns it on.
func SourceEnabled(p workspace.Portals, id string) bool {
	if s, ok := p.Sources[id]; ok && s.Enabled != nil {
		return *s.Enabled
	}
	return id == SourceHNHiring
}

// queryStrings reads the query strings of a source: a scalar, or a mapping
// with a query, q or search key.
func queryStrings(nodes []yaml.Node) []string {
	var out []string
	for _, n := range nodes {
		switch n.Kind {
		case yaml.ScalarNode:
			if s := strings.TrimSpace(n.Value); s != "" {
				out = append(out, s)
			}
		case yaml.MappingNode:
			var m map[string]any
			if n.Decode(&m) != nil {
				continue
			}
			for _, k := range []string{"query", "q", "search"} {
				if s := strings.TrimSpace(fmt.Sprint(m[k])); m[k] != nil && s != "" {
					out = append(out, s)
					break
				}
			}
		}
	}
	return out
}

// feedURLs reads the feed URLs of the alerts source: a scalar URL or a mapping
// with a url key.
func feedURLs(nodes []yaml.Node) []string {
	var out []string
	for _, n := range nodes {
		switch n.Kind {
		case yaml.ScalarNode:
			out = append(out, strings.TrimSpace(n.Value))
		case yaml.MappingNode:
			var m map[string]any
			if n.Decode(&m) == nil && m["url"] != nil {
				out = append(out, strings.TrimSpace(fmt.Sprint(m["url"])))
			}
		}
	}
	return out
}

// ValidateFeedURL is the configuration-time check of a feed address.
func ValidateFeedURL(raw string) error { return workspace.ValidateFeedURL(raw) }

// ---- collector state

type collectorState struct {
	ETag         string
	LastModified string
	Cursor       int64
}

func (r *Repo) CollectorState(ctx context.Context, source, scope string) (collectorState, error) {
	var st collectorState
	err := r.s.Read().QueryRowContext(ctx, `SELECT etag, last_modified, cursor FROM signal_collector_state WHERE source_id = ? AND scope = ?`, source, scope).
		Scan(&st.ETag, &st.LastModified, &st.Cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	return st, err
}

func (r *Repo) PutCollectorState(ctx context.Context, source, scope string, st collectorState) error {
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO signal_collector_state (source_id, scope, etag, last_modified, cursor, updated_at) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (source_id, scope) DO UPDATE SET etag = excluded.etag, last_modified = excluded.last_modified, cursor = excluded.cursor, updated_at = excluded.updated_at`,
			source, scope, st.ETag, st.LastModified, st.Cursor, r.stamp())
		return err
	})
}

// commitAll runs the commits in order, stopping at the first failure.
func commitAll(fns []func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		for _, f := range fns {
			if err := f(ctx); err != nil {
				return err
			}
		}
		return nil
	}
}

// statusError turns an unexpected HTTP status of a source into a failure.
func statusError(source string, res Response) error {
	return &CollectError{Reason: fmt.Sprintf("%s answered %d", source, res.Status), Retryable: res.Status >= 500 || res.Status == 429}
}
