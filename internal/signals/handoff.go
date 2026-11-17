package signals

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"applysmith/internal/discovery"
)

// Lead categories the handoff writes.
const (
	LeadPosting     = "posting"
	LeadNoATS       = "no-ats"
	LeadCareersPage = "careers-page"
	evidenceMax     = 4000
)

// Evidence is the evidence block of a lead: always written, either the latest
// source document or an explicit reason it is absent.
type Evidence struct {
	URL          string
	Title        string
	Body         string
	Adapter      string
	Date         time.Time
	AbsentReason string
}

// Lead is what the handoff gives the leads store.
type Lead struct {
	Source      string
	Category    string
	Company     string
	Role        string
	Location    string
	ApplyLink   string
	Description string
	Evidence    Evidence
	// InboxID is the inbox row of a posting lead.
	InboxID int64
}

// LeadSink is the leads store of job discovery. It de-duplicates on its own
// keys; the handoff additionally writes each review lead once.
type LeadSink interface {
	AddLead(ctx context.Context, l Lead) error
}

// evidenceOf is the latest source document of a company.
func (s *Service) evidenceOf(ctx context.Context, companyID int64) (Evidence, error) {
	docs, err := s.repo.CompanyDocs(ctx, companyID, 1)
	if err != nil {
		return Evidence{}, err
	}
	if len(docs) == 0 {
		return Evidence{AbsentReason: "no source document is stored for this company"}, nil
	}
	d := docs[0]
	if d.BodyDropped {
		return Evidence{URL: d.URL, Title: d.Title, Adapter: d.Adapter, Date: d.EventTime(), AbsentReason: "the document body was reduced to metadata by retention"}, nil
	}
	ev := Evidence{URL: d.URL, Title: d.Title, Body: capRunes(d.Body, evidenceMax), Adapter: d.Adapter, Date: d.EventTime()}
	if strings.TrimSpace(ev.Body) == "" {
		ev.AbsentReason = "the source document has no body text"
	}
	return ev, nil
}

// noATSLead stores one review lead for a scan-worthy company with no board:
// with the careers page when one was reached, else with no apply link. It is
// written once per company and evidence link.
func (s *Service) noATSLead(ctx context.Context, c Company, id Identity) error {
	if s.d.Leads == nil {
		return nil
	}
	ev, err := s.evidenceOf(ctx, c.ID)
	if err != nil {
		return err
	}
	l := Lead{Source: "signals", Category: LeadNoATS, Company: c.Name, Evidence: ev}
	if id.CareersURL != "" {
		l.Category, l.ApplyLink = LeadCareersPage, id.CareersURL
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%d|%s", c.ID, ev.URL))
	key := hex.EncodeToString(sum[:16])
	var done bool
	if err := s.d.Store.Read().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM signal_lead_keys WHERE key = ?)`, key).Scan(&done); err != nil {
		return err
	}
	if done {
		return nil
	}
	// The sink de-duplicates durable leads. Record completion only afterwards:
	// interruption between these writes retries the lead without certifying work
	// that was never stored. Tick's single active owner serializes these calls.
	if err := s.d.Leads.AddLead(ctx, l); err != nil {
		return err
	}
	return s.d.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO signal_lead_keys (key, company_id, category, created_at) VALUES (?, ?, ?, ?) ON CONFLICT (key) DO NOTHING`, key, c.ID, l.Category, s.repo.stamp())
		return err
	})
}

// HandoffStats counts the handoff stage.
type HandoffStats struct {
	Inserted   int `json:"inserted"`
	Duplicates int `json:"duplicates"`
	Blocked    int `json:"blocked"`
	Invalid    int `json:"invalid"`
	Failed     int `json:"failed"`
}

