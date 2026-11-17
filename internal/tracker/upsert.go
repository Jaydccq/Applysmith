package tracker

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"applysmith/internal/identity"
)

// Outcome is what the evaluation decided about the posting.
type Outcome string

// The outcomes that decide a new row's status and whether a newer report may
// replace a skipped row.
const (
	OutcomeRecommended Outcome = "recommended" // evaluated
	OutcomeReview      Outcome = "review"      // evaluated, with a note (screening review)
	OutcomeSkip        Outcome = "skip"        // skipped (recommendation skip)
	OutcomeGated       Outcome = "gated"       // skipped, no score
	OutcomeDrop        Outcome = "drop"        // skipped (screening drop)
)

func (o Outcome) skipped() bool { return o == OutcomeSkip || o == OutcomeGated || o == OutcomeDrop }

// Report is what the evaluation capability hands the tracker.
type Report struct {
	Number  int64
	Date    string // ISO date of the report
	Posting identity.Posting
	Score   *float64
	Kind    ScoreKind
	Outcome Outcome
	// Note is recorded on a new row (the gate rules for a gated posting).
	Note string
	// EntryID names a row the caller already knows (rule 2).
	EntryID int64
	// Aliases are the keys of the addresses the evaluation reached the posting
	// through (a tracking link, a job board's listing): rule 3 matches them,
	// and the row keeps them as aliases.
	Aliases []string
	// Simulated marks a report the offline simulation wrote, which no model
	// read: it gives the row a score but never its status, so a new row is
	// Evaluated whatever the outcome and a skipped row is not revived.
	Simulated bool
}

// UpsertResult says what the upsert did.
type UpsertResult struct {
	Entry   Entry
	Created bool
	// Rule is the duplicate rule that attached the report, empty for a new row.
	Rule string
}

func (r Report) validate() (score *float64, err error) {
	if r.Number <= 0 {
		return nil, fmt.Errorf("%w: a report number is required", ErrInvalid)
	}
	if !validDate(r.Date) {
		return nil, fmt.Errorf("%w: report date %q is not an ISO date", ErrInvalid, r.Date)
	}
	switch r.Outcome {
	case OutcomeRecommended, OutcomeReview, OutcomeSkip, OutcomeGated, OutcomeDrop:
	default:
		return nil, fmt.Errorf("%w: unknown outcome %q", ErrInvalid, r.Outcome)
	}
	if err := checkNames(r.Posting.Company, r.Posting.Role); err != nil {
		return nil, err
	}
	if r.Score == nil || r.Outcome == OutcomeGated {
		return nil, nil
	}
	v, err := checkScore(*r.Score, r.Kind)
	return &v, err
}

func checkNames(company, role string) error {
	if strings.TrimSpace(company) == "" || strings.TrimSpace(role) == "" {
		return fmt.Errorf("%w: company and role must not be blank", ErrInvalid)
	}
	return nil
}

// UpsertReportIn records a finished report as the evaluation actor, inside
// the caller's transaction, so the evaluation commits its report index row
// and the tracker row together: it creates a row, or attaches the report to
// the row the duplicate rules find and applies the score rules.
func (t *Tracker) UpsertReportIn(ctx context.Context, tx *sql.Tx, r Report) (UpsertResult, error) {
	score, err := r.validate()
	if err != nil {
		return UpsertResult{}, err
	}
	p := r.Posting
	key, err := rowKey(p)
	if err != nil {
		return UpsertResult{}, err
	}
	p.Key = key
	cands, err := loadCandidates(ctx, tx)
	if err != nil {
		return UpsertResult{}, err
	}
	c, rule, found := findMatch(cands, matchInput{report: r.Number, entryID: r.EntryID, posting: p, aliases: r.Aliases})
	if !found {
		return t.createFromReport(ctx, tx, r, p, score)
	}
	return t.attachReport(ctx, tx, c, rule, r, p, score)
}

// addAliases records keys as aliases of row id, each unless a row already
// holds it, and says whether one was added.
func addAliases(ctx context.Context, tx *sql.Tx, id int64, keys ...string) (bool, error) {
	added := false
	for _, k := range keys {
		res, err := tx.ExecContext(ctx, `INSERT INTO tracker_aliases (identity_key, entry_id) SELECT ?1, ?2
			WHERE NOT EXISTS (SELECT 1 FROM tracker_aliases WHERE identity_key = ?1) AND NOT EXISTS (SELECT 1 FROM tracker_entries WHERE identity_key = ?1)`, k, id)
		if err != nil {
			return added, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added = true
		}
	}
	return added, nil
}

