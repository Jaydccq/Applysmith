package engine

import (
	"errors"
	"fmt"
)

// Code is a stable failure class of a page fill.
type Code string

// Failure classes.
const (
	CodeNavigationLimit      Code = "navigation-limit"
	CodeEngineHung           Code = "engine-hung"
	CodeTabClosed            Code = "tab-closed"
	CodeBrowserLost          Code = "browser-lost"
	CodeApplicantUnavailable Code = "applicant-unavailable"
	CodeStartRefused         Code = "start-refused"
	CodeStartLost            Code = "start-lost"
	CodeInjectFailed         Code = "inject-failed"
	CodePollFailed           Code = "poll-failed"
	CodeBundleMissing        Code = "bundle-missing"
	CodeBusy                 Code = "fill-in-progress"
	// CodeTopOriginChanged ends a fill whose tab moved to another origin (same
	// scheme, host and port is the test, not the same site) after the first
	// attempt pinned the top document's origin.
	CodeTopOriginChanged Code = "top-origin-changed"
)

// Error is a typed fill failure.
type Error struct {
	Code    Code
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// Is matches on the code alone.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

func newError(code Code, err error, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Err: err}
}

// CodeOf returns the code of the first Error in err's chain.
func CodeOf(err error) (Code, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Code, true
	}
	return "", false
}
