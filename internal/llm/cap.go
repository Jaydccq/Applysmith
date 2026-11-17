package llm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"
)

type capCache struct {
	mu     sync.Mutex
	at     time.Time
	day    daySpend
	loaded bool
	logged map[string]bool
	// calls counts the day's ledger attempts per "provider/model" for the
	// usage groups (groups.go). It is written under mu: a read of the ledger
	// replaces it, and countCall adds the calls made since.
	calls map[string]int
	// alertDay and alertNext spare the alert claim's write transaction: on
	// alertDay no claim is tried before alertNext, which is set after each claim.
	alertDay  string
	alertNext time.Time
}

// daySpend is the day's metered spend, in all and per task, and the limits it
// is held to. Its maps are shared and never written after the read.
type daySpend struct {
	total, cap      float64
	byTask, budgets map[string]float64
}

func (d daySpend) capReached() bool { return d.cap > 0 && d.total >= d.cap }

// overBudget reports whether a task's spend today has reached its budget.
func (d daySpend) overBudget(task string) bool {
	b := d.budgets[task]
	return b > 0 && d.byTask[task] >= b
}

// localDay names the clock's day: the date in the zone of the clock's time,
// which time.Now gives in this computer's zone.
func localDay(t time.Time) string { return t.Format("2006-01-02") }

// dayBounds runs from the day's local midnight to the next one, so a day
// across a daylight-saving change lasts 23 or 25 hours.
func dayBounds(t time.Time) (string, string) {
	y, m, d := t.Date()
	return timeText(time.Date(y, m, d, 0, 0, 0, 0, t.Location())), timeText(time.Date(y, m, d+1, 0, 0, 0, 0, t.Location()))
}

// spend returns the day's metered spend and limits, reusing a read for 15
// seconds of the same day unless force is set. A failed read counts as zero
// spend and is logged once per day; it never fails a call.
func (r *Router) spend(ctx context.Context, force bool) daySpend {
	c := &r.capCache
	c.mu.Lock()
	defer c.mu.Unlock()
	now := r.now()
	if c.loaded && !force && now.Sub(c.at) >= 0 && now.Sub(c.at) < capRefresh && localDay(now) == localDay(c.at) {
		return c.day
	}
	day := daySpend{cap: DefaultDailyCapUSD}
	var calls map[string]int
	lo, hi := dayBounds(now)
	s, err := LoadSettings(ctx, r.store)
	if err == nil {
		day.cap, day.budgets = s.DailyUSDCap, s.Budgets
		day.byTask, err = r.spendByTask(ctx, lo, hi)
	}
	if err == nil {
		calls, err = r.callsByModel(ctx, lo, hi)
	}
	if err != nil {
		day.byTask, calls = nil, nil
		if c.logged == nil {
			c.logged = map[string]bool{}
		}
		if d := localDay(now); !c.logged[d] {
			c.logged[d] = true
			r.log.Error("llm: the day's spend could not be read; the cap reads as not reached", "err", err)
		}
	}
	for _, usd := range day.byTask {
		day.total += usd
	}
	c.at, c.day, c.calls, c.loaded = now, day, calls, true
	return day
}

func (r *Router) callsByModel(ctx context.Context, lo, hi string) (map[string]int, error) {
	rows, err := r.store.Read().QueryContext(ctx, `SELECT provider_id, model, COUNT(*) FROM llm_ledger
		WHERE started_at >= ? AND started_at < ? GROUP BY provider_id, model`, lo, hi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var provider, model string
		var n int
		if err := rows.Scan(&provider, &model, &n); err != nil {
			return nil, err
		}
		out[provider+"/"+model] = n
	}
	return out, rows.Err()
}

// dayCalls returns the day's ledger attempts per "provider/model", from the
// same 15-second read as the spend.
func (r *Router) dayCalls(ctx context.Context) map[string]int {
	r.spend(ctx, false)
	c := &r.capCache
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.calls)
}

// countCall adds a call that was just routed to a model, so calls that come
// before the next read spread over a usage group instead of all going to the
// member that was least used at the last one.
func (r *Router) countCall(m Fallback) {
	c := &r.capCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls == nil {
		c.calls = map[string]int{}
	}
	c.calls[m.Provider+"/"+m.Model]++
}

func (r *Router) spendByTask(ctx context.Context, lo, hi string) (map[string]float64, error) {
	rows, err := r.store.Read().QueryContext(ctx, `SELECT task, SUM(cost_usd) FROM llm_ledger
		WHERE billing = 'metered' AND cost_usd IS NOT NULL AND started_at >= ? AND started_at < ? GROUP BY task`, lo, hi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var task string
		var usd float64
		if err := rows.Scan(&task, &usd); err != nil {
			return nil, err
		}
		out[task] = usd
	}
	return out, rows.Err()
}

// capReached reports whether the day's spend has reached an enabled cap.
func (r *Router) capReached(ctx context.Context) bool {
	d := r.spend(ctx, false)
	if d.capReached() {
		r.raiseCapAlert(ctx, d.total, d.cap)
		return true
	}
	return false
}

// TaskSpend returns each task's metered spend of the day, which its budget is
// held to.
func (r *Router) TaskSpend(ctx context.Context) map[string]float64 {
	return maps.Clone(r.spend(ctx, false).byTask)
}

// BudgetReached reports whether a task's spend today has reached its budget,
// from the read the call path uses.
func (r *Router) BudgetReached(ctx context.Context, task string) bool {
	return r.spend(ctx, false).overBudget(task)
}

// evaluationTasks are the tasks an evaluation job calls.
var evaluationTasks = []string{"jd-extract", "quick-screen", "deep-eval"}

