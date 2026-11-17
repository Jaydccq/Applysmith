package documents

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"applysmith/internal/documents/assets"
)

// carriedFiles are the files of a version a PDF run copies forward unchanged.
// The list is fixed, so no other file of the directory is ever read.
var carriedFiles = []string{
	"posting.md", "company-research.md", "success-profile.md", "application-strategy.md", "resume-selection-manifest.md",
	"resume-materials.md", "evidence-manifest.json", "document-review.json", "resume.json", "resume.md", "resume-quality-gate.md",
	"cover-letter.json", "cover-letter.md", "cover-letter-quality-gate.md",
}

// runPDF makes the PDFs of an existing version; a document whose passed PDF
// the version holds keeps it rather than being built again. The structured
// sources are read back, checked against their stored hashes and gated again,
// and the result is a new version: the version it started from never changes.
func (j *job) runPDF(ctx context.Context) (Result, error) {
	s := j.s
	if err := j.stage(ctx, StageManifest); err != nil {
		return Result{}, err
	}
	d, err := s.Get(ctx, j.req.DocumentID)
	if err != nil {
		return Result{}, err
	}
	if err := pdfable(d); err != nil {
		return Result{}, err
	}
	if s.refinePending(ctx, d.ID) {
		return Result{}, ErrProposalPending
	}
	if d.CandidacyID != nil {
		if err := s.Eligible(ctx, *d.CandidacyID); err != nil {
			return Result{}, err
		}
	}
	v, gen, err := s.loadVersion(ctx, d, j.req)
	if err != nil {
		return Result{}, err
	}
	j.req = v.req
	if gen != "" {
		j.gen = gen
	}
	j.req.PDF = true
	return j.finishVersion(ctx, v)
}

// pdfable refuses the PDF run of a version a newer one replaced: the run
// publishes the newest version, so it would bring back the older text.
func pdfable(d Document) error {
	if !d.Current {
		return fmt.Errorf("%w: a newer version of this %s exists; make the PDF of that one", ErrConflict, d.Kind)
	}
	return nil
}

// loadVersion reads a stored version back into memory: the carried files, the
// evidence manifest checked against its hash, and each structured source
// checked against its stored hash and gated again. The version it describes
// never changes. The returned generation is the résumé's, or empty when the
// version holds no résumé.
func (s *Service) loadVersion(ctx context.Context, d Document, base Request) (*version, Generation, error) {
	var gen Generation
	docs, err := s.versionOf(ctx, d)
	if err != nil {
		return nil, "", err
	}
	dir, err := s.versionDir(d.Path)
	if err != nil {
		return nil, "", err
	}
	carried := map[string][]byte{}
	for _, n := range carriedFiles {
		if b, err := readRegular(filepath.Join(dir, n), 4<<20); err == nil {
			carried[n] = b
		} else if !os.IsNotExist(err) {
			return nil, "", err
		}
	}
	var m Manifest
	if err := json.Unmarshal(carried["evidence-manifest.json"], &m); err != nil {
		return nil, "", fmt.Errorf("%w: the stored manifest cannot be read", ErrInvalid)
	}
	if err := m.Verify(); err != nil {
		return nil, "", err
	}
	if m.Hash != d.Manifest {
		return nil, "", fmt.Errorf("%w: the stored manifest is not the one this version was written for", ErrConflict)
	}
	v := &version{m: m, req: base, lang: d.Language, paper: assets.Paper(d.Paper), pages: d.PageLimit, carried: carried, bundle: d.Bundle}
	v.req.Mode, v.req.Language, v.req.Paper, v.req.Pages = d.Mode, d.Language, assets.Paper(d.Paper), d.PageLimit
	if d.CandidacyID != nil {
		v.req.CandidacyID = *d.CandidacyID
	}
	for _, x := range docs {
		if x.ID != d.ID && !x.Current {
			// A one-document run replaced this partner since; carrying it would
			// make its older text current again.
			for _, n := range []string{".json", ".md", "-quality-gate.md"} {
				delete(carried, string(x.Kind)+n)
			}
			continue
		}
		v.from = append(v.from, x)
		raw := carried[string(x.Kind)+".json"]
		if sha(raw) != x.ContentHash {
			return nil, "", fmt.Errorf("%w: the stored %s changed after it was written", ErrConflict, x.Kind)
		}
		switch x.Kind {
		case KindResume:
			var rd ResumeDoc
			if err := json.Unmarshal(raw, &rd); err != nil {
				return nil, "", err
			}
			if v.resumeGate = checkResume(m, rd); !v.resumeGate.OK() {
				return nil, "", fmt.Errorf("%w: %s", ErrNoValidDraft, codes(v.resumeGate))
			}
			v.resume = &rd
			gen = rd.Generation
		case KindCover:
			var ld LetterDoc
			if err := json.Unmarshal(raw, &ld); err != nil {
				return nil, "", err
			}
			if v.letterGate = checkLetter(m, ld); !v.letterGate.OK() {
				return nil, "", fmt.Errorf("%w: %s", ErrNoValidDraft, codes(v.letterGate))
			}
			v.letter = &ld
		}
	}
	return v, gen, nil
}
