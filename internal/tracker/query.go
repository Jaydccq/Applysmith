package tracker

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"modernc.org/sqlite"

	"applysmith/internal/identity"
)

// noteText is the lower-cased text of every note line without the prefix
// appendNote puts before it (date and actor; a merge adds the folded row's id
// and a hash before that). Search must not see the prefix: the actor names are
// words a person searches for. This is the one place the notes column is read
// for search, and it covers every note a row holds, whichever path wrote it (an
// event exists for only some).
func noteText(notes string) string {
	var b strings.Builder
	for _, line := range strings.Split(notes, "\n") {
		b.WriteString(strings.ToLower(noteBody(line)))
		b.WriteByte('\n')
	}
	return b.String()
}

// noteBody is the line without its prefix, or the whole line when it has none.
func noteBody(line string) string {
	rest := line
	if strings.HasPrefix(rest, "#") {
		if i := strings.IndexByte(rest, ' '); i > 0 {
			rest = rest[i+1:]
		}
	}
	if len(rest) < 12 || rest[4] != '-' || rest[7] != '-' || rest[10] != ' ' {
		return line
	}
	if i := strings.Index(rest[11:], ": "); i >= 0 && i <= 40 {
		return rest[11+i+2:]
	}
	return line
}

func init() {
	sqlite.MustRegisterDeterministicScalarFunction("tracker_note_text", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		s, _ := args[0].(string)
		return noteText(s), nil
	})
}

// Sort orders for List.
const (
	SortUpdatedDesc = "updated_desc"
	SortScoreDesc   = "score_desc"
	SortDateDesc    = "date_desc"
	SortCompanyAsc  = "company_asc"
	SortRoleAsc     = "role_asc"
	SortStatusAsc   = "status_asc" // stage order: evaluated first, discarded last
)

// maxAppliedWithinDays bounds the application-date window.
const maxAppliedWithinDays = 3650

// sortOrders maps each sort to its ORDER BY; every one ends in id, so a page
// boundary never reshuffles tied rows.
var sortOrders = map[string]string{
	SortUpdatedDesc: "updated_at DESC, id DESC",
	SortScoreDesc:   "score IS NULL, score DESC, id DESC",
	SortDateDesc:    "date DESC, id DESC",
	SortCompanyAsc:  "lower(company), id DESC",
	SortRoleAsc:     "lower(role), lower(company), id DESC",
	SortStatusAsc:   statusRankSQL() + ", updated_at DESC, id DESC",
}

// statusRankSQL ranks a row's status by its place in the status table.
func statusRankSQL() string {
	var b strings.Builder
	b.WriteString("CASE status")
	for i, s := range Statuses() {
		fmt.Fprintf(&b, " WHEN '%s' THEN %d", s, i)
	}
	b.WriteString(" END")
	return b.String()
}

const (
	defaultPageSize = 25
	maxPageSize     = 100
	maxQueryChars   = 200
)

// ListQuery filters and pages the rows. The zero value lists the first page of
// non-archived rows, most recently updated first.
type ListQuery struct {
	Status Status
	// Statuses keeps the rows in any of these statuses; with Status set, a row
	// must satisfy both.
	Statuses []Status
	Company  string   // case-insensitive substring
	Source   string   // case-insensitive substring of the row's source
	Query    string   // free text over company and role, then note text
	MinScore *float64 // inclusive
	// ScoreBelow is an exclusive upper bound; ScoreKind and AppliedBy match
	// exactly. Rows without a score never satisfy a score bound.
	ScoreBelow *float64
	ScoreKind  ScoreKind
	AppliedBy  AppliedBy
	// AppliedWithinDays keeps the rows applied to on or after that many days
	// before today (the tracker clock's calendar day); a row with no
	// application date never matches. since is that day, set by prepare.
	AppliedWithinDays int
	since             string
	// Reached keeps the rows that moved to one of these statuses (created in
	// it, changed or corrected to it), MovedBy those this actor moved, on or
	// after the local day ReachedSince, YYYY-MM-DD: the rows the Home and
	// funnel counts of those statuses, and Home's Gmail line, count.
	Reached      []Status
	MovedBy      Actor
	ReachedSince string
	reachedFrom  string
	Archived     bool // include archived rows
	// Keys narrows the rows by identity key (the row's own or an alias).
	Keys *KeyFilter
	// IDs keeps only these rows when ByID is set; an empty set keeps nothing.
	IDs      []int64
	ByID     bool
	Sort     string
	Page     int // 1-based
	PageSize int
}

// KeyFilter keeps the rows whose identity key is in Keys, or with Exclude the
// rows whose key is not. An empty set keeps nothing, or everything when it
// excludes.
type KeyFilter struct {
	Keys    []string
	Exclude bool
}

