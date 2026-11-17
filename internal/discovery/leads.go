package discovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"applysmith/internal/identity"
)

// Lead review categories.
const (
	LeadPosting       = "posting"
	LeadCareersPage   = "careers-page"
	LeadNoATS         = "no-ats"
	LeadEmail         = "email"
	LeadDM            = "dm"
	LeadComment       = "comment"
	LeadGenericPage   = "generic-page"
	LeadUnreachable   = "unreachable"
	LeadLowConfidence = "low-confidence"
	LeadEvalCap       = "eval-cap"
	LeadEvalFailed    = "eval-failed"
)

// LeadCategories lists the review categories.
var LeadCategories = []string{LeadPosting, LeadCareersPage, LeadNoATS, LeadEmail, LeadDM, LeadComment,
	LeadGenericPage, LeadUnreachable, LeadLowConfidence, LeadEvalCap, LeadEvalFailed}

// Lead states.
const (
	LeadNew       = "new"
	LeadEvaluated = "evaluated"
	LeadDismissed = "dismissed"
)

// Caps on text a lead carries.
const (
	maxEvidenceText = 4000
	maxLeadText     = maxDescription
)

// ErrLeadDuplicate is returned by LeadAdd with the id of the lead that
// already holds the key.
var ErrLeadDuplicate = errors.New("discovery: lead already stored")

// Evidence is what produced a lead. Text is capped at 4,000 characters; when
// there is none, AbsentReason says why, because a lead never stands without
// either.
type Evidence struct {
	URL          string
	Title        string
	Text         string
	Adapter      string
	At           time.Time
	AbsentReason string
}

// Lead is a hiring signal that is not yet a single posting.
type Lead struct {
	ID       int64
	Source   string
	DedupKey string
	BodyHash string

	PosterType, Author, Company string
	Roles, Locations            []string
	WorkModel, Compensation     string
	ApplyLinks, ContactEmails   []string
	ContactMethod, Sponsorship  string
	Confidence                  float64

	Category, State string
	// EvaluationCategory preserves the original category across eval-cap and
	// eval-failed review states; a URL cannot replace this provenance.
	EvaluationCategory string
	// Reason says why the lead is in its category or state: a recovery
	// verdict, a dismissal, a failure.
	Reason string
	// Expanded is empty until a careers page was read, then `none` or a
	// count of postings.
	Expanded string
	// Description is the posting text recovered from its board or carried by
	// the lead; capped at 20,000 characters.
	Description string
	Evidence    Evidence
	// InboxID is the inbox row the lead was promoted to.
	InboxID int64

	Created, Updated time.Time
}

// LeadFilter selects leads. Zero values mean no filter. ByConfidence orders
// the list by confidence, highest first, instead of newest first. Text keeps
// the leads whose company, poster, roles or evidence title hold every one of
// its words, in any case; no body text is searched.
type LeadFilter struct {
	State, Category, Source, Text string
	Limit, Offset                 int
	ByConfidence                  bool
}

