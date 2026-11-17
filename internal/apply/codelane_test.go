package apply

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/engine"
	"applysmith/internal/gmail"
)

type fakeCodes struct {
	mu     sync.Mutex
	since  []time.Time
	domain []string
	// account is the mailbox the lane reads; grants records the bound a demo
	// grant carried on each lookup ("domain|since", empty without a grant).
	account string
	grants  []string
	// replies are returned in order; the last repeats.
	replies []codeReply
}

type codeReply struct {
	res gmail.OTPResult
	err error
}

func (f *fakeCodes) LatestCode(ctx context.Context, since time.Time, domain string) (gmail.OTPResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.since = append(f.since, since)
	f.domain = append(f.domain, domain)
	g := ""
	if d, s, ok := gmail.CodeGrantFrom(ctx); ok {
		g = d + "|" + s.UTC().Format(time.RFC3339Nano)
	}
	f.grants = append(f.grants, g)
	r := f.replies[0]
	if len(f.replies) > 1 {
		f.replies = f.replies[1:]
	}
	return r.res, r.err
}

// Mailbox makes the fake a Mailboxed lane.
func (f *fakeCodes) Mailbox(context.Context) string { return f.account }

func notYet() codeReply {
	return codeReply{err: &gmail.LookupError{Kind: gmail.KindNotFound, Message: "no code yet"}}
}

func mailed(code, from, subject string, at time.Time) codeReply {
	return codeReply{res: gmail.OTPResult{Code: code, From: from, Subject: subject, ReceivedAt: at}}
}

var fastCode = CodeTiming{Poll: 5 * time.Millisecond, GiveUp: 200 * time.Millisecond, Settle: time.Millisecond, Verify: 50 * time.Millisecond}

func TestApply_CodeLane_WaitBoundsAndFailureIsNotNoCode(t *testing.T) {
	fc := &fakeCodes{replies: []codeReply{notYet(), notYet(), mailed("424242", "x@example.test", "s", time.Now())}}
	var l codeLane
	if code, _, err := l.Lookup(context.Background(), fc, fastCode, time.Now, time.Time{}, "", nil); err != nil || code != "424242" || len(fc.since) != 3 {
		t.Fatalf("code %q err %v after %d lookups: no code yet is polled again", code, err, len(fc.since))
	}
	if !fc.since[0].Before(time.Now().Add(-9 * time.Minute)) {
		t.Fatalf("with no press anchor the lookback is 10 minutes: %s", fc.since[0])
	}
	expired := &fakeCodes{replies: []codeReply{{err: &gmail.LookupError{Kind: gmail.KindUnavailable, Reason: gmail.ReasonNotSignedIn, Message: gmail.ErrNotConnected.Error()}}}}
	var l2 codeLane
	_, _, err := l2.Lookup(context.Background(), expired, fastCode, time.Now, time.Time{}, "", nil)
	var cf *CodeFailure
	if !errors.As(err, &cf) || len(expired.since) != 1 || !strings.Contains(cf.Message, "connect it") {
		t.Fatalf("err %v after %d lookups: a sign-in failure is a failure with the instruction to connect, not 'no code'", err, len(expired.since))
	}
	// The host's record names the missing Gmail connection, the stop's fix;
	// a Gmail that is only unreachable names nothing.
	e := &Exec{Run: &Run{}}
	if e.noMailbox("careers.example.test", &CodeFailure{Reason: gmail.ReasonUnreachable, Message: "Gmail could not be reached"}); len(e.Run.Signins) != 0 {
		t.Fatalf("an unreachable Gmail marked %+v", e.Run.Signins)
	}
	if e.noMailbox("careers.example.test", err); len(e.Run.Signins) != 1 || e.Run.Signins[0].Mail != MailNotConnected {
		t.Fatalf("records %+v", e.Run.Signins)
	}
}

