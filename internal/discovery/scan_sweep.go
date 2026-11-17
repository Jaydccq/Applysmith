// Clean-room: docs/provenance.md#CR-SCAN-SKELETON

package discovery

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"sync"

	"applysmith/internal/workspace"
)

// sweepBoard is one board to visit.
type sweepBoard struct {
	Board
	tracked bool
	// rule replaces the global title filter for a tracked company.
	rule *TitleRule
	etag string
	// idx is the board's place in the sweep order.
	idx int
	// feed marks a query of a non-board source: it has no registry row.
	feed bool
}

// ReasonRegistryEmpty skips a registry scan with no active board and no
// tracked company to read. Each front end words what to do: the command line
// names `applysmith registry seed`, the dashboard links the download on its
// Sources and portals tab.
const ReasonRegistryEmpty = "registry-empty"

// collectBoards lists the boards of the run. skip is a reason when there is
// nothing to do.
func (r *run) collectBoards(ctx context.Context) (boards []sweepBoard, skip string, err error) {
	tracked, err := r.trackedBoards(ctx)
	if err != nil {
		return nil, "", err
	}
	if r.req.Source == "tracked" {
		if len(tracked) == 0 {
			return nil, "no enabled tracked companies in portals.yml", nil
		}
		return tracked, "", nil
	}
	if !r.req.Portals.Registry.IsEnabled() {
		return nil, "registry disabled in portals.yml", nil
	}
	rows, err := r.repo.RegistryActive(ctx)
	if err != nil {
		return nil, "", err
	}
	if len(rows) == 0 && len(tracked) == 0 {
		return nil, ReasonRegistryEmpty, nil
	}
	seen := map[string]bool{}
	for _, t := range tracked {
		seen[t.Key()] = true
	}
	boards = append(boards, tracked...)
	for _, row := range rows {
		if !seen[row.Key()] {
			boards = append(boards, sweepBoard{Board: row.Board, etag: row.ETag})
		}
	}
	return boards, "", nil
}

// trackedBoards resolves the enabled entries of portals.yml companies. They
// are added to the registry (source "tracked") and are never struck.
func (r *run) trackedBoards(ctx context.Context) ([]sweepBoard, error) {
	var out []sweepBoard
	for _, c := range r.req.Portals.Companies {
		if !c.IsEnabled() {
			continue
		}
		b := Board{ATS: c.ATS, Slug: lowerSlug(c.Slug), Company: c.Name}
		if c.CareersURL != "" {
			rb, err := ResolveBoard(c.CareersURL)
			if err != nil {
				r.bumpBoardError()
				r.noteError("tracked %q: careers page %s is not a supported board", c.Name, hostOnly(c.CareersURL))
				continue
			}
			b = Board{ATS: rb.ATS, Slug: rb.Slug, Company: c.Name}
		}
		if !validATS(b.ATS) || !ValidSlug(b.Slug) {
			r.bumpBoardError()
			r.noteError("tracked %q: %q is not a valid board", c.Name, b.Key())
			continue
		}
		if _, err := r.repo.RegistryAdd(ctx, "tracked", b); err != nil {
			return nil, err
		}
		row, err := r.repo.RegistryGet(ctx, b)
		if err != nil {
			return nil, err
		}
		if row.Status == StatusBlocked {
			r.noteError("tracked %q: %s is blocked in the registry", c.Name, b.Key())
			continue
		}
		sb := sweepBoard{Board: b, tracked: true, etag: row.ETag}
		if c.Filters.Title != nil {
			rule := NewTitleRule(*c.Filters.Title)
			sb.rule = &rule
		}
		out = append(out, sb)
	}
	return out, nil
}

func lowerSlug(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func hostOnly(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Host
	}
	return "unknown host"
}

func (r *run) bumpBoardError() {
	r.mu.Lock()
	r.rec.Counts.BoardErrors++
	r.mu.Unlock()
}

// sweep visits the boards with a bounded worker pool, stopping when the
// context ends (the scan's budget is its deadline) or the host is judged
// offline.
func (r *run) sweep(ctx context.Context, boards []sweepBoard) {
	wctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	r.cancelWork = cancel
	jobs := make(chan sweepBoard)
	var wg sync.WaitGroup
	for i := 0; i < min(maxInFlight, len(boards)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for sb := range jobs {
				if wctx.Err() == nil && !r.capReached() {
					r.visit(wctx, sb)
				}
			}
		}()
	}
