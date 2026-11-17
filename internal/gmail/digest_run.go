package gmail

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Digest schedule.
const (
	MorningHour = 8
	EveningHour = 18
	// CatchUp is how long after its instant a missed slot still runs.
	CatchUp = 6 * time.Hour
	// FirstWindow and MaxWindow bound the span a daily digest reports.
	FirstWindow  = 24 * time.Hour
	MaxWindow    = 36 * time.Hour
	WeeklyWindow = 7 * 24 * time.Hour
	// TelegramPageChars keeps each Telegram message under the 4,000 character
	// limit and under the sender's own clip.
	TelegramPageChars = 3700
	// AgentBudget bounds a digest the Ask Agent asks for, so the tool answers
	// inside its own deadline.
	AgentBudget = 20 * time.Second
	// RatingItems is how many items the Telegram keyboard rates.
	RatingItems = 8
)

// draftBudget is DraftBudget; tests shorten it.
var draftBudget = DraftBudget

// ErrChannelOff is returned by a channel that is switched off or not set up;
// the delivery record then says skipped rather than failed.
var ErrChannelOff = errors.New("gmail: the channel is off")

// RatingButton is one inline button of the rating keyboard.
type RatingButton struct{ Text, Data string }

// Channels deliver a stored digest. Each is optional; a failure of one never
// stops another.
type Channels struct {
	// Telegram queues the stored digest with the notification policy, whose
	// Telegram sink sends the pages from TelegramView.
	Telegram func(ctx context.Context, d Digest) error
	// Desktop queues the headline with the notification policy.
	Desktop func(ctx context.Context, d Digest) error
	// Agent opens an Ask Agent conversation with channel digest and returns its id.
	Agent func(ctx context.Context, d Digest) (string, error)
}

// Digests produces, stores and delivers the daily digests and the Sunday
// weekly review.
type Digests struct {
	S        *Stack
	Channels Channels
	Now      func() time.Time

	mu   sync.Mutex
	busy bool
}

func (d *Digests) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Slot is one scheduled digest instant.
type Slot struct {
	At   time.Time
	Kind string
}

// SlotAt returns the latest slot at or before now in the zone: 08:00 and 18:00
// local time, and the Sunday 18:00 slot is the weekly review.
func SlotAt(now time.Time, zone *time.Location) Slot {
	local := now.In(zone)
	y, m, day := local.Date()
	best := Slot{}
	for _, back := range []int{0, 1} {
		for _, hour := range []int{EveningHour, MorningHour} {
			at := time.Date(y, m, day-back, hour, 0, 0, 0, zone)
			if !at.After(now) && (best.At.IsZero() || at.After(best.At)) {
				best = Slot{At: at.UTC()}
			}
		}
	}
	best.Kind = DigestDaily
	if l := best.At.In(zone); l.Weekday() == time.Sunday && l.Hour() == EveningHour {
		best.Kind = DigestWeekly
	}
	return best
}

// Readable reports whether Gmail is connected and did not refuse its app
// password.
func (s *Service) Readable(ctx context.Context) bool {
	return s.Account.connected() == nil && s.Repo.stateOr(ctx, keyLoginStatus) != loginRefused
}

// Tick runs the slot that is due, if any. It does nothing while Gmail is not
// readable or scheduled digests are off, runs a slot at most once (the slot
// instant is the key, and a failed slot stays claimed), and catches a slot up
// only within six hours of it.
func (d *Digests) Tick(ctx context.Context, now time.Time, zone *time.Location) error {
	r := d.S.Repo
	if !d.S.Service.Readable(ctx) || !r.Switches(ctx).ScheduledDigests {
		return nil
	}
	slot := SlotAt(now, zone)
	if slot.At.IsZero() || now.Sub(slot.At) > CatchUp {
		return nil
	}
	_, err := d.produce(ctx, slot.Kind, TriggerSlot, slot.At)
	if errors.Is(err, ErrBusy) {
		return nil
	}
	return err
}

