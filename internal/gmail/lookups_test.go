package gmail

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// lookupEnv answers every search with every message put, the way the
// lookups' own filters are tested; it records the All Mail queries.
type lookupEnv struct {
	*env
	lk   *Lookups
	mu   sync.Mutex
	qs   []string
	list []string
	spam map[string]bool
	// admit, when set, decides whether a query finds the messages; hold runs
	// before an All Mail search is answered.
	admit func(q string) bool
	hold  func()
}

func newLookupEnv(t *testing.T) *lookupEnv {
	e := newEnv(t)
	e.connect()
	le := &lookupEnv{env: e, lk: &Lookups{Client: e.client, Now: e.clk.now}}
	e.imap.SearchWith(func(attr, q string) []string {
		le.mu.Lock()
		hold, admit := le.hold, le.admit
		if attr == `\All` {
			le.qs = append(le.qs, q)
		}
		ids := slices.Clone(le.list)
		le.mu.Unlock()
		if attr == `\All` && hold != nil {
			hold()
		}
		// Gmail answers a search in Spam only for a query that asks for it.
		if attr == `\Junk` && !strings.Contains(q, "in:anywhere") && !strings.Contains(q, "in:spam") || admit != nil && !admit(q) {
			return nil
		}
		return ids
	})
	return le
}

func (le *lookupEnv) put(m msgSpec) {
	le.mu.Lock()
	folder := 0
	if le.spam[m.ID] {
		folder = 1
	}
	le.list = append(le.list, m.ID)
	le.mu.Unlock()
	le.imap.Add(folder, m)
}

func (le *lookupEnv) queries() []string {
	le.mu.Lock()
	defer le.mu.Unlock()
	return slices.Clone(le.qs)
}

var since = time.Date(2027, 3, 10, 9, 0, 0, 500_000_000, time.UTC)

func ms(offset time.Duration) int64 { return since.Add(offset).UnixMilli() }

func codeMail(id, domain, code string, offset time.Duration) msgSpec {
	return msgSpec{ID: id, From: "Careers <" + addr("no-reply", domain) + ">", Subject: "Your verification code", Text: "Your verification code: " + code, MS: ms(offset)}
}

