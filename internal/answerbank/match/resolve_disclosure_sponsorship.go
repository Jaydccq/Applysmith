package match

import (
	"slices"
	"strings"

	"applysmith/internal/lazyregexp"
)

// The sponsorship stack. Sponsorship is two profile facts, requires_sponsorship_now
// and requires_sponsorship_future, each true, false or unset; an unset fact is
// never read as false. The entries sponsorship-now-or-future and
// work-auth-and-future-sponsorship are derived from them, and so is every
// sponsorship route: the stored body of those entries is ignored.

// sponsorScope is which fact a question reads.
type sponsorScope int

const (
	scopeEither sponsorScope = iota
	scopeNow
	scopeFuture
)

var (
	// authorizedNowRE is a time word on the authorization rather than on the
	// need; the group is the authorization without it. Corelight's "currently
	// have unrestricted authorization" holds it as a noun.
	authorizedNowRE = lazyregexp.New(`(?i)\b(?:currently|presently)\s+((?:legally\s+)?(?:authori[sz]ed|able|eligible|permitted|entitled)|(?:have|hold|possess)\s+(?:(?:the|an?)\s+)?(?:unrestricted\s+)?(?:legal\s+)?(?:work\s+)?(?:authori[sz]ation|right)\b)\b`)
	nowOnlyRE       = lazyregexp.New(`(?i)\bbefore\s+(?:you\s+)?(?:could|can|would|are\s+able\s+to)\s+(?:begin|start)\b|\b(?:currently|presently)\s+(?:require|need)\b|\b(?:require|need)\s+(?:visa\s+)?sponsorship\s+(?:now|at\s+this\s+time|currently)\b`)
	// laterRE marks any mention of a later need: the question is the combined one.
	laterRE = lazyregexp.New(`(?i)\bfuture\b|\bat\s+any\s+(?:time|point)\b|\blater\b|\bever\b|\bcontinue\b|\bnow\s+or\b|\bwill\s+you\b`)
	// rightNowRE is a time word that pins the need to the present (Ello:
	// "Will you require sponsorship to work in the U.S. right now?"); with
	// it, "will you" is only the question's grammar, and only an explicit
	// later word (laterWordRE) makes the question the combined one.
	rightNowRE   = lazyregexp.New(`(?i)\b(?:right\s+now|at\s+this\s+(?:time|moment)|at\s+present)\b`)
	laterWordRE  = lazyregexp.New(`(?i)\bfuture\b|\bat\s+any\s+(?:time|point)\b|\blater\b|\bever\b|\bcontinue\b|\bnow\s+or\b|\bor\s+(?:right\s+)?now\b`)
	futureOnlyRE = lazyregexp.New(`(?i)\bin\s+the\s+future\b|\bfuture\s+sponsorship\b|\bfuture\s+(?:visa|work)\b|\beventually\b`)
	// anyPointFutureRE is "at any point in the future": the future alone.
	anyPointFutureRE = lazyregexp.New(`(?i)\bat\s+any\s+(?:time|point)\s+(in\s+the\s+future)\b`)
	nowMentionRE     = lazyregexp.New(`(?i)\b(?:now|currently|presently|at\s+this\s+time|at\s+present|immediately)\b|\bat\s+any\s+(?:time|point)\b|\bever\b|\bcurrent\s+(?:or|and)\s+future\b`)

	negatedSponsorRE = lazyregexp.New(`(?i)` + guardNegatedSponsorship)
	// Vercel: "a valid work permit which needs to be sponsored by the company".
	sponsorMentionRE = lazyregexp.New(`(?i)\b(?:requires?|requiring|needs?|needing|looking\s+for|seeking)\b.*\b(?:sponsor(?:ship|ed|ing)?|visa)\b`)
	// The details box may follow the need it asks about (Gamma: "require any
	// visa sponsorship to work in the US? If yes, please share more details below:"),
	// past an example list (Stackline: "... sponsorship from Stackline? (e.g., F-1
	// OPT or CPT to H-1B, ...) If yes, please explain.").
	sponsorDetailRE  = lazyregexp.New(`(?i)\b(?:provide additional details on your (?:required )?sponsorship|if so, please explain.*\bsponsorship)|\bsponsorship\b[^?]*\?\s*(?:\([^)]*\)\s*)?if\s+(?:yes|so)\b[,.]?\s*(?:please\s+)?(?:(?:share|provide|give)\s+(?:(?:more|additional|further|any)\s+)?details|explain|describe|elaborate)\b`)
	notRequireRE     = lazyregexp.New(`(?i)\b(?:do(?:es)?|will|would)\s+not\s+(?:currently\s+)?(?:require|need)\b|\bdon'?t\s+(?:require|need)\b|\bno\s+(?:need|requirement)\b|\bnot\s+(?:currently\s+)?(?:looking\s+for|seeking)\b`)
	futureOptionRE   = lazyregexp.New(`(?i)\b(?:in\s+the\s+)?future\b|\blater\b|\beventually\b`)
	presentOptionRE  = lazyregexp.New(`(?i)\b(?:currently|presently)\s+(?:\w+\s+){0,2}?(?:require|need)|\b(?:require|need)\w*\s+(?:visa\s+)?sponsorship\s+(?:now|currently|at\s+this\s+time)\b`)
	conjunctionYesRE = lazyregexp.New(`(?i)\b(?:select|choose|answer|pick|mark|check|click|respond)\s+["'“”‘’]?yes["'“”‘’]?\s+(?:only\s+)?(?:if|when)\b`)
	conjunctionNoRE  = lazyregexp.New(`(?i)\b(?:select|choose|answer|pick|mark|check|click|respond)\s+["'“”‘’]?no["'“”‘’]?(?:[^a-z]|$)`)
	conjunctNeedsRE  = lazyregexp.New(`(?i)\b(?:need|require|needing|requiring)\b[^.?!]{0,60}\bsponsor\w*|\bsponsor\w*[^.?!]{0,30}\b(?:needed|required)\b`)
	conjunctSplitRE  = lazyregexp.New(`(?i),?\s+\band\b\s+`)
	conjunctOnSiteRE = lazyregexp.New(`(?i)\b(?:on|in)[\s\-\x{2010}\x{2011}]?(?:site|office|person)\b`)
	// bareExplainFollowE may name what it asks after the verb (Lithic: "If so,
	// please specify the type of sponsorship required."), or ask the type
	// with an example list (Belvedere: "If yes, what type of sponsorship will
	// you require? (Ex. F-1, H1-B, OPT, etc.)").
	bareExplainFollowE = lazyregexp.New(`(?i)^\s*if\s+(?:yes|so)\s*[,:;-]?\s*(?:(?:please\s+)?(?:explain|describe|elaborate|specify|provide\s+(?:details?|more\s+information))(?:\s+(?:the\s+)?(?:type|kind)\s+of\s+(?:visa\s+)?sponsorship(?:\s+(?:required|needed|you\s+(?:will\s+)?(?:require|need)))?)?\b|(?:what|which)\s+(?:type|kind)\s+of\s+(?:visa\s+)?sponsorship\s+(?:will|would|do)\s+you\s+(?:require|need)\b\s*\??(?:\s*\((?:e\.?\s?g\.?|ex\.?)[^)]*\))?)[\s.:*?)]*$`)
)

const idSponsorshipTypeDescription = "sponsorship-type-description"

