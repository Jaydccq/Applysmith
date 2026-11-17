package tracker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"applysmith/internal/identity"
	"applysmith/internal/store"
)

// DedupGroup is a set of rows that are one application.
type DedupGroup struct {
	Survivor int64   `json:"survivor"`
	Members  []int64 `json:"members"`
	// Rule says why the rows are grouped: the identity key, or the same
	// company with the same role.
	Rule string `json:"rule"`
	// Status is the status the survivor keeps: the one set by the group's
	// most recent status event.
	Status Status `json:"status"`
	// NeedsUser marks a group the command lists but does not merge.
	NeedsUser bool   `json:"needs_user"`
	Reason    string `json:"reason,omitempty"`
	// Merged is set after a successful --apply.
	Merged bool `json:"merged"`
	// Dismissed marks a group whose exact rows the user said are not
	// duplicates (DismissDuplicates); --apply and the dashboard leave it.
	Dismissed bool `json:"dismissed,omitempty"`
	// Rows are the members as they were read, for a caller that shows them.
	Rows []Entry `json:"-"`
}

const (
	ruleDedupKey  = "identity key"
	ruleDedupRole = "same company and role"
)

// DedupPlan finds duplicate groups without changing anything.
func (t *Tracker) DedupPlan(ctx context.Context) ([]DedupGroup, error) {
	tx, err := t.s.Read().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	p, err := planDedup(ctx, tx)
	return p.groups, err
}

// DedupMerge merges the one group whose rows are exactly members (the
// dashboard's Merge) and returns the kept row with the token UndoMerge takes.
// A changed group is store.ErrConflict; one left to the user is ErrInvalid.
func (t *Tracker) DedupMerge(ctx context.Context, actor Actor, members []int64) (Entry, string, error) {
	if actor != ActorUser {
		return Entry{}, "", fmt.Errorf("%w: only the user merges rows", ErrForbidden)
	}
	members = slices.Sorted(slices.Values(members))
	var kept Entry
	var undo mergeUndo
	err := t.s.Write(ctx, func(tx *sql.Tx) error {
		p, err := planDedup(ctx, tx)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(p.groups, func(g DedupGroup) bool { return slices.Equal(g.Members, members) })
		switch {
		case i < 0:
			return fmt.Errorf("%w: rows %s are no longer one duplicate group", store.ErrConflict, rowList(members))
		case p.groups[i].NeedsUser:
			return fmt.Errorf("%w: %s", ErrInvalid, p.groups[i].Reason)
		}
		if undo, err = t.mergeGroup(ctx, tx, p.groups[i], p, actor); err != nil {
			return err
		}
		kept, err = getEntry(ctx, tx, p.groups[i].Survivor)
		return err
	})
	if err != nil {
		return Entry{}, "", err
	}
	undo.version = kept.Version
	return kept, t.merges.keep(undo, t.now()), nil
}

// notDuplicatesMeta prefixes the workspace_meta key that records one set of
// rows the user said are not duplicates; the rest of the key is their ids.
const notDuplicatesMeta = "tracker.not_duplicates:"

func notDuplicatesKey(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return notDuplicatesMeta + strings.Join(parts, ",")
}

// DismissDuplicates records that the rows of one listed group, exactly
// members, are not duplicates: the group stays out of the dashboard's list and
// of DedupApply until its rows change. undo takes the decision back. A set that
// is no longer one group is store.ErrConflict.
func (t *Tracker) DismissDuplicates(ctx context.Context, actor Actor, members []int64, undo bool) error {
	if actor != ActorUser {
		return fmt.Errorf("%w: only the user decides what is a duplicate", ErrForbidden)
	}
	key := notDuplicatesKey(slices.Sorted(slices.Values(members)))
	return t.s.Write(ctx, func(tx *sql.Tx) error {
		if undo {
			_, err := tx.ExecContext(ctx, `DELETE FROM workspace_meta WHERE key = ?`, key)
			return err
		}
		p, err := planDedup(ctx, tx)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(p.groups, func(g DedupGroup) bool { return notDuplicatesKey(g.Members) == key }) {
			return fmt.Errorf("%w: rows %s are no longer one duplicate group", store.ErrConflict, rowList(members))
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO workspace_meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, t.stamp())
		return err
	})
}

