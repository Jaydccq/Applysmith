package discovery

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Run reasons the feed sources end with.
const (
	ReasonNoKey         = "no key"
	ReasonLayoutChanged = "layout-changed"
	ReasonNoCards       = "no-cards"
	ReasonEmptyWindow   = "empty-window"
	ReasonEmptyExtract  = "empty-extraction"
	ReasonBudget        = "budget-reached"
	// ReasonLimit marks a board sweep that stopped at its row cap.
	ReasonLimit   = "limit-reached"
	ReasonAllSeen = "all_rows_already_seen"
)

// feedReader walks one source for a run: it reads pages or API results and
// hands rows to emit. A returned *endRun ends the run with that status; any
// other error fails it.
type feedReader func(ctx context.Context, r *run) error

func feedReaderFor(source string) feedReader {
	switch source {
	case "builtin":
		return readBuiltIn
	case "amazon":
		return readAmazon
	case "a16z":
		return readA16Z
	case "theirstack":
		return readTheirStack
	case "github":
		return readGitHub
	case "speedrun":
		return readSpeedrun
	case "exa":
		return readExa
	case "linkedin":
		return readBoard(linkedInSpec)
	case "linkedin-feed":
		return readLinkedInFeed
	case "indeed":
		return readBoard(indeedSpec)
	case "ziprecruiter":
		return readBoard(zipSpec)
	case "google-jobs":
		return readBoard(googleSpec)
	case "glassdoor":
		return readBoard(glassdoorSpec)
	case "newgrad":
		return readNewGrad
	case "jobright":
		return readJobrightRecommend
	}
	return nil
}

// endRun is a reader's way to end the run early with a status.
type endRun struct {
	status, reason string
	err            error
}

func (e *endRun) Error() string {
	if e.err != nil {
		return e.reason + ": " + e.err.Error()
	}
	return e.reason
}

func skipRun(reason string) *endRun { return &endRun{status: RunSkipped, reason: reason} }

// Query is one saved search of a source: a search string and its parameters.
type Query struct {
	Search string
	P      map[string]string
}

// Get returns a parameter, empty when absent.
func (q Query) Get(k string) string { return q.P[k] }

// place is where a board search looks: a saved search's location, else the
// city and state of an ad hoc run.
func (q Query) place() string {
	return firstNonEmpty(q.Get("location"), joinNonEmpty(q.Get("city"), q.Get("state")))
}

// label names the query in the run record and the row's portal detail.
func (q Query) label() string {
	for _, k := range []string{"url", "path"} {
		if v := q.P[k]; v != "" {
			return v
		}
	}
	if q.Search != "" {
		return q.Search
	}
	for _, k := range []string{"title", "city", "state"} {
		if v := q.P[k]; v != "" {
			return v
		}
	}
	return "all"
}

func decodeQuery(n yaml.Node) (Query, error) {
	q := Query{P: map[string]string{}}
	switch n.Kind {
	case yaml.ScalarNode:
		q.Search = strings.TrimSpace(n.Value)
	case yaml.MappingNode:
		var m map[string]any
		if err := n.Decode(&m); err != nil {
			return q, err
		}
		for k, v := range m {
			s := strings.TrimSpace(fmt.Sprint(v))
			switch k {
			case "search", "query", "q", "keyword":
				q.Search = s
			default:
				q.P[k] = s
			}
		}
	default:
		return q, errors.New("a query is a search string or a mapping of parameters")
	}
	return q, nil
}

// queries returns the queries of the run. An ad hoc run (a query-like flag)
// replaces the saved ones; a result-size flag or a catalog default never does.
// The remote flag narrows every query, saved or ad hoc.
func (r *run) queries() ([]Query, error) {
	req := r.req
	var out []Query
	if req.Search != "" || req.Title != "" || req.City != "" || req.State != "" || req.URL != "" {
		q := Query{Search: req.Search, P: map[string]string{}}
		for k, v := range map[string]string{"title": req.Title, "city": req.City, "state": req.State, "url": req.URL} {
			if v != "" {
				q.P[k] = v
			}
		}
		out = []Query{q}
	} else {
		for i, n := range req.Portals.Sources[req.Source].Queries {
			q, err := decodeQuery(n)
			if err != nil {
				return nil, fmt.Errorf("sources.%s.queries[%d]: %w", req.Source, i, err)
			}
			out = append(out, q)
		}
	}
	if req.Remote {
		for i := range out {
			if out[i].P == nil {
				out[i].P = map[string]string{}
			}
			out[i].P["remote"] = "true"
		}
	}
	return out, nil
}

