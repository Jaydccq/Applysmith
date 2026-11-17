package match

import (
	"fmt"
	"strings"

	"applysmith/internal/lazyregexp"
)

// residenceAreaRE names a finer or other place than the country (a state, a
// city, an address), which a country does not answer.
var residenceAreaRE = lazyregexp.New(`(?i)\b(?:city|town|state|province|region|county|zip|postal|address|metro\w*)\b`)

// rerouteLongResidenceList answers a question on where the applicant lives over
// a list that runs past the 100 options the engine sends, so it arrives with
// none (Groupon: "Do you currently reside where this role is based, or would
// you need to relocate?" over 196 countries): no state or Yes/No list runs that
// long, so it is a country list and takes the country. Only a question no entry
// answered is re-routed; one naming a finer place or a preference is not.
func rerouteLongResidenceList(req *Request, qs []Question, results []Result) {
	for i := range results {
		if i >= len(qs) {
			continue
		}
		q := qs[i]
		if q.Kind != KindSelect && q.Kind != KindCustomListbox || len(q.Options) != 0 || results[i].Outcome != OutcomeAbstained || results[i].Terminal ||
			!resideAskRE.MatchString(q.Text) || residePreferRE.MatchString(q.Text) || residenceAreaRE.MatchString(q.Text) {
			continue
		}
		proxy := req.Questions[i]
		proxy.Text = "Country"
		rr, err := req.Rematch(proxy)
		if err != nil || rr.EntryID != "address-country" || rr.Outcome == OutcomeAbstained {
			continue
		}
		rr.Selector = results[i].Selector
		rr.Trace = append([]string{"residence list past 100 options, sent with none: a country list: re-routed to address-country"}, rr.Trace...)
		results[i] = rr
	}
}

var residenceQuestionRE = lazyregexp.New(`(?i)\bwhere\s+do\s+you\s+(?:currently\s+)?(?:reside|live)\b|\bwhat\s+is\s+your\s+(?:current\s+)?location\b`)

// residenceProxies are the coarser location entries, finest first, each with a
// label that routes to it. State goes before country: a state list often has an
// "Outside the United States" option that a country answer would contain-match,
// while no country list carries the user's state (its code, which a country's
// ISO code can share, is never read as that country).
var residenceProxies = []struct{ entry, text string }{
	{"address-state", "State"},
	{"address-country", "Country"},
}

// swappable reports a result a reroute may replace: an entry won it and it is
// not a terminal abstain.
func swappable(r Result, entryID string) bool {
	return r.EntryID == entryID && !r.Terminal
}

// rerouteResidenceOptionList answers "Where do you currently reside?" rendered
// as a closed list of states or countries. The phrasing routes to the city
// entry, right for an option-less typeahead; when the city is not among the
// options the control is re-matched under each coarser entry's own label and the
// first re-match that fills wins. A native select that arrives with no options
// holds more than the engine sends (100): no list of states runs that long, so
// it is a country list (HelloFresh's 196; Lever's "What is your location?",
// 250) and takes the country, though the city filled it. It never claims a
// question another entry won.
func rerouteResidenceOptionList(req *Request, qs []Question, results []Result) {
	for i := range results {
		if i >= len(qs) || !swappable(results[i], "address-city") || !residenceQuestionRE.MatchString(qs[i].Text) {
			continue
		}
		long := qs[i].Kind == KindSelect && len(qs[i].Options) == 0 && isFilled(results[i])
		if !long && (len(qs[i].Options) < 2 || results[i].Outcome != OutcomeAbstained) {
			continue
		}
		for _, p := range residenceProxies {
			if long && p.entry != "address-country" {
				continue
			}
			proxy := req.Questions[i]
			proxy.Text = p.text
			rr, err := req.Rematch(proxy)
			if err != nil || rr.EntryID != p.entry || rr.Outcome == OutcomeAbstained {
				continue
			}
			rr.Selector = results[i].Selector
			why := fmt.Sprintf("residence option list: the city is not among %d options", len(qs[i].Options))
			if long {
				why = "residence option list: a native list sent without its options runs past 100, a country list"
			}
			rr.Trace = append([]string{why + ": re-routed to " + p.entry}, rr.Trace...)
			results[i] = rr
			break
		}
	}
}

var searchChromeRE = lazyregexp.New(`(?i)^\s*search by`)
var countryWordRE = lazyregexp.New(`(?i)\bcountry\b`)

