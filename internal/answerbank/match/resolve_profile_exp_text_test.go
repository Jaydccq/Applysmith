package match

import "testing"

func TestAnswerBank_TextAnswer_ListCardinalityTrim(t *testing.T) {
	cases := []struct {
		name, q, answer, want string
	}{
		{"top three", "Please list your top three programming languages", "Go, Python, Rust, C, Java", "Go, Python, Rust"},
		{"digits", "List 2 of your favorite tools", "a, b, c", "a, b"},
		{"already short", "Name your top 5 skills", "a, b", "a, b"},
		{"not a list verb", "3 years of experience with tools", "a, b, c, d", "a, b, c, d"},
		{"section number", "Section 3 of the form", "a, b, c, d", "a, b, c, d"},
		{"out of range", "List your top 12 languages", "a, b, c", "a, b, c"},
		{"single value", "List your top three languages", "Go", "Go"},
		{"blank items dropped on trim", "Share your top two picks", "a, , b, c", "a, b"},
		{"sentences are not items", "Please share 2-3 sentences on why you want this role", "I admire the mission, the engineering culture, and the product.", "I admire the mission, the engineering culture, and the product."},
		{"months are not items", "What would you do in your first 3 months?", "ship, learn, mentor, lead", "ship, learn, mentor, lead"},
		{"prose is not a list", "Give one reason you want to join", "The mission, which I have followed for years, matches my work", "The mission, which I have followed for years, matches my work"},
		{"a sentence is not a list", "Provide one example", "Led a migration, on time.", "Led a migration, on time."},
		{"a city and its state are one item", "List your top 2 preferred locations", "San Francisco, CA, New York, NY", "San Francisco, CA, New York, NY"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := finishTextAnswer(&Entry{ID: "programming-languages-used"}, Question{Text: c.q}, c.answer)
			if got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
	if n := requestedListCardinality("Please list your top three languages"); n != 3 {
		t.Fatalf("cardinality = %d", n)
	}
}

// evlo: the bank's "No" for licenses typed into "list any you currently hold"
// read as a refusal to answer; the list it means is empty.
func TestAnswerBank_TextAnswer_ANoToAListAskIsNone(t *testing.T) {
	cases := []struct{ q, answer, want string }{
		{"Licenses and certifications (optional) — list any you currently hold professionally so we can mention them to employers.", "No", "None"},
		{"Which certifications do you hold?", "no.", "None"},
		{"Please list any licenses you hold", "Yes", "Yes"},
		{"Do you hold a professional license?", "No", "No"},
		{"Please explain any gaps in your employment", "No", "No"},
	}
	for _, c := range cases {
		if got, _ := finishTextAnswer(&Entry{ID: "professional-licenses"}, Question{Text: c.q}, c.answer); got != c.want {
			t.Errorf("%q with %q: got %q want %q", c.q, c.answer, got, c.want)
		}
	}
}

func TestAnswerBank_NumericThreshold_DaysPerWeekExtractor(t *testing.T) {
	cases := []struct {
		q    string
		want int
		ok   bool
	}{
		{"This is a full-time onsite role", 5, true},
		{"You will be in the office five days a week", 5, true},
		{"Monday to Friday in office", 5, true},
		{"Monday - Friday", 5, true},
		{"We expect 2-3 days in the office", 3, true},
		{"We expect three to five days in the office", 5, true},
		{"3-5 days", 5, true},
		{"at least 3 days per week", 3, true},
		{"a minimum of two days", 2, true},
		{"Yes - regularly (2+ days per week)", 2, true}, // Rocket Money: "N+" is a floor
		{"up to 4 days a week onsite", 4, true},
		{"no more than one day", 1, true},
		{"on site 3x per week", 3, true},
		{"on site two times a week", 0, false},
		{"on site 2x/week", 2, true},
		{"a 4-day office week", 4, true},
		{"onsite 3 days", 3, true},
		{"stone to three days", 3, true},
		{"20 days a month", 0, false},
		{"8 days", 0, false},
		{"no schedule given", 0, false},
	}
	for _, c := range cases {
		t.Run(c.q, func(t *testing.T) {
			got, rule, ok := extractDaysPerWeek(c.q)
			if ok != c.ok || got != c.want || (ok && rule == "") {
				t.Fatalf("got %d %q %v want %d %v", got, rule, ok, c.want, c.ok)
			}
		})
	}
	t.Run("a digit outside 0..7 is a miss and the lane continues", func(t *testing.T) {
		got, _, ok := extractDaysPerWeek("8-9 days of training, then at least 3 days onsite")
		if !ok || got != 3 {
			t.Fatalf("got %d %v", got, ok)
		}
	})
}
