package documents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
)

// Source is the stored text of one document of a version: the structured
// content the user edits and the readable rendering of it.
type Source struct {
	Document Document
	Resume   *ResumeDraft
	Letter   *LetterDraft
	// Text is the Markdown rendering stored with the version.
	Text string
	// Evidence is each manifest line the content cites, in manifest order;
	// empty when the stored manifest is not the version's.
	Evidence []Evidence
}

// Source reads the text of a version. The structured content must still hash
// to what the row names: a file changed behind the service is a conflict, not
// text to edit.
func (s *Service) Source(ctx context.Context, id int64) (Source, error) {
	d, err := s.Get(ctx, id)
	if err != nil {
		return Source{}, err
	}
	dir, err := s.versionDir(d.Path)
	if err != nil {
		return Source{}, err
	}
	raw, err := readRegular(filepath.Join(dir, string(d.Kind)+".json"), 4<<20)
	if err != nil {
		return Source{}, fmt.Errorf("%w: the %s source of version %d is not on disk", ErrNotFound, d.Kind, d.Version)
	}
	if sha(raw) != d.ContentHash {
		return Source{}, fmt.Errorf("%w: the stored %s changed after it was written", ErrConflict, d.Kind)
	}
	md, err := readRegular(filepath.Join(dir, string(d.Kind)+".md"), 4<<20)
	if err != nil {
		return Source{}, fmt.Errorf("%w: the %s text of version %d is not on disk", ErrNotFound, d.Kind, d.Version)
	}
	out := Source{Document: d, Text: string(md)}
	var cited []string
	switch d.Kind {
	case KindResume:
		var rd ResumeDoc
		if err := json.Unmarshal(raw, &rd); err != nil {
			return Source{}, fmt.Errorf("%w: the stored résumé cannot be read", ErrInvalid)
		}
		out.Resume = &rd.ResumeDraft
		cited = rd.ProfileEvidence
		for _, c := range rd.Competencies {
			cited = append(cited, c.Evidence...)
		}
		for _, e := range slices.Concat(rd.Experience, rd.Projects) {
			for _, b := range e.Bullets {
				cited = append(cited, b.Evidence...)
			}
		}
	case KindCover:
		var ld LetterDoc
		if err := json.Unmarshal(raw, &ld); err != nil {
			return Source{}, fmt.Errorf("%w: the stored letter cannot be read", ErrInvalid)
		}
		out.Letter = &ld.LetterDraft
		cited = ld.OpeningEvidence
		for _, b := range ld.Bullets {
			cited = append(cited, b.Evidence...)
		}
	}
	var m Manifest
	if raw, err := readRegular(filepath.Join(dir, "evidence-manifest.json"), 4<<20); err == nil && json.Unmarshal(raw, &m) == nil && m.Hash == d.Manifest {
		for _, e := range m.Evidence {
			if slices.Contains(cited, e.ID) {
				out.Evidence = append(out.Evidence, e)
			}
		}
	}
	return out, nil
}

// prepareEdit builds the version an edit would publish: the stored one with
// one document's text replaced. The text goes through the grounding gate that
// checked the draft, so an edit never types a fact the user's sources do not
// hold. A version attached to a submitted application is refused, and so are
// a version a newer one replaced and a text identical to the stored one.
func (s *Service) prepareEdit(ctx context.Context, req Request) (*version, Document, error) {
	d, err := s.Get(ctx, req.DocumentID)
	if err != nil {
		return nil, Document{}, err
	}
	if d.FrozenAt != "" {
		return nil, d, ErrFrozen
	}
	if !d.Current {
		// An edit publishes the newest version, so editing an older one would drop the later text.
		return nil, d, fmt.Errorf("%w: a newer version of this %s exists; edit that one", ErrConflict, d.Kind)
	}
	v, _, err := s.loadVersion(ctx, d, req)
	if err != nil {
		return nil, d, err
	}
	v.repaired = map[Kind]bool{d.Kind: true}
	// The review judged the text that was drafted, not this one.
	delete(v.carried, "document-review.json")
	switch {
	case req.Resume != nil && d.Kind == KindResume:
		rd := *v.resume
		stored := v.resume.ResumeDraft
		rd.ResumeDraft = normalizeResume(*req.Resume)
		if req.ProposalID > 0 {
			rd.Generation = GenModel
		}
		if sameContent(rd.ResumeDraft, stored) {
			return nil, d, invalid("resume", "the text is the same as version %d", d.Version)
		}
		if v.resumeGate = checkResume(v.m, rd); !v.resumeGate.OK() {
			return nil, d, fmt.Errorf("%w: %s", ErrNoValidDraft, gateDetail(v.resumeGate))
		}
		v.resume = &rd
	case req.Letter != nil && d.Kind == KindCover:
		ld := *v.letter
		stored := v.letter.LetterDraft
		ld.LetterDraft = normalizeLetter(*req.Letter)
		if req.ProposalID > 0 {
			ld.Generation, ld.VoiceSHA256 = GenModel, req.Voice
		}
		if sameContent(ld.LetterDraft, stored) {
			return nil, d, invalid("letter", "the text is the same as version %d", d.Version)
		}
		if v.letterGate = checkLetter(v.m, ld); !v.letterGate.OK() {
			return nil, d, fmt.Errorf("%w: %s", ErrNoValidDraft, gateDetail(v.letterGate))
		}
		v.letter = &ld
	default:
		return nil, d, invalid("content", "send the text of the %s this id names", d.Kind)
	}
	return v, d, nil
}

// runEdit publishes the edit as a new version in which the edited document has
// no PDF, so its checks read pending until a PDF run makes them; the other
// keeps its passed PDF (keptPDF). The version it started from, ready or not,
// is never touched.
func (j *job) runEdit(ctx context.Context) (Result, error) {
	if err := j.stage(ctx, StageManifest); err != nil {
		return Result{}, err
	}
	v, d, err := j.s.prepareEdit(ctx, j.req)
	if err != nil {
		return Result{}, err
	}
	j.req = v.req
	j.req.PDF = false
	if v.resume != nil {
		j.gen = v.resume.Generation
	}
	j.warnOn(d.Kind, "Edited from version %d. The PDF checks have not run on this text.", d.Version)
	return j.finishVersion(ctx, v)
}

// sameContent says whether an edit changed nothing. Lists compare as JSON
// with an empty list equal to an absent one, since a client sends [] where a
// stored file may hold null.
func sameContent(a, b any) bool {
	return bytes.Equal(canonical(a), canonical(b))
}

func canonical(v any) []byte {
	raw, _ := json.Marshal(v)
	var generic any
	_ = json.Unmarshal(raw, &generic)
	out, _ := json.Marshal(dropEmpty(generic))
	return out
}

func dropEmpty(v any) any {
	switch x := v.(type) {
	case []any:
		if len(x) == 0 {
			return nil
		}
		for i := range x {
			x[i] = dropEmpty(x[i])
		}
	case map[string]any:
		for k, e := range x {
			x[k] = dropEmpty(e)
		}
	}
	return v
}
