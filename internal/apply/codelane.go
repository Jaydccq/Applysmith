package apply

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/gmail"
	"applysmith/internal/lazyregexp"
)

// Codes is the in-process lookup behind GET /api/gmail/otp/latest: the newest
// verification code received strictly after a time, bounded to a sender
// domain where the site's sender is known.
type Codes interface {
	LatestCode(ctx context.Context, since time.Time, fromDomain string) (gmail.OTPResult, error)
}

// Mailboxed is a Codes lane that knows which mailbox it reads. A demo
// workspace reads a code only when the persona's address is an alias of it.
type Mailboxed interface {
	Mailbox(ctx context.Context) string
}

// CodeTiming holds the code lane's clocks; a zero field takes the specified
// value.
type CodeTiming struct {
	Poll, GiveUp, Settle, Lookback, Verify, Close time.Duration
	// Resend is how long a pane's Resend control is waited for after the first
	// code failed: the site's countdown.
	Resend time.Duration
}

func (t CodeTiming) withDefaults() CodeTiming {
	set := func(d *time.Duration, v time.Duration) {
		if *d == 0 {
			*d = v
		}
	}
	set(&t.Poll, 1500*time.Millisecond)
	set(&t.GiveUp, 180*time.Second)
	set(&t.Settle, 500*time.Millisecond)
	set(&t.Lookback, 10*time.Minute)
	set(&t.Verify, 3*time.Second)
	set(&t.Close, 10*time.Second)
	set(&t.Resend, 90*time.Second)
	return t
}

// Sender domains the code lookup is bounded to.
const (
	SenderADP        = "adp.com"
	SenderGreenhouse = "greenhouse-mail.io"
	SenderWorkday    = "otp.workday.com"
)

// codeSenders pairs the hosts whose code sender is known with that sender. A
// host matches by equality or as a subdomain, never as a bare suffix.
var codeSenders = []struct{ host, sender string }{
	{"adp.com", SenderADP},
	{"greenhouse.io", SenderGreenhouse},
	{"myworkdayjobs.com", SenderWorkday},
	{"myworkdaysite.com", SenderWorkday},
	{"myworkday.com", SenderWorkday},
}

// SenderDomainFor is the mail domain the verification codes of a site come
// from, or "" when its sender is not known and the lookup takes any sender.
func SenderDomainFor(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	for _, c := range codeSenders {
		if hostIs(h, c.host) {
			return c.sender
		}
	}
	return ""
}

// CodeAnchor is where a lookup for the mail caused by a press starts: one
// second before the press, because mail timestamps have whole-second
// resolution and a code produced by the press would otherwise read as older.
func CodeAnchor(pressed time.Time) time.Time { return pressed.Add(-time.Second) }

// ErrCodeBoxes is a code that does not fit the boxes about to take it.
var ErrCodeBoxes = errors.New("the number of boxes differs from the code's length")

// CodeFailure is a lookup that failed, as opposed to no code having come: a
// mailbox that cannot be signed in to is reported with its fix and the lane
// does not keep polling.
type CodeFailure struct{ Reason, Message string }

func (e *CodeFailure) Error() string { return "the verification-code lookup failed: " + e.Message }

// mailNotConnected reports a mail lookup that failed because Gmail is not
// connected or refuses the stored app password.
func mailNotConnected(err error) bool {
	var cf *CodeFailure
	var le *gmail.LookupError
	return errors.As(err, &cf) && cf.Reason == gmail.ReasonNotSignedIn || errors.As(err, &le) && le.Reason == gmail.ReasonNotSignedIn
}

// noMailbox marks the host's sign-in record when a mail lookup there failed
// because Gmail is not connected: the stop's fix is the Gmail connection.
func (e *Exec) noMailbox(host string, err error) {
	if mailNotConnected(err) {
		e.Run.Signins[e.signinIndex(host)].Mail = MailNotConnected
	}
}

// CodeEvidence is what the lane records about a mail: sender, subject and time
// with every run of four or more digits masked. The code itself is never
// recorded.
type CodeEvidence struct {
	Sender, Subject string
	At              time.Time
	// Length is the number of characters of the mailed code, so a refusal can
	// name it without recording the code.
	Length int
}

var digitsRE = lazyregexp.New(`\d{4,}`)

