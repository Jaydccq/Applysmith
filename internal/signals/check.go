package signals

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"applysmith/internal/discovery"
)

// Tier budgets and cadences for posting checks.
var (
	tierBudget  = map[string]int{TierHot: 5, TierWarm: 10, TierWatch: 20}
	tierCadence = map[string]time.Duration{TierHot: 24 * time.Hour, TierWarm: 72 * time.Hour, TierWatch: 168 * time.Hour}
	tierOrder   = []string{TierHot, TierWarm, TierWatch}
)

const checkWindow = 24 * time.Hour

// PostingRow is a posting seen on a company's board with its stored verdict.
type PostingRow struct {
	ID          int64
	CompanyID   int64
	ATS         string
	Board       string
	ExternalID  string
	URL         string
	Title       string
	Location    string
	Remote      bool
	Description string
	Verdict     string
	Reasons     []string
	FirstSeen   time.Time
	HandedOff   time.Time
	Handoff     string
	InboxID     int64
}

// RevisionKey identifies a priority revision: company, tier, momentum to the
// nearest 0.5, policy version and the 24-hour cooldown window.
func RevisionKey(company int64, tier string, momentum float64, policy string, at time.Time) string {
	window := at.Unix() / int64(checkWindow.Seconds())
	sum := sha256.Sum256(fmt.Appendf(nil, "%d|%s|%.1f|%s|%d", company, tier, math.Round(momentum*2)/2, policy, window))
	return hex.EncodeToString(sum[:16])
}

// RecordPosting stores a posting once per (ATS, board, external id), with the
// verdict of the gate at scan time.
func (r *Repo) RecordPosting(ctx context.Context, p PostingRow) (id int64, created bool, err error) {
	reasons, _ := json.Marshal(nonNil(p.Reasons))
	err = r.s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO signal_postings (company_id, ats, board, external_id, url, title, location, remote, description, verdict, reasons_json, first_seen_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (ats, board, external_id) DO NOTHING`,
			p.CompanyID, p.ATS, p.Board, p.ExternalID, p.URL, p.Title, p.Location, b2i(p.Remote), capRunes(p.Description, 20000), p.Verdict, string(reasons), r.stamp())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			id, _ = res.LastInsertId()
			created = true
			return nil
		}
		return tx.QueryRowContext(ctx, `SELECT id FROM signal_postings WHERE ats = ? AND board = ? AND external_id = ?`, p.ATS, p.Board, p.ExternalID).Scan(&id)
	})
	return id, created, err
}

const postingColumns = `id, company_id, ats, board, external_id, url, title, location, remote, description, verdict, reasons_json, first_seen_at, handed_off_at, handoff, COALESCE(inbox_id, 0)`

func scanPosting(row interface{ Scan(...any) error }) (PostingRow, error) {
	var p PostingRow
	var remote int
	var reasons, first string
	var handed sql.NullString
	if err := row.Scan(&p.ID, &p.CompanyID, &p.ATS, &p.Board, &p.ExternalID, &p.URL, &p.Title, &p.Location, &remote, &p.Description, &p.Verdict, &reasons, &first, &handed, &p.Handoff, &p.InboxID); err != nil {
		return p, err
	}
	p.Remote, p.FirstSeen, p.HandedOff = remote == 1, parseTS(first), parseNullTS(handed)
	_ = json.Unmarshal([]byte(reasons), &p.Reasons)
	return p, nil
}

// Postings lists the postings seen for a company, newest first.
func (r *Repo) Postings(ctx context.Context, companyID int64, passOnly bool) ([]PostingRow, error) {
	q := `SELECT ` + postingColumns + ` FROM signal_postings WHERE company_id = ?`
	if passOnly {
		q += ` AND verdict = 'pass'`
	}
	rows, err := r.s.Read().QueryContext(ctx, q+` ORDER BY id DESC`, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PostingRow
	for rows.Next() {
		p, err := scanPosting(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type dueCompany struct {
	Company  Company
	Tier     string
	Momentum float64
	Priority float64
}

// dueCompanies lists the admitted companies whose last check is at least a
// cadence old, by tier, in scan-priority order. noRole counts companies whose
// observed teams match none of the target roles.
func (r *Repo) dueCompanies(ctx context.Context, now time.Time) (due map[string][]dueCompany, noRole int, err error) {
	rows, err := r.s.Read().QueryContext(ctx, `SELECT c.id, c.name, c.canon, c.state, t.tier, t.momentum, t.priority,
		COALESCE((SELECT max(k.checked_at) FROM signal_checks k WHERE k.company_id = c.id AND k.status IN ('ok', 'failed')), '')
		FROM signal_tiers t JOIN signal_companies c ON c.id = t.company_id WHERE t.tier <> 'cold' AND c.state <> 'dismissed'`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	due = map[string][]dueCompany{}
	for rows.Next() {
		var d dueCompany
		var last string
		if err := rows.Scan(&d.Company.ID, &d.Company.Name, &d.Company.Canon, &d.Company.State, &d.Tier, &d.Momentum, &d.Priority, &last); err != nil {
			return nil, 0, err
		}
		if last != "" && now.Sub(parseTS(last)) < tierCadence[d.Tier] {
			continue
		}
		if d.Priority <= 0 {
			noRole++
			continue
		}
		due[d.Tier] = append(due[d.Tier], d)
	}
	for _, l := range due {
		sort.SliceStable(l, func(i, j int) bool {
			if l[i].Priority != l[j].Priority {
				return l[i].Priority > l[j].Priority
			}
			if l[i].Momentum != l[j].Momentum {
				return l[i].Momentum > l[j].Momentum
			}
			return l[i].Company.ID < l[j].Company.ID
		})
	}
	return due, noRole, rows.Err()
}

// beginCheck claims a revision or resumes one left unfinished by another tick.
// Completed revisions and a repeated claim by the same tick are refused.
func (r *Repo) beginCheck(ctx context.Context, d dueCompany, key string, tickID int64) (bool, error) {
	ok := false
	err := r.s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO signal_checks (company_id, revision_key, tier, momentum, tick_id, checked_at) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (revision_key) DO UPDATE SET tick_id = excluded.tick_id, checked_at = excluded.checked_at, detail = ''
			WHERE signal_checks.status = 'started' AND signal_checks.tick_id IS NOT excluded.tick_id`,
			d.Company.ID, key, d.Tier, d.Momentum, tickID, r.stamp())
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		ok = n > 0
		return nil
	})
	return ok, err
}

