package classify

import (
	"applysmith/internal/lazyregexp"
)

// Recruiting identity vocabularies. They differ on purpose: the display-name
// list can turn a display name into an employer name, so it stays tight; the
// signature list may be broader because a signature is self-description; the
// broad list only corroborates an interview claim and can never rename a
// company.
var (
	senderNameRE  = lazyregexp.New(`(?i)\b(?:recruit(?:er|ers|ing|ment)|talent\s+(?:acquisition|team|partner|coordinator)|candidate\s+experience|campus\s+recruit\w*|university\s+recruit\w*|sourcer|careers)\b`)
	signatureRE   = lazyregexp.New(`(?i)\b(?:recruit(?:er|ers|ing|ment)|talent\s+(?:acquisition|team|partner|coordinator)|hiring\s+(?:team|manager|partner)|candidate\s+experience|people\s+(?:team|operations)|human\s+resources|campus\s+recruit\w*|university\s+recruit\w*|sourcer|careers)\b`)
	broadIdentity = lazyregexp.New(`(?i)\b(?:recruit(?:er|ers|ing|ment)|talent|hiring|candidate\s+experience|people\s+team|human\s+resources|hr|sourcer|careers?)\b`)
	claimNameRE   = lazyregexp.New(`(?i)\b(recruiter|recruiting|talent|hiring|careers|candidate experience|people team|hr|human resources)\b`)
)

var signatureMarker = lazyregexp.New(`\n\s*--\s*\n`)

const signatureTail = 300

// signatureBlock is the text after the last "--" marker, else the message tail.
func signatureBlock(body string) string {
	if body == "" {
		return ""
	}
	locs := signatureMarker.FindAllStringIndex(body, -1)
	if len(locs) > 0 {
		return body[locs[len(locs)-1][1]:]
	}
	r := []rune(body)
	if len(r) > signatureTail {
		return string(r[len(r)-signatureTail:])
	}
	return body
}

// Policy is what the taxonomy and the sender say about one From address.
type Policy struct {
	Allowed    map[string]bool // nil = every type
	Restricted bool
	// Trusted says the sender passes the trust gates of the claim families: a
	// trusted ATS domain, or a recruiting identity in the name, the address or
	// the signature. A name or a signature is text anyone can write, so it
	// corroborates a sender and never vouches for one; Anchored is what does.
	Trusted            bool
	TrustedBySignature bool
	// Anchored is set for senders the taxonomy itself vouches for: a trusted
	// ATS domain or a vendor ATS domain. An employer-domain match is added by
	// the classifier once the employer is known.
	Anchored          bool
	VendorATS         bool
	SchedulingTool    bool
	Webmail           bool
	Domain, LocalPart string
}

func (p Policy) allows(t string) bool { return p.Allowed == nil || p.Allowed[t] }

// senderPolicy classifies a sender. body is the sender's own message when the
// signature may confer trust, and empty for a recipient.
func (t *Taxonomy) senderPolicy(from Sender, body string) Policy {
	domain, local := emailDomain(from.Email), emailLocal(from.Email)
	p := Policy{
		Domain: domain, LocalPart: local,
		VendorATS:      domainIn(domain, t.VendorATS),
		Anchored:       domainIn(domain, t.VendorATS),
		SchedulingTool: domainIn(domain, t.SchedulingTools),
		Webmail:        domainIn(domain, t.Webmail),
	}
	if allowed, ok := t.restrictedFor(domain); ok {
		p.Allowed, p.Restricted = allowed, true
		return p
	}
	trustedDomain := domainIn(domain, t.TrustedATS)
	trustedName := senderNameRE.MatchString(from.Name + " " + from.Email)
	p.TrustedBySignature = !trustedDomain && !trustedName && !p.Webmail && signatureRE.MatchString(signatureBlock(body))
	p.Trusted = trustedDomain || trustedName || p.TrustedBySignature
	p.Anchored = p.Anchored || trustedDomain
	return p
}
