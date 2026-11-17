// Clean-room: docs/provenance.md#CR-DOCTOR

package onboarding

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/api"
	"applysmith/internal/store"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

func fixedChecks() []Check {
	pass := func(context.Context, *Env) Outcome { return Outcome{Status: Pass, Summary: "fine"} }
	fail := func(context.Context, *Env) Outcome { return Outcome{Status: Fail, Summary: "broken", Fix: "repair it"} }
	return []Check{
		{ID: "alpha.one", Required: true, Run: pass},
		{ID: "alpha.two", Required: false, Run: fail},
		{ID: "beta.one", Required: true, Run: fail},
	}
}

func TestSelectByGroupAndLeaf(t *testing.T) {
	got, err := Select(fixedChecks(), "alpha")
	if err != nil || len(got) != 2 {
		t.Fatalf("group: %v %v", got, err)
	}
	got, err = Select(fixedChecks(), "beta.one")
	if err != nil || len(got) != 1 || got[0].ID != "beta.one" {
		t.Fatalf("leaf: %v %v", got, err)
	}
	if _, err := Select(fixedChecks(), "alph"); !errors.Is(err, ErrUnknownCheck) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestRequiredFailureSetsTheExitCode(t *testing.T) {
	rep, err := Run(context.Background(), fixedChecks(), &Env{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.ExitCode() != ExitFailed {
		t.Errorf("exit code %d", rep.ExitCode())
	}
	if rep.Results[1].Status != Warn {
		t.Errorf("an optional failure is %s, want warn", rep.Results[1].Status)
	}
	only, _ := Run(context.Background(), fixedChecks(), &Env{}, Options{Only: "alpha"})
	if only.ExitCode() != ExitOK {
		t.Errorf("optional failure sets exit code %d", only.ExitCode())
	}
}

// The dashboard shows the fix of a check no step fixes (runtime, agent), so the
// doctor's own lines name a command only at a terminal (flow round 7).
func TestSlowAndCrashingChecksBecomeFailures(t *testing.T) {
	// The slow check outlives its timeout and returns when the test ends, so
	// its goroutine does not outlive the test.
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	checks := []Check{
		{ID: "x.slow", Required: true, Run: func(context.Context, *Env) Outcome { <-release; return Outcome{} }},
		{ID: "x.crash", Required: true, Run: func(context.Context, *Env) Outcome { panic("boom") }},
	}
	for _, tc := range []struct {
		inServer    bool
		slow, crash string
	}{
		{false, "`applysmith doctor --only x.slow`", "`applysmith version`"},
		{true, "run the checks again", "Settings, Advanced"},
	} {
		rep, err := Run(context.Background(), checks, &Env{InServer: tc.inServer}, Options{Timeout: 50 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rep.Results {
			if r.Status != Fail {
				t.Errorf("%s is %s", r.ID, r.Status)
			}
			if tc.inServer && strings.Contains(r.Fix, "applysmith") {
				t.Errorf("%s in the dashboard: fix %q names a command", r.ID, r.Fix)
			}
		}
		if got := rep.Results[0].Fix; !strings.Contains(got, tc.slow) {
			t.Errorf("x.slow (in server %v): fix %q, want %s", tc.inServer, got, tc.slow)
		}
		if got := rep.Results[1].Fix; !strings.Contains(got, tc.crash) {
			t.Errorf("x.crash (in server %v): fix %q, want %s", tc.inServer, got, tc.crash)
		}
		if strings.Contains(rep.Results[1].Summary, "boom") {
			t.Error("the panic value reached the report")
		}
	}
}

// A result names the setup step that fixes it, so the dashboard links to the
// step instead of printing the command-line fix; a runtime check names none.
func TestEveryResultNamesTheSetupStepThatFixesIt(t *testing.T) {
	env, _ := newEnv(t)
	rep, err := Run(context.Background(), Checks(), env, Options{Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Results {
		group := strings.SplitN(r.ID, ".", 2)[0]
		_, isStep := StepByID(group)
		if want := map[bool]string{true: group}[isStep]; r.Step != want {
			t.Errorf("%s: step %q, want %q", r.ID, r.Step, want)
		}
	}
	if r := run(t, env, "answer-bank.blocking"); r.Step != "answer-bank" || !strings.Contains(r.Fix, "applysmith setup apply") {
		t.Errorf("answer-bank: step %q fix %q", r.Step, r.Fix)
	}
	if r := run(t, env, "runtime.keychain"); r.Step != "" {
		t.Errorf("runtime.keychain names step %q", r.Step)
	}
}

func newEnv(t *testing.T) (*Env, *vault.Memory) {
	t.Helper()
	ws := workspace.Open(filepath.Join(t.TempDir(), "ws"))
	mem := vault.NewMemory()
	return &Env{Workspace: ws, Backend: mem, Port: 0}, mem
}

func run(t *testing.T, env *Env, id string) Result {
	t.Helper()
	rep, err := Run(context.Background(), Checks(), env, Options{Only: id})
	if err != nil || len(rep.Results) != 1 {
		t.Fatalf("%s: %v %v", id, rep, err)
	}
	return rep.Results[0]
}

func TestWorkspaceLocationAndFiles(t *testing.T) {
	env, _ := newEnv(t)
	if r := run(t, env, "workspace.location"); r.Status != Pass {
		t.Errorf("location: %+v", r)
	}
	if r := run(t, env, "workspace.files"); r.Status != Fail || !r.Required || r.Fix == "" {
		t.Errorf("files of an empty workspace: %+v", r)
	}
	if _, err := env.Workspace.Init(); err != nil {
		t.Fatal(err)
	}
	if r := run(t, env, "workspace.files"); r.Status != Fail || !strings.Contains(r.Summary, "answer-bank.yml") {
		t.Errorf("init alone leaves the answer bank to its service: %+v", r)
	}
	if _, err := answerbank.New(env.Workspace, nil).Seed(); err != nil {
		t.Fatal(err)
	}
	if r := run(t, env, "workspace.files"); r.Status != Pass {
		t.Errorf("files after init and seed: %+v", r)
	}
	bad := &Env{ResolveErr: errors.New("workspace_invalid: home directory")}
	if r := run(t, bad, "workspace.location"); r.Status != Fail || r.Fix == "" {
		t.Errorf("unresolvable workspace: %+v", r)
	}
	if r := run(t, bad, "workspace.files"); r.Status != Skipped {
		t.Errorf("files without a workspace: %+v", r)
	}
}

func TestMigrationsCheck(t *testing.T) {
	env, _ := newEnv(t)
	if r := run(t, env, "workspace.migrations"); r.Status != Warn {
		t.Errorf("no database: %+v", r)
	}
	path := filepath.Join(env.Workspace.StateDir(), store.DBFile)
	s, err := store.Open(context.Background(), path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if r := run(t, env, "workspace.migrations"); r.Status != Pass {
		t.Errorf("migrated database: %+v", r)
	}
}

func TestServerLockCheck(t *testing.T) {
	env, _ := newEnv(t)
	dir := env.Workspace.StateDir()
	if r := run(t, env, "runtime.server-lock"); r.Status != Warn || r.Fix == "" {
		t.Errorf("absent: %+v", r)
	}
	l, err := api.AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.WriteInfo(api.LockInfo{PID: 4242, Port: 1, Origin: "http://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	// A document that nothing answers for is not reported as a running server.
	if r := run(t, env, "runtime.server-lock"); r.Status != Pass || strings.Contains(r.Summary, "http://127.0.0.1:1") || !strings.Contains(r.Summary, "another Applysmith process") {
		t.Errorf("unverified document: %+v", r)
	}
	l.Release()
	if r := run(t, env, "runtime.server-lock"); r.Status != Warn || !strings.Contains(r.Fix, "reclaims") {
		t.Errorf("stale: %+v", r)
	}
	id, _ := env.Workspace.ID()
	v, _ := vault.New(id, vault.NewMemory(), vault.WithGetenv(func(string) string { return "" }))
	srv, err := api.New(api.Config{Workspace: env.Workspace, Vault: v})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(context.Background())
	if r := run(t, env, "runtime.server-lock"); r.Status != Pass || !strings.Contains(r.Summary, srv.Origin()) {
		t.Errorf("alive: %+v", r)
	}
	if r := run(t, env, "runtime.server-lock"); r.Required {
		t.Error("the server lock check must not be required")
	}
}

func TestPortCheck(t *testing.T) {
	env, _ := newEnv(t)
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	env.Port = probe.Addr().(*net.TCPAddr).Port
	probe.Close()
	if r := run(t, env, "runtime.port"); r.Status != Pass {
		t.Errorf("free port: %+v", r)
	}

	other, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	go func() {
		for {
			c, err := other.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	env.Port = other.Addr().(*net.TCPAddr).Port
	if r := run(t, env, "runtime.port"); r.Status != Fail || !strings.Contains(r.Summary, "another program") {
		t.Errorf("foreign port: %+v", r)
	}
}

func TestPortHeldByThisWorkspacesServerPasses(t *testing.T) {
	env, mem := newEnv(t)
	id, _ := env.Workspace.ID()
	v, _ := vault.New(id, mem, vault.WithGetenv(func(string) string { return "" }))
	srv, err := api.New(api.Config{Workspace: env.Workspace, Vault: v})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(context.Background())
	env.Port = srv.Port()
	if r := run(t, env, "runtime.port"); r.Status != Pass || !strings.Contains(r.Summary, "this workspace") {
		t.Errorf("own server: %+v", r)
	}
	other := &Env{Workspace: workspace.Open(t.TempDir()), Backend: mem, Port: srv.Port()}
	if r := run(t, other, "runtime.port"); r.Status != Fail || !strings.Contains(r.Summary, "another workspace") {
		t.Errorf("another workspace's server: %+v", r)
	}
	// The running server makes the configured port moot, even when another
	// program holds it (the app chose a free port).
	foreign, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	env.Port = foreign.Addr().(*net.TCPAddr).Port
	if r := run(t, env, "runtime.port"); r.Status != Pass || !strings.Contains(r.Summary, "this workspace's server is running") {
		t.Errorf("own server elsewhere, configured port taken: %+v", r)
	}
}

func TestKeychainCheck(t *testing.T) {
	env, mem := newEnv(t)
	if r := run(t, env, "runtime.keychain"); r.Status != Pass {
		t.Errorf("working keychain: %+v", r)
	}
	if got := mem.Accounts(); len(got) != 0 {
		t.Errorf("the probe item was left behind: %v", got)
	}
	mem.FailWith(vault.ErrDenied)
	if r := run(t, env, "runtime.keychain"); r.Status != Fail || !strings.Contains(r.Summary, "keychain_denied") {
		t.Errorf("denied: %+v", r)
	}
	mem.FailWith(vault.ErrUnavailable)
	r := run(t, env, "runtime.keychain")
	if r.Status != Warn || !strings.Contains(r.Summary, "keychain_unavailable") || !strings.Contains(r.Fix, "APPLYSMITH_SECRET_") {
		t.Errorf("unavailable: %+v", r)
	}
}

// A vault supplied through the environment must not cause any keychain call,
// not even a probe item that is removed again.
func TestKeychainCheckLeavesAnEnvManagedVaultAlone(t *testing.T) {
	env, mem := newEnv(t)
	env.Getenv = func(k string) string {
		if k == vault.EnvName(vault.APIToken) {
			return "env-supplied-token-value"
		}
		return ""
	}
	mem.FailWith(vault.ErrDenied) // any keychain call would turn the result into a failure
	r := run(t, env, "runtime.keychain")
	if r.Status != Pass || r.Summary != "env-managed, keychain not probed" {
		t.Errorf("env-managed vault: %+v", r)
	}
	if got := mem.Accounts(); len(got) != 0 {
		t.Errorf("the keychain was touched: %v", got)
	}
}

func TestACheckThatOutlivesItsTimeoutCannotChangeWhatTheNextOneSees(t *testing.T) {
	seen := -1
	nextStarted, lateWrote := make(chan struct{}), make(chan struct{})
	checks := []Check{
		{ID: "x.late", Required: true, Run: func(_ context.Context, env *Env) Outcome {
			<-nextStarted // the checks run at once: it writes while the next one runs
			env.Port = 7
			close(lateWrote)
			return Outcome{Status: Pass}
		}},
		{ID: "x.next", Required: true, Run: func(_ context.Context, env *Env) Outcome {
			close(nextStarted)
			<-lateWrote
			seen = env.Port
			return Outcome{Status: Pass}
		}},
	}
	rep, err := Run(context.Background(), checks, &Env{}, Options{Timeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Results[0].ID != "x.late" || rep.Results[1].ID != "x.next" || rep.Results[1].Status != Pass {
		t.Fatalf("results = %+v", rep.Results)
	}
	if seen != 0 {
		t.Fatalf("the next check saw port %d written by the timed-out one", seen)
	}
}

func TestMCPCheck(t *testing.T) {
	env, _ := newEnv(t)
	env.Backend = vault.NewMemory()
	r := run(t, env, "runtime.mcp")
	if r.Status != Warn || r.Required || r.Fix == "" || !strings.Contains(r.Summary, "MCP client path") {
		t.Errorf("no server: %+v", r)
	}
	id, _ := env.Workspace.ID()
	v, _ := vault.New(id, env.Backend, vault.WithGetenv(func(string) string { return "" }))
	srv, err := api.New(api.Config{Workspace: env.Workspace, Vault: v})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(context.Background())
	if r := run(t, env, "runtime.mcp"); r.Status != Pass {
		t.Errorf("server up: %+v", r)
	}
}
