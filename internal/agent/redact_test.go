// Clean-room: docs/provenance.md#CR-AGENT-TAINT

package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"applysmith/internal/agent/sessions"
	"applysmith/internal/gates"
)

// secretLike builds credential-shaped values at run time.
func secretLike() (key, bearer, assigned, block string) {
	key = "sk-" + strings.Repeat("q", 6) + "mail" + strings.Repeat("Z", 22)
	bearer = "Bearer " + strings.Repeat("a1", 12)
	assigned = "API_" + "TOKEN=" + strings.Repeat("x9", 8)
	block = "-----BEGIN " + "RSA PRIVATE KEY-----\n" + strings.Repeat("MIIE", 10) + "\n-----END " + "RSA PRIVATE KEY-----"
	return
}

func tool(name string, class Class, text string, err error, mod func(*Tool)) Tool {
	t := Tool{Name: name, Label: name, Description: name + ".", Class: class, PII: PIINone, Timeout: time.Second,
		Schema: `{"type":"object","additionalProperties":false,"properties":{}}`,
		Run:    func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{Text: text}, err }}
	if mod != nil {
		mod(&t)
	}
	return t
}

func TestAgent_Redaction_SpansAndVaultValuesReplaced(t *testing.T) {
	key, bearer, assigned, block := secretLike()
	mail := "Hi Alex, your key is " + key + " and the header was " + bearer + ", config " + assigned + "\n" + block + "\nThanks"
	b := newBridgeEnv(t, ChannelDashboard, "hello",
		tool("gmail_signals", ClassRead, mail, nil, func(t *Tool) { t.OutsideContent = true }))
	res := b.call(t, "c1", "gmail_signals", `{}`)
	for _, secret := range []string{key, strings.TrimPrefix(bearer, "Bearer "), strings.TrimPrefix(assigned, "API_TOKEN="), "MIIEMIIE"} {
		if strings.Contains(res.Text, secret) {
			t.Fatalf("the model saw a secret: %q", res.Text)
		}
	}
	if got := strings.Count(res.Text, "[REDACTED:credential]"); got != 4 {
		t.Fatalf("redaction markers = %d, want 4: %q", got, res.Text)
	}
	for _, keep := range []string{"Hi Alex, your key is", "Thanks"} {
		if !strings.Contains(res.Text, keep) {
			t.Errorf("only the spans are replaced: %q lacks %q", res.Text, keep)
		}
	}
	kept, _ := b.run.takeResult("c1")
	if strings.Contains(kept.text, key) || !strings.Contains(kept.text, "[REDACTED:credential]") {
		t.Fatalf("the stored copy is not redacted: %q", kept.text)
	}

	rows := mustDecisions(t, b.f, "turn_b")
	var row gates.AuditRow
	for _, r := range rows {
		if r.Gate == GateResultRedaction {
			row = r
		}
	}
	if row.Code != CodeRedacted || !strings.Contains(row.Reason, "credential=4") || !strings.Contains(row.Reason, "4 spans") {
		t.Fatalf("audit = %+v", rows)
	}
	for _, r := range rows {
		if strings.Contains(r.Reason, key) || strings.Contains(r.Reason, "PRIVATE") {
			t.Fatalf("the audit stores matched text: %+v", r)
		}
	}
}

func TestAgent_Redaction_OrdinaryTextIsUntouched(t *testing.T) {
	text := "Example Corp, Backend Engineer. Report at /Users/alex/workspace/reports/012-example.md. Password: required. Key skills: Go. Bearer of good news."
	b := newBridgeEnv(t, ChannelDashboard, "hello", tool("find_report", ClassRead, text, nil, nil))
	res := b.call(t, "c1", "find_report", `{}`)
	if !strings.Contains(res.Text, "\n"+text+"\n") {
		t.Fatalf("result changed: %q", res.Text)
	}
	for _, r := range mustDecisions(t, b.f, "turn_b") {
		if r.Gate == GateResultRedaction {
			t.Fatalf("nothing was redacted but the audit says so: %+v", r)
		}
	}
}

func TestAgent_Redaction_APostingIDIsNotACredential(t *testing.T) {
	key, _, _, _ := secretLike()
	gh := "ghp" + "_" + strings.Repeat("Ab3", 8)
	posting := "https://job-boards.greenhouse.io/embed/job_app?for=authenticinsurance&" + "token=4114318009"
	for _, text := range []string{posting, posting + "&gh_src=board", "gh_jid=7798264003", "Apply at " + posting + "."} {
		if out, n := redactCredentialShapes(text); out != text || n != 0 {
			t.Errorf("%q became %q (%d spans)", text, out, n)
		}
	}
	for text, secret := range map[string]string{
		"api_" + "key=" + key: key, "token=" + gh: gh, "pass" + "word=12345678": "12345678",
		posting + "&access_" + "token=" + gh: gh, "client_" + "secret=7798264003": "7798264003", "refresh_" + "token=abcd-12345": "abcd-12345",
	} {
		if out, n := redactCredentialShapes(text); strings.Contains(out, secret) || n != 1 {
			t.Errorf("%q became %q (%d spans); the secret must go", text, out, n)
		}
	}
	if out, _ := redactCredentialShapes(posting + "&access_" + "token=" + gh); !strings.HasPrefix(out, posting+"&") {
		t.Errorf("the posting id before a secret was lost: %q", out)
	}
}

