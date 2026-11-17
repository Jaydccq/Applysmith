package discovery

import (
	"context"

	"applysmith/internal/workspace"
)

// DefaultSeedSources are the public slug lists `registry seed` reads when no
// source is named. They are addresses only; the lists are fetched at setup
// and never committed, and each fetch records its address, hash and licence.
func DefaultSeedSources() []SeedSource {
	const base = "https://raw.githubusercontent.com/Feashliaa/job-board-aggregator/main/data/"
	return []SeedSource{
		{URL: base + "greenhouse_companies.json", ATS: Greenhouse, Licence: "MIT"},
		{URL: base + "lever_companies.json", ATS: Lever, Licence: "MIT"},
		{URL: base + "ashby_companies.json", ATS: Ashby, Licence: "MIT"},
	}
}

// RefreshResult is what a registry refresh did.
type RefreshResult struct {
	Seeds []SeedResult
	Probe ProbeResult
}

// New is the number of boards the refresh added.
func (r RefreshResult) New() int {
	n := 0
	for _, s := range r.Seeds {
		n += s.Inserted
	}
	return n
}

// RegistryRefresh re-runs the seed sources, adding new rows only, and probes
// the dead boards that are due. It fails only when every seed source failed.
func RegistryRefresh(ctx context.Context, seeder *Seeder, sc *Scanner, portals workspace.Portals, sources []SeedSource) (RefreshResult, error) {
	if len(sources) == 0 {
		sources = DefaultSeedSources()
	}
	var out RefreshResult
	out.Seeds = seeder.Seed(ctx, sources)
	if err := SeedFailed(out.Seeds); err != nil {
		return out, err
	}
	probe, err := sc.ProbeDead(ctx, portals)
	out.Probe = probe
	return out, err
}
