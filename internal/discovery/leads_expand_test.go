package discovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// ldRoute sends every request the scanner's client makes to the fake server,
// whatever host it names, so fixtures can use example.com addresses.
func ldRoute(t *testing.T, e *env) {
	t.Helper()
	u, err := url.Parse(e.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	e.sc.Client.HTTP.Transport = &rewriteTransport{target: u}
}

func ldBoard(e *env, slug, company string, jobs ...obj) {
	for _, j := range jobs {
		if j["content"] == nil {
			j["content"] = "<p>Build services for " + company + ". Responsibilities include shipping.</p>"
		}
		j["company_name"] = company
		if j["location"] == nil {
			j["location"] = obj{"name": "Exampleton"}
		}
	}
	e.srv.json("/v1/boards/"+slug+"/jobs", ghJobs(slug, jobs...))
}

func ldJob(id int, title string) obj { return obj{"id": id, "title": title} }

func ldPage(e *env, path, body string) {
	e.srv.set(path, route{body: "<html><body>" + body + "</body></html>"})
}

func ldCareers(t *testing.T, e *env, pageURL string) int64 {
	t.Helper()
	if err := e.rp.AddCareersPage(e.ctx, pageURL, "Example Corp", "exa"); err != nil {
		t.Fatal(err)
	}
	ls, _ := e.rp.LeadList(e.ctx, LeadFilter{Category: LeadCareersPage})
	for _, l := range ls {
		if l.ApplyLinks[0] == pageURL {
			return l.ID
		}
	}
	t.Fatalf("no lead for %s", pageURL)
	return 0
}

func ldExpand(t *testing.T, e *env, limit int, force ...int64) ExpandResult {
	t.Helper()
	res, err := e.sc.ExpandLeads(e.ctx, ExpandRequest{Portals: portals([]string{"engineer"}, nil), Limit: limit, Force: force})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func ldOutcome(t *testing.T, e *env, pageURL string) (int, string) {
	t.Helper()
	found, outcome, ok, err := e.rp.ledgerGet(e.ctx, pageURL)
	if err != nil || !ok {
		t.Fatalf("no ledger entry for %s: %v", pageURL, err)
	}
	return found, outcome
}

const ldJSONLD = `<script type="application/ld+json">{"@context":"https://schema.org","@type":"JobPosting","title":"Data Engineer",
"url":"https://careers.example.com/jobs/ld-1","hiringOrganization":{"name":"LD Corp"},"description":"<p>Build pipelines.</p>"}</script>`

func ldLinks(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `<a href="/jobs/%d">Backend Engineer %d</a> `, 100+i, i)
	}
	return b.String()
}

func TestLeadsExpand_LadderStopsAtFirstRung(t *testing.T) {
	e := newEnv(t)
	ldRoute(t, e)
	ldBoard(e, "board-one", "Board One", ldJob(1, "Backend Engineer"), ldJob(2, "Data Engineer"))
	ldBoard(e, "board-two", "Board Two", ldJob(3, "Platform Engineer"))
	ldPage(e, "/careers/linked", `<a href="https://job-boards.greenhouse.io/board-two">Open roles</a>`+ldJSONLD+ldLinks(4))
	ldPage(e, "/careers/ld", ldJSONLD+ldLinks(4))
	ldPage(e, "/careers/links", ldLinks(4))
	ldPage(e, "/careers/none", ldLinks(2))

	pages := map[string]string{
		"https://job-boards.greenhouse.io/board-one": expandBoard,
		"https://careers.example.com/careers/linked": expandLinked,
		"https://careers.example.com/careers/ld":     expandJSONLD,
		"https://careers.example.com/careers/links":  expandLinks,
		"https://careers.example.com/careers/none":   expandNone,
	}
	for u := range pages {
		ldCareers(t, e, u)
	}
	res := ldExpand(t, e, 0)
	if res.Read != 5 || res.WithPosting != 4 || res.None != 1 {
		t.Fatalf("result = %+v", res)
	}
	for u, want := range pages {
		if _, got := ldOutcome(t, e, u); got != want {
			t.Errorf("%s: stopped at %q, want %q", u, got, want)
		}
	}
	if found, _ := ldOutcome(t, e, "https://careers.example.com/careers/linked"); found != 1 {
		t.Errorf("the linked-board rung read %d postings, want only the board's 1 (JSON-LD and links must not add to it)", found)
	}
}

