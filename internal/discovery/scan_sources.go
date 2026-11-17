// Clean-room: docs/provenance.md#CR-SCAN-SKELETON

package discovery

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"applysmith/internal/workspace"
)

// Source kinds.
const (
	KindAPI     = "api"
	KindFeed    = "feed"
	KindBrowser = "browser"
)

// SourceInfo describes one named scan.
type SourceInfo struct {
	Name string
	// Title is the name shown to the user.
	Title string
	Kind  string
	// Needs is "key" or "session" when the source cannot run without one.
	Needs string
	// KeyName is the vault name of the key a keyed source reads.
	KeyName string
	// Host is the main host a browser source reads. It decides whether the
	// user's "watch scans" setting applies: guarded boards always stay hidden.
	Host string
	// SavedSearch is true for a source that runs only the saved searches of
	// portals.yml and skips a run that has none.
	SavedSearch bool
}

// Sources is the catalog, in the order the spec lists it.
var Sources = []SourceInfo{
	{Name: "registry", Title: "Company job boards", Kind: KindAPI},
	{Name: "tracked", Title: "Tracked companies", Kind: KindAPI},
	{Name: "builtin", Title: "Built In", Kind: KindFeed, SavedSearch: true},
	{Name: "amazon", Title: "Amazon Jobs", Kind: KindAPI, SavedSearch: true},
	{Name: "a16z", Title: "a16z portfolio jobs", Kind: KindAPI},
	{Name: "theirstack", Title: "TheirStack", Kind: KindAPI, Needs: "key", KeyName: "api-key-theirstack"},
	{Name: "github", Title: "GitHub new-grad lists", Kind: KindFeed},
	{Name: "speedrun", Title: "a16z Speedrun", Kind: KindFeed},
	{Name: "exa", Title: "Exa search", Kind: KindAPI, Needs: "key", KeyName: "api-key-exa"},
	{Name: "linkedin", Title: "LinkedIn search", Kind: KindBrowser, Needs: "session", Host: "linkedin.com"},
	{Name: "linkedin-feed", Title: "LinkedIn feed", Kind: KindBrowser, Needs: "session", Host: "linkedin.com"},
	{Name: "indeed", Title: "Indeed", Kind: KindBrowser, Host: "indeed.com", SavedSearch: true},
	{Name: "ziprecruiter", Title: "ZipRecruiter", Kind: KindBrowser, Host: "ziprecruiter.com", SavedSearch: true},
	{Name: "google-jobs", Title: "Google Jobs", Kind: KindBrowser, Host: "google.com", SavedSearch: true},
	{Name: "glassdoor", Title: "Glassdoor", Kind: KindBrowser, Needs: "session", Host: "glassdoor.com", SavedSearch: true},
	{Name: "newgrad", Title: "New-grad Jobs", Kind: KindBrowser, Host: "jobright.ai"},
	{Name: "jobright", Title: "Jobright", Kind: KindBrowser, Needs: "session", Host: "jobright.ai"},
}

// OpenBoards are the job boards that run saved searches and need neither a key
// nor a sign-in, the ones setup can turn on for a new user.
func OpenBoards() []SourceInfo {
	var out []SourceInfo
	for _, s := range Sources {
		if s.SavedSearch && s.Needs == "" {
			out = append(out, s)
		}
	}
	return out
}

// LookupSource finds a source by name.
func LookupSource(name string) (SourceInfo, error) {
	for _, s := range Sources {
		if s.Name == name {
			return s, nil
		}
	}
	names := make([]string, len(Sources))
	for i, s := range Sources {
		names[i] = s.Name
	}
	return SourceInfo{}, fmt.Errorf("unknown source %q; the sources are %s", name, strings.Join(names, ", "))
}

// Readiness describes whether a source can run now. hasKey reports whether a
// vault secret exists.
func (s SourceInfo) Readiness(hasKey func(name string) bool) string {
	if s.Needs == "key" && (hasKey == nil || !hasKey(s.KeyName)) {
		return "needs key"
	}
	return "ready"
}

