package intake

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"applysmith/internal/api/wire"
	"applysmith/internal/cvparse"
	"applysmith/internal/identity"
	"applysmith/internal/workspace"
)

// Entry kinds.
const (
	kindEmployment = "employment"
	kindEducation  = "education"
	kindSkill      = "skill"
)

// A history entry has four fields. The indexes keep the pairing and conflict
// code the same for jobs and studies.
const (
	fOrg = iota
	fRole
	fStart
	fEnd
	nFields
)

func fieldNames(kind string) [nFields]string {
	if kind == kindEducation {
		return [nFields]string{"school", "degree", "start", "end"}
	}
	return [nFields]string{"employer", "title", "start", "end"}
}

// ChangeSet is the reviewable result: every item, and what each would do.
type ChangeSet struct {
	Items []wire.IntakeItem
	plans map[string]plan
}

type plan struct {
	kind    string
	profile int // index in the profile's list, or -1 for a new entry
	entry   map[string]string
}

// rec is one entry as a document states it.
type rec struct {
	v   [nFields]string
	src wire.IntakeSource
}

// group collects the statements about one position or study: the profile's
// entry when there is one, and the documents' claims per field.
type group struct {
	kind    string
	profile int
	pv      [nFields]string
	rep     [nFields]string
	claims  [nFields][]wire.IntakeOption
	sources []wire.IntakeSource
}