func TestLeadsExpand_NothingFoundIsRecordedAndNothingInvented(t *testing.T) {
	e := newEnv(t)
	ldRoute(t, e)
	ldPage(e, "/careers", ldLinks(2))
	id := ldCareers(t, e, "https://careers.example.com/careers")
	res := ldExpand(t, e, 0)
	if res.None != 1 || res.Inserted != 0 {
		t.Fatalf("result = %+v", res)
	}
	l, _ := e.rp.LeadGet(e.ctx, id)
	if l.Expanded != "none" {
		t.Fatalf("expanded = %q, want none", l.Expanded)
	}
	if rows, _ := e.rp.InboxList(e.ctx, InboxQuery{}); len(rows) != 0 {
		t.Fatalf("%d inbox rows invented", len(rows))
	}
	if n, _ := e.rp.expansionPending(e.ctx, nil); n != 0 {
		t.Fatalf("a page that yielded nothing is listed as pending (%d)", n)
	}
}

func TestLeadsExpand_GreenhouseBoardLeadReadsTheAPI(t *testing.T) {
	e := newEnv(t)
	ldBoard(e, "example-co", "Example Co", ldJob(1, "Backend Engineer"))
	id := ldCareers(t, e, "https://boards.greenhouse.io/example-co")
	res := ldExpand(t, e, 0)
	if res.Inserted != 1 {
		t.Fatalf("result = %+v", res)
	}
	if e.srv.hitCount("/v1/boards/example-co/jobs") != 1 {
		t.Fatal("the board was not read through its API")
	}
	if l, _ := e.rp.LeadGet(e.ctx, id); l.Expanded != "1 postings (board)" {
		t.Fatalf("expanded = %q", l.Expanded)
	}
}

func TestLeadsExpand_UrlExpandedOnceAndCappedPerRun(t *testing.T) {
	e := newEnv(t)
	ldRoute(t, e)
	var ids []int64
	for i := 0; i < 50; i++ {
		p := fmt.Sprintf("/careers/n%d", i)
		ldPage(e, p, ldLinks(1))
		ids = append(ids, ldCareers(t, e, "https://careers.example.com"+p))
	}
	res := ldExpand(t, e, 20)
	if res.Read != 20 || res.Pending != 30 {
		t.Fatalf("result = %+v; want 20 read and 30 pending", res)
	}
	if !strings.Contains(res.Summary(), "30 still pending") {
		t.Fatalf("summary = %q", res.Summary())
	}
	hits := e.srv.hitCount("/careers/n0")

	// A URL in the ledger becomes pending again: it is settled, not fetched.
	exp := ""
	if err := e.rp.LeadUpdate(e.ctx, ids[0], LeadPatch{Expanded: &exp}); err != nil {
		t.Fatal(err)
	}
	res = ldExpand(t, e, 20)
	if res.Read != 20 || res.Settled != 20 || res.Pending != 10 {
		t.Fatalf("second run = %+v", res)
	}
	if got := e.srv.hitCount("/careers/n0"); got != hits {
		t.Fatalf("a ledgered URL was fetched again (%d -> %d)", hits, got)
	}
	if l, _ := e.rp.LeadGet(e.ctx, ids[0]); l.Expanded != "none" {
		t.Fatalf("the settled lead shows %q, want the ledger's none", l.Expanded)
	}
	// --force reads it again.
	ldExpand(t, e, 20, ids[0])
	if got := e.srv.hitCount("/careers/n0"); got != hits+1 {
		t.Fatalf("--force fetched %d times, want one more", got-hits)
	}
}

func TestLeadsExpand_PostingsPassTheFiltersAndRefusalsAreAudited(t *testing.T) {
	e := newEnv(t)
	e.sc.Gate = func(row InboxRow) string {
		if strings.Contains(row.Role, "Staff") {
			return "seniority"
		}
		return ""
	}
	ldBoard(e, "gate-co", "Gate Co", ldJob(1, "Backend Engineer"), ldJob(2, "Staff Engineer"), ldJob(3, "Office Manager"))
	ldCareers(t, e, "https://boards.greenhouse.io/gate-co")
	res := ldExpand(t, e, 0)
	if res.Postings != 3 || res.Rejected != 2 || res.Inserted != 1 {
		t.Fatalf("result = %+v", res)
	}
	audit, _ := e.rp.GateAuditList(e.ctx, IngressExpansion, 10)
	if len(audit) != 2 {
		t.Fatalf("audit = %+v", audit)
	}
}

