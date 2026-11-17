// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"applysmith/internal/workspace"
)

// Profile is the seeker as evaluation reads them: profile.yml plus the CV and
// the narrative text.
type Profile struct {
	workspace.Profile
	CV, Narrative string
}

// LoadProfile reads profile.yml, cv.md and narrative.md from a workspace.
func LoadProfile(ws *workspace.Workspace) (Profile, error) {
	raw, err := ws.Read(workspace.FileProfile)
	if err != nil {
		return Profile{}, err
	}
	p, rep := workspace.ParseProfile(raw.Data)
	if !rep.OK() {
		return Profile{}, fmt.Errorf("profile.yml is not valid; run applysmith doctor")
	}
	out := Profile{Profile: p}
	if c, err := ws.Read(workspace.FileCV); err == nil {
		out.CV = string(c.Data)
	}
	if n, err := ws.Read(workspace.FileNarrative); err == nil {
		out.Narrative = string(n.Data)
	}
	return out, nil
}

// Years returns the profile's years of experience: the time the employment
// history covers, overlapping jobs once and the gaps between them not at all,
// else the largest skill years; nil when neither is known.
func (p Profile) Years(now time.Time) *float64 {
	type span struct{ start, end time.Time }
	var spans []span
	for _, e := range p.History.Employment {
		start, err := time.Parse("2006-01", e.Start)
		if err != nil {
			continue
		}
		end := now
		if e.End != "" && e.End != "present" {
			if t, err := time.Parse("2006-01", e.End); err == nil {
				end = t
			}
		}
		spans = append(spans, span{start, end})
	}
	if len(spans) > 0 {
		slices.SortFunc(spans, func(a, b span) int { return a.start.Compare(b.start) })
		var covered time.Duration
		var reached time.Time
		for _, s := range spans {
			if s.start.Before(reached) {
				s.start = reached
			}
			if s.end.After(s.start) {
				covered += s.end.Sub(s.start)
				reached = s.end
			}
		}
		y := covered.Hours() / (24 * 365.25)
		return &y
	}
	var best *float64
	for _, s := range p.Skills {
		if s.Years != nil && (best == nil || *s.Years > *best) {
			v := *s.Years
			best = &v
		}
	}
	return best
}

// CompTarget annualizes the profile's compensation target.
func (p Profile) CompTarget() CompTarget {
	c := p.Targets.Compensation
	mult := 1.0
	if c.Period == "hour" {
		mult = 2080
	}
	t := CompTarget{Currency: strings.ToUpper(c.Currency)}
	if c.Minimum != nil {
		t.Minimum = float64(*c.Minimum) * mult
	}
	if c.Target != nil {
		t.Target = float64(*c.Target) * mult
	}
	return t
}

// limitsSeniority reports whether the profile targets roles below senior, so
// that a clearly senior title is a mismatch. With no targets it is false.
func (p Profile) limitsSeniority() bool {
	top := -1
	for _, r := range p.Targets.Roles {
		for i, t := range ladder {
			if t == r.Seniority {
				top = max(top, i)
			}
		}
	}
	return top >= 0 && top < 3
}

// Views the templates read. Field names are the documented template fields.

// PostingView is the posting as a prompt reads it. Text is escaped.
type PostingView struct {
	Title, Company, Location, Compensation, Source, Text string
}

// ProfileView is the profile as a prompt reads it.
type ProfileView struct {
	Summary, CV, Narrative, Targets, Preferences, Compensation, WorkAuth string
}

// SponsorView is the sponsorship background of a prompt.
type SponsorView struct {
	Stance, Note, Need string
}

// Snippet is one retrieved knowledge snippet.
type Snippet struct {
	ID, Text string
}

func yesNo(v *bool) string {
	switch {
	case v == nil:
		return "unanswered"
	case *v:
		return "yes"
	}
	return "no"
}

func (p Profile) view(esc func(string) string) ProfileView {
	var targets []string
	for _, r := range p.Targets.Roles {
		targets = append(targets, strings.TrimSpace(r.Title+" "+r.Seniority))
	}
	wa := p.WorkAuthorization
	workAuth := fmt.Sprintf("authorized in %s; needs sponsorship now: %s; in future: %s",
		strings.Join(wa.AuthorizedCountries, ", "), yesNo(wa.RequiresSponsorshipNow), yesNo(wa.RequiresSponsorshipFuture))
	ct := p.CompTarget()
	comp := "not stated"
	if ct.Minimum > 0 {
		comp = fmt.Sprintf("minimum %.0f, target %.0f per year (%s)", ct.Minimum, max(ct.Target, 1.1*ct.Minimum), ct.Currency)
	}
	v := ProfileView{
		CV: esc(p.CV), Narrative: esc(p.Narrative), Targets: strings.Join(targets, "; "),
		Preferences: esc(p.preferences()), Compensation: comp, WorkAuth: workAuth,
	}
	v.Summary = fmt.Sprintf("Target roles: %s\nCompensation: %s\nWork authorization: %s\nSkills: %s",
		v.Targets, comp, workAuth, skillNames(p.Skills))
	return v
}

// preferences is the seeker's stated places and working limits in one line,
// "not stated" when the profile names none.
func (p Profile) preferences() string {
	var parts []string
	if len(p.Targets.Locations) > 0 {
		parts = append(parts, "locations "+strings.Join(p.Targets.Locations, ", "))
	}
	pr := p.Preferences
	if len(pr.WorkModes) > 0 {
		parts = append(parts, "work modes "+strings.Join(pr.WorkModes, ", "))
	}
	if pr.MaxDaysInOffice != nil {
		parts = append(parts, fmt.Sprintf("at most %d office days a week", *pr.MaxDaysInOffice))
	}
	if pr.MaxTravelPct != nil {
		parts = append(parts, fmt.Sprintf("at most %d%% travel", *pr.MaxTravelPct))
	}
	if pr.Relocation.Default != "" {
		parts = append(parts, "relocation "+pr.Relocation.Default)
	}
	if len(parts) == 0 {
		return "not stated"
	}
	return strings.Join(parts, "; ")
}

func skillNames(skills []workspace.Skill) string {
	var names []string
	for _, s := range skills {
		names = append(names, s.Name)
	}
	return strings.Join(names, ", ")
}
