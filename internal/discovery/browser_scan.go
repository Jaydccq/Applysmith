package discovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"applysmith/internal/workspace"
)

// Browser scan constants of the contract.
const (
	defaultCooldownHours = 6
	maxFollowClicks      = 40
	maxDetailPerCompany  = 4
	maxPageAttempts      = 2
	maxTransientNav      = 2
	maxChallengeClears   = 2
	defaultContentWait   = 12 * time.Second
	defaultPoll          = 300 * time.Millisecond
)

// Reasons and flags of browser scans.
const (
	ReasonCooldown     = "cooldown"
	ReasonHostPressure = "host-pressure"
	ReasonNeedsSignIn  = "needs-sign-in"
	FlagEasyApply      = "easy-apply"
	FlagEmployerURLGap = "employer-url-unresolved"
	FlagDegraded       = "enrichment=degraded"
	FlagEnrichFailed   = "enrichment=failed"
	FlagNoSponsorship  = "no-sponsorship"
	FlagMirror         = "mirror"
	FilterSponsorship  = "sponsorship"
)

// BrowserEnv is what the browser sources need besides the scan window. The
// zero value is the product; tests replace the clock, the random source and
// the host measurement.
type BrowserEnv struct {
	Window Window
	// Host measures memory pressure and load; nil measures the real host.
	Host func(context.Context) HostSample
	// Rand returns a random duration in [lo, hi]; nil uses the process source.
	Rand func(lo, hi time.Duration) time.Duration
	// Sleep waits d or until ctx ends; nil sleeps for real.
	Sleep func(ctx context.Context, d time.Duration) error
	// ContentWait is how long a page may take to show job cards, Poll the gap
	// between looks.
	ContentWait, Poll time.Duration
	// Detail page limits of the new-grad aggregator: navigation and
	// extraction on the first attempt, and the one retry.
	DetailNav, DetailEval, DetailRetry time.Duration
}

func (e *BrowserEnv) host(ctx context.Context) HostSample {
	if e.Host != nil {
		return e.Host(ctx)
	}
	return MeasureHost(ctx)
}

func (e *BrowserEnv) rand(lo, hi time.Duration) time.Duration {
	if e.Rand != nil {
		return e.Rand(lo, hi)
	}
	if hi <= lo {
		return lo
	}
	return lo + rand.N(hi-lo+1)
}

