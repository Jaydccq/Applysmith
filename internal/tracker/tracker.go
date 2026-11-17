package tracker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
	"applysmith/internal/store"
)

// Typed failures. Callers branch with errors.Is.
var (
	ErrInvalid           = errors.New("tracker: invalid input")
	ErrForbidden         = errors.New("tracker: actor not allowed")
	ErrTransitionRefused = errors.New("tracker: transition refused")
	ErrDuplicate         = errors.New("tracker: duplicate_candidacy")
)

// RefusedError is returned after a refused automated change. The refusal event
// has been committed.
type RefusedError struct {
	ID       int64
	Actor    Actor
	From, To Status
	Reason   string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("tracker: %s refused for row %d: %s", e.Actor, e.ID, e.Reason)
}
func (e *RefusedError) Is(target error) bool { return target == ErrTransitionRefused }

// DuplicateError names the existing row a new one would duplicate.
type DuplicateError struct {
	ID   int64
	Rule string
}

func (e *DuplicateError) Error() string {
	return fmt.Sprintf("tracker: duplicate_candidacy: row %d matches by %s", e.ID, e.Rule)
}
func (e *DuplicateError) Is(target error) bool { return target == ErrDuplicate }

// Matched says what the existing row matched, in the user's words: "the same
// posting", "the same company and role".
func (e *DuplicateError) Matched() string {
	if w, ok := ruleWords[e.Rule]; ok {
		return w
	}
	return e.Rule
}

// LinkedTable is a table owned by another capability whose rows point at a
// tracker row. A merge re-points them to the survivor so no link is lost.
type LinkedTable struct {
	Table, Column string
}

// Options configures a Tracker. The zero value uses the wall clock.
type Options struct {
	// Now replaces the clock. Its location decides the calendar day stamped
	// into applied_at.
	Now          func() time.Time
	LinkedTables []LinkedTable
}

// Tracker reads and writes the tracker tables of one workspace database.
type Tracker struct {
	s      *store.Store
	now    func() time.Time
	linked []LinkedTable
	merges *mergeTrash
}

var identRE = lazyregexp.New(`^[a-z_][a-z0-9_]*$`)

// New returns a Tracker over an open store.
func New(s *store.Store, opts Options) (*Tracker, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	for _, l := range opts.LinkedTables {
		if !identRE.MatchString(l.Table) || !identRE.MatchString(l.Column) {
			return nil, fmt.Errorf("%w: linked table %q.%q is not a plain identifier", ErrInvalid, l.Table, l.Column)
		}
	}
	return &Tracker{s: s, now: opts.Now, linked: opts.LinkedTables, merges: &mergeTrash{}}, nil
}

const (
	dateLayout   = "2006-01-02"
	maxNoteChars = 1000
	maxNotes     = 10
)

func (t *Tracker) stamp() string { return t.now().UTC().Format(time.RFC3339) }
func (t *Tracker) today() string { return t.now().Format(dateLayout) }

// dayEnd is the instant the local day ends, in the UTC form events are stamped
// in, so an event from late in the local day is not read as the next day's.
func (t *Tracker) dayEnd(day string) string {
	d, _ := time.ParseInLocation(dateLayout, day, t.now().Location())
	return d.AddDate(0, 0, 1).UTC().Format(time.RFC3339)
}

func validDate(s string) bool { _, err := time.Parse(dateLayout, s); return err == nil }

// querier is what *sql.DB, *sql.Tx and *sql.Conn share.
type querier interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// ScoreKind says whether a score is provisional.
type ScoreKind string

// The two kinds of score.
const (
	ScoreFinal     ScoreKind = "final"
	ScoreScreening ScoreKind = "screening"
)

// AppliedBy says who submitted an application.
type AppliedBy string

// The two submitters.
const (
	ByUser  AppliedBy = "user"
	ByAgent AppliedBy = "agent"
)

// Entry is one tracker row. Empty strings stand for NULL.
type Entry struct {
	ID           int64     `json:"id"`
	Date         string    `json:"date"`
	Company      string    `json:"company"`
	Role         string    `json:"role"`
	Score        *float64  `json:"score"`
	ScoreKind    ScoreKind `json:"score_kind"`
	Status       Status    `json:"status"`
	AppliedBy    AppliedBy `json:"applied_by"`
	AppliedAt    string    `json:"applied_at"`
	ResumePDF    bool      `json:"resume_pdf"`
	CoverLetter  bool      `json:"cover_letter"`
	ReportNumber *int64    `json:"report_number"`
	Notes        string    `json:"notes"`
	PostingURL   string    `json:"posting_url"`
	Location     string    `json:"location"`
	Source       string    `json:"source"`
	IdentityKey  string    `json:"identity_key"`
	ArchivedAt   string    `json:"archived_at"`
	Version      int       `json:"version"`
	CreatedAt    string    `json:"created_at"`
	UpdatedAt    string    `json:"updated_at"`
}

// DisplayStatus is the status with the agent mark: "applied (agent)".
func (e Entry) DisplayStatus() string {
	if e.AppliedBy == ByAgent && e.Status.Submitted() {
		return string(e.Status) + " (agent)"
	}
	return string(e.Status)
}

// EventKind names what an event records.
type EventKind string

