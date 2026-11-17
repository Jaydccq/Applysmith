package match

// rxbSponsorshipRoutes: the positive sponsorship family. A stem whose real ask
// is hybrid or on-site attendance is excluded and handed to the office
// attendance entry, even when it opens with a "no sponsorship" disclaimer.
func rxbSponsorshipRoutes() []Route {
	const describeAsk = `|\b(?:describe|explain|provide\s+details?|elaborate)\b`
	// h1bNeedAsk asks whether the need is an H-1B (Pogo: "would you need an
	// H1B, O1 visa, or transfer of either?"): the H-1B entry's, which the
	// sponsorship family routes leave to it. "an H-1B transfer" alone stays
	// theirs.
	const h1bNeedAsk = `\b(?:would|will|do)\s+you\s+(?:need|require)\s+(?:an?\s+)?h-?1-?b(?:\s*,|\s+(?:visa|sponsorship|or)\b|\s*\?)`
	return []Route{
		{Priority: 105, Target: "sponsorship-type-description", Pattern: guardSponsorshipDescribe},
		// Sola: "require employment visa sponsporship" is misspelt; a word that
		// opens with "spons" is the sponsor family.
		{Priority: 106, Target: "sponsorship-now-or-future", Exclude: rxbHybridDays + `|` + rxbUSPersonAsk,
			Pattern: `\b(?:require|need)\b.*\bspons\w*`},
		{Priority: 107, Target: "sponsorship-now-or-future", Exclude: rxbHybridDays + `|` + rxbUSPersonAsk,
			Pattern: `\bsponsor(?:ship)?\b.*\b(?:now|future|continue|begin)\b`},
		// Cirrus Logic's "Are you a U.S. person ...? *Temporary residence does not
		// mean visa status such as B, H, ..." names visa status only to define it;
		// Valkyrie Trading's certification names it as a topic of true answers.
		{Priority: 108, Target: "sponsorship-now-or-future", Exclude: rxbHybridAsk + `|\bu\.?s\.?\s+persons?\b|` + certifiedAnswers, Pattern: `\bvisa\s+(?:status|sponsorship)\b`},

		{Priority: 110, Target: "sponsorship-now-or-future", Exclude: rxbHybridAsk,
			Pattern: `\b(?:require|need|requiring|needing)\b[^?]{0,60}\b(?:visa|work\s+permit|permit)\b[^?]{0,60}\bto\s+work\b|\b(?:will|do|would)\s+you\b[^?]{0,40}\b(?:require|need)\s+(?:an?\s+|any\s+)?(?:work|employment)\s+(?:authori[sz]ation|visas?|permits?)\b` +
				// Starbridge: "Do you require any form of authorization to work legally
				// in the United States?"
				`|\b(?:will|do|would)\s+you\b[^?]{0,40}\b(?:require|need)\s+(?:an?\s+|any\s+)?(?:(?:form|kind|type)\s+of\s+)?authori[sz]ation\s+to\s+work\b`},
		// Fluidstack: "I will now or in the future need assistance with a work visa."
		{Priority: 111, Target: "sponsorship-now-or-future", Exclude: rxbHybridAsk, Pattern: `\bassistance\s+with\s+(?:a\s+|my\s+|your\s+)?(?:work\s+authori[sz](?:ation|e)|(?:work\s+|employment\s+)?visa)\b`},

		{Priority: 112, Target: "sponsorship-now-or-future", Exclude: rxbHybridAsk + describeAsk + `|` + h1bNeedAsk,
			Pattern: `\b(?:require|need|requiring|needing)\b[^.?!]*\bimmigration\s+support\b|\bimmigration\s+support\b[^.?!]*\b(?:maintain|continue|keep|retain)\b[^.?!]*\b(?:work\s+authori[sz]\w*|status|employment)\b`},
		{Priority: 113, Target: "sponsorship-now-or-future", Exclude: rxbHybridAsk + describeAsk,
			Pattern: `\b(?:require|need|requiring|needing)\b[^?]{0,40}\b(?:employer|company|immigration|visa)\s+(?:support|assistance)\b[^?]{0,40}\b(?:obtain|maintain|secure|keep|retain|continue)\b[^?]{0,60}\b(?:(?:work\s+)?authori[sz]\w*|work\s+permit|visa)\b`},
		// The pattern itself is narrow enough that a bare non-immigrant status
		// question does not reach it; only the H-1B entry's ask is excluded.
		{Priority: 114, Target: "sponsorship-now-or-future", Exclude: h1bNeedAsk,
			Pattern: `\b(?:need|require|file|register)\b[^.?!]*\bh-?1-?b\b[^.?!]*\b(?:transfer|lottery|registration|cap|petition)\b|\bh-?1-?b\s+(?:transfer|lottery|cap\s+registration)\b`},

		// The need of an H-1B sponsored. Holding one ("Do you currently hold an
		// H-1B visa?") is a status, and whether the employer offers the
		// sponsorship is no question about the applicant.
		{Priority: 115, Target: "h1b-sponsorship", Pattern: `\bh-?1-?b\s+sponsor(?:ship)?\b|` + h1bNeedAsk,
			Exclude: `\bsponsor(?:ship)?\s+(?:is\s+)?(?:offered|available|provided)\b|\b(?:offer|provide)s?\s+(?:an?\s+)?h-?1-?b\b`},
		// Needs both a US location token and a permit token, so a plain
		// "located in the US" question is untouched.
		{Priority: 117, Target: "us-based-with-work-permit",
			Pattern: `\bbased\s+in\s+(?:the\s+)?` + rxbUS + `[^?]*\b(?:work(?:ing)?\s+permit|work\s+authori[sz](?:ation|ed))\b`},
	}
}
