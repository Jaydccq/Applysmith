package signals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"applysmith/internal/llm"
)

var docIDRE = regexp.MustCompile(`document id=(\d+)`)

func firstDocID(prompt string) int {
	m := docIDRE.FindStringSubmatch(prompt)
	if m == nil {
		return 0
	}
	return atoi(m[1])
}

// assessReply builds the model's JSON for the first document of the prompt.
func assessReply(prompt, quote string, intent string, conf float64, growth string, typ string) string {
	return fmt.Sprintf(`{"event_type":%q,"hiring_intent":%q,"affected_teams":["engineering"],"use_of_funds_growth":%s,"scale":"explicit_headcount","strength":8,"confidence":%v,"reasoning":"r","evidence_quotes":[{"document_id":%d,"quote":%s}]}`,
		typ, intent, growth, conf, firstDocID(prompt), jsonString(quote))
}

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }

func (e *env) assessing(quote, intent string, conf float64, growth, typ string) {
	e.brain.fn = func(task, prompt string) (string, bool) {
		if task != "signal-assess" {
			return "", false
		}
		return assessReply(prompt, quote, intent, conf, growth, typ), true
	}
}

func (e *env) status(eventID int64) string {
	var s string
	if err := e.st.Read().QueryRowContext(e.ctx, `SELECT status FROM signal_assessments WHERE event_id = ?`, eventID).Scan(&s); err != nil {
		return "none"
	}
	return s
}

const raiseBody = "Example Corp raised $14M. The company plans to hire 20 engineers this year."

func TestAssess_SchemaFields(t *testing.T) {
	e := newEnv(t)
	_, ev, _ := e.company("Example Corp", raiseBody, epoch.Add(-24*time.Hour), adapterAlerts)
	e.assessing("plans to hire 20 engineers", IntentStrong, 0.9, "true", TypeFunding)
	st, err := e.svc.assessPending(e.ctx)
	if err != nil || st.OK != 1 {
		t.Fatalf("stats %+v, %v", st, err)
	}
	var a Assessment
	e.st.Read().QueryRowContext(e.ctx, `SELECT id FROM signal_assessments WHERE event_id = ?`, ev.ID).Scan(&a.ID)
	got, _ := e.svc.repo.Assessment(e.ctx, a.ID)
	if got.Status != StatusOK || got.Type != TypeFunding || got.Intent != IntentStrong || got.Scale != ScaleHeadcount || got.Strength != 8 || got.Confidence != 0.9 ||
		got.Growth == nil || !*got.Growth || len(got.Teams) != 1 || len(got.Quotes) != 1 || got.Rubric != RubricVersion {
		t.Fatalf("assessment = %+v", got)
	}
	// The cache: a second pass makes no call for an assessed event.
	before := e.brain.count("signal-assess")
	if _, err := e.svc.assessPending(e.ctx); err != nil || e.brain.count("signal-assess") != before {
		t.Fatalf("a cached event cost a call")
	}
}

func TestAssess_SchemaInvalidAfterOneRepairGoesToReview(t *testing.T) {
	e := newEnv(t)
	_, ev, _ := e.company("Example Corp", raiseBody, epoch.Add(-24*time.Hour), adapterAlerts)
	e.brain.fn = func(task, prompt string) (string, bool) {
		return `{"event_type":"layoffs","hiring_intent":"strong"}`, true
	}
	st, err := e.svc.assessPending(e.ctx)
	if err != nil || st.SchemaInvalid != 1 {
		t.Fatalf("stats %+v, %v", st, err)
	}
	if e.status(ev.ID) != StatusSchemaInvalid || e.count(`SELECT count(*) FROM signal_review WHERE state = 'open'`) != 1 {
		t.Fatal("not held")
	}
	if n := e.brain.count("signal-assess"); n != 2 {
		t.Fatalf("calls = %d, want the call and one repair", n)
	}
}

