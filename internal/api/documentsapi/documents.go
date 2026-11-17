// Package documentsapi serves the document routes of the local API: résumé,
// cover letter and PDF runs, the versions and their sources, edits and
// refinement, the stored PDFs and the document preferences. internal/api
// registers the handlers.
package documentsapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/documents"
	"applysmith/internal/documents/assets"
	"applysmith/internal/documents/toolchain"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
)

// Deps is what the document handlers use from the server.
type Deps struct {
	Docs    func() (*documents.Service, error)
	Tracker func() (*tracker.Tracker, error) // the list says whether the row has a report
	DB      func() (*store.Store, error)     // a document run's row
	Notify  func(topics ...string)
}

// Handlers serves the document routes.
type Handlers struct{ Deps }

func New(d Deps) *Handlers { return &Handlers{Deps: d} }

// toolchainUnavailable says why no PDF can be made: the components missing, or,
// with every one present, that they have not passed the render test for this
// combination of tools and templates (one of them changed), which the smoke runs.
func toolchainUnavailable(r toolchain.Report) string {
	if len(r.Missing) == 0 {
		return "the PDF tools are installed but have not passed their render test since a tool or template changed; run it from Setup, Résumé PDFs, or with `applysmith documents toolchain smoke`"
	}
	return "PDF generation needs: " + strings.Join(r.Missing, ", ") + "; run `applysmith documents toolchain check`"
}

// DocumentError maps a document-service failure onto the API errors.
func DocumentError(err error) error {
	var ve *documents.ValidationError
	var floor *documents.BelowFloorError
	var unavailable *toolchain.UnavailableError
	var active *documents.RunActiveError
	switch {
	case errors.As(err, &ve):
		return apikit.InvalidField(ve.Field, "%s", ve.Msg)
	case errors.As(err, &floor):
		e := apikit.NewErr(wire.CodeValidationFailed, "%s", floor.Error())
		// Without a report there is no floor decision to override.
		if floor.Report != 0 {
			e.Details = &wire.ErrorDetails{Reason: wire.ReasonBelowScoreFloor}
		}
		return e
	case errors.As(err, &unavailable):
		e := apikit.Unavailable(toolchainUnavailable(unavailable.Report))
		e.Details = &wire.ErrorDetails{Reason: wire.ReasonToolchainUnavailable}
		e.Retryable = false
		return e
	case errors.Is(err, documents.ErrProposalPending):
		e := apikit.NewErr(wire.CodeConflict, "%s", err.Error())
		e.Details = &wire.ErrorDetails{Reason: wire.ReasonProposalPending}
		return e
	case errors.Is(err, documents.ErrNoModel):
		return apikit.Unavailable("revising a document needs a model; configure one in Settings")
	case errors.Is(err, documents.ErrNoPosting):
		return apikit.InvalidField("posting", "a tailored run needs the job description; send it as posting or evaluate the posting first")
	case errors.Is(err, documents.ErrBaselineCover):
		return apikit.InvalidField("baseline", "%s", err.Error())
	case errors.Is(err, documents.ErrNotFound), errors.Is(err, store.ErrNotFound):
		return apikit.NewErr(wire.CodeNotFound, "%s", err.Error())
	case errors.Is(err, documents.ErrQueueFull):
		e := apikit.Unavailable("the document queue is full; retry shortly")
		e.Details = &wire.ErrorDetails{Reason: wire.ReasonQueueFull}
		return e
	case errors.As(err, &active):
		e := apikit.NewErr(wire.CodeConflict, "a document for this application is still being written; wait for that run to end")
		e.Details = &wire.ErrorDetails{Reason: wire.ReasonDocumentRunActive, RunID: active.RunID}
		return e
	case errors.Is(err, documents.ErrFrozen), errors.Is(err, documents.ErrConflict), errors.Is(err, store.ErrConflict):
		return apikit.NewErr(wire.CodeConflict, "%s", err.Error())
	case errors.Is(err, documents.ErrInvalid), errors.Is(err, documents.ErrNoValidDraft):
		return apikit.NewErr(wire.CodeValidationFailed, "%s", err.Error())
	}
	return err
}

func documentActor(p apikit.Principal) tracker.Actor { return tracker.ActorUser }