func TestApply_CodeLane_SegmentedFormRefusalsAndMailLeftUnused(t *testing.T) {
	if b, why := codeBoxes(segPage(6)); len(b) != 6 || why != "" {
		t.Fatalf("six boxes: %d %q", len(b), why)
	}
	if b, _ := codeBoxes(segPage(3)); len(b) != 0 {
		t.Fatal("three boxes read as a code form")
	}
	// Eightfold numbers its boxes in their labels and sets no maxlength.
	numbered := &PageState{URL: "https://careers.example.test/apply"}
	for i := 0; i < 6; i++ {
		numbered.Inputs = append(numbered.Inputs, InputInfo{I: i, Type: "text", Label: fmt.Sprintf("Please enter OTP character %d", i+1), Form: -1, Modal: true})
	}
	if b, why := codeBoxes(numbered); len(b) != 6 || why != "" {
		t.Fatalf("six numbered boxes: %d %q", len(b), why)
	}
	withPassword := segPage(6)
	withPassword.Inputs = append(withPassword.Inputs, InputInfo{I: 9, Type: "password", Label: "Password"})
	withText := segPage(6)
	withText.Inputs = append(withText.Inputs, InputInfo{I: 9, Type: "text", Label: "Full name"})
	withCaptcha := segPage(6)
	withCaptcha.Captcha = []CaptchaW{{Kind: "recaptcha_v2", Visible: true, W: 300, H: 78}}
	for name, st := range map[string]*PageState{"password": withPassword, "non-code text": withText, "captcha": withCaptcha} {
		if b, why := codeBoxes(st); len(b) != 0 || why == "" {
			t.Errorf("%s: boxes %d reason %q", name, len(b), why)
		}
	}
	// Six boxes and an eight character code: nothing is typed and the mail is not consumed.
	fc := &fakeCodes{replies: []codeReply{mailed("12345678", "x@example.test", "s", time.Now())}}
	var l codeLane
	_, _, err := l.Lookup(context.Background(), fc, fastCode, time.Now, time.Time{}, "", fitsBoxes(6))
	if !errors.Is(err, ErrCodeBoxes) || !l.given.IsZero() {
		t.Fatalf("err %v given %v: a code that does not fit does not consume the mail", err, l.given)
	}
	tab := newScriptTab(segPage(6))
	b6, _ := codeBoxes(segPage(6))
	if err := typeCode(context.Background(), tab, b6, "12345678"); !errors.Is(err, ErrCodeBoxes) || len(tab.typed) != 0 {
		t.Fatalf("typed %v err %v", tab.typed, err)
	}
	if err := typeCode(context.Background(), tab, b6, "123456"); err != nil || tab.typed[0] != "1" || tab.typed[5] != "6" {
		t.Fatalf("typed %v err %v", tab.typed, err)
	}
	one := newScriptTab(segPage(1))
	b1 := []*InputInfo{{I: 0}}
	if err := typeCode(context.Background(), one, b1, "A1B2C3D4"); err != nil || one.typed[0] != "A1B2C3D4" {
		t.Fatalf("one box takes the whole code: %v %v", one.typed, err)
	}
}

func TestApply_CodeLane_GreenhouseBoxBeforeSubmitLeftToUser(t *testing.T) {
	fc := &fakeCodes{replies: []codeReply{mailed("ABCD1234", mailFrom("x", "greenhouse-mail.io"), "s", time.Now())}}
	x := newSubmit(t, func(d *Deps) { d.Page.Codes, d.Page.Code = fc, fastCode })
	x.e.Run.ATS = ATSGreenhouse
	st := &PageState{URL: "https://job-boards.greenhouse.io/x", Inputs: []InputInfo{{I: 0, Type: "text", Label: "Security code", Form: 0}}}
	ps := &pageState{leftovers: []Leftover{{Label: "Security code", Reason: ReasonCodeBox}}}
	x.s.fillCodeBoxes(context.Background(), st, ps, time.Time{})
	if len(fc.since) != 0 || len(x.tab.typed) != 0 || len(ps.leftovers) != 1 {
		t.Fatalf("lookups %d typed %v leftovers %d: a code box before any Submit is the user's", len(fc.since), x.tab.typed, len(ps.leftovers))
	}
	// On another system the lane types the code and the row is no longer a leftover.
	x.e.Run.ATS = ATSGeneric
	x.e.Run.Input.URL = "https://careers.example.test/apply"
	x.tab.set(st)
	x.s.fillCodeBoxes(context.Background(), st, ps, time.Time{})
	if x.tab.typed[0] != "ABCD1234" || len(ps.leftovers) != 0 {
		t.Fatalf("typed %v leftovers %d", x.tab.typed, len(ps.leftovers))
	}
	if strings.Contains(strings.Join(ps.report.Notes, " "), "ABCD1234") {
		t.Fatal("the code reached the page report")
	}
}

