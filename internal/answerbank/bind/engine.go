package bind

import (
	"context"
	"fmt"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/engine"
)

var _ engine.Matcher = (*Binding)(nil)

// Match answers the in-page engine's questions. A question that arrives with no
// section path gets the one its control name states.
func (b *Binding) Match(ctx context.Context, req engine.MatchRequest) (engine.MatchResponse, error) {
	out, err := b.Run(ctx, requestOf(req))
	if err != nil {
		return engine.MatchResponse{}, err
	}
	resp := engine.MatchResponse{Results: make([]engine.Resolution, len(out.Results))}
	for i, res := range out.Results {
		resp.Results[i] = resolutionFor(res, out.Own[i])
	}
	return resp, nil
}

// Pinned resolves each question of req against the bank entry named at the
// same index, with the matcher's own guards and binding, as if classification
// had chosen it (match.Matcher.MatchPinned). It is the matcher's verdict on an
// entry another lane proposed: no model lane runs and nothing is recorded.
func (b *Binding) Pinned(ctx context.Context, req engine.MatchRequest, entryIDs []string) ([]engine.Resolution, error) {
	if len(entryIDs) != len(req.Questions) {
		return nil, fmt.Errorf("bind: %d entry ids for %d questions", len(entryIDs), len(req.Questions))
	}
	in, err := b.build(ctx, requestOf(req))
	if err != nil {
		return nil, err
	}
	m := match.New(in.bank, match.DefaultStages())
	out := make([]engine.Resolution, len(entryIDs))
	for i, id := range entryIDs {
		r, err := m.MatchPinned(ctx, in.req, in.req.Questions[i], id)
		if err != nil {
			return nil, err
		}
		out[i] = resolutionFor(r, in.bank.OwnAnswer(r))
	}
	return out, nil
}

// OwnSearch reads, for each question of req about the user's own experience
// (match.OwnQuestion), what the user's information says about it: the terms it
// asks about, those some of it names and whether all of it was searched
// (F-10). Any other question gets an empty search, which answers nothing.
// Nothing is recorded.
func (b *Binding) OwnSearch(ctx context.Context, req engine.MatchRequest) ([]match.OwnSearch, error) {
	in, err := b.build(ctx, requestOf(req))
	if err != nil {
		return nil, err
	}
	srcs := match.NewOwnSources(in.req.Facts, in.bank.EntriesForHost(req.Host))
	out := make([]match.OwnSearch, len(in.req.Questions))
	for i, q := range in.req.Questions {
		if t := match.Sanitize(q.Text, q.Options); match.OwnQuestion(t) {
			out[i] = srcs.Mentions(t, t)
		}
	}
	return out, nil
}

func requestOf(req engine.MatchRequest) Request {
	r := Request{
		RunID: req.RunID, RecordAs: req.RecordRun, Host: req.Host,
		Posting: Posting{Company: req.Posting.Company, Title: req.Posting.Title, Description: req.Posting.JDText,
			Location: req.Posting.Location, URL: req.Posting.URL},
		Questions: make([]match.Question, len(req.Questions)),
	}
	if req.CoverLetterAttached {
		r.CoverLetterAttached = ptr(true)
	}
	if req.ResumeAttached {
		r.ResumeAttached = ptr(true)
	}
	for i, q := range req.Questions {
		r.Questions[i] = questionFromWire(q)
	}
	return r
}

func ptr[T any](v T) *T { return &v }

// questionFromWire reads one engine question. The engine's email and tel input
// types are not in the matcher's closed set; they are single-line text there,
// and inferring them from the wording would misread "Email address" as an
// address. Any other type the matcher does not know is left to inference.
//
// The engine calls a choice control yes/no only when its two labels are exactly
// Yes and No. "Yes, I need visa sponsorship" / "No, I do not need visa
// sponsorship" arrives as single_select, and a bank entry that takes yes_no
// would then refuse a question it can answer. A single_select on a choice
// control is therefore left to the matcher's own reading of its labels
// (WireInputType).
func questionFromWire(q engine.WireQuestion) match.Question {
	out := match.Question{Selector: q.Selector, Text: q.Text, SectionPath: q.Section, Kind: match.ControlKind(q.Kind),
		Required: q.Required, MaxLength: q.MaxLength, Description: q.Description}
	switch t := match.InputType(q.InputType); {
	case t.Valid():
		out.InputType = WireInputType(out.Kind, t)
	case q.InputType == "email" || q.InputType == "tel":
		out.InputType = match.InputText
	}
	if len(out.SectionPath) == 0 {
		out.SectionPath = SectionPath(q.Selector)
	}
	for _, o := range q.Options {
		out.Options = append(out.Options, match.Option{Value: o, Label: o})
	}
	return out
}

// WireInputType is the input type the matcher takes for a control whose page
// scan reported t. A single_select on a select, radio group or custom listbox is
// dropped, so the matcher reads the labels itself and finds a sentence-spelled
// Yes/No pair; every other type is the scan's own.
func WireInputType(kind match.ControlKind, t match.InputType) match.InputType {
	if t == match.InputSingleSelect && (kind == match.KindSelect || kind == match.KindRadioGroup || kind == match.KindCustomListbox) {
		return ""
	}
	return t
}

// resolutionFor maps a result onto the engine's wire. The reason travels as the
// matcher's own, so the summary shows why a control stayed blank.
func resolutionFor(r match.Result, own bool) engine.Resolution {
	return engine.Resolution{
		Selector: r.Selector, Outcome: string(r.Outcome), Value: r.Answer,
		OptionLabel: r.ChosenLabel, OptionLabels: r.ChosenLabels, Alternatives: r.Alternatives,
		Reason: string(r.Reason), Terminal: r.Terminal, ClientMayOverwrite: ClientMayOverwrite(r), OwnAnswer: own,
	}
}
