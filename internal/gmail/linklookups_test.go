package gmail

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const tenant = "acme.wd5.myworkdayjobs.com"

func wdFrom(local string) string { return "Workday <" + addr(local, "otp.workday.com") + ">" }

func activationMail(id string, host string, offset time.Duration) msgSpec {
	return msgSpec{ID: id, From: wdFrom("acme"), Subject: "Verify your candidate account",
		HTML: `<p>Confirm your account</p><a href="https://` + host + `/Careers/activate/a1b2c3d4e5">Confirm</a>`, MS: ms(offset)}
}

func TestGmail_ActivationRoute_SinceDaysBounds(t *testing.T) {
	for in, ok := range map[string]bool{"": true, "1": true, "30": true, "90": true, "0": false, "91": false, "-3": false, "abc": false, "7.5": false} {
		q, err := ParseLinkRequest(LinkRequest{Host: tenant, SinceDays: in}, false)
		if (err == nil) != ok {
			t.Errorf("sinceDays %q: err = %v", in, err)
		}
		if ok && in == "" && q.SinceDays != 30 {
			t.Errorf("default sinceDays = %d", q.SinceDays)
		}
	}
	if _, err := ParseLinkRequest(LinkRequest{Host: "  "}, false); err == nil || err.Kind != KindBadRequest {
		t.Fatalf("missing host: %v", err)
	}
	q, _ := ParseLinkRequest(LinkRequest{Host: "  ACME.example.test "}, false)
	if q.Host != "acme.example.test" {
		t.Fatalf("host = %q", q.Host)
	}
}

func TestGmail_ResetLink_RequiresAfterAndWorkdaySender(t *testing.T) {
	le := newLookupEnv(t)
	if _, err := ParseLinkRequest(LinkRequest{Host: tenant}, true); err == nil || err.Kind != KindBadRequest || !strings.Contains(err.Message, "after") {
		t.Fatalf("reset without after: %v", err)
	}
	if len(le.qs) != 0 {
		t.Fatal("mail was read")
	}
	q, err := ParseLinkRequest(LinkRequest{Host: tenant, After: since.Format(time.RFC3339Nano), SinceDays: "400"}, true)
	if err != nil {
		t.Fatalf("a reset ignores sinceDays: %v", err)
	}
	le.put(msgSpec{ID: "e8", From: wdFrom("acme"), Subject: "Reset your password", HTML: `<a href="https://` + tenant + `/Careers/passwordreset/a1b2c3d4e5">Reset</a>`, MS: ms(time.Minute)})
	r, lerr := le.lk.ResetLink(le.ctx, q)
	if lerr != nil || r.Host != tenant || !strings.Contains(le.qs[0], "from:otp.workday.com") || strings.Contains(le.qs[0], "successfactors") {
		t.Fatalf("result = %+v, %v, query %q", r, lerr, le.qs)
	}
	if _, err := ParseLinkRequest(LinkRequest{Host: tenant, After: "soon"}, false); err == nil {
		t.Fatal("unparsable after accepted")
	}
}

// Gmail keeps a receipt time to the second: a mail in the second of the
// after instant is not newer than it, one a second later is.
func TestGmail_ActivationRoute_AfterIsExclusive(t *testing.T) {
	le := newLookupEnv(t)
	le.put(activationMail("a7", tenant, 0))
	q := LinkQuery{Host: tenant, SinceDays: 30, After: since}
	_, err := le.lk.ActivationLink(le.ctx, q)
	var lerr *LookupError
	if !errors.As(err, &lerr) || lerr.Kind != KindNotFound {
		t.Fatalf("a mail at the exact after instant must not qualify: %v", err)
	}
	if !strings.HasPrefix(le.qs[0], fmt.Sprintf("after:%d ", since.Unix())) {
		t.Fatalf("the query bound is whole seconds: %q", le.qs[0])
	}
	le.put(activationMail("b7", tenant, time.Second))
	r, err := le.lk.ActivationLink(le.ctx, q)
	if err != nil || !r.ReceivedAt.Equal(since.Add(time.Second).Truncate(time.Second)) {
		t.Fatalf("result = %+v, %v", r, err)
	}
	// The newer-than-the-press case: the only qualifying mail predates it.
	le2 := newLookupEnv(t)
	le2.put(activationMail("c7", tenant, -time.Minute))
	if _, err := le2.lk.ActivationLink(le2.ctx, q); err == nil {
		t.Fatal("a mail older than after was returned")
	}
}

