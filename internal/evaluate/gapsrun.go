// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"applysmith/internal/store"
)

const (
	gapRunKind      = "gap-report"
	gapOperation    = "evaluate.gaps"
	maxGapSummaries = 50
)

// GapOptions configures Gaps. The zero value uses the wall clock and has no
// research source.
type GapOptions struct {
	Now func() time.Time
	// Finder is the research route for learning resources. Nil means none is
	// configured, and a plan then reports the lookup as unavailable.
	Finder ResourceFinder
}

// Gaps runs skill-gap analyses over saved reports and keeps their plans and
// progress. It reads reports and writes only the gap tables.
type Gaps struct {
	st     *store.Store
	root   string
	now    func() time.Time
	finder ResourceFinder
}

// NewGaps returns a Gaps over an open store and the workspace root that holds
// the report files.
func NewGaps(st *store.Store, root string, opts GapOptions) *Gaps {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Gaps{st: st, root: root, now: opts.Now, finder: opts.Finder}
}

func (g *Gaps) stamp() string { return g.now().UTC().Format(time.RFC3339) }

// GapSummary is one analysis in the list of earlier analyses.
type GapSummary struct {
	ID          string
	GeneratedAt string
	Sample      int
	Gaps        int
}

func normalizeReports(numbers []int64) ([]int64, error) {
	out := slices.Clone(numbers)
	slices.Sort(out)
	out = slices.Compact(out)
	if len(out) == 0 || len(out) > MaxGapReports {
		return nil, gapInvalid("select 1 to %d reports, got %d", MaxGapReports, len(out))
	}
	if out[0] < 1 {
		return nil, gapInvalid("report numbers are positive whole numbers")
	}
	return out, nil
}

// Start records the analysis as a run under the idempotency key and carries it
// out. A repeated key returns the first run without analyzing again.
func (g *Gaps) Start(ctx context.Context, principal, key string, numbers []int64) (runID string, replayed bool, err error) {
	nums, err := normalizeReports(numbers)
	if err != nil {
		return "", false, err
	}
	input, _ := json.Marshal(map[string]any{"reports": nums})
	ref, err := g.st.CreateRun(ctx, store.RunRequest{Principal: principal, Operation: gapOperation, Key: key, Kind: gapRunKind, Input: input})
	if err != nil || ref.Replayed {
		return ref.RunID, ref.Replayed, err
	}
	if err := g.st.SetRun(ctx, ref.RunID, "running", "analyzing", ""); err != nil {
		return ref.RunID, false, err
	}
	rep, err := g.analyze(ctx, nums)
	if err == nil {
		rep.ID = ref.RunID
		err = g.save(ctx, rep)
	}
	if err != nil {
		_ = g.st.SetRun(context.WithoutCancel(ctx), ref.RunID, "failed", "analyzing", "analysis-failed")
		return ref.RunID, false, err
	}
	return ref.RunID, false, g.st.SetRun(ctx, ref.RunID, "succeeded", "done", "")
}

