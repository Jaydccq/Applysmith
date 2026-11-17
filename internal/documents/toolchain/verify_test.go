package toolchain

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/documents/assets"
)

const testSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func pdfResultJSON(t *testing.T, mutate func(*PDFResult)) []byte {
	t.Helper()
	pypdf, pages := "6.19.0", 2
	r := PDFResult{SchemaVersion: VerifierSchema, Tool: "verify_pdf", ScriptVersion: "1", PDFSHA256: testSHA, PDFBytes: 10,
		Python: "3.14.7", Pypdf: &pypdf, Extractor: "pypdf", Pages: &pages,
		Checks: []Check{{ID: "pdf-readable", Status: Pass}, {ID: "text-layer", Status: Pass}, {ID: "max-pages", Status: Pass}}, Overall: Pass}
	if mutate != nil {
		mutate(&r)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var expectMax2 = PDFExpect{MaxPages: 2}

func TestVerifierContract_AcceptsACompleteResult(t *testing.T) {
	r, err := ParsePDFResult(pdfResultJSON(t, nil), testSHA, expectMax2)
	if err != nil || r.Overall != Pass || *r.Pages != 2 {
		t.Fatalf("result = %+v, err = %v", r, err)
	}
}

func TestVerifierContract_StaleHashRejected(t *testing.T) {
	_, err := ParsePDFResult(pdfResultJSON(t, nil), strings.Repeat("f", 64), expectMax2)
	if !errors.Is(err, ErrVerdict) {
		t.Fatalf("err = %v, want ErrVerdict", err)
	}
}

func TestVerifierContract_RefusesMalformedOutput(t *testing.T) {
	tests := []struct {
		name string
		raw  func() []byte
	}{
		{"not json", func() []byte { return []byte("Verified x.pdf (extractor: pypdf, pages: 2)\n") }},
		{"empty", func() []byte { return nil }},
		{"trailing data", func() []byte { return append(pdfResultJSON(t, nil), []byte(` {}`)...) }},
		{"unknown field", func() []byte {
			return []byte(strings.Replace(string(pdfResultJSON(t, nil)), `"tool"`, `"extra":1,"tool"`, 1))
		}},
		{"future schema", func() []byte { return pdfResultJSON(t, func(r *PDFResult) { r.SchemaVersion = 2 }) }},
		{"other tool", func() []byte { return pdfResultJSON(t, func(r *PDFResult) { r.Tool = "verify_layout" }) }},
		{"missing check", func() []byte {
			return pdfResultJSON(t, func(r *PDFResult) { r.Checks = r.Checks[:2] })
		}},
		{"reordered checks", func() []byte {
			return pdfResultJSON(t, func(r *PDFResult) { r.Checks[0], r.Checks[1] = r.Checks[1], r.Checks[0] })
		}},
		{"unknown status", func() []byte { return pdfResultJSON(t, func(r *PDFResult) { r.Checks[0].Status = "skipped" }) }},
		{"overall claims more than the checks", func() []byte {
			return pdfResultJSON(t, func(r *PDFResult) { r.Checks[2].Status = Fail })
		}},
		{"skipped counted as pass", func() []byte {
			return pdfResultJSON(t, func(r *PDFResult) { r.Checks[2].Status = Unverified; r.Overall = Pass })
		}},
		{"pass although the extractor failed", func() []byte { return pdfResultJSON(t, func(r *PDFResult) { r.Pages = nil }) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParsePDFResult(tc.raw(), testSHA, expectMax2); !errors.Is(err, ErrVerdict) {
				t.Fatalf("err = %v, want ErrVerdict", err)
			}
		})
	}
}

func TestVerifierContract_UnavailableExtractorIsUnverified(t *testing.T) {
	raw := pdfResultJSON(t, func(r *PDFResult) {
		r.Pypdf, r.Pages, r.Extractor = nil, nil, "pypdf"
		r.Checks = []Check{{ID: "pdf-readable", Status: Unverified, Reason: "pypdf is not importable"}, {ID: "text-layer", Status: Unverified}, {ID: "max-pages", Status: Unverified}}
		r.Overall = Unverified
	})
	r, err := ParsePDFResult(raw, testSHA, expectMax2)
	if err != nil || r.Overall != Unverified {
		t.Fatalf("result = %+v, err = %v", r, err)
	}
}

func TestVerifierContract_ExpectationsSelectTheChecks(t *testing.T) {
	e := PDFExpect{Pages: 1, MaxPages: 2, Contains: []string{"x"}, ASCIIDates: true}
	want := []string{"pdf-readable", "text-layer", "page-count", "max-pages", "required-text", "ascii-dates"}
	if got := e.requested(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("requested = %v", got)
	}
	args := strings.Join(e.args(), " ")
	for _, frag := range []string{"--pages 1", "--max-pages 2", "--contains x", "--ascii-dates"} {
		if !strings.Contains(args, frag) {
			t.Errorf("args %q lack %q", args, frag)
		}
	}
}

var (
	resumeA4  = LayoutExpect{Template: TemplateResume, Paper: assets.PaperA4}
	coverLtr  = LayoutExpect{Template: TemplateCover, Paper: assets.PaperLetter}
	errBroken = errors.New("verify_pdf: exit status 3\nsecond line quoting the document")
)

func layoutResultJSON(t *testing.T, e LayoutExpect, mutate func(*LayoutResult)) []byte {
	t.Helper()
	pages := 1
	r := LayoutResult{SchemaVersion: VerifierSchema, Tool: "verify_layout", ScriptVersion: "2", Profile: e.ProfileID(), PDFSHA256: testSHA, Python: "3.14.7",
		Poppler: "pdftotext version 26.10.0", Pages: &pages, Overall: Pass}
	for _, id := range e.checks() {
		r.Checks = append(r.Checks, Check{ID: id, Status: Pass})
	}
	if mutate != nil {
		mutate(&r)
	}
	b, _ := json.Marshal(r)
	return b
}

func TestVerifierContract_Layout(t *testing.T) {
	if _, err := ParseLayoutResult(layoutResultJSON(t, resumeA4, nil), testSHA, resumeA4); err != nil {
		t.Fatalf("complete result: %v", err)
	}
	if _, err := ParseLayoutResult(layoutResultJSON(t, resumeA4, nil), strings.Repeat("a", 64), resumeA4); !errors.Is(err, ErrVerdict) {
		t.Errorf("stale hash: %v", err)
	}
	failing := layoutResultJSON(t, resumeA4, func(r *LayoutResult) { r.Checks[1].Status = Fail; r.Overall = Fail })
	if r, err := ParseLayoutResult(failing, testSHA, resumeA4); err != nil || r.Overall != Fail {
		t.Errorf("a failing layout must be accepted as a failure: %+v %v", r, err)
	}
	short := layoutResultJSON(t, resumeA4, func(r *LayoutResult) { r.Checks = r.Checks[:3] })
	if _, err := ParseLayoutResult(short, testSHA, resumeA4); !errors.Is(err, ErrVerdict) {
		t.Errorf("missing checks: %v", err)
	}
}

// The thresholds are versioned per template and paper size, so a verdict that
// was produced for another profile is not a verdict for this document.
func TestVerifierContract_LayoutProfileMustMatch(t *testing.T) {
	letterResume := LayoutExpect{Template: TemplateResume, Paper: assets.PaperLetter}
	for name, tc := range map[string]struct {
		raw    []byte
		expect LayoutExpect
	}{
		"verdict for the other paper size": {layoutResultJSON(t, letterResume, nil), resumeA4},
		"verdict for the other template":   {layoutResultJSON(t, coverLtr, nil), LayoutExpect{Template: TemplateCover, Paper: assets.PaperA4}},
		"résumé verdict without entry-gap": {layoutResultJSON(t, resumeA4, func(r *LayoutResult) { r.Checks = r.Checks[:len(r.Checks)-1] }), resumeA4},
		"cover verdict with entry-gap": {layoutResultJSON(t, coverLtr, func(r *LayoutResult) {
			r.Checks = append(r.Checks, Check{ID: "entry-gap", Status: Pass})
		}), coverLtr},
		"unknown profile requested": {layoutResultJSON(t, resumeA4, nil), LayoutExpect{Template: "poster", Paper: assets.PaperA4}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseLayoutResult(tc.raw, testSHA, tc.expect); !errors.Is(err, ErrVerdict) {
				t.Fatalf("err = %v, want ErrVerdict", err)
			}
		})
	}
}

func TestVerifierContract_FontUsageIsOptionalButStrict(t *testing.T) {
	raw := pdfResultJSON(t, func(r *PDFResult) {
		r.FontUsage = []FontUse{{Font: "Raleway-Medium-Identity-H", SizePt: 11, Letters: 400}}
	})
	r, err := ParsePDFResult(raw, testSHA, expectMax2)
	if err != nil || len(r.FontUsage) != 1 || r.FontUsage[0].Letters != 400 {
		t.Fatalf("result = %+v, err = %v", r, err)
	}
	bad := strings.Replace(string(raw), `"letters"`, `"glyphs"`, 1)
	if _, err := ParsePDFResult([]byte(bad), testSHA, expectMax2); !errors.Is(err, ErrVerdict) {
		t.Errorf("an unknown font_usage field: %v", err)
	}
}

// A verifier that crashed or printed something unacceptable leaves the PDF
// unverified with every check named; it is never a pass and never a failure
// the document is blamed for.
func TestUnverifiedVerdicts_NameEveryCheck(t *testing.T) {
	p := UnverifiedPDF(PDFExpect{Pages: 1, MinChars: 10, ASCIIDates: true}, testSHA, "no python")
	if p.Overall != Unverified || p.Pages != nil || len(p.Checks) != 4 {
		t.Fatalf("pdf verdict = %+v", p)
	}
	l := UnverifiedLayout(resumeA4, testSHA, "no pdftotext")
	if l.Overall != Unverified || l.Profile != "resume-a4" || len(l.Checks) != len(resumeA4.checks()) {
		t.Fatalf("layout verdict = %+v", l)
	}
	for _, c := range append(p.Checks, l.Checks...) {
		if c.Status != Unverified || c.Reason == "" {
			t.Errorf("check %+v", c)
		}
	}
	if got := reasonOf("verify_pdf", errBroken); strings.Contains(got, "quoting") {
		t.Errorf("the reason %q keeps the second line", got)
	}
}

func TestUnverifiable_CancellationAndTimeoutStopTheBuild(t *testing.T) {
	live := context.Background()
	cancelled, cancel := context.WithCancel(live)
	cancel()
	for name, tc := range map[string]struct {
		ctx  context.Context
		err  error
		want bool
	}{
		"crash":     {live, errBroken, true},
		"verdict":   {live, ErrVerdict, true},
		"timeout":   {live, ErrTimeout, false},
		"flood":     {live, ErrLogLimit, false},
		"cancelled": {live, ErrCancelled, false},
		"context":   {cancelled, errBroken, false},
	} {
		if got := unverifiable(tc.ctx, tc.err); got != tc.want {
			t.Errorf("%s: unverifiable = %v, want %v", name, got, tc.want)
		}
	}
}

// fakePython prints a canned result and then appends to the PDF, which is what
// a document changing under the verifier looks like.
func TestVerifyPDF_RejectsAPDFThatChangedWhileVerified(t *testing.T) {
	dir := t.TempDir()
	pdf := filepath.Join(dir, "a.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.7 original"), 0o600); err != nil {
		t.Fatal(err)
	}
	sha, _, err := fileSHA256(pdf)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(pdfResultJSON(t, func(r *PDFResult) { r.PDFSHA256 = sha }))
	py := filepath.Join(dir, "python")
	body := "#!/bin/sh\ncat >/dev/null\nprintf x >> \"$3\"\ncat <<'EOF'\n" + raw + "\nEOF\n"
	if err := os.WriteFile(py, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	tc := &Toolchain{Root: Root(filepath.Join(dir, "root")), Tools: Tools{Python: py}}
	if _, err := tc.VerifyPDF(context.Background(), pdf, expectMax2); !errors.Is(err, ErrVerdict) {
		t.Fatalf("err = %v, want ErrVerdict", err)
	}
}

func TestFileSHA256_RefusesNonRegularAndOversizeFiles(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "link.pdf")
	target := filepath.Join(dir, "t.pdf")
	_ = os.WriteFile(target, []byte("x"), 0o600)
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, _, err := fileSHA256(link); err == nil {
		t.Error("a symlink must be refused")
	}
	big := filepath.Join(dir, "big.pdf")
	f, _ := os.Create(big)
	_ = f.Truncate(MaxPDFBytes + 1)
	f.Close()
	if _, _, err := fileSHA256(big); err == nil {
		t.Error("a PDF over the size cap must be refused")
	}
}
