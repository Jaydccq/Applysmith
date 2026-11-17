package tracker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"applysmith/internal/store"
)

// StatusChange asks for a status change on one row.
type StatusChange struct {
	ID    int64
	To    Status
	Actor Actor
	Note  string
	// On is the ISO date of the application, honoured for the user actor when
	// the row enters applied.
	On string
	// AppliedBy is required from the agent actor when the row enters applied:
	// agent when its own run submitted, user when it relays the user's word.
	// From the user and Gmail actors, agent settles an unconfirmed submission
	// the apply chain made of the row's posting (a receipt, or the user's
	// "Confirm submitted"); without one the change is refused.
	AppliedBy AppliedBy
	// ExpectedVersion, when positive, makes a stale edit fail with a conflict.
	ExpectedVersion int
	// Detail is recorded in the status event next to the tracker's own keys,
	// so a producer can name its source (a Gmail message id, say).
	Detail map[string]any
	// Unarchive lets an automated actor change an archived row, which then
	// leaves the archive in the same transaction: the apply chain recording a
	// submission the user started on that one posting. Without it an archived
	// row refuses every automated change.
	Unarchive bool
}

// StatusResult describes a status change that was applied or was a no-op.
type StatusResult struct {
	Entry      Entry
	Changed    bool
	Correction bool
	Inferred   bool
}

// SetStatus changes a row's status under the transition table. A refused
// automated change returns a *RefusedError after its refusal event commits.
func (t *Tracker) SetStatus(ctx context.Context, c StatusChange) (StatusResult, error) {
	var res StatusResult
	var refused *RefusedError
	err := t.s.Write(ctx, func(tx *sql.Tx) error {
		var err error
		res, refused, err = t.setStatusTx(ctx, tx, c)
		return err
	})
	if err != nil {
		return StatusResult{}, err
	}
	if refused != nil {
		return StatusResult{Entry: res.Entry}, refused
	}
	return res, nil
}

// RevertStatus puts a row back, as the user's correction, to the status it had
// before the status change whose event detail holds key = value, only while
// Revertible holds; otherwise it returns store.ErrConflict. The note that
// change wrote is taken back with it. also runs in the same transaction, so the
// revert and the caller's own write commit together or not at all.
func (t *Tracker) RevertStatus(ctx context.Context, id int64, key, value string, also func(tx *sql.Tx) error) (Entry, error) {
	var out Entry
	err := t.s.Write(ctx, func(tx *sql.Tx) error {
		e, err := getEntry(ctx, tx, id)
		if err != nil {
			return err
		}
		events, err := eventsOf(ctx, tx, id)
		if err != nil {
			return err
		}
		i := latestChange(e, events, key, value)
		if i < 0 {
			return fmt.Errorf("%w: the application changed since (it is %s now), so it was left as it is", store.ErrConflict, e.Status)
		}
		ev := events[i]
		res, refused, err := t.setStatusTx(ctx, tx, StatusChange{ID: id, To: ev.PreviousStatus, Actor: ActorUser})
		if err != nil {
			return err
		}
		if refused != nil {
			return refused
		}
		var d struct {
			Note string `json:"note"`
		}
		_ = json.Unmarshal(ev.Detail, &d)
		if n := noteOf(events, i, d.Note); n > 0 {
			if err := t.takeBackNote(ctx, tx, res.Entry, ev.Actor, n, d.Note); err != nil {
				return err
			}
		}
		if out, err = getEntry(ctx, tx, id); err != nil {
			return err
		}
		return also(tx)
	})
	return out, err
}

// Revertible reports whether the status change whose event detail holds
// key = value is still the row's latest, from a status, and the row still has
// the status it set: the one change RevertStatus puts back.
func (d Detail) Revertible(key, value string) bool {
	return latestChange(d.Entry, d.Events, key, value) >= 0
}

// latestChange is the index in events of that change, or -1.
func latestChange(e Entry, events []Event, key, value string) int {
	for i := len(events) - 1; i >= 0; i-- {
		if ev := events[i]; ev.Kind == EventStatusChanged || ev.Kind == EventStatusCorrected {
			var d map[string]any
			_ = json.Unmarshal(ev.Detail, &d)
			if d[key] != value || ev.PreviousStatus == "" || ev.NewStatus != e.Status {
				return -1
			}
			return i
		}
	}
	return -1
}