// handoffPostings inserts each gate-passing posting not yet handed off into the
// inbox, stores its posting lead and marks it handed off, so a posting is
// never handed off twice.
func (s *Service) handoffPostings(ctx context.Context, st Settings) (HandoffStats, error) {
	var stats HandoffStats
	rows, err := s.d.Store.Read().QueryContext(ctx, `SELECT `+postingColumns+` FROM signal_postings p
		WHERE p.verdict = 'pass' AND p.handed_off_at IS NULL AND p.company_id IN (SELECT id FROM signal_companies WHERE state <> 'dismissed') ORDER BY p.id`)
	if err != nil {
		return stats, err
	}
	var list []PostingRow
	for rows.Next() {
		p, err := scanPosting(rows)
		if err != nil {
			rows.Close()
			return stats, err
		}
		list = append(list, p)
	}
	rows.Close()
	if len(list) == 0 {
		return stats, nil
	}
	// One reading of the inbox, the tracker and the scan history serves the
	// whole batch.
	adm, err := s.disc.NewAdmitter(ctx, st.Portals, st.Profile)
	if err != nil {
		return stats, err
	}
	for _, p := range list {
		if ctx.Err() != nil {
			return stats, ctx.Err()
		}
		if err := s.handoffOne(ctx, adm, p, &stats); err != nil {
			if ctx.Err() != nil {
				return stats, ctx.Err()
			}
			stats.Failed++
			s.log.Warn("a posting could not be handed off", "posting", p.ID, "error", err.Error())
		}
	}
	return stats, nil
}

func (s *Service) handoffOne(ctx context.Context, admitter *discovery.Admitter, p PostingRow, stats *HandoffStats) error {
	c, err := s.repo.Company(ctx, p.CompanyID)
	if err != nil {
		return err
	}
	ev, err := s.evidenceOf(ctx, c.ID)
	if err != nil {
		return err
	}
	dp := discovery.Posting{Source: "signals", ATS: p.ATS, Board: p.Board, Company: c.Name, Title: p.Title, Location: p.Location, Remote: p.Remote,
		URL: p.URL, JobID: p.ExternalID, Description: p.Description}
	if p.ATS == genericATS {
		dp.ATS, dp.Board, dp.JobID = "", "", ""
	}
	detail := "signal: " + c.Name
	if ev.URL != "" {
		detail += "; evidence " + ev.URL
	}
	outcome, inbox := "", p.InboxID
	if inbox == 0 {
		adm, err := admitter.Admit(ctx, dp, detail)
		if err != nil {
			return err
		}
		switch {
		case adm.Row != nil:
			stats.Inserted++
			inbox = adm.Row.ID
			// Remember the row before the lead is stored, so a lead that fails
			// is retried without the inbox calling the posting a duplicate.
			if err := s.setInbox(ctx, p.ID, inbox); err != nil {
				return err
			}
		case adm.Blocked:
			stats.Blocked++
			outcome = "blocked"
		case adm.Duplicate != "":
			stats.Duplicates++
			outcome = "duplicate:" + adm.Duplicate
		default:
			stats.Invalid++
			outcome = "invalid:" + adm.Invalid
		}
	}
	if inbox != 0 {
		outcome = "inbox"
		if s.d.Leads != nil {
			if err := s.d.Leads.AddLead(ctx, Lead{Source: "signals", Category: LeadPosting, Company: c.Name, Role: p.Title, Location: p.Location,
				ApplyLink: p.URL, Description: p.Description, Evidence: ev, InboxID: inbox}); err != nil {
				return err
			}
		}
	}
	return s.d.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE signal_postings SET handed_off_at = ?, handoff = ? WHERE id = ? AND handed_off_at IS NULL`, s.repo.stamp(), outcome, p.ID)
		return err
	})
}

func (s *Service) setInbox(ctx context.Context, postingID, inboxID int64) error {
	return s.d.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE signal_postings SET inbox_id = ? WHERE id = ?`, inboxID, postingID)
		return err
	})
}

// ErrNoEnqueuer is returned when this server cannot start evaluations.
var ErrNoEnqueuer = errors.New("signals: evaluations cannot be started here")

// EvaluatePostings enqueues the handed-off, still pending inbox rows of a
// company for evaluation, at most 10 per call, in deep mode.
func (s *Service) EvaluatePostings(ctx context.Context, companyID int64) (queued []int64, err error) {
	if s.d.Enqueuer == nil {
		return nil, ErrNoEnqueuer
	}
	if _, err := s.repo.Company(ctx, companyID); err != nil {
		return nil, err
	}
	posts, err := s.repo.Postings(ctx, companyID, true)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for i := len(posts) - 1; i >= 0 && len(ids) < 10; i-- {
		if posts[i].InboxID == 0 {
			continue
		}
		row, err := s.disc.InboxGet(ctx, posts[i].InboxID)
		if err != nil || row.Status != discovery.InboxPending {
			continue
		}
		ids = append(ids, posts[i].InboxID)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	queued, _, err = s.disc.Promote(ctx, s.d.Enqueuer, nil, ids...)
	return queued, err
}
