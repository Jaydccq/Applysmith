package apply

import (
	"context"
	"database/sql"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
	"applysmith/internal/store"
)

// applyModelTasks are the model tasks an application run drives. The first
// four serve only the chain; option-pick and narrative also serve the panel,
// which is why a call counts only inside a run's own time window.
var applyModelTasks = []string{"apply-check", "apply-complete", "apply-fix", "apply-page-decision", "option-pick", "narrative"}

// flagBlockReason is the check stage's reason when a review flag blocks Submit.
const flagBlockReason = "the review contradicts a value this run wrote"

// ModelCall is one model call of the ledger.
type ModelCall struct {
	Task, Provider string
	At             time.Time
	OK             bool
	LatencyMs      int64
	CostUSD        float64
	// Priced is false for a call whose cost is unknown or not metered.
	Priced bool
}

// ProviderStats is the model calls of one provider.
type ProviderStats struct {
	Provider     string  `json:"provider"`
	Calls        int     `json:"calls"`
	FailRate     float64 `json:"failRate"`
	LatencyP50Ms *int64  `json:"latencyP50Ms"`
	CostUSD      float64 `json:"costUsd"`
}

// PageTimings are the p50 of the per-page phases, over pages that carry them.
type PageTimings struct {
	Pages      int    `json:"pages"`
	FillMsP50  *int64 `json:"fillMsP50"`
	CheckMsP50 *int64 `json:"checkMsP50"`
	FixMsP50   *int64 `json:"fixMsP50"`
}

// ScoreRow is one UTC day, or the whole window under Day "total".
type ScoreRow struct {
	Day        string  `json:"day"`
	Runs       int     `json:"runs"`
	RunMinutes float64 `json:"runMinutes"`
	Submitted  int     `json:"submitted"`
	Confirmed  int     `json:"confirmed"`
	// SubmitSecondsP50 and P90 are start to finish of the submitted runs,
	// confirmation wait included.
	SubmitSecondsP50 *float64 `json:"submitSecondsP50"`
	SubmitSecondsP90 *float64 `json:"submitSecondsP90"`
	// Outcomes counts `submitted`, `filled` (a dry run) and, for a stopped
	// run, its failure class.
	Outcomes map[string]int `json:"outcomes"`
	// RepeatRuns and RepeatMinutes are the stopped runs whose verdict repeated
	// the previous run of the same posting.
	RepeatRuns    int     `json:"repeatRuns"`
	RepeatMinutes float64 `json:"repeatMinutes"`
	// ReviewedPages and Flags are the advisory review's; BlockedRuns the runs
	// a flag stopped.
	ReviewedPages        int             `json:"reviewedPages"`
	Flags                int             `json:"flags"`
	FlagsPerReviewedPage *float64        `json:"flagsPerReviewedPage"`
	BlockedRuns          int             `json:"blockedRuns"`
	PageTimings          PageTimings     `json:"pageTimings"`
	Models               []ProviderStats `json:"models"`
}

// DefectCluster is one recurring way the chain itself failed.
type DefectCluster struct {
	Stage  string `json:"stage"`
	System string `json:"system"`
	// Shape is the reason with numbers and addresses collapsed.
	Shape     string    `json:"shape"`
	Runs      int       `json:"runs"`
	Postings  int       `json:"postings"`
	Minutes   float64   `json:"minutes"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
	// Open counts the postings whose latest run still ends in this cluster.
	Open int `json:"open"`
	// LatestSnapshot is the newest run's stop snapshot, where a fixer starts.
	LatestSnapshot string `json:"latestSnapshot,omitempty"`
}

// Scorecard summarizes apply runs per UTC day and for the window.
type Scorecard struct {
	Since    time.Time       `json:"since"`
	Days     []ScoreRow      `json:"days"`
	Total    ScoreRow        `json:"total"`
	Clusters []DefectCluster `json:"defectClusters"`
}

// WindowStart is midnight UTC, days-1 days before now: one day is today so far.
func WindowStart(days int, now time.Time) time.Time {
	n := now.UTC()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(max(days, 1) - 1))
}

var (
	scoreDigitsRE = lazyregexp.New(`\d+`)
	scoreSpacesRE = lazyregexp.New(`\s+`)
	scoreURLRE    = lazyregexp.New(`https?://\S+`)
)

// verdictKey is what a stopped run concluded, in a form two runs compare by:
// stage, class, and the reason with its numbers collapsed and cut to its
// opening. Empty for a run that did not stop.
func verdictKey(r RunFact) string {
	if !stoppedRun(r) {
		return ""
	}
	return string(r.Stage) + ":" + string(r.Class) + ":" + clipRunes(collapse(scoreDigitsRE, r.Reason), 60)
}