func maskDigits(s string) string {
	return digitsRE.ReplaceAllStringFunc(s, func(d string) string { return strings.Repeat("*", len(d)) })
}

// codeLane is the run's one code lookup: a code mail is handed out at most
// once per run, to either consumer.
type codeLane struct {
	mu    sync.Mutex
	given time.Time
}

// spent reports whether a code mail was handed out in this run.
func (c *codeLane) spent() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.given.IsZero()
}

// Lookup waits for the newest code received strictly after the anchor (and
// after any mail already handed out), polling until the give-up time. fits
// reports whether the consumer can take this code; a code that does not fit is
// returned with ErrCodeBoxes and is not consumed, so another consumer may use
// it.
func (c *codeLane) Lookup(ctx context.Context, codes Codes, tm CodeTiming, now func() time.Time, anchor time.Time, domain string, fits func(string) bool) (string, CodeEvidence, error) {
	tm = tm.withDefaults()
	// An older anchor than the code's lifetime would hand out a mail the sender
	// has already expired.
	if floor := now().Add(-tm.Lookback); anchor.IsZero() || anchor.Before(floor) {
		anchor = floor
	}
	deadline := time.Now().Add(tm.GiveUp)
	for {
		c.mu.Lock()
		since := anchor
		if c.given.After(since) {
			since = c.given
		}
		c.mu.Unlock()
		res, err := codes.LatestCode(ctx, since, domain)
		switch {
		case err == nil:
			ev := CodeEvidence{Sender: senderDomain(res.From), Subject: maskDigits(res.Subject), At: res.ReceivedAt, Length: len([]rune(res.Code))}
			if fits != nil && !fits(res.Code) {
				return "", ev, ErrCodeBoxes
			}
			c.mu.Lock()
			c.given = res.ReceivedAt
			c.mu.Unlock()
			return res.Code, ev, nil
		case isNotFound(err):
			// "no code yet" is polled again.
		default:
			if ctx.Err() != nil {
				return "", CodeEvidence{}, ctx.Err()
			}
			var le *gmail.LookupError
			if errors.As(err, &le) {
				return "", CodeEvidence{}, &CodeFailure{Reason: le.Reason, Message: le.Message}
			}
			return "", CodeEvidence{}, &CodeFailure{Message: err.Error()}
		}
		if !time.Now().Add(tm.Poll).Before(deadline) {
			return "", CodeEvidence{}, errNoCode
		}
		if !sleepCtx(ctx, tm.Poll) {
			return "", CodeEvidence{}, ctx.Err()
		}
	}
}

var errNoCode = errors.New("no verification code arrived")

var segmentRE = lazyregexp.New(`(?i)\b(?:one[- ]?time|otp|digit|character|char|code|verification)\b`)

// segmentIndexRE is a box whose label numbers its place in the code
// ("Please enter OTP character 3", Eightfold, which sets no maxlength).
var segmentIndexRE = lazyregexp.New(`(?i)\b(?:digit|character|char)\s*#?\d\b`)

// codeBoxes reads the verification-code boxes of a page and refuses a form
// that is not a code form: one that sets a passcode, a password, a signature
// or a national-identifier box, shows a captcha, or has a non-code text box
// beside the boxes. A segmented form of 4 to 8 boxes, each labelled with
// one-time, OTP, digit or character vocabulary and taking one character (by
// its maxlength, or by a label that numbers its place), counts as a code
// form; so does one box labelled as a verification code.
func codeBoxes(st *PageState) (boxes []*InputInfo, refusal string) {
	var others, segs, singles []*InputInfo
	for i := range st.Inputs {
		in := &st.Inputs[i]
		if in.Furniture || in.Type == "hidden" {
			continue
		}
		low := strings.ToLower(in.Label + " " + in.Name + " " + in.ID)
		switch {
		case in.Type == "password" || strings.Contains(low, "passcode") || strings.Contains(low, "password") || strings.Contains(low, "signature") || strings.Contains(low, "ssn") || strings.Contains(low, "social security") || strings.Contains(low, "national id"):
			return nil, "the form sets a secret or an identifier, not a code"
		case (in.MaxLength == 1 || segmentIndexRE.MatchString(low)) && segmentRE.MatchString(low):
			segs = append(segs, in)
		case isCodeBox(in.Label) || segmentRE.MatchString(low) && (in.Type == "text" || in.Type == "tel" || in.Type == "number"):
			singles = append(singles, in)
		case in.Type == "checkbox" || in.Type == "radio":
		default:
			others = append(others, in)
		}
	}
	if len(st.Captcha) > 0 && DetectChallenge(st) != nil {
		return nil, "a captcha stands on the form"
	}
	if len(others) > 0 && (len(segs) > 0 || len(singles) > 0) {
		return nil, "a non-code text box stands beside the code boxes"
	}
	switch {
	case len(segs) >= 4 && len(segs) <= 8:
		return segs, ""
	case len(segs) == 0 && len(singles) == 1:
		return singles, ""
	}
	return nil, ""
}

