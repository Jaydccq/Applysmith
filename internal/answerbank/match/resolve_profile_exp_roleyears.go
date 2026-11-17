package match

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Years in a role the recorded titles name ("How many years of experience do
// you have as a software engineer?") are the years of those jobs, as sure as
// the career total: the months they span together, overlaps counted once, an
// ongoing job through today. When every recorded job is one, the answer is the
// career total the user recorded. A role no title names is the skill lane's.

// roleLevelWords are seniority and level words a title carries beside its role.
var roleLevelWords = setOf("senior", "sr", "staff", "lead", "principal", "junior", "jr", "intern", "internship",
	"i", "ii", "iii", "iv", "v", "1", "2", "3", "4", "mid", "level", "entry", "a", "an", "the")

// roleWordForms fold the role nouns whose forms the mention stemmer keeps
// apart: an analyst works in analytics, a scientist in a science.
var roleWordForms = map[string]string{"analyst": "analy", "analytic": "analy", "scientist": "scienc"}

// roleWords are the words of a role or title that name the role, each up to
// the mention stemmer and roleWordForms.
func roleWords(text string) []string {
	var out []string
	for _, w := range tokenize(normalizeLabel(text)) {
		if roleLevelWords[w] {
			continue
		}
		st := mentionStem(w)
		if f, ok := roleWordForms[st]; ok {
			st = f
		}
		out = append(out, st)
	}
	return out
}

// roleTitleYears answers a years question about a role from the jobs whose
// titles hold every word of it. It reports false when no title does, when a
// matching job's dates cannot be read, on a Yes/No and without today's date.
func roleTitleYears(s *Step, role string) (Draft, bool) {
	jobs, today := s.Req.Facts.Employment, s.Req.Today
	asked := roleWords(role)
	if len(asked) == 0 || len(jobs) == 0 || today.Year == 0 || isYesNoOptionSet(s.Q.Options) || s.Q.Kind != KindText && len(s.Q.Options) == 0 {
		return Draft{}, false
	}
	months := map[int]bool{}
	matched := 0
	for _, e := range jobs {
		title := roleWords(e.Title)
		if slices.ContainsFunc(asked, func(w string) bool { return !slices.Contains(title, w) }) {
			continue
		}
		matched++
		start, ok := parseYearMonth(e.Start)
		if !ok {
			return Draft{}, false
		}
		end := monthOf(today).key()
		if !e.OngoingEnd() {
			ym, ok := parseYearMonth(e.End)
			if !ok {
				return Draft{}, false
			}
			end = min(ym.key(), end)
		}
		for k := start.key(); k <= end; k++ {
			months[k] = true
		}
	}
	if matched == 0 {
		return Draft{}, false
	}
	if matched == len(jobs) {
		for i := range s.Entries {
			if e := &s.Entries[i]; e.ID == IDYearsFullTimeWork && ownText(e) != "" {
				return roleYearsDraft(s, ownText(e), -1, fmt.Sprintf("role years: every recorded job is a %q job -> the career total %q", role, ownText(e)))
			}
		}
	}
	years := float64(len(months)) / 12
	return roleYearsDraft(s, "", years, fmt.Sprintf("role years: %d of %d recorded jobs are %q jobs, %d months together", matched, len(jobs), role, len(months)))
}

// roleYearsDraft writes the years into the control: a stored total as it is,
// or computed years whole (months when months are asked); on a menu, the
// bucket that holds them. A span under a whole year is the least that is not
// none: 1 in a box, the lowest option that is not "None" in a list.
func roleYearsDraft(s *Step, total string, years float64, trace string) (Draft, bool) {
	unit := experienceUnit(s.Q.Text)
	if len(s.Q.Options) > 0 {
		var opt Option
		var ok bool
		if total != "" {
			opt, ok = yearsEntryBucket(total, s.Q.Options)
		} else {
			opt, ok = pickYearsBucket(s.Q.Options, years, unit)
			if years > 0 && (!ok || noneYearsRE.MatchString(opt.Label)) {
				if low := lowestYearsOption(s.Q.Options, unit); low != nil {
					opt, ok = *low, true
				}
			}
		}
		if !ok {
			return Draft{}, false
		}
		d := derivedDraft(opt.Label, "", startPattern, trace+" -> "+strconv.Quote(opt.Label))
		d.ChosenLabel, d.HasOption, d.OptionScore = opt.Label, true, 1
		return d, true
	}
	answer := strings.TrimSpace(total)
	if n, ok := yearsEntryNumber(total); ok && unit == "months" {
		answer = strconv.Itoa(n * 12)
	}
	if answer == "" {
		amount := years
		if unit == "months" {
			amount = years * 12
		}
		whole := int(amount + 1e-9)
		if whole == 0 && years > 0 {
			whole = 1
		}
		answer = strconv.Itoa(whole)
	}
	return derivedDraft(answer, "", startPattern, trace), true
}