// moveToPosting moves a row the report reached its posting from (the row's key
// is one of the report's aliases: its URL was the tracking link or the listing
// the evaluation followed) to the report's posting: the row's URL and key
// become the report's. Its old key, one of the report's aliases, stays with it
// when attachReport adds them. A key another row holds is not taken from it.
func (t *Tracker) moveToPosting(ctx context.Context, tx *sql.Tx, e Entry, r Report, p identity.Posting) (bool, error) {
	if p.Key == e.IdentityKey || strings.TrimSpace(p.URL) == "" || !slices.Contains(r.Aliases, e.IdentityKey) {
		return false, nil
	}
	var held int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM tracker_entries WHERE identity_key = ?1 AND id <> ?2)
		+ (SELECT count(*) FROM tracker_aliases WHERE identity_key = ?1 AND entry_id <> ?2)`, p.Key, e.ID).Scan(&held); err != nil || held > 0 {
		return false, err
	}
	url := canonicalOrRaw(p.URL)
	if _, err := tx.ExecContext(ctx, `DELETE FROM tracker_aliases WHERE identity_key = ? AND entry_id = ?`, p.Key, e.ID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tracker_entries SET posting_url = ?, identity_key = ?, version = version + 1, updated_at = ? WHERE id = ?`,
		url, p.Key, t.stamp(), e.ID); err != nil {
		return false, err
	}
	detail := map[string]any{"text": "posting address", "report_number": r.Number, "old_url": e.PostingURL, "url": url}
	return true, t.addEvent(ctx, tx, e.ID, ActorEvaluation, EventNoteAdded, "", "", detail)
}

