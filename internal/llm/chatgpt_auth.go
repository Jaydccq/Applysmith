// Clean-room: docs/provenance.md#CR-SUBSCRIPTIONS
// Written from OpenAI's public Sign in with ChatGPT documentation and the
// research spec (section 1a) alone.

package llm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"applysmith/internal/vault"
)

// Sign in with ChatGPT, plan usage. The server owns one token family: the
// refresh token rotates on every use, so a second refresher would invalidate
// the first. Only a router built with WithChatGPTRefresh (the server's) calls
// the token endpoint with the refresh token; every other process uses the
// access token while it lasts and otherwise says that the app renews it.
//
// The tokens live in the vault (chatgpt-refresh, chatgpt-access). What is not
// secret and must survive a sign-out (the host id, the issued client id, the
// account label) is one llm_settings row, key "chatgpt".

const (
	chatgptIssuer    = "https://auth.openai.com"
	chatgptEntryID   = "dynamic_agent_client"
	chatgptResource  = "https://api.openai.com/v1"
	chatgptAppName   = "Applysmith"
	chatgptScope     = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
	chatgptPlanScope = "chatgpt.tokens.use.direct"
	chatgptCallback  = "/auth/callback"

	vaultChatGPTRefresh = "chatgpt-refresh"
	vaultChatGPTAccess  = "chatgpt-access"
	keyChatGPT          = "chatgpt"

	// chatgptSkew is how long before expiry an access token is renewed.
	chatgptSkew = 2 * time.Minute
	// chatgptSignInWait is how long a sign-in waits for the browser.
	chatgptSignInWait = 10 * time.Minute
	// chatgptRefreshLimit bounds the token request of a refresh or a sign-in.
	chatgptRefreshLimit = 30 * time.Second
)

// chatgptReg is what the app keeps about the registration, tokens excluded.
// The host id and the client id stay after a sign-out, so signing in again
// reuses them.
type chatgptReg struct {
	HostID   string   `json:"host_id"`
	ClientID string   `json:"client_id,omitempty"`
	Subject  string   `json:"subject,omitempty"`
	Email    string   `json:"email,omitempty"`
	Scopes   []string `json:"scopes,omitempty"`
	SignedIn bool     `json:"signed_in"`
	// Message says why the sign-in ended or what it lacks.
	Message string `json:"message,omitempty"`
}

func (g chatgptReg) planUsage() bool {
	return g.SignedIn && slices.Contains(g.Scopes, chatgptPlanScope)
}

// ChatGPTStatus is the sign-in state shown to the user. It holds no token.
type ChatGPTStatus struct {
	SignedIn bool
	// PlanUsage is false for a sign-in that did not grant the plan-usage scope.
	PlanUsage bool
	// Account is the email of the signed-in account.
	Account string
	// Pending says a sign-in waits for the browser.
	Pending bool
	// Message explains a signed-out state or the failure of the last attempt.
	Message string
}

type chatgptState struct {
	// issuer replaces the OpenAI issuer in tests.
	issuer string
	// owner is set on the server's router: the only one that refreshes.
	owner bool
	// mu serializes token use: the refresh, a sign-in's save and a sign-out.
	mu        sync.Mutex
	access    string
	accessExp time.Time
	// pmu guards the sign-in attempt and its last failure.
	pmu      sync.Mutex
	pending  *chatgptAttempt
	lastFail string
	models   chatgptModelCache
}

// WithChatGPTRefresh makes this router the owner of the token family: it
// renews the access token with the refresh token. Only the server installs it.
func WithChatGPTRefresh() Option { return func(r *Router) { r.chatgpt.owner = true } }

// WithChatGPTIssuer replaces the OpenAI issuer URL, for tests.
func WithChatGPTIssuer(u string) Option { return func(r *Router) { r.chatgpt.issuer = u } }

func (r *Router) chatgptRegistration(ctx context.Context) (chatgptReg, error) {
	var reg chatgptReg
	var raw string
	err := r.store.Read().QueryRowContext(ctx, `SELECT value FROM llm_settings WHERE key = ?`, keyChatGPT).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return reg, nil
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &reg)
	}
	return reg, err
}

func (r *Router) saveChatGPTRegistration(ctx context.Context, reg chatgptReg) error {
	b, err := json.Marshal(reg)
	if err != nil {
		return err
	}
	return r.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO llm_settings (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, keyChatGPT, string(b), timeText(r.now()))
		return err
	})
}