// sponsorshipScope reads which fact the wording asks about: now only, the
// future only, or either.
func sponsorshipScope(text string) sponsorScope {
	text = anyPointFutureRE.ReplaceAllString(text, "$1")
	switch {
	case nowOnlyRE.MatchString(text) && !laterRE.MatchString(text):
		return scopeNow
	case rightNowRE.MatchString(text) && !laterWordRE.MatchString(text):
		return scopeNow
	case futureOnlyRE.MatchString(text) && !nowMentionRE.MatchString(text):
		return scopeFuture
	}
	return scopeEither
}

// isNowOnlySponsorshipQuestion reports a question about now alone.
func isNowOnlySponsorshipQuestion(text string) bool { return sponsorshipScope(text) == scopeNow }

// sponsorshipNeed reads the facts a scope needs. For the combined scope a true
// fact decides yes; both false decides no; any other state is unknown.
func sponsorshipNeed(f Facts, sc sponsorScope) (need, known bool) {
	now, fut := f.RequiresSponsorshipNow, f.RequiresSponsorshipFuture
	switch sc {
	case scopeNow:
		if now == nil {
			return false, false
		}
		return *now, true
	case scopeFuture:
		if fut == nil {
			return false, false
		}
		return *fut, true
	}
	switch {
	case now != nil && *now, fut != nil && *fut:
		return true, true
	case now != nil && fut != nil:
		return false, true
	}
	return false, false
}

// notAuthorizedWithoutRE is a need said as a double negative (Engine: "I am
// not currently authorized to work in the U.S. without sponsorship"): the
// "not" negates the "without".
var notAuthorizedWithoutRE = lazyregexp.New(`(?i)\b(?:not|n't)\s+(?:(?:currently|yet|legally|presently)\s+)?(?:authori[sz]ed|eligible|able|permitted|allowed)\s+to\s+work\b[^;?!]{0,80}?\bwithout\s+(?:\w+\s+){0,3}?sponsor(?:ship|ing)?\b`)

// namesSponsor reports a label that holds "sponsor" in any case. The
// sponsorship readings run on every option of a pick, so a label without it
// skips their expressions.
func namesSponsor(label string) bool {
	return strings.Contains(label, "ponsor") || strings.Contains(label, "PONSOR")
}

// sponsorClaim is the label with each double-negative need said plainly.
func sponsorClaim(label string) string {
	if !namesSponsor(label) {
		return label
	}
	return notAuthorizedWithoutRE.ReplaceAllString(label, "need sponsorship")
}

// optionStatesSponsorshipRequirement reports an option that states the applicant
// requires sponsorship; any negation disqualifies it.
func optionStatesSponsorshipRequirement(label string) bool {
	label = sponsorClaim(label)
	return sponsorMentionRE.MatchString(label) && !hasNegationToken(label)
}

// optionNegatesSponsorship reports an option that states the applicant does NOT
// need sponsorship. A label whose negated clause is followed by an affirmed
// requirement ("without sponsorship now, but will require sponsorship later")
// is mixed, not a plain negation.
func optionNegatesSponsorship(label string) bool {
	// Every negated phrase holds "sponsor" or "support".
	if !namesSponsor(label) && !strings.Contains(label, "upport") && !strings.Contains(label, "UPPORT") {
		return false
	}
	label = sponsorClaim(label)
	if !negatedSponsorRE.MatchString(label) {
		return false
	}
	rest := negatedSponsorRE.ReplaceAllString(label, " ")
	return !optionStatesSponsorshipRequirement(rest)
}

// contradictsSponsorship is the veto: an option asserting the opposite of the
// facts is never chosen by any lane.
func contradictsSponsorship(label string, need bool) bool {
	if need {
		return optionNegatesSponsorship(label)
	}
	return optionStatesSponsorshipRequirement(label)
}

// withoutContradictions removes the options that contradict the facts.
func withoutContradictions(opts []Option, need bool) ([]Option, []string) {
	var kept []Option
	var notes []string
	for _, o := range opts {
		if contradictsSponsorship(o.Label, need) {
			notes = append(notes, "sponsorship option "+quoteTerm(o.Label)+" asserts the opposite of the recorded facts -> vetoed")
			continue
		}
		kept = append(kept, o)
	}
	return kept, notes
}

// sponsorBind binds the first of the candidate answers that the control can take,
// after the options that contradict the facts were removed.
func sponsorBind(s *Step, e *Entry, need bool, answers []string, trace string) Draft {
	step := *s
	kept, notes := withoutContradictions(s.Q.Options, need)
	step.Q.Options = kept
	if hasRenderedOptions(s.Q.Options) && !hasRenderedOptions(kept) {
		return abstainDraft(ReasonInputMismatch, "", "every option asserts the opposite of the recorded sponsorship facts")
	}
	// The candidate that binds best wins; on equal scores the first listed does.
	var best Draft
	found := false
	for _, a := range answers {
		d := bindEntryAnswer(&step, e, a, trace)
		d.Trace = append(append([]string(nil), notes...), d.Trace...)
		switch {
		case d.Abstain != nil && !found:
			best = d
		case d.Abstain == nil && (!found || d.OptionScore > best.OptionScore):
			best, found = d, true
		}
	}
	if found {
		best.Source, best.Start = SourceDerived, startProfileDerive
	}
	return best
}

// orTransferRE is a requirement list's last item, a transfer: ", or Transfer",
// "or to transfer or extend your current visa" (Procore). renewalOrRE is its
// first item, a renewal (Sysdig: "require renewal or sponsorship"). orGreenCardRE
// is a green card beside the visa (Helion: "require visa or green card
// sponsorship"), another sponsorship of the same list. transferOrRE is a
// transfer as the first item (EvolutionIQ: "require a H1-B transfer / visa
// sponsorship").
var (
	orGreenCardRE = lazyregexp.New(`(?i)\s+or\s+(?:a\s+)?(?:green\s*card|permanent\s+residen(?:ce|cy))\b`)
	orTransferRE  = lazyregexp.New(`(?i),?\s+or\s+(?:to\s+)?(?:an?\s+)?(?:(?:visa|h-?1-?b)\s+)?transfer\b(?:\s+or\s+extend\b)?(?:\s+(?:your\s+)?(?:current\s+)?(?:visa|status)\b)?`)
	renewalOrRE   = lazyregexp.New(`(?i)\b(?:an?\s+)?(?:renewal|extension)\s+or\s+|\s+or\s+(?:renew|extend)\b`)
	transferOrRE  = lazyregexp.New(`(?i)\b(?:an?\s+)?(?:(?:visa|h-?1-?b)\s+)?transfer\s*(?:/|\s+or\s)\s*`)
)

// orVerificationRE is a verification beside the sponsorship (Fleet: "require
// visa sponsorship or verification"), which a recorded need answers with the
// sponsorship.
var orVerificationRE = lazyregexp.New(`(?i)\s+or\s+(?:employment\s+)?verification\b`)

// orLMIARE is Canada's labour market impact assessment beside the sponsorship
// (FutureFit AI: "require visa sponsorship or an employer-assisted LMIA"),
// another sponsorship of the same list.
var orLMIARE = lazyregexp.New(`(?i)\s+or\s+(?:an?\s+)?(?:employer[-\s]+(?:assisted|supported|sponsored)\s+)?lmia\b`)