// noteOf is the note event the status change at events[i] wrote with text and
// nothing took back yet, or 0.
func noteOf(events []Event, i int, text string) int64 {
	if text == "" {
		return 0
	}
	undone := map[int64]bool{}
	for _, ev := range events {
		undone[ev.Undoes()] = true
	}
	for _, n := range events[i+1:] {
		var d struct {
			Text string `json:"text"`
		}
		if n.Kind == EventNoteAdded && n.Actor == events[i].Actor && json.Unmarshal(n.Detail, &d) == nil && d.Text == text && !undone[n.ID] {
			return n.ID
		}
	}
	return 0
}

func onRefusal(to, from Status) string {
	if to != Applied {
		return "moves the row to " + string(to)
	}
	return "leaves a row that is already " + string(from) + " as it is"
}

// BulkSetStatus makes ordinary or correcting changes as the user for up to 100
// rows in one transaction; any failure changes nothing.
func (t *Tracker) BulkSetStatus(ctx context.Context, ids []int64, to Status, note string) ([]StatusResult, error) {
	if len(ids) == 0 || len(ids) > 100 {
		return nil, fmt.Errorf("%w: a bulk change names 1 to 100 rows, got %d", ErrInvalid, len(ids))
	}
	if !to.Valid() {
		_, err := ParseStatus(string(to))
		return nil, err
	}
	var out []StatusResult
	err := t.s.Write(ctx, func(tx *sql.Tx) error {
		out = out[:0]
		for _, id := range ids {
			res, _, err := t.setStatusTx(ctx, tx, StatusChange{ID: id, To: to, Actor: ActorUser, Note: note})
			if err != nil {
				return err
			}
			out = append(out, res)
		}
		return nil
	})
	return out, err
}

