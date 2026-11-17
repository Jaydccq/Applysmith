package signals

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"applysmith/internal/store"
)

// ErrNotFound is returned when a company, assessment or review item does not exist.
var ErrNotFound = errors.New("signals: not found")

// Repo reads and writes the signal_ tables.
type Repo struct {
	s   *store.Store
	now func() time.Time
}

// NewRepo returns a repository over an open store; now defaults to the wall clock.
func NewRepo(s *store.Store, now func() time.Time) *Repo {
	if now == nil {
		now = time.Now
	}
	return &Repo{s: s, now: now}
}

const timeLayout = time.RFC3339

func (r *Repo) stamp() string { return fmtTS(r.now()) }

func fmtTS(t time.Time) string { return t.UTC().Format(timeLayout) }

func fmtNullTS(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return fmtTS(t)
}

func parseTS(s string) time.Time {
	t, _ := time.Parse(timeLayout, s)
	return t
}

func parseNullTS(s sql.NullString) time.Time {
	if !s.Valid {
		return time.Time{}
	}
	return parseTS(s.String)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Audit appends one line to the audit log.
func (r *Repo) Audit(ctx context.Context, kind string, companyID int64, detail string) error {
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		return auditTx(ctx, tx, r.stamp(), kind, companyID, detail)
	})
}

func auditTx(ctx context.Context, tx *sql.Tx, at, kind string, companyID int64, detail string) error {
	var cid any
	if companyID != 0 {
		cid = companyID
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO signal_audit (at, kind, company_id, detail) VALUES (?, ?, ?, ?)`, at, kind, cid, detail)
	return err
}

// AuditLine is one audit record.
type AuditLine struct {
	At        time.Time
	Kind      string
	CompanyID int64
	Detail    string
}

// AuditList returns the newest audit lines of a kind, newest first.
func (r *Repo) AuditList(ctx context.Context, kind string, limit int) ([]AuditLine, error) {
	rows, err := r.s.Read().QueryContext(ctx, `SELECT at, kind, COALESCE(company_id, 0), detail FROM signal_audit WHERE kind = ? ORDER BY id DESC LIMIT ?`, kind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditLine
	for rows.Next() {
		var l AuditLine
		var at string
		if err := rows.Scan(&at, &l.Kind, &l.CompanyID, &l.Detail); err != nil {
			return nil, err
		}
		l.At = parseTS(at)
		out = append(out, l)
	}
	return out, rows.Err()
}

// ---- documents

// StoredDoc is a document as stored.
type StoredDoc struct {
	ID          int64
	SourceID    string
	Adapter     string
	URL         string
	FetchedAt   time.Time
	PublishedAt time.Time
	Title       string
	Body        string
	BodyDropped bool
	Filing      *Filing
}

// EventTime is the time the document speaks of: published, else fetched.
func (d StoredDoc) EventTime() time.Time {
	if !d.PublishedAt.IsZero() {
		return d.PublishedAt
	}
	return d.FetchedAt
}

// InsertDocument stores a document once per canonical URL. created is false
// when the document was stored before: nothing downstream changes.
func (r *Repo) InsertDocument(ctx context.Context, d Doc) (id int64, created bool, err error) {
	canon, err := CanonicalURL(d.URL)
	if err != nil {
		return 0, false, fmt.Errorf("document URL %q: %w", d.URL, err)
	}
	var filing any
	if d.Filing != nil {
		b, _ := json.Marshal(d.Filing)
		filing = string(b)
	}
	now := r.stamp()
	err = r.s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO signal_documents (source_id, adapter, url, canon_url, fetched_at, published_at, title, body, filing_json, last_used_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (canon_url) DO NOTHING`,
			d.SourceID, d.Adapter, d.URL, canon, fmtTS(d.FetchedAt), fmtNullTS(d.PublishedAt), capBytes(d.Title, 1000), capBytes(d.Body, maxBodyBytes), filing, now)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			id, err = res.LastInsertId()
			created = true
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT id FROM signal_documents WHERE canon_url = ?`, canon).Scan(&id)
	})
	return id, created, err
}

const docColumns = `id, source_id, adapter, url, fetched_at, published_at, title, body, body_dropped, filing_json`

func scanDoc(row interface{ Scan(...any) error }) (StoredDoc, error) {
	var d StoredDoc
	var fetched string
	var pub, filing sql.NullString
	var dropped int
	if err := row.Scan(&d.ID, &d.SourceID, &d.Adapter, &d.URL, &fetched, &pub, &d.Title, &d.Body, &dropped, &filing); err != nil {
		return d, err
	}
	d.FetchedAt, d.PublishedAt, d.BodyDropped = parseTS(fetched), parseNullTS(pub), dropped == 1
	if filing.Valid && filing.String != "" {
		var f Filing
		if json.Unmarshal([]byte(filing.String), &f) == nil {
			d.Filing = &f
		}
	}
	return d, nil
}

// PendingDocuments lists documents not yet turned into events in the order a
// pass takes them: the ones after the document the last pass stopped at,
// oldest first, then the ones up to it.
func (r *Repo) PendingDocuments(ctx context.Context, after int64, limit int) ([]StoredDoc, error) {
	rows, err := r.s.Read().QueryContext(ctx, `SELECT `+docColumns+` FROM signal_documents WHERE identified_at IS NULL ORDER BY (id <= ?), id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredDoc
	for rows.Next() {
		d, err := scanDoc(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DocumentByURL reads the document stored under a URL's canonical form, if
// one is.
func (r *Repo) DocumentByURL(ctx context.Context, raw string) (StoredDoc, bool, error) {
	canon, err := CanonicalURL(raw)
	if err != nil {
		return StoredDoc{}, false, nil
	}
	d, err := scanDoc(r.s.Read().QueryRowContext(ctx, `SELECT `+docColumns+` FROM signal_documents WHERE canon_url = ?`, canon))
	if errors.Is(err, sql.ErrNoRows) {
		return d, false, nil
	}
	return d, err == nil, err
}

// Document reads one document.
func (r *Repo) Document(ctx context.Context, id int64) (StoredDoc, error) {
	d, err := scanDoc(r.s.Read().QueryRowContext(ctx, `SELECT `+docColumns+` FROM signal_documents WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// MarkIdentified records that a document was turned into events (possibly none).
func (r *Repo) MarkIdentified(ctx context.Context, id int64) error {
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE signal_documents SET identified_at = ? WHERE id = ?`, r.stamp(), id)
		return err
	})
}

// ---- companies and aliases

// UpsertCompany returns the company of a canonical key, creating it with the
// display name when new.
func (r *Repo) UpsertCompany(ctx context.Context, name, canon string) (Company, error) {
	var c Company
	err := r.s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO signal_companies (name, canon, created_at) VALUES (?, ?, ?) ON CONFLICT (canon) DO NOTHING`,
			strings.TrimSpace(name), canon, r.stamp()); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT id, name, canon, state FROM signal_companies WHERE canon = ?`, canon).Scan(&c.ID, &c.Name, &c.Canon, &c.State)
	})
	return c, err
}

