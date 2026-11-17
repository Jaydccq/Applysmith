package telegram

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

// A demo workspace never talks to Telegram: no send, no token check, no poll.
func TestDemo_TelegramSendsAreRefusedInADemoWorkspace(t *testing.T) {
	r := newRig(t)
	if err := os.WriteFile(filepath.Join(filepath.Dir(r.dbPath), workspace.DemoMarkerName), []byte("demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer srv.Close()
	r.setToken()
	gw := New(Config{State: r.state, Token: TokenSource(r.vault), API: srv.URL, Clock: r.clk})
	_, err := gw.Client().SendMessage(bg, ownerChat, "hello", "", 0, nil)
	var ref *store.DemoRefusal
	if !errors.As(err, &ref) || ref.Decision.Code != "demo_workspace" {
		t.Fatalf("send: err = %v, want a demo_workspace refusal", err)
	}
	mgr := &Manager{State: r.state, Vault: r.vault, API: srv.URL}
	if _, err := mgr.SetToken(bg, fakeToken()); !errors.As(err, &ref) {
		t.Fatalf("token check: err = %v, want a demo_workspace refusal", err)
	}
	gw.Run(bg) // returns at once: a demo workspace does not poll
	if n := hits.Load(); n != 0 {
		t.Fatalf("%d requests reached the Bot API", n)
	}
	var rows int
	if err := r.st.Read().QueryRow(`SELECT count(*) FROM gate_decisions WHERE gate = 'demo_workspace' AND code = 'demo_workspace'`).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("%d audited refusals (%v), want 2", rows, err)
	}
}
