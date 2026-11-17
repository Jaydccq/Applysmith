package vault

import (
	"errors"
	"os"
	"strings"
	"sync"
	"time"
)

// Source says where a secret's value comes from.
type Source string

const (
	SourceEnv      Source = "env"
	SourceKeychain Source = "keychain"
)

// Vault reads and writes the secrets of one workspace.
type Vault struct {
	workspaceID string
	backend     Backend
	getenv      func(string) string

	mu         sync.Mutex
	presence   map[string]presenceEntry // keychain answers of Has, by account
	refreshing map[string]bool          // accounts looked up again beside the callers
}

// presenceTTL is how long Has reuses a keychain answer before looking it up
// again. Each lookup runs the platform keychain tool, which takes tens of
// milliseconds, seconds on a busy machine (the process starts one tool at a
// time), and status pages ask on every load: past this time the kept answer
// is still given while one lookup runs beside the caller. A Set or Delete
// through this vault forgets the name at once; one made by another process
// shows up at the first ask after this time and its lookup.
const presenceTTL = 30 * time.Second

type presenceEntry struct {
	present bool
	at      time.Time
}

// Option configures a Vault.
type Option func(*Vault)

// WithGetenv replaces os.Getenv, so tests control the environment overrides.
func WithGetenv(fn func(string) string) Option { return func(v *Vault) { v.getenv = fn } }

// New returns the vault of one workspace. workspaceID is the opaque identifier
// persisted with the workspace; two workspaces never share a keychain account.
func New(workspaceID string, backend Backend, opts ...Option) (*Vault, error) {
	if workspaceID == "" || strings.Contains(workspaceID, ":") {
		return nil, &Error{Code: CodeNameInvalid, Message: "a workspace id is a non-empty string without a colon"}
	}
	v := &Vault{workspaceID: workspaceID, backend: backend, getenv: os.Getenv}
	for _, o := range opts {
		o(v)
	}
	return v, nil
}

// EnvManaged reports whether the environment supplies the API token, which is
// how a machine without a keychain runs: nothing then needs the keychain.
func EnvManaged(getenv func(string) string) bool {
	return getenv != nil && strings.TrimSpace(getenv(EnvName(APIToken))) != ""
}

// Account is the keychain account of a secret: <workspaceId>:<name>.
func (v *Vault) Account(name string) string { return v.workspaceID + ":" + name }

// envValue returns the environment override, ignoring an empty or
// whitespace-only variable. It is read on every call.
func (v *Vault) envValue(name string) (string, bool) {
	val := v.getenv(EnvName(name))
	if strings.TrimSpace(val) == "" {
		return "", false
	}
	return val, true
}

// Get returns the secret's value: the environment override when present, else
// the keychain. Only in-process callers that act on the value should call it.
func (v *Vault) Get(name string) (string, error) {
	if _, err := Lookup(name); err != nil {
		return "", err
	}
	if val, ok := v.envValue(name); ok {
		remember(val)
		return val, nil
	}
	val, err := v.backend.Get(v.Account(name))
	if err != nil {
		return "", translate(name, err)
	}
	remember(val)
	return val, nil
}

// SetResult reports what a Set did besides storing the value.
type SetResult struct {
	// ShadowedBy names the environment variable that currently overrides the
	// stored value, so the caller can warn that the new value is not in use.
	ShadowedBy string
}

// maxValueBytes bounds a secret before any backend call. go-keyring on macOS
// starts a `security -i` child before it notices that the command exceeds its
// 4096-byte limit, and leaves the child running on that error. 2048 bytes keeps
// the longest namespaced account plus the base64-encoded value under the limit
// and is far above any key or token the registry holds.
const maxValueBytes = 2048

// Set stores the value in the keychain, replacing any existing entry.
func (v *Vault) Set(name, value string) (SetResult, error) {
	if _, err := Lookup(name); err != nil {
		return SetResult{}, err
	}
	if value == "" {
		return SetResult{}, &Error{Code: CodeEmpty, Name: name, Message: "a secret value must not be empty"}
	}
	if len(value) > maxValueBytes {
		remember(value) // refused, but offered as a secret all the same
		return SetResult{}, translate(name, ErrTooLarge)
	}
	defer v.forget(name) // after the write, so a lookup meanwhile is not kept
	if err := v.backend.Set(v.Account(name), value); err != nil {
		return SetResult{}, translate(name, err)
	}
	remember(value)
	var res SetResult
	if _, ok := v.envValue(name); ok {
		res.ShadowedBy = EnvName(name)
	}
	return res, nil
}

