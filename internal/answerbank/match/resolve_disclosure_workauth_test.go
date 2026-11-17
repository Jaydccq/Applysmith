package match

import "testing"

// TestReadCompound: exactly one work-authorization clause and one sponsorship
// clause joined by "and"; a time qualifier's "and" is not a conjunction.
func TestReadCompound(t *testing.T) {
	cases := []struct {
		text    string
		ok      bool
		authNeg bool
		first   bool
	}{
		{"Are you legally authorized to work in the US and will you now or in the future require sponsorship?", true, false, true},
		{"Are you authorized to work in the US and require sponsorship?", true, false, true},
		{"Do you need a visa and are you authorized to work in the US?", true, false, false},
		{"Are you not authorized to work in the US and will you require sponsorship?", true, true, true},
		{"Are you authorized to work in the US and do you require sponsorship now and in the future?", true, false, true},
		// Avride (Greenhouse, live): a statement, with "current or future" as the one time qualifier.
		{"I am authorized to work in the United States and do not require current or future visa sponsorship.", true, false, true},
		{"Do you require sponsorship now and in the future?", false, false, false},
		{"Are you authorized to work in the US and eligible to work in the UK?", false, false, false},
		{"Do you require sponsorship and will you relocate?", false, false, false},
		{"Are you authorized to work in the US without restriction and will you require sponsorship?", false, false, false},
	}
	for _, c := range cases {
		got, ok := readCompound(c.text, "")
		if ok != c.ok || (ok && (got.authNeg != c.authNeg || got.authFirst != c.first)) {
			t.Errorf("%q: %+v ok=%t", c.text, got, ok)
		}
	}
}
