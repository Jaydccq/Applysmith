package browser

import (
	"errors"
	"fmt"
	"time"
)

// Code is a stable, typed failure class. The API layer maps codes to envelope
// reasons, so a code is never reworded once shipped.
type Code string

// Environment faults.
const (
	CodeChromeMissing       Code = "chrome-missing"
	CodeChromeTooOld        Code = "chrome-too-old"
	CodeChromeUnsupported   Code = "chrome-unsupported"
	CodeProfileUnsafe       Code = "profile-unsafe"
	CodeProfileInUse        Code = "profile-in-use"
	CodeLaunchTimeout       Code = "launch-timeout"
	CodeEndpointUnverified  Code = "browser-endpoint-unverified"
	CodeBrowserDisconnected Code = "browser-disconnected"
	CodePlatformUnsupported Code = "platform-unsupported"
	// CodeBrowserVersion reports a surviving browser of ours that runs another
	// major version than the configured Chrome, as after an on-disk update.
	CodeBrowserVersion Code = "browser-version-mismatch"
)

// State conflicts and rejected input.
const (
	CodeLaneTabLimit     Code = "lane-tab-limit"
	CodeWrongLane        Code = "wrong-lane"
	CodeSchemeNotAllowed Code = "scheme-not-allowed"
	CodeTabClosed        Code = "tab-closed"
	// CodeNotAllowed reports a command a kept tab does not take: the user owns
	// the page, and only the recorder's observer may read it.
	CodeNotAllowed       Code = "not-allowed"
	CodeOperationTimeout Code = "operation-timeout"
	// CodeOperationFailed reports a DevTools operation that failed for a
	// reason no other code names while the browser and the tab were both
	// still there: the browser answered, so it is not lost.
	CodeOperationFailed Code = "operation-failed"
	CodeNetworkOffline  Code = "network-offline"
	// CodeDocumentReplaced reports a call whose frame document went away
	// (navigation or reload) before the call finished; the caller may look for
	// the new document and try again.
	CodeDocumentReplaced Code = "document-replaced"
	// CodeFrameGone reports a frame id that is not part of the tab any more.
	CodeFrameGone Code = "frame-gone"
	// CodeEvalFailed reports a script that threw, or a lookup that found nothing.
	CodeEvalFailed Code = "eval-failed"
	// CodeBindingConflict reports a binding name registered again for another world.
	CodeBindingConflict Code = "binding-conflict"
)

// Cookie import and undo.
const (
	// CodeCookieOutOfScope reports a cookie outside the domains of an import;
	// nothing was changed.
	CodeCookieOutOfScope Code = "cookie-out-of-scope"
	// CodeSnapshotFailed reports that the undo snapshot could not be written;
	// the profile was not touched.
	CodeSnapshotFailed Code = "snapshot-failed"
	// CodeReplaceFailed reports that the browser refused to delete a cookie
	// being replaced; the profile was restored from the snapshot.
	CodeReplaceFailed Code = "replace-failed"
	// CodeNoSnapshot reports an undo with nothing to restore.
	CodeNoSnapshot Code = "no-snapshot"
)

// Error is a typed browser failure.
type Error struct {
	Code    Code
	Message string
	Err     error
	// Op and Bound name the operation and the deadline it ran into, for an
	// operation-timeout, so a consumer branches on the class and not the text.
	Op    string
	Bound time.Duration
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// Is matches on the code alone, so errors.Is(err, &Error{Code: c}) works.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

func newError(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func wrapError(code Code, err error, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Err: err}
}

// CodeOf returns the code of the first typed error in err's chain.
func CodeOf(err error) (Code, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Code, true
	}
	return "", false
}

// EnvironmentFault reports whether err carries one of the environment faults:
// what the computer has or lacks (Chrome, its profile, the platform) stopped
// the browser, not the page it was asked for.
func EnvironmentFault(err error) bool {
	code, _ := CodeOf(err)
	switch code {
	case CodeChromeMissing, CodeChromeTooOld, CodeChromeUnsupported, CodeProfileUnsafe, CodeProfileInUse, CodeLaunchTimeout,
		CodeEndpointUnverified, CodeBrowserDisconnected, CodePlatformUnsupported, CodeBrowserVersion:
		return true
	}
	return false
}

// Unresponsive reports whether err says the browser did not answer: it was
// lost or crashed, did not start in time, or let an operation run past its
// bound. A refusal it answered with (a busy lane, its tab cap, a script that
// threw) and what the computer lacks (Chrome, its profile) are not.
func Unresponsive(err error) bool {
	code, _ := CodeOf(err)
	switch code {
	case CodeBrowserDisconnected, CodeBrowserCrashed, CodeLaunchTimeout, CodeOperationTimeout:
		return true
	}
	return false
}

// timeoutError is an operation-timeout naming the operation and its bound.
func timeoutError(op string, bound time.Duration, err error) *Error {
	return &Error{Code: CodeOperationTimeout, Message: fmt.Sprintf("%s timed out after %v", op, bound), Err: err, Op: op, Bound: bound}
}
