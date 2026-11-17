package answerbank

import (
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/lazyregexp"
)

// withheld replaces a value that is not a short vocabulary word, so an error
// or warning never carries what the user wrote into the file.
const withheld = "(value withheld)"

var vocabularyWord = lazyregexp.New(`^[A-Za-z0-9_.@:/-]{1,32}$`)

// quoted renders a value the user typed where a vocabulary word belongs (a
// kind, a category, a state). A short identifier without spaces is echoed so
// the user can find it; anything else, which could be a sentence of their own,
// is withheld.
func quoted(s string) string {
	if vocabularyWord.MatchString(s) {
		return strconv.Quote(s)
	}
	return withheld
}

// word is quoted without the quotes, for places that already read as a name.
func word(s string) string {
	if vocabularyWord.MatchString(s) {
		return s
	}
	return withheld
}

// repairEntry sanitizes the optional fields of an entry the way the load does
// and reports every change it makes through warn. The load keeps the repaired
// entry and shows the warnings; a save refuses an entry that needs any, so the
// file never holds what a load would change. The messages carry no entry id.
func repairEntry(e *match.Entry, warn func(format string, args ...any)) {
	e.InputTypes = repairInputTypes(e.InputTypes, warn)
	if c := e.Confidence; c != nil && !(*c >= 0 && *c <= 1) {
		warn("confidence is outside 0..1 or not a number and was ignored")
		e.Confidence = nil
	}
	if f := e.ConfidenceFloor; f != nil && math.IsNaN(*f) {
		warn("confidence_floor is not a number and was ignored")
		e.ConfidenceFloor = nil
	}
	if e.State != "" && !slices.Contains(match.States, e.State) {
		warn("unknown state %s; loaded as active", quoted(e.State))
		e.State = match.StateActive
	}
	if e.CreatedBy != "" && !slices.Contains(match.Creators, e.CreatedBy) {
		warn("unknown created_by %s; loaded as user", quoted(e.CreatedBy))
		e.CreatedBy = match.CreatedByUser
	}
	if e.SkillMode != "" && !slices.Contains(match.SkillModes, e.SkillMode) {
		warn("unknown skill_mode %s ignored", quoted(e.SkillMode))
		e.SkillMode = ""
	}
	if t := e.NumericThreshold; t != nil {
		switch {
		case t.Extractor != match.ExtractorDaysPerWeekOnsite:
			warn("numeric_threshold ignored: unknown extractor %s", quoted(t.Extractor))
			e.NumericThreshold = nil
		case t.OnParseMiss != "" && !slices.Contains(match.ParseMisses, t.OnParseMiss):
			warn("numeric_threshold.on_parse_miss %s is unknown; the default no-op applies", quoted(t.OnParseMiss))
			t.OnParseMiss = ""
		}
	}
	if lp := e.LocationPolicy; lp != nil && !slices.Contains(match.Predicates, lp.Predicate) {
		warn("location_policy ignored: unknown predicate %s", quoted(lp.Predicate))
		e.LocationPolicy = nil
	}
	for i, s := range e.CapturedScope {
		if !s.Valid() {
			warn("captured_scope item %d needs a host and a question; the entry answers on no page until it is fixed", i+1)
		}
	}
	if opts := cleanOptions(e.AllowedOptions); !slices.Equal(opts, e.AllowedOptions) {
		warn("allowed_options held blank, repeated or oddly spaced options and were cleaned")
		e.AllowedOptions = opts
	}
	for _, f := range []struct {
		name  string
		terms *[]string
	}{{"skill_terms", &e.SkillTerms}, {"skill_blacklist_terms", &e.SkillBlacklistTerms}} {
		if terms := splitTerms(*f.terms); !slices.Equal(terms, *f.terms) {
			warn("%s held comma-joined, blank or repeated terms and were cleaned", f.name)
			*f.terms = terms
		}
	}
	if id := strings.TrimSpace(e.LinkedExplainID); id != e.LinkedExplainID {
		warn("linked_explain_id had surrounding whitespace and was trimmed")
		e.LinkedExplainID = id
	}
	e.NegationPatterns = nil
	for i, pattern := range e.QuestionNegationTerms {
		re, err := regexp.Compile("(?i)" + pattern)
		if err != nil {
			warn("question_negation_terms item %d is not a valid RE2 pattern and was dropped", i+1)
			continue
		}
		e.NegationPatterns = append(e.NegationPatterns, re)
	}
	if e.StrayAskMe() {
		warn("%s sits inside a list of several values, where it is an ordinary label; it is the marker only as the sole value", match.AskMe)
	}
	for _, key := range e.Body.Stray(e.Kind) {
		warn("field %s is not used by kind %s and was ignored", key, e.Kind)
	}
	e.Body = e.Body.KeepOnly(e.Kind)
}

func repairInputTypes(types []string, warn func(string, ...any)) []string {
	var kept []string
	for _, t := range types {
		if match.ValidInputType(t) {
			kept = append(kept, t)
			continue
		}
		warn("unknown input type %s dropped", quoted(t))
	}
	if types != nil && kept == nil {
		return nil
	}
	return kept
}

// cleanOptions collapses whitespace, drops empties and de-duplicates
// case-insensitively, keeping the first spelling.
func cleanOptions(options []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, o := range options {
		o = strings.Join(strings.Fields(o), " ")
		if key := strings.ToLower(o); o != "" && !seen[key] {
			seen[key] = true
			out = append(out, o)
		}
	}
	return out
}

// splitTerms splits comma-joined items and de-duplicates case-insensitively.
func splitTerms(items []string) []string {
	var flat []string
	for _, it := range items {
		flat = append(flat, strings.Split(it, ",")...)
	}
	return cleanOptions(flat)
}

// scrub removes quoted values from a YAML library message, which can repeat
// the text of the offending scalar.
var quotedValue = lazyregexp.New("`[^`]*`|\"[^\"]*\"")

func scrub(msg string) string {
	return quotedValue.ReplaceAllString(msg, withheld)
}