func TestLeadsExpand_FailedReadsConsumeTheCapAndPreserveSuccessfulWork(t *testing.T) {
	e := newEnv(t)
	ldRoute(t, e)
	failed := "https://careers.example.com/down"
	e.srv.set("/down", route{status: 503})
	ldPage(e, "/good", ldJSONLD)
	ldPage(e, "/later", "No openings")
	id := ldCareers(t, e, failed)
	ldCareers(t, e, "https://careers.example.com/good")
	ldCareers(t, e, "https://careers.example.com/later")
	res, err := e.sc.ExpandLeads(e.ctx, ExpandRequest{Portals: portals([]string{"engineer"}, nil), Limit: 2})
	var temporary *TransientError
	if !errors.As(err, &temporary) || temporary.Status != 503 {
		t.Errorf("failed read was hidden or lost its cause: result=%+v err=%v", res, err)
	}
	if res.Read != 2 || res.Failed != 1 || res.Inserted != 1 || res.Pending != 2 || res.WithPosting != 1 {
		t.Fatalf("result=%+v; want two attempts including one failure, one insertion and two still pending", res)
	}
	if e.srv.hitCount("/later") != 0 {
		t.Fatal("a failed read escaped the two-URL cap")
	}
	if _, _, ok, err := e.rp.ledgerGet(e.ctx, failed); err != nil || ok {
		t.Fatalf("failed URL was settled: ok=%t err=%v", ok, err)
	}
	if l, err := e.rp.LeadGet(e.ctx, id); err != nil || l.State != LeadNew || l.Expanded != "" {
		t.Fatalf("failed lead=%+v err=%v", l, err)
	}
	ldPage(e, "/down", "No openings")
	again, err := e.sc.ExpandLeads(e.ctx, ExpandRequest{Portals: portals([]string{"engineer"}, nil), Limit: 2})
	if err != nil || again.Read != 2 || again.Failed != 0 || again.Pending != 0 || again.Inserted != 0 {
		t.Fatalf("retry=%+v err=%v", again, err)
	}
	rows, err := e.rp.InboxList(e.ctx, InboxQuery{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("successful work was duplicated or lost: rows=%+v err=%v", rows, err)
	}
}

func TestLeadsExpand_FinalizationFailureLeavesTheLeadRetryable(t *testing.T) {
	for _, target := range []string{"ledger", "lead"} {
		t.Run(target, func(t *testing.T) {
			e := newEnv(t)
			ldRoute(t, e)
			ldPage(e, "/careers", ldJSONLD)
			pageURL := "https://careers.example.com/careers"
			id := ldCareers(t, e, pageURL)
			trigger := "CREATE TRIGGER owned_finalization_refusal BEFORE INSERT ON lead_expansions BEGIN SELECT RAISE(FAIL, 'owned finalization refusal'); END"
			if target == "lead" {
				trigger = "CREATE TRIGGER owned_finalization_refusal BEFORE UPDATE ON leads BEGIN SELECT RAISE(FAIL, 'owned finalization refusal'); END"
			}
			if err := e.st.Write(e.ctx, func(tx *sql.Tx) error { _, err := tx.Exec(trigger); return err }); err != nil {
				t.Fatal(err)
			}
			res, err := e.sc.ExpandLeads(e.ctx, ExpandRequest{Limit: 1})
			if err == nil || !strings.Contains(err.Error(), "owned finalization refusal") || res.Failed != 1 || res.Inserted != 1 {
				t.Fatalf("result=%+v err=%v", res, err)
			}
			lead, err := e.rp.LeadGet(e.ctx, id)
			if err != nil || lead.State != LeadNew || lead.Expanded != "" {
				t.Errorf("lead became settled before finalization: %+v err=%v", lead, err)
			}
			if _, _, ok, err := e.rp.ledgerGet(e.ctx, pageURL); err != nil || ok {
				t.Errorf("ledger became settled before finalization: ok=%t err=%v", ok, err)
			}
			if err := e.st.Write(e.ctx, func(tx *sql.Tx) error { _, err := tx.Exec("DROP TRIGGER owned_finalization_refusal"); return err }); err != nil {
				t.Fatal(err)
			}
			again, err := e.sc.ExpandLeads(e.ctx, ExpandRequest{Limit: 1})
			if err != nil || again.Read != 1 || again.Inserted != 0 || again.Failed != 0 || again.Pending != 0 {
				t.Fatalf("retry=%+v err=%v", again, err)
			}
			lead, err = e.rp.LeadGet(e.ctx, id)
			if err != nil || lead.State != LeadEvaluated || lead.Expanded != "1 postings (json-ld)" {
				t.Fatalf("retried lead=%+v err=%v", lead, err)
			}
			rows, err := e.rp.InboxList(e.ctx, InboxQuery{})
			if err != nil || len(rows) != 1 {
				t.Fatalf("retry duplicated/lost inbox: %+v err=%v", rows, err)
			}
		})
	}
}

type cancelExpansionTransport struct {
	next   http.RoundTripper
	cancel context.CancelFunc
}

func (t cancelExpansionTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/cancel" {
		t.cancel()
		return nil, r.Context().Err()
	}
	return t.next.RoundTrip(r)
}
func TestLeadsExpand_CancellationPreservesCommittedAccounting(t *testing.T) {
	e := newEnv(t)
	ldRoute(t, e)
	ldPage(e, "/good", ldJSONLD)
	ldCareers(t, e, "https://careers.example.com/good")
	ldCareers(t, e, "https://careers.example.com/cancel")
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	e.sc.Client.HTTP.Transport = cancelExpansionTransport{next: e.sc.Client.HTTP.Transport, cancel: cancel}
	res, err := e.sc.ExpandLeads(ctx, ExpandRequest{Portals: portals([]string{"engineer"}, nil), Limit: 2})
	rows, dbErr := e.rp.InboxList(e.ctx, InboxQuery{})
	pending, pendingErr := e.rp.expansionPending(e.ctx, nil)
	if !errors.Is(err, context.Canceled) || dbErr != nil || pendingErr != nil || len(rows) != 1 {
		t.Fatalf("fixture result=%+v err=%v rows=%d db=%v pending=%d pendingErr=%v", res, err, len(rows), dbErr, pending, pendingErr)
	}
	if res.Inserted != len(rows) || res.Pending != pending {
		t.Fatalf("cancelled result=%+v durable inserted=%d pending=%d", res, len(rows), pending)
	}
}

