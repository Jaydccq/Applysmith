package cli

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"applysmith/internal/gmail"
	"applysmith/internal/gmail/imaptest"
	"applysmith/internal/store"
)

func (te *testEnv) gmail(args ...string) output {
	te.t.Helper()
	return te.run(context.Background(), append([]string{"gmail"}, append(args, "--workspace", te.ws)...)...)
}

// connect reads the app password from standard input only, signs in before
// it stores anything, and gives the mailbox its sync schedule; nothing it
// prints carries the password.
func TestGmailCLI_ConnectSignsInFirstAndKeepsThePasswordOffTheCommandLine(t *testing.T) {
	te := newTestEnv(t)
	address, password := "alex"+"@example.org", strings.Repeat("wxyz", 4)
	srv, err := imaptest.New(address, password)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	te.gmailDial = srv.Dial
	if o := te.gmail("connect", "--address", address, "--password", password); o.code != 2 || strings.Contains(o.stderr, password) {
		t.Fatalf("a password argument = %d %q", o.code, o.stderr)
	}
	if o := te.gmail("connect", "--address", address); o.code != 2 {
		t.Fatalf("no --password-stdin = %d %q", o.code, o.stderr)
	}
	if n := len(srv.Commands("")); n != 0 {
		t.Fatalf("a refused command line reached Gmail with %d commands", n)
	}
	// The commands the messages name are the ones that connect.
	te.stdin = strings.Repeat("abcd", 4) + "\n"
	if o := te.gmail(quotedGmailCommand(t, gmail.ErrNotConnected.Error(), address)...); o.code == 0 || !strings.Contains(o.stderr, "Gmail refused the sign-in") {
		t.Fatalf("a wrong password = %d %q", o.code, o.stderr)
	}
	if len(te.backend.Accounts()) != 0 {
		t.Fatal("a refused password was stored")
	}
	te.stdin = "wxyz wxyz wxyz wxyz\n"
	o := te.gmail(quotedGmailCommand(t, gmail.LoginFix, address)...)
	if o.code != 0 || !strings.Contains(o.stdout, "connected: "+address) || !strings.Contains(o.stdout, "read as it arrives") || strings.Contains(o.stdout+o.stderr, password) {
		t.Fatalf("connect = %d %q %q", o.code, o.stdout, o.stderr)
	}
	if o := te.gmail("test"); o.code != 0 || !strings.Contains(o.stdout, "ok: 0 messages") {
		t.Fatalf("test = %d %q %q", o.code, o.stdout, o.stderr)
	}
	if o := te.gmail("status"); o.code != 0 || !strings.Contains(o.stdout, "sign-in: ok") {
		t.Fatalf("status = %d %q", o.code, o.stdout)
	}
	if o := te.gmail("purge", "--yes"); o.code != 0 || len(te.backend.Accounts()) != 0 {
		t.Fatalf("purge = %d %q, %d secrets left", o.code, o.stderr, len(te.backend.Accounts()))
	}
	if o := te.gmail("status", "--json"); o.code != 0 || !strings.Contains(o.stdout, `"connected": false`) && !strings.Contains(o.stdout, `"connected":false`) {
		t.Fatalf("status = %d %q %q", o.code, o.stdout, o.stderr)
	}
	if o := te.run(context.Background(), "gmail"); o.code != 2 {
		t.Fatalf("usage code = %d", o.code)
	}
}

// quotedGmailCommand is the `applysmith gmail ...` command a message quotes, as
// the words after `applysmith gmail`, with the address filled in.
func quotedGmailCommand(t *testing.T, text, address string) []string {
	t.Helper()
	_, rest, ok := strings.Cut(text, "`applysmith gmail ")
	cmd, _, closed := strings.Cut(rest, "`")
	if !ok || !closed {
		t.Fatalf("%q quotes no gmail command", text)
	}
	return strings.Fields(strings.ReplaceAll(cmd, "<address>", address))
}

func TestGmailCLI_StartClosesARunADeadProcessLeft(t *testing.T) {
	te := newTestEnv(t)
	if o := te.gmail("status", "--json"); o.code != 0 {
		t.Fatalf("status = %d %q", o.code, o.stderr)
	}
	te.withStore(func(st *store.Store) {
		err := st.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO gmail_runs (trigger_kind, status, mode, started_at, classifier_version, query_version, stages_json, counts_json)
				VALUES ('cli', 'running', 'bootstrap', '2026-01-02T03:00:00.000Z', 'v', 'q', '[]', '{}')`)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	o := te.gmail("status", "--json")
	if o.code != 0 || !strings.Contains(o.stdout, `"running": false`) && !strings.Contains(o.stdout, `"running":false`) {
		t.Fatalf("status after the orphan = %d %q %q", o.code, o.stdout, o.stderr)
	}
	if !strings.Contains(o.stdout, `"status": "failed"`) && !strings.Contains(o.stdout, `"status":"failed"`) {
		t.Fatalf("the orphan run was not closed: %q", o.stdout)
	}
}

// The command line's disconnect pauses the sync schedules as the dashboard's
// does; scheduler.TestScheduler_Packs_GmailDisconnectPausesTheSyncAndConnectResumesIt owns the rule.
func TestGmailCLI_DisconnectPausesTheSyncSchedule(t *testing.T) {
	te := newInitTestEnv(t)
	if o := te.schedule("add", "--name", "My mail", "--kind", "gmail.sync", "--rrule", "FREQ=HOURLY", "--tz", "UTC"); o.code != 0 {
		t.Fatalf("add = %d %q", o.code, o.stderr)
	}
	if o := te.gmail("disconnect"); o.code != 0 || !strings.Contains(o.stdout, "My mail") {
		t.Fatalf("disconnect = %d %q %q", o.code, o.stdout, o.stderr)
	}
	if got := te.listJSON(); len(got) != 1 || got[0].Enabled {
		t.Fatalf("schedules after disconnect: %+v", got)
	}
}