// typeCode spreads a code over the boxes only when their number equals the
// number of characters in the code; one box takes the whole code. Segmented
// boxes are typed one character each with key events.
func typeCode(ctx context.Context, t Tab, boxes []*InputInfo, code string) error {
	runes := []rune(code)
	if len(boxes) != 1 && len(boxes) != len(runes) {
		return fmt.Errorf("%w: %d boxes for a code of %d characters", ErrCodeBoxes, len(boxes), len(runes))
	}
	for i, b := range boxes {
		ok, err := Focus(ctx, t, b.I)
		if err != nil || !ok {
			return fmt.Errorf("the code box could not be focused")
		}
		text := code
		if len(boxes) > 1 {
			text = string(runes[i])
		}
		if err := t.TypeText(ctx, text); err != nil {
			return err
		}
	}
	return nil
}

func fitsBoxes(n int) func(string) bool {
	return func(code string) bool { return n == 1 || n == len([]rune(code)) }
}

// codeLaneFor is the check stage's lane: it claims the required unanswered
// rows that read as code boxes, makes one mail read series, types the code and
// settles before the page is re-read. A code box on a Greenhouse page before
// any Submit was pressed is left to the user without waiting on mail.
func (s *session) fillCodeBoxes(ctx context.Context, st *PageState, ps *pageState, anchor time.Time) {
	if len(ps.leftovers) == 0 && !hasSegmented(st) {
		return
	}
	hasCode := false
	for _, l := range ps.leftovers {
		if l.Reason == ReasonCodeBox {
			hasCode = true
		}
	}
	if !hasCode && !hasSegmented(st) {
		return
	}
	r := s.r
	if s.e.Run.ATS == ATSGreenhouse && !s.e.Run.Pressed {
		ps.report.Notes = append(ps.report.Notes, "a code box stands before any Submit; it is left to you and no mail is waited for")
		return
	}
	boxes, why := codeBoxes(st)
	if len(boxes) == 0 {
		if why != "" {
			ps.report.Notes = append(ps.report.Notes, "the code lane refused the form: "+why)
		}
		return
	}
	if r.d.Page.Codes == nil {
		return
	}
	code, ev, err := s.e.codeLane().Lookup(ctx, r.d.Page.Codes, r.d.Page.Code, r.d.Now, anchor, s.codeDomain(), fitsBoxes(len(boxes)))
	if err != nil {
		var cf *CodeFailure
		switch {
		case errors.As(err, &cf):
			s.e.noMailbox(hostOf(st.URL), err)
			ps.report.Notes = append(ps.report.Notes, cf.Error())
		case errors.Is(err, ErrCodeBoxes):
			ps.report.Notes = append(ps.report.Notes, fmt.Sprintf("the mailed code of %d characters does not fit the %d boxes; the mail stays available", ev.Length, len(boxes)))
		default:
			ps.report.Notes = append(ps.report.Notes, err.Error())
		}
		return
	}
	if err := typeCode(ctx, s.tab, boxes, code); err != nil {
		ps.report.Notes = append(ps.report.Notes, "the code could not be typed: "+err.Error())
		return
	}
	if !sleepCtx(ctx, r.d.Page.Code.withDefaults().Settle) {
		return
	}
	ps.report.Notes = append(ps.report.Notes, fmt.Sprintf("a verification code was typed (sender %s, subject %q, received %s)", ev.Sender, ev.Subject, ev.At.UTC().Format("15:04:05")))
	unlanded := s.codeLanded(ctx)
	keep := ps.leftovers[:0]
	for _, l := range ps.leftovers {
		switch {
		case l.Reason != ReasonCodeBox:
			keep = append(keep, l)
		case unlanded != "":
			l.Detail = unlanded
			keep = append(keep, l)
		}
	}
	ps.leftovers = keep
	ps.report.Unresolved = nil
	for _, l := range ps.leftovers {
		ps.report.Unresolved = append(ps.report.Unresolved, fmt.Sprintf("%s (%s)", l.Label, l.Detail))
	}
}

