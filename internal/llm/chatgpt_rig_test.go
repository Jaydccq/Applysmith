// Clean-room: docs/provenance.md#CR-SUBSCRIPTIONS
// Written from OpenAI's public Sign in with ChatGPT documentation and the
// research spec (section 1a) alone.

package llm

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOpenAI is a stand-in for auth.openai.com and the plan-usage API, written
// from the published protocol: it is the oracle of the sign-in tests, so it
// checks what the documents require of the client (PKCE, the issued client id,
// the resource, no scope on refresh) and rotates refresh tokens on every use.
type fakeOpenAI struct {
	t   *testing.T
	srv *httptest.Server
	now func() time.Time
	key *rsa.PrivateKey

	mu      sync.Mutex
	issued  string
	grants  map[string]fakeGrant
	live    map[string]bool // refresh tokens that still work
	seq     int
	tokens  []url.Values // every request to the token endpoint
	revokes []url.Values

	// knobs
	scope        string // granted scope
	idAud        string // ID token audience, "" for the issued client
	idSub        string // ID token subject
	idEmail      string
	idNonce      string // "" for the nonce of the grant
	idExpiry     time.Duration
	idKey        *rsa.PrivateKey // signs the ID token, the server key if nil
	omitClientID bool            // the callback carries no issued client id
	refreshFail  func(form url.Values) (int, string)
	refreshGate  func() // runs inside a refresh before it answers
	revokeStatus []int  // statuses for successive revocations, then 200
	api          http.HandlerFunc
}

type fakeGrant struct {
	challenge, redirect, nonce string
}

var (
	rigKeyOnce  sync.Once
	rigKeys     [2]*rsa.PrivateKey
	rigKeyError error
)

func rigKey(i int) *rsa.PrivateKey {
	rigKeyOnce.Do(func() {
		for j := range rigKeys {
			rigKeys[j], rigKeyError = rsa.GenerateKey(rand.Reader, 2048)
		}
	})
	if rigKeyError != nil {
		panic(rigKeyError)
	}
	return rigKeys[i]
}

func newFakeOpenAI(t *testing.T) *fakeOpenAI {
	t.Helper()
	f := &fakeOpenAI{t: t, key: rigKey(0), issued: "client_issued_1", grants: map[string]fakeGrant{}, live: map[string]bool{},
		scope: chatgptScope, idSub: "user-1", idEmail: "me@example.test", idExpiry: time.Hour}
	mux := http.NewServeMux()
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issuer":%q,"jwks_uri":%q,"revocation_endpoint":%q}`, f.srv.URL, f.srv.URL+"/jwks", f.srv.URL+"/revoke")
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		pub := f.key.PublicKey
		fmt.Fprintf(w, `{"keys":[{"kty":"RSA","kid":"k1","alg":"RS256","n":%q,"e":%q}]}`,
			base64.RawURLEncoding.EncodeToString(pub.N.Bytes()), base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()))
	})
	mux.HandleFunc("/api/accounts/oauth/token", f.token)
	mux.HandleFunc("/revoke", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.revokes = append(f.revokes, r.PostForm)
		status := http.StatusOK
		if len(f.revokeStatus) > 0 {
			status, f.revokeStatus = f.revokeStatus[0], f.revokeStatus[1:]
		}
		f.mu.Unlock()
		w.WriteHeader(status)
	})
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		if f.api == nil {
			http.NotFound(w, r)
			return
		}
		f.api(w, r)
	})
	return f
}

func (f *fakeOpenAI) options() []Option {
	return []Option{WithChatGPTIssuer(f.srv.URL), WithChatGPTRefresh()}
}

