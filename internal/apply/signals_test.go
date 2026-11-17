package apply

import (
	"applysmith/internal/engine"
	"context"

	"testing"
	"time"

	"applysmith/internal/gates"
)

type fakeSignals struct{ got [][3]string }

func (f *fakeSignals) RecordSignal(_ context.Context, system, signal, runID string) (time.Time, error) {
	f.got = append(f.got, [3]string{system, signal, runID})
	return time.Time{}, nil
}

func signupInput() Input {
	return Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true, UserStarted: true}
}

// refuseDefault makes the shared default the host's only guess and refuses it,
// so the account stage reaches the sign-up decision.
func refuseDefault(a *acctEnv) {
	signupPages(a, false)
	orig := a.tab.onClick
	a.tab.onClick = func(t *scriptTab, c Control) {
		if c.Labels[0] == "Sign in" && t.typed[1] == "Default-Pw-1!x" {
			t.typed = map[int]string{}
			t.set(loginPage("Invalid email or password"))
			return
		}
		orig(t, c)
	}
}

func TestApply_Signals_AGenericChallengeStopIsKeyedByTheHostItStoodOn(t *testing.T) {
	rec := &fakeSignals{}
	a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", func(d *Deps) { d.Page.Signals = rec })
	a.e.sess = a.s
	a.s.last = &PageState{URL: "https://apply.example.test/form/2"}
	a.e.Run.Outcome = Outcome{Kind: OutcomeStopped, Stage: StageSubmit, Board: "challenge"}
	a.e.Run.Signins = []SigninRecord{{Host: "login.example.test", Lockout: true}}
	a.g.r.recordSignals(a.e)
	// A dry run's challenge cools that host's sign-ups only, never every
	// unnamed host; the lockout is the sign-in host's.
	want := [][3]string{{"generic:apply.example.test", gates.SignalCaptcha, "run_acct"}, {"generic:login.example.test", gates.SignalAccountLocked, "run_acct"}}
	if len(rec.got) != 2 || rec.got[0] != want[0] || rec.got[1] != want[1] {
		t.Fatalf("signals = %v, want %v", rec.got, want)
	}
	// A stop at the account stage is about the account page.
	b := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", func(d *Deps) { d.Page.Signals = rec })
	b.e.sess = b.s
	b.s.last = &PageState{URL: "https://apply.example.test/form/2"}
	b.e.Run.Outcome = Outcome{Kind: OutcomeStopped, Stage: StageAccount, Board: "challenge"}
	rec.got = nil
	b.g.r.recordSignals(b.e)
	if len(rec.got) != 1 || rec.got[0][0] != "generic:careers.example.test" {
		t.Fatalf("signals = %v", rec.got)
	}
}

func TestApply_Signals_AChallengeStopAndALockoutAreRecordedOncePerRun(t *testing.T) {
	rec := &fakeSignals{}
	a := newAcct(t, signupInput(), ATSWorkday, "example", func(d *Deps) { d.Page.Signals = rec })
	a.e.Run.Outcome = Outcome{Kind: OutcomeStopped, Board: "challenge"}
	a.e.Run.Signins = []SigninRecord{{Host: "example.test", Lockout: true}, {Host: "example.test", Lockout: true}}
	a.g.r.recordSignals(a.e)
	a.g.r.recordSignals(a.e)
	want := [][3]string{{"workday", gates.SignalCaptcha, "run_acct"}, {"workday", gates.SignalAccountLocked, "run_acct"}}
	if len(rec.got) != 2 || rec.got[0] != want[0] || rec.got[1] != want[1] {
		t.Fatalf("signals = %v, want %v", rec.got, want)
	}
}

func TestApply_Signals_AStopThatIsNotADetectionRecordsNothing(t *testing.T) {
	rec := &fakeSignals{}
	a := newAcct(t, signupInput(), ATSGeneric, "careers.example.test", func(d *Deps) { d.Page.Signals = rec })
	a.e.Run.Outcome = Outcome{Kind: OutcomeStopped, Board: "answer"}
	a.g.r.recordSignals(a.e)
	if len(rec.got) != 0 {
		t.Fatalf("signals = %v", rec.got)
	}
}