func (g *Gaps) save(ctx context.Context, rep GapReport) error {
	body, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	return g.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO gap_reports (run_id, created_at, result_json) VALUES (?, ?, ?)`, rep.ID, rep.GeneratedAt, string(body))
		return err
	})
}

// Get returns an analysis with the states the user's confirmations give now
// and the progress recorded on its plan.
func (g *Gaps) Get(ctx context.Context, id string) (GapReport, error) {
	var result string
	var plan sql.NullString
	err := g.st.Read().QueryRowContext(ctx, `SELECT result_json, plan_json FROM gap_reports WHERE run_id = ?`, id).Scan(&result, &plan)
	if errors.Is(err, sql.ErrNoRows) {
		return GapReport{}, store.ErrNotFound
	}
	if err != nil {
		return GapReport{}, err
	}
	var rep GapReport
	if err := json.Unmarshal([]byte(result), &rep); err != nil {
		return GapReport{}, err
	}
	if plan.Valid {
		rep.Plan = &Plan{}
		if err := json.Unmarshal([]byte(plan.String), rep.Plan); err != nil {
			return GapReport{}, err
		}
	}
	confirmed, err := g.confirmed(ctx)
	if err != nil {
		return GapReport{}, err
	}
	for i := range rep.Gaps {
		rep.Gaps[i].State = gapState(rep.Gaps[i], confirmed)
	}
	done, err := g.progress(ctx, id)
	if err != nil {
		return GapReport{}, err
	}
	rep.Done = done
	if rep.Plan != nil {
		for i := range rep.Plan.Items {
			rep.Plan.Items[i].Done = slices.Contains(done, rep.Plan.Items[i].Skill)
		}
	}
	return rep, nil
}

// List returns the earlier analyses, newest first.
func (g *Gaps) List(ctx context.Context) ([]GapSummary, error) {
	rows, err := g.st.Read().QueryContext(ctx, `SELECT run_id, result_json FROM gap_reports ORDER BY created_at DESC, run_id DESC LIMIT ?`, maxGapSummaries)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GapSummary{}
	for rows.Next() {
		var id, body string
		if err := rows.Scan(&id, &body); err != nil {
			return nil, err
		}
		var rep GapReport
		if err := json.Unmarshal([]byte(body), &rep); err != nil {
			return nil, err
		}
		out = append(out, GapSummary{ID: id, GeneratedAt: rep.GeneratedAt, Sample: len(rep.Sample), Gaps: len(rep.Gaps)})
	}
	return out, rows.Err()
}

func (g *Gaps) confirmed(ctx context.Context) (map[string]bool, error) {
	rows, err := g.st.Read().QueryContext(ctx, `SELECT skill FROM gap_confirmations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out[s] = true
	}
	return out, rows.Err()
}

func (g *Gaps) progress(ctx context.Context, id string) ([]string, error) {
	rows, err := g.st.Read().QueryContext(ctx, `SELECT skill FROM gap_progress WHERE run_id = ? ORDER BY skill`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r GapReport) gap(skill string) (Gap, bool) {
	for _, x := range r.Gaps {
		if x.Skill == skill {
			return x, true
		}
	}
	return Gap{}, false
}

// Confirm records that the user lacks a skill listed in the analysis, or takes
// the confirmation back. Only a confirmed gap can enter a learning plan.
func (g *Gaps) Confirm(ctx context.Context, id, skill string, on bool) (GapReport, error) {
	rep, err := g.Get(ctx, id)
	if err != nil {
		return GapReport{}, err
	}
	skill = SkillKey(skill)
	if _, ok := rep.gap(skill); !ok {
		return GapReport{}, gapInvalid("%q is not a gap of this analysis", skill)
	}
	err = g.st.Write(ctx, func(tx *sql.Tx) error {
		if !on {
			_, err := tx.ExecContext(ctx, `DELETE FROM gap_confirmations WHERE skill = ?`, skill)
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO gap_confirmations (skill, confirmed_at) VALUES (?, ?) ON CONFLICT (skill) DO NOTHING`, skill, g.stamp())
		return err
	})
	if err != nil {
		return GapReport{}, err
	}
	return g.Get(ctx, id)
}

// SetProgress marks the study exercise of a planned skill complete or open
// again. It records progress in the analysis and nothing else: the profile and
// the answer bank do not learn of it.
func (g *Gaps) SetProgress(ctx context.Context, id, skill string, done bool) (GapReport, error) {
	rep, err := g.Get(ctx, id)
	if err != nil {
		return GapReport{}, err
	}
	skill = SkillKey(skill)
	planned := false
	if rep.Plan != nil {
		for _, it := range rep.Plan.Items {
			planned = planned || it.Skill == skill
		}
	}
	if !planned {
		return GapReport{}, gapInvalid("%q is not in this analysis's learning plan", skill)
	}
	err = g.st.Write(ctx, func(tx *sql.Tx) error {
		if !done {
			_, err := tx.ExecContext(ctx, `DELETE FROM gap_progress WHERE run_id = ? AND skill = ?`, id, skill)
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO gap_progress (run_id, skill, done_at) VALUES (?, ?, ?) ON CONFLICT (run_id, skill) DO NOTHING`, id, skill, g.stamp())
		return err
	})
	if err != nil {
		return GapReport{}, err
	}
	return g.Get(ctx, id)
}
