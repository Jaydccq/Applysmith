package documents

import (
	"strings"
	"testing"
	"unicode"
)

func TestDocuments_Bundle_SlugAndRegenerationKeepsVersions(t *testing.T) {
	const date = "2026-10-11"
	// Characters of other scripts are built at run time.
	kanji, snowman := string([]rune{0x65e5, 0x672c}), string([]rune{0x2603})
	long := strings.Repeat("Distributed Systems ", 12)
	for _, c := range []struct {
		name          string
		company, role string
		mode          Mode
		want          string
	}{
		{"plain", "Example Corp", "Backend Engineer", ModeTailored, "example-corp-backend-engineer-" + date},
		{"punctuation collapses", "Example, Corp.", "Sr. Engineer (Platform) / SRE", ModeTailored, "example-corp-sr-engineer-platform-sre-" + date},
		{"non-ASCII letters are dropped", "Caf\u00e9 \u00dcber", "Ing\u00e9nieur " + kanji, ModeTailored, "caf-ber-ing-nieur-" + date},
		{"nothing usable", kanji, snowman, ModeTailored, "document-" + date},
		{"baseline", "", "", ModeBaseline, "baseline-" + date},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := bundleName(Manifest{Company: c.company, Role: c.role, Mode: c.mode}, date)
			if got != c.want {
				t.Fatalf("bundleName = %q, want %q", got, c.want)
			}
		})
	}
	got := bundleName(Manifest{Company: "Example Corp", Role: long, Mode: ModeTailored}, date)
	if len(got) > 80 || !strings.HasSuffix(got, "-"+date) || strings.Contains(got, "--") {
		t.Fatalf("a long name is cut to 80 characters before the date: %q (%d)", got, len(got))
	}
	for _, r := range got {
		if r > unicode.MaxASCII {
			t.Fatalf("bundle %q holds a non-ASCII character", got)
		}
	}

	// Regenerating on the same day adds a version to the same bundle and
	// leaves the first one as it was.
	f := newFixture(t, fixtureOpts{})
	req := Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting}
	first := f.run(req)
	second := f.run(req)
	a, err := f.svc.Get(t.Context(), first.Documents[0])
	must(t, err)
	b, err := f.svc.Get(t.Context(), second.Documents[0])
	must(t, err)
	if a.Bundle != b.Bundle || a.Version != 1 || b.Version != 2 || a.Path == b.Path {
		t.Fatalf("first %s/%d at %s, second %s/%d at %s", a.Bundle, a.Version, a.Path, b.Bundle, b.Version, b.Path)
	}
	if f.read(a.SourcePath) == "" {
		t.Error("version 1 is gone")
	}
}
