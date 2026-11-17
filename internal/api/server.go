package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"applysmith/internal/agent/memory"
	chatsessions "applysmith/internal/agent/sessions"
	"applysmith/internal/api/agentapi"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/applyapi"
	"applysmith/internal/api/documentsapi"
	"applysmith/internal/api/gmailapi"
	"applysmith/internal/api/knowledgeapi"
	"applysmith/internal/api/learningapi"
	"applysmith/internal/api/modelsapi"
	"applysmith/internal/api/providerapi"
	"applysmith/internal/api/schedulesapi"
	"applysmith/internal/api/signalsapi"
	"applysmith/internal/api/trackerapi"
	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/computeruse"
	"applysmith/internal/kb"
	"applysmith/internal/ledger"
	"applysmith/internal/llm"
	"applysmith/internal/remote/tunnel"
	"applysmith/internal/signals"
	"applysmith/internal/store"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

// DefaultPort is the port of a server started without --port.
const DefaultPort = 47319

const (
	headerTimeout  = 10 * time.Second
	idleTimeout    = 120 * time.Second
	shutdownWait   = 10 * time.Second
	componentEvery = 5 * time.Minute
	componentRetry = 15 * time.Second
	// knowledgeRefresh is how often the knowledge index is reconciled with
	// the files, so that the read-only story views follow edits.
	knowledgeRefresh = 30 * time.Second
	probeTimeout     = 2 * time.Second
	handlerDeadline  = 30 * time.Second
	readTimeout      = 30 * time.Second
)

// bankWatch is how often answer-bank.yml is looked at for the answerbank topic.
const bankWatch = 2 * time.Second

// Config describes one server.
type Config struct {
	Workspace *workspace.Workspace
	Vault     *vault.Vault
	Version   string
	BuildID   string // commit identifier injected at build time, "dev" otherwise
	Host      string // 127.0.0.1, ::1 or localhost; empty means 127.0.0.1
	Port      int    // 0 selects an ephemeral port
	Assets    fs.FS  // embedded dashboard, nil serves a placeholder page
	LogLevel  string // value of APPLYSMITH_LOG_LEVEL
	Now       func() time.Time

	// PreferredPort is asked for first when Port is 0; when it cannot be bound
	// the system assigns one as usual.
	PreferredPort int
	// AvoidPort is a port the system-assigned or preferred port never is: the
	// address of the workspace the desktop app just left, whose paired
	// extension would otherwise meet a server that rejects its token.
	AvoidPort int
	// Lock is the workspace lock the caller already holds; Start takes
	// ownership of it. Nil makes Start acquire the lock itself.
	Lock *Lock
	// Domain points the product capabilities at test doubles; the zero value
	// is the production wiring.
	Domain DomainOptions
	// ExtensionBuild replaces the build of the embedded extension (tests).
	ExtensionBuild func() (wire.ExtensionBuild, error)
	// SessionBrowser is the managed browser the sign-in sync copies cookies
	// into; nil means the browser is not wired to it yet.
	SessionBrowser SessionBrowser
	// CookieImport reads a user's own sign-in cookies from an installed browser
	// on this computer; nil disables the direct import (the extension sync
	// still works).
	CookieImport CookieImporter
	// PressClaimer is the duplicate guard and press claim; nil grants nothing,
	// so the extension's final submit press cannot happen.
	PressClaimer PressClaimer
	// ApplyRunLive reports whether an apply run is driving the apply window;
	// nil means none is.
	ApplyRunLive func() bool
	// ApplyWindow attaches the application chain to the managed browser's
	// apply window; nil makes every application run stop at preflight.
	ApplyWindow func(context.Context) (apply.Window, error)

	// ChromeCheck reports why Google Chrome cannot be used, for the Run tab's
	// readiness of the browser sources; nil means it is not checked.
	ChromeCheck func(context.Context) error

	// BrowserControl is the managed browser's status, screenshots and window
	// controls for the Browser page; nil answers a stopped browser.
	BrowserControl BrowserControl

	// ComputerUse is PersonalCU, the computer-use service the agent's desktop
	// tools act through; nil answers that it is not running.
	ComputerUse computeruse.Service

	// OpenInApplyWindow opens a page in a tab the user owns in the managed
	// browser's apply window and brings the window forward; nil means the
	// browser is not wired to it.
	OpenInApplyWindow func(ctx context.Context, rawURL string) error

	// ManagedBrowserPID is the process id of the managed browser this server
	// has open, 0 while none is; the Ask Agent acts in no other Chrome. Nil
	// knows none.
	ManagedBrowserPID func() int

	// Setup is the onboarding catalog behind the setup routes; nil answers
	// them unavailable.
	Setup SetupService
	// RemoteTunnel starts the supervisor of the tunnel child; nil runs the
	// cloudflared the user installed. Tests give a scripted one.
	RemoteTunnel func(tunnel.Config) TunnelProcess
	// RemoteEvery is how often policy.yml is re-read for remote access; zero
	// is the product's interval (tests shorten or lengthen it).
	RemoteEvery time.Duration

	// SnapshotDir receives the scheduler's daily user-data snapshots and must
	// lie outside the workspace; empty turns the snapshot timer off.
	SnapshotDir string

	// Home and Executable name the user's home directory and the program the
	// command link points at; empty reads them from the process.
	Home       string
	Executable string

	knowledgeEvery time.Duration // test override of knowledgeRefresh
	ownHostWait    time.Duration // test override of ownHostWait
	bankWatchEvery time.Duration // test override of bankWatch

	tunnelListen func(port int) (net.Listener, error) // test override of bindTunnel

	extra []boundRoute // routes added by in-package tests
}

