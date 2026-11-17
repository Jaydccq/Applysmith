package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/agent/sessions"
	"applysmith/internal/gates"
	"applysmith/internal/store"
)

func noop(context.Context, ToolCall) (ToolResult, error) { return ToolResult{Text: "ok"}, nil }

func guardTools() []Tool {
	obj := func(props string) string {
		return `{"type":"object","additionalProperties":false,"properties":{` + props + `}}`
	}
	mk := func(name string, class Class, props string, mod func(*Tool)) Tool {
		t := Tool{Name: name, Label: name, Description: name + ".", Class: class, PII: PIINone, Timeout: time.Second, Schema: obj(props), Run: noop}
		if mod != nil {
			mod(&t)
		}
		return t
	}
	return []Tool{
		mk("save_fact", ClassWriteLocal, `"body":{"type":"string"}`, nil),
		mk("answer_bank_save", ClassWriteLocal, `"body":{"type":"string"}`, func(t *Tool) { t.RequiresApproval = true }),
		mk("propose_rule", ClassDraft, `"text":{"type":"string"}`, nil),
		mk("gmail_signals", ClassRead, ``, func(t *Tool) { t.OutsideContent = true }),
		mk("evaluate_job", ClassExternalAction, `"url":{"type":"string"}`, func(t *Tool) { t.OutsideContent = true }),
		mk("scan_portal", ClassExternalAction, `"source":{"type":"string"}`, func(t *Tool) { t.Idempotent = true }),
		mk("autofill_application", ClassExternalAction, `"url":{"type":"string"},"submit":{"type":"boolean"}`, func(t *Tool) { t.Idempotent = true }),
		mk("apply_from_list", ClassExternalAction, `"submit":{"type":"boolean"}`, func(t *Tool) { t.Idempotent = true }),
		mk("wipe_workspace", ClassWriteLocal, ``, nil),
		mk("batch_skip_list", ClassWriteLocal, `"action":{"type":"string"},"company":{"type":"string"}`, nil),
	}
}

type guardCase struct {
	name    string
	channel Channel
	origin  Origin
	message string
	tool    string
	args    string
	carried bool
	outside bool
	remote  bool
	list    []string
	// mandate binds the turn to a task that mandate delegated; continues makes
	// it a turn the task took by itself (its message is the mandate).
	mandate   string
	continues bool
	taskRuns  []string
	want      gates.Verdict
	code      string
}

func runGuardCases(t *testing.T, cases []guardCase, extra ...Tool) {
	t.Helper()
	f := newFixture(t, append(guardTools(), extra...)...)
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := NewGuard(f.registry, GuardOptions{Sessions: f.sessions, Salt: "salt", ApplyList: func(context.Context) ([]string, error) { return c.list, nil }})
			turn := &Turn{ID: "turn_" + string(rune('a'+i)), SessionID: "ses_g", Channel: c.channel, Origin: cmpOrOrigin(c.origin), UserMessage: c.message, CarriedTaint: c.carried, Remote: c.remote}
			if c.outside {
				turn.MarkOutside()
			}
			if c.mandate != "" || c.continues {
				turn.Task, turn.Mandate, turn.Continues, turn.TaskRuns = "task_1", c.mandate, c.continues, c.taskRuns
			}
			tool, ok := f.registry.Lookup(c.tool)
			if !ok {
				t.Fatalf("no tool %s", c.tool)
			}
			args := json.RawMessage(cmp.Or(c.args, `{}`))
			d := g.Decide(context.Background(), turn, tool, "call_"+turn.ID, args)
			if d.Verdict != c.want || d.Code != c.code {
				t.Fatalf("decision = %s %s (%s) at %s, want %s %s", d.Verdict, d.Code, d.Reason, d.Gate, c.want, c.code)
			}
			// The decision's gate audited the decision's verdict and code; a
			// refusal is the last row, as nothing after it runs.
			rows := mustDecisions(t, f, turn.ID)
			i := slices.IndexFunc(rows, func(r gates.AuditRow) bool { return r.Gate == d.Gate })
			if i < 0 || rows[i].Verdict != d.Verdict || rows[i].Code != d.Code || rows[i].Tool != c.tool || !d.Allowed() && i != len(rows)-1 {
				t.Fatalf("audit rows = %+v, decision %+v", rows, d)
			}
		})
	}
}

func cmpOrOrigin(o Origin) Origin {
	if o == "" {
		return OriginModel
	}
	return o
}

func TestGuard_PolicyFloorAndChannelEnvelope(t *testing.T) {
	runGuardCases(t, []guardCase{
		{name: "irreversible write is denied by the floor", channel: ChannelDashboard, message: "wipe it", tool: "wipe_workspace", want: gates.Deny, code: CodeIrreversibleWrite},
		{name: "scheduled turn cannot write", channel: ChannelScheduled, message: "remember this", tool: "save_fact", want: gates.Deny, code: CodeChannelEnvelope},
		{name: "digest turn cannot start an action", channel: ChannelDigest, message: "scan it", tool: "scan_portal", want: gates.Deny, code: CodeChannelEnvelope},
		{name: "scheduled turn can read", channel: ChannelScheduled, tool: "gmail_signals", want: gates.Allow, code: CodeReadOnly},
		{name: "scheduled turn can draft", channel: ChannelScheduled, tool: "propose_rule", args: `{"text":"x"}`, want: gates.AllowWithTrace, code: CodeDraftTier},
		{name: "telegram cannot submit", channel: ChannelTelegram, message: "apply to https://jobs.example.test/p/1", tool: "apply_from_list", args: `{"submit":true}`, want: gates.Deny, code: gates.CodeSubmitDashboardOnly},
		{name: "telegram cannot submit one posting", channel: ChannelTelegram, message: "apply to https://jobs.example.test/p/1", tool: "autofill_application", args: `{"url":"https://jobs.example.test/p/1","submit":true}`, want: gates.Deny, code: gates.CodeSubmitDashboardOnly},
		{name: "telegram cannot start a dry run", channel: ChannelTelegram, message: "fill https://jobs.example.test/p/1", tool: "autofill_application", args: `{"url":"https://jobs.example.test/p/1"}`, want: gates.Deny, code: gates.CodeApplyDashboardOnly},
		{name: "telegram cannot start the list either", channel: ChannelTelegram, message: "apply to the list", tool: "apply_from_list", args: `{}`, want: gates.Deny, code: gates.CodeApplyDashboardOnly},
		{name: "telegram can still evaluate", channel: ChannelTelegram, message: "evaluate https://jobs.example.test/p/1", tool: "evaluate_job", args: `{"url":"https://jobs.example.test/p/1"}`, want: gates.AllowWithTrace, code: CodeActionNamed},
		{name: "telegram can read", channel: ChannelTelegram, tool: "gmail_signals", want: gates.Allow, code: CodeReadOnly},
	})
}

