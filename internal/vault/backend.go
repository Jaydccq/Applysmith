package vault

import (
	"errors"
	"os/exec"
	"sort"
	"sync"

	"github.com/zalando/go-keyring"
)

// Backend is the keychain as the vault uses it. Implementations return the
// package sentinels (ErrNotFound, ErrDenied, ErrUnavailable, ErrTooLarge);
// anything else is treated as unavailable.
type Backend interface {
	Get(account string) (string, error)
	Set(account, value string) error
	Delete(account string) error
}

// keyringBackend stores entries through go-keyring. On macOS the library runs
// /usr/bin/security with the value on standard input, never in argv. The entry
// is owned by that tool, so any process of the same user can read it by running
// the tool: the keychain keeps secrets from other users and from the disk, not
// from the user's own processes, which the product trusts.
type keyringBackend struct{ service string }

// NewKeychain returns the operating-system keychain under the product service
// name.
func NewKeychain() Backend { return keyringBackend{service: serviceName} }

func (k keyringBackend) Get(account string) (string, error) {
	v, err := keyring.Get(k.service, account)
	return v, mapKeyringErr(err)
}

func (k keyringBackend) Set(account, value string) error {
	return mapKeyringErr(keyring.Set(k.service, account, value))
}

func (k keyringBackend) Delete(account string) error {
	return mapKeyringErr(keyring.Delete(k.service, account))
}

// mapKeyringErr drops the library's error text: for a failed macOS command it
// can carry the tool's output.
func mapKeyringErr(err error) error {
	var exit *exec.ExitError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, keyring.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, keyring.ErrSetDataTooBig):
		return ErrTooLarge
	case errors.Is(err, keyring.ErrUnsupportedPlatform):
		return ErrUnavailable
	case errors.As(err, &exit):
		return ErrDenied
	}
	return ErrUnavailable
}

// Memory is an in-process Backend for deterministic tests.
type Memory struct {
	mu     sync.Mutex
	items  map[string]string
	fail   error
	maxLen int
}

// NewMemory returns an empty fake keychain.
func NewMemory() *Memory { return &Memory{items: map[string]string{}} }

// FailWith makes every call return err until it is called with nil; use the
// package sentinels to simulate a locked or missing keychain.
func (m *Memory) FailWith(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fail = err
}

// SetMaxLen makes Set refuse longer values with ErrTooLarge; 0 means no limit.
func (m *Memory) SetMaxLen(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maxLen = n
}

// Accounts lists the stored keychain accounts, sorted.
func (m *Memory) Accounts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.items))
	for a := range m.items {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

func (m *Memory) Get(account string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return "", m.fail
	}
	v, ok := m.items[account]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (m *Memory) Set(account, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return m.fail
	}
	if m.maxLen > 0 && len(value) > m.maxLen {
		return ErrTooLarge
	}
	m.items[account] = value
	return nil
}

func (m *Memory) Delete(account string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return m.fail
	}
	if _, ok := m.items[account]; !ok {
		return ErrNotFound
	}
	delete(m.items, account)
	return nil
}