// Company reads one company.
func (r *Repo) Company(ctx context.Context, id int64) (Company, error) {
	var c Company
	err := r.s.Read().QueryRowContext(ctx, `SELECT id, name, canon, state FROM signal_companies WHERE id = ?`, id).Scan(&c.ID, &c.Name, &c.Canon, &c.State)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// SetState sets watching, dismissed or new, and audits the user action.
func (r *Repo) SetState(ctx context.Context, id int64, state string) error {
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE signal_companies SET state = ? WHERE id = ?`, state, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return auditTx(ctx, tx, r.stamp(), "state", id, state)
	})
}

// Alias returns the cached canonical name for a raw name.
func (r *Repo) Alias(ctx context.Context, raw string) (canon string, ok bool, err error) {
	err = r.s.Read().QueryRowContext(ctx, `SELECT canon FROM signal_aliases WHERE raw = ?`, raw).Scan(&canon)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return canon, err == nil, err
}

// PutAlias caches the model's answer for a raw name.
func (r *Repo) PutAlias(ctx context.Context, raw, canon string) error {
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO signal_aliases (raw, canon, created_at) VALUES (?, ?, ?) ON CONFLICT (raw) DO NOTHING`, raw, canon, r.stamp())
		return err
	})
}

// ---- events

const mergeWindow = 7 * 24 * time.Hour