// A turn started from a remote device may read and draft; nothing that writes
// locally or acts externally is offered or decided in its favour.
func TestGuard_RemoteTurnHasReadAndDraftToolsOnly(t *testing.T) {
	const posting = "https://jobs.example.test/acme/44"
	runGuardCases(t, []guardCase{
		{name: "remote turn can read", channel: ChannelDashboard, remote: true, tool: "gmail_signals", want: gates.Allow, code: CodeReadOnly},
		{name: "remote turn can draft", channel: ChannelDashboard, remote: true, tool: "propose_rule", args: `{"text":"x"}`, want: gates.AllowWithTrace, code: CodeDraftTier},
		{name: "remote turn cannot write", channel: ChannelDashboard, remote: true, message: "remember that I prefer remote roles", tool: "save_fact", args: `{"body":"x"}`, want: gates.Deny, code: CodeChannelEnvelope},
		{name: "remote turn cannot evaluate", channel: ChannelDashboard, remote: true, message: "evaluate " + posting, tool: "evaluate_job", args: `{"url":"` + posting + `"}`, want: gates.Deny, code: CodeChannelEnvelope},
		{name: "remote turn cannot scan", channel: ChannelDashboard, remote: true, message: "scan it", tool: "scan_portal", args: `{"source":"x"}`, want: gates.Deny, code: CodeChannelEnvelope},
		{name: "remote turn cannot start an apply run", channel: ChannelDashboard, remote: true, message: "apply to " + posting, tool: "autofill_application", args: `{"url":"` + posting + `","submit":true}`, want: gates.Deny, code: CodeChannelEnvelope},
		{name: "remote turn cannot run the list", channel: ChannelDashboard, remote: true, message: "apply to the list", tool: "apply_from_list", args: `{}`, want: gates.Deny, code: CodeChannelEnvelope},
		{name: "the same call from the dashboard is not refused for its channel", channel: ChannelDashboard, message: "remember that I prefer remote roles", tool: "save_fact", args: `{"body":"x"}`, want: gates.AllowWithTrace, code: CodeIntentStated},
	})
}

func TestGuard_ApprovalAndActionConfirmationByTier(t *testing.T) {
	runGuardCases(t, []guardCase{
		{name: "approval-gated tool is held without a recorded approval", channel: ChannelDashboard, message: "add this to my answer bank", tool: "answer_bank_save", args: `{"body":"x"}`, want: gates.Hold, code: CodeApprovalRequired},
		{name: "write without stated intent is held", channel: ChannelDashboard, message: "what do you think of my chances?", tool: "save_fact", args: `{"body":"x"}`, want: gates.Hold, code: CodeIntentNotStated},
		{name: "explicit intent is allowed with a trace", channel: ChannelDashboard, message: "remember that I prefer remote roles", tool: "save_fact", args: `{"body":"remote"}`, want: gates.AllowWithTrace, code: CodeIntentStated},
		{name: "a past question is not intent", channel: ChannelDashboard, message: "did you save my preference?", tool: "save_fact", args: `{"body":"x"}`, want: gates.Hold, code: CodeIntentNotStated},
		{name: "a negation cancels intent", channel: ChannelDashboard, message: "please do not remember that", tool: "save_fact", args: `{"body":"x"}`, want: gates.Hold, code: CodeIntentNotStated},
		{name: "a button counts as named", channel: ChannelDashboard, origin: OriginButton, message: "Save", tool: "save_fact", args: `{"body":"x"}`, want: gates.AllowWithTrace, code: CodeIntentStated},
		{name: "evaluation the user named", channel: ChannelDashboard, message: "evaluate https://jobs.example.test/p/1", tool: "evaluate_job", args: `{"url":"https://jobs.example.test/p/1"}`, want: gates.AllowWithTrace, code: CodeActionNamed},
		{name: "action not named is held", channel: ChannelDashboard, message: "what about https://jobs.example.test/p/1?", tool: "evaluate_job", args: `{"url":"https://jobs.example.test/p/1"}`, want: gates.Hold, code: CodeActionNotNamed},
		{name: "the verb inside a URL is not a verb", channel: ChannelDashboard, message: "https://jobs.example.test/scan/evaluate", tool: "evaluate_job", args: `{"url":"x"}`, want: gates.Hold, code: CodeActionNotNamed},
		{name: "tier 2 with intent after outside content", channel: ChannelDashboard, message: "remember that I prefer remote roles", tool: "save_fact", args: `{"body":"remote"}`, outside: true, want: gates.AllowWithTrace, code: CodeIntentStated},
		{name: "tier 2 with intent and a carried taint", channel: ChannelDashboard, message: "remember that I prefer remote roles", tool: "save_fact", args: `{"body":"remote"}`, carried: true, want: gates.AllowWithTrace, code: CodeIntentStated},
		{name: "action after outside content is held", channel: ChannelDashboard, message: "evaluate the second posting", tool: "evaluate_job", args: `{"url":"x"}`, outside: true, want: gates.Hold, code: CodeOutsideContent},
		{name: "taint carried into a model turn holds the call", channel: ChannelDashboard, message: "evaluate the second posting", tool: "evaluate_job", args: `{"url":"x"}`, carried: true, want: gates.Hold, code: CodeOutsideContent},
		{name: "router call is not held by carried taint", channel: ChannelDashboard, origin: OriginRouter, message: "https://jobs.example.test/p/1", tool: "evaluate_job", args: `{"url":"https://jobs.example.test/p/1"}`, carried: true, want: gates.AllowWithTrace, code: CodeActionNamed},
		{name: "a read after outside content is still a read", channel: ChannelDashboard, tool: "gmail_signals", outside: true, want: gates.Allow, code: CodeReadOnly},
		{name: "a Chinese evaluation request names the action", channel: ChannelDashboard, message: zhEvaluate + "\uff1ahttps://job-boards.greenhouse.io/otterai/jobs/7798264003", tool: "evaluate_job", args: `{"url":"https://job-boards.greenhouse.io/otterai/jobs/7798264003"}`, want: gates.AllowWithTrace, code: CodeActionNamed},
		{name: "a question about applying names no fill", channel: ChannelDashboard, message: "Should I apply to https://jobs.example.test/p/1?", tool: "autofill_application", args: `{"url":"https://jobs.example.test/p/1"}`, want: gates.Hold, code: CodeActionNotNamed},
		{name: "a fill the user asked for runs after outside content", channel: ChannelDashboard, message: "fill in the best match, do not submit", tool: "autofill_application", args: `{"url":"https://jobs.example.test/p/1"}`, outside: true, want: gates.AllowWithTrace, code: CodeOutsideAllowed},
		{name: "a pasted posting's own Apply now names no fill", channel: ChannelDashboard, message: pastedPosting, tool: "autofill_application", args: `{"url":"https://jobs.example.test/p/1"}`, want: gates.Hold, code: CodeActionNotNamed},
	})
}

