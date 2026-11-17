package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/discovery"
	"applysmith/internal/workspace"
)

// registrySeedClaim keeps a second seed out while one runs.
const registrySeedClaim = "registry-seed"

// boardOf is the board a portals entry names: its ats and slug, else the board
// its careers address resolves to.
func boardOf(c workspace.Company) (discovery.Board, bool) {
	if c.ATS != "" && c.Slug != "" {
		return discovery.Board{ATS: c.ATS, Slug: strings.ToLower(c.Slug)}, true
	}
	b, err := discovery.ResolveBoard(c.CareersURL)
	return b, err == nil
}

// portalCompanies answers the company table of the Sources and portals tab:
// each entry of portals.yml with the registry's last scan time and job count
// of its board, and the revision that structured edits quote.
func (s *Server) portalCompanies(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	portals, e := loadPortals(s.cfg.Workspace)
	if e != nil {
		return apikit.Reply{}, e
	}
	out := wire.PortalCompanyList{Companies: make([]wire.PortalCompany, 0, len(portals.Companies))}
	for _, co := range portals.Companies {
		row := wire.PortalCompany{Name: co.Name, Enabled: co.IsEnabled(), ATS: co.ATS, Slug: co.Slug, CareersURL: co.CareersURL, OwnTitleFilter: co.Filters.Title != nil}
		if b, ok := boardOf(co); ok {
			row.Board = b.Key()
			reg, err := d.repo.RegistryGet(c.R.Context(), b)
			switch {
			case err == nil:
				row.Status = reg.Status
				row.LastScannedAt = apikit.TimePtr(reg.LastScanned)
				if !reg.LastScanned.IsZero() {
					n := apikit.Safe(reg.LastJobCount)
					row.JobCount = &n
				}
			case !errors.Is(err, sql.ErrNoRows):
				return apikit.Reply{}, err
			}
		}
		out.Companies = append(out.Companies, row)
	}
	return apikit.Reply{Data: out}, nil
}

// portalTargetRoles plans "Use my target roles" against portals.yml as it is
// now; the dashboard writes the plan through the revision-checked patch.
func (s *Server) portalTargetRoles(c *apikit.Call) (apikit.Reply, error) {
	out := wire.PortalTargetRoles{Lines: []string{}, Ops: []wire.ConfigOp{}}
	cur, err := s.cfg.Workspace.Read(workspace.FilePortals)
	if err != nil {
		return apikit.Reply{}, err
	}
	if cur.Revision == workspace.RevisionAbsent {
		return apikit.Reply{Data: out}, nil
	}
	portals, rep := workspace.ParsePortals(cur.Data)
	if !rep.OK() {
		return apikit.Reply{}, apikit.NewErr(wire.CodeConfigInvalid, "portals.yml is invalid: %s", rep.String())
	}
	profile, e := loadProfile(s.cfg.Workspace)
	if e != nil {
		return apikit.Reply{}, e
	}
	plan := discovery.PlanTargetRoles(portals, profile)
	out.Revision, out.Lines = cur.Revision, append(out.Lines, plan.Lines()...)
	for _, op := range plan.Ops() {
		out.Ops = append(out.Ops, wire.ConfigOp{Op: string(op.Kind), Path: op.Path, Value: op.Value})
	}
	return apikit.Reply{Data: out}, nil
}

// registryStatus is the count shown beside the seed button.
func (s *Server) registryStatus(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	portals, e := loadPortals(s.cfg.Workspace)
	if e != nil {
		return apikit.Reply{}, e
	}
	stats, err := d.repo.RegistryStats(c.R.Context())
	if err != nil {
		return apikit.Reply{}, err
	}
	seeded, err := d.repo.RegistryLastSeeded(c.R.Context())
	if err != nil {
		return apikit.Reply{}, err
	}
	a, dead, blocked := stats[discovery.StatusActive], stats[discovery.StatusDead], stats[discovery.StatusBlocked]
	return apikit.Reply{Data: wire.PortalRegistry{Active: apikit.Safe(a), Dead: apikit.Safe(dead), Blocked: apikit.Safe(blocked), Total: apikit.Safe(a + dead + blocked),
		LastSeededAt: apikit.TimePtr(seeded), Enabled: portals.Registry.IsEnabled()}}, nil
}

