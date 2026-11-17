package api

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/discovery"
	"applysmith/internal/notify"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
)

func WireLead(l discovery.Lead) wire.Lead {
	return wire.Lead{
		ID: apikit.Safe64(l.ID), Source: l.Source, Category: l.Category, State: l.State, Reason: l.Reason, Company: l.Company,
		Roles: apikit.NonNil(l.Roles), Locations: apikit.NonNil(l.Locations), WorkModel: l.WorkModel, Compensation: l.Compensation,
		ApplyLinks: apikit.NonNil(l.ApplyLinks), ContactEmails: apikit.NonNil(l.ContactEmails), ContactMethod: l.ContactMethod,
		Sponsorship: l.Sponsorship, Confidence: l.Confidence, PosterType: l.PosterType, Author: l.Author, Expanded: l.Expanded,
		EvidenceURL: l.Evidence.URL, EvidenceTitle: l.Evidence.Title, EvidenceText: l.Evidence.Text, EvidenceAdapter: l.Evidence.Adapter,
		EvidenceAt: apikit.TimePtr(l.Evidence.At), AbsentReason: l.Evidence.AbsentReason, InboxID: apikit.Safe64(l.InboxID), Evaluable: discovery.Evaluable(l),
		CreatedAt: wire.Time{Time: l.Created.UTC()}, UpdatedAt: wire.Time{Time: l.Updated.UTC()},
	}
}

// maxLeadSearchChars bounds a search as the Tracker's does (tracker.maxQueryChars):
// every word is one AND clause, and SQLite refuses an expression nested too deep.
const maxLeadSearchChars = 200

func (s *Server) listLeads(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	q := apikit.QueryOf(c)
	state, category, source, sort, text := q.Str("state"), q.Str("category"), q.Str("source"), q.Str("sort"), q.Str("q")
	page, size := q.Paging(25)
	if sort != "" && sort != "confidence" {
		q.Fail(apikit.InvalidField("sort", "sort must be confidence or empty for newest first"))
	}
	if state != "" && !slices.Contains([]string{discovery.LeadNew, discovery.LeadEvaluated, discovery.LeadDismissed}, state) {
		q.Fail(apikit.InvalidField("state", "state must be one of new, evaluated, dismissed"))
	}
	if category != "" && !slices.Contains(discovery.LeadCategories, category) {
		q.Fail(apikit.InvalidField("category", "category must be one of %s", strings.Join(discovery.LeadCategories, ", ")))
	}
	if len([]rune(text)) > maxLeadSearchChars {
		q.Fail(apikit.InvalidField("q", "q holds at most %d characters", maxLeadSearchChars))
	}
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	f := discovery.LeadFilter{State: state, Category: category, Source: source, Text: text, Limit: size, Offset: (page - 1) * size, ByConfidence: sort == "confidence"}
	rows, err := d.repo.LeadList(c.R.Context(), f)
	if err != nil {
		return apikit.Reply{}, err
	}
	total, err := d.repo.LeadCount(c.R.Context(), f)
	if err != nil {
		return apikit.Reply{}, err
	}
	counts, err := d.repo.LeadCounts(c.R.Context(), f)
	if err != nil {
		return apikit.Reply{}, err
	}
	st, err := s.db()
	if err != nil {
		return apikit.Reply{}, err
	}
	list, err := WireLeads(c.R.Context(), st, d.repo, d.tracker, rows)
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: wire.LeadPage{Rows: list, Page: apikit.Safe(page), PageSize: apikit.Safe(size), Total: apikit.Safe(total), Counts: apikit.SafeMap(counts),
		Evaluating: d.scans.busy(leadRunClaim("evaluate")), Expanding: d.scans.busy(leadRunClaim("expand"))}}, nil
}

