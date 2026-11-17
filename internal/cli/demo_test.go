package cli

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"applysmith/internal/demo"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

// Scenario "The demo is never written over a real workspace", at the command line.
func TestDemoInit_RefusesAFolderThatHoldsYourWorkspaceAndWritesNothing(t *testing.T) {
	te := newTestEnv(t)
	if o := te.run(context.Background(), "init", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("%+v", o)
	}
	before := snapshot(t, te.ws)
	o := te.run(context.Background(), "demo", "init", "--workspace", te.ws)
	if o.code != 2 || !strings.Contains(o.stderr, "not a demo") {
		t.Fatalf("demo init over a workspace: %+v", o)
	}
	if after := snapshot(t, te.ws); !reflect.DeepEqual(before, after) {
		t.Fatal("the refused demo init changed the workspace")
	}
}

func TestDemoInit_CreatesTheDemoAndEveryCommandSaysItIsOne(t *testing.T) {
	te := newTestEnv(t)
	dir := filepath.Join(te.home, "Applysmith-Demo")
	o := te.run(context.Background(), "demo", "init")
	if o.code != 0 || !strings.Contains(o.stdout, "added 3 sample posting(s)") || !strings.Contains(o.stderr, "demo workspace "+dir) {
		t.Fatalf("demo init: %+v", o)
	}
	if !workspace.Open(dir).IsDemo() {
		t.Fatal("the default demo folder carries no marker")
	}
	o = te.run(context.Background(), "setup", "status", "--json", "--workspace", dir)
	if !strings.Contains(o.stderr, "demo workspace "+dir) || !strings.Contains(o.stderr, "never presses Submit") {
		t.Fatalf("the CLI printed no demo line: %+v", o)
	}
	if strings.Contains(o.stdout, "demo workspace") {
		t.Fatal("the demo line went into the command's own output")
	}
	// A repeat keeps the demo and adds nothing twice.
	o = te.run(context.Background(), "demo", "init", "--workspace", dir)
	if o.code != 0 || !strings.Contains(o.stdout, "added 0 sample posting(s)") {
		t.Fatalf("repeat: %+v", o)
	}
}

func profileEmailOf(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "profile.yml"))
	if err != nil {
		t.Fatal(err)
	}
	p, rep := workspace.ParseProfile(b)
	if !rep.OK() {
		t.Fatalf("profile: %+v", rep)
	}
	return p.Candidate.Email
}

func setMailbox(t *testing.T, dir, mailbox string) {
	t.Helper()
	ws := workspace.Open(dir)
	id, err := ws.ID()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(ws.StateDir(), store.DBFile), store.Options{WorkspaceID: id})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO gmail_state (key, value) VALUES ('mailbox_address', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, mailbox)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// assertResumeShows checks that cv.md and the résumé PDF carry the address and
// not the previous one.
func assertResumeShows(t *testing.T, dir, want, gone string) {
	t.Helper()
	cv, _ := os.ReadFile(filepath.Join(dir, "cv.md"))
	pdf, err := os.ReadFile(filepath.Join(dir, demo.ResumeFile))
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]string{"cv.md": string(cv), "the résumé PDF": string(pdf)} {
		if !strings.Contains(got, want) || strings.Contains(got, gone) {
			t.Errorf("%s must show %s and not %s", name, want, gone)
		}
	}
}

