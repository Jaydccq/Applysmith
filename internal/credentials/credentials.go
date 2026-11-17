// Package credentials answers the extension's sign-in question: which stored
// credential may be typed on a page's host, and whether the host's passwords
// are on hold. Passwords come from the vault; the non-secret index and the
// hold record come from the workspace database.
package credentials

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/store"
	"applysmith/internal/vault"
)

// Source labels of a resolved credential.
const (
	SourceSite    = "settings-site"
	SourceDefault = "settings-default"
)

const (
	defaultName = "signin-default"
	siteName    = "signin-site-"
	stampLayout = "2006-01-02T15:04:05.000Z"
	dayLayout   = "2006-01-02"
)

// ErrNotFound means nothing usable is stored for the host: no password, or a
// password with no email to type beside it.
var ErrNotFound = errors.New("credentials: nothing stored for this site")

// Credential is what the sign-in widget may type.
type Credential struct {
	Email, Password, Source string
}

// Service resolves credentials and reads holds.
type Service struct {
	s   *store.Store
	v   *vault.Vault
	now func() time.Time
}

// New returns a service over the open store and the workspace vault. A nil now
// uses the wall clock.
func New(s *store.Store, v *vault.Vault, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{s: s, v: v, now: now}
}

// Resolve returns the credential for a host: its site override, else the
// shared default. A keychain that refuses or is missing is an error and never
// falls through to the default, and nothing is ever read for any other host.
func (c *Service) Resolve(ctx context.Context, host string) (Credential, error) {
	host = strings.ToLower(host)
	cred := Credential{Source: SourceSite}
	name := siteName + host
	var pw string
	var err error
	if _, lookup := vault.Lookup(name); lookup == nil {
		pw, err = c.v.Get(name)
	} else {
		err = vault.ErrNotFound // a host that cannot name an override has none
	}
	switch {
	case err == nil:
	case errors.Is(err, vault.ErrNotFound):
		cred.Source = SourceDefault
		name = defaultName
		if pw, err = c.v.Get(name); errors.Is(err, vault.ErrNotFound) {
			return Credential{}, ErrNotFound
		} else if err != nil {
			return Credential{}, err
		}
	default:
		return Credential{}, err
	}
	cred.Password = pw
	if cred.Email, err = c.email(ctx, name); err != nil {
		return Credential{}, err
	}
	if cred.Email == "" && cred.Source == SourceSite {
		if cred.Email, err = c.email(ctx, defaultName); err != nil {
			return Credential{}, err
		}
	}
	if cred.Email == "" {
		return Credential{}, ErrNotFound
	}
	return cred, nil
}

func (c *Service) email(ctx context.Context, name string) (string, error) {
	var email sql.NullString
	err := c.s.Read().QueryRowContext(ctx, `SELECT email FROM credential_index WHERE name = ?`, name).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return strings.TrimSpace(email.String), err
}

// Hold says whether passwords may be submitted on a host today.
type Hold struct {
	Held bool
	// Lockout is true when the run that recorded it met lockout wording.
	Lockout bool
	// Facts are the stored facts the verdict was reached from, for the caller
	// that asks the password gate and audits its decision.
	Facts gates.PasswordInput
}

// Held reads the host's hold record and applies the lift rules. Any read
// failure is returned; the caller must treat it as held.
func (c *Service) Held(ctx context.Context, host string) (Hold, error) {
	host = strings.ToLower(host)
	db := c.s.Read()
	var recorded, day string
	var lockout bool
	err := db.QueryRowContext(ctx, `SELECT recorded_at, day, lockout FROM signin_holds WHERE host = ?`, host).Scan(&recorded, &day, &lockout)
	if errors.Is(err, sql.ErrNoRows) {
		return Hold{}, nil
	}
	if err != nil {
		return Hold{}, err
	}
	at, err := stampMs(recorded)
	if err != nil {
		return Hold{}, err
	}
	changed, err := latest(ctx, db, `SELECT value FROM credential_meta WHERE key = 'credentials_changed_at'`)
	if err != nil {
		return Hold{}, err
	}
	override, err := latest(ctx, db, `SELECT updated_at FROM credential_index WHERE kind = 'signin_site' AND host = ?`, host)
	if err != nil {
		return Hold{}, err
	}
	signedIn, err := latest(ctx, db, `SELECT max(last_signin_at) FROM credential_index WHERE host = ?`, host)
	if err != nil {
		return Hold{}, err
	}
	in := gates.PasswordInput{Prior: gates.Exhaustion{Recorded: true, At: at, Day: day, Lockout: lockout}, TodayLocal: c.now().Format(dayLayout), CredentialsChangedAt: max(changed, override), SignedInAt: signedIn}
	lifted := gates.HoldLifted(in.Prior, in.TodayLocal, in.CredentialsChangedAt, in.SignedInAt, in.NamedRetry)
	return Hold{Held: !lifted, Lockout: lockout, Facts: in}, nil
}

func stampMs(s string) (int64, error) {
	t, err := time.Parse(stampLayout, s)
	if err != nil {
		return 0, err
	}
	return t.UnixMilli(), nil
}

// latest reads one optional timestamp as Unix milliseconds; none is zero.
func latest(ctx context.Context, db *sql.DB, query string, args ...any) (int64, error) {
	var v sql.NullString
	err := db.QueryRowContext(ctx, query, args...).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !v.Valid) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return stampMs(v.String)
}

// RecordExhausted marks a host's passwords as run out for the rest of the
// local day, in the caller's transaction so it commits with the run record. A
// later record on the same day keeps an earlier lockout flag.
func (c *Service) RecordExhausted(ctx context.Context, tx *sql.Tx, host string, lockout bool, runID string) error {
	now := c.now()
	_, err := tx.ExecContext(ctx, `INSERT INTO signin_holds (host, recorded_at, day, lockout, run_id) VALUES (?1, ?2, ?3, ?4, ?5)
		ON CONFLICT (host) DO UPDATE SET recorded_at = ?2, run_id = ?5,
		lockout = CASE WHEN signin_holds.day = ?3 THEN max(signin_holds.lockout, ?4) ELSE ?4 END, day = ?3`,
		strings.ToLower(host), now.UTC().Format(stampLayout), now.Format(dayLayout), lockout, runID)
	return err
}
