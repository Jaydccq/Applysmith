package gmailapi

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/gmail"
	"applysmith/internal/gmail/lifecycle"
	"applysmith/internal/gmail/link"
	"applysmith/internal/llm"
	"applysmith/internal/messages"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
)

// The Gmail read models and actions built on stored signals: the action inbox,
// thread summaries, the link review queue, offer confirmations, the digests and
// the switches. Nothing here makes a Gmail request.

func wireTime(t time.Time) wire.Time { return wire.Time{Time: t} }

func optTime(t time.Time) *wire.Time {
	if t.IsZero() {
		return nil
	}
	w := wireTime(t)
	return &w
}

func optID(id int64) *wire.SafeInt {
	if id == 0 {
		return nil
	}
	w := wire.SafeInt(id)
	return &w
}

func toInboxThread(t gmail.Thread) wire.GmailInboxThread {
	return wire.GmailInboxThread{ThreadID: t.ID, Employer: t.Employer, Role: t.Role, Subject: t.Subject, State: t.View.State, Kind: t.Kind(), Lane: t.View.Lane,
		Owner: t.View.Owner, Attention: t.View.Attention, NextAction: t.View.NextAction, DraftKind: t.View.DraftKind,
		StalenessDays: wire.SafeInt(t.View.StalenessDays), DueAt: optTime(t.View.Due), LastIncomingAt: wireTime(t.LastIncoming),
		Link: t.Link, CandidacyID: optID(t.CandidacyID), Done: t.Done}
}

var (
	inboxLanes  = lifecycle.Lanes
	inboxOwners = []string{lifecycle.OwnerUser, lifecycle.OwnerCompany, lifecycle.OwnerNone, lifecycle.OwnerUnclear}
	inboxLinks  = []string{link.Auto, link.Review, link.Unlinked}
)

func oneOf(q *apikit.Query, name string, allowed []string) string {
	v := q.Str(name)
	if v != "" && !slices.Contains(allowed, v) {
		q.Fail(apikit.InvalidField(name, "%s must be one of %s", name, strings.Join(allowed, ", ")))
	}
	return v
}

// Inbox serves the action inbox, filtered by lane, owner, kind, link
// decision, linked tracker row (candidacy) and a text (q) over employer, role
// and subject, and sorted by attention and then recency.
func (s *Handlers) Inbox(c *apikit.Call) (apikit.Reply, error) {
	q := apikit.QueryOf(c)
	iq := gmail.InboxQuery{Lane: oneOf(q, "lane", inboxLanes), Owner: oneOf(q, "owner", inboxOwners), Link: oneOf(q, "link", inboxLinks),
		Kind: oneOf(q, "kind", gmail.InboxKinds), Text: strings.TrimSpace(q.Str("q")), Cursor: q.Str("cursor"), Limit: q.Integer("limit", gmail.DefaultInboxLimit), Candidacy: int64(q.Integer("candidacy", 0))}
	if q.Err == nil && iq.Limit < 1 {
		q.Fail(apikit.InvalidField("limit", "limit must be 1 or more"))
	}
	if q.Err == nil && iq.Candidacy < 0 {
		q.Fail(apikit.InvalidField("candidacy", "candidacy must be a tracker row id"))
	}
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	page, err := stack.Repo.Inbox(c.R.Context(), iq, s.Now())
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.GmailInboxPage{Threads: make([]wire.GmailInboxThread, len(page.Threads)), Next: page.Next, Total: wire.SafeInt(page.Total)}
	for i, t := range page.Threads {
		out.Threads[i] = toInboxThread(t)
	}
	return apikit.Reply{Data: out}, nil
}

func threadError(err error) error {
	switch {
	case errors.Is(err, gmail.ErrNoThread):
		return apikit.NewErr(wire.CodeNotFound, "no such thread")
	case errors.Is(err, gmail.ErrNotActionable):
		return apikit.NewErr(wire.CodeConflict, "%s", err.Error())
	}
	return err
}

