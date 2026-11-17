// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"strings"
	"testing"
)

// Every input below would be allowed outside a demo workspace; the marker
// alone turns each into a refusal with demo_workspace.
func TestGates_Demo_EveryOutboundActionIsRefusedBeforeAnyOtherCondition(t *testing.T) {
	submit := SubmitAuthInput{Caller: CallerAskAgent, Tool: ToolAutofillApplication, Submit: true, ExplicitRequest: true}
	got(t, SubmitAuthorization(submit), GateSubmitAuthorization, Allow, CodeOK)
	submit.Demo = true
	got(t, SubmitAuthorization(submit), GateSubmitAuthorization, Deny, CodeDemoWorkspace)

	acct := acctAllowed(nil)
	got(t, AccountCreation(acct), GateAccountCreation, Allow, CodeOK)
	acct.Demo = true
	got(t, AccountCreation(acct), GateAccountCreation, Deny, CodeDemoWorkspace)

	reset := ResetStartInput{Now: t0, AccountKnown: true, SavedPasswordRefused: true, HostIsWorkday: true}
	got(t, ResetStart(reset), GateResetStart, Allow, CodeOK)
	reset.Demo = true
	got(t, ResetStart(reset), GateResetStart, Deny, CodeDemoWorkspace)

	link := workdayMail(nil)
	got(t, ActivationLink(link), GateActivationLink, Allow, CodeOK)
	link.Demo = true
	got(t, ActivationLink(link), GateActivationLink, Deny, CodeDemoWorkspace)

	solve := solveOK(nil)
	got(t, CaptchaSolve(solve), GateCaptchaSolve, Allow, CodeOK)
	solve.Demo = true
	got(t, CaptchaSolve(solve), GateCaptchaSolve, Deny, CodeDemoWorkspace)
	// A dry run is refused by the solver gate anyway; in the demo the reason
	// is the demo, so the run ends as a bot check that names it.
	solve.Run = RunDryRun
	got(t, CaptchaSolve(solve), GateCaptchaSolve, Deny, CodeDemoWorkspace)

	// A code lookup with no facts is refused like any other mail.
	got(t, DemoWorkspace(DemoInput{Demo: true, Action: DemoMailCode}), GateDemoWorkspace, Deny, CodeDemoWorkspace)
	for _, a := range []DemoAction{DemoMail, DemoTelegram} {
		got(t, DemoWorkspace(DemoInput{Action: a}), GateDemoWorkspace, Allow, CodeOK)
		got(t, DemoWorkspace(DemoInput{Demo: true, Action: a}), GateDemoWorkspace, Deny, CodeDemoWorkspace)
	}
}

func TestGates_Demo_TheFictionalPersonasDocumentMayBeUploadedToAnyPage(t *testing.T) {
	for _, h := range []string{"127.0.0.1", "localhost", "boards.greenhouse.io", "careers.example.test", ""} {
		d := DemoWorkspace(DemoInput{Demo: true, Action: DemoUpload, Host: h})
		got(t, d, GateDemoWorkspace, Allow, CodeOK)
		if !strings.Contains(d.Reason, "fictional persona") {
			t.Errorf("%q: reason %q does not say whose document it is", h, d.Reason)
		}
	}
	got(t, DemoWorkspace(DemoInput{Action: DemoUpload, Host: "boards.greenhouse.io"}), GateDemoWorkspace, Allow, CodeOK)
}

// addr builds an address without writing one: fixtures stay on reserved domains
// except where the Gmail dot rule needs gmail.com itself.
func addr(local, domain string) string { return local + "@" + domain }

func codeFacts(mod func(*DemoCodeFacts)) DemoInput {
	const opened = int64(1_800_000_000_000)
	f := DemoCodeFacts{
		DryRun: true, InWalk: true, AtCodeDialog: true,
		PersonaEmail: addr("jordan.sample+applysmithdemo", "gmail.com"), Account: addr("jordansample", "gmail.com"),
		SenderDomain: "adp.com", OpenedMS: opened, SinceMS: opened - 1000,
	}
	if mod != nil {
		mod(&f)
	}
	return DemoInput{Demo: true, Action: DemoMailCode, Host: "workforcenow.adp.com", Code: f}
}

