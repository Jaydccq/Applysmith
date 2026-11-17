package discovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"applysmith/internal/lazyregexp"
	"applysmith/internal/workspace"
)

const (
	// DefaultEvaluateCap is how many leads one run sends to evaluation.
	DefaultEvaluateCap = 25
	// jdMinChars and jdMinHeadings are what a post's own text needs to read
	// like a job description.
	jdMinChars    = 200
	jdMinHeadings = 2
)

var jdHeadingRE = []*lazyregexp.Regexp{
	lazyregexp.New(`(?i)\bresponsibilities\b`),
	lazyregexp.New(`(?i)\brequirements\b`),
	lazyregexp.New(`(?i)\bqualifications\b`),
	lazyregexp.New(`(?i)\bwhat you(?:'|’|)ll do\b|\bwhat you will do\b`),
	lazyregexp.New(`(?i)\babout the (?:role|job|position)\b`),
	lazyregexp.New(`(?i)\bbenefits\b`),
}

// LooksLikeJD reports whether a text reads like a job description: at least
// two section headings and at least 200 characters.
func LooksLikeJD(text string) bool {
	if utf8.RuneCountInString(strings.TrimSpace(text)) < jdMinChars {
		return false
	}
	n := 0
	for _, re := range jdHeadingRE {
		if re.MatchString(text) {
			n++
		}
	}
	return n >= jdMinHeadings
}

// EvaluateRequest is one run of leads.evaluate.
type EvaluateRequest struct {
	Portals workspace.Portals
	Profile workspace.Profile
	// Limit caps the leads sent to evaluation; zero means DefaultEvaluateCap.
	Limit int
	// IDs restricts the run to these leads; empty takes every eligible lead.
	IDs []int64
}

// EvaluateResult says what a run did with each lead it looked at.
type EvaluateResult struct {
	Enqueued   int
	Closed     int // the board no longer lists the posting
	Unknown    int // no board answered; the lead stays new
	Refused    int // the title filter or the gate refused the recovered facts
	Known      int // the posting is already in a store
	BoardIndex int // a board root, not a posting
	Capped     int // eligible but beyond the cap
	Failed     int
}

// Summary is the one line a scheduled run reports.
func (e EvaluateResult) Summary() string {
	return fmt.Sprintf("%d leads sent to evaluation, %d closed, %d unrecovered, %d refused by the filters, %d already known, %d board indexes, %d beyond the cap, %d failed",
		e.Enqueued, e.Closed, e.Unknown, e.Refused, e.Known, e.BoardIndex, e.Capped, e.Failed)
}

// leadPath is how an eligible lead is evaluated.
type leadPath int

const (
	pathNone    leadPath = iota
	pathPosting          // recovered from its board, then evaluated by URL
	pathText             // evaluated on its own text
)

func leadText(l Lead) string {
	if strings.TrimSpace(l.Description) != "" {
		return l.Description
	}
	return l.Evidence.Text
}

// evalPath says how a lead is evaluated. A lead whose link is an ATS board
// root is never evaluated as a posting.
func evaluationCategory(l Lead) string {
	if l.Category == LeadEvalCap || l.Category == LeadEvalFailed {
		return l.EvaluationCategory
	}
	return l.Category
}

func evalPath(l Lead) leadPath {
	switch evaluationCategory(l) {
	case LeadPosting:
		if leadPostingLink(l) != "" {
			return pathPosting
		}
	case LeadNoATS, LeadEmail, LeadDM, LeadComment:
		if LooksLikeJD(leadText(l)) {
			return pathText
		}
	}
	return pathNone
}

// Evaluable reports whether a run of leads.evaluate naming this lead acts on
// it: a new lead it sends to evaluation, or a posting lead whose link is a
// board root, which it files as a careers page. The dashboard offers Evaluate
// only on these.
func Evaluable(l Lead) bool {
	return l.State == LeadNew && (evalPath(l) != pathNone || isBoardIndexLead(l))
}

// evaluableLeads lists the new leads in the order they are taken: posting
// leads, then no-ats leads, then the contact-only ones, each oldest first.
func (r *Repo) evaluableLeads(ctx context.Context, retrySelected bool) ([]Lead, error) {
	rows, err := r.s.Read().QueryContext(ctx, `SELECT `+leadCols+` FROM leads WHERE state = 'new' AND (category IN ('posting', 'eval-cap', 'no-ats', 'email', 'dm', 'comment') OR (? AND category = 'eval-failed'))
		ORDER BY CASE category WHEN 'posting' THEN 0 WHEN 'eval-cap' THEN 0 WHEN 'no-ats' THEN 1 ELSE 2 END, id`, retrySelected)
	if err != nil {
		return nil, err
	}
	return scanLeads(rows)
}