// ThreadDone marks a thread done.
func (s *Handlers) ThreadDone(c *apikit.Call) (apikit.Reply, error) {
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("threadId")
	if err := stack.Repo.MarkDone(c.R.Context(), id); err != nil {
		return apikit.Reply{}, threadError(err)
	}
	s.Notify("gmail")
	return apikit.Reply{Data: wire.GmailThreadDone{ThreadID: id}}, nil
}

// ThreadReopen undoes marking a thread done.
func (s *Handlers) ThreadReopen(c *apikit.Call) (apikit.Reply, error) {
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("threadId")
	if err := stack.Repo.ReopenDone(c.R.Context(), id); err != nil {
		return apikit.Reply{}, err
	}
	s.Notify("gmail")
	return apikit.Reply{Data: wire.GmailThreadDone{ThreadID: id}}, nil
}

// ThreadDismiss marks the incoming signals of a thread as not job related.
func (s *Handlers) ThreadDismiss(c *apikit.Call) (apikit.Reply, error) {
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("threadId")
	ids, err := stack.Repo.DismissThread(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, threadError(err)
	}
	s.Notify("gmail")
	return apikit.Reply{Data: wire.GmailThreadDismissed{ThreadID: id, SignalIDs: apikit.NonNil(ids)}}, nil
}

// Thread serves one thread with its timeline and evidence; a status a
// mail moved carries the row's status now and whether it can be put back, by
// the rule of Moves.
func (s *Handlers) Thread(c *apikit.Call) (apikit.Reply, error) {
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	tr, err := s.Tracker()
	if err != nil {
		return apikit.Reply{}, err
	}
	d, ok, err := stack.Repo.ThreadDetail(c.R.Context(), c.R.PathValue("threadId"), s.Now())
	if err != nil {
		return apikit.Reply{}, err
	}
	if !ok {
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no such thread")
	}
	out := wire.GmailThreadDetail{Thread: toInboxThread(d.Thread), Timeline: make([]wire.GmailTimelineEntry, len(d.Timeline))}
	for i, e := range d.Timeline {
		row := wire.GmailTimelineEntry{MessageID: e.MessageID, Type: e.Type, Tier: e.Tier, Direction: e.Direction, Employer: e.Employer, Role: e.Role,
			Subject: e.Subject, Snippet: e.Snippet, ReceivedAt: wireTime(time.UnixMilli(e.ReceivedMS).UTC()), RuleID: e.RuleID,
			ReviewReasons: apikit.NonNil(splitCSV(e.ReviewReasons)), Link: e.Link, LinkReasons: apikit.NonNil(e.Reasons)}
		if e.DueMS != 0 {
			row.DueAt = optTime(time.UnixMilli(e.DueMS).UTC())
		}
		if len(e.Evidence) > 0 {
			row.Evidence = e.Evidence
		}
		row.Outcome = toOutcome(e.Outcome)
		if o := e.Outcome; o != nil && o.Result == gmail.OutcomeChanged {
			if now, err := tr.Get(c.R.Context(), o.CandidacyID); err == nil {
				row.RowStatus, row.PutBack = string(now.Entry.Status), now.Revertible("source_message_id", e.MessageID)
			}
		}
		out.Timeline[i] = row
	}
	return apikit.Reply{Data: out}, nil
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// Summary serves the short summary of a thread from stored data.
func (s *Handlers) Summary(c *apikit.Call) (apikit.Reply, error) {
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	sum, err := stack.SummaryFor(c.R.Context(), c.R.PathValue("id"))
	if errors.Is(err, gmail.ErrNoThread) {
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no such thread")
	}
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: wire.GmailSummary{Text: sum.Text, GeneratedAt: wireTime(sum.GeneratedAt), Method: sum.Method, Cached: sum.Cached}}, nil
}

