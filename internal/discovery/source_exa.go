package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"applysmith/internal/identity"
)

// Exa bounds.
const (
	exaBase            = "https://api.exa.ai"
	exaKeywordPerGroup = 6
	exaSimilarMax      = 8
	exaWatchMax        = 8
	exaResults         = 10
	exaTextMax         = 8000
	exaSpendCap        = 0.25
	exaDefaultCost     = 0.01
	exaFirstLookback   = 72 * time.Hour
	exaOverlap         = 36 * time.Hour
	exaSimilarLookback = 14 * day
	exaPhraseWords     = 5
)

// exaATSHosts are the board hosts the ats group searches.
var exaATSHosts = []string{"boards.greenhouse.io", "job-boards.greenhouse.io", "jobs.lever.co", "jobs.ashbyhq.com", "jobs.smartrecruiters.com", "apply.workable.com"}

// LeadSink receives careers pages found by sources, for expansion.
type LeadSink interface {
	AddCareersPage(ctx context.Context, pageURL, company, source string) error
}

// exaQuery is one planned search.
type exaQuery struct {
	track   string // ats, web, similar or watch
	text    string // the search text, or the seed URL of a similar query
	include []string
	exclude []string
	phrase  string // watch: the company, pinned as a required phrase
	company string
}

func (q exaQuery) key() string { return q.track + "|" + q.text }

// exaPlan derives the run's queries from the profile's target roles and the
// title filter, and from the tracker's seeds.
func (r *run) exaPlan(seeds ExaSeeds) []exaQuery {
	var terms []string
	seen := map[string]bool{}
	add := func(t string) {
		t = strings.TrimSpace(t)
		if k := strings.ToLower(t); t != "" && !seen[k] {
			seen[k] = true
			terms = append(terms, t)
		}
	}
	for _, role := range r.req.Profile.Targets.Roles {
		add(role.Title)
	}
	for _, t := range r.req.Portals.Filters.Title.Positive {
		add(t)
	}
	var exclude []string
	if r.sc.Mirror != nil {
		exclude = r.sc.Mirror.AggregatorHosts()
	}
	var plan []exaQuery
	for _, t := range terms[:min(len(terms), exaKeywordPerGroup)] {
		plan = append(plan, exaQuery{track: "ats", text: t + " job opening", include: exaATSHosts})
	}
	for _, t := range terms[:min(len(terms), exaKeywordPerGroup)] {
		plan = append(plan, exaQuery{track: "web", text: t + " job opening", exclude: exclude})
	}
	for _, u := range seeds.Similar[:min(len(seeds.Similar), exaSimilarMax)] {
		plan = append(plan, exaQuery{track: "similar", text: u, include: exaATSHosts})
	}
	for _, c := range seeds.Watch[:min(len(seeds.Watch), exaWatchMax)] {
		plan = append(plan, exaQuery{track: "watch", text: c + " careers", include: exaATSHosts, phrase: firstWords(c, exaPhraseWords), company: c})
	}
	return plan
}

func firstWords(s string, n int) string {
	w := strings.Fields(s)
	return strings.Join(w[:min(len(w), n)], " ")
}

// exaHit is one search result.
type exaHit struct {
	URL, Title, Text, Published string
}

type exaReply struct {
	Hits []exaHit
	Cost float64
}

