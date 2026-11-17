package learn

import (
	"strings"
	"testing"
)

func TestAnswerBank_Signal_NonQuestionTable(t *testing.T) {
	notQuestions := []struct{ name, text string }{
		{"empty", ""}, {"blank", "   \n "},
		{"id selector", "#question_12345678 > div"}, {"class selector", ".field-label"},
		{"custom form response", "answers[custom_form_response_3]"}, {"nth child", "ul li:nth-child(2)"}, {"child combinator", "div > div span"},
		{"attributes path", "job_application_answers_attributes_0_text_value"},
		{"captcha", "Please complete the CAPTCHA"}, {"retype code", "Type the code shown below"}, {"anti spam", "Anti-spam question: what is 6 x 7"},
		{"chrome sign out", "Sign out"}, {"chrome profile", "Candidate Profile"}, {"chrome back", "Back to job posting"},
		{"chrome required", "* indicates a required field"}, {"chrome experience", "My Experience"}, {"chrome progress", "50 percent complete"},
		{"chrome pages", "Page 2/5 pages"}, {"chrome sms", "Reply STOP to opt out. Msg & data rates may apply"},
		{"framework id", "$$field_ref"}, {"question id", "question_30011834002"}, {"required id", "question_30011834002 *"},
		{"rti suffix", "answer_RTi4k2x"}, {"bracket path", "cards[7d1e4f9a][field0]"}, {"snake id", "field_group_3_value"},
		{"caps run", "firstNAME"}, {"date format", "MM/DD/YYYY"},
		{"select one", "--Select One--"}, {"select ellipsis", "Select..."}, {"please select", "Please select"}, {"dashes", "---"},
		{"three in a row", "Degree Degree Degree"},
		{"row company", "Company Name"}, {"row company required", "Company Name *"}, {"row position", "Position"}, {"row end date", "End Date"},
		{"row reason", "Reason for Leaving (optional)"}, {"row month", "From Month"}, {"row school", "School Name"},
		{"phone extension", "Phone Extension"}, {"ext", "Ext. *"}, {"skills prompt", "Type to Add Skills"}, {"search prompt", "Type to search"}, {"honeypot", "honeypot"},
		{"statement prose", strings.Repeat("The company is a leader in widgets and values every person on the team. ", 3)},
	}
	for _, c := range notQuestions {
		if !IsNonQuestion(c.text) {
			t.Errorf("%s: %q must not be learnable", c.name, c.text)
		}
	}
	questions := []string{
		"How did you hear about Example Corp?", "Are you legally authorized to work in the United States?", "Desired salary",
		"Please describe your experience with distributed systems.", "Do you have a degree in computer science?",
		"What is the company name you currently work for?", "Why Example Corp?", "Start date",
		"Do you have a phone extension we should dial?", "Type of degree",
	}
	for _, q := range questions {
		if IsNonQuestion(q) {
			t.Errorf("%q is a question", q)
		}
	}
}

func TestAnswerBank_Signal_OverScoopDetectors(t *testing.T) {
	for _, c := range []struct {
		name, text string
		want       bool
	}{
		{"token three in a row", "Country Country Country", true},
		{"punctuation between copies", "Country, country; country", true},
		{"wedged token", "State 7d1e4f9a State 7d1e4f9a State 7d1e4f9a", true},
		{"two copies are not enough", "Country Country", false},
		{"phrase doubled", "work authorization status work authorization status", true},
		{"phrase doubled with one interloper", "work authorization status 7 work authorization status", true},
		{"phrase with a uuid token", "your current employer 7d1e4f9a-aaaa-bbbb your current employer", true},
		{"short phrase of two words is not flagged", "full name full name please", false},
		{"six words are enough", "ab cd ef ab cd ef", true},
		{"five words are not", "ab cd ef ab cd", false},
		{"ordinary question", "Please describe the largest system you have operated and what you would change", false},
	} {
		if got := hasOverScoop(c.text); got != c.want {
			t.Errorf("%s: hasOverScoop(%q) = %v, want %v", c.name, c.text, got, c.want)
		}
	}
	// The phrase scan reads only the first 120 tokens, so a long consent blob
	// costs a bounded amount.
	long := strings.Repeat("unique", 1) + " " + strings.Repeat("alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu ", 1)
	for i := 0; i < 400; i++ {
		long += " w" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26))
	}
	long += " tail tail2 tail3 tail tail2 tail3"
	if hasOverScoop(long) {
		t.Fatal("a repeat beyond the first 120 tokens is not scanned")
	}
}

func TestAnswerBank_Signal_SubstantiveValue(t *testing.T) {
	for _, c := range []struct {
		name, q, v string
		want       bool
	}{
		{"words", "Preferred name", "Alex", true},
		{"number of years", "Years of experience", "5", false}, // a single character
		{"two digits", "Years of experience", "12", true},
		{"zip", "Postal code", "90210", true},
		{"year", "Graduation year", "2019", true},
		{"phone digits", "Mobile", "415 555 0100", false},
		{"formatted phone", "Mobile", "+1 (415) 555-0100", false},
		{"account number", "Reference number", "1234567", false},
		{"six digits is fine", "Reference number", "123456", true},
		{"captcha token", "Enter value", "a7k9x2", false},
		{"toggle on", "Subscribe", "on", false}, {"toggle n/a", "Visa", "n/a", false}, {"toggle true", "Agree", "True", false},
		{"retype question", "Type the code shown", "hello world", false},
		{"single char", "Middle initial", "J", false},
		{"short mixed code needs letters and digits", "Level", "senior", true},
	} {
		if got := IsSubstantiveValue(c.q, c.v); got != c.want {
			t.Errorf("%s: IsSubstantiveValue(%q, %q) = %v, want %v", c.name, c.q, c.v, got, c.want)
		}
	}
}

func TestAnswerBank_Signal_ValueContradictsLabel(t *testing.T) {
	essay := strings.Repeat("I enjoy building reliable systems and working with small teams. ", 3)
	for _, c := range []struct {
		name, label, value string
		want               bool
	}{
		{"essay in name", "First name", essay, true},
		{"url in name", "Full Name", "https://example.com/alex", true},
		{"email in name", "Name", "alex@example.com", true},
		{"number in name", "Last name", "12345", true},
		{"plain name", "First name", "Alex", false},
		{"essay in pronouns", "Pronouns", essay, true},
		{"number in city", "City", "90210", true},
		{"city name", "City", "Springfield", false},
		{"essay in phone", "Phone", essay, true},
		{"url in zip", "Zip code", "https://example.com", true},
		{"email in date", "Date", "alex@example.com", true},
		{"number in phone is fine", "Phone number", "4155550100", false},
		{"number in zip is fine", "Postal code", "90210", false},
		{"url in email field", "Email address", "https://example.com", true},
		{"number in email field", "E-mail", "123", true},
		{"email in email field", "Email", "alex@example.com", false},
		{"newline is an essay", "City", "line one\nline two", true},
		{"long label stays unjudged", strings.Repeat("Please tell us your city of residence. ", 3), essay, false},
		{"unrelated label stays unjudged", "Why Example Corp?", "https://example.com", false},
		{"company name stays unjudged", "Company name", "12345", false},
	} {
		if got := ValueContradictsLabel(c.label, c.value); got != c.want {
			t.Errorf("%s: ValueContradictsLabel(%q) = %v, want %v", c.name, c.label, got, c.want)
		}
	}
}
