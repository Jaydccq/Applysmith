package engine

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"applysmith/internal/engine/bundler"
)

const regenerate = "run `make generate` (or `go generate ./internal/engine`)"

// A bundle generated from older sources would make the stamp lie.
func TestEmbeddedBundleIsCurrent(t *testing.T) {
	fresh, err := bundler.Build("../../engine")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fresh, Bundle()) {
		t.Fatal("embedded bundle differs from the engine sources; " + regenerate)
	}
}

func TestMainWorldHelperCarriesPlaceholders(t *testing.T) {
	b := MainWorldBundle()
	if len(b) == 0 {
		t.Fatal("no embedded helper; " + regenerate)
	}
	for _, ph := range []string{"__AJ_NONCE__", "__AJ_STAMP__"} {
		if n := bytes.Count(b, []byte(ph)); n != 1 {
			t.Fatalf("%s appears %d times in the helper", ph, n)
		}
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(MainWorldStamp()) {
		t.Fatalf("helper stamp %q", MainWorldStamp())
	}
	p := &Page{nonce: "abc123"}
	src, err := p.helperSource()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(src, "__AJ_") || !strings.Contains(src, `"abc123"`) || !strings.Contains(src, MainWorldStamp()) {
		t.Fatal("placeholders were not replaced")
	}
}

func TestEmbeddedHelperIsCurrent(t *testing.T) {
	fresh, err := bundler.BuildMainWorld("../../engine")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fresh, MainWorldBundle()) {
		t.Fatal("embedded helper differs from the engine sources; " + regenerate)
	}
}

func TestHelperHoldsNoEngineGlobalAndNoBinding(t *testing.T) {
	b := string(MainWorldBundle())
	for _, banned := range []string{"__applysmithEngine", "__ajMatch", "__ajResume", "__ajTerminal", "applicant"} {
		if strings.Contains(b, banned) {
			t.Fatalf("the helper mentions %q", banned)
		}
	}
}
