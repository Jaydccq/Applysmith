package documents

import (
	"context"
	"math"
	"regexp"
	"strings"
	"sync"

	"applysmith/internal/documents/assets"
	"applysmith/internal/documents/toolchain"
)

// fakeBuilder stands in for the toolchain in tests of the repair loop. It
// reads the TeX it is given, lays it out with a simple height model and
// returns the build output the real toolchain would for that page count, so
// the final checks and the PDF state run for real on what it returns.
type fakeBuilder struct {
	mu    sync.Mutex
	calls []fakeCall
	// resumeCap and coverCap are the text height of one page in points.
	resumeCap, coverCap float64
	// itemPt is the height of one list item in the résumé; 0 means 12.
	itemPt float64
	// override replaces check verdicts for a call: tool/id to status.
	override func(call fakeCall) map[string]string
	// dropText is removed from the text layer, as a lost glyph run would be.
	dropText string
	// hook runs at the start of every call; a non-nil error fails the build.
	hook func(ctx context.Context, call fakeCall) error
}

type fakeCall struct {
	N     int
	Kind  Kind
	SHA   string
	Tight bool
	Pages int
	// Hashed is set when the caller gave the hash of the TeX it rendered.
	Hashed bool
	// Content is the text height the call laid out.
	Content float64
}

var (
	reCommand = regexp.MustCompile(`\\[a-zA-Z]+\*?(\[[^\]]*\])?`)
	reItem    = regexp.MustCompile(`\\item([^a-zA-Z]|$)`)
	reEscaped = strings.NewReplacer(`\%`, "%", `\&`, "&", `\_`, "_", `\#`, "#", `\$`, "$", `\{`, " ", `\}`, " ")
)

// detex turns rendered TeX into a text layer: commands go, escaped characters
// stay, braces separate words.
func detex(tex string) string {
	s := reEscaped.Replace(tex)
	s = reCommand.ReplaceAllString(s, " ")
	s = strings.NewReplacer("{", " ", "}", " ", "\\\\", " ").Replace(s)
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.Join(strings.Fields(l), " "); l != "" && !strings.HasPrefix(l, "%") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n") + "\n"
}

func (f *fakeBuilder) heights(kind Kind, tex string) (content, capacity float64) {
	items := len(reItem.FindAllString(tex, -1))
	if kind == KindResume {
		entries := float64(strings.Count(tex, `\cventry{`))
		// A list line is a bullet or a competency line; an entry's own heading line costs one more.
		// The summary paragraph is left out because the capacities are calibrated without it.
		lines := float64(items+strings.Count(tex, `\cvitem{}{\textbf{`)) + entries
		content := 150 + 28*entries + orDefault(f.itemPt, 12)*lines
		if !strings.Contains(tex, `\itemsep0pt`) {
			// At the stock spacing a bullet takes 2pt more, as on the real
			// template; the spacing adjustment takes them back.
			content += tightSavingPt * float64(items)
		}
		return content, orDefault(f.resumeCap, 700)
	}
	return 200 + 40*float64(strings.Count(tex, `\lettercontent{`)) + 15*float64(items), orDefault(f.coverCap, 640)
}

func orDefault(v, d float64) float64 {
	if v == 0 {
		return d
	}
	return v
}

