// Package trackerapi serves the tracker routes of the local API: the
// candidacy rows and their status, notes, follow-ups, archive and duplicates,
// the summary, outcomes and export, and the lookup of a posting URL.
// internal/api registers the handlers.
package trackerapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/gmail"
	"applysmith/internal/gmail/lifecycle"
	"applysmith/internal/ledger"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
	"applysmith/internal/workspace"
)

// Deps is what the tracker handlers use from the server. The log exists only
// once the server has started, so it is read through a function.
type Deps struct {
	Tracker func() (*tracker.Tracker, error)
	Gmail   func() (*gmail.Stack, error) // the list's mail state and filter
	// Ledger is the submissions ledger the lookup reads a prior submission
	// from, nil before the store is ready.
	Ledger    func() *ledger.Ledger
	IsRemote  func(*http.Request) bool // the request came through the tunnel
	Notify    func(topics ...string)
	Now       func() time.Time
	Log       func() *slog.Logger
	Workspace *workspace.Workspace // policy.yml's follow-up cadence
}

// Handlers serves the tracker routes.
type Handlers struct{ Deps }

func New(d Deps) *Handlers { return &Handlers{Deps: d} }

// Actor is the actor of a change the caller makes through the API. It
// comes from the credential that authenticated the request and never from the
// body: a dashboard session or the local token is the user at the keyboard or
// the user's own tool. Other actors write to the tracker from inside the
// server, not through these routes.
func Actor(p apikit.Principal) (tracker.Actor, *apikit.Error) {
	switch p.Kind {
	case apikit.PrincipalDashboard, apikit.PrincipalLocalToken:
		return tracker.ActorUser, nil
	}
	return "", apikit.NewErr(wire.CodeForbidden, "this credential may not change the tracker")
}

func WireCandidacy(e tracker.Entry) wire.Candidacy {
	out := wire.Candidacy{
		ID: apikit.Safe64(e.ID), Date: e.Date, Company: e.Company, Role: e.Role, Score: e.Score, ScoreKind: string(e.ScoreKind),
		Status: string(e.Status), AppliedBy: string(e.AppliedBy), AppliedAt: e.AppliedAt, ResumePDF: e.ResumePDF, CoverLetter: e.CoverLetter,
		Notes: e.Notes, PostingURL: e.PostingURL, Location: e.Location, Source: e.Source, IdentityKey: e.IdentityKey,
		ArchivedAt: e.ArchivedAt, Version: apikit.Safe(e.Version), CreatedAt: apikit.Stamp(e.CreatedAt), UpdatedAt: apikit.Stamp(e.UpdatedAt),
	}
	if e.ReportNumber != nil {
		n := apikit.Safe64(*e.ReportNumber)
		out.ReportNumber = &n
	}
	return out
}

func WireStatusResult(r tracker.StatusResult) wire.StatusChangeResult {
	return wire.StatusChangeResult{Entry: WireCandidacy(r.Entry), Changed: r.Changed, Correction: r.Correction, Inferred: r.Inferred}
}

// TrackerError maps a tracker failure onto the API errors.
func TrackerError(err error) error {
	var dup *tracker.DuplicateError
	var refused *tracker.RefusedError
	switch {
	case errors.As(err, &dup):
		e := apikit.NewErr(wire.CodeConflict, "this job is already row %d of the tracker (%s)", dup.ID, dup.Matched())
		e.Details = &wire.ErrorDetails{Reason: wire.ReasonDuplicateCandidacy, MatchID: apikit.Safe64(dup.ID)}
		return e
	case errors.As(err, &refused):
		return apikit.NewErr(wire.CodeConflict, "%s", refused.Reason)
	case errors.Is(err, tracker.ErrForbidden):
		return apikit.NewErr(wire.CodeForbidden, "%s", strings.TrimPrefix(err.Error(), tracker.ErrForbidden.Error()+": "))
	case errors.Is(err, tracker.ErrInvalid):
		return apikit.NewErr(wire.CodeValidationFailed, "%s", strings.TrimPrefix(err.Error(), tracker.ErrInvalid.Error()+": "))
	case errors.Is(err, store.ErrConflict):
		return apikit.NewErr(wire.CodeRevisionConflict, "the row changed since it was loaded; reload and try again")
	}
	return err
}

