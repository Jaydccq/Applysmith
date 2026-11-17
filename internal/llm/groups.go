package llm

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Routing groups, after magpie's route groups: a routing row names a group
// instead of one model, and each call spreads over the group's members by the
// group's mode. The mode only decides the order of the members; the rest of
// the walk (rests, the cap's cheaper head, agent affinity, the row's
// fallbacks) is applied to that order exactly as it is to any candidates.

// GroupProvider is the provider of a routing row that names a group; the row's
// model is the group's name.
const GroupProvider = "group"

// Group modes.
const (
	GroupOrder  = "order"  // the members as listed
	GroupRotate = "rotate" // each call starts at the next member
	GroupUsage  = "usage"  // the member with the fewest ledger attempts today first
)

var groupModes = []string{GroupOrder, GroupRotate, GroupUsage}

const maxGroupMembers = 8

// Group is a named list of models. A member is a configured provider's model:
// not a pi login and not another group.
type Group struct {
	Mode    string     `json:"mode"`
	Members []Fallback `json:"members"`
}

func groupKey(name string) string { return "group/" + name }

// ValidateGroup checks one group against the configured providers.
func (s Settings) ValidateGroup(name string, g Group) error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("groups.%s: %s", name, fmt.Sprintf(format, args...))
	}
	if !providerID.MatchString(name) {
		return fail("the name must be 1 to 40 lowercase letters, digits, hyphen or underscore, starting with a letter or digit")
	}
	if !slices.Contains(groupModes, g.Mode) {
		return fail("mode: %q is not one of %s", g.Mode, strings.Join(groupModes, ", "))
	}
	if n := len(g.Members); n < 1 || n > maxGroupMembers {
		return fail("members: 1 to %d, got %d", maxGroupMembers, n)
	}
	seen := map[Fallback]bool{}
	for i, m := range g.Members {
		if m.Provider == GroupProvider {
			return fail("members[%d]: a group cannot hold a group", i)
		}
		if _, isPi := PiLogin(m.Provider); isPi {
			return fail("members[%d]: %q is a login of the agent runtime; a group holds configured providers only", i, m.Provider)
		}
		if _, ok := s.Provider(m.Provider); !ok {
			return fail("members[%d]: provider %q is not configured", i, m.Provider)
		}
		if err := ValidModelID(m.Model); err != nil {
			return fail("members[%d].model: %v", i, err)
		}
		if seen[m] {
			return fail("members[%d]: %s/%s is listed twice", i, m.Provider, m.Model)
		}
		seen[m] = true
	}
	return nil
}

// WithPatch returns s with the routing rows and groups of a patch applied: a
// nil entry deletes. It does not validate them, but it refuses to delete a
// group that a row still names, and names the rows.
func (s Settings) WithPatch(routes map[string]*Route, groups map[string]*Group) (Settings, error) {
	s.Routes, s.Groups = maps.Clone(s.Routes), maps.Clone(s.Groups)
	if s.Routes == nil {
		s.Routes = map[string]Route{}
	}
	if s.Groups == nil {
		s.Groups = map[string]Group{}
	}
	for id, row := range routes {
		if row == nil {
			delete(s.Routes, id)
		} else {
			s.Routes[id] = *row
		}
	}
	for _, name := range slices.Sorted(maps.Keys(groups)) {
		if g := groups[name]; g != nil {
			s.Groups[name] = *g
			continue
		}
		delete(s.Groups, name)
		var users []string
		for _, id := range slices.Sorted(maps.Keys(s.Routes)) {
			if row := s.Routes[id]; row.Provider == GroupProvider && row.Model == name {
				users = append(users, "routes."+id)
			}
		}
		if len(users) > 0 {
			return s, fmt.Errorf("groups.%s: cannot be deleted while %s name it", name, strings.Join(users, ", "))
		}
	}
	return s, nil
}

// listed is the row's candidates in the order it configures them: the group's
// members as listed (or the one model), then the fallbacks. It is never empty.
func (s Settings) listed(res Resolved) []Fallback {
	if res.Provider == GroupProvider {
		if g, ok := s.Groups[res.Model]; ok && len(g.Members) > 0 {
			return append(slices.Clone(g.Members), res.Fallbacks...)
		}
	}
	return append([]Fallback{{res.Provider, res.Model}}, res.Fallbacks...)
}

// head is the candidate that stands for the row where one model is needed (the
// agent runtime's model id, the wire): the first one listed.
func (s Settings) head(res Resolved) Fallback { return s.listed(res)[0] }

// callCandidates is listed for one call, with the group's mode applied. reason
// is ReasonRotate or ReasonUsage when the mode put a member other than the
// first listed in front, else empty. advance says the call is really made:
// only then does a rotation move on and a usage pick count as an attempt, so
// a look at the route (the cap check) leaves the order alone.
func (r *Router) callCandidates(ctx context.Context, s Settings, res Resolved, advance bool) (cands []Fallback, reason string) {
	g, ok := s.Groups[res.Model]
	if res.Provider != GroupProvider || !ok || g.Mode == GroupOrder || len(g.Members) < 2 {
		return s.listed(res), ""
	}
	members := slices.Clone(g.Members)
	switch g.Mode {
	case GroupRotate:
		start := r.rotation(res.Model, len(members), advance)
		members = slices.Concat(members[start:], members[:start])
		reason = ReasonRotate
	case GroupUsage:
		calls := r.dayCalls(ctx)
		slices.SortStableFunc(members, func(a, b Fallback) int {
			return cmp.Compare(calls[a.Provider+"/"+a.Model], calls[b.Provider+"/"+b.Model])
		})
		if advance {
			r.countCall(members[0])
		}
		reason = ReasonUsage
	}
	if members[0] == g.Members[0] {
		reason = ""
	}
	return append(members, res.Fallbacks...), reason
}

// advanceGroup moves a rotate group of an agent call on to its next member when
// the call was placed by the rotation, which the gateway knows only after
// affinity has had its say.
func (r *Router) advanceGroup(s Settings, res Resolved, placed bool) {
	if g, ok := s.Groups[res.Model]; ok && res.Provider == GroupProvider && g.Mode == GroupRotate && placed {
		r.rotation(res.Model, len(g.Members), true)
	}
}

// countGroupCall counts an agent call of a usage group on the member that
// answered it.
func (r *Router) countGroupCall(s Settings, res Resolved, won candidate) {
	if g, ok := s.Groups[res.Model]; ok && res.Provider == GroupProvider && g.Mode == GroupUsage {
		r.countCall(Fallback{Provider: won.provider.ID, Model: won.model})
	}
}

// rotation is the index of the member the next call of a group starts at: the
// calls so far, wrapped. The counts live in memory and start again at the first
// member after a restart.
func (r *Router) rotation(group string, members int, advance bool) int {
	r.restMu.Lock()
	defer r.restMu.Unlock()
	n := r.rotor[group]
	if advance {
		r.rotor[group] = n + 1
	}
	return n % members
}