// queriesOrSkip is queries for a source that has nothing to search without
// them.
func (r *run) queriesOrSkip() ([]Query, error) {
	qs, err := r.queries()
	if err != nil {
		return nil, err
	}
	if len(qs) == 0 {
		return nil, skipRun("no saved searches for " + r.req.Source + " in portals.yml")
	}
	return qs, nil
}

// window is the freshness window: the flag (--posted-within any, a negative
// Window, is zero: every age), else the profile's posted_within, else the
// source's default.
func (r *run) window(def time.Duration) time.Duration {
	switch {
	case r.req.Window < 0:
		return 0
	case r.req.Window > 0:
		return r.req.Window
	}
	if d, ok := PostedWithin(r.req.Profile.Scan.PostedWithin); ok {
		return d
	}
	return def
}

// PostedWithin reads a posted-within token (1h, 24h, 3d, week, month). ok is
// false for "any" and for an unknown value.
func PostedWithin(tok string) (time.Duration, bool) {
	switch tok {
	case "1h":
		return time.Hour, true
	case "24h":
		return 24 * time.Hour, true
	case "3d":
		return 72 * time.Hour, true
	case "week":
		return 7 * 24 * time.Hour, true
	case "month":
		return 30 * 24 * time.Hour, true
	}
	return 0, false
}

// ParseWindow reads a window such as 6h, 24h, 3d or 2w.
func ParseWindow(s string) (time.Duration, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) < 2 {
		return 0, fmt.Errorf("window %q: want a number and a unit such as 6h, 3d or 2w", s)
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("window %q: want a number and a unit such as 6h, 3d or 2w", s)
	}
	unit := map[byte]time.Duration{'h': time.Hour, 'd': 24 * time.Hour, 'w': 7 * 24 * time.Hour}[s[len(s)-1]]
	if unit == 0 {
		return 0, fmt.Errorf("window %q: the unit is h, d or w", s)
	}
	return time.Duration(n) * unit, nil
}

// base is the API base URL of a source; tests point Endpoints at local
// servers.
func (r *run) base(source, def string) string {
	if v := r.sc.Endpoints[source]; v != "" {
		return strings.TrimRight(v, "/")
	}
	return def
}

// emit sends one query's rows through the shared pipeline (block, filter,
// scope, collapse, de-duplicate, rank, insert).
func (r *run) emit(ctx context.Context, label string, posts []Posting) error {
	r.mu.Lock()
	r.feedIdx++
	idx := r.feedIdx
	r.rec.Boards++
	r.mu.Unlock()
	sb := sweepBoard{Board: Board{ATS: r.req.Source, Slug: label}, feed: true, idx: idx}
	if err := r.commit(ctx, sb, BoardResult{Postings: posts}); err != nil {
		return err
	}
	r.flush(ctx)
	return nil
}

// dropRows counts rows a source discarded before the pipeline, each under its
// reason, so every discovered row still ends in one terminal count.
func (r *run) dropRows(reason string, n int) {
	if n == 0 {
		return
	}
	r.mu.Lock()
	r.rec.Counts.Discovered += n
	r.rec.Counts.Filtered[reason] += n
	r.mu.Unlock()
}

// undatedRows counts rows whose age could not be read.
func (r *run) undatedRows(n int) {
	r.mu.Lock()
	r.rec.Counts.Discovered += n
	r.rec.Counts.Undated += n
	r.mu.Unlock()
}