func TestLeadsExpand_MixedLinkedBoardsPreservePartialWork(t *testing.T) {
	e := newEnv(t)
	ldRoute(t, e)
	ldPage(e, "/careers", `<a href="https://job-boards.greenhouse.io/good">Good roles</a><a href="https://job-boards.greenhouse.io/down">Other roles</a>`)
	ldBoard(e, "good", "Good Corp", ldJob(1, "Backend Engineer"))
	e.srv.set("/v1/boards/down/jobs", route{status: 503})
	pageURL := "https://careers.example.com/careers"
	ldCareers(t, e, pageURL)
	res, err := e.sc.ExpandLeads(e.ctx, ExpandRequest{Portals: portals([]string{"engineer"}, nil), Limit: 1})
	var temporary *TransientError
	_, _, settled, dbErr := e.rp.ledgerGet(e.ctx, pageURL)
	if !errors.As(err, &temporary) || temporary.Status != 503 || settled || dbErr != nil || res.Inserted != 1 {
		t.Fatalf("mixed linked result=%+v err=%v settled=%v dbErr=%v", res, err, settled, dbErr)
	}
	ldBoard(e, "down", "Recovered Corp", ldJob(2, "Data Engineer"))
	again, err := e.sc.ExpandLeads(e.ctx, ExpandRequest{Portals: portals([]string{"engineer"}, nil), Limit: 1})
	rows, dbErr := e.rp.InboxList(e.ctx, InboxQuery{})
	if err != nil || dbErr != nil || again.Inserted != 1 || len(rows) != 2 {
		t.Fatalf("retry=%+v err=%v rows=%d dbErr=%v", again, err, len(rows), dbErr)
	}
}

