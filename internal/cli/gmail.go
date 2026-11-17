package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"applysmith/internal/api/gmailapi"
	"applysmith/internal/gmail"
	"applysmith/internal/scheduler"
	"applysmith/internal/scheduler/jobs"
	"applysmith/internal/store"
	"applysmith/internal/vault"
)

const gmailUsage = `usage:
  applysmith gmail connect --address <address> --password-stdin
  applysmith gmail test
  applysmith gmail status [--json]
  applysmith gmail disconnect
  applysmith gmail sync
  applysmith gmail resync --yes
  applysmith gmail reclassify --yes
  applysmith gmail purge --yes

connect signs in to Gmail over IMAP with the address and a Google app password
(Google Account, Security, App passwords; it needs 2-Step Verification), and
stores them only once Gmail accepts them; the password is read from standard
input or a hidden prompt, never from an argument, and goes to the keychain;
the running app then reads new mail as it arrives; another account than the
last one syncs from its own start
disconnect removes the app password from the keychain and pauses any sync
schedule you made; the address and the mail already read stay
every command takes --workspace <dir>
`

// passwordArgRefused refuses an app password given as an argument.
const passwordArgRefused = "an app password must not be passed as a command-line argument, because arguments appear in process listings and shell history; give --password-stdin and pipe it on standard input or type it at the hidden prompt"

func (e *env) gmail(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(e.stderr, gmailUsage)
		return 2
	}
	fs, parse := e.subFlags("gmail", args[0], gmailUsage, "password")
	ws := fs.String("workspace", "", "workspace directory")
	yes := fs.Bool("yes", false, "confirm a command that changes stored Gmail data")
	asJSON := fs.Bool("json", false, "print JSON")
	address := fs.String("address", "", "the Gmail address to connect")
	passwordStdin := fs.Bool("password-stdin", false, "read the app password from standard input or a hidden prompt")
	refused := fs.String("password", "", "refused: a password is never accepted as an argument")
	pos, ok := parse(args[1:])
	if !ok {
		return 2
	}
	name := "gmail " + args[0]
	if *refused != "" || args[0] == "connect" && len(pos) > 0 {
		fmt.Fprintf(e.stderr, "applysmith %s: %s\n", name, passwordArgRefused)
		return 2
	}
	if args[0] == "connect" && (*address == "" || !*passwordStdin) {
		fmt.Fprintf(e.stderr, "applysmith %s: give --address <address> and --password-stdin\n", name)
		return 2
	}
	root, err := e.workspacePath(*ws)
	if err != nil {
		return e.fail(name, err)
	}
	wsp, st, release, err := e.openStore(ctx, root, accessShared)
	if err != nil {
		return e.fail(name, err)
	}
	defer release()
	id, err := wsp.ID()
	if err != nil {
		return e.fail(name, err)
	}
	v, err := vault.New(id, e.backend, vault.WithGetenv(e.getenv))
	if err != nil {
		return e.fail(name, err)
	}
	tax, terr := gmail.LoadTaxonomy(filepath.Join(root, gmail.SendersFile))
	if terr != nil {
		fmt.Fprintf(e.stderr, "applysmith %s: %v; using the shipped sender taxonomy\n", name, terr)
	}
	stack := gmail.Open(st, v, gmail.Options{Taxonomy: tax, Dial: e.gmailDial})
	// A run a dead process left behind is closed before anything reads or
	// starts a run, so status never reads as running for ever.
	if err := stack.Syncer.Recover(ctx); err != nil {
		return e.fail(name, err)
	}
	switch args[0] {
	case "connect":
		pw, code := e.readValue(name, "Google app password")
		if code != 0 {
			return code
		}
		res, err := stack.Service.Connect(ctx, *address, pw)
		var le *gmail.LoginError
		if errors.As(err, &le) {
			err = errors.New(le.ConnectRefusal())
		}
		if err != nil {
			return e.fail(name, err)
		}
		fmt.Fprintf(e.stdout, "connected: %s\n", res.Mailbox)
		if res.Previous != "" {
			fmt.Fprintf(e.stdout, "This is a different account from %s. Mail already read stays; syncing starts again for %s.\n", res.Previous, res.Mailbox)
		}
		if err := e.gmailSchedules(ctx, st, true); err != nil {
			return e.fail(name, err)
		}
		return 0
	case "disconnect":
		was, err := stack.Account.Disconnect(ctx)
		if err != nil {
			return e.fail(name, err)
		}
		if was {
			fmt.Fprintln(e.stdout, "disconnected: the app password left the keychain; the address and the mail already read stay")
		} else {
			fmt.Fprintln(e.stdout, "Gmail was not connected")
		}
		if err := e.gmailSchedules(ctx, st, false); err != nil {
			return e.fail(name, err)
		}
		return 0
	case "test":
		n, err := stack.Service.Test(ctx)
		if err != nil {
			return e.fail(name, err)
		}
		fmt.Fprintf(e.stdout, "ok: %d messages in the last day\n", n)
		return 0
	case "status":
		h, err := stack.Service.Health(ctx)
		if err != nil {
			return e.fail(name, err)
		}
		if *asJSON {
			return e.printJSON(gmailapi.WireGmailHealth(h))
		}
		if !h.Connected {
			fmt.Fprintf(e.stdout, "not connected; `%s` connects it\n", gmail.ConnectCommand)
			return 0
		}
		login := h.Login
		if h.LoginError != "" {
			login += " (" + h.LoginError + ")"
		}
		fmt.Fprintf(e.stdout, "mailbox: %s\nsign-in: %s\nsync: %s\nrunning: %v\n", h.Mailbox, login, h.Sync, h.Running)
		return 0
	case "sync":
		return e.gmailRun(name, func() (*gmail.RunRecord, error) { return stack.Syncer.Run(ctx, gmail.TriggerCLI) })
	case "resync":
		return e.gmailRun(name, func() (*gmail.RunRecord, error) { return stack.Syncer.Resync(ctx, "cli", gmail.TriggerCLI, *yes) })
	case "reclassify":
		return e.gmailRun(name, func() (*gmail.RunRecord, error) { return stack.Syncer.Reclassify(ctx, "cli", gmail.TriggerCLI, *yes) })
	case "purge":
		if err := gmail.Purge(ctx, stack.Repo, v, *yes); err != nil {
			return e.fail(name, err)
		}
		fmt.Fprintln(e.stdout, "all Gmail data and the stored app password were removed")
		return 0
	}
	fmt.Fprint(e.stderr, gmailUsage)
	return 2
}

