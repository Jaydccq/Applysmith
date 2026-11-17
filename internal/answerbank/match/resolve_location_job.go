package match

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// The posting location. The request carries it as a text such as "Sampleton,
// ES" or "Remote"; when it is absent the first 2,000 characters of the posting
// text are read for a labelled line or explicit remote wording.

const jobHeadChars = 2000

var (
	// locationLineRE is a labelled field, colon required, not a sentence that
	// happens to contain one of the words.
	locationLineRE = lazyregexp.New(`(?i)\b(?:Location|Office|Based(?:\s+in)?)\s*:\s*([^\n\r]{2,80})`)
	// cityRegionRE reads "City, ST" or "City, State". The region is a full state
	// name (longest first, so "New York" is not read as "New"), a two-letter
	// code or one capitalized word; it is not open-ended because the posting
	// text may have its newlines collapsed to spaces. A country in its place is
	// read by countryAfterCity.
	cityRegionRE = lazyregexp.New(`^([A-Za-z][\w.'-]*(?:\s+[A-Za-z][\w.'-]*){0,3}),\s*(` + stateNameAlternation + `|[A-Za-z]{2}|[A-Z][a-z]+)\b`)
	remoteWordRE = lazyregexp.New(`(?i)\bremote\b`)
	// remoteWordingRE is explicit remote wording anywhere in the head.
	remoteWordingRE = lazyregexp.New(`(?i)\bfully\s+remote\b|\bremote\s*\([^)]*\)|\b100%\s*remote\b|\bwork(?:s|ing)?\s+remotely\b|\bremote[-\s]first\b|\bremote\s*-\s*(?:us|usa|u\.s\.)\b`)
)

// jobPlaceOf reads the posting's place: the structured-looking location text of
// the request first, the posting text otherwise. It reports false when nothing
// usable is present.
func jobPlaceOf(p Posting) (jobPlace, bool) {
	if loc := strings.TrimSpace(p.Location); loc != "" {
		return parseLocationText(loc)
	}
	return jobPlaceFromPostingText(p.Description)
}

// parseLocationText reads "City, Region[, Country]", a lone country or a
// remote wording.
func parseLocationText(loc string) (jobPlace, bool) {
	if remoteWordRE.MatchString(loc) {
		return jobPlace{Remote: true}, true
	}
	parts := strings.Split(loc, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	switch len(parts) {
	case 1:
		if usCountryRE.MatchString(parts[0]) {
			return jobPlace{Country: parts[0]}, true
		}
		return jobPlace{City: parts[0]}, parts[0] != ""
	case 2:
		if usCountryRE.MatchString(parts[1]) {
			return jobPlace{Region: parts[0], Country: parts[1]}, parts[0] != ""
		}
		return jobPlace{City: parts[0], Region: parts[1]}, parts[0] != ""
	}
	return jobPlace{City: parts[0], Region: parts[1], Country: parts[len(parts)-1]}, parts[0] != ""
}

// jobPlaceFromPostingText is the fallback reader of the posting text. Only the
// first 2,000 characters are read: the location line is near the top, and
// reading further risks an unrelated city in the body.
func jobPlaceFromPostingText(text string) (jobPlace, bool) {
	head := text
	if len(head) > jobHeadChars {
		head = head[:jobHeadChars]
	}
	if m := locationLineRE.FindStringSubmatch(head); m != nil {
		raw := strings.TrimSpace(m[1])
		if remoteWordRE.MatchString(raw) {
			return jobPlace{Remote: true}, true
		}
		if c := cityRegionRE.FindStringSubmatch(raw); c != nil {
			if country := countryAfterCity(raw[len(c[1]):], c[2]); country != "" {
				return jobPlace{City: strings.TrimSpace(c[1]), Country: country}, true
			}
			return jobPlace{City: strings.TrimSpace(c[1]), Region: strings.TrimSpace(c[2])}, true
		}
	}
	if remoteWordingRE.MatchString(head) {
		return jobPlace{Remote: true}, true
	}
	return jobPlace{}, false
}

// countryAfterCity is the country a location line names after its city
// ("Manchester, United Kingdom"), or "" when the region cityRegionRE read is a
// state, a two-letter code, or no country: tail is the line from the city's
// comma on. The country is the longest run of up to four words that names one,
// so "United Kingdom" is never the region "United".
func countryAfterCity(tail, region string) string {
	if len(region) == 2 || isStateName(region) {
		return ""
	}
	words := strings.Fields(strings.TrimLeft(tail, ", "))
	for n := min(4, len(words)); n >= 1; n-- {
		name := strings.TrimRight(strings.Join(words[:n], " "), ".,;")
		if _, ok := CountryCode(name); ok && len(name) > 2 {
			return name
		}
	}
	return ""
}

var (
	// roleLocationAskRE asks which listed place is the role's own (iCapital:
	// "Please review the job description before applying, to identify the
	// specified location/s for the role.").
	roleLocationAskRE = lazyregexp.New(`(?i)\b(?:identify|select|indicate|choose|which|what)\b[^?.]*\blocations?(?:/s|\(s\))?\s+(?:for|of)\s+(?:the|this)\s+(?:role|position|job|opening)\b`)
	// locationWishRE is the applicant's own wish, which the posting does not answer.
	locationWishRE = lazyregexp.New(`(?i)\bprefer|\bwilling\b|\bopen\s+to\b|\brelocat|\binterest`)
)

// roleLocationPick answers a list of places that asks for the role's own
// location: the options whose city, and state when both name one, are the
// posting's. A posting with no city leaves it to the other lanes; a city no
// option names, or two options on a single-answer control, abstains.
func roleLocationPick(s *Step) (Draft, bool) {
	q := s.Q
	opts := realOptions(q.Options)
	if len(opts) < 2 || !roleLocationAskRE.MatchString(q.Text) || locationWishRE.MatchString(q.Text) {
		return Draft{}, false
	}
	job, ok := jobPlaceOf(s.Req.Posting)
	if !ok || strings.TrimSpace(job.City) == "" {
		return Draft{}, false
	}
	var picked []string
	for _, o := range opts {
		p, ok := parseLocationText(o.Label)
		if !ok || !strings.EqualFold(strings.TrimSpace(p.City), strings.TrimSpace(job.City)) {
			continue
		}
		if a, b := stateCodeOf(p.Region), stateCodeOf(job.Region); a != "" && b != "" && a != b {
			continue
		}
		picked = append(picked, o.Label)
	}
	multi := mselMultiAnswer(q, len(q.Options))
	if len(picked) == 0 || len(picked) > 1 && !multi {
		return abstainDraft(ReasonAmbiguous, "", "role location: %d options name the posting's place %s", len(picked), quoteTerm(job.finest())), true
	}
	d := derivedDraft(picked[0], "role_location", startProfileDerive, "role location: the posting's place "+quoteTerm(job.finest())+" -> "+strings.Join(picked, "; "))
	d.ChosenLabel, d.HasOption, d.OptionScore, d.Deterministic = picked[0], true, 1, true
	if multi {
		d.ChosenLabels, d.Answer = picked, strings.Join(picked, ", ")
	}
	return d, true
}

// jobPtr returns the posting's place for the predicates, nil when unknown.
func jobPtr(r *Request) *jobPlace {
	if j, ok := jobPlaceOf(r.Posting); ok {
		return &j
	}
	return nil
}