func (r *Repo) endCheck(ctx context.Context, key, status, detail string) error {
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE signal_checks SET status = ?, detail = ? WHERE revision_key = ?`, status, detail, key)
		return err
	})
}

// auditDeferral is the audit kind of a company a tier's budget left over.
const auditDeferral = "deferred"

// auditDeferred writes one audit line per company a tier's budget left for a
// later tick, in one transaction.
func (r *Repo) auditDeferred(ctx context.Context, tier string, budget int, over []dueCompany) error {
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		at := r.stamp()
		for _, d := range over {
			if err := auditTx(ctx, tx, at, auditDeferral, d.Company.ID, fmt.Sprintf("%s: scan budget %d reached; priority %.3f", tier, budget, d.Priority)); err != nil {
				return err
			}
		}
		return nil
	})
}

// PruneDeferrals deletes the deferral lines of the audit log older than the
// retention window: a tick writes one per company its budget left over, and
// only the recent ones say anything about the queue. Every other audit line is
// kept.
func (r *Repo) PruneDeferrals(ctx context.Context) (int, error) {
	n := 0
	err := r.s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM signal_audit WHERE kind = ? AND at < ?`, auditDeferral, fmtTS(r.now().Add(-BodyRetention)))
		if err != nil {
			return err
		}
		c, _ := res.RowsAffected()
		n = int(c)
		return nil
	})
	return n, err
}

// CheckStats counts the posting-check stage.
type CheckStats struct {
	Scanned       int  `json:"scanned"`
	Deferred      int  `json:"deferred"`
	AlreadyDone   int  `json:"alreadyChecked"`
	NoRoleMatch   int  `json:"noRoleMatch"`
	Failed        int  `json:"failed"`
	Seen          int  `json:"postingsSeen"`
	Passed        int  `json:"postingsPassed"`
	NoATS         int  `json:"noATS"`
	BudgetReached bool `json:"budgetReached"`
}

