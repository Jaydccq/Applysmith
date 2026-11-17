package match

import "testing"

func opts(labels ...string) []Option {
	out := make([]Option, len(labels))
	for i, l := range labels {
		out[i] = Option{Value: l, Label: l}
	}
	return out
}

func TestSanitize(t *testing.T) {
	const nbsp = "\xc2\xa0"
	const ideographic = "\xe3\x80\x80"
	cases := []struct {
		row, name string
		in        string
		options   []Option
		want      string
	}{
		{"IN-002", "clean sentence", "How many years of professional work experience do you have?", nil, "How many years of professional work experience do you have?"},
		{"IN-002", "short label", "Location", nil, "Location"},
		{"IN-002", "a reassurance after the question asks nothing", "How many years of professional software engineering experience do you have? Any range is fine!", nil, "How many years of professional software engineering experience do you have?"},
		{"IN-002", "a rough estimate is okay", "How many people did you manage? A rough estimate is okay.", nil, "How many people did you manage?"},
		{"IN-002", "a remark that asks something stays", "How many years? Any range above five is fine for this role", nil, "How many years? Any range above five is fine for this role"},
		{"IN-002", "required as first word", "Required certifications you hold?", nil, "Required certifications you hold?"},
		{"IN-002", "indeed colon asterisk", "Are you legally able to work here: *", nil, "Are you legally able to work here: *"},
		{"IN-002", "empty", "", nil, ""},
		{"IN-002", "a misspelt sponsorship is spelt whole", "Do you require sponorship?", nil, "Do you require sponsorship?"},
		{"IN-002", "a misspelt capital Sponsorship keeps its capital", "Sponsorhip required", nil, "Sponsorship required"},
		{"IN-002", "sponsorship and sponsored stay", "Is sponsorship sponsored?", nil, "Is sponsorship sponsored?"},
		// Poshmark (Ashby, live): "sponsorship for employement visa status".
		{"IN-002", "a misspelt employment is spelt whole", "Sponsorship for employement visa status? Employeement", nil, "Sponsorship for employment visa status? Employment"},
		// Customer.io (Greenhouse, live): "How did your hear about this job?".
		{"IN-002", "your before a bare verb is you", "How did your hear about this job?", nil, "How did you hear about this job?"},
		{"IN-002", "your before a noun stays", "Did your manager approve?", nil, "Did your manager approve?"},
		// CircleCI (Greenhouse, live): "the country(s) where the job is posted".
		{"IN-002", "a plural marker on country is the country", "Are you eligible to work in the Country(s) where the job is posted?", nil, "Are you eligible to work in the Country where the job is posted?"},
		{"IN-003", "named entities", "Are you 18 &amp; over &quot;today&quot;&nbsp;?", nil, `Are you 18 & over "today" ?`},
		{"IN-003", "other entities become a space", "Preferred&#39;s name&copy;", nil, "Preferred s name"},
		{"IN-003", "no lowercasing", "Do You Hold A Valid License?", nil, "Do You Hold A Valid License?"},
		{"IN-003", "in-word hyphens fold to ASCII", "Status (e.g., H\u20111B, F\u20101, OPT\u2012STEM)", nil, "Status (e.g., H-1B, F-1, OPT-STEM)"},
		{"IN-003", "nfkc fullwidth", "\xef\xbc\xb7\xef\xbd\x8f\xef\xbd\x92\xef\xbd\x8b history", nil, "Work history"},
		{"IN-004", "sap hint and tail", "One or more results available. Press Up or Down Arrow Keys to navigate the selection list. Have you previously been employed with Example Corp? No Selection 3:_input", nil, "Have you previously been employed with Example Corp?"},
		{"IN-004", "Workday's required-field error glued after the question", "Will you now or in the future require sponsorship for an employment-based visa status? Error : The field Will you now or in the future require sponsorship for an employment-based visa status? is required and must have a value.", nil, "Will you now or in the future require sponsorship for an employment-based visa status?"},
		{"IN-004", "hint with resize sentence", "One or more results available. Press Up or Down Arrow Keys to navigate the selection list. Press Alt + Arrow keys to resize the selection list. Preferred city 4:_input", nil, "Preferred city"},
		{"IN-004", "no sap markers", "Have you worked here before?", nil, "Have you worked here before?"},
		{"IN-005", "stars", "** Will you now or in the future require sponsorship?", nil, "Will you now or in the future require sponsorship?"},
		{"IN-005", "star then word", "*Ethnic Origin", nil, "Ethnic Origin"},
		{"IN-005", "ordinal", "Q5. Are you willing to relocate?", nil, "Are you willing to relocate?"},
		{"IN-005", "ordinal lowercase paren", "q12) Do you have a license?", nil, "Do you have a license?"},
		{"IN-005", "year quarter kept", "Q4 2025 revenue growth", nil, "Q4 2025 revenue growth"},
		{"IN-005", "mid sentence ordinal kept", "See note Q5. Are you ready?", nil, "See note Q5. Are you ready?"},
		{"IN-006", "linkedin ids", "Are you authorized to work here? Required Are you authorized to work here? text-entity-list-form-component-formElement-urn-li-example-multipleChoice", nil, "Are you authorized to work here?"},
		{"IN-006", "ids without duplicate", "Do you drive? urn:li:fsd_example:(1,2)", nil, "Do you drive?"},
		{"IN-006", "marker before newline", "Do you drive?\nurn-li-example-thing\nmore", nil, "Do you drive?"},
		{"IN-007", "guillemet id", "Pick a shift radio-group-\xc2\xabr1a\xc2\xbb extra", nil, "Pick a shift"},
		{"IN-007", "colon id", "Pick :r13: a shift", nil, "Pick a shift"},
		{"IN-007", "plain colon kept", "Note: Pick a shift", nil, "Note: Pick a shift"},
		{"IN-008", "badge duplicate", "Are you over 18? Required Are you over 18?", nil, "Are you over 18?"},
		{"IN-008", "trailing badge", "Middle Required", nil, "Middle"},
		{"IN-008", "different text after badge kept", "Notice Required Please read the policy below", nil, "Notice Required Please read the policy below"},
		{"IN-009", "smart apply envelope", "Are you eligible to work in this country? Back to Job PostingExample Role * Indicates a required field", nil, "Are you eligible to work in this country?"},
		{"IN-009", "bare select one keeps envelope", "Select One Back to Job Posting Example Role Indicates a required field", nil, "Back to Job Posting Example Role Indicates a"},
		{"IN-009", "needs both markers", "Are you eligible to work here? Back to Job Posting", nil, "Are you eligible to work here? Back to Job Posting"},
		{"IN-017", "adjacent repeat only", "Do you have experience with C and C++?", nil, "Do you have experience with C and C++?"},
		{"IN-010", "paylocity id", "OK to contact multiQuestionAnswer12_3 you?", nil, "OK to contact you?"},
		{"IN-010", "rails input name", "Are you authorized application_form[application][answers_attributes][11][boolean_answer] to work?", nil, "Are you authorized to work?"},
		{"IN-011", "camel id on duplicate text", "Do you consent to texts? Do you consent to texts? communicationConsent", nil, "Do you consent to texts?"},
		{"IN-011", "brand word kept", "Do you own an iPhone or an eBay account?", nil, "Do you own an iPhone or an eBay account?"},
		{"IN-012", "caption with value twice", "Set Your Location* Austin, Texas Set Your Location*Austin, Texas", nil, "Set Your Location*"},
		{"IN-012", "different value kept", "Set Your Location* Austin Set Your Location* Dallas", nil, "Set Your Location* Austin Set Your Location* Dallas"},
		{"IN-013", "repeated question with option echo", "OK to contact? OK to contact? Yes OK to contact? OK to contact?", opts("Yes", "No"), "OK to contact?"},
		{"IN-013", "interleaved prose kept", "Are you able to travel? Notes apply Are you able to travel? Sunday only", opts("Yes", "No"), "Are you able to travel? Notes apply Sunday only"},
		{"IN-013", "single copy untouched", "Are you able to travel for work?", nil, "Are you able to travel for work?"},
		{"IN-013", "short sentence untouched", "Why now? Why now?", nil, "Why now? Why now?"},
		{"IN-014", "snake tokens", "Willingness to Travel uniqname_16_0", nil, "Willingness to Travel"},
		{"IN-014", "system field token", "Veteran status __systemfield_eeoc_veteran_status", nil, "Veteran status"},
		{"IN-014", "sample email", "Contact email alex.example@example.com...", nil, "Contact email"},
		{"IN-015", "kebab input token", "Email address email-input", nil, "Email address"},
		{"IN-015", "autocomplete token", "First name* given-name", nil, "First name*"},
		{"IN-015", "form control", "Your phone number form-control", nil, "Your phone number"},
		{"IN-015", "export-control kept", "Is this an export-control matter?", nil, "Is this an export-control matter?"},
		{"IN-015", "prose hyphens kept", "Are you open to a full-time follow-up role?", nil, "Are you open to a full-time follow-up role?"},
		{"IN-015", "a hyphenated word the label does not echo kept", "If offered a position, what is your ideal start-date?", nil, "If offered a position, what is your ideal start-date?"},
		{"IN-015", "a date token echoing the label", "Start date start-date", nil, "Start date"},
		{"IN-016", "duplicate lead sentence with off badge", "What is your phone number today? off What is your phone number today?", nil, "What is your phone number today?"},
		{"IN-016", "trailing off", "Phone number off", nil, "Phone number"},
		{"IN-016", "single word before off kept", "Switch off", nil, "Switch off"},
		{"IN-017", "repeated short token", "Email* email", nil, "Email*"},
		{"IN-017", "long labels not deduped", "Do you currently work or intend to work, and if you work, where do you work now?", nil, "Do you currently work or intend to work, and if you work, where do you work now?"},
		{"IN-017", "numbers kept", "Rate 5 out of 5", nil, "Rate 5 out of 5"},
		{"IN-018", "asp.net name prefix", "ctl00$Main$genchk2 In some cases, a photograph may be taken.", nil, "In some cases, a photograph may be taken."},
		{"IN-018", "asp.net name suffix", "Are you authorized to work in the US? ctl00$Main$workvisa Select... Yes No Please enter a value", nil, "Are you authorized to work in the US?"},
		{"IN-019", "repeated caption", "Degree* Degree* Degree* Select One Degree Select One Required degree education-2", nil, "Degree*"},
		{"IN-019", "two copies reduce through token repeat", "Degree* Degree", nil, "Degree*"},
		{"IN-020", "leading select one", "Select One Select One Required I acknowledge that I have read the above", nil, "I acknowledge that I have read the above"},
		{"IN-020", "bracket variant", "Select One [Required] Please choose your office", nil, "Please choose your office"},
		{"IN-020", "prompt only stays for contentless check", "Select One Required", nil, ""},
		{"IN-021", "trailing select an option", "Degree Type * Select an option", nil, "Degree Type"},
		{"IN-021", "required field tail", "Middle Required field", nil, "Middle"},
		{"IN-021", "please select tail", "Which office do you prefer? Please select", nil, "Which office do you prefer?"},
		{"IN-021", "repeated tails", "Degree Type * Select an option Please select", nil, "Degree Type"},
		{"IN-022", "required parenthetical", "Email address (required)", nil, "Email address"},
		{"IN-022", "if applicable", "Preferred Name (if applicable)", nil, "Preferred Name"},
		{"IN-022", "typing prompt", "Enter Email...", nil, "Email"},
		{"IN-022", "what is untouched", "What is your preferred name?", nil, "What is your preferred name?"},
		{"IN-023", "glued yes", "Yes Are you currently subject to any agreement that limits your work?", opts("Yes", "No"), "Are you currently subject to any agreement that limits your work?"},
		{"IN-023", "longest label wins", "One week If presented with an offer, when could you start?", opts("One", "One week", "Two weeks"), "If presented with an offer, when could you start?"},
		{"IN-023", "yesterday kept", "Yesterday's onboarding was great, right?", opts("Yes", "No"), "Yesterday's onboarding was great, right?"},
		{"IN-023", "nothing meaningful left", "Yes", opts("Yes", "No"), "Yes"},
		{"IN-024", "uuid glued", "Veteran status 3f2b1c9a-1d2e-4f5a-8b9c-0123456789ab__systemfield_eeoc_veteran_status", nil, "Veteran status"},
		{"IN-024", "hex ids after envelope", "Are you eligible to work here? primaryQuestionnaire--0123456789abcdef0123", nil, "Are you eligible to work here?"},
		{"IN-024", "short workday id", "Preferred office ab12c9 location", nil, "Preferred office location"},
		{"IN-024", "react token", "Preferred office :r5: location", nil, "Preferred office location"},
		{"IN-024", "dash glue", "Engineer I - Software", nil, "Engineer I Software"},
		{"IN-084", "BOM preserves question boundaries", "Select\ufeffyour\ufefffuture team", nil, "Select your future team"},
		{"IN-084", "BOM separates prompt words", "\ufeffSelect\ufeffone\ufeff", nil, ""},
		{"IN-084", "unicode spaces", "Are" + nbsp + "you" + ideographic + "over 18?", nil, "Are you over 18?"},
		{"IN-084", "line break after leaked id", "Do you drive?\r\nurn-li-example-thing\r\nmore", nil, "Do you drive?"},
	}
	for _, c := range cases {
		t.Run(c.row+"/"+c.name, func(t *testing.T) {
			if got := Sanitize(c.in, c.options); got != c.want {
				t.Fatalf("Sanitize(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
