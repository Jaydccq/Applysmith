package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/lazyregexp"
)

var requestIDShape = lazyregexp.New(`^[A-Za-z0-9_-]{8,64}$`)

// newRequestID honors a well-formed client identifier and generates one
// otherwise.
func newRequestID(r *http.Request) string {
	if id := r.Header.Get("X-Request-Id"); requestIDShape.MatchString(id) {
		return id
	}
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "req_" + hex.EncodeToString(b[:])
}

// securityHeaders are on every response, including errors and static files.
func securityHeaders(h http.Header) {
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self'; frame-src 'self' blob:; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	b, err := json.Marshal(body)
	if err != nil {
		status, b = http.StatusInternalServerError, []byte(`{"ok":false,"error":{"code":"internal","message":"internal error","retryable":false}}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

func writeSuccess(w http.ResponseWriter, status int, requestID string, data any) {
	writeJSON(w, status, wire.Success[any]{OK: true, RequestID: requestID, Data: data})
}

func writeError(w http.ResponseWriter, requestID string, e *apikit.Error) {
	if rec, ok := w.(*recorder); ok {
		rec.failure = e
	}
	body := wire.Failure{OK: false, RequestID: requestID, Error: wire.ErrorBody{Code: e.Code, Message: e.Message, Retryable: e.Retryable}}
	if e.Details != nil {
		body.Error.Details = e.Details
	}
	if e.Code == wire.CodeRateLimited && e.Details != nil {
		w.Header().Set("Retry-After", strconv.Itoa(e.Details.RetryAfterSeconds))
	}
	writeJSON(w, e.Code.Status(), body)
}
