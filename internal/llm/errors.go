// Package llm routes model calls: provider clients, task routing and presets,
// the fallback walk, the daily cost cap, the per-call ledger, structured
// output and optional embeddings.
package llm

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"syscall"

	"applysmith/internal/lazyregexp"
)

// Kind classifies a failed attempt or call. The same strings are ledger
// outcomes.
type Kind string

const (
	KindRateLimited      Kind = "rate-limited"
	KindQuota            Kind = "quota-exhausted"
	KindOverloaded       Kind = "overloaded"
	KindEmptyCompletion  Kind = "empty-completion"
	KindStreamStalled    Kind = "stream-stalled"
	KindFirstTokenLate   Kind = "first-token-late"
	KindTimeout          Kind = "timeout"
	KindModelGone        Kind = "model-gone"
	KindNetwork          Kind = "network"
	KindUnconfigured     Kind = "unconfigured"
	KindAuth             Kind = "auth"
	KindAborted          Kind = "aborted"
	KindBadRequest       Kind = "bad-request"
	KindTruncated        Kind = "truncated"
	KindInvalidStructure Kind = "invalid-structured-output"
	KindNoRoute          Kind = "no-route"
	KindUnknownTask      Kind = "unknown-task"
	KindDeadline         Kind = "deadline-exhausted"
	KindFakeFixture      Kind = "fake-fixture-missing"
	KindOther            Kind = "other"
	// KindCostCap refuses an agent call over the daily cap that no cheaper
	// candidate can take.
	KindCostCap Kind = "cost-cap"
	// KindRunaway is a plan-route reply the client left once its text passed
	// twice the row's output ceiling, which that route cannot be sent.
	KindRunaway Kind = "runaway"
)

// advances lists the failure kinds after which the walk tries the next
// candidate.
var advances = map[Kind]bool{
	KindRateLimited: true, KindQuota: true, KindOverloaded: true, KindEmptyCompletion: true,
	KindStreamStalled: true, KindFirstTokenLate: true, KindTimeout: true, KindModelGone: true,
	KindNetwork: true, KindUnconfigured: true, KindRunaway: true,
}

// AttemptError is the redacted outcome of one failed attempt.
type AttemptError struct {
	Provider string
	Model    string
	Kind     Kind
	Status   int
	Message  string
}

// Error is the failure of a call or attempt. Its text never holds a key.
type Error struct {
	Kind    Kind
	Task    string
	Status  int
	Message string
	// Attempts is set when the walk ran more than one attempt.
	Attempts []AttemptError
	// Partial holds the text of a truncated reply.
	Partial string
	// Unreached says the provider's address could not be reached: the name
	// did not resolve, no connection could be made, or the system found the
	// network path broken (no route, or sent packets never acknowledged).
	// Every model of a provider shares its address, so all of them fail the
	// same way. A reset or a server's slow answer is not one: the server was
	// there.
	Unreached bool
	// retryAfter is the server's retry delay for a rate limit, when it sent one.
	retryAfter int
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Kind))
	if e.Task != "" {
		b.WriteString(" (" + e.Task + ")")
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	for _, a := range e.Attempts {
		fmt.Fprintf(&b, "; %s/%s: %s", a.Provider, a.Model, a.Kind)
		if a.Message != "" {
			b.WriteString(" " + a.Message)
		}
	}
	return b.String()
}

// Is matches on the kind, so errors.Is(err, llm.ErrNoRoute) works for any error
// of that kind.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Kind == e.Kind
}

// Sentinels for errors.Is.
var (
	ErrNoRoute      = &Error{Kind: KindNoRoute}
	ErrUnconfigured = &Error{Kind: KindUnconfigured}
	ErrAuth         = &Error{Kind: KindAuth}
)

// KindOf returns the kind of err, or KindOther.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return KindOther
}

// NoModel is how every front end but the command line says that subject has no
// model it can use: it names where one is chosen. The router's own message
// names a command-line remedy, which is the command line's to give.
func NoModel(subject string) string {
	return subject + " has no model it can use; choose one under Settings, Models"
}

// quotaBody matches the 429 and 403 bodies that mean the account is out of
// credit or over its plan, not that it asked too fast or holds a bad key
// (OpenRouter: 403 "Key limit exceeded (total limit)").
var quotaBody = lazyregexp.New(`(?i)insufficient[_ ]quota|out of credits?|insufficient (credits?|balance|funds)|credit balance is too low|exceeded your current quota|billing (hard )?limit|usage limit (reached|exceeded)|key limit exceeded`)