var (
	// h1bAsideRE is a lead that sets an H-1B transfer or extension aside
	// (Quantcast: "Excluding the transfer or extension of a currently valid
	// H1B visa, will you ..."): the rest of the sponsorship is asked about.
	h1bAsideRE = lazyregexp.New(`(?i)^\s*(?:excluding|except\s+for|other\s+than|apart\s+from|aside\s+from|not\s+including)\s+` +
		`(?:the\s+|an?\s+|any\s+)?(?:transfer|extension|renewal)s?(?:\s*(?:,|or|and|/)\s*(?:the\s+|an?\s+)?(?:transfer|extension|renewal)s?)*` +
		`\s+of\s+(?:the\s+|an?\s+|your\s+)?(?:(?:currently|current|existing)\s+)?(?:valid\s+)?h-?1-?b(?:\s+(?:visa|status|petition))?\s*,\s*`)
	h1bStatusRE = lazyregexp.New(`(?i)\bh-?1-?b`)
	// exampleKeepRE keeps an example list that excludes, negates, or names an
	// H-1B transfer ("(e.g. H-1B transfer)"), which is a different fact; a
	// move into H-1B ("student visa transfer to H1B") is sponsorship.
	exampleKeepRE = lazyregexp.New(`(?i)\b(?:not|no|never|without|except|excluding|unless|other\s+than|besides)\b|` +
		`\bh-?1-?b\s+transfer|\btransfer\s+of\s+(?:an?\s+|your\s+|the\s+)?(?:(?:current|existing|valid)\s+)?h-?1-?b`)
	// trailingExamplesRE is the same list closing the question without
	// brackets (Riskified: "... without sponsorship or transfer including
	// H-1B or OPT?"); group 1 is the question mark.
	trailingExamplesRE = lazyregexp.New(`(?i),?\s+(?:including|e\.g\.,?|such\s+as|for\s+example)\s+[^?.;()]{1,60}(\?\s*)$`)
	// openListTailRE is a closing list the question calls open (Qrypt: "...,
	// including but not limited to a new H-1B, an H-1B transfer from a current
	// or former employer, ... or green card sponsorship?"): whatever it names,
	// it only widens what is asked. Group 1 is the question mark.
	openListTailRE = lazyregexp.New(`(?i),?\s+including\s+(?:but\s+not\s+limited\s+to|without\s+limitation)\b[^?]{1,240}(\?\s*)$`)
)

// outsideH1BAside reports a user the H-1B set-aside cannot concern: one who
// needs no sponsorship at all, or whose recorded status is of another
// category.
func outsideH1BAside(f Facts) bool {
	if f.RequiresSponsorshipNow != nil && f.RequiresSponsorshipFuture != nil && !*f.RequiresSponsorshipNow && !*f.RequiresSponsorshipFuture {
		return true
	}
	return statusCategory(f.VisaStatus) != nil && !h1bStatusRE.MatchString(f.VisaStatus)
}

// orQuestionsRE joins two questions with "OR" (Mural).
var orQuestionsRE = lazyregexp.New(`(?i)\?\s*\bor\s+`)

// withoutInclusionExamples drops the "(i.e. ...)" and "(e.g. ...)" lists, and
// one closing the question unbracketed, that only widen the sponsorship asked
// about.
func withoutInclusionExamples(text string) string {
	text = openListTailRE.ReplaceAllString(text, "$1")
	text = exampleListRE.ReplaceAllStringFunc(text, func(m string) string {
		if exampleKeepRE.MatchString(m) {
			return m
		}
		return " "
	})
	if m := trailingExamplesRE.FindStringSubmatchIndex(text); m != nil && !exampleKeepRE.MatchString(text[m[0]:m[2]]) {
		text = text[:m[0]] + text[m[2]:]
	}
	return text
}

