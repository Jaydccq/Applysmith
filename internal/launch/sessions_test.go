package launch

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/browser"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

func openDeps(t *testing.T) (*sessionDeps, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "applysmith.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	d := &sessionDeps{}
	d.st.Store(st)
	return d, st
}

func TestSignInSync_ListsTheCredentialIndexsAccountsWithoutStartingChrome(t *testing.T) {
	d, st := openDeps(t)
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO credential_index (name, kind, host, email, has_secret, created_at, updated_at) VALUES ('signin-site-careers.example.org', 'signin_site', 'careers.example.org', 'a@example.org', 1, 'x', 'x')`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	ws := workspace.Open(filepath.Join(t.TempDir(), "ws"))
	sites, err := sessionBrowser(ws, d).Sites(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range sites {
		found = found || s.Domain == "careers.example.org"
	}
	if !found {
		t.Fatalf("the stored account's host is missing from %v", sites)
	}
}

// The server's sign-in sync is the managed browser's; before the database is
// open, listing its sites answers without starting Chrome or creating the
// profile.
func TestSignInSync_BeforeTheDatabaseIsOpenListsNoAccountsAndNoStates(t *testing.T) {
	ws := workspace.Open(filepath.Join(t.TempDir(), "ws"))
	sync := sessionBrowser(ws, &sessionDeps{})
	if _, ok := sync.(*browser.Sessions); !ok {
		t.Fatal("the server does not use the real sign-in sync")
	}
	sites, err := sync.Sites(context.Background())
	if err != nil || len(sites) != len(browser.SignInSites) {
		t.Fatalf("%v %v", sites, err)
	}
	for _, s := range sites {
		if s.SignIn != "unknown" {
			t.Fatalf("%+v", s)
		}
	}
	if _, err := os.Stat(filepath.Join(ws.StateDir(), "browser")); !os.IsNotExist(err) {
		t.Fatalf("listing sites created the browser directory: %v", err)
	}
}

func TestSignInProbe_StatesPersistInTheDatabaseAndReachTheSiteList(t *testing.T) {
	d, st := openDeps(t)
	at := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	ctx := context.Background()
	if err := SaveSignInState(ctx, st, "linkedin.com", browser.StateSignedOut, at); err != nil {
		t.Fatal(err)
	}
	if err := SaveSignInState(ctx, st, "linkedin.com", browser.StateSignedIn, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSignInStates(ctx, st)
	if err != nil || got["linkedin.com"].State != browser.StateSignedIn || !got["linkedin.com"].CheckedAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("%v %+v", err, got)
	}
	ws := workspace.Open(filepath.Join(t.TempDir(), "ws"))
	sites, _ := sessionBrowser(ws, d).Sites(ctx)
	for _, s := range sites {
		if s.Domain == "linkedin.com" && s.SignIn != "signed_in" {
			t.Fatalf("%+v", s)
		}
	}
}

func TestWatchScans_ReadsTheRuntimeSettingAndDefaultsToHidden(t *testing.T) {
	ws := workspace.Open(filepath.Join(t.TempDir(), "ws"))
	ctx := context.Background()
	if (&sessionDeps{}).watchScans(ws)(ctx) {
		t.Fatal("before the database is open scans stay hidden")
	}
	d, st := openDeps(t)
	watch := d.watchScans(ws)
	if watch(ctx) {
		t.Fatal("a workspace without the setting watches scans")
	}
	on := true
	if _, err := answerbank.New(ws, st).PatchSettings(ctx, answerbank.SettingsPatch{WatchScans: &on}); err != nil {
		t.Fatal(err)
	}
	if !watch(ctx) {
		t.Fatal("the stored setting is not read")
	}
	off := false
	if _, err := answerbank.New(ws, st).PatchSettings(ctx, answerbank.SettingsPatch{WatchScans: &off}); err != nil {
		t.Fatal(err)
	}
	if watch(ctx) {
		t.Fatal("turning the setting off is not read")
	}
}