// EvaluationHeld reports whether unattended evaluation waits for the day: the
// metered spend has reached an enabled daily cap, or an evaluation task's has
// reached its budget. The inbox drain then queues nothing more and a quick
// screen's review waits instead of going deep (F-1, F-8).
func (r *Router) EvaluationHeld(ctx context.Context) bool {
	d := r.spend(ctx, false)
	return d.capReached() || slices.ContainsFunc(evaluationTasks, d.overBudget)
}

// EvaluationUnrouted returns the first evaluation task that has no model it
// can use, or "" when each has one. Every evaluation would end at no-route,
// which decides nothing about its posting, so a scan's top rows and the inbox
// drain queue nothing while one is unrouted.
func (r *Router) EvaluationUnrouted(ctx context.Context) string {
	for _, t := range evaluationTasks {
		var le *Error
		if _, err := r.Resolve(ctx, t); errors.As(err, &le) && le.Kind == KindNoRoute {
			return t
		}
	}
	return ""
}

// Tick is the scheduler's cap check: it re-reads the spend and raises the
// day's alert without waiting for the next model call.
func (r *Router) Tick(ctx context.Context) {
	if d := r.spend(ctx, true); d.capReached() {
		r.raiseCapAlert(ctx, d.total, d.cap)
	}
}

const alertRetry = 10 * time.Minute

// raiseCapAlert claims the day's alert in the database and delivers it. A
// process without a sink never claims, so the server still can. A claim that
// no channel accepted can be taken again after ten minutes.
func (r *Router) raiseCapAlert(ctx context.Context, spend, capUSD float64) {
	if r.sink == nil {
		return
	}
	now := r.now()
	day := localDay(now)
	c := &r.capCache
	c.mu.Lock()
	wait := c.alertDay == day && now.Before(c.alertNext)
	c.mu.Unlock()
	if wait {
		return
	}
	// A claim that is refused or not delivered can be taken again only after
	// alertRetry; a delivered alert is the day's last.
	next := now.Add(alertRetry)
	defer func() {
		c.mu.Lock()
		c.alertDay, c.alertNext = day, next
		c.mu.Unlock()
	}()
	var claimed bool
	err := r.store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO llm_cap_alerts (day, claimed_at, delivered) VALUES (?, ?, 0)
			ON CONFLICT(day) DO UPDATE SET claimed_at = excluded.claimed_at WHERE delivered = 0 AND claimed_at <= ?`,
			day, timeText(now), timeText(now.Add(-alertRetry)))
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		claimed = n == 1
		return err
	})
	if err != nil {
		r.log.Error("llm: cap alert claim failed", "err", err)
		next = now
		return
	}
	if !claimed {
		return
	}
	alert := CapAlert{Day: day, CapUSD: capUSD, SpendUSD: spend,
		Message: fmt.Sprintf("The daily model spend cap of USD %.2f is reached (spend USD %.2f). Later calls use cheaper models. Change the cap with the daily_usd_cap setting.", capUSD, spend)}
	if !r.deliver(ctx, alert) {
		return
	}
	next = now.Add(48 * time.Hour)
	if err := r.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE llm_cap_alerts SET delivered = 1 WHERE day = ?`, day)
		return err
	}); err != nil {
		r.log.Error("llm: cap alert mark failed", "err", err)
	}
}

// deliver calls the sink; a panicking sink counts as not delivered.
func (r *Router) deliver(ctx context.Context, a CapAlert) (ok bool) {
	defer func() {
		if p := recover(); p != nil {
			r.log.Error("llm: cap alert sink panicked", "panic", p)
			ok = false
		}
	}()
	return r.sink(ctx, a)
}

// downgradeOrder serves a call over the cap from the cheapest model. The
// choice is the lowest-priced model among the candidates and the fast-class
// model of the primary provider's preset, priced strictly below the primary
// and not a free-tier id. The result is that head, then the candidates no
// dearer than it, then the configured primary, which the walk reaches only
// after an auth failure on the head. When the primary is unpriced, free or
// subscription, or nothing is cheaper, the configured order stands.
func (r *Router) downgradeOrder(ctx context.Context, s Settings, cands []candidate) []candidate {
	if len(cands) == 0 {
		return cands
	}
	primary := cands[0]
	best, ok := r.pairCost(ctx, s, primary.provider, primary.model)
	if !ok {
		return cands
	}
	type option struct {
		c    candidate
		cost float64
		from int // index in cands, -1 for the preset's fast model
	}
	var pick *option
	consider := func(c candidate, from int) {
		cost, ok := r.pairCost(ctx, s, c.provider, c.model)
		if !ok || cost >= best || isFreeModel(c.model) {
			return
		}
		if pick == nil || cost < pick.cost {
			pick = &option{c, cost, from}
		}
	}
	for i, c := range cands[1:] {
		consider(c, i+1)
	}
	if pr, ok := PresetByName(primary.provider.Preset); ok {
		if fast := pr.Models[ClassFast]; fast != "" && fast != primary.model {
			consider(candidate{provider: primary.provider, model: fast, source: "primary"}, -1)
		}
	}
	if pick == nil {
		return cands
	}
	// Only the head and the candidates no dearer than it may serve the call; the
	// configured primary is kept behind them for an auth failure on the head.
	head := pick.c
	head.downgraded = true
	out := []candidate{head}
	for i, cand := range cands {
		if i == 0 || i == pick.from {
			continue
		}
		if cost, ok := r.pairCost(ctx, s, cand.provider, cand.model); ok && cost <= pick.cost {
			cand.downgraded = true
			out = append(out, cand)
		}
	}
	primary.authOnly = true
	return append(out, primary)
}