func (s *Handlers) ListCandidacies(c *apikit.Call) (apikit.Reply, error) {
	tr, err := s.Tracker()
	if err != nil {
		return apikit.Reply{}, err
	}
	q := apikit.QueryOf(c)
	page, size := q.Paging(25)
	lq, err := candidacyFilter(q)
	lq.Sort, lq.Page, lq.PageSize = q.Str("sort"), page, size
	if err != nil {
		return apikit.Reply{}, TrackerError(err)
	}
	mail, err := s.narrowByMail(c, q, &lq)
	if err != nil {
		return apikit.Reply{}, err
	}
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	res, err := tr.List(c.R.Context(), lq)
	if err != nil {
		return apikit.Reply{}, TrackerError(err)
	}
	out := WireCandidacyPage(res)
	for i, r := range out.Entries {
		if m, ok := mail[int64(r.ID)]; ok {
			v := m.Thread.View
			out.Entries[i].Mail = &wire.CandidacyMail{ThreadID: m.Thread.ID, Lane: v.Lane, Owner: v.Owner, NextAction: v.NextAction,
				AgeDays: wire.SafeInt(v.StalenessDays), Open: wire.SafeInt(m.Open)}
		}
	}
	return apikit.Reply{Data: out}, nil
}

// mailOwners are the values of the list's mail filter: whose move the row's
// most pressing open thread is (the lifecycle's own owner, so "needs_me" is
// the do-now and prepare lanes). The mailbox is read before paging, so the
// total and the pages count the narrowed rows.
var mailOwners = map[string]string{"needs_me": lifecycle.OwnerUser, "waiting": lifecycle.OwnerCompany}

// narrowByMail reads the mailbox once and returns each row's mail state. A
// mail filter narrows lq to the rows whose state matches. A remote client is
// refused the filter, since every /api/gmail route is denied to it, and gets no
// mail state; a failed read without a filter is logged and the list does not
// wait on Gmail.
func (s *Handlers) narrowByMail(c *apikit.Call, q *apikit.Query, lq *tracker.ListQuery) (map[int64]gmail.RowMail, error) {
	want := q.Str("mail")
	owner, known := mailOwners[want]
	switch {
	case want != "" && !known:
		return nil, apikit.InvalidField("mail", "mail must be needs_me or waiting")
	case s.IsRemote(c.R):
		if want != "" {
			return nil, apikit.InvalidField("mail", "the mail filter is not available on a remote connection")
		}
		return nil, nil
	}
	lq.ByID = want != ""
	stack, err := s.Gmail()
	if err != nil || stack == nil {
		return nil, err
	}
	mail, err := stack.Repo.MailByCandidacy(c.R.Context(), s.Now())
	if err != nil {
		if want != "" {
			return nil, err
		}
		s.Log().Warn("the tracker list could not read the linked mail", "error", err.Error())
		return nil, nil
	}
	for id, m := range mail {
		if want != "" && m.Thread.View.Owner == owner {
			lq.IDs = append(lq.IDs, id)
		}
	}
	return mail, nil
}

// candidacyFilter reads the filters the list and its export share: status (one
// or a comma-separated list), company, source, query, minScore, appliedWithin
// (days), reached and movedBy with since (a status and an actor, rows moved to
// it or by it on or after that local day) and archived.
func candidacyFilter(q *apikit.Query) (tracker.ListQuery, error) {
	statuses, err := tracker.ParseStatuses(q.Str("status"))
	return tracker.ListQuery{
		Statuses: statuses, Company: q.Str("company"), Source: q.Str("source"), Query: q.Str("query"), MinScore: q.Float("minScore"),
		AppliedWithinDays: q.Integer("appliedWithin", 0), Archived: q.Boolean("archived"),
		Reached: tracker.Status(q.Str("reached")), MovedBy: tracker.Actor(q.Str("movedBy")), ReachedSince: q.Str("since"),
	}, err
}