// isBoardIndexLead reports a posting lead whose only link is an ATS board
// root.
func isBoardIndexLead(l Lead) bool {
	if l.Category != LeadPosting || len(l.ApplyLinks) == 0 || leadPostingLink(l) != "" {
		return false
	}
	for _, link := range l.ApplyLinks {
		if LinkKind(link) == LinkCareersIndex {
			return true
		}
	}
	return false
}

// EvaluateLeads sends stored leads to evaluation, mode deep: posting leads
// after the recovery of their facts from the board that owns them, and leads
// whose own text reads like a job description on that text. At most the cap
// per run; an eligible lead beyond it is marked eval-cap and taken first next
// time, and one whose evaluation could not be started is marked eval-failed.
func (sc *Scanner) EvaluateLeads(ctx context.Context, req EvaluateRequest) (res EvaluateResult, err error) {
	var failures []error
	defer func() { err = errors.Join(append(failures, err)...) }()
	if sc.Enqueuer == nil {
		return res, errors.New("no evaluation pool is available to take the leads")
	}
	r, err := sc.leadRun(ctx, req.Portals, req.Profile)
	if err != nil {
		return res, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultEvaluateCap
	}
	leads, err := sc.Repo.evaluableLeads(ctx, len(req.IDs) > 0)
	if err != nil {
		return res, err
	}
	rc := NewLeadRecoverer(r.client, sc.Endpoints, sc.Repo)
	for _, l := range leads {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		if len(req.IDs) > 0 && !containsID(req.IDs, l.ID) {
			continue
		}
		if isBoardIndexLead(l) {
			res.BoardIndex++
			cat, why := LeadCareersPage, "the link is a board index, not one posting"
			if err := sc.Repo.LeadUpdate(ctx, l.ID, LeadPatch{Category: &cat, Reason: &why}); err != nil {
				return res, err
			}
			continue
		}
		originalCategory := evaluationCategory(l)
		if originalCategory == "" && (l.Category == LeadEvalCap || l.Category == LeadEvalFailed) {
			res.Failed++
			why := "original evaluation category is missing; review and re-create this lead from its source"
			failures = append(failures, fmt.Errorf("lead %d: %s", l.ID, why))
			cat := LeadEvalFailed
			if err := sc.Repo.LeadUpdate(ctx, l.ID, LeadPatch{Category: &cat, Reason: &why}); err != nil {
				return res, err
			}
			continue
		}
		path := evalPath(l)
		if path == pathNone {
			continue
		}
		if res.Enqueued >= limit {
			res.Capped++
			cat, why := LeadEvalCap, fmt.Sprintf("beyond the cap of %d leads per run", limit)
			if err := sc.Repo.LeadUpdate(ctx, l.ID, LeadPatch{Category: &cat, Reason: &why, EvaluationCategory: &originalCategory}); err != nil {
				return res, err
			}
			continue
		}
		var out leadOutcome
		if path == pathPosting {
			out, err = r.evaluatePostingLead(ctx, rc, l)
		} else {
			out, err = r.evaluateTextLead(ctx, l)
		}
		switch out {
		case outEnqueued:
			res.Enqueued++
		case outClosed:
			res.Closed++
		case outUnknown:
			res.Unknown++
		case outRefused:
			res.Refused++
		case outKnown:
			res.Known++
		}
		if err != nil {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			res.Failed++
			failures = append(failures, fmt.Errorf("lead %d: %w", l.ID, err))
			cat, why := LeadEvalFailed, err.Error()
			if uerr := sc.Repo.LeadUpdate(ctx, l.ID, LeadPatch{Category: &cat, Reason: &why, EvaluationCategory: &originalCategory}); uerr != nil {
				return res, uerr
			}
			continue
		}
	}
	return res, nil
}

type leadOutcome int

const (
	outNone leadOutcome = iota
	outEnqueued
	outClosed
	outUnknown
	outRefused
	outKnown
)

