package toolchain

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"applysmith/internal/documents/assets"
)

// Verdict values of a check and of a whole result.
const (
	Pass       = "pass"
	Fail       = "fail"
	Unverified = "unverified"
)

// VerifierSchema is the version of the JSON the adapted scripts print.
const VerifierSchema = 1

// ErrVerdict is returned when a verifier's output cannot be accepted. The PDF
// stays unverified: a malformed, incomplete or stale result is never a pass.
var ErrVerdict = errors.New("toolchain: verifier result refused")

// Check is one named verdict.
type Check struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	// Reason may quote document text; it belongs in private diagnostics.
	Reason string `json:"reason"`
}

// PDFResult is the output of verify_pdf.py.
type PDFResult struct {
	SchemaVersion int     `json:"schema_version"`
	Tool          string  `json:"tool"`
	ScriptVersion string  `json:"script_version"`
	PDFSHA256     string  `json:"pdf_sha256"`
	PDFBytes      int     `json:"pdf_bytes"`
	Python        string  `json:"python"`
	Pypdf         *string `json:"pypdf"`
	Extractor     string  `json:"extractor"`
	Pages         *int    `json:"pages"`
	TextSHA256    *string `json:"text_sha256"`
	// FontUsage is null when the font runs could not be read.
	FontUsage []FontUse `json:"font_usage"`
	Checks    []Check   `json:"checks"`
	Overall   string    `json:"overall"`
}

// FontUse is the number of letters set in one font at one size.
type FontUse struct {
	Font    string  `json:"font"`
	SizePt  float64 `json:"size_pt"`
	Letters int     `json:"letters"`
}

// Measurement is one page of verify_layout.py's geometry.
type Measurement struct {
	Page          int      `json:"page"`
	HeightPt      float64  `json:"height_pt"`
	Empty         bool     `json:"empty"`
	TextTopPt     *float64 `json:"text_top_pt"`
	TextBottomPt  *float64 `json:"text_bottom_pt"`
	BottomSpacePt *float64 `json:"bottom_space_pt"`
	BottomShare   *float64 `json:"bottom_share"`
	LargestGapPt  *float64 `json:"largest_gap_pt"`
	LargestGapYPt *float64 `json:"largest_gap_y_pt"`
}

// LayoutResult is the output of verify_layout.py.
type LayoutResult struct {
	SchemaVersion int           `json:"schema_version"`
	Tool          string        `json:"tool"`
	ScriptVersion string        `json:"script_version"`
	Profile       string        `json:"profile"`
	PDFSHA256     string        `json:"pdf_sha256"`
	PDFBytes      int           `json:"pdf_bytes"`
	Python        string        `json:"python"`
	Poppler       string        `json:"poppler"`
	Pages         *int          `json:"pages"`
	Measurements  []Measurement `json:"measurements"`
	Checks        []Check       `json:"checks"`
	Overall       string        `json:"overall"`
}

// PDFExpect is the input contract of verify_pdf.py besides the PDF path.
type PDFExpect struct {
	Pages      int
	MaxPages   int
	MinChars   int
	Contains   []string
	ASCIIDates bool
	// DumpText, when set, receives the extracted text layer.
	DumpText string
}

func (e PDFExpect) requested() []string {
	ids := []string{"pdf-readable", "text-layer"}
	if e.Pages > 0 {
		ids = append(ids, "page-count")
	}
	if e.MaxPages > 0 {
		ids = append(ids, "max-pages")
	}
	if len(e.Contains) > 0 {
		ids = append(ids, "required-text")
	}
	if e.ASCIIDates {
		ids = append(ids, "ascii-dates")
	}
	return ids
}

// Layout templates. Each has its own thresholds in verify_layout.py.
const (
	TemplateResume = "resume"
	TemplateCover  = "cover"
)

// LayoutExpect selects the threshold profile of verify_layout.py: one per
// template and paper size.
type LayoutExpect struct {
	Template string
	Paper    assets.Paper
}

// ProfileID is the profile the script must echo.
func (e LayoutExpect) ProfileID() string { return e.Template + "-" + string(e.Paper) }

func (e LayoutExpect) valid() bool {
	return (e.Template == TemplateResume || e.Template == TemplateCover) && (e.Paper == assets.PaperA4 || e.Paper == assets.PaperLetter)
}