func WireCandidacyPage(p tracker.Page) wire.CandidacyPage {
	out := wire.CandidacyPage{Entries: make([]wire.Candidacy, len(p.Entries)), Page: apikit.Safe(p.Page), PageSize: apikit.Safe(p.PageSize), Total: apikit.Safe(p.Total), Archived: apikit.Safe(p.Archived)}
	for i, e := range p.Entries {
		out.Entries[i] = WireCandidacy(e)
	}
	return out
}

func (s *Handlers) GetCandidacy(c *apikit.Call) (apikit.Reply, error) {
	tr, err := s.Tracker()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	d, err := tr.Get(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, TrackerError(err)
	}
	return apikit.Reply{Data: WireCandidacyDetail(d)}, nil
}

func WireCandidacyDetail(d tracker.Detail) wire.CandidacyDetail {
	out := wire.CandidacyDetail{Entry: WireCandidacy(d.Entry), Events: make([]wire.CandidacyEvent, len(d.Events)), Aliases: d.Aliases}
	for i, ev := range d.Events {
		out.Events[i] = wire.CandidacyEvent{ID: apikit.Safe64(ev.ID), At: apikit.Stamp(ev.At), Actor: string(ev.Actor), Kind: string(ev.Kind),
			PreviousStatus: string(ev.PreviousStatus), NewStatus: string(ev.NewStatus), Detail: ev.Detail}
	}
	return out
}

func (s *Handlers) SetCandidacyStatus(c *apikit.Call) (apikit.Reply, error) {
	actor, e := Actor(c.P)
	if e != nil {
		return apikit.Reply{}, e
	}
	tr, err := s.Tracker()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	var req wire.StatusChangeRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if e := requireVersion(req.ExpectedVersion); e != nil {
		return apikit.Reply{}, e
	}
	to, err := tracker.ParseStatus(req.Status)
	if err != nil {
		return apikit.Reply{}, apikit.InvalidField("status", "%s", strings.TrimPrefix(err.Error(), tracker.ErrInvalid.Error()+": "))
	}
	res, err := tr.SetStatus(c.R.Context(), tracker.StatusChange{ID: id, To: to, Actor: actor, Note: req.Note, On: req.On, ExpectedVersion: int(req.ExpectedVersion)})
	if err != nil {
		return apikit.Reply{}, TrackerError(err)
	}
	if res.Changed {
		s.Notify("tracker")
	}
	return apikit.Reply{Data: WireStatusResult(res)}, nil
}

// requireVersion refuses an edit that does not say which version of the row it
// read: without it a stale page would overwrite a newer change.
func requireVersion(v wire.SafeInt) *apikit.Error {
	if v < 1 {
		return apikit.InvalidField("expectedVersion", "expectedVersion must be the row version you last read, at least 1")
	}
	return nil
}

// OverrideDocumentFloor is the dashboard's control for the 4.5 document floor:
// the user's override of the report the row links to, as `applysmith report
// override` does.
func (s *Handlers) OverrideDocumentFloor(c *apikit.Call) (apikit.Reply, error) {
	actor, e := Actor(c.P)
	if e != nil {
		return apikit.Reply{}, e
	}
	tr, err := s.Tracker()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	d, err := tr.Get(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, TrackerError(err)
	}
	if d.Entry.ReportNumber == nil {
		return apikit.Reply{}, apikit.NewErr(wire.CodeValidationFailed, "the row has no report to override; evaluate the posting first")
	}
	entry, err := tr.OverrideDocuments(c.R.Context(), *d.Entry.ReportNumber, actor)
	if err != nil {
		return apikit.Reply{}, TrackerError(err)
	}
	s.Notify("tracker")
	return apikit.Reply{Data: WireCandidacy(entry)}, nil
}

