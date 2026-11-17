package discovery

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
)

// boardSpec is what the list sources (LinkedIn, Indeed, ZipRecruiter, Google
// Jobs, Glassdoor) differ in; readBoard is the one contract they share.
type boardSpec struct {
	source   string
	defBase  string
	pageSize int
	defPages int
	// maxPages caps --pages for a board that serves one screen.
	maxPages int
	// warmUp loads the board's home page before a first request beyond the
	// first result page.
	warmUp  bool
	extract string
	// scrollCards, when set, scrolls the list so a virtualised page renders
	// all of its cards before they are read.
	scrollCards string
	signInText  *lazyregexp.Regexp
	// build returns the URL of result page n (from 0) of a query and whether
	// it carries the board's own freshness parameter.
	build func(base string, q Query, n int, window time.Duration) (u string, boardWindow bool, err error)
	// locate turns a query's place into the board's own location parameters
	// before its first page is built, for a board whose search ignores a
	// location keyword. A place it cannot resolve is an error: searching the
	// whole country while the saved search names a city is the failure it
	// replaces.
	locate func(ctx context.Context, b *bscan, base string, q Query) (Query, error)
	guard  func(requested string) func(live string) bool
	// posting turns a card into a row; false drops a card that is not a posting.
	posting func(base string, c rawCard, now time.Time) (Posting, bool)
	// enforceWindow keeps the local freshness check on even when the board
	// applied its own window.
	enforceWindow bool
	// rowCap caps the rows of one query (the --limit flag overrides it).
	rowCap int
	// rereadEmpty reads a later page that came back empty once more.
	rereadEmpty bool
	// frontier stops paging at pages of rows the scan history already knows.
	frontier bool
	// deferURLCheck runs the URL admission after the detail read.
	deferURLCheck bool
	detail        *detailSpec
}

// shortfall records the rows a page gave against the rows it was asked for.
func (r *run) shortfall(page, asked, got int) {
	r.detail(fmt.Sprintf("page-%d-requested", page), asked)
	r.addDetail(fmt.Sprintf("page-%d-extracted", page), got)
}

func (r *run) pages(def int) int {
	if r.req.Pages > 0 {
		return r.req.Pages
	}
	return def
}

func (r *run) rowLimit(def int) int {
	if r.req.Limit > 0 {
		return r.req.Limit
	}
	return def
}

// readBoard walks the saved searches of one list source.
func readBoard(spec *boardSpec) feedReader {
	return func(ctx context.Context, r *run) error {
		qs, err := r.boardQueries(spec.source)
		if err != nil {
			return err
		}
		b, err := r.newBscan()
		if err != nil {
			return err
		}
		defer b.close()
		base := r.base(spec.source, spec.defBase)
		window := r.browserWindow()
		for _, q := range qs {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			r.queryRan()
			if err := b.readQuery(ctx, spec, base, q, window); err != nil {
				var end *endRun
				if errors.As(err, &end) || ctx.Err() != nil || errors.Is(err, ErrOffline) {
					return err
				}
				r.queryFailed(q.label(), err)
			}
		}
		return nil
	}
}

func (b *bscan) readQuery(ctx context.Context, spec *boardSpec, base string, q Query, window time.Duration) error {
	r := b.r
	label := q.label()
	pages := r.pages(spec.defPages)
	if spec.maxPages > 0 {
		pages = min(pages, spec.maxPages)
	}
	limit := r.rowLimit(spec.rowCap)
	if spec.locate != nil {
		located, err := spec.locate(ctx, b, base, q)
		if err != nil {
			return err
		}
		q = located
	}
	var rows []Posting
	var pageFailure error
	pagesRead := 0
	knownPages := 0
	for n := 0; n < pages && (limit <= 0 || len(rows) < limit); n++ {
		u, boardWindow, err := spec.build(base, q, n, window)
		if err != nil {
			return err
		}
		if n == 0 {
			r.recordSearch(label, u, window, boardWindow)
		}
		if err := b.warmUp(ctx, spec, base, n, u); err != nil {
			return err
		}
		if err := b.pauseBetween(ctx); err != nil {
			return err
		}
		ps := pageSpec{url: u, extract: spec.extract, signInText: spec.signInText, scrollCards: spec.scrollCards}
		if spec.guard != nil {
			ps.guard = spec.guard(u)
		}
		lr, err := b.readList(ctx, ps)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, ErrOffline) {
				return err
			}
			pageFailure = errors.Join(pageFailure, err)
			continue // the page is skipped and counted page-failed
		}
		pagesRead++
		if lr.Cards == 0 && lr.Block == blockNone && spec.rereadEmpty && n > 0 {
			// A page that yielded nothing is waited on once more and read
			// again before it counts as the end of the results.
			if err := b.env.sleep(ctx, 1500*time.Millisecond); err != nil {
				return err
			}
			if again, err := b.readList(ctx, ps); err == nil {
				lr = again
			}
		}
		if end := b.pageEnds(lr, u); end != nil {
			return end
		}
		page, unparsed := cardsToPostings(spec, base, lr, r.repo.now())
		if lr.Cards == 0 || len(page) == 0 {
			if n == 0 {
				switch {
				case lr.Cards > 0:
					r.layoutChange(ReasonEmptyExtract, label)
				case boardWindow:
					r.endReason = ReasonEmptyWindow
				default:
					r.layoutChange(ReasonNoCards, label+": the route may have been retired")
					r.noteError("%s showed no job cards and its URL carries no freshness parameter; the route may have been retired", u)
				}
			}
			break
		}
		r.shortfall(n+1, spec.pageSize, len(page))
		r.addDetail("cards-unparsed", unparsed)
		if spec.frontier && !r.req.RetryKnown && n > 0 {
			k := r.knownRows(page)
			if len(page) >= 5 && k*100 >= len(page)*80 {
				knownPages++
			} else {
				knownPages = 0
			}
			if knownPages >= 2 {
				r.endReason = "frontier-reached"
				rows = append(rows, page...)
				break
			}
		}
		rows = append(rows, page...)
	}
	// Continue other pages/queries, but a query for which every page failed
	// has no successful observation from which to infer an empty result.
	if pagesRead == 0 && pageFailure != nil {
		return pageFailure
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return b.finishQuery(ctx, spec, label, rows, window, q)
}