// DraftError maps a failed draft of the given kind onto the closed set of API
// errors; no route for the model reads as the follow-up drafter does. The
// Telegram Draft reply button says the same.
func DraftError(kind string, err error) *apikit.Error {
	none := "no reply was drafted: "
	if kind == lifecycle.DraftFollowUp {
		none = "no follow-up was drafted: "
	}
	switch {
	case errors.Is(err, gmail.ErrNoThread):
		return apikit.NewErr(wire.CodeNotFound, "no such thread")
	case errors.Is(err, gmail.ErrNoModel), errors.Is(err, llm.ErrNoRoute):
		return apikit.WithReason(apikit.Unavailable(none+llm.NoModel("the "+gmail.TaskDigest+" task")), wire.ReasonNoModelRoute)
	case llm.KindOf(err) == llm.KindFakeFixture:
		return apikit.WithReason(apikit.Unavailable(none+"the offline simulation writes no messages; choose a language model under Settings, Models to draft one"), wire.ReasonNoModelRoute)
	case errors.Is(err, gmail.ErrUnusable):
		return apikit.Unavailable(none + "both tries failed the checks (an unsourced number, a link, a cut-off sentence); try again")
	case errors.Is(err, messages.ErrNoDraft):
		return apikit.Unavailable(none + strings.TrimPrefix(err.Error(), messages.ErrNoDraft.Error()+": ") + "; try again")
	}
	return apikit.Unavailable(none + err.Error())
}

func toGmailDraft(d gmail.Draft) *wire.GmailDraft {
	return &wire.GmailDraft{Text: d.Text, Kind: d.Kind, GeneratedAt: wireTime(d.GeneratedAt), Method: d.Method, Confirmed: d.Confirmed, ToConfirm: d.ToConfirm, Note: d.Note}
}

// Draft serves the draft kept for the thread's current state, if any.
func (s *Handlers) Draft(c *apikit.Call) (apikit.Reply, error) {
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	d, err := stack.StoredDraftOf(c.R.Context(), c.R.PathValue("id"))
	if err != nil {
		return apikit.Reply{}, DraftError(d.Kind, err)
	}
	if d.Text == "" {
		return apikit.Reply{Data: wire.GmailThreadDraft{Kind: d.Kind}}, nil
	}
	return apikit.Reply{Data: wire.GmailThreadDraft{Draft: toGmailDraft(d), Kind: d.Kind}}, nil
}

// DraftWrite writes a new reply draft for a thread and keeps it. Nothing
// is sent.
func (s *Handlers) DraftWrite(c *apikit.Call) (apikit.Reply, error) {
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	d, err := stack.DraftReply(c.R.Context(), c.R.PathValue("id"))
	if err != nil {
		return apikit.Reply{}, DraftError(d.Kind, err)
	}
	return apikit.Reply{Data: wire.GmailThreadDraft{Draft: toGmailDraft(d), Kind: d.Kind}}, nil
}

func toOutcome(o *gmail.StatusOutcome) *wire.GmailOutcome {
	if o == nil {
		return nil
	}
	return &wire.GmailOutcome{Result: o.Result, FromStatus: o.From, ToStatus: o.To, Reason: o.Reason, CandidacyID: optID(o.CandidacyID)}
}

// Links serves the review queue.
func (s *Handlers) Links(c *apikit.Call) (apikit.Reply, error) {
	q := apikit.QueryOf(c)
	decision := oneOf(q, "decision", []string{link.Review, link.Unlinked})
	if q.Err == nil && decision == "" {
		q.Fail(apikit.InvalidField("decision", "decision must be review or unlinked"))
	}
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	items, err := stack.Repo.LinkReview(c.R.Context(), decision)
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.GmailLinkList{Items: make([]wire.GmailLinkItem, len(items))}
	for i, it := range items {
		row := wire.GmailLinkItem{SignalID: it.SignalID, ThreadID: it.ThreadID, Type: it.Type, Tier: it.Tier, Employer: it.Employer, Role: it.Role,
			Subject: it.Subject, ReceivedAt: wireTime(it.Received), Decision: it.Decision, CandidacyID: optID(it.CandidacyID), Score: it.Score,
			RunnerUp: it.RunnerUp, Reasons: apikit.NonNil(it.Reasons), Strength: link.Strength(it.Score), NearlyAsClose: link.NearlyAsClose(it.Score, it.RunnerUp)}
		if it.OutcomeReason != "" {
			row.Outcome = &wire.GmailOutcome{Result: gmail.OutcomeReview, FromStatus: it.OutcomeFrom, ToStatus: it.OutcomeTo, Reason: it.OutcomeReason}
		}
		out.Items[i] = row
	}
	return apikit.Reply{Data: out}, nil
}