func TestLeadsExpand_BoardFailureAcrossEveryRung(t *testing.T) {
	for _, rung := range []string{"direct", "redirect", "linked"} {
		t.Run(rung, func(t *testing.T) {
			e := newEnv(t)
			ldRoute(t, e)
			board := "https://job-boards.greenhouse.io/down"
			pageURL := "https://careers.example.com/careers"
			switch rung {
			case "direct":
				pageURL = board
			case "redirect":
				e.sc.Client.HTTP.Transport = expansionRedirectTransport{next: e.sc.Client.HTTP.Transport, target: board}
			case "linked":
				ldPage(e, "/careers", `<a href="`+board+`">Open roles</a>`)
			}
			e.srv.set("/v1/boards/down/jobs", route{status: 503})
			ldCareers(t, e, pageURL)
			res, err := e.sc.ExpandLeads(e.ctx, ExpandRequest{Limit: 1})
			var temporary *TransientError
			if !errors.As(err, &temporary) || res.Read != 1 || res.Failed != 1 || res.None != 0 || res.Pending != 1 {
				t.Fatalf("result=%+v err=%v", res, err)
			}
			if _, _, ok, err := e.rp.ledgerGet(e.ctx, pageURL); err != nil || ok {
				t.Fatalf("failed board settled: ok=%v err=%v", ok, err)
			}
			ldBoard(e, "down", "Recovered Corp", ldJob(1, "Data Engineer"))
			again, err := e.sc.ExpandLeads(e.ctx, ExpandRequest{Limit: 1})
			if err != nil || again.Inserted != 1 || again.Pending != 0 {
				t.Fatalf("retry=%+v err=%v", again, err)
			}
		})
	}
}

// A board that answers a permanent failure (gone, or listing nothing) says all
// it will about its page: the lead is settled, so it does not take a place in
// every later run's cap.
func TestLeadsExpand_APermanentBoardFailureSettlesTheLead(t *testing.T) {
	for _, c := range []struct {
		rung, want string
		gone       bool
	}{{"direct", expandUnreach, true}, {"redirect", expandUnreach, true}, {"linked", expandNone, true}, {"direct", expandNone, false}} {
		t.Run(fmt.Sprintf("%s gone=%v", c.rung, c.gone), func(t *testing.T) {
			e := newEnv(t)
			ldRoute(t, e)
			board := "https://job-boards.greenhouse.io/gone"
			pageURL := "https://careers.example.com/careers"
			switch c.rung {
			case "direct":
				pageURL = board
			case "redirect":
				e.sc.Client.HTTP.Transport = expansionRedirectTransport{next: e.sc.Client.HTTP.Transport, target: board}
			case "linked":
				ldPage(e, "/careers", `<a href="`+board+`">Open roles</a>`)
			}
			if c.gone {
				e.srv.set("/v1/boards/gone/jobs", route{status: 404})
			} else {
				e.srv.json("/v1/boards/gone/jobs", ghJobs("gone"))
			}
			id := ldCareers(t, e, pageURL)
			res, err := e.sc.ExpandLeads(e.ctx, ExpandRequest{Limit: 1})
			if err != nil || res.Read != 1 || res.Failed != 0 || res.Pending != 0 {
				t.Fatalf("result=%+v err=%v", res, err)
			}
			if _, outcome := ldOutcome(t, e, pageURL); outcome != c.want {
				t.Fatalf("outcome %q, want %q", outcome, c.want)
			}
			l, err := e.rp.LeadGet(e.ctx, id)
			if err != nil || (c.want == expandUnreach) != (l.Category == LeadUnreachable) {
				t.Fatalf("lead=%+v err=%v", l, err)
			}
			if again, err := e.sc.ExpandLeads(e.ctx, ExpandRequest{Limit: 1}); err != nil || again.Read != 0 {
				t.Fatalf("a settled page was read again: %+v err=%v", again, err)
			}
		})
	}
}

func TestLeadsExpand_ForcedWorkPrecedesOrdinaryBacklog(t *testing.T) {
	e := newEnv(t)
	ldRoute(t, e)
	ldPage(e, "/ordinary", "No openings")
	ldPage(e, "/forced", "No openings")
	ldCareers(t, e, "https://careers.example.com/ordinary")
	forced := ldCareers(t, e, "https://careers.example.com/forced")
	res, err := e.sc.ExpandLeads(e.ctx, ExpandRequest{Limit: 1, Force: []int64{forced}})
	if err != nil || res.Read != 1 || res.Pending != 1 || e.srv.hitCount("/forced") != 1 || e.srv.hitCount("/ordinary") != 0 {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	e.srv.set("/forced", route{status: 503})
	res, err = e.sc.ExpandLeads(e.ctx, ExpandRequest{Limit: 1, Force: []int64{forced}})
	if err == nil || res.Read != 1 || res.Failed != 1 || res.Pending != 2 || e.srv.hitCount("/ordinary") != 0 {
		t.Fatalf("failed force result=%+v err=%v", res, err)
	}
}

type expansionRedirectTransport struct {
	next   http.RoundTripper
	target string
}

func (t expansionRedirectTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/careers" {
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{t.target}}, Body: http.NoBody, Request: r}, nil
	}
	return t.next.RoundTrip(r)
}
