package documents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"applysmith/internal/documents/assets"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

// Document is one stored version of a résumé or a letter.
type Document struct {
	ID          int64    `json:"id"`
	Bundle      string   `json:"bundle"`
	Version     int      `json:"version"`
	Kind        Kind     `json:"kind"`
	Mode        Mode     `json:"mode"`
	CandidacyID *int64   `json:"candidacyId,omitempty"`
	Path        string   `json:"path"`
	SourcePath  string   `json:"sourcePath"`
	ContentHash string   `json:"contentHash"`
	Manifest    string   `json:"manifestHash"`
	Generation  string   `json:"generation"`
	Language    string   `json:"language"`
	Paper       string   `json:"paper"`
	PageLimit   int      `json:"pageLimit"`
	Limited     bool     `json:"limitedContext"`
	PDFState    string   `json:"pdfState"`
	PDFPath     string   `json:"pdfPath,omitempty"`
	PDFSHA256   string   `json:"pdfSha256,omitempty"`
	Pages       int      `json:"pages,omitempty"`
	Warnings    []string `json:"warnings"`
	RunID       string   `json:"runId,omitempty"`
	FrozenAt    string   `json:"frozenAt,omitempty"`
	CreatedAt   string   `json:"createdAt"`
	// Current is the newest version of its kind for its candidacy; Ready is the
	// newest one whose PDF passed its checks and so may be attached.
	Current bool `json:"current"`
	Ready   bool `json:"ready"`
	// RefinePending says a revision proposal waits for the user's decision; the
	// version's PDF is unavailable meanwhile.
	RefinePending bool `json:"refinePending"`
}

const docColumns = `d.id, d.bundle, d.version, d.kind, d.mode, d.candidacy_id, d.path, d.source_path, d.content_hash, d.manifest_hash,
	d.generation, d.language, d.paper, d.page_limit, d.limited, d.pdf_state, d.pdf_path, d.pdf_sha256, d.pages, d.warnings_json,
	d.run_id, d.frozen_at, d.created_at,
	d.id = (SELECT max(x.id) FROM documents x WHERE x.kind = d.kind AND x.candidacy_id IS d.candidacy_id),
	d.pdf_state = 'ready' AND d.id = (SELECT max(x.id) FROM documents x WHERE x.kind = d.kind AND x.candidacy_id IS d.candidacy_id AND x.pdf_state = 'ready'),
	EXISTS (SELECT 1 FROM document_proposals p WHERE p.document_id = d.id AND p.status = 'pending')`

type rowScanner interface{ Scan(...any) error }

func scanDocument(r rowScanner) (Document, error) {
	var d Document
	var cand, pages sql.NullInt64
	var pdfPath, pdfSHA, runID, frozen sql.NullString
	var limited, current, ready, refinePending int
	var warnings string
	if err := r.Scan(&d.ID, &d.Bundle, &d.Version, &d.Kind, &d.Mode, &cand, &d.Path, &d.SourcePath, &d.ContentHash, &d.Manifest,
		&d.Generation, &d.Language, &d.Paper, &d.PageLimit, &limited, &d.PDFState, &pdfPath, &pdfSHA, &pages, &warnings,
		&runID, &frozen, &d.CreatedAt, &current, &ready, &refinePending); err != nil {
		return Document{}, err
	}
	if cand.Valid {
		d.CandidacyID = &cand.Int64
	}
	d.PDFPath, d.PDFSHA256, d.RunID, d.FrozenAt = pdfPath.String, pdfSHA.String, runID.String, frozen.String
	d.Pages, d.Limited, d.Current, d.Ready, d.RefinePending = int(pages.Int64), limited == 1, current == 1, ready == 1, refinePending == 1
	d.Warnings = []string{}
	_ = json.Unmarshal([]byte(warnings), &d.Warnings)
	return d, nil
}

// List returns the documents of a candidacy, newest first. A zero candidacy
// lists the baseline documents.
func (s *Service) List(ctx context.Context, candidacy int64) ([]Document, error) {
	q := `SELECT ` + docColumns + ` FROM documents d WHERE d.candidacy_id IS ? ORDER BY d.id DESC`
	var arg any
	if candidacy > 0 {
		arg = candidacy
	}
	rows, err := s.d.Store.Read().QueryContext(ctx, q, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Document{}
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		s.settleReady(ctx, &out[i])
	}
	return out, nil
}

// Get returns one document or ErrNotFound.
func (s *Service) Get(ctx context.Context, id int64) (Document, error) {
	d, err := getDocument(ctx, s.d.Store.Read(), id)
	if err == nil {
		s.settleReady(ctx, &d)
	}
	return d, err
}

// settleReady narrows the stored flag by the files on disk. A document is
// ready only while its stored verdict still describes the PDF in the version
// directory; a PDF replaced after the verdict reads as stale, so it is not
// ready, whatever the database row says.
func (s *Service) settleReady(ctx context.Context, d *Document) {
	if !d.Ready {
		return
	}
	v, err := s.Verification(ctx, *d)
	d.Ready = err == nil && v != nil && v.Stale == ""
}