func (s *Handlers) AddCandidacyNote(c *apikit.Call) (apikit.Reply, error) {
	actor, e := Actor(c.P)
	if e != nil {
		return apikit.Reply{}, e
	}
	tr, err := s.Tracker()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	var req wire.NoteRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if e := requireVersion(req.ExpectedVersion); e != nil {
		return apikit.Reply{}, e
	}
	var entry tracker.Entry
	if req.Undo {
		entry, err = tr.UndoNote(c.R.Context(), id, actor, int(req.ExpectedVersion))
	} else {
		entry, err = tr.AddNote(c.R.Context(), id, actor, req.Text, int(req.ExpectedVersion))
	}
	if err != nil {
		return apikit.Reply{}, TrackerError(err)
	}
	s.Notify("tracker")
	return apikit.Reply{Status: http.StatusCreated, Data: WireCandidacy(entry)}, nil
}

func (s *Handlers) CreateCandidacy(c *apikit.Call) (apikit.Reply, error) {
	if _, e := Actor(c.P); e != nil {
		return apikit.Reply{}, e
	}
	tr, err := s.Tracker()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.CandidacyCreate
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if req.URL != "" {
		if e := apikit.CheckPostingURL(req.URL); e != nil {
			return apikit.Reply{}, e
		}
	}
	entry, err := tr.Add(c.R.Context(), tracker.AddRow{
		Company: req.Company, Role: req.Role, URL: req.URL, Location: req.Location,
		Status: tracker.Status(req.Status), AppliedAt: req.AppliedAt, Source: req.Source,
	})
	if err != nil {
		return apikit.Reply{}, TrackerError(err)
	}
	s.Notify("tracker")
	return apikit.Reply{Status: http.StatusCreated, Location: "/api/candidacies/" + strconv.FormatInt(entry.ID, 10), Data: WireCandidacy(entry)}, nil
}

// ArchiveCandidacy and UnarchiveCandidacy move one row out of or back into the
// default lists, as `applysmith tracker unarchive` does.
func (s *Handlers) ArchiveCandidacy(c *apikit.Call) (apikit.Reply, error) {
	return s.setArchived(c, (*tracker.Tracker).ArchiveRow)
}

func (s *Handlers) UnarchiveCandidacy(c *apikit.Call) (apikit.Reply, error) {
	return s.setArchived(c, (*tracker.Tracker).Unarchive)
}

func (s *Handlers) setArchived(c *apikit.Call, move func(*tracker.Tracker, context.Context, int64, tracker.Actor) (tracker.Entry, error)) (apikit.Reply, error) {
	actor, e := Actor(c.P)
	if e != nil {
		return apikit.Reply{}, e
	}
	tr, err := s.Tracker()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	entry, err := move(tr, c.R.Context(), id, actor)
	if err != nil {
		return apikit.Reply{}, TrackerError(err)
	}
	s.Notify("tracker")
	return apikit.Reply{Data: WireCandidacy(entry)}, nil
}

// MaxBulkRows is the bulk status limit of the tracker.
const MaxBulkRows = 100

func (s *Handlers) BulkCandidacyStatus(c *apikit.Call) (apikit.Reply, error) {
	if _, e := Actor(c.P); e != nil {
		return apikit.Reply{}, e
	}
	tr, err := s.Tracker()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.BulkStatusRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	ids, e := bulkIDs(req.IDs)
	if e != nil {
		return apikit.Reply{}, e
	}
	to, err := tracker.ParseStatus(req.Status)
	if err != nil {
		return apikit.Reply{}, apikit.InvalidField("status", "%s", strings.TrimPrefix(err.Error(), tracker.ErrInvalid.Error()+": "))
	}
	results, err := tr.BulkSetStatus(c.R.Context(), ids, to, req.Note)
	if err != nil {
		return apikit.Reply{}, TrackerError(err)
	}
	out := wire.BulkStatusResult{Results: make([]wire.StatusChangeResult, len(results))}
	for i, r := range results {
		out.Results[i] = WireStatusResult(r)
	}
	s.Notify("tracker")
	return apikit.Reply{Data: out}, nil
}