func (e *BrowserEnv) sleep(ctx context.Context, d time.Duration) error {
	if e.Sleep != nil {
		return e.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *BrowserEnv) contentWait() time.Duration {
	if e.ContentWait > 0 {
		return e.ContentWait
	}
	return defaultContentWait
}

func (e *BrowserEnv) poll() time.Duration {
	if e.Poll > 0 {
		return e.Poll
	}
	return defaultPoll
}

func orDur(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// Search is one effective search of a browser run.
type Search struct {
	Label  string `json:"label"`
	URL    string `json:"url"`
	Window string `json:"window"`
	// BoardWindow is true when the URL carried the board's own freshness
	// parameter.
	BoardWindow bool `json:"board_window"`
}

// bscan is the state of one browser scan.
type bscan struct {
	r   *run
	env *BrowserEnv
	win Window

	tab      Tab
	reads    int
	warmed   bool
	detailN  int
	degraded bool
	// clears counts the Cloudflare checkbox attempts of the run.
	clears int

	mu         sync.Mutex
	end        *endRun
	threads    int
	clicks     int
	perCompany map[string]int
}

// host is the main host of the source being scanned.
func (b *bscan) host() string {
	s, _ := LookupSource(b.r.req.Source)
	return s.Host
}

// detailLimit is the detail pages at once: the lesser of the portals setting
// and the source cap, one on a pressured host.
func detailLimit(source string, setting int, degraded bool) int {
	cap := 3
	if source == "linkedin" {
		cap = 2
	}
	n := min(setting, cap)
	if degraded {
		n = 1
	}
	return max(n, 1)
}

// pace is the delay range between list pages, by source.
func pace(source string) (lo, hi time.Duration) {
	if source == "linkedin" {
		return 3 * time.Second, 7 * time.Second
	}
	return 4 * time.Second, 9 * time.Second
}

func (r *run) newBscan() (*bscan, error) {
	env := r.sc.Browser
	if env == nil || env.Window == nil {
		return nil, errors.New("the scan window is not available: no browser is configured for this build")
	}
	b := &bscan{r: r, env: env, win: env.Window, perCompany: map[string]int{}, degraded: r.hostDegraded}
	b.detailN = detailLimit(r.req.Source, r.req.Portals.Concurrency.EffectiveDetail(), b.degraded)
	r.bs = b
	return b, nil
}

func (b *bscan) close() {
	if b.tab != nil {
		b.tab.Close()
		b.tab = nil
	}
}

// pauseBetween waits one random delay of the source's list range; the first
// read of a run waits for nothing.
func (b *bscan) pauseBetween(ctx context.Context) error {
	if b.reads == 0 {
		return nil
	}
	lo, hi := pace(b.r.req.Source)
	return b.env.sleep(ctx, b.env.rand(lo, hi))
}

// browserPreflight applies the cooldown and the host-pressure checks before
// any tab opens. A non-nil outcome ends the run.
func (r *run) browserPreflight(ctx context.Context) *outcome {
	if !r.req.IgnoreCooldown {
		if until, ok := r.cooldownUntil(ctx); ok {
			return &outcome{status: RunSkipped, reason: ReasonCooldown,
				err: fmt.Errorf("%s is in its cooldown until %s after a blocked run; --ignore-cooldown runs it anyway", r.req.Source, until.UTC().Format(time.RFC3339))}
		}
	}
	env := r.sc.Browser
	if env == nil {
		return &outcome{status: RunFailed, err: errors.New("the scan window is not available: no browser is configured for this build")}
	}
	h := env.host(ctx)
	r.mu.Lock()
	r.rec.Host = &h
	r.mu.Unlock()
	if h.Verdict == PressureCritical && !r.req.Force {
		return &outcome{status: RunSkipped, reason: ReasonHostPressure, err: errors.New("memory pressure is critical, " + h.String() + "; --force runs the scan anyway")}
	}
	if h.Degrade() {
		r.hostDegraded = true
		r.detail("host_degraded", 1)
		r.noteError("detail concurrency is 1 on this host: %s", h)
	}
	return nil
}

// cooldownUntil reports a source still cooling down for this run.
func (r *run) cooldownUntil(ctx context.Context) (time.Time, bool) {
	return r.repo.CooldownUntil(ctx, r.req.Source, CooldownHours(r.req.Profile, r.req.Source), r.rec.ID)
}

// CooldownHours is how long a blocked run holds a source:
// scan.defaults.<source>.cooldown_hours, default 6, 0 disables.
func CooldownHours(p workspace.Profile, source string) int {
	if h := p.Scan.Defaults[source].CooldownHours; h != nil {
		return *h
	}
	return defaultCooldownHours
}

// CooldownUntil reports a source still cooling down. A cooldown starts when a
// run ends `blocked`; a later run that was not skipped ends it. The run named
// by except is ignored, so a run does not read its own record.
func (r *Repo) CooldownUntil(ctx context.Context, source string, hours int, except string) (time.Time, bool) {
	if hours <= 0 {
		return time.Time{}, false
	}
	recs, err := r.ScanList(ctx, source, 10)
	if err != nil {
		return time.Time{}, false
	}
	for _, rec := range recs {
		if rec.Status == RunSkipped || rec.Status == RunRunning || rec.ID == except {
			continue
		}
		if rec.Status != RunBlocked {
			return time.Time{}, false
		}
		end := rec.Finished
		if end.IsZero() {
			end = rec.Updated
		}
		until := end.Add(time.Duration(hours) * time.Hour)
		return until, r.now().Before(until)
	}
	return time.Time{}, false
}

// browserWindow is the freshness window of a browser run: the flag, else the
// profile's posted_within, else 24 hours. Zero means any.
func (r *run) browserWindow() time.Duration {
	switch {
	case r.req.Window < 0:
		return 0
	case r.req.Window > 0:
		return r.req.Window
	case r.req.Profile.Scan.PostedWithin == "any":
		return 0
	}
	if d, ok := PostedWithin(r.req.Profile.Scan.PostedWithin); ok {
		return d
	}
	return 24 * time.Hour
}

// freshOrDrop applies the freshness rules to one page of rows. A row without
// a posted time is kept only when the board applied the window itself; a
// dated row is trusted when the board applied the window, unless the board
// only honors a wider one and the local check must enforce the ask.
func (r *run) freshOrDrop(posts []Posting, window time.Duration, boardApplied, enforce bool) []Posting {
	if window <= 0 {
		return posts
	}
	cutoff := r.repo.now().Add(-window)
	var out []Posting
	stale, undated := 0, 0
	for _, p := range posts {
		if !p.PostedAt.IsZero() {
			r.newestRow(r.repo.now().Sub(p.PostedAt))
		}
		switch {
		case p.PostedAt.IsZero() && !boardApplied:
			undated++
		case p.PostedAt.IsZero():
			out = append(out, p)
		case boardApplied && !enforce:
			out = append(out, p)
		case p.PostedAt.Before(cutoff):
			stale++
		default:
			out = append(out, p)
		}
	}
	r.dropRows("stale", stale)
	r.undatedRows(undated)
	return out
}

// listingRejects drops the rows the listing text alone settles, before any
// detail page is read: an unusable URL, a company on the blocked list, a
// title or location the filters refuse, a row outside the scope, a row the
// stores already know. The pipeline repeats these checks for the survivors.
// checkURL is false for a source whose rows carry no employer URL yet.
func (r *run) listingRejects(ctx context.Context, posts []Posting, checkURL bool) ([]Posting, error) {
	var keep, dups []Posting
	filtered := map[string]int{}
	dupStores := map[string]int{}
	for _, p := range posts {
		if p.fetchURL == "" {
			p.fetchURL = p.URL
		}
		if checkURL {
			canon, err := postingURL(p.URL)
			switch {
			case err != nil:
				filtered[FilterInvalidURL]++
				continue
			case NonPostingURL(canon):
				filtered[FilterNonPostingURL]++
				continue
			}
			p.URL = canon
		}
		p.Source = rowSource(r.req.Source)
		switch {
		case r.filters.CompanyBlocked(p.Company):
			filtered[FilterBlockedCompany]++
			continue
		case !r.filters.Title.Check(p.Title).Pass:
			filtered[FilterTitle]++
			continue
		case !r.filters.LocationOK(p.Location):
			filtered[FilterLocation]++
			continue
		case !ApplyScope(r.scope, p.Location).Pass:
			filtered[FilterScope]++
			continue
		}
		if checkURL {
			r.dedupMu.Lock()
			store, _, known := r.known.Match(p, r.req.RetryKnown)
			r.dedupMu.Unlock()
			if known {
				dupStores[store]++
				dups = append(dups, p)
				continue
			}
		}
		keep = append(keep, p)
	}
	if err := r.recordDropped(ctx, filtered, dupStores, dups); err != nil {
		return nil, err
	}
	return keep, nil
}

func (r *run) recordDropped(ctx context.Context, filtered, dupStores map[string]int, dups []Posting) error {
	if len(dups) > 0 {
		err := r.repo.s.Write(ctx, func(tx *sql.Tx) error {
			for _, p := range dups {
				if err := r.repo.historyUpsert(ctx, tx, p, HistoryDuplicate, nil, r.rec.ID); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, n := range filtered {
		r.rec.Counts.Discovered += n
		r.rec.Counts.Filtered[k] += n
	}
	for k, n := range dupStores {
		r.rec.Counts.Discovered += n
		r.rec.Counts.Duplicates[k] += n
	}
	return nil
}

// recordSearch keeps the effective search of a query in the run record.
func (r *run) recordSearch(label, rawURL string, window time.Duration, boardWindow bool) {
	w := "any"
	if window > 0 {
		w = window.String()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.rec.Searches) < maxAttempts {
		r.rec.Searches = append(r.rec.Searches, Search{Label: label, URL: rawURL, Window: w, BoardWindow: boardWindow})
	}
}

// signInDetail is the text of a needs-sign-in end.
func signInDetail(source string) error {
	return fmt.Errorf("%s asks for a sign-in; sign in through the apply window, then run the scan again", source)
}

// newestRow keeps the age of the freshest dated row the run saw, in minutes,
// so a run that yields nothing says how old everything was.
func (r *run) newestRow(age time.Duration) {
	m := max(int(age.Minutes()), 0)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rec.Detail == nil {
		r.rec.Detail = map[string]int{}
	}
	if cur, ok := r.rec.Detail["newest-row-age-minutes"]; !ok || m < cur {
		r.rec.Detail["newest-row-age-minutes"] = m
	}
}

// Baseline is how often one browser source met a wall over a period: the pages
// its scans opened and the runs that ended blocked, by the wall they met. It is
// the measure of whether a change to the browser helped (BL-167).
type Baseline struct {
	Source string
	// Runs are the scans that were not skipped.
	Runs int
	// Visits are the list and detail pages the scans opened.
	Visits int
	// Challenges are runs that ended blocked by a challenge page; RateLimited
	// those that ended on a rate-limit page.
	Challenges, RateLimited int
}

// ChallengeBaseline reads the baseline of every browser source from the scan
// records started since the given time. A source with no run is omitted.
func (r *Repo) ChallengeBaseline(ctx context.Context, since time.Time) ([]Baseline, error) {
	var out []Baseline
	for _, src := range Sources {
		if src.Kind != KindBrowser {
			continue
		}
		b := Baseline{Source: src.Name}
		for offset := 0; ; offset += 200 {
			recs, total, err := r.ScanPage(ctx, ScanFilter{Source: src.Name, From: since, Limit: 200, Offset: offset})
			if err != nil {
				return nil, err
			}
			for _, rec := range recs {
				if rec.Status == RunSkipped || rec.Status == RunRunning {
					continue
				}
				b.Runs++
				b.Visits += rec.Detail["list-opens"] + rec.Detail["detail-opens"]
				if rec.Status == RunBlocked {
					switch rec.Reason {
					case blockChallenge:
						b.Challenges++
					case blockRateLimit:
						b.RateLimited++
					}
				}
			}
			if offset+len(recs) >= total || len(recs) == 0 {
				break
			}
		}
		if b.Runs > 0 {
			out = append(out, b)
		}
	}
	return out, nil
}