// WireLeads is the listing form of leads: a lead promoted to the inbox also
// says what came of it, the row's status, its report and its tracker row.
func WireLeads(ctx context.Context, st *store.Store, repo *discovery.Repo, tr *tracker.Tracker, rows []discovery.Lead) ([]wire.Lead, error) {
	out := make([]wire.Lead, len(rows))
	for i, l := range rows {
		out[i] = WireLead(l)
		if l.InboxID == 0 {
			continue
		}
		x, err := repo.InboxGet(ctx, l.InboxID)
		if errors.Is(err, discovery.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		in, err := WireInboxRows(ctx, st, []discovery.InboxRow{x})
		if err != nil {
			return nil, err
		}
		out[i].InboxStatus, out[i].ReportNumber = x.Status, in[0].ReportNumber
		found, err := tr.Lookup(ctx, x.URL)
		if err != nil && !errors.Is(err, tracker.ErrInvalid) {
			return nil, err
		}
		if found.Found {
			out[i].TrackerID = apikit.Safe64(found.Entry.ID)
		}
	}
	return out, nil
}

func (s *Server) dismissLead(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	return s.leadState(c, d, id, d.repo.LeadDismiss(c.R.Context(), id, "dismissed by the user"))
}

// restoreLead puts a dismissed lead back to new.
func (s *Server) restoreLead(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	return s.leadState(c, d, id, d.repo.LeadRestore(c.R.Context(), id))
}

// leadState answers a lead state change with the lead as it now is.
func (s *Server) leadState(c *apikit.Call, d *domain, id int64, err error) (apikit.Reply, error) {
	switch {
	case errors.Is(err, discovery.ErrNotFound):
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no such lead")
	case errors.Is(err, discovery.ErrBadTransition):
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "%s", strings.TrimPrefix(err.Error(), discovery.ErrBadTransition.Error()+": "))
	case err != nil:
		return apikit.Reply{}, err
	}
	l, err := d.repo.LeadGet(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, err
	}
	s.notify("scans")
	return apikit.Reply{Data: WireLead(l)}, nil
}

// leadsExpand is the body of the scheduled leads.expand job.
func (s *Server) leadsExpand(d *domain) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		portals, e := loadPortals(s.cfg.Workspace)
		if e != nil {
			return "", e
		}
		profile, e := loadProfile(s.cfg.Workspace)
		if e != nil {
			return "", e
		}
		res, err := s.newScanner(d, profile, nil).ExpandLeads(ctx, discovery.ExpandRequest{Portals: portals, Profile: profile})
		return res.Summary(), err
	}
}

// leadsEvaluate is the body of the scheduled leads.evaluate job.
func (s *Server) leadsEvaluate(d *domain) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		portals, e := loadPortals(s.cfg.Workspace)
		if e != nil {
			return "", e
		}
		profile, e := loadProfile(s.cfg.Workspace)
		if e != nil {
			return "", e
		}
		res, err := s.newScanner(d, profile, nil).EvaluateLeads(ctx, discovery.EvaluateRequest{Portals: portals, Profile: profile})
		return res.Summary(), err
	}
}

// afterScheduledScan marks a finished scheduled run and raises the alarm of a
// source that stopped producing: one needs-you item per source per day.
func (s *Server) afterScheduledScan(ctx context.Context, d *domain, rec discovery.Record) {
	ctx = context.WithoutCancel(ctx)
	if err := d.repo.MarkScheduled(ctx, rec.ID); err != nil {
		s.log.Warn("a scheduled scan could not be marked", "run", rec.ID, "error", err.Error())
		return
	}
	alarm, err := d.repo.SourceAlarmFor(ctx, rec)
	if err != nil || alarm == nil || d.notify == nil {
		if err != nil {
			s.log.Warn("the silent-source check failed", "source", rec.Source, "error", err.Error())
		}
		return
	}
	reason, detail := "other", fmt.Sprintf("%s. Last productive: %s.", alarm.Status, alarm.LastProductive.UTC().Format(time.RFC3339))
	if alarm.Action != "" {
		reason, detail = "sign-in", alarm.Action+" "+detail
	}
	id := "source-alarm-" + rec.Source + "-" + s.now().UTC().Format("2006-01-02")
	_, err = d.notify.board.Raise(ctx, notify.RaiseArgs{ID: id, Kind: notify.KindTask, Origin: notify.OriginSchedule,
		Title: "Scan " + sourceTitle(rec.Source) + " stopped producing", State: notify.NeedsYou, Reason: reason, Detail: detail, Link: "/discovery"})
	if err != nil {
		s.log.Warn("the source alarm could not be raised", "source", rec.Source, "error", err.Error())
	}
}
