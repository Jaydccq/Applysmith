package gmail

import (
	"context"
	"fmt"
	"time"

	"applysmith/internal/gmail/extract"
)

// Receipt lookup bounds.
const (
	// A receipt can carry an internal date slightly before the click.
	ReceiptLead = 2 * time.Minute
	// The slowest measured receipt arrived within ten minutes; an unbounded
	// lookup matched another applicant's receipt hours later.
	ReceiptWindow      = 30 * time.Minute
	receiptMaxMessages = 20
)

// Poll schedules, measured from the submit press. A receipt lands within
// seconds to a minute; a later one reaches the tracker through the regular
// sync.
var (
	ReceiptPollOffsets          = []time.Duration{5 * time.Second, 20 * time.Second, 45 * time.Second, 60 * time.Second, 90 * time.Second}
	ReceiptPollOffsetsConfirmed = []time.Duration{5 * time.Second, 20 * time.Second}
)

// receiptTerms is the search vocabulary; scoring decides.
var receiptTerms = []string{
	"thank you for applying", "thanks for applying", "received your application", "application received",
	"application submitted", "application has been submitted", "application was sent", "your application",
	"applying to", "successfully applied", "application complete",
}

// ReceiptLookup identifies one submission.
type ReceiptLookup struct {
	JobURL, ATS, Tenant, Company string
	// SubmittedAt is the instant the submit control was pressed.
	SubmittedAt time.Time
}

// Receipt is the confirmation mail of a submission.
type Receipt struct {
	MessageID  string
	ReceivedAt time.Time
	From       string
	Subject    string
}

// FindReceipt makes one bounded pass over the inbox: mail received from two
// minutes before the press to thirty minutes after it, at most 20 messages.
// Both ends go into the query so the cap can never push the submission's
// receipt out behind newer unrelated mail, and each message is re-checked
// against the window. It returns nil when nothing qualifies and an error when
// the lookup itself failed; a failure is never reported as not found.
func (l *Lookups) FindReceipt(ctx context.Context, lk ReceiptLookup) (*Receipt, error) {
	if lk.SubmittedAt.IsZero() {
		return nil, badRequest("the submit instant is required")
	}
	from, until := lk.SubmittedAt.Add(-ReceiptLead), lk.SubmittedAt.Add(ReceiptWindow)
	terms := ""
	for _, t := range receiptTerms {
		terms += ` "` + t + `"`
	}
	q := fmt.Sprintf("after:%d before:%d%s {%s}", from.Unix(), (until.UnixMilli()+999)/1000, anywhere, terms[1:])
	msgs, err := l.fetchNewestFirst(ctx, q, receiptMaxMessages)
	if err != nil {
		return nil, failureFor(err)
	}
	in := extract.ReceiptLookup{JobURL: lk.JobURL, ATS: lk.ATS, Tenant: lk.Tenant, Company: lk.Company}
	var best *Receipt
	bestScore := 0
	for _, m := range msgs { // newest first, so a tie keeps the newest
		if m.InternalMS < from.UnixMilli() || m.InternalMS > until.UnixMilli() {
			continue
		}
		score := extract.ScoreReceipt(in, extract.ReceiptMessage{From: m.From, Subject: m.Subject, Body: m.Text})
		if score > bestScore {
			bestScore = score
			best = &Receipt{MessageID: m.ID, ReceivedAt: time.UnixMilli(m.InternalMS).UTC(), From: m.From, Subject: m.Subject}
		}
	}
	return best, nil
}

// AwaitOptions tune AwaitReceipt.
type AwaitOptions struct {
	// PageConfirmed selects the short schedule: the submission page already
	// confirmed, so the receipt is corroboration, not the evidence.
	PageConfirmed bool
	// Offsets replaces the schedule (tests).
	Offsets []time.Duration
	// Sleep waits for d or until ctx ends; nil uses a timer.
	Sleep func(ctx context.Context, d time.Duration) error
}

func sleepTimer(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// AwaitReceipt polls for the receipt on the schedule, measured from the press
// (a lane that already spent 40 seconds skips the early polls). It stops on the
// first hit, after the last offset, or on abort: an aborted wait returns the
// context error without a further inbox call.
func (l *Lookups) AwaitReceipt(ctx context.Context, lk ReceiptLookup, opt AwaitOptions) (*Receipt, error) {
	offsets := opt.Offsets
	if offsets == nil {
		offsets = ReceiptPollOffsets
		if opt.PageConfirmed {
			offsets = ReceiptPollOffsetsConfirmed
		}
	}
	sleep := opt.Sleep
	if sleep == nil {
		sleep = sleepTimer
	}
	for _, off := range offsets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if wait := lk.SubmittedAt.Add(off).Sub(l.now()); wait > 0 {
			if err := sleep(ctx, wait); err != nil {
				return nil, err
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r, err := l.FindReceipt(ctx, lk)
		if err != nil || r != nil {
			return r, err
		}
	}
	return nil, nil
}