// checkDue admits companies by tier under the per-tick budgets, reads their
// boards and records every posting with its gate verdict. Work not reached by
// the deadline stays due.
func (s *Service) checkDue(ctx context.Context, tickID int64, st Settings, deadline time.Time) (CheckStats, error) {
	var stats CheckStats
	now := s.now()
	due, noRole, err := s.repo.dueCompanies(ctx, now)
	if err != nil {
		return stats, err
	}
	stats.NoRoleMatch = noRole
	gater := NewGater(st.Profile)
	searches := searchesPerTick
	for _, tier := range tierOrder {
		list := due[tier]
		budget := tierBudget[tier]
		if len(list) > budget {
			over := list[budget:]
			stats.Deferred += len(over)
			if err := s.repo.auditDeferred(ctx, tier, budget, over); err != nil {
				return stats, err
			}
			list = list[:budget]
		}
		for _, d := range list {
			if ctx.Err() != nil {
				return stats, ctx.Err()
			}
			if !s.now().Before(deadline) {
				stats.BudgetReached = true
				return stats, nil
			}
			key := RevisionKey(d.Company.ID, d.Tier, d.Momentum, s.policy.Version, now)
			fresh, err := s.repo.beginCheck(ctx, d, key, tickID)
			if err != nil {
				return stats, err
			}
			if !fresh {
				stats.AlreadyDone++
				continue
			}
			stats.Scanned++
			status, detail := "ok", ""
			if err := s.checkCompany(ctx, d.Company, st, gater, &searches, &stats); err != nil {
				if ctx.Err() != nil {
					return stats, ctx.Err()
				}
				stats.Failed++
				status, detail = "failed", err.Error()
				s.log.Warn("a posting check failed", "company", d.Company.ID, "error", err.Error())
			}
			if err := s.repo.endCheck(ctx, key, status, detail); err != nil {
				return stats, err
			}
		}
	}
	return stats, nil
}

func (s *Service) checkCompany(ctx context.Context, c Company, st Settings, gater *Gater, searches *int, stats *CheckStats) error {
	id, err := s.ensureIdentity(ctx, c, st, searches)
	if err != nil {
		return err
	}
	if id.Status != IdentityResolved {
		stats.NoATS++
		return s.noATSLead(ctx, c, id)
	}
	var posts []discovery.Posting
	if id.ATS == genericATS {
		page, ok := s.getPage(ctx, id.Slug)
		if !ok {
			return errors.New("the careers page could not be read")
		}
		_, parsed := ReadCareers(page, id.Slug)
		for _, p := range parsed {
			posts = append(posts, discovery.Posting{ATS: genericATS, Board: id.Slug, JobID: p.ID, URL: p.URL, Title: p.Title, Location: p.Location, Remote: p.Remote, Description: p.Description})
		}
	} else {
		br, err := s.discoveryClient().FetchBoard(ctx, s.d.Endpoints.ATS, id.Board(), "")
		if err != nil {
			var bf *discovery.BoardFailure
			if errors.As(err, &bf) && bf.Reason == "empty posting list" {
				return nil
			}
			return err
		}
		posts = br.Postings
	}
	client := s.discoveryClient()
	for _, p := range posts {
		ext := p.JobID
		if ext == "" {
			ext = p.URL
		}
		reasons := gater.Check(p.Title, p.Location, p.Description)
		if gater.NeedsSponsorship() && p.Description == "" && p.ATS == discovery.Greenhouse && len(reasons) == 0 {
			if text, err := client.FetchDescription(ctx, s.d.Endpoints.ATS, p); err == nil {
				p.Description = text
				reasons = gater.Check(p.Title, p.Location, p.Description)
			}
		}
		verdict := "pass"
		if len(reasons) > 0 {
			verdict = "fail"
		}
		_, created, err := s.repo.RecordPosting(ctx, PostingRow{CompanyID: c.ID, ATS: id.ATS, Board: id.Slug, ExternalID: ext, URL: p.URL, Title: p.Title,
			Location: p.Location, Remote: p.Remote, Description: p.Description, Verdict: verdict, Reasons: reasons})
		if err != nil {
			return err
		}
		if created {
			stats.Seen++
			if verdict == "pass" {
				stats.Passed++
			}
		}
	}
	return nil
}
