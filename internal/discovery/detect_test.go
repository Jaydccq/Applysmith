package discovery

import (
	"slices"
	"testing"
)

func TestNameSlugs_JoinedAndHyphenatedWithAndWithoutALegalSuffix(t *testing.T) {
	cases := map[string][]string{
		"Example Co":        {"exampleco", "example-co", "example"},
		"Sample Labs, Inc.": {"samplelabsinc", "sample-labs-inc", "samplelabs", "sample-labs"},
		"Acme":              {"acme"},
		"!!":                nil,
	}
	for name, want := range cases {
		if got := nameSlugs(name); !slices.Equal(got, want) {
			t.Errorf("%q: %v, want %v", name, got, want)
		}
	}
}

func TestLooksLikeAddress_SeparatesAnAddressFromAName(t *testing.T) {
	for in, want := range map[string]bool{
		"https://jobs.example.com/x": true, "careers.example.org/jobs": true, "example.com": true,
		"Example Co": false, "Example Co.": false, "Acme": false,
	} {
		if got := looksLikeAddress(in); got != want {
			t.Errorf("%q: %v, want %v", in, got, want)
		}
	}
}
