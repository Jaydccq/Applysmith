package kb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// fakeGitHub serves the device flow and the account reads the way github.com
// answers them: the token endpoint answers form-encoded, an unfinished
// request with a 200 that carries an error.
// A device-flow test creates it before entering synctest.Test and polls from
// inside, so the OAuth library's one-second interval passes on the bubble's
// clock; every answer closes its connection, so no idle one keeps the bubble
// waiting on the network.
type fakeGitHub struct {
	srv *httptest.Server
	mu  sync.Mutex
	// polls are the answers of the token endpoint, in order; the last repeats.
	polls []string
	asked int
}

func newFakeGitHub(t *testing.T, polls ...string) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{polls: polls}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/device/code", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "dev-1", "user_code": "WDJB-MJHT",
			"verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 1})
	})
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("device_code") != "dev-1" || r.PostForm.Get("client_id") == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		body := f.polls[min(f.asked, len(f.polls)-1)]
		f.asked++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
		_, _ = fmt.Fprint(w, body)
	})
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gho_secret" {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		_, _ = fmt.Fprint(w, `{"login":"octo-cat"}`)
	})
	mux.HandleFunc("GET /user/repos", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("sort") != "pushed" || r.URL.Query().Get("affiliation") != "owner,collaborator,organization_member" {
			http.Error(w, "unexpected query", http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprint(w, `[
			{"full_name":"Octo-Cat/reef-tool","private":true,"description":null,"pushed_at":"2026-10-20T10:00:00Z","default_branch":"main","language":"Go"},
			{"full_name":"octo-cat/notes","private":false,"description":"Notes","pushed_at":"2026-10-01T10:00:00Z","default_branch":"trunk","language":null}]`)
	})
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

type tokenStore struct {
	mu  sync.Mutex
	tok string
}

func (s *tokenStore) load() string { s.mu.Lock(); defer s.mu.Unlock(); return s.tok }
func (s *tokenStore) save(t string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tok = t
	return nil
}
func (s *tokenStore) forget() error { return s.save("") }

func newTestAccount(t *testing.T, f *fakeGitHub, store *tokenStore) *GitHubAccount {
	t.Helper()
	a := NewGitHubAccount(GitHubAccountOptions{Load: store.load, Save: store.save, Forget: store.forget,
		CLIToken: func(context.Context) string { return "" }})
	a.clientID, a.oauthBase, a.apiBase, a.insecure = "client-id", f.srv.URL, f.srv.URL, true
	return a
}

// waitForState polls Status until it leaves pending.
func waitForState(t *testing.T, a *GitHubAccount) GitHubStatus {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if st := a.Status(context.Background()); st.State != GitHubPending {
			return st
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the sign-in stayed pending")
	return GitHubStatus{}
}

// A device flow that is pending first and then granted ends with the token
// stored and the account signed in; the page never sees the token.
func TestGitHubAccount_DeviceFlow_StoresTheTokenWhenGranted(t *testing.T) {
	f := newFakeGitHub(t, "error=authorization_pending", "access_token=gho_secret&token_type=bearer&scope=repo")
	synctest.Test(t, func(t *testing.T) {
		store := &tokenStore{}
		a := newTestAccount(t, f, store)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		st, err := a.SignIn(ctx, ctx)
		if err != nil {
			t.Fatal(err)
		}
		if st.State != GitHubPending || st.UserCode != "WDJB-MJHT" || st.VerificationURI != "https://github.com/login/device" {
			t.Fatalf("started = %+v", st)
		}
		done := waitForState(t, a)
		if done.State != GitHubSignedIn || done.Login != "octo-cat" || done.Source != GitHubSourceVault || done.Error != "" {
			t.Fatalf("finished = %+v", done)
		}
		if store.load() != "gho_secret" || f.asked != 2 {
			t.Fatalf("stored token = %q after %d polls, want one pending and one granted", store.load(), f.asked)
		}
	})
}

// SignIn answers with the code of the flow it started, also when the wait for
// that flow had already ended by the time it returns.
func TestGitHubAccount_SignIn_NamesItsCodeEvenWhenTheWaitHasEnded(t *testing.T) {
	f := newFakeGitHub(t, "access_token=gho_secret&token_type=bearer&scope=repo")
	synctest.Test(t, func(t *testing.T) {
		store := &tokenStore{}
		a := newTestAccount(t, f, store)
		a.opts.Spawn = func(wait func()) { wait() }
		st, err := a.SignIn(context.Background(), context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if st.State != GitHubPending || st.UserCode != "WDJB-MJHT" || st.ExpiresAt.IsZero() {
			t.Fatalf("started = %+v, want the pending flow and its code", st)
		}
		if done := a.Status(context.Background()); done.State != GitHubSignedIn {
			t.Fatalf("finished = %+v", done)
		}
	})
}

// A request denied on GitHub ends in an error and stores nothing; signing in
// again starts over.
func TestGitHubAccount_DeviceFlow_DeniedStoresNothing(t *testing.T) {
	f := newFakeGitHub(t, "error=access_denied")
	synctest.Test(t, func(t *testing.T) {
		store := &tokenStore{}
		a := newTestAccount(t, f, store)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		if _, err := a.SignIn(ctx, ctx); err != nil {
			t.Fatal(err)
		}
		done := waitForState(t, a)
		if done.State != GitHubError || done.Error == "" || done.Login != "" {
			t.Fatalf("finished = %+v", done)
		}
		if store.load() != "" {
			t.Fatalf("a denied request stored %q", store.load())
		}
	})
}
