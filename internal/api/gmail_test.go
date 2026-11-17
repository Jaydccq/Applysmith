package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/gmail"
	"applysmith/internal/gmail/imaptest"
)

const (
	secretCode       = "482913"
	secretActivation = "a1b2c3d4e5"
	gmTenant         = "acme.wd5.myworkdayjobs.com"
)

// gmPassword is the app password the fake Gmail accepts.
var gmPassword = strings.Repeat("wxyz", 4)

func gmAddr(local, domain string) string { return local + "@" + domain }

// gmMessage is one message of the fake Gmail. Its ids are hexadecimal, as
// Gmail's are; the thread defaults to the message.
type gmMessage struct {
	id, thread, from, subject, text, html string
	ms                                    int64
}

// fakeGmail is a scripted Gmail over IMAP that answers every search in All
// Mail with every message it holds: the tests here do not model Gmail's
// search, the lookups and the classifier sort what it returns.
type fakeGmail struct {
	*imaptest.Server
	mu      sync.Mutex
	ids     []string
	queries []string
}

func newFakeGmail(t *testing.T) *fakeGmail {
	srv, err := imaptest.New(gmAddr("alex", "example.org"), gmPassword)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	g := &fakeGmail{Server: srv}
	srv.SearchWith(func(attr, q string) []string {
		if attr != `\All` {
			return nil
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		g.queries = append(g.queries, q)
		return slices.Clone(g.ids)
	})
	return g
}

func (g *fakeGmail) add(m gmMessage) {
	g.Add(imaptest.AllMail, imaptest.Message{ID: m.id, Thread: m.thread, From: m.from, Subject: m.subject, Text: m.text, HTML: m.html, MS: m.ms})
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ids = append(g.ids, m.id)
}

func (g *fakeGmail) searched() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.queries)
}

func (g *fakeGmail) requestCount() int { return len(g.Commands("")) }

// gmailHarness is a domain harness whose Gmail stack talks to a fake Gmail
// and whose account is connected.
func gmailHarness(t *testing.T, mods ...func(*Config)) (*harness, *fakeGmail) {
	t.Helper()
	h, g := unconnectedGmailHarness(t, mods...)
	h.connectGmail()
	return h, g
}

// unconnectedGmailHarness is gmailHarness before the connection exists, for a
// test that must change a setting while the scheduler's Gmail timers are idle.
func unconnectedGmailHarness(t *testing.T, mods ...func(*Config)) (*harness, *fakeGmail) {
	t.Helper()
	g := newFakeGmail(t)
	all := append([]func(*Config){func(c *Config) { c.Domain.Gmail = gmail.Options{Dial: g.Dial, NoWatch: true} }}, mods...)
	return newDomainHarness(t, all...), g
}

// connectGmail connects the fake Gmail's account as the dashboard does.
func (h *harness) connectGmail() {
	h.t.Helper()
	if _, err := h.domainOf().gmail.Service.Connect(context.Background(), gmAddr("alex", "example.org"), gmPassword); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) ms(offset time.Duration) int64 { return h.clock.now().Add(offset).UnixMilli() }

func (h *harness) since(offset time.Duration) string {
	return url.QueryEscape(h.clock.now().Add(offset).UTC().Format(time.RFC3339Nano))
}

func TestGmailRoutes_AreStandardSecretWhereTheyCarryASecretAndRemoteDenied(t *testing.T) {
	for _, c := range []struct {
		path, response string
		secret         bool
	}{
		{"/api/gmail/health", "GmailHealth", false},
		{"/api/gmail/runs", "GmailRunList", false},
		{"/api/gmail/runs/{id}", "GmailRun", false},
		{"/api/gmail/otp/latest", "OneTimeCode", true},
		{"/api/gmail/activation-link/latest", "GmailAccountLink", true},
		{"/api/gmail/reset-link/latest", "GmailAccountLink", true},
		{"/api/gmail/signin-link/latest", "GmailSigninLink", true},
	} {
		r := routeOf(t, "GET", c.path)
		if r.Access != AccessStandard || r.Remote != RemoteDenied || r.Limit != LimitGeneral || r.Body != BodyNone || r.Request != PayloadNone ||
			r.Response != c.response || r.Secret != c.secret || r.SessionOnly {
			t.Errorf("%s: %+v", c.path, r)
		}
	}
}

