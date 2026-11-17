package credentials

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"applysmith/internal/vault"
)

// Candidate kinds, in the vocabulary of the index.
const (
	KindTenant    = "tenant"
	KindSite      = "signin_site"
	KindDefault   = "signin_default"
	KindAlternate = "signin_alternate"
)

// Candidate is one stored password the account stage may try. Label names the
// index row and never the password; Password is a secret and is never logged.
type Candidate struct {
	// Label is the spec's label: tenant-key, settings-site, settings-default
	// or alternate-<n>. Name is the secret's registry name.
	Label    string
	Name     string
	Kind     string
	Email    string
	Password string
}

// namedSystems are the systems whose passwords are kept per tenant.
var namedSystems = map[string]bool{"greenhouse": true, "lever": true, "ashby": true, "workday": true}

// IsNamed reports whether a system keeps a tenant key.
func IsNamed(system string) bool { return namedSystems[strings.ToLower(system)] }

func tenantName(system, tenant string) string {
	return "tenant-" + strings.ToLower(system) + "-" + strings.ToLower(tenant)
}

// Candidates lists the stored passwords to try on a host, in the order the
// account stage tries them. For a named system: the tenant key, then the
// host's site override, then the shared default, then the alternates in
// position order. For a generic host: the site override, the shared default,
// the alternates. An identical email and password pair appears once. A
// keychain that refuses is an error and never skipped over.
func (c *Service) Candidates(ctx context.Context, host, system, tenant string) ([]Candidate, error) {
	host = strings.ToLower(host)
	var names []struct{ name, kind string }
	if IsNamed(system) && tenant != "" {
		names = append(names, struct{ name, kind string }{tenantName(system, tenant), KindTenant})
	}
	names = append(names, struct{ name, kind string }{siteName + host, KindSite}, struct{ name, kind string }{defaultName, KindDefault})
	rows, err := c.s.Read().QueryContext(ctx, `SELECT name FROM credential_index WHERE kind = 'signin_alternate' ORDER BY COALESCE(position, 1<<30), name`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, struct{ name, kind string }{n, KindAlternate})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	defEmail, err := c.email(ctx, defaultName)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Candidate
	alt := 0
	for _, n := range names {
		if _, lookup := vault.Lookup(n.name); lookup != nil {
			continue
		}
		pw, err := c.v.Get(n.name)
		if errors.Is(err, vault.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		email, err := c.email(ctx, n.name)
		if err != nil {
			return nil, err
		}
		if email == "" {
			email = defEmail
		}
		if email == "" {
			continue // a password with no email to type beside it is not a candidate
		}
		key := strings.ToLower(email) + "\x00" + pw
		if seen[key] {
			continue
		}
		seen[key] = true
		label := map[string]string{KindTenant: "tenant-key", KindSite: "settings-site", KindDefault: "settings-default"}[n.kind]
		if n.kind == KindAlternate {
			alt++
			label = fmt.Sprintf("alternate-%d", alt)
		}
		out = append(out, Candidate{Label: label, Name: n.name, Kind: n.kind, Email: email, Password: pw})
	}
	return out, nil
}

// Keep records the candidate that signed in, or reached "verify your email",
// which proves the password. On a named system it becomes the tenant key; on a
// generic host an alternate becomes that site's override. The shared default
// is never copied into a site override.
func (c *Service) Keep(ctx context.Context, host, system, tenant string, cand Candidate) error {
	host = strings.ToLower(host)
	var name, kind string
	switch {
	case IsNamed(system) && tenant != "" && cand.Kind != KindTenant:
		name, kind = tenantName(system, tenant), KindTenant
	case !IsNamed(system) && cand.Kind == KindAlternate:
		name, kind = siteName+host, KindSite
	default:
		return c.stamp(ctx, cand.Name, host)
	}
	if _, err := c.v.Set(name, cand.Password); err != nil {
		return err
	}
	return c.s.Write(ctx, func(tx *sql.Tx) error {
		now := c.now().UTC().Format(stampLayout)
		_, err := tx.ExecContext(ctx, `INSERT INTO credential_index (name, kind, host, system, tenant, email, has_secret, created_at, updated_at, account_created_at, last_signin_at, last_outcome)
			VALUES (?1, ?2, ?3, NULLIF(?4, ''), NULLIF(?5, ''), ?6, 1, ?7, ?7, ?7, ?7, 'signed-in')
			ON CONFLICT (name) DO UPDATE SET email = ?6, has_secret = 1, updated_at = ?7, account_created_at = COALESCE(account_created_at, ?7), last_signin_at = ?7, last_outcome = 'signed-in'`,
			name, kind, host, system, tenant, cand.Email, now)
		return err
	})
}

// stamp records a sign-in on the index row the candidate came from, when that
// row belongs to the host.
func (c *Service) stamp(ctx context.Context, name, host string) error {
	return c.s.Write(ctx, func(tx *sql.Tx) error {
		now := c.now().UTC().Format(stampLayout)
		_, err := tx.ExecContext(ctx, `UPDATE credential_index SET account_created_at = COALESCE(account_created_at, ?1), last_signin_at = ?1, last_outcome = 'signed-in' WHERE name = ?2 AND (host = ?3 OR host IS NULL)`, now, name, host)
		return err
	})
}

// Created records that an account was created on a host with the run's
// sign-up, as a created account in the index. The row exists afterwards even
// when the password was stored without one.
func (c *Service) Created(ctx context.Context, host, system, tenant, email, name string) error {
	kind := KindSite
	if IsNamed(system) && tenant != "" {
		kind = KindTenant
	}
	return c.s.Write(ctx, func(tx *sql.Tx) error {
		now := c.now().UTC().Format(stampLayout)
		_, err := tx.ExecContext(ctx, `INSERT INTO credential_index (name, kind, host, system, tenant, email, has_secret, created_at, updated_at, account_created_at, last_signin_at, last_outcome)
			VALUES (?1, ?2, ?3, NULLIF(?4, ''), NULLIF(?5, ''), ?6, 1, ?7, ?7, ?7, ?7, 'created')
			ON CONFLICT (name) DO UPDATE SET email = ?6, has_secret = 1, account_created_at = COALESCE(account_created_at, ?7), last_signin_at = ?7, last_outcome = 'created', updated_at = ?7`,
			name, kind, strings.ToLower(host), system, tenant, email, now)
		return err
	})
}

// Remove deletes a password the account stage saved for a form the site then
// refused, and its index row. A password the caller chose is never passed here.
func (c *Service) Remove(ctx context.Context, name string) error {
	if err := c.v.Delete(name); err != nil && !errors.Is(err, vault.ErrNotFound) {
		return err
	}
	return c.s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM credential_index WHERE name = ?`, name)
		return err
	})
}

// Account is a stored sign-in account as the session sync lists it: the host
// of a site override, or the system and tenant of a tenant key (whose host the
// profile's cookies reveal).
type Account struct{ Host, System, Tenant string }

// StoredAccounts lists the accounts the index knows, never a value.
func (c *Service) StoredAccounts(ctx context.Context) ([]Account, error) {
	rows, err := c.s.Read().QueryContext(ctx, `SELECT COALESCE(host, ''), COALESCE(system, ''), COALESCE(tenant, '') FROM credential_index
		WHERE kind IN ('signin_site', 'tenant') AND has_secret = 1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.Host, &a.System, &a.Tenant); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
