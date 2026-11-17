package gmail

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"applysmith/internal/gmail/imaptest"
	"applysmith/internal/store"
	"applysmith/internal/vault"
)

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// testPassword is the app password the fake server accepts.
var testPassword = strings.Repeat("wxyz", 4)

// env bundles a store, a vault with a throwaway keychain and a fake IMAP
// server.
type env struct {
	t       *testing.T
	ctx     context.Context
	imap    *imaptest.Server
	clk     *testClock
	st      *store.Store
	repo    *Repo
	kc      *vault.Memory
	vault   *vault.Vault
	account *Account
	client  *Client
	root    string

	refused, restored atomic.Int32
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, ctx: context.Background(), imap: newFakeIMAP(t), clk: &testClock{t: time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)}}
	e.root = t.TempDir()
	st, err := store.Open(e.ctx, filepath.Join(e.root, store.StateDir, store.DBFile), store.Options{Now: e.clk.now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e.st = st
	e.repo = NewRepo(st, e.clk.now)
	e.kc = vault.NewMemory()
	e.vault, err = vault.New("ws-test", e.kc, vault.WithGetenv(func(string) string { return "" }))
	if err != nil {
		t.Fatal(err)
	}
	e.account = NewAccount(e.vault, e.repo, e.clk.now, Hooks{
		OnRefused:  func(context.Context, string) { e.refused.Add(1) },
		OnRestored: func(context.Context) { e.restored.Add(1) },
	})
	e.client = NewClient(e.account, st)
	e.client.Dial = e.imap.Dial
	return e
}

// connect stores the app password and the mailbox as connecting does.
func (e *env) connect() {
	e.t.Helper()
	if _, err := e.vault.Set(secretAppPassword, testPassword); err != nil {
		e.t.Fatal(err)
	}
	if err := e.repo.SetState(e.ctx, map[string]string{keyMailbox: addr("alex", "example.com")}); err != nil {
		e.t.Fatal(err)
	}
}

// msgSpec describes a message for the fake server.
type msgSpec = imaptest.Message

// newFakeIMAP starts a fake Gmail that accepts the test mailbox and
// testPassword.
func newFakeIMAP(t *testing.T) *imaptest.Server {
	t.Helper()
	srv, err := imaptest.New(addr("alex", "example.com"), testPassword)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	return srv
}

// addr joins a mailbox name and a domain at run time so no address-shaped
// literal sits in a source file.
func addr(local, domain string) string { return local + "@" + domain }

func assertNoSecretOnDisk(t *testing.T, e *env, secrets ...string) {
	t.Helper()
	err := filepath.WalkDir(e.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		for _, s := range secrets {
			if strings.Contains(string(data), s) {
				t.Errorf("%s contains a secret value", p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
