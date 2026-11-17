package signals

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"
)

// TierRow is the stored tier of a company.
type TierRow struct {
	CompanyID int64
	Tier      string
	Momentum  float64
	Priority  float64
	Policy    string
	HotSince  time.Time
	ChangedAt time.Time
	UpdatedAt time.Time
}

// TierChange is a tier that moved in a rescore.
type TierChange struct {
	CompanyID int64   `json:"companyId"`
	Company   string  `json:"company"`
	From      string  `json:"from"`
	To        string  `json:"to"`
	Momentum  float64 `json:"momentum"`
}

// scoredByCompany reads every ok assessment of the current rubric with its
// event time and the adapters of its documents.
func (r *Repo) scoredByCompany(ctx context.Context, p Policy, now time.Time, only int64) (map[int64][]Scored, error) {
	q := `SELECT ` + "a." + strings.ReplaceAll(assessmentColumns, ", ", ", a.") + `, e.company_id, e.event_time,
		COALESCE((SELECT group_concat(DISTINCT d.adapter) FROM signal_event_docs ed JOIN signal_documents d ON d.id = ed.document_id WHERE ed.event_id = e.id), '')
		FROM signal_assessments a JOIN signal_events e ON e.id = a.event_id WHERE a.status = 'ok' AND a.rubric_version = ?`
	args := []any{RubricVersion}
	if only != 0 {
		q += ` AND e.company_id = ?`
		args = append(args, only)
	}
	rows, err := r.s.Read().QueryContext(ctx, q+` ORDER BY a.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]Scored{}
	for rows.Next() {
		var a Assessment
		var teams, quotes, created, et, adapters string
		var growth sql.NullInt64
		var company int64
		if err := rows.Scan(&a.ID, &a.EventID, &a.Rubric, &a.Status, &a.Type, &a.Intent, &teams, &growth, &a.Scale, &a.Strength, &a.Confidence,
			&a.Reasoning, &quotes, &created, &company, &et, &adapters); err != nil {
			return nil, err
		}
		a.Teams = decodeStrings(teams)
		_ = jsonUnmarshal(quotes, &a.Quotes)
		if growth.Valid {
			g := growth.Int64 == 1
			a.Growth = &g
		}
		var ads []string
		if adapters != "" {
			ads = strings.Split(adapters, ",")
		}
		t := parseTS(et)
		out[company] = append(out[company], Scored{Assessment: a, EventTime: t, Adapters: ads, Factors: p.Score(a, t, ads, now)})
	}
	return out, rows.Err()
}

// ActiveBlock reports whether a hiring block is in force for the company.
func (r *Repo) ActiveBlock(ctx context.Context, companyID int64, now time.Time) (bool, error) {
	var n int
	err := r.s.Read().QueryRowContext(ctx, `SELECT count(*) FROM signal_blocks WHERE company_id = ? AND lifted_at IS NULL AND expires_at > ?`, companyID, fmtTS(now)).Scan(&n)
	return n > 0, err
}

// LiftBlock ends the active blocks of a company; only the user does this.
func (r *Repo) LiftBlock(ctx context.Context, companyID int64) (int, error) {
	n := 0
	err := r.s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE signal_blocks SET lifted_at = ? WHERE company_id = ? AND lifted_at IS NULL AND expires_at > ?`, r.stamp(), companyID, r.stamp())
		if err != nil {
			return err
		}
		c, _ := res.RowsAffected()
		n = int(c)
		return auditTx(ctx, tx, r.stamp(), "block-lifted", companyID, "user action")
	})
	return n, err
}

