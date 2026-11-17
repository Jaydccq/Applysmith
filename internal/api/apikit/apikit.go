// Package apikit is the handler contract of the local API and the helpers
// every handler package shares: the call a handler receives and its reply, the
// typed error, query and path parameters, strict body decoding and the wire
// forms of stored values. internal/api serves the routes; a handler package
// below it imports this one, never internal/api.
package apikit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/lazyregexp"
)

// Call is what a handler receives for one request.
type Call struct {
	W         http.ResponseWriter
	R         *http.Request // its context carries the principal and the deadline
	P         Principal
	RequestID string
	Body      []byte // the buffered body of a json route
	Handled   bool   // set by a handler that wrote the response itself
}

// Reply is a handler's success result.
type Reply struct {
	Status   int // 200 when zero
	Location string
	Data     any
}

// Handler serves one route.
type Handler func(*Call) (Reply, error)

// PrincipalKind says which credential authenticated a request.
type PrincipalKind string

const (
	PrincipalNone         PrincipalKind = ""
	PrincipalDashboard    PrincipalKind = "dashboard"
	PrincipalLocalToken   PrincipalKind = "local-token"
	PrincipalRemoteDevice PrincipalKind = "remote-device"
	// PrincipalAgentProcess is a running pi process, known by its credential.
	PrincipalAgentProcess PrincipalKind = "agent-process"
)

// Principal is who a handler serves. ID is stable across restarts for the
// local kinds, so a retry after a restart lands in the same idempotency scope.
type Principal struct {
	Kind PrincipalKind
	ID   string
}

// CheckSubmissionPrincipal limits a route to the paired extension and the
// dashboard.
func CheckSubmissionPrincipal(p Principal) *Error {
	if p.Kind != PrincipalDashboard && p.Kind != PrincipalLocalToken {
		return NewErr(wire.CodeForbidden, "only a paired client or the dashboard may use this route")
	}
	return nil
}

// MaxPostingURL is the longest posting URL a request may carry.
const MaxPostingURL = 2048

// CheckPostingURL requires an absolute http or https URL of a sane length.
func CheckPostingURL(raw string) *Error {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > MaxPostingURL || u.Host == "" || (!strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https")) {
		return InvalidField("url", "url must be an absolute http or https URL of at most %d characters", MaxPostingURL)
	}
	return nil
}

// Error is a typed API failure. Its message reaches the client, so it never
// carries a credential or a stack.
type Error struct {
	Code      wire.ErrorCode
	Message   string
	Details   *wire.ErrorDetails
	Retryable bool
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

func NewErr(code wire.ErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func WithReason(e *Error, reason string) *Error {
	e.Details = &wire.ErrorDetails{Reason: reason}
	return e
}

func Unavailable(msg string) *Error {
	e := NewErr(wire.CodeUnavailable, "%s", msg)
	e.Retryable = true
	return e
}

var idempotencyKeyPattern = lazyregexp.New(`^[A-Za-z0-9._:-]{8,128}$`)

// IdempotencyKey reads the header every run-creating route requires.
func IdempotencyKey(c *Call) (string, *Error) {
	key := c.R.Header.Get("Idempotency-Key")
	if !idempotencyKeyPattern.MatchString(key) {
		return "", NewErr(wire.CodeBadRequest, "an Idempotency-Key header of 8 to 128 letters, digits, dot, colon, underscore or hyphen is required")
	}
	return key, nil
}

// Pass is a background job over the workspace that the dashboard starts and
// can cancel: it has a run row, a claim that keeps a second one out while it
// runs, and a summary line as its outcome.
type Pass struct {
	Operation, Kind, Stage, Claim, Busy string
	Key                                 string
	Principal                           string
	Input                               []byte
	// Begin, when set, is told the run id once the row exists and before the
	// pass starts.
	Begin func(runID string)
	Run   func(ctx context.Context, progress func(string)) (string, error)
}

func InvalidField(field, format string, args ...any) *Error {
	e := NewErr(wire.CodeValidationFailed, format, args...)
	e.Details = &wire.ErrorDetails{Field: field}
	return e
}

// PathID reads a positive integer path value.
func PathID(c *Call, name string) (int64, *Error) {
	n, err := strconv.ParseInt(c.R.PathValue(name), 10, 64)
	if err != nil || n < 1 || n > wire.MaxSafeInt {
		return 0, InvalidField(name, "%s must be a positive whole number", name)
	}
	return n, nil
}

// Query wraps the URL query with typed reads that report the offending
// parameter. The first problem sticks; callers check Err once.
type Query struct {
	v   url.Values
	Err *Error
}

func QueryOf(c *Call) *Query { return &Query{v: c.R.URL.Query()} }

func (q *Query) Fail(e *Error) {
	if q.Err == nil {
		q.Err = e
	}
}

func (q *Query) Str(name string) string { return strings.TrimSpace(q.v.Get(name)) }

func (q *Query) Integer(name string, def int) int {
	raw := q.Str(name)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		q.Fail(InvalidField(name, "%s must be a whole number", name))
		return def
	}
	return n
}

// Day reads a local calendar day, YYYY-MM-DD, as its midnight; empty is zero.
func (q *Query) Day(name string) time.Time {
	raw := q.Str(name)
	if raw == "" {
		return time.Time{}
	}
	d, err := time.ParseInLocation(time.DateOnly, raw, time.Local)
	if err != nil {
		q.Fail(InvalidField(name, "%s must be a date, YYYY-MM-DD", name))
	}
	return d
}

// Paging reads page (1 or more) and pageSize (1 to 100).
func (q *Query) Paging(defaultSize int) (page, size int) {
	page, size = q.Integer("page", 1), q.Integer("pageSize", defaultSize)
	if q.Err == nil && (page < 1 || size < 1 || size > 100) {
		q.Fail(InvalidField("pageSize", "page must be 1 or more and pageSize between 1 and 100"))
	}
	return page, size
}

func (q *Query) Float(name string) *float64 {
	raw := q.Str(name)
	if raw == "" {
		return nil
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		q.Fail(InvalidField(name, "%s must be a number", name))
		return nil
	}
	return &f
}

func (q *Query) Boolean(name string) bool {
	switch raw := q.Str(name); raw {
	case "":
		return false
	case "true", "1":
		return true
	case "false", "0":
		return false
	default:
		q.Fail(InvalidField(name, "%s must be true or false", name))
		return false
	}
}

// Stamp converts a stored RFC 3339 timestamp; an unreadable one reads as zero.
func Stamp(s string) wire.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return wire.Time{Time: t}
}