// derivedSponsorship answers sponsorship-now-or-future and
// work-auth-and-future-sponsorship from the two profile facts.
func derivedSponsorship(s *Step, e *Entry) (Draft, bool) {
	if e.ID == idWorkAuthAndFutureSponsorship {
		return workAuthSponsorshipPicker(s, e), true
	}
	if d, ok := sponsorshipDetailText(s); ok {
		return d, true
	}
	if d, ok := sponsorshipWithStatus(s, e); ok {
		return d, true
	}
	if d, ok := workAuthCompound(s, e); ok {
		return d, true
	}
	if d, ok := answerLedOption(s, e); ok {
		return d, true
	}
	text := s.Q.Text
	if rest := h1bAsideRE.ReplaceAllString(text, ""); rest != text && outsideH1BAside(s.Req.Facts) {
		text = rest
		s.Tracef("the question sets an H-1B transfer or extension aside; the recorded facts are not one -> read without it")
	}
	// Babylist: "currently authorized ... without needing sponsorship now or in
	// the future" names a later need of its own, so the authorization's time
	// word is not the need's and is read past.
	if bare := authorizedNowRE.ReplaceAllString(text, "$1"); bare != text && laterRE.MatchString(bare) {
		text = bare
		s.Tracef("the time word on the authorization is not the need's: the need names its own")
	}
	sc := sponsorshipScope(text)
	need, known := sponsorshipNeed(s.Req.Facts, sc)
	if !known {
		return abstainDraft(ReasonPlaceholderEmpty, "", "the sponsorship fact this question reads is unset"), true
	}
	// Ivo: "Are you currently authorized ... without employer sponsorship?" puts
	// the time word on the authorization, so it reads as now or as now or later;
	// it is answered only when both readings agree.
	if sc == scopeEither && authorizedNowRE.MatchString(text) {
		if now, ok := sponsorshipNeed(s.Req.Facts, scopeNow); !ok || now != need {
			return abstainDraft(ReasonAmbiguous, "", "the time word sits on the authorization, and the recorded need now and later differ: left for review"), true
		}
	}
	negated, readable := sponsorshipPolarityOf(s, text)
	if !readable && need {
		// "Do you now or in the future require a Work Visa, Sponsorship, or
		// Transfer?" (Baton): a transfer is another fact, but a recorded need
		// is one of the list, so Yes is true whatever the transfer; a recorded
		// No says nothing of it. A renewal is the same (Sysdig), and so are a
		// transfer named first (EvolutionIQ) and a green card beside the visa
		// (Helion). Riskified's
		// "authorized ... without sponsorship or transfer" is the negation: one
		// who needs sponsorship is not authorized without it -> No.
		ex := withoutInclusionExamples(text)
		rest := ex
		for _, re := range []*lazyregexp.Regexp{orTransferRE, renewalOrRE, transferOrRE, orGreenCardRE, orVerificationRE, orLMIARE} {
			rest = re.ReplaceAllString(rest, "")
		}
		if rest != ex {
			if n, ok := sponsorshipPolarity(rest, s.Req.Posting.Company); ok {
				negated, readable = n, true
				s.Tracef("the list also names a transfer, a renewal, a green card or an LMIA; the recorded need is one of the list")
			}
		}
	}
	if !readable && need {
		// iCapital: "... in the future (i.e. student visa transfer to H1B,
		// etc)?" An example only widens what is asked, so a recorded need is in
		// it, under a plain question or a negated one (Babylist: "without
		// needing sponsorship ... (e.g., H-1B, work permit, or other
		// visa/authorization extension)").
		if rest := withoutInclusionExamples(text); rest != text {
			if n, ok := sponsorshipPolarity(rest, s.Req.Posting.Company); ok {
				negated, readable = n, true
				s.Tracef("the example list only widens the sponsorship asked about; the recorded need is in it")
			}
		}
	}
	if !readable {
		// Meadow: "... Please Note: We are only considering candidates who do
		// not require sponsorship ...". A note of the employer's limit asks
		// nothing; the question before it is read, whichever way.
		if rest := withoutSponsorLimitNote(text); rest != text {
			if n, ok := sponsorshipPolarity(rest, s.Req.Posting.Company); ok {
				negated, readable = n, true
				s.Tracef("the note only states the employer's sponsorship limit; the question before it is read")
			}
		}
	}
	if !readable && need {
		// Peloton: "Note: Answer yes if you are on an employer-sponsored visa
		// ...". The note only adds applicants who answer Yes; a recorded need
		// answers Yes whatever it adds.
		if rest := withoutAnswerNotes(text); rest != text {
			if n, ok := sponsorshipPolarity(rest, s.Req.Posting.Company); ok && !n {
				negated, readable = false, true
				s.Tracef("the note only widens Yes; the recorded need answers Yes -> Yes")
			}
		}
	}
	if !readable && need {
		// LaunchDarkly: "(Note: This includes ... and applies even if you
		// don't need sponsorship immediately but will in the future ...)". The
		// note only widens Yes; a recorded need answers Yes whatever it adds.
		if rest := withoutInclusionNote(text); rest != text {
			if n, ok := sponsorshipPolarity(rest, s.Req.Posting.Company); ok && !n {
				negated, readable = false, true
				s.Tracef("the inclusion note only widens Yes; the recorded need answers Yes -> Yes")
			}
		}
	}
	if !readable && need {
		// Mural: "If yes, is your eligibility to work dependent on sponsorship
		// from your employer? OR Will you now or in the future need sponsorship
		// from your employer in the location you will be working from?"
		// Questions joined by "OR" ask whether either holds; a recorded need
		// answers Yes to one that asks it plainly.
		if parts := orQuestionsRE.Split(text, -1); len(parts) > 1 {
			for _, part := range parts {
				if n, ok := sponsorshipPolarity(part, s.Req.Posting.Company); ok && !n {
					negated, readable = false, true
					s.Tracef("questions joined by OR: the recorded need answers Yes to %q -> Yes", strings.TrimSpace(part))
					break
				}
			}
		}
	}
	if !readable {
		return abstainDraft(ReasonAmbiguous, "", "the sponsorship wording mixes negations, contrasts or tenses: left for review"), true
	}
	yes := need != negated
	if yes && yesSplitByDetail(s.Q.Options) {
		if o, ok := timeSplitYes(s); ok {
			d := optionDraft(optionBinding{Label: o.Label, Value: o.Value, Score: 1}, e, SourceDerived, true)
			d.Start = startProfileDerive
			d.Trace = []string{"the options split Yes by time; the recorded standing -> " + quoteTerm(o.Label)}
			return d, true
		}
		// GitLab: "No", "Yes, EU Blue Card", ..., "Yes, F-1 Visa OPT (USA)":
		// the one Yes that names the recorded status is the user's, in the
		// review band; none or several leave the choice to them.
		var named []Option
		for _, o := range statusNamedOptions(s) {
			if leadYesRE.MatchString(o.Label) {
				named = append(named, o)
			}
		}
		why := "the options split Yes by visa; the only one naming the recorded status "
		// Both halves of a new-or-transfer split may name the status (CSC
		// Generation: "an H1b transfer", "full H1b sponsorship"); the split
		// decides then.
		if len(named) != 1 {
			if o, ok := newOrTransferYes(s); ok {
				named, why = []Option{o}, "the options split Yes into a new visa and a transfer; the recorded status "
			}
		}
		if len(named) != 1 {
			return abstainDraft(ReasonAmbiguous, "", "the options split Yes by visa: which one is the user's to say"), true
		}
		d := optionDraft(optionBinding{Label: named[0].Label, Value: named[0].Value, Score: 0.8}, e, SourceDerived, false)
		d.Start = 0.8
		d.Trace = []string{why + quoteTerm(s.Req.Facts.VisaStatus) + " -> " + quoteTerm(named[0].Label)}
		return d, true
	}
	var answers []string
	switch {
	case yes:
		answers = []string{"Yes"}
	case negated:
		// The need under a negated question ("able to work without
		// sponsorship?"): No, and the words say the need.
		answers = []string{"No, I will require sponsorship", "No"}
	case sc == scopeNow:
		answers = []string{"No, I do not currently require sponsorship", "No"}
	default:
		answers = []string{"No"}
	}
	trace := "sponsorship facts (scope " + [...]string{"either", "now", "future"}[sc] + ") need=" + boolWord(need) +
		"; negated phrasing=" + boolWord(negated) + " -> " + answers[0]
	return sponsorBind(s, e, need, answers, trace), true
}

// yesSplitByDetail reports options that answer Yes or No with one No and a
// Yes split by detail: "No", "Yes, EU Blue Card", "Yes, but not one of the
// visas listed here", and at most a catch-all "Other" beside them (Truveta).
// It stops at the first option that is none of these, so a long list of
// other options costs one read, not one per option.
func yesSplitByDetail(opts []Option) bool {
	yes, no := 0, 0
	for _, o := range opts {
		switch {
		case isPlaceholderOption(o.Label):
		case leadYesRE.MatchString(o.Label):
			yes++
		case noWordRE.MatchString(o.Label):
			no++
		case otherOptionRE.MatchString(o.Label):
		default:
			return false
		}
	}
	return no == 1 && yes >= 2
}

// timeSplitYes is the Yes of options that split it by time, not by visa
// (Stellar: "Yes, I will require immigration sponsorship now ...", "Yes, ...
// in the future ..."): every Yes states a need now or one only later, one of
// each, and the recorded standing picks it.
func timeSplitYes(s *Step) (Option, bool) {
	f := s.Req.Facts
	if f.RequiresSponsorshipNow == nil {
		return Option{}, false
	}
	want := "current"
	if !*f.RequiresSponsorshipNow {
		if f.RequiresSponsorshipFuture == nil || !*f.RequiresSponsorshipFuture {
			return Option{}, false
		}
		want = "future"
	}
	kinds := map[string][]Option{}
	yes := 0
	for _, o := range realOptions(s.Q.Options) {
		if leadYesRE.MatchString(o.Label) {
			yes++
			k := sponsorOptionKind(o.Label)
			if rest := leadYesRE.ReplaceAllString(o.Label, ""); bareNowRE.MatchString(rest) {
				// Tessera Labs: "Yes, now" and "Yes, in the future" name the
				// time alone, under the question that names the need.
				k = "current"
			} else if bareFutureRE.MatchString(rest) {
				k = "future"
			}
			kinds[k] = append(kinds[k], o)
		}
	}
	if len(kinds["current"]) != 1 || len(kinds["future"]) != 1 || yes != 2 {
		return Option{}, false
	}
	return kinds[want][0], true
}