// Server is the local API of one workspace.
type Server struct {
	cfg    Config
	now    func() time.Time
	mux    *http.ServeMux
	routes map[string]*boundRoute

	sessions *sessions
	token    tokenGuard
	pair     *pairing
	exts     extensionClients
	limiter  *limiter
	streams  *streamSet
	board    *board
	notifs   *eventStream // the notification stream
	launch   *launchCodes
	backups  *backupOps
	runs     *runStreams
	chat     *chatStreams
	signin   *sessionSync
	shots    shotGate
	remote   *remoteRT
	rotateMu sync.Mutex
	gzipped  gzipCache // the dashboard files in gzip form

	log     *slog.Logger
	logFile *rotatingFile

	keepAlive      time.Duration
	componentEvery time.Duration
	deadline       time.Duration // synchronous handler deadline
	readTimeout    time.Duration // request read deadline of non-stream routes
	stallAfter     time.Duration // a read running this long records the goroutine stacks
	lastStall      atomic.Int64  // unix nanoseconds of the last stall record
	epoch          string        // names this server run in stream event ids
	shutdownWait   time.Duration

	store      atomic.Pointer[store.Store]
	domain     atomic.Pointer[domain]
	components atomic.Pointer[componentSnapshot]

	lock      *Lock
	infoMu    sync.Mutex // guards info and the lock document
	info      LockInfo
	pathKey   string
	startedAt time.Time
	port      int
	hosts     map[string]bool
	listener  net.Listener
	server    *http.Server
	bg        context.Context // ends when Shutdown starts
	stopBG    context.CancelFunc
	bgWG      sync.WaitGroup // background loops that touch the store
	gmail     *gmailapi.Handlers
	kb        *knowledgeapi.Handlers
	track     *trackerapi.Handlers
	documents *documentsapi.Handlers
	agent     *agentapi.Handlers
	models    *modelsapi.Handlers
	provider  *providerapi.Handlers
	schedules *schedulesapi.Handlers
	signals   *signalsapi.Handlers
	learn     *learningapi.Handlers
	apply     *applyapi.Handlers
	github    *kb.GitHubAccount
	stopOnce  sync.Once
	done      chan struct{}
}

