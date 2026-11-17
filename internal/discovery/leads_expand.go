package discovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"applysmith/internal/workspace"
)

// Expansion bounds and the ladder's outcomes.
const (
	// DefaultExpandCap is how many careers URLs one run reads.
	DefaultExpandCap = 20
	// minSameSiteLinks is the fewest posting links a page must list for the
	// last rung to read it.
	minSameSiteLinks = 3
	maxPageBoards    = 3

	expandBoard    = "board"
	expandLinked   = "linked-board"
	expandJSONLD   = "json-ld"
	expandLinks    = "same-site-links"
	expandNone     = "none"
	expandUnreach  = "unreachable"
	maxExpandTitle = 150
)

// ExpandRequest is one run of the careers-page expansion.
type ExpandRequest struct {
	Portals workspace.Portals
	Profile workspace.Profile
	// Limit caps the URLs read this run; zero means DefaultExpandCap.
	Limit int
	// Force reads these leads again even when their URL is in the ledger.
	Force []int64
}

// ExpandResult says what a run did. Pending is the careers pages still waiting
// after it, never a smaller number than there are.
type ExpandResult struct {
	Read        int // URLs fetched this run
	Settled     int // leads whose URL the ledger already held
	WithPosting int
	None        int
	Unreachable int
	Failed      int // read, admission or persistence failures; not settled
	Postings    int // postings the pages listed
	Rejected    int // postings the title filter or the gate refused
	Inserted    int // postings added to the inbox
	Pending     int
}

// Summary is the one line a scheduled run reports.
func (e ExpandResult) Summary() string {
	return fmt.Sprintf("%d careers pages read (%d with postings, %d none, %d unreachable, %d failed), %d postings listed, %d refused by the filters, %d added to the inbox; %d already looked at; %d still pending",
		e.Read, e.WithPosting, e.None, e.Unreachable, e.Failed, e.Postings, e.Rejected, e.Inserted, e.Settled, e.Pending)
}

