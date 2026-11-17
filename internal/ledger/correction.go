package ledger

import (
	"context"
	"database/sql"
	"strconv"

	"applysmith/internal/gates"
)

// CorrectionRequest asks the claim to re-send a submission of the posting as a
// correction. The facts come from the run: who started it, whether it is one
// posting, and the user's own message. Whether it opens is the correction
// door's decision on that message and the ledger, never the caller's.
type CorrectionRequest struct {
	Caller        gates.Caller
	SinglePosting bool
	Message       string
}

// Judgement is what the duplicate guard decided for a run: every decision in
// the order taken (a refusing one last) and, for a correction the door
// allowed, the ledger row the new row supersedes.
type Judgement struct {
	Decisions  []gates.Decision
	Supersedes int64
}

// Last is the decision that settled the judgement.
func (j Judgement) Last() gates.Decision {
	if len(j.Decisions) == 0 {
		return gates.Decision{}
	}
	return j.Decisions[len(j.Decisions)-1]
}

// correctionRows reads the rows of the posting that still count, for the
// correction door. A failed row was never sent, so it neither opens a
// correction nor uses one up. The ledger id stands in for the time because
// rows are written in order.
func correctionRows(ctx context.Context, q queryer, key string) ([]gates.CorrectionRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT s.id, s.company,
		COALESCE((SELECT t.company FROM tracker_entries t WHERE s.report_number IS NOT NULL AND t.report_number = s.report_number ORDER BY t.id LIMIT 1), ''),
		s.supersedes IS NOT NULL, EXISTS (SELECT 1 FROM submissions c WHERE c.supersedes = s.id AND c.state <> 'failed')
		FROM submissions s WHERE s.identity_key = ? AND s.state <> 'failed' ORDER BY s.id`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []gates.CorrectionRow
	for rows.Next() {
		var id int64
		var r gates.CorrectionRow
		if err := rows.Scan(&id, &r.Company, &r.ReportCompany, &r.Supersedes, &r.SupersededByOther); err != nil {
			return nil, err
		}
		r.ID, r.At = strconv.FormatInt(id, 10), id
		out = append(out, r)
	}
	return out, rows.Err()
}

// judge runs the duplicate guard over the evidence; a correction request goes
// through the correction door first, and only an open door lifts the earlier
// submission. A live submitting row of another run still blocks either way.
func judge(ctx context.Context, q queryer, req ClaimRequest, key string, in gates.DuplicateInput) (Judgement, error) {
	var j Judgement
	if c := req.Correction; c != nil {
		rows, err := correctionRows(ctx, q, key)
		door := gates.CorrectionDoor(gates.CorrectionInput{Caller: c.Caller, SinglePosting: c.SinglePosting, Message: c.Message, Submit: in.Submit,
			LedgerReadable: err == nil && in.LedgerReadable, Rows: rows})
		j.Decisions = append(j.Decisions, door.Decision)
		if !door.Allowed() {
			return j, refusal(door.Decision)
		}
		id, err := strconv.ParseInt(door.SupersedesRowID, 10, 64)
		if err != nil {
			return j, unavailable(door.Decision)
		}
		j.Supersedes, in.Correction = id, true
	}
	d := gates.DuplicateGuard(in)
	j.Decisions = append(j.Decisions, d)
	if !d.Allowed() {
		return j, refusal(d)
	}
	return j, nil
}

// CorrectionOffer returns the correction request the runs board may offer for
// a submitted application of the posting: the exact message, and true only
// when the correction door would accept that message as the Ask Agent's
// single-posting request today. It writes nothing.
func (l *Ledger) CorrectionOffer(ctx context.Context, postingURL string) (string, bool, error) {
	key := claimKey(postingURL)
	rows, err := correctionRows(ctx, l.s.Read(), key)
	if err != nil {
		return "", false, err
	}
	if len(rows) == 0 {
		return "", false, nil
	}
	newest := rows[len(rows)-1]
	company := newest.Company
	if company == "" {
		company = newest.ReportCompany
	}
	if company == "" {
		return "", false, nil
	}
	msg := gates.CorrectionMessage(company, postingURL)
	door := gates.CorrectionDoor(gates.CorrectionInput{Caller: gates.CallerAskAgent, SinglePosting: true, Message: msg, Submit: true, LedgerReadable: true, Rows: rows})
	return msg, door.Allowed(), nil
}

// supersedesArg is the supersedes column of a new row: NULL unless the row is
// a correction.
func supersedesArg(id int64) any {
	if id == 0 {
		return sql.NullInt64{}
	}
	return id
}