func linkError(err error) error {
	switch {
	case errors.Is(err, gmail.ErrNoSignal):
		return apikit.NewErr(wire.CodeNotFound, "no such signal")
	case errors.Is(err, gmail.ErrNoLinkable):
		return apikit.NewErr(wire.CodeConflict, "%s", err.Error())
	}
	return err
}

// LinkConfirm links the thread of a signal to a candidacy, finally, and
// attempts the signal's status change as the Gmail actor.
func (s *Handlers) LinkConfirm(c *apikit.Call) (apikit.Reply, error) {
	var req wire.GmailLinkConfirm
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if req.CandidacyID < 1 {
		return apikit.Reply{}, apikit.InvalidField("candidacyId", "candidacyId must be a positive whole number")
	}
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("signalId")
	res, err := stack.Pipeline.ConfirmLink(c.R.Context(), id, int64(req.CandidacyID))
	if err != nil {
		return apikit.Reply{}, linkError(err)
	}
	s.Notify("gmail", "tracker")
	return apikit.Reply{Data: wire.GmailLinkConfirmed{SignalID: id, ThreadID: res.ThreadID, CandidacyID: req.CandidacyID, Outcome: toOutcome(res.Outcome)}}, nil
}

// LinkReject marks a signal as not job-related.
func (s *Handlers) LinkReject(c *apikit.Call) (apikit.Reply, error) {
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("signalId")
	if err := stack.Repo.RejectLink(c.R.Context(), id); err != nil {
		return apikit.Reply{}, linkError(err)
	}
	s.Notify("gmail")
	return apikit.Reply{Data: wire.GmailLinkRejected{SignalID: id}}, nil
}

// LinkRestore undoes a rejection of the signal.
func (s *Handlers) LinkRestore(c *apikit.Call) (apikit.Reply, error) {
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("signalId")
	if err := stack.Repo.RestoreLink(c.R.Context(), id); err != nil {
		return apikit.Reply{}, linkError(err)
	}
	s.Notify("gmail")
	return apikit.Reply{Data: wire.GmailLinkRestored{SignalID: id}}, nil
}

// Confirmations lists the offers waiting for the user, each named by the
// tracker row it would move.
func (s *Handlers) Confirmations(c *apikit.Call) (apikit.Reply, error) {
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	tr, err := s.Tracker()
	if err != nil {
		return apikit.Reply{}, err
	}
	rows, err := stack.Repo.Confirmations(c.R.Context(), "pending")
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.GmailConfirmationList{Confirmations: make([]wire.GmailConfirmation, len(rows))}
	for i, r := range rows {
		out.Confirmations[i] = wire.GmailConfirmation{ID: wire.SafeInt(r.ID), SignalID: r.MessageID, ThreadID: r.ThreadID,
			CandidacyID: wire.SafeInt(r.CandidacyID), Status: r.Status, State: r.State, CreatedAt: wireTime(r.CreatedAt)}
		if row, err := tr.Get(c.R.Context(), r.CandidacyID); err == nil {
			out.Confirmations[i].Company, out.Confirmations[i].Role = row.Entry.Company, row.Entry.Role
		}
	}
	return apikit.Reply{Data: out}, nil
}

// Moves lists the statuses mails moved on their own in the last two
// weeks, each with the row's company, role and status now, and whether the
// move can be put back: only while it is still the row's latest status change.
func (s *Handlers) Moves(c *apikit.Call) (apikit.Reply, error) {
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	tr, err := s.Tracker()
	if err != nil {
		return apikit.Reply{}, err
	}
	moves, err := stack.Repo.Moves(c.R.Context(), s.Now().Add(-14*24*time.Hour), 10)
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.GmailMoveList{Moves: []wire.GmailMove{}}
	for _, m := range moves {
		row, err := tr.Get(c.R.Context(), m.CandidacyID)
		if err != nil {
			continue
		}
		out.Moves = append(out.Moves, wire.GmailMove{ThreadID: m.ThreadID, Subject: m.Subject, At: wireTime(m.At), Outcome: *toOutcome(&m.StatusOutcome),
			Company: row.Entry.Company, Role: row.Entry.Role, Status: string(row.Entry.Status), PutBack: row.Revertible("source_message_id", m.MessageID)})
	}
	return apikit.Reply{Data: out}, nil
}

