package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"

	"applysmith/internal/api"
	"applysmith/internal/launch"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

func launchOpts(ws *workspace.Workspace, backend vault.Backend) launch.Options {
	return launch.Options{Workspace: ws, Backend: backend, Getenv: func(string) string { return "" }, Version: "test", BuildID: "test"}
}

func newWorkspace(t *testing.T) *workspace.Workspace {
	t.Helper()
	return workspace.Open(filepath.Join(t.TempDir(), "ws"))
}

// holdPort listens on a loopback port and closes every connection, as a
// program that is not Applysmith does.
func holdPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// quiet is an inspection that reaches nothing outside the process.
func quiet() inspection {
	return inspection{stateDir: "/state", holder: func(int) string { return "" },
		probe: func(string) (healthSummary, bool) { return healthSummary{}, false },
		lock:  func(string) (api.LockInfo, error) { return api.LockInfo{}, errors.New("no lock") },
		logs:  func(string) []string { return nil }}
}

// Each cause is produced by the failure it names, not by a hand-built error:
// the screen is only as good as the match between the two.
func TestStartupFailuresAreClassifiedByTheirCause(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		fail func(t *testing.T) error
		want cause
	}{
		"port held by another program": {func(t *testing.T) error {
			o := launchOpts(newWorkspace(t), vault.NewMemory())
			o.Port = holdPort(t)
			_, err := launch.AttachOrStart(ctx, o, time.Second)
			return err
		}, causePortProgram},
		"port held by a server of another workspace": {func(t *testing.T) error {
			holder, err := launch.Start(ctx, launchOpts(newWorkspace(t), vault.NewMemory()))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { holder.Shutdown(ctx) })
			o := launchOpts(newWorkspace(t), vault.NewMemory())
			o.Port = holder.Port()
			_, err = launch.AttachOrStart(ctx, o, time.Second)
			return err
		}, causePortWorkspace},
		"workspace not usable": {func(t *testing.T) error {
			_, err := workspace.Resolve(workspace.ResolveOptions{Flag: "relative/folder"})
			return err
		}, causeWorkspace},
		"database migration failure": {func(t *testing.T) error {
			ws := newWorkspace(t)
			srv, err := launch.Start(ctx, launchOpts(ws, vault.NewMemory()))
			if err != nil {
				t.Fatal(err)
			}
			if err := srv.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", filepath.Join(ws.StateDir(), "applysmith.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			// A schema from a newer build.
			if _, err := db.Exec(`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (9999, 'later', 'x', '2026-01-01T00:00:00Z')`); err != nil {
				t.Fatal(err)
			}
			_, err = launch.AttachOrStart(ctx, launchOpts(ws, vault.NewMemory()), time.Second)
			return err
		}, causeMigration},
		"keychain access denied": {func(t *testing.T) error {
			backend := vault.NewMemory()
			backend.FailWith(vault.ErrDenied)
			_, err := launch.AttachOrStart(ctx, launchOpts(newWorkspace(t), backend), time.Second)
			return err
		}, causeKeychain},
		"lock held by an unhealthy server of the same workspace": {func(t *testing.T) error {
			ws := newWorkspace(t)
			lock, err := api.AcquireLock(ws.StateDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { lock.Release() })
			// The document names a loopback port nothing answers on.
			free, _ := net.Listen("tcp", "127.0.0.1:0")
			port := free.Addr().(*net.TCPAddr).Port
			free.Close()
			info := api.LockInfo{PID: os.Getpid(), Port: port, PathKey: workspace.PathKey(ws.Root), Origin: fmt.Sprintf("http://127.0.0.1:%d", port)}
			if err := lock.WriteInfo(info); err != nil {
				t.Fatal(err)
			}
			_, err = launch.AttachOrStart(ctx, launchOpts(ws, vault.NewMemory()), 300*time.Millisecond)
			return err
		}, causeUnhealthyLock},
		"unknown": {func(*testing.T) error { return errors.New("something nobody foresaw") }, causeUnknown},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := tc.fail(t)
			if err == nil {
				t.Fatal("the start did not fail")
			}
			if got := classify(err); got != tc.want {
				t.Fatalf("classified %q, want %q (error: %v)", got, tc.want, err)
			}
			d := diagnose(err, quiet())
			if d.Title != causeTexts[tc.want].Title || d.Err != err.Error() {
				t.Errorf("diagnosis %+v", d)
			}
		})
	}
}

