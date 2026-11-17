package assist

import (
	"context"
	"strings"
	"testing"

	"applysmith/internal/answerbank/match"
	"time"
)

func TestAnswerBank_Compose_HeldCompensationNeverTargetFigure(t *testing.T) {
	for _, q := range []string{"What forms of deferred compensation do you currently hold?", "What is your current net monthly salary?", "Do you hold unvested equity?"} {
		if !match.AsksHeldCompensation(q) {
			t.Errorf("%q is held compensation", q)
		}
		e := newCompose(t)
		e.respond(func(_, _ string) string { return "My target range is 100000 to 120000." })
		if got := e.compose(ComposeRequest{Question: q}); got.Filled || e.ledgerCalls("") != 0 {
			t.Errorf("%q composed: %+v", q, got)
		}
	}
	if match.AsksHeldCompensation("What is your current salary expectation?") {
		t.Error("a current salary expectation is the target question")
	}
}

func TestAnswerBank_FreeText_RefusalsOutrankLiveFactLane(t *testing.T) {
	// "What is the nature of the conviction?" is also an impersonal wh-question;
	// the refusal wins and nothing is searched or composed.
	q := "What is the nature of the conviction?"
	if rf := refusalOf(q, true); !rf.Refused || rf.Reason != match.ReasonSensitiveCategory {
		t.Fatalf("%+v", rf)
	}
	e := newCompose(t)
	e.respond(func(_, _ string) string { return longPlain })
	res := e.runHand(match.ReasonNoEntry, enabled, profileSources(), textQ("#t", q, true))
	if res[0].Reason != match.ReasonSensitiveCategory || e.ledgerCalls("") != 0 {
		t.Fatalf("%+v", res[0])
	}
}

// Adobe's Workday asks "What's their name or email address?" after How Did You
// Hear About Us: a web search had typed a stranger's name. Someone else's name
// or contact details are refused before any call.
func TestAnswerBank_FreeText_AnotherPersonsNameOrContactIsRefused(t *testing.T) {
	for _, q := range []string{"What's their name or email address?", "Referrer's email", "Name of the employee who referred you", "Recruiter name"} {
		if rf := refusalOf(q, false); !rf.Refused {
			t.Errorf("%q: %+v", q, rf)
		}
	}
	e := newCompose(t)
	e.respond(func(_, _ string) string { return longPlain })
	res := e.runHand(match.ReasonNoEntry, enabled, profileSources(), textQ("#t", "What's their name or email address?", true))
	if res[0].Outcome != match.OutcomeAbstained || e.ledgerCalls("") != 0 {
		t.Fatalf("%+v", res[0])
	}
}

func TestAnswerBank_TextFallback_RefusalDoesNotSpendBudget(t *testing.T) {
	// A sensitive question, a trap and a question about a structured fact spend
	// neither the request nor the session budget, so the next real question still
	// has its call.
	e := newCompose(t)
	e.respond(func(_, _ string) string {
		return "I built the Example Ledger in Go for Example Corp, and I enjoy small teams and clear tests every day."
	})
	qs := []match.Question{
		textQ("#a", "What is your religion?", true), textQ("#b", "If you are human, leave this field empty", true),
		textQ("#c", "What are your salary expectations?", true),
	}
	for i := 0; i < 4; i++ {
		qs = append(qs, textQ("#r"+string(rune('0'+i)), essayQuestion+strings.Repeat(" ", i)+" (part "+string(rune('a'+i))+")", true))
	}
	res := e.runHand(match.ReasonNoEntry, enabled, profileSources(), qs...)
	for _, r := range res[:3] {
		if r.Outcome != match.OutcomeAbstained {
			t.Fatalf("%+v", r)
		}
	}
	if res[3].Outcome != match.OutcomeFilledModel {
		t.Fatalf("the first real question lost its call: %+v", res[3])
	}
	if got := e.svc.Sessions().Count(SessionKey("jobs.example.com", "Example Corp")); got != e.ledgerCalls(TaskNarrative) {
		t.Fatalf("session count %d, ledger %d", got, e.ledgerCalls(TaskNarrative))
	}
}