// DedupApply merges every group that does not need the user, in one
// transaction, and returns all groups with Merged set on the ones it merged.
func (t *Tracker) DedupApply(ctx context.Context, actor Actor) ([]DedupGroup, error) {
	if actor != ActorUser {
		return nil, fmt.Errorf("%w: only the user merges rows", ErrForbidden)
	}
	var groups []DedupGroup
	err := t.s.Write(ctx, func(tx *sql.Tx) error {
		p, err := planDedup(ctx, tx)
		if err != nil {
			return err
		}
		groups = p.groups
		for i := range groups {
			if groups[i].NeedsUser || groups[i].Dismissed {
				continue
			}
			if _, err := t.mergeGroup(ctx, tx, groups[i], p, actor); err != nil {
				return err
			}
			groups[i].Merged = true
		}
		return nil
	})
	return groups, err
}

type dedupPlan struct {
	groups  []DedupGroup
	entries map[int64]Entry
}

func planDedup(ctx context.Context, q querier) (dedupPlan, error) {
	all, err := queryEntries(ctx, q, `SELECT `+entryColumns+` FROM tracker_entries ORDER BY id`)
	if err != nil {
		return dedupPlan{}, err
	}
	cands, err := loadCandidates(ctx, q)
	if err != nil {
		return dedupPlan{}, err
	}
	p := dedupPlan{entries: map[int64]Entry{}}
	for _, e := range all {
		p.entries[e.ID] = e
	}

	clusters := clusterRows(cands, func(a, b candidate) bool {
		return identity.SameForDedup(a.posting(), b.posting())
	})
	last, err := lastStatusEvents(ctx, q)
	if err != nil {
		return dedupPlan{}, err
	}
	dismissed := map[string]bool{}
	rows, err := q.QueryContext(ctx, `SELECT key FROM workspace_meta WHERE substr(key, 1, ?) = ?`, len(notDuplicatesMeta), notDuplicatesMeta)
	if err != nil {
		return dedupPlan{}, err
	}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return dedupPlan{}, err
		}
		dismissed[k] = true
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return dedupPlan{}, err
	}
	for _, c := range clusters {
		ids := c.ids()
		if len(ids) < 2 {
			continue
		}
		g := DedupGroup{Members: ids, Rule: ruleDedupKey, Dismissed: dismissed[notDuplicatesKey(ids)]}
		for _, id := range ids {
			g.Rows = append(g.Rows, p.entries[id])
		}
		var reasons []string
		if len(c.units) > 1 {
			g.Rule = ruleDedupRole
		}
		g.Survivor, g.Status = p.chooseSurvivor(ids, last)
		if d := p.conflictingAppliedDates(ids); d != "" {
			reasons = append(reasons, d)
		}
		if d := p.droppedSubmission(ids, g.Status); d != "" {
			reasons = append(reasons, d)
		}
		reasons = append(reasons, c.ambiguous...)
		g.NeedsUser, g.Reason = len(reasons) > 0, strings.Join(reasons, "; ")
		p.groups = append(p.groups, g)
	}
	slices.SortFunc(p.groups, func(a, b DedupGroup) int { return int(a.Members[0] - b.Members[0]) })
	return p, nil
}

// lastStatusEvents maps each row to the time of its latest status-setting
// event.
func lastStatusEvents(ctx context.Context, q querier) (map[int64]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT entry_id, at FROM tracker_events
		WHERE kind IN ('created', 'status_changed', 'status_corrected') AND new_status IS NOT NULL ORDER BY at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var at string
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		out[id] = at
	}
	return out, rows.Err()
}

// betterScore orders scores under the score rules: a final score beats a
// screening one, then the higher wins, and any score beats none.
func betterScore(a, b Entry) bool {
	switch {
	case a.Score == nil:
		return false
	case b.Score == nil:
		return true
	case a.ScoreKind != b.ScoreKind:
		return a.ScoreKind == ScoreFinal
	}
	return tenths(*a.Score) > tenths(*b.Score)
}

