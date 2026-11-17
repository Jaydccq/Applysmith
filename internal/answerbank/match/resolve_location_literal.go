package match

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// dictatedLiteralRE is a relocation-conditioned instruction that dictates the
// string to type: `If you would need to relocate, please type "relocating"`.
// Straight, curly and single quotes are accepted, and the literal is capped at
// 40 characters so a stray quote pair around a sentence cannot become an
// answer. The condition must be about relocating: `If you have a disability,
// please type "yes"` is never obeyed.
var dictatedLiteralRE = lazyregexp.New(`(?i)\bif\s+you\b[^.?!]{0,80}?\brelocat\w*\b[^.?!]{0,60}?(?:please\s+)?(?:type|enter|write|put|input|indicate)\b[^"'\x{201c}\x{201d}\x{2018}\x{2019}]{0,12}?["'\x{201c}\x{2018}]([^"'\x{201d}\x{2019}]{1,40})["'\x{201d}\x{2019}]`)

// policyOf is the relocation policy shared by the bound entries of the request,
// nil while it is unconfigured.
func policyOf(s *Step) *RelocationPolicy {
	for i := range s.Entries {
		if lp := s.Entries[i].LocationPolicy; lp != nil && lp.Resolved != nil {
			return lp.Resolved
		}
	}
	return nil
}

// relocationLiteral types the literal a form dictates for the relocation case,
// only when the place is known, the applicant is not there and would relocate
// to it; otherwise the control abstains.
func relocationLiteral(s *Step) (Draft, bool) {
	q := s.Q
	if q.Kind != KindText || hasRenderedOptions(q.Options) {
		return Draft{}, false
	}
	m := dictatedLiteralRE.FindStringSubmatch(q.Text)
	if m == nil {
		return Draft{}, false
	}
	pol := policyOf(s)
	if pol == nil {
		return abstainDraft(ReasonPlaceholderEmpty, "", "relocation literal: the relocation preference is unset"), true
	}
	job := jobPtr(s.Req)
	known := len(questionPlaces(pol, q.Text)) > 0 || job != nil && !job.Remote && job.known()
	if !known || currentlyAt(pol, q.Text, job) == verdictYes || willingToWorkAt(pol, q.Text, job) != verdictYes {
		return abstainDraft(ReasonNoEntry, "", "relocation literal: the applicant is at the place, would not relocate to it, or the place is unknown -> not typed"), true
	}
	d := Draft{Answer: strings.TrimSpace(m[1]), Source: SourceFormText, Deterministic: true,
		Trace: []string{"relocation literal: the form dictates " + quoteTerm(m[1]) + " for a relocating applicant"}}
	return d, true
}

// relocationReasonRE asks one who lives elsewhere why the place is a target
// (SumUp: "If you live outside of the greater Boulder area, please let us know
// why Boulder is a target area for you.").
var relocationReasonRE = lazyregexp.New(`(?i)\bif\s+you\s+(?:currently\s+)?(?:live|reside|are\s+(?:located|based))\s+outside\b[^.?!]{0,80}?\b(?:let\s+us\s+know|tell\s+us|share|explain|describe)\s+why\b`)

// officeNamedRE and dailyNamedRE read whether a relocation-reason box names an
// office and attendance on every working day (SumUp: "office Daily (M-F)").
var (
	officeNamedRE = lazyregexp.New(`(?i)\boffices?\b|\bon-?site\b|\bin[\s-]person\b`)
	dailyNamedRE  = lazyregexp.New(`(?i)\bdaily\b|\bevery\s*day\b|\b(?:5|five)\s+days\b|\bm-f\b|\bmonday\s*(?:through|to|-|\x{2013})\s*friday\b`)
)

