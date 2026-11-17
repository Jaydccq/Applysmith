package api

import (
	"context"
	"strings"

	"applysmith/internal/answerbank/assist"
	"applysmith/internal/answerbank/bind"
	"applysmith/internal/answerbank/learn"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/workspace"
)

// Bounds of a compose request.
const (
	composeMaxQuestion    = assist.MaxQuestionChars
	composeMaxDescription = assist.MaxPostingTextChars
	composeMaxCurrent     = assist.MaxCurrentChars
	composeMaxInstruction = assist.MaxInstructionChars
)

func (s *Server) composeService() (*assist.Service, error) {
	d, err := s.dom()
	if err != nil {
		return nil, err
	}
	return d.assist, nil
}

// composeSources reads the user's own facts for a draft: the bank's entries for
// the host, the profile and the CV, with the CV and profile evidence lines the
// documents rest on (bind.ComposeSources). A missing or unreadable file
// contributes nothing; the draft is then judged on the facts that remain.
func (s *Server) composeSources(host string) (assist.Sources, error) {
	bank, err := s.answerBank()
	if err != nil {
		return assist.Sources{}, err
	}
	var entries []match.Entry
	if l, err := bank.Bank(); err == nil {
		entries = l.Bank.EntriesForHost(host)
	}
	var prof workspace.Profile
	if c, err := s.cfg.Workspace.Read(workspace.FileProfile); err == nil && c.Revision != workspace.RevisionAbsent {
		prof, _ = workspace.ParseProfile(c.Data)
	}
	var cv string
	if c, err := s.cfg.Workspace.Read(workspace.FileCV); err == nil && c.Revision != workspace.RevisionAbsent {
		cv = string(c.Data)
	}
	return bind.ComposeSources(prof, cv, entries), nil
}

// reviewSuggestion is the learning service's review hook: it sorts one waiting
// suggestion and drafts the answer of a reusable one from the user's own facts
// (assist.Service.ReviewSuggestion), in the stored grounding mode except
// dangerous, which only a supervised form fill may use.
func (s *Server) reviewSuggestion(ctx context.Context, c learn.Candidate) (learn.Review, error) {
	d, err := s.dom()
	if err != nil {
		return learn.Review{}, err
	}
	host := ""
	if len(c.Hosts) > 0 {
		host = c.Hosts[len(c.Hosts)-1]
	}
	pcs, err := d.learn.CandidateContexts(ctx, []int64{c.ID})
	if err != nil {
		return learn.Review{}, err
	}
	src, err := s.composeSources(host)
	if err != nil {
		return learn.Review{}, err
	}
	mode := assist.Mode(d.bank.Settings(ctx).GroundingMode)
	if mode == assist.ModeDangerous {
		mode = assist.ModeFlex
	}
	pc := pcs[c.ID]
	y, m, day := s.now().Date()
	out, err := d.assist.ReviewSuggestion(ctx, assist.SuggestionIn{Question: c.SeedQuestion, Questions: c.Questions, PickOption: c.ProposedKind == "pick-option",
		Multiple: pc.Multiple, Options: pc.Options, Sections: pc.Sections, Postings: pc.Postings, Host: host, Answered: c.ProposedAnswer != "" || c.LongForm,
		Mode: mode, Sources: src, Today: match.Date{Year: y, Month: int(m), Day: day}})
	if err != nil {
		return learn.Review{}, err
	}
	return learn.Review{Verdict: out.Verdict, Reason: out.Reason, Draft: out.Draft, Cited: out.Cited}, nil
}

func (s *Server) answersCompose(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.composeService()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.AnswerComposeRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	switch {
	case strings.TrimSpace(req.Question) == "":
		return apikit.Reply{}, apikit.NewErr(wire.CodeValidationFailed, "question is required")
	case len([]rune(req.Question)) > composeMaxQuestion:
		return apikit.Reply{}, apikit.NewErr(wire.CodeValidationFailed, "question is longer than %d characters", composeMaxQuestion)
	case len([]rune(req.Posting.Description)) > composeMaxDescription:
		return apikit.Reply{}, apikit.NewErr(wire.CodeValidationFailed, "posting.description is longer than %d characters", composeMaxDescription)
	case len([]rune(req.Current)) > composeMaxCurrent:
		return apikit.Reply{}, apikit.NewErr(wire.CodeValidationFailed, "current is longer than %d characters", composeMaxCurrent)
	case len([]rune(req.Instruction)) > composeMaxInstruction:
		return apikit.Reply{}, apikit.NewErr(wire.CodeValidationFailed, "instruction is longer than %d characters", composeMaxInstruction)
	case req.MaxLength < 0:
		return apikit.Reply{}, apikit.NewErr(wire.CodeValidationFailed, "maxLength must not be negative")
	case strings.TrimSpace(req.Instruction) != "" && strings.TrimSpace(req.Current) == "":
		return apikit.Reply{}, apikit.NewErr(wire.CodeValidationFailed, "an instruction needs the current text to rewrite")
	}
	src, err := s.composeSources(req.Host)
	if err != nil {
		return apikit.Reply{}, err
	}
	bank, err := s.answerBank()
	if err != nil {
		return apikit.Reply{}, err
	}
	// A supervised draft may use the stored mode, dangerous included.
	mode := assist.Mode(bank.Settings(c.R.Context()).GroundingMode)
	y, m, d := s.now().Date()
	out := svc.Compose(c.R.Context(), assist.ComposeRequest{
		Question: req.Question, Host: req.Host, MaxLength: req.MaxLength,
		Company: req.Posting.Company, Role: req.Posting.Role, JD: req.Posting.Description,
		ApplicationURL: req.Posting.ApplicationURL, JobURL: req.Posting.JobURL,
		Current: req.Current, Instruction: req.Instruction,
		Mode: mode, Sources: src, Today: match.Date{Year: y, Month: int(m), Day: d},
	})
	if !out.Filled {
		return apikit.Reply{Data: wire.AnswerComposeResult{Outcome: wire.ComposeAbstained, Sources: []string{}, Reason: string(out.Reason)}}, nil
	}
	return apikit.Reply{Data: wire.AnswerComposeResult{Outcome: wire.ComposeFilled, Answer: out.Answer, Sources: apikit.NonNil(out.Cited)}}, nil
}
