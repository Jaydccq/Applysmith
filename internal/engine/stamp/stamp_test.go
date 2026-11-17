package stamp

import (
	"regexp"
	"strings"
	"testing"
)

func TestOfAcceptsWhatAppendBuilds(t *testing.T) {
	out := Append([]byte("(() => {})();"))
	s, err := Of(out)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(s) || s != HashOf([]byte("(() => {})();")) {
		t.Fatalf("stamp %q", s)
	}
}

func TestOfRejectsDamage(t *testing.T) {
	good := Append([]byte("(() => {})();"))
	changedBody := append([]byte("x"), good...)
	changedTrailer := append([]byte{}, good...)
	changedTrailer[len(changedTrailer)-3] = '!'
	for name, in := range map[string][]byte{"empty": nil, "body changed": changedBody, "trailer changed": changedTrailer, "no trailer": []byte(strings.Repeat("x", 100))} {
		if _, err := Of(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