// RestoreConfirmation is the undo of a decision: the offer waits again, and
// the undo of a confirmation puts the row back in the same transaction, or
// refuses with nothing changed when the row moved on since.
func (s *Handlers) RestoreConfirmation(c *apikit.Call) (apikit.Reply, error) {
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	back, err := stack.Pipeline.RestoreConfirmation(c.R.Context(), id)
	switch {
	case errors.Is(err, gmail.ErrNoConfirmation):
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no decided confirmation with that id")
	case errors.Is(err, store.ErrConflict):
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "%s", strings.TrimPrefix(err.Error(), store.ErrConflict.Error()+": "))
	case err != nil:
		return apikit.Reply{}, s.TrackerError(err)
	}
	s.Notify("gmail", "tracker")
	return apikit.Reply{Data: wire.GmailConfirmationDecided{ID: wire.SafeInt(id), State: "pending", RowStatus: string(back)}}, nil
}

func (s *Handlers) DecideConfirmationOf(confirm bool) apikit.Handler {
	return func(c *apikit.Call) (apikit.Reply, error) {
		id, e := apikit.PathID(c, "id")
		if e != nil {
			return apikit.Reply{}, e
		}
		stack, err := s.Stack()
		if err != nil {
			return apikit.Reply{}, err
		}
		if err := stack.Pipeline.DecideConfirmation(c.R.Context(), id, confirm); err != nil {
			if errors.Is(err, gmail.ErrNoConfirmation) {
				return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no pending confirmation with that id")
			}
			var refused *tracker.RefusedError
			if errors.As(err, &refused) {
				return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "%s", err.Error())
			}
			return apikit.Reply{}, s.TrackerError(err)
		}
		state := "dismissed"
		if confirm {
			state = "confirmed"
		}
		s.Notify("gmail", "tracker")
		return apikit.Reply{Data: wire.GmailConfirmationDecided{ID: wire.SafeInt(id), State: state}}, nil
	}
}

func toDigestItems(in []gmail.DigestItem) []wire.GmailDigestItem {
	out := make([]wire.GmailDigestItem, len(in))
	for i, it := range in {
		out[i] = wire.GmailDigestItem{Key: it.Key, Section: it.Section, Company: it.Company, Role: it.Role, Lane: it.Lane, State: it.State,
			Owner: it.Owner, NextAction: it.NextAction, Due: it.Due, Line: it.Line, DraftKind: it.DraftKind, Draft: it.Draft}
	}
	return out
}

func toDigest(d gmail.Digest) wire.GmailDigest {
	out := wire.GmailDigest{ID: wire.SafeInt(d.ID), Kind: d.Kind, Trigger: d.Trigger, Status: d.Status, CreatedAt: wireTime(d.CreatedAt),
		WindowStart: wireTime(d.WindowStart), WindowEnd: wireTime(d.WindowEnd), Method: d.Method, Headline: d.Headline, Text: d.Body.Text,
		Items: toDigestItems(d.Body.Items), Muted: toDigestItems(d.Body.Muted), Omitted: wire.SafeInt(d.Body.Omitted),
		Receipts: wire.SafeInt(d.Body.Receipts), Narrative: d.Body.Narrative, Delivery: d.Delivery, Error: d.Error}
	if out.Delivery == nil {
		out.Delivery = map[string]string{}
	}
	if d.Body.Funnel != nil {
		out.Funnel = map[string]wire.SafeInt{}
		for k, v := range d.Body.Funnel {
			out.Funnel[k] = wire.SafeInt(v)
		}
	}
	return out
}