// `applysmith demo email` sets the persona's address only to a plus alias, only in
// a demo workspace, and only of the Gmail account connected there.
func TestDemoEmail_SetsAPlusAliasOfTheConnectedAccountAndRefusesEverythingElse(t *testing.T) {
	te := newTestEnv(t)
	ctx := context.Background()
	dir := filepath.Join(te.home, "Applysmith-Demo")
	if o := te.run(ctx, "demo", "init"); o.code != 0 {
		t.Fatalf("demo init: %+v", o)
	}
	const shipped = "alex@example.com"
	at := func(local, domain string) string { return local + "@" + domain }
	alias := at("owner+applysmithdemo", "example.test")

	if o := te.run(ctx, "demo", "email", shipped); o.code != 2 || !strings.Contains(o.stderr, "plus-address alias") || profileEmailOf(t, dir) != shipped {
		t.Fatalf("a non-alias: %+v", o)
	}
	if o := te.run(ctx, "demo", "email"); o.code != 2 || !strings.Contains(o.stderr, "usage") {
		t.Fatalf("no address: %+v", o)
	}
	// Gmail is not connected yet: the alias is set and the output says what is missing.
	if o := te.run(ctx, "demo", "email", alias); o.code != 0 || !strings.Contains(o.stdout, "Gmail is not connected") || profileEmailOf(t, dir) != alias {
		t.Fatalf("alias without Gmail: %+v", o)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "profile.yml"))
	if !strings.Contains(string(data), "# Demo workspace profile.") || !strings.Contains(string(data), "preferred_name: Alex") {
		t.Fatal("the edit rewrote more than the address")
	}
	// The CV and the résumé PDF show the address the profile gives, never the shipped one.
	assertResumeShows(t, dir, alias, shipped)
	// A connected account that the address is not an alias of changes nothing.
	setMailbox(t, dir, at("someone", "example.test"))
	other := at("owner+second", "example.test")
	if o := te.run(ctx, "demo", "email", other); o.code != 2 || !strings.Contains(o.stderr, "not an alias of the Gmail account") || profileEmailOf(t, dir) != alias {
		t.Fatalf("alias of another account: %+v", o)
	}
	setMailbox(t, dir, at("Owner", "example.test"))
	if o := te.run(ctx, "demo", "email", other); o.code != 0 || strings.Contains(o.stdout, "not connected") || profileEmailOf(t, dir) != other {
		t.Fatalf("alias of the connected account: %+v", o)
	}
	assertResumeShows(t, dir, other, alias)
	// A résumé the user replaced is not rewritten.
	mine := []byte("%PDF-1.4 the user's own résumé\n")
	if err := os.WriteFile(filepath.Join(dir, demo.ResumeFile), mine, 0o600); err != nil {
		t.Fatal(err)
	}
	third := at("owner+third", "example.test")
	if o := te.run(ctx, "demo", "email", third); o.code != 0 || profileEmailOf(t, dir) != third {
		t.Fatalf("third alias: %+v", o)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, demo.ResumeFile)); !bytes.Equal(got, mine) {
		t.Fatal("the user's own résumé was overwritten")
	}
	if cv, _ := os.ReadFile(filepath.Join(dir, "cv.md")); !strings.Contains(string(cv), third) {
		t.Fatal("the CV does not show the new address")
	}
	// The demo marker is the condition: a real workspace is never edited.
	if o := te.run(ctx, "init", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("%+v", o)
	}
	before := snapshot(t, te.ws)
	if o := te.run(ctx, "demo", "email", alias, "--workspace", te.ws); o.code != 2 || !strings.Contains(o.stderr, "not a demo workspace") {
		t.Fatalf("a real workspace: %+v", o)
	}
	if !reflect.DeepEqual(before, snapshot(t, te.ws)) {
		t.Fatal("the refused command changed the real workspace")
	}
}

// Connecting Gmail is possible in the demo; nothing that reads mail runs there.
func TestDemoGmail_StatusWorksAndSyncIsRefusedByTheDemoGate(t *testing.T) {
	te := newTestEnv(t)
	ctx := context.Background()
	dir := filepath.Join(te.home, "Applysmith-Demo")
	if o := te.run(ctx, "demo", "init"); o.code != 0 {
		t.Fatalf("demo init: %+v", o)
	}
	if o := te.run(ctx, "gmail", "status", "--json", "--workspace", dir); o.code != 0 {
		t.Fatalf("status: %+v", o)
	}
	for _, args := range [][]string{{"sync"}, {"resync", "--yes"}, {"reclassify", "--yes"}} {
		o := te.run(ctx, append(append([]string{"gmail"}, args...), "--workspace", dir)...)
		if o.code == 0 || !strings.Contains(o.stderr, "demo_workspace") {
			t.Fatalf("gmail %v in the demo: %+v", args, o)
		}
	}
}