// codeLanded re-reads the page once the framework has settled and reports why
// the typed code is not on it: a vanished control reads as unreadable-control
// and a box the page did not keep as a write that did not land. Empty means
// every code box holds its character.
func (s *session) codeLanded(ctx context.Context) string {
	st, err := Probe(ctx, s.tab)
	if err != nil {
		return "unreadable-control"
	}
	boxes, _ := codeBoxes(st)
	if len(boxes) == 0 {
		return "unreadable-control"
	}
	for _, b := range boxes {
		if !b.Filled {
			return "write did not land"
		}
	}
	return ""
}

func hasSegmented(st *PageState) bool {
	n := 0
	for _, in := range st.Inputs {
		if in.MaxLength == 1 && segmentRE.MatchString(strings.ToLower(in.Label+" "+in.Name)) {
			n++
		}
	}
	return n >= 4 && n <= 8
}

// codeDomain bounds the lookup to the sender of the site where it is known.
func (s *session) codeDomain() string {
	host, _ := parseHost(s.e.Run.Input.URL)
	if d := SenderDomainFor(host); d != "" {
		return d
	}
	if s.e.Run.ATS == ATSGreenhouse {
		return SenderGreenhouse
	}
	return ""
}

func (e *Exec) codeLane() *codeLane {
	if e.codes == nil {
		e.codes = &codeLane{}
	}
	return e.codes
}

// securityBoxRE names a Greenhouse security-code box by its label, name or id.
// The boxes are marked aria-required, not required, and carry ids like
// security-input-3.
var securityBoxRE = lazyregexp.New(`(?i)security[ _-]?(?:code|input)`)

// securityCode handles the Greenhouse post-submit security code. When the
// application service refuses the first Submit with a response that names a
// security-code recipient, the emailed 8-character code is read from
// greenhouse-mail.io received after the rejected press, typed into every empty
// security-code box and Submit is pressed once more; Submit is never pressed
// over an empty box and never a third time.
func (r *Runner) securityCode(ctx context.Context, e *Exec, s *session, po *pressOutcome, again func(context.Context) *stop) (retried bool, why string) {
	if e.Run.ATS != ATSGreenhouse || !strings.Contains(po.body, "security_code_recipient") {
		return false, ""
	}
	if r.d.Page.Codes == nil {
		return false, "no mail lookup is configured to read the emailed security code"
	}
	st, err := Probe(ctx, s.tab)
	if err != nil {
		return false, err.Error()
	}
	var boxes []*InputInfo
	seen := 0
	for i := range st.Inputs {
		in := &st.Inputs[i]
		if !in.Furniture && securityBoxRE.MatchString(in.Label+" "+in.Name+" "+in.ID) {
			seen++
			if !in.Filled {
				boxes = append(boxes, in)
			}
		}
	}
	if seen == 0 {
		return false, "the page shows no security-code box"
	}
	if len(boxes) == 0 {
		// The fill already typed the code: Submit is pressed again without a
		// second lookup.
		if stp := again(ctx); stp != nil {
			return false, stp.reason
		}
		return true, ""
	}
	d := gates.CodeRequest(gates.CodeRequestInput{})
	_ = r.audit(e, d, e.Run.Input.URL, false)
	code, _, err := e.codeLane().Lookup(ctx, r.d.Page.Codes, r.d.Page.Code, r.d.Now, CodeAnchor(po.at), SenderGreenhouse, fitsBoxes(len(boxes)))
	if err != nil {
		e.noMailbox(hostOf(st.URL), err)
		return false, err.Error()
	}
	if err := typeCode(ctx, s.tab, boxes, code); err != nil {
		return false, err.Error()
	}
	if stp := again(ctx); stp != nil {
		return false, stp.reason
	}
	return true, ""
}
