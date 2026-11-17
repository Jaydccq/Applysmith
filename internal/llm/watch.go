package llm

import (
	"context"
	"errors"
	"sort"
)

// Reasons the watch has nothing to read.
var (
	ErrWatchNoProvider = errors.New("no OpenRouter provider is configured")
	ErrWatchNoKey      = errors.New("the OpenRouter provider has no API key stored")
)

// WatchReady says whether the watch has anything to do: an OpenRouter provider
// is configured and, when withKey is set, its key is stored. The catalog is
// public, but a routed model is only of interest to a user who can call it.
// The key check reads the vault, so a caller that runs often leaves it off.
func (r *Router) WatchReady(ctx context.Context, withKey bool) error {
	s, err := LoadSettings(ctx, r.store)
	if err != nil {
		return err
	}
	found := false
	for _, p := range s.Providers {
		if p.Preset != "openrouter" {
			continue
		}
		found = true
		if !withKey {
			return nil
		}
		if _, err := r.keys.Get(KeyName(p.ID)); err == nil {
			return nil
		}
	}
	if found {
		return ErrWatchNoKey
	}
	return ErrWatchNoProvider
}

// GoneModel is a routed OpenRouter model that the live catalog no longer lists.
type GoneModel struct {
	Task  string
	Model string
}

// RoutedModelsMissing checks every OpenRouter candidate of every effective
// routing row except embedding against the live catalog. It never changes a
// row. An unreachable catalog is an error and reports nothing; the daily job
// owns the alert, its once-until-fixed memory and the one-hour retry.
func (r *Router) RoutedModelsMissing(ctx context.Context) ([]GoneModel, error) {
	s, err := LoadSettings(ctx, r.store)
	if err != nil {
		return nil, err
	}
	hasOpenRouter := false
	for _, p := range s.Providers {
		hasOpenRouter = hasOpenRouter || p.Preset == "openrouter"
	}
	if !hasOpenRouter {
		return nil, nil
	}
	cat, err := r.FetchCatalog(ctx)
	if err != nil {
		return nil, err
	}
	var gone []GoneModel
	seen := map[GoneModel]bool{}
	for _, t := range tasks {
		if t.Class == ClassEmbedding {
			continue
		}
		res, err := r.resolveWith(s, t.ID)
		if err != nil {
			continue
		}
		for _, c := range s.listed(res) {
			p, ok := s.Provider(c.Provider)
			g := GoneModel{t.ID, c.Model}
			if !ok || p.Preset != "openrouter" || seen[g] {
				continue
			}
			if _, listed := cat[c.Model]; !listed {
				seen[g] = true
				gone = append(gone, g)
			}
		}
	}
	sort.Slice(gone, func(i, j int) bool { return gone[i].Task+gone[i].Model < gone[j].Task+gone[j].Model })
	return gone, nil
}