// ChatGPTStatus reports the sign-in state without touching the keychain.
func (r *Router) ChatGPTStatus(ctx context.Context) (ChatGPTStatus, error) {
	reg, err := r.chatgptRegistration(ctx)
	if err != nil {
		return ChatGPTStatus{}, err
	}
	c := &r.chatgpt
	c.pmu.Lock()
	defer c.pmu.Unlock()
	st := ChatGPTStatus{SignedIn: reg.SignedIn, PlanUsage: reg.planUsage(), Account: reg.Email, Pending: c.pending != nil, Message: reg.Message}
	if c.lastFail != "" {
		st.Message = c.lastFail
	}
	return st, nil
}

// chatgptSignedOutMessage is the error text of a call made without a usable sign-in.
func chatgptSignedOutMessage(reg chatgptReg) string {
	msg := "not signed in to ChatGPT; sign in under Settings, Providers or with `applysmith llm signin chatgpt`"
	if reg.SignedIn && !reg.planUsage() {
		msg = "the ChatGPT sign-in does not include plan usage; sign in again and allow it under Settings, Providers or with `applysmith llm signin chatgpt`"
	}
	if reg.Message != "" && !reg.SignedIn {
		msg += " (" + reg.Message + ")"
	}
	return msg
}

// chatgptToken returns an access token for a call: the one in memory, else
// the one in the vault, else a refreshed one (owner only).
func (r *Router) chatgptToken(ctx context.Context) (string, error) {
	c := &r.chatgpt
	c.mu.Lock()
	defer c.mu.Unlock()
	reg, err := r.chatgptRegistration(ctx)
	if err != nil {
		return "", &Error{Kind: KindOther, Message: err.Error()}
	}
	if !reg.planUsage() {
		return "", &Error{Kind: KindUnconfigured, Message: chatgptSignedOutMessage(reg)}
	}
	fresh := func(exp time.Time) bool { return r.now().Add(chatgptSkew).Before(exp) }
	if c.access != "" && fresh(c.accessExp) {
		return c.access, nil
	}
	if tok, err := r.keys.Get(vaultChatGPTAccess); err == nil {
		if exp, ok := jwtExpiry(tok); ok && fresh(exp) {
			c.access, c.accessExp = tok, exp
			return tok, nil
		}
	}
	if !c.owner {
		return "", &Error{Kind: KindUnconfigured, Message: "the ChatGPT access token has expired, and only the running app renews it; start the app, or run `applysmith llm test chatgpt` while it runs"}
	}
	return r.refreshChatGPT(ctx, reg)
}

// refreshChatGPT trades the refresh token for a new family and saves it. The
// caller holds chatgpt.mu. The request and the save are not cancelled with the
// caller: a refresh cut off in the middle can leave the rotated token unsaved.
func (r *Router) refreshChatGPT(ctx context.Context, reg chatgptReg) (string, error) {
	refresh, err := r.keys.Get(vaultChatGPTRefresh)
	if err != nil {
		if vault.IsCode(err, vault.CodeNotFound) {
			msg := "the stored refresh token is missing"
			r.endChatGPT(ctx, reg, msg)
			return "", &Error{Kind: KindUnconfigured, Message: chatgptSignedOutMessage(chatgptReg{Message: msg})}
		}
		return "", &Error{Kind: KindUnconfigured, Message: "the ChatGPT refresh token cannot be read from the keychain"}
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), chatgptRefreshLimit)
	defer cancel()
	tok, err := r.tokenRequest(rctx, url.Values{"grant_type": {"refresh_token"}, "client_id": {reg.ClientID},
		"refresh_token": {refresh}, "resource": {chatgptResource}})
	var oe *oauthError
	switch {
	case errors.As(err, &oe) && (terminalRefresh[oe.Code] || oe.Code == "invalid_client"):
		msg := "ChatGPT ended the sign-in (" + oe.Code + ")"
		if oe.Code == "invalid_client" {
			reg.ClientID = "" // the registration is gone: the next sign-in registers again
		}
		r.endChatGPT(rctx, reg, msg)
		return "", &Error{Kind: KindUnconfigured, Message: chatgptSignedOutMessage(chatgptReg{Message: msg})}
	case err != nil:
		if errors.As(err, &oe) {
			return "", &Error{Kind: KindAuth, Status: oe.Status, Message: "ChatGPT refused to renew the sign-in: " + oe.Error()}
		}
		return "", err
	}
	if err := r.storeChatGPTTokens(tok.AccessToken, tok.RefreshToken, refresh); err != nil {
		r.log.Error("llm: the renewed ChatGPT tokens could not be saved", "err", err)
	}
	if s := strings.Fields(tok.Scope); len(s) > 0 && !slices.Equal(s, reg.Scopes) {
		reg.Scopes = s
		_ = r.saveChatGPTRegistration(rctx, reg)
	}
	return r.cacheChatGPTAccess(tok), nil
}