// An answer-bank entry id or a database key is not a credential; a secret
// under a credential name still is.
func TestAgent_Redaction_ASlugOrAPlainKeyIsNotACredential(t *testing.T) {
	for _, text := range []string{"entry key: years-full-time-work-experience-number", "Entry " + "key: work_auth_status-v2x!",
		"primary_" + "key=candidacy-row-identifier", "keyword: golang-backend-remote", "The id is api_" + "key=sample-entry-name."} {
		if out, n := redactCredentialShapes(text); out != text || n != 0 {
			t.Errorf("%q became %q (%d spans)", text, out, n)
		}
	}
	key, _, _, _ := secretLike()
	jwt := "eyJ" + strings.Repeat("a", 10) + ".eyJ" + strings.Repeat("b", 10) + "." + strings.Repeat("c", 10)
	for _, text := range []string{key, "AKIA" + strings.Repeat("Q", 16), jwt, "pass" + "word: Hunter2!x", "pass" + "word: correct-horse-battery",
		"api_" + "key=" + strings.Repeat("Zq8-", 3), "entry " + "token: " + key, "session_" + "token=" + strings.Repeat("ab12", 3)} {
		if out, n := redactCredentialShapes(text); n != 1 || strings.Contains(out, "Hunter2") || strings.Contains(out, "horse") {
			t.Errorf("%q became %q (%d spans); the secret must go", text, out, n)
		}
	}
}

func TestAgent_Redaction_RuleFailureWithholdsTheResult(t *testing.T) {
	b := newBridgeEnv(t, ChannelDashboard, "hello", tool("find_report", ClassRead, "Report text", nil, nil))
	broken := NewRedactor()
	broken.rules = append(broken.rules, redactRule{category: "broken", apply: func(string) (string, int) { panic("rule failed") }})
	b.f.bridge.opt.Redactor = broken
	res := b.call(t, "c1", "find_report", `{}`)
	if !strings.Contains(res.Text, RedactionFailedNotice) || strings.Contains(res.Text, "Report text") {
		t.Fatalf("result = %q", res.Text)
	}
	kept, _ := b.run.takeResult("c1")
	if kept.text != RedactionFailedNotice {
		t.Fatalf("stored copy = %q", kept.text)
	}
}

func TestAgent_Redaction_StreamAndRecordNeverHoldASecret(t *testing.T) {
	key, _, _, _ := secretLike()
	f := newFixture(t, tool("gmail_signals", ClassRead, "Mail: "+key, nil, func(t *Tool) { t.OutsideContent = true }))
	res := playTurn(t, f, "", "check my mail",
		joinLines(callAndStart("c1", "gmail_signals", map[string]any{}), []string{lineAnswer("Done."), lineSettled})...)
	for _, e := range f.sink.events(res.SessionID) {
		if strings.Contains(e.Summary, key) || strings.Contains(e.Text, key) {
			t.Fatalf("the stream shows a secret: %+v", e)
		}
	}
	msgs, _ := f.sessions.Messages(context.Background(), res.SessionID)
	found := false
	for _, m := range msgs {
		if strings.Contains(m.Body, key) {
			t.Fatalf("the record holds a secret: %+v", m)
		}
		found = found || (m.Role == sessions.RoleToolResult && strings.Contains(m.Body, "[REDACTED:credential]"))
	}
	if !found {
		t.Fatal("the stored result lost its marker")
	}
}

func TestAgent_Tool_ErrorSummaryRedactedAndClassified(t *testing.T) {
	key, _, _, _ := secretLike()
	slow := tool("find_report", ClassRead, "", nil, func(t *Tool) {
		t.Timeout = 50 * time.Millisecond
		t.Run = func(ctx context.Context, _ ToolCall) (ToolResult, error) {
			<-ctx.Done()
			return ToolResult{}, ctx.Err()
		}
	})
	b := newBridgeEnv(t, ChannelDashboard, "please evaluate https://jobs.example.test/p/1",
		tool("evaluate_job", ClassExternalAction, "", errors.New("upstream refused "+key+strings.Repeat(" long", 200)), func(t *Tool) { t.Idempotent = true }),
		slow)
	res := b.call(t, "c1", "evaluate_job", `{}`)
	if !res.IsError || strings.Contains(res.Text, key) || !strings.Contains(res.Text, "[REDACTED:credential]") || len([]rune(res.Text)) > 500 {
		t.Fatalf("error result = %+v", res)
	}
	if got := claimOutcome(t, b, "evaluate_job", "c1"); got != "error" {
		t.Fatalf("outcome = %q", got)
	}
	res = b.call(t, "c2", "find_report", `{}`)
	if !res.IsError || !strings.Contains(res.Text, "did not finish") {
		t.Fatalf("timeout result = %+v", res)
	}
}

func claimOutcome(t *testing.T, b *bridgeEnv, tool, key string) string {
	t.Helper()
	var outcome string
	if err := b.f.st.Read().QueryRow(`SELECT outcome FROM agent_claims WHERE tool = ? AND key = ?`, tool, key).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	return outcome
}