func TestApply_Run_ACancelledContextMakesTheStopACancellationUnlessAPressWentOut(t *testing.T) {
	for _, c := range []struct {
		name    string
		pressed bool
		want    string
	}{{"before any press", false, "cancelled during account: the page could not be read"}, {"after a press", true, "the page could not be read"}} {
		t.Run(c.name, func(t *testing.T) {
			a := newAcct(t, signupInput(), ATSGeneric, "careers.example.test", nil)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			a.e.Ctx = ctx
			a.e.Run.Pressed = c.pressed
			a.g.r.fail(a.e, &stop{class: ClassEnvironment, reason: "the page could not be read", at: StageAccount, board: "sign-in"})
			if got := a.e.Run.Outcome.Reason; got != c.want {
				t.Fatalf("reason %q, want %q", got, c.want)
			}
			if !c.pressed && a.e.Run.Outcome.Board != "" {
				t.Fatalf("a cancellation kept board reason %q", a.e.Run.Outcome.Board)
			}
		})
	}
}

func signalRun(t *testing.T, status int, mod func(*PageState, *acctEnv)) [][3]string {
	t.Helper()
	rec := &fakeSignals{}
	a := newAcct(t, signupInput(), ATSGeneric, "careers.example.test", func(d *Deps) { d.Page.Signals = rec })
	a.e.Cls = Classification{ApplyURL: loginURL}
	a.s.navStatus = status
	st := loginPage("")
	if mod != nil {
		mod(st, a)
	}
	a.tab.set(st)
	_, _ = a.run()
	return rec.got
}

// The entry page's own evidence records one signal for its system: the main
// resource's status, a login page on another host than the apply page, and a
// page that denies access.
func TestApply_Signals_TheEntryPageRecordsWhatItShows(t *testing.T) {
	url := func(u string) func(*PageState, *acctEnv) { return func(st *PageState, _ *acctEnv) { st.URL = u } }
	for _, c := range []struct {
		name   string
		status int
		mod    func(*PageState, *acctEnv)
		want   string
	}{
		{"HTTP 403", 403, nil, gates.SignalCaptcha},
		{"HTTP 429", 429, nil, gates.SignalRateLimited},
		{"HTTP 200", 200, nil, ""},
		{"a login page on another host", 200, url("https://sso.example.org/login"), gates.SignalLoginRedirect},
		{"a login path on the expected host", 200, url("https://careers.example.test/login"), ""},
		{"another host off a login path", 200, url("https://sso.example.org/jobs/7"), ""},
		{"an access-denied page", 200, func(st *PageState, _ *acctEnv) { st.Text += " Access denied. Please complete a security check." }, gates.SignalVerificationRequired},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := signalRun(t, c.status, c.mod)
			if c.want == "" && len(got) != 0 {
				t.Fatalf("recorded %v", got)
			}
			if c.want != "" && (len(got) != 1 || got[0][1] != c.want || got[0][2] != "run_acct") {
				t.Fatalf("recorded %v, want %s", got, c.want)
			}
		})
	}
}

func TestApply_Signals_AFirstPageWithFewerStandardFieldsThanItsSystemExposesIsSilentDegradation(t *testing.T) {
	row := func(label string) engine.Row { return engine.Row{Label: label, Outcome: "filled"} }
	for _, c := range []struct {
		name string
		ats  string
		rows []engine.Row
		want bool
	}{
		{"greenhouse with all three", ATSGreenhouse, []engine.Row{row("First Name"), row("Last Name"), row("Email")}, false},
		{"greenhouse missing the last name", ATSGreenhouse, []engine.Row{row("First Name"), row("Email"), row("Reference name")}, true},
		{"lever with name and email", ATSLever, []engine.Row{row("Full name"), row("Email"), row("Phone")}, false},
		{"lever with only an email", ATSLever, []engine.Row{row("Email")}, true},
		{"a system with a free-form first page expects none", ATSGeneric, nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := &fakeSignals{}
			a := newAcct(t, signupInput(), c.ats, "acme", func(d *Deps) { d.Page.Signals = rec })
			a.s.page = &fakePage{fills: []*engine.PageResult{doneResult(c.rows...)}}
			a.tab.set(appPage())
			a.s.tab = a.tab
			_ = a.s.pageWork(context.Background(), &walker{r: a.g.r, e: a.e, s: a.s}, appPage())
			if got := len(rec.got) == 1 && rec.got[0][1] == gates.SignalSilentDegradation; got != c.want || len(rec.got) > 1 {
				t.Fatalf("signals = %v, want degradation %v", rec.got, c.want)
			}
		})
	}
}
