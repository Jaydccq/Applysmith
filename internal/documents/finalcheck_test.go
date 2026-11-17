package documents

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"applysmith/internal/documents/toolchain"
)

// layer is the text a healthy extraction of the document would give: the
// printed strings in reading order, with the contact icons' glyph names that
// moderncv leaves in front of the contact values.
func layer(e Expectation) string {
	return "Envelope " + e.Source
}

func fontsOf(rule fontRule) []toolchain.FontUse {
	if rule.single {
		return []toolchain.FontUse{{Font: "Raleway-Medium-Identity-H", SizePt: 11, Letters: 400}, {Font: "Lato-Light", SizePt: 24.8, Letters: 11}}
	}
	return []toolchain.FontUse{{Font: "LMSans10-Regular", SizePt: 10, Letters: 500}, {Font: "FontAwesome5Free-Solid", SizePt: 10, Letters: 9}}
}

// layerOutput wraps a text layer in the verdict fields the checks read.
func layerOutput(text string, use []toolchain.FontUse) toolchain.BuildOutput {
	h := sha256.Sum256([]byte(text))
	sum := hex.EncodeToString(h[:])
	pages := 1
	return toolchain.BuildOutput{Text: text, Verify: toolchain.PDFResult{Pages: &pages, TextSHA256: &sum, FontUsage: use}}
}

func status(t *testing.T, checks []toolchain.Check, id string) toolchain.Check {
	t.Helper()
	for _, c := range checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no %s check in %+v", id, checks)
	return toolchain.Check{}
}

func wantAll(t *testing.T, checks []toolchain.Check, want string) {
	t.Helper()
	if len(checks) != len(finalCheckIDs) {
		t.Fatalf("%d checks, want %d", len(checks), len(finalCheckIDs))
	}
	for i, c := range checks {
		if c.ID != finalCheckIDs[i] || c.Status != want {
			t.Errorf("check %d = %+v, want %s %s", i, c, finalCheckIDs[i], want)
		}
	}
}

func TestFinalChecks_AHealthyLayerPasses(t *testing.T) {
	for name, e := range map[string]Expectation{"résumé": expectResume(fixtureResume()), "letter": expectCover(fixtureCover())} {
		t.Run(name, func(t *testing.T) {
			wantAll(t, FinalChecks(e, layerOutput(layer(e), fontsOf(e.Fonts))), toolchain.Pass)
		})
	}
}

func TestVerify_EmailMissingDespiteWordCoverage(t *testing.T) {
	e := expectResume(fixtureResume())
	text := strings.ReplaceAll(layer(e), "alex.example@example.com", "")
	checks := FinalChecks(e, layerOutput(text, fontsOf(e.Fonts)))
	words := status(t, checks, CheckWords)
	if words.Status != toolchain.Pass {
		t.Fatalf("the words are still preserved: %+v", words)
	}
	c := status(t, checks, CheckContact)
	if c.Status != toolchain.Fail || !strings.Contains(c.Reason, "email") {
		t.Fatalf("contact = %+v, want a failure naming the email", c)
	}
	if state, _ := decide(tag(toolFinal, checks)); state != PDFFailed {
		t.Errorf("state = %s, want failed", state)
	}
}

// moderncv's banking style tied the names with a body-font space, which the
// extractors read as no space at all: an applicant tracking system took
// "JordanAvery" for the first name.
func TestVerify_NameRunTogetherIsMissing(t *testing.T) {
	e := expectResume(fixtureResume())
	text := strings.Replace(layer(e), "Alex Example", "AlexExample", 1)
	c := status(t, FinalChecks(e, layerOutput(text, fontsOf(e.Fonts))), CheckContact)
	if c.Status != toolchain.Fail || !strings.Contains(c.Reason, "the name as separate words") {
		t.Errorf("contact = %+v, want a failure naming the run-together name", c)
	}
	// A line break between the names is a word break.
	text = strings.Replace(layer(e), "Alex Example", "Alex\nExample", 1)
	if c := status(t, FinalChecks(e, layerOutput(text, fontsOf(e.Fonts))), CheckContact); c.Status != toolchain.Pass {
		t.Errorf("contact = %+v, want a pass", c)
	}
}

func TestVerify_EmailFusedToItsNeighbourIsMissing(t *testing.T) {
	e := expectResume(fixtureResume())
	text := "Envelopealex.example@example.com " + strings.ReplaceAll(layer(e), "alex.example@example.com", "")
	if c := status(t, FinalChecks(e, layerOutput(text, fontsOf(e.Fonts))), CheckContact); c.Status != toolchain.Fail {
		t.Errorf("an address glued to icon text is not a clean token: %+v", c)
	}
}