// Delete removes the keychain entry. A secret that only the environment
// provides cannot be deleted from here.
func (v *Vault) Delete(name string) error {
	if _, err := Lookup(name); err != nil {
		return err
	}
	defer v.forget(name)
	err := v.backend.Delete(v.Account(name))
	if err == nil {
		return nil
	}
	if _, ok := v.envValue(name); ok && (errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnavailable)) {
		return &Error{Code: CodeEnvManaged, Name: name,
			Message: "provided by the environment; unset " + EnvName(name) + " to remove it"}
	}
	return translate(name, err)
}

// Test confirms that the secret can be read and says from where. Provider
// checks that need the value live with the capabilities that own the provider.
func (v *Vault) Test(name string) (Source, error) {
	if _, err := Lookup(name); err != nil {
		return "", err
	}
	if _, ok := v.envValue(name); ok {
		return SourceEnv, nil
	}
	if _, err := v.backend.Get(v.Account(name)); err != nil {
		return "", translate(name, err)
	}
	return SourceKeychain, nil
}

// Has reports whether the secret is readable, from the environment or the
// keychain, reusing a keychain answer (see presenceTTL). Keychain trouble other
// than absence reads as absent and is not kept.
func (v *Vault) Has(name string) bool { return v.Check(name) == nil }

// Check is Test without the source: nil when the secret is readable, else the
// reason. It reuses a keychain answer (see presenceTTL), so a status page that
// asks on every load does not wait for the keychain tool; only the first ask
// for a name does. Absence is kept; any other keychain trouble is returned and
// not kept.
func (v *Vault) Check(name string) error {
	if _, err := Lookup(name); err != nil {
		return err
	}
	if _, ok := v.envValue(name); ok {
		return nil
	}
	acct := v.Account(name)
	v.mu.Lock()
	e, ok := v.presence[acct]
	again := ok && time.Since(e.at) >= presenceTTL && !v.refreshing[acct]
	if again {
		if v.refreshing == nil {
			v.refreshing = map[string]bool{}
		}
		v.refreshing[acct] = true
	}
	v.mu.Unlock()
	if !ok {
		if err := v.lookPresence(acct); err != nil {
			return translate(name, err)
		}
		return nil
	}
	if again {
		go func() {
			_ = v.lookPresence(acct)
			v.mu.Lock()
			delete(v.refreshing, acct)
			v.mu.Unlock()
		}()
	}
	if e.present {
		return nil
	}
	return translate(name, ErrNotFound)
}

// lookPresence asks the keychain for acct and keeps a present or absent answer.
func (v *Vault) lookPresence(acct string) error {
	_, err := v.backend.Get(acct)
	if err == nil || errors.Is(err, ErrNotFound) {
		v.mu.Lock()
		if v.presence == nil {
			v.presence = map[string]presenceEntry{}
		}
		v.presence[acct] = presenceEntry{present: err == nil, at: time.Now()}
		v.mu.Unlock()
	}
	return err
}

func (v *Vault) forget(name string) {
	v.mu.Lock()
	delete(v.presence, v.Account(name))
	v.mu.Unlock()
}

// Entry is the value-free state of one secret.
type Entry struct {
	Name    string
	Row     Row
	Present bool
	Source  Source // empty when absent
	Err     error  // keychain trouble, distinct from "absent"
}

// List reports presence and source for each name and never a value. The
// keychain cannot enumerate, so callers pass the names the credential index
// and the setup state know about.
func (v *Vault) List(names []string) []Entry {
	out := make([]Entry, 0, len(names))
	for _, n := range names {
		e := Entry{Name: n}
		row, err := Lookup(n)
		if err != nil {
			e.Err = err
			out = append(out, e)
			continue
		}
		e.Row = row
		switch src, err := v.Test(n); {
		case err == nil:
			e.Present, e.Source = true, src
		case !errors.Is(err, ErrNotFound):
			e.Err = err
		}
		out = append(out, e)
	}
	return out
}

// translate turns a backend error into the vault's own text, which never
// quotes the keychain tool.
func translate(name string, err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return &Error{Code: CodeNotFound, Name: name, Message: "no secret is stored under this name"}
	case errors.Is(err, ErrTooLarge):
		return &Error{Code: CodeTooLarge, Name: name, Message: "the platform keychain refused the value as too large"}
	case errors.Is(err, ErrDenied):
		return &Error{Code: CodeKeychainDenied, Name: name, Message: deniedMessage}
	}
	return &Error{Code: CodeKeychainUnavailable, Name: name, Message: unavailableMessage(name)}
}
