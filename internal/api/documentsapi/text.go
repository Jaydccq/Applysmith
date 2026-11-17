package documentsapi

import (
	"mime"
	"net/http"
	"strconv"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/documents"
)

func bulletsToWire(in []documents.Bullet) []wire.DocumentBullet {
	out := make([]wire.DocumentBullet, 0, len(in))
	for _, b := range in {
		out = append(out, wire.DocumentBullet{Text: b.Text, Evidence: apikit.NonNil(b.Evidence)})
	}
	return out
}

func bulletsFromWire(in []wire.DocumentBullet) []documents.Bullet {
	out := make([]documents.Bullet, 0, len(in))
	for _, b := range in {
		out = append(out, documents.Bullet{Text: b.Text, Evidence: b.Evidence})
	}
	return out
}

func entriesToWire(in []documents.EntryDraft) []wire.DocumentEntryContent {
	out := make([]wire.DocumentEntryContent, 0, len(in))
	for _, e := range in {
		out = append(out, wire.DocumentEntryContent{Entry: e.Entry, Bullets: bulletsToWire(e.Bullets)})
	}
	return out
}

func entriesFromWire(in []wire.DocumentEntryContent) []documents.EntryDraft {
	out := make([]documents.EntryDraft, 0, len(in))
	for _, e := range in {
		out = append(out, documents.EntryDraft{Entry: e.Entry, Bullets: bulletsFromWire(e.Bullets)})
	}
	return out
}

func toResumeContent(r documents.ResumeDraft) *wire.DocumentResumeContent {
	out := &wire.DocumentResumeContent{Profile: r.Profile, ProfileEvidence: apikit.NonNil(r.ProfileEvidence),
		Competencies: make([]wire.DocumentCompetency, 0, len(r.Competencies)), Experience: entriesToWire(r.Experience), Projects: entriesToWire(r.Projects)}
	for _, c := range r.Competencies {
		out.Competencies = append(out.Competencies, wire.DocumentCompetency{Label: c.Label, Text: c.Text, Evidence: apikit.NonNil(c.Evidence)})
	}
	return out
}

func fromResumeContent(w wire.DocumentResumeContent) documents.ResumeDraft {
	out := documents.ResumeDraft{Profile: w.Profile, ProfileEvidence: w.ProfileEvidence, Competencies: make([]documents.Competency, 0, len(w.Competencies)),
		Experience: entriesFromWire(w.Experience), Projects: entriesFromWire(w.Projects)}
	for _, c := range w.Competencies {
		out.Competencies = append(out.Competencies, documents.Competency{Label: c.Label, Text: c.Text, Evidence: c.Evidence})
	}
	return out
}

func toLetterContent(l documents.LetterDraft) *wire.DocumentLetterContent {
	return &wire.DocumentLetterContent{Opening: l.Opening, OpeningEvidence: apikit.NonNil(l.OpeningEvidence), Body: l.Body,
		Bullets: bulletsToWire(l.Bullets), Closing: l.Closing, CompanyFacts: apikit.NonNil(l.CompanyFacts)}
}

func fromLetterContent(w wire.DocumentLetterContent) documents.LetterDraft {
	return documents.LetterDraft{Opening: w.Opening, OpeningEvidence: w.OpeningEvidence, Body: w.Body,
		Bullets: bulletsFromWire(w.Bullets), Closing: w.Closing, CompanyFacts: w.CompanyFacts}
}

func (s *Handlers) GetSource(c *apikit.Call) (apikit.Reply, error) {
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	svc, err := s.Docs()
	if err != nil {
		return apikit.Reply{}, err
	}
	src, err := svc.Source(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, DocumentError(err)
	}
	d := src.Document
	out := wire.DocumentSource{ID: apikit.Safe64(d.ID), Kind: string(d.Kind), Version: d.Version, Frozen: d.FrozenAt != "", Text: src.Text, Evidence: []wire.DocumentEvidence{}}
	for _, e := range src.Evidence {
		out.Evidence = append(out.Evidence, wire.DocumentEvidence{ID: e.ID, Source: e.Source, Text: e.Text})
	}
	if src.Resume != nil {
		out.Resume = toResumeContent(*src.Resume)
	}
	if src.Letter != nil {
		out.Letter = toLetterContent(*src.Letter)
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Handlers) Edit(c *apikit.Call) (apikit.Reply, error) {
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	var w wire.DocumentEditRequest
	if e := apikit.Decode(c.Body, &w); e != nil {
		return apikit.Reply{}, e
	}
	req := documents.Request{Action: documents.ActionEdit, DocumentID: id}
	if w.Resume != nil {
		r := fromResumeContent(*w.Resume)
		req.Resume = &r
	}
	if w.Letter != nil {
		l := fromLetterContent(*w.Letter)
		req.Letter = &l
	}
	return s.Submit(c, req)
}

// GetPDF writes the stored PDF of a version for the preview. The
// bytes must still be the ones the row names; a PDF is not a JSON envelope.
func (s *Handlers) GetPDF(c *apikit.Call) (apikit.Reply, error) {
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
	data, err := svc.ReadPDF(c.R.Context(), d)
	if err != nil {
		return apikit.Reply{}, DocumentError(err)
	}
	h := c.W.Header()
	h.Set("Content-Type", "application/pdf")
	h.Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": string(d.Kind) + "-v" + strconv.Itoa(d.Version) + ".pdf"}))
	h.Set("Content-Length", strconv.Itoa(len(data)))
	c.W.WriteHeader(http.StatusOK)
	_, _ = c.W.Write(data)
	c.Handled = true
	return apikit.Reply{}, nil
}
