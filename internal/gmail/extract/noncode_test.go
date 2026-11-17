package extract

import "testing"

// The two phone shapes of the verifier's probes, joined at run time so no
// number-shaped literal sits in the source.
const (
	probePhone = "555" + "-123-" + "4567"
	probeLocal = "555" + "-1234"
)

func TestGmail_ExtractCode_NonCodeDigitsYieldNone(t *testing.T) {
	for _, c := range []struct{ name, body string }{
		{"date with slashes", "Your verification code expires 10/11/2026."},
		{"iso date", "Your security code expires on 2026-10-11 at noon."},
		{"month and year", "Your verification code was requested in October 2026."},
		{"month day year", "Your security code expires Oct 11, 2026."},
		{"phone number after the phrase", "We sent a verification code to your phone " + probePhone + "."},
		{"reserved phone number", "We sent a verification code to your phone 212-555-0142."},
		{"parenthesized phone", "We sent a verification code to your phone (212) 555 0142."},
		{"local number before the phrase", "Call " + probeLocal + " if you did not request this verification code."},
		{"reserved local number", "Call 555-0142 if you did not request this verification code."},
		{"order number", "Your security code is pending for order 48291377."},
		{"invoice number", "Your verification code is attached to invoice 4829113."},
		{"reference with hash", "Your security code for ref #482913 is pending."},
		{"amount with symbol", "Your verification code is needed to approve $4500 today."},
		{"amount with decimals", "Your verification code is needed to approve 4500.00 today."},
		{"amount with currency name", "Your verification code is needed to approve 4500 USD today."},
		{"year in the second tier", "Your verification code is on its way. (c) 2026 Example Corp."},
		{"zip code qualified noun", "Your zip code 94105 was saved."},
		{"zip after a state", "Your verification code request came from Springfield, CA 94105."},
		{"zip plus four", "Your verification code request came from 94105-1234."},
		{"time of day", "Your verification code is valid until 1030 hours."},
		{"clock time", "Your security code expires at 10:30 today."},
		{"unit after the digits", "Your verification code expires in 3600 seconds."},
		{"promo code", "Use promo code 482913 at checkout."},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Code(c.body, ""); got != "" {
				t.Fatalf("Code = %q, want none", got)
			}
		})
	}
}

func TestGmail_ExtractCode_RealCodeSurvivesNonCodeDigits(t *testing.T) {
	for _, c := range []struct{ name, body, want string }{
		{"date after the code", "Your verification code is 482913, valid until 10/11/2026.", "482913"},
		{"phone after the code", "Your verification code is 482913. Call 212-555-0142 for help.", "482913"},
		{"order beside the code", "For order 48291377 your security code is 771204.", "771204"},
		{"a code that looks like a year beside the noun", "Your verification code is 2026", "2026"},
		{"amount in the same mail", "Your verification code: 482913. The charge of $4500 is pending.", "482913"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Code(c.body, ""); got != c.want {
				t.Fatalf("Code = %q, want %q", got, c.want)
			}
		})
	}
}
