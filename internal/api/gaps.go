package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/evaluate"
	"applysmith/internal/kb"
	"applysmith/internal/research"
	"applysmith/internal/store"
)

// gapError maps a skill-gap failure onto the API errors.
func gapError(err error) error {
	switch {
	case errors.Is(err, evaluate.ErrGapInvalid):
		return apikit.NewErr(wire.CodeValidationFailed, "%s", strings.TrimPrefix(err.Error(), evaluate.ErrGapInvalid.Error()+": "))
	case errors.Is(err, store.ErrNotFound):
		return apikit.NewErr(wire.CodeNotFound, "no such skill-gap analysis")
	case errors.Is(err, store.ErrConflict):
		return apikit.NewErr(wire.CodeConflict, "this Idempotency-Key was used with a different request")
	}
	return err
}

func (s *Server) gapService() (*evaluate.Gaps, error) {
	d, err := s.dom()
	if err != nil {
		return nil, err
	}
	return d.gaps, nil
}

func WireGapReport(r evaluate.GapReport) wire.GapReport {
	out := wire.GapReport{ID: r.ID, GeneratedAt: apikit.Stamp(r.GeneratedAt), Selected: make([]wire.SafeInt, len(r.Selected)), Sample: make([]wire.GapInput, len(r.Sample)),
		Excluded: make([]wire.GapExclusion, len(r.Excluded)), Gaps: make([]wire.Gap, len(r.Gaps)), Inferred: make([]wire.GapInferred, len(r.Inferred)), Done: apikit.NonNil(r.Done)}
	for i, n := range r.Selected {
		out.Selected[i] = apikit.Safe64(n)
	}
	for i, in := range r.Sample {
		out.Sample[i] = wire.GapInput{Report: apikit.Safe64(in.Report), CreatedAt: apikit.Stamp(in.CreatedAt), ContentHash: in.ContentHash}
	}
	for i, x := range r.Excluded {
		out.Excluded[i] = wire.GapExclusion{Report: apikit.Safe64(x.Report), Reason: x.Reason, DuplicateOf: apikit.Safe64(x.DuplicateOf)}
	}
	for i, g := range r.Gaps {
		w := wire.Gap{Skill: g.Skill, Label: g.Label, State: g.State, Required: apikit.Safe(g.Required), Preferred: apikit.Safe(g.Preferred), Sources: make([]wire.GapSource, len(g.Sources))}
		for j, s := range g.Sources {
			w.Sources[j] = wire.GapSource{Report: apikit.Safe64(s.Report), Company: s.Company, Role: s.Role, Requirement: s.Requirement, Quote: s.Quote, Class: s.Class,
				Evidence: s.Evidence, Unsupported: s.Unsupported}
		}
		out.Gaps[i] = w
	}
	for i, x := range r.Inferred {
		out.Inferred[i] = wire.GapInferred{Report: apikit.Safe64(x.Report), Company: x.Company, Role: x.Role, Label: x.Label, Evidence: x.Evidence}
	}
	if p := r.Plan; p != nil {
		plan := wire.GapPlan{GeneratedAt: apikit.Stamp(p.GeneratedAt), Items: make([]wire.GapPlanItem, len(p.Items)), Lookup: wire.GapLookup{Status: p.Lookup.Status, Reason: p.Lookup.Reason}}
		for i, it := range p.Items {
			item := wire.GapPlanItem{Skill: it.Skill, Label: it.Label, Exercise: it.Exercise, Criterion: it.Criterion, Effort: it.Effort, Done: it.Done, Resources: make([]wire.GapResource, len(it.Resources))}
			for j, r := range it.Resources {
				item.Resources[j] = wire.GapResource{Title: r.Title, URL: r.URL}
			}
			plan.Items[i] = item
		}
		out.Plan = &plan
	}
	return out
}

// createGapReport starts an analysis under the run contract: the run exists,
// carries the idempotency key and has ended by the time the 202 is sent,
// because the analysis reads saved reports only and calls no model.
func (s *Server) createGapReport(c *apikit.Call) (apikit.Reply, error) {
	key, e := apikit.IdempotencyKey(c)
	if e != nil {
		return apikit.Reply{}, e
	}
	var w wire.GapRequest
	if e := apikit.Decode(c.Body, &w); e != nil {
		return apikit.Reply{}, e
	}
	g, err := s.gapService()
	if err != nil {
		return apikit.Reply{}, err
	}
	nums := make([]int64, len(w.Reports))
	for i, n := range w.Reports {
		nums[i] = int64(n)
	}
	id, replayed, err := g.Start(c.R.Context(), c.P.ID, key, nums)
	if err != nil {
		return apikit.Reply{}, gapError(err)
	}
	if !replayed {
		s.notify("runs")
		s.notify("reports")
	}
	return apikit.Reply{Status: http.StatusAccepted, Location: "/api/runs/" + id, Data: wire.RunAccepted{RunID: id}}, nil
}

