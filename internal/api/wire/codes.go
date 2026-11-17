// Package wire holds every request, response and event payload of the local
// API as plain Go structs with explicit json tags, plus the scalar types that
// keep the Go and TypeScript sides of the wire in agreement. It imports no
// other product package, so the TypeScript generator can run without any
// embedded asset being built.
package wire

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// APIVersion increases on any breaking wire change.
const APIVersion = 1

// ErrorCode is the closed set of error codes of the response envelope.
type ErrorCode string

const (
	CodeBadRequest           ErrorCode = "bad_request"
	CodeUnauthenticated      ErrorCode = "unauthenticated"
	CodeForbidden            ErrorCode = "forbidden"
	CodeNotFound             ErrorCode = "not_found"
	CodeMethodNotAllowed     ErrorCode = "method_not_allowed"
	CodeConflict             ErrorCode = "conflict"
	CodeRevisionConflict     ErrorCode = "revision_conflict"
	CodePayloadTooLarge      ErrorCode = "payload_too_large"
	CodeUnsupportedMediaType ErrorCode = "unsupported_media_type"
	CodeValidationFailed     ErrorCode = "validation_failed"
	CodeConfigInvalid        ErrorCode = "config_invalid"
	CodeRateLimited          ErrorCode = "rate_limited"
	CodeInternal             ErrorCode = "internal"
	CodeUnavailable          ErrorCode = "unavailable"
)

// ReasonServerStarting is the reason of the unavailable error every route
// answers until the database is ready.
const ReasonServerStarting = "server-starting"

var statusOf = map[ErrorCode]int{
	CodeBadRequest:           http.StatusBadRequest,
	CodeUnauthenticated:      http.StatusUnauthorized,
	CodeForbidden:            http.StatusForbidden,
	CodeNotFound:             http.StatusNotFound,
	CodeMethodNotAllowed:     http.StatusMethodNotAllowed,
	CodeConflict:             http.StatusConflict,
	CodeRevisionConflict:     http.StatusConflict,
	CodePayloadTooLarge:      http.StatusRequestEntityTooLarge,
	CodeUnsupportedMediaType: http.StatusUnsupportedMediaType,
	CodeValidationFailed:     http.StatusUnprocessableEntity,
	CodeConfigInvalid:        http.StatusUnprocessableEntity,
	CodeRateLimited:          http.StatusTooManyRequests,
	CodeInternal:             http.StatusInternalServerError,
	CodeUnavailable:          http.StatusServiceUnavailable,
}

// Status is the HTTP status the code maps to.
func (c ErrorCode) Status() int {
	if s, ok := statusOf[c]; ok {
		return s
	}
	return http.StatusInternalServerError
}

// UnmarshalJSON rejects a code outside the closed set.
func (c *ErrorCode) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if _, ok := statusOf[ErrorCode(s)]; !ok {
		return fmt.Errorf("wire: unknown error code %q", s)
	}
	*c = ErrorCode(s)
	return nil
}

// The generator is syntactic and cannot build a union from the constants
// above, so the closed set is repeated here; a test compares the two.
//
//tygo:emit
var errorCodeUnionTS = `export type ErrorCodeName = typeof CodeBadRequest | typeof CodeUnauthenticated | typeof CodeForbidden | typeof CodeNotFound | typeof CodeMethodNotAllowed | typeof CodeConflict | typeof CodeRevisionConflict | typeof CodePayloadTooLarge | typeof CodeUnsupportedMediaType | typeof CodeValidationFailed | typeof CodeConfigInvalid | typeof CodeRateLimited | typeof CodeInternal | typeof CodeUnavailable;`