func (s *Handlers) Digests(c *apikit.Call) (apikit.Reply, error) {
	q := apikit.QueryOf(c)
	limit := q.Integer("limit", 20)
	if q.Err == nil && limit < 1 {
		q.Fail(apikit.InvalidField("limit", "limit must be 1 or more"))
	}
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	rows, err := stack.Repo.Digests(c.R.Context(), limit)
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.GmailDigestList{Digests: make([]wire.GmailDigest, len(rows))}
	for i, d := range rows {
		out.Digests[i] = toDigest(d)
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Handlers) Digest(c *apikit.Call) (apikit.Reply, error) {
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	d, err := stack.Repo.Digest(c.R.Context(), id)
	if errors.Is(err, gmail.ErrNoDigest) {
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no such digest")
	}
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: toDigest(d)}, nil
}

func (s *Handlers) DigestVerdicts(c *apikit.Call) (apikit.Reply, error) {
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	v, err := stack.Repo.ThreadVerdicts(c.R.Context())
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.GmailDigestVerdicts{Verdicts: map[string]string{}}
	for k, f := range v {
		out.Verdicts[k] = f.Verdict
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Handlers) DigestFeedback(c *apikit.Call) (apikit.Reply, error) {
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	var req wire.GmailDigestFeedback
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if !slices.Contains([]string{gmail.VerdictUp, gmail.VerdictDown, gmail.VerdictClear}, req.Verdict) {
		return apikit.Reply{}, apikit.InvalidField("verdict", "verdict must be up, down or clear")
	}
	if strings.TrimSpace(req.ThreadKey) == "" {
		return apikit.Reply{}, apikit.InvalidField("threadKey", "threadKey is required")
	}
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	switch err := stack.Repo.Rate(c.R.Context(), id, req.ThreadKey, req.Verdict); {
	case errors.Is(err, gmail.ErrNoDigest):
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no such digest")
	case errors.Is(err, gmail.ErrNotInDigest):
		return apikit.Reply{}, apikit.NewErr(wire.CodeBadRequest, "the digest neither lists nor mutes that thread")
	case err != nil:
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: wire.GmailDigestRated{DigestID: wire.SafeInt(id), ThreadKey: req.ThreadKey, Verdict: req.Verdict}}, nil
}

// DigestRun produces a digest now and delivers it like a scheduled one.
// DigestRun starts making a brief now as a background run: composing it
// is a model call that outlasts a request. The outcome is the new brief's id.
func (s *Handlers) DigestRun(c *apikit.Call) (apikit.Reply, error) {
	key, e := apikit.IdempotencyKey(c)
	if e != nil {
		return apikit.Reply{}, e
	}
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	return s.StartPass(c.R.Context(), apikit.Pass{Operation: "gmail.digest", Kind: DigestKind, Stage: "digest", Claim: "gmail-digest",
		Busy: "a digest is already being produced", Key: key, Principal: c.P.ID, Input: []byte("{}"),
		Run: func(ctx context.Context, progress func(string)) (string, error) {
			progress("Making the brief")
			dig, err := stack.Digests.Run(ctx)
			if errors.Is(err, gmail.ErrBusy) {
				return "", errors.New("a digest is already being produced")
			}
			if err != nil {
				return "", err
			}
			s.Notify("gmail")
			return strconv.FormatInt(dig.ID, 10), nil
		}})
}

// DigestKind is the run kind of a brief made on request. The brief
// announces itself, so the board does not show the run.
const DigestKind = "gmail.digest"

func (s *Handlers) Settings(c *apikit.Call) (apikit.Reply, error) {
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	sw := stack.Repo.Switches(c.R.Context())
	return apikit.Reply{Data: wire.GmailSettings{Alerts: sw.Alerts, ScheduledDigests: sw.ScheduledDigests}}, nil
}

func (s *Handlers) PatchSettings(c *apikit.Call) (apikit.Reply, error) {
	var req wire.GmailSettingsPatch
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	if err := stack.Repo.SetSwitches(c.R.Context(), req.Alerts, req.ScheduledDigests); err != nil {
		return apikit.Reply{}, err
	}
	sw := stack.Repo.Switches(c.R.Context())
	return apikit.Reply{Data: wire.GmailSettings{Alerts: sw.Alerts, ScheduledDigests: sw.ScheduledDigests}}, nil
}