func TestApply_Greenhouse_SecurityCodeRetryOncePerRun(t *testing.T) {
	refusal := post(428, `{"code":"captcha-failed","security_code_recipient":"a***@example.com"}`)
	run := func(t *testing.T, secondRefused bool) (*senv, *fakeCodes, *stop) {
		fc := &fakeCodes{replies: []codeReply{mailed("ABCD1234", mailFrom("no-reply", "greenhouse-mail.io"), "Security code for your application", time.Now().Add(time.Minute))}}
		x := newSubmit(t, func(d *Deps) { d.Page.Codes, d.Page.Code = fc, fastCode })
		x.e.Run.ATS = ATSGreenhouse
		x.e.Run.Pressed = false
		presses := 0
		x.tab.onClick = func(t *scriptTab, _ Control) {
			presses++
			switch {
			case presses == 1:
				t.watch.add(refusal)
				f := formState()
				f.Inputs = append(f.Inputs, InputInfo{I: 5, Type: "text", Label: "Security code", Name: "security_code_0", Form: 0})
				t.set(f)
			case secondRefused:
				t.watch.add(refusal)
			default:
				t.watch.add(post(200, "ok"))
				t.set(thanks())
			}
		}
		return x, fc, x.submit()
	}
	x, fc, st := run(t, false)
	if st != nil || x.e.Run.Outcome.Kind != OutcomeSubmitted {
		t.Fatalf("stop %+v outcome %+v", st, x.e.Run.Outcome)
	}
	if x.tab.clicksOf("Submit application") != 2 || x.tab.typed[5] != "ABCD1234" || fc.domain[0] != "greenhouse-mail.io" {
		t.Fatalf("presses %d typed %v domain %v", x.tab.clicksOf("Submit application"), x.tab.typed, fc.domain)
	}
	if n := x.g.count(`SELECT count(*) FROM submissions`); n != 1 {
		t.Fatalf("%d claim rows: one claim covers the retry", n)
	}
	x, _, st = run(t, true)
	if st == nil || !strings.Contains(st.reason, "read the emailed security code and retried once: the retry was refused: HTTP 428") {
		t.Fatalf("stop %+v", st)
	}
	if x.tab.clicksOf("Submit application") != 2 {
		t.Fatalf("%d presses: a third press is never made", x.tab.clicksOf("Submit application"))
	}
	if state, _, _ := ledgerState(x.g); state != "failed" {
		t.Fatalf("ledger %s", state)
	}
}

// Node's security-code-retry dom-walk test: a code the fill already typed
// into the box is submitted again without a second mail lookup.
func TestApply_Greenhouse_SecurityCodeAlreadyTypedIsResubmittedWithoutALookup(t *testing.T) {
	fc := &fakeCodes{}
	x := newSubmit(t, func(d *Deps) { d.Page.Codes, d.Page.Code = fc, fastCode })
	x.e.Run.ATS = ATSGreenhouse
	presses := 0
	x.tab.onClick = func(t *scriptTab, _ Control) {
		presses++
		if presses == 1 {
			t.watch.add(post(428, `{"code":"captcha-failed","security_code_recipient":"a***@example.com"}`))
			f := formState()
			f.Inputs = append(f.Inputs, InputInfo{I: 5, Type: "text", Label: "Security code", Name: "security_code_0", Filled: true, Form: 0})
			t.set(f)
			return
		}
		t.watch.add(post(200, "ok"))
		t.set(thanks())
	}
	if st := x.submit(); st != nil || x.e.Run.Outcome.Kind != OutcomeSubmitted {
		t.Fatalf("stop %+v outcome %+v", st, x.e.Run.Outcome)
	}
	if x.tab.clicksOf("Submit application") != 2 || len(fc.since) != 0 || len(x.tab.typed) != 0 {
		t.Fatalf("presses %d lookups %d typed %v", x.tab.clicksOf("Submit application"), len(fc.since), x.tab.typed)
	}
}

func segPage(n int) *PageState {
	st := &PageState{URL: "https://careers.example.test/verify"}
	for i := 0; i < n; i++ {
		st.Inputs = append(st.Inputs, InputInfo{I: i, Type: "text", Label: "Digit", Name: "otp", MaxLength: 1, Form: -1})
	}
	return st
}

func codeLeftovers(n int) []Leftover {
	var out []Leftover
	for i := 1; i <= n; i++ {
		out = append(out, Leftover{Selector: fmt.Sprintf("#d%d", i), Label: fmt.Sprintf("Enter verification code digit %d of six.", i), Reason: ReasonCodeBox})
	}
	return out
}

