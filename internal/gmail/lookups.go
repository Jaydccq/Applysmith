package gmail

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"applysmith/internal/flight"
	"applysmith/internal/gmail/extract"
	"applysmith/internal/lazyregexp"
)

// Error kinds of a bounded mail lookup, mapped by the API layer to its
// not_found, bad_request and unavailable envelopes.
const (
	KindNotFound    = "not_found"
	KindBadRequest  = "bad_request"
	KindUnavailable = "unavailable"
)

// Detail reasons of an unavailable answer: Gmail is not connected or refused
// the app password, or could not be reached.
const (
	ReasonNotSignedIn = "gmail_not_signed_in"
	ReasonUnreachable = "gmail_unreachable"
)

// ReasonLinkRefused details a not-found: a mail from the sender named the host
// but carried no link the locks accept. An activation mail's address printed
// as text is read under the same locks (Workday's own tenant prints it); a
// reset mail's is not, and an address the locks refuse is never opened.
const ReasonLinkRefused = "link_refused"

// LookupError is the failure of a bounded lookup. Its text never carries a
// code, a link or a message body.
type LookupError struct {
	Kind      string
	Reason    string
	Retryable bool
	Message   string
}

func (e *LookupError) Error() string { return e.Message }

func badRequest(format string, a ...any) *LookupError {
	return &LookupError{Kind: KindBadRequest, Message: fmt.Sprintf(format, a...)}
}

// failureFor maps a Gmail error to the lookup answer. Only a missing or
// refused app password is the user's to fix; Gmail's transient refusals are
// not, because entering a new password would fix nothing. Any other failure
// is retryable and is never reported as not found.
func failureFor(err error) *LookupError {
	if IsAuthFailure(err) {
		return &LookupError{Kind: KindUnavailable, Reason: ReasonNotSignedIn, Message: err.Error() + ". No mail can be read until then."}
	}
	return &LookupError{Kind: KindUnavailable, Reason: ReasonUnreachable, Retryable: true, Message: "Gmail could not be reached; try again shortly"}
}

// Lookups are the bounded, read-only lookups the apply chain and the extension
// make at request time. They hold no database handle on purpose: they read no
// stored mail and write nothing.
type Lookups struct {
	Client *Client
	Now    func() time.Time
	// Account reads the connected mailbox address from the stored state; the
	// demo gate compares it with the persona's alias. Nil reads as none.
	Account func(context.Context) string

	// flights shares one running lookup between identical concurrent requests.
	flights flight.Group
}

// Mailbox is the connected Gmail account's address, or "" when none is.
func (l *Lookups) Mailbox(ctx context.Context) string {
	if l.Account == nil {
		return ""
	}
	return l.Account(ctx)
}

func (l *Lookups) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

var bareDomainRE = lazyregexp.New(`^[a-z0-9-]+(?:\.[a-z0-9-]+)+$`)

// Message limits of the live lookups.
const (
	otpMaxMessages  = 10
	linkMaxMessages = 20
)

// OTPRequest is the input of GET /api/gmail/otp/latest.
type OTPRequest struct {
	Since      string // RFC 3339, required
	FromDomain string // bare domain, optional
}

// OTPResult is the code with its received time and sender. It belongs in the
// response body only.
type OTPResult struct {
	Code       string
	ReceivedAt time.Time
	From       string
	Subject    string
}

// ParseOTPRequest validates the parameters.
func ParseOTPRequest(r OTPRequest) (since time.Time, domain string, err *LookupError) {
	if strings.TrimSpace(r.Since) == "" {
		return time.Time{}, "", badRequest("missing since query parameter")
	}
	t, perr := time.Parse(time.RFC3339Nano, strings.TrimSpace(r.Since))
	if perr != nil {
		return time.Time{}, "", badRequest("since must be an RFC 3339 instant")
	}
	d := strings.ToLower(strings.TrimSpace(r.FromDomain))
	if d != "" && !bareDomainRE.MatchString(d) {
		return time.Time{}, "", badRequest("fromDomain must be a bare domain")
	}
	return t, d, nil
}

func afterClause(t time.Time) string { return fmt.Sprintf("after:%d", t.Unix()) }

// anywhere makes a lookup's search answer in Spam too: All Mail leaves Spam
// out, Gmail answers a search of the Spam folder only for a query that asks
// for it, and mail from a sender the account never wrote to often lands there.
const anywhere = " in:anywhere"