func (t *Tracker) setStatusTx(ctx context.Context, tx *sql.Tx, c StatusChange) (StatusResult, *RefusedError, error) {
	if !c.To.Valid() {
		_, err := ParseStatus(string(c.To))
		return StatusResult{}, nil, err
	}
	if !c.Actor.valid() {
		return StatusResult{}, nil, fmt.Errorf("%w: unknown actor %q", ErrInvalid, c.Actor)
	}
	if c.Note != "" {
		if err := checkNote(c.Note); err != nil {
			return StatusResult{}, nil, err
		}
	}
	if c.On != "" && !validDate(c.On) {
		return StatusResult{}, nil, fmt.Errorf("%w: --on %q is not an ISO date", ErrInvalid, c.On)
	}
	// The guard reads the row inside the write transaction, never a copy
	// read earlier.
	e, err := getEntry(ctx, tx, c.ID)
	if err != nil {
		return StatusResult{}, nil, err
	}
	if c.On != "" && c.Actor == ActorUser && (c.To != Applied || e.Status == Applied) {
		return StatusResult{}, nil, fmt.Errorf("%w: --on sets the date when a row enters applied; this change %s", ErrInvalid, onRefusal(c.To, e.Status))
	}
	if err := checkVersion(e, c.ExpectedVersion); err != nil {
		return StatusResult{}, nil, err
	}
	d := Decide(c.Actor, e.Status, c.To)
	unarchive := false
	if d.Verdict == Allow || d.Verdict == Correct {
		if e.ArchivedAt != "" && c.Actor != ActorUser {
			if unarchive = c.Unarchive; !unarchive {
				d = Decision{Verdict: Refuse, Reason: "an automatic update leaves an archived row as it is"}
			}
		}
	}
	switch d.Verdict {
	case Noop:
		return StatusResult{Entry: e}, nil, nil
	case Refuse:
		if err := t.recordRefusal(ctx, tx, e, c.Actor, c.To, d.Reason); err != nil {
			return StatusResult{}, nil, err
		}
		return StatusResult{Entry: e}, &RefusedError{ID: e.ID, Actor: c.Actor, From: e.Status, To: c.To, Reason: d.Reason}, nil
	}
	if c.Actor == ActorAgent && c.To == Applied && c.AppliedBy != ByUser && c.AppliedBy != ByAgent {
		return StatusResult{}, nil, fmt.Errorf("%w: the agent must say who applied (user or agent)", ErrInvalid)
	}
	if settlesChainSubmission(c) {
		switch ok, err := chainSubmitted(ctx, tx, e.ID); {
		case err != nil:
			return StatusResult{}, nil, err
		case !ok:
			return StatusResult{}, nil, fmt.Errorf("%w: applied by the agent needs a submission the apply chain made of this posting", ErrInvalid)
		}
	}

	detail := map[string]any{}
	for k, v := range c.Detail {
		detail[k] = v
	}
	set := []string{"status = ?"}
	args := []any{string(c.To)}
	if d.InferredApplied {
		detail["inferred_applied"] = true
	}
	if c.To == Applied {
		by := appliedByFor(c)
		if e.AppliedBy == "" {
			set, args = append(set, "applied_by = ?"), append(args, string(by))
		}
		detail["applied_by"] = string(by)
		if c.Actor == ActorAgent && by == ByUser {
			detail["relayed_user_statement"] = true
		}
		switch {
		case c.On != "" && c.Actor == ActorUser:
			set, args = append(set, "applied_at = ?"), append(args, c.On)
		case e.AppliedAt == "":
			set, args = append(set, "applied_at = ?"), append(args, t.today())
		}
	}
	notes := e.Notes
	if c.Note != "" {
		notes = appendNote(notes, t.today(), c.Actor, c.Note)
		set, args = append(set, "notes = ?"), append(args, notes)
		detail["note"] = c.Note
	}
	if unarchive {
		set = append(set, "archived_at = NULL")
		if err := t.addEvent(ctx, tx, e.ID, c.Actor, EventUnarchived, "", "", nil); err != nil {
			return StatusResult{}, nil, err
		}
	}
	set = append(set, "version = version + 1", "updated_at = ?")
	args = append(args, t.stamp(), e.ID)
	if _, err := tx.ExecContext(ctx, `UPDATE tracker_entries SET `+strings.Join(set, ", ")+` WHERE id = ?`, args...); err != nil {
		return StatusResult{}, nil, err
	}
	kind := EventStatusChanged
	if d.Verdict == Correct {
		kind = EventStatusCorrected
	}
	if err := t.addEvent(ctx, tx, e.ID, c.Actor, kind, e.Status, c.To, detail); err != nil {
		return StatusResult{}, nil, err
	}
	if c.To == Applied && c.On != "" && c.Actor == ActorUser {
		if err := t.addEvent(ctx, tx, e.ID, c.Actor, EventAppliedDateSet, "", "", map[string]any{"applied_at": c.On, "previous": e.AppliedAt}); err != nil {
			return StatusResult{}, nil, err
		}
	}
	if c.Note != "" {
		if err := t.addEvent(ctx, tx, e.ID, c.Actor, EventNoteAdded, "", "", map[string]any{"text": c.Note}); err != nil {
			return StatusResult{}, nil, err
		}
	}
	out, err := getEntry(ctx, tx, e.ID)
	return StatusResult{Entry: out, Changed: true, Correction: d.Verdict == Correct, Inferred: d.InferredApplied}, nil, err
}

// appliedByFor applies the marking rule: the apply chain and the agent mark
// agent unless the agent relays the user's statement; the user and Gmail mark
// user, unless they settle a submission of the apply chain.
func appliedByFor(c StatusChange) AppliedBy {
	switch {
	case c.Actor == ActorApplyChain:
		return ByAgent
	case c.Actor == ActorAgent:
		return c.AppliedBy
	case settlesChainSubmission(c):
		return ByAgent
	}
	return ByUser
}

// settlesChainSubmission is a user or Gmail change that marks the row applied
// by the agent: it settles the apply chain's own submission.
func settlesChainSubmission(c StatusChange) bool {
	return c.To == Applied && c.AppliedBy == ByAgent && (c.Actor == ActorUser || c.Actor == ActorGmail)
}

