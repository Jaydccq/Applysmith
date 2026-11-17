package api

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/vault"
)

const resolvePath = "/api/signin/resolve"

// seedCredential stores a fictional password and its index row.
func (h *harness) seedCredential(name, kind, host, email, password string) {
	h.t.Helper()
	if _, err := h.vault.Set(name, password); err != nil {
		h.t.Fatal(err)
	}
	stamp := h.clock.now().UTC().Format("2006-01-02T15:04:05.000Z")
	err := h.srv.store.Load().Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO credential_index (name, kind, host, email, has_secret, created_at, updated_at) VALUES (?, ?, NULLIF(?, ''), ?, 1, ?, ?)`,
			name, kind, host, email, stamp, stamp)
		return err
	})
	if err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) writeSQL(q string, args ...any) {
	h.t.Helper()
	err := h.srv.store.Load().Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(q, args...)
		return err
	})
	if err != nil {
		h.t.Fatal(err)
	}
}

// postWith sends a JSON request with a token taken earlier, so a test can make
// the keychain refuse every later read.
func (h *harness) postWith(tok, path string, body any) result {
	h.t.Helper()
	return h.do(call{method: "POST", path: path, bearer: tok, contentType: "application/json", body: jsonBody(body)})
}

func (h *harness) resolve(pageURL string) (wire.SigninCredential, result) {
	h.t.Helper()
	r := h.post(resolvePath, wire.SigninResolveRequest{URL: pageURL})
	var env wire.Success[wire.SigninCredential]
	if r.StatusCode == 200 {
		strictInto(h.t, r.body, &env)
	}
	return env.Data, r
}

func (h *harness) hold(host string, lockout bool) {
	h.t.Helper()
	d, err := h.srv.dom()
	if err != nil {
		h.t.Fatal(err)
	}
	err = h.srv.store.Load().Write(context.Background(), func(tx *sql.Tx) error {
		return d.creds.RecordExhausted(context.Background(), tx, host, lockout, "run-1")
	})
	if err != nil {
		h.t.Fatal(err)
	}
}

func seedSignin(h *harness) {
	h.seedCredential("signin-default", "signin_default", "", "alex@example.com", "Default-Pw-1!x")
	h.seedCredential("signin-site-careers.example.test", "signin_site", "careers.example.test", "alex.site@example.com", "Override-Pw-2!y")
	h.seedCredential("signin-alternate-0123456789ab", "signin_alternate", "", "alex@example.com", "Alternate-Pw-3!z")
}

func TestSigninResolveSiteOverrideFirst(t *testing.T) {
	h := newDomainHarness(t)
	seedSignin(h)
	got, r := h.resolve("https://careers.example.test/login")
	if r.StatusCode != 200 || got.Password != "Override-Pw-2!y" || got.Email != "alex.site@example.com" || got.Source != "settings-site" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestSigninResolveSharedDefaultForAHostWithoutAnOverrideNeverAnAlternate(t *testing.T) {
	h := newDomainHarness(t)
	seedSignin(h)
	got, r := h.resolve("https://jobs.example.test/login")
	if r.StatusCode != 200 || got.Password != "Default-Pw-1!x" || got.Email != "alex@example.com" || got.Source != "settings-default" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestSigninResolveSiteKeyIsTheLowercaseHostname(t *testing.T) {
	h := newDomainHarness(t)
	seedSignin(h)
	got, r := h.resolve("https://Careers.Example.TEST:8443/a/b?x=1#frag")
	if got.Source != "settings-site" || got.Password != "Override-Pw-2!y" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	// Neither the registrable domain nor a sibling host shares the override.
	for _, u := range []string{"https://www.careers.example.test/", "https://example.test/", "https://other.careers.example.test/x"} {
		if got, r := h.resolve(u); got.Source != "settings-default" {
			t.Errorf("%s: %d %s", u, r.StatusCode, r.body)
		}
	}
}

func TestSigninResolveSiteOverrideWithoutItsOwnEmailInheritsTheDefaultEmail(t *testing.T) {
	h := newDomainHarness(t)
	h.seedCredential("signin-default", "signin_default", "", "alex@example.com", "Default-Pw-1!x")
	h.seedCredential("signin-site-careers.example.test", "signin_site", "careers.example.test", "", "Override-Pw-2!y")
	got, r := h.resolve("https://careers.example.test/login")
	if got.Source != "settings-site" || got.Email != "alex@example.com" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestSigninResolveNothingStoredIsSecretNotFound(t *testing.T) {
	h := newDomainHarness(t)
	r := h.post(resolvePath, wire.SigninResolveRequest{URL: "https://jobs.example.test/login"})
	if r.StatusCode != 404 || r.errCode() != "not_found" || r.errReason() != "secret_not_found" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	// A password with no email to type beside it is no credential either.
	if _, err := h.vault.Set("signin-default", "Default-Pw-1!x"); err != nil {
		t.Fatal(err)
	}
	r = h.post(resolvePath, wire.SigninResolveRequest{URL: "https://jobs.example.test/login"})
	if r.StatusCode != 404 || r.errReason() != "secret_not_found" || strings.Contains(string(r.body), "Default-Pw") {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestSigninResolveKeychainTroubleKeepsItsOwnReason(t *testing.T) {
	h := newDomainHarness(t)
	seedSignin(h)
	tok := h.token()
	for name, c := range map[string]struct {
		err    error
		reason string
	}{
		"locked":   {vault.ErrDenied, "keychain_denied"},
		"no store": {vault.ErrUnavailable, "keychain_unavailable"},
	} {
		h.mem.FailWith(c.err)
		r := h.postWith(tok, resolvePath, wire.SigninResolveRequest{URL: "https://careers.example.test/login"})
		if r.StatusCode != 503 || r.errCode() != "unavailable" || r.errReason() != c.reason {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
	h.mem.FailWith(nil)
}

func TestSigninResolveIsForTheExtensionTokenOnLoopbackOnly(t *testing.T) {
	h := newDomainHarness(t)
	seedSignin(h)
	// With the keychain refusing, a forbidden answer proves the vault was not asked.
	h.mem.FailWith(vault.ErrDenied)
	body := jsonBody(wire.SigninResolveRequest{URL: "https://careers.example.test/login"})
	for name, c := range map[string]struct {
		kind apikit.PrincipalKind
		addr string
	}{
		"dashboard session": {apikit.PrincipalDashboard, "127.0.0.1:50000"},
		"remote device":     {apikit.PrincipalRemoteDevice, "127.0.0.1:50000"},
		"token off-host":    {apikit.PrincipalLocalToken, "203.0.113.9:50000"},
		"token no address":  {apikit.PrincipalLocalToken, ""},
	} {
		r := httptestRequest(t)
		r.RemoteAddr = c.addr
		_, err := h.srv.resolveSignin(&apikit.Call{P: apikit.Principal{Kind: c.kind}, R: r, Body: body})
		var ae *apikit.Error
		if !errors.As(err, &ae) || ae.Code != wire.CodeForbidden {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The same request from the extension's token on loopback reaches the vault.
	r := httptestRequest(t)
	r.RemoteAddr = "[::1]:50000"
	_, err := h.srv.resolveSignin(&apikit.Call{P: apikit.Principal{Kind: apikit.PrincipalLocalToken}, R: r, Body: body})
	var ae *apikit.Error
	if !errors.As(err, &ae) || ae.Details == nil || ae.Details.Reason != "keychain_denied" {
		t.Errorf("loopback token: %v", err)
	}
}

func TestSigninResolveRefusesAnUnusablePageAddressBeforeTheVault(t *testing.T) {
	h := newDomainHarness(t)
	seedSignin(h)
	tok := h.token()
	h.mem.FailWith(vault.ErrDenied)
	for name, u := range map[string]string{
		"empty":       "",
		"file":        "file:///etc/hosts",
		"ftp":         "ftp://careers.example.test/",
		"relative":    "/login",
		"credentials": "https://user:pw@careers.example.test/",
		"too long":    "https://careers.example.test/" + strings.Repeat("a", 2100),
	} {
		if r := h.postWith(tok, resolvePath, wire.SigninResolveRequest{URL: u}); r.StatusCode != 400 {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
	if r := h.postWith(tok, resolvePath, map[string]any{}); r.StatusCode != 400 && r.StatusCode != 422 {
		t.Errorf("no url: %d %s", r.StatusCode, r.body)
	}
}

func TestSigninResolveRefusesAHostWhoseCredentialsAreOnHold(t *testing.T) {
	h := newDomainHarness(t)
	seedSignin(h)
	h.hold("careers.example.test", false)
	tok := h.token()
	// A conflict, not a keychain error, proves the vault was not read.
	h.mem.FailWith(vault.ErrDenied)
	r := h.postWith(tok, resolvePath, wire.SigninResolveRequest{URL: "https://careers.example.test/login"})
	if r.StatusCode != 409 || r.errCode() != "conflict" || r.errReason() != wire.ReasonSigninHeld || !strings.Contains(string(r.body), "ran out") ||
		strings.Contains(string(r.body), "Override-Pw") {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	h.hold("careers.example.test", true)
	r = h.postWith(tok, resolvePath, wire.SigninResolveRequest{URL: "https://careers.example.test/login"})
	if r.StatusCode != 409 || r.errReason() != wire.ReasonSigninHeld || !strings.Contains(string(r.body), "locked") {
		t.Fatalf("lockout: %d %s", r.StatusCode, r.body)
	}
	// A later record the same day keeps the lockout flag.
	h.hold("careers.example.test", false)
	if r := h.postWith(tok, resolvePath, wire.SigninResolveRequest{URL: "https://careers.example.test/login"}); !strings.Contains(string(r.body), "locked") {
		t.Errorf("lockout lost: %s", r.body)
	}
	// Another host is not held.
	h.mem.FailWith(nil)
	if got, r := h.resolve("https://jobs.example.test/login"); r.StatusCode != 200 || got.Source != "settings-default" {
		t.Errorf("other host: %d %s", r.StatusCode, r.body)
	}
}

func TestSigninHoldEndsWithAChangedCredentialALaterSignInOrANewDay(t *testing.T) {
	stamp := func(h *harness, d time.Duration) string {
		return h.clock.now().Add(d).UTC().Format("2006-01-02T15:04:05.000Z")
	}
	cases := map[string]func(h *harness){
		"shared default changed after it": func(h *harness) {
			h.writeSQL(`INSERT INTO credential_meta (key, value) VALUES ('credentials_changed_at', ?)`, stamp(h, time.Minute))
		},
		"the host's override changed after it": func(h *harness) {
			h.writeSQL(`UPDATE credential_index SET updated_at = ? WHERE name = 'signin-site-careers.example.test'`, stamp(h, time.Minute))
		},
		"a later sign-in on the host": func(h *harness) {
			h.writeSQL(`UPDATE credential_index SET last_signin_at = ?, last_outcome = 'signed-in' WHERE name = 'signin-site-careers.example.test'`, stamp(h, time.Minute))
		},
		"recorded on an earlier day": func(h *harness) {
			h.writeSQL(`UPDATE signin_holds SET day = '2026-01-01'`)
		},
	}
	for name, lift := range cases {
		t.Run(name, func(t *testing.T) {
			h := newDomainHarness(t)
			seedSignin(h)
			h.hold("careers.example.test", false)
			if r := h.post(resolvePath, wire.SigninResolveRequest{URL: "https://careers.example.test/"}); r.StatusCode != 409 {
				t.Fatalf("not held first: %d %s", r.StatusCode, r.body)
			}
			lift(h)
			if got, r := h.resolve("https://careers.example.test/"); r.StatusCode != 200 || got.Source != "settings-site" {
				t.Fatalf("%d %s", r.StatusCode, r.body)
			}
		})
	}
	t.Run("a change made before the record does not lift it", func(t *testing.T) {
		h := newDomainHarness(t)
		seedSignin(h)
		h.writeSQL(`INSERT INTO credential_meta (key, value) VALUES ('credentials_changed_at', ?)`, stamp(h, -time.Hour))
		h.hold("careers.example.test", false)
		if r := h.post(resolvePath, wire.SigninResolveRequest{URL: "https://careers.example.test/"}); r.StatusCode != 409 {
			t.Fatalf("%d %s", r.StatusCode, r.body)
		}
	})
}

func TestSigninResolveFailsClosedWhenTheHoldRecordCannotBeRead(t *testing.T) {
	h := newDomainHarness(t)
	seedSignin(h)
	h.writeSQL(`ALTER TABLE signin_holds RENAME TO gone`)
	r := h.post(resolvePath, wire.SigninResolveRequest{URL: "https://careers.example.test/login"})
	if r.StatusCode != 503 || r.errReason() != wire.ReasonSigninHoldUnreadable || strings.Contains(string(r.body), "Override-Pw") {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}