func StampPtr(s string) *wire.Time {
	if s == "" {
		return nil
	}
	t := Stamp(s)
	return &t
}

func TimePtr(t time.Time) *wire.Time {
	if t.IsZero() {
		return nil
	}
	return &wire.Time{Time: t.UTC()}
}

func Safe(n int) wire.SafeInt { return wire.SafeInt(n) }

func Safe64(n int64) wire.SafeInt { return wire.SafeInt(n) }

func SafeMap(m map[string]int) map[string]wire.SafeInt {
	out := make(map[string]wire.SafeInt, len(m))
	for k, v := range m {
		out[k] = wire.SafeInt(v)
	}
	return out
}

func NonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// OneOr is one for a count of one, else many.
func OneOr(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

const maxJSONDepth = 32

// Decode strictly decodes a json body into v: no unknown fields, no data after
// the first value and no nesting beyond the limit.
func Decode(body []byte, v any) *Error {
	if len(bytes.TrimSpace(body)) == 0 {
		return NewErr(wire.CodeBadRequest, "the request body is empty")
	}
	if tooDeep(body) {
		return NewErr(wire.CodeValidationFailed, "the JSON nests deeper than %d levels", maxJSONDepth)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return decodeError(err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return NewErr(wire.CodeValidationFailed, "unexpected data after the JSON value")
	}
	return nil
}

func decodeError(err error) *Error {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syn), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return NewErr(wire.CodeBadRequest, "the request body is not valid JSON")
	case errors.As(err, &typ):
		e := NewErr(wire.CodeValidationFailed, "field %q has the wrong type", typ.Field)
		e.Details = &wire.ErrorDetails{Field: typ.Field}
		return e
	}
	if field, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		field = strings.Trim(field, `"`)
		e := NewErr(wire.CodeValidationFailed, "unknown field %q", field)
		e.Details = &wire.ErrorDetails{Field: field}
		return e
	}
	return NewErr(wire.CodeValidationFailed, "%s", strings.TrimPrefix(err.Error(), "json: "))
}

// tooDeep scans brackets outside strings; the decoder itself would happily
// recurse far deeper than the API allows.
func tooDeep(b []byte) bool {
	depth, inString, escaped := 0, false, false
	for _, c := range b {
		switch {
		case inString:
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
		case c == '"':
			inString = true
		case c == '{' || c == '[':
			depth++
			if depth > maxJSONDepth {
				return true
			}
		case c == '}' || c == ']':
			depth--
		}
	}
	return false
}
