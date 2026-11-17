// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"strings"
	"testing"
)

// TestGates_Matrix_NumericBounds is the scenario "Numeric bounds are
// table-tested": every bound of the capability, each evaluated through the
// real gate just below, exactly at and just above it. A case reports whether
// the gate refuses (or, for a limit that ends a state, whether the state has
// ended).
func TestGates_Matrix_NumericBounds(t *testing.T) {
	type edge struct{ below, at, above bool }
	tests := []struct {
		bound string
		eval  func(n int64) bool // n is the value under test
		vals  [3]int64
		want  edge
	}{
		{"review flag 0.70 (percent)", func(n int64) bool {
			return FlagBlocks(Flag{Value: "x", Probability: float64(n) / 100, HasProbability: true})
		}, [3]int64{69, 70, 71}, edge{false, true, true}},
		{"bare phone code 0.85 (percent)", func(n int64) bool {
			return FlagBlocks(Flag{Value: "+1", Probability: float64(n) / 100, HasProbability: true})
		}, [3]int64{84, 85, 86}, edge{false, true, true}},
		{"3 refused passwords", func(n int64) bool {
			return !PasswordSubmission(PasswordInput{AccountKnown: true, Refused: int(n), TodayLocal: "d"}).Allowed()
		}, [3]int64{2, 3, 4}, edge{false, true, true}},
		{"guess headroom (2 of 3)", func(n int64) bool {
			return !PasswordSubmission(PasswordInput{Refused: int(n), TodayLocal: "d"}).Allowed()
		}, [3]int64{1, 2, 3}, edge{false, true, true}},
		{"2 code requests (request number)", func(n int64) bool {
			return !CodeRequest(CodeRequestInput{RequestsThisHost: int(n) - 1}).Allowed()
		}, [3]int64{1, 2, 3}, edge{false, false, true}},
		{"2 captcha solves (solve number)", func(n int64) bool {
			return !CaptchaSolve(solveOK(func(i *CaptchaInput) { i.SolvesThisRun = int(n) - 1 })).Allowed()
		}, [3]int64{1, 2, 3}, edge{false, false, true}},
		{"USD 0.50 balance floor (micro-dollars)", func(n int64) bool {
			return !CaptchaSolve(solveOK(func(i *CaptchaInput) { i.BalanceMicroUSD = n })).Allowed()
		}, [3]int64{CaptchaBalanceFloorMicroUSD - 1, CaptchaBalanceFloorMicroUSD, CaptchaBalanceFloorMicroUSD + 1}, edge{true, false, false}},
		{"15 minute live window (age)", func(n int64) bool {
			return EffectiveState(LedgerRow{State: StateSubmitting, At: t0 - n}, t0) == StateMayHaveSubmitted
		}, [3]int64{15*Minute - 1, 15 * Minute, 15*Minute + 1}, edge{false, false, true}},
		{"24 hour reset hold (age of the failed reset)", func(n int64) bool {
			return !ResetStart(ResetStartInput{Now: t0, AccountKnown: true, SavedPasswordRefused: true, HostIsWorkday: true, LastFailedResetAt: t0 - n}).Allowed()
		}, [3]int64{24*Hour - 1, 24 * Hour, 24*Hour + 1}, edge{true, false, false}},
		{"24 hour credential cooldown (age of the stop)", func(n int64) bool {
			h, _ := HoldAfterStop(ClassCredential, "acme.example.test", true, t0-n)
			return RowHeld("acme.example.test", []StopHold{h}, t0)
		}, [3]int64{24*Hour - 1, 24 * Hour, 24*Hour + 1}, edge{true, false, false}},
		{"120 second solver ceiling", func(n int64) bool { return CaptchaPollStep(n) == PollGiveUp }, [3]int64{120*Second - 1, 120 * Second, 120*Second + 1}, edge{false, true, true}},
		{"5 second solver first wait", func(n int64) bool { return CaptchaPollStep(n) == PollWait }, [3]int64{5*Second - 1, 5 * Second, 5*Second + 1}, edge{true, false, false}},
		{"30 day activation lookup (age of the mail)", func(n int64) bool {
			return !ActivationLink(workdayMail(func(i *ActivationInput) { i.Mail.ReceivedAt = t0 - n })).Allowed()
		}, [3]int64{30*Day - 1, 30 * Day, 30*Day + 1}, edge{false, false, true}},
		{"300 character reason", func(n int64) bool {
			return len([]rune(decide(GateReadiness, Deny, CodeValueNotHeld, "%s", strings.Repeat("r", int(n))).Reason)) > MaxReasonRunes
		}, [3]int64{299, 300, 301}, edge{false, false, false}},
		{"500 skip-list entries", func(n int64) bool {
			_, d := ResolveSkipList(SkipListConfig{Present: true, Entries: make([]string, n)})
			return !d.Allowed()
		}, [3]int64{499, 500, 501}, edge{false, false, true}},
		{"120 character skip-list entry", func(n int64) bool {
			_, d := ResolveSkipList(SkipListConfig{Present: true, Entries: []string{strings.Repeat("x", int(n))}})
			return !d.Allowed()
		}, [3]int64{119, 120, 121}, edge{false, false, true}},
		{"40 review rows", func(n int64) bool {
			rows := make([]ReviewRow, n)
			for i := range rows {
				rows[i] = ReviewRow{Value: "v", WrittenByRun: true}
			}
			return len(SelectReviewRows(rows)) < int(n)
		}, [3]int64{39, 40, 41}, edge{false, false, true}},
		{"120 character review value", func(n int64) bool {
			r := SelectReviewRows([]ReviewRow{{Value: strings.Repeat("v", int(n)), WrittenByRun: true}})
			return len([]rune(r[0].Value)) < int(n)
		}, [3]int64{119, 120, 121}, edge{false, false, true}},
		{"180 day audit retention (age of the row)", func(n int64) bool { return t0-n < AuditPruneBefore(t0) }, [3]int64{180*Day - 1, 180 * Day, 180*Day + 1}, edge{false, false, true}},
		{"160 character audit path", func(n int64) bool {
			_, p, _ := RedactTarget("https://x.example.test/" + strings.Repeat("p", int(n)-1))
			return int64(len(p)) < n
		}, [3]int64{159, 160, 161}, edge{false, false, true}},
		{"24 character opaque path segment", func(n int64) bool {
			_, p, _ := RedactTarget("https://x.example.test/" + strings.Repeat("a1", 20)[:n])
			return p == "/~"
		}, [3]int64{23, 24, 25}, edge{false, true, true}},
		{"8 character activation token", func(n int64) bool {
			return !ActivationLink(workdayMail(func(i *ActivationInput) {
				i.Mail.Link = "https://acme.example.test/site/activate/" + "a1" + strings.Repeat("b", int(n)-2)
			})).Allowed()
		}, [3]int64{7, 8, 9}, edge{true, false, false}},
		{"20 character verification hash", func(n int64) bool {
			return !ActivationLink(sfMail(func(i *ActivationInput) {
				i.Mail.Link = "https://acme.example.test/career?navBarLevel=EMAIL_VERIFICATION&hash=" + strings.Repeat("a", int(n))
			})).Allowed()
		}, [3]int64{19, 20, 21}, edge{true, false, false}},
	}
	for _, c := range tests {
		t.Run(c.bound, func(t *testing.T) {
			for i, want := range [3]bool{c.want.below, c.want.at, c.want.above} {
				if got := c.eval(c.vals[i]); got != want {
					t.Errorf("%s: value %d (%s): got %v, want %v", c.bound, c.vals[i], [3]string{"below", "at", "above"}[i], got, want)
				}
			}
		})
	}
}