func TestGmailLookupRoutesServeOnlyThePairedClientAndTheDashboard(t *testing.T) {
	h := newDomainHarness(t)
	for name, fn := range map[string]apikit.Handler{
		"otp": h.srv.gmail.LatestOTP, "activation": h.srv.gmail.AccountLink(false), "reset": h.srv.gmail.AccountLink(true), "signin": h.srv.gmail.LatestSigninLink,
	} {
		for _, kind := range []apikit.PrincipalKind{apikit.PrincipalRemoteDevice, apikit.PrincipalNone} {
			_, err := fn(&apikit.Call{P: apikit.Principal{Kind: kind}, R: httptestRequest(t)})
			if ae, ok := err.(*apikit.Error); !ok || ae.Code != wire.CodeForbidden {
				t.Errorf("%s as %s: %v", name, kind, err)
			}
		}
	}
	for _, p := range []string{"otp/latest?since=2026-01-01T00:00:00Z", "activation-link/latest?host=a.example.com", "reset-link/latest?host=a.example.com&after=2026-01-01T00:00:00Z", "signin-link/latest?since=2026-01-01T00:00:00Z"} {
		if r := h.do(call{method: "GET", path: "/api/gmail/" + p}); r.StatusCode != 401 {
			t.Errorf("%s without a credential: %d", p, r.StatusCode)
		}
	}
}

func TestGmailOTP_CodeFromTheExpectedSenderOnly(t *testing.T) {
	h, g := gmailHarness(t)
	ats := "Greenhouse <" + gmAddr("no-reply", "greenhouse-mail.io") + ">"
	g.add(gmMessage{id: "a75", from: ats, subject: "Security code for your application", text: "Your security code is: 8fK2pQ7s", ms: h.ms(-time.Minute)})
	g.add(gmMessage{id: "7e", from: "Other <" + gmAddr("x", "other.example.org") + ">", subject: "Verification", text: "Your verification code is " + secretCode, ms: h.ms(-30 * time.Second)})
	r := h.get("/api/gmail/otp/latest?since=" + h.since(-5*time.Minute) + "&fromDomain=greenhouse-mail.io")
	var env wire.Success[wire.OneTimeCode]
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	strictInto(t, r.body, &env)
	if env.Data.Code != "8fK2pQ7s" || env.Data.From != ats || env.Data.ReceivedAt.UnixMilli() != h.ms(-time.Minute) {
		t.Fatalf("%+v", env.Data)
	}
	// Without a sender bound the newest qualifying mail wins.
	r = h.get("/api/gmail/otp/latest?since=" + h.since(-5*time.Minute))
	strictInto(t, r.body, &env)
	if env.Data.Code != secretCode {
		t.Fatalf("%+v", env.Data)
	}
	if q := g.searched()[0]; !strings.Contains(q, "after:") || !strings.Contains(q, "from:greenhouse-mail.io") {
		t.Fatalf("query %q", q)
	}
}

func TestGmailOTP_BadRequests(t *testing.T) {
	h, g := gmailHarness(t)
	before := g.requestCount()
	for q, want := range map[string]int{
		"":                                      400,
		"?since=2026-01-01T00:00:00Z&token=abc": 400,
	} {
		if r := h.get("/api/gmail/otp/latest" + q); r.StatusCode != want || r.errCode() != "bad_request" && !strings.Contains(q, "token") {
			t.Errorf("%q: %d %s", q, r.StatusCode, r.body)
		}
	}
	if n := g.requestCount() - before; n != 0 {
		t.Fatalf("a rejected request reached Gmail %d times", n)
	}
}

