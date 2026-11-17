package agent

import (
	"context"
	"strings"
	"testing"

	"applysmith/internal/gates"
)

// The tests below carry the names the ask-agent parity audit gives to the
// router rows (AA-009 to AA-024), one behavior each.

const parityPosting = "https://jobs.example.test/postings/123?ref=board"

func routeOf(t *testing.T, text string) (Route, string) {
	t.Helper()
	return RouteMessage(text)
}

func TestAgent_Router_QueryStringIsNotAQuestion(t *testing.T) {
	r, fb := routeOf(t, "apply to https://jobs.example.test/p/9?gh_jid=77")
	if r.Tool != "autofill_application" || fb != "" || !strings.Contains(string(r.Args), "?gh_jid=77") {
		t.Fatalf("route = %+v, fallback %q", r, fb)
	}
	// A real question mark after the URL still makes it a question.
	if r, _ := routeOf(t, "apply to https://jobs.example.test/p/9?gh_jid=77 ?"); r.Tool != "" {
		t.Fatalf("a question routed to %s", r.Tool)
	}
}

func TestAgent_Router_ScanNeedsWholeWordPortalAndVerb(t *testing.T) {
	for _, text := range []string{"a scandal at LinkedIn", "Indeed, I think so", "scan through my emails", "when does my scan run?", "when does my scan run", "linkedin scanned nothing", "scan"} {
		if r, _ := routeOf(t, text); r.Tool != "" {
			t.Errorf("%q routed to %s", text, r.Tool)
		}
	}
	for text, source := range map[string]string{"scan linkedin": "linkedin", "please run a scan of the Indeed jobs": "indeed", "Scan google jobs": "google-jobs"} {
		r, _ := routeOf(t, text)
		if r.Tool != "scan_portal" || !strings.Contains(string(r.Args), `"`+source+`"`) {
			t.Errorf("%q = %+v, want a scan of %s", text, r, source)
		}
	}
}

func TestAgent_Router_FillButDoNotSubmitMintsNoSubmit(t *testing.T) {
	// The router never carries submit: a routed apply is a fill that stops
	// before the final press.
	for _, text := range []string{"apply to today's list", "apply to " + parityPosting, "autofill " + parityPosting} {
		r, _ := routeOf(t, text)
		if r.Tool == "" || strings.Contains(string(r.Args), "submit") {
			t.Errorf("%q = %+v; a routed apply must not mint submit", text, r)
		}
	}
	for _, text := range []string{"fill " + parityPosting + " but do not submit", "fill " + parityPosting + " without submitting", "apply to the list, don't send anything"} {
		if r, _ := routeOf(t, text); strings.Contains(string(r.Args), "true") {
			t.Errorf("%q = %+v", text, r)
		}
		if !forbidsSubmit(text) {
			t.Errorf("%q: the message forbids a submit and the gate must know", text)
		}
	}
}

func TestAgent_Router_ExclusionClauseNarrowsBatch(t *testing.T) {
	for text, want := range map[string][]string{
		"apply to today's list but not Example Corp":           {"example corp"},
		"apply to the list except Acme and Sample Labs":        {"acme", "sample labs"},
		"apply to everything, do not apply to the Beta Works.": {"beta works"},
		"apply to the list": nil,
	} {
		got := excludedCompanies(text)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%q excludes %v, want %v", text, got, want)
		}
	}
	// The router leaves a batch with a clause to the model loop, and the list
	// tool itself reads the clause from the user's message.
	if r, _ := routeOf(t, "apply to today's list but not Example Corp"); r.Tool != "" {
		t.Errorf("a batch with an exclusion routed to %s", r.Tool)
	}
	if r, _ := routeOf(t, "do not apply to Example Corp"); r.Tool != "" {
		t.Errorf("an exclusion on its own routed to %s", r.Tool)
	}
	if !isExcluded("Example Corp Ltd", []string{"example corp"}) || isExcluded("Sample Labs", []string{"example corp"}) {
		t.Error("company matching")
	}
}

