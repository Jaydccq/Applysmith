package applyapi

import (
	"encoding/json"
	"slices"
	"strings"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/gates"
	"applysmith/internal/workspace"
)

// skipListView reads the profile's list with the revision a write is checked
// against. A profile that does not exist yet shows the defaults.
func (s *Handlers) skipListView() (wire.ApplySkipList, error) {
	cur, err := s.Workspace.Read(workspace.FileProfile)
	if err != nil {
		return wire.ApplySkipList{}, s.ConfigError(err)
	}
	var p workspace.Profile
	if cur.Revision != workspace.RevisionAbsent {
		var rep workspace.Report
		p, rep = workspace.ParseProfile(cur.Data)
		if !rep.OK() {
			return wire.ApplySkipList{}, apikit.NewErr(wire.CodeUnavailable, "profile.yml has errors, so the skip list cannot be read")
		}
	}
	eff := p.EffectiveSkipCompanies()
	defaults := slices.Clone(gates.DefaultSkipList)
	return wire.ApplySkipList{Entries: eff, Defaulted: !p.SkipListPresent(), Differs: !sameNames(eff, defaults), Defaults: defaults, Revision: cur.Revision}, nil
}

func sameNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

func (s *Handlers) GetSkipList(c *apikit.Call) (apikit.Reply, error) {
	v, err := s.skipListView()
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: v}, nil
}

// PutSkipList replaces the whole list. The write is checked against the
// revision the client read, so an edit made elsewhere is a conflict, not a
// silent overwrite. Saving also counts as a save of the user's data for the
// list's holds, because the profile file changes.
func (s *Handlers) PutSkipList(c *apikit.Call) (apikit.Reply, error) {
	var req wire.ApplySkipListWrite
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if req.Revision == "" {
		return apikit.Reply{}, apikit.InvalidField("revision", "the revision of the list that was read is required")
	}
	entries := make([]string, 0, len(req.Entries))
	for _, e := range req.Entries {
		e = strings.TrimSpace(e)
		switch {
		case e == "":
			return apikit.Reply{}, apikit.InvalidField("entries", "an entry cannot be empty")
		case len([]rune(e)) > gates.MaxSkipEntryLength:
			return apikit.Reply{}, apikit.InvalidField("entries", "an entry has at most %d characters", gates.MaxSkipEntryLength)
		case slices.ContainsFunc(entries, func(x string) bool { return strings.EqualFold(x, e) }):
			continue
		}
		entries = append(entries, e)
	}
	if len(entries) > gates.MaxSkipEntries {
		return apikit.Reply{}, apikit.InvalidField("entries", "the list holds at most %d entries", gates.MaxSkipEntries)
	}
	b, _ := json.Marshal(entries)
	if _, err := s.Workspace.Edit(workspace.FileProfile, []workspace.Op{{Kind: workspace.OpSet, Path: "batch_skip_companies", Value: string(b)}}, req.Revision); err != nil {
		return apikit.Reply{}, s.ConfigError(err)
	}
	v, err := s.skipListView()
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: v}, nil
}

// skipCover names the list entry that covers a company, or "".
func skipCover(entries []string, company string) string {
	for _, entry := range entries {
		if gates.CoversCompany(entry, company) {
			return entry
		}
	}
	return ""
}

// SkipStatus answers whether a company is on the batch skip list, from
// the same effective list the batch gate reads.
func (s *Handlers) SkipStatus(c *apikit.Call) (apikit.Reply, error) {
	q := apikit.QueryOf(c)
	company := q.Str("company")
	if company == "" {
		return apikit.Reply{}, apikit.InvalidField("company", "company is required")
	}
	if len([]rune(company)) > gates.MaxSkipEntryLength {
		return apikit.Reply{}, apikit.InvalidField("company", "company has at most %d characters", gates.MaxSkipEntryLength)
	}
	entries, err := s.SkipEntries()
	if err != nil {
		return apikit.Reply{}, err
	}
	entry := skipCover(entries, company)
	return apikit.Reply{Data: wire.ApplySkipStatus{Company: company, Listed: entry != "", Entry: entry}}, nil
}
