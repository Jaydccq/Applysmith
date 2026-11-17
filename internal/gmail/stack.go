package gmail

import (
	"context"
	"log/slog"
	"net"
	"time"

	"applysmith/internal/gmail/classify"
	"applysmith/internal/ledger"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
	"applysmith/internal/vault"
)

// Options configure a Stack. The zero value is production: Gmail's IMAP
// server, the shipped taxonomy and the wall clock.
type Options struct {
	// Dial replaces the connection to Gmail's IMAP server (tests).
	Dial     func(ctx context.Context) (net.Conn, error)
	Now      func() time.Time
	Hooks    Hooks
	Taxonomy *classify.Taxonomy
	Windows  SignupWindowSource
	Linker   Linker
	// Alerts receives the instant alerts of the stack; nil drops them.
	Alerts AlertSink
	// Tracker enables the link stage, the outcomes and the inbox's candidacy
	// links. Without one the sync classifies and stores only.
	Tracker *tracker.Tracker
	// Model writes summaries, drafts and digest text; nil gives the
	// deterministic texts.
	Model Model
	Log   *slog.Logger
	// NoWatch keeps the watcher stopped: tests that start every sync
	// themselves.
	NoWatch bool
}

// Stack wires the Gmail pieces of one workspace together.
type Stack struct {
	Repo    *Repo
	Account *Account
	Client  *Client
	Syncer  *Syncer
	// Watcher starts the syncs as mail arrives; the server runs it.
	Watcher *Watcher
	Lookups *Lookups
	Service *Service
	Alerts  AlertSink
	// Pipeline links signals and applies their outcomes; nil without a tracker.
	Pipeline *Pipeline
	Digests  *Digests
	Model    Model
	// Drafter drafts the threads linked to an application; nil drafts every
	// thread from its own mail.
	Drafter Drafter
	Log     *slog.Logger

	noWatch bool
}

// StartWatch starts the watcher, unless the options keep it stopped.
func (s *Stack) StartWatch() {
	if !s.noWatch {
		s.Watcher.Start()
	}
}

// Open builds the stack over a workspace database and its vault.
func Open(st *store.Store, v *vault.Vault, o Options) *Stack {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	tax := o.Taxonomy
	if tax == nil {
		tax = classify.Defaults()
	}
	repo := NewRepo(st, now)
	account := NewAccount(v, repo, now, o.Hooks)
	client := NewClient(account, st)
	if o.Dial != nil {
		client.Dial = o.Dial
	}
	s := &Stack{Repo: repo, Account: account, Client: client, Alerts: o.Alerts, Model: o.Model, Log: o.Log, noWatch: o.NoWatch}
	linker := o.Linker
	if o.Tracker != nil {
		s.Pipeline = &Pipeline{Repo: repo, Tracker: o.Tracker, Ledger: ledger.New(st, now)}
		if linker == nil {
			linker = s.Pipeline
		}
	}
	s.Syncer = &Syncer{Client: client, Repo: repo, Classify: NewClassifyFunc(tax, o.Windows), Version: classify.Version, Linker: linker,
		PostSync: func(ctx context.Context) {
			_, _ = s.EvaluateAlerts(ctx)
			s.BackfillSummaries(ctx)
		}}
	s.Lookups = &Lookups{Client: client, Now: now, Account: repo.Mailbox}
	s.Service = &Service{Client: client, Account: account, Repo: repo, Now: now, Demo: st.Demo}
	s.Watcher = &Watcher{Client: client, Syncer: s.Syncer, Repo: repo, Readable: s.Service.Readable, Log: o.Log}
	s.Digests = &Digests{S: s, Now: now}
	return s
}