func (r *run) exaCall(ctx context.Context, q exaQuery, since time.Time) (exaReply, error) {
	body := map[string]any{
		"numResults":         exaResults,
		"startPublishedDate": since.UTC().Format(time.RFC3339),
		"contents":           map[string]any{"text": map[string]any{"maxCharacters": exaTextMax}},
	}
	path := "/search"
	if q.track == "similar" {
		path = "/findSimilar"
		body["url"] = q.text
	} else {
		body["query"] = q.text
		body["type"] = "neural"
	}
	if len(q.include) > 0 {
		body["includeDomains"] = q.include
	}
	if len(q.exclude) > 0 {
		body["excludeDomains"] = q.exclude
	}
	if q.phrase != "" {
		body["includeText"] = []string{q.phrase}
	}
	b, _ := json.Marshal(body)
	res, err := r.client.Do(ctx, Request{Method: http.MethodPost, URL: r.base("exa", exaBase) + path, Body: b,
		Header: http.Header{"X-Api-Key": {r.key}, "Content-Type": {"application/json"}}})
	if err != nil {
		return exaReply{}, err
	}
	if err := keyedStatus(res); err != nil {
		return exaReply{}, err
	}
	var doc struct {
		Results *[]struct {
			URL       string `json:"url"`
			Title     string `json:"title"`
			Text      string `json:"text"`
			Published string `json:"publishedDate"`
		} `json:"results"`
		Cost struct {
			Total float64 `json:"total"`
		} `json:"costDollars"`
	}
	if err := json.Unmarshal(res.Body, &doc); err != nil || doc.Results == nil {
		return exaReply{}, errLayout{"the response is not a result list"}
	}
	out := exaReply{Cost: doc.Cost.Total}
	for _, h := range *doc.Results {
		out.Hits = append(out.Hits, exaHit{URL: h.URL, Title: h.Title, Text: h.Text, Published: h.Published})
	}
	return out, nil
}

// readExa runs the query plan under the spend cap and the watermark. The
// watermark advances when every query that ran succeeded; a query the spend
// cap or the row limit kept from running is owed its window (ExaState.Owed).
func readExa(ctx context.Context, r *run) error {
	st, err := r.repo.ExaStateGet(ctx)
	if err != nil {
		return err
	}
	seeds, err := r.repo.ExaSeedsGet(ctx)
	if err != nil {
		return err
	}
	plan := r.exaPlan(seeds)
	if len(plan) == 0 {
		return skipRun("empty query plan: add target roles to the profile or positive title terms to portals.yml")
	}
	start := r.repo.now()
	since := start.Add(-exaFirstLookback)
	if !st.Watermark.IsZero() {
		since = st.Watermark.Add(-exaOverlap)
	}
	seen := make(map[string]bool, len(st.Seen))
	for _, u := range st.Seen {
		seen[u] = true
	}
	if st.Zero == nil {
		st.Zero = map[string]int{}
	}
	if st.Suspended == nil {
		st.Suspended = map[string]time.Time{}
	}
	// The queries an earlier run left behind go first, so a plan dearer than
	// the cap still runs every query in turn; one that is planned no more is
	// owed nothing.
	planned := map[string]bool{}
	for _, q := range plan {
		planned[q.key()] = true
	}
	for k := range st.Owed {
		if !planned[k] {
			delete(st.Owed, k)
		}
	}
	if st.Owed == nil {
		st.Owed = map[string]time.Time{}
	}
	sort.SliceStable(plan, func(i, j int) bool {
		_, a := st.Owed[plan[i].key()]
		_, b := st.Owed[plan[j].key()]
		return a && !b
	})
	// leave keeps the window of the queries a stop left unrun: the watermark
	// moves on for the ones that ran.
	leave := func(rest []exaQuery) {
		for _, q := range rest {
			if _, ok := st.Owed[q.key()]; !ok {
				st.Owed[q.key()] = since
			}
		}
	}
	var spent, dearest float64
	allOK := true
	for i, q := range plan {
		if ctx.Err() != nil {
			allOK = false
			break
		}
		// --limit ends the plan once that many rows are in; the query that
		// crosses it still files its own hits, which are already marked seen.
		if r.req.Limit > 0 && r.insertedCount() >= r.req.Limit {
			leave(plan[i:])
			break
		}
		k := q.key()
		if until, ok := st.Suspended[k]; ok {
			if start.Before(until) {
				r.mu.Lock()
				r.rec.Suspended = append(r.rec.Suspended, k)
				r.mu.Unlock()
				continue
			}
			delete(st.Suspended, k)
		}
		next := dearest
		if next == 0 {
			next = exaDefaultCost
		}
		if spent+next > exaSpendCap {
			r.budgetHit = true
			leave(plan[i:])
			break
		}
		r.queryRan()
		from := since
		if at, ok := st.Owed[k]; ok && at.Before(from) {
			from = at
		}
		if q.track == "similar" {
			from = start.Add(-exaSimilarLookback)
		}
		reply, err := r.exaCall(ctx, q, from)
		if err != nil {
			allOK = false
			r.queryFailed(k, err)
			continue
		}
		delete(st.Owed, k)
		spent += reply.Cost
		dearest = max(dearest, reply.Cost)
		before := r.insertedCount()
		if err := r.exaHits(ctx, q, reply.Hits, seen, &st); err != nil {
			return err
		}
		if r.insertedCount()-before > 0 {
			st.Zero[k] = 0
		} else if st.Zero[k]++; st.Zero[k] >= exaSuspendAfter {
			st.Suspended[k] = start.Add(exaSuspendFor)
			st.Zero[k] = 0
		}
	}
	r.detail("spend-micro-usd", int(spent*1e6))
	if allOK && ctx.Err() == nil {
		st.Watermark = start
	}
	checkpoint, cancel := context.WithTimeout(context.WithoutCancel(ctx), scanAccountingTimeout)
	defer cancel()
	return r.repo.ExaStateSave(checkpoint, st)
}

