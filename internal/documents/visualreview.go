package documents

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// A person's visual review lives in its own file beside the verification
// record, because document-manifest.json names the hash of every other file of
// a published version and a record rewritten later would no longer match it.
const visualReviewFile = "visual-review.json"

type visualReviewBody struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Reviews       map[Kind]VisualReview `json:"reviews"`
}

// readVisualReviews returns the reviews stored for a version directory. A
// missing or unreadable file holds none: only a readable record counts as a
// look at the previews.
func readVisualReviews(dir string) map[Kind]VisualReview {
	raw, err := readRegular(filepath.Join(dir, visualReviewFile), maxRecordBytes)
	if err != nil {
		return nil
	}
	var body visualReviewBody
	if json.Unmarshal(raw, &body) != nil || body.SchemaVersion != verificationSchema {
		return nil
	}
	return body.Reviews
}

// RecordVisualReview records that a person looked at the previews of a
// document's stored PDF. The caller names the PDF hash it looked at; a hash
// that is not the stored PDF's, or a verdict that no longer describes the
// stored files, is refused. Regenerating the document makes a new PDF, so the
// record then reads as pending again.
func (s *Service) RecordVisualReview(ctx context.Context, id int64, pdfSHA256 string) (*Verification, error) {
	d, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	v, err := s.Verification(ctx, d)
	if err != nil {
		return nil, err
	}
	switch {
	case v == nil:
		return nil, fmt.Errorf("%w: document %d has no PDF to review", ErrInvalid, id)
	case v.Stale != "":
		return nil, fmt.Errorf("%w: document %d cannot be reviewed: %s", ErrInvalid, id, v.Stale)
	case !strings.EqualFold(strings.TrimSpace(pdfSHA256), v.PDFSHA256):
		return nil, fmt.Errorf("%w: the reviewed hash is not the stored PDF's (%s)", ErrInvalid, v.PDFSHA256)
	}
	dir, err := s.versionDir(d.Path)
	if err != nil {
		return nil, err
	}
	s.reviewMu.Lock()
	defer s.reviewMu.Unlock()
	reviews := readVisualReviews(dir)
	if reviews == nil {
		reviews = map[Kind]VisualReview{}
	}
	reviews[d.Kind] = VisualReview{Status: ReviewPerformed, PDFSHA256: v.PDFSHA256}
	if err := writeFileAtomic(filepath.Join(dir, visualReviewFile), jsonBytes(visualReviewBody{SchemaVersion: verificationSchema, Reviews: reviews})); err != nil {
		return nil, err
	}
	v.VisualReview = reviews[d.Kind]
	return v, nil
}

// writeFileAtomic replaces a file in one rename, so a reader sees the old
// record or the new one and never half of either.
func writeFileAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".visual-review-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
