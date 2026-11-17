package api

import (
	"context"
	"errors"
	"path/filepath"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/gmail"
	"applysmith/internal/gmail/lifecycle"
	"applysmith/internal/llm"
	"applysmith/internal/messages"
	"applysmith/internal/notify"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
)

// gmailStack is the Gmail stack of the open workspace.
func (s *Server) gmailStack() (*gmail.Stack, error) {
	d, err := s.dom()
	if err != nil {
		return nil, err
	}
	return d.gmail, nil
}

// openGmail builds the Gmail stack over the open store and closes the run a
// dead process left behind, so health never stays stuck at running.
func (s *Server) openGmail(ctx context.Context, st *store.Store, ns *notifyStack, tr *tracker.Tracker, router *llm.Router) (*gmail.Stack, error) {
	o := s.cfg.Domain.Gmail
	if o.Hooks.OnRefused == nil && o.Hooks.OnRestored == nil {
		o.Hooks = s.gmailHooks(ns)
	}
	if o.Taxonomy == nil {
		tax, err := gmail.LoadTaxonomy(filepath.Join(s.cfg.Workspace.Root, gmail.SendersFile))
		if err != nil {
			s.log.Warn("gmail-senders.yml is invalid; using the shipped sender taxonomy", "error", err.Error())
		}
		o.Taxonomy = tax
	}
	if o.Now == nil {
		o.Now = s.now
	}
	if o.Windows == nil {
		o.Windows = apply.NewRunWindows(st, o.Now)
	}
	if o.Tracker == nil {
		o.Tracker = tr
	}
	if o.Model == nil {
		o.Model = gmailModel{router}
	}
	if o.Log == nil {
		o.Log = s.log
	}
	if o.Alerts == nil {
		o.Alerts = func(ctx context.Context, a gmail.Alert) error {
			return ns.policy.AnnounceGmailAlert(ctx, notify.GmailAlert{MessageID: a.MessageID, Title: a.Title, Body: a.Body, Link: a.Link, InterviewStart: a.InterviewStart})
		}
	}
	stack := gmail.Open(st, s.cfg.Vault, o)
	stack.Syncer.Ended = func() { s.notifyTopics("gmail") }
	if err := stack.Syncer.Recover(ctx); err != nil {
		return nil, err
	}
	return stack, nil
}