// zhEvaluate is "help me evaluate this posting" in Chinese.
const zhEvaluate = "\u5e2e\u6211\u8bc4\u4f30\u4e00\u4e0b\u8fd9\u4e2a\u804c\u4f4d"

// pastedPosting is a job description pasted with nothing of the user's own
// above it: its words are the employer's.
const pastedPosting = "Senior Backend Engineer\nExample Corp, Remote\nWe build payment rails in Go.\nRequirements: five years of Go.\nTo fill this role we need you. Apply now: https://jobs.example.test/p/1"

func TestGuard_SubmitGate(t *testing.T) {
	const posting = "https://jobs.example.test/acme/44"
	submit := `{"url":"` + posting + `","submit":true}`
	runGuardCases(t, []guardCase{
		{name: "exact URL in the user's own message", channel: ChannelDashboard, message: "apply to " + posting, tool: "autofill_application", args: submit, want: gates.Allow, code: gates.CodeExactURLNamed},
		{name: "outside content was read and the URL is not named", channel: ChannelDashboard, message: "apply to the best one", tool: "autofill_application", args: submit, outside: true, want: gates.Hold, code: gates.CodeOutsideContentRead},
		{name: "carried taint holds a model-chosen submit", channel: ChannelDashboard, message: "apply to the best one", tool: "autofill_application", args: submit, carried: true, want: gates.Hold, code: gates.CodeOutsideContentRead},
		{name: "exact URL beats outside content", channel: ChannelDashboard, message: "apply to " + posting, tool: "autofill_application", args: submit, outside: true, want: gates.Allow, code: gates.CodeExactURLNamed},
		// A host the user named allows a submit only while nothing outside was read
		// (ask-agent "Host only is not enough after outside content").
		{name: "a host the user named, nothing outside read", channel: ChannelDashboard, message: "apply to something on https://jobs.example.test/", tool: "autofill_application", args: submit, want: gates.Allow, code: gates.CodeHostNamedOrListed},
		{name: "a listed posting on a shared host vouches for no other", channel: ChannelDashboard, message: "apply to the best posting", tool: "autofill_application", args: submit, list: []string{"https://jobs.example.test/acme/1"}, want: gates.Hold, code: gates.CodeHostNotNamedNotListed},
		{name: "outside content beats a named host", channel: ChannelDashboard, message: "apply to something on https://jobs.example.test/", tool: "autofill_application", args: submit, outside: true, want: gates.Hold, code: gates.CodeOutsideContentRead},
		{name: "a posting URL that is not http is held", channel: ChannelDashboard, message: "apply to file:///etc/hosts", tool: "autofill_application", args: `{"url":"file:///etc/hosts","submit":true}`, want: gates.Hold, code: gates.CodeURLNotHTTP},
		{name: "a host nobody named is held", channel: ChannelDashboard, message: "apply to the best posting", tool: "autofill_application", args: submit, want: gates.Hold, code: gates.CodeHostNotNamedNotListed},
		{name: "the list vouches for an exact URL", channel: ChannelDashboard, message: "apply to the best posting", tool: "autofill_application", args: submit, list: []string{posting}, want: gates.Allow, code: gates.CodeHostNamedOrListed},
		{name: "look-alike URL with user-info names nothing", channel: ChannelDashboard, message: "apply to https://good.example.test@jobs.example.test/acme/44", tool: "autofill_application", args: submit, want: gates.Hold, code: gates.CodeHostNotNamedNotListed},
		{name: "a submit the user did not ask for is refused", channel: ChannelDashboard, message: "what is the status of " + posting, tool: "autofill_application", args: submit, want: gates.Deny, code: gates.CodeNoExplicitUserRequest},
		{name: "a past question is not a request", channel: ChannelDashboard, message: "what did I submit for " + posting, tool: "autofill_application", args: submit, want: gates.Deny, code: gates.CodeNoExplicitUserRequest},
		{name: "list submit is allowed when nothing outside was read", channel: ChannelDashboard, message: "apply to today's list", tool: "apply_from_list", args: `{"submit":true}`, want: gates.Allow, code: gates.CodeBatchOverUserList},
		{name: "list submit after outside content is held", channel: ChannelDashboard, message: "apply to today's list", tool: "apply_from_list", args: `{"submit":true}`, outside: true, want: gates.Hold, code: gates.CodeOutsideContentRead},
		{name: "a router-chosen submit is explicit", channel: ChannelDashboard, origin: OriginRouter, message: "apply " + posting, tool: "autofill_application", args: submit, carried: true, want: gates.Allow, code: gates.CodeExactURLNamed},
		{name: "scheduled turns cannot submit", channel: ChannelScheduled, message: "apply to " + posting, tool: "autofill_application", args: submit, want: gates.Deny, code: CodeChannelEnvelope},
		{name: "a dry run needs only the named action", channel: ChannelDashboard, message: "fill " + posting, tool: "autofill_application", args: `{"url":"` + posting + `"}`, want: gates.AllowWithTrace, code: CodeActionNamed},
		{name: "a question about applying is not a request", channel: ChannelDashboard, message: "Should I apply to " + posting + "?", tool: "autofill_application", args: submit, want: gates.Deny, code: gates.CodeNoExplicitUserRequest},
		{name: "a pasted posting's own Apply now is not a request", channel: ChannelDashboard, message: strings.ReplaceAll(pastedPosting, "https://jobs.example.test/p/1", posting), tool: "autofill_application", args: submit, want: gates.Deny, code: gates.CodeNoExplicitUserRequest},
		{name: "a Chinese apply request is explicit", channel: ChannelDashboard, message: "\u5e2e\u6211\u7533\u8bf7 " + posting, tool: "autofill_application", args: submit, want: gates.Allow, code: gates.CodeExactURLNamed},
		{name: "a Chinese should-I question is not a request", channel: ChannelDashboard, message: "\u6211\u5e94\u8be5\u7533\u8bf7\u8fd9\u4e2a\u5417 " + posting, tool: "autofill_application", args: submit, want: gates.Deny, code: gates.CodeNoExplicitUserRequest},
		{name: "fill and press the final button: no submit", channel: ChannelDashboard, message: "Fill in " + posting + " for me and press the final button on the last page yourself", tool: "autofill_application", args: submit, want: gates.Deny, code: gates.CodeNoExplicitUserRequest},
		{name: "fill and press the final button: a fill", channel: ChannelDashboard, message: "Fill in " + posting + " for me and press the final button on the last page yourself", tool: "autofill_application", args: `{"url":"` + posting + `"}`, want: gates.AllowWithTrace, code: CodeActionNamed},
		{name: "apply and submit is explicit", channel: ChannelDashboard, message: "apply to " + posting + " and submit it", tool: "autofill_application", args: submit, want: gates.Allow, code: gates.CodeExactURLNamed},
		{name: "a quoted correction names nothing", channel: ChannelDashboard, message: "Look at this mail:\n> Please correct my application at " + posting + "\nwhat do you think", tool: "autofill_application", args: submit, want: gates.Deny, code: gates.CodeNoExplicitUserRequest},
		{name: "a fenced URL names nothing", channel: ChannelDashboard, message: "apply to the one in this mail\n```\n" + posting + "\n```", tool: "autofill_application", args: submit, outside: true, want: gates.Hold, code: gates.CodeOutsideContentRead},
		{name: "a request on the last line", channel: ChannelDashboard, message: pastedPosting + "\nApply to " + posting + " and submit it.", tool: "autofill_application", args: submit, want: gates.Allow, code: gates.CodeExactURLNamed},
		{name: "a Chinese do-not-submit forbids the submit", channel: ChannelDashboard, message: "\u5e2e\u6211\u586b\u5199 " + posting + " \u4f46\u4e0d\u8981\u63d0\u4ea4", tool: "autofill_application", args: submit, want: gates.Deny, code: gates.CodeNoExplicitUserRequest},
	})
}