// AttachEvent attaches a document to the event of a company: an existing event
// of the same type within 7 days (an equal amount preferred), else a new one.
// The event time is the first event's and never moves. A document already
// attached to an event of the company is a no-op.
func (r *Repo) AttachEvent(ctx context.Context, docID, companyID int64, typ string, at time.Time, amount *float64) (eventID int64, created bool, err error) {
	err = r.s.Write(ctx, func(tx *sql.Tx) error {
		switch err := tx.QueryRowContext(ctx, `SELECT event_id FROM signal_event_docs WHERE document_id = ? AND company_id = ?`, docID, companyID).Scan(&eventID); {
		case err == nil:
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id, event_time, amount FROM signal_events WHERE company_id = ? AND type = ? ORDER BY event_time`, companyID, typ)
		if err != nil {
			return err
		}
		var best int64
		var bestGap time.Duration
		bestEqual := false
		for rows.Next() {
			var id int64
			var ts string
			var amt sql.NullFloat64
			if err := rows.Scan(&id, &ts, &amt); err != nil {
				rows.Close()
				return err
			}
			gap := at.Sub(parseTS(ts))
			if gap < 0 {
				gap = -gap
			}
			if gap > mergeWindow {
				continue
			}
			equal := amount != nil && amt.Valid && *amount == amt.Float64
			if best == 0 || (equal && !bestEqual) || (equal == bestEqual && gap < bestGap) {
				best, bestGap, bestEqual = id, gap, equal
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if best != 0 {
			eventID = best
			if amount != nil {
				if _, err := tx.ExecContext(ctx, `UPDATE signal_events SET amount = ? WHERE id = ? AND amount IS NULL`, *amount, best); err != nil {
					return err
				}
			}
		} else {
			var amt any
			if amount != nil {
				amt = *amount
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO signal_events (company_id, type, event_time, amount, created_at) VALUES (?, ?, ?, ?, ?)`,
				companyID, typ, fmtTS(at), amt, r.stamp())
			if err != nil {
				return err
			}
			eventID, _ = res.LastInsertId()
			created = true
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO signal_event_docs (event_id, document_id, company_id) VALUES (?, ?, ?)`, eventID, docID, companyID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE signal_documents SET last_used_at = ? WHERE id = ?`, r.stamp(), docID)
		return err
	})
	return eventID, created, err
}

// EventDocs lists the documents of an event, oldest first.
func (r *Repo) EventDocs(ctx context.Context, eventID int64) ([]StoredDoc, error) {
	rows, err := r.s.Read().QueryContext(ctx, `SELECT `+docColumns+` FROM signal_documents WHERE id IN (SELECT document_id FROM signal_event_docs WHERE event_id = ?) ORDER BY id`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredDoc
	for rows.Next() {
		d, err := scanDoc(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func scanEvent(row interface{ Scan(...any) error }) (Event, error) {
	var e Event
	var ts string
	var amt sql.NullFloat64
	if err := row.Scan(&e.ID, &e.CompanyID, &e.Type, &ts, &amt); err != nil {
		return e, err
	}
	e.Time = parseTS(ts)
	if amt.Valid {
		v := amt.Float64
		e.Amount = &v
	}
	return e, nil
}

// Event reads one event.
func (r *Repo) Event(ctx context.Context, id int64) (Event, error) {
	e, err := scanEvent(r.s.Read().QueryRowContext(ctx, `SELECT id, company_id, type, event_time, amount FROM signal_events WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// PendingEvents lists events with no assessment under the rubric version in
// the order a pass takes them: the ones after the event the last pass stopped
// at, oldest first, then the ones up to it.
func (r *Repo) PendingEvents(ctx context.Context, rubric string, after int64, limit int) ([]Event, error) {
	rows, err := r.s.Read().QueryContext(ctx, `SELECT id, company_id, type, event_time, amount FROM signal_events e
		WHERE NOT EXISTS (SELECT 1 FROM signal_assessments a WHERE a.event_id = e.id AND a.rubric_version = ?) ORDER BY (e.id <= ?), e.id LIMIT ?`, rubric, after, limit)
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

// BodyRetention is how long a document body is kept after its last use as
// evidence.
const BodyRetention = 180 * 24 * time.Hour

// PruneBodies reduces documents last used before the retention window to their
// metadata. Assessments, events and tiers are kept.
func (r *Repo) PruneBodies(ctx context.Context) (int, error) {
	n := 0
	err := r.s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE signal_documents SET body = '', body_dropped = 1 WHERE body_dropped = 0 AND last_used_at < ?`, fmtTS(r.now().Add(-BodyRetention)))
		if err != nil {
			return err
		}
		c, _ := res.RowsAffected()
		n = int(c)
		return nil
	})
	return n, err
}
