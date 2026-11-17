package match

import "testing"

// TestRoutes_ParityQuirksFixed pins the four route defects the first port
// carried over from the old table. Each is a bug, not behaviour to keep.
func TestRoutes_ParityQuirksFixed(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		hasOptions bool
		want       string
	}{
		// A referral by a government official is a disclosure, not a referral by an
		// employee (priority 246 used to claim it first).
		{"referred by a government official", "Were you referred by a government official?", true, "government-official-disclosure"},
		{"have you been referred by an official", "Have you been referred by a government official?", true, "government-official-disclosure"},
		// A referral received from one is no employee referral either; no
		// route claims it, so it is left to the user.
		{"did you receive a referral from an official", "Did you receive a referral from a government official?", true, ""},
		{"plain referral still routes", "Were you referred?", true, "referred-by-current-employee"},

		// cursor and codex name an AI tool only in an AI-tool context (route 534).
		{"cursor as a tool", "Do you use Cursor?", false, "ai-coding-tools-daily-driver"},
		{"codex as a tool", "Which AI tools do you use, for example Codex?", false, "ai-coding-tools-daily-driver"},
		{"bare tool name", "Cursor", false, "ai-coding-tools-daily-driver"},
		{"mouse cursor", "Describe how you move the mouse cursor", false, ""},
		{"codex in another sense", "Have you read the Codex Alimentarius?", false, ""},
		{"cursor in a database sense", "Is the database cursor still open?", false, ""},

		// c++ and c# end at their last symbol (the skill gate, priority 291).
		{"c++ at the end of the text", "Are you proficient in C++", false, "skill-experience-knowledge-gate"},
		{"c++ before punctuation", "Are you proficient in C++?", false, "skill-experience-knowledge-gate"},
		{"c# before punctuation", "Are you experienced with C#?", false, "skill-experience-knowledge-gate"},
		{"c# at the end of the text", "Are you familiar with C#", false, "skill-experience-knowledge-gate"},
		{"c++ followed by a word", "Are you proficient in C++ and Python?", false, "skill-experience-knowledge-gate"},
		{"a longer word is not go", "Are you proficient in Gopher?", false, ""},

		// 188 leaves offer-framed phrasing to offer-start-availability.
		{"able to start", "Are you able to start immediately?", false, "earliest-start-date"},
		{"bare label", "Available to Start", false, "earliest-start-date"},
		{"offer framed", "Are you available to start if an offer is accepted?", false, "offer-start-availability"},
		{"offer framed with options", "When would you be available to start after an offer?", true, "offer-start-availability"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, _ := catalogWinner(c.text, c.hasOptions); got != c.want {
				t.Fatalf("winner = %q, want %q", got, c.want)
			}
		})
	}
}