func TestVerify_NameContactAndReadingOrder_AreMandatory(t *testing.T) {
	e := expectResume(fixtureResume())
	base := layer(e)
	tests := []struct {
		name, id string
		text     string
	}{
		{"name", CheckContact, strings.ReplaceAll(base, "Alex Example", "")},
		{"phone", CheckContact, strings.ReplaceAll(base, "+1 555 0100", "")},
		{"address", CheckContact, strings.ReplaceAll(base, "100 Example Street, Sampleville", "")},
		{"sections reordered", CheckOrder, moveSection(base, "Education", "Professional Experience")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if c := status(t, FinalChecks(e, layerOutput(tc.text, fontsOf(e.Fonts))), tc.id); c.Status != toolchain.Fail {
				t.Errorf("%+v, want fail", c)
			}
		})
	}
}

// moveSection puts the heading `from` before the heading `before`, which is
// what a page flow bug does to a reading order.
func moveSection(text, from, before string) string {
	text = strings.Replace(text, from, "", 1)
	return strings.Replace(text, before, from+"\n"+before, 1)
}

func TestVerify_WordPreservation90Percent(t *testing.T) {
	e := expectResume(fixtureResume())
	words := strings.Fields(e.Source)
	for _, tc := range []struct {
		name   string
		drop   int // distinct source words removed from the layer
		status string
	}{{"none", 0, toolchain.Pass}, {"few", 2, toolchain.Pass}, {"most", 40, toolchain.Fail}} {
		t.Run(tc.name, func(t *testing.T) {
			text, dropped := layer(e), 0
			for _, w := range words {
				if dropped >= tc.drop || strings.ContainsAny(w, "@+") || len(w) < 6 {
					continue
				}
				if strings.Contains(text, w) {
					text, dropped = strings.ReplaceAll(text, w, ""), dropped+1
				}
			}
			c := status(t, FinalChecks(e, layerOutput(text, fontsOf(e.Fonts))), CheckWords)
			if c.Status != tc.status {
				t.Errorf("%d words removed: %+v, want %s", dropped, c, tc.status)
			}
		})
	}
}

func TestVerify_WordsSurviveKerningSplitsAndHyphenation(t *testing.T) {
	e := expectCover(fixtureCover())
	text := strings.NewReplacer("Hiring Team", "Hiring T eam", "Platform Engineer", "Plat-\nform Engi- neer").Replace(layer(e))
	wantAll(t, FinalChecks(e, layerOutput(text, fontsOf(e.Fonts))), toolchain.Pass)
}

func TestVerify_UnavailableIsUnverified(t *testing.T) {
	e := expectResume(fixtureResume())
	noPages := layerOutput(layer(e), nil)
	noPages.Verify.Pages = nil
	staleText := layerOutput(layer(e), fontsOf(e.Fonts))
	staleText.Text += "edited after hashing"
	for name, o := range map[string]toolchain.BuildOutput{"extractor failed": noPages, "text from another run": staleText} {
		t.Run(name, func(t *testing.T) {
			wantAll(t, FinalChecks(e, o), toolchain.Unverified)
			if state, _ := decide(tag(toolFinal, FinalChecks(e, o))); state != PDFUnverified {
				t.Errorf("state = %s", state)
			}
		})
	}
	if c := status(t, FinalChecks(e, layerOutput(layer(e), nil)), CheckFonts); c.Status != toolchain.Unverified {
		t.Errorf("unreadable font runs must leave the font check unverified: %+v", c)
	}
	if c := status(t, FinalChecks(Expectation{}, layerOutput("x", nil)), CheckContact); c.Status != toolchain.Unverified {
		t.Errorf("no source email to look for must not pass: %+v", c)
	}
}

func TestVerify_GlyphCorruption(t *testing.T) {
	e := expectResume(fixtureResume())
	for name, bad := range map[string]string{"replacement": "Senior � Engineer", "cid": "Senior (cid:42) Engineer", "private use": "Senior  Engineer", "control": "Senior \x01 Engineer"} {
		t.Run(name, func(t *testing.T) {
			if c := status(t, FinalChecks(e, layerOutput(layer(e)+"\n"+bad, fontsOf(e.Fonts))), CheckGlyphs); c.Status != toolchain.Fail {
				t.Errorf("%+v, want fail", c)
			}
		})
	}
}

