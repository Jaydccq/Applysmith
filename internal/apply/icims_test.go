package apply

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"testing"

	"applysmith/internal/gates"
)

// icimsTab is a scripted tab that also answers the iCIMS step's page scripts
// from a model of the step: what it shows, what was pressed and what was sent.
type icimsTab struct {
	*scriptTab
	imu     sync.Mutex
	here    bool
	framed  bool
	found   bool
	widgets []CaptchaW
	presses []map[string]any
	sends   int
	tokens  []string
	blanks  int
	// onPress and onSend run after the model recorded the call.
	onPress func(t *icimsTab)
	onSend  func(t *icimsTab)
}

var icimsCall = regexp.MustCompile(`^"(\w+)",(.*)\)$`)

func (t *icimsTab) Eval(ctx context.Context, expr string, out any) error {
	head := "(" + strings.TrimSpace(icimsJS) + ")("
	rest, ok := strings.CutPrefix(expr, head)
	if !ok {
		return t.scriptTab.Eval(ctx, expr, out)
	}
	m := icimsCall.FindStringSubmatch(rest)
	t.imu.Lock()
	var reply any
	var hook func(*icimsTab)
	switch m[1] {
	case "find":
		reply = icimsStepGate{Found: t.found && t.here, Framed: t.framed, Labels: []string{"Next"}}
	case "state":
		reply = icimsState{Here: t.here, Widgets: t.widgets}
	case "press":
		var a map[string]any
		_ = json.Unmarshal([]byte(m[2]), &a)
		t.presses = append(t.presses, a)
		reply, hook = "pressed", t.onPress
	case "deliver":
		var a map[string]string
		_ = json.Unmarshal([]byte(m[2]), &a)
		t.tokens = append(t.tokens, a["kind"])
		reply = delivered{Fields: 1}
	case "blank":
		t.blanks++
		reply = 1
	case "send":
		t.sends++
		reply, hook = true, t.onSend
	}
	t.imu.Unlock()
	if hook != nil {
		hook(t)
	}
	return setJSON(out, reply)
}

// icimsStepPage is the step as its own page: one email box and Next.
func icimsStepPage() *PageState {
	return &PageState{
		URL: "https://careers.example.test/jobs/7/engineer/login", Title: "Log in", Text: "Enter your email", Lines: []string{"Enter your email"},
		Forms:    []FormInfo{{Fields: 1}},
		Inputs:   []InputInfo{{I: 0, Tag: "input", Type: "email", Label: "Email", Name: "css_loginName", Form: 0}},
		Controls: []Control{{I: 1, Tag: "button", Labels: []string{"Next"}, Form: 0}},
	}
}

// icimsFrameWrapper is the tenant's wrapper page: a frame and no field.
func icimsFrameWrapper() *PageState {
	return &PageState{
		URL: "https://careers.example.test/jobs/7/engineer/login", Title: "Careers", Text: "Careers", Lines: []string{"Careers"},
		Frames: []FrameInfo{{Src: "https://careers.example.test/jobs/7/engineer/login?in_iframe=1", Visible: true, W: 900, H: 700}},
	}
}

const icimsApplicant = `{"candidate":{"email":"alex@example.com","address":{"country":"%s"}}}`

type icimsWalk struct {
	*wenv
	it  *icimsTab
	svc *twoCaptcha
}

// newICIMSWalk walks a page whose iCIMS step is described by the model: the
// step leaves the page for a one-page application when Next is pressed.
func newICIMSWalk(t *testing.T, top *PageState, framed bool, country string, in Input) *icimsWalk {
	t.Helper()
	w := newWalk(t, in, top)
	w.g.r.d.Page.Applicant = func(context.Context) (json.RawMessage, error) {
		return json.RawMessage(strings.Replace(icimsApplicant, "%s", country, 1)), nil
	}
	it := &icimsTab{scriptTab: w.tab, here: true, found: true, framed: framed}
	it.onPress = func(t *icimsTab) {
		t.here = false
		t.scriptTab.set(wizardStep(1, 1))
	}
	w.s.tab = it
	return &icimsWalk{wenv: w, it: it}
}