func bulkIDs(in []wire.SafeInt) ([]int64, *apikit.Error) {
	if len(in) < 1 || len(in) > MaxBulkRows {
		return nil, apikit.InvalidField("ids", "ids must name between 1 and %d rows", MaxBulkRows)
	}
	ids := make([]int64, len(in))
	for i, id := range in {
		if id < 1 {
			return nil, apikit.InvalidField("ids", "ids must be positive whole numbers")
		}
		ids[i] = int64(id)
	}
	return ids, nil
}

// BulkCandidacyArchive archives or unarchives the ticked rows; rows still in
// flight are left and named.
func (s *Handlers) BulkCandidacyArchive(c *apikit.Call) (apikit.Reply, error) {
	actor, e := Actor(c.P)
	if e != nil {
		return apikit.Reply{}, e
	}
	tr, err := s.Tracker()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.BulkArchiveRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	ids, e := bulkIDs(req.IDs)
	if e != nil {
		return apikit.Reply{}, e
	}
	changed, open, err := tr.BulkArchive(c.R.Context(), ids, actor, req.Archived)
	if err != nil {
		return apikit.Reply{}, TrackerError(err)
	}
	s.Notify("tracker")
	return apikit.Reply{Data: wire.BulkArchiveResult{Changed: safeIDs(changed), Open: safeIDs(open)}}, nil
}

func safeIDs(ids []int64) []wire.SafeInt {
	out := make([]wire.SafeInt, len(ids))
	for i, id := range ids {
		out[i] = apikit.Safe64(id)
	}
	return out
}

func (s *Handlers) Summary(c *apikit.Call) (apikit.Reply, error) {
	tr, err := s.Tracker()
	if err != nil {
		return apikit.Reply{}, err
	}
	q := apikit.QueryOf(c)
	archived := q.Boolean("archived")
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	counts, err := tr.SummaryOf(c.R.Context(), archived)
	if err != nil {
		return apikit.Reply{}, TrackerError(err)
	}
	out := wire.TrackerSummary{Counts: map[string]wire.SafeInt{}}
	for status, n := range counts {
		out.Counts[string(status)] = apikit.Safe(n)
		out.Total += apikit.Safe(n)
	}
	return apikit.Reply{Data: out}, nil
}

// Duplicates lists the duplicate groups without changing anything,
// leaving out those the user said are not duplicates.
func (s *Handlers) Duplicates(c *apikit.Call) (apikit.Reply, error) {
	tr, err := s.Tracker()
	if err != nil {
		return apikit.Reply{}, err
	}
	return s.duplicateGroups(c.R.Context(), tr)
}

func (s *Handlers) duplicateGroups(ctx context.Context, tr *tracker.Tracker) (apikit.Reply, error) {
	groups, err := tr.DedupPlan(ctx)
	if err != nil {
		return apikit.Reply{}, TrackerError(err)
	}
	return apikit.Reply{Data: WireDuplicates(groups)}, nil
}

// WireDuplicates is the document GET /api/tracker/duplicates answers for
// groups: those still to merge, without the ones the user dismissed or a
// merge already joined.
func WireDuplicates(groups []tracker.DedupGroup) wire.TrackerDuplicates {
	out := wire.TrackerDuplicates{Groups: []wire.DuplicateGroup{}}
	for _, g := range groups {
		if g.Dismissed || g.Merged {
			continue
		}
		w := wire.DuplicateGroup{Survivor: apikit.Safe64(g.Survivor), Status: string(g.Status), Rule: g.Rule, NeedsUser: g.NeedsUser, Reason: g.Reason}
		for _, e := range g.Rows {
			w.Rows = append(w.Rows, WireCandidacy(e))
		}
		out.Groups = append(out.Groups, w)
	}
	return out
}

func memberIDs(in []wire.SafeInt) []int64 {
	out := make([]int64, len(in))
	for i, id := range in {
		out[i] = int64(id)
	}
	return out
}