// detail records a source-specific number in the run record.
func (r *run) detail(k string, v int) {
	r.mu.Lock()
	if r.rec.Detail == nil {
		r.rec.Detail = map[string]int{}
	}
	r.rec.Detail[k] = v
	r.mu.Unlock()
}

func (r *run) addDetail(k string, v int) {
	r.mu.Lock()
	if r.rec.Detail == nil {
		r.rec.Detail = map[string]int{}
	}
	r.rec.Detail[k] += v
	r.mu.Unlock()
}

// queryFailed records a query that failed. The run goes on with its other
// queries; it fails only when every query did.
func (r *run) queryFailed(label string, err error) {
	r.mu.Lock()
	r.queryFails++
	r.queryTransient = r.queryTransient || retryableFailure(err)
	if r.firstFail == nil {
		r.firstFail = fmt.Errorf("%s: %w", label, err)
	}
	r.mu.Unlock()
	r.bumpBoardError()
	r.noteError("%s: %v", label, err)
}

// partialRead records a query that failed after it had given rows. The rows
// are kept and the query is not a failed one; the run's record counts the
// failure and notes it, which is how a run that read less than it should is
// told from a whole one.
func (r *run) partialRead(label string, err error) {
	r.bumpBoardError()
	r.noteError("%s: stopped early, the rows read before it are kept: %v", label, err)
}

func (r *run) queryRan() {
	r.mu.Lock()
	r.queriesRun++
	r.mu.Unlock()
}

// layoutChange marks a page that had content and no parsable rows.
func (r *run) layoutChange(reason, label string) {
	r.mu.Lock()
	if r.layout == "" {
		r.layout = reason
	}
	r.mu.Unlock()
	r.noteError("%s: %s", label, reason)
}

func (r *run) secret(name string) string {
	if r.sc.Secret == nil {
		return ""
	}
	return strings.TrimSpace(r.sc.Secret(name))
}

// executeFeed runs a non-board source: a key check, the reader, and the same
// top-N enqueue as a sweep.
func (r *run) executeFeed(ctx context.Context) outcome {
	info, _ := LookupSource(r.req.Source)
	if info.Needs == "key" {
		if r.key = r.secret(info.KeyName); r.key == "" {
			return outcome{status: RunSkipped, reason: ReasonNoKey}
		}
	}
	if info.Kind == KindBrowser {
		if out := r.browserPreflight(ctx); out != nil {
			return *out
		}
	}
	err := feedReaderFor(r.req.Source)(ctx, r)
	var end *endRun
	switch {
	case errors.As(err, &end):
		if end.status == RunNeedsSignIn && r.sc.NeedsSignIn != nil {
			r.sc.NeedsSignIn(ctx, r.req.Source, end.err.Error())
		}
		return outcome{status: end.status, reason: end.reason, err: end.err}
	case errors.Is(err, ErrOffline):
		return outcome{status: RunFailed, reason: "host-offline", err: err}
	}
	// A terminal sign-in/challenge outcome keeps its meaning across queries.
	// Otherwise an observed source/storage failure outranks cancellation; the
	// outer finalizer still recognizes failures caused only by this context.
	if r.layout != "" {
		return outcome{status: RunFailed, reason: r.layout, err: errors.Join(err, errors.New("the page layout no longer yields rows"))}
	}
	if r.queriesRun > 0 && r.queryFails >= r.queriesRun {
		return outcome{status: RunFailed, err: errors.Join(err, r.firstFail), retryable: r.queryTransient}
	}
	if endedByContext(ctx, err) {
		return r.interrupted(ctx)
	}
	if err != nil {
		return outcome{status: RunFailed, err: err}
	}
	if err := r.enqueueTop(ctx); err != nil {
		if endedByContext(ctx, err) {
			return r.interrupted(ctx)
		}
		return outcome{status: RunFailed, err: err}
	}
	out := outcome{status: RunCompleted, reason: r.endReason}
	if r.budgetHit {
		out.reason = ReasonBudget
	}
	if info.Kind == KindBrowser && r.sc.PastSignIn != nil {
		r.sc.PastSignIn(ctx, r.req.Source)
	}
	return out
}