// "Log tail": the screen shows the last 50 lines of an 80-line log, read
// the way the app reads it.
func TestDiagnosisShowsTheLastFiftyLinesOfTheLog(t *testing.T) {
	var text strings.Builder
	for i := 1; i <= 80; i++ {
		fmt.Fprintf(&text, "log line %d\n", i)
	}
	stateDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stateDir, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "logs", "desktop.log"), []byte(text.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	in := quiet()
	in.logs = systemInspection(0, stateDir, nil).logs
	d := diagnose(errors.New("boom"), in)
	page, _ := d.render()
	if len(d.Log) != 50 || d.LogName != "desktop.log" || !strings.Contains(page, "log line 80") || !strings.Contains(page, "log line 31") || strings.Contains(page, "log line 30\n") {
		t.Fatalf("log shown: %d lines from %s", len(d.Log), d.LogName)
	}
}

// The text of an error is data on the screen, never markup.
func TestDiagnosisEscapesWhatItShows(t *testing.T) {
	d := diagnose(errors.New(`<script>alert(1)</script>`), quiet())
	d.Log = []string{`<img src=x onerror=alert(2)>`}
	page, _ := d.render()
	if strings.Contains(page, "<script>alert(1)") || strings.Contains(page, "<img src=x") {
		t.Fatal("markup from an error or a log reached the page")
	}
}

func TestDiagnosisScreenOffersFourButtonsAndLoadsNothingFromOutside(t *testing.T) {
	page, err := diagnose(errors.New("boom"), quiet()).render()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{"Retry", "OpenLogs", "ChangeWorkspace", "Quit"} {
		if !strings.Contains(page, `data-action="`+a+`"`) {
			t.Errorf("no %s button", a)
		}
	}
	for _, bad := range []string{"http://", "https://", "src="} {
		if strings.Contains(page, bad) {
			t.Errorf("the page refers to something outside itself: %q", bad)
		}
	}
	if !strings.Contains(page, "default-src 'none'") {
		t.Error("no content security policy")
	}
}

// The calls of the startup screens are explicit actions; they are a
// separate service from the dashboard's bindings.
func TestStartupBindingsAreTheScreenActions(t *testing.T) {
	typ := reflect.TypeOf(&Diagnosis{})
	var got []string
	for i := range typ.NumMethod() {
		got = append(got, typ.Method(i).Name)
	}
	slices.Sort(got)
	if want := []string{"ChangeWorkspace", "OpenLogs", "Quit", "Retry", "UseDefault"}; !slices.Equal(got, want) {
		t.Errorf("bound methods %v, want %v", got, want)
	}
}

