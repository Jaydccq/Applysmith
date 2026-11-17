package credentials

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"applysmith/internal/vault"
)

const (
	alternateName   = "signin-alternate-"
	maxLabelRunes   = 40
	minDefaultChars = 12
)

// Management failures a caller maps to a bad request. None of them quotes a
// password.
var (
	ErrEmailRequired = errors.New("credentials: a registration email is needed before a site password or an alternate can be stored")
	ErrBadEmail      = errors.New("credentials: the email address is not valid")
	ErrNothingToSet  = errors.New("credentials: give an email, a password or both")
	ErrLabelTooLong  = fmt.Errorf("credentials: a label is at most %d characters", maxLabelRunes)
	ErrBadOrder      = errors.New("credentials: an order lists every alternate exactly once")
	ErrBadSite       = errors.New("credentials: a site is an http or https address, or a host name")
)

// WeakPasswordError lists the password rules a shared default breaks. It
// carries no part of the password.
type WeakPasswordError struct{ Unmet []string }

func (e *WeakPasswordError) Error() string {
	return "the shared default password must have " + strings.Join(e.Unmet, ", ")
}

// UnmetRules returns the rules of a shared default password that pw breaks: at
// least 12 characters, an uppercase letter, a lowercase letter, a digit and a
// symbol. A per-site override and an alternate accept any non-empty value.
func UnmetRules(pw string) []string {
	var upper, lower, digit, symbol bool
	for _, r := range pw {
		switch {
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsLower(r):
			lower = true
		case unicode.IsDigit(r):
			digit = true
		case !unicode.IsSpace(r) && !unicode.IsLetter(r):
			symbol = true
		}
	}
	var unmet []string
	for _, c := range []struct {
		ok   bool
		rule string
	}{
		{utf8.RuneCountInString(pw) >= minDefaultChars, "at least 12 characters"},
		{upper, "an uppercase letter"},
		{lower, "a lowercase letter"},
		{digit, "a number"},
		{symbol, "a symbol"},
	} {
		if !c.ok {
			unmet = append(unmet, c.rule)
		}
	}
	return unmet
}

// SiteKey is the lowercase host name an account is stored under: the host of
// an http or https address, or a bare host name. Never the registrable domain
// (tenants of one system hold separate accounts) and never the path (one
// account serves every job address of its tenant).
func SiteKey(site string) (string, error) {
	site = strings.TrimSpace(site)
	if site == "" {
		return "", ErrBadSite
	}
	if !strings.Contains(site, "://") {
		site = "https://" + site
	}
	u, err := url.Parse(site)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Hostname() == "" {
		return "", ErrBadSite
	}
	return strings.ToLower(u.Hostname()), nil
}

func (c *Service) stampNow() string { return c.now().UTC().Format(stampLayout) }