func TestGmail_OTP_ParameterValidation(t *testing.T) {
	for name, c := range map[string]struct {
		req  OTPRequest
		kind string
	}{
		"missing since":    {OTPRequest{}, KindBadRequest},
		"unparsable since": {OTPRequest{Since: "yesterday"}, KindBadRequest},
		"url as domain":    {OTPRequest{Since: since.Format(time.RFC3339), FromDomain: "https://example.com"}, KindBadRequest},
		"path as domain":   {OTPRequest{Since: since.Format(time.RFC3339), FromDomain: "example.com/x"}, KindBadRequest},
		"single label":     {OTPRequest{Since: since.Format(time.RFC3339), FromDomain: "example"}, KindBadRequest},
	} {
		_, _, err := ParseOTPRequest(c.req)
		if err == nil || err.Kind != c.kind {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	s, d, err := ParseOTPRequest(OTPRequest{Since: since.Format(time.RFC3339Nano), FromDomain: "  OTP.Example.COM "})
	if err != nil || !s.Equal(since) || d != "otp.example.com" {
		t.Fatalf("valid = %v %q %v", s, d, err)
	}
}

func TestGmail_OTP_MissingSinceMakesNoMailRead(t *testing.T) {
	le := newLookupEnv(t)
	if _, _, err := ParseOTPRequest(OTPRequest{}); err == nil {
		t.Fatal("accepted")
	}
	if len(le.queries()) != 0 {
		t.Fatal("mail was read")
	}
}

func TestGmail_OTP_AfterEpochNotNewerThan(t *testing.T) {
	le := newLookupEnv(t)
	le.put(codeMail("c1", "ats.example.com", "482913", time.Minute))
	if _, err := le.lk.LatestCode(le.ctx, since, ""); err != nil {
		t.Fatal(err)
	}
	q := le.queries()[0]
	if !strings.HasPrefix(q, fmt.Sprintf("after:%d ", since.Unix())) || strings.Contains(q, "newer_than") {
		t.Fatalf("query = %q", q)
	}
}

func TestGmail_OTP_QueryBoundsMarketingCodes(t *testing.T) {
	le := newLookupEnv(t)
	_, _ = le.lk.LatestCode(le.ctx, since, "ats.example.com")
	q := le.queries()[0]
	for _, want := range []string{"from:ats.example.com", `"verification code"`, "otp", "{"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q lacks %q", q, want)
		}
	}
	if otpMaxMessages != 10 {
		t.Fatal("at most 10 messages")
	}
}

func TestGmail_OTP_ACodeMailInSpamIsFound(t *testing.T) {
	le := newLookupEnv(t)
	le.spam = map[string]bool{"5a3": true}
	le.put(codeMail("5a3", "adp.com", "482913", time.Minute))
	r, err := le.lk.LatestCode(le.ctx, since, "adp.com")
	if err != nil || r.Code != "482913" {
		t.Fatalf("result = %+v, %v", r, err)
	}
}

// Gmail's receipt time has whole seconds, so a mail of the second since
// falls in does not qualify; the apply chain anchors since a second before
// its press for that reason (apply.CodeAnchor).
func TestGmail_OTP_StrictlyNewerThanSince(t *testing.T) {
	le := newLookupEnv(t)
	le.put(codeMail("a0", "ats.example.com", "482913", 0))
	_, err := le.lk.LatestCode(le.ctx, since, "")
	var lerr *LookupError
	if !errors.As(err, &lerr) || lerr.Kind != KindNotFound {
		t.Fatalf("a message received exactly at since must not qualify: %v", err)
	}
	le.put(codeMail("af", "ats.example.com", "771204", time.Second))
	r, err := le.lk.LatestCode(le.ctx, since, "")
	if err != nil || r.Code != "771204" {
		t.Fatalf("result = %+v, %v", r, err)
	}
}

func TestGmail_OTP_NewestFirstSkipsUnextractable(t *testing.T) {
	le := newLookupEnv(t)
	le.put(codeMail("c0", "ats.example.com", "111111", time.Minute))
	le.put(msgSpec{ID: "ee", From: addr("x", "ats.example.com"), Subject: "Welcome", Text: "Nothing to see here.", MS: ms(3 * time.Minute)})
	le.put(codeMail("dd", "ats.example.com", "222222", 2*time.Minute))
	r, err := le.lk.LatestCode(le.ctx, since, "")
	if err != nil || r.Code != "222222" {
		t.Fatalf("result = %+v, %v", r, err)
	}
	if r.ReceivedAt.Unix() != since.Add(2*time.Minute).Unix() || !strings.Contains(r.From, "ats.example.com") || r.Subject == "" {
		t.Fatalf("result = %+v", r)
	}
	// An ambiguous message is skipped, not an error.
	le.put(msgSpec{ID: "ab", From: addr("x", "ats.example.com"), Subject: "Codes", Text: "Verification code 482913. Backup code 771204.", MS: ms(5 * time.Minute)})
	if r, err = le.lk.LatestCode(le.ctx, since, ""); err != nil || r.Code != "222222" {
		t.Fatalf("after an ambiguous newest: %+v, %v", r, err)
	}
}

func TestGmail_OTP_CodeFromTheExpectedSenderOnly(t *testing.T) {
	le := newLookupEnv(t)
	le.put(codeMail("a75", "ats.example.com", "482913", time.Minute))
	le.put(codeMail("fe", "other.example.org", "999111", 2*time.Minute))
	r, err := le.lk.LatestCode(le.ctx, since, "ats.example.com")
	if err != nil || r.Code != "482913" {
		t.Fatalf("result = %+v, %v", r, err)
	}
	if q := le.queries()[0]; !strings.Contains(q, "from:ats.example.com") {
		t.Fatalf("the sender is narrowed in the query too: %q", q)
	}
	// A subdomain qualifies; a look-alike does not.
	le2 := newLookupEnv(t)
	le2.put(codeMail("5b", "mail.ats.example.com", "123456", time.Minute))
	le2.put(codeMail("a1", "evilats.example.com", "654321", 2*time.Minute))
	le2.put(codeMail("5f", "ats.example.com.evil.test", "777777", 3*time.Minute))
	r, err = le2.lk.LatestCode(le2.ctx, since, "ats.example.com")
	if err != nil || r.Code != "123456" {
		t.Fatalf("result = %+v, %v", r, err)
	}
}

// Gmail lists a message for the code lookup only when one of the query's
// OR-group terms is in it, so a code mail worded with none of them is never
// read and the run waits out its code step. The mails are real wordings.
func TestGmail_OTP_TheQueryAdmitsTheCodeMailsSitesSend(t *testing.T) {
	for name, m := range map[string]msgSpec{
		// Eightfold (careers.qualcomm.com) sign-up.
		"eightfold": {From: addr("no-reply", "eightfold.ai"), Subject: "Verify your email",
			Text: "Email Verification Verify your email We just need to verify your email address. Enter the following code below to complete the verification process: 482913 The code will expire after 10 minutes. Privacy Policy"},
	} {
		t.Run(name, func(t *testing.T) {
			le := newLookupEnv(t)
			m.ID, m.MS = "ab1", ms(time.Minute)
			le.put(m)
			le.admit = func(q string) bool { return admits(q, m.Subject+" "+m.Text) }
			r, err := le.lk.LatestCode(le.ctx, since, "")
			if err != nil || r.Code != "482913" {
				t.Fatalf("result = %+v, %v", r, err)
			}
		})
	}
}

// admits applies the query's {…} OR group as Gmail does: a quoted phrase or a
// bare word found in the message, case-insensitively.
func admits(q, text string) bool {
	open, end := strings.Index(q, "{"), strings.LastIndex(q, "}")
	if open < 0 || end < open {
		return true
	}
	text = strings.ToLower(strings.Join(strings.Fields(text), " "))
	group := q[open+1 : end]
	for group != "" {
		group = strings.TrimSpace(group)
		var term string
		if strings.HasPrefix(group, `"`) {
			close := strings.Index(group[1:], `"`) + 1
			term, group = group[1:close], group[close+1:]
		} else {
			term, group, _ = strings.Cut(group, " ")
		}
		if term != "" && strings.Contains(text, strings.ToLower(term)) {
			return true
		}
	}
	return false
}

// A refused app password is the user's to fix; Gmail's "too many
// connections" is not, and entering a new password would fix nothing.
func TestGmail_OTP_NotSignedInVsBusy(t *testing.T) {
	le := newLookupEnv(t)
	le.imap.Refuse("Invalid credentials (Failure)")
	_, err := le.lk.LatestCode(le.ctx, since, "")
	var lerr *LookupError
	if !errors.As(err, &lerr) || lerr.Kind != KindUnavailable || lerr.Reason != ReasonNotSignedIn || lerr.Retryable || !strings.Contains(lerr.Message, ConnectCommand) {
		t.Fatalf("refused: %+v", lerr)
	}
	q := newLookupEnv(t)
	q.imap.Refuse("Too many simultaneous connections. (Failure)")
	_, err = q.lk.LatestCode(q.ctx, since, "")
	if !errors.As(err, &lerr) || lerr.Kind != KindUnavailable || lerr.Reason != ReasonUnreachable || !lerr.Retryable || strings.Contains(lerr.Message, ConnectCommand) {
		t.Fatalf("transient: %+v", lerr)
	}
}

func TestGmail_OTP_TransportFailureIsNotNotFound(t *testing.T) {
	le := newLookupEnv(t)
	le.imap.Close()
	_, err := le.lk.LatestCode(le.ctx, since, "")
	var lerr *LookupError
	if !errors.As(err, &lerr) || lerr.Kind == KindNotFound || !lerr.Retryable || lerr.Reason != ReasonUnreachable {
		t.Fatalf("transport failure = %+v", lerr)
	}
}

func TestGmail_OTP_ASharedLookupOutlivesItsFirstCaller(t *testing.T) {
	le := newLookupEnv(t)
	le.put(codeMail("c", "ats.example.com", "482913", time.Minute))
	release, entered := make(chan struct{}), make(chan struct{}, 8)
	le.hold = func() {
		entered <- struct{}{}
		<-release
	}
	first, cancel := context.WithCancel(le.ctx)
	defer cancel()
	firstErr := make(chan error, 1)
	go func() {
		_, err := le.lk.LatestCode(first, since, "")
		firstErr <- err
	}()
	<-entered
	type answer struct {
		r   OTPResult
		err error
	}
	second := make(chan answer, 1)
	go func() {
		r, err := le.lk.LatestCode(le.ctx, since, "")
		second <- answer{r, err}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for le.lk.flights.Joined() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("the second request never joined the running lookup")
		}
		runtime.Gosched()
	}
	// The caller that started the lookup gives up; it is answered at once and
	// the lookup goes on for the one still waiting.
	cancel()
	var lerr *LookupError
	if err := <-firstErr; !errors.As(err, &lerr) || lerr.Kind != KindUnavailable || !lerr.Retryable {
		t.Fatalf("the caller that gave up got %v", err)
	}
	close(release)
	if a := <-second; a.err != nil || a.r.Code != "482913" {
		t.Fatalf("the waiting caller got %+v, %v", a.r, a.err)
	}
	if got := len(le.queries()); got != 1 {
		t.Fatalf("Gmail lookups = %d, want 1", got)
	}
}