// chooseSurvivor takes the status of the row whose status event is most
// recent by calendar day (status order only breaks a same-day tie), then keeps
// the row with that status that has the best score, then the lowest id.
func (p dedupPlan) chooseSurvivor(ids []int64, last map[int64]string) (int64, Status) {
	day := func(id int64) string { return truncate(last[id], 10) }
	lead := ids[0]
	for _, id := range ids[1:] {
		a, b := p.entries[id], p.entries[lead]
		if day(id) > day(lead) || day(id) == day(lead) && a.Status.Order() > b.Status.Order() {
			lead = id
		}
	}
	status := p.entries[lead].Status
	survivor := int64(0)
	for _, id := range ids {
		e := p.entries[id]
		if e.Status != status {
			continue
		}
		if survivor == 0 || betterScore(e, p.entries[survivor]) {
			survivor = id
		}
	}
	return survivor, status
}

func (p dedupPlan) conflictingAppliedDates(ids []int64) string {
	var dates []string
	for _, id := range ids {
		if at := p.entries[id].AppliedAt; at != "" && !slices.Contains(dates, at) {
			dates = append(dates, at)
		}
	}
	if len(dates) > 1 {
		slices.Sort(dates)
		return "different applied histories: " + strings.Join(dates, ", ")
	}
	return ""
}

// droppedSubmission reports a submitted row that would be deleted in favour of
// a survivor that is not submitted: the survivor keeps its status, so the
// application would silently leave the submitted set that the duplicate-apply
// guard reads. The user decides which row is right.
func (p dedupPlan) droppedSubmission(ids []int64, status Status) string {
	if status.Submitted() {
		return ""
	}
	for _, id := range ids {
		if e := p.entries[id]; e.Status.Submitted() {
			return fmt.Sprintf("row #%d was submitted (%s), but the group's latest status is %s", id, e.Status, status)
		}
	}
	return ""
}