// New validates the configuration and builds the route table into a router.
// Nothing is bound or locked until Start.
func New(cfg Config) (*Server, error) {
	host, err := loopbackHost(cfg.Host)
	if err != nil {
		return nil, err
	}
	cfg.Host = host
	if cfg.Workspace == nil || cfg.Vault == nil {
		return nil, errors.New("api: a workspace and a vault are required")
	}
	if cfg.BuildID == "" {
		cfg.BuildID = "dev"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &Server{
		cfg: cfg, now: cfg.Now, keepAlive: keepAlive, componentEvery: componentEvery,
		deadline: handlerDeadline, readTimeout: readTimeout, stallAfter: stallAfter, epoch: newEpoch(), shutdownWait: shutdownWait,
		sessions: &sessions{now: cfg.Now}, pair: &pairing{now: cfg.Now},
		limiter: newLimiter(cfg.Now), streams: newStreamSet(), board: nil,
		done: make(chan struct{}),
	}
	s.github = s.newGitHubAccount()
	s.board = newBoard(s.epoch)
	s.notifs = newEventStream(s.epoch)
	s.launch = &launchCodes{now: cfg.Now}
	s.backups = newBackupOps(cfg.Now)
	s.runs = newRunStreams(s.epoch)
	s.chat = newChatStreams(s.epoch, cfg.Now)
	s.signin = &sessionSync{now: s.now}
	s.remote = newRemoteRT(s)
	s.gmail = gmailapi.New(gmailapi.Deps{Stack: s.gmailStack, Tracker: s.tracker, Scheduler: s.scheduler, StartPass: s.startPass,
		TrackerError: trackerapi.TrackerError, Notify: s.notify, Now: s.now, Log: func() *slog.Logger { return s.log },
		BG: func() context.Context { return s.bg }, BGWG: &s.bgWG})
	s.kb = knowledgeapi.New(knowledgeapi.Deps{Knowledge: s.knowledge, Interviews: s.interviews, StartPass: s.startPass, Store: s.store.Load,
		Router: s.llmRouter,
		Busy: func(claim string) bool {
			d, err := s.dom()
			return err == nil && d.scans.busy(claim)
		},
		Log: func() *slog.Logger { return s.log }})
	s.track = trackerapi.New(trackerapi.Deps{Tracker: s.tracker, Gmail: s.gmailStack,
		Ledger: func() *ledger.Ledger {
			if d := s.domain.Load(); d != nil {
				return d.ledger
			}
			return nil
		},
		IsRemote: requestIsRemote, Notify: s.notify, Now: s.now, Log: func() *slog.Logger { return s.log }, Workspace: cfg.Workspace})
	s.documents = documentsapi.New(documentsapi.Deps{Docs: s.docs, Tracker: s.tracker, DB: s.db, Notify: s.notify})
	s.agent = agentapi.New(agentapi.Deps{
		Memory: func() (*memory.Store, error) {
			d, err := s.dom()
			if err != nil {
				return nil, err
			}
			return d.memory, nil
		},
		Sessions: func() (*chatsessions.Store, error) {
			d, err := s.dom()
			if err != nil {
				return nil, err
			}
			return d.sessions, nil
		},
		Now: s.now})
	s.models = modelsapi.New(modelsapi.Deps{Router: s.llmRouter, DB: s.db, Vault: cfg.Vault, VaultError: vaultError, IsInfrastructure: isInfrastructure,
		Notify: s.notifyTopics, Log: func() *slog.Logger { return s.log }, BG: func() context.Context { return s.bg }, BGWG: &s.bgWG})
	s.provider = providerapi.New(providerapi.Deps{Router: s.llmRouter, DB: s.db, Vault: func() *vault.Vault { return s.cfg.Vault }, VaultError: vaultError, IsInfrastructure: isInfrastructure,
		View: s.models.View, Notify: s.notifyTopics, Now: s.now})
	s.schedules = schedulesapi.New(schedulesapi.Deps{Scheduler: s.scheduler, PackEnv: s.packEnv, SourceTitle: sourceTitle,
		Notify: s.notify, Now: s.now})
	s.signals = signalsapi.New(signalsapi.Deps{
		Signals: func() (*signals.Service, error) {
			d, err := s.dom()
			if err != nil {
				return nil, err
			}
			return d.signals, nil
		},
		Notify: s.notify})
	s.apply = applyapi.New(s.applyDeps())
	s.learn = learningapi.New(learningapi.Deps{Learning: s.learning, LearnError: learnError, TouchFill: s.touchFill, OpenQuestions: s.apply.OpenQuestions,
		MoveAnswered: func(ctx context.Context, before map[string]int) {
			s.apply.MoveAnswered(ctx, before, "", applyapi.DoneFromDashboard)
		}})
	if err := s.buildRoutes(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Server) buildRoutes() error {
	handlers := s.handlers()
	if err := checkBindings(routeDefs, handlers); err != nil {
		return err
	}
	var bound []boundRoute
	for _, r := range routeDefs {
		bound = append(bound, boundRoute{Route: r, Handler: handlers[r.Pattern()]})
	}
	bound = append(bound, s.cfg.extra...)
	defs := make([]Route, len(bound))
	for i, b := range bound {
		defs[i] = b.Route
	}
	if problems := validateRoutes(defs); len(problems) > 0 {
		return fmt.Errorf("api: invalid route table: %v", problems)
	}
	s.mux = http.NewServeMux()
	s.routes = map[string]*boundRoute{}
	for i := range bound {
		b := &bound[i]
		s.routes[b.Pattern()] = b
		s.mux.HandleFunc(b.Pattern(), func(w http.ResponseWriter, r *http.Request) { s.runRoute(b, w, r) })
	}
	return nil
}

// checkBindings requires every table route to have a handler and every handler
// to be in the table, so a route cannot be reachable without being declared.
func checkBindings(defs []Route, handlers map[string]apikit.Handler) error {
	inTable := map[string]bool{}
	for _, r := range defs {
		inTable[r.Pattern()] = true
		if handlers[r.Pattern()] == nil {
			return fmt.Errorf("api: route %s has no handler", r.Pattern())
		}
	}
	for p := range handlers {
		if !inTable[p] {
			return fmt.Errorf("api: handler %s is not in the route table", p)
		}
	}
	return nil
}

// Port is the bound port, valid after Start.
func (s *Server) Port() int { return s.port }

// Origin is the API origin of the primary listener, valid after Start. An IPv6
// address is bracketed.
func (s *Server) Origin() string {
	return "http://" + net.JoinHostPort(s.primaryHost(), strconv.Itoa(s.port))
}

// Start takes the workspace lock, loads the API token, binds the loopback
// listeners, publishes the lock document and begins serving. Health answers
// `starting` and every other route `unavailable` until Ready is called.
func (s *Server) Start(ctx context.Context) error {
	lock := s.cfg.Lock
	if lock == nil {
		var err error
		if lock, err = AcquireLock(s.cfg.Workspace.StateDir()); err != nil {
			return err
		}
	}
	if err := s.startLocked(ctx, lock); err != nil {
		_ = lock.Release()
		if s.logFile != nil {
			_ = s.logFile.Close()
		}
		return err
	}
	return nil
}

func (s *Server) startLocked(ctx context.Context, lock *Lock) error {
	stateDir := s.cfg.Workspace.StateDir()
	s.pathKey = workspace.PathKey(s.cfg.Workspace.Root)
	lf, err := openRotating(filepath.Join(stateDir, "logs", logFileName), logMaxBytes, logKeepFiles)
	if err != nil {
		return fmt.Errorf("open server log: %w", err)
	}
	s.logFile = lf
	s.log = slog.New(slog.NewTextHandler(vault.RedactWriter{W: lf}, &slog.HandlerOptions{Level: logLevel(s.cfg.LogLevel)}))

	tok, err := loadToken(s.cfg.Vault)
	if err != nil {
		return fmt.Errorf("api token: %w", err)
	}
	s.token.set(tok)

	ln, port, err := s.bind()
	if err != nil {
		return err
	}
	s.port, s.listener = port, ln
	go s.models.WarmKeyCache()
	s.startedAt = s.now().UTC()
	s.hosts = map[string]bool{"localhost:" + strconv.Itoa(port): true}
	s.hosts[net.JoinHostPort(s.primaryHost(), strconv.Itoa(port))] = true
	code, _, err := s.launch.current()
	if err != nil {
		_ = ln.Close()
		return err
	}
	info := LockInfo{PID: os.Getpid(), Port: port, PathKey: s.pathKey, BuildID: s.cfg.BuildID,
		StartedAt: s.startedAt.Format(time.RFC3339), Origin: s.Origin(), LaunchCode: code}
	if err := lock.WriteInfo(info); err != nil {
		_ = ln.Close()
		return fmt.Errorf("write lock document: %w", err)
	}
	s.infoMu.Lock()
	s.lock, s.info = lock, info
	s.infoMu.Unlock()

	s.setComponents()
	bgCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	s.bg, s.stopBG = bgCtx, stop
	s.bgWG.Add(1)
	bank := s.bankStamp() // the bank as the server found it, before any request
	go func() {
		defer s.bgWG.Done()
		s.refreshLoop(bgCtx, bank)
	}()

	s.server = &http.Server{
		Handler:           s.listenerHandler(listenLoopback),
		ReadHeaderTimeout: headerTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
	go func() { _ = s.server.Serve(ln) }()
	s.log.Info("server started", "port", port, "build", s.cfg.BuildID)
	return nil
}

// Ready hands the migrated store to the server and builds the product
// capabilities over it: the model router, tracker, discovery, the evaluation
// pool. From here on every route answers; the server owns the store and closes
// it on shutdown. A failure leaves the routes unavailable and the caller shuts
// the server down.
func (s *Server) Ready(st *store.Store) error {
	if err := s.sessions.attach(s.bg, st); err != nil {
		return fmt.Errorf("load the dashboard sessions: %w", err)
	}
	s.store.Store(st)
	d, err := s.buildDomain(s.bg, st)
	if err != nil {
		return fmt.Errorf("start the product capabilities: %w", err)
	}
	s.domain.Store(d)
	if err := s.remote.start(s.bg, st); err != nil {
		return err
	}
	s.setComponents()
	s.bgWG.Add(3)
	// Telling the waiting runs reads every one of them; it never holds the
	// start (the tray's quit check, the CLI's printed link).
	go func() {
		defer s.bgWG.Done()
		s.moveForDefaults(s.bg, st)
	}()
	go func() {
		defer s.bgWG.Done()
		s.kb.Refresh(s.bg)
	}()
	go func() {
		defer s.bgWG.Done()
		s.warmFirstLoad(s.bg, st)
	}()
	return nil
}

// warmFirstLoad pays, while the server starts, what the first dashboard load
// would otherwise wait on: opening the read connections, each parsing the
// schema, and the setup state's one-time machine probes (the macOS version,
// Chrome's --version) and keychain lookups, which the vault then keeps, and
// one read of Home's day view, which touches the tables, the Gmail password and
// the Apply Next lanes.
func (s *Server) warmFirstLoad(ctx context.Context, st *store.Store) {
	if err := st.WarmReaders(ctx); err != nil && ctx.Err() == nil {
		s.log.Warn("read connections not opened ahead", "error", err.Error())
	}
	if h, err := s.setupHost(); err == nil {
		_, _ = s.cfg.Setup.State(ctx, h)
	}
	if d, err := s.dom(); err == nil {
		_, _ = s.dayView(ctx, d, s.now(), false)
	}
}

// Done is closed once Shutdown has finished.
func (s *Server) Done() <-chan struct{} { return s.done }

// Shutdown stops accepting connections, tells open streams, waits up to ten
// seconds for requests in flight, closes whatever remains, checkpoints and
// closes the database and releases the workspace lock.
func (s *Server) Shutdown(ctx context.Context) error {
	var err error
	s.stopOnce.Do(func() {
		defer close(s.done)
		if s.stopBG != nil {
			s.stopBG()
		}
		s.remote.close()
		// A turn running now ends with its error event, published while the
		// conversation streams are still open, so a client sees the turn end
		// before the stream's shutdown event.
		if d := s.domain.Load(); d != nil {
			d.agent.Close()
		}
		s.streams.closeAll(errStreamShutdown)
		wait, cancel := context.WithTimeout(ctx, s.shutdownWait)
		defer cancel()
		if s.server != nil && s.server.Shutdown(wait) != nil {
			_ = s.server.Close()
		}
		s.backups.close()
		s.bgWG.Wait()
		if d := s.domain.Swap(nil); d != nil {
			d.close()
		}
		s.stopBrowser(ctx)
		if st := s.store.Swap(nil); st != nil {
			err = errors.Join(err, st.Close())
		}
		s.publishLaunch("")
		err = errors.Join(err, s.lock.Release())
		if s.log != nil {
			s.log.Info("server stopped")
		}
		if s.logFile != nil {
			err = errors.Join(err, s.logFile.Close())
		}
	})
	return err
}

// stopBrowser closes the managed browser this server opened or adopted: the
// runs that held a lane have ended by now, so the stop is forced. A server that
// dies abruptly never gets here, which is how the next one adopts the browser.
func (s *Server) stopBrowser(ctx context.Context) {
	if s.cfg.BrowserControl == nil {
		return
	}
	if _, err := s.cfg.BrowserControl.Stop(ctx, true); err != nil && s.log != nil {
		s.log.Warn("managed browser not stopped", "error", err.Error())
	}
}

func (s *Server) refreshLoop(ctx context.Context, bank string) {
	t := time.NewTicker(s.componentEvery)
	defer t.Stop()
	retry := time.NewTicker(componentRetry)
	defer retry.Stop()
	l := time.NewTicker(launchRefresh)
	defer l.Stop()
	kbEvery := cmpOrDuration(s.cfg.knowledgeEvery, knowledgeRefresh)
	k := time.NewTicker(kbEvery)
	defer k.Stop()
	f := time.NewTicker(fillSweepEvery)
	defer f.Stop()
	b := time.NewTicker(cmpOrDuration(s.cfg.bankWatchEvery, bankWatch))
	defer b.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.setComponents()
		case <-retry.C:
			if s.componentsNeedRetry() {
				s.setComponents()
			}
		case <-l.C:
			if _, err := s.launchCode(); err != nil {
				s.log.Warn("launch code not refreshed", "error", err.Error())
			}
		case <-k.C:
			s.kb.Refresh(ctx)
		case <-f.C:
			s.sweepFills(ctx)
		case <-b.C:
			// Every writer reaches the page this way: the dashboard, Ask
			// Agent, a run, setup, the CLI and a hand edit.
			if now := s.bankStamp(); now != bank {
				bank = now
				s.notifyTopics("answerbank")
			}
		}
	}
}

// bankStamp is answer-bank.yml's modification time and size, empty when unread.
func (s *Server) bankStamp() string {
	info, err := os.Stat(s.cfg.Workspace.Path(workspace.FileAnswerBank))
	if err != nil {
		return ""
	}
	return fmt.Sprint(info.ModTime().UnixNano(), info.Size())
}

// db is the open database, or the error a handler returns while the server is
// starting or after shutdown has closed it.
func (s *Server) db() (*store.Store, error) {
	if st := s.store.Load(); st != nil {
		return st, nil
	}
	return nil, apikit.Unavailable("the database is not available")
}

type componentSnapshot struct {
	list      []wire.Component
	checkedAt wire.Time
	probeMS   wire.SafeInt
}

// setComponents refreshes the cached health components. The database is probed
// and Gmail reads its own offline health. Model routing is checked locally;
// no health request initiates a provider call.
func (s *Server) setComponents() {
	started := time.Now()
	db := wire.Component{Name: "database", Status: wire.ComponentDown, Detail: "starting"}
	if st := s.store.Load(); st != nil {
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		if err := st.Read().PingContext(ctx); err != nil {
			stats := st.Read().Stats()
			db.Detail = fmt.Sprintf("database probe failed; read pool %d/%d in use, %d waits", stats.InUse, stats.MaxOpenConnections, stats.WaitCount)
			if errors.Is(err, context.DeadlineExceeded) {
				db.Status = wire.ComponentDegraded
				db.Detail = "database probe timed out; " + db.Detail
			}
			if s.log != nil {
				s.log.Warn("health probe failed", "component", "database", "elapsedMs", time.Since(started).Milliseconds(), "readInUse", stats.InUse, "readMax", stats.MaxOpenConnections, "readWaitCount", stats.WaitCount)
			}
		} else {
			db.Status, db.Detail = wire.ComponentOK, ""
		}
	}
	gmail := s.gmailComponent(wire.Component{Name: "gmail", Status: wire.ComponentUnconfigured, Detail: "not set up"})
	list := []wire.Component{db, s.schedulerComponent(), s.browserComponent(), s.llmComponent(), gmail, s.remoteComponent(), s.computerUseComponent()}
	s.components.Store(&componentSnapshot{list: list, checkedAt: wire.Time{Time: s.now().UTC()}, probeMS: apikit.Safe64(time.Since(started).Milliseconds())})
}

// liveComponents is the cached health components with the scheduler component
// read now, because "ticks at least every two minutes" is shorter than the
// cache, and the model routing read now, because a provider chosen in setup,
// Settings or the CLI must not read "not set up" for minutes.
func (s *Server) liveComponents(snapshot *componentSnapshot) []wire.Component {
	list := slices.Clone(snapshot.list)
	for i := range list {
		switch list[i].Name {
		case "scheduler":
			list[i] = s.schedulerComponent()
		case "llm":
			list[i] = s.llmComponent()
		case "remote":
			list[i] = s.remoteComponent()
		case "browser":
			list[i] = s.browserComponent()
		}
	}
	return list
}

func (s *Server) componentsNeedRetry() bool {
	for _, c := range s.components.Load().list {
		if (c.Name == "database" && c.Status != wire.ComponentOK) || (c.Name == "llm" && c.Status == wire.ComponentDown) {
			return true
		}
	}
	return false
}

// llmComponent reports configuration, not remote availability. It is refreshed
// with the other cached components so public health stays cheap and offline.
func (s *Server) llmComponent() wire.Component {
	c := wire.Component{Name: "llm", Status: wire.ComponentUnconfigured, Detail: "not set up"}
	d := s.domain.Load()
	if d == nil || d.router == nil {
		return c
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	table, err := d.router.Show(ctx)
	if err != nil {
		c.Status, c.Detail = wire.ComponentDown, "model routing could not be read"
		if errors.Is(err, context.DeadlineExceeded) {
			c.Detail = "model routing probe timed out"
		}
		if s.log != nil {
			s.log.Warn("health probe failed", "component", "llm", "reason", c.Detail)
		}
		return c
	}
	if len(table.Rows) == 0 {
		return c
	}
	c.Status, c.Detail = wire.ComponentOK, fmt.Sprintf("%d task routes configured; provider availability is not probed", len(table.Rows))
	for _, row := range table.Rows {
		if row.Provider == llm.OfflineProviderID {
			c.Status, c.Detail = wire.ComponentDegraded, "offline simulation is enabled"
			return c
		}
	}
	missing := len(table.Unrouted)
	if _, optional := table.Unrouted["embedding"]; optional {
		missing--
	}
	if missing > 0 || len(table.Broken) > 0 {
		c.Status, c.Detail = wire.ComponentDegraded, "some task routes are missing or invalid"
	}
	return c
}

// schedulerComponent maps the scheduler's health onto the health document.
func (s *Server) schedulerComponent() wire.Component {
	c := wire.Component{Name: "scheduler", Status: wire.ComponentDown, Detail: "the scheduler is not running"}
	d := s.domain.Load()
	if d == nil || d.sched == nil {
		return c
	}
	h := d.sched.Health()
	c.Detail = h.Detail
	switch h.Status {
	case "ok":
		c.Status = wire.ComponentOK
	case "degraded":
		c.Status = wire.ComponentDegraded
	}
	return c
}

// RotateToken replaces the API token, invalidates the old one at once and
// closes the streams opened with it. It returns the new token.
func (s *Server) RotateToken() (string, error) {
	s.rotateMu.Lock()
	defer s.rotateMu.Unlock()
	tok, err := RotateStoredToken(s.cfg.Vault)
	if err != nil {
		return "", err
	}
	s.token.set(tok)
	s.exts.forget()
	s.streams.revoke(apikit.PrincipalLocalToken, errStreamRevoked)
	return tok, nil
}

// Notify publishes a new revision of a board topic to its subscribers. File
// names the changed workspace file for the configuration topic.
func (s *Server) Notify(topic, file string) error { return s.board.notify(topic, file) }

func cmpOrDuration(v, def time.Duration) time.Duration {
	if v == 0 {
		return def
	}
	return v
}
