package onboarding

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/browser"
	"applysmith/internal/cvparse"
	"applysmith/internal/llm"
	"applysmith/internal/store"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

// Env is what the checks, detectors and writers use. Nothing in it is a
// secret value. Every field after Live is optional: Open fills the database
// ones, and a nil function means the real probe.
type Env struct {
	// Workspace is nil when resolution failed; ResolveErr says why.
	Workspace  *workspace.Workspace
	ResolveErr error
	Backend    vault.Backend
	Port       int
	Live       bool
	// InServer is set when the running server's wizard calls a writer or
	// reads the catalog: notes that point a terminal user at that wizard are
	// left out, and a step's instruction is the dashboard's wording. The
	// server cannot tell the app's window from a browser and needs not: both
	// read the dashboard of a running server.
	InServer bool

	// Store is the workspace database, read-only unless Open was asked to
	// write. It is nil while the database does not exist.
	Store  *store.Store
	Vault  *vault.Vault
	Router *llm.Router
	// Bank is the running server's answer bank; nil makes a new one per use,
	// which parses the file again.
	Bank *answerbank.Service
	// LLMOptions are passed to the router Open builds (tests script a fake).
	LLMOptions []llm.Option
	Getenv     func(string) string
	// HomeDir and ConfigDir locate the per-user setting; empty means the real ones.
	HomeDir, ConfigDir string
	Now                func() time.Time
	// DiskFree and Chrome replace the machine probes (tests).
	DiskFree func(path string) (uint64, error)
	Chrome   func(ctx context.Context) (browser.Chrome, error)
	// BrowserCheck probes the apply and scan windows; nil means this process
	// cannot reach a managed browser.
	BrowserCheck func(ctx context.Context) (BrowserReport, error)
	// GmailFetch replaces the live test fetch of `gmail.fetch` (tests).
	GmailFetch func(ctx context.Context) (int, error)
	// liveBrowser runs the browser check once per doctor run; set by Run.
	liveBrowser func() (BrowserReport, error)
	// SeedHTTP is the client the registry seed downloads with; nil is the default client.
	SeedHTTP *http.Client
	// profile and decisions are read once by EvaluateAll for its detectors,
	// which only read; a writer always reads anew.
	profile   *profileRead
	decisions map[string]string
}

func (e *Env) getenv(k string) string {
	if e.Getenv != nil {
		return e.Getenv(k)
	}
	return ""
}

var errNoWorkspace = errors.New("no workspace could be resolved")

var errNoDatabase = errors.New("the workspace database does not exist yet; complete the workspace step first")

// Open fills Store, Vault and Router from the workspace on disk and returns
// the function that releases them. With write false it reads only and creates
// nothing, so a status or doctor run is safe beside a running server. With
// write true the caller holds the workspace lock; the database and the
// workspace identifier are created if needed.
func (e *Env) Open(ctx context.Context, write bool) (func(), error) {
	e.close()
	if e.Workspace == nil {
		return func() {}, nil
	}
	var id string
	if write {
		var err error
		if id, err = e.Workspace.ID(); err != nil {
			return func() {}, err
		}
	} else {
		var ok bool
		var err error
		if id, ok, err = e.Workspace.ExistingID(); err != nil || !ok {
			return func() {}, err
		}
	}
	path := filepath.Join(e.Workspace.StateDir(), store.DBFile)
	var st *store.Store
	var err error
	if write {
		st, err = store.OpenWorkspace(ctx, e.Workspace.Root, store.Options{WorkspaceID: id})
	} else if st, err = store.OpenReadOnly(ctx, path); errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	if err != nil {
		return func() {}, err
	}
	e.Store = st
	opts := []llm.Option{llm.WithGetenv(e.getenv)}
	v, err := vault.New(id, e.Backend, vault.WithGetenv(e.getenv))
	if err != nil {
		e.close()
		return func() {}, err
	}
	e.Vault = v
	if st != nil {
		e.Router = llm.New(st, v, append(opts, e.LLMOptions...)...)
	}
	return e.close, nil
}

func (e *Env) close() {
	if e.Store != nil {
		_ = e.Store.Close()
	}
	e.Store, e.Vault, e.Router = nil, nil, nil
}

// FieldErrors reports rejected input per field.
type FieldErrors map[string]string

func (f FieldErrors) Error() string {
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + ": " + f[k]
	}
	return strings.Join(parts, "; ")
}

func (f FieldErrors) add(field, msg string) {
	if _, ok := f[field]; !ok {
		f[field] = msg
	}
}

func (f FieldErrors) orNil() error {
	if len(f) == 0 {
		return nil
	}
	return f
}

// HumanError says a person must act before the step can finish.
type HumanError struct{ Instruction string }

func (h *HumanError) Error() string { return h.Instruction }

// EnvError is a failure of the machine, not of the input.
type EnvError struct {
	Message string
	Err     error
}

func (e *EnvError) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *EnvError) Unwrap() error { return e.Err }

// Applied is what a writer returns: the step's new status and what changed.
type Applied struct {
	Step      string    `json:"step"`
	DryRun    bool      `json:"dry_run,omitempty"`
	Wrote     bool      `json:"wrote"`
	Detection Detection `json:"detection"`
	Notes     []string  `json:"notes,omitempty"`
	Untouched []string  `json:"untouched,omitempty"`
	// Restart says the workspace changed and setup must run again in it.
	Restart bool `json:"restart,omitempty"`
	// Draft is the history and skills the CV parser extracted, shown for review.
	Draft *cvparse.Draft `json:"draft,omitempty"`
}