func (r *run) insertedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rec.Counts.Inserted
}

// exaHits sorts one query's hits: a single posting on a supported board goes
// through the pipeline, a careers page becomes a lead, anything else is
// dropped and counted.
func (r *run) exaHits(ctx context.Context, q exaQuery, hits []exaHit, seen map[string]bool, st *ExaState) error {
	var posts []Posting
	for _, h := range hits {
		canon, err := postingURL(h.URL)
		if err != nil {
			r.addDetail("dropped", 1)
			continue
		}
		kind := LinkKind(canon)
		if q.track == "watch" && !strings.Contains(strings.ToLower(h.Text+" "+h.Title), strings.ToLower(q.company)) {
			r.addDetail("dropped", 1)
			continue
		}
		switch kind {
		case LinkATSJob:
			if seen[canon] {
				r.mu.Lock()
				r.rec.Counts.Discovered++
				r.rec.Counts.Duplicates["exa-seen"]++
				r.mu.Unlock()
				continue
			}
			seen[canon] = true
			st.Seen = append(st.Seen, canon)
			posts = append(posts, exaPosting(canon, h))
		case LinkCareersIndex:
			if seen[canon] {
				r.addDetail("careers-seen", 1)
				continue
			}
			seen[canon] = true
			st.Seen = append(st.Seen, canon)
			r.addDetail("careers-pages", 1)
			if r.sc.Leads != nil {
				if err := r.sc.Leads.AddCareersPage(ctx, canon, "", "exa"); err != nil {
					r.noteError("storing a careers page: %v", err)
				}
			}
		default:
			r.addDetail("dropped", 1)
		}
	}
	if len(posts) == 0 {
		return nil
	}
	return r.emit(ctx, q.key(), posts)
}

func exaPosting(canon string, h exaHit) Posting {
	p := Posting{URL: canon, Title: strings.TrimSpace(h.Title), PostedAt: parseTime(h.Published),
		Description: truncateRunes(strings.TrimSpace(h.Text), exaTextMax)}
	if b, err := ResolveBoard(canon); err == nil {
		p.ATS, p.Board, p.Company = b.ATS, b.Slug, humanSlug(b.Slug)
		p.Title = identity.RoleWithoutCompany(p.Title, p.Company)
	}
	if key, ok := identity.ATSKey(canon); ok {
		if i := strings.LastIndex(key, "/"); i >= 0 {
			p.JobID = key[i+1:]
		}
	}
	return p
}

// humanSlug turns a board slug into a display name.
func humanSlug(slug string) string {
	parts := strings.FieldsFunc(slug, func(r rune) bool { return r == '-' || r == '_' || r == '.' })
	for i, p := range parts {
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}
