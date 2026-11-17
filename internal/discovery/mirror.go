package discovery

import "context"

// FlagMirrorUnresolved marks a row whose URL is a mirror of an employer
// posting the resolver could not find.
const FlagMirrorUnresolved = "mirror-unresolved"

// MirrorResult is the outcome of resolving one mirror URL.
type MirrorResult struct {
	// URL is the employer's own posting, empty when none was found.
	URL string
	// Reason says why nothing was found.
	Reason string
}

// MirrorResolver is the apply chain's resolver of a mirror row to the
// employer's own posting. Discovery and the apply chain call the same
// implementation, so a scan and a later apply run reach the same answer.
type MirrorResolver interface {
	// IsMirror reports whether the URL is a job-board mirror.
	IsMirror(rawURL string) bool
	// Resolve finds the employer posting for a mirror by company and role.
	// It reads pages over HTTP only and never signs in or submits.
	Resolve(ctx context.Context, mirrorURL, company, role string) MirrorResult
	// AggregatorHosts lists the mirror hosts a web search leaves out.
	AggregatorHosts() []string
}

// resolveMirror replaces a mirror row's URL with the employer's posting and
// keeps the mirror as an alias for de-duplication. An unresolved mirror stays
// as it is, flagged with the resolver's reason.
func (r *run) resolveMirror(ctx context.Context, p *Posting) {
	m := r.sc.Mirror
	if m == nil || !m.IsMirror(p.URL) {
		return
	}
	res := m.Resolve(ctx, p.URL, p.Company, p.Title)
	if res.URL != "" {
		if canon, err := postingURL(res.URL); err == nil && !NonPostingURL(canon) && !m.IsMirror(canon) {
			p.Aliases = append(p.Aliases, p.URL)
			p.URL = canon
			return
		}
		res.Reason = "the resolver returned an unusable URL"
	}
	p.addFlag(FlagMirrorUnresolved)
	p.mirrorReason = res.Reason
}
