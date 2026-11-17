package onboarding

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"applysmith/internal/api"
	"applysmith/internal/mcpserver"
	"applysmith/internal/store"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

const keychainProbeBudget = 5 * time.Second

// Checks is the registry of leaves in report order: the catalog's steps in
// their order, then the groups that belong to no step.
func Checks() []Check {
	all := slices.Concat(coreChecks(), stepChecks(), telegramChecks(), discoveryChecks())
	rank := func(c Check) int {
		if st, ok := StepByID(c.Group()); ok {
			return st.N
		}
		return len(catalog) + slices.Index([]string{"agent", "runtime"}, c.Group())
	}
	slices.SortStableFunc(all, func(a, b Check) int { return rank(a) - rank(b) })
	return all
}

func coreChecks() []Check {
	return []Check{
		{ID: "prerequisites.platform", Required: true, Run: checkPlatform},
		{ID: "prerequisites.chrome", Required: true, Run: checkChrome},
		{ID: "prerequisites.disk", Required: true, Run: checkDisk},
		{ID: "workspace.location", Required: true, Run: checkLocation},
		{ID: "workspace.files", Required: true, Run: checkFiles},
		{ID: "workspace.migrations", Required: true, Run: checkMigrations},
		stepLeaf("cv.content", "cv", "run `applysmith setup apply cv --file <path|->`"),
		stepLeaf("identity.fields", "identity", "run `applysmith setup apply identity --file <path|->`"),
		stepLeaf("work-authorization.answers", "work-authorization", "run `applysmith setup apply work-authorization --file <path|->`"),
		stepLeaf("targets.fields", "targets", "run `applysmith setup apply targets --file <path|->`"),
		{ID: "resumes.toolchain", Run: checkDocumentToolchain},
		{ID: "llm.provider", Required: true, Run: checkLLMProvider},
		{ID: "llm.test-age", Required: true, Run: checkLLMTestAge},
		{ID: "agent.pi", Run: checkPi},
		{ID: "runtime.server-lock", Run: checkServerLock},
		{ID: "runtime.mcp", Run: checkMCP},
		{ID: "runtime.port", Required: true, Run: checkPort},
		{ID: "runtime.keychain", Required: true, Run: checkKeychain},
	}
}

// macOSVersion runs sw_vers once per process: a run costs 60 to 130 ms, the
// setup state asks on every Home load (it held Home's reveal), and macOS
// changes version only across a restart.
var macOSVersion = cachedVersion(func() ([]byte, error) {
	return exec.Command("/usr/bin/sw_vers", "-productVersion").Output()
})

// cachedVersion runs read on the first call only and returns its result ever
// after.
func cachedVersion(read func() ([]byte, error)) func() ([]byte, error) {
	return sync.OnceValues(read)
}

func checkPlatform(_ context.Context, _ *Env) Outcome {
	if runtime.GOOS != "darwin" {
		return Outcome{Status: Warn, Summary: runtime.GOOS + "/" + runtime.GOARCH + " is unverified",
			Fix: "use macOS 13 or later for the supported setup"}
	}
	out, err := macOSVersion()
	if err != nil {
		return Outcome{Status: Warn, Summary: "macOS, version not readable"}
	}
	version := strings.TrimSpace(string(out))
	major, _, _ := strings.Cut(version, ".")
	if n, err := strconv.Atoi(major); err == nil && n < 13 {
		return Outcome{Status: Fail, Summary: "macOS " + version + " is older than 13", Fix: "update macOS to 13 or later"}
	}
	return Outcome{Status: Pass, Summary: "macOS " + version}
}

func checkLocation(_ context.Context, env *Env) Outcome {
	if env.ResolveErr != nil {
		return Outcome{Status: Fail, Summary: env.ResolveErr.Error(),
			Fix: "choose a writable directory with --workspace or APPLYSMITH_WORKSPACE"}
	}
	return Outcome{Status: Pass, Summary: env.Workspace.Root}
}

func checkFiles(_ context.Context, env *Env) Outcome {
	if env.Workspace == nil {
		return Outcome{Status: Skipped, Summary: "no workspace"}
	}
	st, err := env.Workspace.Status()
	if err != nil {
		return Outcome{Status: Fail, Summary: err.Error(), Fix: "check the permissions of " + env.Workspace.Root}
	}
	var bad []string
	for _, f := range st.Files {
		if f.Status == workspace.StatusInvalid || (f.Status == workspace.StatusMissing && !f.Optional) {
			bad = append(bad, f.Name+" ("+f.Status+")")
		}
	}
	if len(bad) > 0 {
		return Outcome{Status: Fail, Summary: strings.Join(bad, ", "), Fix: "run `applysmith init` to create missing files; edit the invalid ones"}
	}
	return Outcome{Status: Pass, Summary: fmt.Sprintf("%d workspace files readable", len(st.Files))}
}

// migrations reads the database's migration record through the open store
// when there is one.
func migrations(ctx context.Context, env *Env) (store.MigrationState, error) {
	if env.Store != nil {
		return env.Store.Migrations(ctx)
	}
	return store.InspectMigrations(ctx, filepath.Join(env.Workspace.StateDir(), store.DBFile))
}

func checkMigrations(ctx context.Context, env *Env) Outcome {
	if env.Workspace == nil {
		return Outcome{Status: Skipped, Summary: "no workspace"}
	}
	st, err := migrations(ctx, env)
	switch {
	case errors.Is(err, store.ErrSchemaTooNew):
		return Outcome{Status: Fail, Summary: "the database is newer than this binary", Fix: "install the newer applysmith, or restore a backup made by this version"}
	case err != nil:
		return Outcome{Status: Fail, Summary: err.Error(), Fix: "run `applysmith backup verify` on a recent backup and restore it if the database is damaged"}
	case !st.Exists || st.Pending():
		return Outcome{Status: Warn, Summary: fmt.Sprintf("schema %d of %d applied", st.Applied, st.Supported), Fix: "start `applysmith serve`; it applies pending migrations"}
	}
	return Outcome{Status: Pass, Summary: fmt.Sprintf("schema %d, every migration applied", st.Applied)}
}