// What a delegated task may do without a new message (owner decision DT-2):
// the user's words that delegated it count as their request, it works the runs
// its conversation started, and it never asks to submit.
func TestGuard_ADelegatedTaskActsOnTheUsersWordsAndNeverSubmits(t *testing.T) {
	const posting = "https://jobs.example.test/acme/44"
	runGuardCases(t, []guardCase{
		{name: "an apply request delegates a task", channel: ChannelDashboard, message: "\u5e2e\u6211\u7533\u8bf7\u5217\u8868\u91cc\u7684\u6240\u6709\u5c97\u4f4d", tool: ToolDelegateTask, want: gates.AllowWithTrace, code: CodeIntentStated},
		{name: "a question delegates nothing", channel: ChannelDashboard, message: "what is the status of my applications?", tool: ToolDelegateTask, want: gates.Hold, code: CodeIntentNotStated},
		{name: "the task's own turn works its own run", channel: ChannelDashboard, message: "apply to today's list", continues: true, taskRuns: []string{"run_1"}, tool: ToolApplyContinue, args: `{"run_id":"run_1"}`, outside: true, want: gates.AllowWithTrace, code: CodeTaskRun},
		{name: "the task's own turn hands its own run back", channel: ChannelDashboard, message: "fill Datadog and stop before Submit", continues: true, taskRuns: []string{"run_1"}, tool: ToolApplyLeave, args: `{"run_id":"run_1","reason":"r"}`, outside: true, want: gates.AllowWithTrace, code: CodeTaskRun},
		{name: "but not a run another conversation started", channel: ChannelDashboard, message: "fill Datadog and stop before Submit", continues: true, taskRuns: []string{"run_1"}, tool: ToolApplyLeave, args: `{"run_id":"run_9","reason":"r"}`, want: gates.Hold, code: CodeIntentNotStated},
		{name: "but no run another conversation started", channel: ChannelDashboard, message: "apply to today's list", continues: true, taskRuns: []string{"run_1"}, tool: ToolApplyContinue, args: `{"run_id":"run_9"}`, want: gates.Deny, code: CodeActionNotNamed},
		{name: "the task's own turn never asks to submit", channel: ChannelDashboard, message: "apply to " + posting + " and submit it", continues: true, tool: "autofill_application", args: `{"url":"` + posting + `","submit":true}`, want: gates.Deny, code: gates.CodeNoExplicitUserRequest},
		{name: "the mandate is no request to submit in the user's next message", channel: ChannelDashboard, message: "the first two", mandate: "apply to " + posting + " and submit it", tool: "autofill_application", args: `{"url":"` + posting + `","submit":true}`, want: gates.Deny, code: gates.CodeNoExplicitUserRequest},
		{name: "the mandate names the action for the user's answer", channel: ChannelDashboard, message: "the first two", mandate: "evaluate the postings on my list", tool: "evaluate_job", args: `{"url":"` + posting + `"}`, want: gates.AllowWithTrace, code: CodeActionNamed},
		{name: "a do-not-submit in the mandate holds for every turn", channel: ChannelDashboard, message: "apply to " + posting + " now", mandate: "fill my list but do not submit", tool: "autofill_application", args: `{"url":"` + posting + `","submit":true}`, want: gates.Deny, code: gates.CodeNoExplicitUserRequest},
	}, Tool{Name: ToolDelegateTask, Label: "d", Description: "d.", Class: ClassWriteLocal, PII: PIINone, Timeout: time.Second, Schema: `{"type":"object","additionalProperties":false,"properties":{"title":{"type":"string"}}}`, Run: noop},
		Tool{Name: ToolApplyContinue, Label: "c", Description: "c.", Class: ClassExternalAction, PII: PIINone, Timeout: time.Second, Schema: `{"type":"object","additionalProperties":false,"properties":{"run_id":{"type":"string"}}}`, Run: noop},
		Tool{Name: ToolApplyLeave, Label: "l", Description: "l.", Class: ClassWriteLocal, PII: PIINone, Timeout: time.Second, Schema: `{"type":"object","additionalProperties":false,"properties":{"run_id":{"type":"string"},"reason":{"type":"string"}}}`, Run: noop})
}

