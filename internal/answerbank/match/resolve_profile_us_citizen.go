package match

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// A Yes/No "Are you a U.S. citizen?" is read from the user's own
// us-citizen-status, whose closed choices are a U.S. citizen (Yes) or a U.S.
// national, a lawful permanent resident, a refugee or asylee, or neither (No),
// the classes of U.S. person the export-control lists name beside
// citizenship (usPersonStage); a bare Yes or No
// stored there is that answer. A note before the question that only says the
// position requires citizenship (GRVTY: "This position requires U.S.
// citizenship pursuant to a government contract.") is read away, and after
// it "Do you meet this requirement?" (WalkMe) asks the same. Any other
// wording, an unfilled entry or a status outside the choices leaves the
// question to the classified entries: citizenship is never inferred from a
// country list or a visa. "A U.S. citizen or green card holder (permanent
// resident)?" (Promise) is Yes for a citizen or a permanent resident and No
// for a national or neither; a bare No is no citizen, and no green card holder
// only beside a recorded need for sponsorship, which a permanent resident
// never has.

const idUSCitizenStatus = "us-citizen-status"

const (
	usNameRE  = `(?:u\.?\s?s\.?(?:a\.?)?|united\s+states(?:\s+of\s+america)?)`
	usBasedRE = usNameRE + `[\s-]*based`
)

var (
	usCitizenAskRE = lazyregexp.New(`(?i)^\s*are\s+you\s+(?:currently\s+)?(?:an?\s+(?:` + usBasedRE + `\s+)?` + usNameRE + `\s+citizen|a\s+citizen\s+of\s+the\s+` + usNameRE + `)\s*\??\s*$`)
	// usBasedAskRE is Manifold's "a U.S.-based U.S. citizen": a citizen whose
	// home is in the U.S.
	usBasedAskRE = lazyregexp.New(`(?i)` + usBasedRE)
	// usCitizenRequirementRE is the note; Synack: "Please note: We can only
	// hire U.S. citizens for this position ..."; a federal note: "As this
	// role supports our Federal businesses, US citizenship is required." (WalkMe);
	// Cyware: "US Citizenship is a requirement of this position in accordance
	// with 8 U.S.C 1324b(a)(2)(C).". The note runs to the full stop that ends
	// it: a point inside a word ("U.S.C") or before a lowercase word is not one.
	usCitizenRequirementRE = lazyregexp.New(`(?i)^\s*(?:(?:please\s+)?note:?\s*)?(?:(?:this|the)\s+(?:position|role|job)\s+requires\s+` + usNameRE + `\s+citizenship|` +
		`we\s+(?:can\s+)?only\s+(?:hire|consider|accept|employ)\s+` + usNameRE + `\s+citizens|[^.?!]{0,120}?\b` + usNameRE + `\s+citizenship\s+is\s+(?:required|a\s+requirement))\b(?:[^.?!]|\.\w|(?-i:\.\s+[a-z]))*\.\s*`)
	// meetsRequirementRE asks, after that note, whether the applicant meets it.
	meetsRequirementRE = lazyregexp.New(`(?i)^\s*(?:do|can)\s+you\s+(?:meet|satisfy|fulfill)\s+(?:this|that|the\s+above)\s+requirement\s*\??\s*$`)
	// usCitizenOrLPRAskRE asks for a citizen or a permanent resident.
	usCitizenOrLPRAskRE = lazyregexp.New(`(?i)^\s*are\s+you\s+(?:currently\s+)?an?\s+` + usNameRE + `\s+citizen\s+or\s+(?:an?\s+)?(?:green\s+card\s+holder|(?:lawful\s+)?permanent\s+resident)\s*\??\s*$`)
	lprStatusRE         = lazyregexp.New(`(?i)^(?:(?:lawful\s+)?permanent\s+resident|green\s+card\s+holder)$`)
	citizenStatusYesRE  = lazyregexp.New(`(?i)^(?:yes|` + usNameRE + `\s+citizen|citizen)$`)
	citizenStatusNoRE   = lazyregexp.New(`(?i)^(?:no|` + usNameRE + `\s+national|(?:lawful\s+)?permanent\s+resident|green\s+card\s+holder|refugee(?:\s+or\s+asylee)?|asylee|neither|other|non-?citizen)$`)
)

// usCitizenYesNo answers "Are you a U.S. citizen?" from us-citizen-status.
func usCitizenYesNo(s *Step) (Draft, bool) {
	text := usCitizenRequirementRE.ReplaceAllString(s.Q.Text, "")
	if usCitizenOrLPRAskRE.MatchString(text) {
		return usCitizenOrLPRYesNo(s)
	}
	if !usCitizenAskRE.MatchString(text) && (text == s.Q.Text || !meetsRequirementRE.MatchString(text)) {
		return Draft{}, false
	}
	yes, okY := ownYesNo(s.Q.Options, true)
	no, okN := ownYesNo(s.Q.Options, false)
	e := entryNamed(s, idUSCitizenStatus)
	stated := strings.TrimSpace(ownText(e))
	if !okY || !okN || stated == "" {
		return Draft{}, false
	}
	var pick Option
	why := "the recorded us-citizen-status is " + quoteTerm(stated)
	switch {
	case citizenStatusYesRE.MatchString(stated) && usBasedAskRE.MatchString(s.Q.Text):
		// A citizen is U.S.-based only by a recorded home in the U.S.
		if c, ok := CountryCode(s.Req.Facts.Address.Country); !ok || c != "US" {
			return Draft{}, false
		}
		pick, why = yes, why+" and the recorded home is in the U.S."
	case citizenStatusYesRE.MatchString(stated):
		pick = yes
	case citizenStatusNoRE.MatchString(stated):
		pick = no
	default:
		return Draft{}, false
	}
	return Draft{Entry: e, Answer: pick.Label, ChosenLabel: pick.Label, HasOption: true, OptionScore: 1, Source: SourceDerived, Start: startProfileDerive,
		Trace: []string{"us-citizen: " + why + " -> " + pick.Label}}, true
}

// usCitizenOrLPRYesNo answers "Are you a U.S. citizen or green card holder?"
// from us-citizen-status and, for a bare No, the recorded sponsorship need.
func usCitizenOrLPRYesNo(s *Step) (Draft, bool) {
	yes, okY := ownYesNo(s.Q.Options, true)
	no, okN := ownYesNo(s.Q.Options, false)
	e := entryNamed(s, idUSCitizenStatus)
	stated := strings.TrimSpace(ownText(e))
	if !okY || !okN || stated == "" {
		return Draft{}, false
	}
	var pick Option
	why := "the recorded us-citizen-status is " + quoteTerm(stated)
	switch need, known := sponsorshipNeed(s.Req.Facts, scopeEither); {
	case lprStatusRE.MatchString(stated), citizenStatusYesRE.MatchString(stated):
		pick = yes
	case strings.EqualFold(stated, "no") && known && need:
		pick, why = no, why+" and a need for sponsorship is recorded, which no permanent resident has"
	case !strings.EqualFold(stated, "no") && citizenStatusNoRE.MatchString(stated):
		pick = no
	default:
		return Draft{}, false
	}
	return Draft{Entry: e, Answer: pick.Label, ChosenLabel: pick.Label, HasOption: true, OptionScore: 1, Source: SourceDerived, Start: startProfileDerive,
		Trace: []string{"us-citizen or permanent resident: " + why + " -> " + pick.Label}}, true
}