func (f *fakeBuilder) Build(ctx context.Context, in toolchain.BuildInput) (toolchain.BuildOutput, error) {
	if in.TeXSHA256 != "" && in.TeXSHA256 != sha(in.TeX) {
		return toolchain.BuildOutput{}, toolchain.ErrStaleSource
	}
	kind := Kind(in.Name)
	tex := string(in.TeX)
	content, capacity := f.heights(kind, tex)
	pages := int(math.Ceil(content / capacity))
	f.mu.Lock()
	call := fakeCall{N: len(f.calls) + 1, Kind: kind, SHA: sha(in.TeX), Tight: strings.Contains(tex, `\itemsep0pt`), Pages: pages, Hashed: in.TeXSHA256 != "", Content: content}
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	if f.hook != nil {
		if err := f.hook(ctx, call); err != nil {
			return toolchain.BuildOutput{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return toolchain.BuildOutput{}, toolchain.ErrCancelled
	}
	text := detex(tex)
	if f.dropText != "" {
		text = strings.ReplaceAll(text, f.dropText, "")
	}
	pdf := []byte("%PDF-1.7 fake " + call.SHA)
	pdfSHA, textSHA, pypdf := sha(pdf), sha([]byte(text)), "6.19.0"
	override := map[string]string{}
	if f.override != nil {
		override = f.override(call)
	}
	status := func(tool, id string, ok bool) toolchain.Check {
		st := toolchain.Pass
		if !ok {
			st = toolchain.Fail
		}
		if o, has := override[tool+"/"+id]; has {
			st = o
		}
		return toolchain.Check{ID: id, Status: st}
	}
	verdict := func(checks []toolchain.Check) string {
		for _, c := range checks {
			if c.Status == toolchain.Fail {
				return toolchain.Fail
			}
		}
		return toolchain.Pass
	}
	pdfChecks := []toolchain.Check{status(toolPDF, "pdf-readable", true), status(toolPDF, "text-layer", true)}
	if in.Expect.Pages > 0 {
		pdfChecks = append(pdfChecks, status(toolPDF, "page-count", pages == in.Expect.Pages))
	}
	if in.Expect.MaxPages > 0 {
		pdfChecks = append(pdfChecks, status(toolPDF, "max-pages", pages <= in.Expect.MaxPages))
	}
	if in.Expect.ASCIIDates {
		pdfChecks = append(pdfChecks, status(toolPDF, "ascii-dates", true))
	}
	var meas []toolchain.Measurement
	lastUsed := 0.0
	for p := 1; p <= pages; p++ {
		// A full page ends where the real template's text area does, and the
		// text flows line by line: a page has no room left but on the last.
		used := math.Min(capacity, content-float64(p-1)*capacity)
		top := resumePages[assets.PaperA4].bottomPt - capacity
		bottom, space := top+used, 842-top-used
		meas = append(meas, toolchain.Measurement{Page: p, HeightPt: 842, TextTopPt: &top, TextBottomPt: &bottom, BottomSpacePt: &space})
		lastUsed = used
	}
	layoutIDs := []string{"page-text", "page-size", "clipping", "text-overlap", "internal-hole", "early-page-end", "footer-collision", "final-page-thin", "orphaned-entry"}
	if kind == KindResume {
		layoutIDs = append(layoutIDs, "entry-gap")
	}
	var layoutChecks []toolchain.Check
	for _, id := range layoutIDs {
		ok := true
		if id == "final-page-thin" && pages > 1 && lastUsed < 0.35*capacity {
			ok = false
		}
		layoutChecks = append(layoutChecks, status(toolLayout, id, ok))
	}
	fonts := []toolchain.FontUse{{Font: "LMSans10-Regular", SizePt: 11, Letters: 900}}
	if kind == KindCover {
		fonts = []toolchain.FontUse{{Font: "Lato-Bold", SizePt: 30, Letters: 12}, {Font: "Raleway-Medium", SizePt: 11, Letters: 800}}
	}
	out := toolchain.BuildOutput{
		PDF: pdf, PDFSHA256: pdfSHA, TeXSHA256: sha(in.TeX), Passes: 1, Pages: pages, Text: text,
		Verify: toolchain.PDFResult{SchemaVersion: 1, Tool: "verify_pdf", ScriptVersion: "1", PDFSHA256: pdfSHA, Python: "3.14.7", Pypdf: &pypdf,
			Extractor: "pypdf", Pages: &pages, TextSHA256: &textSHA, FontUsage: fonts, Checks: pdfChecks, Overall: verdict(pdfChecks)},
		Layout: toolchain.LayoutResult{SchemaVersion: 1, Tool: "verify_layout", ScriptVersion: "2", Profile: in.Layout.ProfileID(), PDFSHA256: pdfSHA,
			Python: "3.14.7", Poppler: "pdftotext 26.01", Pages: &pages, Measurements: meas, Checks: layoutChecks, Overall: verdict(layoutChecks)},
	}
	for p := 1; p <= pages; p++ {
		name := string(kind) + "-page-" + itoa(p) + ".png"
		png := []byte("png " + call.SHA + " " + itoa(p))
		out.Previews = append(out.Previews, toolchain.Preview{Name: name, PNG: png, SHA256: sha(png)})
	}
	return out, nil
}

func (f *fakeBuilder) callsFor(k Kind) []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeCall
	for _, c := range f.calls {
		if c.Kind == k {
			out = append(out, c)
		}
	}
	return out
}
