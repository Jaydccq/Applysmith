package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"applysmith/internal/api/wire"
	"applysmith/internal/vault"
)

const (
	pwDefault = "Default-Pw-1!xyz"
	pwSite    = "Site-Canary-2"
	pwAlt     = "Alt-Canary-3"
)

func (h *harness) secretList() wire.SecretList {
	h.t.Helper()
	r := h.get("/api/secrets")
	if r.StatusCode != 200 {
		h.t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	return decode[wire.SecretList](h.t, r)
}

func (h *harness) putSecretValue(name string, body wire.SecretWrite) result {
	h.t.Helper()
	return h.send("PUT", "/api/secrets/"+name, body)
}

// stored lists the keychain accounts except the server's own token.
func (h *harness) stored() []string {
	var out []string
	for _, a := range h.mem.Accounts() {
		if !strings.HasSuffix(a, ":"+vault.APIToken) {
			out = append(out, a)
		}
	}
	return out
}

// sendWith sends with a token taken earlier, so a test can make the keychain
// refuse every later read.
func (h *harness) sendWith(tok, method, path string, body any) result {
	h.t.Helper()
	c := call{method: method, path: path, bearer: tok}
	if body != nil {
		c.contentType, c.body = "application/json", jsonBody(body)
	}
	return h.do(c)
}

func TestAPI_Secrets_RoutesRegistered(t *testing.T) {
	want := []string{
		"GET /api/secrets", "PUT /api/secrets/{name}", "DELETE /api/secrets/{name}", "POST /api/secrets/{name}/test",
		"POST /api/secrets/alternates", "PUT /api/secrets/alternates/order", "DELETE /api/secrets/alternates/{id}",
	}
	byPattern := map[string]Route{}
	for _, r := range Routes() {
		byPattern[r.Pattern()] = r
	}
	for _, p := range want {
		r, ok := byPattern[p]
		if !ok {
			t.Errorf("%s is not registered", p)
			continue
		}
		if r.Access != AccessAdmin || !r.Secret || r.Remote != RemoteDenied {
			t.Errorf("%s: access %s secret %v remote %s; want admin, secret, never remote", p, r.Access, r.Secret, r.Remote)
		}
	}
	// Exactly one route returns a password; the secrets routes return none.
	for _, r := range Routes() {
		if strings.HasPrefix(r.Path, "/api/secrets") && (r.Response == "SigninCredential" || r.Response == "OneTimeCode") {
			t.Errorf("%s returns a credential", r.Pattern())
		}
	}
}

func TestAPI_Secrets_ListNeverCarriesValue(t *testing.T) {
	h := newDomainHarness(t)
	for _, c := range []struct {
		name string
		body wire.SecretWrite
	}{
		{"signin-default", wire.SecretWrite{Value: pwDefault, Email: "alex@example.com"}},
		{"signin-site-careers.example.test", wire.SecretWrite{Value: pwSite}},
		{"cloudflared-tunnel-token", wire.SecretWrite{Value: "tunnel-canary-key-4"}},
	} {
		if r := h.putSecretValue(c.name, c.body); r.StatusCode != 200 {
			t.Fatalf("%s: %d %s", c.name, r.StatusCode, r.body)
		}
	}
	r := h.get("/api/secrets")
	for _, v := range []string{pwDefault, pwSite, "tunnel-canary-key-4"} {
		if strings.Contains(string(r.body), v) {
			t.Fatalf("the list carries a value: %s", r.body)
		}
	}
	by := map[string]wire.SecretEntry{}
	for _, e := range decode[wire.SecretList](t, r).Entries {
		by[e.Name] = e
	}
	for _, name := range []string{"signin-default", "signin-site-careers.example.test", "cloudflared-tunnel-token"} {
		if e := by[name]; !e.Present || e.Source != "keychain" {
			t.Errorf("%s = %+v", name, e)
		}
	}
	if e := by["signin-default"]; e.Email != "alex@example.com" || e.Kind != "signin_default" {
		t.Errorf("default = %+v", e)
	}
	if e := by["signin-site-careers.example.test"]; e.Host != "careers.example.test" || e.Email != "" {
		t.Errorf("override = %+v", e)
	}
	if reg := decode[wire.SecretList](t, r).Registry; len(reg) != len(vault.Registry()) {
		t.Errorf("registry rows = %d", len(reg))
	}
}

func TestAPI_Secrets_WeakSharedDefaultIsRefusedWithoutEchoingOrStoring(t *testing.T) {
	h := newDomainHarness(t)
	r := h.putSecretValue("signin-default", wire.SecretWrite{Value: "password1", Email: "alex@example.com"})
	if r.StatusCode != 400 || r.errCode() != "bad_request" || r.errReason() != "weak_password" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	for _, rule := range []string{"at least 12 characters", "an uppercase letter", "a symbol"} {
		if !strings.Contains(string(r.body), rule) {
			t.Errorf("the answer does not name %q: %s", rule, r.body)
		}
	}
	if strings.Contains(string(r.body), "password1") || len(h.stored()) != 0 {
		t.Fatalf("echoed or stored: accounts %v body %s", h.stored(), r.body)
	}
}

func TestAPI_Secrets_WeakOverrideIsAccepted(t *testing.T) {
	h := newDomainHarness(t)
	if r := h.putSecretValue("signin-default", wire.SecretWrite{Email: "alex@example.com"}); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if len(h.stored()) != 0 {
		t.Fatalf("an email-only default touched the keychain: %v", h.stored())
	}
	if r := h.putSecretValue("signin-site-capped.example.test", wire.SecretWrite{Value: "short1"}); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if got, _ := h.vault.Get("signin-site-capped.example.test"); got != "short1" {
		t.Fatalf("stored %q", got)
	}
}

func TestAPI_Secrets_AlternateNeedsEmail(t *testing.T) {
	h := newDomainHarness(t)
	r := h.send("POST", "/api/secrets/alternates", wire.AlternateWrite{Value: pwAlt})
	if r.StatusCode != 400 || r.errReason() != "email_required" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	r = h.putSecretValue("signin-site-careers.example.test", wire.SecretWrite{Value: pwSite})
	if r.StatusCode != 400 || r.errReason() != "email_required" {
		t.Fatalf("override: %d %s", r.StatusCode, r.body)
	}
	if len(h.stored()) != 0 {
		t.Fatalf("a refused request stored %v", h.stored())
	}
	if r := h.putSecretValue("signin-default", wire.SecretWrite{Email: "alex@example.com"}); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	one := decode[wire.AlternateAdded](t, h.send("POST", "/api/secrets/alternates", wire.AlternateWrite{Value: pwAlt}))
	two := decode[wire.AlternateAdded](t, h.send("POST", "/api/secrets/alternates", wire.AlternateWrite{Value: pwAlt + "b", Label: "Old laptop"}))
	if len(one.ID) != 12 || one.Name != "signin-alternate-"+one.ID || one.ID == two.ID {
		t.Fatalf("ids %+v %+v", one, two)
	}
	order := func() []string {
		var ids []string
		for _, e := range h.secretList().Entries {
			if e.Kind == "signin_alternate" {
				ids = append(ids, e.Label)
			}
		}
		return ids
	}
	if got := strings.Join(order(), ","); got != "Alternate 1,Old laptop" {
		t.Fatalf("order = %s", got)
	}
	if r := h.send("PUT", "/api/secrets/alternates/order", wire.AlternateOrder{IDs: []string{two.ID}}); r.StatusCode != 400 {
		t.Fatalf("a partial order: %d %s", r.StatusCode, r.body)
	}
	if r := h.send("PUT", "/api/secrets/alternates/order", wire.AlternateOrder{IDs: []string{two.ID, one.ID}}); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if got := strings.Join(order(), ","); got != "Old laptop,Alternate 1" {
		t.Fatalf("order after = %s", got)
	}
	if r := h.send("DELETE", "/api/secrets/alternates/"+one.ID, nil); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if got := strings.Join(order(), ","); got != "Old laptop" {
		t.Fatalf("order after delete = %s", got)
	}
	if r := h.send("DELETE", "/api/secrets/alternates/"+one.ID, nil); r.StatusCode != 404 {
		t.Fatalf("deleting it twice: %d %s", r.StatusCode, r.body)
	}
}

func TestAPI_Secrets_RejectedBodyNotEchoed(t *testing.T) {
	h := newDomainHarness(t)
	canary := "Body-Canary-Pw-9!"
	for name, tc := range map[string]struct {
		method, path string
		body         any
	}{
		"unknown field":    {"PUT", "/api/secrets/signin-default", map[string]any{"value": canary, "bogus": 1}},
		"wrong type":       {"PUT", "/api/secrets/signin-default", map[string]any{"value": 12, "email": canary}},
		"bad email":        {"PUT", "/api/secrets/signin-default", wire.SecretWrite{Value: canary, Email: canary}},
		"long label":       {"POST", "/api/secrets/alternates", wire.AlternateWrite{Value: canary, Label: strings.Repeat("l", 41)}},
		"unregistered":     {"PUT", "/api/secrets/misc-" + "token", wire.SecretWrite{Value: canary}},
		"alternate by put": {"PUT", "/api/secrets/signin-alternate-0123456789ab", wire.SecretWrite{Value: canary}},
	} {
		t.Run(name, func(t *testing.T) {
			r := h.send(tc.method, tc.path, tc.body)
			if r.StatusCode < 400 || r.StatusCode >= 500 {
				t.Fatalf("%d %s", r.StatusCode, r.body)
			}
			if strings.Contains(string(r.body), canary) {
				t.Fatalf("the rejected body was echoed: %s", r.body)
			}
		})
	}
	if strings.Contains(h.logText(), canary) {
		t.Fatal("a rejected value reached the log")
	}
	if len(h.stored()) != 0 {
		t.Fatalf("a rejected request stored %v", h.stored())
	}
}

func TestAPI_Secrets_UnavailableIsNotEmpty(t *testing.T) {
	h := newDomainHarness(t)
	if r := h.putSecretValue("cloudflared-tunnel-token", wire.SecretWrite{Value: "tunnel-canary-key-4"}); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	tok := h.token()
	for _, tc := range []struct {
		fail   error
		reason string
	}{{vault.ErrDenied, "keychain_denied"}, {vault.ErrUnavailable, "keychain_unavailable"}} {
		h.mem.FailWith(tc.fail)
		put := h.sendWith(tok, "PUT", "/api/secrets/cloudflared-tunnel-token", wire.SecretWrite{Value: "tunnel-canary-key-5"})
		if put.StatusCode != 503 || put.errReason() != tc.reason {
			t.Errorf("%s put: %d %s", tc.reason, put.StatusCode, put.body)
		}
		test := h.sendWith(tok, "POST", "/api/secrets/cloudflared-tunnel-token/test", nil)
		if test.StatusCode != 503 || test.errReason() != tc.reason {
			t.Errorf("%s test: %d %s", tc.reason, test.StatusCode, test.body)
		}
		list := h.sendWith(tok, "GET", "/api/secrets", nil)
		for _, e := range decode[wire.SecretList](t, list).Entries {
			if e.Name == "cloudflared-tunnel-token" && (e.Present || e.Problem != tc.reason) {
				t.Errorf("%s list: %+v", tc.reason, e)
			}
		}
	}
	h.mem.FailWith(nil)
	// Nothing stored is a normal answer, distinct from trouble.
	if r := h.send("POST", "/api/secrets/github-token/test", nil); r.StatusCode != 200 || r.data()["live"] != "not_found" || r.data()["present"] != false {
		t.Errorf("absent: %d %s", r.StatusCode, r.body)
	}
	if r := h.send("POST", "/api/secrets/cloudflared-tunnel-token/test", nil); r.StatusCode != 200 || r.data()["present"] != true || r.data()["live"] != "unsupported" || strings.Contains(string(r.body), "tunnel-canary") {
		t.Errorf("present: %d %s", r.StatusCode, r.body)
	}
	if r := h.send("DELETE", "/api/secrets/github-token", nil); r.StatusCode != 404 || r.errReason() != "secret_not_found" {
		t.Errorf("delete absent: %d %s", r.StatusCode, r.body)
	}
}

// Testing the captcha key reads the balance once (the provider's getBalance
// reply and error codes) and holds it to the solver's USD 0.50 floor: a
// balance under it, not at it, leaves challenges to the user. The key never
// comes back.
func TestAPI_Secrets_CaptchaKeyTestReadsTheBalance(t *testing.T) {
	const key = "captcha-canary-key-7"
	var reply string
	hits := 0
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = io.WriteString(w, reply)
	}))
	defer svc.Close()
	h := newDomainHarness(t, func(c *Config) { c.Domain.CaptchaAPI = svc.URL })
	if r := h.putSecretValue(vault.CaptchaKey, wire.SecretWrite{Value: key}); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	for _, tc := range []struct {
		reply, live string
		cents       wire.SafeInt
		below       bool
	}{
		{`{"errorId":0,"balance":3.21}`, "ok", 321, false},
		{`{"errorId":0,"balance":0.5}`, "ok", 50, false},
		{`{"errorId":0,"balance":0.49}`, "ok", 49, true},
		{`{"errorId":1,"errorCode":"ERROR_KEY_DOES_NOT_EXIST"}`, "rejected", 0, false},
		{`{"errorId":1,"errorCode":"ERROR_INTERNAL"}`, "failed", 0, false},
	} {
		reply, hits = tc.reply, 0
		r := h.send("POST", "/api/secrets/"+vault.CaptchaKey+"/test", nil)
		got := decode[wire.SecretTest](t, r)
		if got.Live != tc.live || got.BelowFloor != tc.below || (got.BalanceCents != nil) != (tc.live == "ok") || hits != 1 || strings.Contains(string(r.body), key) {
			t.Errorf("%s: %d hits, %s", tc.reply, hits, r.body)
		}
		if got.BalanceCents != nil && *got.BalanceCents != tc.cents {
			t.Errorf("%s: balance %v", tc.reply, *got.BalanceCents)
		}
	}
}

func TestAPI_Secrets_ProviderKeyIsCheckedLikeTheModelsRoute(t *testing.T) {
	h := newDomainHarness(t)
	if r := h.putSecretValue("api-key-openrouter", wire.SecretWrite{Value: "<your-api-key-here>"}); r.StatusCode != 400 || r.errReason() != "key_invalid" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if r := h.putSecretValue("api-key-openrouter", wire.SecretWrite{Value: testSecret}); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if got, _ := h.vault.Get("api-key-openrouter"); got != testSecret {
		t.Fatal("the key was not stored under the registered name")
	}
	if r := h.send("DELETE", "/api/secrets/api-key-openrouter", nil); r.StatusCode != 200 || r.data()["present"] != false {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestAPI_Secrets_DashboardSessionReachesTheRoutesAndNoValueIsLogged(t *testing.T) {
	h := newDomainHarness(t)
	cookie := h.session()
	r := h.do(call{method: "GET", path: "/api/secrets", cookie: cookie})
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if r := h.putSecretValue("signin-default", wire.SecretWrite{Value: pwDefault, Email: "alex@example.com"}); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if logs := h.logText(); strings.Contains(logs, pwDefault) {
		t.Fatal("a password reached the request log")
	}
}