func TestChain_OtpLane_RowsNeverReachBankOrModel(t *testing.T) {
	m := &fakeMatcher{}
	l := newLadderEnv(t, ladderOwn, script("[]", "[]", "[]"), func(d *Deps) { d.Page.Matcher = m })
	var rows []engine.Row
	for i, lo := range codeLeftovers(6) {
		outcome := "skipped"
		if i >= 3 {
			outcome = "abstained"
		}
		rows = append(rows, engine.Row{Selector: lo.Selector, Label: lo.Label, Required: true, Outcome: outcome, MatcherReason: "no-entry", Kind: "text"})
	}
	ps := l.run(rows...)
	l.s.fix(context.Background(), doneResult(rows...), ps)
	if len(m.reqs) != 0 || len(l.model.calls) != 0 {
		t.Fatalf("%d bank requests and %d model calls: a single-use code is not a curated fact", len(m.reqs), len(l.model.calls))
	}
	if len(ps.leftovers) != 6 {
		t.Fatalf("%d leftovers, want the six code rows", len(ps.leftovers))
	}
	for _, lo := range ps.leftovers {
		if lo.Reason != ReasonCodeBox || lo.Draft != nil {
			t.Fatalf("leftover %+v: a code box is the code lane's, with no answer-bank draft", lo)
		}
	}
}

func TestChain_OtpLane_OneReadSeriesPerPage(t *testing.T) {
	fc := &fakeCodes{replies: []codeReply{mailed("123456", mailFrom("x", "example.com"), "s", time.Now())}}
	x := newSubmit(t, func(d *Deps) { d.Page.Codes, d.Page.Code = fc, fastCode })
	st := segPage(6)
	x.tab.set(st)
	ps := &pageState{leftovers: codeLeftovers(6)}
	x.s.fillCodeBoxes(context.Background(), st, ps, time.Time{})
	if len(fc.since) != 1 {
		t.Fatalf("%d mail reads for six boxes, want one series", len(fc.since))
	}
	if len(x.tab.typed) != 6 || x.tab.typed[0] != "1" || x.tab.typed[5] != "6" || len(ps.leftovers) != 0 {
		t.Fatalf("typed %v leftovers %d", x.tab.typed, len(ps.leftovers))
	}
}

func TestChain_OtpLane_SettleThenVerify(t *testing.T) {
	settle := 60 * time.Millisecond
	run := func(t *testing.T, tune func(*scriptTab)) (*pageState, time.Duration) {
		fc := &fakeCodes{replies: []codeReply{mailed("123456", mailFrom("x", "example.com"), "s", time.Now())}}
		x := newSubmit(t, func(d *Deps) {
			d.Page.Codes, d.Page.Code = fc, CodeTiming{Poll: 5 * time.Millisecond, GiveUp: 200 * time.Millisecond, Settle: settle}
		})
		st := segPage(6)
		x.tab.set(st)
		tune(x.tab)
		ps := &pageState{leftovers: codeLeftovers(6)}
		start := time.Now()
		x.s.fillCodeBoxes(context.Background(), st, ps, time.Time{})
		return ps, time.Since(start)
	}
	t.Run("a box the page kept is answered", func(t *testing.T) {
		ps, took := run(t, func(*scriptTab) {})
		if len(ps.leftovers) != 0 || took < settle {
			t.Fatalf("leftovers %d after %s: the lane settles for %s before it re-reads", len(ps.leftovers), took, settle)
		}
	})
	t.Run("a box the page did not keep is a leftover", func(t *testing.T) {
		ps, _ := run(t, func(tb *scriptTab) { tb.dropWrites = true })
		if len(ps.leftovers) != 6 || ps.leftovers[0].Detail != "write did not land" || len(ps.report.Unresolved) != 6 {
			t.Fatalf("leftovers %+v unresolved %v", ps.leftovers, ps.report.Unresolved)
		}
	})
	t.Run("a vanished control is unreadable", func(t *testing.T) {
		ps, _ := run(t, func(tb *scriptTab) { tb.script(&PageState{URL: "https://careers.example.test/verify"}) })
		if len(ps.leftovers) != 6 || ps.leftovers[0].Detail != "unreadable-control" {
			t.Fatalf("leftovers %+v", ps.leftovers)
		}
	})
}

