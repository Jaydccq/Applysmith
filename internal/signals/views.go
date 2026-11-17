package signals

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ListFilter filters the signal list.
type ListFilter struct {
	Tier   string
	Source string
	State  string
	From   time.Time
	To     time.Time
	// CompanyID, when set, reads that company's row alone.
	CompanyID int64
}

// ListRow is one company in the signal list.
type ListRow struct {
	CompanyID int64
	Company   string
	Tier      string
	Momentum  float64
	Priority  float64
	State     string
	Blocked   bool
	Sources   []string
	Newest    time.Time
}

// List returns the companies that have a tier, hottest first.
func (s *Service) List(ctx context.Context, f ListFilter) ([]ListRow, error) {
	where, args := "", []any{fmtTS(s.now())}
	if f.CompanyID != 0 {
		where, args = ` WHERE c.id = ?`, append(args, f.CompanyID)
	}
	rows, err := s.d.Store.Read().QueryContext(ctx, `SELECT c.id, c.name, c.state, t.tier, t.momentum, t.priority,
		COALESCE((SELECT group_concat(DISTINCT d.adapter) FROM signal_event_docs ed JOIN signal_documents d ON d.id = ed.document_id WHERE ed.company_id = c.id), ''),
		COALESCE((SELECT max(COALESCE(d.published_at, d.fetched_at)) FROM signal_event_docs ed JOIN signal_documents d ON d.id = ed.document_id WHERE ed.company_id = c.id), ''),
		EXISTS (SELECT 1 FROM signal_blocks b WHERE b.company_id = c.id AND b.lifted_at IS NULL AND b.expires_at > ?)
		FROM signal_tiers t JOIN signal_companies c ON c.id = t.company_id`+where+` ORDER BY t.momentum DESC, c.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ListRow
	for rows.Next() {
		var r ListRow
		var ads, newest string
		var blocked int
		if err := rows.Scan(&r.CompanyID, &r.Company, &r.State, &r.Tier, &r.Momentum, &r.Priority, &ads, &newest, &blocked); err != nil {
			return nil, err
		}
		r.Blocked, r.Newest = blocked == 1, parseTS(newest)
		if ads != "" {
			r.Sources = strings.Split(ads, ",")
		}
		switch {
		case f.Tier != "" && r.Tier != f.Tier, f.State != "" && r.State != f.State:
			continue
		case f.Source != "" && !hasSource(r.Sources, f.Source):
			continue
		case !f.From.IsZero() && r.Newest.Before(f.From), !f.To.IsZero() && r.Newest.After(f.To):
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func hasSource(adapters []string, want string) bool {
	want = strings.TrimPrefix(want, "signals-")
	for _, a := range adapters {
		if a == want {
			return true
		}
	}
	return false
}

// EventView is an event with its documents and assessment.
type EventView struct {
	Event      Event
	Documents  []StoredDoc
	Assessment *Assessment
	Held       bool
}

// Detail is the evidence chain of one company.
type Detail struct {
	Company       Company
	Tier          TierRow
	Blocked       bool
	BlockExpires  time.Time
	Events        []EventView
	Decomposition []DecomposeRow
	Momentum      float64
	Identity      *Identity
	Postings      []PostingRow
	Outcomes      []OutcomeRow
	SearchLinks   []string
}

// OutcomeRow is one outcome of the ledger.
type OutcomeRow struct {
	EventID  int64
	Type     string
	Status   string
	LeadDays int
	End      time.Time
}

// SearchLinks builds the two web-search URLs of a company without any network
// request: one for recruiters or talent acquisition, one for engineering
// managers, each limited to a professional network's profile pages.
func SearchLinks(company string) []string {
	build := func(roles string) string {
		q := `site:linkedin.com/in "` + company + `" (` + roles + `)`
		return "https://www.google.com/search?q=" + url.QueryEscape(q)
	}
	return []string{build(`recruiter OR "talent acquisition"`), build(`"engineering manager" OR "VP of engineering" OR "head of engineering"`)}
}

// Detail reads the evidence chain, decomposition, identity, postings and
// outcomes of a company.
func (s *Service) Detail(ctx context.Context, companyID int64) (Detail, error) {
	c, err := s.repo.Company(ctx, companyID)
	if err != nil {
		return Detail{}, err
	}
	d := Detail{Company: c}
	if t, ok, err := s.repo.Tier(ctx, companyID); err != nil {
		return d, err
	} else if ok {
		d.Tier = t
	}
	now := s.now()
	var exp string
	if err := s.d.Store.Read().QueryRowContext(ctx, `SELECT COALESCE(max(expires_at), '') FROM signal_blocks WHERE company_id = ? AND lifted_at IS NULL AND expires_at > ?`, companyID, fmtTS(now)).Scan(&exp); err != nil {
		return d, err
	}
	d.Blocked, d.BlockExpires = exp != "", parseTS(exp)
	events, err := s.repo.CompanyEvents(ctx, companyID)
	if err != nil {
		return d, err
	}
	for _, e := range events {
		ev := EventView{Event: e}
		if ev.Documents, err = s.repo.EventDocs(ctx, e.ID); err != nil {
			return d, err
		}
		var aid int64
		switch err := s.d.Store.Read().QueryRowContext(ctx, `SELECT id FROM signal_assessments WHERE event_id = ? AND rubric_version = ?`, e.ID, RubricVersion).Scan(&aid); {
		case err == nil:
			a, err := s.repo.Assessment(ctx, aid)
			if err != nil {
				return d, err
			}
			ev.Assessment, ev.Held = &a, a.Status != StatusOK
		case !errors.Is(err, sql.ErrNoRows):
			return d, err
		}
		d.Events = append(d.Events, ev)
	}
	if d.Decomposition, d.Momentum, err = s.repo.Decompose(ctx, s.policy, companyID); err != nil {
		return d, err
	}
	if id, ok, err := s.repo.Identity(ctx, companyID); err != nil {
		return d, err
	} else if ok {
		d.Identity = &id
	}
	if d.Postings, err = s.repo.Postings(ctx, companyID, false); err != nil {
		return d, err
	}
	if d.Outcomes, err = s.repo.CompanyOutcomes(ctx, companyID); err != nil {
		return d, err
	}
	pass := false
	for _, p := range d.Postings {
		pass = pass || p.Verdict == "pass"
	}
	if !pass {
		d.SearchLinks = SearchLinks(c.Name)
	}
	return d, nil
}

// CompanyEvents lists the events of a company, newest first.
func (r *Repo) CompanyEvents(ctx context.Context, companyID int64) ([]Event, error) {
	rows, err := r.s.Read().QueryContext(ctx, `SELECT id, company_id, type, event_time, amount FROM signal_events WHERE company_id = ? ORDER BY event_time DESC, id DESC`, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CompanyOutcomes lists a company's outcomes.
func (r *Repo) CompanyOutcomes(ctx context.Context, companyID int64) ([]OutcomeRow, error) {
	rows, err := r.s.Read().QueryContext(ctx, `SELECT event_id, event_type, status, COALESCE(lead_days, 0), window_end FROM signal_outcomes WHERE company_id = ? ORDER BY event_id`, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutcomeRow
	for rows.Next() {
		var o OutcomeRow
		var end string
		if err := rows.Scan(&o.EventID, &o.Type, &o.Status, &o.LeadDays, &end); err != nil {
			return nil, err
		}
		o.End = parseTS(end)
		out = append(out, o)
	}
	return out, rows.Err()
}

// ---- review queue

// ReviewItem is one held assessment waiting for the user.
type ReviewItem struct {
	ID         int64
	Assessment Assessment
	CompanyID  int64
	Company    string
	Reason     string
	State      string
	CreatedAt  time.Time
}

// Review lists the review queue; open items only unless all is set.
func (s *Service) Review(ctx context.Context, all bool) ([]ReviewItem, error) {
	q := `SELECT v.id, v.reason, v.state, v.created_at, e.company_id, c.name, ` + "a." + strings.ReplaceAll(assessmentColumns, ", ", ", a.") + `
		FROM signal_review v JOIN signal_assessments a ON a.id = v.assessment_id JOIN signal_events e ON e.id = a.event_id JOIN signal_companies c ON c.id = e.company_id`
	if !all {
		q += ` WHERE v.state = 'open'`
	}
	rows, err := s.d.Store.Read().QueryContext(ctx, q+` ORDER BY v.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReviewItem
	for rows.Next() {
		var it ReviewItem
		var a Assessment
		var created, teams, quotes, acreated string
		var growth sql.NullInt64
		if err := rows.Scan(&it.ID, &it.Reason, &it.State, &created, &it.CompanyID, &it.Company, &a.ID, &a.EventID, &a.Rubric, &a.Status, &a.Type, &a.Intent, &teams,
			&growth, &a.Scale, &a.Strength, &a.Confidence, &a.Reasoning, &quotes, &acreated); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(teams), &a.Teams)
		_ = json.Unmarshal([]byte(quotes), &a.Quotes)
		if growth.Valid {
			g := growth.Int64 == 1
			a.Growth = &g
		}
		it.Assessment, it.CreatedAt = a, parseTS(created)
		out = append(out, it)
	}
	return out, rows.Err()
}