// checks lists the layout checks of the profile in the order the script
// prints them. Only the résumé has entries, so only it has an entry-gap check.
func (e LayoutExpect) checks() []string {
	ids := []string{"page-text", "page-size", "clipping", "text-overlap", "internal-hole", "early-page-end", "footer-collision", "final-page-thin", "orphaned-entry"}
	if e.Template == TemplateResume {
		ids = append(ids, "entry-gap")
	}
	return ids
}

func (e PDFExpect) args() []string {
	var a []string
	if e.Pages > 0 {
		a = append(a, "--pages", strconv.Itoa(e.Pages))
	}
	if e.MaxPages > 0 {
		a = append(a, "--max-pages", strconv.Itoa(e.MaxPages))
	}
	if e.MinChars > 0 {
		a = append(a, "--min-chars", strconv.Itoa(e.MinChars))
	}
	for _, c := range e.Contains {
		a = append(a, "--contains", c)
	}
	if e.ASCIIDates {
		a = append(a, "--ascii-dates")
	}
	if e.DumpText != "" {
		a = append(a, "--dump-text", e.DumpText)
	}
	return a
}

func decodeStrict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %v", ErrVerdict, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing data", ErrVerdict)
	}
	return nil
}

// verdictOf recomputes the overall verdict from the checks, so a result that
// claims more than its checks show is caught.
func verdictOf(checks []Check) string {
	v := Pass
	for _, c := range checks {
		switch c.Status {
		case Fail:
			return Fail
		case Unverified:
			v = Unverified
		}
	}
	return v
}

func validateChecks(checks []Check, want []string, overall string) error {
	if len(checks) != len(want) {
		return fmt.Errorf("%w: %d checks, want %d", ErrVerdict, len(checks), len(want))
	}
	for i, c := range checks {
		if c.ID != want[i] {
			return fmt.Errorf("%w: check %d is %q, want %q", ErrVerdict, i, c.ID, want[i])
		}
		if c.Status != Pass && c.Status != Fail && c.Status != Unverified {
			return fmt.Errorf("%w: check %q has status %q", ErrVerdict, c.ID, c.Status)
		}
	}
	if overall != verdictOf(checks) {
		return fmt.Errorf("%w: overall %q does not follow from the checks", ErrVerdict, overall)
	}
	return nil
}

// ParsePDFResult accepts the output of verify_pdf.py only when it is
// well-formed, complete for the requested checks and bound to wantSHA. A result
// without a page count means the extractor failed, so every check must be
// unverified.
func ParsePDFResult(raw []byte, wantSHA string, e PDFExpect) (PDFResult, error) {
	var r PDFResult
	if err := decodeStrict(raw, &r); err != nil {
		return PDFResult{}, err
	}
	if r.SchemaVersion != VerifierSchema || r.Tool != "verify_pdf" {
		return PDFResult{}, fmt.Errorf("%w: schema %d tool %q", ErrVerdict, r.SchemaVersion, r.Tool)
	}
	if r.PDFSHA256 != wantSHA {
		return PDFResult{}, fmt.Errorf("%w: the verdict is for a different PDF", ErrVerdict)
	}
	if err := validateChecks(r.Checks, e.requested(), r.Overall); err != nil {
		return PDFResult{}, err
	}
	if r.Pages == nil {
		// The extractor failed, so no check can have passed.
		for _, c := range r.Checks {
			if c.Status != Unverified {
				return PDFResult{}, fmt.Errorf("%w: %q is %s although the extractor failed", ErrVerdict, c.ID, c.Status)
			}
		}
	}
	return r, nil
}

// ParseLayoutResult is the layout counterpart of ParsePDFResult. The verdict
// must also name the threshold profile that was asked for.
func ParseLayoutResult(raw []byte, wantSHA string, e LayoutExpect) (LayoutResult, error) {
	var r LayoutResult
	if !e.valid() {
		return LayoutResult{}, fmt.Errorf("%w: unknown layout profile %q", ErrVerdict, e.ProfileID())
	}
	if err := decodeStrict(raw, &r); err != nil {
		return LayoutResult{}, err
	}
	if r.SchemaVersion != VerifierSchema || r.Tool != "verify_layout" {
		return LayoutResult{}, fmt.Errorf("%w: schema %d tool %q", ErrVerdict, r.SchemaVersion, r.Tool)
	}
	if r.PDFSHA256 != wantSHA {
		return LayoutResult{}, fmt.Errorf("%w: the verdict is for a different PDF", ErrVerdict)
	}
	if r.Profile != e.ProfileID() {
		return LayoutResult{}, fmt.Errorf("%w: the verdict used profile %q, not %q", ErrVerdict, r.Profile, e.ProfileID())
	}
	if err := validateChecks(r.Checks, e.checks(), r.Overall); err != nil {
		return LayoutResult{}, err
	}
	return r, nil
}