func TestGmail_ActivationLink_ExactHostThroughTheLookup(t *testing.T) {
	le := newLookupEnv(t)
	le.put(activationMail("d7", "other-tenant.wd5.myworkdayjobs.com", time.Minute))
	_, err := le.lk.ActivationLink(le.ctx, LinkQuery{Host: tenant, SinceDays: 30})
	var lerr *LookupError
	if !errors.As(err, &lerr) || lerr.Kind != KindNotFound {
		t.Fatalf("a different tenant: %v", err)
	}
	if strings.Contains(lerr.Message, "a1b2c3d4e5") {
		t.Fatal("the not-found text carries link material")
	}
	// A message that yields no link is skipped and the search continues, newest first.
	le.put(activationMail("e7", tenant, 2*time.Minute))
	le.put(msgSpec{ID: "f7", From: wdFrom("acme"), Subject: "Welcome", Text: "no link here", MS: ms(5 * time.Minute)})
	r, err := le.lk.ActivationLink(le.ctx, LinkQuery{Host: tenant, SinceDays: 30})
	if err != nil || r.URL != "https://"+tenant+"/Careers/activate/a1b2c3d4e5" {
		t.Fatalf("result = %+v, %v", r, err)
	}
	if !strings.Contains(le.qs[len(le.qs)-1], "from:otp.workday.com") || !strings.Contains(le.qs[len(le.qs)-1], "from:successfactors.com") {
		t.Fatalf("activation lists by both senders: %q", le.qs[len(le.qs)-1])
	}
}

// Workday's own tenant prints the activation address as text: it is the link
// (owner decision). A mail that names the host with no address the locks
// accept is reported as such, so the stop can send the user to it instead of
// saying none arrived.
func TestGmail_ActivationLink_AddressPrintedAsTextIsTheLink(t *testing.T) {
	le := newLookupEnv(t)
	le.put(msgSpec{ID: "a8", From: wdFrom("workday"), Subject: "Verify your candidate account",
		HTML: `<div>Click this link to confirm your email address<br>https://` + tenant + `/Careers/activate/a1b2c3d4e5/?redirect=x<br>The link will expire after 24 hours.</div><a href="http://www.workday.com/company/careers.php">Workday</a>`, MS: ms(time.Minute)})
	r, err := le.lk.ActivationLink(le.ctx, LinkQuery{Host: tenant, SinceDays: 30})
	if err != nil || r.URL != "https://"+tenant+"/Careers/activate/a1b2c3d4e5/?redirect=x" {
		t.Fatalf("the printed address: %+v, %v", r, err)
	}
	le1 := newLookupEnv(t)
	le1.put(msgSpec{ID: "b8", From: wdFrom("workday"), Subject: "Your candidate account",
		HTML: `<div>Sign in at https://` + tenant + `/Careers/login to continue.</div>`, MS: ms(time.Minute)})
	_, err = le1.lk.ActivationLink(le1.ctx, LinkQuery{Host: tenant, SinceDays: 30})
	var lerr *LookupError
	if !errors.As(err, &lerr) || lerr.Kind != KindNotFound || lerr.Reason != ReasonLinkRefused {
		t.Fatalf("a mail naming the host with no link the locks accept: %+v", err)
	}
	// A mail for another tenant is no mail for this host.
	le2 := newLookupEnv(t)
	le2.put(activationMail("d7", "other-tenant.wd5.myworkdayjobs.com", time.Minute))
	if _, err := le2.lk.ActivationLink(le2.ctx, LinkQuery{Host: tenant, SinceDays: 30}); !errors.As(err, &lerr) || lerr.Reason == ReasonLinkRefused {
		t.Fatalf("another tenant's mail: %+v", err)
	}
}

// A refused app password and a disconnected account both answer as not
// signed in, a state the caller can name, rather than as no mail.
func TestGmail_ActivationRoute_NotSignedIn(t *testing.T) {
	e2 := newLookupEnv(t)
	e2.refuseLogins("Invalid credentials (Failure)")
	_, err := e2.lk.ActivationLink(e2.ctx, LinkQuery{Host: tenant, SinceDays: 30})
	var lerr *LookupError
	if !errors.As(err, &lerr) || lerr.Reason != ReasonNotSignedIn || lerr.Kind != KindUnavailable {
		t.Fatalf("err = %v", err)
	}
	if _, err := e2.account.Disconnect(e2.ctx); err != nil {
		t.Fatal(err)
	}
	_, err = e2.lk.ResetLink(e2.ctx, LinkQuery{Host: tenant, After: since})
	if !errors.As(err, &lerr) || lerr.Reason != ReasonNotSignedIn {
		t.Fatalf("reset err = %v", err)
	}
}

func TestGmail_ActivationRoute_UrlNeverLogged(t *testing.T) {
	le := newLookupEnv(t)
	le.put(activationMail("c8", tenant, time.Minute))
	if _, err := le.lk.ActivationLink(le.ctx, LinkQuery{Host: tenant, SinceDays: 30}); err != nil {
		t.Fatal(err)
	}
	assertNoSecretOnDisk(t, le.env, "a1b2c3d4e5")
}

