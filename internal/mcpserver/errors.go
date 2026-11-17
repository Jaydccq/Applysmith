package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"syscall"

	"applysmith/internal/api/wire"
)

// Code is the closed set of tool error codes.
type Code string

const (
	CodeServerNotRunning Code = "server_not_running"
	CodeUnauthorized     Code = "unauthorized"
	CodeNotFound         Code = "not_found"
	CodeInvalidArgument  Code = "invalid_argument"
	CodeRateLimited      Code = "rate_limited"
	CodeTimeout          Code = "timeout"
	CodeServerError      Code = "server_error"
)

// Remedies are one sentence each and name the command that fixes the cause.
const (
	remedyStart  = "Start the app or run `applysmith serve`."
	remedyPair   = "Pair this client with `applysmith pair`, or set APPLYSMITH_SECRET_API_TOKEN."
	remedyRetry  = "Retry the call in a moment."
	remedyInput  = "Correct the argument and call again."
	remedyServer = "Check the server log, or run `applysmith doctor`."
)

// ToolError is the failure a tool reports. Its text is a small JSON document
// so that a client can read the code without parsing prose.
type ToolError struct {
	Code              Code   `json:"code"`
	Message           string `json:"message"`
	Remedy            string `json:"remedy"`
	RetryAfterSeconds int    `json:"retryAfterSeconds,omitempty"`

	// staleToken marks an answer of the API itself that refused the token,
	// the one failure worth a single retry after re-reading it.
	staleToken bool
}

func (e *ToolError) Error() string {
	b, err := json.Marshal(e)
	if err != nil {
		return string(e.Code) + ": " + e.Message
	}
	return string(b)
}

func newToolError(code Code, remedy, msg string) *ToolError {
	return &ToolError{Code: code, Message: msg, Remedy: remedy}
}

// foreignAnswer is the error for an answer the API never gives.
func foreignAnswer(msg string) *ToolError {
	return newToolError(CodeServerError, remedyServer, msg)
}

func invalidArgument(msg string) *ToolError {
	return newToolError(CodeInvalidArgument, remedyInput, msg)
}

// classify turns a transport or decoding failure into a tool error.
func classify(err error) *ToolError {
	var te *ToolError
	switch {
	case errors.As(err, &te):
		return te
	case errors.Is(err, context.DeadlineExceeded):
		return newToolError(CodeTimeout, remedyRetry, "the local server did not answer in time")
	case errors.Is(err, context.Canceled):
		return newToolError(CodeTimeout, remedyRetry, "the call was cancelled")
	case isUnreachable(err):
		return newToolError(CodeServerNotRunning, remedyStart, "no Applysmith server answers for this workspace")
	}
	return newToolError(CodeServerError, remedyServer, "the local server could not be reached: "+err.Error())
}

func isUnreachable(err error) bool {
	var op *net.OpError
	if errors.Is(err, syscall.ECONNREFUSED) || (errors.As(err, &op) && op.Op == "dial") {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// fromEnvelope maps an API failure onto the closed set.
func fromEnvelope(status int, f wire.Failure, retryAfter string) *ToolError {
	switch f.Error.Code {
	case wire.CodeUnauthenticated, wire.CodeForbidden:
		te := newToolError(CodeUnauthorized, remedyPair, "the local server refused the token: "+f.Error.Message)
		te.staleToken = f.Error.Code == wire.CodeUnauthenticated
		return te
	case wire.CodeNotFound:
		return newToolError(CodeNotFound, "Check the identifier and call again.", f.Error.Message)
	case wire.CodeBadRequest, wire.CodeValidationFailed:
		return newToolError(CodeInvalidArgument, remedyInput, f.Error.Message)
	case wire.CodeRateLimited:
		te := newToolError(CodeRateLimited, remedyRetry, f.Error.Message)
		te.RetryAfterSeconds = retryAfterSeconds(f, retryAfter)
		return te
	case wire.CodeUnavailable:
		return newToolError(CodeServerError, remedyRetry, f.Error.Message)
	}
	if status == 0 {
		status = 500
	}
	return newToolError(CodeServerError, remedyServer, "the local server answered "+strconv.Itoa(status)+": "+f.Error.Message)
}

func retryAfterSeconds(f wire.Failure, header string) int {
	if m, ok := f.Error.Details.(map[string]any); ok {
		if n, ok := m["retryAfterSeconds"].(float64); ok && n > 0 {
			return int(n)
		}
	}
	if n, err := strconv.Atoi(header); err == nil && n > 0 {
		return n
	}
	return 1
}