// Submit queues a document run for the caller and answers its run id.
func (s *Handlers) Submit(c *apikit.Call, req documents.Request) (apikit.Reply, error) {
	key, e := apikit.IdempotencyKey(c)
	if e != nil {
		return apikit.Reply{}, e
	}
	svc, err := s.Docs()
	if err != nil {
		return apikit.Reply{}, err
	}
	req.Actor = documentActor(c.P)
	h, err := svc.Submit(c.R.Context(), documents.Claim{Principal: c.P.ID, Key: key}, req)
	if err != nil {
		return apikit.Reply{}, DocumentError(err)
	}
	if !h.Replayed {
		s.Notify("runs")
	}
	return apikit.Reply{Status: http.StatusAccepted, Location: "/api/runs/" + h.RunID, Data: wire.RunAccepted{RunID: h.RunID}}, nil
}

func (s *Handlers) CreateResume(c *apikit.Call) (apikit.Reply, error) {
	var w wire.DocumentResumeRequest
	if e := apikit.Decode(c.Body, &w); e != nil {
		return apikit.Reply{}, e
	}
	req := documents.Request{Action: documents.ActionResume, Mode: documents.ModeTailored, CandidacyID: int64(w.CandidacyID),
		Posting: w.Posting, PDF: w.PDF, Options: documents.Options{Language: w.Language, Paper: assets.Paper(w.Paper), Pages: w.Pages}}
	switch {
	case w.Baseline && w.CandidacyID != 0:
		return apikit.Reply{}, apikit.InvalidField("baseline", "baseline and candidacyId are mutually exclusive")
	case w.Baseline && w.IncludeCoverLetter:
		return apikit.Reply{}, apikit.InvalidField("includeCoverLetter", "a baseline résumé has no company-specific letter")
	case w.Baseline:
		req.Mode = documents.ModeBaseline
	case w.CandidacyID < 1:
		return apikit.Reply{}, apikit.InvalidField("candidacyId", "send candidacyId, or baseline for a baseline résumé")
	}
	if w.IncludeCoverLetter {
		req.Action = documents.ActionPair
	}
	return s.Submit(c, req)
}

func (s *Handlers) CreateCover(c *apikit.Call) (apikit.Reply, error) {
	var w wire.DocumentCoverRequest
	if e := apikit.Decode(c.Body, &w); e != nil {
		return apikit.Reply{}, e
	}
	if w.CandidacyID < 1 {
		return apikit.Reply{}, apikit.InvalidField("candidacyId", "a cover letter needs a candidacy")
	}
	return s.Submit(c, documents.Request{Action: documents.ActionCover, Mode: documents.ModeTailored, CandidacyID: int64(w.CandidacyID),
		Posting: w.Posting, PDF: w.PDF, Options: documents.Options{Language: w.Language, Paper: assets.Paper(w.Paper)}})
}

func (s *Handlers) CreatePDF(c *apikit.Call) (apikit.Reply, error) {
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	var w wire.DocumentPDFRequest
	if e := apikit.Decode(c.Body, &w); e != nil {
		return apikit.Reply{}, e
	}
	return s.Submit(c, documents.Request{Action: documents.ActionPDF, DocumentID: id})
}

func WireDocument(d documents.Document) wire.DocumentView {
	v := wire.DocumentView{ID: apikit.Safe64(d.ID), Bundle: d.Bundle, Version: d.Version, Kind: string(d.Kind), Mode: string(d.Mode), Path: d.Path,
		SourcePath: d.SourcePath, PDFPath: d.PDFPath, PDFState: d.PDFState, Pages: d.Pages, Paper: d.Paper, PageLimit: d.PageLimit,
		Language: d.Language, Generation: d.Generation, RefinePending: d.RefinePending, Warnings: apikit.NonNil(d.Warnings), ContentHash: d.ContentHash,
		PDFSHA256: d.PDFSHA256, ManifestHash: d.Manifest, Current: d.Current, Ready: d.Ready, Frozen: d.FrozenAt != "", CreatedAt: apikit.Stamp(d.CreatedAt)}
	if d.CandidacyID != nil {
		n := apikit.Safe64(*d.CandidacyID)
		v.CandidacyID = &n
	}
	return v
}