// ParseStatuses reads a comma-separated list of statuses; empty text is none.
func ParseStatuses(text string) ([]Status, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	var out []Status
	for _, part := range strings.Split(text, ",") {
		s, err := ParseStatus(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// prepare validates the query and fixes the application-date window to the
// tracker clock's today.
func (t *Tracker) prepare(q ListQuery) (ListQuery, error) {
	q, err := q.validate()
	if err != nil {
		return q, err
	}
	if q.AppliedWithinDays > 0 {
		q.since = t.now().AddDate(0, 0, -q.AppliedWithinDays).Format(dateLayout)
	}
	if len(q.Reached) > 0 || q.MovedBy != "" {
		d, err := time.ParseInLocation(dateLayout, q.ReachedSince, t.now().Location())
		if err != nil {
			return q, fmt.Errorf("%w: since must be a date, YYYY-MM-DD", ErrInvalid)
		}
		q.reachedFrom = d.UTC().Format(time.RFC3339)
	}
	return q, nil
}

func (q ListQuery) validate() (ListQuery, error) {
	for _, s := range slices.Concat([]Status{q.Status}, q.Reached, q.Statuses) {
		if _, err := ParseStatus(string(s)); s != "" && err != nil {
			return q, err
		}
	}
	if q.MovedBy != "" && !q.MovedBy.valid() {
		return q, fmt.Errorf("%w: movedBy must be user, gmail, apply_chain, agent or evaluation", ErrInvalid)
	}
	if len(q.Reached) == 0 && q.MovedBy == "" && q.ReachedSince != "" {
		return q, fmt.Errorf("%w: since needs reached or movedBy", ErrInvalid)
	}
	if q.AppliedWithinDays < 0 || q.AppliedWithinDays > maxAppliedWithinDays {
		return q, fmt.Errorf("%w: appliedWithin must be 1 to %d days", ErrInvalid, maxAppliedWithinDays)
	}
	if len([]rune(q.Source)) > maxQueryChars {
		return q, fmt.Errorf("%w: source holds at most %d characters", ErrInvalid, maxQueryChars)
	}
	if len([]rune(q.Query)) > maxQueryChars {
		return q, fmt.Errorf("%w: query holds at most %d characters", ErrInvalid, maxQueryChars)
	}
	if q.Sort == "" {
		q.Sort = SortUpdatedDesc
	}
	if _, ok := sortOrders[q.Sort]; !ok {
		return q, fmt.Errorf("%w: sort must be %s, %s, %s, %s, %s or %s", ErrInvalid, SortUpdatedDesc, SortScoreDesc, SortDateDesc, SortCompanyAsc, SortRoleAsc, SortStatusAsc)
	}
	if q.Page == 0 {
		q.Page = 1
	}
	if q.PageSize == 0 {
		q.PageSize = defaultPageSize
	}
	if q.Page < 1 || q.PageSize < 1 || q.PageSize > maxPageSize {
		return q, fmt.Errorf("%w: page must be 1 or more and pageSize 1 to %d", ErrInvalid, maxPageSize)
	}
	return q, nil
}

// queryWords splits free text into words of at least two characters.
func queryWords(text string) []string {
	var out []string
	for _, w := range strings.Fields(strings.ToLower(text)) {
		if len([]rune(w)) >= 2 {
			out = append(out, w)
		}
	}
	return out
}

// where builds the one filter every count and list uses.
func (q ListQuery) where() (clause string, args []any, rank string, rankArgs []any) {
	conds := []string{"1 = 1"}
	if !q.Archived {
		conds = append(conds, "archived_at IS NULL")
	}
	if q.Status != "" {
		conds = append(conds, "status = ?")
		args = append(args, string(q.Status))
	}
	if len(q.Statuses) > 0 {
		conds = append(conds, "status IN ("+strings.TrimSuffix(strings.Repeat("?,", len(q.Statuses)), ",")+")")
		for _, st := range q.Statuses {
			args = append(args, string(st))
		}
	}
	if q.since != "" {
		conds = append(conds, "applied_at >= ?")
		args = append(args, q.since)
	}
	if q.reachedFrom != "" {
		cond := "id IN (SELECT entry_id FROM tracker_events WHERE kind IN (?, ?, ?) AND at >= ?"
		args = append(args, string(EventCreated), string(EventStatusChanged), string(EventStatusCorrected), q.reachedFrom)
		if len(q.Reached) > 0 {
			cond += " AND new_status IN (" + strings.TrimSuffix(strings.Repeat("?,", len(q.Reached)), ",") + ")"
			for _, st := range q.Reached {
				args = append(args, string(st))
			}
		}
		if q.MovedBy != "" {
			cond += " AND actor = ?"
			args = append(args, string(q.MovedBy))
		}
		conds = append(conds, cond+")")
	}
	if src := strings.TrimSpace(q.Source); src != "" {
		conds = append(conds, "instr(lower(ifnull(source, '')), lower(?)) > 0")
		args = append(args, src)
	}
	if c := strings.TrimSpace(q.Company); c != "" {
		conds = append(conds, "instr(lower(company), lower(?)) > 0")
		args = append(args, c)
	}
	if q.MinScore != nil {
		conds = append(conds, "score >= ?")
		args = append(args, *q.MinScore)
	}
	if q.ScoreBelow != nil {
		conds = append(conds, "score < ?")
		args = append(args, *q.ScoreBelow)
	}
	if q.ScoreKind != "" {
		conds = append(conds, "score_kind = ?")
		args = append(args, string(q.ScoreKind))
	}
	if q.AppliedBy != "" {
		conds = append(conds, "applied_by = ?")
		args = append(args, string(q.AppliedBy))
	}
	if q.ByID {
		conds = append(conds, "id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(q.IDs)), ",")+")")
		for _, id := range q.IDs {
			args = append(args, id)
		}
	}
	if f := q.Keys; f != nil {
		switch {
		case len(f.Keys) == 0 && !f.Exclude:
			conds = append(conds, "0 = 1")
		case len(f.Keys) > 0:
			marks := strings.TrimSuffix(strings.Repeat("?,", len(f.Keys)), ",")
			cond := "(identity_key IN (" + marks + ") OR id IN (SELECT entry_id FROM tracker_aliases WHERE identity_key IN (" + marks + ")))"
			if f.Exclude {
				cond = "NOT " + cond
			}
			conds = append(conds, cond)
			for range 2 {
				for _, k := range f.Keys {
					args = append(args, k)
				}
			}
		}
	}
	rank = "0"
	if words := queryWords(q.Query); len(words) > 0 {
		// A word in the company or role outweighs the note matches of every
		// word together, so a note-only row never outranks a named one.
		parts := make([]string, len(words))
		for i, w := range words {
			parts[i] = fmt.Sprintf("((instr(lower(company || ' ' || role), ?) > 0) * %d + (instr(tracker_note_text(notes), ?) > 0))", len(words)+1)
			rankArgs = append(rankArgs, w, w)
		}
		rank = strings.Join(parts, " + ")
		conds = append(conds, "("+rank+") > 0")
		args = append(args, rankArgs...)
	}
	return strings.Join(conds, " AND "), args, rank, rankArgs
}

// countEntries is the one count behind both the list total and the summary.
func countEntries(ctx context.Context, q querier, lq ListQuery) (int, error) {
	clause, args, _, _ := lq.where()
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM tracker_entries WHERE `+clause, args...).Scan(&n)
	return n, err
}

// Page is one page of rows with the total behind it.
type Page struct {
	Entries  []Entry `json:"entries"`
	Page     int     `json:"page"`
	PageSize int     `json:"pageSize"`
	Total    int     `json:"total"`
	// Archived counts the archived rows the filter also matches when it
	// leaves them out.
	Archived int `json:"archived,omitempty"`
}

// List returns one page of rows.
func (t *Tracker) List(ctx context.Context, q ListQuery) (Page, error) {
	q, err := t.prepare(q)
	if err != nil {
		return Page{}, err
	}
	// One read transaction, so the page and the total describe the same rows.
	db, err := t.s.Read().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Page{}, err
	}
	defer db.Rollback()
	total, err := countEntries(ctx, db, q)
	if err != nil {
		return Page{}, err
	}
	archived := 0
	if !q.Archived {
		all := q
		all.Archived = true
		if archived, err = countEntries(ctx, db, all); err != nil {
			return Page{}, err
		}
		archived -= total
	}
	clause, args, rank, rankArgs := q.where()
	order := sortOrders[q.Sort]
	sqlText := `SELECT ` + entryColumns + ` FROM tracker_entries WHERE ` + clause
	if rank != "0" {
		sqlText += ` ORDER BY (` + rank + `) DESC, ` + order
		args = append(args, rankArgs...)
	} else {
		sqlText += ` ORDER BY ` + order
	}
	sqlText += ` LIMIT ? OFFSET ?`
	args = append(args, q.PageSize, (q.Page-1)*q.PageSize)
	entries, err := queryEntries(ctx, db, sqlText, args...)
	return Page{Entries: entries, Page: q.Page, PageSize: q.PageSize, Total: total, Archived: archived}, err
}

func queryEntries(ctx context.Context, q querier, text string, args ...any) ([]Entry, error) {
	rows, err := q.QueryContext(ctx, text, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Summary counts non-archived rows per status with the filter List uses, so a
// count and the list it links to agree.
func (t *Tracker) Summary(ctx context.Context) (map[Status]int, error) {
	return t.SummaryOf(ctx, false)
}

// SummaryOf is Summary with the archived rows counted too when archived is
// set, for a list that includes them.
func (t *Tracker) SummaryOf(ctx context.Context, archived bool) (map[Status]int, error) {
	db, err := t.s.Read().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer db.Rollback()
	out := map[Status]int{}
	for _, s := range Statuses() {
		n, err := countEntries(ctx, db, ListQuery{Status: s, Archived: archived})
		if err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, nil
}

// Detail is a row with its events in time order and its aliases.
type Detail struct {
	Entry   Entry    `json:"entry"`
	Events  []Event  `json:"events"`
	Aliases []string `json:"aliases"`
}

// Get returns a row with its events.
func (t *Tracker) Get(ctx context.Context, id int64) (Detail, error) {
	tx, err := t.s.Read().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Detail{}, err
	}
	defer tx.Rollback()
	e, err := getEntry(ctx, tx, id)
	if err != nil {
		return Detail{}, err
	}
	events, err := eventsOf(ctx, tx, id)
	if err != nil {
		return Detail{}, err
	}
	arows, err := tx.QueryContext(ctx, `SELECT identity_key FROM tracker_aliases WHERE entry_id = ? ORDER BY identity_key`, id)
	if err != nil {
		return Detail{}, err
	}
	defer arows.Close()
	aliases := []string{}
	for arows.Next() {
		var k string
		if err := arows.Scan(&k); err != nil {
			return Detail{}, err
		}
		aliases = append(aliases, k)
	}
	return Detail{Entry: e, Events: events, Aliases: aliases}, arows.Err()
}

func eventsOf(ctx context.Context, q querier, id int64) ([]Event, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, entry_id, at, actor, kind, previous_status, new_status, detail_json FROM tracker_events WHERE entry_id = ? ORDER BY at, id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var ev Event
		var prev, next sql.NullString
		var detail string
		if err := rows.Scan(&ev.ID, &ev.EntryID, &ev.At, &ev.Actor, &ev.Kind, &prev, &next, &detail); err != nil {
			return nil, err
		}
		ev.PreviousStatus, ev.NewStatus, ev.Detail = Status(prev.String), Status(next.String), json.RawMessage(detail)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// HistoryItem is one status the posting's row held.
type HistoryItem struct {
	Status Status `json:"status"`
	At     string `json:"at"`
	Actor  Actor  `json:"actor"`
}

// LookupResult answers an application lookup by URL.
type LookupResult struct {
	Found       bool          `json:"found"`
	Entry       *Entry        `json:"entry,omitempty"`
	IdentityKey string        `json:"identityKey,omitempty"`
	History     []HistoryItem `json:"history,omitempty"`
}

const maxLookupURL = 2048

// Lookup finds the row for a posting URL by identity key or alias only, never
// by company and role, because a crossed answer would bind documents to the
// wrong employer. It reads and writes nothing else.
func (t *Tracker) Lookup(ctx context.Context, rawURL string) (LookupResult, error) {
	u, err := url.Parse(rawURL)
	if len(rawURL) > maxLookupURL || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return LookupResult{}, fmt.Errorf("%w: lookup needs an absolute http or https URL of at most %d characters", ErrInvalid, maxLookupURL)
	}
	key, err := identity.URLKey(rawURL)
	if err != nil {
		return LookupResult{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	db := t.s.Read()
	var id int64
	err = db.QueryRowContext(ctx, `SELECT id FROM tracker_entries WHERE identity_key = ?
		UNION SELECT entry_id FROM tracker_aliases WHERE identity_key = ? ORDER BY 1 LIMIT 1`, key, key).Scan(&id)
	if err == sql.ErrNoRows {
		return LookupResult{}, nil
	}
	if err != nil {
		return LookupResult{}, err
	}
	e, err := getEntry(ctx, db, id)
	if err != nil {
		return LookupResult{}, err
	}
	events, err := eventsOf(ctx, db, id)
	if err != nil {
		return LookupResult{}, err
	}
	var history []HistoryItem
	for _, ev := range events {
		switch ev.Kind {
		case EventCreated, EventStatusChanged, EventStatusCorrected:
			if ev.NewStatus != "" {
				history = append(history, HistoryItem{Status: ev.NewStatus, At: ev.At, Actor: ev.Actor})
			}
		}
	}
	return LookupResult{Found: true, Entry: &e, IdentityKey: key, History: history}, nil
}