func TestAnswerBank_Compose_ContextSectionsDoNotRecombine(t *testing.T) {
	// The posting says "Acme" and the company text says "Robotics": a claim of
	// "Acme Robotics" is stated by neither section, so it must not be supported.
	p := &composePlan{in: composeIn{Question: essayQuestion}, cinfo: companyInfo{Sections: []string{"The Acme team hires.", "Robotics is hard."}}}
	g := newGrounding(p.allowedFacts())
	if g.supported(spanName0("Acme Robotics")) {
		t.Fatal("tokens scattered across sections recombined into a claim")
	}
	if !g.supported(spanName0("Acme")) || !g.supported(spanName0("Robotics")) {
		t.Fatal("each section still supports its own words")
	}
}

// The reply cache is persistent: a new Service over the same store reuses the
// reply with no model call.
func TestAnswerBank_Fallback_OptionCacheSurvivesRestart(t *testing.T) {
	e := newEnv(t)
	e.respond(func(_, _ string) string { return replyOption("Alpha") })
	first := e.runHand(match.ReasonNoEntry, enabled, profileSources(), roleQ(1))[0]
	again := func() *env {
		e.svc = New(Deps{Model: e.router, Knowledge: e.know, Now: e.clock.now, Store: e.st})
		return e
	}
	again()
	second := e.runHand(match.ReasonNoEntry, enabled, profileSources(), roleQ(1))[0]
	if first.ChosenLabel != "Alpha" || second.ChosenLabel != "Alpha" || e.ledgerCalls(TaskOption) != 1 {
		t.Fatalf("a restart repeated the call: %d calls, %+v", e.ledgerCalls(TaskOption), second)
	}
	if got := e.svc.Sessions().Count(SessionKey("jobs.example.com", "Example Corp")); got != 0 {
		t.Fatalf("a cache hit spent %d calls of the new session", got)
	}
	// 24 hours later the reply has expired, in the store too.
	e.clock.advance(CacheTTL + time.Minute)
	again()
	e.runHand(match.ReasonNoEntry, enabled, profileSources(), roleQ(1))
	if e.ledgerCalls(TaskOption) != 2 {
		t.Fatalf("an expired reply was reused: %d calls", e.ledgerCalls(TaskOption))
	}
}

func TestAnswerBank_Fallback_CacheStoresHashesAndPrunesExpired(t *testing.T) {
	e := newEnv(t)
	e.respond(func(_, _ string) string { return replyOption("Alpha") })
	e.runHand(match.ReasonNoEntry, enabled, profileSources(), roleQ(1), roleQ(2))
	rows, err := e.st.Read().Query(`SELECT key_hash, lane, mode, reply FROM answer_reply_cache`)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		var k, lane, mode, reply string
		if err := rows.Scan(&k, &lane, &mode, &reply); err != nil {
			t.Fatal(err)
		}
		n++
		if len(k) != 64 || lane != TaskOption || strings.Contains(k+lane+mode+reply, "team number") {
			t.Fatalf("row %q %q %q %q", k, lane, mode, reply)
		}
	}
	rows.Close()
	if n != 2 {
		t.Fatalf("%d rows", n)
	}
	// A write after the expiry deletes the old rows.
	e.clock.advance(CacheTTL + time.Minute)
	e.svc = New(Deps{Model: e.router, Knowledge: e.know, Now: e.clock.now, Store: e.st})
	e.runHand(match.ReasonNoEntry, enabled, profileSources(), roleQ(3))
	var left int
	if err := e.st.Read().QueryRow(`SELECT count(*) FROM answer_reply_cache`).Scan(&left); err != nil || left != 1 {
		t.Fatalf("%d rows left: %v", left, err)
	}
}