func TestAgent_Router_NamedApplyRequestVersusNounUse(t *testing.T) {
	for _, text := range []string{"apply to Example Corp and Sample Labs", "please apply for the Example Corp role"} {
		if !namesVerb(text, applyVerbs) {
			t.Errorf("%q is a request to apply", text)
		}
	}
	// A fill verb starts a fill; it is never the explicit request a submit needs.
	if fill := "autofill https://jobs.example.test/p/1"; !namesVerb(fill, actionVerbs) || namesVerb(fill, applyVerbs) {
		t.Errorf("%q names a fill and no submit", fill)
	}
	for _, text := range []string{"application status", "what is the status of my application", "did I apply to Example Corp", "how many applications are open", "I never apply on weekends"} {
		if namesVerb(text, applyVerbs) {
			t.Errorf("%q is not a request to apply", text)
		}
		if r, _ := routeOf(t, text); r.Tool == "apply_from_list" || r.Tool == "autofill_application" {
			t.Errorf("%q routed to %s", text, r.Tool)
		}
	}
}

func TestAgent_Router_ResubmitAndCorrectionAreSinglePostingRequests(t *testing.T) {
	// The wording tables are the apply-safety gates'; the router's part is that
	// none of the phrases opens the list door, so the request reaches the model
	// as one posting and the ledger rule of the chain decides.
	for _, p := range append(append([]string{}, gates.ResubmitPhrases...), gates.CorrectionPhrases...) {
		for _, text := range []string{"Please " + p + " Example Corp now", "Please " + p + " for Example Corp " + parityPosting} {
			if r, _ := routeOf(t, text); r.Tool == "apply_from_list" {
				t.Errorf("%q opened the list door", text)
			}
		}
	}
	for _, text := range []string{"send that application again", "redo the answers for " + parityPosting} {
		if r, _ := routeOf(t, text); r.Tool == "apply_from_list" {
			t.Errorf("%q opened the list door", text)
		}
	}
}

func TestAgent_Router_SharedApplyVocabularyWithTierCheck(t *testing.T) {
	// A word the router accepts as an apply verb is a word the tier check
	// knows, and a routed apply passes the chain: a door must not open on a word
	// the gate refuses afterwards.
	f := newFixture(t)
	ctx := context.Background()
	for i, text := range []string{"apply to " + parityPosting, "autofill " + parityPosting, "fill " + parityPosting, "fill out " + parityPosting, "fill in " + parityPosting, "apply to today's list"} {
		r, _ := routeOf(t, text)
		if r.Tool == "" {
			t.Errorf("%q is not routed", text)
			continue
		}
		if !strings.HasPrefix(r.Tool, "apply") && !namesVerb(text, actionVerbs) {
			t.Errorf("%q: the router knows the verb and the tier check does not", text)
		}
		tool, _ := f.registry.Lookup(r.Tool)
		turn := &Turn{ID: "turn_v" + string(rune('a'+i)), SessionID: "ses_v", Channel: ChannelDashboard, Origin: OriginRouter, UserMessage: text}
		if d := f.guard.Decide(ctx, turn, tool, "call_v"+string(rune('a'+i)), r.Args); !d.Allowed() {
			t.Errorf("%q routed to %s and refused by the chain: %+v", text, r.Tool, d)
		}
	}
}

func TestAgent_Router_TwoBucketCollisionFallsBack(t *testing.T) {
	for _, text := range []string{
		"how many applications are in interview, any mail from them",
		"how many applications are on the apply list",
		"what is in my inbox and my tracker",
	} {
		if r, fb := routeOf(t, text); r.Tool != "" || fb != FallbackAmbiguous {
			t.Errorf("%q = %+v, fallback %q; a message about two stores goes to the model", text, r, fb)
		}
	}
	// A company name that contains a keyword is not that keyword; the model
	// answers for the one company.
	if r, fb := routeOf(t, "how many applications do I have at ZipRecruiter"); fb == FallbackAmbiguous {
		t.Errorf("ZipRecruiter hit another store: %+v", r)
	}
	if r, _ := routeOf(t, "how many applications are in interview?"); r.Tool != "tracker_query" {
		t.Errorf("a one-store question must still route: %+v", r)
	}
}