// markShared records that a shared password changed, which lifts the apply
// chain's same-day hold.
func (c *Service) markShared(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO credential_meta (key, value) VALUES ('credentials_changed_at', ?1)
		ON CONFLICT (key) DO UPDATE SET value = ?1`, c.stampNow())
	return err
}

func validEmail(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || a.Name != "" {
		return "", ErrBadEmail
	}
	return s, nil
}

// DefaultEmail is the registration email recorded for the shared default, the
// one every alternate types.
func (c *Service) DefaultEmail(ctx context.Context) (string, error) { return c.email(ctx, defaultName) }

// SetDefault stores the shared default. An email alone records it in the index
// and touches no keychain entry and no change stamp; a password must satisfy
// the five rules, is stored under the keychain value alone, and moves
// credentials_changed_at.
func (c *Service) SetDefault(ctx context.Context, email, password string) (vault.SetResult, error) {
	email, err := validEmail(email)
	if err != nil {
		return vault.SetResult{}, err
	}
	if email == "" && password == "" {
		return vault.SetResult{}, ErrNothingToSet
	}
	var res vault.SetResult
	if password != "" {
		if unmet := UnmetRules(password); len(unmet) > 0 {
			return res, &WeakPasswordError{Unmet: unmet}
		}
		if res, err = c.v.Set(defaultName, password); err != nil {
			return res, err
		}
	}
	return res, c.s.Write(ctx, func(tx *sql.Tx) error {
		now := c.stampNow()
		if _, err := tx.ExecContext(ctx, `INSERT INTO credential_index (name, kind, email, has_secret, created_at, updated_at)
			VALUES (?1, 'signin_default', NULLIF(?2, ''), ?3, ?4, ?4)
			ON CONFLICT (name) DO UPDATE SET email = COALESCE(NULLIF(?2, ''), email), has_secret = max(has_secret, ?3), updated_at = ?4`,
			defaultName, email, password != "", now); err != nil {
			return err
		}
		if password == "" {
			return nil
		}
		return c.markShared(ctx, tx)
	})
}

// SetSite stores the override password of one site. The email is the site's
// own, else the shared default's, which must exist. Any non-empty password is
// accepted because overrides exist for sites with hostile rules.
func (c *Service) SetSite(ctx context.Context, site, email, password string) (vault.SetResult, error) {
	host, err := SiteKey(site)
	if err != nil {
		return vault.SetResult{}, err
	}
	email, err = validEmail(email)
	if err != nil {
		return vault.SetResult{}, err
	}
	if email == "" {
		// The row inherits the default email, so a later change of it follows.
		inherited, err := c.DefaultEmail(ctx)
		if err != nil {
			return vault.SetResult{}, err
		}
		if inherited == "" {
			return vault.SetResult{}, ErrEmailRequired
		}
	}
	name := siteName + host
	res, err := c.v.Set(name, password)
	if err != nil {
		return res, err
	}
	return res, c.s.Write(ctx, func(tx *sql.Tx) error {
		now := c.stampNow()
		_, err := tx.ExecContext(ctx, `INSERT INTO credential_index (name, kind, host, email, has_secret, created_at, updated_at)
			VALUES (?1, 'signin_site', ?2, NULLIF(?3, ''), 1, ?4, ?4)
			ON CONFLICT (name) DO UPDATE SET email = COALESCE(NULLIF(?3, ''), email), has_secret = 1, updated_at = ?4`,
			name, host, email, now)
		return err
	})
}

// AddAlternate appends an alternate shared password and returns its id, 12
// random hexadecimal characters so the name says nothing about the password.
// The label defaults to "Alternate N" and is at most 40 characters; the email
// is always the current default email, which must exist.
func (c *Service) AddAlternate(ctx context.Context, label, password string) (string, error) {
	label = strings.TrimSpace(label)
	if utf8.RuneCountInString(label) > maxLabelRunes {
		return "", ErrLabelTooLong
	}
	email, err := c.DefaultEmail(ctx)
	if err != nil {
		return "", err
	}
	if email == "" {
		return "", ErrEmailRequired
	}
	var id string
	for {
		b := make([]byte, 6)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		id = hex.EncodeToString(b)
		var taken int
		if err := c.s.Read().QueryRowContext(ctx, `SELECT count(*) FROM credential_index WHERE name = ?`, alternateName+id).Scan(&taken); err != nil {
			return "", err
		}
		if taken == 0 {
			break
		}
	}
	if _, err := c.v.Set(alternateName+id, password); err != nil {
		return "", err
	}
	err = c.s.Write(ctx, func(tx *sql.Tx) error {
		var n, last int
		if err := tx.QueryRowContext(ctx, `SELECT count(*), COALESCE(max(position), 0) FROM credential_index WHERE kind = 'signin_alternate'`).Scan(&n, &last); err != nil {
			return err
		}
		if label == "" {
			label = fmt.Sprintf("Alternate %d", n+1)
		}
		now := c.stampNow()
		if _, err := tx.ExecContext(ctx, `INSERT INTO credential_index (name, kind, label, position, has_secret, created_at, updated_at)
			VALUES (?1, 'signin_alternate', ?2, ?3, 1, ?4, ?4)`, alternateName+id, label, last+1, now); err != nil {
			return err
		}
		return c.markShared(ctx, tx)
	})
	if err != nil {
		_ = c.v.Delete(alternateName + id) // no orphan secret without an index row
		return "", err
	}
	return id, nil
}

// ReorderAlternates stores a new try order. ids must list every alternate
// exactly once.
func (c *Service) ReorderAlternates(ctx context.Context, ids []string) error {
	return c.s.Write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT name FROM credential_index WHERE kind = 'signin_alternate'`)
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				rows.Close()
				return err
			}
			have[n] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(ids) != len(have) {
			return ErrBadOrder
		}
		seen := map[string]bool{}
		for _, id := range ids {
			n := alternateName + id
			if !have[n] || seen[n] {
				return ErrBadOrder
			}
			seen[n] = true
		}
		now := c.stampNow()
		for i, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE credential_index SET position = ?, updated_at = ? WHERE name = ?`, i+1, now, alternateName+id); err != nil {
				return err
			}
		}
		return c.markShared(ctx, tx)
	})
}

// Delete removes a stored secret and updates the index. The shared default and
// an alternate move credentials_changed_at. A site override or tenant key whose
// account was ever recorded keeps its row with no secret, because deleting a
// password does not un-create the account; any other row is removed. A secret
// that only the environment provides cannot be deleted.
func (c *Service) Delete(ctx context.Context, name string) error {
	row, err := vault.Lookup(name)
	if err != nil {
		return err
	}
	verr := c.v.Delete(name)
	absent := vault.IsCode(verr, vault.CodeNotFound)
	if verr != nil && !absent {
		return verr
	}
	var indexed bool
	err = c.s.Write(ctx, func(tx *sql.Tx) error {
		var history bool
		switch qerr := tx.QueryRowContext(ctx, `SELECT account_created_at IS NOT NULL OR last_signin_at IS NOT NULL FROM credential_index WHERE name = ?`, name).Scan(&history); {
		case errors.Is(qerr, sql.ErrNoRows):
		case qerr != nil:
			return qerr
		default:
			indexed = true
		}
		shared := row.Kind == KindDefault || row.Kind == KindAlternate
		keepRow := row.Kind == KindDefault || (history && (row.Kind == KindSite || row.Kind == KindTenant))
		switch {
		case indexed && keepRow:
			if _, err := tx.ExecContext(ctx, `UPDATE credential_index SET has_secret = 0, updated_at = ? WHERE name = ?`, c.stampNow(), name); err != nil {
				return err
			}
		case indexed:
			if _, err := tx.ExecContext(ctx, `DELETE FROM credential_index WHERE name = ?`, name); err != nil {
				return err
			}
		}
		if shared && (indexed || verr == nil) {
			return c.markShared(ctx, tx)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if absent && !indexed {
		return verr
	}
	return nil
}

// ErrUseAddAlternate says an alternate is created, never named by the caller.
var ErrUseAddAlternate = errors.New("credentials: an alternate password is added with AddAlternate; its name is generated")

// Set stores one registered secret by name. A sign-in default or a site
// override goes through its own rules (password strength, a registration
// email). An alternate is added with AddAlternate. Any other kind is stored
// under its name and noted in the index with its label; kind-specific input
// checks belong to the capability that owns the kind.
func (c *Service) Set(ctx context.Context, name, value, email, label string) (vault.SetResult, error) {
	row, err := vault.Lookup(name)
	if err != nil {
		return vault.SetResult{}, err
	}
	switch row.Kind {
	case KindDefault:
		return c.SetDefault(ctx, email, value)
	case KindSite:
		return c.SetSite(ctx, strings.TrimPrefix(name, siteName), email, value)
	case KindAlternate:
		return vault.SetResult{}, ErrUseAddAlternate
	}
	label = strings.TrimSpace(label)
	if utf8.RuneCountInString(label) > maxLabelRunes {
		return vault.SetResult{}, ErrLabelTooLong
	}
	res, err := c.v.Set(name, value)
	if err != nil {
		return res, err
	}
	var system, tenant string
	if row.Kind == KindTenant {
		parts := strings.SplitN(strings.TrimPrefix(name, "tenant-"), "-", 2)
		system, tenant = parts[0], parts[1]
	}
	return res, c.s.Write(ctx, func(tx *sql.Tx) error {
		now := c.stampNow()
		_, err := tx.ExecContext(ctx, `INSERT INTO credential_index (name, kind, label, system, tenant, has_secret, created_at, updated_at)
			VALUES (?1, ?2, ?3, NULLIF(?4, ''), NULLIF(?5, ''), 1, ?6, ?6)
			ON CONFLICT (name) DO UPDATE SET label = CASE WHEN ?3 = '' THEN label ELSE ?3 END, has_secret = 1, updated_at = ?6`,
			name, row.Kind, label, system, tenant, now)
		return err
	})
}

// Test says whether the secret can be read and from where, never its value.
// Provider checks that need a live call belong to the capability owning the
// kind; sign-in passwords have none because they cannot be tried without a site.
func (c *Service) Test(name string) (vault.Source, error) { return c.v.Test(name) }

// Entry is one stored secret as the management surface shows it: what exists
// and where it came from, never a value.
type Entry struct {
	Name, Kind, Label, Host, System, Tenant, Email string
	// Position is the try order of an alternate, 0 for any other entry.
	Position int
	// Indexed says the index holds a row; HasSecret is what that row claims.
	Indexed, HasSecret bool
	// Present and Source come from the vault; Problem is the vault's error
	// code when the keychain could not be read, which is not "absent".
	Present bool
	Source  vault.Source
	Problem string
	// Account history, RFC 3339 text or empty.
	CreatedAt, UpdatedAt, AccountCreatedAt, LastSigninAt, LastOutcome string
}

// Entries lists every index row with the vault's presence and source, plus the
// registered single-name secrets that exist without an index row, in a stable
// order: kind, then position, then name.
func (c *Service) Entries(ctx context.Context) ([]Entry, error) {
	rows, err := c.s.Read().QueryContext(ctx, `SELECT name, kind, label, COALESCE(host, ''), COALESCE(system, ''), COALESCE(tenant, ''), COALESCE(email, ''),
		COALESCE(position, 0), has_secret, created_at, updated_at, COALESCE(account_created_at, ''), COALESCE(last_signin_at, ''), COALESCE(last_outcome, '')
		FROM credential_index ORDER BY kind, COALESCE(position, 1<<30), name`)
	if err != nil {
		return nil, err
	}
	var out []Entry
	indexed := map[string]bool{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Name, &e.Kind, &e.Label, &e.Host, &e.System, &e.Tenant, &e.Email, &e.Position,
			&e.HasSecret, &e.CreatedAt, &e.UpdatedAt, &e.AccountCreatedAt, &e.LastSigninAt, &e.LastOutcome); err != nil {
			rows.Close()
			return nil, err
		}
		e.Indexed = true
		indexed[e.Name] = true
		out = append(out, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, r := range vault.Registry() {
		if !strings.Contains(r.NamePattern, "<") && !indexed[r.NamePattern] {
			out = append(out, Entry{Name: r.NamePattern, Kind: r.Kind})
		}
	}
	names := make([]string, len(out))
	for i := range out {
		names[i] = out[i].Name
	}
	for i, ve := range c.v.List(names) {
		out[i].Present, out[i].Source = ve.Present, ve.Source
		var verr *vault.Error
		if errors.As(ve.Err, &verr) {
			out[i].Problem = verr.Code
		}
	}
	return out, nil
}