// leadRun builds the run the lead paths push postings through, so they share
// the filters, the duplicate check and the inbox insert with every scan.
func (sc *Scanner) leadRun(ctx context.Context, portals workspace.Portals, profile workspace.Profile) (*run, error) {
	r, err := sc.newRun(ctx, ScanRequest{Source: "leads", Portals: portals, Profile: profile})
	if err != nil {
		return nil, err
	}
	r.rec = Record{Source: "leads", Counts: newCounts()}
	if r.known, err = r.repo.LoadKnown(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *run) leadChecker() LeadChecker {
	return LeadChecker{Filters: r.filters, Scope: r.scope, Gate: r.sc.Gate}
}

// ExpandLeads turns careers-page leads into postings, cheapest rung first.
func (sc *Scanner) ExpandLeads(ctx context.Context, req ExpandRequest) (res ExpandResult, err error) {
	r, err := sc.leadRun(ctx, req.Portals, req.Profile)
	if err != nil {
		return ExpandResult{}, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultExpandCap
	}
	force := slices.Clone(req.Force)
	defer func() {
		r.mu.Lock()
		res.Inserted = r.rec.Counts.Inserted
		r.mu.Unlock()
		finishCtx, finish := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer finish()
		var accountingErr error
		res.Pending, accountingErr = sc.Repo.expansionPending(finishCtx, force)
		err = errors.Join(err, accountingErr)
	}()
	var failures []error
	work, err := sc.Repo.expansionWork(ctx, req.Force)
	if err != nil {
		return res, err
	}
	sort.SliceStable(work, func(i, j int) bool { return containsID(force, work[i].ID) && !containsID(force, work[j].ID) })
	for _, l := range work {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		forced := containsID(req.Force, l.ID)
		pageURL := firstOf(l.ApplyLinks, l.Evidence.URL)
		if pageURL == "" {
			continue
		}
		if !forced {
			if found, outcome, ok, err := sc.Repo.ledgerGet(ctx, pageURL); err != nil {
				return res, err
			} else if ok {
				res.Settled++
				if l.Expanded == "" {
					exp := expandedText(outcome, found)
					if err := sc.Repo.LeadUpdate(ctx, l.ID, LeadPatch{Expanded: &exp}); err != nil {
						return res, err
					}
				}
				continue
			}
		}
		if res.Read >= limit {
			continue
		}
		if err := r.expandOne(ctx, l, pageURL, &res); err != nil {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			res.Failed++
			failures = append(failures, fmt.Errorf("lead %d: %w", l.ID, err))
		} else if forced {
			force = slices.DeleteFunc(force, func(id int64) bool { return id == l.ID })
		}
	}
	return res, errors.Join(failures...)
}

func containsID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// expansionWork lists the careers-page leads to look at: those still new, and
// any forced one whatever its state.
func (r *Repo) expansionWork(ctx context.Context, force []int64) ([]Lead, error) {
	rows, err := r.s.Read().QueryContext(ctx, `SELECT `+leadCols+` FROM leads WHERE category = 'careers-page' AND state = 'new' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	out, err := scanLeads(rows)
	if err != nil {
		return nil, err
	}
	for _, id := range force {
		if l, err := r.LeadGet(ctx, id); err != nil {
			return nil, err
		} else if !containsLead(out, id) {
			out = append(out, l)
		}
	}
	return out, nil
}

func containsLead(list []Lead, id int64) bool {
	for _, l := range list {
		if l.ID == id {
			return true
		}
	}
	return false
}

func expandedText(outcome string, found int) string {
	if found == 0 || outcome == expandNone {
		return "none"
	}
	return fmt.Sprintf("%d postings (%s)", found, outcome)
}

// expandOne reads one careers page, admits its postings, then records completion.
func (r *run) expandOne(ctx context.Context, l Lead, pageURL string, res *ExpandResult) error {
	res.Read++
	posts, outcome, readErr := r.readCareers(ctx, pageURL)
	if readErr != nil && len(posts) == 0 {
		return readErr
	}
	patch := LeadPatch{}
	switch outcome {
	case expandUnreach:
		res.Unreachable++
		cat := LeadUnreachable
		patch.Category = &cat
	case expandNone:
		res.None++
		exp := "none"
		patch.Expanded = &exp
	default:
		res.WithPosting++
		res.Postings += len(posts)
		exp, state := expandedText(outcome, len(posts)), LeadEvaluated
		patch.Expanded, patch.State = &exp, &state
		kept, err := r.admitExpanded(ctx, l, pageURL, posts, res)
		if err != nil {
			return errors.Join(readErr, err)
		}
		if len(kept) > 0 {
			if err := r.emit(ctx, "expanded from "+pageURL, kept); err != nil {
				return errors.Join(readErr, err)
			}
		}
	}
	if readErr != nil {
		return readErr
	}
	return r.repo.settleExpansion(ctx, pageURL, len(posts), outcome, l.ID, patch)
}

// admitExpanded applies the title filter, the blocked list and the gate to
// what a page listed, auditing every refusal with all of its rules.
func (r *run) admitExpanded(ctx context.Context, l Lead, pageURL string, posts []Posting, res *ExpandResult) ([]Posting, error) {
	checker := r.leadChecker()
	var kept []Posting
	for _, p := range posts {
		if p.Company == "" {
			p.Company = l.Company
		}
		if rules := checker.Failing(p); len(rules) > 0 {
			res.Rejected++
			if err := r.repo.LeadGateAudit(ctx, IngressExpansion, p, rules); err != nil {
				return nil, err
			}
			continue
		}
		kept = append(kept, p)
	}
	return kept, nil
}

// readCareers climbs the ladder and stops at the first rung that yields
// postings. A page that yields nothing is reported as such; a transport
// failure is an error and is not ledgered, because it says nothing about the
// page. A board's permanent failure is an answer: see boardOutcome.
func (r *run) readCareers(ctx context.Context, pageURL string) ([]Posting, string, error) {
	if b, err := ResolveBoard(pageURL); err == nil {
		res, err := r.client.FetchBoard(ctx, r.sc.Endpoints, b, "")
		outcome, err := boardOutcome(expandBoard, err)
		return res.Postings, outcome, err
	}
	resp, err := r.client.Do(ctx, Request{URL: pageURL, Accept: "text/html,application/xhtml+xml;q=0.9,*/*;q=0.5"})
	if err != nil {
		return nil, "", err
	}
	if resp.Status >= 300 && resp.Status < 400 {
		if loc := resp.Header.Get("Location"); loc != "" {
			if b, err := ResolveBoard(absURL(pageURL, loc)); err == nil {
				res, err := r.client.FetchBoard(ctx, r.sc.Endpoints, b, "")
				outcome, err := boardOutcome(expandLinked, err)
				return res.Postings, outcome, err
			}
		}
		return nil, expandUnreach, nil
	}
	if resp.Status != http.StatusOK {
		return nil, expandUnreach, nil
	}
	page := string(resp.Body)
	if posts, err := r.boardsLinkedFrom(ctx, page); len(posts) > 0 || err != nil {
		return posts, expandLinked, err
	}
	var ld []Posting
	for _, o := range jsonLDPostings(page) {
		p := postingFromLD(o)
		p.URL = absURL(pageURL, p.URL)
		if p.Title != "" && p.URL != "" {
			ld = append(ld, p)
		}
	}
	if len(ld) > 0 {
		return ld, expandJSONLD, nil
	}
	if links := sameSitePostingLinks(page, pageURL); len(links) >= minSameSiteLinks {
		return links, expandLinks, nil
	}
	return nil, expandNone, nil
}

// boardOutcome is the outcome of a page that is a board: the rung when the
// board was read, and for a failure that will not change on a retry
// (BoardFailure) the outcome that settles the page, unreachable for a board
// that is gone or closed to the reader and none for one that lists nothing.
// Any other failure stays an error, so the page is read again.
func boardOutcome(rung string, err error) (string, error) {
	var permanent *BoardFailure
	switch {
	case !errors.As(err, &permanent):
		return rung, err
	case permanent.Status != 0:
		return expandUnreach, nil
	}
	return expandNone, nil
}

// boardsLinkedFrom enumerates the supported boards a page points at. A linked
// board's permanent failure adds nothing and is no failure of the page.
func (r *run) boardsLinkedFrom(ctx context.Context, page string) ([]Posting, error) {
	seen := map[string]bool{}
	var out []Posting
	var failures []error
	for _, raw := range textURLRE.FindAllString(strings.ReplaceAll(page, "&amp;", "&"), -1) {
		b, err := ResolveBoard(strings.TrimRight(raw, `\.,;`))
		if err != nil || seen[b.Key()] || len(seen) >= maxPageBoards {
			continue
		}
		seen[b.Key()] = true
		res, err := r.client.FetchBoard(ctx, r.sc.Endpoints, b, "")
		out = append(out, res.Postings...)
		if _, err := boardOutcome(expandLinked, err); err != nil {
			failures = append(failures, fmt.Errorf("board %s: %w", b.Key(), err))
		}
	}
	return out, errors.Join(failures...)
}

// sameSitePostingLinks reads the anchors of a page that name one posting on
// the same site.
func sameSitePostingLinks(page, pageURL string) []Posting {
	base, err := url.Parse(pageURL)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []Posting
	for _, m := range anchorRE.FindAllStringSubmatch(page, -1) {
		href := absURL(pageURL, attr(m[1], "href"))
		u, err := url.Parse(href)
		if err != nil || !sameSite(base.Hostname(), u.Hostname()) || seen[href] {
			continue
		}
		if k := LinkKind(href); k != LinkJobDetail && k != LinkATSJob {
			continue
		}
		title := textOf(m[2])
		if title == "" || len(title) > maxExpandTitle {
			continue
		}
		seen[href] = true
		out = append(out, Posting{Title: title, URL: href})
	}
	return out
}

func sameSite(a, b string) bool {
	a, b = strings.TrimPrefix(strings.ToLower(a), "www."), strings.TrimPrefix(strings.ToLower(b), "www.")
	return a != "" && (a == b || strings.HasSuffix(a, "."+b) || strings.HasSuffix(b, "."+a))
}

func (r *Repo) ledgerGet(ctx context.Context, pageURL string) (found int, outcome string, ok bool, err error) {
	err = r.s.Read().QueryRowContext(ctx, `SELECT found, outcome FROM lead_expansions WHERE url = ?`, leadURLKey(pageURL)).Scan(&found, &outcome)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil
	}
	return found, outcome, err == nil, err
}

// settleExpansion records completion only after posting admission succeeded.
// The ledger and lead state must commit together so either write can be retried.
func (r *Repo) settleExpansion(ctx context.Context, pageURL string, found int, outcome string, leadID int64, patch LeadPatch) error {
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO lead_expansions (url, expanded_at, found, outcome, lead_id) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (url) DO UPDATE SET expanded_at = excluded.expanded_at, found = excluded.found, outcome = excluded.outcome, lead_id = excluded.lead_id`,
			leadURLKey(pageURL), r.stamp(), found, outcome, nullInt(leadID))
		if err != nil {
			return err
		}
		return r.updateLead(ctx, tx, leadID, patch)
	})
}