// chainSubmitted reports whether the submissions ledger holds a press of the
// apply chain for the row's posting, under its identity key or an alias.
func chainSubmitted(ctx context.Context, tx *sql.Tx, id int64) (bool, error) {
	var ok bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM submissions s WHERE s.source = 'apply-chain' AND s.state IN ('submitted', 'may-have-submitted')
		AND (s.identity_key = (SELECT identity_key FROM tracker_entries WHERE id = ?) OR s.identity_key IN (SELECT identity_key FROM tracker_aliases WHERE entry_id = ?)))`, id, id).Scan(&ok)
	return ok, err
}

// recordRefusal writes one transition_refused event, at most once per row,
// actor and target status per day.
func (t *Tracker) recordRefusal(ctx context.Context, tx *sql.Tx, e Entry, actor Actor, to Status, reason string) error {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tracker_events WHERE entry_id = ? AND kind = ? AND actor = ?
		AND json_extract(detail_json, '$.to') = ? AND substr(at, 1, 10) = substr(?, 1, 10)`,
		e.ID, string(EventTransitionRefused), string(actor), string(to), t.stamp()).Scan(&n)
	if err != nil || n > 0 {
		return err
	}
	return t.addEvent(ctx, tx, e.ID, actor, EventTransitionRefused, e.Status, to, map[string]any{"to": string(to), "reason": reason})
}