// SearchReadiness is the state of a source whose Start would end skipped for
// want of something to read in portals.yml: a source that runs only saved
// searches has none (a search typed under Options still runs), or the tracked
// companies source has no enabled company. It is "" when there is something
// to read.
func SearchReadiness(src SourceInfo, portals workspace.Portals) (readiness, reason string) {
	switch {
	case src.SavedSearch && len(portals.Sources[src.Name].Queries) == 0:
		return "needs searches", src.Title + " runs saved searches and has none yet; type a search under Options to run one now."
	case src.Name == "tracked" && !slices.ContainsFunc(portals.Companies, func(c workspace.Company) bool { return c.IsEnabled() }):
		return "needs companies", "No company is tracked yet."
	}
	return "", ""
}

// evalMode is the evaluation mode a source's rows are enqueued with: auto
// (quick screen first) for the browser and list sources, deep for the rest,
// unless a mode is named. A promotion passes the row's stored source, so the
// name Jobright rows are stored under counts as Jobright.
func evalMode(source, named string) string {
	if named != "" {
		return named
	}
	switch source {
	case "linkedin", "linkedin-feed", "indeed", "ziprecruiter", "google-jobs", "glassdoor", "newgrad", "jobright", "jobright-recommend",
		"github", "speedrun", "theirstack":
		return "auto"
	}
	return "deep"
}

// TargetRoles is what "Use my target roles" writes to portals.yml: the target
// role titles the title filter lacks, and the primary target role as the one
// saved search of each enabled source that skips a run without one. Planning
// again after it is written plans nothing.
type TargetRoles struct {
	// Terms are the titles filters.title.positive gains; Positive is the
	// whole list afterwards.
	Terms, Positive []string
	Searches        []SavedSearch
}

// SavedSearch is one saved search of a source.
type SavedSearch struct{ Source, Search string }

// PlanTargetRoles plans the write from the profile's target roles.
func PlanTargetRoles(portals workspace.Portals, profile workspace.Profile) TargetRoles {
	var titles []string
	primary := ""
	for _, role := range profile.Targets.Roles {
		t := strings.TrimSpace(role.Title)
		if t == "" || slices.ContainsFunc(titles, func(s string) bool { return strings.EqualFold(s, t) }) {
			continue
		}
		titles = append(titles, t)
		if primary == "" && role.EffectivePriority() == "primary" {
			primary = t
		}
	}
	if len(titles) == 0 {
		return TargetRoles{}
	}
	plan := TargetRoles{Positive: slices.Clone(portals.Filters.Title.Positive)}
	for _, t := range titles {
		if !slices.ContainsFunc(plan.Positive, func(s string) bool { return strings.EqualFold(strings.TrimSpace(s), t) }) {
			plan.Terms = append(plan.Terms, t)
			plan.Positive = append(plan.Positive, t)
		}
	}
	for _, src := range Sources {
		cfg, listed := portals.Sources[src.Name]
		if src.SavedSearch && listed && (cfg.Enabled == nil || *cfg.Enabled) && len(cfg.Queries) == 0 {
			plan.Searches = append(plan.Searches, SavedSearch{Source: src.Name, Search: cmp.Or(primary, titles[0])})
		}
	}
	return plan
}

// Empty is true when there is nothing to write.
func (p TargetRoles) Empty() bool { return len(p.Terms) == 0 && len(p.Searches) == 0 }

// Ops are the edits of portals.yml that write the plan.
func (p TargetRoles) Ops() []workspace.Op {
	value := func(v any) string {
		b, _ := json.Marshal(v) // JSON is YAML; a list of strings always encodes
		return string(b)
	}
	var ops []workspace.Op
	if len(p.Terms) > 0 {
		ops = append(ops, workspace.Op{Kind: workspace.OpSet, Path: "filters.title.positive", Value: value(p.Positive)})
	}
	for _, s := range p.Searches {
		ops = append(ops, workspace.Op{Kind: workspace.OpSet, Path: "sources." + s.Source + ".queries", Value: value([]string{s.Search})})
	}
	return ops
}

// Lines say what the plan writes, one line per change.
func (p TargetRoles) Lines() []string {
	var out []string
	if len(p.Terms) > 0 {
		out = append(out, "Title must contain one of: add "+strings.Join(p.Terms, ", "))
	}
	for _, s := range p.Searches {
		out = append(out, fmt.Sprintf("Saved search of %s: %s", s.Source, s.Search))
	}
	return out
}