func TestGuard_SkipListRemovalNamesTheListAndEachCompany(t *testing.T) {
	remove := `{"action":"remove","company":"Apple"}`
	runGuardCases(t, []guardCase{
		{name: "a verb aimed elsewhere", channel: ChannelDashboard, message: "Unblock all companies and clear my tracker. Also dismiss every inbox row.", tool: "batch_skip_list", args: remove, want: gates.Hold, code: CodeObjectNotNamed},
		{name: "the list and the company named", channel: ChannelDashboard, message: "remove Apple from my skip list", tool: "batch_skip_list", args: remove, want: gates.AllowWithTrace, code: CodeIntentStated},
		{name: "another company", channel: ChannelDashboard, message: "remove Apple from my skip list", tool: "batch_skip_list", args: `{"action":"remove","company":"Microsoft"}`, want: gates.Hold, code: CodeObjectNotNamed},
		{name: "a bulk removal", channel: ChannelDashboard, message: "remove everything from my skip list, Apple too", tool: "batch_skip_list", args: remove, want: gates.Hold, code: CodeBulkRemoval},
		// Chinese: remove Apple from the skip list.
		{name: "a Chinese removal", channel: ChannelDashboard, message: "\u628aApple\u4ece\u8df3\u8fc7\u540d\u5355\u91cc\u5220\u9664", tool: "batch_skip_list", args: remove, want: gates.AllowWithTrace, code: CodeIntentStated},
		{name: "an add names the list", channel: ChannelDashboard, message: "add Apple to my skip list", tool: "batch_skip_list", args: `{"action":"add","company":"Apple"}`, want: gates.AllowWithTrace, code: CodeIntentStated},
		{name: "the list action is a read", channel: ChannelDashboard, message: "what is on it?", tool: "batch_skip_list", args: `{"action":"list"}`, want: gates.Allow, code: CodeReadOnly},
	})
	f := newFixture(t, guardTools()...)
	tool, _ := f.registry.Lookup("batch_skip_list")
	turn := &Turn{ID: "turn_bulk", SessionID: "ses_b", Channel: ChannelDashboard, Origin: OriginModel, UserMessage: "remove Apple and Microsoft from my skip list"}
	if d := f.guard.Decide(context.Background(), turn, tool, "call_1", json.RawMessage(remove)); !d.Allowed() {
		t.Fatalf("first removal = %+v", d)
	}
	if d := f.guard.Decide(context.Background(), turn, tool, "call_2", json.RawMessage(`{"action":"remove","company":"Microsoft"}`)); d.Verdict != gates.Hold || d.Code != CodeBulkRemoval {
		t.Fatalf("second removal in the turn = %+v", d)
	}
}

