package llm

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// PatchRoutes replaces the named rows in full; a nil row clears the task's
// row. The patch is validated as a whole before anything changes.
func (r *Router) PatchRoutes(ctx context.Context, patch map[string]*Route) error {
	return r.PatchRouting(ctx, patch, nil)
}

// PatchRouting applies routing rows and groups in one transaction; a nil group
// deletes it, which is refused while a row still names it. Rows are validated
// against the groups of the same patch.
func (r *Router) PatchRouting(ctx context.Context, routes map[string]*Route, groups map[string]*Group) error {
	return UpdateSettings(ctx, r.store, r.now(), func(s *Settings) error {
		for id := range routes {
			if _, ok := LookupTask(id); !ok {
				return fmt.Errorf("routes.%s: unknown task id", id)
			}
		}
		patched, err := s.WithPatch(routes, groups)
		*s = patched
		return err
	})
}

// SetProvider adds a provider or replaces the entry with the same id.
func (r *Router) SetProvider(ctx context.Context, p ProviderConfig) error {
	return UpdateSettings(ctx, r.store, r.now(), func(s *Settings) error {
		for i := range s.Providers {
			if s.Providers[i].ID == p.ID {
				s.Providers[i] = p
				return nil
			}
		}
		s.Providers = append(s.Providers, p)
		return nil
	})
}

// SetDailyCap sets daily_usd_cap; 0 disables the cap.
func (r *Router) SetDailyCap(ctx context.Context, usd float64) error {
	return UpdateSettings(ctx, r.store, r.now(), func(s *Settings) error {
		s.DailyUSDCap = usd
		return nil
	})
}

// SetBudgets sets the named tasks' daily budgets; 0 removes a budget.
func (r *Router) SetBudgets(ctx context.Context, budgets map[string]float64) error {
	return UpdateSettings(ctx, r.store, r.now(), func(s *Settings) error {
		for id, usd := range budgets {
			if usd == 0 {
				delete(s.Budgets, id)
			} else {
				s.Budgets[id] = usd
			}
		}
		return nil
	})
}

// SetPrice stores a user price for a custom model; it wins over every other
// price source. The key is "<provider id>/<model>".
func (r *Router) SetPrice(ctx context.Context, providerID, model string, p Price) error {
	return UpdateSettings(ctx, r.store, r.now(), func(s *Settings) error {
		if _, ok := s.Provider(providerID); !ok {
			return fmt.Errorf("provider %q is not configured", providerID)
		}
		s.Prices[providerID+"/"+model] = p
		return nil
	})
}

// ConfigureOptions tunes ConfigurePreset.
type ConfigureOptions struct {
	// Fallback is the preset name of a provider whose equivalent model becomes
	// the first fallback of every task of the preset.
	Fallback string
}

// SetupError carries guidance for the user to fix a configuration step.
type SetupError struct{ Message string }

func (e *SetupError) Error() string { return e.Message }

// ConfigurePreset adds the preset's provider when missing and makes the preset
// active. It fails with setup guidance when the provider has no key (or, for a
// command-line preset, no usable program), and leaves explicit table rows
// untouched.
func (r *Router) ConfigurePreset(ctx context.Context, name string, opts ConfigureOptions) error {
	p, ok := PresetByName(name)
	if !ok {
		return fmt.Errorf("unknown preset %q; valid presets: %s", name, strings.Join(PresetNames(), ", "))
	}
	if err := r.requireUsable(ctx, p); err != nil {
		return err
	}
	var fb Preset
	if opts.Fallback != "" {
		var ok bool
		if fb, ok = PresetByName(opts.Fallback); !ok {
			return fmt.Errorf("unknown fallback preset %q; valid presets: %s", opts.Fallback, strings.Join(PresetNames(), ", "))
		}
		if err := r.requireUsable(ctx, fb); err != nil {
			return err
		}
	}
	return UpdateSettings(ctx, r.store, r.now(), func(s *Settings) error {
		for _, pp := range []Preset{p, fb} {
			if pp.Name == "" {
				continue
			}
			cfg := pp.ProviderConfig()
			if have, ok := s.Provider(cfg.ID); ok {
				if have.Kind != cfg.Kind {
					return fmt.Errorf("provider %q exists with kind %s; remove it first", cfg.ID, have.Kind)
				}
				continue
			}
			s.Providers = append(s.Providers, cfg)
		}
		s.Preset, s.PresetFallback = p.Name, fb.Name
		s.Offline = false
		return nil
	})
}

// SetOffline selects or leaves offline simulation: with it on, every task is
// served by the fake provider and no key or program is needed.
func (r *Router) SetOffline(ctx context.Context, on bool) error {
	return UpdateSettings(ctx, r.store, r.now(), func(s *Settings) error {
		s.Offline = on
		return nil
	})
}

// requireUsable fails with guidance when the preset's provider has no key or
// program.
func (r *Router) requireUsable(ctx context.Context, p Preset) error {
	if p.Kind == KindChatGPT {
		st, err := r.ChatGPTStatus(ctx)
		if err != nil {
			return err
		}
		if !st.PlanUsage {
			return &SetupError{Message: "the " + p.Name + " preset needs a ChatGPT sign-in with plan usage: sign in under Settings, Providers or with `applysmith llm signin chatgpt`"}
		}
		return nil
	}
	if p.Kind == KindCLI {
		if _, err := CheckCLI(ctx, p.Program); err != nil {
			return &SetupError{Message: err.Error()}
		}
		return nil
	}
	if _, err := r.keys.Get(KeyName(p.Name)); err != nil {
		return &SetupError{Message: fmt.Sprintf("the %s preset needs an API key: create one at %s and store it with `applysmith llm keys set %s`", p.Name, p.KeyURL, p.Name)}
	}
	return nil
}

// SetKey stores a provider's API key in the vault. The value never leaves the
// vault again except to authorize a request.
func (r *Router) SetKey(providerID, key string) error {
	if err := ValidateAPIKey(key); err != nil {
		return err
	}
	_, err := r.keys.Set(KeyName(providerID), key)
	if err == nil {
		r.ClearRests(providerID)
	}
	return err
}

// ClearKey removes a provider's stored key.
func (r *Router) ClearKey(providerID string) error {
	return r.keys.Delete(KeyName(providerID))
}

func (r *Router) now() time.Time { return r.clock() }