func TestGmail_OTP_NoStoreReadOrWrite(t *testing.T) {
	le := newLookupEnv(t)
	le.put(codeMail("c", "ats.example.com", "482913", time.Minute))
	count := func() (n int) {
		for _, tbl := range gmailTables {
			var c int
			if err := le.st.Read().QueryRow("SELECT count(*) FROM " + tbl).Scan(&c); err != nil {
				t.Fatal(err)
			}
			n += c
		}
		return n
	}
	before := count()
	if _, err := le.lk.LatestCode(le.ctx, since, ""); err != nil {
		t.Fatal(err)
	}
	if count() != before {
		t.Fatal("a code lookup wrote to the database")
	}
}

func TestGmail_OTP_CodeNeverInLogsOrTraces(t *testing.T) {
	le := newLookupEnv(t)
	code := "48" + "2913"
	le.put(codeMail("c", "ats.example.com", code, time.Minute))
	r, err := le.lk.LatestCode(le.ctx, since, "")
	if err != nil || r.Code != code {
		t.Fatal(err)
	}
	// Failure text, the stored state and the database carry no code.
	le.put(msgSpec{ID: "e0", From: addr("a", "ats.example.com"), Subject: "s", Text: "Verification code " + code, MS: ms(2 * time.Minute)})
	le.imap.OnBodyFetch(func([]string) bool { return false })
	_, err = le.lk.LatestCode(le.ctx, since.Add(time.Second), "")
	if err == nil || strings.Contains(err.Error(), code) {
		t.Fatalf("error text: %v", err)
	}
	assertNoSecretOnDisk(t, le.env, code)
}

func TestGmail_OTP_SpoofedFromHeadersAreNotTheExpectedSender(t *testing.T) {
	le := newLookupEnv(t)
	le.put(codeMail("ea1", "greenhouse-mail.io", "111222", time.Minute))
	for i, from := range []string{
		`"greenhouse-mail.io" <` + addr("attacker", "evil.test") + `>`,
		addr("attacker", "evil.test") + " .greenhouse-mail.io",
		"<" + addr("attacker", "greenhouse-mail.io.evil.test") + ">",
		addr("attacker", "evil.test") + ", " + addr("a", "greenhouse-mail.io"),
	} {
		le.put(msgSpec{ID: fmt.Sprintf("5f0%d", i), From: from, Subject: "Your verification code", Text: "Your verification code: 999888", MS: ms(time.Duration(i+2) * time.Minute)})
	}
	res, err := le.lk.LatestCode(le.ctx, since, "greenhouse-mail.io")
	if err != nil || res.Code != "111222" {
		t.Fatalf("a spoofed newer mail won: %+v %v", res, err)
	}
}