func WireDocumentVerification(v *documents.Verification) *wire.DocumentVerification {
	if v == nil {
		return nil
	}
	out := &wire.DocumentVerification{State: v.State, Stale: v.Stale, Pages: v.Pages, PDFSHA256: v.PDFSHA256, TeXSHA256: v.TeXSHA256,
		SourceSHA256: v.SourceSHA256, ManifestHash: v.ManifestHash, Template: v.Template,
		Verifiers: wire.DocumentVerifiers{Rules: v.Verifiers.Rules, PDFScript: v.Verifiers.PDFScript, LayoutScript: v.Verifiers.LayoutScript,
			LayoutProfile: v.Verifiers.LayoutProfile, Python: v.Verifiers.Python, Pypdf: v.Verifiers.Pypdf, Poppler: v.Verifiers.Poppler},
		Checks: make([]wire.DocumentCheck, 0, len(v.Checks)), Previews: make([]wire.DocumentPreview, 0, len(v.Previews)),
		VisualReview: wire.DocumentVisualReview{Status: v.VisualReview.Status, PDFSHA256: v.VisualReview.PDFSHA256}}
	for _, c := range v.Checks {
		out.Checks = append(out.Checks, wire.DocumentCheck{Tool: c.Tool, ID: c.ID, Status: c.Status, Reason: c.Reason})
	}
	for _, p := range v.Previews {
		out.Previews = append(out.Previews, wire.DocumentPreview{Page: p.Page, Name: p.Name, SHA256: p.SHA256})
	}
	return out
}

// DocumentList builds the list answer, reading the stored verdict of every
// version that holds a PDF.
func DocumentList(ctx context.Context, svc *documents.Service, docs []documents.Document) (wire.DocumentList, error) {
	out := wire.DocumentList{Documents: make([]wire.DocumentView, 0, len(docs))}
	for _, d := range docs {
		v := WireDocument(d)
		ver, err := svc.Verification(ctx, d)
		if err != nil {
			return wire.DocumentList{}, err
		}
		v.Verification = WireDocumentVerification(ver)
		v.SourcesChanged = svc.SourcesChanged(ctx, d)
		rev, err := svc.Review(ctx, d)
		if err != nil {
			return wire.DocumentList{}, err
		}
		if rev != nil {
			v.Review = &wire.DocumentReview{Status: rev.Status, Reason: rev.Reason, Revisions: rev.Revisions, Factual: rev.Factual, Narrative: rev.Narrative, Rejected: rev.Rejected}
		}
		out.Documents = append(out.Documents, v)
	}
	return out, nil
}

func (s *Handlers) List(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Docs()
	if err != nil {
		return apikit.Reply{}, err
	}
	var candidacy int64
	if raw := strings.TrimSpace(c.R.URL.Query().Get("candidacy")); raw != "" {
		n, perr := strconv.ParseInt(raw, 10, 64)
		if perr != nil || n < 1 || n > wire.MaxSafeInt {
			return apikit.Reply{}, apikit.InvalidField("candidacy", "candidacy must be a positive whole number")
		}
		candidacy = n
	}
	docs, err := svc.List(c.R.Context(), candidacy)
	if err != nil {
		return apikit.Reply{}, DocumentError(err)
	}
	list, err := DocumentList(c.R.Context(), svc, docs)
	if err != nil {
		return apikit.Reply{}, DocumentError(err)
	}
	if candidacy > 0 {
		if list.ActiveRunID, err = svc.ActiveRun(c.R.Context(), candidacy, false); err != nil {
			return apikit.Reply{}, err
		}
		tr, err := s.Tracker()
		if err != nil {
			return apikit.Reply{}, err
		}
		if det, err := tr.Get(c.R.Context(), candidacy); err == nil {
			list.NoReport = det.Entry.ReportNumber == nil
		}
		// A report under the floor is said before a run is asked for, so the
		// override is offered up front.
		var floor *documents.BelowFloorError
		if err := svc.Eligible(c.R.Context(), candidacy); errors.As(err, &floor) && floor.Report != 0 {
			list.BelowFloor = true
		}
	}
	return apikit.Reply{Data: list}, nil
}