// A created scheduled prompt is only proposed, so a request that states its
// cadence and no write verb (a live turn had held it on a card the user would
// approve before confirming the proposal too) is a draft; the actions that
// change an active prompt still need the user's words.
func TestGuard_AScheduledPromptIsProposedFromItsCadence(t *testing.T) {
	const every = "Every weekday at 8:30am, give me a short summary of what needs my attention today."
	runGuardCases(t, []guardCase{
		{name: "create is a draft", channel: ChannelDashboard, message: every, tool: "manage_scheduled_prompt", args: `{"action":"create","text":"x","days":["weekdays"],"time":"08:30"}`, want: gates.AllowWithTrace, code: CodeDraftTier},
		{name: "delete needs the user's words", channel: ChannelDashboard, message: "what runs every morning?", tool: "manage_scheduled_prompt", args: `{"action":"delete","id":"prompt_1"}`, want: gates.Hold, code: CodeIntentNotStated},
		{name: "resume needs the user's words", channel: ChannelDashboard, message: every, tool: "manage_scheduled_prompt", args: `{"action":"resume","id":"prompt_1"}`, want: gates.Hold, code: CodeIntentNotStated},
	}, Tool{Name: "manage_scheduled_prompt", Label: "m", Description: "m.", Class: ClassWriteLocal, PII: PIINone, Timeout: time.Second, Run: noop,
		Schema: `{"type":"object","additionalProperties":false,"properties":{"action":{"type":"string"},"id":{"type":"string"},"text":{"type":"string"},"days":{"type":"array","items":{"type":"string"}},"time":{"type":"string"}}}`})
}

// "Find me new jobs" asks for a search: it names a scan and a task that runs
// one, and nothing wider (live, it had held delegate_task on a card).
func TestGuard_ASearchNamesAScanAndItsTaskOnly(t *testing.T) {
	const find = "Find me new senior platform engineer jobs posted this week and tell me the best three."
	runGuardCases(t, []guardCase{
		{name: "find names a scan", channel: ChannelDashboard, message: find, tool: "scan_portal", args: `{"source":"greenhouse"}`, want: gates.AllowWithTrace, code: CodeActionNamed},
		{name: "find delegates a task", channel: ChannelDashboard, message: find, tool: ToolDelegateTask, want: gates.AllowWithTrace, code: CodeIntentStated},
		{name: "find names no evaluation", channel: ChannelDashboard, message: find, tool: "evaluate_job", args: `{"url":"https://jobs.example.test/p/1"}`, want: gates.Hold, code: CodeActionNotNamed},
		{name: "find names no fill", channel: ChannelDashboard, message: find, tool: "autofill_application", args: `{"url":"https://jobs.example.test/p/1"}`, want: gates.Hold, code: CodeActionNotNamed},
		// Chinese: help me find this week's new platform engineer roles.
		{name: "a Chinese search names a scan", channel: ChannelDashboard, message: "\u5e2e\u6211\u627e\u4e00\u4e0b\u8fd9\u5468\u65b0\u7684\u5e73\u53f0\u5de5\u7a0b\u5e08\u804c\u4f4d", tool: "scan_portal", args: `{"source":"greenhouse"}`, want: gates.AllowWithTrace, code: CodeActionNamed},
		{name: "a question about a past search names none", channel: ChannelDashboard, message: "did you find anything new?", tool: "scan_portal", args: `{"source":"greenhouse"}`, want: gates.Hold, code: CodeActionNotNamed},
	}, Tool{Name: ToolDelegateTask, Label: "d", Description: "d.", Class: ClassWriteLocal, PII: PIINone, Timeout: time.Second, Schema: `{"type":"object","additionalProperties":false,"properties":{"title":{"type":"string"}}}`, Run: noop})
}

func TestGuard_SteerMessagesAreTheUsersWords(t *testing.T) {
	const posting = "https://jobs.example.test/acme/44"
	f := newFixture(t, guardTools()...)
	tool, _ := f.registry.Lookup("autofill_application")
	args := json.RawMessage(`{"url":"` + posting + `","submit":true}`)
	for i, c := range []struct {
		steer string
		want  gates.Verdict
	}{{"apply to it and submit it", gates.Allow}, {"should I submit it?", gates.Deny}, {"apply to it, do not submit", gates.Deny}} {
		turn := &Turn{ID: fmt.Sprint("turn_s", i), SessionID: "ses_s", Channel: ChannelDashboard, Origin: OriginModel, UserMessage: "look at " + posting}
		turn.Steer(c.steer)
		if d := f.guard.Decide(context.Background(), turn, tool, fmt.Sprint("call_s", i), args); d.Verdict != c.want {
			t.Errorf("steer %q = %+v, want %s", c.steer, d, c.want)
		}
	}
}

func TestGuard_IdempotencyClaimSurvivesARestart(t *testing.T) {
	f := newFixture(t, guardTools()...)
	tool, _ := f.registry.Lookup("scan_portal")
	turn := &Turn{ID: "turn_1", SessionID: "ses_1", Channel: ChannelDashboard, Origin: OriginModel, UserMessage: "scan the example portal"}
	ctx := context.Background()
	if d := f.guard.Decide(ctx, turn, tool, "call_1", json.RawMessage(`{}`)); !d.Allowed() {
		t.Fatalf("first call = %+v", d)
	}
	if d := f.guard.Decide(ctx, turn, tool, "call_1", json.RawMessage(`{}`)); d.Verdict != gates.Deny || d.Code != CodeIdempotencyClaim {
		t.Fatalf("replay = %+v", d)
	}
	restarted := NewGuard(f.registry, GuardOptions{Sessions: f.sessions, Salt: "salt"})
	turn2 := &Turn{ID: "turn_2", SessionID: "ses_1", Channel: ChannelDashboard, Origin: OriginModel, UserMessage: "scan the example portal"}
	if d := restarted.Decide(ctx, turn2, tool, "call_1", json.RawMessage(`{}`)); d.Code != CodeIdempotencyClaim {
		t.Fatalf("after a restart = %+v", d)
	}
	if d := restarted.Decide(ctx, turn2, tool, "call_2", json.RawMessage(`{}`)); !d.Allowed() {
		t.Fatalf("a fresh call id = %+v", d)
	}
}

