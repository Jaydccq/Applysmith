package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Spec says how to start the pi process.
type Spec struct {
	Path string
	Args []string
	Env  []string
	Dir  string
}

// Process is a started pi: its stdin and stdout and the means to end it.
type Process interface {
	io.Writer // stdin
	Stdout() io.Reader
	CloseInput()
	Kill()
	// Done is closed once the process has exited and its output is drained.
	Done() <-chan struct{}
}

// Launcher starts processes and asks a binary for its version. Tests replace it.
type Launcher interface {
	Start(spec Spec, stderr io.Writer) (Process, error)
	Version(ctx context.Context, path string, env []string) (string, error)
	// AuthCheck runs `pi auth check --provider <id> --json` and returns nil
	// only when pi reports the login ready.
	AuthCheck(ctx context.Context, path, provider string, env []string) error
}

// versionTimeout bounds `pi --version`; the first run on a machine is slow
// while the operating system checks the binary.
const versionTimeout = 10 * time.Second

type execLauncher struct{}

func (execLauncher) Version(ctx context.Context, path string, env []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("run %s --version: %w", path, err)
	}
	return out.String(), nil
}

func (execLauncher) AuthCheck(ctx context.Context, path, provider string, env []string) error {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "auth", "check", "--provider", provider, "--json")
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pi auth check: %w", err)
	}
	var st struct{ Status string }
	if err := json.Unmarshal(out.Bytes(), &st); err != nil || st.Status != "ready" {
		return fmt.Errorf("pi reports the login is not ready")
	}
	return nil
}

func (execLauncher) Start(spec Spec, stderr io.Writer) (Process, error) {
	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Env, cmd.Dir, cmd.Stderr = spec.Env, spec.Dir, stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	// stdout goes through an in-memory pipe so that Wait, which returns once
	// the output has been copied, never closes a pipe the reader still needs.
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &execProcess{cmd: cmd, in: in, out: pr, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		_ = pw.Close()
		close(p.done)
	}()
	return p, nil
}

type execProcess struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *io.PipeReader
	done chan struct{}
	once sync.Once
}

func (p *execProcess) Write(b []byte) (int, error) { return p.in.Write(b) }
func (p *execProcess) Stdout() io.Reader           { return p.out }
func (p *execProcess) CloseInput()                 { p.once.Do(func() { _ = p.in.Close() }) }
func (p *execProcess) Done() <-chan struct{}       { return p.done }

// Kill ends the process and drops what it had not yet written, so a copy
// blocked on an abandoned reader cannot keep the exit from being seen.
func (p *execProcess) Kill() {
	_ = p.cmd.Process.Kill()
	_ = p.out.Close()
}

// childBase is the part of the parent's environment pi keeps: enough to find
// its own files and certificates, and nothing that carries a credential of the
// product.
var childBase = []string{"PATH", "HOME", "USERPROFILE", "USER", "LOGNAME", "LANG", "LC_ALL", "TMPDIR", "TMP", "TEMP",
	"SystemRoot", "SYSTEMROOT", "ComSpec", "APPDATA", "LOCALAPPDATA", "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY"}

func baseEnv(environ []string) []string {
	var out []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		for _, keep := range childBase {
			if strings.EqualFold(name, keep) {
				out = append(out, kv)
				break
			}
		}
	}
	return out
}

func osEnviron() []string { return os.Environ() }

// installAsset names the pinned release's asset for this machine.
func installAsset(version string) string {
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("pi-%s-%s%s (release %s)", runtime.GOOS, arch, ext, version)
}

// noProxyFor makes sure the loopback bridge is never sent through a proxy the
// parent environment names for the provider traffic.
func noProxyFor(env []string) []string {
	out := make([]string, 0, len(env)+1)
	existing := ""
	for _, kv := range env {
		if name, v, _ := strings.Cut(kv, "="); strings.EqualFold(name, "NO_PROXY") {
			existing = v
			continue
		}
		out = append(out, kv)
	}
	list := "127.0.0.1,localhost,[::1]"
	if existing != "" {
		list += "," + existing
	}
	return append(out, "NO_PROXY="+list)
}

// AgentDir is the directory the pi runtime keeps its files and logins in, below
// the workspace's state directory.
func AgentDir(stateDir string) string { return filepath.Join(stateDir, "agent") }

// LoginCommand returns pi started on the terminal in the product's agent
// directory, where the user signs in with /login. The login is pi's own and
// lands in that directory's auth file; this product never reads it.
func LoginCommand(piPath, stateDir string, environ []string) (*exec.Cmd, error) {
	dir := AgentDir(stateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if piPath == "" {
		p, err := exec.LookPath("pi")
		if err != nil {
			return nil, &UnavailableError{Message: "pi is not installed", Fix: InstallHint()}
		}
		piPath = p
	}
	cmd := exec.Command(piPath)
	cmd.Dir = dir
	cmd.Env = append(baseEnv(environ), "PI_CODING_AGENT_DIR="+dir, "PI_SKIP_VERSION_CHECK=1", "PI_TELEMETRY=0")
	return cmd, nil
}