func (t *Tracker) createFromReport(ctx context.Context, tx *sql.Tx, r Report, p identity.Posting, score *float64) (UpsertResult, error) {
	status := Evaluated
	if r.Outcome.skipped() && !r.Simulated {
		status = Skipped
	}
	if d := DecideNew(ActorEvaluation, status); d.Verdict != Allow {
		return UpsertResult{}, fmt.Errorf("%w: %s", ErrForbidden, d.Reason)
	}
	note := strings.TrimSpace(r.Note)
	switch {
	case r.Outcome == OutcomeReview && note == "":
		note = "Screening: needs your review"
	case r.Outcome == OutcomeGated && note == "":
		note = "Gated"
	}
	notes := ""
	if note != "" {
		notes = appendNote("", t.today(), ActorEvaluation, truncate(note, maxNoteChars))
	}
	var kind any
	if score != nil {
		kind = string(r.Kind)
	}
	now := t.stamp()
	rs, err := tx.ExecContext(ctx, `INSERT INTO tracker_entries
		(date, company, role, score, score_kind, status, report_number, notes, posting_url, location, source, identity_key, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Date, strings.TrimSpace(p.Company), strings.TrimSpace(p.Role), scoreArg(score), kind, string(status), r.Number, notes,
		canonicalOrRaw(p.URL), strings.TrimSpace(p.Location), nullStr(strings.TrimSpace(p.Source)), p.Key, now, now)
	if err != nil {
		return UpsertResult{}, err
	}
	id, err := rs.LastInsertId()
	if err != nil {
		return UpsertResult{}, err
	}
	detail := map[string]any{"report_number": r.Number, "outcome": string(r.Outcome)}
	if err := t.addEvent(ctx, tx, id, ActorEvaluation, EventCreated, "", status, detail); err != nil {
		return UpsertResult{}, err
	}
	if _, err := addAliases(ctx, tx, id, r.Aliases...); err != nil {
		return UpsertResult{}, err
	}
	e, err := getEntry(ctx, tx, id)
	return UpsertResult{Entry: e, Created: true}, err
}

func scoreArg(s *float64) any {
	if s == nil {
		return nil
	}
	return *s
}

func canonicalOrRaw(raw string) string {
	if c, err := identity.CanonicalURL(raw); err == nil {
		return c
	}
	return strings.TrimSpace(raw)
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// planScore applies the score rules, in their order, for one report on one
// row. revive is true when a skipped row is replaced by a newer report whose
// recommendation is not skip and which is not simulated; it needs canRevive
// (the row is not archived).
func planScore(e Entry, r Report, score *float64, canRevive bool) (replace, revive bool) {
	if e.Status == Skipped && !r.Outcome.skipped() && !r.Simulated && (e.ReportNumber == nil || r.Number > *e.ReportNumber) && canRevive {
		return true, true
	}
	switch {
	case score == nil:
		return false, false
	case e.Score == nil:
		return true, false
	case r.Kind == ScoreFinal && e.ScoreKind == ScoreScreening:
		return true, false
	case r.Kind == ScoreScreening && e.ScoreKind == ScoreFinal:
		return false, false
	case r.Kind == e.ScoreKind && tenths(*score) > tenths(*e.Score):
		return true, false
	}
	return false, false
}

func reportOf(e Entry) int64 {
	if e.ReportNumber == nil {
		return 0
	}
	return *e.ReportNumber
}

func fmtScore(s *float64, k ScoreKind) string {
	if s == nil {
		return "none"
	}
	return fmt.Sprintf("%.1f %s", *s, k)
}

// attachReport applies a report to the row the duplicate rules found.
func (t *Tracker) attachReport(ctx context.Context, tx *sql.Tx, c candidate, rule string, r Report, p identity.Posting, score *float64) (UpsertResult, error) {
	e, err := getEntry(ctx, tx, c.id)
	if err != nil {
		return UpsertResult{}, err
	}
	dirty := false

	linked, _ := planScore(e, r, score, e.ArchivedAt == "")
	linked = linked || e.ReportNumber == nil || reportOf(e) == r.Number
	renamed, err := t.refreshNames(ctx, tx, e, rule, linked, r, p)
	if err != nil {
		return UpsertResult{}, err
	}
	if renamed.ID != e.ID || renamed.IdentityKey != e.IdentityKey || renamed.Company != e.Company || renamed.Role != e.Role {
		e = renamed
		if c, err = candidateOf(ctx, tx, e.ID); err != nil {
			return UpsertResult{}, err
		}
	}
	if moved, err := t.moveToPosting(ctx, tx, e, r, p); err != nil {
		return UpsertResult{}, err
	} else if moved {
		if e, err = getEntry(ctx, tx, e.ID); err != nil {
			return UpsertResult{}, err
		}
		if c, err = candidateOf(ctx, tx, e.ID); err != nil {
			return UpsertResult{}, err
		}
	}
	if _, err := addAliases(ctx, tx, e.ID, r.Aliases...); err != nil {
		return UpsertResult{}, err
	}

	// A different report for a row found by key or opening is a duplicate
	// posting attached to it; a mirror's key becomes an alias of the row.
	if (rule == RuleKey || rule == RuleOpening) && reportOf(e) != r.Number {
		aliasAdded := false
		if !c.hasKey(p.Key) {
			if aliasAdded, err = addAliases(ctx, tx, e.ID, p.Key); err != nil {
				return UpsertResult{}, err
			}
		}
		detail := map[string]any{"rule": rule, "report_number": r.Number, "alias_added": aliasAdded, "archived": e.ArchivedAt != ""}
		if err := t.addEvent(ctx, tx, e.ID, ActorEvaluation, EventDuplicateAttached, "", "", detail); err != nil {
			return UpsertResult{}, err
		}
		dirty = true
	}

	canRevive := e.ArchivedAt == ""
	if !canRevive && e.Status == Skipped && !r.Outcome.skipped() && !r.Simulated {
		if err := t.recordRefusal(ctx, tx, e, ActorEvaluation, Evaluated, "the row is archived"); err != nil {
			return UpsertResult{}, err
		}
	}
	replace, revive := planScore(e, r, score, canRevive)

	newScore, newKind, newReport := e.Score, e.ScoreKind, e.ReportNumber
	if replace || e.ReportNumber == nil {
		n := r.Number
		newReport = &n
	}
	if replace && score != nil {
		newScore, newKind = score, r.Kind
	}
	scoreChanged := replace && score != nil && (e.Score == nil || tenths(*e.Score) != tenths(*score) || e.ScoreKind != r.Kind)
	linkChanged := reportOf(e) != *newReport

	notes, status := e.Notes, e.Status
	if scoreChanged {
		notes = appendNote(notes, t.today(), ActorEvaluation, fmt.Sprintf("Score %s -> %s (report %d)", fmtScore(e.Score, e.ScoreKind), fmtScore(newScore, newKind), r.Number))
		detail := map[string]any{"old_score": scoreArg(e.Score), "new_score": scoreArg(newScore), "old_kind": string(e.ScoreKind), "new_kind": string(newKind),
			"old_report": reportOf(e), "new_report": r.Number}
		if err := t.addEvent(ctx, tx, e.ID, ActorEvaluation, EventScoreUpdated, "", "", detail); err != nil {
			return UpsertResult{}, err
		}
	}
	if linkChanged {
		if err := t.addEvent(ctx, tx, e.ID, ActorEvaluation, EventReportLinked, "", "", map[string]any{"from": reportOf(e), "to": r.Number}); err != nil {
			return UpsertResult{}, err
		}
	}
	if revive {
		status = Evaluated
		notes = appendNote(notes, t.today(), ActorEvaluation, fmt.Sprintf("Replaced skipped report %d with report %d", reportOf(e), r.Number))
		detail := map[string]any{"replaced_report": reportOf(e), "report": r.Number}
		if err := t.addEvent(ctx, tx, e.ID, ActorEvaluation, EventStatusChanged, e.Status, status, detail); err != nil {
			return UpsertResult{}, err
		}
	}
	if !dirty && !scoreChanged && !linkChanged && !revive {
		return UpsertResult{Entry: e, Rule: rule}, nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE tracker_entries SET score = ?, score_kind = ?, report_number = ?, status = ?, notes = ?, version = version + 1, updated_at = ? WHERE id = ?`,
		scoreArg(newScore), nullStr(string(newKind)), *newReport, string(status), notes, t.stamp(), e.ID)
	if err != nil {
		return UpsertResult{}, err
	}
	out, err := getEntry(ctx, tx, e.ID)
	return UpsertResult{Entry: out, Rule: rule}, err
}