// relocationReason answers an office-attendance box that also asks a non-local
// applicant why the place is a target: "Yes" for one who lives there; for one
// who lives elsewhere and would relocate to it, "Yes" and the reason, written
// from the address and the relocation preference. Anyone else is left.
func relocationReason(s *Step) (Draft, bool) {
	q := s.Q
	if q.Kind != KindText || hasRenderedOptions(q.Options) || !relocationReasonRE.MatchString(q.Text) {
		return Draft{}, false
	}
	pol, job := policyOf(s), jobPtr(s.Req)
	if pol == nil || job == nil || job.Remote || strings.TrimSpace(job.City) == "" {
		return abstainDraft(ReasonPlaceholderEmpty, "", "relocation reason: the relocation preference or the posting's city is unknown"), true
	}
	if e := entryNamed(s, "office-attendance-ok"); e == nil || !strings.EqualFold(strings.TrimSpace(ownText(e)), "yes") {
		return abstainDraft(ReasonNoEntry, "", "relocation reason: office attendance is not a recorded Yes"), true
	}
	if currentlyAt(pol, q.Text, job) == verdictYes {
		return Draft{Answer: "Yes", Source: SourceDerived, Deterministic: true, Start: startProfileDerive,
			Trace: []string{"relocation reason: the applicant lives at " + quoteTerm(job.City) + " -> \"Yes\""}}, true
	}
	a := s.Req.Facts.Address
	if willingToWorkAt(pol, q.Text, job) != verdictYes || strings.TrimSpace(a.City) == "" {
		return abstainDraft(ReasonNoEntry, "", "relocation reason: the applicant would not relocate to the place, or the home city is unrecorded"), true
	}
	place := job.City
	if job.Region != "" {
		place += ", " + job.Region
	}
	homeTown := a.City
	if a.Region != "" {
		homeTown += ", " + a.Region
	}
	company := strings.TrimSpace(s.Req.Posting.Company)
	why := "I am open to relocating for the right role, and this position is what makes " + job.City + " a target area for me."
	if company != "" {
		why = "I am open to relocating for the right role, and this position at " + company + " is what makes " + job.City + " a target area for me."
	}
	// The reason promises only the attendance the question names.
	attendance := ""
	if officeNamedRE.MatchString(q.Text) {
		attendance = " to work from the office"
		if dailyNamedRE.MatchString(q.Text) {
			attendance += " daily"
		}
	}
	answer := "Yes. I currently live in " + homeTown + ", and I am willing to relocate to " + place + attendance + ". " + why
	return Draft{Answer: answer, Source: SourceDerived, Deterministic: true, Start: startProfileDerive,
		Trace: []string{"relocation reason: the applicant lives in " + quoteTerm(homeTown) + " and would relocate to " + quoteTerm(place) + " -> the Yes and the reason"}}, true
}

// relocationPlacesAskRE asks in a text box where the applicant would move
// (AHEAD: "If so, where would you be open to relocating?").
var relocationPlacesAskRE = lazyregexp.New(`(?i)\bwhere\b[^?]{0,40}\b(?:open|willing|like|prefer|want)\s+to\s+(?:relocat\w*|move)\b|\b(?:which|what)\s+(?:cities|locations|places|areas)\b[^?]{0,40}\b(?:relocat\w*|move)\b`)

// relocationPlaces answers a text box asking where the applicant would move
// with the places the relocation preference names, in its order, or, open
// with none named, that any place will do. Closed with none named is left.
func relocationPlaces(s *Step) (Draft, bool) {
	q := s.Q
	if q.Kind != KindText || hasRenderedOptions(q.Options) || !relocationPlacesAskRE.MatchString(q.Text) {
		return Draft{}, false
	}
	pol := policyOf(s)
	if pol == nil {
		return Draft{}, false
	}
	answer := strings.Join(pol.Places, ", ")
	switch {
	case answer != "":
	case pol.Default == RelocationOpen:
		answer = "Open to relocating anywhere"
	default:
		return abstainDraft(ReasonNoEntry, "", "relocation places: the preference names no place and is not open"), true
	}
	return Draft{Answer: answer, Source: SourceDerived, Deterministic: true, Start: startProfileDerive,
		Trace: []string{"relocation places: the relocation preference -> " + quoteTerm(answer)}}, true
}

// authorizedOnsiteStatementRE is a lone box stating US work authorization and
// in-person work at a place (Verisoul, Ashby: "I'm authorized to work in the
// US and am excited to work in-person in Austin, Texas").
var authorizedOnsiteStatementRE = lazyregexp.New(`(?i)^\W*i(?:['\x{2019}]m|\s+am)\s+(?:legally\s+)?authori[sz]ed\s+to\s+work\s+in\s+the\s+(?:u\.?s\.?a?|united\s+states)\.?\s+and\s+(?:am\s+)?(?:excited|able|willing|happy|eager|ready)\s+to\s+work\s+(?:in[- ]person|on-?site|in[- ]office)\s+in\s+[^.?!]+[.!]?\s*$`)

// authorizedOnsiteStatement ticks a required lone box whose statement joins
// the user's US work authorization and in-person work at a place: the user's
// own work-auth-us entry says Yes and the relocation policy would work at the
// place. Anything else leaves it to the user.
func authorizedOnsiteStatement(s *Step) (Draft, bool) {
	opts := realOptions(s.Q.Options)
	if s.Q.Kind != KindCheckbox || len(opts) != 1 || !s.Raw.IsRequired() || !authorizedOnsiteStatementRE.MatchString(opts[0].Label) {
		return Draft{}, false
	}
	auth := entryNamed(s, idWorkAuthUS)
	if auth == nil || !isYesLabel(auth.Answer) || willingToWorkAt(policyOf(s), opts[0].Label, jobPtr(s.Req)) != verdictYes {
		return abstainDraft(ReasonNoEntry, "", "authorized and on-site statement: the US work authorization is not a recorded Yes, or the relocation policy would not work at the place -> not ticked"), true
	}
	d := optionDraft(optionBinding{Label: opts[0].Label, Value: opts[0].Value, Score: 1}, &Entry{}, SourceDerived, true)
	d.Start = startProfileDerive
	d.ChosenLabels = []string{opts[0].Label}
	d.Trace = []string{"authorized and on-site statement: work-auth-us is Yes and the relocation policy would work at the place -> ticked"}
	return d, true
}