func collapse(re *lazyregexp.Regexp, s string) string {
	return strings.TrimSpace(scoreSpacesRE.ReplaceAllString(re.ReplaceAllString(s, "N"), " "))
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// repeatedRuns are the runs whose verdict repeated the previous run of the
// same posting. It needs the whole history, not the window: the earlier run of
// a repeat may predate it. A stop on the user's own missing data is not
// counted until the user saved something after the previous run ended, since
// nothing but that save could have changed it.
func repeatedRuns(runs []RunFact, dataSavedAt time.Time) map[string]bool {
	last := map[string]RunFact{}
	out := map[string]bool{}
	for _, r := range runs {
		k := postingKey(r.URL)
		prev, had := last[k]
		if v := verdictKey(r); v != "" && had && verdictKey(prev) == v {
			if !userDataStop(r) || dataSavedAt.After(prev.Finished) {
				out[r.ID] = true
			}
		}
		last[k] = r
	}
	return out
}

func outcomeLabel(r RunFact) string {
	if stoppedRun(r) {
		return firstNonEmpty(string(r.Class), "unclassified")
	}
	return string(r.Kind)
}

// percentile is the nearest-rank percentile; nil for no values.
func percentile[T int64 | float64](vs []T, p float64) *T {
	if len(vs) == 0 {
		return nil
	}
	s := slices.Clone(vs)
	slices.Sort(s)
	v := s[max(int(math.Ceil(p*float64(len(s))))-1, 0)]
	return &v
}

func round(v float64, digits int) float64 {
	f := math.Pow(10, float64(digits))
	return math.Round(v*f) / f
}

func roundPtr(v *float64) *float64 {
	if v == nil {
		return nil
	}
	r := round(*v, 0)
	return &r
}

func scoreRow(day string, runs []RunFact, calls []ModelCall, repeated map[string]bool) ScoreRow {
	row := ScoreRow{Day: day, Runs: len(runs), Outcomes: map[string]int{}, Models: providerStats(calls)}
	var submitSecs []float64
	var fill, check, fix []int64
	var minutes, repeatMinutes float64
	for _, r := range runs {
		minutes += r.Minutes()
		row.Outcomes[outcomeLabel(r)]++
		if r.Kind == OutcomeSubmitted {
			row.Submitted++
			if r.Confirmed {
				row.Confirmed++
			}
			submitSecs = append(submitSecs, r.Minutes()*60)
		}
		if repeated[r.ID] {
			row.RepeatRuns++
			repeatMinutes += r.Minutes()
		}
		if stoppedRun(r) && strings.Contains(r.Reason, flagBlockReason) {
			row.BlockedRuns++
		}
		for _, p := range r.Pages {
			if p.Review == "ok" {
				row.ReviewedPages++
			}
			row.Flags += len(p.Flags)
			if p.FillMs+p.CheckMs+p.FixMs > 0 {
				row.PageTimings.Pages++
				fill, check, fix = append(fill, p.FillMs), append(check, p.CheckMs), append(fix, p.FixMs)
			}
		}
	}
	row.RunMinutes, row.RepeatMinutes = round(minutes, 1), round(repeatMinutes, 1)
	row.SubmitSecondsP50, row.SubmitSecondsP90 = roundPtr(percentile(submitSecs, 0.5)), roundPtr(percentile(submitSecs, 0.9))
	if row.ReviewedPages > 0 {
		v := round(float64(row.Flags)/float64(row.ReviewedPages), 2)
		row.FlagsPerReviewedPage = &v
	}
	row.PageTimings.FillMsP50, row.PageTimings.CheckMsP50, row.PageTimings.FixMsP50 = percentile(fill, 0.5), percentile(check, 0.5), percentile(fix, 0.5)
	return row
}

func providerStats(calls []ModelCall) []ProviderStats {
	by := map[string][]ModelCall{}
	for _, c := range calls {
		by[firstNonEmpty(c.Provider, "(unknown)")] = append(by[firstNonEmpty(c.Provider, "(unknown)")], c)
	}
	var out []ProviderStats
	for name, cs := range by {
		var failed int
		var lat []int64
		var cost float64
		for _, c := range cs {
			if !c.OK {
				failed++
			}
			lat = append(lat, c.LatencyMs)
			if c.Priced {
				cost += c.CostUSD
			}
		}
		out = append(out, ProviderStats{Provider: name, Calls: len(cs), FailRate: round(float64(failed)/float64(len(cs)), 3), LatencyP50Ms: percentile(lat, 0.5), CostUSD: round(cost, 4)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].Provider < out[j].Provider
	})
	return out
}

// BuildScorecard aggregates runs started at or after since, one row per UTC
// day that has a run, plus the window total. runs is the whole history that
// matters (repeat detection reads the run before the window's first); calls
// may be any superset: only apply-task calls inside a run of the window count.
// dataSavedAt is when the user last saved the answer bank, profile or CV.
func BuildScorecard(runs []RunFact, calls []ModelCall, since, dataSavedAt time.Time) Scorecard {
	ordered := slices.Clone(runs)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Started.Before(ordered[j].Started) })
	repeated := repeatedRuns(ordered, dataSavedAt)
	var window []RunFact
	byDay := map[string][]RunFact{}
	for _, r := range ordered {
		if !r.Started.Before(since) {
			window = append(window, r)
			d := r.Started.UTC().Format("2006-01-02")
			byDay[d] = append(byDay[d], r)
		}
	}
	callsByDay := map[string][]ModelCall{}
	var counted []ModelCall
	for _, c := range calls {
		if !slices.Contains(applyModelTasks, c.Task) {
			continue
		}
		for _, r := range window {
			if !r.Finished.IsZero() && !c.At.Before(r.Started) && !c.At.After(r.Finished) {
				d := r.Started.UTC().Format("2006-01-02")
				callsByDay[d] = append(callsByDay[d], c)
				counted = append(counted, c)
				break
			}
		}
	}
	sc := Scorecard{Since: since, Total: scoreRow("total", window, counted, repeated), Clusters: defectClusters(ordered, since, 10)}
	days := make([]string, 0, len(byDay))
	for d := range byDay {
		days = append(days, d)
	}
	sort.Strings(days)
	for _, d := range days {
		sc.Days = append(sc.Days, scoreRow(d, byDay[d], callsByDay[d], repeated))
	}
	return sc
}