// AddNote appends a note as actor and records a note_added event.
func (t *Tracker) AddNote(ctx context.Context, id int64, actor Actor, text string, expectedVersion int) (Entry, error) {
	if err := checkNote(text); err != nil {
		return Entry{}, err
	}
	if !actor.valid() {
		return Entry{}, fmt.Errorf("%w: unknown actor %q", ErrInvalid, actor)
	}
	var out Entry
	err := t.s.Write(ctx, func(tx *sql.Tx) error {
		e, err := getEntry(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := checkVersion(e, expectedVersion); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tracker_entries SET notes = ?, version = version + 1, updated_at = ? WHERE id = ?`,
			appendNote(e.Notes, t.today(), actor, text), t.stamp(), id); err != nil {
			return err
		}
		if err := t.addEvent(ctx, tx, id, actor, EventNoteAdded, "", "", map[string]any{"text": text}); err != nil {
			return err
		}
		out, err = getEntry(ctx, tx, id)
		return err
	})
	return out, err
}

// UndoNote takes back the row's latest note by actor still in force. Events
// are append-only, so it appends a note_added event naming the note it undoes
// and drops that note's line from the row's notes (a line the cap already
// rotated out stays gone); the history keeps both events.
func (t *Tracker) UndoNote(ctx context.Context, id int64, actor Actor, expectedVersion int) (Entry, error) {
	if !actor.valid() {
		return Entry{}, fmt.Errorf("%w: unknown actor %q", ErrInvalid, actor)
	}
	var out Entry
	err := t.s.Write(ctx, func(tx *sql.Tx) error {
		e, err := getEntry(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := checkVersion(e, expectedVersion); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id, actor, detail_json FROM tracker_events WHERE entry_id = ? AND kind = ? ORDER BY id`, id, string(EventNoteAdded))
		if err != nil {
			return err
		}
		defer rows.Close()
		var evs []followupEvent
		texts := map[int64]string{}
		for rows.Next() {
			var ev followupEvent
			var raw string
			var d struct {
				Text   string `json:"text"`
				Undoes int64  `json:"undoes"`
			}
			if err := rows.Scan(&ev.id, &ev.actor, &raw); err != nil {
				return err
			}
			_ = json.Unmarshal([]byte(raw), &d)
			ev.undoes, texts[ev.id] = d.Undoes, d.Text
			evs = append(evs, ev)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		live := inForce(evs)
		for i := len(live) - 1; i >= 0; i-- {
			if ev := live[i]; ev.actor == actor {
				if err := t.takeBackNote(ctx, tx, e, actor, ev.id, texts[ev.id]); err != nil {
					return err
				}
				out, err = getEntry(ctx, tx, id)
				return err
			}
		}
		return fmt.Errorf("%w: row %d has no note of yours to undo", ErrInvalid, id)
	})
	return out, err
}

// takeBackNote appends the undo of the note event noteID and drops that
// note's line from the row's notes.
func (t *Tracker) takeBackNote(ctx context.Context, tx *sql.Tx, e Entry, actor Actor, noteID int64, text string) error {
	if err := t.addEvent(ctx, tx, e.ID, actor, EventNoteAdded, "", "", map[string]any{"undoes": noteID}); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE tracker_entries SET notes = ?, version = version + 1, updated_at = ? WHERE id = ?`, dropNote(e.Notes, actor, text), t.stamp(), e.ID)
	return err
}

// dropNote removes the latest line appendNote wrote for actor's text.
func dropNote(notes string, actor Actor, text string) string {
	lines := strings.Split(notes, "\n")
	tail := " " + string(actor) + ": " + strings.Join(strings.Fields(text), " ")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasSuffix(lines[i], tail) {
			return strings.Join(append(lines[:i], lines[i+1:]...), "\n")
		}
	}
	return notes
}

// Doc names a document flag.
type Doc string

// The two sticky document flags.
const (
	DocResume Doc = "resume_pdf"
	DocCover  Doc = "cover_letter"
)

// SetDocumentFlag sets a flag (any actor, when a document is produced) or
// clears it (the user only). Nothing else ever resets a flag.
func (t *Tracker) SetDocumentFlag(ctx context.Context, id int64, actor Actor, doc Doc, value bool) (Entry, error) {
	var out Entry
	err := t.s.Write(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = t.SetDocumentFlagIn(ctx, tx, id, actor, doc, value)
		return err
	})
	return out, err
}

// SetDocumentFlagIn is SetDocumentFlag inside the caller's transaction, so a
// document that is published and its flag commit together.
func (t *Tracker) SetDocumentFlagIn(ctx context.Context, tx *sql.Tx, id int64, actor Actor, doc Doc, value bool) (Entry, error) {
	if doc != DocResume && doc != DocCover {
		return Entry{}, fmt.Errorf("%w: unknown document flag %q", ErrInvalid, doc)
	}
	if !actor.valid() {
		return Entry{}, fmt.Errorf("%w: unknown actor %q", ErrInvalid, actor)
	}
	if !value && actor != ActorUser {
		return Entry{}, fmt.Errorf("%w: only the user clears %s", ErrForbidden, doc)
	}
	e, err := getEntry(ctx, tx, id)
	if err != nil {
		return Entry{}, err
	}
	have := e.ResumePDF
	if doc == DocCover {
		have = e.CoverLetter
	}
	if have == value {
		return e, nil
	}
	v := 0
	kind := EventDocumentFlagCleared
	if value {
		v, kind = 1, EventDocumentFlagSet
	}
	// doc is one of two constants, never caller text.
	if _, err := tx.ExecContext(ctx, `UPDATE tracker_entries SET `+string(doc)+` = ?, version = version + 1, updated_at = ? WHERE id = ?`, v, t.stamp(), id); err != nil {
		return Entry{}, err
	}
	if err := t.addEvent(ctx, tx, id, actor, kind, "", "", map[string]any{"flag": string(doc)}); err != nil {
		return Entry{}, err
	}
	return getEntry(ctx, tx, id)
}

// OverrideDocuments is the user's override of the 4.5 document floor for one
// report (`applysmith report override <number>`): the report index marks the
// posting eligible for a tailored PDF and the row the report is linked to
// records a documents_override event, in one transaction. Only a final-kind
// report can be overridden; a screening score never makes a posting
// eligible. Overriding an eligible report changes nothing.
func (t *Tracker) OverrideDocuments(ctx context.Context, report int64, actor Actor) (Entry, error) {
	if actor != ActorUser {
		return Entry{}, fmt.Errorf("%w: only the user overrides the document floor", ErrForbidden)
	}
	var out Entry
	err := t.s.Write(ctx, func(tx *sql.Tx) error {
		var kind string
		var eligible int
		var score sql.NullFloat64
		err := tx.QueryRowContext(ctx, `SELECT kind, documents_eligible, score FROM report_index WHERE report_number = ?`, report).Scan(&kind, &eligible, &score)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: report %d", store.ErrNotFound, report)
		}
		if err != nil {
			return err
		}
		if kind != string(ScoreFinal) {
			return fmt.Errorf("%w: report %d is a %s report; evaluate the posting in deep mode, then override its final report", ErrInvalid, report, kind)
		}
		var id int64
		err = tx.QueryRowContext(ctx, `SELECT id FROM tracker_entries WHERE report_number = ?`, report).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: no tracker row is linked to report %d; override the report the row shows", ErrInvalid, report)
		}
		if err != nil {
			return err
		}
		if eligible == 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE report_index SET documents_eligible = 1 WHERE report_number = ?`, report); err != nil {
				return err
			}
			detail := map[string]any{"report_number": report}
			if score.Valid {
				detail["score"] = score.Float64
			}
			if err := t.addEvent(ctx, tx, id, actor, EventDocumentsOverride, "", "", detail); err != nil {
				return err
			}
		}
		out, err = getEntry(ctx, tx, id)
		return err
	})
	return out, err
}

