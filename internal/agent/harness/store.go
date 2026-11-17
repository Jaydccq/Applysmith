package harness

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"applysmith/internal/store"
)

// Sources of a version.
const (
	SourceImprove = "improve"
	SourceManual  = "manual"
)

// Version is a stored harness version with its record.
type Version struct {
	Harness
	ParentID    int64
	Source      string
	Summary     string
	Evidence    json.RawMessage
	RoundID     string
	Active      bool
	CreatedAt   time.Time
	ActivatedAt time.Time
}

// Store keeps the versions. Exactly one row is active, or none (version 0).
type Store struct {
	db  *store.Store
	now func() time.Time
}

// New returns the store over the workspace database.
func New(db *store.Store, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now}
}

// ErrNotFound means no version has the id.
var ErrNotFound = errors.New("harness: no such version")

const cols = `id, parent_id, guidance, tool_notes, source, summary, evidence, round_id, active, created_at, activated_at`

func scan(row interface{ Scan(...any) error }) (Version, error) {
	var v Version
	var guidance, notes, evidence, created, activated string
	var active int
	if err := row.Scan(&v.Version, &v.ParentID, &guidance, &notes, &v.Source, &v.Summary, &evidence, &v.RoundID, &active, &created, &activated); err != nil {
		return Version{}, err
	}
	if err := json.Unmarshal([]byte(guidance), &v.Guidance); err != nil {
		return Version{}, fmt.Errorf("harness %d guidance: %w", v.Version, err)
	}
	if err := json.Unmarshal([]byte(notes), &v.ToolNotes); err != nil {
		return Version{}, fmt.Errorf("harness %d notes: %w", v.Version, err)
	}
	if len(v.ToolNotes) == 0 {
		v.ToolNotes = nil
	}
	v.Evidence = json.RawMessage(evidence)
	v.Active = active == 1
	v.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	v.ActivatedAt, _ = time.Parse(time.RFC3339Nano, activated)
	return v, nil
}

// Active returns the harness turns run with: the active row, or version 0.
func (s *Store) Active(ctx context.Context) (Harness, error) {
	v, err := scan(s.db.Read().QueryRowContext(ctx, `SELECT `+cols+` FROM agent_harness_versions WHERE active = 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return Harness{}, nil
	}
	return v.Harness, err
}

// Get returns one version.
func (s *Store) Get(ctx context.Context, id int64) (Version, error) {
	v, err := scan(s.db.Read().QueryRowContext(ctx, `SELECT `+cols+` FROM agent_harness_versions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Version{}, ErrNotFound
	}
	return v, err
}

// Versions lists the newest versions first.
func (s *Store) Versions(ctx context.Context, limit int) ([]Version, error) {
	rows, err := s.db.Read().QueryContext(ctx, `SELECT `+cols+` FROM agent_harness_versions ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Draft is a version to store.
type Draft struct {
	Harness  Harness
	Source   string
	Summary  string
	Evidence json.RawMessage
	RoundID  string
}

// Activate validates a draft against the catalog's tools, stores it and makes
// it the active version. Its parent is the version that was active. A draft
// whose parent changed since base was read is refused, so a round never
// replaces a version it did not test against.
func (s *Store) Activate(ctx context.Context, d Draft, base int64, tools []string) (int64, error) {
	if err := Validate(d.Harness, tools); err != nil {
		return 0, err
	}
	if d.Source != SourceImprove && d.Source != SourceManual {
		return 0, fmt.Errorf("harness: %q is not a source", d.Source)
	}
	guidance, _ := json.Marshal(nonNil(d.Harness.Guidance))
	notes, _ := json.Marshal(d.Harness.ToolNotes)
	if d.Harness.ToolNotes == nil {
		notes = []byte("{}")
	}
	evidence := d.Evidence
	if len(evidence) == 0 {
		evidence = json.RawMessage("{}")
	}
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	var id int64
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		var parent int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM agent_harness_versions WHERE active = 1`).Scan(&parent)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if parent != base {
			return fmt.Errorf("harness: the active version is %d, not %d", parent, base)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_harness_versions SET active = 0 WHERE active = 1`); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO agent_harness_versions
			(parent_id, guidance, tool_notes, source, summary, evidence, round_id, active, created_at, activated_at)
			VALUES (?,?,?,?,?,?,?,1,?,?)`, parent, string(guidance), string(notes), d.Source, d.Summary, string(evidence), d.RoundID, stamp, stamp)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// Restore makes an earlier version's text the active one, recorded as a new
// manual version whose summary names the one restored. Restoring 0 empties
// the harness.
func (s *Store) Restore(ctx context.Context, id int64, tools []string) (int64, error) {
	var h Harness
	if id != 0 {
		v, err := s.Get(ctx, id)
		if err != nil {
			return 0, err
		}
		h = v.Harness
	}
	cur, err := s.Active(ctx)
	if err != nil {
		return 0, err
	}
	// A note for a tool the catalog no longer has is dropped, not refused: the
	// restore must work after a tool was removed.
	for name := range h.ToolNotes {
		if !slices.Contains(tools, name) {
			delete(h.ToolNotes, name)
		}
	}
	return s.Activate(ctx, Draft{Harness: h, Source: SourceManual, Summary: fmt.Sprintf("Restored version %d", id)}, cur.Version, tools)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