// A sensitive question the matcher left with a work-authorization entry is
// never answered with that entry's stored Yes: its own reading turned the
// wording away, and the literal answer can invert what is asked.
func TestAnswerBank_Fallback_CuratedPickNeverTakesAWorkAuthEntryLiterally(t *testing.T) {
	q := selectQ("#q", "Are you currently legally authorized to work for Example Corp for an indefinite period of time in the United States?", "Yes", "No")
	for _, c := range []struct {
		name string
		e    match.Entry
		want match.Outcome
	}{
		{"work authorization", match.Entry{ID: "work-auth-us", Kind: match.KindFixed, Body: match.Body{Answer: "Yes"}, Meta: match.Meta{Category: match.CategoryWorkAuthorization, AnswerKey: "work_authorized"}}, match.OutcomeAbstained},
		{"another curated statement", match.Entry{ID: "legal-statement", Kind: match.KindFixed, Body: match.Body{Answer: "Yes"}, Meta: match.Meta{Category: match.CategoryCompliance}}, match.OutcomeFilledReview},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			req, resp := hand(match.ReasonAmbiguous, q)
			resp.Results[0].EntryID = c.e.ID
			src := profileSources()
			src.Bank = []match.Entry{c.e}
			r := e.svc.Run(context.Background(), RunInput{Req: req, Response: resp, Settings: enabled, Sources: src})[0]
			if r.Outcome != c.want || e.ledgerCalls("") != 0 {
				t.Fatalf("%s %s %q, want %s\n%v", r.Outcome, r.Reason, r.Answer, c.want, r.Trace)
			}
		})
	}
}

// searchByCompany is a web search that answers per counterparty named in the
// question.
type searchByCompany struct {
	replies map[string]LiveFact
	errs    map[string]error
	lookups []string
}

func (f *searchByCompany) Available(context.Context) bool { return true }

func (f *searchByCompany) Lookup(_ context.Context, q string) (LiveFact, error) {
	f.lookups = append(f.lookups, q)
	for name, err := range f.errs {
		if strings.HasPrefix(q, "Is "+name+" ") {
			return LiveFact{}, err
		}
	}
	for name, r := range f.replies {
		if strings.HasPrefix(q, "Is "+name+" ") {
			return r, nil
		}
	}
	return LiveFact{}, ErrLiveFactRefused
}

