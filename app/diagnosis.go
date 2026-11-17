package main

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"applysmith/internal/api"
	"applysmith/internal/launch"
	"applysmith/internal/store"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

// cause is the category the diagnosis screen names for a startup failure.
type cause string

// The categories of the desktop-shell capability, in the order it lists them.
const (
	causePortProgram   cause = "port-held-by-program"
	causePortWorkspace cause = "port-held-by-other-workspace"
	causeWorkspace     cause = "workspace-not-usable"
	causeMigration     cause = "migration-failure"
	causeKeychain      cause = "keychain-denied"
	causeUnhealthyLock cause = "unhealthy-lock"
	causeUnknown       cause = "unknown"
)

type causeText struct{ Title, Advice string }

var causeTexts = map[cause]causeText{
	causePortProgram: {"Port held by another program",
		"Another program is listening on the port Applysmith needs. Quit it, or set APPLYSMITH_PORT to a free port, then press Retry."},
	causePortWorkspace: {"Port held by a server of another workspace",
		"An Applysmith server for a different workspace holds the port. Quit that app or server and press Retry, or press Change Workspace to open that workspace instead."},
	causeWorkspace: {"Workspace not usable",
		"The workspace folder is missing, cannot be read or written, or is not a valid workspace. Fix the folder and press Retry, or choose another one with Change Workspace."},
	causeMigration: {"Database migration failure",
		"The workspace database could not be upgraded to this version. Your files were not changed. Install the version that last opened this workspace, or restore a backup (see the storage notes), then press Retry."},
	causeKeychain: {"Keychain access denied",
		"Applysmith could not use the operating system's keychain for this workspace's secrets. Allow access when the system asks, or unlock the keychain, then press Retry."},
	causeUnhealthyLock: {"Lock held by an unhealthy server of the same workspace",
		"A process holds this workspace's lock but does not answer as a healthy server. Quit it (it is named below when known) and press Retry."},
	causeUnknown: {"Unknown cause",
		"The start failed for a reason this screen does not recognise. The log below has the details."},
}

// classify names the category of a startup failure. The first rule that
// matches wins: the specific, typed failures come before the broad ones.
func classify(err error) cause {
	var se *api.StartupError
	switch {
	case errors.As(err, &se) && se.Code == api.CodePortInUse, errors.Is(err, syscall.EADDRINUSE):
		return causePortProgram
	case errors.As(err, &se) && se.Code == api.CodeServerAlreadyRunning && se.OtherWorkspace:
		return causePortWorkspace
	case errors.As(err, &se) && se.Code == api.CodeWorkspaceLocked, errors.Is(err, launch.ErrHolderUnhealthy):
		return causeUnhealthyLock
	case vault.IsCode(err, vault.CodeKeychainDenied), vault.IsCode(err, vault.CodeKeychainUnavailable):
		return causeKeychain
	case errors.Is(err, store.ErrSchemaTooNew), errors.Is(err, store.ErrChecksumMismatch), errors.Is(err, store.ErrMigrationPending),
		strings.Contains(err.Error(), "store: migration"):
		return causeMigration
	case errors.Is(err, &workspace.Error{Code: workspace.CodeWorkspaceInvalid}), errors.Is(err, &workspace.Error{Code: workspace.CodeWorkspaceIDInvalid}), errors.Is(err, fs.ErrNotExist), errors.Is(err, fs.ErrPermission),
		errors.Is(err, store.ErrIO), errors.Is(err, store.ErrCorrupt), errors.Is(err, store.ErrDiskFull), errors.Is(err, store.ErrReadOnly),
		errors.Is(err, store.ErrNoWorkspaceID):
		return causeWorkspace
	}
	return causeUnknown
}

// diagnosis is everything the screen shows.
type diagnosis struct {
	Stage       string
	DefaultPath string
	Cause       cause
	Title       string
	Advice      string
	Port        int
	Holder      string
	Err         string
	LogName     string
	Log         []string
	HasLogs     bool
}

// inspection is what the shell can look at to explain a failure; tests replace
// the parts that reach outside the process.
type inspection struct {
	port     int
	stateDir string // empty until a workspace is resolved
	holder   func(port int) string
	probe    func(addr string) (healthSummary, bool)
	lock     func(stateDir string) (api.LockInfo, error)
	logs     func(name string) []string
	ring     []string
}

type healthSummary struct{ Version, BuildID string }

func systemInspection(port int, stateDir string, ring []string) inspection {
	return inspection{port: port, stateDir: stateDir, holder: listenerOf, ring: ring,
		logs: func(name string) []string { return tailFile(filepath.Join(stateDir, "logs", name), maxLogLines) },
		probe: func(addr string) (healthSummary, bool) {
			h, ok := api.ProbeServer(addr)
			return healthSummary{h.Version, h.BuildID}, ok
		},
		lock: api.ReadLockInfo,
	}
}

// maxLogLines is how much of the relevant log the screen shows.
const maxLogLines = 50

// diagnose builds the screen for a startup failure.
func diagnose(err error, in inspection) diagnosis {
	c := classify(err)
	text := causeTexts[c]
	d := diagnosis{Cause: c, Title: text.Title, Advice: text.Advice, Err: err.Error(), Port: in.port, HasLogs: in.stateDir != ""}
	var se *api.StartupError
	if errors.As(err, &se) && se.Port != 0 {
		d.Port = se.Port
	}
	switch c {
	case causePortProgram:
		d.Holder = in.holder(d.Port)
	case causePortWorkspace:
		addr := "127.0.0.1:" + strconv.Itoa(d.Port)
		var lines []string
		if h, ok := in.probe(addr); ok {
			lines = append(lines, fmt.Sprintf("An Applysmith server answers there: version %s, build %s.", h.Version, h.BuildID))
		}
		if l := in.holder(d.Port); l != "" {
			lines = append(lines, l)
		}
		d.Holder = strings.Join(lines, "\n")
	case causeUnhealthyLock:
		if in.stateDir != "" {
			if info, lerr := in.lock(in.stateDir); lerr == nil {
				d.Holder = fmt.Sprintf("The lock document names process %d at %s, started %s.", info.PID, info.Origin, info.StartedAt)
			}
		}
	}
	// The log that explains the failure: a lock holder's own server log first,
	// the shell's own otherwise. This run's lines are the fallback.
	names := []string{"desktop.log", "server.log"}
	if c == causeUnhealthyLock {
		names = []string{"server.log", "desktop.log"}
	}
	for _, n := range names {
		if lines := in.logs(n); len(lines) > 0 {
			d.LogName, d.Log = n, lines
			break
		}
	}
	if len(d.Log) == 0 {
		d.LogName, d.Log = "this run", lastLines(in.ring, maxLogLines)
	}
	return d
}

func lastLines(lines []string, n int) []string {
	if len(lines) > n {
		return lines[len(lines)-n:]
	}
	return lines
}

// listenerOf names the program listening on a TCP port, as far as this
// account may see it. An empty answer means it cannot be seen without more
// privileges.
func listenerOf(port int) string {
	if port <= 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "lsof", "-nP", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN")
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/bin", "LC_ALL=C"}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.Join(lastLines(strings.Split(strings.TrimSpace(string(out)), "\n"), 6), "\n")
}

//go:embed diagnosis.html
var diagnosisSource string

var diagnosisPage = template.Must(template.New("diagnosis").Parse(diagnosisSource))

// render is the diagnosis screen: one static document that needs no server.
func (d diagnosis) render() (string, error) {
	var b bytes.Buffer
	if err := diagnosisPage.Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}