func (r *Router) cacheChatGPTAccess(tok chatgptTokens) string {
	c := &r.chatgpt
	exp, ok := jwtExpiry(tok.AccessToken)
	if !ok {
		exp = r.now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	}
	c.access, c.accessExp = tok.AccessToken, exp
	return tok.AccessToken
}

// storeChatGPTTokens saves the rotated refresh token first, then the access
// token. An access token the keychain refuses (too large) stays in memory only.
// A ChatGPT access token is longer than the keychain takes (2 KB), so that is
// the usual case, not a fault: the running app, the only one that renews it,
// keeps it.
func (r *Router) storeChatGPTTokens(access, refresh, previous string) error {
	if refresh == "" {
		refresh = previous
	}
	if _, err := r.keys.Set(vaultChatGPTRefresh, refresh); err != nil {
		return fmt.Errorf("refresh token: %w", err)
	}
	if _, err := r.keys.Set(vaultChatGPTAccess, access); err != nil {
		_ = r.keys.Delete(vaultChatGPTAccess)
		r.log.Info("llm: the ChatGPT access token is kept in memory only; other processes cannot use it", "err", err)
	}
	return nil
}

// endChatGPT ends the sign-in locally: the tokens go, the registration stays.
// The caller holds chatgpt.mu.
func (r *Router) endChatGPT(ctx context.Context, reg chatgptReg, message string) {
	c := &r.chatgpt
	c.access, c.accessExp = "", time.Time{}
	_ = r.keys.Delete(vaultChatGPTRefresh)
	_ = r.keys.Delete(vaultChatGPTAccess)
	reg.SignedIn, reg.Message = false, message
	if err := r.saveChatGPTRegistration(ctx, reg); err != nil {
		r.log.Error("llm: the ChatGPT sign-in state could not be saved", "err", err)
	}
}

// forgetChatGPTAccess drops an access token the server refused, so the next
// call renews it (or learns that the sign-in is over).
func (r *Router) forgetChatGPTAccess(token string) {
	c := &r.chatgpt
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.access == token {
		c.access, c.accessExp = "", time.Time{}
	}
	if cur, err := r.keys.Get(vaultChatGPTAccess); err == nil && cur == token {
		_ = r.keys.Delete(vaultChatGPTAccess)
	}
}

// ---- sign-in ----

type chatgptAttempt struct {
	state, nonce, verifier, redirect, hostID string
	clientID, email                          string
	srv                                      *http.Server
	timer                                    *time.Timer
	once                                     sync.Once
	// subject is the account the registration is bound to, "" before the first.
	subject string
}

func randomText(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func newHostID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b[:])
	return "urn:uuid:" + h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// BeginChatGPTSignIn starts a loopback sign-in and returns the address the
