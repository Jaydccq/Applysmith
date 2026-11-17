package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"applysmith/internal/answerbank/assist"
	"applysmith/internal/answerbank/bind"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/learningapi"
	"applysmith/internal/api/wire"
	"applysmith/internal/research"
)

// liveFacts serves the answer bank's live-fact lane from the research service
// behind research_question: the same sources, citation enforcement and
// deadline. The brief request asks for the fact alone; the lane still derives
// the typed text itself.
type liveFacts struct{ svc *research.Service }

func (l liveFacts) Available(ctx context.Context) bool { return l.svc.Available(ctx) }

func (l liveFacts) Lookup(ctx context.Context, question string) (assist.LiveFact, error) {
	ans, err := l.svc.Research(ctx, research.Request{Question: question, Brief: true})
	var uncited *research.UncitedError
	switch {
	case errors.Is(err, research.ErrNoSource):
		return assist.LiveFact{}, assist.ErrLiveFactNoSource
	case errors.As(err, &uncited), errors.Is(err, research.ErrNoResults):
		return assist.LiveFact{}, fmt.Errorf("%w: %v", assist.ErrLiveFactRefused, err)
	case err != nil:
		return assist.LiveFact{}, err
	case ans.Unanswered || strings.TrimSpace(ans.Text) == "":
		return assist.LiveFact{}, fmt.Errorf("%w: the sources do not answer", assist.ErrLiveFactRefused)
	}
	out := assist.LiveFact{Text: ans.Text, Via: ans.Via}
	for _, s := range ans.Sources {
		out.Sources = append(out.Sources, assist.LiveFactSource{N: s.N, URL: s.URL})
	}
	return out, nil
}

func (s *Server) answerMatcher() (*bind.Binding, error) {
	d, err := s.dom()
	if err != nil {
		return nil, err
	}
	return d.bind, nil
}

// answersMatch is the extension's match route. It answers from the profile and
// the bank as they are now, runs the model lanes, and records usage; it never
// changes the bank.
func (s *Server) answersMatch(c *apikit.Call) (apikit.Reply, error) {
	b, err := s.answerMatcher()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.AnswerMatchRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if req.Host == "" {
		return apikit.Reply{}, apikit.InvalidField("host", "host is required")
	}
	if svc, err := s.learning(); err != nil {
		return apikit.Reply{}, err
	} else if e := learningapi.RequireIssuedRun(c.R.Context(), svc, req.RunID); e != nil {
		return apikit.Reply{}, e
	}
	s.touchFill(c.R.Context(), req.RunID)
	qs := make([]match.Question, len(req.Questions))
	for i, q := range req.Questions {
		qs[i] = match.Question{Selector: q.Selector, Text: q.Text, SectionPath: q.Section, Kind: match.ControlKind(q.Kind),
			InputType: bind.WireInputType(match.ControlKind(q.Kind), match.InputType(q.InputType)), Required: q.Required, MaxLength: q.MaxLength}
		for _, o := range q.Options {
			qs[i].Options = append(qs[i].Options, match.Option{Value: o.Value, Label: o.Label})
		}
		if len(qs[i].SectionPath) == 0 {
			qs[i].SectionPath = bind.SectionPath(q.Selector)
		}
	}
	p := req.Posting
	out, err := b.Run(c.R.Context(), bind.Request{RunID: req.RunID, Host: req.Host, Questions: qs,
		Posting:             bind.Posting{Company: p.Company, Title: p.Title, Description: p.Description, Location: p.Location, URL: p.URL},
		CoverLetterAttached: req.CoverLetterAttached, ResumeAttached: req.ResumeAttached})
	if err != nil {
		if errors.Is(err, match.ErrValidation) {
			return apikit.Reply{}, apikit.NewErr(wire.CodeValidationFailed, "%s", err.Error())
		}
		return apikit.Reply{}, answerBankError(err)
	}
	res := wire.AnswerMatchResult{Results: make([]wire.AnswerMatchItem, len(out.Results))}
	for i, r := range out.Results {
		res.Results[i] = wire.AnswerMatchItem{Selector: r.Selector, Outcome: string(r.Outcome), Answer: r.Answer, ChosenLabel: r.ChosenLabel,
			ChosenLabels: r.ChosenLabels, EntryID: r.EntryID, AnswerKey: r.AnswerKey, Source: string(r.Source), Confidence: r.Confidence,
			Reason: string(r.Reason), Detail: string(r.Detail), Terminal: r.Terminal, Trace: apikit.NonNil(r.Trace), Cited: r.Cited,
			ClientMayOverwrite: bind.ClientMayOverwrite(r), OwnAnswer: out.Own[i]}
	}
	return apikit.Reply{Data: res}, nil
}