func TestVerify_PercentAndDateEncoding(t *testing.T) {
	e := expectResume(fixtureResume())
	for _, want := range []string{"40%", "99.9%", "2021-03 - Present", "2018-2021", "2011-2015"} {
		if !contains(e.Percents, want) && !contains(e.Dates, want) {
			t.Fatalf("the expectation lacks %q: %v %v", want, e.Percents, e.Dates)
		}
	}
	tests := []struct{ name, id, from, to string }{
		{"percent sign lost", CheckPercent, "40%", "40"},
		{"decimal percent lost", CheckPercent, "99.9%", "99.9"},
		{"en dash in a range", CheckDates, "2018-2021", "2018–2021"},
		{"double hyphen kept", CheckDates, "2018-2021", "2018--2021"},
		{"range split across a break", CheckDates, "2021-03 - Present", "2021-03 -\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text := strings.ReplaceAll(layer(e), tc.from, tc.to)
			if c := status(t, FinalChecks(e, layerOutput(text, fontsOf(e.Fonts))), tc.id); c.Status != toolchain.Fail {
				t.Errorf("%+v, want fail", c)
			}
		})
	}
	// A space TeX or the extractor adds around a sign is not a loss.
	text := strings.ReplaceAll(layer(e), "40%", "40 %")
	if c := status(t, FinalChecks(e, layerOutput(text, fontsOf(e.Fonts))), CheckPercent); c.Status != toolchain.Pass {
		t.Errorf("%+v", c)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestVerify_FontRules(t *testing.T) {
	res, cov := expectResume(fixtureResume()), expectCover(fixtureCover())
	tests := []struct {
		name string
		e    Expectation
		use  []toolchain.FontUse
		want string
	}{
		{"letter in the template fonts", cov, fontsOf(coverFonts), toolchain.Pass},
		{"cover list in the main font", cov, append(fontsOf(coverFonts), toolchain.FontUse{Font: "Lato-Light", SizePt: 10, Letters: 120}), toolchain.Fail},
		{"cover text in a foreign font", cov, append(fontsOf(coverFonts), toolchain.FontUse{Font: "LMRoman10-Regular", SizePt: 11, Letters: 60}), toolchain.Fail},
		{"résumé in the template fonts", res, fontsOf(resumeFonts), toolchain.Pass},
		{"résumé list in a serif font", res, append(fontsOf(resumeFonts), toolchain.FontUse{Font: "LMRoman10-Regular", SizePt: 10, Letters: 80}), toolchain.Fail},
		{"a stray glyph in another font is noise", res, append(fontsOf(resumeFonts), toolchain.FontUse{Font: "LMRoman10-Regular", SizePt: 10, Letters: 1}), toolchain.Pass},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if c := status(t, FinalChecks(tc.e, layerOutput(layer(tc.e), tc.use)), CheckFonts); c.Status != tc.want {
				t.Errorf("%+v, want %s", c, tc.want)
			}
		})
	}
}

func TestDecide_FailureOutranksUnverifiedAndEmptyIsNotAPass(t *testing.T) {
	pass := toolchain.Check{ID: "a", Status: toolchain.Pass}
	unv := toolchain.Check{ID: "b", Status: toolchain.Unverified, Reason: "no pdftotext"}
	fail := toolchain.Check{ID: "c", Status: toolchain.Fail, Reason: "x"}
	all := func(pdf, layout, final []toolchain.Check) []RecordedCheck {
		return append(append(tag(toolPDF, pdf), tag(toolLayout, layout)...), tag(toolFinal, final)...)
	}
	P := []toolchain.Check{pass}
	tests := []struct {
		name   string
		checks []RecordedCheck
		want   string
	}{
		{"all pass", all(P, P, P), PDFReady},
		{"one unverified", all(P, []toolchain.Check{unv}, P), PDFUnverified},
		{"one failed", all(P, P, []toolchain.Check{fail}), PDFFailed},
		{"failed and unverified", all([]toolchain.Check{unv}, P, []toolchain.Check{fail}), PDFFailed},
		{"no checks at all", nil, PDFUnverified},
		{"a verifier with no checks", all(P, nil, P), PDFUnverified},
		{"the final checks never ran", all(P, P, nil), PDFUnverified},
		{"an unknown status", all(P, P, []toolchain.Check{{ID: "a", Status: "skipped"}}), PDFUnverified},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := decide(tc.checks); got != tc.want {
				t.Errorf("state = %s, want %s", got, tc.want)
			}
		})
	}
}