// Scenario "The demo reads one emailed code for a plus alias of the connected account".
func TestGates_Demo_AnEmailedCodeIsReadOnlyForAPlusAliasOfTheConnectedAccount(t *testing.T) {
	d := DemoWorkspace(codeFacts(nil))
	got(t, d, GateDemoWorkspace, Allow, CodeOK)
	if strings.Contains(d.Reason, "@") || !strings.Contains(d.Reason, "adp.com") {
		t.Errorf("reason %q must name the bound and carry no address", d.Reason)
	}
	// Outside a demo workspace the facts do not matter.
	in := codeFacts(func(f *DemoCodeFacts) { f.Account = "" })
	in.Demo = false
	got(t, DemoWorkspace(in), GateDemoWorkspace, Allow, CodeOK)

	other := addr("someone", "example.test")
	for name, c := range map[string]struct {
		mod  func(*DemoCodeFacts)
		want string
	}{
		"outside an apply walk":           {func(f *DemoCodeFacts) { f.InWalk = false }, "page walk"},
		"a run that submits":              {func(f *DemoCodeFacts) { f.DryRun = false }, "not a dry run"},
		"not at a code dialog":            {func(f *DemoCodeFacts) { f.AtCodeDialog = false }, "emailed-code dialog"},
		"no Gmail connected":              {func(f *DemoCodeFacts) { f.Account = "" }, "no Gmail account"},
		"a non-alias address":             {func(f *DemoCodeFacts) { f.PersonaEmail = addr("alex", "example.com") }, "not a plus-address alias"},
		"an empty tag":                    {func(f *DemoCodeFacts) { f.PersonaEmail = addr("jordansample+", "gmail.com") }, "not a plus-address alias"},
		"an empty base":                   {func(f *DemoCodeFacts) { f.PersonaEmail = addr("+tag", "gmail.com") }, "not a plus-address alias"},
		"no persona address":              {func(f *DemoCodeFacts) { f.PersonaEmail = "" }, "not a plus-address alias"},
		"an alias of another account":     {func(f *DemoCodeFacts) { f.Account = other }, "not an alias of the connected"},
		"the base differs":                {func(f *DemoCodeFacts) { f.PersonaEmail = addr("jordan.other+x", "gmail.com") }, "not an alias of the connected"},
		"another domain":                  {func(f *DemoCodeFacts) { f.PersonaEmail = addr("jordansample+x", "example.test") }, "not an alias of the connected"},
		"the dialog names another person": {func(f *DemoCodeFacts) { f.PageAddress = "k***@gmail.com" }, "other than the persona's"},
		"no known sender":                 {func(f *DemoCodeFacts) { f.SenderDomain = "" }, "sender domain"},
		"a sender that is not a domain":   {func(f *DemoCodeFacts) { f.SenderDomain = "adp" }, "sender domain"},
		"no dialog time":                  {func(f *DemoCodeFacts) { f.OpenedMS = 0 }, "before the dialog opened"},
		"mail from before the dialog":     {func(f *DemoCodeFacts) { f.SinceMS = f.OpenedMS - 60_000 }, "before the dialog opened"},
		"a second code without Resend":    {func(f *DemoCodeFacts) { f.CodesRead = 1 }, "already took its code"},
		"a third code":                    {func(f *DemoCodeFacts) { f.CodesRead, f.Resent = 2, true }, "already took its code"},
	} {
		d := DemoWorkspace(codeFacts(c.mod))
		got(t, d, GateDemoWorkspace, Deny, CodeDemoWorkspace)
		if !strings.Contains(d.Reason, c.want) || strings.Contains(d.Reason, "@") {
			t.Errorf("%s: reason %q, want it to say %q and carry no address", name, d.Reason, c.want)
		}
	}
	// The one more code after the dialog's own Resend, and the second of mail
	// timestamps rounded down.
	got(t, DemoWorkspace(codeFacts(func(f *DemoCodeFacts) { f.CodesRead, f.Resent = 1, true })), GateDemoWorkspace, Allow, CodeOK)
	got(t, DemoWorkspace(codeFacts(func(f *DemoCodeFacts) { f.SinceMS = f.OpenedMS })), GateDemoWorkspace, Allow, CodeOK)
	// A masked address on the dialog matches when its first letter and domain do.
	got(t, DemoWorkspace(codeFacts(func(f *DemoCodeFacts) { f.PageAddress = "j***@gmail.com" })), GateDemoWorkspace, Allow, CodeOK)
	got(t, DemoWorkspace(codeFacts(func(f *DemoCodeFacts) { f.PageAddress = addr("jordan.sample+applysmithdemo", "GMAIL.com") })), GateDemoWorkspace, Allow, CodeOK)
}

func TestGates_Demo_AliasRules(t *testing.T) {
	gm := func(local string) string { return addr(local, "gmail.com") }
	for _, c := range []struct {
		alias, account string
		want           bool
	}{
		{gm("jordansample+demo"), gm("jordansample"), true},
		{gm("Jordan.Sample+Demo"), gm("jordansample"), true}, // case and dots, Gmail only
		{gm("jordansample+demo"), gm("jordan.sample"), true}, // dots on the account side
		{addr("jordansample+demo", "googlemail.com"), gm("jordansample"), true},
		{addr("jordan.sample+demo", "example.test"), addr("jordansample", "example.test"), false}, // dots count off Gmail
		{addr("jordan.sample+demo", "example.test"), addr("Jordan.Sample", "EXAMPLE.test"), true},
		{gm("jordansample"), gm("jordansample"), false}, // not an alias: no tag
		{gm("jordansample+a+b"), gm("jordansample"), true},
		{gm("jordansample+demo"), gm("jordansample+demo"), false}, // an account with a tag is not a mailbox
		{gm("jordansample+demo"), "", false},
		{"not an address", gm("jordansample"), false},
	} {
		if got := AliasOf(c.alias, c.account); got != c.want {
			t.Errorf("AliasOf(%q, %q) = %v, want %v", c.alias, c.account, got, c.want)
		}
	}
}

// Connecting Gmail reads no mail and is allowed; the other mail actions stay refused.
func TestGates_Demo_ConnectingGmailIsAllowedAndEveryOtherMailActionStaysRefused(t *testing.T) {
	got(t, DemoWorkspace(DemoInput{Demo: true, Action: DemoMailConnect, Host: "imap.gmail.com"}), GateDemoWorkspace, Allow, CodeOK)
	for _, a := range []DemoAction{DemoMail, DemoTelegram, DemoAction("send"), DemoAction("sync"), DemoAction("")} {
		got(t, DemoWorkspace(DemoInput{Demo: true, Action: a}), GateDemoWorkspace, Deny, CodeDemoWorkspace)
	}
}