func fileSHA256(path string) (string, int, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return "", 0, err
	}
	if !fi.Mode().IsRegular() {
		return "", 0, fmt.Errorf("toolchain: %s is not a regular file", baseName(path))
	}
	if fi.Size() > MaxPDFBytes {
		return "", 0, fmt.Errorf("toolchain: the PDF exceeds %d bytes", MaxPDFBytes)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), len(b), nil
}

// VerifyPDF runs verify_pdf.py on pdf and returns its accepted result. The
// hash of the file is taken before and after the run; both must equal the one
// the script reports.
func (t *Toolchain) VerifyPDF(ctx context.Context, pdf string, e PDFExpect) (PDFResult, error) {
	script, err := assets.Read("verify_pdf.py")
	if err != nil {
		return PDFResult{}, err
	}
	before, _, err := fileSHA256(pdf)
	if err != nil {
		return PDFResult{}, err
	}
	out, err := run(ctx, command{Path: t.Tools.Python, Args: append([]string{"-I", "-", pdf}, e.args()...),
		Env: t.Root.minimalEnv(), Stdin: script, Timeout: HelperTimeout})
	if err != nil {
		return PDFResult{}, fmt.Errorf("verify_pdf: %w", err)
	}
	res, err := ParsePDFResult(out.Stdout, before, e)
	if err != nil {
		return PDFResult{}, err
	}
	if after, _, err := fileSHA256(pdf); err != nil || after != before {
		return PDFResult{}, fmt.Errorf("%w: the PDF changed while it was verified", ErrVerdict)
	}
	return res, nil
}

// VerifyLayout runs verify_layout.py on pdf with the resolved pdftotext and the
// threshold profile of the template and paper.
func (t *Toolchain) VerifyLayout(ctx context.Context, pdf string, e LayoutExpect) (LayoutResult, error) {
	if !e.valid() {
		return LayoutResult{}, fmt.Errorf("%w: unknown layout profile %q", ErrVerdict, e.ProfileID())
	}
	script, err := assets.Read("verify_layout.py")
	if err != nil {
		return LayoutResult{}, err
	}
	before, _, err := fileSHA256(pdf)
	if err != nil {
		return LayoutResult{}, err
	}
	out, err := run(ctx, command{Path: t.Tools.Python, Args: []string{"-I", "-", pdf, "--pdftotext", t.Tools.PDFToText, "--template", e.Template, "--paper", string(e.Paper)},
		Env: t.Root.minimalEnv(), Stdin: script, Timeout: HelperTimeout})
	if err != nil {
		return LayoutResult{}, fmt.Errorf("verify_layout: %w", err)
	}
	res, err := ParseLayoutResult(out.Stdout, before, e)
	if err != nil {
		return LayoutResult{}, err
	}
	if after, _, err := fileSHA256(pdf); err != nil || after != before {
		return LayoutResult{}, fmt.Errorf("%w: the PDF changed while it was verified", ErrVerdict)
	}
	return res, nil
}

// UnverifiedPDF is the verdict for a PDF the text verifier could not inspect:
// every requested check is unverified and says why, so the PDF can be kept as
// a preview and never counts as passed.
func UnverifiedPDF(e PDFExpect, sha, reason string) PDFResult {
	r := PDFResult{SchemaVersion: VerifierSchema, Tool: "verify_pdf", PDFSHA256: sha, Overall: Unverified}
	for _, id := range e.requested() {
		r.Checks = append(r.Checks, Check{ID: id, Status: Unverified, Reason: reason})
	}
	return r
}

// UnverifiedLayout is UnverifiedPDF for the layout verifier.
func UnverifiedLayout(e LayoutExpect, sha, reason string) LayoutResult {
	r := LayoutResult{SchemaVersion: VerifierSchema, Tool: "verify_layout", Profile: e.ProfileID(), PDFSHA256: sha, Overall: Unverified}
	for _, id := range e.checks() {
		r.Checks = append(r.Checks, Check{ID: id, Status: Unverified, Reason: reason})
	}
	return r
}
