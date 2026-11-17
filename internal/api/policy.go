package api

import (
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/discovery"
	"applysmith/internal/gates"
)

// riskDays is the period the risk view reports on, and riskSignalLimit the
// signals it lists.
const (
	riskDays        = 30
	riskSignalLimit = 50
)

// riskView reads the detection view: the per-source challenge baseline, the
// recent signals and the cooldowns still running. It changes nothing.
func (s *Server) riskView(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	ctx := c.R.Context()
	profile, e := loadProfile(s.cfg.Workspace)
	if e != nil {
		return apikit.Reply{}, e
	}
	now := s.now()
	since := now.AddDate(0, 0, -riskDays)
	base, err := d.repo.ChallengeBaseline(ctx, since)
	if err != nil {
		return apikit.Reply{}, err
	}
	sigs, err := d.accounts.RecentSignals(ctx, since, riskSignalLimit)
	if err != nil {
		return apikit.Reply{}, err
	}
	active, err := d.accounts.ActiveCooldowns(ctx)
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.RiskView{Days: riskDays, Baseline: make([]wire.RiskBaselineRow, 0, len(base)),
		Signals: make([]wire.RiskSignal, 0, len(sigs)), Cooldowns: make([]wire.RiskCooldown, 0, len(active))}
	for _, b := range base {
		out.Baseline = append(out.Baseline, wire.RiskBaselineRow{Source: b.Source, Runs: b.Runs, Visits: b.Visits, Challenges: b.Challenges, RateLimited: b.RateLimited})
	}
	for _, g := range sigs {
		system, host := gates.SplitCooldownKey(g.System)
		row := wire.RiskSignal{System: system, Host: host, Signal: g.Signal, RunID: g.RunID, At: wire.Time{Time: g.At}}
		if g.Started() {
			row.EndsAt = apikit.TimePtr(g.EndsAt)
		}
		out.Signals = append(out.Signals, row)
	}
	for _, g := range active {
		system, host := gates.SplitCooldownKey(g.System)
		out.Cooldowns = append(out.Cooldowns, wire.RiskCooldown{Kind: "apply", System: system, Host: host, Signal: g.Signal, Until: wire.Time{Time: g.EndsAt}})
	}
	for _, src := range discovery.Sources {
		if src.Kind != discovery.KindBrowser {
			continue
		}
		if until, cooling := d.repo.CooldownUntil(ctx, src.Name, discovery.CooldownHours(profile, src.Name), ""); cooling {
			out.Cooldowns = append(out.Cooldowns, wire.RiskCooldown{Kind: "scan", System: src.Name, Signal: "blocked scan", Until: wire.Time{Time: until.UTC()}})
		}
	}
	return apikit.Reply{Data: out}, nil
}