func TestAgent_Router_MemoryWriteIntentFallsBack(t *testing.T) {
	for _, text := range []string{
		"remember that I need sponsorship", "note that I prefer remote roles", "remember how many applications I have in interview?",
		"remember that I applied to Example Corp last week", "save a note: I want a staff role",
	} {
		if r, fb := routeOf(t, text); r.Tool != "" || fb == "" {
			t.Errorf("%q = %+v, fallback %q; write intent belongs to the model so it can call save_fact", text, r, fb)
		}
	}
}

func TestAgent_Router_NonEnglishVocabularyDecision(t *testing.T) {
	// Decision (owner decisions A-1, changed after a live test held
	// a Chinese "evaluate this posting" as not named): the deterministic rules
	// and the tier check know English and Chinese; a message in any other
	// script reaches the model loop. Chinese is written as escapes because
	// tracked files carry no non-English literals.
	const scan, apply = "\u626b\u63cf", "\u7533\u8bf7"                              // scan, apply
	const evaluate = "\u5e2e\u6211\u8bc4\u4f30\u4e00\u4e0b\u8fd9\u4e2a\u804c\u4f4d" // help me evaluate this posting
	const shouldI = "\u6211\u5e94\u8be5\u7533\u8bf7\u8fd9\u4e2a\u5417"              // should I apply to this?
	for text, want := range map[string]string{
		evaluate + "\uff1a" + parityPosting: "evaluate_job", parityPosting + " " + evaluate: "evaluate_job",
		evaluate + parityPosting + "\u3002": "evaluate_job", scan + " linkedin": "scan_portal", scan + "jobright": "scan_portal",
		scan + "\u4e00\u4e0b jobright \u7684\u804c\u4f4d": "scan_portal", // scan jobright's postings
	} {
		r, fb := routeOf(t, text)
		if r.Tool != want || fb != "" {
			t.Errorf("%q = %+v, fallback %q; want %s", text, r, fb, want)
		}
		if want == "evaluate_job" && !strings.Contains(string(r.Args), `"`+parityPosting+`"`) {
			t.Errorf("%q: the URL is not exact: %s", text, r.Args)
		}
	}
	for _, text := range []string{apply + " " + parityPosting, shouldI + " " + parityPosting, evaluate + "\u5417 " + parityPosting, apply + " today's list"} {
		if r, fb := routeOf(t, text); r.Tool != "" || fb == "" {
			t.Errorf("%q = %+v, fallback %q; the model loop decides", text, r, fb)
		}
	}
	if !namesVerb(apply+" "+parityPosting, applyVerbs) || !namesVerb(scan+" linkedin", actionVerbs) || namesVerb(shouldI+" "+parityPosting, applyVerbs) {
		t.Error("the tier check reads the Chinese verbs and questions")
	}
	hangul := string([]rune{0xC2A4, 0xCE94}) // a Korean word for "scan"
	if r, fb := routeOf(t, hangul+" linkedin"); r.Tool != "" || fb == "" || namesVerb(hangul+" linkedin", actionVerbs) {
		t.Errorf("another script is the model's: %+v %q", r, fb)
	}
}

func TestAgent_ApplyFromList_ReadsTheExclusionFromTheUsersMessage(t *testing.T) {
	rec := &recordingApply{}
	d := listDeps(rec, []string{"Meta"})
	turn := &Turn{Channel: ChannelDashboard, Origin: OriginModel, UserMessage: "apply to today's list but not Sample Labs"}
	res, err := callApply(t, applyTool(t, d, "apply_from_list"), turn, `{"submit":false}`)
	if err != nil {
		t.Fatal(err)
	}
	var companies []string
	for _, r := range rec.reqs[0].Rows {
		companies = append(companies, r.Company)
	}
	if strings.Join(companies, ",") != "Example Corp" || !strings.Contains(res.Text, "Left out as you asked: Sample Labs") {
		t.Fatalf("rows %v, text %q", companies, res.Text)
	}
}