var modelGoneBody = lazyregexp.New(`(?i)model[^.]{0,80}(not found|does not exist|unavailable|not available|deprecated|retired|decommission|no longer)|(no longer|not) (available|supported)[^.]{0,40}model`)

// classifyHTTP maps a non-2xx response. body is already redacted and bounded.
func classifyHTTP(status int, body string) *Error {
	e := &Error{Status: status, Message: body}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		e.Kind = KindAuth
		if status == http.StatusForbidden && quotaBody.MatchString(body) {
			e.Kind = KindQuota
		}
	case status == http.StatusPaymentRequired:
		e.Kind = KindQuota
	case status == http.StatusTooManyRequests:
		e.Kind = KindRateLimited
		if quotaBody.MatchString(body) {
			e.Kind = KindQuota
		}
	case status == http.StatusNotFound || status == http.StatusGone:
		e.Kind = KindModelGone
	case status == http.StatusBadRequest:
		e.Kind = KindBadRequest
		if modelGoneBody.MatchString(body) {
			e.Kind = KindModelGone
		}
	case status == http.StatusRequestTimeout:
		e.Kind = KindTimeout
	case status == http.StatusTooEarly:
		e.Kind = KindOverloaded
	case status == http.StatusNotImplemented:
		e.Kind = KindOther
	case status >= 500:
		e.Kind = KindOverloaded
	case status >= 400:
		e.Kind = KindBadRequest
	default:
		e.Kind = KindOther
	}
	return e
}

var (
	networkText = lazyregexp.New(`(?i)connection (error|reset|refused|closed)|no such host|dial tcp|tls:|x509|unexpected eof|broken pipe|i/o timeout|econnreset|network is unreachable`)
	goneText    = lazyregexp.New(`(?i)\b(410|404)\b.*(status|model)|model.*(retired|no longer available|decommission)`)
)

// classifyError maps a transport error that carries no HTTP status. The status
// is read first elsewhere; the text is the fallback, because some client
// errors arrive without one.
func classifyError(err error) *Error {
	var le *Error
	if errors.As(err, &le) {
		return le
	}
	msg := err.Error()
	var ne net.Error
	var op *net.OpError
	var dns *net.DNSError
	switch {
	case errors.As(err, &dns), errors.As(err, &op) && op.Op == "dial",
		errors.Is(err, syscall.ETIMEDOUT), errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return &Error{Kind: KindNetwork, Message: msg, Unreached: true}
	case errors.As(err, &ne), networkText.MatchString(msg):
		return &Error{Kind: KindNetwork, Message: msg}
	case goneText.MatchString(msg):
		return &Error{Kind: KindModelGone, Message: msg}
	}
	return &Error{Kind: KindOther, Message: msg}
}

var (
	bearerRE   = lazyregexp.New(`(?i)(bearer|basic)\s+[A-Za-z0-9._~+/=-]{6,}`)
	authHdrRE  = lazyregexp.New(`(?i)(authorization|x-api-key|api-key)["']?\s*[:=]\s*["']?[^\s"',}]+`)
	keyShapeRE = lazyregexp.New(`\b(?:sk-[A-Za-z0-9_-]{16,}|AIza[A-Za-z0-9_-]{30,}|nvapi-[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|xox[abprs]-[A-Za-z0-9-]{16,})`)
)

const redacted = "[redacted]"

// maxErrText bounds stored error text.
const maxErrText = 300

// Redact removes the given secrets, authorization headers, bearer tokens and
// strings shaped like API keys from s.
func Redact(s string, secrets ...string) string {
	for _, sec := range secrets {
		if len(sec) >= 4 {
			s = strings.ReplaceAll(s, sec, redacted)
		}
	}
	s = bearerRE.ReplaceAllString(s, "$1 "+redacted)
	s = authHdrRE.ReplaceAllString(s, "$1: "+redacted)
	return keyShapeRE.ReplaceAllString(s, redacted)
}

// boundedText redacts and truncates provider text before it is stored or shown.
func boundedText(s string, secrets ...string) string {
	s = strings.Join(strings.Fields(Redact(s, secrets...)), " ")
	if len(s) > maxErrText {
		s = strings.ToValidUTF8(s[:maxErrText], "")
	}
	return s
}