// Run produces a digest now, on request. It is delivered like a scheduled one;
// a request while another is being produced is refused as busy.
func (d *Digests) Run(ctx context.Context) (Digest, error) {
	return d.produce(ctx, DigestDaily, TriggerHand, time.Time{})
}

// Generate produces a digest for the Ask Agent: it is stored and delivered
// nowhere, and starts no window.
func (d *Digests) Generate(ctx context.Context) (Digest, error) {
	return d.produce(ctx, DigestDaily, TriggerAgent, time.Time{})
}

func (d *Digests) acquire() (func(), bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.busy {
		return nil, false
	}
	d.busy = true
	return func() { d.mu.Lock(); d.busy = false; d.mu.Unlock() }, true
}

// produce claims the slot, composes, stores, delivers and stores again. A zero
// Digest with a nil error means the slot was already taken.
func (d *Digests) produce(ctx context.Context, kind, trigger string, slot time.Time) (Digest, error) {
	if err := d.S.Repo.guardDemo(ctx); err != nil {
		return Digest{}, err
	}
	release, ok := d.acquire()
	if !ok {
		return Digest{}, ErrBusy
	}
	defer release()
	r := d.S.Repo
	now := d.now()
	dig := Digest{Kind: kind, Trigger: trigger, Slot: slot, CreatedAt: now, WindowEnd: now, StartsWindow: kind == DigestDaily && trigger != TriggerAgent}
	switch {
	case kind == DigestWeekly:
		dig.WindowStart = now.Add(-WeeklyWindow)
	default:
		dig.WindowStart = now.Add(-FirstWindow)
		if prev, ok := r.previousWindowEnd(ctx); ok {
			dig.WindowStart = prev
			if floor := now.Add(-MaxWindow); prev.Before(floor) {
				dig.WindowStart = floor
			}
		}
	}
	id, claimed, err := r.claimDigest(ctx, dig)
	if err != nil || !claimed {
		return Digest{}, err
	}
	dig.ID, dig.Status = id, digestRunning
	budget := draftBudget
	if trigger == TriggerAgent {
		budget = min(budget, AgentBudget)
	}
	if err := d.compose(ctx, &dig, budget); err != nil {
		dig.Status, dig.Error, dig.StartsWindow = digestFailed, err.Error(), false
		_ = r.saveDigest(ctx, dig)
		return dig, err
	}
	dig.Status = digestDone
	if err := r.saveDigest(ctx, dig); err != nil {
		return dig, err
	}
	if trigger == TriggerAgent {
		return dig, nil
	}
	dig.Delivery = d.deliver(ctx, dig)
	return dig, r.saveDigest(ctx, dig)
}

func (d *Digests) compose(ctx context.Context, dig *Digest, budget time.Duration) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("the digest could not be composed: %v", p)
		}
	}()
	s := d.S
	now := dig.CreatedAt
	threads, err := s.Repo.Threads(ctx, now)
	if err != nil {
		return err
	}
	bctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if dig.Kind == DigestWeekly {
		d.composeWeekly(bctx, dig, threads)
		return nil
	}
	verdicts, err := s.Repo.ThreadVerdicts(ctx)
	if err != nil {
		return err
	}
	c := collect(threads, verdicts, dig.WindowStart, dig.WindowEnd)
	headline := deterministicHeadline(c)
	dig.Method = MethodDeterministic
	if len(c.items) > 0 {
		examples, _ := s.Repo.RecentRatings(ctx, RatingExamples)
		type textRes struct {
			m  modelText
			ok bool
		}
		ch := make(chan textRes, 1)
		// The text works on its own copy, so the drafts can be attached beside it.
		forText := c
		forText.items = slices.Clone(c.items)
		go func() { m, ok := s.composeModel(bctx, forText, examples); ch <- textRes{m, ok} }()
		drafts := s.draftsFor(bctx, c.threads)
		for i, text := range drafts {
			c.items[i].Draft = text
		}
		select {
		case r := <-ch:
			if r.ok {
				headline, dig.Method = r.m.Headline, MethodModel
				for n, line := range r.m.Lines {
					c.items[n-1].Line = line
				}
			}
		case <-bctx.Done():
		}
	}
	dig.Headline = headline
	dig.Body = DigestBody{Items: c.items, Muted: c.muted, Omitted: c.omitted, Receipts: c.receipts}
	dig.Body.Text = renderDaily(headline, now, c)
	return nil
}