// MergeDuplicates merges one listed group and answers the row it kept with
// the token that undoes the merge, or with undo takes a merge back.
func (s *Handlers) MergeDuplicates(c *apikit.Call) (apikit.Reply, error) {
	actor, e := Actor(c.P)
	if e != nil {
		return apikit.Reply{}, e
	}
	tr, err := s.Tracker()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.DuplicateMergeRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	var kept tracker.Entry
	var undo string
	if req.Undo != "" {
		kept, err = tr.UndoMerge(c.R.Context(), actor, req.Undo)
		// The tracker's words say why: past the window, or the kept row changed.
		for code, sentinel := range map[wire.ErrorCode]error{wire.CodeNotFound: store.ErrNotFound, wire.CodeConflict: store.ErrConflict} {
			if errors.Is(err, sentinel) {
				return apikit.Reply{}, apikit.NewErr(code, "%s", strings.TrimPrefix(err.Error(), sentinel.Error()+": "))
			}
		}
	} else {
		kept, undo, err = tr.DedupMerge(c.R.Context(), actor, memberIDs(req.Members))
	}
	if err != nil {
		return apikit.Reply{}, TrackerError(err)
	}
	s.Notify("tracker")
	return apikit.Reply{Data: wire.DuplicateMergeResult{Kept: WireCandidacy(kept), Undo: undo}}, nil
}

// DismissDuplicates records that one listed group is not duplicates, or takes
// that back, and answers the groups listed then.
func (s *Handlers) DismissDuplicates(c *apikit.Call) (apikit.Reply, error) {
	actor, e := Actor(c.P)
	if e != nil {
		return apikit.Reply{}, e
	}
	tr, err := s.Tracker()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.DuplicateDismissal
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if err := tr.DismissDuplicates(c.R.Context(), actor, memberIDs(req.Members), req.Undo); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "these rows are no longer one duplicate group; the list is reloaded")
		}
		return apikit.Reply{}, TrackerError(err)
	}
	s.Notify("tracker")
	return s.duplicateGroups(c.R.Context(), tr)
}

// Followups lists the follow-ups due today under policy.yml's cadence.
func (s *Handlers) Followups(c *apikit.Call) (apikit.Reply, error) {
	tr, err := s.Tracker()
	if err != nil {
		return apikit.Reply{}, err
	}
	pol, _ := s.Workspace.FollowupPolicy()
	list, err := tr.Followups(c.R.Context(), pol, true)
	return apikit.Reply{Data: WireFollowups(list)}, err
}

// WireFollowups is the document GET /api/tracker/followups answers.
func WireFollowups(list []tracker.Suggestion) wire.FollowupList {
	out := wire.FollowupList{Suggestions: make([]wire.FollowupSuggestion, len(list))}
	for i, sg := range list {
		out.Suggestions[i] = wire.FollowupSuggestion{ID: apikit.Safe64(sg.ID), Company: sg.Company, Role: sg.Role, Version: apikit.Safe(sg.Version), Kind: string(sg.Kind), Number: apikit.Safe(sg.Number), Due: sg.Due}
	}
	return out
}

// RecordFollowup records a follow-up the user sent, snoozes the next one, or
// undoes the latest of those.
func (s *Handlers) RecordFollowup(c *apikit.Call) (apikit.Reply, error) {
	actor, e := Actor(c.P)
	if e != nil {
		return apikit.Reply{}, e
	}
	tr, err := s.Tracker()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	var req wire.FollowupRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if e := requireVersion(req.ExpectedVersion); e != nil {
		return apikit.Reply{}, e
	}
	pol, _ := s.Workspace.FollowupPolicy()
	var entry tracker.Entry
	switch v := int(req.ExpectedVersion); {
	case req.Undo:
		entry, err = tr.UndoFollowup(c.R.Context(), id, actor, v)
	case req.SnoozeDays != 0:
		entry, err = tr.SnoozeFollowup(c.R.Context(), id, actor, int(req.SnoozeDays), v, pol)
	default:
		entry, err = tr.RecordFollowup(c.R.Context(), id, actor, v, pol)
	}
	if err != nil {
		return apikit.Reply{}, TrackerError(err)
	}
	s.Notify("tracker")
	return apikit.Reply{Data: WireCandidacy(entry)}, nil
}