func (f *fakeOpenAI) jwt(claims map[string]any, key *rsa.PrivateKey) string {
	enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	signed := enc(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"}) + "." + enc(claims)
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		f.t.Fatal(err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// access mints an access token that expires after ttl.
func (f *fakeOpenAI) access(ttl time.Duration) string {
	f.mu.Lock()
	f.seq++
	n := f.seq
	f.mu.Unlock()
	now := f.now()
	return f.jwt(map[string]any{"sub": f.idSub, "aud": chatgptResource, "client_id": f.issued, "scope": f.scope, "iss": f.srv.URL,
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(ttl).Unix(), "jti": fmt.Sprintf("jti-%d", n)}, f.key)
}

func (f *fakeOpenAI) newRefresh() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	rt := fmt.Sprintf("rt-%d", f.seq)
	f.live[rt] = true
	return rt
}

func (f *fakeOpenAI) tokenBody(nonce string) string {
	aud := f.idAud
	if aud == "" {
		aud = f.issued
	}
	if f.idNonce != "" {
		nonce = f.idNonce
	}
	signer := f.idKey
	if signer == nil {
		signer = f.key
	}
	now := f.now()
	id := f.jwt(map[string]any{"iss": f.srv.URL, "sub": f.idSub, "aud": aud, "email": f.idEmail, "nonce": nonce,
		"iat": now.Unix(), "exp": now.Add(f.idExpiry).Unix()}, signer)
	b, _ := json.Marshal(map[string]any{"access_token": f.access(time.Hour), "refresh_token": f.newRefresh(), "id_token": id,
		"token_type": "Bearer", "expires_in": 3600, "scope": f.scope, "earliest_refresh_at": "2026-01-15T12:50:00Z"})
	return string(b)
}

func (f *fakeOpenAI) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	form := r.PostForm
	f.mu.Lock()
	f.tokens = append(f.tokens, form)
	f.mu.Unlock()
	reply := func(status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}
	fail := func(code string) { reply(http.StatusBadRequest, fmt.Sprintf(`{"error":%q}`, code)) }
	if got := form.Get("client_id"); got != f.issued {
		f.t.Errorf("token request uses client_id %q, want the issued %q (dynamic_agent_client is the registration entrypoint only)", got, f.issued)
	}
	if got := form.Get("resource"); got != chatgptResource {
		f.t.Errorf("token request resource = %q", got)
	}
	switch form.Get("grant_type") {
	case "authorization_code":
		f.mu.Lock()
		g, ok := f.grants[form.Get("code")]
		delete(f.grants, form.Get("code"))
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(form.Get("code_verifier")))
		switch {
		case !ok:
			fail("invalid_grant")
		case base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge:
			fail("invalid_grant")
		case form.Get("redirect_uri") != g.redirect:
			fail("invalid_grant")
		default:
			reply(http.StatusOK, f.tokenBody(g.nonce))
		}
	case "refresh_token":
		if form.Has("scope") {
			f.t.Errorf("a refresh must omit scope, got %q", form.Get("scope"))
		}
		if f.refreshGate != nil {
			f.refreshGate()
		}
		if f.refreshFail != nil {
			if status, body := f.refreshFail(form); status != 0 {
				reply(status, body)
				return
			}
		}
		f.mu.Lock()
		ok := f.live[form.Get("refresh_token")]
		delete(f.live, form.Get("refresh_token")) // rotation: a refresh token works once
		f.mu.Unlock()
		if !ok {
			fail("refresh_token_reused")
			return
		}
		reply(http.StatusOK, f.tokenBody(""))
	default:
		fail("unsupported_grant_type")
	}
}

func (f *fakeOpenAI) tokenCalls(grant string) []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []url.Values
	for _, v := range f.tokens {
		if v.Get("grant_type") == grant {
			out = append(out, v)
		}
	}
	return out
}

// chatgptEnv is a router over the fake with the chatgpt provider configured.
func chatgptEnv(t *testing.T) (*env, *fakeOpenAI) {
	t.Helper()
	f := newFakeOpenAI(t)
	e := newEnv(t, f.options()...)
	f.now = e.clock.now
	pr, _ := PresetByName("chatgpt")
	p := pr.ProviderConfig()
	p.BaseURL = f.srv.URL + "/v1"
	e.provider(p, "")
	return e, f
}

// browser plays the user's browser: it takes the sign-in address the app
// opened and answers like the issuer would, then calls the loopback listener.
type browser struct {
	t        *testing.T
	f        *fakeOpenAI
	auth     url.Values
	redirect string
}

func (e *env) beginSignIn(f *fakeOpenAI) *browser {
	e.t.Helper()
	raw, err := e.r.BeginChatGPTSignIn(bg())
	if err != nil {
		e.t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		e.t.Fatal(err)
	}
	if want := f.srv.URL + "/api/accounts/authorize"; u.Scheme+"://"+u.Host+u.Path != want {
		e.t.Fatalf("sign-in address %s, want %s", raw, want)
	}
	q := u.Query()
	return &browser{t: e.t, f: f, auth: q, redirect: q.Get("redirect_uri")}
}

// consent registers a code for the sign-in and returns the callback query the
// issuer would redirect with.
func (b *browser) consent() url.Values {
	b.f.mu.Lock()
	defer b.f.mu.Unlock()
	b.f.seq++
	code := fmt.Sprintf("code-%d", b.f.seq)
	b.f.grants[code] = fakeGrant{challenge: b.auth.Get("code_challenge"), redirect: b.redirect, nonce: b.auth.Get("nonce")}
	q := url.Values{"code": {code}, "state": {b.auth.Get("state")}, "scope": {b.f.scope}}
	if !b.f.omitClientID && b.auth.Get("client_id") == chatgptEntryID {
		q.Set("client_id", b.f.issued)
	}
	return q
}

// callback sends the redirect to the loopback listener and returns the status
// and body of the page the user sees.
func (b *browser) callback(q url.Values) (int, string) {
	b.t.Helper()
	resp, err := http.Get(b.redirect + "?" + q.Encode())
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// signIn runs a whole successful sign-in.
func (e *env) signIn(f *fakeOpenAI) {
	e.t.Helper()
	b := e.beginSignIn(f)
	if status, body := b.callback(b.consent()); status != http.StatusOK {
		e.t.Fatalf("callback answered %d: %s", status, body)
	}
	st, err := e.r.ChatGPTStatus(bg())
	if err != nil || !st.PlanUsage {
		e.t.Fatalf("status = %+v, err %v", st, err)
	}
}

func (e *env) refreshToken() string {
	e.t.Helper()
	v, err := e.vault.Get(vaultChatGPTRefresh)
	if err != nil {
		e.t.Fatal(err)
	}
	return v
}

func (e *env) hasToken(name string) bool {
	_, err := e.vault.Get(name)
	return err == nil
}

func contains(s string, words ...string) bool {
	for _, w := range words {
		if !strings.Contains(s, w) {
			return false
		}
	}
	return true
}
