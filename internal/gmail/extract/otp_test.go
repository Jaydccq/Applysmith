package extract

import (
	"strings"
	"testing"
)

func TestGmail_ExtractCode_Scenarios(t *testing.T) {
	for _, c := range []struct {
		name, body, subject, want string
	}{
		{"digit code after the noun", "Your verification code: 482913", "", "482913"},
		{"alphanumeric code in a context phrase", "Enter the security code on your application: Ab3kQ9xZ", "", "Ab3kQ9xZ"},
		{"candidate before the noun", "482913 is the code", "", "482913"},
		{"same code twice", "Your code is 482913. If asked for a second code, use code 482913.", "", "482913"},
		{"copyright year near the noun", "After you enter the code, resubmit your application. (c) 2026. " + strings.Repeat("Footer text goes here. ", 6) + "Enter the security code field: Ab3kQ9xZ", "", "Ab3kQ9xZ"},
		{"two distinct candidates", "Your verification code 482913 and also code 771204", "", ""},
		{"long number and url fragments", "Order 1234567890 shipped. Track: https://t.example.com/a/9482615 for your verification code.", "", ""},
		{"code in its own layout cell", "Verification code" + strings.Repeat(" ", 300) + "482913" + strings.Repeat(" ", 300) + "This code expires in 10 minutes. Enter this code to access ADP services. 3f1e2fbd-b47d-4821-b93e-12baa9cfae (c)2026 ADP, Inc.", "Here's your verification code from ADP", "482913"},
		{"ordinary words", "Your Application at Example needs a verification code. Application Example", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Code(c.body, c.subject); got != c.want {
				t.Fatalf("Code = %q, want %q", got, c.want)
			}
		})
	}
}

func TestGmail_ExtractCode_SubjectCounts(t *testing.T) {
	if got := Code("Welcome aboard, nothing else here.", "7KLFJY is your verification code"); got != "7KLFJY" {
		t.Fatalf("subject code = %q", got)
	}
	if got := Code("", "Your code 482913"); got != "482913" {
		t.Fatalf("subject-only = %q", got)
	}
}

func TestGmail_ExtractCode_BlanksLinksEntitiesAddresses(t *testing.T) {
	for name, body := range map[string]string{
		"tracking link tail":   "Your verification code is below. https://click.example.com/track/lR2d3uMU9 Thanks",
		"entity spacer":        "Your verification code: &#8199;&#8199; (see mail)",
		"mailbox local part":   "Your verification code was sent to jbxkq1@example.com today.",
		"www link":             "Use the security code at www.example.com/reset/4831926 if asked",
		"subdomain with digit": "Your access code lives at https://a1b2c3d4.example.com",
	} {
		if got := Code(body, ""); got != "" {
			t.Errorf("%s: noise supplied the candidate %q", name, got)
		}
	}
	// The real code survives next to noise, and offsets stay valid.
	if got := Code("Visit https://example.com/x?y=1234567 and use your verification code 482913.", ""); got != "482913" {
		t.Fatalf("code beside a link = %q", got)
	}
}

func TestGmail_ExtractCode_DigitBounds(t *testing.T) {
	for body, want := range map[string]string{
		"verification code 1234":        "1234",
		"verification code 12345678":    "12345678",
		"verification code 123456789":   "",
		"verification code 123":         "",
		"verification code 1d92558f":    "1d92558f",
		"verification code a1b2c3d4e5f": "",
	} {
		if got := Code(body, ""); got != want {
			t.Errorf("Code(%q) = %q, want %q", body, got, want)
		}
	}
	// The digits of a mixed token are not sliced out: the whole token is the code.
	if got := Code("verification code 482913abc", ""); got != "482913abc" {
		t.Errorf("digits inside a longer alphanumeric token were sliced: %q", got)
	}
}

func TestGmail_ExtractCode_MixedAlphanumeric(t *testing.T) {
	for body, want := range map[string]string{
		"Your code is ToufLc6k":            "ToufLc6k",
		"7KLFJY is your verification code": "7KLFJY",
		"Verification code: ab12":          "ab12",
		"Verification code: abcdefghij11":  "",
		"Verification code: Ab3":           "",
	} {
		if got := Code(body, ""); got != want {
			t.Errorf("Code(%q) = %q, want %q", body, got, want)
		}
	}
}

func TestGmail_ExtractCode_MixedCaseNotWordShaped(t *testing.T) {
	if got := Code("Enter the security code field on your application: FFDaDNWA", ""); got != "FFDaDNWA" {
		t.Fatalf("letters-only mixed case code = %q", got)
	}
	for _, w := range []string{"Jordan", "GitLab", "DoorDash", "iPhone", "McDonald", "Application", "Example"} {
		if strong, weak := classify(w); strong || weak {
			t.Errorf("word-shaped %q counted as a code", w)
		}
	}
	if got := Code("Your verification code is below. Dear GitLab and DoorDash users.", ""); got != "" {
		t.Fatalf("word-shaped tokens supplied %q", got)
	}
}

