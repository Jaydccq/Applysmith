package documents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"applysmith/internal/documents/assets"
	"applysmith/internal/documents/toolchain"
)

// The verification record binds a verdict to the exact PDF and the exact
// source revision it describes. A verdict is read back only while those still
// match; otherwise it no longer applies and the document reads as unverified.

// Tools whose checks a record holds.
const (
	toolPDF    = "verify_pdf"
	toolLayout = "verify_layout"
	toolFinal  = "final"
)

const (
	verificationFile   = "pdf-verification.json"
	verificationSchema = 1
	maxRecordBytes     = 1 << 20
)

// Visual review states. The mechanical checks never set Performed: only a
// person who looked at the previews of this exact PDF does.
const (
	ReviewPending   = "pending"
	ReviewPerformed = "performed"
)

// RecordedCheck is one verdict with the tool that gave it.
type RecordedCheck struct {
	Tool   string `json:"tool"`
	ID     string `json:"id"`
	Status string `json:"status"`
	// Reason may quote a few words of the document; it is shown to the owner
	// of the workspace and stays out of logs.
	Reason string `json:"reason,omitempty"`
}

// PreviewRecord names one rasterised page and its hash.
type PreviewRecord struct {
	Page   int    `json:"page"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// VisualReview is kept apart from the checks: a person's look at the previews
// of one PDF, recorded against that PDF's hash.
type VisualReview struct {
	Status    string `json:"status"`
	PDFSHA256 string `json:"pdfSha256,omitempty"`
}

// Verifiers records which scripts, thresholds and tools gave the verdict.
type Verifiers struct {
	Rules         string `json:"rules"`
	PDFScript     string `json:"pdfScript"`
	LayoutScript  string `json:"layoutScript"`
	LayoutProfile string `json:"layoutProfile"`
	Python        string `json:"python,omitempty"`
	Pypdf         string `json:"pypdf,omitempty"`
	Poppler       string `json:"poppler,omitempty"`
}

// Verification is the stored verdict for one PDF.
type Verification struct {
	SchemaVersion int             `json:"schemaVersion"`
	Kind          Kind            `json:"kind"`
	State         string          `json:"state"`
	Pages         int             `json:"pages"`
	PDFSHA256     string          `json:"pdfSha256"`
	TeXSHA256     string          `json:"texSha256"`
	TextSHA256    string          `json:"textSha256,omitempty"`
	SourceSHA256  string          `json:"sourceSha256"`
	ManifestHash  string          `json:"manifestHash"`
	Template      string          `json:"template"`
	Verifiers     Verifiers       `json:"verifiers"`
	Checks        []RecordedCheck `json:"checks"`
	Previews      []PreviewRecord `json:"previews"`
	VisualReview  VisualReview    `json:"visualReview"`
	// Stale says why a stored record no longer applies to the document it was
	// read for. It is never written.
	Stale string `json:"stale,omitempty"`
}

type verificationFileBody struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Verifications map[Kind]Verification `json:"verifications"`
}

func tag(tool string, checks []toolchain.Check) []RecordedCheck {
	out := make([]RecordedCheck, 0, len(checks))
	for _, c := range checks {
		out = append(out, RecordedCheck{Tool: tool, ID: c.ID, Status: c.Status, Reason: c.Reason})
	}
	return out
}

// recordedChecks lists the verdicts of both verifiers and the final checks.
func recordedChecks(o toolchain.BuildOutput, finals []toolchain.Check) []RecordedCheck {
	out := tag(toolPDF, o.Verify.Checks)
	out = append(out, tag(toolLayout, o.Layout.Checks)...)
	return append(out, tag(toolFinal, finals)...)
}

// decide turns the checks into the PDF state. A failure outranks a check that
// could not run, and a PDF is ready only when every tool reported and every
// check passed: an absent verifier, an empty list or an unknown status is
// unverified, never a pass.
func decide(checks []RecordedCheck) (string, []string) {
	var warns []string
	failed, open := false, false
	seen := map[string]bool{}
	for _, c := range checks {
		seen[c.Tool] = true
		switch c.Status {
		case toolchain.Pass:
			continue
		case toolchain.Fail:
			failed = true
		default:
			open = true
		}
		status := c.Status
		if status != toolchain.Fail {
			status = toolchain.Unverified
		}
		warns = append(warns, fmt.Sprintf("%s %s: %s", c.Tool, c.ID, status))
	}
	for _, t := range []string{toolPDF, toolLayout, toolFinal} {
		if !seen[t] {
			open = true
			warns = append(warns, t+": no checks ran")
		}
	}
	switch {
	case failed:
		return PDFFailed, warns
	case open:
		return PDFUnverified, warns
	}
	return PDFReady, warns
}

// pdfState decides whether a compiled PDF may be attached.
func pdfState(o toolchain.BuildOutput, finals []toolchain.Check) (string, []string) {
	return decide(recordedChecks(o, finals))
}

// newVerification assembles the record of one built document.
func newVerification(k Kind, b *built, sourceSHA, manifestHash string, layout toolchain.LayoutExpect) Verification {
	o := b.out
	v := Verification{SchemaVersion: verificationSchema, Kind: k, State: b.state, Pages: o.Pages, PDFSHA256: o.PDFSHA256, TeXSHA256: o.TeXSHA256,
		SourceSHA256: sourceSHA, ManifestHash: manifestHash, Template: assets.TemplateVersion, Checks: recordedChecks(o, b.finals),
		Previews: []PreviewRecord{}, VisualReview: VisualReview{Status: ReviewPending},
		Verifiers: Verifiers{Rules: RulesVersion, PDFScript: o.Verify.ScriptVersion, LayoutScript: o.Layout.ScriptVersion, LayoutProfile: layout.ProfileID(),
			Python: o.Verify.Python, Poppler: o.Layout.Poppler}}
	if o.Verify.TextSHA256 != nil {
		v.TextSHA256 = *o.Verify.TextSHA256
	}
	if o.Verify.Pypdf != nil {
		v.Verifiers.Pypdf = *o.Verify.Pypdf
	}
	for i, p := range o.Previews {
		v.Previews = append(v.Previews, PreviewRecord{Page: i + 1, Name: p.Name, SHA256: p.SHA256})
	}
	return v
}

// Verification reads the verdict stored for a document's PDF and checks that it
// still describes the stored files: the same PDF bytes, the same structured
// source and the same evidence manifest. A document without a PDF has none.
func (s *Service) Verification(ctx context.Context, d Document) (*Verification, error) {
	if d.PDFPath == "" {
		return nil, nil
	}
	dir, err := s.versionDir(d.Path)
	if err != nil {
		return nil, err
	}
	stale := func(reason string) (*Verification, error) {
		return &Verification{SchemaVersion: verificationSchema, Kind: d.Kind, State: PDFUnverified, PDFSHA256: d.PDFSHA256, Checks: []RecordedCheck{},
			Previews: []PreviewRecord{}, VisualReview: VisualReview{Status: ReviewPending}, Stale: reason}, nil
	}
	raw, err := readRegular(filepath.Join(dir, verificationFile), maxRecordBytes)
	if errors.Is(err, os.ErrNotExist) {
		return stale("the version has no verification record")
	}
	if err != nil {
		return nil, err
	}
	var body verificationFileBody
	if err := json.Unmarshal(raw, &body); err != nil || body.SchemaVersion != verificationSchema {
		return stale("the verification record cannot be read")
	}
	v, ok := body.Verifications[d.Kind]
	if !ok {
		return stale("the record has no verdict for this document")
	}
	pdf, err := readRegular(filepath.Join(dir, string(d.Kind)+".pdf"), toolchain.MaxPDFBytes)
	switch {
	case err != nil:
		return stale("the PDF is not in the version directory")
	case sha(pdf) != v.PDFSHA256 || v.PDFSHA256 != d.PDFSHA256:
		return stale("the PDF is not the one the verdict describes")
	case v.SourceSHA256 != d.ContentHash || v.ManifestHash != d.Manifest:
		return stale("the source changed after the verdict")
	case v.State != d.PDFState:
		return stale("the verdict disagrees with the stored state")
	}
	if rv, ok := readVisualReviews(dir)[d.Kind]; ok {
		v.VisualReview = rv
	}
	// A look at another file's previews says nothing about this one.
	if v.VisualReview.Status == ReviewPerformed && v.VisualReview.PDFSHA256 != v.PDFSHA256 {
		v.VisualReview = VisualReview{Status: ReviewPending}
	}
	return &v, nil
}

// ReadPDF returns the stored PDF of a version, refusing bytes that are not the
// ones the row names: the apply chain attaches exactly the file that was
// verified, never a replaced one.
func (s *Service) ReadPDF(_ context.Context, d Document) ([]byte, error) {
	if d.PDFPath == "" {
		return nil, ErrNotFound
	}
	dir, err := s.versionDir(d.Path)
	if err != nil {
		return nil, err
	}
	pdf, err := readRegular(filepath.Join(dir, string(d.Kind)+".pdf"), toolchain.MaxPDFBytes)
	if err != nil {
		return nil, err
	}
	if sha(pdf) != d.PDFSHA256 {
		return nil, fmt.Errorf("%w: the stored PDF is not the one the version names", ErrConflict)
	}
	return pdf, nil
}

// keptPDF takes over the passed PDF of d, a document of the version v was
// loaded from, when v holds that document byte-identical: the same structured
// source, evidence manifest, paper and page limit. Its verdict travels with
// it only while every hash the verdict binds still matches the bytes copied;
// anything else leaves the document without a PDF in the new version.
func (s *Service) keptPDF(ctx context.Context, d Document, v *version) *built {
	ver, err := s.Verification(ctx, d)
	k := d.Kind
	if err != nil || ver == nil || ver.Stale != "" || ver.State != PDFReady || ver.ManifestHash != v.m.Hash ||
		v.docHash(k) != ver.SourceSHA256 ||
		d.Paper != string(v.paper) || d.PageLimit != v.pages {
		return nil
	}
	dir, err := s.versionDir(d.Path)
	if err != nil {
		return nil
	}
	read := func(name, want string) []byte {
		b, err := readRegular(filepath.Join(dir, name), toolchain.MaxPDFBytes)
		if err != nil || (want != "" && sha(b) != want) {
			return nil
		}
		return b
	}
	pdf, tex, text := read(string(k)+".pdf", ver.PDFSHA256), read(string(k)+".tex", ver.TeXSHA256), read(string(k)+".txt", "")
	if pdf == nil || tex == nil || text == nil {
		return nil
	}
	out := toolchain.BuildOutput{PDF: pdf, PDFSHA256: ver.PDFSHA256, TeXSHA256: ver.TeXSHA256, Pages: ver.Pages, Text: string(text),
		Verify: toolchain.PDFResult{ScriptVersion: ver.Verifiers.PDFScript, Python: ver.Verifiers.Python},
		Layout: toolchain.LayoutResult{ScriptVersion: ver.Verifiers.LayoutScript, Poppler: ver.Verifiers.Poppler}}
	for _, p := range ver.Previews {
		png := read(p.Name, p.SHA256)
		if png == nil {
			return nil
		}
		out.Previews = append(out.Previews, toolchain.Preview{Name: p.Name, PNG: png, SHA256: p.SHA256})
	}
	return &built{state: PDFReady, out: out, kept: ver, tex: tex}
}