func TestChain_OtpSenderDomain_ExactOrSubdomain(t *testing.T) {
	for host, want := range map[string]string{
		"adp.com": SenderADP, "workforcenow.adp.com": SenderADP, "WorkforceNow.ADP.com": SenderADP,
		"job-boards.greenhouse.io": SenderGreenhouse, "greenhouse.io": SenderGreenhouse,
		"acme.wd5.myworkdayjobs.com": SenderWorkday, "wd3.myworkday.com": SenderWorkday, "acme.myworkdaysite.com": SenderWorkday,
		"notadp.com": "", "myadp.com": "", "adp.com.example.test": "", "evilgreenhouse.io": "", "myworkdayjobs.com.example.test": "",
		"careers.example.test": "", "": "",
	} {
		if got := SenderDomainFor(host); got != want {
			t.Errorf("SenderDomainFor(%q) = %q, want %q", host, got, want)
		}
	}
	x := newSubmit(t, nil)
	x.e.Run.Input.URL = "https://workforcenow.adp.com/careers"
	if got := x.s.codeDomain(); got != SenderADP {
		t.Fatalf("the lane bounds an ADP page to %q", got)
	}
	x.e.Run.Input.URL = "https://careers.example.test/apply"
	if got := x.s.codeDomain(); got != "" {
		t.Fatalf("the lane bounds an unknown site to %q", got)
	}
}

// hungCodes never answers until its context ends.
type hungCodes struct{}

func (hungCodes) LatestCode(ctx context.Context, _ time.Time, _ string) (gmail.OTPResult, error) {
	<-ctx.Done()
	return gmail.OTPResult{}, ctx.Err()
}

func TestChain_OtpLookup_InProcessBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	var l codeLane
	start := time.Now()
	_, _, err := l.Lookup(ctx, hungCodes{}, CodeTiming{Poll: 5 * time.Millisecond, GiveUp: time.Minute}, time.Now, time.Time{}, "", nil)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("err %v after %s: the run context bounds the in-process lookup", err, time.Since(start))
	}
	var cf *CodeFailure
	if errors.As(err, &cf) {
		t.Fatal("a cancelled run is not a lookup failure")
	}
}

func TestChain_OtpLane_StaleAnchorNeverWidensTheLookback(t *testing.T) {
	now := time.Date(2027, 3, 10, 12, 0, 0, 0, time.UTC)
	fc := &fakeCodes{replies: []codeReply{mailed("123456", mailFrom("x", "example.com"), "s", now)}}
	var l codeLane
	stale := now.Add(-3 * time.Hour)
	if _, _, err := l.Lookup(context.Background(), fc, fastCode, func() time.Time { return now }, CodeAnchor(stale), "", nil); err != nil {
		t.Fatal(err)
	}
	if !fc.since[0].Equal(now.Add(-10 * time.Minute)) {
		t.Fatalf("a press three hours ago asked since %s: the window never exceeds the code's lifetime", fc.since[0])
	}
	fresh := &fakeCodes{replies: []codeReply{mailed("123456", mailFrom("x", "example.com"), "s", now)}}
	var l2 codeLane
	press := now.Add(-time.Minute)
	if _, _, err := l2.Lookup(context.Background(), fresh, fastCode, func() time.Time { return now }, CodeAnchor(press), "", nil); err != nil {
		t.Fatal(err)
	}
	if !fresh.since[0].Equal(press.Add(-time.Second)) {
		t.Fatalf("a press a minute ago asked since %s", fresh.since[0])
	}
}

func TestChain_OtpLane_BoxCountMustMatch(t *testing.T) {
	fc := &fakeCodes{replies: []codeReply{mailed("12345678", mailFrom("x", "example.com"), "s", time.Now())}}
	x := newSubmit(t, func(d *Deps) { d.Page.Codes, d.Page.Code = fc, fastCode })
	st := segPage(6)
	x.tab.set(st)
	ps := &pageState{leftovers: codeLeftovers(6)}
	x.s.fillCodeBoxes(context.Background(), st, ps, time.Time{})
	if len(x.tab.typed) != 0 || len(ps.leftovers) != 6 {
		t.Fatalf("typed %v leftovers %d: nothing is written when the count differs", x.tab.typed, len(ps.leftovers))
	}
	notes := strings.Join(ps.report.Notes, " ")
	if !strings.Contains(notes, "8 characters") || !strings.Contains(notes, "6 boxes") || strings.Contains(notes, "12345678") {
		t.Fatalf("notes %q: the reason names both numbers and never the code", notes)
	}
	b6, _ := codeBoxes(st)
	err := typeCode(context.Background(), newScriptTab(st), b6, "12345678")
	if !errors.Is(err, ErrCodeBoxes) || !strings.Contains(err.Error(), "6 boxes") || !strings.Contains(err.Error(), "8 characters") {
		t.Fatalf("err %v", err)
	}
}

