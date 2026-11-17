package extract

import (
	"net/mail"
	"net/url"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Sender domains of the two systems whose mail may carry an activation or
// reset link.
const (
	WorkdaySenderDomain        = "otp.workday.com"
	SuccessFactorsSenderDomain = "successfactors.com"
)

// LinkInput is a message as the link extractors read it: raw parts with markup
// intact, the From header and the one tenant host the caller works with. Text
// is read only for an address an activation mail prints.
type LinkInput struct {
	HTML, Text string
	From       string
	Host       string
}

// LinkMatch is a qualifying link and why it qualified. The URL is a bearer
// secret: callers return it in a response body only.
type LinkMatch struct {
	URL    string
	Host   string
	Reason string
}

var (
	activateRE     = lazyregexp.New(`(?i)/activate/([^/?#]+)`)
	resetRE        = lazyregexp.New(`(?i)/(?:passwordreset|resetpassword|reset-password|password-reset)/([^/?#]+)`)
	hashAlphabetRE = lazyregexp.New(`^[A-Za-z0-9+/=_-]+$`)
)

// tokenOf finds the secret in a URL of one system's link shape, or "".
type tokenOf func(u *url.URL) string

func hasLetterAndDigit(s string) bool {
	var letter, digit bool
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			letter = true
		}
	}
	return letter && digit
}

// opaqueToken: at least 8 characters including a letter and a digit.
func opaqueToken(t string) bool { return len(t) >= 8 && hasLetterAndDigit(t) }

// opaqueHash: at least 20 characters from the base64 or URL-safe alphabet.
func opaqueHash(h string) bool { return len(h) >= 20 && hashAlphabetRE.MatchString(h) }

func pathToken(re *lazyregexp.Regexp) tokenOf {
	return func(u *url.URL) string {
		if m := re.FindStringSubmatch(u.EscapedPath()); m != nil && opaqueToken(m[1]) {
			return m[1]
		}
		return ""
	}
}

func sfActivationToken(u *url.URL) string {
	if u.Path != "/career" || u.Query().Get("navBarLevel") != "EMAIL_VERIFICATION" {
		return ""
	}
	if h := u.Query().Get("hash"); opaqueHash(h) {
		return h
	}
	return ""
}

// senderDomainOf returns the lowercase domain of the one address in a From
// header, or "" when the header is not a single well-formed address. A display
// name is never read, and a malformed header fails closed.
func senderDomainOf(from string) string {
	a, err := mail.ParseAddress(strings.TrimSpace(from))
	if err != nil {
		return ""
	}
	_, d, ok := strings.Cut(strings.ToLower(a.Address), "@")
	if !ok || d == "" || strings.HasSuffix(d, ".") {
		return ""
	}
	return d
}

// SenderDomain exposes the From-domain parse for callers that filter on it.
func SenderDomain(from string) string { return senderDomainOf(from) }

// DomainOrSub reports whether domain equals parent or is a subdomain of it.
func DomainOrSub(domain, parent string) bool {
	return domain != "" && (domain == parent || strings.HasSuffix(domain, "."+parent))
}

// matchTenantLink applies the four locks: the mail's From domain equals the
// system's own sender domain or is a subdomain of it on a dot boundary; the URL is https; its host equals the caller's host
// exactly; and the URL has the system's link shape with an opaque token.
// The href attribute of an anchor in the HTML part is read first. An address
// the mail prints as visible text is read only when printed is set (an
// activation mail: Workday's own tenant prints its link; owner decision), and
// then under the same locks, so no phrasing can talk a mail in. Any other
// attribute, a comment and a script are never read.
func matchTenantLink(in LinkInput, senderDomain string, token tokenOf, label string, printed bool) *LinkMatch {
	want := strings.ToLower(strings.TrimSpace(in.Host))
	if want == "" || !DomainOrSub(senderDomainOf(in.From), senderDomain) {
		return nil
	}
	candidates := AnchorHrefs(in.HTML)
	if printed {
		candidates = append(candidates, PrintedURLs(in.HTML, in.Text)...)
	}
	for _, href := range candidates {
		u, err := url.Parse(href)
		if err != nil || u.Scheme != "https" || strings.ToLower(u.Hostname()) != want || token(u) == "" {
			continue
		}
		return &LinkMatch{URL: u.String(), Host: want, Reason: "from:" + senderDomain + " host:" + want + " path:" + label}
	}
	return nil
}

// shapes pairs each system's sender with the only link shape it may open.
var activationShapes = []struct {
	sender string
	token  tokenOf
	label  string
}{
	{WorkdaySenderDomain, pathToken(activateRE), "/activate/"},
	{SuccessFactorsSenderDomain, sfActivationToken, "/career?navBarLevel=EMAIL_VERIFICATION&hash="},
}

// ActivationLink finds an account-activation link, an anchor's or one the mail
// prints. A Workday sender qualifies only the Workday shape and a
// SuccessFactors sender only its own.
func ActivationLink(in LinkInput) *LinkMatch {
	for _, s := range activationShapes {
		if m := matchTenantLink(in, s.sender, s.token, s.label, true); m != nil {
			return m
		}
	}
	return nil
}

// ResetLink finds a password-reset link; Workday's sender only.
func ResetLink(in LinkInput) *LinkMatch {
	return matchTenantLink(in, WorkdaySenderDomain, pathToken(resetRE), "/passwordreset/", false)
}