func (s *Handlers) CandidacyTransitions(c *apikit.Call) (apikit.Reply, error) {
	tr, err := s.Tracker()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	d, err := tr.Get(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, TrackerError(err)
	}
	ordinary, correction := tracker.Targets(d.Entry.Status)
	return apikit.Reply{Data: wire.CandidacyTransitions{Status: string(d.Entry.Status), Ordinary: statusNames(ordinary), Correction: statusNames(correction)}}, nil
}

func statusNames(list []tracker.Status) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = string(s)
	}
	return out
}

func (s *Handlers) ApplicationLookup(c *apikit.Call) (apikit.Reply, error) {
	tr, err := s.Tracker()
	if err != nil {
		return apikit.Reply{}, err
	}
	raw := c.R.URL.Query().Get("url")
	res, err := tr.Lookup(c.R.Context(), raw)
	if errors.Is(err, tracker.ErrInvalid) {
		return apikit.Reply{}, apikit.InvalidField("url", "url must be an absolute http or https URL of at most 2048 characters")
	}
	if err != nil {
		return apikit.Reply{}, TrackerError(err)
	}
	out := wire.ApplicationLookup{Found: res.Found, IdentityKey: res.IdentityKey}
	if res.Entry != nil {
		e := WireCandidacy(*res.Entry)
		out.Entry = &e
	}
	for _, h := range res.History {
		out.History = append(out.History, wire.StatusHistoryItem{Status: string(h.Status), At: apikit.Stamp(h.At), Actor: string(h.Actor)})
	}
	if led := s.Ledger(); led != nil {
		p, ok, err := led.PriorFor(c.R.Context(), raw)
		if err != nil {
			return apikit.Reply{}, err
		}
		if ok {
			out.Submission = &wire.PriorSubmission{Confirmed: p.Confirmed, Source: p.Source, At: wire.Time{Time: p.ObservedAt.UTC()}}
		}
	}
	return apikit.Reply{Data: out}, nil
}

// exportFormats maps the format parameter to the content type and file name.
var exportFormats = map[string]struct{ contentType, file string }{
	tracker.FormatMarkdown: {"text/markdown; charset=utf-8", "tracker.md"},
	tracker.FormatCSV:      {"text/csv; charset=utf-8", "tracker.csv"},
}

// ExportCandidacies writes the export itself: a file, not an envelope.
func (s *Handlers) ExportCandidacies(c *apikit.Call) (apikit.Reply, error) {
	tr, err := s.Tracker()
	if err != nil {
		return apikit.Reply{}, err
	}
	q := apikit.QueryOf(c)
	format := q.Str("format")
	lq, err := candidacyFilter(q)
	lq.Sort = q.Str("sort")
	if err != nil {
		return apikit.Reply{}, TrackerError(err)
	}
	if _, err := s.narrowByMail(c, q, &lq); err != nil {
		return apikit.Reply{}, err
	}
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	meta, ok := exportFormats[format]
	if !ok {
		return apikit.Reply{}, apikit.InvalidField("format", "format must be md or csv")
	}
	var buf bytes.Buffer
	if err := tr.Export(c.R.Context(), &buf, format, lq); err != nil {
		return apikit.Reply{}, TrackerError(err)
	}
	h := c.W.Header()
	h.Set("Content-Type", meta.contentType)
	h.Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, url.PathEscape(meta.file)))
	h.Set("Content-Length", strconv.Itoa(buf.Len()))
	c.W.WriteHeader(http.StatusOK)
	_, _ = c.W.Write(buf.Bytes())
	c.Handled = true
	return apikit.Reply{}, nil
}
