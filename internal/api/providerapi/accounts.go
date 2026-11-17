// Package providerapi serves the model providers in the local API: adding,
// changing and removing a provider, its token prices, and the subscription
// accounts whose plan usage the Models page shows. internal/api registers the
// handlers.
package providerapi

import (
	"context"
	"errors"
	"math"
	"net/url"
	"slices"
	"strings"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/llm"
	"applysmith/internal/providerusage"
	"applysmith/internal/store"
	"applysmith/internal/vault"
)

// Deps is what the provider handlers use from the server.
type Deps struct {
	Router func() (*llm.Router, error)
	DB     func() (*store.Store, error)
	Vault  func() *vault.Vault // the server's, read on each call
	// VaultError and IsInfrastructure are the error mappings api's other
	// routes share.
	VaultError       func(error) error
	IsInfrastructure func(error) bool
	// View is the Models page a provider change answers with.
	View   func(ctx context.Context, router *llm.Router) (wire.LLMRouting, error)
	Notify func(topics ...string)
	Now    func() time.Time
}

// Handlers serves the provider routes.
type Handlers struct {
	Deps
	Usage providerusage.Cache // the last plan usage read of each account
}

func New(d Deps) *Handlers { return &Handlers{Deps: d} }

func providerConfig(id string, req wire.LLMProviderRequest) (llm.ProviderConfig, error) {
	if req.BaseURL != "" {
		u, err := url.Parse(req.BaseURL)
		if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return llm.ProviderConfig{}, apikit.InvalidField("baseUrl", "the endpoint must not contain credentials, a query or a fragment")
		}
	}
	p := llm.ProviderConfig{ID: id, Kind: req.Kind}
	if req.Preset != "" {
		preset, ok := llm.PresetByName(req.Preset)
		if !ok {
			return p, apikit.InvalidField("preset", "unknown provider preset")
		}
		p = preset.ProviderConfig()
		p.ID = id
		if req.Kind != "" && req.Kind != p.Kind {
			return p, apikit.InvalidField("kind", "kind must match the preset")
		}
	} else if p.Kind != llm.KindOpenAI && p.Kind != llm.KindAnthropic {
		return p, apikit.InvalidField("kind", "a custom provider must be openai-compatible or anthropic")
	}
	if p.Kind == llm.KindCLI || p.Kind == llm.KindChatGPT {
		if p.ID != p.Preset {
			return p, apikit.InvalidField("preset", "this preset has one sign-in context; use its preset name as the provider id")
		}
		if req.BaseURL != "" || req.Program != "" || req.StructuredOutput != nil || req.Embeddings != nil {
			return p, apikit.InvalidField("preset", "sign-in presets do not accept endpoint or capability overrides")
		}
	} else {
		if req.BaseURL != "" {
			p.BaseURL = req.BaseURL
		}
		if req.Program != "" {
			return p, apikit.InvalidField("program", "API providers do not run a program")
		}
		if req.StructuredOutput != nil {
			p.StructuredOutput = *req.StructuredOutput
		}
		if req.Embeddings != nil {
			p.Embeddings = *req.Embeddings
		}
	}
	if err := p.Validate(); err != nil {
		return p, apikit.InvalidField("provider", "%s", err.Error())
	}
	return p, nil
}

// PutProvider adds an account without selecting a preset or rewriting any
// task route. An endpoint change first requires removing its stored key, so a
// credential cannot silently move to a different host.
func (s *Handlers) PutProvider(c *apikit.Call) (apikit.Reply, error) {
	router, err := s.Router()
	if err != nil {
		return apikit.Reply{}, err
	}
	st, err := s.DB()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.LLMProviderRequest
	if err := apikit.Decode(c.Body, &req); err != nil {
		return apikit.Reply{}, err
	}
	p, err := providerConfig(c.R.PathValue("provider"), req)
	if err != nil {
		return apikit.Reply{}, err
	}
	err = llm.UpdateSettings(c.R.Context(), st, s.Now(), func(settings *llm.Settings) error {
		if old, ok := settings.Provider(p.ID); ok {
			if old.BaseURL != p.BaseURL || old.Kind != p.Kind {
				keyErr := s.Vault().Check(llm.KeyName(p.ID))
				if keyErr == nil {
					return apikit.NewErr(wire.CodeConflict, "remove the provider key before changing its endpoint or kind")
				}
				var ve *vault.Error
				if !errors.As(keyErr, &ve) || ve.Code != vault.CodeNotFound {
					return s.VaultError(keyErr)
				}
			}
			for i := range settings.Providers {
				if settings.Providers[i].ID == p.ID {
					settings.Providers[i] = p
					break
				}
			}
		} else {
			settings.Providers = append(settings.Providers, p)
		}
		// Settings normally preserves already stored rows. Revalidate the rows
		// this edit affects so replacing a capability cannot break a live route.
		for id, route := range settings.Routes {
			if routeUsesProvider(*settings, route, p.ID) {
				if err := settings.ValidateRoute(id, route); err != nil {
					return apikit.NewErr(wire.CodeConflict, "%s", err.Error())
				}
			}
		}
		for name, group := range settings.Groups {
			if slices.ContainsFunc(group.Members, func(f llm.Fallback) bool { return f.Provider == p.ID }) {
				if err := settings.ValidateGroup(name, group); err != nil {
					return apikit.NewErr(wire.CodeConflict, "%s", err.Error())
				}
			}
		}
		return nil
	})
	if err != nil {
		return apikit.Reply{}, err
	}
	s.Notify("configuration")
	view, err := s.View(c.R.Context(), router)
	return apikit.Reply{Data: view}, err
}