// user opens in the system browser. The result arrives through ChatGPTStatus:
// Pending turns false, and SignedIn or Message says how it ended. A second call
// replaces an attempt still waiting.
func (r *Router) BeginChatGPTSignIn(ctx context.Context) (string, error) {
	reg, err := r.chatgptRegistration(ctx)
	if err != nil {
		return "", err
	}
	if reg.HostID == "" {
		// The host id is saved before the first sign-in and never changes.
		reg.HostID = newHostID()
		if err := r.saveChatGPTRegistration(ctx, reg); err != nil {
			return "", err
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("the sign-in could not listen on the loopback address: %w", err)
	}
	a := &chatgptAttempt{state: randomText(24), nonce: randomText(24), verifier: randomText(32), hostID: reg.HostID,
		clientID: reg.ClientID, email: reg.Email, subject: reg.Subject,
		redirect: fmt.Sprintf("http://127.0.0.1:%d%s", ln.Addr().(*net.TCPAddr).Port, chatgptCallback)}
	challenge := sha256.Sum256([]byte(a.verifier))
	q := url.Values{
		"client_id": {chatgptEntryID}, "ext_agent_host_id": {a.hostID}, "response_type": {"code"}, "redirect_uri": {a.redirect},
		"resource": {chatgptResource}, "scope": {chatgptScope}, "state": {a.state}, "nonce": {a.nonce},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"},
	}
	if a.clientID == "" {
		q.Set("agent_name_hint", chatgptAppName)
	} else {
		q.Set("client_id", a.clientID)
		if a.email != "" {
			q.Set("login_hint", a.email)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+chatgptCallback, func(w http.ResponseWriter, req *http.Request) { r.chatgptCallback(a, w, req) })
	a.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	// The attempt is whole before another caller can see it: a sign-in or a
	// sign-out that finds it pending stops its timer.
	a.timer = time.AfterFunc(chatgptSignInWait, func() { r.endAttempt(a, "the ChatGPT sign-in was not finished in time; start it again") })
	c := &r.chatgpt
	c.pmu.Lock()
	old := c.pending
	c.pending, c.lastFail = a, ""
	c.pmu.Unlock()
	if old != nil {
		old.stop()
	}
	go func() { _ = a.srv.Serve(ln) }()
	return r.chatgptEndpoint("/api/accounts/authorize") + "?" + q.Encode(), nil
}

func (a *chatgptAttempt) stop() {
	a.timer.Stop()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = a.srv.Shutdown(ctx)
	}()
}

// endAttempt closes an attempt once and records how it ended. failure is empty
// on success.
func (r *Router) endAttempt(a *chatgptAttempt, failure string) {
	c := &r.chatgpt
	c.pmu.Lock()
	if c.pending == a {
		c.pending, c.lastFail = nil, failure
	}
	c.pmu.Unlock()
	a.stop()
}

func (r *Router) chatgptCallback(a *chatgptAttempt, w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(a.state)) != 1 {
		http.Error(w, "this sign-in link is not the one Applysmith started", http.StatusBadRequest)
		return
	}
	failure := "the sign-in callback was used twice"
	a.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), 2*chatgptRefreshLimit)
		defer cancel()
		failure = r.finishSignIn(ctx, a, q)
	})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if failure != "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "<!doctype html><title>Applysmith</title><p>%s</p><p>You can close this tab.</p>", html.EscapeString(failure))
	} else {
		fmt.Fprint(w, "<!doctype html><title>Applysmith</title><p>Signed in to ChatGPT. You can close this tab.</p>")
	}
	r.endAttempt(a, failure)
}

// finishSignIn handles the redirect: it exchanges the code, validates the ID
// token and saves the family. It returns the user-facing failure, or "".
func (r *Router) finishSignIn(ctx context.Context, a *chatgptAttempt, q url.Values) string {
	if e := q.Get("error"); e != "" {
		if e == "access_denied" {
			return "ChatGPT sign-in was declined, so plan usage is not enabled. Sign in again and allow it, or use an API key."
		}
		return "ChatGPT sign-in failed (" + boundedText(e) + ")"
	}
	code, issued := q.Get("code"), q.Get("client_id")
	switch {
	case code == "":
		return "ChatGPT sent no sign-in code"
	case a.clientID != "" && issued != "" && issued != a.clientID:
		return "ChatGPT answered for a different client than the one this app registered; the sign-in was rejected"
	case a.clientID == "" && issued == "":
		return "ChatGPT issued no client id, so the registration is incomplete; start the sign-in again"
	}
	clientID := a.clientID
	if clientID == "" {
		clientID = issued
	}
	tok, err := r.tokenRequest(ctx, url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code},
		"code_verifier": {a.verifier}, "redirect_uri": {a.redirect}, "resource": {chatgptResource}})
	var oe *oauthError
	switch {
	case errors.As(err, &oe) && oe.Code == "invalid_grant":
		return "ChatGPT refused the sign-in code; start the sign-in again"
	case err != nil:
		return "ChatGPT sign-in could not be completed: " + boundedText(err.Error())
	}
	claims, err := r.verifyIDToken(ctx, tok.IDToken, clientID, a.nonce)
	if err != nil {
		return err.Error()
	}
	if a.subject != "" && a.subject != claims.Sub {
		return "that is a different ChatGPT account than the one this app is registered to; sign out and forget the account first"
	}
	scopes := strings.Fields(cmpOrText(tok.Scope, q.Get("scope")))
	reg := chatgptReg{HostID: a.hostID, ClientID: clientID, Subject: claims.Sub, Email: claims.Email, Scopes: scopes, SignedIn: true}
	c := &r.chatgpt
	c.mu.Lock()
	defer c.mu.Unlock()
	if !reg.planUsage() {
		// Identity only: the registration is kept so the next OAuth uses the
		// saved client, and no inference runs on it.
		reg.Message = "signed in without ChatGPT plan usage; sign in again and allow it"
		c.access, c.accessExp = "", time.Time{}
		_ = r.keys.Delete(vaultChatGPTRefresh)
		_ = r.keys.Delete(vaultChatGPTAccess)
		if err := r.saveChatGPTRegistration(ctx, reg); err != nil {
			return "the sign-in could not be saved: " + err.Error()
		}
		return reg.Message
	}
	if tok.RefreshToken == "" {
		return "ChatGPT sent no refresh token, so the sign-in cannot last an hour; start it again and allow offline access"
	}
	if err := r.storeChatGPTTokens(tok.AccessToken, tok.RefreshToken, ""); err != nil {
		return "the sign-in could not be saved to the keychain: " + boundedText(err.Error())
	}
	r.cacheChatGPTAccess(tok)
	c.models = chatgptModelCache{}
	if err := r.saveChatGPTRegistration(ctx, reg); err != nil {
		return "the sign-in could not be saved: " + err.Error()
	}
	if err := r.addChatGPTProvider(ctx); err != nil {
		return "the sign-in could not be saved: " + err.Error()
	}
	return ""
}