func boolWord(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// workAuthSponsorshipPicker chooses among the sentence options of the combined
// work-authorization and sponsorship picker: the option stating a current
// requirement when the now fact is true, the one stating only a later
// requirement when now is false and the future is true, the one stating no
// requirement when both are false. Anything else abstains.
func workAuthSponsorshipPicker(s *Step, e *Entry) Draft {
	if s.Q.InputType == InputMultiSelect {
		return workAuthStatements(s)
	}
	if d, ok := compoundPairOption(s, e); ok {
		return d
	}
	f := s.Req.Facts
	var want string
	switch {
	case f.RequiresSponsorshipNow != nil && *f.RequiresSponsorshipNow:
		want = "current"
	case f.RequiresSponsorshipNow != nil && f.RequiresSponsorshipFuture != nil && *f.RequiresSponsorshipFuture:
		want = "future"
	case f.RequiresSponsorshipNow != nil && f.RequiresSponsorshipFuture != nil:
		want = "none"
	default:
		return abstainDraft(ReasonPlaceholderEmpty, "", "a sponsorship fact this picker needs is unset")
	}
	var hits []Option
	for _, o := range realOptions(s.Q.Options) {
		if sponsorOptionKind(o.Label) == want {
			hits = append(hits, o)
		}
	}
	if len(hits) == 0 && want == "future" {
		// brightwheel: a list with no option for a need only later ("I require
		// company sponsorship for a new visa") does not split the time, so the
		// need takes the requirement that does not say it is now.
		for _, o := range realOptions(s.Q.Options) {
			if sponsorOptionKind(o.Label) == "current" && !presentOptionRE.MatchString(o.Label) {
				hits = append(hits, o)
			}
		}
	}
	if len(hits) == 0 && want == "none" {
		// Circleback: beside Yes options qualified by a need, the bare Yes is
		// the one with none; Method: beside a search for sponsorship, the
		// authorization that names none is.
		var bare, authorized []Option
		qualified, seeking := false, false
		for _, o := range realOptions(s.Q.Options) {
			qualified = qualified || yesButNeedRE.MatchString(o.Label)
			seeking = seeking || seekingSponsorRE.MatchString(o.Label)
			if bareYesRE.MatchString(o.Label) {
				bare = append(bare, o)
			}
			if sponsorOptionKind(o.Label) == "" && authUSRE.MatchString(normalizeLabel(o.Label)) {
				authorized = append(authorized, o)
			}
		}
		switch {
		case qualified:
			hits = bare
		case seeking:
			hits = authorized
		}
	}
	if len(hits) != 1 {
		return abstainDraft(ReasonInputMismatch, "", "%d options state the %s sponsorship standing", len(hits), want)
	}
	// The standing decides, but an option that also states the work-authorization
	// fact must agree with the user's recorded answer; and a need of sponsorship
	// does not say the applicant is not authorized (an H-1B holder needs a
	// transfer), so an option that says so takes a recorded answer.
	if stated, ok := statedAuthorization(hits[0].Label, s.Req.Posting.Company); ok && !stated {
		if _, known := storedWorkAuth(s); !known {
			return abstainDraft(ReasonPlaceholderEmpty, "", "the option for the sponsorship standing says the applicant is not authorized, and the recorded work-authorization answer is unset")
		}
	}
	if contradictsWorkAuth(s, hits[0].Label) {
		return compoundAmbiguous("the option for the sponsorship standing states a work authorization the recorded answer contradicts")
	}
	d := optionDraft(optionBinding{Label: hits[0].Label, Value: hits[0].Value, Score: 1}, e, SourceDerived, true)
	d.Start = startProfileDerive
	d.Trace = []string{"work authorization and sponsorship picker: standing " + want + " -> " + quoteTerm(hits[0].Label)}
	return d
}

// laterNeedOptionRE is an option that refuses the need now and states it
// later (Acorns: "I do not require sponsorship now, but I may require
// sponsorship in the future.").
// bareNowRE and bareFutureRE are the rest of a Yes option that names only
// when ("Yes, now", "Yes, in the future", Ginkgo's "Yes, but not until the
// future").
var (
	bareNowRE    = lazyregexp.New(`(?i)^\W*(?:right\s+)?(?:now|currently|immediately|at\s+this\s+time)\W*$`)
	bareFutureRE = lazyregexp.New(`(?i)^\W*(?:(?:in\s+the\s+)?future|later|eventually|in\s+the\s+future\s+only|(?:but\s+)?not\s+until\s+(?:later|(?:in\s+)?the\s+future))\W*$`)
)

var laterNeedOptionRE = lazyregexp.New(`(?i)\bnot\s+(?:\w+\s+){0,2}?(?:require|need)\w*\s+(?:\w+\s+){0,2}?(?:now|currently|at\s+this\s+time)\b.*\bbut\b.*\b(?:will|may|might|could)\s+(?:\w+\s+){0,2}?(?:require|need)\b.*\bfuture\b`)

// aidNeedOptionRE is a need of aid that names no sponsorship, which on the
// sponsorship picker is the sponsorship (Popl: "I require assistance
// immediately.").
var aidNeedOptionRE = lazyregexp.New(`(?i)\b(?:require|need)s?\s+(?:immigration\s+|visa\s+)?(?:assistance|support)\b`)

// sponsorOptionKind classifies a sentence option: none (no requirement), future
// (a requirement only later), transfer (a visa holder's transfer) or current.
func sponsorOptionKind(label string) string {
	switch {
	case transferOptionRE.MatchString(label):
		// A visa transfer is the standing of a visa holder, which the
		// sponsorship facts do not record (Circleback: "Yes, but I will need
		// a visa transfer").
		return "transfer"
	case laterNeedOptionRE.MatchString(label):
		return "future"
	case optionNegatesSponsorship(label) || notRequireRE.MatchString(label):
		return "none"
	case !sponsorMentionRE.MatchString(label) && !aidNeedOptionRE.MatchString(label):
		return ""
	case futureOptionRE.MatchString(label) && !presentOptionRE.MatchString(label):
		return "future"
	}
	return "current"
}

// sponsorshipDetailText answers the free-text "provide additional details on
// your sponsorship" box from sponsorship-type-description when sponsorship is
// needed. It stays blank-safe: a blank description abstains.
func sponsorshipDetailText(s *Step) (Draft, bool) {
	q := s.Q
	if q.Kind != KindText || hasRenderedOptions(q.Options) || !sponsorDetailRE.MatchString(q.Text) {
		return Draft{}, false
	}
	return sponsorshipDescription(s)
}

func sponsorshipDescription(s *Step) (Draft, bool) {
	need, known := sponsorshipNeed(s.Req.Facts, scopeEither)
	if !known {
		return abstainDraft(ReasonPlaceholderEmpty, "", "the sponsorship facts are unset"), true
	}
	if !need {
		return abstainDraft(ReasonNoEntry, "", "no sponsorship is needed: nothing to describe"), true
	}
	desc := entryNamed(s, idSponsorshipTypeDescription)
	text := ownText(desc)
	if text == "" {
		return abstainDraft(ReasonPlaceholderEmpty, "", "%s is blank", idSponsorshipTypeDescription), true
	}
	d := Draft{Entry: desc, Answer: text, Source: SourceDerived, Start: startProfileDerive, Deterministic: true,
		Trace: []string{"sponsorship detail: sponsorship is needed -> " + idSponsorshipTypeDescription}}
	return d, true
}

// statusFollowUpRE is a closing question that asks, of a Yes, the current
// authorization or status (Apollo.io: "... If yes, what is your current work
// authorization?").
var statusFollowUpRE = lazyregexp.New(`(?i)[?.]\s+if\s+(?:yes|so)\s*[,:;-]?\s*(?:what|which)\s+(?:is|are)\s+your\s+(?:current\s+)?(?:work\s+authori[sz]ation(?:\s+status)?|visa(?:\s+(?:status|type))?|immigration\s+status|status)\s*\??\s*$`)

// sponsorshipWithStatus answers one text box that asks the need and, of a
// Yes, the current authorization: the question is read without its
// follow-up; a need is "Yes." and the recorded work-authorization detail (the
// bare status when no detail is composed), no need a bare "No".
func sponsorshipWithStatus(s *Step, e *Entry) (Draft, bool) {
	q := s.Q
	loc := statusFollowUpRE.FindStringIndex(q.Text)
	if q.Kind != KindText || hasRenderedOptions(q.Options) || loc == nil {
		return Draft{}, false
	}
	ask := q.Text[:loc[0]+1]
	need, known := sponsorshipNeed(s.Req.Facts, sponsorshipScope(ask))
	if !known {
		return abstainDraft(ReasonPlaceholderEmpty, "", "the sponsorship fact this question reads is unset"), true
	}
	negated, ok := sponsorshipPolarityOf(s, ask)
	if !ok {
		return abstainDraft(ReasonAmbiguous, "", "the sponsorship wording before the status follow-up is not read: left for review"), true
	}
	if need == negated {
		return entryDraft("No", "sponsorship facts: the question before its status follow-up is answered No"), true
	}
	status := statusOf(s)
	detail, ok := workAuthSentence(status, entryText(s, idWorkAuthorizationExpiry), entryText(s, idStemOptExpiry), s.Req.Today)
	if !ok {
		detail = status
	}
	if strings.TrimSpace(detail) == "" {
		return abstainDraft(ReasonPlaceholderEmpty, "", "the follow-up asks the current authorization and no status is recorded"), true
	}
	return entryDraft("Yes. "+detail, "sponsorship facts: Yes, and the follow-up's current authorization from the recorded status"), true
}

// sponsorshipDetailStage is the pre-classification form of the detail box: the
// question carries no subject beyond "sponsorship", so no route may catch it.
func sponsorshipDetailStage(s *Step) (Draft, bool) {
	if d, ok := sponsorshipDetailText(s); ok {
		return d, true
	}
	return Draft{}, false
}

// conjunctions reads the other conjuncts of a form that defines "Yes" as
// "<needs sponsorship> and <other>", or false when the label is not that form.
func readConjuncts(text string) (others []string, ok bool) {
	loc := conjunctionYesRE.FindStringIndex(text)
	if loc == nil {
		return nil, false
	}
	branch := text[loc[1]:]
	if no := conjunctionNoRE.FindStringIndex(branch); no != nil {
		branch = branch[:no[0]]
	}
	parts := conjunctSplitRE.Split(branch, -1)
	var clean []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			clean = append(clean, p)
		}
	}
	if len(clean) < 2 {
		return nil, false
	}
	at := -1
	for i, p := range clean {
		if conjunctNeedsRE.MatchString(p) {
			at = i
			break
		}
	}
	if at < 0 {
		return nil, false
	}
	for i, p := range clean {
		if i != at {
			others = append(others, p)
		}
	}
	return others, true
}

