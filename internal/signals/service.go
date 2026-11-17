package signals

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"applysmith/internal/discovery"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

// TickBudget is the wall-clock budget of one tick.
const TickBudget = 20 * time.Minute

// KeyExa is the keychain name of the Exa key, which the web-search provider
// shares with the exa discovery source.
const KeyExa = "api-key-exa"

// Settings are the user's portals.yml and profile.yml at the start of a tick.
type Settings struct {
	Portals workspace.Portals
	Profile workspace.Profile
}

// Deps are the outside world of the pipeline.
type Deps struct {
	Store     *store.Store
	Discovery *discovery.Repo
	// Model is the model router; nil leaves identification and assessment
	// pending.
	Model Model
	// Settings reads portals.yml and profile.yml.
	Settings func(ctx context.Context) (Settings, error)
	// Secret reads a keychain secret by name, empty when absent.
	Secret    func(name string) string
	HTTP      *http.Client
	Endpoints Endpoints
	// Search replaces the web-search provider; the default is Exa when its key
	// exists.
	Search   SearchProvider
	Leads    LeadSink
	Notifier Notifier
	// Enqueuer starts an evaluation for a handed-off posting.
	Enqueuer discovery.Enqueuer
	Now      func() time.Time
	Sleep    func(context.Context, time.Duration) error
	Log      *slog.Logger
	// Budget overrides the 20 minute tick budget (tests).
	Budget time.Duration
}

// Service runs ticks and serves the signal read models.
type Service struct {
	d      Deps
	repo   *Repo
	disc   *discovery.Repo
	model  Model
	fetch  *Fetcher
	log    *slog.Logger
	now    func() time.Time
	policy Policy
	// hook fails a stage by name; tests use it.
	hook func(stage string) error
	// deadline ends ctx with errTickBudget when the service's clock reaches
	// at; tests replace it to end a share of the budget on their own clock.
	deadline func(ctx context.Context, at time.Time) (context.Context, context.CancelFunc)
}

// New builds the service.
func New(d Deps) (*Service, error) {
	if d.Store == nil {
		return nil, errors.New("signals: a store is required")
	}
	p, err := LoadPolicy()
	if err != nil {
		return nil, err
	}
	s := &Service{d: d, disc: d.Discovery, now: d.Now, log: d.Log, policy: p}
	if s.now == nil {
		s.now = time.Now
	}
	s.deadline = func(ctx context.Context, at time.Time) (context.Context, context.CancelFunc) {
		return context.WithTimeoutCause(ctx, at.Sub(s.now()), errTickBudget)
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if d.Model != nil {
		s.model = d.Model
	}
	if d.Secret == nil {
		s.d.Secret = func(string) string { return "" }
	}
	s.repo = NewRepo(d.Store, s.now)
	s.fetch = NewFetcher(d.HTTP)
	s.fetch.Now = s.now
	if d.Sleep != nil {
		s.fetch.Sleep = d.Sleep
	}
	return s, nil
}

// Repo exposes the repository to the API and the command line.
func (s *Service) Repo() *Repo { return s.repo }

// Policy is the shipped scoring policy.
func (s *Service) Policy() Policy { return s.policy }

func (s *Service) budget() time.Duration {
	if s.d.Budget > 0 {
		return s.d.Budget
	}
	return TickBudget
}

func (s *Service) settings(ctx context.Context) (Settings, error) {
	if s.d.Settings == nil {
		return Settings{}, nil
	}
	return s.d.Settings(ctx)
}

// SearchProvider is the configured web-search provider, or nil. The key is
// read on every call, so one stored later applies at once.
func (s *Service) SearchProvider() SearchProvider { return s.searchProvider() }

// searchProvider is the configured web-search provider, or nil.
func (s *Service) searchProvider() SearchProvider {
	if s.d.Search != nil {
		return s.d.Search
	}
	if key := s.d.Secret(KeyExa); key != "" {
		return &ExaSearch{Fetch: s.fetch, Base: s.d.Endpoints.exa(), Key: key}
	}
	return nil
}
