package intake

import (
	"encoding/json"
	"fmt"
	"strings"

	"applysmith/internal/api/wire"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/workspace"
)

// Keep is the choice that leaves the profile's own value in place.
const Keep = "keep"

var monthRE = lazyregexp.New(`^\d{4}-(0[1-9]|1[0-2])$`)

// Applied is what the selected items come to: the profile edits, and the items
// they came from.
type Applied struct {
	Ops   []workspace.Op
	Items []wire.IntakeItem
}

// Select checks the person's selections against the change set and builds the
// profile edits. It refuses a selection of an item that is not in the set, a
// match (nothing to apply), a signal that was not confirmed, a conflict whose
// sides were not all picked, and a bare-year date given no month. Items that
// are not selected change nothing, conflicts included.
func (cs ChangeSet) Select(sels []wire.IntakeSelection) (Applied, error) {
	byID := map[string]wire.IntakeItem{}
	for _, it := range cs.Items {
		byID[it.ID] = it
	}
	rej := Rejection{}
	var out Applied
	seen := map[string]bool{}
	for i, sel := range sels {
		at := func(f string) string { return fmt.Sprintf("selections[%d].%s", i, f) }
		it, ok := byID[sel.ID]
		switch {
		case !ok:
			rej.add(at("id"), "is not in this change set; preview the documents again")
			continue
		case seen[sel.ID]:
			rej.add(at("id"), "is selected twice")
			continue
		}
		seen[sel.ID] = true
		pl := cs.plans[sel.ID]
		before := len(rej)
		var ops []workspace.Op
		switch it.Type {
		case wire.IntakeMatch:
			rej.add(at("id"), "is already in your profile; there is nothing to apply")
		case wire.IntakeSignal:
			if !sel.Confirm {
				rej.add(at("confirm"), "a suggestion becomes a skill only when you confirm that the experience is yours")
			} else {
				ops = append(ops, appendOp(pl.kind, []string{"name"}, pl.entry))
			}
		case wire.IntakeAddition:
			e := withMonths(pl.entry, sel.Months, rej, at)
			if len(rej) == before {
				ops = append(ops, appendOp(pl.kind, entryKeys(pl.kind), e))
			}
		case wire.IntakeConflict:
			ops = conflictOps(it, pl, sel, rej, at)
		}
		if len(rej) == before {
			out.Ops = append(out.Ops, ops...)
			out.Items = append(out.Items, it)
		}
	}
	if len(rej) > 0 {
		return Applied{}, rej
	}
	return out, nil
}

func entryKeys(kind string) []string {
	if kind == kindSkill {
		return []string{"name"}
	}
	n := fieldNames(kind)
	return n[:]
}

func listPath(kind string) string {
	switch kind {
	case kindEducation:
		return "history.education"
	case kindSkill:
		return "skills"
	}
	return "history.employment"
}

// appendOp adds one entry at the end of its list, fields in a fixed order.
func appendOp(kind string, keys []string, e map[string]string) workspace.Op {
	var b strings.Builder
	b.WriteByte('{')
	first := true
	for _, k := range keys {
		if e[k] == "" {
			continue
		}
		if !first {
			b.WriteString(", ")
		}
		first = false
		q, _ := json.Marshal(e[k])
		fmt.Fprintf(&b, "%q: %s", k, q)
	}
	b.WriteByte('}')
	return workspace.Op{Kind: workspace.OpAppend, Path: listPath(kind), Value: b.String()}
}

// resolveMonth turns a date into one the profile can hold. A bare year takes
// the month the person typed, which must belong to that year.
func resolveMonth(field, value string, months map[string]string, rej Rejection, at func(string) string) string {
	if !bareYear(value) {
		return value
	}
	m := months[field]
	if !monthRE.MatchString(m) || !strings.HasPrefix(m, value+"-") {
		rej.add(at("months."+field), fmt.Sprintf("the document gives only the year %s; type the month as %s-MM", value, value))
		return value
	}
	return m
}

func withMonths(entry, months map[string]string, rej Rejection, at func(string) string) map[string]string {
	e := map[string]string{}
	for k, v := range entry {
		e[k] = v
		if isDateName(k) {
			e[k] = resolveMonth(k, v, months, rej, at)
		}
	}
	return e
}

func conflictOps(it wire.IntakeItem, pl plan, sel wire.IntakeSelection, rej Rejection, at func(string) string) []workspace.Op {
	known := map[string]bool{}
	for _, f := range it.Fields {
		known[f.Field] = true
	}
	for k := range sel.Choices {
		if !known[k] {
			rej.add(at("choices."+k), "is not a field of this conflict")
		}
	}
	e := withMonths(pl.entry, sel.Months, rej, at)
	var ops []workspace.Op
	for _, f := range it.Fields {
		c, ok := sel.Choices[f.Field]
		if !ok || c == "" {
			rej.add(at("choices."+f.Field), "pick a side: the value in your profile or one a document gives")
			continue
		}
		if c == Keep {
			if f.Profile == "" {
				rej.add(at("choices."+f.Field), "your profile has no value to keep; pick one a document gives")
			} else {
				e[f.Field] = f.Profile
			}
			continue
		}
		listed := false
		for _, o := range f.Options {
			listed = listed || o.Value == c
		}
		if !listed {
			rej.add(at("choices."+f.Field), fmt.Sprintf("%q is not one of the values the documents give", c))
			continue
		}
		v := c
		if isDateName(f.Field) {
			v = resolveMonth(f.Field, c, sel.Months, rej, at)
		}
		e[f.Field] = v
		if pl.profile >= 0 && v != f.Profile {
			q, _ := json.Marshal(v)
			ops = append(ops, workspace.Op{Kind: workspace.OpSet, Path: fmt.Sprintf("%s[%d].%s", listPath(pl.kind), pl.profile, f.Field), Value: string(q)})
		}
	}
	if pl.profile < 0 {
		return []workspace.Op{appendOp(pl.kind, entryKeys(pl.kind), e)}
	}
	return ops
}
