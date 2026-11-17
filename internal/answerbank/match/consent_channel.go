package match

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// textConsentEntryID is the entry that records whether the user agrees to be
// texted. Some forms ask it as a channel preference ("Email only" beside "SMS
// text and email") instead of Yes/No; the recorded Yes or No still decides.
const textConsentEntryID = "marketing-text-consent"

var (
	textChannelRE      = lazyregexp.New(`\b(?:sms|texts?|text\s+messages?)\b`)
	textNegationRE     = lazyregexp.New(`\b(?:no|not|never|without|don't|do not|opt[- ]?out)\b`)
	otherChannelOnlyRE = lazyregexp.New(`\b(?:e-?mail|phone(?:\s+calls?)?|mail)\s+only\b|\bonly\s+(?:by\s+)?(?:e-?mail|phone)\b`)
	// bareOptInRE and bareOptOutRE are a bare Opt-In or Opt-Out. In a list the
	// text-consent entry already claimed, the question names the channel (Visa).
	bareOptInRE  = lazyregexp.New(`^opt[- ]?in$`)
	bareOptOutRE = lazyregexp.New(`^opt[- ]?out$`)
	// channelLabelRE is an option naming one way to be reached (Elastic's
	// "Email", "Phone Call", "Text Message", "WhatsApp"); textLikeChannelRE is
	// one of them the text consent decides.
	channelLabelRE    = lazyregexp.New(`^(?:e-?mail|(?:tele)?phone(?:\s+calls?)?|(?:phone\s+|voice\s+)?calls?|texts?|text\s+messages?|sms(?:\s+text)?|whats\s?app)$`)
	textLikeChannelRE = lazyregexp.New(`^(?:texts?|text\s+messages?|sms(?:\s+text)?|whats\s?app)$`)
)

// channelList reports a list whose every option names one way to be reached,
// at least one by text and one not: the question asks which channels to use.
func channelList(opts []Option) bool {
	text, other := 0, 0
	for _, o := range realOptions(opts) {
		l := normalizeLabel(o.Label)
		switch {
		case !channelLabelRE.MatchString(l):
			return false
		case textLikeChannelRE.MatchString(l):
			text++
		default:
			other++
		}
	}
	return text > 0 && other > 0
}

// channelsConsented ticks every channel of a channel list the recorded text
// consent allows: all of them on Yes, all but the text channels on No (the
// owner's default). Any other answer is left to the user.
func channelsConsented(s *Step, e *Entry) Draft {
	yes := false
	switch normalizeLabel(e.Answer) {
	case "yes":
		yes = true
	case "no":
	default:
		return abstainDraft(ReasonInputMismatch, "", "text-consent answer %q is neither Yes nor No: left to the user", e.Answer)
	}
	var labels []string
	for _, o := range realOptions(s.Q.Options) {
		if yes || !textLikeChannelRE.MatchString(normalizeLabel(o.Label)) {
			labels = append(labels, o.Label)
		}
	}
	return Draft{Entry: e, Source: SourceEntry, Deterministic: true,
		Answer: strings.Join(labels, ", "), ChosenLabel: labels[0], ChosenLabels: labels, HasOption: true, OptionScore: 0.96,
		Trace: []string{"text-consent " + quoteTerm(e.Answer) + " ticks the channels [" + strings.Join(labels, ", ") + "]"}}
}

// textsOptIn reports a normalized label that names texts as a channel it
// will use.
func textsOptIn(label string) bool {
	return textChannelRE.MatchString(label) && !textNegationRE.MatchString(label) && !otherChannelOnlyRE.MatchString(label)
}

// textsOptOut reports a normalized label that leaves texts off: it refuses
// them, or it names another channel as the only one.
func textsOptOut(label string) bool {
	if textChannelRE.MatchString(label) {
		return textNegationRE.MatchString(label)
	}
	return otherChannelOnlyRE.MatchString(label)
}