func TestGuard_AGateThatErrorsDenies(t *testing.T) {
	f := newFixture(t, guardTools()...)
	tool, _ := f.registry.Lookup("gmail_signals")
	turn := &Turn{ID: "turn_1", SessionID: "ses_1", Channel: ChannelDashboard, Origin: OriginModel}
	g := NewGuard(f.registry, GuardOptions{Sessions: f.sessions})
	g.step = append([]guardStep{{GateConfirmation, func(context.Context, *callState) (gates.Decision, error) {
		return gates.Decision{}, errors.New("boom")
	}}}, g.step...)
	d := g.Decide(context.Background(), turn, tool, "call_1", json.RawMessage(`{}`))
	if d.Verdict != gates.Deny || d.Code != CodeGateError {
		t.Fatalf("decision = %+v", d)
	}
	rows, _ := f.sessions.Decisions(context.Background(), "turn_1")
	if len(rows) != 1 || rows[0].Code != CodeGateError {
		t.Fatalf("audit = %+v", rows)
	}
}

func TestGuard_AnAuditThatCannotBeWrittenDenies(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "applysmith.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := NewRegistry(guardTools()...)
	g := NewGuard(reg, GuardOptions{Sessions: sessions.New(st, nil)})
	_ = st.Close()
	tool, _ := reg.Lookup("gmail_signals")
	d := g.Decide(context.Background(), &Turn{ID: "t", SessionID: "s", Channel: ChannelDashboard}, tool, "c", json.RawMessage(`{}`))
	if d.Verdict != gates.Deny || d.Code != CodeGateError {
		t.Fatalf("decision = %+v", d)
	}
}

func TestGuard_UndeclaredClassAndForbiddenNamesAreDeniedEvenIfRegistrationWasBypassed(t *testing.T) {
	f := newFixture(t)
	turn := &Turn{ID: "turn_1", SessionID: "ses_1", Channel: ChannelDashboard, Origin: OriginModel, UserMessage: "do it"}
	for name, tool := range map[string]Tool{
		"no class":       {Name: "mystery_tool"},
		"submit name":    {Name: "submit_application", Class: ClassExternalAction},
		"withdraw name":  {Name: "withdraw_application", Class: ClassExternalAction},
		"tracker_update": {Name: "tracker_update", Class: ClassWriteLocal},
	} {
		d := f.guard.Decide(context.Background(), turn, tool, "call_"+name, json.RawMessage(`{}`))
		want := CodeForbiddenTool
		if name == "no class" {
			want = CodeUndeclaredClass
		}
		if d.Verdict != gates.Deny || d.Code != want {
			t.Errorf("%s: decision = %+v, want deny %s", name, d, want)
		}
	}
}

func TestGuard_AuditIsOneRowPerDistinctDecisionAndStepDoesNotChangeTheHash(t *testing.T) {
	f := newFixture(t, guardTools()...)
	tool, _ := f.registry.Lookup("save_fact")
	turn := &Turn{ID: "turn_1", SessionID: "ses_1", Channel: ChannelDashboard, Origin: OriginModel, UserMessage: "remember my notice period"}
	ctx := context.Background()
	a, _, _ := splitStep(json.RawMessage(`{"body":"two months","step":"Saving a note"}`))
	b, _, _ := splitStep(json.RawMessage(`{"step":"Writing it down","body":"two months"}`))
	if argHash(a) != argHash(b) {
		t.Fatal("two calls that differ only in step hash differently")
	}
	for i := 0; i < 3; i++ {
		f.guard.Decide(ctx, turn, tool, "call_1", a)
	}
	f.guard.Decide(ctx, turn, tool, "call_2", json.RawMessage(`{"body":"another"}`))
	rows, _ := f.sessions.Decisions(ctx, "turn_1")
	perGate := map[gates.Gate]int{}
	for _, r := range rows {
		perGate[r.Gate]++
		if r.Caller != gates.CallerAskAgent || r.RiskTier != "2" || r.ConversationID != "ses_1" || r.UserMessageHash == "" {
			t.Fatalf("row = %+v", r)
		}
		if strings.Contains(r.Reason, "notice period") || strings.Contains(r.UserMessageHash, "notice") {
			t.Fatal("the user's text reached the audit")
		}
	}
	for _, g := range []gates.Gate{GatePolicyFloor, GateChannelEnvelope, GateApproval, GateConfirmation} {
		if perGate[g] != 2 {
			t.Errorf("gate %s has %d rows; want one per distinct argument fingerprint (2)", g, perGate[g])
		}
	}
}

func TestGuard_RouterAndModelCallsWalkTheSameChain(t *testing.T) {
	f := newFixture(t, guardTools()...)
	tool, _ := f.registry.Lookup("evaluate_job")
	ctx := context.Background()
	chain := func(turnID string, origin Origin) []string {
		turn := &Turn{ID: turnID, SessionID: "ses_1", Channel: ChannelDashboard, Origin: origin, UserMessage: "evaluate https://jobs.example.test/p/1"}
		if d := f.guard.Decide(ctx, turn, tool, "call_"+turnID, json.RawMessage(`{"url":"https://jobs.example.test/p/1"}`)); !d.Allowed() {
			t.Fatalf("%s: %+v", origin, d)
		}
		rows, _ := f.sessions.Decisions(ctx, turnID)
		var out []string
		for _, r := range rows {
			out = append(out, string(r.Gate)+":"+string(r.Verdict))
		}
		return out
	}
	model, router := chain("turn_m", OriginModel), chain("turn_r", OriginRouter)
	if strings.Join(model, ",") != strings.Join(router, ",") {
		t.Fatalf("model chain %v, router chain %v", model, router)
	}
	if len(model) != 6 {
		t.Fatalf("chain = %v; want the six gates in order", model)
	}
}

