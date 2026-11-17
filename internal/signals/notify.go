package signals

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Notice kinds.
const (
	NoticeHot    = "hot"
	NoticeDigest = "digest"
)

// Notice is one announcement through the notification policy.
type Notice struct {
	Key   string
	Kind  string
	Title string
	Body  string
	Link  string
}

// Delivery says what the announcement did. Queued is false when no channel
// took it; NoOp lists the channels that were not configured and so did
// nothing (Telegram without a pairing).
type Delivery struct {
	Queued bool
	NoOp   []string
}

// Alert is a lane alarm that waits for the user.
type Alert struct {
	Key    string
	Title  string
	Detail string
	Link   string
}

// Notifier reaches the notification policy and the needs-you inbox.
type Notifier interface {
	Announce(ctx context.Context, n Notice) (Delivery, error)
	NeedsYou(ctx context.Context, a Alert) error
}

// recordNotice writes the outcome of an announcement. Only a delivered notice
// uses its key; a channel that is not configured records a no-op and leaves
// the key free, so a later tick still sends it.
func (r *Repo) recordNotice(ctx context.Context, n Notice, companyID int64, d Delivery, sent bool) error {
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		state, detail := "noop", "no channel configured"
		if sent {
			state, detail = "sent", ""
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO signal_notifications (key, kind, state, company_id, detail, created_at) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT DO NOTHING`, n.Key, n.Kind, state, nullID(companyID), detail, r.stamp()); err != nil {
			return err
		}
		for _, ch := range d.NoOp {
			if _, err := tx.ExecContext(ctx, `INSERT INTO signal_notifications (key, kind, state, company_id, detail, created_at) VALUES (?, ?, 'noop', ?, ?, ?)`,
				n.Key+"|"+ch, n.Kind, nullID(companyID), ch+" is not configured", r.stamp()); err != nil {
				return err
			}
		}
		return nil
	})
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// NoticeSent reports whether a notice with the key was delivered.
func (r *Repo) NoticeSent(ctx context.Context, key string) (bool, error) {
	var n int
	err := r.s.Read().QueryRowContext(ctx, `SELECT count(*) FROM signal_notifications WHERE key = ? AND state = 'sent'`, key).Scan(&n)
	return n > 0, err
}

// announce sends a notice once: a key that was delivered is never sent again.
func (s *Service) announce(ctx context.Context, n Notice, companyID int64) (bool, error) {
	if sent, err := s.repo.NoticeSent(ctx, n.Key); err != nil || sent {
		return false, err
	}
	if s.d.Notifier == nil {
		return false, s.repo.recordNotice(ctx, n, companyID, Delivery{}, false)
	}
	d, err := s.d.Notifier.Announce(ctx, n)
	if err != nil {
		return false, err
	}
	return d.Queued, s.repo.recordNotice(ctx, n, companyID, d, d.Queued)
}

// strongestEvidence is the quote with the largest contribution and the link of
// its document.
func (s *Service) strongestEvidence(ctx context.Context, companyID int64) (quote, link string, err error) {
	scored, err := s.repo.scoredByCompany(ctx, s.policy, s.now(), companyID)
	if err != nil {
		return "", "", err
	}
	items := scored[companyID]
	sort.SliceStable(items, func(i, j int) bool { return items[i].Factors.Contribution() > items[j].Factors.Contribution() })
	for _, it := range items {
		for _, q := range it.Assessment.Quotes {
			if d, err := s.repo.Document(ctx, q.DocumentID); err == nil {
				return q.Quote, d.URL, nil
			}
		}
	}
	return "", "", nil
}

// NotifyStats counts the notification stage.
type NotifyStats struct {
	Hot    int  `json:"hot"`
	Digest bool `json:"digest"`
}

// notifyHot raises one notification for each company that is hot and was not
// announced since it entered hot. The key is the company plus its priority
// revision, so the same revision is never announced twice.
func (s *Service) notifyHot(ctx context.Context) (int, error) {
	tiers, err := s.repo.Tiers(ctx)
	if err != nil {
		return 0, err
	}
	sent := 0
	for id, t := range tiers {
		if t.Tier != TierHot || t.HotSince.IsZero() {
			continue
		}
		c, err := s.repo.Company(ctx, id)
		if err != nil {
			return sent, err
		}
		if c.State == StateDismissed {
			continue
		}
		var since int
		if err := s.d.Store.Read().QueryRowContext(ctx, `SELECT count(*) FROM signal_notifications WHERE kind = ? AND company_id = ? AND state = 'sent' AND created_at >= ?`,
			NoticeHot, id, fmtTS(t.HotSince)).Scan(&since); err != nil {
			return sent, err
		}
		if since > 0 {
			continue
		}
		quote, link, err := s.strongestEvidence(ctx, id)
		if err != nil {
			return sent, err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s is now %s (momentum %.2f).", c.Name, t.Tier, t.Momentum)
		if quote != "" {
			fmt.Fprintf(&b, "\nEvidence: \"%s\" %s", capRunes(quote, 280), link)
		}
		posts, err := s.repo.Postings(ctx, id, true)
		if err != nil {
			return sent, err
		}
		for i, p := range posts {
			if i >= 3 {
				break
			}
			fmt.Fprintf(&b, "\n- %s: %s", p.Title, p.URL)
		}
		n := Notice{Key: fmt.Sprintf("signal-hot|%d|%s", id, RevisionKey(id, t.Tier, t.Momentum, t.Policy, s.now())), Kind: NoticeHot,
			Title: "Hiring signal: " + c.Name, Body: b.String(), Link: "/leads?tab=signals"}
		ok, err := s.announce(ctx, n, id)
		if err != nil {
			s.log.Warn("a hot notification could not be raised", "company", id, "error", err.Error())
			continue
		}
		if ok {
			sent++
		}
	}
	return sent, nil
}

// digestText assembles the digest of the last 24 hours. It carries no secret.
func (s *Service) digestText(ctx context.Context, alarms []Alarm) (string, error) {
	since := fmtTS(s.now().Add(-24 * time.Hour))
	var b strings.Builder
	list := func(title string, rows []string) {
		fmt.Fprintf(&b, "%s: %d\n", title, len(rows))
		for _, r := range rows {
			fmt.Fprintf(&b, "- %s\n", r)
		}
	}
	rows, err := s.d.Store.Read().QueryContext(ctx, `SELECT c.name FROM signal_tiers t JOIN signal_companies c ON c.id = t.company_id WHERE t.tier = 'hot' AND t.hot_since >= ? ORDER BY c.name`, since)
	if err != nil {
		return "", err
	}
	var hot []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return "", err
		}
		hot = append(hot, n)
	}
	rows.Close()
	list("Newly hot", hot)
	changes, err := s.repo.auditSince(ctx, "tier-change", since)
	if err != nil {
		return "", err
	}
	list("Notable signal changes", changes)
	var blocks []string
	brows, err := s.d.Store.Read().QueryContext(ctx, `SELECT c.name, b.opened_at >= ?, b.lifted_at IS NOT NULL AND b.lifted_at >= ? FROM signal_blocks b JOIN signal_companies c ON c.id = b.company_id
		WHERE b.opened_at >= ? OR b.lifted_at >= ?`, since, since, since, since)
	if err != nil {
		return "", err
	}
	for brows.Next() {
		var n string
		var opened, lifted bool
		if err := brows.Scan(&n, &opened, &lifted); err != nil {
			brows.Close()
			return "", err
		}
		if opened {
			blocks = append(blocks, n+": hiring block opened")
		}
		if lifted {
			blocks = append(blocks, n+": hiring block cleared")
		}
	}
	brows.Close()
	list("Hiring blocks", blocks)
	var al []string
	for _, a := range alarms {
		al = append(al, a.Source+": "+a.Status)
	}
	list("Lane alarms", al)
	var open int
	if err := s.d.Store.Read().QueryRowContext(ctx, `SELECT count(*) FROM signal_review WHERE state = 'open'`).Scan(&open); err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "Assessments waiting in the review queue: %d\n", open)
	return strings.TrimSpace(b.String()), nil
}

func (r *Repo) auditSince(ctx context.Context, kind, since string) ([]string, error) {
	rows, err := r.s.Read().QueryContext(ctx, `SELECT detail FROM signal_audit WHERE kind = ? AND at >= ? ORDER BY id`, kind, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// sendDigest sends the daily digest once per date.
func (s *Service) sendDigest(ctx context.Context, alarms []Alarm) (bool, error) {
	day := s.now().UTC().Format("2006-01-02")
	var have int
	if err := s.d.Store.Read().QueryRowContext(ctx, `SELECT count(*) FROM signal_digests WHERE day = ?`, day).Scan(&have); err != nil || have > 0 {
		return false, err
	}
	text, err := s.digestText(ctx, alarms)
	if err != nil {
		return false, err
	}
	ok, err := s.announce(ctx, Notice{Key: "signal-digest|" + day, Kind: NoticeDigest, Title: "Hiring signals digest", Body: text, Link: "/leads?tab=signals"}, 0)
	if err != nil || !ok {
		return false, err
	}
	return true, s.d.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO signal_digests (day, body, created_at) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`, day, text, s.repo.stamp())
		return err
	})
}

// EdgarDisclosure is what any surface that enables the SEC source shows first.
const EdgarDisclosure = "Every request to the SEC carries your contact email, as the SEC fair-access policy requires."