// rerouteGeoSearchPlaceholder fixes a State picker whose search box reads "Search
// by country/region or code": placeholder chrome, not a label, whose "country"
// token routed the control to the country entry. Only when the form also carries
// a dedicated country control that is not itself search chrome is the control
// re-matched under the state entry's own label.
func rerouteGeoSearchPlaceholder(req *Request, qs []Question, results []Result) {
	dedicated := false
	for _, q := range qs {
		if !searchChromeRE.MatchString(strings.TrimSpace(q.Text)) && countryWordRE.MatchString(q.Text) {
			dedicated = true
			break
		}
	}
	if !dedicated {
		return
	}
	for i := range results {
		if i >= len(qs) || len(qs[i].Options) != 0 || !swappable(results[i], "address-country") || !searchChromeRE.MatchString(strings.TrimSpace(qs[i].Text)) {
			continue
		}
		proxy := req.Questions[i]
		proxy.Text = "State or Region"
		rr, err := req.Rematch(proxy)
		if err != nil || rr.EntryID != "address-state" || rr.Outcome == OutcomeAbstained {
			continue
		}
		rr.Selector = results[i].Selector
		rr.Trace = append([]string{fmt.Sprintf("geo-search placeholder reroute: %q is typeahead chrome and the form already has a dedicated country control: re-routed to address-state", clip(qs[i].Text, 60))}, rr.Trace...)
		results[i] = rr
	}
}

// linkEntryIDs are the entries whose answer is a link to one of the user's
// profiles; a closed list, since a company website or a reference's profile also
// holds a URL and must not be de-duplicated.
// otherLinkRE is a box that asks for "Other URL" or "Other website".
var otherLinkRE = lazyregexp.New(`(?i)\bother\s+(?:websites?|sites?|urls?|links?)\b`)

var linkEntryIDs = setOf("linkedin-url", "github-url", "portfolio-url", "other-website-url", "twitter-url")

var (
	linkSchemeRE = lazyregexp.New(`^https?://`)
	linkSlashRE  = lazyregexp.New(`/+$`)
)

// normalizeLink collapses the cosmetic differences between two spellings of one
// link.
func normalizeLink(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = linkSchemeRE.ReplaceAllString(v, "")
	v = strings.TrimPrefix(v, "www.")
	return linkSlashRE.ReplaceAllString(v, "")
}

func isFilling(r Result) bool {
	return r.Outcome == OutcomeFilled || r.Outcome == OutcomeFilledReview || r.Outcome == OutcomeFilledModel
}

// dedupeLinks makes sure no link is emitted twice in one request. A value belongs
// to the entry whose own answer it is, however many entries store it, and no
// other link entry may emit it while an unowned link remains: the dedicated
// LinkedIn field is filled by the engine's own profile lane the matcher never
// sees, so a purely positional pass would let the portfolio field keep the
// LinkedIn URL. A lone link field keeps its curated value.
func dedupeLinks(req *Request, _ []Question, results []Result) {
	var pool []string
	owner := map[string]string{}
	for _, e := range req.Entries() {
		if !linkEntryIDs[e.ID] || e.Kind != KindFixed {
			continue
		}
		ans := strings.TrimSpace(e.Answer)
		key := normalizeLink(ans)
		if key == "" {
			continue
		}
		if _, claimed := owner[key]; !claimed {
			owner[key] = e.ID
			pool = append(pool, ans)
		}
	}
	filling := 0
	reserved := map[string]string{}
	for _, r := range results {
		if linkEntryIDs[r.EntryID] && isFilling(r) && strings.TrimSpace(r.Answer) != "" {
			filling++
			if key := normalizeLink(r.Answer); owner[key] == r.EntryID {
				reserved[key] = r.EntryID
			}
		}
	}
	if filling == 0 {
		return
	}
	// used is each spent link and the entry that spent it: the same entry asked
	// twice (Gopuff's standard and custom LinkedIn fields) writes its own link
	// again; another entry may not.
	used := map[string]bool{}
	spentBy := map[string]string{}
	ownershipApplies := filling > 1
	mayKeep := func(value, source string) bool {
		key := normalizeLink(value)
		if used[key] {
			return spentBy[key] == source
		}
		if !ownershipApplies {
			return true
		}
		o, owned := owner[key]
		return !owned || o == source
	}
	for i, r := range results {
		answer := strings.TrimSpace(r.Answer)
		if !linkEntryIDs[r.EntryID] || !isFilling(r) || answer == "" {
			continue
		}
		key := normalizeLink(answer)
		if mayKeep(answer, r.EntryID) {
			used[key], spentBy[key] = true, r.EntryID
			continue
		}
		if r.EntryID == "portfolio-url" || r.EntryID == "other-website-url" {
			if alt, ok := nextUnusedLink(pool, key, r.EntryID, used, reserved); ok {
				used[normalizeLink(alt)] = true
				r.Answer, r.ChosenLabel, r.ChosenLabels = alt, "", nil
				r.Outcome = OutcomeFilledReview
				r.Trace = append(r.Trace, fmt.Sprintf("link-dedupe: %q is not %s's own link: %q", answer, r.EntryID, alt))
				results[i] = r
				continue
			}
		}
		r.Answer, r.ChosenLabel, r.ChosenLabels = "", "", nil
		r.Outcome, r.Reason, r.Terminal, r.Confidence = OutcomeAbstained, ReasonDuplicateLink, true, 0
		r.Trace = append(r.Trace, fmt.Sprintf("link-dedupe: %q is not %s's own link and no unowned link remains", answer, r.EntryID))
		results[i] = r
	}
}

