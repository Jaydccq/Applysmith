// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// MaxPlanSkills bounds the gaps one learning plan covers, and MaxResources the
// verified resources it lists for each.
const (
	MaxPlanSkills = 5
	MaxResources  = 2
)

// Lookup statuses of a plan's resource lookup.
const (
	LookupNotRequested = "not-requested"
	LookupUnavailable  = "unavailable"
	LookupDone         = "done"
	LookupFailed       = "failed"
)

// Resource is a learning resource the research route found.
type Resource struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	// Verified is true only when the route fetched the page and it was there.
	Verified bool `json:"verified"`
}

// ErrResourceSourceUnavailable means no configured research route can search.
var ErrResourceSourceUnavailable = errors.New("learning resources: no research source configured")

// ResourceFinder is the research route a plan asks for learning resources.
type ResourceFinder interface {
	Find(ctx context.Context, skill string, max int) ([]Resource, error)
}

// PlanItem is the study plan for one confirmed gap.
type PlanItem struct {
	Skill     string `json:"skill"`
	Label     string `json:"label"`
	Exercise  string `json:"exercise"`
	Criterion string `json:"criterion"`
	// Effort is an estimate, and its text says so.
	Effort    string     `json:"effort"`
	Resources []Resource `json:"resources"`
	Done      bool       `json:"done"`
}

// ResourceLookup says what became of the request for learning resources.
type ResourceLookup struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// Plan is a bounded learning plan over selected gaps.
type Plan struct {
	GeneratedAt string         `json:"generated_at"`
	Items       []PlanItem     `json:"items"`
	Lookup      ResourceLookup `json:"lookup"`
}

func exerciseFor(g Gap) (exercise, criterion string) {
	exercise = fmt.Sprintf("Review the requirement for %s. Choose a relevant practice exercise, such as a small project, scenario walkthrough or written analysis, and record its result and limitations. If the requirement concerns experience, credentials or availability, identify the actual steps or constraints separately.", g.Label)
	criterion = fmt.Sprintf("A reviewer can inspect your exercise result for %s, reproduce or discuss the method, and identify what remains unproven. Completing practice does not establish work experience, credentials or availability, and does not close the qualification gap.", g.Label)
	return exercise, criterion
}

const planScopingEffort = "Estimate: 1 to 2 hours to review the requirement and scope an exercise. This is an unmeasured estimate for planning only; practice time depends on the chosen exercise, and time to meet the actual qualification is not estimated."

// BuildPlan proposes an exercise and a completion criterion for up to five
// confirmed gaps of an analysis. Learning resources come only from the
// research route, only when asked for, and only when verified; with no route
// the plan says the lookup is unavailable and lists no links.
func (g *Gaps) BuildPlan(ctx context.Context, id string, skills []string, lookup bool) (GapReport, error) {
	parent := ctx
	if lookup {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
	}
	rep, err := g.Get(ctx, id)
	if err != nil {
		return GapReport{}, err
	}
	var keys []string
	for _, s := range skills {
		if k := SkillKey(s); k != "" && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 || len(keys) > MaxPlanSkills {
		return GapReport{}, gapInvalid("select 1 to %d gaps for a plan, got %d", MaxPlanSkills, len(keys))
	}
	plan := Plan{GeneratedAt: g.stamp(), Items: []PlanItem{}, Lookup: ResourceLookup{Status: LookupNotRequested}}
	if lookup {
		plan.Lookup = ResourceLookup{Status: LookupDone}
		if g.finder == nil {
			plan.Lookup = ResourceLookup{Status: LookupUnavailable, Reason: "no research source is configured, so no learning resources are listed"}
		}
	}
	for _, k := range keys {
		gap, ok := rep.gap(k)
		switch {
		case !ok:
			return GapReport{}, gapInvalid("%q is not a gap of this analysis", k)
		case gap.State != GapConfirmed:
			return GapReport{}, gapInvalid("confirm that you lack %q before planning to learn it; a missing line in the CV does not show it", gap.Label)
		}
		item := PlanItem{Skill: k, Label: gap.Label, Resources: []Resource{}, Effort: planScopingEffort}
		item.Exercise, item.Criterion = exerciseFor(gap)
		if lookup && g.finder != nil {
			found, err := g.finder.Find(ctx, gap.Label, MaxResources)
			if errors.Is(err, ErrResourceSourceUnavailable) {
				plan.Lookup = ResourceLookup{Status: LookupUnavailable, Reason: "no research source is configured, so no learning resources are listed"}
			} else if err != nil {
				plan.Lookup = ResourceLookup{Status: LookupFailed, Reason: "the research lookup failed for " + gap.Label + "; no resources were added"}
			}
			for _, r := range found {
				if r.Verified && strings.TrimSpace(r.URL) != "" && len(item.Resources) < MaxResources {
					item.Resources = append(item.Resources, r)
				}
			}
		}
		plan.Items = append(plan.Items, item)
	}
	ctx = parent
	body, err := json.Marshal(plan)
	if err != nil {
		return GapReport{}, err
	}
	err = g.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE gap_reports SET plan_json = ? WHERE run_id = ?`, string(body), id); err != nil {
			return err
		}
		// Progress belongs to the exercises of the current plan.
		for _, done := range rep.Done {
			if !slices.Contains(keys, done) {
				if _, err := tx.ExecContext(ctx, `DELETE FROM gap_progress WHERE run_id = ? AND skill = ?`, id, done); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return GapReport{}, err
	}
	return g.Get(ctx, id)
}