// conjunctionHolds evaluates the only conjunct the posting can settle: that the
// position is on-site, true when the posting has a known non-remote location.
func conjunctionHolds(conjunct string, j *jobPlace) bool {
	if !conjunctOnSiteRE.MatchString(conjunct) || j == nil || j.Remote {
		return false
	}
	return strings.TrimSpace(j.City) != "" || strings.TrimSpace(j.Country) != ""
}

// conditionalSponsorship answers a yes/no whose "Yes" the form defines as a
// conjunction with the sponsorship need.
func conditionalSponsorship(s *Step) (Draft, bool) {
	q := s.Q
	others, ok := readConjuncts(q.Text)
	if !ok {
		return Draft{}, false
	}
	yes, hasYes := ownYesNoExactlyOne(q.Options, true)
	no, hasNo := ownYesNoExactlyOne(q.Options, false)
	if !hasYes || !hasNo {
		return Draft{}, false
	}
	e := entryNamed(s, idSponsorshipNowOrFuture)
	need, known := sponsorshipNeed(s.Req.Facts, scopeEither)
	if !known {
		return abstainDraft(ReasonPlaceholderEmpty, "", "the sponsorship facts are unset"), true
	}
	job := jobPtr(s.Req)
	block := Abstain{Reason: ReasonContextPolicy, Detail: DetailSponsorshipConjunction}
	if job != nil && strings.TrimSpace(job.Country) != "" && !usCountryRE.MatchString(strings.TrimSpace(job.Country)) {
		return Draft{Entry: e, Abstain: &block, Trace: []string{"conditional sponsorship: the posting country " + quoteTerm(job.Country) + " is not the one the sponsorship facts describe"}}, true
	}
	answerYes := need
	if need {
		for _, c := range others {
			if !conjunctionHolds(c, job) {
				answerYes = false
			}
		}
		if !answerYes {
			return Draft{Entry: e, Abstain: &block, Trace: []string{"conditional sponsorship: Yes needs every other conjunct to hold from the posting; one is unknown or false -> left for the user"}}, true
		}
	}
	chosen := no
	if answerYes {
		chosen = yes
	}
	d := optionDraft(optionBinding{Label: chosen.Label, Value: chosen.Value, Score: 1}, e, SourceDerived, true)
	d.Start = startProfileDerive
	d.Trace = []string{"conditional sponsorship: need=" + boolWord(need) + " -> " + quoteTerm(chosen.Label)}
	return d, true
}

// ownYesNoExactlyOne is the control's Yes or No option when exactly one option
// opens with the word.
func ownYesNoExactlyOne(opts []Option, yes bool) (Option, bool) {
	re := noWordRE
	if yes {
		re = yesWordRE
	}
	var found []Option
	for _, o := range opts {
		if re.MatchString(o.Label) {
			found = append(found, o)
		}
	}
	if len(found) != 1 {
		return Option{}, false
	}
	return found[0], true
}

// SponsorshipTiming is the pair of sponsorship facts the check stage needs to
// understand why a now-only question was answered No.
type SponsorshipTiming struct {
	Now, Future *bool
}

// TimingForReview reports both facts when a now-only sponsorship question was
// answered "No", so the advisory review does not read the user's later need as
// a contradiction. It is false for every other answer.
func TimingForReview(f Facts, q Question, r Result) (SponsorshipTiming, bool) {
	if r.EntryID != idSponsorshipNowOrFuture || r.Outcome == OutcomeAbstained || !isNowOnlySponsorshipQuestion(q.Text) {
		return SponsorshipTiming{}, false
	}
	said := r.ChosenLabel
	if said == "" {
		said = r.Answer
	}
	if !strings.HasPrefix(normalizeLabel(said), "no") {
		return SponsorshipTiming{}, false
	}
	return SponsorshipTiming{Now: f.RequiresSponsorshipNow, Future: f.RequiresSponsorshipFuture}, true
}

// sponsorshipFactOf is the fact the question reads: now, the future or either,
// by its wording. Known is false when a fact it needs is unset.
func sponsorshipFactOf(s *Step) (required, known bool) {
	return sponsorshipNeed(s.Req.Facts, sponsorshipScope(s.Q.Text))
}

