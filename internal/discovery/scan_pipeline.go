// Clean-room: docs/provenance.md#CR-SCAN-SKELETON

package discovery

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"sync"

	"applysmith/internal/identity"
)

// Filter reasons counted under `filtered`.
const (
	FilterBlockedCompany = "blocked-company"
	FilterTitle          = "title"
	FilterLocation       = "location"
	FilterScope          = "scope"
	FilterInvalidURL     = "invalid-url"
	FilterOverLimit      = "over-limit"
)

// Flags a posting can carry into the inbox.
const FlagLocationUnverified = "location-unverified"

type candidate struct {
	p     Posting
	title TitleResult
	scope ScopeResult
	// original is the URL as the vendor sent it; p.URL is its canonical form.
	original string
}

// commit runs a board's postings through the shared pipeline: block, filter,
// scope, collapse, de-duplicate, enrich, rank, and insert into the inbox and
// the scan history in one transaction.
func (r *run) commit(ctx context.Context, sb sweepBoard, res BoardResult) error {
	rule := r.filters.Title
	if sb.rule != nil {
		rule = *sb.rule
	}
	var cands []candidate
	filtered := map[string]int{}
	for _, p := range res.Postings {
		original := p.URL
		canon, err := postingURL(p.URL)
		if err != nil {
			filtered[FilterInvalidURL]++
			continue
		}
		if NonPostingURL(canon) {
			filtered[FilterNonPostingURL]++
			continue
		}
		p.URL = canon
		p.Source = rowSource(r.req.Source)
		if p.Company == "" && !sb.feed {
			p.Company = sb.company()
		}
		// Blocked companies are dropped before any detail is fetched.
		if r.filters.CompanyBlocked(p.Company) {
			filtered[FilterBlockedCompany]++
			continue
		}
		tr := rule.Check(p.Title)
		if !tr.Pass {
			filtered[FilterTitle]++
			continue
		}
		if !r.filters.LocationOK(p.Location) {
			filtered[FilterLocation]++
			continue
		}
		sr := ApplyScope(r.scope, p.Location)
		if !sr.Pass {
			filtered[FilterScope]++
			continue
		}
		if sr.Unverified {
			p.addFlag(FlagLocationUnverified)
		}
		r.resolveMirror(ctx, &p)
		cands = append(cands, candidate{p, tr, sr, original})
	}
	cands, collapsed := collapse(cands)

	dups := map[string]int{}
	if collapsed > 0 {
		dups[StoreRun] += collapsed
	}
	var keep []candidate
	var dupPosts []Posting
	r.dedupMu.Lock()
	for _, c := range cands {
		if store, _, ok := r.known.Match(c.p, r.req.RetryKnown); ok {
			dups[store]++
			dupPosts = append(dupPosts, c.p)
			continue
		}
		c.p.novelty = r.known.CompanyNovelty(c.p.Company)
		keep = append(keep, c)
	}
	if !sb.feed {
		var over int
		if keep, over = r.takeWithinCap(keep); over > 0 {
			filtered[FilterOverLimit] += over
		}
	}
	for _, c := range keep {
		r.known.Reserve(c.p)
	}
	r.dedupMu.Unlock()

	urls := make([]string, len(keep))
	for i, c := range keep {
		urls[i] = c.p.URL
	}
	if err := r.repo.RegistryHarvest(ctx, r.req.Source, urls...); err != nil {
		return err
	}
	var enriched, degraded, skipped int
	if r.req.Source == "github" {
		enriched, degraded, skipped = r.enrichApplicationPages(ctx, keep)
	} else {
		enriched, degraded, skipped = r.enrich(ctx, keep)
	}
	now := r.repo.now()
	targets := NewRankTargets(r.req.Profile.Targets.Roles)
	rows := make([]insertedRow, 0, len(keep))
	posts := make([]Posting, 0, len(keep))
	originals := make([]string, 0, len(keep))
	for _, c := range keep {
		p := c.p
		originals = append(originals, c.original)
		p.Signals = ComputeSignals(p.Title, p.Description)
		p.Score, p.Reasons = Rank(p, RankInput{Title: c.title, Targets: targets, Scope: r.scope, InScope: c.scope.InScope, Now: now})
		if p.mirrorReason != "" {
			p.Reasons = append(p.Reasons, FlagMirrorUnresolved+": "+p.mirrorReason)
		}
		posts = append(posts, p)
	}
	var raced int
	err := r.repo.s.Write(ctx, func(tx *sql.Tx) error {
		rows, raced = rows[:0], 0
		for i, p := range posts {
			id, inserted, err := r.repo.insertInbox(ctx, tx, p, originals[i], sb.Key(), r.rec.ID)
			if err != nil {
				return fmt.Errorf("inserting %s: %w", p.URL, err)
			}
			if !inserted {
				// Another writer took the identity key since the run loaded
				// what it already knew.
				raced++
				if err := r.repo.historyUpsert(ctx, tx, p, HistoryDuplicate, nil, r.rec.ID); err != nil {
					return err
				}
				continue
			}
			score := p.Score
			if err := r.repo.historyUpsert(ctx, tx, p, HistoryInbox, &score, r.rec.ID); err != nil {
				return err
			}
			rows = append(rows, insertedRow{id: id, p: p, board: sb.idx, n: i})
		}
		for _, p := range dupPosts {
			if err := r.repo.historyUpsert(ctx, tx, p, HistoryDuplicate, nil, r.rec.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if raced > 0 {
		dups[StoreInbox] += raced
	}

	r.mu.Lock()
	c := &r.rec.Counts
	c.Discovered += len(res.Postings)
	for k, v := range filtered {
		c.Filtered[k] += v
	}
	for k, v := range dups {
		c.Duplicates[k] += v
	}
	c.Inserted += len(rows)
	c.Enriched += enriched
	c.EnrichmentDegraded += degraded
	c.EnrichmentSkipped += skipped
	if res.Truncated {
		r.rec.Truncated = append(r.rec.Truncated, sb.Key())
		c.TruncatedPostings += res.Lost
	}
	r.inserted = append(r.inserted, rows...)
	r.mu.Unlock()
	if sb.feed {
		return nil
	}
	return r.repo.RegistryRecord(ctx, sb.Board, VisitOK, len(res.Postings), res.ETag)
}

// collapse merges postings that are duplicates of each other into the most
// recently posted one (the first wins a tie). It returns how many it dropped.
func collapse(in []candidate) ([]candidate, int) {
	var out []candidate
	dropped := 0
outer:
	for _, c := range in {
		for i := range out {
			if sameRow(c.p, out[i].p) {
				dropped++
				if c.p.PostedAt.After(out[i].p.PostedAt) {
					out[i] = c
				}
				continue outer
			}
		}
		out = append(out, c)
	}
	return out, dropped
}

func sameRow(a, b Posting) bool {
	return identity.SameOpening(a.Identity(), b.Identity())
}

// enrich reads the description of survivors whose listing omitted it. Reads
// run concurrently up to the portals detail setting.
func (r *run) enrich(ctx context.Context, cands []candidate) (enriched, degraded, skipped int) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := range cands {
		p := &cands[i].p
		if p.ATS != Greenhouse || p.Description != "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case r.detailSem <- struct{}{}:
				defer func() { <-r.detailSem }()
			case <-ctx.Done():
				mu.Lock()
				skipped++
				mu.Unlock()
				return
			}
			text, err := r.client.FetchDescription(ctx, r.sc.Endpoints, *p)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				degraded++
				return
			}
			p.Description = text
			enriched++
		}()
	}
	wg.Wait()
	return
}

