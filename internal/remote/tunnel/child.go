package tunnel

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// Spec is how to start the child.
type Spec struct {
	Path string
	Args []string
	Env  []string
}

// Child is a running tunnel process.
type Child interface {
	PID() int
	// Output is the child's combined standard output and error; it ends when
	// the child exits.
	Output() io.Reader
	// Wait blocks until the child exits.
	Wait() error
	Terminate() error
	Kill() error
}

// Starter starts a child. The production starter runs the executable.
type Starter func(Spec) (Child, error)

// Command starts the configured named tunnel. The token travels only in the
// environment, never in the process arguments.
func Command(token string) (args, env []string) {
	return []string{"tunnel", "--no-autoupdate", "run"}, []string{"TUNNEL_TOKEN=" + token}
}

// childEnvKeep names the variables the child inherits: enough to find its
// certificates and resolve names, and none of this program's secrets.
var childEnvKeep = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "TMPDIR": true, "TEMP": true, "TMP": true, "LANG": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "HTTPS_PROXY": true, "HTTP_PROXY": true, "NO_PROXY": true,
	"SYSTEMROOT": true, "USERPROFILE": true, "APPDATA": true, "LOCALAPPDATA": true,
}

// ChildEnv filters environ down to childEnvKeep and appends extra.
func ChildEnv(environ, extra []string) []string {
	var out []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if childEnvKeep[strings.ToUpper(name)] {
			out = append(out, kv)
		}
	}
	return append(out, extra...)
}

type execChild struct {
	cmd *exec.Cmd
	pr  *io.PipeReader
	pw  *io.PipeWriter
}

// ExecStarter runs the executable of a Spec.
func ExecStarter(spec Spec) (Child, error) {
	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Env = spec.Env
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		return nil, err
	}
	return &execChild{cmd: cmd, pr: pr, pw: pw}, nil
}

func (c *execChild) PID() int          { return c.cmd.Process.Pid }
func (c *execChild) Output() io.Reader { return c.pr }
func (c *execChild) Wait() error {
	err := c.cmd.Wait()
	_ = c.pw.Close()
	return err
}
func (c *execChild) Terminate() error { return c.cmd.Process.Signal(syscall.SIGTERM) }
func (c *execChild) Kill() error      { return c.cmd.Process.Signal(os.Kill) }