func (d *Digests) composeWeekly(ctx context.Context, dig *Digest, threads []Thread) {
	s := d.S
	f := funnel(threads, dig.WindowStart, dig.WindowEnd)
	you, follow, moreYou, moreFollow := weeklyLists(threads, dig.CreatedAt)
	narrative, modelled := s.weeklyNarrative(ctx, f, len(you)+moreYou, len(follow)+moreFollow)
	dig.Method = MethodDeterministic
	if modelled {
		dig.Method = MethodModel
	}
	dig.Headline = oneLineClip(narrative, headlineChars)
	dig.Body = DigestBody{Items: append(you, follow...), Funnel: f, MoreWaiting: moreYou, MoreFollowUp: moreFollow, Narrative: narrative}
	dig.Body.Text = renderWeekly(narrative, dig.CreatedAt, dig.Body)
}

func oneLineClip(s string, n int) string {
	s = oneLine(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "..."
	}
	return s
}

// empty reports whether a digest has nothing to push: no thread, no receipt and
// nothing muted, or an all-zero weekly review.
func (dig Digest) empty() bool {
	if dig.Kind == DigestWeekly {
		return funnelTotal(dig.Body.Funnel) == 0 && len(dig.Body.Items) == 0
	}
	return len(dig.Body.Items) == 0 && dig.Body.Receipts == 0 && len(dig.Body.Muted) == 0
}

// deliver sends a stored digest to each channel on its own and returns what
// happened to each.
func (d *Digests) deliver(ctx context.Context, dig Digest) map[string]string {
	out := map[string]string{}
	if dig.empty() {
		for _, c := range []string{"telegram", "desktop", "agent"} {
			out[c] = "skipped: nothing to report"
		}
		return out
	}
	run := func(name string, fn func() error) {
		err := safely(fn)
		switch {
		case err == nil:
			out[name] = "sent"
		case errors.Is(err, ErrChannelOff):
			// A channel switched off says so; one that is on but cannot take
			// the brief names why (not paired).
			why, named := strings.CutPrefix(err.Error(), ErrChannelOff.Error()+": ")
			if !named {
				why = "turned off"
			}
			out[name] = "skipped: " + why
		default:
			out[name] = "failed: " + oneLineClip(err.Error(), 160)
		}
	}
	ch := d.Channels
	run("telegram", func() error {
		if ch.Telegram == nil {
			return ErrChannelOff
		}
		return ch.Telegram(ctx, dig)
	})
	run("desktop", func() error {
		if ch.Desktop == nil {
			return ErrChannelOff
		}
		return ch.Desktop(ctx, dig)
	})
	run("agent", func() error {
		if ch.Agent == nil {
			return ErrChannelOff
		}
		_, err := ch.Agent(ctx, dig)
		return err
	})
	return out
}

func safely(fn func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return fn()
}

// SplitPages cuts text on line boundaries into messages of fewer than limit
// characters. A single longer line is cut where it must be.
func SplitPages(text string, limit int) []string {
	var pages []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimRight(cur.String(), "\n"); s != "" {
			pages = append(pages, s)
		}
		cur.Reset()
	}
	for _, line := range strings.Split(text, "\n") {
		for utf8.RuneCountInString(line) >= limit {
			flush()
			r := []rune(line)
			pages = append(pages, string(r[:limit-1]))
			line = string(r[limit-1:])
		}
		if utf8.RuneCountInString(cur.String())+utf8.RuneCountInString(line)+1 >= limit {
			flush()
		}
		cur.WriteString(line + "\n")
	}
	flush()
	if len(pages) == 0 {
		pages = []string{""}
	}
	return pages
}

