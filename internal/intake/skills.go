package intake

import (
	"applysmith/internal/api/wire"
	"applysmith/internal/workspace"
)

// signalSeen is a technology a signal document names.
type signalSeen struct {
	term string
	kind string
	src  wire.IntakeSource
}

type skillClaim struct {
	name string
	srcs []wire.IntakeSource
}

// skillSet compares skills the documents list with the approved ones.
type skillSet struct {
	approved map[string]bool
	order    []string
	claims   map[string]*skillClaim
}

func newSkillSet(p workspace.Profile) *skillSet {
	s := &skillSet{approved: map[string]bool{}, claims: map[string]*skillClaim{}}
	for _, k := range p.Skills {
		s.approved[termKey(k.Name)] = true
	}
	return s
}

func (s *skillSet) claim(name string, src wire.IntakeSource) {
	k := termKey(name)
	if k == "" {
		return
	}
	c := s.claims[k]
	if c == nil {
		c = &skillClaim{name: name}
		s.claims[k] = c
		s.order = append(s.order, k)
	}
	c.srcs = addSource(c.srcs, src)
}

// items lists the skills a document states: a match when the profile has the
// skill, an addition when it does not.
func (s *skillSet) items() []wire.IntakeItem {
	var out []wire.IntakeItem
	for _, k := range s.order {
		c := s.claims[k]
		typ := wire.IntakeAddition
		if s.approved[k] {
			typ = wire.IntakeMatch
		}
		out = append(out, wire.IntakeItem{ID: hashID(typ, kindSkill, k), Type: typ, Kind: kindSkill, Summary: c.name,
			Entry: map[string]string{"name": c.name}, Sources: c.srcs, Fields: []wire.IntakeField{}, NeedsMonth: []string{}})
	}
	return out
}

// suggestions lists the technologies signal documents name that the profile
// does not approve and no fact document states. Each is a question to the
// person; accepting it takes an explicit confirmation.
func (s *skillSet) suggestions(seen []signalSeen) []wire.IntakeItem {
	type acc struct {
		name string
		kind string
		srcs []wire.IntakeSource
	}
	byKey := map[string]*acc{}
	var order []string
	for _, h := range seen {
		k := termKey(h.term)
		if s.approved[k] || s.claims[k] != nil {
			continue
		}
		a := byKey[k]
		if a == nil {
			a = &acc{name: h.term, kind: h.kind}
			byKey[k] = a
			order = append(order, k)
		}
		a.srcs = addSource(a.srcs, h.src)
	}
	var out []wire.IntakeItem
	for _, k := range order {
		a := byKey[k]
		q := "A " + signalKinds[a.kind] + " mentions " + a.name + ". Do you have working experience with it? That is not shown by the document, so it stays out of your profile unless you confirm it."
		out = append(out, wire.IntakeItem{ID: hashID(wire.IntakeSignal, kindSkill, k), Type: wire.IntakeSignal, Kind: kindSkill, Summary: a.name,
			Entry: map[string]string{"name": a.name}, Sources: a.srcs, Fields: []wire.IntakeField{}, NeedsMonth: []string{}, Question: q})
	}
	return out
}
