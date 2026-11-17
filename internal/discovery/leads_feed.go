package discovery

import (
	"context"
	"errors"
	"strings"
)

// feedConfidenceFloor is the confidence a feed lead needs to reach the inbox.
const feedConfidenceFloor = 0.5

// FeedLeadInput is one classified LinkedIn post.
type FeedLeadInput struct {
	Permalink, Author, Body, PosterType string // PosterType person|company
	Company                             string
	Roles, Locations                    []string
	WorkModel, Compensation             string
	// ApplyLinks are the links of the post with shorteners already expanded;
	// they are ranked here with the shared link classifier.
	ApplyLinks    []string
	ContactEmails []string
	// ContactMethod is link, email, dm, comment or tag; Sponsorship is yes, no
	// or unknown.
	ContactMethod, Sponsorship string
	Confidence                 float64
}

// feedCategory is the review category of a hiring post: how the user can act
// on it.
func feedCategory(in FeedLeadInput, best string, emails []string) string {
	switch {
	case in.Confidence < feedConfidenceFloor:
		return LeadLowConfidence
	case best != "":
		switch kind := LinkKind(best); {
		case kind == LinkCareersIndex:
			return LeadCareersPage
		case SinglePostingLink(kind):
			return LeadPosting
		}
		return LeadGenericPage
	}
	switch in.ContactMethod {
	case "email":
		return LeadEmail
	case "comment", "tag":
		return LeadComment
	case "dm":
		return LeadDM
	}
	if len(emails) > 0 {
		return LeadEmail
	}
	return LeadDM
}

// addFeedLead stores or de-duplicates one hiring post and, when it names a
// single posting with enough confidence, promotes the posting to the inbox
// through the shared pipeline. Every post adds one to Discovered and ends in
// exactly one terminal count: a promoted post is counted by the inbox
// pipeline itself, a stored lead under filtered `lead:<category>`, a repeat
// under duplicates `leads` or `repost`, a post the gate refuses under
// filtered `lead-gate`.
func (r *run) addFeedLead(ctx context.Context, in FeedLeadInput) (leadID int64, promoted bool, err error) {
	permalink := permalinkKey(in.Permalink)
	hash := leadBodyHash(in.Author, in.Body)
	key := "h:" + hash
	if permalink != "" {
		key = "p:" + permalink
	}
	if id, seen, err := r.repo.leadSeen(ctx, key, permalink, hash); err != nil || seen {
		return id, false, r.countLead(err, "", "leads", 0)
	}
	if id, repost, err := r.repo.leadRepost(ctx, in.Company, in.Roles); err != nil || repost {
		if repost {
			r.addDetail("duplicate:repost", 1)
		}
		return id, false, r.countLead(err, "", "repost", 0)
	}

	ranked := RankLinks(in.ApplyLinks)
	best := ""
	if len(ranked) > 0 {
		best = ranked[0]
	}
	emails := mergeStrings(in.ContactEmails, ExtractEmails(in.Body))
	checker := LeadChecker{Filters: r.filters, Scope: r.scope, Gate: r.sc.Gate}
	base := Posting{Source: r.req.Source, Company: in.Company, URL: best, Description: truncateRunes(in.Body, maxDescription)}
	roles, rejected := checker.Narrow(base, in.Roles, in.Locations)
	for _, v := range rejected {
		p := base
		p.Title = v.Role
		if len(in.Locations) > 0 {
			p.Location = in.Locations[0]
		}
		if err := r.repo.LeadGateAudit(ctx, IngressFeed, p, v.Rules); err != nil {
			return 0, false, err
		}
	}
	if len(roles) == 0 && len(rejected) > 0 {
		return 0, false, r.countLead(nil, FilterLeadGate, "", 1)
	}

	category := feedCategory(in, best, emails)
	lead := Lead{
		Source: r.req.Source, DedupKey: key, BodyHash: hash, PosterType: in.PosterType, Author: in.Author, Company: in.Company,
		Roles: roles, Locations: in.Locations, WorkModel: in.WorkModel, Compensation: in.Compensation, ApplyLinks: ranked,
		ContactEmails: emails, ContactMethod: in.ContactMethod, Sponsorship: in.Sponsorship, Confidence: in.Confidence,
		Category: category,
		Evidence: Evidence{URL: in.Permalink, Text: in.Body, Adapter: r.req.Source, At: r.repo.now().UTC()},
	}
	if strings.TrimSpace(in.Body) == "" {
		lead.Evidence.AbsentReason = "the post carried no text"
	}
	if category == LeadEmail || category == LeadDM || category == LeadComment {
		lead.Description = in.Body
	}
	leadID, err = r.repo.LeadAdd(ctx, lead)
	if errors.Is(err, ErrLeadDuplicate) {
		return leadID, false, r.countLead(nil, "", "leads", 0)
	}
	if err != nil {
		return 0, false, err
	}
	if category != LeadPosting {
		return leadID, false, r.countLead(nil, "lead:"+category, "", 1)
	}

	p := base
	p.Title = firstOf(roles, "")
	if len(in.Locations) > 0 {
		p.Location = in.Locations[0]
	}
	p.Compensation = in.Compensation
	inboxID, err := r.promoteLead(ctx, p)
	if err != nil {
		return leadID, false, err
	}
	if inboxID != 0 {
		return leadID, true, r.repo.LeadUpdate(ctx, leadID, LeadPatch{InboxID: &inboxID})
	}
	return leadID, false, nil
}

// FilterLeadGate counts posts the eligibility rules refused on a lead path.
const FilterLeadGate = "lead-gate"

// countLead adds one discovered post to its terminal count. dup names the
// duplicate store; filtered the reason (ignored when n is zero).
func (r *run) countLead(err error, filtered, dup string, n int) error {
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rec.Counts.Discovered++
	switch {
	case dup != "":
		r.rec.Counts.Duplicates[dup]++
	case filtered != "":
		r.rec.Counts.Filtered[filtered] += n
	}
	return nil
}

// promoteLead sends a posting through the shared pipeline and returns the
// inbox row it created, zero when the pipeline filtered it or already knew it.
func (r *run) promoteLead(ctx context.Context, p Posting) (int64, error) {
	r.mu.Lock()
	before := len(r.inserted)
	r.mu.Unlock()
	if err := r.emit(ctx, p.Source, []Posting{p}); err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.inserted) > before {
		return r.inserted[len(r.inserted)-1].id, nil
	}
	return 0, nil
}

// mergeStrings joins lists without repeats, keeping the first spelling.
func mergeStrings(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lists {
		for _, s := range l {
			if k := strings.ToLower(strings.TrimSpace(s)); k != "" && !seen[k] {
				seen[k] = true
				out = append(out, s)
			}
		}
	}
	return out
}