func defectShape(reason string) string {
	return clipRunes(collapse(scoreDigitsRE, scoreURLRE.ReplaceAllString(reason, "URL")), 90)
}

// defectClusters groups the window's defect stops by stage, system and reason
// shape, most open postings first. Open separates a cluster a merged fix
// already cleared (its postings have moved on) from one still failing.
func defectClusters(ordered []RunFact, since time.Time, limit int) []DefectCluster {
	keyOf := func(r RunFact) string {
		if stoppedRun(r) && r.Class == ClassDefect {
			return string(r.Stage) + "\x00" + r.ATS + "\x00" + defectShape(r.Reason)
		}
		return ""
	}
	latest := map[string]RunFact{}
	for _, r := range ordered {
		latest[postingKey(r.URL)] = r
	}
	groups := map[string][]RunFact{}
	for _, r := range ordered {
		if k := keyOf(r); k != "" && !r.Started.Before(since) {
			groups[k] = append(groups[k], r)
		}
	}
	var out []DefectCluster
	for k, rs := range groups {
		parts := strings.SplitN(k, "\x00", 3)
		c := DefectCluster{Stage: parts[0], System: parts[1], Shape: parts[2], Runs: len(rs), FirstSeen: rs[0].Started, LastSeen: rs[len(rs)-1].Started, LatestSnapshot: rs[len(rs)-1].Snapshot}
		postings := map[string]bool{}
		var minutes float64
		for _, r := range rs {
			postings[postingKey(r.URL)] = true
			minutes += r.Minutes()
		}
		c.Postings, c.Minutes = len(postings), round(minutes, 1)
		for p := range postings {
			if keyOf(latest[p]) == k {
				c.Open++
			}
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Open != out[j].Open {
			return out[i].Open > out[j].Open
		}
		if out[i].Runs != out[j].Runs {
			return out[i].Runs > out[j].Runs
		}
		return out[i].Shape < out[j].Shape
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// LoadScorecard reads the window's runs (and the newest run of each posting
// before it, which repeat detection needs) and the apply-task model calls, and
// aggregates them. days is the window length, default 7.
func LoadScorecard(ctx context.Context, st *store.Store, days int, now, dataSavedAt time.Time) (Scorecard, error) {
	if days <= 0 {
		days = 7
	}
	since := WindowStart(days, now)
	stamp := since.Format(stampLayout)
	runs, err := loadRuns(ctx, st.Read(), true, `started_at >= ? OR run_id IN (SELECT run_id FROM (SELECT run_id,
		ROW_NUMBER() OVER (PARTITION BY identity_key ORDER BY started_at DESC, run_id DESC) AS rn FROM apply_runs WHERE started_at < ?) WHERE rn = 1)`, stamp, stamp)
	if err != nil {
		return Scorecard{}, err
	}
	calls, err := loadModelCalls(ctx, st.Read(), stamp)
	if err != nil {
		return Scorecard{}, err
	}
	return BuildScorecard(runs, calls, since, dataSavedAt), nil
}

func loadModelCalls(ctx context.Context, db *sql.DB, since string) ([]ModelCall, error) {
	rows, err := db.QueryContext(ctx, `SELECT task, provider_id, started_at, outcome, latency_ms, cost_usd, billing FROM llm_ledger
		WHERE started_at >= ? AND task IN (`+strings.TrimSuffix(strings.Repeat("?,", len(applyModelTasks)), ",")+`)`,
		append([]any{since}, anySlice(applyModelTasks)...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelCall
	for rows.Next() {
		var c ModelCall
		var at, outcome, billing string
		var cost sql.NullFloat64
		if err := rows.Scan(&c.Task, &c.Provider, &at, &outcome, &c.LatencyMs, &cost, &billing); err != nil {
			return nil, err
		}
		c.At, c.OK, c.CostUSD, c.Priced = parseStampText(at), outcome == "ok", cost.Float64, cost.Valid && billing == "metered"
		out = append(out, c)
	}
	return out, rows.Err()
}

func anySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
