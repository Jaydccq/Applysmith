// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"reflect"
	"strings"
	"testing"
)

func TestGates_Audit_EveryGuardedDecisionWritesOneRow(t *testing.T) {
	d := AgentSubmit(AgentSubmitInput{Caller: CallerAskAgent, Tool: ToolAutofillApplication, PostingURL: "https://boards.example.test/example-corp/jobs/1234567?token=abc#frag", UserMessage: "apply"})
	got(t, d, GateAgentSubmit, Hold, CodeHostNotNamedNotListed)
	r := NewAuditRow(t0, d, AuditContext{Caller: CallerAskAgent, Tool: ToolAutofillApplication, TargetURL: "https://boards.example.test/example-corp/jobs/1234567?token=abc#frag", UserMessage: "apply", Salt: "salt"})
	if r.Verdict != Hold || r.TargetHost != "boards.example.test" || r.TargetPath != "/example-corp/jobs/1234567" || r.TargetScheme != "https" {
		t.Fatalf("row = %+v", r)
	}
	if strings.Contains(r.TargetPath, "token") || strings.Contains(r.TargetPath, "frag") {
		t.Fatal("query or fragment leaked")
	}
	// A confirmation step's allow carries the trace verdict.
	allow := decide(GateAgentSubmit, Allow, CodeExactURLNamed, "x")
	if NewAuditRow(t0, allow, AuditContext{Trace: true}).Verdict != AllowWithTrace || NewAuditRow(t0, d, AuditContext{Trace: true}).Verdict != Hold {
		t.Fatal("trace applies to allow only")
	}
	// The row type holds no field that could carry message text.
	for _, f := range reflect.VisibleFields(reflect.TypeOf(AuditRow{})) {
		if strings.Contains(strings.ToLower(f.Name), "message") && f.Name != "UserMessageHash" {
			t.Fatalf("field %s could hold message text", f.Name)
		}
	}
}

func TestGates_Audit_NoSwitchDisablesAndReasonCapped(t *testing.T) {
	long := decide(GateReadiness, Deny, CodeValueNotHeld, "%s", strings.Repeat("r", 1000))
	if n := len([]rune(long.Reason)); n != MaxReasonRunes {
		t.Fatalf("reason has %d runes", n)
	}
	// NewAuditRow takes no switch: the same decision always yields a row.
	if r := NewAuditRow(t0, long, AuditContext{}); r.Gate != GateReadiness || r.At != t0 {
		t.Fatalf("row = %+v", r)
	}
}

func TestGates_Audit_OneRowPerDistinctToolArgsVerdictPerTurn(t *testing.T) {
	var turn TurnAudit
	hold := AuditRow{Gate: GateAgentSubmit, Tool: "autofill_application", ArgHash: "a1", Verdict: Hold}
	for i, c := range []struct {
		row  AuditRow
		want bool
	}{
		{hold, true},
		{hold, false},
		{AuditRow{Gate: GateAgentSubmit, Tool: "autofill_application", ArgHash: "a2", Verdict: Hold}, true},
		{AuditRow{Gate: GateAgentSubmit, Tool: "autofill_application", ArgHash: "a1", Verdict: Allow}, true},
		{AuditRow{Gate: GateSubmitAuthorization, Tool: "autofill_application", ArgHash: "a1", Verdict: Hold}, true},
		{AuditRow{Gate: GateAgentSubmit, Tool: "apply_from_list", ArgHash: "a1", Verdict: Hold}, true},
	} {
		if got := turn.Admit(c.row); got != c.want {
			t.Errorf("case %d: admit = %v", i, got)
		}
	}
	var fresh TurnAudit
	if !fresh.Admit(hold) {
		t.Fatal("a new turn starts empty")
	}
}