// textChannelOptions finds the options that clearly opt in to and out of
// texts. Each is the sole such option; ok is false unless both exist, so a
// list that does not settle the question is never answered.
func textChannelOptions(opts []Option) (in, out Option, ok bool) {
	nIn, nOut := 0, 0
	for _, o := range realOptions(opts) {
		l := normalizeLabel(o.Label)
		switch {
		case textsOptIn(l) || bareOptInRE.MatchString(l):
			in, nIn = o, nIn+1
		case textsOptOut(l) || bareOptOutRE.MatchString(l):
			out, nOut = o, nOut+1
		}
	}
	return in, out, nIn == 1 && nOut == 1
}

// textChannelConsent binds the recorded Yes or No of the text-consent entry to
// the channel option that includes or leaves out texts. Any other answer is
// left to the user.
func textChannelConsent(s *Step, e *Entry) Draft {
	in, out, _ := textChannelOptions(s.Q.Options)
	var pick Option
	switch normalizeLabel(e.Answer) {
	case "yes":
		pick = in
	case "no":
		pick = out
	default:
		return abstainDraft(ReasonInputMismatch, "", "text-consent answer %q is neither Yes nor No: left to the user", e.Answer)
	}
	d := optionDraft(optionBinding{Label: pick.Label, Value: pick.Value, Score: 0.96}, e, SourceEntry, true)
	d.Trace = append(d.Trace, "text-consent "+quoteTerm(e.Answer)+" bound to the channel option "+quoteTerm(pick.Label))
	return d
}

// advertisingShareRE asks to share the applicant's personal information with
// advertising or marketing partners (P&G).
var advertisingShareRE = lazyregexp.New(`(?i)\bshare\s+(?:my|your)\s+(?:personal\s+)?(?:information|data|details)\s+with\b[^.]{0,40}\b(?:advertising|marketing|media)\s+partners\b`)

// advertisingShareDeclined answers a Yes/No asking to share the applicant's
// personal information with advertising or marketing partners No: an opt-in is
// never given for the user, and declining it is always an answer the page
// takes.
func advertisingShareDeclined(s *Step) (Draft, bool) {
	if !advertisingShareRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	no, okN := ownYesNo(s.Q.Options, false)
	if _, okY := ownYesNo(s.Q.Options, true); !okY || !okN {
		return Draft{}, false
	}
	d := derivedDraft(no.Label, "advertising_share", startProfileDerive, "advertising-share: an opt-in to share personal information with advertising partners is never given -> "+no.Label)
	d.ChosenLabel = no.Label
	d.Deterministic = true
	return d, true
}

var (
	// profileVisibilityRE asks who may see or be sent the applicant's profile or
	// resume (SuccessFactors' "Allow My Profile and Resume to be visible or
	// shared with:").
	profileVisibilityRE = lazyregexp.New(`(?i)\b(?:profile|r[eé]sum[eé]|cv)\b[^.?]{0,60}\b(?:visible|visibility|shared)\b`)
	// onlyAppliedRE is the choice that keeps them to the jobs the applicant
	// applies to ("Only RWE recruiters managing jobs I apply to").
	onlyAppliedRE = lazyregexp.New(`(?i)\bonly\b[^.]{0,60}\b(?:jobs?|positions?|roles?|requisitions?)\s+(?:that\s+)?i\s+(?:apply|applied|have\s+applied)\b`)
)

// profileVisibilityNarrowest answers a choice of who may see the applicant's
// profile and resume with the one option that keeps them to the jobs applied
// to: sharing beyond the application is never opted into, and the narrowest
// choice is always one the page takes.
func profileVisibilityNarrowest(s *Step) (Draft, bool) {
	if !profileVisibilityRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	var pick Option
	n := 0
	for _, o := range realOptions(s.Q.Options) {
		if onlyAppliedRE.MatchString(o.Label) {
			pick, n = o, n+1
		}
	}
	if n != 1 {
		return Draft{}, false
	}
	d := derivedDraft(pick.Label, "profile_visibility", startProfileDerive, "profile-visibility: sharing beyond the jobs applied to is never opted into -> "+pick.Label)
	d.ChosenLabel = pick.Label
	d.Deterministic = true
	return d, true
}