// mergeGroup folds the other members into the survivor and deletes them. It
// returns what UndoMerge needs to put them back.
func (t *Tracker) mergeGroup(ctx context.Context, tx *sql.Tx, g DedupGroup, p dedupPlan, actor Actor) (mergeUndo, error) {
	s := p.entries[g.Survivor]
	undo := mergeUndo{survivor: s.ID}
	if err := undo.copyEntries(ctx, tx, g.Members); err != nil {
		return undo, err
	}
	var removed []Entry
	for _, id := range g.Members {
		if id != g.Survivor {
			removed = append(removed, p.entries[id])
		}
	}

	merged := s
	notes := []string{}
	if s.Notes != "" {
		notes = strings.Split(s.Notes, "\n")
	}
	best := s
	appliedFrom := s
	archived := s.ArchivedAt != ""
	for _, r := range removed {
		merged.ResumePDF = merged.ResumePDF || r.ResumePDF
		merged.CoverLetter = merged.CoverLetter || r.CoverLetter
		if r.Date < merged.Date {
			merged.Date = r.Date
		}
		if r.AppliedAt != "" && (appliedFrom.AppliedAt == "" || r.AppliedAt < appliedFrom.AppliedAt) {
			appliedFrom = r
		}
		if merged.Location == "" {
			merged.Location = r.Location
		}
		if merged.PostingURL == "" {
			merged.PostingURL = r.PostingURL
		}
		if betterScore(r, best) {
			best = r
		}
		archived = archived && r.ArchivedAt != ""
		if r.Notes != "" {
			for _, line := range strings.Split(r.Notes, "\n") {
				notes = append(notes, fmt.Sprintf("#%d %s", r.ID, line))
			}
		}
	}
	merged.AppliedAt = appliedFrom.AppliedAt
	if merged.AppliedBy == "" {
		merged.AppliedBy = appliedFrom.AppliedBy
	}
	if best.ID != s.ID {
		merged.Score, merged.ScoreKind = best.Score, best.ScoreKind
		if best.ReportNumber != nil {
			merged.ReportNumber = best.ReportNumber
		}
	}
	for _, r := range removed {
		if merged.ReportNumber == nil {
			merged.ReportNumber = r.ReportNumber
		}
	}
	if len(notes) > maxNotes {
		notes = notes[len(notes)-maxNotes:]
	}
	merged.Notes = strings.Join(notes, "\n")
	if !archived {
		merged.ArchivedAt = ""
	}

	var removedIDs []int64
	var removedKeys []string
	removedReports := []int64{}
	for _, r := range removed {
		removedIDs = append(removedIDs, r.ID)
		if r.ReportNumber != nil {
			removedReports = append(removedReports, *r.ReportNumber)
		}
		removedKeys = append(removedKeys, r.IdentityKey)
		for _, l := range t.pointers() {
			ids, err := rowids(ctx, tx, l, r.ID)
			if err != nil {
				return undo, err
			}
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET %s = ? WHERE %s = ?`, l.Table, l.Column, l.Column), s.ID, r.ID); err != nil {
				return undo, err
			}
			undo.moved = append(undo.moved, movedRows{LinkedTable: l, to: r.ID, rowids: ids})
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM tracker_entries WHERE id = ?`, r.ID); err != nil {
			return undo, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tracker_aliases (identity_key, entry_id) VALUES (?, ?)`, r.IdentityKey, s.ID); err != nil {
			return undo, err
		}
	}
	undo.keys = removedKeys
	_, err := tx.ExecContext(ctx, `UPDATE tracker_entries SET date = ?, score = ?, score_kind = ?, applied_by = ?, applied_at = ?, resume_pdf = ?, cover_letter = ?,
		report_number = ?, notes = ?, posting_url = ?, location = ?, archived_at = ?, version = version + 1, updated_at = ? WHERE id = ?`,
		merged.Date, scoreArg(merged.Score), nullStr(string(merged.ScoreKind)), nullStr(string(merged.AppliedBy)), nullStr(merged.AppliedAt),
		boolInt(merged.ResumePDF), boolInt(merged.CoverLetter), nullInt(merged.ReportNumber), merged.Notes, merged.PostingURL, merged.Location,
		nullStr(merged.ArchivedAt), t.stamp(), s.ID)
	if err != nil {
		return undo, err
	}
	err = t.addEvent(ctx, tx, s.ID, actor, EventMerged, "", "", map[string]any{
		"removed": removedIDs, "removed_keys": removedKeys, "removed_reports": removedReports, "rule": g.Rule,
	})
	if err == nil {
		err = tx.QueryRowContext(ctx, `SELECT max(id) FROM tracker_events WHERE entry_id = ?`, s.ID).Scan(&undo.event)
	}
	return undo, err
}

// MergeUndoWindow is how long a merge can be taken back. What it removed is
// held in the server's memory only (the rows verbatim and which events,
// aliases and links moved, at most mergeUndoKept merges), so a restart or
// the window's end makes the merge final, as the CLI's dedup --apply is.
const MergeUndoWindow = 15 * time.Minute

const mergeUndoKept = 20

// mergeUndo is one merge, held for its undo.
type mergeUndo struct {
	at       time.Time
	survivor int64
	version  int // the version the merge gave the survivor
	event    int64
	// cols name the columns of tracker_entries; kept and removed are the
	// members' rows as they were, verbatim.
	cols    []string
	kept    []any
	removed [][]any
	keys    []string
	moved   []movedRows
}

// movedRows are the rows of one pointing table a merge moved off row to.
type movedRows struct {
	LinkedTable
	to     int64
	rowids []int64
}

type mergeTrash struct {
	mu sync.Mutex
	m  map[string]mergeUndo
}

// pointers are the tables whose rows point at a tracker row.
func (t *Tracker) pointers() []LinkedTable {
	return append([]LinkedTable{{Table: "tracker_events", Column: "entry_id"}, {Table: "tracker_aliases", Column: "entry_id"}}, t.linked...)
}

// copyEntries reads the group's rows verbatim into u, the survivor's apart.
func (u *mergeUndo) copyEntries(ctx context.Context, tx *sql.Tx, ids []int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT * FROM tracker_entries WHERE id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`) ORDER BY id`, anys(ids)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	if u.cols, err = rows.Columns(); err != nil {
		return err
	}
	at := slices.Index(u.cols, "id")
	for rows.Next() {
		vals := make([]any, len(u.cols))
		ptrs := make([]any, len(vals))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		if vals[at] == u.survivor {
			u.kept = vals
		} else {
			u.removed = append(u.removed, vals)
		}
	}
	return rows.Err()
}

func rowids(ctx context.Context, tx *sql.Tx, l LinkedTable, id int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT rowid FROM %s WHERE %s = ?`, l.Table, l.Column), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func anys(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

func (m *mergeTrash) keep(u mergeUndo, now time.Time) string {
	u.at = now
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m = map[string]mergeUndo{}
	}
	oldest := ""
	for k, v := range m.m {
		if now.Sub(v.at) > MergeUndoWindow {
			delete(m.m, k)
		} else if oldest == "" || v.at.Before(m.m[oldest].at) {
			oldest = k
		}
	}
	if len(m.m) >= mergeUndoKept {
		delete(m.m, oldest)
	}
	token := fmt.Sprintf("merge-%d-%d", u.event, now.UnixNano())
	m.m[token] = u
	return token
}

// UndoMerge puts back the rows the merge behind token removed, with their
// events, aliases and links, and the kept row as it was before, then records
// the undo on the kept row. It is store.ErrNotFound once the window has passed
// and store.ErrConflict when the kept row changed since the merge.
func (t *Tracker) UndoMerge(ctx context.Context, actor Actor, token string) (Entry, error) {
	if actor != ActorUser {
		return Entry{}, fmt.Errorf("%w: only the user merges rows", ErrForbidden)
	}
	t.merges.mu.Lock()
	u, ok := t.merges.m[token]
	t.merges.mu.Unlock()
	if !ok || t.now().Sub(u.at) > MergeUndoWindow {
		return Entry{}, fmt.Errorf("%w: this merge can no longer be undone; a merge is kept for %v and not across a restart", store.ErrNotFound, MergeUndoWindow)
	}
	var kept Entry
	err := t.s.Write(ctx, func(tx *sql.Tx) error {
		cur, err := getEntry(ctx, tx, u.survivor)
		if err != nil || cur.Version != u.version {
			return fmt.Errorf("%w: row %d changed since the merge, so it was left as it is", store.ErrConflict, u.survivor)
		}
		for _, k := range u.keys {
			if _, err := tx.ExecContext(ctx, `DELETE FROM tracker_aliases WHERE identity_key = ? AND entry_id = ?`, k, u.survivor); err != nil {
				return err
			}
		}
		// The kept row first, so the report number it took is free again.
		sets, args := []string{}, []any{}
		for i, c := range u.cols {
			if c != "id" && c != "version" && c != "updated_at" {
				sets = append(sets, c+" = ?")
				args = append(args, u.kept[i])
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tracker_entries SET `+strings.Join(sets, ", ")+`, version = version + 1, updated_at = ? WHERE id = ?`, append(args, t.stamp(), u.survivor)...); err != nil {
			return err
		}
		marks := strings.TrimSuffix(strings.Repeat("?,", len(u.cols)), ",")
		for _, vals := range u.removed {
			if _, err := tx.ExecContext(ctx, `INSERT INTO tracker_entries (`+strings.Join(u.cols, ", ")+`) VALUES (`+marks+`)`, vals...); err != nil {
				return fmt.Errorf("%w: a removed row could not be put back: %v", store.ErrConflict, err)
			}
		}
		for _, m := range u.moved {
			if len(m.rowids) == 0 {
				continue
			}
			q := fmt.Sprintf(`UPDATE %s SET %s = ? WHERE %s = ? AND rowid IN (%s)`, m.Table, m.Column, m.Column, strings.TrimSuffix(strings.Repeat("?,", len(m.rowids)), ","))
			if _, err := tx.ExecContext(ctx, q, append([]any{m.to, u.survivor}, anys(m.rowids)...)...); err != nil {
				return err
			}
		}
		if err := t.addEvent(ctx, tx, u.survivor, actor, EventMerged, "", "", map[string]any{"undoes": u.event}); err != nil {
			return err
		}
		kept, err = getEntry(ctx, tx, u.survivor)
		return err
	})
	if err == nil {
		t.merges.mu.Lock()
		delete(t.merges.m, token)
		t.merges.mu.Unlock()
	}
	return kept, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
