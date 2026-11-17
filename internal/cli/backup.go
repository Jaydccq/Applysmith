package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"applysmith/internal/api"
	"applysmith/internal/api/wire"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

const backupUsage = `usage:
  applysmith backup create --out <new-directory> [--workspace <dir>]
  applysmith backup verify <directory>
  applysmith backup restore <directory> --to <new-workspace>
`

const liveBackupTimeout = 10 * time.Minute

func (e *env) backup(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(e.stderr, backupUsage)
		return 2
	}
	switch args[0] {
	case "create":
		return e.backupCreate(ctx, args[1:])
	case "verify":
		return e.backupVerify(ctx, args[1:])
	case "restore":
		return e.backupRestore(ctx, args[1:])
	}
	fmt.Fprint(e.stderr, backupUsage)
	return 2
}

// backupCreate asks the owning server when one runs, so the snapshot is taken
// by the process that owns the database; otherwise it takes the workspace lock
// and snapshots the database itself.
func (e *env) backupCreate(ctx context.Context, args []string) int {
	fs := e.flags("backup create")
	wsFlag := fs.String("workspace", "", "workspace directory")
	out := fs.String("out", "", "new directory to write the backup to")
	if err := fs.Parse(args); err != nil || *out == "" || fs.NArg() > 0 {
		fmt.Fprint(e.stderr, backupUsage)
		return 2
	}
	root, err := e.workspacePath(*wsFlag)
	if err != nil {
		return e.fail("backup", err)
	}
	ws := workspace.Open(root)
	abs, err := filepath.Abs(*out)
	if err != nil {
		return e.fail("backup", err)
	}

	lock, err := api.AcquireLock(ws.StateDir())
	switch {
	case err == nil:
		defer lock.Release()
		return e.backupOffline(ctx, ws, abs)
	case api.IsStartupCode(err, api.CodeServerAlreadyRunning):
		return e.backupLive(ctx, ws, abs)
	}
	return e.fail("backup", err)
}

func (e *env) backupOffline(ctx context.Context, ws *workspace.Workspace, out string) int {
	db := filepath.Join(ws.StateDir(), store.DBFile)
	if _, err := os.Stat(db); err != nil {
		return e.fail("backup", errNoDatabase)
	}
	m, err := store.BackupOffline(ctx, db, store.BackupOptions{Out: out, WorkspaceRoot: ws.Root, ProductVersion: version})
	if err != nil {
		return e.fail("backup", err)
	}
	fmt.Fprintf(e.stdout, "backup written to %s (schema %d, %d files)\n%s\n", out, m.SchemaVersion, len(m.Files), m.Excluded)
	return 0
}

var backupPollEvery = 250 * time.Millisecond

func (e *env) backupLive(ctx context.Context, ws *workspace.Workspace, out string) int {
	srv, err := e.attachLive(ctx, ws)
	if err != nil {
		return e.fail("backup", err)
	}
	ctx, cancel := context.WithTimeout(ctx, liveBackupTimeout)
	defer cancel()
	call := func(method, path string, body []byte) (*http.Response, error) { return srv.do(ctx, method, path, body) }

	body, _ := json.Marshal(wire.BackupRequest{Out: out})
	var op wire.BackupOperation
	loc, err := e.decodeBackup(call, http.MethodPost, "/api/backups", body, http.StatusAccepted, &op)
	if err != nil {
		return e.fail("backup", err)
	}
	if !strings.HasPrefix(loc, "/api/backups/") {
		return e.fail("backup", fmt.Errorf("the running server answered with an unexpected location %q", loc))
	}
	if err := e.awaitBackup(ctx, call, loc, &op); err != nil {
		return e.fail("backup", err)
	}
	if op.State != wire.BackupSucceeded || op.Result == nil {
		if op.Error != nil {
			return e.fail("backup", fmt.Errorf("the running server could not write the backup: %s: %s", op.Error.Code, op.Error.Message))
		}
		return e.fail("backup", errors.New("the running server reported a failed backup"))
	}
	fmt.Fprintf(e.stdout, "backup written to %s by the running server (schema %d, %d files)\n", op.Result.Out, op.Result.SchemaVersion, op.Result.FileCount)
	return 0
}

// awaitBackup polls the operation until it leaves the running state.
func (e *env) awaitBackup(ctx context.Context, call func(method, path string, body []byte) (*http.Response, error), loc string, op *wire.BackupOperation) error {
	var quiet silence
	for op.State == wire.BackupRunning {
		select {
		case <-ctx.Done():
			return fmt.Errorf("the backup did not finish within %s; it may still be running in the server", liveBackupTimeout)
		case <-time.After(backupPollEvery):
		}
		_, err := e.decodeBackup(call, http.MethodGet, loc, nil, http.StatusOK, op)
		if !quiet.tolerates(ctx, e, err) {
			return err
		}
	}
	return nil
}

// decodeBackup performs one call, requires the status and decodes the
// operation. It returns the Location header.
func (e *env) decodeBackup(call func(method, path string, body []byte) (*http.Response, error), method, path string, body []byte, want int, op *wire.BackupOperation) (string, error) {
	resp, err := call(method, path, body)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errServerSilent, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != want {
		var f wire.Failure
		if json.NewDecoder(resp.Body).Decode(&f) == nil && f.Error.Message != "" {
			return "", fmt.Errorf("the running server refused: %s: %s", f.Error.Code, f.Error.Message)
		}
		return "", fmt.Errorf("the running server answered %s", resp.Status)
	}
	var ok wire.Success[wire.BackupOperation]
	if err := json.NewDecoder(resp.Body).Decode(&ok); err != nil {
		return "", err
	}
	*op = ok.Data
	return resp.Header.Get("Location"), nil
}

func (e *env) backupVerify(ctx context.Context, args []string) int {
	fs := e.flags("backup verify")
	ignoredWorkspace(fs)
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, backupUsage)
		return 2
	}
	m, err := store.VerifyBackup(ctx, pos[0])
	if err != nil {
		return e.fail("backup", notABackup(pos[0], err))
	}
	fmt.Fprintf(e.stdout, "backup verified: schema %d, created %s, %d files, workspace %s\n", m.SchemaVersion, m.CreatedAt, len(m.Files), m.WorkspaceID)
	return 0
}

func (e *env) backupRestore(ctx context.Context, args []string) int {
	fs := e.flags("backup restore")
	ignoredWorkspace(fs)
	to := fs.String("to", "", "new workspace directory to restore into")
	// The directory conventionally comes first; flags may also precede it.
	var dir string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		dir, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if dir == "" && fs.NArg() == 1 {
		dir = fs.Arg(0)
	} else if fs.NArg() > 0 {
		dir = ""
	}
	if dir == "" || *to == "" {
		fmt.Fprint(e.stderr, backupUsage)
		return 2
	}
	if err := store.RestoreBackup(ctx, dir, *to, store.RestoreOptions{}); err != nil {
		return e.fail("backup", notABackup(dir, err))
	}
	abs, _ := filepath.Abs(*to)
	fmt.Fprintf(e.stdout, "restored to %s with a new workspace identity. Schedules and desktop and Telegram notices are off and interrupted runs need review; "+
		"turn them back on under Discovery, Schedules and Settings, Notifications, and set up API keys and sign-in credentials again.\n", abs)
	return 0
}

// notABackup words a directory without a manifest as what it is.
func notABackup(dir string, err error) error {
	if errors.Is(err, store.ErrInvalidBackup) && errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s is not a backup: it has no manifest.json", dir)
	}
	return err
}