func (s *Handlers) Delete(c *apikit.Call) (apikit.Reply, error) {
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	svc, err := s.Docs()
	if err != nil {
		return apikit.Reply{}, err
	}
	d, err := svc.Get(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, DocumentError(err)
	}
	if err := svc.Delete(c.R.Context(), id, c.R.URL.Query().Get("submitted") == "confirm"); err != nil {
		return apikit.Reply{}, DocumentError(err)
	}
	var candidacy int64
	if d.CandidacyID != nil {
		candidacy = *d.CandidacyID
	}
	left, err := svc.List(c.R.Context(), candidacy)
	if err != nil {
		return apikit.Reply{}, DocumentError(err)
	}
	list, err := DocumentList(c.R.Context(), svc, left)
	if err != nil {
		return apikit.Reply{}, DocumentError(err)
	}
	s.Notify("tracker")
	return apikit.Reply{Data: list}, nil
}

func (s *Handlers) GetRun(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Docs()
	if err != nil {
		return apikit.Reply{}, err
	}
	st, err := s.DB()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("id")
	run, err := st.GetRun(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, err
	}
	if run.Kind != "documents" {
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no such document run")
	}
	res, err := svc.Outcome(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, DocumentError(err)
	}
	view := WireDocumentRun(run, res)
	// The run row ends before the service forgets the run's deadline, so an
	// ended run must not read the deadline in that window.
	if left, ok := svc.Remaining(id); ok && view.Status == wire.RunRunning {
		view.RemainingSeconds = int(left.Seconds())
	}
	return apikit.Reply{Data: view}, nil
}

func WireDocumentRun(run store.Run, res documents.Result) wire.DocumentRunView {
	v := wire.DocumentRunView{RunID: run.ID, Status: run.Status, Stage: run.Stage, Reason: res.Reason, Generation: string(res.Generation),
		Documents: make([]wire.SafeInt, 0, len(res.Documents)), Warnings: apikit.NonNil(res.Warnings),
		Retrieval: wire.DocumentRetrieval{Query: res.Retrieval.Query, Retrieved: res.Retrieval.Retrieved, Items: make([]wire.DocumentRetrievalItem, 0, len(res.Retrieval.Items))}}
	for _, id := range res.Documents {
		v.Documents = append(v.Documents, apikit.Safe64(id))
	}
	if res.Pack != nil {
		v.Pack = &wire.InterviewPackRef{CandidacyID: apikit.Safe64(res.Pack.Candidacy), Stage: res.Pack.Stage, Revision: res.Pack.Revision}
	}
	for _, it := range res.Retrieval.Items {
		v.Retrieval.Items = append(v.Retrieval.Items, wire.DocumentRetrievalItem{Source: it.Source, Score: it.Score, Reasons: apikit.NonNil(it.Reasons)})
	}
	return v
}

func toPreferences(p documents.Preferences) wire.DocumentPreferences {
	return wire.DocumentPreferences{Language: p.Language, Paper: string(p.Paper), ResumePages: p.ResumePages, CoverWhenAsked: p.CoverWhenAsked, Revision: p.Revision}
}

func (s *Handlers) GetPreferences(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Docs()
	if err != nil {
		return apikit.Reply{}, err
	}
	p, err := svc.Preferences(c.R.Context())
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: toPreferences(p)}, nil
}

func (s *Handlers) PatchPreferences(c *apikit.Call) (apikit.Reply, error) {
	var w wire.DocumentPreferencesPatch
	if e := apikit.Decode(c.Body, &w); e != nil {
		return apikit.Reply{}, e
	}
	if w.Revision < 1 {
		return apikit.Reply{}, apikit.InvalidField("revision", "revision is the revision the edit was read at")
	}
	svc, err := s.Docs()
	if err != nil {
		return apikit.Reply{}, err
	}
	p, err := svc.SetPreferences(c.R.Context(), documents.PreferencePatch{Language: w.Language, Paper: w.Paper, ResumePages: w.ResumePages, CoverWhenAsked: w.CoverWhenAsked, Revision: w.Revision})
	if err != nil {
		return apikit.Reply{}, DocumentError(err)
	}
	return apikit.Reply{Data: toPreferences(p)}, nil
}