func checkServerLock(_ context.Context, env *Env) Outcome {
	const start = "start the app or `applysmith serve`"
	if env.Workspace == nil {
		return Outcome{Status: Skipped, Summary: "no workspace"}
	}
	dir := env.Workspace.StateDir()
	held, err := api.LockHeld(dir)
	if err != nil {
		return Outcome{Status: Warn, Summary: err.Error(), Fix: start}
	}
	info, infoErr := api.ReadLockInfo(dir)
	switch {
	case held && infoErr == nil && api.LoopbackOrigin(info.Origin) && serverAnswers(info):
		return Outcome{Status: Pass, Summary: fmt.Sprintf("server running at %s (pid %d)", info.Origin, info.PID)}
	case held:
		return Outcome{Status: Pass, Summary: "another Applysmith process holds the workspace"}
	case infoErr == nil:
		return Outcome{Status: Warn, Summary: "the lock file names a server that is no longer running", Fix: start + "; the next start reclaims the lock"}
	}
	return Outcome{Status: Warn, Summary: "no server has run for this workspace", Fix: start}
}

// serverAnswers is true when the server a lock document names is up and says it
// is the workspace the document claims; a document left by an earlier server
// proves nothing about the process that holds the lock now.
func serverAnswers(info api.LockInfo) bool {
	u, err := url.Parse(info.Origin)
	if err != nil {
		return false
	}
	h, ok := api.ProbeServer(u.Host)
	return ok && h.PathKey == info.PathKey
}

// checkMCP runs the same stages as `applysmith mcp --check`, so a client that
// cannot reach the server is found here as well.
func checkMCP(ctx context.Context, env *Env) Outcome {
	if env.Workspace == nil {
		return Outcome{Status: Skipped, Summary: "no workspace"}
	}
	loc := mcpserver.Locator{Workspace: env.Workspace, Backend: env.Backend, Getenv: env.getenv}
	steps := mcpserver.RunCheck(ctx, loc, nil)
	for _, s := range steps {
		if !s.Pass {
			return Outcome{Status: Warn, Summary: "MCP client path: " + s.Name + ": " + s.Detail, Fix: s.Remedy}
		}
	}
	return Outcome{Status: Pass, Summary: "an MCP client can reach the running server"}
}

func checkPort(_ context.Context, env *Env) Outcome {
	// The app picks any free port, so with this workspace's server already up
	// the configured port is not needed and another program on it is no fault.
	if env.Workspace != nil {
		dir := env.Workspace.StateDir()
		if held, err := api.LockHeld(dir); err == nil && held {
			if info, err := api.ReadLockInfo(dir); err == nil && api.LoopbackOrigin(info.Origin) && serverAnswers(info) {
				return Outcome{Status: Pass, Summary: fmt.Sprintf("this workspace's server is running at %s", info.Origin)}
			}
		}
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(env.Port))
	if ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(env.Port)); err == nil {
		_ = ln.Close()
		return Outcome{Status: Pass, Summary: fmt.Sprintf("port %d is free", env.Port)}
	}
	if h, ok := api.ProbeServer(addr); ok {
		if env.Workspace != nil && h.PathKey == workspace.PathKey(env.Workspace.Root) {
			return Outcome{Status: Pass, Summary: fmt.Sprintf("port %d is held by this workspace's server", env.Port)}
		}
		return Outcome{Status: Fail, Summary: fmt.Sprintf("port %d is held by an Applysmith server of another workspace", env.Port),
			Fix: "stop that server or choose another port with --port"}
	}
	return Outcome{Status: Fail, Summary: fmt.Sprintf("port %d is held by another program", env.Port),
		Fix: "stop that program or choose another port with --port"}
}

// checkKeychain writes, reads and deletes one probe item, unless the
// environment supplies the vault: then the keychain is not used and is left
// untouched.
func checkKeychain(ctx context.Context, env *Env) Outcome {
	if vault.EnvManaged(env.Getenv) {
		return Outcome{Status: Pass, Summary: "env-managed, keychain not probed"}
	}
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	account := "doctor-probe-" + hex.EncodeToString(raw[:])
	ctx, cancel := context.WithTimeout(ctx, keychainProbeBudget)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		if err := env.Backend.Set(account, "probe"); err != nil {
			done <- err
			return
		}
		v, err := env.Backend.Get(account)
		derr := env.Backend.Delete(account)
		switch {
		case err != nil:
			done <- err
		case v != "probe":
			done <- vault.ErrUnavailable
		default:
			done <- derr
		}
	}()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		err = ctx.Err()
	}
	switch {
	case err == nil:
		return Outcome{Status: Pass, Summary: "the keychain accepted, returned and removed a probe item"}
	case errors.Is(err, vault.ErrDenied):
		return Outcome{Status: Fail, Summary: "keychain_denied: the keychain refused access",
			Fix: "unlock the keychain or allow access when asked; see the setup guide's note on rebuilt unsigned binaries"}
	case errors.Is(err, vault.ErrUnavailable):
		return Outcome{Status: Warn, Summary: "keychain_unavailable: no keychain on this platform",
			Fix: "supply secrets through APPLYSMITH_SECRET_<NAME> environment variables and restart the server"}
	}
	return Outcome{Status: Fail, Summary: "the keychain probe did not complete", Fix: rerunFix(env, "runtime.keychain")}
}
