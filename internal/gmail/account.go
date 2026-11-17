package gmail

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"
	"unicode"

	"applysmith/internal/vault"
)

// secretAppPassword is the vault name of the Google app password.
const secretAppPassword = "gmail-app-password"

const loginRefused = "refused"

// Hooks are called on edges of the login state, never on every failure, so a
// caller can raise one board item per outage.
type Hooks struct {
	OnRefused  func(ctx context.Context, reason string)
	OnRestored func(ctx context.Context)
}

// Account is the stored connection: the mailbox address in the database, its
// app password in the keychain, and whether Gmail last refused them.
type Account struct {
	vault *vault.Vault
	repo  *Repo
	now   func() time.Time
	hooks Hooks
}

// NewAccount builds the account of a workspace.
func NewAccount(v *vault.Vault, repo *Repo, now func() time.Time, hooks Hooks) *Account {
	if now == nil {
		now = time.Now
	}
	return &Account{vault: v, repo: repo, now: now, hooks: hooks}
}

// credentials reads the stored address and app password.
func (a *Account) credentials(ctx context.Context) (Credentials, error) {
	pw, err := a.vault.Get(secretAppPassword)
	if errors.Is(err, vault.ErrNotFound) {
		return Credentials{}, ErrNotConnected
	}
	if err != nil {
		return Credentials{}, err
	}
	addr := a.repo.Mailbox(ctx)
	if addr == "" {
		return Credentials{}, ErrNotConnected
	}
	return Credentials{Address: addr, Password: pw}, nil
}

// connected reads whether an app password is stored, without its value: nil,
// vault.ErrNotFound, or the keychain's error.
func (a *Account) connected() error { return a.vault.Check(secretAppPassword) }

// InputError is an address or app password refused before any sign-in. Its
// text never carries the value.
type InputError struct{ Field, Reason string }

func (e *InputError) Error() string { return e.Reason }

// cleanCredentials checks an address and an app password as typed. Google
// shows an app password as four groups of four letters; the spaces are not
// part of it.
func cleanCredentials(address, password string) (Credentials, error) {
	address = strings.TrimSpace(address)
	if a, err := mail.ParseAddress(address); err != nil || a.Name != "" || a.Address != address {
		return Credentials{}, &InputError{Field: "address", Reason: "the address must be one email address, such as the Gmail address you sign in with"}
	}
	password = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, password)
	if len(password) != 16 || strings.ContainsFunc(password, func(r rune) bool { return !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z') }) {
		return Credentials{}, &InputError{Field: "password", Reason: "a Google app password is 16 letters (the spaces Google shows may be left in); the password of your Google account does not work here"}
	}
	return Credentials{Address: address, Password: password}, nil
}

// save stores a connection Gmail has just accepted. Another account than the
// stored one starts its own sync: the cursor and a bootstrap in progress
// belong to the old mailbox, so they are cleared and the next sync bootstraps
// with the usual lookback; the mail already read stays. It returns the
// address it replaced, empty for the same account or a first connection.
func (a *Account) save(ctx context.Context, c Credentials) (replaced string, err error) {
	if _, err := a.vault.Set(secretAppPassword, c.Password); err != nil {
		return "", err
	}
	now := stamp(a.now())
	kv := map[string]string{keyMailbox: c.Address, keyConnectedAt: now, keyLoginStatus: "", keyLoginError: "", keyLoginRefusedAt: ""}
	if prev := a.repo.Mailbox(ctx); prev != "" && !strings.EqualFold(prev, c.Address) {
		replaced = prev
		kv[keyCursor], kv[keyCursorUpdated], kv[keyResumeToken], kv[keyBootstrapStart] = "", "", "", ""
		kv[keyCursorResetAt] = now
	}
	if err := a.repo.SetState(ctx, kv); err != nil {
		return "", err
	}
	// A refusal, or any sign-in item still open, is answered by connecting.
	if a.hooks.OnRestored != nil {
		a.hooks.OnRestored(ctx)
	}
	return replaced, nil
}

// Disconnect removes the app password. The address and the mail already read
// stay, so connecting the same account again carries on where it stopped. It
// reports whether a password was stored.
func (a *Account) Disconnect(ctx context.Context) (bool, error) {
	was := a.vault.Has(secretAppPassword)
	if err := a.vault.Delete(secretAppPassword); err != nil && !errors.Is(err, vault.ErrNotFound) {
		return was, err
	}
	refused := a.repo.stateOr(ctx, keyLoginStatus) == loginRefused
	if err := a.repo.SetState(ctx, map[string]string{keyConnectedAt: "", keyLoginStatus: "", keyLoginError: "", keyLoginRefusedAt: ""}); err != nil {
		return was, err
	}
	if refused && a.hooks.OnRestored != nil {
		a.hooks.OnRestored(ctx)
	}
	return was, nil
}

// markRefused records Gmail's refusal of the stored credentials. The hook
// fires only when the state changes, so five failed syncs in one outage raise
// one item.
func (a *Account) markRefused(ctx context.Context, cause error) {
	was := a.repo.stateOr(ctx, keyLoginStatus)
	kv := map[string]string{keyLoginStatus: loginRefused, keyLoginError: cause.Error()}
	if was != loginRefused {
		kv[keyLoginRefusedAt] = stamp(a.now())
	}
	_ = a.repo.SetState(ctx, kv)
	if was != loginRefused && a.hooks.OnRefused != nil {
		a.hooks.OnRefused(ctx, cause.Error())
	}
}

// markAccepted clears a refusal once Gmail accepts the stored credentials.
func (a *Account) markAccepted(ctx context.Context) {
	if a.repo.stateOr(ctx, keyLoginStatus) != loginRefused {
		return
	}
	_ = a.repo.SetState(ctx, map[string]string{keyLoginStatus: "", keyLoginError: "", keyLoginRefusedAt: ""})
	if a.hooks.OnRestored != nil {
		a.hooks.OnRestored(ctx)
	}
}

// RefusalDetail is what the sign-in item says, from the refusal that raised
// it: the fix first and in the dashboard's words, because the item is also
// the push that may be cut short. Gmail's own text stays in the Gmail page's
// status.
const RefusalDetail = "Gmail no longer accepts the app password, so no job mail is read. Create a new app password in your Google Account and enter it in Settings, Gmail."
