package documents

import (
	"sort"
	"strings"

	"applysmith/internal/lazyregexp"
	"applysmith/internal/workspace"
)

// MaxRankDescription is the longest posting text the ranking reads.
const MaxRankDescription = 20_000

// maxReasonTerms keeps a reason short enough to show in a panel.
const maxReasonTerms = 12

// ReasonDefault is the reason of the chosen entry when no entry matched.
const ReasonDefault = "default"

// A word is letters and digits, may continue through dots (node.js) and may
// end in plus signs or a hash (c++, c#), so those names stay whole.
var wordRE = lazyregexp.New(`[\p{L}\p{N}]+(?:\.[\p{L}\p{N}]+)*(?:\+\+|\+|#)?`)

// ignoredWords are function words and the vocabulary every posting shares, so
// that a match means a skill or a role, not the shape of the advertisement.
var ignoredWords = setOf(`a an and are as at be been but by can do for from has have how if in into is it its of on or our
	that the their them they this to us was we were what when where which who will with you your about all also any
	both each more most new other over such than then these those through under up use using well work working
	team teams role roles experience experienced years year ability able strong skills skill knowledge looking join
	responsibilities requirements required preferred qualifications plus including etc job position candidate
	candidates company opportunity needed need must should across within without per day days love great good
	excellent related relevant degree bachelor bachelors master masters equivalent`)

func setOf(words string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(words) {
		out[w] = true
	}
	return out
}

// RankPosting is the text of a posting that the ranking reads. Company is
// accepted with the request but does not score: an employer's name says
// nothing about which variant fits the role.
type RankPosting struct {
	Title       string
	Company     string
	Description string
}

// RankedResume is one roster entry's place in a ranking.
type RankedResume struct {
	ID     string
	Score  int
	Reason string
}

// Ranking lists every roster entry best first and names the chosen one, which
// is empty only for an empty roster.
type Ranking struct {
	Chosen string
	Ranked []RankedResume
}

// RankResumes scores each entry by the number of distinct posting words found
// in its tags, label and description. Ties go to the default entry, then to
// roster order. It uses no model, so the same roster and posting always give
// the same answer. When nothing matches the default entry is chosen.
func RankResumes(roster []workspace.Resume, p RankPosting) Ranking {
	if len(roster) == 0 {
		return Ranking{Ranked: []RankedResume{}}
	}
	terms := postingTerms(p)
	type scored struct {
		RankedResume
		pos int
		def bool
	}
	rows := make([]scored, len(roster))
	for i, r := range roster {
		have := map[string]bool{}
		for _, text := range append([]string{r.Label, r.Description}, r.Tags...) {
			for _, w := range rankWords(text) {
				have[w] = true
			}
		}
		var matched []string
		for _, t := range terms {
			if have[t] {
				matched = append(matched, t)
			}
		}
		reason := strings.Join(matched[:min(len(matched), maxReasonTerms)], ", ")
		rows[i] = scored{RankedResume{ID: r.ID, Score: len(matched), Reason: reason}, i, r.Default}
	}
	sort.SliceStable(rows, func(a, b int) bool {
		if rows[a].Score != rows[b].Score {
			return rows[a].Score > rows[b].Score
		}
		if rows[a].def != rows[b].def {
			return rows[a].def
		}
		return rows[a].pos < rows[b].pos
	})
	out := Ranking{Chosen: rows[0].ID, Ranked: make([]RankedResume, len(rows))}
	if rows[0].Score == 0 {
		rows[0].Reason = ReasonDefault
	}
	for i, r := range rows {
		out.Ranked[i] = r.RankedResume
	}
	return out
}

// RankReport ranks the roster for an evaluated report: the role's title and,
// when the report has one, its posting view (postingFromReport: the title,
// keywords and requirements). The rest of a report describes the seeker as
// well as the job (a final report's evidence, a quick screen's reasons), so it
// never scores: a report with no posting view is ranked on the title alone.
func RankReport(roster []workspace.Resume, title, company, report string) Ranking {
	view := postingFromReport(report)
	if view == report {
		view = ""
	}
	return RankResumes(roster, RankPosting{Title: title, Company: company, Description: view})
}

func rankWords(text string) []string {
	return wordRE.FindAllString(strings.ToLower(text), -1)
}

// postingTerms returns the distinct words of the title and the description in
// order of first appearance, without the ignored ones.
func postingTerms(p RankPosting) []string {
	desc := p.Description
	if r := []rune(desc); len(r) > MaxRankDescription {
		desc = string(r[:MaxRankDescription])
	}
	seen := map[string]bool{}
	var out []string
	for _, w := range rankWords(p.Title + "\n" + desc) {
		if ignoredWords[w] || seen[w] || len(w) < 2 && w != "r" && w != "c" {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}
