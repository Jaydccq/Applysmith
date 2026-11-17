// Package vault is the only reader and writer of secrets. Secrets live in the
// operating-system keychain under one namespace per workspace, with
// APPLYSMITH_SECRET_<NAME> environment variables as the headless override.
package vault

import (
	"errors"
	"fmt"
)

// Stable error codes.
const (
	CodeNameInvalid         = "secret_name_invalid"
	CodeNotFound            = "secret_not_found"
	CodeEmpty               = "secret_empty"
	CodeTooLarge            = "secret_too_large"
	CodeEnvManaged          = "secret_env_managed"
	CodeKeychainDenied      = "keychain_denied"
	CodeKeychainUnavailable = "keychain_unavailable"
)

// Error is a typed vault failure. Its text is built by the vault and never
// quotes the keychain tool's output, which can echo a value.
type Error struct {
	Code    string
	Name    string // logical secret name, empty when not about one secret
	Message string
}

func (e *Error) Error() string {
	if e.Name != "" {
		return fmt.Sprintf("%s: %s: %s", e.Code, e.Name, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Is matches on the code, so errors.Is(err, vault.ErrNotFound) works for any
// error with that code.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// Sentinels for errors.Is. Backends return these (or errors wrapping them).
var (
	ErrNotFound    = &Error{Code: CodeNotFound}
	ErrTooLarge    = &Error{Code: CodeTooLarge}
	ErrEnvManaged  = &Error{Code: CodeEnvManaged}
	ErrDenied      = &Error{Code: CodeKeychainDenied}
	ErrUnavailable = &Error{Code: CodeKeychainUnavailable}
)

// IsCode reports whether err carries the given code.
func IsCode(err error, code string) bool {
	return errors.Is(err, &Error{Code: code})
}