func TestGmailOTP_CodeNeverReachesLogsOrTheDatabase(t *testing.T) {
	h, g := gmailHarness(t)
	g.add(gmMessage{id: "e0", from: gmAddr("a", "greenhouse-mail.io"), subject: "Verification", text: "Your verification code is " + secretCode, ms: h.ms(-time.Minute)})
	if r := h.get("/api/gmail/otp/latest?since=" + h.since(-5*time.Minute)); r.StatusCode != 200 || !strings.Contains(string(r.body), secretCode) {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if strings.Contains(h.logText(), secretCode) {
		t.Fatal("the code reached the log")
	}
	if h.dbHolds(secretCode) {
		t.Fatal("the code reached the database")
	}
}

func workdayMail(h *harness, id, href, text string, offset time.Duration) gmMessage {
	return gmMessage{id: id, from: "Workday <" + gmAddr("acme", "otp.workday.com") + ">", subject: "Verify your candidate account", html: text + `<a href="` + href + `">go</a>`, ms: h.ms(offset)}
}

// An activation address printed in the mail is the link (owner decision);
// a link to another host never is, href or text.
func TestGmailActivationLink_RouteReturnsTheTenantsLinkFromAnHrefOrThePrintedText(t *testing.T) {
	h, g := gmailHarness(t)
	good := "https://" + gmTenant + "/Careers/activate/" + secretActivation
	g.add(workdayMail(h, "dec0", "https://evil.test/activate/zzzzzzzz99", "https://evil.test/Careers/activate/zzzzzzzz99", time.Minute))
	if r := h.get("/api/gmail/activation-link/latest?host=" + gmTenant); r.StatusCode != 404 {
		t.Fatalf("another host's link qualified: %d %s", r.StatusCode, r.body)
	}
	g.add(workdayMail(h, "9e1", "https://evil.test/x", good, 2*time.Minute))
	r := h.get("/api/gmail/activation-link/latest?host=" + strings.ToUpper(gmTenant))
	var env wire.Success[wire.GmailAccountLink]
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	strictInto(t, r.body, &env)
	if env.Data.URL != good || env.Data.Host != gmTenant {
		t.Fatalf("%+v", env.Data)
	}
	if strings.Contains(h.logText(), secretActivation) || h.dbHolds(secretActivation) {
		t.Fatal("the link reached the log or the database")
	}
}

func TestGmailActivationAndResetLink_Validation(t *testing.T) {
	h, g := gmailHarness(t)
	before := g.requestCount()
	for _, p := range []string{
		"activation-link/latest",
		"activation-link/latest?host=" + gmTenant + "&sinceDays=0",
		"reset-link/latest?after=2026-01-01T00:00:00Z",
	} {
		if r := h.get("/api/gmail/" + p); r.StatusCode != 400 || r.errCode() != "bad_request" {
			t.Errorf("%s: %d %s", p, r.StatusCode, r.body)
		}
	}
	if g.requestCount() != before {
		t.Fatal("mail was read for a rejected request")
	}
}

func TestGmailSigninLink_RouteIsDisplayOnlyAndNeverFetched(t *testing.T) {
	var opened atomic.Int32
	site := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { opened.Add(1) }))
	defer site.Close()
	h, g := gmailHarness(t)
	link := site.URL + "/never-open/magic-link?token=Ab3dEf5hIjK"
	g.add(gmMessage{id: "5", from: gmAddr("no-reply", "ats.example.com"), subject: "Your sign-in link",
		html: `<p>Sign in to your account</p><a href="` + link + `">Sign in</a>`, ms: h.ms(-time.Minute)})
	if r := h.get("/api/gmail/signin-link/latest"); r.StatusCode != 400 {
		t.Fatalf("missing since: %d %s", r.StatusCode, r.body)
	}
	r := h.get("/api/gmail/signin-link/latest?since=" + h.since(-5*time.Minute))
	var env wire.Success[wire.GmailSigninLink]
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	strictInto(t, r.body, &env)
	if env.Data.URL != link || !env.Data.DisplayOnly || env.Data.Host == "" || env.Data.Subject == "" || len(env.Data.Reasons) == 0 {
		t.Fatalf("%+v", env.Data)
	}
	if opened.Load() != 0 {
		t.Fatal("the product requested the link")
	}
}

func TestGmailHealthAndRuns_AreOfflineReadModels(t *testing.T) {
	h, g := gmailHarness(t)
	before := g.requestCount()
	r := h.get("/api/gmail/health")
	var env wire.Success[wire.GmailHealth]
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	strictInto(t, r.body, &env)
	if env.Data.Sync != "never-run" || env.Data.Running || env.Data.Login != gmail.LoginOK {
		t.Fatalf("%+v", env.Data)
	}
	if got := h.get("/api/gmail/runs"); got.StatusCode != 200 || len(got.data()["runs"].([]any)) != 0 {
		t.Fatalf("%d %s", got.StatusCode, got.body)
	}
	if got := h.get("/api/gmail/runs/99"); got.StatusCode != 404 {
		t.Fatalf("unknown run: %d", got.StatusCode)
	}
	if g.requestCount() != before {
		t.Fatal("a read model made a Gmail request")
	}
}

