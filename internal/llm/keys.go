package llm

import (
	"errors"
	"fmt"
	"strings"

	"applysmith/internal/vault"
)

// Vault is the part of internal/vault the router uses. Provider keys live in
// the credentials vault under api-key-<provider id>; the vault also owns the
// headless environment override. This package has no other path to a key.
type Vault interface {
	Get(name string) (string, error)
	Set(name, value string) (vault.SetResult, error)
	Delete(name string) error
}

// KeyName is the vault name of a provider's API key.
func KeyName(providerID string) string { return "api-key-" + providerID }

// apiKey reads a provider's key. A missing key, or a keychain that cannot be
// read, is unconfigured: the call fails before any network request.
func (r *Router) apiKey(providerID string) (string, error) {
	key, err := r.keys.Get(KeyName(providerID))
	if err != nil {
		msg := "no API key for provider " + providerID + "; run `applysmith llm keys set " + providerID + "`"
		var ve *vault.Error
		if errors.As(err, &ve) && ve.Code != vault.CodeNotFound {
			msg = "the key for provider " + providerID + " cannot be read: " + ve.Message
		}
		return "", &Error{Kind: KindUnconfigured, Message: msg}
	}
	return key, nil
}

const minAPIKeyLen = 16

// ValidateAPIKey rejects a value that cannot be a key before it reaches the
// vault: empty, containing whitespace, shorter than 16 characters or a
// placeholder. The error never quotes the value.
func ValidateAPIKey(v string) error {
	low := strings.ToLower(v)
	switch {
	case v == "":
		return errors.New("the key is empty")
	case strings.ContainsFunc(v, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }):
		return errors.New("the key contains whitespace; paste it without spaces or line breaks")
	case len(v) < minAPIKeyLen:
		return fmt.Errorf("the key is shorter than %d characters", minAPIKeyLen)
	case strings.Contains(low, "your_key") || strings.Contains(low, "your-key") || strings.Contains(low, "changeme"),
		strings.HasPrefix(v, "<") && strings.HasSuffix(v, ">"),
		strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]"),
		strings.Trim(low, "x*.-_") == "":
		return errors.New("the key looks like a placeholder")
	}
	return nil
}