// fetchNewestFirst runs a search and fetches up to max messages, newest
// received first.
func (l *Lookups) fetchNewestFirst(ctx context.Context, q string, max int) ([]*Message, error) {
	return l.Client.Find(ctx, q, max)
}

// LatestCode makes its own bounded lookup for the newest verification code
// received strictly after since, optionally from one sender domain. The search
// bound is whole seconds and inclusive, so it over-fetches; the strict
// per-message filter decides. Identical concurrent requests share one lookup,
// which does not end with the request that started it.
func (l *Lookups) LatestCode(ctx context.Context, since time.Time, fromDomain string) (OTPResult, error) {
	key := fmt.Sprintf("otp|%d|%s", since.UnixMilli(), fromDomain)
	v, err := l.flights.Do(ctx, key, func(ctx context.Context) (any, error) { return l.latestCode(ctx, since, fromDomain) })
	var lerr *LookupError
	switch {
	case errors.As(err, &lerr):
		return OTPResult{}, err
	case err != nil:
		// This caller's own context ended while the lookup ran.
		return OTPResult{}, failureFor(err)
	}
	return v.(OTPResult), nil
}

func (l *Lookups) latestCode(ctx context.Context, since time.Time, fromDomain string) (OTPResult, error) {
	q := afterClause(since) + anywhere
	if fromDomain != "" {
		q += " from:" + fromDomain
	}
	q += " " + extract.OTPQueryTerms()
	msgs, err := l.fetchNewestFirst(ctx, q, otpMaxMessages)
	if err != nil {
		return OTPResult{}, failureFor(err)
	}
	for _, m := range msgs {
		if m.InternalMS <= since.UnixMilli() {
			continue
		}
		if fromDomain != "" && !extract.DomainOrSub(extract.SenderDomain(m.From), fromDomain) {
			continue
		}
		if code := extract.Code(m.Text, m.Subject); code != "" {
			return OTPResult{Code: code, ReceivedAt: time.UnixMilli(m.InternalMS).UTC(), From: m.From, Subject: m.Subject}, nil
		}
	}
	return OTPResult{}, &LookupError{Kind: KindNotFound, Message: "no qualifying verification email found"}
}

// Link lookup bounds.
const (
	DefaultSinceDays = 30
	MaxSinceDays     = 90
)

// activationSenders and resetSenders list by sender rather than phrasing: the
// extractor requires the same sender domain, so anything the query admits that
// it rejects costs one fetch and can never be returned.
const (
	activationSenders = "{from:" + extract.WorkdaySenderDomain + " from:" + extract.SuccessFactorsSenderDomain + "}"
	resetSenders      = "from:" + extract.WorkdaySenderDomain
)

// LinkRequest holds the raw parameters of an activation or reset lookup.
type LinkRequest struct {
	Host      string
	SinceDays string // activation only; empty means the default
	After     string // RFC 3339; required for a reset
}

// LinkQuery is a validated link lookup.
type LinkQuery struct {
	Host      string
	SinceDays int
	After     time.Time // zero when absent
}

// ParseLinkRequest validates the parameters. A reset lookup requires after, so
// a reset mail older than the run's own press can never come back; a missing
// host is refused before any mail is read.
func ParseLinkRequest(r LinkRequest, reset bool) (LinkQuery, *LookupError) {
	q := LinkQuery{Host: strings.ToLower(strings.TrimSpace(r.Host)), SinceDays: DefaultSinceDays}
	if q.Host == "" {
		return q, badRequest("missing host query parameter")
	}
	if !reset && strings.TrimSpace(r.SinceDays) != "" {
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(r.SinceDays), "%d", &n); err != nil || fmt.Sprint(n) != strings.TrimSpace(r.SinceDays) || n < 1 || n > MaxSinceDays {
			return q, badRequest("sinceDays must be 1 to %d", MaxSinceDays)
		}
		q.SinceDays = n
	}
	if strings.TrimSpace(r.After) == "" {
		if reset {
			return q, badRequest("missing after query parameter")
		}
		return q, nil
	}
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(r.After))
	if err != nil {
		return q, badRequest("after must be an RFC 3339 instant")
	}
	q.After = t
	return q, nil
}