// evaluatePostingLead recovers a posting lead from its board and, when the
// board lists it, puts it in the inbox and starts its evaluation.
func (r *run) evaluatePostingLead(ctx context.Context, rc *LeadRecoverer, l Lead) (leadOutcome, error) {
	rec := rc.Recover(ctx, l)
	switch rec.Verdict {
	case RecoveryClosed:
		return outClosed, r.repo.LeadDismiss(ctx, l.ID, "posting-closed")
	case RecoveryUnknown:
		return outUnknown, r.repo.LeadUpdate(ctx, l.ID, LeadPatch{Reason: &rec.Reason})
	}
	desc, company := rec.Description, rec.Company
	if err := r.repo.LeadUpdate(ctx, l.ID, LeadPatch{Description: &desc, Company: &company}); err != nil {
		return 0, err
	}
	p := rec.Posting
	canon, err := postingURL(rec.URL)
	if err != nil {
		reason := "the board record has no usable URL"
		return outUnknown, r.repo.LeadUpdate(ctx, l.ID, LeadPatch{Reason: &reason})
	}
	p.URL, p.Source = canon, "leads"
	if rules := r.leadChecker().Failing(p); len(rules) > 0 {
		if err := r.repo.LeadGateAudit(ctx, IngressEvaluate, p, rules); err != nil {
			return 0, err
		}
		return outRefused, r.repo.LeadDismiss(ctx, l.ID, "gate:"+strings.Join(rules, ","))
	}
	store, matchID, dup := r.known.Match(p, false)
	rowID := matchID
	switch {
	case dup && store != StoreInbox:
		why := "already in " + store
		return outKnown, r.repo.LeadUpdate(ctx, l.ID, LeadPatch{Category: leadPtr(evaluationCategory(l)), State: leadPtr(LeadEvaluated), Reason: &why})
	case !dup:
		if rowID, err = r.promoteLead(ctx, p); err != nil {
			return 0, err
		}
		if rowID == 0 {
			return outKnown, r.repo.LeadUpdate(ctx, l.ID, LeadPatch{Category: leadPtr(evaluationCategory(l)), State: leadPtr(LeadEvaluated), Reason: leadPtr("already in the inbox")})
		}
	}
	return r.enqueueLead(ctx, l, rowID, EvalRequest{})
}

// evaluateTextLead puts a lead whose own text reads like a job description in
// the inbox and evaluates it on that text. Nothing is sent to the poster.
func (r *run) evaluateTextLead(ctx context.Context, l Lead) (leadOutcome, error) {
	text := truncateRunes(leadText(l), maxLeadText)
	base := Posting{Source: "leads", Company: l.Company, URL: l.Evidence.URL, Description: text}
	roles, rejected := r.leadChecker().Narrow(base, l.Roles, l.Locations)
	for _, v := range rejected {
		p := base
		p.Title = v.Role
		if err := r.repo.LeadGateAudit(ctx, IngressEvaluate, p, v.Rules); err != nil {
			return 0, err
		}
	}
	if len(roles) == 0 && len(rejected) > 0 {
		return outRefused, r.repo.LeadDismiss(ctx, l.ID, "gate:"+strings.Join(rejected[0].Rules, ","))
	}
	p := base
	p.Title = firstOf(roles, "")
	if len(l.Locations) > 0 {
		p.Location = l.Locations[0]
	}
	var rowID int64
	var inserted bool
	err := r.repo.s.Write(ctx, func(tx *sql.Tx) error {
		var err error
		if rowID, inserted, err = r.repo.insertInbox(ctx, tx, p, p.URL, fmt.Sprintf("lead %d", l.ID), ""); err != nil {
			return err
		}
		if !inserted {
			return tx.QueryRowContext(ctx, `SELECT id FROM inbox WHERE identity_key = ?`, p.key()).Scan(&rowID)
		}
		return r.repo.historyUpsert(ctx, tx, p, HistoryInbox, nil, "")
	})
	if err != nil {
		return 0, err
	}
	return r.enqueueLead(ctx, l, rowID, EvalRequest{Text: text, Title: p.Title, Company: l.Company, Location: p.Location})
}

// enqueueLead starts the deep evaluation of a lead's inbox row. A row that is
// not pending was started already.
func (r *run) enqueueLead(ctx context.Context, l Lead, rowID int64, req EvalRequest) (leadOutcome, error) {
	row, err := r.repo.InboxGet(ctx, rowID)
	if err != nil {
		return 0, err
	}
	patch := LeadPatch{State: leadPtr(LeadEvaluated), InboxID: &rowID, Reason: leadPtr("")}
	if l.Category == LeadEvalCap || l.Category == LeadEvalFailed {
		patch.Category = leadPtr(evaluationCategory(l))
	}
	if row.Status != InboxPending {
		patch.Reason = leadPtr("its inbox row is " + row.Status)
		return outKnown, r.repo.LeadUpdate(ctx, l.ID, patch)
	}
	req.InboxID, req.URL, req.Mode, req.Source, req.PostedAt = rowID, row.URL, "deep", "leads", row.PostedAt
	jobID, admissionErr := r.repo.queueAndEnqueue(ctx, rowID, func() (string, error) { return r.sc.Enqueuer.Enqueue(ctx, req) })
	if jobID == "" && admissionErr != nil {
		return outNone, admissionErr
	}
	// Once the pool accepts work, it consumes the cap even if final accounting
	// fails. Complete the lead link independently of cancellation, as the inbox does.
	finishCtx, finish := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer finish()
	finalErr := r.repo.LeadUpdate(finishCtx, l.ID, patch)
	return outEnqueued, errors.Join(admissionErr, finalErr, ctx.Err())
}

func leadPtr[T any](v T) *T { return &v }
