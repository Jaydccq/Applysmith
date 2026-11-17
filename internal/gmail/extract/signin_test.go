package extract

import (
	"strings"
	"testing"
)

func a(href, text string) string { return `<a href="` + href + `">` + text + `</a>` }

func TestGmail_SigninLink_CredentialGate(t *testing.T) {
	in := SigninInput{HTML: "<p>Log in to your account</p>" + a("https://ats.example.com/login", "Log in"), Subject: "Sign in"}
	if SigninLink(in) != nil {
		t.Fatal("a token-free login page qualified")
	}
	stmt := SigninInput{HTML: "<p>Log in to your account to view your statement</p>" + a("https://bank.example.com/statements/view", "View")}
	if SigninLink(stmt) != nil {
		t.Fatal("a statement mail qualified")
	}
	for u, want := range map[string]bool{
		"https://x.example.com/auth?token=Ab3dEf5hIj":          true,
		"https://x.example.com/auth?token=abcdefghij":          false,
		"https://x.example.com/p#FAKE_Ab3dEf5hIjK":             true,
		"https://x.example.com/p#short":                        false,
		"https://x.example.com/iT4up5AuH6":                     true,
		"https://x.example.com/application":                    false,
		"https://x.example.com/p?q=" + strings.Repeat("a", 24): true,
	} {
		if got := carriesCredential(u); got != want {
			t.Errorf("carriesCredential(%s) = %v, want %v", u, got, want)
		}
	}
}

func TestGmail_SigninLink_RejectionGate(t *testing.T) {
	good := "https://ats.example.com/magic-link?token=FAKE_Ab3dEf5hIjK"
	for _, ctx := range []string{"To reset your password", "unsubscribe from these mails", "manage your email preferences", "contact support", "view in browser", "your receipt", "download the app"} {
		html := "<p>" + ctx + " </p>" + a(good, "Sign in to your account")
		if SigninLink(SigninInput{HTML: html}) != nil {
			t.Errorf("rejection context %q was ignored", ctx)
		}
	}
	// The rejection reads the URL and the anchor text too.
	if SigninLink(SigninInput{HTML: a("https://ats.example.com/unsubscribe?token=FAKE_Ab3dEf5hIjK", "Sign in")}) != nil {
		t.Error("rejection in the URL was ignored")
	}
	if SigninLink(SigninInput{HTML: a(good, "Sign in and reset your password")}) != nil {
		t.Error("rejection in the anchor text was ignored")
	}
}

func TestGmail_SigninLink_RejectionWindowBeforeOnly(t *testing.T) {
	real := "https://ats.example.com/magic-link?token=FAKE_Ab3dEf5hIjK"
	optional := "https://ats.example.com/set?x=Zz9yXw8vUt"
	body := "<p>Sign in to your account by clicking " + a(real, "Sign in") + ". You can set an optional password as well " + a(optional, "set a password") + ".</p>"
	m := SigninLink(SigninInput{HTML: body})
	if m == nil || m.URL != real {
		t.Fatalf("the sentence after the real link threw it away: %+v", m)
	}
}

func TestGmail_SigninLink_ScoreWeights(t *testing.T) {
	// URL intent (2) + call to action (2) + button and printed text (2) = 6.
	link := "https://ats.example.com/magic-link?token=FAKE_Ab3dEf5hIjK"
	body := "<p>Sign in to your account:</p>" + a(link, "Sign in") + "<p>Or copy this link: " + link + "</p>"
	m := SigninLink(SigninInput{HTML: body})
	if m == nil || m.Score != 6 || len(m.Reasons) != 3 {
		t.Fatalf("match = %+v", m)
	}
	// Intent in the URL and the call to action, not printed: 4.
	m = SigninLink(SigninInput{HTML: "<p>Sign in</p>" + a(link, "Sign in")})
	if m == nil || m.Score != 4 {
		t.Fatalf("two signals = %+v", m)
	}
}

func TestGmail_SigninLink_NeedsTwoSignalsAndMargin(t *testing.T) {
	// A call to action without a URL marker, not printed: one signal.
	cta := SigninInput{HTML: "<p>Continue your application</p>" + a("https://ats.example.com/p/Ab3dEf5hIj", "Go")}
	if SigninLink(cta) != nil {
		t.Fatal("one signal was enough")
	}
	// A URL marker without a call to action, not printed: one signal.
	urlOnly := SigninInput{HTML: a("https://ats.example.com/auth?token=FAKE_Ab3dEf5hIjK", "Open")}
	if SigninLink(urlOnly) != nil {
		t.Fatal("a URL shape alone was enough")
	}
	// More independent signals win: 6 against 4.
	l6 := "https://ats.example.com/magic-link?token=FAKE_Ab3dEf5hIjK"
	l4 := "https://other.example.net/auth?token=Zz9yXw8vUtS"
	body := "<p>Sign in</p>" + a(l4, "Sign in") + "<p>Sign in here: " + l6 + "</p>" + a(l6, "Sign in")
	m := SigninLink(SigninInput{HTML: body})
	if m == nil || m.URL != l6 {
		t.Fatalf("winner = %+v", m)
	}
	// Two qualifying links less than 2 points apart: none.
	tie := "<p>Sign in</p>" + a(l4, "Sign in") + a("https://third.example.net/auth?token=Qq1wEe2rTy", "Sign in")
	if SigninLink(SigninInput{HTML: tie}) != nil {
		t.Fatal("an unresolvable tie returned a link")
	}
}

func TestGmail_SigninLink_WrapperPenalizedNotRejected(t *testing.T) {
	real := "https://app.example.com/magic-link?token=FAKE_Ab3dEf5hIjK"
	wrapper := "https://abc123.r.awstrack.me/L0/" + "https:%2F%2Fapp.example.com%2Fmagic-link%3Ftoken%3DFAKE_Ab3dEf5hIjK/1/xyz"
	body := "<p>Sign in to your account</p>" + a(wrapper, "Sign in") + "<p>If the button fails, copy: " + real + "</p>"
	m := SigninLink(SigninInput{HTML: body})
	if m == nil || m.URL != real {
		t.Fatalf("the printed address must beat the wrapper: %+v", m)
	}
}

func TestGmail_SigninLink_TwoLinksOnOneHost(t *testing.T) {
	real := "https://ats.example.com/magic-link?token=FAKE_Ab3dEf5hIjK"
	optional := "https://ats.example.com/optional-password?token=Zz9yXw8vUtS"
	body := "<p>Sign in to your account:</p>" + a(real, "Sign in") + "<p>Or set an optional password instead:</p>" + a(optional, "Set password")
	m := SigninLink(SigninInput{HTML: body})
	if m == nil || m.URL != real {
		t.Fatalf("match = %+v", m)
	}
}
