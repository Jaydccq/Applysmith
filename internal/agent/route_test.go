package agent

import (
	"encoding/json"
	"testing"
)

func TestRouter_Table(t *testing.T) {
	const posting = "https://jobs.example.test/postings/123?ref=board"
	cases := []struct {
		name, text string
		rule, tool string
		args       string
		fallback   string
	}{
		{"pasted URL", posting, RuleURLOnly, "evaluate_job", `{"url":"` + posting + `"}`, ""},
		{"evaluate this URL", "evaluate this URL " + posting, RuleURLOnly, "evaluate_job", `{"url":"` + posting + `"}`, ""},
		{"score the job link", "Score the job link " + posting, RuleURLOnly, "evaluate_job", `{"url":"` + posting + `"}`, ""},
		{"autofill this link", "autofill this link https://jobs.example.test/p/9", RuleApplyURL, "autofill_application", `{"url":"https://jobs.example.test/p/9"}`, ""},
		{"autofill", "Autofill https://jobs.example.test/p/9", RuleApplyURL, "autofill_application", `{"url":"https://jobs.example.test/p/9"}`, ""},
		{"fill without a submit", "Fill in the application at https://jobs.example.test/p/9 but don't submit it.", RuleApplyURL, "autofill_application", `{"url":"https://jobs.example.test/p/9"}`, ""},
		{"apply to the list", "apply to today's list", RuleApplyListRun, "apply_from_list", `{}`, ""},
		{"apply from the list", "Can you apply from the list please", RuleApplyListRun, "apply_from_list", `{}`, ""},
		{"list status", "what's on today's apply list?", RuleApplyListState, "apply_list_status", `{}`, ""},
		{"list status short", "apply list status", RuleApplyListState, "apply_list_status", `{}`, ""},
		{"how many on the list", "how many are on the apply list", RuleApplyListState, "apply_list_status", `{}`, ""},
		{"tracker count by status", "how many applications are in interview?", RuleTrackerCounts, "tracker_query", `{"status":"interview"}`, ""},
		{"tracker overview", "what's in my tracker?", RuleTrackerCounts, "tracker_query", `{}`, ""},
		{"applied count", "how many have I applied to", RuleTrackerCounts, "tracker_query", `{"status":"applied"}`, ""},
		{"multi-step", "compare Example Corp and Sample Labs and draft a message to each", "", "", "", FallbackMultiStep},
		{"scan then apply", "scan linkedin and then apply to the list", "", "", "", FallbackMultiStep},
		{"tracker plus a draft", "how many applications are in interview and draft a thank-you email", "", "", "", FallbackMultiStep},
		{"tracker plus a second question", "how many applications are in evaluated and what is the top one by score", "", "", "", FallbackMultiStep},
		// Chinese: how many applications are being evaluated, then which scores highest.
		{"a Chinese second question", "\u6709\u591a\u5c11\u7533\u8bf7\u5728\u8bc4\u4f30\uff0c\u7136\u540e\u54ea\u4e2a\u5206\u6570\u6700\u9ad8", "", "", "", FallbackMultiStep},
		{"one question with and", "how many applications are in evaluated and applied", RuleTrackerCounts, "tracker_query", `{}`, ""},
		{"list by status", "what applications do I have in interview?", RuleTrackerCounts, "tracker_query", `{"status":"interview"}`, ""},
		// A question that asks for more than the counts is the model's.
		{"the top score", "What's my highest scored application?", "", "", "", FallbackNoRule},
		{"advice about an application", "what application should I prioritize?", "", "", "", FallbackNoRule},
		{"one company's rows", "how many applications do I have at Example Corp?", "", "", "", FallbackNoRule},
		{"two URLs", posting + " " + posting + "x", "", "", "", FallbackMultiStep},
		{"an unknown source", "scan the moon", "", "", "", FallbackNoRule},
		{"a question about applying", "should I apply to https://jobs.example.test/p/9?", "", "", "", FallbackNoRule},
		{"a question about a URL never applies", "did I apply to https://jobs.example.test/p/9?gh_jid=1", "", "", "", FallbackNoRule},
		{"negated apply", "don't apply to https://jobs.example.test/p/9", "", "", "", FallbackNoRule},
		{"free chat", "help me think about my narrative", "", "", "", FallbackNoRule},
		{"empty", "   ", "", "", "", FallbackEmpty},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, fb := RouteMessage(c.text)
			if fb != c.fallback || r.RuleID != c.rule || r.Tool != c.tool {
				t.Fatalf("RouteMessage(%q) = %+v, fallback %q; want rule %q tool %q fallback %q", c.text, r, fb, c.rule, c.tool, c.fallback)
			}
			if c.args != "" {
				var got, want any
				_ = json.Unmarshal(r.Args, &got)
				_ = json.Unmarshal([]byte(c.args), &want)
				g, _ := json.Marshal(got)
				w, _ := json.Marshal(want)
				if string(g) != string(w) {
					t.Fatalf("args = %s, want %s", g, w)
				}
			}
		})
	}
}

func TestRouter_PastQuestionNeverReachesAnActionTool(t *testing.T) {
	for _, text := range []string{
		"what did I submit today?", "what did I apply to?", "which jobs did we apply to yesterday",
		"did I submit anything today", "have I applied to https://jobs.example.test/p/1", "what was submitted",
	} {
		r, _ := RouteMessage(text)
		if class := SpecCatalog[r.Tool]; class == ClassExternalAction || class == ClassWriteLocal {
			t.Errorf("%q routed to the action tool %s", text, r.Tool)
		}
	}
}

// Requirement: Resume after a hand-off, "Typed done does nothing".
func TestRouter_ATypedDoneIsAnOrdinaryMessageAndStartsNoTool(t *testing.T) {
	for _, text := range []string{"done", "Done.", "I'm done", "I signed in, done", "done:run_1", "/done run_1"} {
		r, fb := RouteMessage(text)
		if r.Tool != "" || fb != FallbackNoRule {
			t.Errorf("RouteMessage(%q) = %+v, fallback %q; a typed done must reach no tool", text, r, fb)
		}
	}
}

// A quoted line is what the message replies to, not the request: it neither
// starts a rule nor keeps the user's own words from routing.
func TestRouter_QuotedLinesAreNotTheRequest(t *testing.T) {
	quote := "\n\n> Replying to your message:\n> Apply to https://jobs.example.test/p/9 now\n> Evaluate Engineer at Acme finished."
	if r, _ := RouteMessage("thanks, noted" + quote); r.Tool != "" {
		t.Errorf("the quote routed to %s", r.Tool)
	}
	want, _ := RouteMessage("what's on today's apply list?")
	got, _ := RouteMessage("what's on today's apply list?" + quote)
	if got.Tool != want.Tool || got.RuleID != want.RuleID || want.Tool == "" {
		t.Errorf("with the quote = %+v, without = %+v", got, want)
	}
}