func (w *icimsWalk) withSolver(t *testing.T) {
	w.svc = newTwoCaptcha(t)
	w.g.r.d.Page.Solver = w.svc.solver(gates.RunApplySubmit, gates.SkipNotListed)
}

var submitting = Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true}

func TestApply_ICIMS_EmailStepSubmittedOnceOtherIdentifierFirstRefused(t *testing.T) {
	t.Run("the profile email, once, no password", func(t *testing.T) {
		w := newICIMSWalk(t, icimsStepPage(), false, "United States", submitting)
		res, st := w.walk()
		if st != nil || res.End != EndSubmit {
			t.Fatalf("end %+v stop %+v", res, st)
		}
		if len(w.it.presses) != 1 || w.it.presses[0]["email"] != "alex@example.com" || w.it.presses[0]["eu"] != false {
			t.Fatalf("presses %v", w.it.presses)
		}
		if len(w.tab.typed) != 0 || len(w.tab.keyed) != 0 {
			t.Fatalf("the step typed through the page: %v %v", w.tab.typed, w.tab.keyed)
		}
		if w.g.audits(gates.GateControlPress) == 0 {
			t.Fatal("the Next press was not audited")
		}
	})
	t.Run("a step that returns is not submitted twice", func(t *testing.T) {
		w := newICIMSWalk(t, icimsStepPage(), false, "United States", submitting)
		w.it.onPress = func(*icimsTab) {} // Next did nothing
		res, st := w.walk()
		if st != nil || res.End != EndStalled || !strings.Contains(res.Detail, "still showing") || len(w.it.presses) != 1 {
			t.Fatalf("end %+v stop %+v presses %d", res, st, len(w.it.presses))
		}
	})
	t.Run("the EU box follows the profile country", func(t *testing.T) {
		for country, want := range map[string]bool{"Germany": true, "United Kingdom": true, "gb": true, "United States": false, "": false, "Canada": false} {
			w := newICIMSWalk(t, icimsStepPage(), false, country, submitting)
			if _, st := w.walk(); st != nil || len(w.it.presses) != 1 || w.it.presses[0]["eu"] != want {
				t.Fatalf("%q: stop %+v presses %v, want eu %v", country, st, w.it.presses, want)
			}
		}
	})
	t.Run("another identifier-first sign-in is not the step", func(t *testing.T) {
		w := newICIMSWalk(t, icimsStepPage(), false, "United States", submitting)
		w.it.found = false // no enterEmailForm markup: the gate finder reads nothing
		w.tab.onClick = func(t *scriptTab, c Control) { t.set(wizardStep(1, 1)) }
		res, st := w.walk()
		if st != nil || len(w.it.presses) != 0 || len(w.it.tokens) != 0 {
			t.Fatalf("end %+v stop %+v presses %v", res, st, w.it.presses)
		}
	})
	t.Run("the step needs a profile email", func(t *testing.T) {
		w := newICIMSWalk(t, icimsStepPage(), false, "United States", submitting)
		w.g.r.d.Page.Applicant = func(context.Context) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }
		_, st := w.walk()
		if st == nil || st.class != ClassAnswerGap || len(w.it.presses) != 0 {
			t.Fatalf("stop %+v presses %v", st, w.it.presses)
		}
	})
	t.Run("the account stage reports no login needed", func(t *testing.T) {
		for _, framed := range []bool{false, true} {
			top := icimsStepPage()
			if framed {
				top = icimsFrameWrapper()
			}
			a := newAcct(t, Input{URL: loginURL, Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", nil)
			a.tab.set(top)
			a.s.entry = top
			a.s.tab = &icimsTab{scriptTab: a.tab, here: true, found: true, framed: framed}
			d, st := a.run()
			if st != nil || !strings.Contains(d, "no login needed") || len(a.tab.typed) != 0 || len(a.tab.clicks) != 0 {
				t.Fatalf("framed=%v: detail %q stop %+v", framed, d, st)
			}
		}
	})
}

func TestApply_ICIMS_FramedEmailStepSubmittedOnce(t *testing.T) {
	w := newICIMSWalk(t, icimsFrameWrapper(), true, "United States", submitting)
	res, st := w.walk()
	if st != nil || res.End != EndSubmit {
		t.Fatalf("end %+v stop %+v", res, st)
	}
	if len(w.it.presses) != 1 || w.tab.clicks != nil && len(w.tab.clicks) > 1 {
		t.Fatalf("presses %v clicks %v: a framed step is driven by the page script, once", w.it.presses, w.tab.clicks)
	}
	if len(w.tab.typed) != 0 {
		t.Fatalf("typed through the page: %v", w.tab.typed)
	}
}

func hcaptchaOpen() []CaptchaW {
	return []CaptchaW{{Kind: "hcaptcha", SiteKey: "site-key-4", Visible: true, W: 304, H: 78}}
}

func TestApply_ICIMS_CaptchaOnNextIsTheSolversThenTheUsers(t *testing.T) {
	t.Run("solved at evidence point 4, sent once, Next not pressed again", func(t *testing.T) {
		w := newICIMSWalk(t, icimsStepPage(), false, "United States", submitting)
		w.withSolver(t)
		w.it.onPress = func(t *icimsTab) { t.widgets = hcaptchaOpen() }
		w.it.onSend = func(t *icimsTab) {
			t.here, t.widgets = false, nil
			t.scriptTab.set(wizardStep(1, 1))
		}
		res, st := w.walk()
		if st != nil || res.End != EndSubmit {
			t.Fatalf("end %+v stop %+v lines %v", res, st, w.e.Run.Captcha)
		}
		if len(w.it.presses) != 1 || w.it.sends != 1 || len(w.it.tokens) != 1 || w.it.tokens[0] != "hcaptcha" || w.svc.tasks != 1 {
			t.Fatalf("presses %d sends %d tokens %v tasks %d", len(w.it.presses), w.it.sends, w.it.tokens, w.svc.tasks)
		}
		if got := strings.Join(w.e.Run.Captcha, " "); !strings.Contains(got, gates.PointICIMSEmail) || strings.Contains(got, "tok-one") {
			t.Fatalf("captcha lines %q", got)
		}
	})
	t.Run("a token the tenant refuses is retried once inside the cap", func(t *testing.T) {
		w := newICIMSWalk(t, icimsStepPage(), false, "United States", submitting)
		w.withSolver(t)
		w.it.onPress = func(t *icimsTab) { t.widgets = hcaptchaOpen() }
		res, st := w.walk() // the puzzle stands after every send
		if st != nil || res.End != EndBotChallenge {
			t.Fatalf("end %+v stop %+v", res, st)
		}
		if w.svc.tasks != 2 || w.it.sends != 2 || w.it.blanks == 0 || len(w.it.presses) != 1 {
			t.Fatalf("tasks %d sends %d blanks %d presses %d", w.svc.tasks, w.it.sends, w.it.blanks, len(w.it.presses))
		}
	})
	t.Run("no solver: the walk ends for the user, nothing sent", func(t *testing.T) {
		w := newICIMSWalk(t, icimsStepPage(), false, "United States", submitting)
		w.it.onPress = func(t *icimsTab) { t.widgets = hcaptchaOpen() }
		res, st := w.walk()
		if st != nil || res.End != EndBotChallenge || w.it.sends != 0 || len(w.it.tokens) != 0 {
			t.Fatalf("end %+v stop %+v sends %d", res, st, w.it.sends)
		}
	})
	t.Run("a dry run never reaches the service", func(t *testing.T) {
		w := newICIMSWalk(t, icimsStepPage(), false, "United States", Input{URL: loginURL, Caller: gates.CallerCLI})
		w.svc = newTwoCaptcha(t)
		w.g.r.d.Page.Solver = w.svc.solver(gates.RunDryRun, gates.SkipNotListed)
		w.it.onPress = func(t *icimsTab) { t.widgets = hcaptchaOpen() }
		res, st := w.walk()
		if st != nil || res.End != EndBotChallenge || w.svc.tasks != 0 || w.it.sends != 0 {
			t.Fatalf("end %+v stop %+v tasks %d", res, st, w.svc.tasks)
		}
	})
	t.Run("a puzzle already open on arrival is left alone", func(t *testing.T) {
		w := newICIMSWalk(t, icimsStepPage(), false, "United States", submitting)
		w.withSolver(t)
		w.it.widgets = hcaptchaOpen()
		res, st := w.walk()
		if st != nil || res.End != EndBotChallenge || len(w.it.presses) != 0 || w.svc.tasks != 0 {
			t.Fatalf("end %+v stop %+v presses %d", res, st, len(w.it.presses))
		}
	})
	t.Run("a dormant or invisible widget is not a puzzle", func(t *testing.T) {
		if d := icimsChallenge(icimsState{Widgets: []CaptchaW{{Kind: "hcaptcha", Visible: false}, {Kind: "hcaptcha", Visible: true, Invisible: true}}}); d != nil {
			t.Fatalf("read %+v", d)
		}
		open := icimsState{Widgets: []CaptchaW{{Kind: "hcaptcha", Visible: true, Invisible: true, BframeOpen: true}}}
		if d := icimsChallenge(open); d == nil || d.ChallengeOnly {
			t.Fatalf("an invisible widget with its puzzle open reads %+v", d)
		}
	})
}

func TestApply_CombinedForm_CaptchaStandsNothingTyped(t *testing.T) {
	svc := newTwoCaptcha(t)
	combined := func(captcha bool) *PageState {
		p := wizardStep(1, 2)
		p.Inputs = append(p.Inputs,
			InputInfo{I: 2, Tag: "input", Type: "email", Label: "Email", Form: 0},
			InputInfo{I: 3, Tag: "input", Type: "password", Label: "Password", Form: 0},
			InputInfo{I: 4, Tag: "input", Type: "text", Label: "Address", Form: 0},
			InputInfo{I: 5, Tag: "input", Type: "file", Label: "Resume", Form: 0})
		if captcha {
			p.Captcha = []CaptchaW{{Kind: "recaptcha_v2", SiteKey: "site-key-5", Visible: true, W: 304, H: 78}}
			p.Resp = []RespField{{Name: "g-recaptcha-response", Empty: true}}
		}
		return p
	}
	if !combinedAccountForm(combined(true)) || combinedAccountForm(wizardStep(1, 2)) || combinedAccountForm(loginPage("")) {
		t.Fatal("only a password beside application fields is a combined form")
	}
	w := newWalk(t, submitting, combined(false))
	w.g.r.d.Page.Solver = svc.solver(gates.RunApplySubmit, gates.SkipNotListed)
	nexts := 0
	w.tab.onClick = func(t *scriptTab, c Control) {
		if c.Labels[0] == "Next" {
			nexts++
			t.set(combined(true)) // the Next raised a checkbox puzzle and the page stayed put
		}
	}
	res, st := w.walk()
	if st == nil || st.class != ClassVerification || st.board != "challenge" {
		t.Fatalf("the puzzle is the user's bot check: end %+v stop %+v", res, st)
	}
	if svc.tasks != 0 || len(w.tab.delivered) != 0 || len(w.tab.typed) != 0 || len(w.tab.keyed) != 0 {
		t.Fatalf("tasks %d delivered %v typed %v: the page is the user's", svc.tasks, w.tab.delivered, w.tab.typed)
	}
	if got := strings.Join(w.e.Run.Captcha, " "); !strings.Contains(got, "combined") {
		t.Fatalf("captcha lines %q", got)
	}
}
