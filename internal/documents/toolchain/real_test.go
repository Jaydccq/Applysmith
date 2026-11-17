package toolchain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"applysmith/internal/documents/assets"
)

// envRealTools opts in to the test that runs the actual installed tools: it
// downloads the locked pypdf wheel and compiles the synthetic templates.
const envRealTools = "APPLYSMITH_TEST_DOCUMENT_TOOLS"

func TestToolchain_RealSetupAndSmoke(t *testing.T) {
	if os.Getenv(envRealTools) != "1" {
		t.Skipf("%s=1 runs the installed LaTeX, Python and Poppler tools (make verify-documents)", envRealTools)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	root := Root(t.TempDir())
	if v := os.Getenv("APPLYSMITH_TEST_DOCUMENT_ROOT"); v != "" {
		root = Root(v)
	}
	cfg := Config{Root: root}
	if err := Setup(ctx, cfg); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	tc, err := OpenForSmoke(ctx, cfg)
	if err != nil {
		t.Fatalf("OpenForSmoke: %v", err)
	}
	res, err := tc.Smoke(ctx)
	b, _ := json.MarshalIndent(res.Record, "", "  ")
	t.Logf("%s", b)
	if out := os.Getenv("APPLYSMITH_TEST_CLOSURE_OUT"); out != "" {
		cb, _ := json.MarshalIndent(res.Closure, "", " ")
		_ = os.WriteFile(out, cb, 0o600)
	}
	if err != nil {
		t.Fatalf("Smoke: %v", err)
	}
	rep, _ := Probe(ctx, cfg)
	if rep.State != StateReady {
		t.Fatalf("state after smoke = %s, want ready: %+v", rep.State, rep)
	}
}

// TestToolchain_RealMaliciousContentIsLiteral compiles documents whose every
// field is a TeX or shell payload. The payloads must come out as text, read no
// file, create no file and leave the build directory as it was.
func TestToolchain_RealMaliciousContentIsLiteral(t *testing.T) {
	tc := realToolchain(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel.txt")
	created := filepath.Join(outside, "created-by-payload")
	if err := os.WriteFile(sentinel, []byte("SENTINEL-CONTENT"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Each is a mechanism that would read the sentinel, make a file, run a
	// command or end the document early if it executed.
	payloads := []string{
		`\input{` + sentinel + `} \write18{touch ` + created + `} \immediate\openout3=` + created + ` \directlua{io.open("` + created + `","w")} $(touch ` + created + `) 100% \end{document}`,
		`\input{|touch ` + created + `} \immediate\write18{touch ` + created + `} \catcode` + "`" + `\%=12 50% of {braces} \read16 to \x`,
		"% a comment trick \\\n\\end{itemize}\\end{document} `touch " + created + "` ; touch " + created + " && x | y > z",
	}
	payload := strings.Join(payloads, " ")

	build, err := tc.newBuildDir()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(build)
	for _, d := range []struct {
		name, engine string
		render       func() ([]byte, error)
	}{
		{"evil-resume", "lualatex", func() ([]byte, error) {
			return assets.RenderResume(assets.Resume{Paper: assets.PaperA4, FirstName: "Alex", LastName: "Example", Email: "alex.example@example.com",
				Profile: payload, Languages: payload,
				Competencies: []assets.Competency{{Label: "x", Text: payload}},
				Experience:   []assets.Entry{{Dates: "2020-2021", Title: payload, Organization: "Example", Location: "Exampleland", Bullets: []string{payload}}}})
		}},
		{"evil-cover", "xelatex", func() ([]byte, error) {
			return assets.RenderCover(assets.Cover{Paper: assets.PaperA4, Name: "Alex Example", Date: "October 11, 2026", Salutation: payload, Closing: "Kind regards,",
				Blocks: []assets.Block{{Text: payload, Bullets: []string{payload}}}})
		}},
	} {
		dir := filepath.Join(build, d.name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if d.engine == "xelatex" {
			if err := writeAssets(dir); err != nil {
				t.Fatal(err)
			}
		}
		src, err := d.render()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, d.name+".tex"), src, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := tc.compile(ctx, d.engine, dir, d.name); err != nil {
			t.Fatalf("%s: %v", d.name, err)
		}
		dump := filepath.Join(dir, d.name+".txt")
		res, err := tc.VerifyPDF(ctx, filepath.Join(dir, d.name+".pdf"), PDFExpect{MinChars: 100, DumpText: dump})
		if err != nil || res.Overall != Pass {
			t.Fatalf("%s: verify = %+v, err = %v", d.name, res, err)
		}
		text, _ := os.ReadFile(dump)
		if strings.Contains(string(text), "SENTINEL-CONTENT") {
			t.Errorf("%s: the sentinel file was read", d.name)
		}
		// The payload is text: its commands read back, past the percent sign.
		flat := strings.Join(strings.Fields(string(text)), "")
		for _, want := range []string{`\write18{touch`, `\input{|touch`, `100%`, `50%of{braces}`} {
			if d.name == "evil-resume" && !strings.Contains(flat, strings.Join(strings.Fields(want), "")) {
				t.Errorf("%s: %q is not in the text layer", d.name, want)
			}
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if n := e.Name(); strings.Contains(n, "created") {
				t.Errorf("%s: payload created %s in the build directory", d.name, n)
			}
		}
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Error("a payload created a file outside the build directory")
	}
}

var (
	realOnce sync.Once
	realTC   *Toolchain
	realErr  error
	realTmp  string
)

func TestMain(m *testing.M) {
	code := m.Run()
	if realTmp != "" {
		os.RemoveAll(realTmp)
	}
	os.Exit(code)
}

// realToolchain prepares one toolchain root for the whole test run.
func realToolchain(t *testing.T) *Toolchain {
	t.Helper()
	if os.Getenv(envRealTools) != "1" {
		t.Skipf("%s=1 runs the installed LaTeX, Python and Poppler tools (make verify-documents)", envRealTools)
	}
	realOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		root := Root(os.Getenv("APPLYSMITH_TEST_DOCUMENT_ROOT"))
		if root == "" {
			dir, err := os.MkdirTemp("", "applysmith-toolchain-real-")
			if err != nil {
				realErr = err
				return
			}
			realTmp, root = dir, Root(dir)
		}
		cfg := Config{Root: root}
		if realErr = Setup(ctx, cfg); realErr == nil {
			realTC, realErr = OpenForSmoke(ctx, cfg)
		}
	})
	if realErr != nil {
		t.Fatalf("toolchain setup: %v", realErr)
	}
	return realTC
}

// spinSource never finishes: a macro that calls itself without growing.
const spinSource = `\documentclass{article}\begin{document}\def\a{\a}\a\end{document}`

// floodSource prints to the terminal without end, in long lines.
var floodSource = `\documentclass{article}\begin{document}\def\a{\message{` + strings.Repeat("0123456789", 300) + `}\a}\a\end{document}`

// startEngine writes src as a uniquely named document and compiles it in the
// background. The name appears in the engine's command line, so the process
// can be found by it.
func startEngine(t *testing.T, tc *Toolchain, ctx context.Context, src string) (name string, done <-chan error) {
	t.Helper()
	build, err := tc.newBuildDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(build) })
	name = fmt.Sprintf("spin-%d", rand.Uint32())
	if err := os.WriteFile(filepath.Join(build, name+".tex"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	ch := make(chan error, 1)
	go func() {
		_, err := tc.compile(ctx, EngineLuaLaTeX, build, name)
		ch <- err
	}()
	return name, ch
}

// enginePIDs lists the processes whose command line names the document.
func enginePIDs(name string) []int {
	out, _ := exec.Command("pgrep", "-f", name+".tex").Output()
	var pids []int
	for _, f := range strings.Fields(string(out)) {
		if n, err := strconv.Atoi(f); err == nil && n != os.Getpid() {
			pids = append(pids, n)
		}
	}
	return pids
}

// waitFor polls cond every 50 ms for at most limit.
func waitFor(limit time.Duration, cond func() bool) bool {
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if cond() {
			return true
		}
	}
	return cond()
}

// The installed engine is stopped when the run is cancelled (with the whole
// process group), at its deadline and at the output cap, and each is reported.
func TestToolchain_RealEngineIsStoppedAndTheStopReported(t *testing.T) {
	tc := realToolchain(t)
	for _, c := range []struct {
		name, src string
		limit     time.Duration
		cancel    bool
		want      error
	}{
		{"cancellation", spinSource, time.Minute, true, ErrCancelled},
		{"timeout", spinSource, 6 * time.Second, false, ErrTimeout},
		{"output cap", floodSource, 50 * time.Second, false, ErrLogLimit},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), c.limit)
			defer cancel()
			name, done := startEngine(t, tc, ctx, c.src)
			pgid := 0
			if c.cancel {
				var pids []int
				if !waitFor(30*time.Second, func() bool { pids = enginePIDs(name); return len(pids) > 0 }) {
					t.Fatal("the engine never started")
				}
				// The engine leads a process group of its own, so the kill reaches its descendants.
				if pgid, _ = syscall.Getpgid(pids[0]); pgid != pids[0] || pgid == syscall.Getpgrp() {
					t.Fatalf("engine pid %d has process group %d", pids[0], pgid)
				}
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, c.want) {
					t.Fatalf("err = %v, want %v", err, c.want)
				}
			case <-time.After(time.Minute):
				t.Fatal("the compile did not stop")
			}
			if !waitFor(10*time.Second, func() bool { return len(enginePIDs(name)) == 0 && (pgid == 0 || syscall.Kill(-pgid, 0) != nil) }) {
				t.Fatal("the engine or its process group is still alive")
			}
		})
	}
}
