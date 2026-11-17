package credentials

import (
	"crypto/rand"
	"errors"
	"math/big"

	"applysmith/internal/vault"
)

const (
	defaultLen = 24
	minLen     = 12
	lower      = "abcdefghijkmnopqrstuvwxyz"
	upper      = "ABCDEFGHJKLMNPQRSTUVWXYZ"
	digits     = "23456789"
	symbols    = "!#$%&*+-=?@^_"
)

// Generate returns a password for a new account from the operating system's
// random source: 24 characters by default, never fewer than 12, never more
// than the site's stated maximum (the minimum of 12 wins over a lower
// maximum), at least one lowercase letter, uppercase letter, digit and symbol,
// and never the same character three times in a row. A maxLen of 0 states no
// maximum.
func Generate(maxLen int) (string, error) {
	n := defaultLen
	if maxLen > 0 && maxLen < n {
		n = maxLen
	}
	if n < minLen {
		n = minLen
	}
	all := lower + upper + digits + symbols
	for {
		b := make([]byte, n)
		for i := range b {
			set := all
			if i < 4 {
				set = []string{lower, upper, digits, symbols}[i]
			}
			c, err := pick(set)
			if err != nil {
				return "", err
			}
			b[i] = c
		}
		if err := shuffle(b); err != nil {
			return "", err
		}
		if !tripled(b) {
			return string(b), nil
		}
	}
}

func pick(set string) (byte, error) {
	i, err := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
	if err != nil {
		return 0, err
	}
	return set[i.Int64()], nil
}

func shuffle(b []byte) error {
	for i := len(b) - 1; i > 0; i-- {
		j, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return err
		}
		b[i], b[j.Int64()] = b[j.Int64()], b[i]
	}
	return nil
}

func tripled(b []byte) bool {
	for i := 2; i < len(b); i++ {
		if b[i] == b[i-1] && b[i] == b[i-2] {
			return true
		}
	}
	return false
}

// GenerateFor returns the password to use for a new account stored under the
// secret name: the existing one when the name already holds a password,
// because the account is possibly registered with it, else a fresh one that
// is stored before the sign-up form is submitted. created reports whether it
// is new.
func (c *Service) GenerateFor(name string, maxLen int) (pw string, created bool, err error) {
	switch got, gerr := c.v.Get(name); {
	case gerr == nil:
		return got, false, nil
	case !errors.Is(gerr, vault.ErrNotFound):
		return "", false, gerr
	}
	pw, err = Generate(maxLen)
	if err != nil {
		return "", false, err
	}
	if _, err := c.v.Set(name, pw); err != nil {
		return "", false, err
	}
	return pw, true, nil
}