func (e *env) gmailSchedules(ctx context.Context, st *store.Store, connected bool) error {
	reg, err := jobs.Catalog()
	if err != nil {
		return err
	}
	m := scheduler.NewManager(st, reg, time.Now)
	var changed []string
	verb := "paused until Gmail is connected again"
	if connected {
		if changed, err = m.ResumeGmailSync(ctx); err != nil {
			return err
		}
		verb = "switched back on"
		fmt.Fprintln(e.stdout, "job mail is read as it arrives while Applysmith runs")
	} else if changed, err = m.PauseGmailSync(ctx); err != nil {
		return err
	}
	for _, n := range changed {
		fmt.Fprintf(e.stdout, "schedule %s: %s\n", verb, n)
	}
	return nil
}

func (e *env) gmailRun(name string, run func() (*gmail.RunRecord, error)) int {
	rec, err := run()
	if err != nil {
		return e.fail(name, err)
	}
	fmt.Fprintf(e.stdout, "run %d: %s (%s), %d stored, coverage complete: %v\n", rec.ID, rec.Status, rec.Mode, rec.Counts.Stored, rec.CoverageComplete)
	if rec.Status != "succeeded" {
		fmt.Fprintf(e.stderr, "failed stage %s: %s\n", rec.FailedStage, rec.Error)
		return 1
	}
	return 0
}