// detectPortal previews what a name or a careers address names. Nothing is
// saved; the page writes the entry through the revision-checked portals patch.
func (s *Server) detectPortal(c *apikit.Call) (apikit.Reply, error) {
	var req wire.PortalDetectRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	portals, e := loadPortals(s.cfg.Workspace)
	if e != nil {
		return apikit.Reply{}, e
	}
	det, err := s.newScanner(d, workspace.Profile{}, nil).Detect(c.R.Context(), req.Input)
	switch {
	case errors.Is(err, discovery.ErrCareersScheme):
		return apikit.Reply{}, apikit.InvalidField("input", "%s", err.Error())
	case err != nil && det.Kind == "":
		return apikit.Reply{}, apikit.InvalidField("input", "%s", err.Error())
	case err != nil:
		return apikit.Reply{}, err
	}
	inPortals := map[string]bool{}
	for _, co := range portals.Companies {
		if b, ok := boardOf(co); ok {
			inPortals[b.Key()] = true
		}
	}
	out := wire.PortalDetection{Input: det.Input, Kind: det.Kind, Name: det.Name, CareersURL: det.CareersURL, Note: det.Note, Candidates: make([]wire.PortalCandidate, 0, len(det.Candidates))}
	for _, cand := range det.Candidates {
		_, regErr := d.repo.RegistryGet(c.R.Context(), cand.Board)
		if regErr != nil && !errors.Is(regErr, sql.ErrNoRows) {
			return apikit.Reply{}, regErr
		}
		out.Candidates = append(out.Candidates, wire.PortalCandidate{ATS: cand.ATS, Slug: cand.Slug, Company: cand.Company, Jobs: apikit.Safe(cand.Jobs), Live: cand.Live,
			Reason: cand.Reason, InRegistry: regErr == nil, InPortals: inPortals[cand.Key()]})
	}
	return apikit.Reply{Data: out}, nil
}

// seedRegistry is `applysmith registry seed` for the dashboard: the default
// public lists are downloaded one after another, so the run stream shows
// each one and a cancel stops between lists.
func (s *Server) seedRegistry(c *apikit.Call) (apikit.Reply, error) {
	key, e := apikit.IdempotencyKey(c)
	if e != nil {
		return apikit.Reply{}, e
	}
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	return s.startPass(c.R.Context(), apikit.Pass{Operation: "registry.seed", Kind: "registry", Stage: "seed", Claim: registrySeedClaim,
		Busy: "the registry is already being seeded", Key: key, Principal: c.P.ID, Input: []byte("{}"),
		Run: func(ctx context.Context, progress func(string)) (string, error) {
			return s.seedDefaultLists(ctx, d, progress)
		}})
}

func (s *Server) seedDefaultLists(ctx context.Context, d *domain, progress func(string)) (string, error) {
	seeder := &discovery.Seeder{Repo: d.repo, Dir: filepath.Join(s.cfg.Workspace.StateDir(), "downloads"), HTTP: s.cfg.Domain.ScanHTTP}
	var results []discovery.SeedResult
	added := 0
	for _, src := range discovery.DefaultSeedSources() {
		if err := ctx.Err(); err != nil {
			return fmt.Sprintf("%d new boards before it was cancelled", added), err
		}
		res := seeder.Seed(ctx, []discovery.SeedSource{src})[0]
		results = append(results, res)
		added += res.Inserted
		line := fmt.Sprintf("%s: %d rows, %d new, %d rejected", res.URL, res.Rows, res.Inserted, res.Rejected)
		if res.Err != nil {
			line = fmt.Sprintf("%s: %v", res.URL, res.Err)
		}
		progress(line)
	}
	summary := fmt.Sprintf("%d new boards from %d lists", added, len(results))
	return summary, discovery.SeedFailed(results)
}