// LinkResult is a qualifying account link. The URL is a bearer secret: it goes
// in a response body and nowhere else.
type LinkResult struct {
	URL        string
	Host       string
	Reason     string
	ReceivedAt time.Time
	From       string
	Subject    string
}

// ActivationLink looks for an account-activation mail for exactly one tenant
// host, newest first, at most 20 messages. A message that yields no
// qualifying link is skipped and the search continues.
func (l *Lookups) ActivationLink(ctx context.Context, q LinkQuery) (LinkResult, error) {
	return l.tenantLink(ctx, q, activationSenders, extract.ActivationLink, "activation email")
}

// ResetLink is the same for a password-reset mail from Workday's sender.
func (l *Lookups) ResetLink(ctx context.Context, q LinkQuery) (LinkResult, error) {
	return l.tenantLink(ctx, q, resetSenders, extract.ResetLink, "password-reset email")
}

func (l *Lookups) tenantLink(ctx context.Context, q LinkQuery, senders string, find func(extract.LinkInput) *extract.LinkMatch, noun string) (LinkResult, error) {
	floor := l.now().Add(-time.Duration(q.SinceDays) * 24 * time.Hour)
	if !q.After.IsZero() && q.After.After(floor) {
		floor = q.After
	}
	msgs, err := l.fetchNewestFirst(ctx, afterClause(floor)+anywhere+" "+senders, linkMaxMessages)
	if err != nil {
		return LinkResult{}, failureFor(err)
	}
	named := false
	for _, m := range msgs {
		// The search bound is whole seconds and inclusive; the exclusive bound
		// is enforced here at the millisecond.
		if !q.After.IsZero() && m.InternalMS <= q.After.UnixMilli() {
			continue
		}
		if match := find(extract.LinkInput{HTML: m.HTML, Text: m.Plain, From: m.From, Host: q.Host}); match != nil {
			return LinkResult{URL: match.URL, Host: match.Host, Reason: match.Reason, ReceivedAt: time.UnixMilli(m.InternalMS).UTC(), From: m.From, Subject: m.Subject}, nil
		}
		named = named || strings.Contains(strings.ToLower(m.HTML+" "+m.Plain), "://"+q.Host+"/")
	}
	if named {
		return LinkResult{}, &LookupError{Kind: KindNotFound, Reason: ReasonLinkRefused, Message: fmt.Sprintf("the %s for %s came with no link the run may open", noun, q.Host)}
	}
	return LinkResult{}, &LookupError{Kind: KindNotFound, Message: fmt.Sprintf("no %s found for %s", noun, q.Host)}
}

// SigninSearchTerms is the vocabulary of the sign-in link search.
var SigninSearchTerms = []string{
	"sign-in link", "login link", "magic link", "secure link", "link to sign in", "view application", "edit application",
	"continue your application", "sign in to", "log in to", "access your account", "complete your application",
	"verify your email", "confirm your email", "one-time link",
}

// SigninResult is a sign-in link offered for display only.
type SigninResult struct {
	URL         string
	Host        string
	ReceivedAt  time.Time
	From        string
	Subject     string
	Reasons     []string
	DisplayOnly bool
}

// LatestSigninLink runs the same bounded, read-only, strictly-newer lookup as
// the code lookup, at most 10 messages, and returns the link for display. No
// code path fetches or navigates to it.
func (l *Lookups) LatestSigninLink(ctx context.Context, since time.Time) (SigninResult, error) {
	var b strings.Builder
	for i, t := range SigninSearchTerms {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(`"` + t + `"`)
	}
	msgs, err := l.fetchNewestFirst(ctx, afterClause(since)+anywhere+" {"+b.String()+"}", otpMaxMessages)
	if err != nil {
		return SigninResult{}, failureFor(err)
	}
	for _, m := range msgs {
		if m.InternalMS <= since.UnixMilli() {
			continue
		}
		if match := extract.SigninLink(extract.SigninInput{HTML: m.HTML, Text: m.Plain, Subject: m.Subject}); match != nil {
			return SigninResult{URL: match.URL, Host: match.Host, ReceivedAt: time.UnixMilli(m.InternalMS).UTC(), From: m.From, Subject: m.Subject, Reasons: match.Reasons, DisplayOnly: true}, nil
		}
	}
	return SigninResult{}, &LookupError{Kind: KindNotFound, Message: "no sign-in link found"}
}