func TestChain_OtpLane_EvidenceMasksDigits(t *testing.T) {
	at := time.Date(2027, 3, 10, 10, 15, 2, 0, time.UTC)
	if got := maskDigits("Code 482916 for order 12345 ref 987 and 12-03"); got != "Code ****** for order ***** ref 987 and 12-03" {
		t.Fatalf("masked %q: every run of four or more digits, no shorter run", got)
	}
	fc := &fakeCodes{replies: []codeReply{mailed("482916", "Security <no-reply@mail.example.com>", "Your code is 482916", at)}}
	var l codeLane
	_, ev, err := l.Lookup(context.Background(), fc, fastCode, time.Now, time.Time{}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Sender != "mail.example.com" || ev.Subject != "Your code is ******" || !ev.At.Equal(at) {
		t.Fatalf("evidence %+v: sender and time stay, digits are masked", ev)
	}
	x := newSubmit(t, func(d *Deps) { d.Page.Codes, d.Page.Code = fc, fastCode })
	fc.replies = []codeReply{mailed("482916", "Security <no-reply@mail.example.com>", "Your code is 482916", at)}
	st := segPage(6)
	x.tab.set(st)
	ps := &pageState{leftovers: codeLeftovers(6)}
	x.s.fillCodeBoxes(context.Background(), st, ps, time.Time{})
	record := strings.Join(ps.report.Notes, " ") + strings.Join(ps.report.Unresolved, " ")
	if strings.Contains(record, "482916") || !strings.Contains(record, "10:15:02") {
		t.Fatalf("page report %q: no digits of the code, the timestamp intact", record)
	}
}

func TestChain_OtpLane_TransportFailureNotNoCode(t *testing.T) {
	down := &fakeCodes{replies: []codeReply{{err: errors.New("connection refused")}}}
	var l codeLane
	_, _, err := l.Lookup(context.Background(), down, fastCode, time.Now, time.Time{}, "", nil)
	var cf *CodeFailure
	if !errors.As(err, &cf) || errors.Is(err, errNoCode) || len(down.since) != 1 {
		t.Fatalf("err %v after %d lookups: a transport failure is a failure and is not polled again", err, len(down.since))
	}
	x := newSubmit(t, func(d *Deps) { d.Page.Codes, d.Page.Code = down, fastCode })
	st := segPage(6)
	x.tab.set(st)
	ps := &pageState{leftovers: codeLeftovers(6)}
	x.s.fillCodeBoxes(context.Background(), st, ps, time.Time{})
	notes := strings.Join(ps.report.Notes, " ")
	if !strings.Contains(notes, "lookup failed") || strings.Contains(notes, "no verification code arrived") {
		t.Fatalf("notes %q: reported as a failure, never as no code", notes)
	}
}

func TestChain_OtpLookup_EachMailOncePerRun(t *testing.T) {
	first := time.Date(2027, 3, 10, 10, 0, 0, 0, time.UTC)
	fc := &fakeCodes{replies: []codeReply{mailed("111111", mailFrom("x", "example.com"), "c", first), notYet()}}
	x := newSubmit(t, func(d *Deps) { d.Page.Codes, d.Page.Code = fc, fastCode })
	// The walker's consumer takes the code first, then the check lane asks.
	if _, _, err := x.e.codeLane().Lookup(context.Background(), fc, fastCode, time.Now, first.Add(-time.Minute), "", fitsBoxes(6)); err != nil {
		t.Fatal(err)
	}
	st := segPage(6)
	x.tab.set(st)
	ps := &pageState{leftovers: codeLeftovers(6)}
	x.s.fillCodeBoxes(context.Background(), st, ps, time.Time{})
	if len(x.tab.typed) != 0 || !fc.since[len(fc.since)-1].Equal(first) {
		t.Fatalf("typed %v lookups since %v: the lane asks only for mail after the one already given", x.tab.typed, fc.since)
	}
	// A consumer that reports the code does not fit leaves the mail for one that does.
	long := &fakeCodes{replies: []codeReply{mailed("12345678", mailFrom("x", "example.com"), "c", first)}}
	var l codeLane
	if _, _, err := l.Lookup(context.Background(), long, fastCode, time.Now, first.Add(-time.Minute), "", fitsBoxes(6)); !errors.Is(err, ErrCodeBoxes) {
		t.Fatalf("err %v", err)
	}
	code, _, err := l.Lookup(context.Background(), long, fastCode, time.Now, first.Add(-time.Minute), "", fitsBoxes(8))
	if err != nil || code != "12345678" || !long.since[1].Equal(first.Add(-time.Minute)) {
		t.Fatalf("code %q err %v since %v: the unfit mail stays the latest", code, err, long.since)
	}
}

// With a mailbox to read, ByteDance's code-only Create account is completed:
// the profile's email, Get code once it is enabled, the code mailed after that
// press, Continue; the page after it is the new account's. A code that never
// comes stops as the user's code step with nothing pressed after Get code.
func TestApply_Signup_ACodeOnlyFormTakesTheCodeMailedAfterGetCode(t *testing.T) {
	codePage := func(sendDisabled bool) *PageState {
		bd := loginPage("")
		bd.Inputs[1].Autocomplete = "new-password"
		p := &PageState{URL: loginURL, Title: "Create account", Text: "Create account", Lines: []string{"Create account"},
			Inputs: []InputInfo{
				{I: 10, Tag: "input", Type: "text", Label: "Email", Placeholder: "Email", ID: "email", Form: -1},
				{I: 11, Tag: "input", Type: "text", Label: "Enter Email Verification Code", Placeholder: "Enter Email Verification Code", ID: "validCode", Form: -1},
			},
			Controls: []Control{{I: 12, Tag: "button", Labels: []string{"Get code"}, Disabled: sendDisabled, Form: -1}, {I: 13, Tag: "button", Labels: []string{"Continue"}, Form: -1}},
		}
		p.Inputs = append(p.Inputs, bd.Inputs...)
		return p
	}
	run := func(t *testing.T, reply codeReply) (*acctEnv, *fakeCodes, string, *stop) {
		fc := &fakeCodes{replies: []codeReply{reply}}
		a, _ := sfRig(t, "United States", codePage(true), false, func(d *Deps) { d.Page.Codes, d.Page.Code = fc, fastCode })
		bd := loginPage("")
		bd.Inputs[1].Autocomplete = "new-password"
		bd.Controls = append(bd.Controls, Control{I: 4, Tag: "button", Labels: []string{"Forgot password?"}, Form: -1})
		a.tab.set(bd)
		a.s.entry = bd
		signin := a.tab.onClick
		a.tab.onClick = func(tab *scriptTab, c Control) {
			switch c.Labels[0] {
			case "Get code":
			case "Continue":
				tab.set(appPage())
			case "Create account":
				// Get code turns enabled a few looks after the form shows.
				tab.set(codePage(true))
				tab.script(codePage(true), codePage(true), codePage(true), codePage(false))
			default:
				signin(tab, c)
			}
		}
		d, st := a.run()
		return a, fc, d, st
	}
	a, fc, d, st := run(t, mailed("482916", mailFrom("no-reply", "bytedance.com"), "Verification code", t0.Add(time.Minute)))
	if st != nil || !strings.Contains(d, "created an account") {
		t.Fatalf("detail %q stop %+v", d, st)
	}
	if a.tab.typed[10] != "alex@example.com" || a.tab.typed[11] != "482916" {
		t.Fatalf("typed %v: the email, then the code", a.tab.typed)
	}
	if got := strings.Join(a.tab.clicks, ","); !strings.HasSuffix(got, "Create account,Get code,Continue") {
		t.Fatalf("pressed %s", got)
	}
	if len(fc.since) == 0 || strings.Contains(stringify(a.e.Run), "482916") {
		t.Fatalf("lookups %v; the code is never recorded", fc.since)
	}
	a, _, _, st = run(t, notYet())
	if st == nil || st.board != "one-time-code" || strings.HasSuffix(strings.Join(a.tab.clicks, ","), "Continue") {
		t.Fatalf("stop %+v pressed %v", st, a.tab.clicks)
	}
}