// nameKey compares organisations and titles: letters and digits, folded.
func nameKey(s string) string {
	return strings.Join(strings.FieldsFunc(identity.Fold(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " ")
}

func isDate(f int) bool { return f == fStart || f == fEnd }

// dateCompat reads a year with no month as agreeing with any month of it.
func dateCompat(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	return a == b || (len(a) == 4 && strings.HasPrefix(b, a)) || (len(b) == 4 && strings.HasPrefix(a, b))
}

func compat(f int, a, b string) bool {
	if isDate(f) {
		return dateCompat(a, b)
	}
	return nameKey(a) == nameKey(b)
}

// precise keeps the date that carries the month.
func precise(a, b string) string {
	if len(b) > len(a) {
		return b
	}
	return a
}

func (g *group) claim(f int, v string, src wire.IntakeSource) {
	if v == "" {
		return
	}
	for i := range g.claims[f] {
		c := &g.claims[f][i]
		if compat(f, c.Value, v) {
			if isDate(f) {
				c.Value = precise(c.Value, v)
			}
			c.Sources = addSource(c.Sources, src)
			return
		}
	}
	g.claims[f] = append(g.claims[f], wire.IntakeOption{Value: v, Sources: []wire.IntakeSource{src}})
}

func addSource(list []wire.IntakeSource, s wire.IntakeSource) []wire.IntakeSource {
	for _, x := range list {
		if x.Hash == s.Hash && x.Locator == s.Locator {
			return list
		}
	}
	return append(list, s)
}

// score says how well a statement belongs to a group; zero means not at all.
// The same organisation and role is the surest tie; the same organisation with
// the anchor date (start of a job, graduation of a study) or no role to
// contradict it comes next; a different organisation with the same role and
// both dates is a renamed institution or employer.
func score(kind string, rep [nFields]string, r rec) int {
	sameOrg := rep[fOrg] != "" && nameKey(rep[fOrg]) == nameKey(r.v[fOrg])
	roleKnown := rep[fRole] != "" && r.v[fRole] != ""
	sameRole := roleKnown && nameKey(rep[fRole]) == nameKey(r.v[fRole])
	anchor := fStart
	if kind == kindEducation {
		anchor = fEnd
	}
	both := func(f int) bool { return rep[f] != "" && r.v[f] != "" }
	anchorEq := both(anchor) && dateCompat(rep[anchor], r.v[anchor])
	switch {
	case sameOrg && sameRole:
		return 4
	case sameOrg && anchorEq:
		return 3
	case sameOrg && !roleKnown && (!both(anchor) || anchorEq):
		return 2
	case !sameOrg && sameRole && both(fStart) && both(fEnd) && dateCompat(rep[fStart], r.v[fStart]) && dateCompat(rep[fEnd], r.v[fEnd]):
		return 1
	}
	return 0
}

func (g *group) add(r rec) {
	for f := range nFields {
		g.claim(f, r.v[f], r.src)
	}
	g.sources = addSource(g.sources, r.src)
}

// pick returns the best group for r, or a new one.
func pick(groups *[]*group, kind string, r rec) *group {
	var best *group
	top := 0
	for _, g := range *groups {
		if g.kind != kind {
			continue
		}
		if s := score(kind, g.rep, r); s > top {
			best, top = g, s
		}
	}
	if best == nil {
		best = &group{kind: kind, profile: -1, rep: r.v}
		*groups = append(*groups, best)
	}
	return best
}

func hashID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(sum[:])[:12]
}

func span(start, end string) string {
	switch {
	case start != "" && end != "":
		return start + " to " + end
	default:
		return start + end
	}
}

func summary(kind string, e map[string]string) string {
	if kind == kindSkill {
		return e["name"]
	}
	n := fieldNames(kind)
	parts := []string{}
	for _, f := range []string{n[fRole], n[fOrg]} {
		if e[f] != "" {
			parts = append(parts, e[f])
		}
	}
	out := strings.Join(parts, ", ")
	if d := span(e["start"], e["end"]); d != "" {
		out += " (" + d + ")"
	}
	return out
}

func bareYear(v string) bool { return len(v) == 4 }

// Build compares the documents with the profile. A statement the profile
// already holds is a match, a statement it lacks is an addition, and a
// statement that disagrees with the profile or with another document about a
// date, title, institution or employer is a conflict. File times play no part.
func Build(docs []Doc, p workspace.Profile) ChangeSet {
	var groups []*group
	for i, e := range p.History.Employment {
		g := &group{kind: kindEmployment, profile: i, pv: [nFields]string{e.Employer, e.Title, e.Start, e.End}}
		g.rep = g.pv
		groups = append(groups, g)
	}
	for i, e := range p.History.Education {
		g := &group{kind: kindEducation, profile: i, pv: [nFields]string{e.School, e.Degree, e.Start, e.End}}
		g.rep = g.pv
		groups = append(groups, g)
	}
	skills := newSkillSet(p)
	var signals []signalSeen

	for _, d := range docs {
		if d.Duplicate {
			continue
		}
		src := func(loc, origin string) wire.IntakeSource {
			return wire.IntakeSource{Label: d.label(), Hash: d.Hash, Locator: loc, Origin: origin}
		}
		if IsSignal(d.Kind) {
			for _, h := range signalTerms(d.Text) {
				signals = append(signals, signalSeen{term: h.Term, kind: d.Kind, src: src(locator(h.Line, h.Section), wire.IntakeInferred)})
			}
			continue
		}
		draft, loc := cvparse.ParseLocated(d.Text)
		for i, j := range draft.Employment {
			r := rec{v: [nFields]string{j.Employer, j.Title, j.Start, j.End}, src: src(locator(loc.Employment[i].Line, loc.Employment[i].Section), wire.IntakeDirect)}
			pick(&groups, kindEmployment, r).add(r)
		}
		for i, s := range draft.Education {
			// The degree as the document wrote it, its field included: intake
			// compares it with the profile's degree.
			degree := strings.TrimSpace(s.Degree + " " + s.Field)
			r := rec{v: [nFields]string{s.School, degree, s.Start, s.End}, src: src(locator(loc.Education[i].Line, loc.Education[i].Section), wire.IntakeDirect)}
			pick(&groups, kindEducation, r).add(r)
		}
		for i, name := range draft.Skills {
			skills.claim(name, src(locator(loc.Skills[i].Line, loc.Skills[i].Section), wire.IntakeDirect))
		}
	}

	cs := ChangeSet{plans: map[string]plan{}}
	var adds, conflicts, matches []wire.IntakeItem
	for _, g := range groups {
		if len(g.sources) == 0 {
			continue
		}
		it, pl := g.item()
		cs.plans[it.ID] = pl
		switch it.Type {
		case wire.IntakeAddition:
			adds = append(adds, it)
		case wire.IntakeConflict:
			conflicts = append(conflicts, it)
		default:
			matches = append(matches, it)
		}
	}
	for _, it := range skills.items() {
		cs.plans[it.ID] = plan{kind: kindSkill, profile: -1, entry: it.Entry}
		if it.Type == wire.IntakeAddition {
			adds = append(adds, it)
		} else {
			matches = append(matches, it)
		}
	}
	var sugg []wire.IntakeItem
	for _, it := range skills.suggestions(signals) {
		cs.plans[it.ID] = plan{kind: kindSkill, profile: -1, entry: it.Entry}
		sugg = append(sugg, it)
	}
	cs.Items = slices.Concat(adds, conflicts, matches, sugg)
	return cs
}

func locator(line int, section string) string {
	if section == "" {
		return "line " + strconv.Itoa(line)
	}
	return fmt.Sprintf("line %d, under %q", line, section)
}

// item turns a group into its change set line and the plan to apply it.
func (g *group) item() (wire.IntakeItem, plan) {
	names := fieldNames(g.kind)
	entry := map[string]string{}
	var conflicts []wire.IntakeField
	var needsMonth []string
	idParts := []string{g.kind, strconv.Itoa(g.profile)}
	for f := range nFields {
		// The values this field takes across the profile and the documents,
		// merged where they agree.
		var classes []string
		if g.pv[f] != "" {
			classes = append(classes, g.pv[f])
		}
		for _, c := range g.claims[f] {
			merged := false
			for i := range classes {
				if compat(f, classes[i], c.Value) {
					if isDate(f) && g.pv[f] == "" {
						classes[i] = precise(classes[i], c.Value)
					}
					merged = true
					break
				}
			}
			if !merged {
				classes = append(classes, c.Value)
			}
		}
		idParts = append(idParts, names[f])
		if len(classes) >= 2 {
			conflicts = append(conflicts, wire.IntakeField{Field: names[f], Profile: g.pv[f], Options: g.claims[f]})
			for _, c := range g.claims[f] {
				idParts = append(idParts, c.Value)
			}
			if g.pv[f] != "" {
				entry[names[f]] = g.pv[f]
			}
			continue
		}
		if len(classes) == 1 {
			// A profile entry is shown as the profile holds it; a blank there is
			// not filled from a document.
			val := classes[0]
			if g.profile >= 0 {
				val = g.pv[f]
			}
			if val != "" {
				entry[names[f]] = val
			}
			idParts = append(idParts, classes[0])
			if isDate(f) && bareYear(classes[0]) && g.profile < 0 {
				needsMonth = append(needsMonth, names[f])
			}
		}
	}
	it := wire.IntakeItem{Kind: g.kind, Entry: entry, Sources: g.sources, Fields: []wire.IntakeField{}, NeedsMonth: []string{}}
	switch {
	case len(conflicts) > 0:
		it.Type, it.Fields = wire.IntakeConflict, conflicts
		for _, c := range conflicts {
			if isDateName(c.Field) {
				for _, o := range c.Options {
					if bareYear(o.Value) {
						it.NeedsMonth = append(it.NeedsMonth, c.Field)
						break
					}
				}
			}
		}
		it.NeedsMonth = append(it.NeedsMonth, needsMonth...)
	case g.profile >= 0:
		it.Type = wire.IntakeMatch
	default:
		it.Type, it.NeedsMonth = wire.IntakeAddition, needsMonth
	}
	it.ID = hashID(append([]string{it.Type}, idParts...)...)
	it.Summary = summary(g.kind, entry)
	if it.Summary == "" {
		it.Summary = "an entry the documents disagree about"
	}
	return it, plan{kind: g.kind, profile: g.profile, entry: entry}
}

func isDateName(f string) bool { return f == "start" || f == "end" }
