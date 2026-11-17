package signals

import (
	"context"
	"errors"

	"applysmith/internal/discovery"
)

// DiscoveryLeads writes the handoff's leads into the leads store of job
// discovery, which de-duplicates them: a posting lead by its link, a review
// lead by company and evidence URL. A lead stored already is not an error.
type DiscoveryLeads struct{ Repo *discovery.Repo }

// AddLead implements LeadSink.
func (d DiscoveryLeads) AddLead(ctx context.Context, l Lead) error {
	dl := discovery.Lead{Source: l.Source, Category: l.Category, Company: l.Company, PosterType: "company", Confidence: 1,
		Description: l.Description, InboxID: l.InboxID,
		Evidence: discovery.Evidence{URL: l.Evidence.URL, Title: l.Evidence.Title, Text: l.Evidence.Body, Adapter: l.Evidence.Adapter,
			At: l.Evidence.Date, AbsentReason: l.Evidence.AbsentReason}}
	if l.Role != "" {
		dl.Roles = []string{l.Role}
	}
	if l.Location != "" {
		dl.Locations = []string{l.Location}
	}
	if l.ApplyLink != "" {
		dl.ApplyLinks = []string{l.ApplyLink}
	}
	if _, err := d.Repo.LeadAdd(ctx, dl); err != nil && !errors.Is(err, discovery.ErrLeadDuplicate) {
		return err
	}
	return nil
}