func (f LeadFilter) where() (string, []any) {
	var conds []string
	var args []any
	for _, c := range []struct{ col, v string }{{"state", f.State}, {"category", f.Category}, {"source", f.Source}} {
		if c.v != "" {
			conds = append(conds, c.col+" = ?")
			args = append(args, c.v)
		}
	}
	for _, w := range strings.Fields(f.Text) {
		conds = append(conds, `(company || ' ' || author || ' ' || roles_json || ' ' || evidence_title) LIKE ? ESCAPE '\'`)
		args = append(args, "%"+strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(w)+"%")
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

const leadCols = `id, source, dedup_key, body_hash, poster_type, author, company, roles_json, locations_json, work_model, compensation,
	apply_links_json, contact_emails_json, contact_method, sponsorship, confidence, category, state, reason, expanded, description,
	evidence_url, evidence_title, evidence_text, evidence_adapter, evidence_at, evidence_absent, inbox_id, created_at, updated_at, evaluation_category`

func scanLeads(rows *sql.Rows) ([]Lead, error) {
	defer rows.Close()
	var out []Lead
	for rows.Next() {
		var l Lead
		var roles, locs, links, emails, created, updated string
		var evAt sql.NullString
		var inbox sql.NullInt64
		if err := rows.Scan(&l.ID, &l.Source, &l.DedupKey, &l.BodyHash, &l.PosterType, &l.Author, &l.Company, &roles, &locs, &l.WorkModel,
			&l.Compensation, &links, &emails, &l.ContactMethod, &l.Sponsorship, &l.Confidence, &l.Category, &l.State, &l.Reason, &l.Expanded,
			&l.Description, &l.Evidence.URL, &l.Evidence.Title, &l.Evidence.Text, &l.Evidence.Adapter, &evAt, &l.Evidence.AbsentReason,
			&inbox, &created, &updated, &l.EvaluationCategory); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(roles), &l.Roles)
		_ = json.Unmarshal([]byte(locs), &l.Locations)
		_ = json.Unmarshal([]byte(links), &l.ApplyLinks)
		_ = json.Unmarshal([]byte(emails), &l.ContactEmails)
		l.Evidence.At = parseStamp(evAt)
		l.InboxID = inbox.Int64
		l.Created, _ = time.Parse(timeLayout, created)
		l.Updated, _ = time.Parse(timeLayout, updated)
		out = append(out, l)
	}
	return out, rows.Err()
}

func validLeadCategory(c string) bool {
	for _, x := range LeadCategories {
		if x == c {
			return true
		}
	}
	return false
}

// leadNormalize applies the caps and defaults a stored lead has and reports
// what makes it unfit to store.
func leadNormalize(l *Lead) error {
	if l.Source == "" {
		return errors.New("a lead needs a source")
	}
	if !validLeadCategory(l.Category) {
		return fmt.Errorf("unknown lead category %q", l.Category)
	}
	switch l.Category {
	case LeadPosting, LeadNoATS, LeadEmail, LeadDM, LeadComment:
		l.EvaluationCategory = l.Category
	}
	if l.State == "" {
		l.State = LeadNew
	}
	l.Evidence.Text = truncateRunes(l.Evidence.Text, maxEvidenceText)
	l.Description = truncateRunes(l.Description, maxLeadText)
	if strings.TrimSpace(l.Evidence.Text) == "" && strings.TrimSpace(l.Evidence.AbsentReason) == "" {
		return errors.New("a lead needs evidence text or the reason there is none")
	}
	if l.DedupKey == "" {
		l.DedupKey = defaultLeadKey(*l)
	}
	return nil
}

// LeadAdd stores a lead. The dedup key is the caller's, or derived from the
// category: a careers page by its URL, a no-ats lead by company and evidence
// URL, a posting by its link. A lead whose key is stored already is not added
// again: the existing id comes back with ErrLeadDuplicate.
func (r *Repo) LeadAdd(ctx context.Context, l Lead) (int64, error) {
	if err := leadNormalize(&l); err != nil {
		return 0, err
	}
	var id int64
	err := r.s.Write(ctx, func(tx *sql.Tx) error {
		now := r.stamp()
		roles, _ := json.Marshal(nonNilStrings(l.Roles))
		locs, _ := json.Marshal(nonNilStrings(l.Locations))
		links, _ := json.Marshal(nonNilStrings(l.ApplyLinks))
		emails, _ := json.Marshal(nonNilStrings(l.ContactEmails))
		res, err := tx.ExecContext(ctx, `INSERT INTO leads (source, dedup_key, body_hash, poster_type, author, company, roles_json, locations_json,
			work_model, compensation, apply_links_json, contact_emails_json, contact_method, sponsorship, confidence, category, state, reason,
			expanded, description, evidence_url, evidence_title, evidence_text, evidence_adapter, evidence_at, evidence_absent, inbox_id,
			created_at, updated_at, evaluation_category) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (dedup_key) DO NOTHING`,
			l.Source, l.DedupKey, l.BodyHash, l.PosterType, l.Author, l.Company, string(roles), string(locs), l.WorkModel, l.Compensation,
			string(links), string(emails), l.ContactMethod, l.Sponsorship, l.Confidence, l.Category, l.State, l.Reason, l.Expanded,
			l.Description, l.Evidence.URL, l.Evidence.Title, l.Evidence.Text, l.Evidence.Adapter, fmtTime(l.Evidence.At), l.Evidence.AbsentReason,
			nullInt(l.InboxID), now, now, l.EvaluationCategory)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			if err := tx.QueryRowContext(ctx, `SELECT id FROM leads WHERE dedup_key = ?`, l.DedupKey).Scan(&id); err != nil {
				return err
			}
			return ErrLeadDuplicate
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

func nullInt(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

// LeadGet returns one lead.
func (r *Repo) LeadGet(ctx context.Context, id int64) (Lead, error) {
	rows, err := r.s.Read().QueryContext(ctx, `SELECT `+leadCols+` FROM leads WHERE id = ?`, id)
	if err != nil {
		return Lead{}, err
	}
	list, err := scanLeads(rows)
	if err != nil {
		return Lead{}, err
	}
	if len(list) == 0 {
		return Lead{}, fmt.Errorf("%w: lead %d", ErrNotFound, id)
	}
	return list[0], nil
}

// LeadList lists leads, newest first, or by confidence with the newest first
// among equals.
func (r *Repo) LeadList(ctx context.Context, f LeadFilter) ([]Lead, error) {
	where, args := f.where()
	order := ` ORDER BY id DESC`
	if f.ByConfidence {
		order = ` ORDER BY confidence DESC, id DESC`
	}
	q := `SELECT ` + leadCols + ` FROM leads` + where + order
	switch {
	case f.Limit > 0:
		q += fmt.Sprintf(` LIMIT %d OFFSET %d`, f.Limit, max(f.Offset, 0))
	case f.Offset > 0:
		q += fmt.Sprintf(` LIMIT -1 OFFSET %d`, f.Offset)
	}
	rows, err := r.s.Read().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return scanLeads(rows)
}

// LeadCount counts the leads a filter selects, ignoring Limit and Offset.
func (r *Repo) LeadCount(ctx context.Context, f LeadFilter) (int, error) {
	where, args := f.where()
	var n int
	err := r.s.Read().QueryRowContext(ctx, `SELECT count(*) FROM leads`+where, args...).Scan(&n)
	return n, err
}

// LeadCounts counts per category the leads f selects, whatever their
// category; with no state, the dismissed ones are left out.
func (r *Repo) LeadCounts(ctx context.Context, f LeadFilter) (map[string]int, error) {
	out := map[string]int{}
	for _, c := range LeadCategories {
		out[c] = 0
	}
	f.Category = ""
	where, args := f.where()
	if f.State == "" {
		where = " WHERE state <> 'dismissed'" + strings.Replace(where, " WHERE ", " AND ", 1)
	}
	rows, err := r.s.Read().QueryContext(ctx, `SELECT category, count(*) FROM leads`+where+` GROUP BY category`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		var n int
		if err := rows.Scan(&c, &n); err != nil {
			return nil, err
		}
		out[c] = n
	}
	return out, rows.Err()
}

// LeadPatch names the fields LeadUpdate changes; nil leaves a field alone.
type LeadPatch struct {
	Category, State, Reason, Expanded, Company, Description *string
	EvaluationCategory                                      *string
	Roles                                                   *[]string
	InboxID                                                 *int64
}

// LeadUpdate changes the named fields of a lead.
func (r *Repo) LeadUpdate(ctx context.Context, id int64, p LeadPatch) error {
	return r.s.Write(ctx, func(tx *sql.Tx) error { return r.updateLead(ctx, tx, id, p) })
}

func (r *Repo) updateLead(ctx context.Context, tx *sql.Tx, id int64, p LeadPatch) error {
	sets := []string{"updated_at = ?"}
	args := []any{r.stamp()}
	add := func(col string, v any) {
		sets = append(sets, col+" = ?")
		args = append(args, v)
	}
	if p.Category != nil {
		if !validLeadCategory(*p.Category) {
			return fmt.Errorf("unknown lead category %q", *p.Category)
		}
		add("category", *p.Category)
	}
	if p.EvaluationCategory != nil {
		add("evaluation_category", *p.EvaluationCategory)
	}
	if p.State != nil {
		add("state", *p.State)
	}
	if p.Reason != nil {
		add("reason", *p.Reason)
	}
	if p.Expanded != nil {
		add("expanded", *p.Expanded)
	}
	if p.Company != nil {
		add("company", *p.Company)
	}
	if p.Description != nil {
		add("description", truncateRunes(*p.Description, maxLeadText))
	}
	if p.Roles != nil {
		b, _ := json.Marshal(nonNilStrings(*p.Roles))
		add("roles_json", string(b))
	}
	if p.InboxID != nil {
		add("inbox_id", nullInt(*p.InboxID))
	}
	res, err := tx.ExecContext(ctx, `UPDATE leads SET `+strings.Join(sets, ", ")+` WHERE id = ?`, append(args, id)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: lead %d", ErrNotFound, id)
	}
	return nil
}

// LeadSetState moves a lead to a state and records why.
func (r *Repo) LeadSetState(ctx context.Context, id int64, state, reason string) error {
	switch state {
	case LeadNew, LeadEvaluated, LeadDismissed:
	default:
		return fmt.Errorf("unknown lead state %q; states are new, evaluated, dismissed", state)
	}
	return r.LeadUpdate(ctx, id, LeadPatch{State: &state, Reason: &reason})
}

// LeadDismiss dismisses a lead with a reason.
func (r *Repo) LeadDismiss(ctx context.Context, id int64, reason string) error {
	return r.LeadSetState(ctx, id, LeadDismissed, reason)
}

// LeadRestore puts a dismissed lead back to new: the undo of a dismissal.
func (r *Repo) LeadRestore(ctx context.Context, id int64) error {
	l, err := r.LeadGet(ctx, id)
	if err != nil {
		return err
	}
	if l.State != LeadDismissed {
		return fmt.Errorf("%w: lead %d is %s, only a dismissed lead is restored", ErrBadTransition, id, l.State)
	}
	return r.LeadSetState(ctx, id, LeadNew, "")
}

// AddNoATSLead stores the review lead of a company with no ATS board. It is
// de-duplicated by company and evidence URL: the second call returns the
// stored id with ErrLeadDuplicate.
func (r *Repo) AddNoATSLead(ctx context.Context, source, company string, ev Evidence) (int64, error) {
	return r.LeadAdd(ctx, Lead{Source: source, Company: company, Category: LeadNoATS, Evidence: ev, Description: ev.Text})
}

// AddCareersPage stores a careers page a source found, for expansion. It
// implements LeadSink. A page stored already is left alone.
func (r *Repo) AddCareersPage(ctx context.Context, pageURL, company, source string) error {
	_, err := r.LeadAdd(ctx, Lead{
		Source: source, Company: company, Category: LeadCareersPage, ApplyLinks: []string{pageURL},
		Evidence: Evidence{URL: pageURL, Adapter: source, At: r.now().UTC(), AbsentReason: "found by a " + source + " search; the careers page is its own evidence"},
	})
	if errors.Is(err, ErrLeadDuplicate) {
		return nil
	}
	return err
}

// defaultLeadKey is the identity of a lead whose source named none.
func defaultLeadKey(l Lead) string {
	company := NormalizeCompany(l.Company)
	switch l.Category {
	case LeadCareersPage:
		return "careers:" + leadURLKey(firstOf(l.ApplyLinks, l.Evidence.URL))
	case LeadNoATS:
		return "noats:" + company + "|" + leadURLKey(l.Evidence.URL)
	case LeadPosting:
		return "posting:" + leadURLKey(firstOf(l.ApplyLinks, l.Evidence.URL))
	}
	return "lead:" + l.Source + "|" + company + "|" + leadURLKey(l.Evidence.URL) + "|" + strings.ToLower(strings.Join(l.Roles, ","))
}

func firstOf(list []string, fallback string) string {
	if len(list) > 0 {
		return list[0]
	}
	return fallback
}

// leadURLKey is the form a URL is compared in: the canonical URL when it has
// one, else the trimmed text.
func leadURLKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if c, err := identity.CanonicalURL(raw); err == nil {
		return c
	}
	return strings.ToLower(raw)
}
