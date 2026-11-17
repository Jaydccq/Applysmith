package match

import (
	"strconv"

	"applysmith/internal/lazyregexp"
)

// The owner's standing answers to questions no record settles, each the
// owner's own decision (docs/architecture/apply.md).

var (
	// smallestCompanyRE asks the size of the smallest company the applicant
	// worked for (Osano: "What is the smallest size company you have worked
	// for?").
	smallestCompanyRE = lazyregexp.New(`(?i)\bsmallest\b[^?]{0,40}\b(?:compan(?:y|ies)|organi[sz]ations?|employers?|startups?)\b[^?]{0,40}\b(?:worked|been\s+employed)\b`)
	// sponsorshipQuestionsAskRE asks whether the applicant will answer the
	// employer's questions about sponsoring them (Flatiron: "If yes you will
	// require sponsorship: Are you willing to answer questions to help the
	// company evaluate potential sponsorship of a work visa?").
	sponsorshipQuestionsAskRE = lazyregexp.New(`(?i)\bwilling\s+to\s+(?:answer|respond\s+to)\s+(?:some\s+|a\s+few\s+|additional\s+|further\s+)?questions\b[^?]{0,100}\b(?:sponsor\w*|visas?|immigration|work\s+authori[sz]ation)\b`)
)

// ownerSmallestCompany is the owner's standing answer to the smallest company
// worked for, in employees.
const ownerSmallestCompany = 10

// smallestCompanySize answers the smallest company worked for with ten
// employees: "10" in a box, else the one option whose range holds it
// ("1–10 employees").
func smallestCompanySize(s *Step) (Draft, bool) {
	if !smallestCompanyRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	trace := "smallest company worked for: the owner's standing answer, " + strconv.Itoa(ownerSmallestCompany) + " employees"
	if len(s.Q.Options) == 0 {
		d := derivedDraft(strconv.Itoa(ownerSmallestCompany), "", startProfileDerive, trace)
		d.Source = SourceOwnerDefault
		return d, true
	}
	for _, o := range realOptions(s.Q.Options) {
		if b, ok := parseYearsBucket(o.Label); ok && b.lo <= ownerSmallestCompany && ownerSmallestCompany <= b.hi {
			d := chosenOption(s, o, trace)
			d.Source = SourceOwnerDefault
			return d, true
		}
	}
	return abstainDraft(ReasonInputMismatch, "", "%s: no option among %d holds it", trace, len(s.Q.Options)), true
}

// sponsorshipQuestionsYes answers Yes to being willing to answer the
// employer's questions about sponsorship.
func sponsorshipQuestionsYes(s *Step) (Draft, bool) {
	if !sponsorshipQuestionsAskRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	yes, ok := ownYesNo(s.Q.Options, true)
	if !ok {
		return Draft{}, false
	}
	d := chosenOption(s, yes, "willing to answer the employer's sponsorship questions: the owner's standing Yes")
	d.Source = SourceOwnerDefault
	return d, true
}

// Entries whose blank the owner answers No: no Google Scholar profile, no
// publication, no portfolio (the owner's decision). A Yes/No question takes
// its No; a required box is typed "No"; an optional box stays blank.
const (
	idGoogleScholarURL       = "google-scholar-url"
	idPublications           = "publications"
	idCompetingOffers        = "competing-offers"
	idCompetingOfferDeadline = "competing-offer-deadline"
)

// deriveBlankIsNo answers google-scholar-url and publications: the user's own
// answer, else the owner's No for a blank.
func deriveBlankIsNo(s *Step, e *Entry) (Draft, bool) {
	if own := ownText(e); own != "" {
		return bindEntryAnswer(s, e, own, "the user's own answer"), true
	}
	if !hasRenderedOptions(s.Q.Options) && !s.Raw.IsRequired() {
		return abstainDraft(ReasonPlaceholderEmpty, "", "%s: blank, and the box is optional", e.ID), true
	}
	return bindEntryAnswer(s, e, "No", e.ID+": blank, which the owner answers No"), true
}

// deriveOfferDeadline answers competing-offer-deadline: the user's own date,
// else "N/A" while competing-offers says No, as a deadline follows only an
// offer held.
func deriveOfferDeadline(s *Step, e *Entry) (Draft, bool) {
	if own := ownText(e); own != "" {
		return bindEntryAnswer(s, e, own, "the user's own answer"), true
	}
	offers := entryText(s, idCompetingOffers)
	if offers == "" || affirmative(offers) {
		return abstainDraft(ReasonPlaceholderEmpty, "", "%s: blank, and %s does not say No", e.ID, idCompetingOffers), true
	}
	if hasRenderedOptions(s.Q.Options) {
		return abstainDraft(ReasonPlaceholderEmpty, "", "%s: no offer is held, and the options name no none", e.ID), true
	}
	return bindEntryAnswer(s, e, "N/A", idCompetingOffers+" says No: no offer, so no deadline"), true
}