// enqueueTop enqueues the top evaluate_top_n rows of the run among those the
// eligibility gate does not reject; the rest stay pending. A rejected row
// keeps the failing rule in its reasons and uses no slot. While an evaluation
// task has no model (Unrouted), every row stays pending.
func (r *run) enqueueTop(ctx context.Context) error {
	if r.sc.Enqueuer == nil || *r.req.TopN == 0 || len(r.inserted) == 0 {
		return nil
	}
	if r.sc.Unrouted != nil {
		if task := r.sc.Unrouted(ctx); task != "" {
			r.note("new postings wait in the inbox: the %s task has no model it can use; choose one under Settings, Models", task)
			r.mu.Lock()
			r.rec.Unrouted = task
			r.mu.Unlock()
			return nil
		}
	}
	rows := append([]insertedRow(nil), r.inserted...)
	// Boards finish in any order; ties keep the order the boards and their
	// postings were listed in.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].board != rows[j].board {
			return rows[i].board < rows[j].board
		}
		return rows[i].n < rows[j].n
	})
	less := Less(r.order)
	sort.SliceStable(rows, func(i, j int) bool { return less(rows[i].p, rows[j].p) })
	queued := 0
	for _, row := range rows {
		if queued >= *r.req.TopN || ctx.Err() != nil {
			break
		}
		inbox, err := r.repo.InboxGet(ctx, row.id)
		if err != nil {
			return err
		}
		if r.sc.Gate != nil {
			if rule := r.sc.Gate(inbox); rule != "" {
				if err := r.repo.addReason(ctx, row.id, "gate:"+rule); err != nil {
					return err
				}
				r.mu.Lock()
				r.rec.Counts.GateRejected++
				r.mu.Unlock()
				continue
			}
		}
		jobID, err := r.repo.queueAndEnqueue(ctx, row.id, func() (string, error) {
			return r.sc.Enqueuer.Enqueue(ctx, EvalRequest{InboxID: row.id, URL: inbox.URL, Mode: r.req.Mode, RunID: r.rec.ID, Source: r.req.Source,
				Title: inbox.Role, Company: inbox.Company, Location: inbox.Location, Scanned: inbox.Description, PostedAt: inbox.PostedAt})
		})
		// Accepted work still consumes its slot if recording the job link
		// fails; it already belongs to the pool. Preserve both count and error.
		if err == nil || jobID != "" {
			queued++
			r.mu.Lock()
			r.rec.Counts.Queued++
			r.mu.Unlock()
		}
		if err != nil {
			return fmt.Errorf("queueing inbox row %d: %w", row.id, err)
		}
	}
	return nil
}

// rowSource is the source name a source's rows carry into the inbox. The
// Jobright feed is told apart from the aggregator it shares a host with.
func rowSource(source string) string {
	if source == "jobright" {
		return "jobright-recommend"
	}
	return source
}
