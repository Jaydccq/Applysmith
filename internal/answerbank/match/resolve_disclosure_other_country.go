package match

import (
	"slices"
	"strings"

	"applysmith/internal/lazyregexp"
)

// The user's own answers for a country the profile does not record as
// authorized (owner decision: work authorization and sponsorship elsewhere are
// the user's to write in the bank). Both ship blank, so such a question stays
// the posting_country abstain until the user writes Yes or No.
const (
	idWorkAuthOtherCountries    = "work-authorization-other-countries"
	idSponsorshipOtherCountries = "sponsorship-other-countries"
)

// otherCountryAnswer answers a work-authorization or sponsorship question the
// posting-country guard blocked because none of its countries is recorded,
// from the user's other-country entry, for review. The answer goes through the
// entry's own resolver, so a negated wording still takes its complement: the
// work-authorization entry is resolved with the user's other-country answer
// as its answer, and the sponsorship entry with the sponsorship facts set to
// it; either way the question's countries count as recorded for this question
// only, and a negated sponsorship wording ("the ability to work in Australia
// without the need for sponsorship", Shield AI) takes the complement as the
// resolver reads it. The combined entry reads both other-country answers
// (otherCountryStatements). A question that names a recorded country beside
// others, another route variant, another non-fixed entry and a blank or
// non-Yes/No answer keep the abstain, and so does a country of a recorded citizenship: a
// citizen may work in their own country, which the answer for the others does
// not say. The fill is the user's own answer, so it names the other-country
// entry.
func (m *Matcher) otherCountryAnswer(s *Step, cl classification, a Abstain) (Result, bool) {
	codes := s.otherCountries
	s.otherCountries = nil
	e := cl.entry
	if a.Detail != DetailPostingCountry || len(codes) == 0 || cl.tag != "" && cl.tag != "negated-sponsorship" || e == nil {
		return Result{}, false
	}
	if cits := recordedCitizenships(s); slices.ContainsFunc(codes, func(c string) bool { return slices.Contains(cits, c) }) {
		return Result{}, false
	}
	if e.ID == idWorkAuthAndFutureSponsorship {
		return m.otherCountryStatements(s, cl, codes)
	}
	if e.Kind != KindFixed {
		return Result{}, false
	}
	var id string
	switch {
	case e.ID == idSponsorshipNowOrFuture:
		id = idSponsorshipOtherCountries
	case e.EffectiveAnswerKey() == "work_authorized":
		id = idWorkAuthOtherCountries
	default:
		return Result{}, false
	}
	other, yes, ok := otherCountryYesNo(s, id)
	if !ok {
		return Result{}, false
	}
	answer := ownText(other)
	req := *s.Req
	req.Facts.AuthorizedCountries = append(slices.Clip(req.Facts.AuthorizedCountries), codes...)
	if id == idSponsorshipOtherCountries {
		req.Facts.RequiresSponsorshipNow, req.Facts.RequiresSponsorshipFuture = &yes, &yes
	}
	ec := *e
	ec.Answer, ec.Placeholder = answer, false
	s.Req, s.reviewOnly = &req, true
	s.Tracef("the profile records no authorization for %s; the user's %s entry answers it: %s, for review", strings.Join(codes, ", "), id, answer)
	cl.entry = &ec
	r := m.resolveChosen(s, cl)
	if r.EntryID == e.ID {
		r.EntryID, r.AnswerKey, r.Source = id, other.EffectiveAnswerKey(), SourceEntry
	}
	return r, true
}

// otherCountryYesNo is the user's own Yes or No in an other-country entry;
// blank, or anything but Yes or No, is no answer.
func otherCountryYesNo(s *Step, id string) (*Entry, bool, bool) {
	i := slices.IndexFunc(s.Entries, func(o Entry) bool { return o.ID == id })
	if i < 0 {
		return nil, false, false
	}
	other := &s.Entries[i]
	answer := normalizeLabel(ownText(other))
	if other.Placeholder || !other.UserWritten() || answer != "yes" && answer != "no" {
		return nil, false, false
	}
	return other, answer == "yes", true
}

// otherCountryStatements answers the combined work-authorization and
// sponsorship entry (a list of statements to tick, or one question of both)
// about a country the profile does not record, from both of the user's
// other-country answers, for review (Ideogram, Toronto: "I will need
// sponsorship to work in Canada" ticked, "I am authorized to work in Canada"
// not). Either answer blank keeps the abstain.
func (m *Matcher) otherCountryStatements(s *Step, cl classification, codes []string) (Result, bool) {
	_, authorized, okAuth := otherCountryYesNo(s, idWorkAuthOtherCountries)
	_, need, okNeed := otherCountryYesNo(s, idSponsorshipOtherCountries)
	if !okAuth || !okNeed {
		return Result{}, false
	}
	req := *s.Req
	req.Facts.AuthorizedCountries = append(slices.Clip(req.Facts.AuthorizedCountries), codes...)
	req.Facts.RequiresSponsorshipNow, req.Facts.RequiresSponsorshipFuture = &need, &need
	s.Req, s.reviewOnly, s.otherAuth = &req, true, &authorized
	s.Tracef("the profile records no authorization for %s; the user's other-country entries answer it: authorized %t, sponsorship needed %t, for review", strings.Join(codes, ", "), authorized, need)
	return m.resolveChosen(s, cl), true
}

// workInRE is the place a question asks about working in, up to the employer
// it names after "for" ("in Canada for US Mobile Inc").
var workInRE = lazyregexp.New(`(?i)\bwork\s+(?:(?:legally|lawfully)\s+)?in\s+(?:the\s+)?([^?.,;:!()]{2,60}?)(?:\s+for\b|[?.,;:!()]|$)`)

// worksInNamedCountry reports a question whose "work in" names a country
// other than the United States, which has routes of its own.
func worksInNamedCountry(text string) bool {
	m := workInRE.FindStringSubmatch(text)
	if m == nil {
		return false
	}
	_, codes := scanCountries(m[1], false)
	return len(codes) > 0 && !slices.Contains(codes, "US")
}