// expansionPending counts the careers-page leads expansionWork would list
// whose URL the ledger does not hold yet, from the leads' links and the
// ledger's keys alone: two reads, whatever the number of leads.
func (r *Repo) expansionPending(ctx context.Context, force []int64) (int, error) {
	settled := map[string]bool{}
	keys, err := r.s.Read().QueryContext(ctx, `SELECT url FROM lead_expansions`)
	if err != nil {
		return 0, err
	}
	for keys.Next() {
		var k string
		if err := keys.Scan(&k); err != nil {
			keys.Close()
			return 0, err
		}
		settled[k] = true
	}
	keys.Close()
	if err := keys.Err(); err != nil {
		return 0, err
	}
	rows, err := r.s.Read().QueryContext(ctx, `SELECT id, apply_links_json, evidence_url FROM leads WHERE category = 'careers-page' AND state = 'new'`)
	if err != nil {
		return 0, err
	}
	n := 0
	listed := map[int64]bool{}
	for rows.Next() {
		var id int64
		var links, evidence string
		if err := rows.Scan(&id, &links, &evidence); err != nil {
			rows.Close()
			return 0, err
		}
		var apply []string
		_ = json.Unmarshal([]byte(links), &apply)
		listed[id] = true
		if containsID(force, id) || !settled[leadURLKey(firstOf(apply, evidence))] {
			n++
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// A forced lead counts whatever its state.
	for _, id := range force {
		if listed[id] {
			continue
		}
		if _, err := r.LeadGet(ctx, id); err != nil {
			return 0, err
		}
		listed[id] = true
		n++
	}
	return n, nil
}
