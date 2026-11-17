package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/store"
)

const backupRetention = time.Hour

// backupOps tracks live backups. A backup can run for minutes, far past the
// synchronous deadline, so the route starts it and answers 202; the caller
// polls the operation. Only one runs at a time and the table lives in memory:
// a backup is bound to the server process that holds the writer.
type backupOps struct {
	mu      sync.Mutex
	now     func() time.Time
	items   map[string]*backupEntry
	running bool
	closed  bool
	wg      sync.WaitGroup
}

type backupEntry struct {
	op       wire.BackupOperation
	finished time.Time
}

func newBackupOps(now func() time.Time) *backupOps {
	return &backupOps{now: now, items: map[string]*backupEntry{}}
}

// start runs fn in the background and returns the operation in state running.
func (o *backupOps) start(fn func() (*wire.BackupResult, *apikit.Error)) (wire.BackupOperation, *apikit.Error) {
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return wire.BackupOperation{}, apikit.NewErr(wire.CodeInternal, "internal error")
	}
	id := "bak_" + hex.EncodeToString(raw[:])
	o.mu.Lock()
	defer o.mu.Unlock()
	switch {
	case o.closed:
		return wire.BackupOperation{}, apikit.Unavailable("the server is shutting down")
	case o.running:
		return wire.BackupOperation{}, apikit.NewErr(wire.CodeConflict, "a backup is already running")
	}
	for k, e := range o.items {
		if !e.finished.IsZero() && o.now().Sub(e.finished) > backupRetention {
			delete(o.items, k)
		}
	}
	e := &backupEntry{op: wire.BackupOperation{ID: id, State: wire.BackupRunning}}
	o.items[id], o.running = e, true
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		res, fail := fn()
		o.mu.Lock()
		defer o.mu.Unlock()
		o.running, e.finished = false, o.now()
		if fail != nil {
			e.op.State = wire.BackupFailed
			e.op.Error = &wire.ErrorBody{Code: fail.Code, Message: fail.Message, Retryable: fail.Retryable}
			if fail.Details != nil {
				e.op.Error.Details = fail.Details
			}
			return
		}
		e.op.State, e.op.Result = wire.BackupSucceeded, res
	}()
	return e.op, nil
}

func (o *backupOps) get(id string) (wire.BackupOperation, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	e, ok := o.items[id]
	if !ok {
		return wire.BackupOperation{}, false
	}
	return e.op, true
}

// close refuses new backups and waits for the one in flight for as long as it
// takes: the caller cancels the backup's context first, and the database must
// not close under a copy that is still reading it.
func (o *backupOps) close() {
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
	o.wg.Wait()
}

// createBackup is the live half of `applysmith backup create`: the owning server
// snapshots the database, so no other writer races the copy. It answers 202
// and the snapshot continues in the background.
func (s *Server) createBackup(c *apikit.Call) (apikit.Reply, error) {
	var req wire.BackupRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if !filepath.IsAbs(req.Out) {
		e := apikit.NewErr(wire.CodeValidationFailed, "out must be an absolute path")
		e.Details = &wire.ErrorDetails{Field: "out"}
		return apikit.Reply{}, e
	}
	st, err := s.db()
	if err != nil {
		return apikit.Reply{}, err
	}
	op, e := s.backups.start(func() (*wire.BackupResult, *apikit.Error) {
		m, err := st.Backup(s.bg, store.BackupOptions{
			Out: req.Out, WorkspaceRoot: s.cfg.Workspace.Root, ProductVersion: s.cfg.Version,
		})
		if err != nil {
			ae, unexpected := backupError(err)
			if unexpected {
				s.log.Error("backup failed", "error", err.Error())
			}
			return nil, ae
		}
		return &wire.BackupResult{
			Out: req.Out, SchemaVersion: wire.SafeInt(m.SchemaVersion), WorkspaceID: m.WorkspaceID,
			FileCount: wire.SafeInt(len(m.Files)), CreatedAt: m.CreatedAt,
		}, nil
	})
	if e != nil {
		return apikit.Reply{}, e
	}
	return apikit.Reply{Status: http.StatusAccepted, Location: "/api/backups/" + op.ID, Data: op}, nil
}

func (s *Server) getBackup(c *apikit.Call) (apikit.Reply, error) {
	op, ok := s.backups.get(c.R.PathValue("id"))
	if !ok {
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no such backup")
	}
	return apikit.Reply{Data: op}, nil
}

// backupError maps a failed backup onto the API errors; the second result is
// true for a failure the log should record.
func backupError(err error) (*apikit.Error, bool) {
	switch {
	case errors.Is(err, store.ErrUnsafePath), errors.Is(err, store.ErrConcurrentChange):
		e := apikit.NewErr(wire.CodeValidationFailed, "%s", err.Error())
		e.Details = &wire.ErrorDetails{Field: "out"}
		return e, false
	case errors.Is(err, store.ErrConflict):
		return apikit.NewErr(wire.CodeConflict, "the output path already exists"), false
	case errors.Is(err, context.Canceled):
		return apikit.Unavailable("the server stopped before the backup finished"), false
	}
	return asError(err)
}