func TestAssess_PooledIssuerZeroSignalNoModelCall(t *testing.T) {
	e := newEnv(t)
	c, err := e.svc.repo.UpsertCompany(e.ctx, "Example Growth Fund LP", RuleCanon("Example Growth Fund LP"))
	if err != nil {
		t.Fatal(err)
	}
	d := e.doc(Doc{SourceID: SourceEdgar, Adapter: adapterEdgar, URL: "https://www.sec.gov/x/1", PublishedAt: epoch, Title: "Form D", Body: "filed",
		Filing: &Filing{Issuer: "Example Growth Fund LP", FirstSale: epoch}})
	evID, _, _ := e.svc.repo.AttachEvent(e.ctx, d, c.ID, TypeFunding, epoch, nil)
	st, err := e.svc.assessPending(e.ctx)
	if err != nil || st.Pooled != 1 || e.brain.total() != 0 {
		t.Fatalf("stats %+v calls %d, %v", st, e.brain.total(), err)
	}
	if e.status(evID) != StatusOK {
		t.Fatalf("status = %s", e.status(evID))
	}
	var intent string
	var conf float64
	e.st.Read().QueryRowContext(e.ctx, `SELECT hiring_intent, confidence FROM signal_assessments WHERE event_id = ?`, evID).Scan(&intent, &conf)
	if intent != IntentNone {
		t.Fatalf("intent = %s", intent)
	}
	for _, n := range []string{"Acme Capital", "Foo Ventures", "Bar Holdings", "Baz Real Estate", "Qux Acquisition Corp", "X Trust", "Y SPV", "Z Partners", "Q, a series of W LLC", "Example Equity Investors"} {
		if !PooledVehicle(n) {
			t.Errorf("%q is not read as pooled", n)
		}
	}
	if PooledVehicle("Example Robotics Inc") {
		t.Error("an operating company reads as pooled")
	}
}

type noRouteModel struct{}

func (noRouteModel) Structured(context.Context, llm.Request, json.RawMessage, any) (*llm.Response, error) {
	return nil, &llm.Error{Kind: llm.KindNoRoute, Task: "signal-assess"}
}

func TestAssess_NoModelRouteLeavesEventsPendingThenAssessed(t *testing.T) {
	e := newEnv(t)
	_, ev, _ := e.company("Example Corp", raiseBody, epoch.Add(-24*time.Hour), adapterAlerts)
	good := e.svc.model
	e.svc.model = noRouteModel{}
	e.sets.Portals.Sources = nil
	sum := e.tick()
	if e.status(ev.ID) != "none" || !containsNote(sum.Notes, "assessment skipped") {
		t.Fatalf("status %s notes %v", e.status(ev.ID), sum.Notes)
	}
	e.svc.model = good
	e.assessing("plans to hire 20 engineers", IntentStrong, 0.9, "true", TypeFunding)
	e.tick()
	if e.status(ev.ID) != StatusOK {
		t.Fatalf("status after a route = %s", e.status(ev.ID))
	}
}

// answerModel fails every call with one error kind and counts the calls.
type answerModel struct {
	kind  llm.Kind
	calls int
}

func (m *answerModel) Structured(context.Context, llm.Request, json.RawMessage, any) (*llm.Response, error) {
	m.calls++
	return nil, &llm.Error{Kind: m.kind, Task: "signal-assess"}
}

func TestAssess_RejectedCallIsNeverRetried(t *testing.T) {
	for _, kind := range []llm.Kind{llm.KindAuth, llm.KindBadRequest} {
		t.Run(string(kind), func(t *testing.T) {
			e := newEnv(t)
			_, ev, _ := e.company("Example Corp", raiseBody, epoch.Add(-24*time.Hour), adapterAlerts)
			m := &answerModel{kind: kind}
			e.svc.model = m
			if _, err := e.svc.assessPending(e.ctx); err != nil {
				t.Fatal(err)
			}
			if m.calls != 1 || e.status(ev.ID) != "none" {
				t.Fatalf("calls = %d, status = %s: a refused call is a real answer", m.calls, e.status(ev.ID))
			}
		})
	}
}

