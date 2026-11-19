package discovery

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"applysmith/internal/gates"
	"applysmith/internal/workspace"
)

// Sort orders.
const (
	SortScore   = "score"
	SortRecency = "recency"
)

var (
	seniorTerms = compileTerms([]string{"senior", "sr", "staff", "principal", "lead", "manager", "director", "head of", "vp", "architect"}, false)
	maxTitleHit = 6
	// roleNoise are the words of a target role title that name no family.
	roleNoise = map[string]bool{"senior": true, "sr": true, "junior": true, "jr": true, "staff": true, "principal": true,
		"lead": true, "entry": true, "level": true, "mid": true, "i": true, "ii": true, "iii": true, "and": true, "of": true, "the": true, "for": true}
)

// Target role weights of the ranking.
const (
	targetRoleWeight = 4
	seniorityWeight  = 4
	earlyWeight      = 3
)

// RankTargets is the seeker's target roles as the ranker reads them: which
// seniority wording to reward or penalize, and the role families.
type RankTargets struct {
	// anyEarly: some target is intern or junior. allEarly: every target with a
	// seniority is. allSenior: every target with a seniority is senior or above.
	anyEarly, allEarly, allSenior bool
	families                      [][][]term
	titles                        []string
}

// NewRankTargets reads profile.yml's target roles. No targets give no
// seniority adjustment and no role-family bonus.
func NewRankTargets(roles []workspace.Role) RankTargets {
	var t RankTargets
	early, senior, rated := 0, 0, 0
	for _, r := range roles {
		switch r.Seniority {
		case "intern", "junior":
			early++
			rated++
		case "mid":
			rated++
		case "senior", "lead", "staff", "principal", "manager", "director", "vp", "executive":
			senior++
			rated++
		}
		var fam [][]term
		for _, w := range strings.FieldsFunc(strings.ToLower(r.Title), func(c rune) bool { return !unicode.IsLetter(c) && !unicode.IsDigit(c) }) {
			if roleNoise[w] {
				continue
			}
			// A whole word or its plural: "Platforms" is the platform family,
			// "Engineering Manager" is not the engineer family.
			if tm, ok := newTerm(w, false); ok {
				plural, _ := newTerm(w+"s", false)
				fam = append(fam, []term{tm, plural})
			}
		}
		if len(fam) > 0 {
			t.families, t.titles = append(t.families, fam), append(t.titles, strings.TrimSpace(r.Title))
		}
	}
	t.anyEarly = early > 0
	t.allEarly = rated > 0 && early == rated
	t.allSenior = rated > 0 && senior == rated
	return t
}

// targetRole returns the first target role whose every family word is in the
// title, in any order.
func (t RankTargets) targetRole(title string) (string, bool) {
	for i, fam := range t.families {
		all := true
		for _, forms := range fam {
			if _, ok := anyMatch(forms, title); !ok {
				all = false
				break
			}
		}
		if all {
			return t.titles[i], true
		}
	}
	return "", false
}

// RankInput is what ranking needs beyond the posting.
type RankInput struct {
	Title   TitleResult
	Targets RankTargets
	Scope   string
	InScope bool // location classification equals the scope
	Now     time.Time
}

// Rank scores a posting with no model call: title hits (+2 each, at most +6),
// a target-role match (+4), the seniority adjustment, +3 for a location equal
// to the scope, recency and novelty. The reasons list each term.
func Rank(p Posting, in RankInput) (int, []string) {
	var score int
	var reasons []string
	add := func(n int, why string) {
		score += n
		reasons = append(reasons, fmt.Sprintf("%s:%+d", why, n))
	}
	if n := min(2*len(in.Title.Hits), maxTitleHit); n > 0 {
		add(n, "title("+strings.Join(in.Title.Hits, ",")+")")
	}
	for _, t := range targetTerms(p.Title, in.Title.Boosted, in.Targets) {
		add(t.n, t.why)
	}
	if in.Title.Boosted && in.Title.Boost != 0 {
		add(in.Title.Boost, "seniority("+strings.Join(in.Title.Terms, ",")+")")
	}
	if in.Scope != ScopeWorldwide && in.InScope {
		add(3, "location")
	}
	if !p.PostedAt.IsZero() {
		days := int(in.Now.Sub(p.PostedAt).Hours() / 24)
		switch {
		case days <= 7:
			add(3, "recency")
		case days <= 30:
			add(1, "recency")
		case days > 90:
			add(-2, "recency")
		}
	}
	if p.novelty != 0 {
		add(p.novelty, "novelty")
	}
	return score, reasons
}

type rankTerm struct {
	n   int
	why string
}

// targetTerms are the terms read against the seeker's target roles: the
// target-role match and, where no seniority weights are configured
// (boosted false), the seniority wording.
func targetTerms(title string, boosted bool, t RankTargets) []rankTerm {
	var out []rankTerm
	// Seniority wording counts only against the seeker's own targets: a senior
	// title is penalized for an early-career seeker, an early-career title
	// rewarded for one and penalized for a senior one.
	_, seniorWording := anyMatch(seniorTerms, gates.SeniorityText(title))
	above := !boosted && seniorWording && t.allEarly
	// A title above every target's seniority is not a target role: its family
	// match would cancel the senior wording, and a junior seeker's "Senior
	// Backend Engineer" would rank with the junior ones.
	if role, ok := t.targetRole(title); ok && !above {
		out = append(out, rankTerm{targetRoleWeight, "target-role(" + role + ")"})
	}
	if boosted {
		return out
	}
	if above {
		out = append(out, rankTerm{-seniorityWeight, "senior-wording"})
	}
	if EarlyCareer(title, "") {
		switch {
		case t.anyEarly:
			out = append(out, rankTerm{earlyWeight, "early-career"})
		case t.allSenior:
			out = append(out, rankTerm{-seniorityWeight, "early-career"})
		}
	}
	return out
}

// Less reports whether a ranks before b under the sort order: by score
// descending, or by posted time first (newest first, undated last) with ties
// by score. Equal postings keep their input order under a stable sort.
func Less(order string) func(a, b Posting) bool {
	if order == SortRecency {
		return func(a, b Posting) bool {
			switch {
			case a.PostedAt.IsZero() != b.PostedAt.IsZero():
				return !a.PostedAt.IsZero()
			case !a.PostedAt.Equal(b.PostedAt):
				return a.PostedAt.After(b.PostedAt)
			}
			return a.Score > b.Score
		}
	}
	return func(a, b Posting) bool { return a.Score > b.Score }
}
