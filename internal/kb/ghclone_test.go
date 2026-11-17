package kb

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeGit puts a script named git first on PATH, and gives the test its own
// temporary directory, so cloneDirs counts only this test's clones.
func fakeGit(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	t.Setenv("TMPDIR", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func cloneDirs() int {
	m, _ := filepath.Glob(filepath.Join(os.TempDir(), "applysmith-kb-clone-*"))
	return len(m)
}

func TestClone_FixedArgumentVectorNoHooksNoPrompt(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls.log")
	fakeGit(t, `echo "ARGS: $*" >> `+log+`
echo "ENV: prompt=$GIT_TERMINAL_PROMPT allow=$GIT_ALLOW_PROTOCOL global=$GIT_CONFIG_GLOBAL" >> `+log+`
echo "HDR: $GIT_CONFIG_VALUE_0" >> `+log+`
case "$1" in
  -c) case "$*" in *ls-tree*) printf '100644 blob aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\tREADME.md\0';; esac;;
esac
exit 0
`)
	before := cloneDirs()
	token := "tok" + "en-value-1234567890"
	c := newCloner(token)
	entries, err := c.listTree(context.Background(), "alex-example", "reef-tool", strings.Repeat("a", 40))
	if err != nil || len(entries) != 1 || entries[0].Path != "README.md" {
		t.Fatalf("entries = %+v, %v", entries, err)
	}
	b, _ := os.ReadFile(log)
	text := string(b)
	for _, want := range []string{"core.hooksPath=" + os.DevNull, "--depth 1", "--filter=blob:none", "https://github.com/alex-example/reef-tool.git",
		strings.Repeat("a", 40), "prompt=0", "allow=https", "global=" + os.DevNull} {
		if !strings.Contains(text, want) {
			t.Errorf("git calls lack %q:\n%s", want, text)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "ARGS") && strings.Contains(line, token) {
			t.Errorf("the token is on a command line: %s", line)
		}
	}
	if !strings.Contains(text, "HDR: Authorization: Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))) {
		t.Errorf("the token did not travel in the environment:\n%s", text)
	}
	if cloneDirs() != before {
		t.Fatal("the temporary directory was not removed")
	}
}

func TestClone_CancelKillsTheProcessGroupAndRemovesTheDir(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child-alive")
	// A sleep no other run of this test has, so pgrep sees only this one.
	nap := fmt.Sprintf("sleep 37.%d", time.Now().UnixNano()%1_000_000_000)
	fakeGit(t, `case "$*" in *fetch*) (`+nap+`; echo alive > `+marker+`) & `+nap+`;; esac
exit 0
`)
	before := cloneDirs()
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := newCloner("").listTree(ctx, "alex-example", "reef-tool", strings.Repeat("b", 40))
	if err == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
	if cloneDirs() != before {
		t.Fatal("the temporary directory survived a cancelled clone")
	}
	if out, _ := exec_pgrep(nap); out != "" {
		t.Fatalf("a child of git survived: %s", out)
	}
}

func TestClone_GitFailureHidesTheToken(t *testing.T) {
	fakeGit(t, `echo "fatal: could not read from remote with header $GIT_CONFIG_VALUE_0" >&2
exit 128
`)
	token := "tok" + "en-value-1234567890"
	_, err := newCloner(token).listTree(context.Background(), "alex-example", "reef-tool", strings.Repeat("c", 40))
	if err == nil || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), basicCredential(token)) {
		t.Fatalf("err = %v", err)
	}
}

func TestClone_RefusesABadSHA(t *testing.T) {
	if _, err := newCloner("").listTree(context.Background(), "a", "b", "--upload-pack=evil"); err == nil {
		t.Fatal("a non-SHA reached git")
	}
}

func TestGHCLIToken(t *testing.T) {
	fakeGh := func(script string) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\n"+script), 0o755)
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	fakeGh(`[ "$1 $2" = "auth token" ] && echo "tok-from-gh"`)
	if got := GHCLIToken(context.Background()); got != "tok-from-gh" {
		t.Fatalf("token = %q", got)
	}
	fakeGh(`echo "not logged in" >&2; exit 1`)
	if got := GHCLIToken(context.Background()); got != "" {
		t.Fatalf("token = %q", got)
	}
	fakeGh(`echo "two words"`)
	if got := GHCLIToken(context.Background()); got != "" {
		t.Fatalf("token = %q", got)
	}
}

// os/exec copies a pipe with io.Copy, which prefers ReadFrom when the
// destination has one; the cap must hold on that path too.
func TestClone_OutputBufferCapHoldsUnderIOCopy(t *testing.T) {
	b := limitedBuffer{max: 10}
	n, err := io.Copy(&b, strings.NewReader(strings.Repeat("x", 25)))
	if err != nil || n != 25 {
		t.Fatalf("io.Copy = %d, %v; the buffer must accept the whole stream", n, err)
	}
	if !b.over || len(b.Bytes()) != 10 {
		t.Fatalf("over = %v, kept %d bytes; want the cap of 10 and the overflow recorded", b.over, len(b.Bytes()))
	}
}