// SourcesChanged names what a current, unattached version was written from
// that changed since: cv.md and profile.yml for either document, voice.md for
// a letter whose text a model wrote from it, and the evaluation report a
// tailored version read the posting from when the application now links a
// report whose posting reads otherwise. A pasted posting is the user's, so
// only a report linked since dates it.
func (s *Service) SourcesChanged(ctx context.Context, d Document) []string {
	dir, err := s.versionDir(d.Path)
	if !d.Current || d.FrozenAt != "" || err != nil {
		return nil
	}
	var m Manifest
	if raw, err := readRegular(filepath.Join(dir, "evidence-manifest.json"), 4<<20); err != nil || json.Unmarshal(raw, &m) != nil || m.Hash != d.Manifest {
		return nil
	}
	var voice string
	if d.Kind == KindCover {
		var ld LetterDoc
		if raw, err := readRegular(filepath.Join(dir, "cover-letter.json"), 4<<20); err == nil && sha(raw) == d.ContentHash && json.Unmarshal(raw, &ld) == nil {
			voice = ld.VoiceSHA256
		}
	}
	var out []string
	for _, f := range []struct {
		file workspace.File
		was  string
	}{{workspace.FileCV, m.Inputs.CVSHA256}, {workspace.FileProfile, m.Inputs.ProfileSHA256}, {workspace.FileVoice, voice}} {
		if c, err := s.d.Workspace.Read(f.file); err == nil && f.was != "" && sha(c.Data) != f.was {
			out = append(out, f.file.Name())
		}
	}
	if d.CandidacyID != nil && m.Mode == ModeTailored {
		e, err := s.candidacy(ctx, *d.CandidacyID)
		if err == nil && e.ReportNumber != nil && *e.ReportNumber != m.Inputs.ReportNumber {
			if text, _, err := s.postingFor(ctx, Request{CandidacyID: *d.CandidacyID}); err == nil && sha([]byte(text)) != m.Inputs.PostingSHA256 {
				out = append(out, "the evaluation report")
			}
		}
	}
	return out
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getDocument(ctx context.Context, q queryRower, id int64) (Document, error) {
	d, err := scanDocument(q.QueryRowContext(ctx, `SELECT `+docColumns+` FROM documents d WHERE d.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Document{}, fmt.Errorf("%w: document %d", ErrNotFound, id)
	}
	return d, err
}

// version returns the documents that share a bundle version.
func (s *Service) versionOf(ctx context.Context, d Document) ([]Document, error) {
	rows, err := s.d.Store.Read().QueryContext(ctx, `SELECT `+docColumns+` FROM documents d WHERE d.bundle = ? AND d.version = ? ORDER BY d.kind`, d.Bundle, d.Version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Document
	for rows.Next() {
		x, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		s.settleReady(ctx, &out[i])
	}
	return out, nil
}

func (s *Service) nextVersion(ctx context.Context, bundle string) (int, error) {
	var v sql.NullInt64
	if err := s.d.Store.Read().QueryRowContext(ctx, `SELECT max(version) FROM documents WHERE bundle = ?`, bundle).Scan(&v); err != nil {
		return 0, err
	}
	return int(v.Int64) + 1, nil
}

// Freeze marks versions as attached to a submitted application. A frozen
// version is never changed and is deleted only on an explicit request that
// names it. The ids must belong to the candidacy.
func (s *Service) Freeze(ctx context.Context, candidacy int64, ids ...int64) error {
	now := s.d.Now().UTC().Format(time.RFC3339)
	return s.d.Store.Write(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			res, err := tx.ExecContext(ctx, `UPDATE documents SET frozen_at = COALESCE(frozen_at, ?) WHERE id = ? AND candidacy_id = ?`, now, id, candidacy)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return fmt.Errorf("%w: document %d of candidacy %d", ErrNotFound, id, candidacy)
			}
		}
		return nil
	})
}

// Delete removes a version's files and record; the tracker row and its sticky
// flags stay. The last kind of a version also takes the version's artifact
// row, so its number and path are free for the next version of the bundle;
// its proposals go by cascade. A version attached to a submitted application
// is refused unless explicit is true, which only a request that names the
// application's documents may set.
func (s *Service) Delete(ctx context.Context, id int64, explicit bool) error {
	d, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if d.FrozenAt != "" && !explicit {
		return ErrFrozen
	}
	err = s.d.Store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM documents WHERE id = ?`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM report_artifacts WHERE kind = ? AND path = ? AND NOT EXISTS (SELECT 1 FROM documents WHERE path = ?)`,
			artifactKind, path.Join(d.Path, manifestFile), d.Path)
		return err
	})
	if err != nil {
		return err
	}
	return s.removeFiles(ctx, d)
}

// removeFiles deletes the files that belong to one kind of a version, and the
// version directory when no other record uses it. The path comes from a row
// this package wrote; it is still checked before anything is removed.
func (s *Service) removeFiles(ctx context.Context, d Document) error {
	dir, err := s.versionDir(d.Path)
	if err != nil {
		return err
	}
	prefix := "resume"
	if d.Kind == KindCover {
		prefix = "cover-letter"
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.Type().IsRegular() && ownedBy(prefix, e.Name()) {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	var left int
	if err := s.d.Store.Read().QueryRowContext(ctx, `SELECT count(*) FROM documents WHERE path = ?`, d.Path).Scan(&left); err != nil {
		return err
	}
	if left == 0 {
		return os.RemoveAll(dir)
	}
	return nil
}

// ownedBy says whether a file of a version directory belongs to one kind.
func ownedBy(base, name string) bool {
	for _, ext := range []string{".json", ".md", ".tex", ".pdf", ".txt"} {
		if name == base+ext {
			return true
		}
	}
	return strings.HasPrefix(name, base+"-page-") && strings.HasSuffix(name, ".png")
}

// versionDir resolves a stored version path inside the documents directory.
func (s *Service) versionDir(rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(clean) || !strings.HasPrefix(filepath.ToSlash(clean), "documents/") || strings.Contains(clean, "..") {
		return "", fmt.Errorf("%w: %q", store.ErrUnsafePath, rel)
	}
	return filepath.Join(s.d.Workspace.Root, clean), nil
}

// Preferences returns the workspace document preferences.
func (s *Service) Preferences(ctx context.Context) (Preferences, error) {
	var p Preferences
	var paper string
	err := s.d.Store.Read().QueryRowContext(ctx, `SELECT language, paper, resume_pages, cover_when_asked, revision FROM document_preferences WHERE id = 1`).
		Scan(&p.Language, &paper, &p.ResumePages, &p.CoverWhenAsked, &p.Revision)
	p.Paper = assets.Paper(paper)
	return p, err
}

// PreferencePatch changes some preferences; nil fields stay.
type PreferencePatch struct {
	Language       *string
	Paper          *string
	ResumePages    *int
	CoverWhenAsked *string
	Revision       int
}

// SetPreferences applies a patch to the revision the caller read. A stale
// revision is ErrConflict; an unsupported value is ErrInvalid.
func (s *Service) SetPreferences(ctx context.Context, patch PreferencePatch) (Preferences, error) {
	var out Preferences
	err := s.d.Store.Write(ctx, func(tx *sql.Tx) error {
		var p Preferences
		var paper string
		if err := tx.QueryRowContext(ctx, `SELECT language, paper, resume_pages, cover_when_asked, revision FROM document_preferences WHERE id = 1`).
			Scan(&p.Language, &paper, &p.ResumePages, &p.CoverWhenAsked, &p.Revision); err != nil {
			return err
		}
		if patch.Revision != p.Revision {
			return fmt.Errorf("%w: the preferences are at revision %d", ErrConflict, p.Revision)
		}
		p.Paper = assets.Paper(paper)
		if patch.Language != nil {
			if _, ok := lookupLanguage(*patch.Language); !ok {
				return invalid("language", "unsupported language %q; supported: %s", *patch.Language, strings.Join(Languages(), ", "))
			}
			p.Language = *patch.Language
		}
		if patch.Paper != nil {
			pp := assets.Paper(*patch.Paper)
			if pp != assets.PaperA4 && pp != assets.PaperLetter {
				return invalid("paper", "paper must be a4 or letter")
			}
			p.Paper = pp
		}
		if patch.ResumePages != nil {
			if *patch.ResumePages != 1 && *patch.ResumePages != 2 {
				return invalid("resumePages", "the résumé page limit is 1 or 2")
			}
			p.ResumePages = *patch.ResumePages
		}
		if patch.CoverWhenAsked != nil {
			if *patch.CoverWhenAsked != CoverAlways && *patch.CoverWhenAsked != CoverEligible {
				return invalid("coverWhenAsked", "coverWhenAsked is %s or %s", CoverAlways, CoverEligible)
			}
			p.CoverWhenAsked = *patch.CoverWhenAsked
		}
		p.Revision++
		if _, err := tx.ExecContext(ctx, `UPDATE document_preferences SET language = ?, paper = ?, resume_pages = ?, cover_when_asked = ?, revision = ?, updated_at = ? WHERE id = 1`,
			p.Language, string(p.Paper), p.ResumePages, p.CoverWhenAsked, p.Revision, s.d.Now().UTC().Format(time.RFC3339)); err != nil {
			return err
		}
		out = p
		return nil
	})
	return out, err
}

// ValidationError names the request field that is wrong.
type ValidationError struct{ Field, Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// Is makes errors.Is(err, ErrInvalid) true for every validation error.
func (e *ValidationError) Is(t error) bool { return t == ErrInvalid }

func invalid(field, format string, args ...any) error {
	return &ValidationError{Field: field, Msg: fmt.Sprintf(format, args...)}
}