// The event kinds the schema accepts.
const (
	EventCreated             EventKind = "created"
	EventStatusChanged       EventKind = "status_changed"
	EventStatusCorrected     EventKind = "status_corrected"
	EventTransitionRefused   EventKind = "transition_refused"
	EventScoreUpdated        EventKind = "score_updated"
	EventReportLinked        EventKind = "report_linked"
	EventNoteAdded           EventKind = "note_added"
	EventDocumentFlagSet     EventKind = "document_flag_set"
	EventDocumentFlagCleared EventKind = "document_flag_cleared"
	EventDocumentsOverride   EventKind = "documents_override"
	EventFollowupRecorded    EventKind = "followup_recorded"
	EventFollowupSnoozed     EventKind = "followup_snoozed"
	EventArchived            EventKind = "archived"
	EventUnarchived          EventKind = "unarchived"
	EventMerged              EventKind = "merged"
	EventDuplicateAttached   EventKind = "duplicate_attached"
	EventAppliedDateSet      EventKind = "applied_date_set"
)

// Event is one append-only record of a change.
type Event struct {
	ID             int64           `json:"id"`
	EntryID        int64           `json:"entry_id"`
	At             string          `json:"at"`
	Actor          Actor           `json:"actor"`
	Kind           EventKind       `json:"kind"`
	PreviousStatus Status          `json:"previous_status"`
	NewStatus      Status          `json:"new_status"`
	Detail         json.RawMessage `json:"detail"`
}

const entryColumns = `id, date, company, role, score, score_kind, status, applied_by, applied_at, resume_pdf, cover_letter,
	report_number, notes, posting_url, location, source, identity_key, archived_at, version, created_at, updated_at`

type scanner interface{ Scan(...any) error }

func scanEntry(r scanner) (Entry, error) {
	var e Entry
	var score sql.NullFloat64
	var kind, by, appliedAt, source, archived sql.NullString
	var report sql.NullInt64
	var resume, cover int
	if err := r.Scan(&e.ID, &e.Date, &e.Company, &e.Role, &score, &kind, &e.Status, &by, &appliedAt, &resume, &cover,
		&report, &e.Notes, &e.PostingURL, &e.Location, &source, &e.IdentityKey, &archived, &e.Version, &e.CreatedAt, &e.UpdatedAt); err != nil {
		return Entry{}, err
	}
	if score.Valid {
		e.Score = &score.Float64
	}
	e.ScoreKind, e.AppliedBy, e.AppliedAt = ScoreKind(kind.String), AppliedBy(by.String), appliedAt.String
	e.ResumePDF, e.CoverLetter = resume == 1, cover == 1
	if report.Valid {
		e.ReportNumber = &report.Int64
	}
	e.Source, e.ArchivedAt = source.String, archived.String
	return e, nil
}

func getEntry(ctx context.Context, q querier, id int64) (Entry, error) {
	e, err := scanEntry(q.QueryRowContext(ctx, `SELECT `+entryColumns+` FROM tracker_entries WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, fmt.Errorf("%w: tracker row %d", store.ErrNotFound, id)
	}
	return e, err
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// addEvent appends one event; detail may be nil.
func (t *Tracker) addEvent(ctx context.Context, q querier, id int64, actor Actor, kind EventKind, prev, next Status, detail map[string]any) error {
	raw := []byte("{}")
	if detail != nil {
		var err error
		if raw, err = json.Marshal(detail); err != nil {
			return err
		}
	}
	_, err := q.ExecContext(ctx, `INSERT INTO tracker_events (entry_id, at, actor, kind, previous_status, new_status, detail_json) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, t.stamp(), string(actor), string(kind), nullStr(string(prev)), nullStr(string(next)), string(raw))
	return err
}

// tenths converts a score to integer tenths so comparisons never meet float
// rounding.
func tenths(score float64) int { return int(math.Round(score * 10)) }

// checkScore rounds to one decimal and enforces the range of its kind.
func checkScore(score float64, kind ScoreKind) (float64, error) {
	if kind != ScoreFinal && kind != ScoreScreening {
		return 0, fmt.Errorf("%w: score kind must be final or screening", ErrInvalid)
	}
	low := 0.0
	if kind == ScoreFinal {
		low = 1.0
	}
	r := math.Round(score*10) / 10
	if math.IsNaN(score) || r < low || r > 5.0 {
		return 0, fmt.Errorf("%w: a %s score must be between %.1f and 5.0", ErrInvalid, kind, low)
	}
	return r, nil
}

// appendNote adds a "{date} {actor}: {text}" line and keeps the latest ten;
// older notes remain in their events.
func appendNote(notes, date string, actor Actor, text string) string {
	var lines []string
	if notes != "" {
		lines = strings.Split(notes, "\n")
	}
	text = strings.Join(strings.Fields(text), " ")
	lines = append(lines, fmt.Sprintf("%s %s: %s", date, actor, text))
	if len(lines) > maxNotes {
		lines = lines[len(lines)-maxNotes:]
	}
	return strings.Join(lines, "\n")
}

func checkNote(text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("%w: a note needs text", ErrInvalid)
	}
	if n := len([]rune(text)); n > maxNoteChars {
		return fmt.Errorf("%w: a note holds at most %d characters, got %d", ErrInvalid, maxNoteChars, n)
	}
	return nil
}

func checkVersion(e Entry, expected int) error {
	if expected > 0 && e.Version != expected {
		return fmt.Errorf("%w: row %d is at version %d, the edit was made against %d", store.ErrConflict, e.ID, e.Version, expected)
	}
	return nil
}

func nullInt(n *int64) any {
	if n == nil {
		return nil
	}
	return *n
}