func TestGmail_SigninLink_DisplayOnlyFlag(t *testing.T) {
	le := newLookupEnv(t)
	link := "https://ats.example.com/magic-link?token=Ab3dEf5hIjK"
	le.put(msgSpec{ID: "d8", From: addr("no-reply", "ats.example.com"), Subject: "Your sign-in link",
		HTML: `<p>Sign in to your account</p><a href="` + link + `">Sign in</a>`, MS: ms(time.Minute)})
	r, err := le.lk.LatestSigninLink(le.ctx, since)
	if err != nil || !r.DisplayOnly || r.URL != link || r.Host != "ats.example.com" || len(r.Reasons) == 0 || r.Subject == "" {
		t.Fatalf("result = %+v, %v", r, err)
	}
	if !strings.Contains(le.qs[0], `"magic link"`) || !strings.HasPrefix(le.qs[0], "after:") {
		t.Fatalf("query = %q", le.qs[0])
	}
	// Strictly newer than since.
	le2 := newLookupEnv(t)
	le2.put(msgSpec{ID: "d8", From: addr("no-reply", "ats.example.com"), Subject: "x", HTML: `<p>Sign in</p><a href="` + link + `">Sign in</a>`, MS: ms(0)})
	if _, err := le2.lk.LatestSigninLink(le2.ctx, since); err == nil {
		t.Fatal("a mail at since qualified")
	}
}

// All Mail leaves Spam out, and Gmail answers a Spam search only for a query
// that asks for it, so every bounded lookup asks.
func TestGmail_Lookups_MailInSpamIsFound(t *testing.T) {
	inSpam := func(t *testing.T, m msgSpec) *lookupEnv {
		le := newLookupEnv(t)
		le.spam = map[string]bool{m.ID: true}
		le.put(m)
		return le
	}
	t.Run("activation link", func(t *testing.T) {
		le := inSpam(t, activationMail("a51", tenant, time.Minute))
		q, _ := ParseLinkRequest(LinkRequest{Host: tenant}, false)
		if r, err := le.lk.ActivationLink(le.ctx, q); err != nil || r.Host != tenant {
			t.Fatalf("result = %+v, %v (query %q)", r, err, le.queries())
		}
	})
	t.Run("reset link", func(t *testing.T) {
		le := inSpam(t, msgSpec{ID: "a52", From: wdFrom("acme"), Subject: "Reset your password",
			HTML: `<a href="https://` + tenant + `/Careers/passwordreset/a1b2c3d4e5">Reset</a>`, MS: ms(time.Minute)})
		q, _ := ParseLinkRequest(LinkRequest{Host: tenant, After: since.Format(time.RFC3339Nano)}, true)
		if r, err := le.lk.ResetLink(le.ctx, q); err != nil || r.Host != tenant {
			t.Fatalf("result = %+v, %v (query %q)", r, err, le.queries())
		}
	})
	t.Run("sign-in link", func(t *testing.T) {
		link := "https://ats.example.com/magic-link?token=Ab3dEf5hIjK"
		le := inSpam(t, msgSpec{ID: "a53", From: addr("no-reply", "ats.example.com"), Subject: "Your sign-in link",
			HTML: `<p>Sign in to your account</p><a href="` + link + `">Sign in</a>`, MS: ms(time.Minute)})
		if r, err := le.lk.LatestSigninLink(le.ctx, since); err != nil || r.URL != link {
			t.Fatalf("result = %+v, %v (query %q)", r, err, le.queries())
		}
	})
	t.Run("receipt", func(t *testing.T) {
		le := inSpam(t, receiptMail("a54", press.Add(time.Minute), "Example Corp"))
		if r, err := le.lk.FindReceipt(le.ctx, rcptLookup); err != nil || r == nil || r.MessageID != "a54" {
			t.Fatalf("receipt = %+v, %v (query %q)", r, err, le.queries())
		}
	})
}

func TestGmail_SigninLink_NeverFetchedByProduct(t *testing.T) {
	var opened atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { opened.Add(1) }))
	defer srv.Close()
	le := newLookupEnv(t)
	link := srv.URL + "/never-open/magic-link?token=Ab3dEf5hIjK"
	le.put(msgSpec{ID: "d8", From: addr("no-reply", "ats.example.com"), Subject: "Your sign-in link",
		HTML: `<p>Sign in to your account</p><a href="` + link + `">Sign in</a>`, MS: ms(time.Minute)})
	if _, err := le.lk.LatestSigninLink(le.ctx, since); err != nil {
		t.Fatal(err)
	}
	if opened.Load() != 0 {
		t.Fatal("the product requested the sign-in link")
	}
}
