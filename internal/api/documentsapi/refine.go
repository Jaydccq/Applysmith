package documentsapi

import (
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/documents"
)

func toProposal(p documents.Proposal) *wire.DocumentProposal {
	out := &wire.DocumentProposal{ID: apikit.Safe64(p.ID), DocumentID: apikit.Safe64(p.DocumentID), Kind: string(p.Kind), Instruction: p.Instruction,
		Text: p.Text, Diff: make([]wire.DocumentDiffLine, 0, len(p.Diff)), CreatedAt: apikit.Stamp(p.CreatedAt)}
	for _, l := range p.Diff {
		out.Diff = append(out.Diff, wire.DocumentDiffLine{Op: l.Op, Text: l.Text})
	}
	if p.Resume != nil {
		out.Resume = toResumeContent(*p.Resume)
	}
	if p.Letter != nil {
		out.Letter = toLetterContent(*p.Letter)
	}
	return out
}

func toRefinement(r documents.Refinement) wire.DocumentRefinement {
	out := wire.DocumentRefinement{Turns: make([]wire.DocumentRefineTurn, 0, len(r.Turns))}
	if r.Pending != nil {
		out.Pending = toProposal(*r.Pending)
	}
	for _, t := range r.Turns {
		out.Turns = append(out.Turns, wire.DocumentRefineTurn{Kind: t.Kind, Text: t.Text, Decision: t.Decision})
	}
	return out
}

func (s *Handlers) Refine(c *apikit.Call) (apikit.Reply, error) {
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	var w wire.DocumentRefineRequest
	if e := apikit.Decode(c.Body, &w); e != nil {
		return apikit.Reply{}, e
	}
	req := documents.Request{Action: documents.ActionRefine, DocumentID: id, Instruction: w.Instruction}
	for _, t := range w.Turns {
		req.Turns = append(req.Turns, documents.Turn{Kind: t.Kind, Text: t.Text, Decision: t.Decision})
	}
	return s.Submit(c, req)
}

func (s *Handlers) GetRefinement(c *apikit.Call) (apikit.Reply, error) {
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	svc, err := s.Docs()
	if err != nil {
		return apikit.Reply{}, err
	}
	r, err := svc.Refinement(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, DocumentError(err)
	}
	return apikit.Reply{Data: toRefinement(r)}, nil
}

// AcceptRefinement publishes the proposal as a new version through the
// run that stores a user's edit, so it passes the same checks and the queue.
func (s *Handlers) AcceptRefinement(c *apikit.Call) (apikit.Reply, error) {
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	var w wire.DocumentRefineDecision
	if e := apikit.Decode(c.Body, &w); e != nil {
		return apikit.Reply{}, e
	}
	if w.ProposalID < 1 {
		return apikit.Reply{}, apikit.InvalidField("proposalId", "send the id of the pending proposal")
	}
	return s.Submit(c, documents.Request{Action: documents.ActionEdit, DocumentID: id, ProposalID: int64(w.ProposalID)})
}

func (s *Handlers) DiscardRefinement(c *apikit.Call) (apikit.Reply, error) {
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	var w wire.DocumentRefineDecision
	if e := apikit.Decode(c.Body, &w); e != nil {
		return apikit.Reply{}, e
	}
	if w.ProposalID < 1 {
		return apikit.Reply{}, apikit.InvalidField("proposalId", "send the id of the pending proposal")
	}
	svc, err := s.Docs()
	if err != nil {
		return apikit.Reply{}, err
	}
	if err := svc.Discard(c.R.Context(), id, int64(w.ProposalID)); err != nil {
		return apikit.Reply{}, DocumentError(err)
	}
	r, err := svc.Refinement(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, DocumentError(err)
	}
	return apikit.Reply{Data: toRefinement(r)}, nil
}