// roleKeywords are the words of the target role titles.
func roleKeywords(titles []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range titles {
		for _, w := range strings.FieldsFunc(strings.ToLower(t), func(r rune) bool { return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') }) {
			if len(w) >= 3 && !seen[w] {
				seen[w] = true
				out = append(out, w)
			}
		}
	}
	return out
}

// wordMatch reports whether two words match, allowing one to be the stem of
// the other ("engineer" and "engineering").
func wordMatch(a, b string) bool {
	if a == b {
		return true
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	return len(a) >= 4 && strings.HasPrefix(b, a)
}

// scanPriority is momentum when the affected teams match the target role
// keywords, when no team evidence exists yet or no target roles are set, and
// zero when teams were observed and none match.
func scanPriority(momentum float64, items []Scored, keywords []string) float64 {
	if len(keywords) == 0 {
		return momentum
	}
	observed := false
	for _, it := range items {
		for _, team := range it.Assessment.Teams {
			observed = true
			for _, tw := range strings.FieldsFunc(strings.ToLower(team), func(r rune) bool { return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') }) {
				for _, k := range keywords {
					if wordMatch(tw, k) {
						return momentum
					}
				}
			}
		}
	}
	if !observed {
		return momentum
	}
	return 0
}

// Tier reads the stored tier of a company.
func (r *Repo) Tier(ctx context.Context, companyID int64) (TierRow, bool, error) {
	var t TierRow
	var hot sql.NullString
	var changed, updated string
	err := r.s.Read().QueryRowContext(ctx, `SELECT company_id, tier, momentum, priority, policy, hot_since, changed_at, updated_at FROM signal_tiers WHERE company_id = ?`, companyID).
		Scan(&t.CompanyID, &t.Tier, &t.Momentum, &t.Priority, &t.Policy, &hot, &changed, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return t, false, nil
	}
	t.HotSince, t.ChangedAt, t.UpdatedAt = parseNullTS(hot), parseTS(changed), parseTS(updated)
	return t, err == nil, err
}

// Tiers lists all stored tiers.
func (r *Repo) Tiers(ctx context.Context) (map[int64]TierRow, error) {
	rows, err := r.s.Read().QueryContext(ctx, `SELECT company_id, tier, momentum, priority, policy, hot_since, changed_at, updated_at FROM signal_tiers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]TierRow{}
	for rows.Next() {
		var t TierRow
		var hot sql.NullString
		var changed, updated string
		if err := rows.Scan(&t.CompanyID, &t.Tier, &t.Momentum, &t.Priority, &t.Policy, &hot, &changed, &updated); err != nil {
			return nil, err
		}
		t.HotSince, t.ChangedAt, t.UpdatedAt = parseNullTS(hot), parseTS(changed), parseTS(updated)
		out[t.CompanyID] = t
	}
	return out, rows.Err()
}

// Rescore recomputes the momentum, tier and scan priority of every company
// from stored events and assessments under the policy. It makes no model or
// network call and is idempotent.
func (r *Repo) Rescore(ctx context.Context, p Policy, keywords []string) ([]TierChange, error) {
	now := r.now()
	scored, err := r.scoredByCompany(ctx, p, now, 0)
	if err != nil {
		return nil, err
	}
	old, err := r.Tiers(ctx)
	if err != nil {
		return nil, err
	}
	ids := map[int64]bool{}
	for id := range scored {
		ids[id] = true
	}
	for id := range old {
		ids[id] = true
	}
	var changes []TierChange
	// One transaction writes every company's row.
	var writes [][]any
	for id := range ids {
		momentum := p.Momentum(scored[id])
		blocked, err := r.ActiveBlock(ctx, id, now)
		if err != nil {
			return nil, err
		}
		tier := p.TierOf(momentum, blocked)
		prio := scanPriority(momentum, scored[id], keywords)
		prev, had := old[id]
		from := TierCold
		if had {
			from = prev.Tier
		}
		hotSince, changedAt := prev.HotSince, prev.ChangedAt
		if !had || from != tier {
			changedAt = now
		}
		switch {
		case tier == TierHot && from != TierHot:
			hotSince = now
		case tier != TierHot:
			hotSince = time.Time{}
		}
		writes = append(writes, []any{id, tier, momentum, prio, p.Version, fmtNullTS(hotSince), fmtTS(changedAt), fmtTS(now)})
		if from != tier {
			c, err := r.Company(ctx, id)
			if err != nil {
				return nil, err
			}
			changes = append(changes, TierChange{CompanyID: id, Company: c.Name, From: from, To: tier, Momentum: momentum})
		}
	}
	if err := r.s.Write(ctx, func(tx *sql.Tx) error {
		for _, w := range writes {
			if _, err := tx.ExecContext(ctx, `INSERT INTO signal_tiers (company_id, tier, momentum, priority, policy, hot_since, changed_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (company_id) DO UPDATE SET tier = excluded.tier, momentum = excluded.momentum, priority = excluded.priority, policy = excluded.policy,
				hot_since = excluded.hot_since, changed_at = excluded.changed_at, updated_at = excluded.updated_at`, w...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].CompanyID < changes[j].CompanyID })
	return changes, nil
}

// ReplayRow is one company in a policy replay: its stored tier and the tier
// under the candidate policy.
type ReplayRow struct {
	CompanyID int64   `json:"companyId"`
	Company   string  `json:"company"`
	Before    string  `json:"before"`
	After     string  `json:"after"`
	Momentum  float64 `json:"momentum"`
}

// Replay computes the tiers a candidate policy would give, without changing
// anything stored.
func (r *Repo) Replay(ctx context.Context, p Policy) ([]ReplayRow, error) {
	now := r.now()
	scored, err := r.scoredByCompany(ctx, p, now, 0)
	if err != nil {
		return nil, err
	}
	old, err := r.Tiers(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.Read().QueryContext(ctx, `SELECT id, name FROM signal_companies ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var cs []Company
	for rows.Next() {
		var c Company
		if err := rows.Scan(&c.ID, &c.Name); err != nil {
			rows.Close()
			return nil, err
		}
		cs = append(cs, c)
	}
	rows.Close()
	var out []ReplayRow
	for _, c := range cs {
		blocked, err := r.ActiveBlock(ctx, c.ID, now)
		if err != nil {
			return nil, err
		}
		m := p.Momentum(scored[c.ID])
		before := TierCold
		if t, ok := old[c.ID]; ok {
			before = t.Tier
		}
		out = append(out, ReplayRow{CompanyID: c.ID, Company: c.Name, Before: before, After: p.TierOf(m, blocked), Momentum: m})
	}
	return out, nil
}

// DecomposeRow is one assessment's factors and contribution.
type DecomposeRow struct {
	EventID      int64    `json:"eventId"`
	EventType    string   `json:"eventType"`
	EventTime    string   `json:"eventTime"`
	Intent       string   `json:"intent"`
	Status       string   `json:"status"`
	Type         float64  `json:"typeWeight"`
	Strength     float64  `json:"strengthFactor"`
	Confidence   float64  `json:"confidence"`
	Source       float64  `json:"sourceFactor"`
	Decay        float64  `json:"timeDecay"`
	Contribution float64  `json:"contribution"`
	Weak         bool     `json:"weak"`
	Teams        []string `json:"teams"`
}

// Decompose lists the factors of every ok assessment of a company and its
// momentum.
func (r *Repo) Decompose(ctx context.Context, p Policy, companyID int64) ([]DecomposeRow, float64, error) {
	scored, err := r.scoredByCompany(ctx, p, r.now(), companyID)
	if err != nil {
		return nil, 0, err
	}
	var out []DecomposeRow
	for _, it := range scored[companyID] {
		f := it.Factors
		out = append(out, DecomposeRow{EventID: it.Assessment.EventID, EventType: it.Assessment.Type, EventTime: fmtTS(it.EventTime), Intent: it.Assessment.Intent,
			Status: it.Assessment.Status, Type: f.Type, Strength: f.Strength, Confidence: f.Confidence, Source: f.Source, Decay: f.Decay, Contribution: f.Contribution(),
			Weak: it.Assessment.Intent != IntentStrong && it.Assessment.Intent != IntentModerate, Teams: it.Assessment.Teams})
	}
	return out, p.Momentum(scored[companyID]), nil
}

func decodeStrings(s string) []string {
	var out []string
	_ = jsonUnmarshal(s, &out)
	return out
}
