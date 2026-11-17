package kb

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Bounds of the clone fallback.
const (
	cloneTimeout   = 120 * time.Second
	cloneKillAfter = 3 * time.Second
	cloneMaxOutput = 32 << 20
	cloneMaxDisk   = 256 << 20
)

// cloner lists a commit's tree from a blobless shallow clone. url and protocols
// are replaced by tests; the product values are an https GitHub address and
// the https protocol only.
type cloner struct {
	url       func(owner, repo string) string
	protocols string
	token     string
}

func newCloner(token string) *cloner {
	return &cloner{url: func(owner, repo string) string { return "https://github.com/" + owner + "/" + repo + ".git" }, protocols: "https", token: token}
}

// gitEnv is the environment of every git run: no prompt, no user or system
// configuration, no protocol but the allowed one. The token travels in the
// environment, never on the command line, as the Basic credential
// x-access-token:<token>, the form GitHub's git endpoint accepts for every
// token kind.
func (c *cloner) gitEnv(home string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_ALLOW_PROTOCOL=" + c.protocols, "LC_ALL=C",
	}
	if runtime.GOOS == "windows" {
		env = append(env, "SystemRoot="+os.Getenv("SystemRoot"))
	}
	if c.token != "" {
		env = append(env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Basic "+basicCredential(c.token))
	}
	return env
}

// listTree fetches the commit without file contents and returns its entries.
// The temporary directory is removed whatever happens.
func (c *cloner) listTree(ctx context.Context, owner, repo, sha string) ([]repoEntry, error) {
	if !shaRE.MatchString(sha) {
		return nil, fmt.Errorf("%w: bad commit SHA", ErrInvalid)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("%w: the tree listing was truncated and git is not installed to list it from a clone", ErrUnavailable)
	}
	dir, err := os.MkdirTemp("", "applysmith-kb-clone-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(ctx, cloneTimeout)
	defer cancel()
	stopWatch := watchDisk(dir, cancel)
	defer stopWatch()

	run := func(args ...string) ([]byte, error) { return c.run(ctx, gitPath, dir, args...) }
	if _, err := run("init", "--quiet", "--", dir); err != nil {
		return nil, err
	}
	if _, err := run("-c", "core.hooksPath="+os.DevNull, "-c", "fetch.recurseSubmodules=false", "fetch", "--quiet", "--depth", "1",
		"--filter=blob:none", "--no-tags", "--no-recurse-submodules", c.url(owner, repo), sha); err != nil {
		return nil, err
	}
	out, err := run("-c", "core.hooksPath="+os.DevNull, "ls-tree", "-r", "-z", "FETCH_HEAD")
	if err != nil {
		return nil, err
	}
	return parseLsTree(out)
}

func (c *cloner) run(ctx context.Context, gitPath, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, gitPath, args...)
	cmd.Dir = dir
	cmd.Env = c.gitEnv(dir)
	cmd.SysProcAttr = groupAttr()
	cmd.Cancel = func() error {
		time.AfterFunc(cloneKillAfter, func() { signalGroup(cmd, true) })
		return signalGroup(cmd, false)
	}
	cmd.WaitDelay = cloneKillAfter
	var out, errBuf limitedBuffer
	out.max, errBuf.max = cloneMaxOutput, 4096
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	if err := cmd.Run(); err != nil {
		signalGroup(cmd, true)
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: git %s was stopped: %v", ErrUnavailable, args[0], ctx.Err())
		}
		return nil, fmt.Errorf("%w: git %s failed: %s", ErrUnavailable, args[0], scrubText(strings.TrimSpace(errBuf.String()), c.token))
	}
	if out.over {
		return nil, fmt.Errorf("%w: git %s printed more than %d bytes", ErrUnavailable, args[0], cloneMaxOutput)
	}
	return out.Bytes(), nil
}

func basicCredential(token string) string {
	return base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
}

// scrubText hides the token and its Basic credential.
func scrubText(s, token string) string {
	if token != "" {
		s = strings.ReplaceAll(s, basicCredential(token), "***")
		s = strings.ReplaceAll(s, token, "***")
	}
	return s
}

// limitedBuffer keeps at most max bytes and records that it overflowed. The
// buffer is a named field so the type has no ReadFrom: io.Copy, which os/exec
// uses on a pipe, then goes through Write and its cap.
type limitedBuffer struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); len(p) > room {
		b.over = true
		b.buf.Write(p[:max(room, 0)])
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) Bytes() []byte { return b.buf.Bytes() }

func (b *limitedBuffer) String() string { return b.buf.String() }

// watchDisk cancels the run when the clone directory grows past the cap.
func watchDisk(dir string, cancel context.CancelFunc) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				var total int64
				filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
					if err == nil && !d.IsDir() {
						if info, ierr := d.Info(); ierr == nil {
							total += info.Size()
						}
					}
					return nil
				})
				if total > cloneMaxDisk {
					cancel()
					return
				}
			}
		}
	}()
	return func() { close(done) }
}

// parseLsTree reads `git ls-tree -r -z` output: "<mode> <type> <sha>\t<path>\0".
// The listing carries no sizes: asking for them would make git fetch every
// blob the clone left out.
func parseLsTree(out []byte) ([]repoEntry, error) {
	var entries []repoEntry
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		meta, p, ok := bytes.Cut(rec, []byte{'\t'})
		if !ok {
			return nil, errors.New("kb: unreadable git tree listing")
		}
		f := strings.Fields(string(meta))
		if len(f) != 3 || f[1] != "blob" {
			continue
		}
		entries = append(entries, repoEntry{Path: string(p), SHA: f[2]})
	}
	return entries, nil
}