// A sync the dashboard did not start, the schedule's, moves the gmail topic
// when it ends, so the mail views follow it without reloading on every run's
// change of state.
func TestGmailSync_AScheduledRunMovesTheGmailTopic(t *testing.T) {
	h, _ := gmailHarness(t)
	before := h.topicRev("gmail")
	if _, err := h.domainOf().gmail.Syncer.Run(context.Background(), gmail.TriggerScheduled); err != nil {
		t.Fatal(err)
	}
	if got := h.topicRev("gmail"); got != before+1 {
		t.Fatalf("gmail topic %d -> %d, want one notice", before, got)
	}
}

func TestGmailRuns_ListNewestFirstAndOneRunWithStages(t *testing.T) {
	h, _ := gmailHarness(t)
	stack := h.domainOf().gmail
	for i := 0; i < 3; i++ {
		if _, err := stack.Syncer.Run(context.Background(), gmail.TriggerManual); err != nil {
			t.Fatal(err)
		}
	}
	r := h.get("/api/gmail/runs?limit=2")
	var list wire.Success[wire.GmailRunList]
	strictInto(t, r.body, &list)
	if len(list.Data.Runs) != 2 || list.Data.Runs[0].ID != 3 || list.Data.Runs[1].ID != 2 {
		t.Fatalf("%s", r.body)
	}
	one := h.get("/api/gmail/runs/3")
	var run wire.Success[wire.GmailRun]
	strictInto(t, one.body, &run)
	if run.Data.ID != 3 || len(run.Data.Stages) != 5 || run.Data.Trigger != "manual" || run.Data.StartedAt.IsZero() {
		t.Fatalf("%s", one.body)
	}
}

func TestGmail_RetentionSweepRunsInTheSchedulersDailySweep(t *testing.T) {
	h, _ := gmailHarness(t)
	sched := h.srv.domain.Load().sched
	// The start-up sweep runs in the background; a sweep still running when the
	// next slot is due would hold that slot, so let it finish first.
	sched.Wait()
	old := h.clock.now().Add(-40 * 24 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	fresh := h.clock.now().UTC().Format("2006-01-02T15:04:05.000Z")
	err := h.srv.store.Load().Write(context.Background(), func(tx *sql.Tx) error {
		stmts := []string{
			`INSERT INTO gmail_runs (id, trigger_kind, status, mode, started_at, classifier_version, query_version, stages_json, counts_json)
				VALUES (1, 'cli', 'succeeded', 'bootstrap', '` + old + `', 'v', 'q', '[]', '{}')`,
		}
		for _, id := range []string{"old", "new"} {
			at := map[string]string{"old": old, "new": fresh}[id]
			stmts = append(stmts,
				`INSERT INTO gmail_evidence (message_id, thread_id, received_ms, from_header, to_header, cc_header, subject, snippet, labels, first_run_id, first_seen_at)
					VALUES ('`+id+`', 't', 1, 'f', 't', 'c', 's', 'sn', '[]', 1, '`+at+`')`,
				`INSERT INTO gmail_bodies (message_id, body_text, raw_text, bytes, stored_at) VALUES ('`+id+`', 'body', 'raw', 8, '`+at+`')`)
		}
		for _, q := range stmts {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The sweep that ran at start is done; the next 04:00 slot runs it again.
	h.clock.advance(25 * time.Hour)
	sched.Tick(context.Background())
	sched.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for h.rowCount("gmail_bodies WHERE message_id = 'old'") != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if h.rowCount("gmail_bodies WHERE message_id = 'old'") != 0 {
		t.Fatal("the body past 30 days was not removed")
	}
	if h.rowCount("gmail_bodies WHERE message_id = 'new'") != 1 || h.rowCount("gmail_evidence") != 2 {
		t.Fatal("the sweep removed more than the old body")
	}
}

// gmailReads are the IMAP commands of a read-only session.
var gmailReads = regexp.MustCompile(`^(CAPABILITY|LOGIN|LIST|EXAMINE|UID SEARCH|UID FETCH|LOGOUT)\b`)

func componentStatus(t *testing.T, h *harness, name string) wire.ComponentStatus {
	t.Helper()
	for _, c := range h.srv.components.Load().list {
		if c.Name == name {
			return c.Status
		}
	}
	t.Fatalf("no component %s", name)
	return ""
}