func TestGates_Audit_MessageHashSaltedAndTextNeverStored(t *testing.T) {
	h := MessageHash("salt-one", "apply to everything")
	if len(h) != 16 || strings.Trim(h, "0123456789abcdef") != "" {
		t.Fatalf("hash = %q", h)
	}
	if h != MessageHash("salt-one", "apply to everything") || h == MessageHash("salt-two", "apply to everything") || h == MessageHash("salt-one", "apply to nothing") {
		t.Fatal("the hash must be stable and depend on salt and text")
	}
	r := NewAuditRow(t0, decide(GateAgentSubmit, Hold, CodeURLNotHTTP, "x"), AuditContext{Salt: "salt-one", UserMessage: "apply to everything"})
	if r.UserMessageHash != h {
		t.Fatalf("row hash = %q", r.UserMessageHash)
	}
	if NewAuditRow(t0, decide(GateAgentSubmit, Hold, CodeURLNotHTTP, "x"), AuditContext{}).UserMessageHash != "" {
		t.Fatal("no message, no hash")
	}
}

func TestGates_Audit_TargetURLRedaction(t *testing.T) {
	seg24 := strings.Repeat("a1", 12)
	for _, c := range []struct {
		name, raw, host, path, scheme string
	}{
		{"query and fragment dropped", "https://boards.example.test/example-corp/jobs/1234567?token=abc#frag", "boards.example.test", "/example-corp/jobs/1234567", "https"},
		{"opaque segment replaced", "https://apply.example.test/a1b2c3d4e5f6g7h8i9j0k1l2m3/form", "apply.example.test", "/~/form", "https"},
		{"23 characters kept", "https://apply.example.test/" + seg24[:23], "apply.example.test", "/" + seg24[:23], "https"},
		{"24 characters replaced", "https://apply.example.test/" + seg24, "apply.example.test", "/~", "https"},
		{"25 characters replaced", "https://apply.example.test/" + seg24 + "1", "apply.example.test", "/~", "https"},
		{"24 letters without a digit kept", "https://apply.example.test/" + strings.Repeat("a", 24), "apply.example.test", "/" + strings.Repeat("a", 24), "https"},
		{"javascript scheme", "javascript:alert(1)", "", "", "javascript"},
		{"unparsable", "http://[::1", "", "", "invalid"},
		{"no scheme", "jobs.example.test/x", "", "", "invalid"},
		{"mixed-case host", "HTTPS://Jobs.Example.Test/Apply", "jobs.example.test", "/Apply", "https"},
	} {
		t.Run(c.name, func(t *testing.T) {
			host, path, scheme := RedactTarget(c.raw)
			if host != c.host || path != c.path || scheme != c.scheme {
				t.Fatalf("got %q %q %q", host, path, scheme)
			}
		})
	}
}

// The store prunes the rows written before AuditPruneBefore: a row exactly
// 180 days old is not before it and stays (sessions.Store.PruneDecisions,
// whose own boundary test deletes real rows).
func TestGates_Audit_RetentionBoundary(t *testing.T) {
	for _, c := range []struct {
		name string
		age  int64
		want bool
	}{{"below", 180*Day - 1, false}, {"at", 180 * Day, false}, {"above", 180*Day + 1, true}, {"fresh", 0, false}} {
		t.Run(c.name, func(t *testing.T) {
			if got := t0-c.age < AuditPruneBefore(t0); got != c.want {
				t.Fatalf("pruned = %v", got)
			}
		})
	}
}

func TestGates_Audit_ValidCaller(t *testing.T) {
	for _, c := range []Caller{CallerAskAgent, CallerApplyList, CallerCLI, CallerDashboard, CallerScheduler, CallerTelegram} {
		if !ValidCaller(c) {
			t.Errorf("%s should be valid", c)
		}
	}
	if ValidCaller("remote") || ValidCaller("") {
		t.Fatal("unknown callers are invalid")
	}
}

func TestGates_VocabularyIsClosed(t *testing.T) {
	for _, g := range allGates() {
		seen := map[string]bool{}
		for _, c := range vocabulary[g] {
			if seen[c] || c == "" || strings.ToLower(c) != c || strings.ContainsAny(c, " -") {
				t.Errorf("gate %s: bad or repeated code %q", g, c)
			}
			seen[c] = true
		}
	}
	if (Decision{Gate: GateAgentSubmit, Verdict: Hold, Code: "made_up", Reason: "x"}).Valid() {
		t.Fatal("a code outside the vocabulary must be invalid")
	}
}
