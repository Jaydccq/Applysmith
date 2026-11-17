package llm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"applysmith/internal/store"
)

const (
	// attemptFloor is the least deadline time worth starting an attempt for:
	// successful calls almost never finish inside it, so a shorter attempt only
	// spends a provider slot.
	attemptFloor = 500 * time.Millisecond
	// defaultStall is the silence after which a streaming attempt fails.
	defaultStall = 45 * time.Second
	// maxRetryAfter is the longest 429 delay waited out on the same candidate.
	maxRetryAfter = 10 * time.Second
	// capRefresh is how long a read of the day's spend is reused.
	capRefresh = 15 * time.Second
	// ledgerWriteTimeout bounds a best-effort ledger write.
	ledgerWriteTimeout = 2 * time.Second
	// offlineProvider is the provider id offline simulation routes to.
	offlineProvider = "offline"
)

// OfflineProviderID is the provider id that offline simulation reports.
const OfflineProviderID = offlineProvider

// Usage counts tokens. Input is the total including CachedInput and
// CacheWrite; Output includes Reasoning. CacheWrite is the part of Input that
// the provider billed as a prompt-cache write.
type Usage struct {
	Input       int
	Output      int
	CachedInput int
	CacheWrite  int
	Reasoning   int
}

// AlertSink delivers the daily cost-cap alert through the notification policy
// and reports whether a channel accepted it. Only the server process installs
// one.
type AlertSink func(ctx context.Context, a CapAlert) bool

// CapAlert is the content of the daily cost-cap alert.
type CapAlert struct {
	Day      string
	CapUSD   float64
	SpendUSD float64
	Message  string
}

// Router performs model calls. Routing comes from the settings, the
// environment and the active preset, all read on every call; the only state it
// keeps in memory is the rests of candidates that failed lately (walk.go) and
// the agent conversations' last answerers (affinity.go) and the calls of each
// rotate group (groups.go), lost on restart. It is safe for concurrent use.
type Router struct {
	store    *store.Store
	keys     Vault
	getenv   func(string) string
	clock    func() time.Time
	log      *slog.Logger
	http     *http.Client
	fake     *Fake
	offline  bool
	sink     AlertSink
	catalog  string
	stall    time.Duration
	sleep    func(context.Context, time.Duration) error
	warned   sync.Map
	mu       sync.Mutex
	limiters map[string]*limiter
	capCache capCache
	restMu   sync.Mutex
	rests    map[string]Rest
	// sticks are the agent conversations' last answerers (affinity.go), under restMu.
	sticks map[string]stick
	// rotor counts the calls of each rotate group (groups.go), under restMu.
	rotor map[string]int
	// chatgpt is the sign-in and the token of the chatgpt kind (chatgpt_auth.go).
	chatgpt chatgptState
}

// Option configures a Router.
type Option func(*Router)

// WithGetenv replaces os.Getenv, so tests control environment overrides.
func WithGetenv(fn func(string) string) Option { return func(r *Router) { r.getenv = fn } }

// WithClock replaces the clock used for the ledger, the cap and the alert.
func WithClock(fn func() time.Time) Option { return func(r *Router) { r.clock = fn } }

// WithLogger sets the logger; the default discards.
func WithLogger(l *slog.Logger) Option { return func(r *Router) { r.log = l } }

// dialTimeout bounds making a connection to a provider. With the default of
// 30 seconds, every first call of an agent turn waited that long on a
// provider whose address did not answer; the provider SDKs give it 5.
const dialTimeout = 10 * time.Second

// apiTransport is the default transport with a bounded dial.
func apiTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext
	return t
}

// WithHTTPClient replaces the HTTP client of the API kinds.
func WithHTTPClient(c *http.Client) Option { return func(r *Router) { r.http = c } }

// WithFake registers the fixtures that serve the fake kind.
func WithFake(f *Fake) Option { return func(r *Router) { r.fake = f } }

// WithOfflineSimulation routes every task to the fake provider, so no network
// or subprocess call is made. It is the only way every task reaches the fake
// provider.
func WithOfflineSimulation() Option { return func(r *Router) { r.offline = true } }

// WithAlertSink installs the cost-cap alert sink. A process without one
// downgrades its own calls but never claims the day's alert.
func WithAlertSink(s AlertSink) Option { return func(r *Router) { r.sink = s } }

// WithCatalogURL replaces the OpenRouter model catalog URL.
func WithCatalogURL(u string) Option { return func(r *Router) { r.catalog = u } }

// New returns a router over the workspace database and the credentials vault.
func New(st *store.Store, keys Vault, opts ...Option) *Router {
	r := &Router{
		store: st, keys: keys, getenv: os.Getenv, clock: time.Now,
		log:     slog.New(slog.DiscardHandler),
		http:    &http.Client{Transport: apiTransport()},
		catalog: defaultCatalogURL, stall: defaultStall,
		limiters: map[string]*limiter{}, rests: map[string]Rest{}, sticks: map[string]stick{}, rotor: map[string]int{},
		sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-t.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
	for _, o := range opts {
		o(r)
	}
	if r.fake == nil {
		r.fake = NewFake()
	}
	return r
}

// warnOnce logs a message once per process for a key.
func (r *Router) warnOnce(key, msg string) {
	if _, loaded := r.warned.LoadOrStore(key+"|"+msg, true); !loaded {
		r.log.Warn("llm: " + msg)
	}
}

func newCallID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "call_" + hex.EncodeToString(b[:])
}