feed:
	for i, sb := range boards {
		sb.idx = i
		if r.capReached() {
			break
		}
		select {
		case jobs <- sb:
		case <-wctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	r.flushStrikes(ctx)
	r.flush(ctx)
}

// visit reads one board and settles its registry row.
func (r *run) visit(ctx context.Context, sb sweepBoard) {
	res, err := r.client.FetchBoard(ctx, r.sc.Endpoints, sb.Board, sb.etag)
	if ctx.Err() != nil {
		return
	}
	var (
		fail *BoardFailure
		rl   *RateLimitedError
		tr   *TransientError
	)
	switch {
	case err == nil:
		r.onReached(ctx)
		r.mu.Lock()
		r.rec.Boards++
		r.mu.Unlock()
		if res.NotModified {
			r.addDetail("unchanged", 1)
			r.fatalOn(r.repo.RegistryRecord(ctx, sb.Board, VisitNotModified, 0, ""))
			return
		}
		r.addDetail("changed", 1)
		r.fatalOn(r.commit(ctx, sb, res))
	case errors.As(err, &fail):
		r.responded()
		r.bumpBoardError()
		r.noteError("%s: %v", sb.Key(), fail)
		if !sb.tracked {
			r.strike(ctx, sb.Board)
		}
	case errors.As(err, &rl):
		r.responded()
		r.mu.Lock()
		if r.rec.Deferred == nil {
			r.rec.Deferred = map[string]int{}
		}
		r.rec.Deferred[rl.Host]++
		r.mu.Unlock()
	case errors.As(err, &tr):
		r.bumpBoardError()
		r.noteError("%s: %v", sb.Key(), err)
		if tr.Network {
			r.netFail()
		} else {
			r.responded()
		}
	default:
		r.bumpBoardError()
		r.noteError("%s: %v", sb.Key(), err)
	}
	r.flush(ctx)
}

func (r *run) fatalOn(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	if r.fatal == nil {
		r.fatal = err
	}
	r.mu.Unlock()
	r.cancelWork(err)
}

// responded resets the consecutive network-failure count: the host answered.
func (r *run) responded() {
	r.mu.Lock()
	r.consecNet = 0
	r.mu.Unlock()
}

// onReached records the first successful visit and releases the strikes held
// back until the network was known to work.
func (r *run) onReached(ctx context.Context) {
	r.mu.Lock()
	r.consecNet = 0
	first := !r.reached
	r.reached = true
	r.mu.Unlock()
	if first {
		r.flushStrikes(ctx)
	}
}

// netFail counts a network-level failure. After 25 in a row with no success
// the run is an outage, not a verdict on the boards.
func (r *run) netFail() {
	r.mu.Lock()
	r.consecNet++
	trip := !r.reached && r.consecNet >= offlineAfter && !r.offline
	if trip {
		r.offline = true
	}
	r.mu.Unlock()
	if trip {
		r.cancelWork(errHostOffline)
	}
}

// strike records a failed visit. Until some visit has succeeded the strike is
// held back, so an outage cannot retire boards.
func (r *run) strike(ctx context.Context, b Board) {
	r.mu.Lock()
	if !r.reached {
		r.pending = append(r.pending, b)
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	r.fatalOn(r.repo.RegistryRecord(ctx, b, VisitFailed, 0, ""))
}

func (r *run) flushStrikes(ctx context.Context) {
	r.mu.Lock()
	// Without a single successful visit the run cannot tell a dead board from
	// a dead network, a captive portal or a wrong endpoint.
	if r.offline || !r.reached {
		r.pending = nil
	}
	pending := r.pending
	r.pending = nil
	r.mu.Unlock()
	for _, b := range pending {
		if err := r.repo.RegistryRecord(ctx, b, VisitFailed, 0, ""); err != nil {
			r.fatalOn(err)
		}
	}
}

// ProbeResult counts a probe of the dead boards that are due.
type ProbeResult struct{ Probed, Revived int }

// ProbeDead visits each dead board whose 30-day probe is due. A successful
// non-empty probe sets it active again; a failure only moves the next probe.
// Blocked boards are never probed.
func (sc *Scanner) ProbeDead(ctx context.Context, portals workspace.Portals) (ProbeResult, error) {
	due, err := sc.Repo.RegistryProbeDue(ctx)
	if err != nil {
		return ProbeResult{}, err
	}
	base := sc.Client
	if base == nil {
		base = NewClient()
	}
	client := base.Session(portals.Concurrency.EffectiveList())
	var res ProbeResult
	for _, row := range due {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		got, err := client.FetchBoard(ctx, sc.Endpoints, row.Board, "")
		var fail *BoardFailure
		switch {
		case err == nil:
			res.Revived++
			err = sc.Repo.RegistryRecord(ctx, row.Board, VisitOK, len(got.Postings), "")
		case errors.As(err, &fail):
			err = sc.Repo.RegistryRecord(ctx, row.Board, VisitFailed, 0, "")
		default:
			err = nil // a transient error says nothing about the board
		}
		res.Probed++
		if err != nil {
			return res, err
		}
	}
	return res, nil
}

// takeWithinCap keeps the best-ranked rows that fit in what is left of the
// run's insert cap and returns how many it dropped. Boards are read in
// parallel, so the cap is shared: the first rows to arrive use it up.
func (r *run) takeWithinCap(keep []candidate) ([]candidate, int) {
	if r.insertCap <= 0 || len(keep) == 0 {
		return keep, 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	left := max(r.insertCap-r.capUsed, 0)
	over := max(len(keep)-left, 0)
	if over > 0 {
		now := r.repo.now()
		targets := NewRankTargets(r.req.Profile.Targets.Roles)
		score := func(c candidate) int {
			s, _ := Rank(c.p, RankInput{Title: c.title, Targets: targets, Scope: r.scope, InScope: c.scope.InScope, Now: now})
			return s
		}
		sort.SliceStable(keep, func(i, j int) bool { return score(keep[i]) > score(keep[j]) })
		keep = keep[:left]
	}
	r.capUsed += len(keep)
	r.capHit = r.capUsed >= r.insertCap
	return keep, over
}

// capReached reports whether the sweep has used its insert cap and should
// stop reading boards.
func (r *run) capReached() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.capHit
}
