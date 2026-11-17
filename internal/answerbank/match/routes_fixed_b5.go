package match

// Relocation clauses. A presence clause and a willingness clause are tried in
// both orders so a question that asks "A or B" reads both halves.
const (
	rxbPresence = `\b(?:(?:currently|presently|already)\s+)?(?:located|based|living|live|reside|residing|local)\b`
	// A plan is a willingness (Brex: "Do you currently live in, or plan to relocate to, the specified location"),
	// and so is being comfortable with the move (Auror: "or comfortable relocating prior to starting").
	rxbWilling = `\b(?:(?:willing|able|open|prepared|happy|plan(?:s|ning)?|intend(?:ing)?)\s+to\s+(?:(?:re)?locate|move|moving|relocating)|comfortable\s+(?:with\s+)?(?:relocating|moving))\b`
	// rxbWillingOrCommute is also able to commute to a place, which is able to
	// work there (Spade: "currently located in or able to commute to New York
	// City at least 2 days/week").
	rxbWillingOrCommute = `(?:` + rxbWilling + `|\b(?:willing|able)\s+to\s+commute\b)`

	// rxbOnsiteClause is the required second conjunct of the in-and-onsite ask.
	// Ramp: "able to work out of our office 2-3 days per week".
	rxbOnsiteClause = `\b(?:willing|able|open|prepared|happy)\s+to\s+(?:work\w*|be|come|commut\w*)\b[^?]*\b(?:on[- ]?site|in[- ]office|in[- ]person|hybrid|office)\b`

	// rxbNotAMetro lists the questions the in-and-onsite route must leave
	// alone: a leading what/which, country-level presence and non-location
	// objects of "in".
	rxbNotAMetro = `^\W*(?:what|which)\b|\bin\s+(?:the\s+)?(?:us|usa|u\.s\.a?\.?|united\s+states|canada)\s+(?:and|&)(?:\s|$)|\bin\s+(?:school|college|university|class|(?:a|an|the)\s+(?:position|relationship|role|job|program|degree|process))\b`
)

// rxbLocationRoutes: travel, relocation willingness and the located-or-willing
// compounds that must be read before any plain "based in" fact route.
func rxbLocationRoutes() []Route {
	return []Route{
		{Priority: 28, Target: "willing-to-travel", Pattern: `\b(?:can|could)\s+you\s+travel\b|\bable\s+to\s+travel\b`},

		{Priority: 122, Target: "willing-to-relocate",
			Pattern: rxbPresence + `[^?]{0,60}\bor\b[^?]{0,60}` + rxbWillingOrCommute + `|` + rxbWillingOrCommute + `[^?]{0,60}\bor\b[^?]{0,60}` + rxbPresence},
		// The presence clause is the condition, the relocation clause the ask.
		{Priority: 123, Target: "willing-to-relocate",
			Pattern: `\bif\s+you(?:'re|\s+are|\s+aren't|\s+are\s+not)\s+(?:not\s+)?[^?]{0,20}` + rxbPresence + `[^?]{0,80}` + rxbWilling},
		{Priority: 125, Target: "based-in-named-metro", Exclude: rxbNotAMetro,
			Pattern: `\b(?:are|do)\s+you\s+(?:(?:currently|presently)\s+)?(?:(?:based|located|living)\s+(?:out\s+of|in)|in)\b[^?]{2,80}?,?\s+(?:and|&)\s+(?:are\s+you\s+)?` + rxbOnsiteClause},

		// Relativity Space: "willing to commute and/or relocate for this role".
		{Priority: 126, Target: "willing-to-relocate", Pattern: `\bwilling\s+to\s+(?:consider(?:ing)?\s+|commute\s+(?:and/or|and|or)\s+)?relocat(?:e|ion|ing)\b`},
		{Priority: 127, Target: "willing-to-relocate", Pattern: `\bopen\s+to\s+(?:consider(?:ing)?\s+)?relocat(?:e|ion|ing)\b`},
		{Priority: 128, Target: "willing-to-relocate", Pattern: `\b(?:able|prepared)\s+to\s+relocat(?:e|ion|ing)\b`},
		{Priority: 129, Target: "willing-to-relocate", Pattern: `\bwilling\s+to\s+locate\b`},
		{Priority: 130, Target: "willing-to-relocate", Pattern: `\bopen\s+to\s+locate\b`},
		// Sesame: "Are you willing to work from the required location?" is the
		// posting's place, which the policy answers.
		{Priority: 131, Target: "willing-to-relocate", Pattern: `\b(?:willing|able|open|prepared)\s+to\s+(?:move|moving)\s+to\b` +
			`|\b(?:willing|able|open|prepared)\s+to\s+work\s+(?:from|at|in|out\s+of)\s+(?:the|this|our)\s+(?:required|stated|listed|posted|designated|specified)\s+(?:work\s+)?(?:location|office|site)\b`},

		// The negated shape; a benefits question or an "or local" disjunction
		// keeps its own entry.
		{Priority: 133, Target: "requires-relocation",
			Exclude: `\brelocation\s+(?:assistance|package|support|bonus|benefits?)\b|\bassist(?:ance)?\s+with\s+relocat\w*|\bor\b[^?]{0,40}\blocal\b`,
			// Stream: "Would you be relocating?".
			Pattern: `\b(?:will|would|do|does|might)\s+you\s+(?:currently\s+)?(?:require|need)\b[^?]{0,20}?\brelocat(?:e|ion|ing)\b|\b(?:will|would)\s+you\s+be\s+relocating\b`},

		{Priority: 172, Target: "open-to-remote", Pattern: `\bopen\s+to\s+(?:remote|working\s+remotely)\b`},
		// Fluidstack: "Are you open to travel?"; Standard Bots: "open to domestic travel".
		{Priority: 176, Target: "willing-to-travel", Pattern: `\b(?:willing(?:ness)?|open)\s+to\s+(?:(?:domestic|international|occasional|frequent|regular|some)\s+)?travel\b`},
		{Priority: 177, Target: "willing-to-travel", Pattern: `\bcomfortable\s+with\s+(?:travel|travell?ing)\b`},
		{Priority: 178, Target: "willing-to-travel", Pattern: `\bimpediments?\s+to\s+travell?ing\b`},
		{Priority: 179, Target: "willing-to-travel", Pattern: `\bopen\s+to\s+(?:(?:frequent|occasional|regular)\s+)?travell?ing\b`},
		{Priority: 180, Target: "willing-to-travel", Pattern: `\b(?:frequent|occasional|regular)\s+travel\b`},
		// AHEAD: "What % of travel are you open to?".
		{Priority: 181, Target: "willing-to-travel", Pattern: `\btravel\s+availability\b|^\W*travel\s+(?:expectations?|requirements?)\s*:|\b(?:what|how\s+much)\s+(?:%|percent(?:age)?)\s+(?:of\s+)?travel\b`},
	}
}