func TestGmail_ExtractCode_AllCapsOnlyTierTwoAndNeverCompound(t *testing.T) {
	// An all-capitals token is a code only in tier two.
	if got := Code("Your verification code expires soon. DHVFPD This code can only be used once.", ""); got != "DHVFPD" {
		t.Fatalf("all-caps tier two = %q", got)
	}
	if got := Code("code: DHVFPD", ""); got != "" {
		t.Fatalf("all-caps in tier one = %q", got)
	}
	// A punctuation-joined compound is one part of a name, never a code.
	if got := Code("Enter the security code field for INSTALILY.AI: V5q2JzkC", ""); got != "V5q2JzkC" {
		t.Fatalf("compound competed with the real code: %q", got)
	}
	for _, body := range []string{"Your verification code at Zxcvbn-Qwerty", "Your verification code via INSTALILY_AI", "Your verification code Foo/Bar"} {
		if got := Code(body, ""); got != "" {
			t.Errorf("%q -> %q", body, got)
		}
	}
	// A capitalized-fragment rule also holds for mixed-case words.
	if got := Code("Your verification code AbCdEfGh.example", ""); got != "" {
		t.Errorf("joined fragment counted: %q", got)
	}
}

func TestGmail_ExtractCode_TierOneEitherSide(t *testing.T) {
	for body, want := range map[string]string{
		"Verification code 319139":                "319139",
		"319139 is your code":                     "319139",
		"Your PIN = 4821":                         "4821",
		"code is was 319139":                      "",
		"The code, which you should keep: 319139": "",
	} {
		got := Code(body, "")
		if got != want {
			t.Errorf("Code(%q) = %q, want %q", body, got, want)
		}
	}
	// More than 20 characters of connector text breaks the tie.
	far := "code" + strings.Repeat("-", 25) + "319139"
	if got := Code(far, ""); got != "" {
		t.Errorf("far connector = %q", got)
	}
}

func TestGmail_ExtractCode_TierTwoPhrasesNotBareNoun(t *testing.T) {
	// The phrase is near the candidate but no code noun touches it.
	if got := Code("Use the following to confirm your identity then continue: 482913 expires fast", ""); got != "482913" {
		t.Fatalf("identity phrase = %q", got)
	}
	// A bare noun without a phrase never makes tier two.
	if got := Code("Read the code of conduct (c) 2026 before you apply.", ""); got != "" {
		t.Fatalf("bare noun near a year = %q", got)
	}
	// Each second-tier sample is read by the extractor.
	for _, p := range ContextPhrases {
		for _, s := range p.Samples {
			if !contextRE.MatchString(s) {
				t.Errorf("sample %q is not matched by the context phrases", s)
			}
		}
	}
	if contextRE.MatchString("code") {
		t.Fatal("the bare code noun must not be a context phrase")
	}
}

func TestGmail_ExtractCode_AmbiguousReturnsNone(t *testing.T) {
	for name, body := range map[string]string{
		"no candidate":              "Please check your inbox soon.",
		"candidate without context": "Order number 482913 has shipped.",
		"two tier one candidates":   "Verification code 482913. Backup code 771204.",
		"two tier two candidates":   "Please confirm your identity. Numbers: 482913 771204.",
	} {
		if got := Code(body, ""); got != "" {
			t.Errorf("%s: %q", name, got)
		}
	}
	if got := Code("Verification code 482913. Backup code 482913.", ""); got != "482913" {
		t.Errorf("repeated identical value = %q", got)
	}
}

func TestGmail_ExtractCode_SmsPhoneWording(t *testing.T) {
	for body, want := range map[string]string{
		"Enter the SMS verification code we texted you: 482913":       "482913",
		"Use the code we sent via sms, 482913, to confirm your phone": "482913",
	} {
		if got := Code(body, ""); got != want {
			t.Errorf("Code(%q) = %q, want %q", body, got, want)
		}
	}
}

func TestGmail_OTP_QueryCoversExtractorPhrases(t *testing.T) {
	norm := func(s string) string {
		return strings.Join(strings.Fields(strings.ToLower(strings.ReplaceAll(s, "-", " "))), " ")
	}
	for _, p := range ContextPhrases {
		for _, sample := range p.Samples {
			covered := false
			for _, term := range OTPSearchTerms {
				if strings.Contains(norm(sample), norm(term)) {
					covered = true
					break
				}
			}
			if !covered {
				t.Errorf("the search vocabulary never returns mail saying %q", sample)
			}
		}
	}
	q := OTPQueryTerms()
	if !strings.HasPrefix(q, "{") || !strings.HasSuffix(q, "}") || !strings.Contains(q, `"verification code"`) || !strings.Contains(q, " otp ") {
		t.Fatalf("query = %s", q)
	}
}

func TestGmail_OTP_HTMLOnlyMessageText(t *testing.T) {
	html := `<html><head><style>.x{width:1234567}</style></head><body><p style="font-size:1234">Your <b>verification code</b> is</p><p>482913</p><img src="https://x.example.com/p/9988776.gif"></body></html>`
	if got := Code(StripHTML(html), ""); got != "482913" {
		t.Fatalf("HTML-only code = %q (text %q)", got, StripHTML(html))
	}
}

func TestGmail_ExtractCode_NeverAcrossASentenceEnd(t *testing.T) {
	for body, want := range map[string]string{
		"Your verification code.\n\n482913 is an unrelated number.":            "",
		"Your verification code is ready! 482913":                              "",
		"Your code is ready. 482913 is not it.":                                "",
		"Your verification code is pending. Ship to 94103 by Friday.":          "",
		"Did you request a verification code? Call 482913 for help.":           "",
		"Enter this verification code to sign in:\n\n482913":                   "482913",
		"Did you request a verification code? 482913 is your code.":            "482913",
		"Your verification code is 482913. It expires in 10 minutes.":          "482913",
		"Use the security code below to continue\n482913\nIt expires shortly.": "482913",
	} {
		if got := Code(body, ""); got != want {
			t.Errorf("Code(%q) = %q, want %q", body, got, want)
		}
	}
}
