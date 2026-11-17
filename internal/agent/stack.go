package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"applysmith/internal/agent/approvals"
	"applysmith/internal/agent/memory"
	"applysmith/internal/agent/sessions"
)

// StackConfig is everything the Ask Agent needs from the rest of the product.
type StackConfig struct {
	Tools    ToolDeps
	Sessions *sessions.Store
	Router   Router
	// Sink receives the events of every turn, for the conversation streams.
	Sink Sink
	// StateDir holds the audit salt and, below it, the agent directory.
	StateDir string
	// PiPath is the pi executable; empty looks `pi` up on PATH.
	PiPath string
	// BaseURL returns the loopback origin of the API, which the extension calls.
	BaseURL  func() string
	Launcher Launcher
	Now      func() time.Time
	Log      *slog.Logger
	// Summarizer writes compaction checkpoints; nil switches compaction off.
	Summarizer memory.Completer
	// ProtectedRoots are the directories whose paths the disclosure gate keeps
	// out of an answer, besides StateDir: the source tree of a checkout.
	ProtectedRoots []string
	// Local is the zone in which "today" ends for a standing approval; nil is
	// the machine's own.
	Local *time.Location
	// Harness supplies the active harness version; nil is the empty harness.
	Harness HarnessSource
	// Tasks carries delegated tasks on by themselves; its store is
	// Tools.Tasks. Nil means a task never takes a turn of its own.
	Tasks *TaskConfig
}

// Stack is the assembled agent: the service the API calls and the bridge its
// pi processes call.
type Stack struct {
	Agent      *Agent
	Bridge     *Bridge
	Supervisor *Supervisor
	Registry   *Registry
	// Confirmations holds the cards and standing approvals of held calls.
	Confirmations *Confirmations
}

// NewStack builds the registry, the guard chain, the bridge, the supervisor and
// the service, and settles what a stopped process left behind.
func NewStack(cfg StackConfig) (*Stack, error) {
	if cfg.Tools.Tasks == nil {
		cfg.Tasks = nil
	} else if cfg.Tasks != nil {
		cfg.Tasks.Store = cfg.Tools.Tasks
	}
	reg, err := BuildRegistry(cfg.Tools)
	if err != nil {
		return nil, err
	}
	salt, err := auditSalt(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	cards := approvals.New(cfg.Tools.Store, cfg.Sessions, cfg.Now, cfg.Local)
	if cfg.Tasks != nil {
		cfg.Tasks.CardsWaiting = func(ctx context.Context, sessionID string) (bool, error) {
			list, err := cards.OfSession(ctx, sessionID, cardsShown)
			for _, c := range list {
				if c.State == approvals.Pending {
					return true, err
				}
			}
			return false, err
		}
	}
	conf := NewConfirmations(ConfirmationsConfig{Cards: cards, Sessions: cfg.Sessions, Log: cfg.Log,
		Describe: describeFromLanes(cfg.Tools.ApplyLanes)})
	guard := NewGuard(reg, GuardOptions{Sessions: cfg.Sessions, Now: cfg.Now, Log: cfg.Log, Salt: salt, ApplyList: applyListURLs(cfg.Tools.ApplyLanes), Grants: conf})
	packer := ContextPacker{Sessions: cfg.Sessions, Memory: cfg.Tools.Memory, Goals: cfg.Tools.Goals, Cards: cards, Tasks: cfg.Tools.Tasks, Now: cfg.Now}
	disclosure := NewDisclosureGate(DisclosureOptions{ProtectedRoots: append([]string{cfg.StateDir}, cfg.ProtectedRoots...)})
	bridge := NewBridge(BridgeOptions{Registry: reg, Guard: guard, Sessions: cfg.Sessions, Packer: packer, Confirmations: conf, Disclosure: disclosure, Salt: salt, Log: cfg.Log, Harness: cfg.Harness, Gateway: cfg.Router})
	sup := NewSupervisor(SupervisorConfig{PiPath: cfg.PiPath, AgentDir: filepath.Join(cfg.StateDir, "agent"), BaseURL: cfg.BaseURL,
		Router: cfg.Router, Sessions: cfg.Sessions, Bridge: bridge, Launcher: cfg.Launcher, Log: cfg.Log})
	var compactor *Compactor
	if cfg.Summarizer != nil {
		compactor = &Compactor{Packer: packer, Model: cfg.Summarizer, Log: cfg.Log}
	}
	return &Stack{Agent: New(Config{Sessions: cfg.Sessions, Supervisor: sup, Sink: cfg.Sink, Log: cfg.Log, Bridge: bridge, Compactor: compactor, Confirmations: conf, Tasks: cfg.Tasks}), Bridge: bridge, Confirmations: conf, Supervisor: sup, Registry: reg}, nil
}

// AuditSalt is the per-install salt of the audit, which the apply chain uses
// for the hashes it stores so a run and its gate decisions agree.
func AuditSalt(stateDir string) (string, error) { return auditSalt(stateDir) }

// auditSalt returns the per-install salt the audit hashes user messages with,
// creating it with mode 0600 on first use.
func auditSalt(stateDir string) (string, error) {
	path := filepath.Join(stateDir, "audit-salt")
	if b, err := os.ReadFile(path); err == nil && len(b) >= 16 {
		return string(b), nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	salt := hex.EncodeToString(raw[:])
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", err
	}
	return salt, os.WriteFile(path, []byte(salt), 0o600)
}