// warmUp loads the board's home page, cookies and a real referrer, before the
// first request of a run that starts beyond the first result page.
func (b *bscan) warmUp(ctx context.Context, spec *boardSpec, base string, n int, target string) error {
	if !spec.warmUp || b.warmed || b.reads > 0 {
		return nil
	}
	b.warmed = true
	if n == 0 && !deepPage(target) {
		return nil
	}
	if err := b.ensureTab(ctx); err != nil {
		return err
	}
	b.r.addDetail("list-opens", 1)
	if _, err := b.tab.Goto(ctx, strings.TrimRight(base, "/")+"/"); err != nil {
		return err
	}
	b.reads++
	return nil
}

// deepPage reports a result URL that asks for a page beyond the first.
func deepPage(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	q := u.Query()
	for _, k := range []string{"start", "page", "p"} {
		if n, err := strconv.Atoi(q.Get(k)); err == nil && n > 0 {
			return true
		}
	}
	return false
}

func cardsToPostings(spec *boardSpec, base string, lr listRead, now time.Time) (out []Posting, unparsed int) {
	for _, c := range lr.cards() {
		p, ok := spec.posting(base, c, now)
		if !ok {
			unparsed++
			continue
		}
		out = append(out, p)
	}
	return out, unparsed + max(lr.Cards-len(lr.Rows), 0)
}

// knownRows counts the rows of a page that the stores already know.
func (r *run) knownRows(page []Posting) int {
	n := 0
	r.dedupMu.Lock()
	defer r.dedupMu.Unlock()
	for _, p := range page {
		if canon, err := postingURL(p.URL); err == nil {
			p.URL = canon
		}
		if _, _, ok := r.known.Match(p, false); ok {
			n++
		}
	}
	return n
}

// finishQuery applies the freshness window, the listing-only rejections,
// the detail reads and the pipeline to the rows of one query.
func (b *bscan) finishQuery(ctx context.Context, spec *boardSpec, label string, rows []Posting, window time.Duration, q Query) error {
	r := b.r
	boardWindow := false
	r.mu.Lock()
	if n := len(r.rec.Searches); n > 0 {
		boardWindow = r.rec.Searches[n-1].BoardWindow
	}
	r.mu.Unlock()
	if spec.source == "linkedin" && r.req.EasyApplyOnly {
		var kept []Posting
		dropped := 0
		for _, p := range rows {
			if p.hasFlag(FlagEasyApply) {
				kept = append(kept, p)
			} else {
				dropped++
			}
		}
		r.dropRows("not-easy-apply", dropped)
		rows = kept
	}
	rows = r.freshOrDrop(rows, window, boardWindow, spec.enforceWindow)
	rows, err := r.listingRejects(ctx, rows, !spec.deferURLCheck)
	if err != nil {
		return err
	}
	if spec.detail != nil && len(rows) > 0 {
		rows, err = b.enrichRows(ctx, spec.detail, rows)
		if err != nil {
			return err
		}
		if spec.deferURLCheck {
			rows, err = r.listingRejects(ctx, rows, true)
			if err != nil {
				return err
			}
		}
	}
	if len(rows) == 0 {
		return b.pendingEnd()
	}
	if err := r.emit(ctx, label, rows); err != nil {
		return err
	}
	return b.pendingEnd()
}

// pendingEnd returns the end a detail read left for the run: a challenge on a
// detail page ends the run `blocked` after the rows already read are written.
func (b *bscan) pendingEnd() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.end != nil {
		return b.end
	}
	return nil
}
