package learn

import (
	"slices"
	"strings"
	"testing"
)

func TestClassifyWithheldControls(t *testing.T) {
	for _, c := range []struct {
		name, q, kind, inputType, autocomplete string
		options                                []string
		want                                   Withheld
	}{
		{"password input", "Account secret", "text", "password", "", nil, WithheldPassword},
		{"password label", "Create a password", "text", "", "", nil, WithheldPassword},
		{"otp label", "Enter the one-time code we sent you", "text", "", "", nil, WithheldOneTimeCode},
		{"otp autocomplete", "Your details", "text", "", "one-time-code", nil, WithheldOneTimeCode},
		{"verification code", "Verification code", "text", "", "", nil, WithheldOneTimeCode},
		{"card number", "Credit card number", "text", "", "", nil, WithheldPayment},
		{"cvv", "CVV", "text", "", "", nil, WithheldPayment},
		{"cc autocomplete", "Number", "text", "", "cc-number", nil, WithheldPayment},
		{"iban", "IBAN", "text", "", "", nil, WithheldPayment},
		{"swift code", "SWIFT code", "text", "", "", nil, WithheldPayment},
		{"swift and bic", "SWIFT/BIC", "text", "", "", nil, WithheldPayment},
		{"card expiry", "Card expiry date", "text", "", "", nil, WithheldPayment},
		{"card expiration", "Credit card expiration date", "text", "", "", nil, WithheldPayment},
		{"swift the language", "How many years of Swift experience do you have?", "text", "", "", nil, WithheldNone},
		{"an authorization's expiry", "Work authorization expiration date", "text", "", "", nil, WithheldNone},
		{"an expiry with nothing beside it", "Expiry date", "text", "", "", nil, WithheldPayment},
		{"an expiration with nothing beside it", "Expiration Date *", "text", "", "", nil, WithheldPayment},
		{"a visa's expiry", "When does your visa expire? Please give the expiry date.", "text", "", "", nil, WithheldNone},
		{"ssn", "Social Security Number", "text", "", "", nil, WithheldGovernmentID},
		{"passport", "Passport number", "text", "", "", nil, WithheldGovernmentID},
		{"license", "Driver's license number", "text", "", "", nil, WithheldGovernmentID},
		{"gender", "Gender", "select", "", "", []string{"Female", "Male", "Non-binary", "Decline to self-identify"}, WithheldSelfID},
		{"gender label only", "What is your gender identity?", "text", "", "", nil, WithheldSelfID},
		{"veteran", "Veteran status", "select", "", "", nil, WithheldSelfID},
		{"disability", "Do you have a disability?", "radio-group", "", "", nil, WithheldSelfID},
		{"race", "Race / Ethnicity", "select", "", "", nil, WithheldSelfID},
		{"dob", "Date of birth", "text", "", "", nil, WithheldDateOfBirth},
		{"dob autocomplete", "Your details", "text", "", "bday", nil, WithheldDateOfBirth},
		{"first name", "First name", "text", "", "", nil, WithheldProfileIdentity},
		{"full name sentence", "What is your full legal name?", "text", "", "", nil, WithheldProfileIdentity},
		{"email", "Email address", "text", "", "", nil, WithheldProfileIdentity},
		{"phone input type", "Your details", "text", "tel", "", nil, WithheldProfileIdentity},
		{"email input type", "Your details", "text", "email", "", nil, WithheldProfileIdentity},
		{"mobile", "Mobile number", "text", "", "", nil, WithheldProfileIdentity},
		{"street", "Street address", "text", "", "", nil, WithheldProfileIdentity},
		{"name autocomplete", "Your details", "text", "", "given-name", nil, WithheldProfileIdentity},
		{"reference email is someone else", "Reference email", "text", "email", "", nil, WithheldNone},
		{"referrer name is someone else", "Referrer's name", "text", "", "", nil, WithheldNone},
		{"company name", "Company name", "text", "", "", nil, WithheldNone},
		{"city", "City", "text", "", "", nil, WithheldNone},
		{"zip", "Postal code", "text", "", "", nil, WithheldNone},
		{"free text", "Why do you want to work here?", "text", "", "", nil, WithheldNone},
		{"captcha", "Type the code shown", "text", "", "", nil, WithheldNone},
		{"years", "Years of experience with Go", "text", "", "", nil, WithheldNone},
		{"work authorization is a bank fact", "Are you legally authorized to work?", "radio-group", "", "", []string{"Yes", "No"}, WithheldNone},
		{"a-number", "USCIS A-Number", "text", "", "", nil, WithheldGovernmentID},
		{"your a number", "Your A number, if any", "text", "", "", nil, WithheldGovernmentID},
		{"alien number", "Alien registration number", "text", "", "", nil, WithheldGovernmentID},
		// Thumbtack (Ashby): "remote work in a number of locations" is no A-Number.
		{"a number of", "Thumbtack currently supports remote work in a number of locations across the United States, Ontario and the Philippines. Using the drop down list below, please select the location you intend to work from.", "custom-listbox", "", "", nil, WithheldNone},
	} {
		if got := Classify(c.q, c.kind, c.inputType, c.autocomplete, c.options); got != c.want {
			t.Errorf("%s: Classify = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestHostOfKeepsOnlyAHostName(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"jobs.example.com", "jobs.example.com"},
		{"Jobs.Example.COM.", "jobs.example.com"},
		{"https://jobs.example.com/apply/1?token=abc#x", "jobs.example.com"},
		{"https://user:pw@jobs.example.com:8443/a", "jobs.example.com"},
		{"jobs.example.com:8443", "jobs.example.com"},
		{"jobs.example.com/apply?x=1", "jobs.example.com"},
		{"", ""}, {"not a host", ""}, {"-bad.example.com", ""},
	} {
		if got := HostOf(c.in); got != c.want {
			t.Errorf("HostOf(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// In every language an expiry date is a card's only when the label holds
// nothing else, as English "Expiry date" is; a label that names what expires
// (a visa, a work authorization) is an ordinary question.
func TestWithheld_Terms_ABareExpiryDateIsACardsInEveryLanguage(t *testing.T) {
	var payment termGroup
	for _, g := range termGroups {
		if g.withheld == WithheldPayment {
			payment = g
		}
	}
	for _, lang := range languages {
		var labels []string
		for _, raw := range payment.terms[lang] {
			if strings.HasPrefix(raw, "^") {
				labels = append(labels, strings.TrimSuffix(strings.TrimPrefix(raw, "^"), "*"))
			}
		}
		// English reads the bare label in classify; Chinese, Japanese and
		// Korean list no expiry term.
		if len(labels) == 0 && !slices.Contains([]string{"en", "zh", "ja", "ko"}, lang) {
			t.Errorf("%s lists no bare expiry label", lang)
		}
		for _, label := range labels {
			if w, _ := classify(label+" *", "text", "", "", nil); w != WithheldPayment {
				t.Errorf("%s: the bare label %q is %q", lang, label, w)
			}
			for _, beside := range []string{" visa", " visa 2027"} {
				if w, _ := classify(label+beside, "text", "", "", nil); w != WithheldNone {
					t.Errorf("%s: %q beside what expires is %q", lang, label+beside, w)
				}
			}
		}
	}
}