func identityPosting(url, source, company, role, location string) identity.Posting {
	return identity.Posting{URL: url, Source: source, Company: company, Role: role, Location: location}
}

// IsPlaceholderName reports a name an evaluation writes when the page named
// none ("Unknown company", "Unknown role").
func IsPlaceholderName(s string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "unknown ")
}

// jobSites are the job boards whose name ends the document title of their
// pages ("Software Engineer | Example Co | LinkedIn"). The list is closed on
// purpose: a segment is dropped only when it is one of these or the company.
var jobSites = map[string]bool{
	"linkedin": true, "indeed": true, "glassdoor": true, "ziprecruiter": true,
	"jobright": true, "handshake": true, "wellfound": true, "builtin": true,
}

// isJobSite reports whether a title segment is a job board's name, whatever its
// case, spacing or domain ending ("Indeed.com", "Built In").
func isJobSite(seg string) bool {
	k := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(seg), " ", ""))
	k, _, _ = strings.Cut(strings.TrimPrefix(k, "www."), ".")
	return jobSites[k]
}

// RoleName is the role a page's document title names. The title is read as
// segments divided by " | ": a job board's name and the company are not the
// role, and the first segment left is. A title with neither in it is the role
// as written, because roles hold a " | " of their own ("Backend Engineer |
// Payments"). A title made only of them is not emptied: the first segment that
// is no job board's name stays, else the title. A " - " is not a division: it
// would cut real roles ("Software Engineer - Entry Level").
func RoleName(title, company string) string {
	title, company = strings.TrimSpace(title), strings.TrimSpace(company)
	var left, notBoard []string
	dropped := false
	for _, seg := range strings.Split(title, " | ") {
		seg = strings.TrimSpace(seg)
		switch {
		case seg == "":
		case isJobSite(seg):
			dropped = true
		case company != "" && strings.EqualFold(seg, company):
			dropped = true
			notBoard = append(notBoard, seg)
		default:
			left = append(left, seg)
			notBoard = append(notBoard, seg)
		}
	}
	switch {
	case !dropped:
		return title
	case len(left) > 0:
		return left[0]
	case len(notBoard) > 0:
		return notBoard[0]
	}
	return title
}

func candidateOf(ctx context.Context, q querier, id int64) (candidate, error) {
	cands, err := loadCandidates(ctx, q)
	if err != nil {
		return candidate{}, err
	}
	for _, c := range cands {
		if c.id == id {
			return c, nil
		}
	}
	return candidate{}, fmt.Errorf("tracker: row %d not found", id)
}

