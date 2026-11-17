// Package api is the local HTTP API: one loopback server per workspace, the
// credentials that reach it, the response envelope, event streams, rate limits
// and the route table that declares every route's class.
package api

import (
	"context"
	"errors"
	"syscall"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/store"
	"applysmith/internal/vault"
)

func errUnauthenticated() *apikit.Error {
	return apikit.NewErr(wire.CodeUnauthenticated, "a valid credential is required")
}

// errStarting answers a request that arrives before the database is ready. Its
// reason lets the dashboard tell it from other unavailable answers.
func errStarting() *apikit.Error {
	return apikit.WithReason(apikit.Unavailable("the server is starting"), wire.ReasonServerStarting)
}

func errRateLimited(retryAfter int, reason string) *apikit.Error {
	e := apikit.NewErr(wire.CodeRateLimited, "too many requests; retry in %d seconds", retryAfter)
	e.Details = &wire.ErrorDetails{Reason: reason, RetryAfterSeconds: retryAfter}
	e.Retryable = true
	return e
}

// asError maps any handler failure onto the closed set of API errors. The
// second result is true when the original error is an unexpected one that the
// caller should log.
func asError(err error) (*apikit.Error, bool) {
	var ae *apikit.Error
	switch {
	case errors.As(err, &ae):
		return ae, false
	case errors.Is(err, store.ErrConflict):
		return apikit.NewErr(wire.CodeConflict, "the request conflicts with an earlier one"), false
	case errors.Is(err, store.ErrNotFound):
		return apikit.NewErr(wire.CodeNotFound, "not found"), false
	case errors.Is(err, store.ErrBusy), errors.Is(err, context.DeadlineExceeded):
		return apikit.Unavailable("the server is busy; retry"), false
	case errors.Is(err, store.ErrDiskFull), errors.Is(err, syscall.ENOSPC):
		// Logged: the owner has to act, and the log says where it happened.
		return apikit.Unavailable("the disk is full; free some space, then retry"), true
	case errors.Is(err, context.Canceled):
		// The client left; nobody reads this answer and nothing is wrong.
		return apikit.Unavailable("the request was cancelled"), false
	}
	return apikit.NewErr(wire.CodeInternal, "internal error"), true
}

// Startup failure codes printed by `applysmith serve`.
const (
	CodeServerAlreadyRunning = "server_already_running"
	CodePortInUse            = "port_in_use"
	CodeWorkspaceLocked      = "workspace_locked"
	CodeNonLoopbackBind      = "non_loopback_bind"
)

// StartupError stops the server before it serves anything.
type StartupError struct {
	Code    string
	Message string
	// Port is the port that could not be bound, when the failure is about one.
	Port int
	// OtherWorkspace says the port is held by an Applysmith server of another
	// workspace.
	OtherWorkspace bool
}

func (e *StartupError) Error() string { return e.Code + ": " + e.Message }

func (e *StartupError) Is(target error) bool {
	t, ok := target.(*StartupError)
	return ok && t.Code == e.Code
}

// IsStartupCode reports whether err is a StartupError with the given code.
func IsStartupCode(err error, code string) bool {
	return errors.Is(err, &StartupError{Code: code})
}

// isInfrastructure reports a failure of the machine rather than of the
// request: a handler must not present it to the caller as invalid input.
func isInfrastructure(err error) bool {
	for _, target := range []error{store.ErrBusy, store.ErrDiskFull, store.ErrIO, store.ErrCorrupt, store.ErrReadOnly,
		context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// vaultError maps a keychain failure; no message carries a secret value.
func vaultError(err error) error {
	var ve *vault.Error
	if !errors.As(err, &ve) {
		return err
	}
	switch ve.Code {
	case vault.CodeEmpty, vault.CodeTooLarge, vault.CodeNameInvalid:
		return apikit.InvalidField("key", "%s", ve.Message)
	case vault.CodeNotFound:
		return apikit.NewErr(wire.CodeNotFound, "no key is stored for that provider")
	case vault.CodeEnvManaged:
		return apikit.NewErr(wire.CodeConflict, "%s", ve.Message)
	}
	return apikit.Unavailable("the keychain is not available")
}