func containsNote(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

type flakyModel struct {
	inner Model
	fail  string
}

func (f flakyModel) Structured(ctx context.Context, req llm.Request, schema json.RawMessage, out any) (*llm.Response, error) {
	if strings.Contains(req.Prompt, f.fail) {
		return nil, errors.New("read timeout")
	}
	return f.inner.Structured(ctx, req, schema, out)
}

func TestVerify_QuotesVerbatimAfterEntityDecodeNoFuzzy(t *testing.T) {
	docs := []StoredDoc{{ID: 7, Body: "K&amp;M raises $5M. It said “we will hire” — soon.\n  More   text."}}
	mk := func(q string) Assessment {
		return Assessment{Intent: IntentStrong, Quotes: []Quote{{DocumentID: 7, Quote: q}}}
	}
	for q, want := range map[string]bool{
		"K&M raises $5M":           true,
		"\"we will hire\" - soon.": true,
		"More text.":               true,
		"k&m raises $5m":           false,
		"K&M raised $5M":           false,
		"We will hire":             false,
		"":                         false,
	} {
		if got := quotesVerify(mk(q), docs); got != want {
			t.Errorf("quote %q verified = %v", q, got)
		}
	}
	wrongDoc := Assessment{Intent: IntentStrong, Quotes: []Quote{{DocumentID: 9, Quote: "K&M raises $5M"}}}
	if quotesVerify(wrongDoc, docs) {
		t.Error("a quote citing another document verified")
	}
	if quotesVerify(Assessment{Intent: IntentStrong}, docs) {
		t.Error("a hiring claim with no quote verified")
	}

	e := newEnv(t)
	_, ev, _ := e.company("Example Corp", raiseBody, epoch.Add(-24*time.Hour), adapterAlerts)
	e.assessing("Example Corp will hire 500 engineers", IntentStrong, 0.9, "true", TypeFunding)
	st, _ := e.svc.assessPending(e.ctx)
	if st.QuoteFailed != 1 || e.status(ev.ID) != StatusQuoteFailed || e.count(`SELECT count(*) FROM signal_review`) != 1 {
		t.Fatalf("stats %+v status %s", st, e.status(ev.ID))
	}
	if err := e.svc.Rescore(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM signal_tiers WHERE momentum > 0`); n != 0 {
		t.Fatal("a failed quote added to a score")
	}
}

func TestAssess_FundingUseGuard_RunwayOpensBlock(t *testing.T) {
	e := newEnv(t)
	c, ev, _ := e.company("Example Corp", "Example Corp raised $14M. The proceeds will extend the company's runway through 2028.", epoch.Add(-24*time.Hour), adapterAlerts)
	e.assessing("The proceeds will extend the company's runway through 2028.", IntentStrong, 0.9, "true", TypeFunding)
	if _, err := e.svc.assessPending(e.ctx); err != nil {
		t.Fatal(err)
	}
	var intent string
	var growth *int
	e.st.Read().QueryRowContext(e.ctx, `SELECT hiring_intent, use_of_funds FROM signal_assessments WHERE event_id = ?`, ev.ID).Scan(&intent, &growth)
	if intent != IntentNone || growth == nil || *growth != 0 {
		t.Fatalf("intent %s growth %v", intent, growth)
	}
	if blocked, _ := e.svc.repo.ActiveBlock(e.ctx, c.ID, e.clk.now()); !blocked {
		t.Fatal("no hiring block opened")
	}

	// Growth language without runway language sets growth true.
	e2 := newEnv(t)
	_, ev2, _ := e2.company("Beta Corp", "Beta Corp raised $9M. It is hiring across engineering.", epoch.Add(-24*time.Hour), adapterAlerts)
	e2.assessing("It is hiring across engineering.", IntentModerate, 0.9, "null", TypeFunding)
	e2.svc.assessPending(e2.ctx)
	var g2 *int
	e2.st.Read().QueryRowContext(e2.ctx, `SELECT use_of_funds FROM signal_assessments WHERE event_id = ?`, ev2.ID).Scan(&g2)
	if g2 == nil || *g2 != 1 {
		t.Fatalf("growth = %v", g2)
	}
}

func TestAssess_MidConfidenceAndContradictoryHeld(t *testing.T) {
	for conf, want := range map[float64]string{0.65: StatusMidConfidence, 0.66: StatusOK, 0.35: StatusMidConfidence, 0.34: StatusOK} {
		e := newEnv(t)
		_, ev, _ := e.company("Example Corp", raiseBody, epoch.Add(-24*time.Hour), adapterAlerts)
		e.assessing("plans to hire 20 engineers", IntentStrong, conf, "true", TypeFunding)
		e.svc.assessPending(e.ctx)
		if got := e.status(ev.ID); got != want {
			t.Errorf("confidence %v: status %s, want %s", conf, got, want)
		}
	}
	e := newEnv(t)
	c, ev, _ := e.company("Example Corp", raiseBody, epoch.Add(-24*time.Hour), adapterAlerts)
	layoff := e.doc(Doc{URL: "https://news.example.test/layoff", PublishedAt: epoch.Add(-24 * 20 * time.Hour), Title: "Example Corp lays off staff", Body: "Example Corp announced a hiring freeze."})
	if _, _, err := e.svc.repo.AttachEvent(e.ctx, layoff, c.ID, TypeOther, epoch.Add(-24*20*time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	e.assessing("plans to hire 20 engineers", IntentStrong, 0.9, "true", TypeFunding)
	// Only the funding event is under test: assess it first.
	events, _ := e.svc.repo.PendingEvents(e.ctx, RubricVersion, 0, 10)
	for _, p := range events {
		if p.ID == ev.ID {
			if err := e.svc.assessEvent(e.ctx, p, &AssessStats{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if e.status(ev.ID) != StatusContradictory {
		t.Fatalf("status = %s", e.status(ev.ID))
	}
	// Held: no contribution and no scan, until a user approves.
	e.svc.Rescore(e.ctx)
	if tr, _, _ := e.svc.repo.Tier(e.ctx, c.ID); tr.Momentum != 0 {
		t.Fatalf("momentum = %v while held", tr.Momentum)
	}
	items, _ := e.svc.Review(e.ctx, false)
	if len(items) != 1 {
		t.Fatalf("review items = %d", len(items))
	}
	if _, err := e.svc.Decide(e.ctx, items[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if e.status(ev.ID) != StatusOK {
		t.Fatalf("status after approval = %s", e.status(ev.ID))
	}
	if tr, _, _ := e.svc.repo.Tier(e.ctx, c.ID); tr.Momentum <= 0 {
		t.Fatal("an approved assessment adds nothing")
	}
	if _, err := e.svc.Decide(e.ctx, items[0].ID, false); !errors.Is(err, ErrDecided) {
		t.Fatalf("second decision: %v", err)
	}
}

// A full pass of events whose assessment always fails does not keep the events
// after them waiting.
func TestAssess_EventsThatAlwaysFailDoNotStarveTheOnesAfterThem(t *testing.T) {
	e := newEnv(t)
	for i := range maxAssessPerTick {
		e.company(fmt.Sprintf("Broken %d Corp", i), "It raised money.", epoch.Add(-48*time.Hour), adapterAlerts)
	}
	_, good, _ := e.company("Good Corp", "Good Corp raised $7M and will hire 12 engineers.", epoch.Add(-24*time.Hour), adapterAlerts)
	e.assessing("will hire 12 engineers", IntentStrong, 0.9, "true", TypeFunding)
	e.svc.model = flakyModel{inner: e.svc.model, fail: "Broken"}
	st, err := e.svc.assessPending(e.ctx)
	if err != nil || st.Errors != maxAssessPerTick || e.status(good.ID) != "none" {
		t.Fatalf("first pass = %+v, %v, the event after it %s", st, err, e.status(good.ID))
	}
	if st, err = e.svc.assessPending(e.ctx); err != nil {
		t.Fatal(err)
	}
	if e.status(good.ID) != StatusOK {
		t.Fatalf("the event after %d failing ones was not reached by the second pass: %+v", maxAssessPerTick, st)
	}
	if st.OK != 1 || st.Errors != maxAssessPerTick-1 {
		t.Fatalf("second pass = %+v", st)
	}
}