// Booz Allen (owner decision): whether the applicant, or a family member, works
// for a client, prime contractor or partner of the employer is answered from
// cited research on the employers the user recorded.
func TestAnswerBank_Counterparty_AnsweredFromResearchOnTheUsersEmployers(t *testing.T) {
	const own = "7. To the best of your knowledge, are you a current or former employee of an organization (‘Client’) that you believe is or may soon become (1) a Booz Allen Hamilton client, (2) a prime contractor that has hired Booz Allen Hamilton, or (3) a Booz Allen Hamilton partner?"
	const family = "To the best of your knowledge, is any member of your household a current employee of an organization that is a Booz Allen Hamilton client or partner?"
	jobs := match.Facts{Employment: []match.EmploymentFact{{Employer: "Northwind Traders"}, {Employer: "Contoso"}}}
	run := func(lf LiveFacts, f match.Facts, q match.Question) match.Result {
		e := newCompose(t)
		e.svc = New(Deps{Model: e.router, Knowledge: e.know, Now: e.clock.now, Store: e.st, LiveFacts: lf})
		req, resp := hand(match.ReasonContextPolicy, q)
		req.Posting.Company, req.Facts = "Booz Allen Hamilton", f
		resp.Results[0].Detail = match.DetailCounterpartyEmployer
		return e.svc.Run(context.Background(), RunInput{Req: req, Response: resp, Settings: enabled, Sources: profileSources()})[0]
	}
	knowledge := selectQ("#c", own, "Yes", "Not to my knowledge")
	partner := &searchByCompany{replies: map[string]LiveFact{"Contoso": cited("Yes, Contoso is a Booz Allen Hamilton partner [2].")}}
	if r := run(partner, jobs, knowledge); r.ChosenLabel != "Yes" || r.Outcome != match.OutcomeFilledModel || !strings.Contains(strings.Join(r.Trace, " "), "https://news.example/a") {
		t.Fatalf("a cited partner is the Yes: %+v", r)
	}
	if len(partner.lookups) != 2 {
		t.Fatalf("one search per employer: %q", partner.lookups)
	}
	none := &searchByCompany{replies: map[string]LiveFact{"Contoso": cited("No [1]")}}
	if r := run(none, jobs, knowledge); r.ChosenLabel != "Not to my knowledge" {
		t.Fatalf("none found is the knowledge No: %+v", r)
	}
	// The applicant's own employers, with no tie found, answer a plain No for
	// review (owner decision, Flatiron); a relative's plain No stays the user's.
	if r := run(none, jobs, selectQ("#c", "Are you employed by an organization that is a Booz Allen Hamilton client?", "Yes", "No")); r.ChosenLabel != "No" || r.Outcome != match.OutcomeFilledReview {
		t.Fatalf("a plain No for the applicant's own employers: %+v", r)
	}
	for name, c := range map[string]struct {
		lf LiveFacts
		f  match.Facts
		q  match.Question
	}{
		"a relative's plain No": {none, match.Facts{Employment: jobs.Employment, RelationshipsComplete: map[match.RelationshipFamily]string{match.FamilyRelative: "2026-10-01"}},
			selectQ("#c", "Is any member of your household a current employee of an organization that is a Booz Allen Hamilton client?", "Yes", "No")},
		"an unclear reply":        {&searchByCompany{replies: map[string]LiveFact{"Contoso": cited("Contoso builds software [1].")}}, jobs, knowledge},
		"a failed search":         {&searchByCompany{errs: map[string]error{"Contoso": errBoom}}, jobs, knowledge},
		"no employer":             {none, match.Facts{}, knowledge},
		"an unconfirmed relative": {none, jobs, selectQ("#c", family, "Yes", "Not to my knowledge")},
	} {
		if r := run(c.lf, c.f, c.q); r.Outcome != match.OutcomeAbstained || r.Detail != match.DetailCounterpartyEmployer {
			t.Errorf("%s: %+v", name, r)
		}
	}
	// A relatives list the user confirmed complete, with no relative at any
	// company, is the No with no search.
	empty := &searchByCompany{}
	confirmed := match.Facts{RelationshipsComplete: map[match.RelationshipFamily]string{match.FamilyRelative: "2026-10-01"}}
	if r := run(empty, confirmed, selectQ("#c", family, "Yes", "Not to my knowledge")); r.ChosenLabel != "Not to my knowledge" || len(empty.lookups) != 0 {
		t.Fatalf("no relative: %+v %q", r, empty.lookups)
	}
}

// One verdict per pair of companies is kept for the reply cache's day.
func TestAnswerBank_Counterparty_AVerdictIsSearchedOncePerDay(t *testing.T) {
	q := selectQ("#c", "To the best of your knowledge, are you an employee of an organization that is a Booz Allen Hamilton client?", "Yes", "Not to my knowledge")
	lf := &searchByCompany{}
	e := newCompose(t)
	e.svc = New(Deps{Model: e.router, Knowledge: e.know, Now: e.clock.now, Store: e.st, LiveFacts: lf})
	for range 2 {
		req, resp := hand(match.ReasonContextPolicy, q)
		req.Posting.Company, req.Facts = "Booz Allen Hamilton", match.Facts{Employment: []match.EmploymentFact{{Employer: "Contoso"}}}
		resp.Results[0].Detail = match.DetailCounterpartyEmployer
		if r := e.svc.Run(context.Background(), RunInput{Req: req, Response: resp, Settings: enabled, Sources: profileSources()})[0]; r.ChosenLabel != "Not to my knowledge" {
			t.Fatalf("%+v", r)
		}
	}
	if len(lf.lookups) != 1 {
		t.Fatalf("lookups %q", lf.lookups)
	}
}