// gmailComponent is the `gmail` component of the health document: unconfigured
// without an app password, degraded when Gmail refuses it or the last sync is
// degraded, ok otherwise. It makes no Gmail request.
func (s *Server) gmailComponent(unset wire.Component) wire.Component {
	d := s.domain.Load()
	if d == nil {
		return unset
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	switch d.gmail.Service.Component(ctx) {
	case gmail.ComponentOK:
		return wire.Component{Name: "gmail", Status: wire.ComponentOK}
	case gmail.ComponentDegraded:
		return wire.Component{Name: "gmail", Status: wire.ComponentDegraded, Detail: "sign-in or sync needs attention"}
	}
	return unset
}

// gmailModel writes the Gmail texts through the model router under the
// gmail-summary and gmail-digest tasks.
type gmailModel struct{ router *llm.Router }

func (m gmailModel) Complete(ctx context.Context, task, system, prompt string) (gmail.Completion, error) {
	resp, err := m.router.Complete(ctx, llm.Request{Task: task, System: system, Prompt: prompt})
	if err != nil {
		return gmail.Completion{}, err
	}
	return gmail.Completion{Text: resp.Text, Finish: resp.FinishReason}, nil
}

// gmailDrafter drafts a linked thread with the Tracker's drafter; a row it
// refuses, or no route for its task (said in Note), falls back to the
// thread-only draft.
func gmailDrafter(m *messages.Service) gmail.Drafter {
	return func(ctx context.Context, id int64, kind, mail string) (gmail.Draft, error) {
		k := ""
		if kind == lifecycle.DraftReply {
			k = messages.FollowupReply
		}
		d, err := m.Followup(ctx, messages.FollowupRequest{Candidacy: id, Kind: k, Mail: mail})
		if errors.Is(err, messages.ErrInvalid) || errors.Is(err, messages.ErrNotFound) {
			return gmail.Draft{}, gmail.ErrThreadOnly
		}
		if errors.Is(err, llm.ErrNoRoute) {
			return gmail.Draft{Note: "the " + messages.Task + " task has no model it can use"}, gmail.ErrThreadOnly
		}
		return gmail.Draft{Text: d.Primary.Message, Confirmed: d.Confirmed, ToConfirm: d.ToConfirm}, err
	}
}

// The Tracker's message drafts stay in api beside the Gmail drafter: both
// draw on the messages service, which imports the answer-bank matcher, and
// trackerapi does not.

// draftFollowup is `applysmith followup-draft` for the Tracker: a draft to copy;
// nothing is sent or recorded.
func (s *Server) draftFollowup(c *apikit.Call) (apikit.Reply, error) {
	var w wire.FollowupDraftRequest
	return s.draftMessage(c, &w, func(d *domain, id int64) (messages.Draft, error) {
		return d.drafts.Followup(c.R.Context(), messages.FollowupRequest{Candidacy: id, Kind: w.Kind, Contact: w.Contact, Context: w.Context})
	}, messages.FollowupMaxChars)
}

// draftOutreach is `applysmith outreach` for one tracker row: a note to copy;
// nothing is sent or recorded.
func (s *Server) draftOutreach(c *apikit.Call) (apikit.Reply, error) {
	var w wire.OutreachDraftRequest
	return s.draftMessage(c, &w, func(d *domain, id int64) (messages.Draft, error) {
		return d.drafts.Outreach(c.R.Context(), messages.OutreachRequest{Candidacy: id, Contact: messages.Contact{Kind: w.Contact, Name: w.Name, Note: w.About, Date: w.Date}})
	}, messages.OutreachMaxChars)
}

func (s *Server) draftMessage(c *apikit.Call, body any, draft func(*domain, int64) (messages.Draft, error), maxChars int) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	if e := apikit.Decode(c.Body, body); e != nil {
		return apikit.Reply{}, e
	}
	dr, err := draft(d, id)
	switch {
	case errors.Is(err, messages.ErrNotFound):
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "%s", err)
	case errors.Is(err, messages.ErrInvalid), errors.Is(err, messages.ErrCadence):
		return apikit.Reply{}, apikit.NewErr(wire.CodeValidationFailed, "%s", err)
	case errors.Is(err, llm.ErrNoRoute):
		// The router's text names a command; the page links Settings, Models.
		return apikit.Reply{}, apikit.WithReason(apikit.Unavailable("no message was drafted: "+llm.NoModel("the "+messages.Task+" task")), wire.ReasonNoModelRoute)
	case llm.KindOf(err) == llm.KindFakeFixture:
		// The offline simulation evaluates postings but writes no message.
		return apikit.Reply{}, apikit.WithReason(apikit.Unavailable("no message was drafted: the offline simulation writes no messages; choose a language model under Settings, Models to draft one"), wire.ReasonNoModelRoute)
	case err != nil:
		return apikit.Reply{}, apikit.Unavailable("no message was drafted: " + err.Error())
	}
	variant := func(v messages.Variant) wire.MessageVariant {
		return wire.MessageVariant{Angle: v.Angle, Message: v.Message}
	}
	alts := make([]wire.MessageVariant, 0, len(dr.Alternatives))
	for _, v := range dr.Alternatives {
		alts = append(alts, variant(v))
	}
	return apikit.Reply{Data: wire.MessageDraft{Kind: dr.Kind, Primary: variant(dr.Primary), Alternatives: alts, MaxChars: maxChars,
		Confirmed: apikit.NonNil(dr.Confirmed), ToConfirm: apikit.NonNil(dr.ToConfirm), Dropped: apikit.NonNil(dr.Dropped), Notes: apikit.NonNil(dr.Notes)}}, nil
}
