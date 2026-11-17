package cli

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// maxInitBytes bounds what package initialisation allocates, which every
// process pays before main: 6.7 MiB now, 62 MiB when every regular expression
// compiled at start.
const maxInitBytes = 10 << 20

// initBytes is the sum of the bytes each package's initialisation allocates in
// a run of bin, as GODEBUG=inittrace=1 reports them.
func initBytes(t *testing.T, bin string) int {
	t.Helper()
	cmd := exec.Command(bin, "version")
	cmd.Env = append(os.Environ(), "GODEBUG=inittrace=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("version with inittrace: %v\n%s", err, stderr.Bytes())
	}
	total, inits := 0, 0
	for _, line := range strings.Split(stderr.String(), "\n") {
		// init <package> @<t> ms, <t> ms clock, <n> bytes, <n> allocs
		f := strings.Fields(line)
		if len(f) != 11 || f[0] != "init" || f[8] != "bytes," {
			continue
		}
		n, err := strconv.Atoi(f[7])
		if err != nil {
			t.Fatalf("inittrace line %q: %v", line, err)
		}
		total += n
		inits++
	}
	if inits < 100 {
		t.Fatalf("read %d package inits from inittrace:\n%s", inits, stderr.Bytes())
	}
	return total
}

// buildBinary compiles the command once per test run.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "applysmith")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "../../cmd/applysmith")
	// The compiler keeps its caches under the real home.
	cmd.Env = append(realEnviron, "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

type process struct {
	cmd    *exec.Cmd
	stderr *bytes.Buffer
	lines  chan string
}

func startProcess(t *testing.T, bin string, extraEnv []string, args ...string) *process {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	p := &process{cmd: cmd, stderr: &bytes.Buffer{}, lines: make(chan string, 16)}
	cmd.Stderr = p.stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			p.lines <- sc.Text()
		}
		close(p.lines)
	}()
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return p
}

func (p *process) wait(t *testing.T) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("process did not exit")
	}
	return p.cmd.ProcessState.ExitCode()
}

// The built binary, as a user runs it: serve answers health, a second serve
// on the same workspace exits with a lock error, and SIGTERM stops the first
// cleanly so the workspace can be served again.
func TestBuiltBinaryServeHealthLockAndShutdown(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("builds and signals the real binary")
	}
	bin := buildBinary(t)
	ws := t.TempDir()
	env := []string{"APPLYSMITH_SECRET_API_TOKEN=" + strings.Repeat("t", 43), "APPLYSMITH_PORT="}
	args := []string{"serve", "--workspace", ws, "--port", "0"}

	if out, err := exec.Command(bin, "version").Output(); err != nil || !strings.HasPrefix(string(out), "applysmith ") {
		t.Fatalf("version: %q %v", out, err)
	}
	if b := initBytes(t, bin); b > maxInitBytes {
		t.Fatalf("package initialisation allocates %d bytes, over the %d budget (docs/architecture/performance.md, \"Process start\")", b, maxInitBytes)
	}

	first := startProcess(t, bin, env, args...)
	var origin string
	select {
	case line := <-first.lines:
		_, after, ok := strings.Cut(line, "listening on ")
		if !ok {
			t.Fatalf("unexpected first line %q (stderr %s)", line, first.stderr)
		}
		origin = after
	case <-time.After(30 * time.Second):
		t.Fatalf("serve printed nothing (stderr %s)", first.stderr)
	}
	var body []byte
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		resp, err := http.Get(origin + "/api/health")
		if err != nil {
			continue
		}
		body, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(body), `"status":"ok"`) {
			break
		}
	}
	if !strings.Contains(string(body), `"status":"ok"`) || !strings.Contains(string(body), `"buildId"`) {
		t.Fatalf("health: %s", body)
	}

	second := exec.Command(bin, args...)
	second.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	second.Stderr = &stderr
	err := second.Run()
	var exit *exec.ExitError
	if !asExit(err, &exit) || exit.ExitCode() == 0 || !strings.Contains(stderr.String(), "server_already_running") || !strings.Contains(stderr.String(), origin) {
		t.Fatalf("second serve: %v\n%s", err, stderr.String())
	}

	if err := first.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := first.wait(t); code != 0 {
		t.Fatalf("exit code after SIGTERM: %d\n%s", code, first.stderr)
	}

	third := startProcess(t, bin, env, args...)
	select {
	case line := <-third.lines:
		if !strings.Contains(line, "listening on") {
			t.Fatalf("restart after SIGTERM: %q\n%s", line, third.stderr)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("restart printed nothing: %s", third.stderr)
	}
}

func asExit(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}