// addChatGPTProvider makes a signed-in plan a provider that a task row can
// name. The active preset and every row stay as they are.
func (r *Router) addChatGPTProvider(ctx context.Context) error {
	pr, _ := PresetByName("chatgpt")
	cfg := pr.ProviderConfig()
	return UpdateSettings(ctx, r.store, r.now(), func(s *Settings) error {
		if _, ok := s.Provider(cfg.ID); !ok {
			s.Providers = append(s.Providers, cfg)
		}
		return nil
	})
}

func cmpOrText(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ---- sign-out ----

// SignOutChatGPT ends the sign-in: it revokes the refresh token at the
// issuer, deletes the family and keeps the registration. forget also drops the
// registration and the account, so another account can sign in. revoked is
// false when the revocation could not be confirmed; the family is cleared
// either way and the user can disconnect the app in ChatGPT settings.
func (r *Router) SignOutChatGPT(ctx context.Context, forget bool) (revoked bool, err error) {
	c := &r.chatgpt
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pmu.Lock()
	pending := c.pending
	c.pmu.Unlock()
	if pending != nil {
		r.endAttempt(pending, "")
	}
	reg, err := r.chatgptRegistration(ctx)
	if err != nil {
		return false, err
	}
	revoked = true
	if refresh, gerr := r.keys.Get(vaultChatGPTRefresh); gerr == nil && reg.ClientID != "" {
		revoked = r.revokeChatGPT(ctx, reg.ClientID, refresh)
	}
	c.access, c.accessExp = "", time.Time{}
	c.models = chatgptModelCache{}
	for _, name := range []string{vaultChatGPTRefresh, vaultChatGPTAccess} {
		if derr := r.keys.Delete(name); derr != nil && !vault.IsCode(derr, vault.CodeNotFound) {
			err = errors.Join(err, derr)
		}
	}
	reg.SignedIn, reg.Message = false, ""
	if forget {
		reg = chatgptReg{HostID: reg.HostID}
	}
	return revoked, errors.Join(err, r.saveChatGPTRegistration(ctx, reg))
}

// revokeChatGPT revokes the refresh token at the issuer's revocation endpoint,
// retrying a network failure or a 5xx a few times with a growing wait. An empty
// 200 is success, also for a token that was already invalid.
func (r *Router) revokeChatGPT(ctx context.Context, clientID, refresh string) bool {
	for attempt := range 3 {
		if attempt > 0 && r.sleep(ctx, time.Duration(attempt)*time.Second) != nil {
			return false
		}
		d, err := r.discover(ctx)
		if err != nil || d.Revocation == "" {
			continue
		}
		form := url.Values{"token": {refresh}, "token_type_hint": {"refresh_token"}, "client_id": {clientID}}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.Revocation, strings.NewReader(form.Encode()))
		if err != nil {
			return false
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := r.http.Do(req)
		if err != nil {
			continue
		}
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusOK:
			return true
		case resp.StatusCode < 500:
			return false
		}
	}
	return false
}
