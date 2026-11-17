package workspace

import (
	"net/url"
	"path"
	"strings"
	"time"
	_ "time/tzdata" // zone validation must not depend on the host's zoneinfo

	"applysmith/internal/lazyregexp"
)

// askMeMarker is the answer-bank reserved value meaning "leave this to the user".
const askMeMarker = "@ask-me"

var (
	alpha2Re = lazyregexp.New(`^[A-Z]{2}$`)
	alpha3Re = lazyregexp.New(`^[A-Z]{3}$`)
	idRe     = lazyregexp.New(`^[a-z0-9][a-z0-9-]{0,39}$`)       // also a résumé upload name, which must not start with a hyphen
	hhmmRe   = lazyregexp.New(`^([01][0-9]|2[0-3]):[0-5][0-9]$`) // fixed width, as notify's quiet hours read it
)

// clock is this computer's clock, the zone the dashboard writes today's date in.
var clock = time.Now

// checkFormat validates v against a named format. On failure it returns the
// phrase describing the expected form.
func checkFormat(name, v string) (expected string, ok bool) {
	if strings.TrimSpace(v) == "" {
		return "", true // blank means unanswered
	}
	switch name {
	case "https":
		u, err := url.Parse(v)
		return "an absolute https URL such as https://example.com/me",
			err == nil && u.Scheme == "https" && u.Host != "" && !strings.ContainsAny(v, " \t")
	case "date-past":
		// Calendar dates compare, today's on this computer's clock: east of
		// UTC the local day starts before the UTC one.
		t, err := time.Parse("2006-01-02", v)
		y, m, d := clock().Date()
		return "a date in the form YYYY-MM-DD that is not in the future", err == nil && !t.After(time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
	case "month":
		_, err := time.Parse("2006-01", v)
		return "a month in the form YYYY-MM", err == nil
	case "month-present":
		_, err := time.Parse("2006-01", v)
		return "a month in the form YYYY-MM or present", err == nil || v == "present"
	case "hhmm":
		return "a 24-hour time in the form HH:MM", hhmmRe.MatchString(v)
	case "alpha2":
		return "an ISO 3166-1 alpha-2 country code such as US", alpha2Re.MatchString(v)
	case "alpha3":
		return "an ISO 4217 currency code such as USD", alpha3Re.MatchString(v)
	case "timezone":
		_, err := time.LoadLocation(v)
		return "an IANA time zone name such as Europe/London", err == nil && v != "Local"
	case "id":
		return "1 to 40 lowercase letters, digits and hyphens, not starting with a hyphen", idRe.MatchString(v)
	case "pdf-file":
		return "a PDF file such as resumes/backend.pdf", strings.EqualFold(path.Ext(v), ".pdf")
	}
	panic("workspace: unknown format " + name)
}

// Closest returns the candidate within edit distance 2 of key, preferring the
// nearest and then the alphabetically first.
func Closest(key string, candidates []string) (string, bool) {
	best, bestDist := "", 3
	for _, c := range candidates {
		d := editDistance(key, c)
		if d < bestDist || (d == bestDist && c < best) {
			best, bestDist = c, d
		}
	}
	return best, bestDist <= 2
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}
