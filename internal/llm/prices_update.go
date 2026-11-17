package llm

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const defaultCatalogURL = "https://openrouter.ai/api/v1/models"

// driftTolerance is the relative price difference the drift check reports.
const driftTolerance = 0.05

// CatalogEntry is one model of OpenRouter's public catalog, with prices in USD
// per million tokens.
type CatalogEntry struct {
	ID    string
	Price Price
	// HasCache says the catalog listed a cached-input price.
	HasCache bool
}

// FetchCatalog reads OpenRouter's public model catalog.
func (r *Router) FetchCatalog(ctx context.Context) (map[string]CatalogEntry, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, r.catalog, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("model catalog unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model catalog answered HTTP %d", resp.StatusCode)
	}
	var body struct {
		Data []struct {
			ID      string `json:"id"`
			Pricing struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
				CacheRead  string `json:"input_cache_read"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("model catalog unreadable: %w", err)
	}
	out := map[string]CatalogEntry{}
	for _, m := range body.Data {
		in, err1 := strconv.ParseFloat(m.Pricing.Prompt, 64)
		outp, err2 := strconv.ParseFloat(m.Pricing.Completion, 64)
		if err1 != nil || err2 != nil || in < 0 || outp < 0 {
			continue // a variable-priced or free entry has no usable price
		}
		cached, hasCache := in, false
		if c, err := strconv.ParseFloat(m.Pricing.CacheRead, 64); err == nil && c >= 0 {
			cached, hasCache = c, true
		}
		out[m.ID] = CatalogEntry{ID: m.ID, HasCache: hasCache, Price: Price{
			InputPerM: perMillion(in), CachedInputPerM: perMillion(cached), OutputPerM: perMillion(outp)}}
	}
	return out, nil
}

func perMillion(perToken float64) float64 { return math.Round(perToken*1e6*1e6) / 1e6 }

// PriceChange is a refreshed price that differs from the one in use.
type PriceChange struct {
	Key string
	Old *Price
	New Price
}

const openRouterPrefix = "openrouter/"

// routedOpenRouterModels lists the OpenRouter model ids that any stored row,
// the active preset or the price tables name.
func (r *Router) knownOpenRouterIDs(ctx context.Context, s Settings) (map[string]bool, error) {
	ids := map[string]bool{}
	for k := range embeddedPrices {
		if id, ok := strings.CutPrefix(k, openRouterPrefix); ok {
			ids[id] = true
		}
	}
	rows, err := r.store.Read().QueryContext(ctx, `SELECT key FROM llm_prices WHERE key LIKE 'openrouter/%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		ids[strings.TrimPrefix(k, openRouterPrefix)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range s.routedModels(func(p ProviderConfig) bool { return p.Preset == "openrouter" }) {
		ids[id] = true
	}
	return ids, nil
}