// refreshNames lets a re-evaluation of the same posting correct the row's
// company and role, which an earlier, poorer read of the page may have got
// wrong ("CAREERS AT NVIDIA", "Unknown company"). The names follow the report
// the row links to (the best-scored one, or the first), when that report is
// the row's own (the same report number or row id, or the same posting URL):
// a later report that scores lower, or a mirror posting of the job under
// another URL, never renames it, so two readings of one page cannot trade
// places. A placeholder name is replaced by any real one, and a placeholder
// never replaces a name. The identity key is recomputed in the same statement when
// it was built from the names. When the new key belongs to another row, the
// two are one application: they are merged (the history of both is kept)
// unless the merge needs the user, in which case the row keeps its names.
func (t *Tracker) refreshNames(ctx context.Context, tx *sql.Tx, e Entry, rule string, linked bool, r Report, p identity.Posting) (Entry, error) {
	company, role := strings.TrimSpace(p.Company), strings.TrimSpace(p.Role)
	if IsPlaceholderName(company) || IsPlaceholderName(role) || (strings.EqualFold(company, e.Company) && strings.EqualFold(role, e.Role)) {
		return e, nil
	}
	if !IsPlaceholderName(e.Company) && !IsPlaceholderName(e.Role) {
		sameRow := rule == RuleReport || rule == RuleID || (e.PostingURL != "" && canonicalOrRaw(p.URL) == e.PostingURL)
		if !sameRow || !linked {
			return e, nil
		}
	}
	key := e.IdentityKey
	if key == (identity.Posting{Company: e.Company, Role: e.Role, Location: e.Location}).IdentityKey() {
		if nk := (identity.Posting{Company: company, Role: role, Location: e.Location}).IdentityKey(); nk != "" {
			key = nk
		}
	}
	survivor := e
	if key != e.IdentityKey {
		cands, err := loadCandidates(ctx, tx)
		if err != nil {
			return e, err
		}
		for _, c := range cands {
			if c.id == e.ID || !c.hasKey(key) {
				continue
			}
			other, err := getEntry(ctx, tx, c.id)
			if err != nil {
				return e, err
			}
			merged, ok, err := t.mergeForRename(ctx, tx, e, other)
			if err != nil || !ok {
				return e, err
			}
			survivor = merged
			if survivor.ID != e.ID {
				key = survivor.IdentityKey
			}
			break
		}
	}
	notes := appendNote(survivor.Notes, t.today(), ActorEvaluation, fmt.Sprintf("Renamed from %s / %s (report %d)", survivor.Company, survivor.Role, r.Number))
	// The one statement that assigns company or role: it recomputes the key
	// with them (see TestTracker_IdentityKeyRecomputedOnRename).
	if _, err := tx.ExecContext(ctx, `UPDATE tracker_entries SET company = ?, role = ?, identity_key = ?, notes = ?, version = version + 1, updated_at = ? WHERE id = ?`,
		company, role, key, notes, t.stamp(), survivor.ID); err != nil {
		return e, err
	}
	// A key that is both the row's key and its own alias adds nothing.
	if _, err := tx.ExecContext(ctx, `DELETE FROM tracker_aliases WHERE identity_key = ? AND entry_id = ?`, key, survivor.ID); err != nil {
		return e, err
	}
	detail := map[string]any{"text": "renamed", "report_number": r.Number, "old_company": survivor.Company, "old_role": survivor.Role, "company": company, "role": role}
	if err := t.addEvent(ctx, tx, survivor.ID, ActorEvaluation, EventNoteAdded, "", "", detail); err != nil {
		return e, err
	}
	return getEntry(ctx, tx, survivor.ID)
}

// mergeForRename folds a row into the one that already holds the key its new
// names give it. ok is false when the rows' histories conflict, which only the
// user resolves; nothing is changed then.
func (t *Tracker) mergeForRename(ctx context.Context, tx *sql.Tx, e, other Entry) (Entry, bool, error) {
	ids := []int64{e.ID, other.ID}
	slices.Sort(ids)
	plan := dedupPlan{entries: map[int64]Entry{e.ID: e, other.ID: other}}
	last, err := lastStatusEvents(ctx, tx)
	if err != nil {
		return e, false, err
	}
	id, status := plan.chooseSurvivor(ids, last)
	if plan.conflictingAppliedDates(ids) != "" || plan.droppedSubmission(ids, status) != "" {
		return e, false, nil
	}
	g := DedupGroup{Survivor: id, Members: ids, Rule: ruleDedupKey, Status: status}
	if _, err := t.mergeGroup(ctx, tx, g, plan, ActorEvaluation); err != nil {
		return e, false, err
	}
	out, err := getEntry(ctx, tx, id)
	return out, err == nil, err
}