func routeUsesProvider(settings llm.Settings, route llm.Route, id string) bool {
	if route.Provider == id || slices.ContainsFunc(route.Fallbacks, func(f llm.Fallback) bool { return f.Provider == id }) {
		return true
	}
	return route.Provider == llm.GroupProvider && slices.ContainsFunc(settings.Groups[route.Model].Members, func(f llm.Fallback) bool { return f.Provider == id })
}

func providerReferenced(settings llm.Settings, id string) bool {
	if settings.Preset == id || settings.PresetFallback == id {
		return true
	}
	for _, route := range settings.Routes {
		if route.Provider == id || slices.ContainsFunc(route.Fallbacks, func(f llm.Fallback) bool { return f.Provider == id }) {
			return true
		}
	}
	for _, group := range settings.Groups {
		if slices.ContainsFunc(group.Members, func(f llm.Fallback) bool { return f.Provider == id }) {
			return true
		}
	}
	return false
}

// DeleteProvider checks references while the settings write is serialized.
// Key removal must succeed before configuration is removed. If the database
// write then fails, the provider remains configured without a key and is safe
// to retry; no credential is restored or left orphaned by a successful delete.
func (s *Handlers) DeleteProvider(c *apikit.Call) (apikit.Reply, error) {
	router, err := s.Router()
	if err != nil {
		return apikit.Reply{}, err
	}
	st, err := s.DB()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("provider")
	err = llm.UpdateSettings(c.R.Context(), st, s.Now(), func(settings *llm.Settings) error {
		if _, ok := settings.Provider(id); !ok {
			return apikit.NewErr(wire.CodeNotFound, "provider is not configured")
		}
		if providerReferenced(*settings, id) {
			return apikit.NewErr(wire.CodeConflict, "provider is in use; change its preset, task routes and groups before removing it")
		}
		source, keyErr := s.Vault().Test(llm.KeyName(id))
		if keyErr == nil && source == vault.SourceEnv {
			return apikit.NewErr(wire.CodeConflict, "the provider key is supplied by the environment; unset it before removing the provider")
		}
		var ve *vault.Error
		if keyErr != nil && (!errors.As(keyErr, &ve) || ve.Code != vault.CodeNotFound) {
			return s.VaultError(keyErr)
		}
		if err := router.ClearKey(id); err != nil && (!errors.As(err, &ve) || ve.Code != vault.CodeNotFound) {
			return s.VaultError(err)
		}
		settings.Providers = slices.DeleteFunc(settings.Providers, func(p llm.ProviderConfig) bool { return p.ID == id })
		for key := range settings.Prices {
			if strings.HasPrefix(key, id+"/") {
				delete(settings.Prices, key)
			}
		}
		return nil
	})
	if err != nil {
		return apikit.Reply{}, err
	}
	router.ClearRests(id)
	s.Notify("configuration")
	view, err := s.View(c.R.Context(), router)
	return apikit.Reply{Data: view}, err
}

func (s *Handlers) PutPrice(c *apikit.Call) (apikit.Reply, error) {
	router, err := s.Router()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.LLMPriceRequest
	if err := apikit.Decode(c.Body, &req); err != nil {
		return apikit.Reply{}, err
	}
	if err := llm.ValidModelID(req.Model); err != nil {
		return apikit.Reply{}, apikit.InvalidField("model", "%s", err.Error())
	}
	for field, value := range map[string]*float64{"inputPerM": req.InputPerM, "cachedInputPerM": req.CachedInputPerM, "outputPerM": req.OutputPerM} {
		if value == nil {
			return apikit.Reply{}, apikit.InvalidField(field, "each token price must be supplied explicitly; use zero for a free token category")
		}
		if *value < 0 || math.IsNaN(*value) || math.IsInf(*value, 0) {
			return apikit.Reply{}, apikit.InvalidField(field, "prices must be finite, nonnegative USD amounts per million tokens")
		}
	}
	st, err := s.DB()
	if err != nil {
		return apikit.Reply{}, err
	}
	settings, err := llm.LoadSettings(c.R.Context(), st)
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("provider")
	if _, ok := settings.Provider(id); !ok {
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "provider is not configured")
	}
	if err := router.SetPrice(c.R.Context(), id, req.Model, llm.Price{InputPerM: *req.InputPerM, CachedInputPerM: *req.CachedInputPerM, OutputPerM: *req.OutputPerM}); err != nil {
		if s.IsInfrastructure(err) {
			return apikit.Reply{}, err
		}
		return apikit.Reply{}, apikit.InvalidField("price", "%s", err.Error())
	}
	s.Notify("configuration")
	view, err := s.View(c.R.Context(), router)
	return apikit.Reply{Data: view}, err
}