// Decide approves or rejects a review item. Approving makes the assessment
// count (and so can trigger a scan); rejecting leaves it with no effect.
func (s *Service) Decide(ctx context.Context, id int64, approve bool) (ReviewItem, error) {
	err := s.d.Store.Write(ctx, func(tx *sql.Tx) error {
		var aid int64
		var state string
		if err := tx.QueryRowContext(ctx, `SELECT assessment_id, state FROM signal_review WHERE id = ?`, id).Scan(&aid, &state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if state != "open" {
			return ErrDecided
		}
		next := "rejected"
		if approve {
			next = "approved"
			if _, err := tx.ExecContext(ctx, `UPDATE signal_assessments SET status = 'ok' WHERE id = ?`, aid); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE signal_review SET state = ?, decided_at = ? WHERE id = ?`, next, s.repo.stamp(), id); err != nil {
			return err
		}
		return auditTx(ctx, tx, s.repo.stamp(), "review-"+next, 0, "review item "+itoa(id))
	})
	if err != nil {
		return ReviewItem{}, err
	}
	if approve {
		if err := s.Rescore(ctx); err != nil {
			return ReviewItem{}, err
		}
	}
	return s.reviewItem(ctx, id)
}

// Reopen puts a rejected item back in the queue, the undo of the rejection. A
// rejection changes nothing but the item, so nothing else is put back; the
// audit keeps both lines.
func (s *Service) Reopen(ctx context.Context, id int64) (ReviewItem, error) {
	err := s.d.Store.Write(ctx, func(tx *sql.Tx) error {
		var state string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM signal_review WHERE id = ?`, id).Scan(&state); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if state != "rejected" {
			return fmt.Errorf("%w: the review item is %s, and only a rejected one is put back", ErrNotUndoable, state)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE signal_review SET state = 'open', decided_at = NULL WHERE id = ?`, id); err != nil {
			return err
		}
		return auditTx(ctx, tx, s.repo.stamp(), "review-reopened", 0, "review item "+itoa(id))
	})
	if err != nil {
		return ReviewItem{}, err
	}
	return s.reviewItem(ctx, id)
}

func (s *Service) reviewItem(ctx context.Context, id int64) (ReviewItem, error) {
	items, err := s.Review(ctx, true)
	if err != nil {
		return ReviewItem{}, err
	}
	for _, it := range items {
		if it.ID == id {
			return it, nil
		}
	}
	return ReviewItem{}, ErrNotFound
}

// ErrDecided is returned when a review item was decided already.
var ErrDecided = errors.New("signals: the review item was decided already")

// ErrNotUndoable is returned when an undo finds its item no longer as the press
// left it: undone already, or changed since.
var ErrNotUndoable = errors.New("signals: nothing to undo")

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// Rescore recomputes every company's momentum and tier from stored events and
// assessments under the current policy. It makes no model or network call.
func (s *Service) Rescore(ctx context.Context) error {
	st, err := s.settings(ctx)
	if err != nil {
		return err
	}
	_, err = s.rescore(ctx, st)
	return err
}

// Watch, Dismiss, Restore and LiftBlock are the user's actions on a company.
func (s *Service) Watch(ctx context.Context, id int64) error {
	return s.repo.SetState(ctx, id, StateWatching)
}
func (s *Service) Dismiss(ctx context.Context, id int64) error {
	return s.repo.SetState(ctx, id, StateDismissed)
}

// Restore undoes a dismissal: the company takes back the state it had before.
// Every state change writes a state line to the audit in its transaction, so
// that state is the newest line that is not a dismissal, or new when there is
// none.
func (s *Service) Restore(ctx context.Context, id int64) error {
	return s.d.Store.Write(ctx, func(tx *sql.Tx) error {
		var state string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM signal_companies WHERE id = ?`, id).Scan(&state); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if state != StateDismissed {
			return fmt.Errorf("%w: the company is %s, and only a dismissed one is restored", ErrNotUndoable, state)
		}
		before := StateNew
		err := tx.QueryRowContext(ctx, `SELECT detail FROM signal_audit WHERE kind = 'state' AND company_id = ? AND detail <> ? ORDER BY id DESC LIMIT 1`,
			id, StateDismissed).Scan(&before)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE signal_companies SET state = ? WHERE id = ?`, before, id); err != nil {
			return err
		}
		return auditTx(ctx, tx, s.repo.stamp(), "state", id, before)
	})
}

// LiftBlock ends the hiring block of a company, then rescored.
func (s *Service) LiftBlock(ctx context.Context, id int64) error {
	if _, err := s.repo.Company(ctx, id); err != nil {
		return err
	}
	if _, err := s.repo.LiftBlock(ctx, id); err != nil {
		return err
	}
	return s.Rescore(ctx)
}

// Status is what `applysmith signals status` and the page header show.
type Status struct {
	LastTick   *TickSummary
	Active     int64
	Outcomes   OutcomeStats
	Tiers      map[string]int
	ReviewOpen int
	Lanes      []LaneRow
}

// LaneRow is the health of one source.
type LaneRow struct {
	Source       string
	Enabled      bool
	LastProduced time.Time
	LastStatus   string
}

// Status reads the last tick, the outcome ledger and the lanes.
func (s *Service) Status(ctx context.Context) (Status, error) {
	st := Status{Tiers: map[string]int{}}
	var raw, status string
	var id int64
	switch err := s.d.Store.Read().QueryRowContext(ctx, `SELECT id, status, summary_json FROM signal_ticks ORDER BY id DESC LIMIT 1`).Scan(&id, &status, &raw); {
	case err == nil:
		var sum TickSummary
		if json.Unmarshal([]byte(raw), &sum) == nil {
			sum.TickID, sum.Status = id, status
			st.LastTick = &sum
		}
		if status == TickRunning {
			st.Active = id
		}
	case !errors.Is(err, sql.ErrNoRows):
		return st, err
	}
	var err error
	if st.Outcomes, err = s.repo.OutcomeStats(ctx); err != nil {
		return st, err
	}
	rows, err := s.d.Store.Read().QueryContext(ctx, `SELECT tier, count(*) FROM signal_tiers GROUP BY tier`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var t string
		var n int
		if err := rows.Scan(&t, &n); err != nil {
			rows.Close()
			return st, err
		}
		st.Tiers[t] = n
	}
	rows.Close()
	if err := s.d.Store.Read().QueryRowContext(ctx, `SELECT count(*) FROM signal_review WHERE state = 'open'`).Scan(&st.ReviewOpen); err != nil {
		return st, err
	}
	cfg, err := s.settings(ctx)
	if err != nil {
		return st, err
	}
	for _, src := range SourceIDs {
		last, err := s.repo.LastProduced(ctx, src)
		if err != nil {
			return st, err
		}
		var ls string
		_ = s.d.Store.Read().QueryRowContext(ctx, `SELECT last_status FROM signal_lanes WHERE source_id = ?`, src).Scan(&ls)
		st.Lanes = append(st.Lanes, LaneRow{Source: src, Enabled: SourceEnabled(cfg.Portals, src), LastProduced: last, LastStatus: ls})
	}
	return st, nil
}

// ActiveTick is the id of the tick that is running, zero when none is.
func (s *Service) ActiveTick(ctx context.Context) (int64, error) {
	var id int64
	var beat string
	err := s.d.Store.Read().QueryRowContext(ctx, `SELECT id, heartbeat_at FROM signal_ticks WHERE status = 'running'`).Scan(&id, &beat)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && s.now().Sub(parseTS(beat)) >= staleTick) {
		return 0, nil
	}
	return id, err
}

// FindCompany finds a company by name without creating it.
func (r *Repo) FindCompany(ctx context.Context, name string) (Company, error) {
	var c Company
	canon := RuleCanon(name)
	err := r.s.Read().QueryRowContext(ctx, `SELECT id, name, canon, state FROM signal_companies WHERE canon = ? OR lower(name) = lower(?)`, canon, strings.TrimSpace(name)).Scan(&c.ID, &c.Name, &c.Canon, &c.State)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}