// routedModels lists the model ids of every candidate of every stored and
// preset row whose provider satisfies keep.
func (s Settings) routedModels(keep func(ProviderConfig) bool) []string {
	seen := map[string]bool{}
	add := func(prov, model string) {
		if p, ok := s.Provider(prov); ok && keep(p) {
			seen[model] = true
		}
	}
	for _, row := range s.Routes {
		add(row.Provider, row.Model)
		for _, f := range row.Fallbacks {
			add(f.Provider, f.Model)
		}
	}
	for _, g := range s.Groups {
		for _, m := range g.Members {
			add(m.Provider, m.Model)
		}
	}
	for _, t := range tasks {
		if row, ok := presetRow(s, t); ok {
			add(row.Provider, row.Model)
			for _, f := range row.Fallbacks {
				add(f.Provider, f.Model)
			}
		}
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// UpdatePrices refreshes the price of every known OpenRouter id from the live
// catalog, stores each with its source and fetch time, and returns the ones
// whose price changed.
func (r *Router) UpdatePrices(ctx context.Context) ([]PriceChange, error) {
	cat, err := r.FetchCatalog(ctx)
	if err != nil {
		return nil, err
	}
	s, err := LoadSettings(ctx, r.store)
	if err != nil {
		return nil, err
	}
	ids, err := r.knownOpenRouterIDs(ctx, s)
	if err != nil {
		return nil, err
	}
	orProv := ProviderConfig{ID: "openrouter", Preset: "openrouter"}
	var changes []PriceChange
	err = r.store.Write(ctx, func(tx *sql.Tx) error {
		names := make([]string, 0, len(ids))
		for id := range ids {
			names = append(names, id)
		}
		sort.Strings(names)
		for _, id := range names {
			entry, ok := cat[id]
			if !ok {
				continue
			}
			key := openRouterPrefix + id
			var old *Price
			var cur Price
			err := tx.QueryRowContext(ctx, `SELECT input_per_m, cached_per_m, output_per_m FROM llm_prices WHERE key = ?`, key).
				Scan(&cur.InputPerM, &cur.CachedInputPerM, &cur.OutputPerM)
			switch {
			case err == nil:
				old = &cur
			case err == sql.ErrNoRows:
				if e, ok := embeddedPrices[priceKey(orProv, id)]; ok {
					old = &e
				}
			default:
				return err
			}
			newPrice := entry.Price
			if !entry.HasCache && old != nil {
				newPrice.CachedInputPerM = old.CachedInputPerM // the catalog says nothing about caching
			}
			if old == nil || !samePrice(*old, newPrice) {
				changes = append(changes, PriceChange{Key: key, Old: old, New: newPrice})
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO llm_prices (key, input_per_m, cached_per_m, output_per_m, source, fetched_at)
				VALUES (?,?,?,?,?,?) ON CONFLICT(key) DO UPDATE SET input_per_m = excluded.input_per_m,
				cached_per_m = excluded.cached_per_m, output_per_m = excluded.output_per_m, source = excluded.source, fetched_at = excluded.fetched_at`,
				key, newPrice.InputPerM, newPrice.CachedInputPerM, newPrice.OutputPerM, r.catalog, timeText(r.now())); err != nil {
				return err
			}
		}
		return nil
	})
	return changes, err
}

func samePrice(a, b Price) bool {
	const eps = 1e-9
	return math.Abs(a.InputPerM-b.InputPerM) < eps && math.Abs(a.CachedInputPerM-b.CachedInputPerM) < eps && math.Abs(a.OutputPerM-b.OutputPerM) < eps
}

// PriceDrift is one difference between the embedded table and the catalog.
type PriceDrift struct {
	Key      string  `json:"key"`
	Field    string  `json:"field,omitempty"`
	Embedded float64 `json:"embedded,omitempty"`
	Live     float64 `json:"live,omitempty"`
	// Missing marks a model the catalog no longer lists.
	Missing bool `json:"missing,omitempty"`
}

// CheckPriceDrift compares the embedded OpenRouter prices with the live
// catalog (tolerance 5 percent) and records the result. It is a report, not a
// gate.
func (r *Router) CheckPriceDrift(ctx context.Context) ([]PriceDrift, error) {
	cat, err := r.FetchCatalog(ctx)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(embeddedPrices))
	for k := range embeddedPrices {
		if strings.HasPrefix(k, openRouterPrefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	drift := []PriceDrift{}
	for _, k := range keys {
		emb := embeddedPrices[k]
		live, ok := cat[strings.TrimPrefix(k, openRouterPrefix)]
		if !ok {
			drift = append(drift, PriceDrift{Key: k, Missing: true})
			continue
		}
		for _, f := range []struct {
			name string
			e, l float64
		}{{"input", emb.InputPerM, live.Price.InputPerM}, {"output", emb.OutputPerM, live.Price.OutputPerM}} {
			if f.e == 0 && f.l == 0 {
				continue
			}
			if math.Abs(f.l-f.e) > driftTolerance*math.Max(f.e, f.l) {
				drift = append(drift, PriceDrift{Key: k, Field: f.name, Embedded: f.e, Live: f.l})
			}
		}
	}
	raw, _ := json.Marshal(drift)
	err = r.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO llm_price_checks (checked_at, drift_json) VALUES (?, ?)`, timeText(r.now()), string(raw))
		return err
	})
	return drift, err
}