type fakeGrants struct{ id string }

func (g fakeGrants) Covers(context.Context, *Turn, Tool, json.RawMessage) (string, bool) {
	return g.id, g.id != ""
}

func TestGuard_AStandingApprovalLiftsOnlyAConfirmationHold(t *testing.T) {
	f := newFixture(t, guardTools()...)
	g := NewGuard(f.registry, GuardOptions{Sessions: f.sessions, Grants: fakeGrants{id: "G1"}})
	ctx := context.Background()
	eval, _ := f.registry.Lookup("evaluate_job")
	turn := &Turn{ID: "turn_1", SessionID: "ses_1", Channel: ChannelDashboard, Origin: OriginModel, UserMessage: "evaluate the next one"}
	turn.MarkOutside()
	d := g.Decide(ctx, turn, eval, "call_1", json.RawMessage(`{"url":"x"}`))
	if d.Verdict != gates.AllowWithTrace || d.Code != "grant:G1" {
		t.Fatalf("tainted evaluation with a grant = %+v", d)
	}
	// Not the approval requirement, not the envelope, not the submit gate.
	add, _ := f.registry.Lookup("answer_bank_save")
	if d := g.Decide(ctx, turn, add, "call_2", json.RawMessage(`{"body":"x"}`)); d.Verdict != gates.Hold || d.Code != CodeApprovalRequired {
		t.Fatalf("approval-gated tool = %+v", d)
	}
	sched := &Turn{ID: "turn_2", SessionID: "ses_1", Channel: ChannelScheduled, Origin: OriginModel}
	if d := g.Decide(ctx, sched, eval, "call_3", json.RawMessage(`{"url":"x"}`)); d.Verdict != gates.Deny {
		t.Fatalf("scheduled turn = %+v", d)
	}
	fill, _ := f.registry.Lookup("autofill_application")
	submitTurn := &Turn{ID: "turn_3", SessionID: "ses_1", Channel: ChannelDashboard, Origin: OriginModel, UserMessage: "apply to the best one"}
	submitTurn.MarkOutside()
	if d := g.Decide(ctx, submitTurn, fill, "call_4", json.RawMessage(`{"url":"https://jobs.example.test/p/1","submit":true}`)); d.Verdict != gates.Hold || d.Code != gates.CodeOutsideContentRead {
		t.Fatalf("submit with a grant = %+v", d)
	}
}

func TestIntent_NamesVerb(t *testing.T) {
	for _, tc := range []struct {
		msg  string
		want bool
	}{
		{"please scan Greenhouse", true},
		{"Scan.", true},
		{"can you evaluate this?", true},
		{"did you scan Greenhouse?", false},
		{"what did I submit today?", false},
		{"don't scan anything", false},
		{"do not evaluate that", false},
		{"I never apply on weekends", false},
		{"https://example.test/scan", false},
		{"how are my rows?", false},
		{"", false},
		{"Should I apply to https://jobs.example.test/p/1?", false},
		{"can you tell me if I should apply?", false},
		{"evaluate https://jobs.example.test/p/1 - what do you make of it?", true},
		{"apply to https://jobs.example.test/p/1", true},
		{"apply to https://jobs.example.test/p/1\n\n" + pastedPosting, true},
		{pastedPosting, false},
		{pastedPosting + "\nApply to https://jobs.example.test/p/1 and submit it.", true},
		{pastedPosting + "\nApply now: https://jobs.example.test/p/1", false},
		{"Here is the posting:\n```\nApply now\n```", false},
		{"> Apply now\nwhat do you make of this", false},
		// Chinese: help me evaluate this posting: <url>; help me apply to this;
		// scan jobright with the source glued on; should I apply to this?;
		// I already applied; do not apply to this; apply or not?
		{zhEvaluate + "\uff1ahttps://job-boards.greenhouse.io/otterai/jobs/7798264003", true},
		{"\u5e2e\u6211\u7533\u8bf7\u8fd9\u4e2a https://jobs.example.test/p/1", true},
		{"\u626b\u63cfjobright", true},
		{"\u6211\u5e94\u8be5\u7533\u8bf7\u8fd9\u4e2a\u5417 https://jobs.example.test/p/1", false},
		{"\u6211\u5df2\u7ecf\u7533\u8bf7\u4e86 https://jobs.example.test/p/1", false},
		{"\u4e0d\u8981\u7533\u8bf7\u8fd9\u4e2a", false},
		{"\u8981\u4e0d\u8981\u7533\u8bf7\u8fd9\u4e2a\u804c\u4f4d", false},
		{"\u4e0d\u9519\uff0c\u5e2e\u6211\u7533\u8bf7", true},
		// Answers to a run's form questions: "fill the rest in my words: 1)
		// whether you are in process with other companies: No"; and "whether I
		// should fill this in" is still a question.
		{"\u5269\u4e0b\u7684\u9898\u6309\u6211\u7684\u8bdd\u586b\uff1a1\uff09\u662f\u5426\u5728\u5176\u4ed6\u516c\u53f8\u6d41\u7a0b\u4e2d\uff1aNo", true},
		{"\u6211\u662f\u5426\u5e94\u8be5\u586b\u8fd9\u4e2a", false},
	} {
		if got := namesVerb(tc.msg, actionVerbs) || namesVerb(tc.msg, applyVerbs); got != tc.want {
			t.Errorf("namesVerb(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}
}