// RatingData is the callback data of a rating button: the digest, the item
// number and the verdict.
func RatingData(digestID int64, item int, verdict string) string {
	return fmt.Sprintf("rate:%d.%d:%s", digestID, item, verdict)
}

// ratingRows is the keyboard of the last Telegram message: a thumbs-up and a
// thumbs-down per item for at most the first eight, two items to a row, with
// the verdict in force marked.
func ratingRows(dig Digest, verdicts map[string]Feedback) [][]RatingButton {
	var rows [][]RatingButton
	var row []RatingButton
	for i, it := range dig.Body.Items {
		if i == RatingItems {
			break
		}
		for _, v := range []struct{ verdict, sign string }{{VerdictUp, "+"}, {VerdictDown, "-"}} {
			text := fmt.Sprintf("%d %s", i+1, v.sign)
			if verdicts[it.Key].Verdict == v.verdict {
				text = "[" + text + "]"
			}
			row = append(row, RatingButton{Text: text, Data: RatingData(dig.ID, i+1, v.verdict)})
		}
		if len(row) == 4 {
			rows, row = append(rows, row), nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	return rows
}

// ErrStaleRating is returned for a rating button that names no digest item.
var ErrStaleRating = errors.New("gmail: that rating button is out of date")

// RateItem applies a Telegram rating button, "<digest id>.<item number>" and
// a verdict. A second tap on the verdict in force clears it; a stale or
// malformed button records nothing.
func (d *Digests) RateItem(ctx context.Context, ref, verdict string) error {
	idText, itemText, ok := strings.Cut(ref, ".")
	id, err1 := strconv.ParseInt(idText, 10, 64)
	n, err2 := strconv.Atoi(itemText)
	if !ok || err1 != nil || err2 != nil || n < 1 || n > RatingItems || verdict != VerdictUp && verdict != VerdictDown {
		return ErrStaleRating
	}
	dig, err := d.S.Repo.Digest(ctx, id)
	if err != nil || n > len(dig.Body.Items) {
		return ErrStaleRating
	}
	key := dig.Body.Items[n-1].Key
	verdicts, err := d.S.Repo.ThreadVerdicts(ctx)
	if err != nil {
		return err
	}
	if verdicts[key].Verdict == verdict {
		verdict = VerdictClear
	}
	return d.S.Repo.Rate(ctx, id, key, verdict)
}

// AnnouncementTitle is the title of the digest's announcement.
func (dig Digest) AnnouncementTitle() string {
	if dig.Kind == DigestWeekly {
		return "Gmail weekly review"
	}
	return "Gmail digest"
}

// DigestLink is the dashboard link of a digest, which also names it to the
// Telegram sink.
func DigestLink(id int64) string { return "/gmail?digest=" + strconv.FormatInt(id, 10) }

// ParseDigestLink reads the digest id out of DigestLink.
func ParseDigestLink(link string) (int64, bool) {
	rest, ok := strings.CutPrefix(link, "/gmail?digest=")
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	return id, err == nil && id > 0
}

// TelegramView builds what the Telegram sink sends for a stored digest: pages
// under the message limit, and for a daily digest the rating rows for at most
// its first eight items.
func (d *Digests) TelegramView(ctx context.Context, id int64) ([]string, [][]RatingButton, error) {
	dig, err := d.S.Repo.Digest(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	var rows [][]RatingButton
	if dig.Kind == DigestDaily {
		verdicts, _ := d.S.Repo.ThreadVerdicts(ctx)
		rows = ratingRows(dig, verdicts)
	}
	return SplitPages(dig.Body.Text, TelegramPageChars), rows, nil
}