// A page that is not the diagnosis window cannot use them: here a call that
// carries no window at all, as one from a non-window context does.
func TestDiagnosisCallsFromAnotherPageAreRefused(t *testing.T) {
	var starts atomic.Int32
	a := &shell{desk: (&fakeDesk{}).desktop(), attemptFn: func() { starts.Add(1) }}
	d := &Diagnosis{a}
	ctx := context.Background()
	if res, err := d.Retry(ctx); !errors.Is(err, errDiagnosisOnly) || res != "" {
		t.Errorf("Retry: %q, %v", res, err)
	}
	if err := d.OpenLogs(ctx); !errors.Is(err, errDiagnosisOnly) {
		t.Errorf("OpenLogs: %v", err)
	}
	if _, err := d.ChangeWorkspace(ctx); !errors.Is(err, errDiagnosisOnly) {
		t.Errorf("ChangeWorkspace: %v", err)
	}
	if _, err := d.UseDefault(ctx); !errors.Is(err, errDiagnosisOnly) {
		t.Errorf("UseDefault: %v", err)
	}
	if err := d.Quit(ctx); !errors.Is(err, errDiagnosisOnly) {
		t.Errorf("Quit: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if starts.Load() != 0 {
		t.Error("a refused call started the app")
	}
}

// "Retry SHALL repeat the startup sequence once per click; the screen SHALL
// NOT retry by itself."
func TestRetryRunsTheStartupOncePerClickAndNeverAlone(t *testing.T) {
	var starts atomic.Int32
	release := make(chan struct{})
	done := make(chan struct{}, 4)
	a := &shell{desk: (&fakeDesk{}).desktop()}
	a.attemptFn = func() {
		starts.Add(1)
		<-release
		done <- struct{}{}
	}
	if got := a.retry(); got != resultStarted {
		t.Fatalf("first click: %s", got)
	}
	if got := a.retry(); got != resultBusy {
		t.Fatalf("a click while a start runs: %s", got)
	}
	release <- struct{}{}
	<-done
	// The attempt ended without succeeding; nothing starts another.
	time.Sleep(50 * time.Millisecond)
	if starts.Load() != 1 {
		t.Fatalf("%d starts after one click", starts.Load())
	}
	for a.starting.Load() {
		time.Sleep(time.Millisecond)
	}
	go func() { release <- struct{}{} }()
	if got := a.retry(); got != resultStarted {
		t.Fatalf("second click: %s", got)
	}
	<-done
	if starts.Load() != 2 {
		t.Fatalf("%d starts after two clicks", starts.Load())
	}
}

// "Retry after a fix": the same sequence that failed on a held port loads the
// dashboard once the port is free.
func TestStartupSucceedsOnceThePortIsFree(t *testing.T) {
	ctx := context.Background()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	o := launchOpts(newWorkspace(t), vault.NewMemory())
	o.Port = port
	if _, err := launch.AttachOrStart(ctx, o, time.Second); classify(err) != causePortProgram {
		t.Fatalf("held port: %v", err)
	}
	ln.Close()
	sess, err := launch.AttachOrStart(ctx, o, time.Second)
	if err != nil {
		t.Fatalf("after freeing the port: %v", err)
	}
	t.Cleanup(func() { sess.Server.Shutdown(ctx) })
	if !strings.Contains(sess.Origin, fmt.Sprint(port)) {
		t.Errorf("origin %s", sess.Origin)
	}
}

// "Change workspace": the folder picker opens and the choice is validated by
// the workspace rules.
func TestChangeWorkspaceValidatesTheChoiceThenStartsWithIt(t *testing.T) {
	var starts atomic.Int32
	f := &fakeDesk{}
	a := &shell{desk: f.desktop(), configDir: t.TempDir(), attemptFn: func() { starts.Add(1) }}
	desk := f.desktop()
	pick := func(p string) {
		desk.choose = func(o mygo.OpenDialogOptions) (string, error) {
			if !o.Directory {
				t.Error("the picker must choose a folder")
			}
			return p, nil
		}
		a.desk = desk
	}

	pick("")
	if got, err := a.changeWorkspace(nil); got != resultCancelled || err != nil || starts.Load() != 0 {
		t.Fatalf("cancelled: %q, %v, %d starts", got, err, starts.Load())
	}

	pick("relative/folder")
	if got, err := a.changeWorkspace(nil); err == nil || got != "" || starts.Load() != 0 || a.chosen != "" {
		t.Fatalf("refused folder: %q, %v, %d starts, chosen %q", got, err, starts.Load(), a.chosen)
	}

	good := filepath.Join(t.TempDir(), "other-workspace")
	pick(good)
	got, err := a.changeWorkspace(nil)
	if err != nil || got != resultStarted || a.chosen != good {
		t.Fatalf("accepted folder: %q, %v, chosen %q", got, err, a.chosen)
	}
	for starts.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
}

// A picked workspace beats the environment, and a start that works records it
// for the next launch.
func TestChosenWorkspaceBeatsTheEnvironmentAndIsRecordedOnSuccess(t *testing.T) {
	envWS, chosen, config := t.TempDir(), filepath.Join(t.TempDir(), "chosen"), t.TempDir()
	t.Setenv(workspace.EnvWorkspace, envWS)
	a := &shell{desk: (&fakeDesk{}).desktop(), configDir: config, chosen: chosen}
	ws, err := a.openWorkspace()
	if err != nil || ws.Root != chosen {
		t.Fatalf("opened %v, %v; want %s", ws, err, chosen)
	}
	if _, err := os.Stat(filepath.Join(chosen, ".applysmith", "logs", "desktop.log")); err != nil {
		t.Errorf("no shell log in the chosen workspace: %v", err)
	}
	a.recordChosen(ws.Root)
	t.Setenv(workspace.EnvWorkspace, "")
	got, err := workspace.Resolve(workspace.ResolveOptions{ConfigDir: config})
	if err != nil || got != chosen {
		t.Fatalf("the next launch resolves %q, %v; want %s", got, err, chosen)
	}
}

// A recorded workspace whose folder is gone is a workspace the user must find
// (a moved folder, a drive not connected), never one replaced by an empty one.
func TestARecordedWorkspaceThatIsGoneIsNotRecreated(t *testing.T) {
	gone, config := filepath.Join(t.TempDir(), "moved"), t.TempDir()
	t.Setenv(workspace.EnvWorkspace, "")
	if err := workspace.RecordChoice(config, gone); err != nil {
		t.Fatal(err)
	}
	a := &shell{configDir: config}
	if _, err := a.openWorkspace(); classify(err) != causeWorkspace {
		t.Fatalf("opened a missing workspace: %v", err)
	}
	if _, err := os.Stat(gone); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("an empty workspace took the missing one's place")
	}
	a.chosen = gone
	if ws, err := a.openWorkspace(); err != nil || ws.Root != gone {
		t.Fatalf("a folder picked on the screen is still made: %v", err)
	}
}