// nextUnusedLink is the first pool link that is neither spent nor reserved for a
// dedicated field of another entry.
func nextUnusedLink(pool []string, skip, source string, used map[string]bool, reserved map[string]string) (string, bool) {
	for _, v := range pool {
		k := normalizeLink(v)
		if k == skip || used[k] {
			continue
		}
		if who, held := reserved[k]; held && who != source {
			continue
		}
		return v, true
	}
	return "", false
}

// requiredWebsite fills a required website or portfolio box whose entry is
// blank (BILL: "Website*") with one of the user's own recorded links that no
// other field of the request emits, GitHub and the rest before LinkedIn (whose
// own field the profile lane fills unseen), for review: the form goes no
// further without a link, and the link is the user's. A required "Other URL"
// takes the LinkedIn link: the owner's default (the Institute of Foundation
// Models' Lever form asks LinkedIn, GitHub and a required "Other URL", and the
// engine fills GitHub's box unseen). A portfolio box with no link left is
// answered No (the owner's decision); an optional box stays blank.
func requiredWebsite(req *Request, qs []Question, results []Result) {
	used := map[string]bool{}
	for _, r := range results {
		if linkEntryIDs[r.EntryID] && isFilling(r) {
			used[normalizeLink(r.Answer)] = true
		}
	}
	var links, linkedIn []string
	for _, e := range req.Entries() {
		a := strings.TrimSpace(e.Answer)
		if !linkEntryIDs[e.ID] || e.Kind != KindFixed || e.Placeholder || a == "" {
			continue
		}
		if e.ID == "linkedin-url" {
			linkedIn = append(linkedIn, a)
		} else {
			links = append(links, a)
		}
	}
	links = append(links, linkedIn...)
	for i, r := range results {
		if r.EntryID != "portfolio-url" && r.EntryID != IDOtherWebsiteURL || r.Reason != ReasonPlaceholderEmpty || i >= len(qs) || !qs[i].IsRequired() {
			continue
		}
		fill := func(l, why string) {
			r.Answer, r.Outcome, r.Reason, r.Detail, r.Terminal = l, OutcomeFilledReview, "", "", false
			r.Source, r.Confidence = SourceDerived, ReviewFillConfidence
			r.Trace = append(r.Trace, fmt.Sprintf("required website: the entry is blank; the user's own link %q, %s", l, why))
			results[i] = r
		}
		// The owner's default: a blank required "Other URL" is the LinkedIn
		// link, never a link another field of the page may hold.
		if r.EntryID == IDOtherWebsiteURL && otherLinkRE.MatchString(qs[i].Text) && len(linkedIn) > 0 {
			fill(linkedIn[0], "the LinkedIn link, the default for a blank \"Other URL\"")
			continue
		}
		filled := false
		for _, l := range links {
			if used[normalizeLink(l)] {
				continue
			}
			used[normalizeLink(l)] = true
			fill(l, "which no other field emits")
			filled = true
			break
		}
		if !filled && r.EntryID == IDOtherWebsiteURL && len(linkedIn) > 0 {
			fill(linkedIn[0], "the LinkedIn link, since every other link is in its own field")
		}
		if !filled && r.EntryID == "portfolio-url" {
			r.Answer, r.Outcome, r.Reason, r.Detail, r.Terminal = "No", OutcomeFilledReview, "", "", false
			r.Source, r.Confidence = SourceOwnerDefault, ReviewFillConfidence
			r.Trace = append(r.Trace, "required portfolio: the entry is blank and no link is left: the owner's No")
			results[i] = r
		}
	}
}