// sponsorshipSearchOf complements the fact for a negated sponsorship wording, so
// "able to work without sponsorship" is answered with the opposite of the fact;
// a now-only question about a user with no present need gets the now-only
// "No". A missing fact supplies no candidate (an empty, non-nil list). It
// applies to the sponsorship entry only.
func sponsorshipSearchOf(s *Step, e *Entry) []string {
	if e == nil || e.ID != idSponsorshipNowOrFuture {
		return nil
	}
	need, known := sponsorshipFactOf(s)
	negated, readable := sponsorshipPolarityOf(s, s.Q.Text)
	switch {
	case !known, !readable:
		return []string{}
	case need != negated:
		return []string{"Yes"}
	case negated:
		return []string{"No, I will require sponsorship", "No"}
	case isNowOnlySponsorshipQuestion(s.Q.Text):
		return []string{"No, I do not currently require sponsorship", "No"}
	}
	return nil
}

// idUSWorkAuthStatus is the picker of the applicant's US work-authorization status.
const idUSWorkAuthStatus = "us-work-authorization-status"

var authorizedWordRE = lazyregexp.New(`(?i)\bauthori[sz]ed\b`)

// optionStatesAuthorizationWithoutSponsorship reports an option that affirms
// the applicant is authorized and needs no sponsorship: "without sponsorship"
// restates the authorization, it does not negate it.
func optionStatesAuthorizationWithoutSponsorship(label string) bool {
	if !optionNegatesSponsorship(label) {
		return false
	}
	rest := negatedSponsorRE.ReplaceAllString(label, " ")
	return authorizedWordRE.MatchString(rest) && !hasNegationToken(rest)
}

// sponsorshipStandingOption picks, on a work-authorization status picker, the
// one option that states the applicant's recorded sponsorship standing, in the
// review band: the sole option stating a requirement for a user who will require
// sponsorship, the sole authorized option stating none for a user who will not.
// It applies to the entries whose answer key is work_authorized and to the
// US work-authorization status picker.
func sponsorshipStandingOption(s *Step, e *Entry, required bool) (Draft, bool) {
	if e == nil || e.EffectiveAnswerKey() != "work_authorized" && e.ID != idUSWorkAuthStatus {
		return Draft{}, false
	}
	states, why := optionStatesAuthorizationWithoutSponsorship, "sponsorship is not needed: the only authorized option stating none -> "
	if required {
		states, why = optionStatesSponsorshipRequirement, "sponsorship is needed: the only option stating a requirement -> "
	}
	var standing []Option
	for _, o := range realOptions(s.Q.Options) {
		if states(o.Label) {
			standing = append(standing, o)
		}
	}
	if len(standing) != 1 {
		if e.ID == idUSWorkAuthStatus {
			return statusCategoryOption(s, e, required)
		}
		return Draft{}, false
	}
	d := optionDraft(optionBinding{Label: standing[0].Label, Value: standing[0].Value, Score: 0.8}, e, SourceDerived, false)
	d.Start = 0.8
	d.Trace = []string{why + quoteTerm(standing[0].Label)}
	return d, true
}

// sponsorshipNeedFirst answers the status picker of a user who needs
// sponsorship from the one option stating a requirement, before any stored
// status text: a list that offers it beside "authorized to work for any
// employer" asks the need, and the any-employer status says none is needed
// (Lyft: "I require/will require Lyft's sponsorship ...").
func sponsorshipNeedFirst(s *Step, e *Entry) (Draft, bool) {
	if e == nil || e.ID != idUSWorkAuthStatus || !needsSponsorshipOnSoleOption(s) {
		return Draft{}, false
	}
	return sponsorshipStandingOption(s, e, true)
}

// visaCategoryREs are the status categories a work-authorization statement
// names, most specific first: the recorded status is read as the first it
// names.
var visaCategoryREs = []*lazyregexp.Regexp{
	stemOptCategoryRE, optCategoryRE, lazyregexp.New(`(?i)\bcpt\b`),
	lazyregexp.New(`(?i)\bh-?1-?b1\b`), lazyregexp.New(`(?i)\bh-?1-?b\b`), lazyregexp.New(`(?i)\be-?3\b`),
	lazyregexp.New(`(?i)\btn\b`), lazyregexp.New(`(?i)\bl-?1[ab]?\b`), lazyregexp.New(`(?i)\bo-?1a?\b`),
	lazyregexp.New(`(?i)\bj-?1\b`),
}

// newVisaYesRE and visaTransferYesRE may say who needs it first (Edra: "Yes,
// I will require a new visa sponsorship", "Yes, I will need to transfer an
// existing work visa") and name the H-1B (CSC Generation: "Yes, I would need
// full H1b sponsorship", "Yes, I would need an H1b transfer").
var (
	newVisaYesRE      = lazyregexp.New(`(?i)^\W*yes\b[^a-z]*\(?\s*(?:i\s+(?:will|would)\s+(?:need|require)\s+(?:a\s+)?)?(?:new\s+(?:work\s+)?visa|full\s+h-?1-?b\s+sponsorship)\b`)
	visaTransferYesRE = lazyregexp.New(`(?i)^\W*yes\b[^a-z]*\(?\s*(?:i\s+(?:will|would)\s+need\s+(?:to\s+|an?\s+)?)?(?:(?:visa|h-?1-?b)\s+)?transfer\b`)
)

// newOrTransferYes picks, over a Yes split into a new visa and a visa
// transfer (Point One: "Yes (new visa)", "Yes (visa transfer)"), the one the
// recorded status takes (sponsorKindFor). Other options pick nothing.
func newOrTransferYes(s *Step) (Option, bool) {
	var fresh, transfer []Option
	for _, o := range realOptions(s.Q.Options) {
		switch {
		case newVisaYesRE.MatchString(o.Label):
			fresh = append(fresh, o)
		case visaTransferYesRE.MatchString(o.Label):
			transfer = append(transfer, o)
		}
	}
	if len(fresh) != 1 || len(transfer) != 1 {
		return Option{}, false
	}
	return sponsorKindFor(s.Req.Facts.VisaStatus, fresh[0], transfer[0])
}

// optInOutRE is "opt-in" or "opt out", never the OPT status.
var optInOutRE = lazyregexp.New(`(?i)\bopt[- ]?(?:in|out)\b`)

// statusNamesOneOption reports a blank status picker that the recorded status
// answers: exactly one option names its category, so an empty stored body is
// not a blank to abstain on.
func statusNamesOneOption(s *Step) bool {
	return s.Entry != nil && s.Entry.ID == idUSWorkAuthStatus && len(statusNamedOptions(s)) == 1
}

var (
	stemOptCategoryRE = lazyregexp.New(`(?i)\bstem\s+opt\b`)
	optCategoryRE     = lazyregexp.New(`(?i)\bopt\b`)
)

// statusNamedOptions are the options naming the recorded status's category in
// a sentence with no negation ("This selection does not apply if you have OPT
// or STEM OPT." excludes it); none when the status names no category. An OPT
// status is a STEM OPT one while the applicant is in the STEM extension
// (stemOptNow), and otherwise an option naming STEM OPT is not the OPT one
// (Base Power: "I currently hold OPT ..." beside "I currently hold STEM OPT
// ..."), unless it names plain OPT beside it (Clipbook: "Yes, But i am
// eligible for OPT/STEM OPT"); the extension left blank, the two stay alike.
func statusNamedOptions(s *Step) []Option {
	cat := statusCategory(s.Req.Facts.VisaStatus)
	if cat == nil {
		return nil
	}
	skipStem := false
	if cat == optCategoryRE {
		if now, known := stemOptNow(s); known && now {
			cat = stemOptCategoryRE
		} else if known {
			skipStem = true
		}
	}
	var named []Option
	for _, o := range realOptions(s.Q.Options) {
		if slices.ContainsFunc(sentencesOf(optInOutRE.ReplaceAllString(o.Label, " ")), func(sentence string) bool {
			if skipStem {
				sentence = stemOptCategoryRE.ReplaceAllString(sentence, " ")
			}
			return cat.MatchString(sentence) && !hasNegationToken(sentence)
		}) {
			named = append(named, o)
		}
	}
	return named
}