func (s *Server) listGapReports(c *apikit.Call) (apikit.Reply, error) {
	g, err := s.gapService()
	if err != nil {
		return apikit.Reply{}, err
	}
	list, err := g.List(c.R.Context())
	if err != nil {
		return apikit.Reply{}, gapError(err)
	}
	out := wire.GapList{Analyses: make([]wire.GapSummary, len(list))}
	for i, x := range list {
		out.Analyses[i] = wire.GapSummary{ID: x.ID, GeneratedAt: apikit.Stamp(x.GeneratedAt), Sample: apikit.Safe(x.Sample), Gaps: apikit.Safe(x.Gaps)}
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Server) getGapReport(c *apikit.Call) (apikit.Reply, error) {
	g, err := s.gapService()
	if err != nil {
		return apikit.Reply{}, err
	}
	rep, err := g.Get(c.R.Context(), c.R.PathValue("id"))
	if err != nil {
		return apikit.Reply{}, gapError(err)
	}
	return apikit.Reply{Data: WireGapReport(rep)}, nil
}

func (s *Server) confirmGap(c *apikit.Call) (apikit.Reply, error) {
	var w wire.GapConfirmRequest
	if e := apikit.Decode(c.Body, &w); e != nil {
		return apikit.Reply{}, e
	}
	g, err := s.gapService()
	if err != nil {
		return apikit.Reply{}, err
	}
	rep, err := g.Confirm(c.R.Context(), c.R.PathValue("id"), w.Skill, w.Confirmed)
	if err != nil {
		return apikit.Reply{}, gapError(err)
	}
	s.notify("reports")
	return apikit.Reply{Data: WireGapReport(rep)}, nil
}

func (s *Server) planGaps(c *apikit.Call) (apikit.Reply, error) {
	var w wire.GapPlanRequest
	if e := apikit.Decode(c.Body, &w); e != nil {
		return apikit.Reply{}, e
	}
	g, err := s.gapService()
	if err != nil {
		return apikit.Reply{}, err
	}
	rep, err := g.BuildPlan(c.R.Context(), c.R.PathValue("id"), w.Skills, w.LookupResources)
	if err != nil {
		return apikit.Reply{}, gapError(err)
	}
	s.notify("reports")
	return apikit.Reply{Data: WireGapReport(rep)}, nil
}

func (s *Server) gapProgress(c *apikit.Call) (apikit.Reply, error) {
	var w wire.GapProgressRequest
	if e := apikit.Decode(c.Body, &w); e != nil {
		return apikit.Reply{}, e
	}
	g, err := s.gapService()
	if err != nil {
		return apikit.Reply{}, err
	}
	rep, err := g.SetProgress(c.R.Context(), c.R.PathValue("id"), w.Skill, w.Done)
	if err != nil {
		return apikit.Reply{}, gapError(err)
	}
	s.notify("reports")
	return apikit.Reply{Data: WireGapReport(rep)}, nil
}

// gapResources uses the same research route as Ask Agent. Search citations are
// candidates only: a guarded fetch must confirm each page before it is listed.
type gapResources struct {
	service *research.Service
	fetch   kb.Fetcher
}

func (g gapResources) Find(ctx context.Context, skill string, limit int) ([]evaluate.Resource, error) {
	if limit <= 0 {
		return nil, nil
	}
	limit = min(limit, evaluate.MaxResources)
	ans, err := g.service.Research(ctx, research.Request{Question: "Find official documentation or practical tutorials for learning " + skill + ". Describe each resource briefly with its source."})
	if errors.Is(err, research.ErrNoSource) {
		return nil, evaluate.ErrResourceSourceUnavailable
	}
	if err != nil {
		return nil, err
	}
	var out []evaluate.Resource
	seen := map[string]bool{}
	for _, src := range ans.Sources {
		if len(out) >= limit {
			break
		}
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		u, err := url.Parse(src.URL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || seen[src.URL] {
			continue
		}
		seen[src.URL] = true
		page, err := g.fetch.Fetch(ctx, src.URL)
		if err != nil || page == nil || page.Status < 200 || page.Status > 299 || len(page.Body) == 0 {
			continue
		}
		title := strings.TrimSpace(src.Title)
		if title == "" {
			title = u.Hostname()
		}
		out = append(out, evaluate.Resource{Title: title, URL: page.URL, Verified: true})
	}
	if len(ans.Sources) > 0 && len(out) == 0 {
		return nil, fmt.Errorf("no retrieved learning-resource page could be verified")
	}
	return out, nil
}