func TestOpenLogsOpensTheLogFolderWhenThereIsOne(t *testing.T) {
	a, f := &shell{}, &fakeDesk{}
	a.desk = f.desktop()
	if err := a.openLogs(); !errors.Is(err, errNoLogs) {
		t.Fatalf("before a workspace: %v", err)
	}
	a.workspaceRoot = t.TempDir()
	if err := a.openLogs(); !errors.Is(err, errNoLogs) {
		t.Fatalf("no logs folder yet: %v", err)
	}
	if err := os.MkdirAll(a.logDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := a.openLogs(); err != nil || !slices.Equal(f.opened, []string{a.logDir()}) {
		t.Fatalf("%v, opened %v", err, f.opened)
	}
}

func TestStartupPagesOfferAChoiceBeforeInitializationAndThenASplash(t *testing.T) {
	root := filepath.Join(t.TempDir(), "new-workspace")
	page, err := (diagnosis{Stage: "welcome", DefaultPath: root}).render()
	if err != nil || !strings.Contains(page, "Use Default Workspace") || !strings.Contains(page, "Choose Folder") || strings.Contains(page, "Applysmith could not start") {
		t.Fatalf("welcome page: %v", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rendering the first-run screen initialized a workspace")
	}
	splash, err := (diagnosis{Stage: "starting"}).render()
	if err != nil || !strings.Contains(splash, "Starting Applysmith") || strings.Contains(splash, `data-action="Retry"`) {
		t.Fatal("startup splash is not distinct from an error")
	}
	done := make(chan string, 1)
	a := &shell{welcomeRoot: root}
	a.attemptFn = func() { done <- a.chosen }
	if got, err := a.useDefault(); err != nil || got != resultStarted {
		t.Fatalf("accept default: %s %v", got, err)
	}
	select {
	case got := <-done:
		if got != root {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("default selection did not start")
	}
	if _, err := (&shell{}).useDefault(); !errors.Is(err, errDiagnosisOnly) {
		t.Fatal("a non-welcome screen accepted UseDefault")
	}
}

func TestStartupWritesPrivateLogsBeforeTheWorkspaceIsKnown(t *testing.T) {
	dir := t.TempDir()
	done := make(chan struct{})
	a := &shell{desk: desktop{startupLogDir: func() (string, error) { return dir, nil }}}
	a.attemptFn = func() { a.logger().Info("early startup evidence"); close(done) }
	a.begin()
	<-done
	raw, err := os.ReadFile(filepath.Join(dir, "desktop.log"))
	if err != nil || !strings.Contains(string(raw), "early startup evidence") || a.logDir() != dir {
		t.Fatalf("early logs: %v", err)
	}
	a.stop()
}