// statusCategory is the first category the recorded status names, or nil.
func statusCategory(status string) *lazyregexp.Regexp {
	for _, re := range visaCategoryREs {
		if re.MatchString(status) {
			return re
		}
	}
	return nil
}

// statusCategoryOption picks, on a list of work-authorization statements
// (P&G: "Yes, but my work authorization requires either school approval (CPT
// or OPT) or employer-supported USCIS approval ..."), the one option that
// names the recorded status's category, states it plainly (no negation) and
// says nothing the sponsorship facts contradict, in the review band. Options
// that name no category are read for the applicant's standing instead
// (standingOption); a category two options name picks nothing.
func statusCategoryOption(s *Step, e *Entry, required bool) (Draft, bool) {
	named := statusNamedOptions(s)
	if len(named) == 0 {
		return standingOption(s, e, required)
	}
	if len(named) != 1 || hasNegationToken(named[0].Label) || contradictsSponsorship(named[0].Label, required) {
		return Draft{}, false
	}
	d := optionDraft(optionBinding{Label: named[0].Label, Value: named[0].Value, Score: 0.8}, e, SourceDerived, false)
	d.Start = 0.8
	d.Trace = []string{"the only option naming the recorded status " + quoteTerm(s.Req.Facts.VisaStatus) + " -> " + quoteTerm(named[0].Label)}
	return d, true
}

var (
	// newSponsorOptionRE and transferOptionRE split the sponsorship asked for
	// by whether a work visa is held already (Tabs: "New employment visa
	// sponsorship", "Transfer of an existing employment visa").
	newSponsorOptionRE = lazyregexp.New(`(?i)^\W*new\b[^.]*\b(?:sponsorship|visa|petition)\b`)
	transferOptionRE   = lazyregexp.New(`(?i)\btransfer\b`)
	sponsorKindAskRE   = lazyregexp.New(`(?i)\bsponsor(?:ship)?\b|\bvisa\b`)
	// studentStatusRE is a status that is no employer's work visa, so the
	// employer sponsors anew; an employer's visa (employerVisaRE) is transferred.
	studentStatusRE = lazyregexp.New(`(?i)\bf-?1\b|\bopt\b|\bcpt\b|\bj-?1\b`)
)

// sponsorKindFor is the sponsorship a recorded status takes: an employer's work
// visa (H-1B, L-1, O-1, TN, E-3) is transferred, a student status (F-1, OPT,
// CPT, J-1) holds no employer's visa and is sponsored anew; any other status,
// or none, takes neither.
func sponsorKindFor(status string, fresh, transfer Option) (Option, bool) {
	switch {
	case employerVisaRE.MatchString(status):
		return transfer, true
	case studentStatusRE.MatchString(status) && !optInOutRE.MatchString(status):
		return fresh, true
	}
	return Option{}, false
}

var (
	// newH1BNeedAskRE asks the need of a new H-1B (Sigma: "Would you require a
	// new H-1B sponsorship to work with us?"); newH1BPolicyRE with
	// eligibleUnderPolicyRE asks eligibility under a policy that sponsors no new
	// H-1B (Sift: "Sift is unable to sponsor candidates who require new/initial
	// H-1B visa sponsorship for this role. ... Based on this policy, are you
	// eligible for hire for this position?").
	newH1BNeedAskRE       = lazyregexp.New(`(?i)^\W*(?:would|will|do)\s+you\s+(?:require|need)\s+(?:a\s+)?(?:new|initial)(?:\s*/\s*(?:new|initial))?\s+H-?1-?B\b[^?]*\?\s*$`)
	newH1BPolicyRE        = lazyregexp.New(`(?i)\bunable\s+to\s+sponsor\b[^.?!]*\b(?:new|initial)(?:\s*/\s*(?:new|initial))?\s+H-?1-?B\b`)
	eligibleUnderPolicyRE = lazyregexp.New(`(?i)\bare\s+you\s+eligible\b[^?]*\?\s*$`)
)

// newH1BStage answers the need of a new H-1B, or eligibility under a policy
// that sponsors none, from the recorded need and status: a student status that
// needs sponsorship needs a new H-1B (sponsorKindFor), an employer's visa is a
// transfer, and no need needs none. Eligibility is that need's opposite, read
// from the policy, in the review band. An unrecorded need or status is left.
func newH1BStage(s *Step) (Draft, bool) {
	q := s.Q.Text
	asksNeed := newH1BNeedAskRE.MatchString(q)
	asksEligible := !asksNeed && newH1BPolicyRE.MatchString(q) && eligibleUnderPolicyRE.MatchString(q)
	if !asksNeed && !asksEligible {
		return Draft{}, false
	}
	need, known := sponsorshipNeed(s.Req.Facts, scopeEither)
	if !known {
		return Draft{}, false
	}
	needsNew := false
	if need {
		kind, ok := sponsorKindFor(s.Req.Facts.VisaStatus, Option{Label: "new"}, Option{Label: "transfer"})
		if !ok {
			return Draft{}, false
		}
		needsNew = kind.Label == "new"
	}
	o, ok := ownYesNo(s.Q.Options, needsNew != asksEligible)
	if !ok {
		return Draft{}, false
	}
	why := "a new H-1B needed: the recorded need and status " + quoteTerm(s.Req.Facts.VisaStatus)
	if asksEligible {
		why = "eligible under a policy that sponsors no new H-1B: the recorded need and status " + quoteTerm(s.Req.Facts.VisaStatus)
	}
	d := chosenOption(s, o, why)
	if asksEligible {
		d.Start = 0.8
	}
	return d, true
}

// sponsorshipKindStage answers a new / transfer pair from the recorded status
// when sponsorship is needed: an employer's work visa (H-1B, L-1, O-1, TN, E-3)
// is a transfer, a student status (F-1, OPT, CPT, J-1) a new sponsorship. No
// need, an unrecorded need or status and any other option set are left.
func sponsorshipKindStage(s *Step) (Draft, bool) {
	if !sponsorKindAskRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	var fresh, transfer []Option
	for _, o := range realOptions(s.Q.Options) {
		switch {
		case transferOptionRE.MatchString(o.Label):
			transfer = append(transfer, o)
		case newSponsorOptionRE.MatchString(o.Label):
			fresh = append(fresh, o)
		default:
			return Draft{}, false
		}
	}
	e := entryNamed(s, idSponsorshipTypeDescription)
	if len(fresh) != 1 || len(transfer) != 1 || e == nil {
		return Draft{}, false
	}
	if need, known := sponsorshipNeed(s.Req.Facts, scopeEither); !known || !need {
		return Draft{}, false
	}
	status := s.Req.Facts.VisaStatus
	chosen, ok := sponsorKindFor(status, fresh[0], transfer[0])
	if !ok {
		return Draft{}, false
	}
	d := optionDraft(optionBinding{Label: chosen.Label, Value: chosen.Value, Score: 1}, e, SourceDerived, true)
	d.Start = startProfileDerive
	d.Trace = []string{"sponsorship kind: the recorded status " + quoteTerm(status) + " -> " + quoteTerm(chosen.Label)}
	return d, true
}
