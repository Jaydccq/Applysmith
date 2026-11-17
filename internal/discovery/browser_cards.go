package discovery

import (
	"net/url"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
)

var (
	agePrefixRE = lazyregexp.New(`(?i)^(?:posted|active|employer active|reposted|updated)\s+`)
	plusRE      = lazyregexp.New(`(\d)\+`)
)

// boardAge reads the age a card prints: "Just posted", "Posted 2 days ago",
// "Active 3 days ago", "30+ days ago", "24h", "3d", "30d+", "Yesterday".
func boardAge(s string, now time.Time) (time.Duration, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = agePrefixRE.ReplaceAllString(s, "")
	s = plusRE.ReplaceAllString(s, "$1")
	if s == "just posted" || s == "posted today" {
		return 0, true
	}
	return AgeOf(s, now)
}

// cardTime is the posted time of a card: its machine-readable time when it
// has one, else its printed age against now. A card with neither is undated.
func cardTime(c rawCard, now time.Time) time.Time {
	if t := parseTime(c.Datetime); !t.IsZero() {
		return t
	}
	if d, ok := boardAge(c.Age, now); ok {
		return now.Add(-d)
	}
	return time.Time{}
}

// pickMenu is the smallest value of a board's own menu (in days) that covers
// the window, and false when the window is wider than the whole menu.
func pickMenu(menu []int, w time.Duration) (int, bool) {
	for _, d := range menu {
		if time.Duration(d)*day >= w {
			return d, true
		}
	}
	return 0, false
}

// sameParam compares one query parameter of two URLs, ignoring case and
// spacing.
func sameParam(a, b url.Values, k string) bool {
	norm := func(v string) string {
		return strings.ToLower(strings.Join(strings.Fields(strings.ReplaceAll(v, "+", " ")), " "))
	}
	return norm(a.Get(k)) == norm(b.Get(k))
}

var scopeText = map[string]string{
	"us": "United States", "canada": "Canada", "uk": "United Kingdom", "europe": "Europe", "asia-pacific": "Asia Pacific",
}

// boardQueries are the saved searches of a list source. LinkedIn has a
// fallback: one search per target role of the profile in the location scope.
func (r *run) boardQueries(source string) ([]Query, error) {
	qs, err := r.queries()
	if err != nil {
		return nil, err
	}
	if len(qs) > 0 || source != "linkedin" {
		if len(qs) == 0 {
			return nil, skipRun("no saved searches for " + source + " in portals.yml")
		}
		return qs, nil
	}
	loc := scopeText[r.scope]
	for i, role := range r.req.Profile.Targets.Roles {
		if i >= 5 {
			break
		}
		if t := strings.TrimSpace(role.Title); t != "" {
			qs = append(qs, Query{Search: t, P: map[string]string{"location": loc}})
		}
	}
	if len(qs) == 0 {
		return nil, skipRun("no saved searches for linkedin in portals.yml and no target roles in the profile")
	}
	return qs, nil
}