// AddRow is a row the user adds by hand.
type AddRow struct {
	Company   string
	Role      string
	URL       string
	Location  string
	Status    Status // default evaluated
	AppliedAt string // ISO date, for a submitted status
	Source    string // default manual
	Date      string // default today
}

// Add creates a row as the user. When the duplicate rules match an existing
// row it creates nothing and returns a *DuplicateError.
func (t *Tracker) Add(ctx context.Context, a AddRow) (Entry, error) {
	if err := checkNames(a.Company, a.Role); err != nil {
		return Entry{}, err
	}
	if a.Status == "" {
		a.Status = Evaluated
	}
	if !a.Status.Valid() {
		_, err := ParseStatus(string(a.Status))
		return Entry{}, err
	}
	if a.AppliedAt != "" && !validDate(a.AppliedAt) {
		return Entry{}, fmt.Errorf("%w: applied date %q is not an ISO date", ErrInvalid, a.AppliedAt)
	}
	if a.AppliedAt != "" && !a.Status.Submitted() {
		return Entry{}, fmt.Errorf("%w: a row that is %s has no application date", ErrInvalid, a.Status)
	}
	if a.Date != "" && !validDate(a.Date) {
		return Entry{}, fmt.Errorf("%w: date %q is not an ISO date", ErrInvalid, a.Date)
	}
	if a.Source == "" {
		a.Source = "manual"
	}
	if d := DecideNew(ActorUser, a.Status); d.Verdict != Allow {
		return Entry{}, fmt.Errorf("%w: %s", ErrForbidden, d.Reason)
	}
	p := identityPosting(a.URL, a.Source, a.Company, a.Role, a.Location)
	key, err := rowKey(p)
	if err != nil {
		return Entry{}, err
	}
	p.Key = key
	var out Entry
	err = t.s.Write(ctx, func(tx *sql.Tx) error {
		cands, err := loadCandidates(ctx, tx)
		if err != nil {
			return err
		}
		if c, rule, found := findMatch(cands, matchInput{posting: p}); found {
			return &DuplicateError{ID: c.id, Rule: rule}
		}
		date := a.Date
		if date == "" {
			date = t.today()
		}
		var appliedBy, appliedAt any
		if a.Status.Submitted() {
			appliedAt = nullStr(a.AppliedAt)
			if a.Status == Applied {
				appliedBy = string(ByUser)
				if a.AppliedAt == "" {
					appliedAt = t.today()
				}
			}
		}
		now := t.stamp()
		rs, err := tx.ExecContext(ctx, `INSERT INTO tracker_entries
			(date, company, role, status, applied_by, applied_at, posting_url, location, source, identity_key, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			date, strings.TrimSpace(a.Company), strings.TrimSpace(a.Role), string(a.Status), appliedBy, appliedAt,
			canonicalOrRaw(a.URL), strings.TrimSpace(a.Location), a.Source, key, now, now)
		if err != nil {
			return err
		}
		id, err := rs.LastInsertId()
		if err != nil {
			return err
		}
		if err := t.addEvent(ctx, tx, id, ActorUser, EventCreated, "", a.Status, map[string]any{"source": a.Source}); err != nil {
			return err
		}
		out, err = getEntry(ctx, tx, id)
		return err
	})
	return out, err
}
