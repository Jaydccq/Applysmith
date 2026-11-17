package gmail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/gmail/extract"
	"applysmith/internal/gmail/imaptest"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

// A demo workspace never reads mail: the client refuses before it connects
// and records the refusal.
func TestDemo_GmailReadsAreRefusedInADemoWorkspace(t *testing.T) {
	e, stack := demoStack(t)
	_, err := stack.Client.Find(e.ctx, "label:inbox", 5)
	var ref *store.DemoRefusal
	if !errors.As(err, &ref) || ref.Decision.Code != "demo_workspace" {
		t.Fatalf("err = %v, want a demo_workspace refusal", err)
	}
	if n := len(e.imap.Commands("")); n != 0 {
		t.Fatalf("%d commands reached the server", n)
	}
	var rows int
	if err := e.st.Read().QueryRow(`SELECT count(*) FROM gate_decisions WHERE gate = 'demo_workspace' AND code = 'demo_workspace' AND verdict = 'deny'`).Scan(&rows); err != nil || rows == 0 {
		t.Fatalf("no audited refusal (%v)", err)
	}
}

func demoWorkspace(t *testing.T) (*env, *Stack) {
	t.Helper()
	e := newEnv(t)
	if err := os.WriteFile(filepath.Join(e.root, store.StateDir, workspace.DemoMarkerName), []byte("demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return e, Open(e.st, e.vault, Options{Dial: e.imap.Dial, Now: e.clk.now})
}

// demoStack opens the stack of a connected account in a demo workspace.
func demoStack(t *testing.T) (*env, *Stack) {
	t.Helper()
	e, stack := demoWorkspace(t)
	e.connect()
	return e, stack
}

func demoCount(t *testing.T, e *env, where string) int {
	t.Helper()
	var n int
	if err := e.st.Read().QueryRow(`SELECT count(*) FROM gate_decisions WHERE gate = 'demo_workspace' AND ` + where).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Connecting works in a demo workspace: the sign-in check, the connection
// test and the status read. The test counts messages and reads none.
func TestDemo_GmailCanBeConnectedInADemoWorkspace(t *testing.T) {
	e, stack := demoWorkspace(t)
	if res, err := stack.Service.Connect(e.ctx, addr("alex", "example.com"), testPassword); err != nil || res.Mailbox != addr("alex", "example.com") {
		t.Fatalf("connect in the demo: %+v, %v", res, err)
	}
	e.imap.Add(imaptest.AllMail, msgSpec{ID: "a1", From: "a", Subject: "s", Text: "t", MS: e.clk.now().UnixMilli()})
	e.imap.Add(imaptest.AllMail, msgSpec{ID: "a2", From: "a", Subject: "s", Text: "t", MS: e.clk.now().UnixMilli()})
	if n, err := stack.Service.Test(e.ctx); err != nil || n != 2 {
		t.Fatalf("test = %d, %v", n, err)
	}
	if n := len(e.imap.Commands("UID FETCH")); n != 0 {
		t.Fatalf("the connection test fetched %d time(s) in a demo", n)
	}
	if h, err := stack.Service.Health(e.ctx); err != nil || h.Login != LoginOK || !h.Connected {
		t.Fatalf("status = %+v, %v", h, err)
	}
	if demoCount(t, e, `verdict = 'allow'`) == 0 || demoCount(t, e, `verdict = 'deny'`) != 0 {
		t.Fatalf("allow rows %d, deny rows %d: connecting is audited as allowed", demoCount(t, e, `verdict = 'allow'`), demoCount(t, e, `verdict = 'deny'`))
	}
	// The scope is for the connection commands only: the same client reading
	// mail outside them is refused.
	if _, err := stack.Client.Find(e.ctx, "after:1", 5); err == nil {
		t.Fatal("a search outside the connection commands was allowed")
	}
	if got := stack.Lookups.Mailbox(e.ctx); got != addr("alex", "example.com") {
		t.Fatalf("mailbox = %q", got)
	}
}

// Nothing that reads, classifies or digests mail starts in a demo workspace,
// connected or not; each attempt is refused by the demo gate and audited.
func TestDemo_NoSyncDigestOrClassificationRunsInADemoWorkspace(t *testing.T) {
	e, stack := demoStack(t)
	var ref *store.DemoRefusal
	attempts := map[string]func() error{
		"sync":       func() error { _, err := stack.Syncer.Run(e.ctx, TriggerManual); return err },
		"resync":     func() error { _, err := stack.Syncer.Resync(e.ctx, "cli", TriggerCLI, true); return err },
		"reclassify": func() error { _, err := stack.Syncer.Reclassify(e.ctx, "cli", TriggerCLI, true); return err },
		"digest":     func() error { _, err := stack.Digests.Run(e.ctx); return err },
		"agent":      func() error { _, err := stack.Digests.Generate(e.ctx); return err },
	}
	for name, run := range attempts {
		if err := run(); !errors.As(err, &ref) || ref.Decision.Code != "demo_workspace" {
			t.Errorf("%s: err = %v, want a demo_workspace refusal", name, err)
		}
	}
	if n := len(e.imap.Commands("")); n != 0 {
		t.Fatalf("%d commands reached the server", n)
	}
	if n := demoCount(t, e, `verdict = 'deny'`); n < len(attempts) {
		t.Fatalf("%d audited refusals for %d attempts", n, len(attempts))
	}
	if !e.st.Demo() || !stack.Repo.Demo() {
		t.Fatal("the repo does not see the demo marker")
	}
}

// codeAllowed is the demo gate's allowance for a lookup of the persona's
// alias of the connected account.
func codeAllowed(t *testing.T) gates.Decision {
	t.Helper()
	d := gates.DemoWorkspace(gates.DemoInput{Demo: true, Action: gates.DemoMailCode, Code: gates.DemoCodeFacts{
		DryRun: true, InWalk: true, AtCodeDialog: true,
		PersonaEmail: addr("alex+applysmithdemo", "example.com"), Account: addr("alex", "example.com"),
		SenderDomain: "adp.com", OpenedMS: since.Add(-time.Second).UnixMilli(), SinceMS: since.UnixMilli(),
	}})
	if !d.Allowed() {
		t.Fatalf("the gate refused the lookup: %+v", d)
	}
	return d
}

// One bounded code lookup is the only mail read: the gate's allowance, the
// sender domain, the start instant and the messages of its own search bound
// it.
func TestDemo_OnlyTheGateAllowedCodeLookupReadsMail(t *testing.T) {
	e, stack := demoStack(t)
	e.imap.Add(imaptest.AllMail, codeMail("a1", "adp.com", "482916", time.Second))
	e.imap.Add(imaptest.AllMail, codeMail("a9", "adp.com", "999999", 2*time.Second))
	granted := "after:" + strconv.FormatInt(since.Unix(), 10) + " in:anywhere from:adp.com " + extract.OTPQueryTerms()
	e.imap.Search(granted, "a1")
	refused := func(t *testing.T, ctx context.Context, from time.Time, domain string) {
		t.Helper()
		before := len(e.imap.Commands(""))
		if _, err := stack.Lookups.LatestCode(ctx, from, domain); err == nil {
			t.Fatal("the lookup was served")
		}
		if len(e.imap.Commands("")) != before {
			t.Fatal("a refused lookup reached the server")
		}
	}
	t.Run("no grant is the extension's route and is refused", func(t *testing.T) {
		refused(t, e.ctx, since, "adp.com")
	})
	t.Run("a refused decision grants nothing", func(t *testing.T) {
		deny := gates.DemoWorkspace(gates.DemoInput{Demo: true, Action: gates.DemoMail})
		refused(t, DemoCodeGrant(e.ctx, deny, "adp.com", since), since, "adp.com")
		refused(t, DemoCodeGrant(e.ctx, gates.Decision{}, "adp.com", since), since, "adp.com")
		refused(t, DemoCodeGrant(e.ctx, codeAllowed(t), "", since), since, "")
	})
	t.Run("another sender domain or an earlier start is outside the grant", func(t *testing.T) {
		ctx := DemoCodeGrant(e.ctx, codeAllowed(t), "adp.com", since)
		refused(t, ctx, since, "other.example")
		refused(t, ctx, since, "")
		refused(t, ctx, since.Add(-time.Hour), "adp.com")
	})
	t.Run("the granted lookup returns the code and reads nothing else", func(t *testing.T) {
		ctx := DemoCodeGrant(e.ctx, codeAllowed(t), "adp.com", since)
		res, err := stack.Lookups.LatestCode(ctx, since, "adp.com")
		if err != nil || res.Code != "482916" {
			t.Fatalf("code %q, err %v", res.Code, err)
		}
		for _, l := range e.imap.Commands("UID FETCH") {
			if !strings.HasPrefix(l, "UID FETCH 1 ") {
				t.Errorf("a fetch outside the granted search: %q", l)
			}
		}
		// Another search, the UID cursor and a sync are refused under the
		// same grant, before any command.
		before := len(e.imap.Commands(""))
		if _, err := stack.Client.Find(ctx, "label:inbox", 10); err == nil {
			t.Error("another query was searched")
		}
		if _, err := stack.Syncer.Run(ctx, TriggerManual); err == nil {
			t.Error("a sync ran under a code grant")
		}
		if len(e.imap.Commands("")) != before {
			t.Fatal("a command outside the grant reached the server")
		}
	})
	t.Run("the grant is for that context only", func(t *testing.T) {
		refused(t, e.ctx, since, "adp.com")
	})
}
