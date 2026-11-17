//go:build !windows

package toolchain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"applysmith/internal/documents/assets"
)

// fakeTool writes an executable that records its arguments, one per line, runs
// body and exits 0.
func fakeTool(t *testing.T, dir, name, body string) (exe, argvFile string) {
	t.Helper()
	exe, argvFile = filepath.Join(dir, name), filepath.Join(dir, name+".argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argvFile + "'\n" + body + "\nexit 0\n"
	if err := os.WriteFile(exe, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return exe, argvFile
}

func argvOf(t *testing.T, file string) []string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("the tool never ran: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func fakeToolchain(t *testing.T) (*Toolchain, string) {
	dir := t.TempDir()
	return &Toolchain{Root: Root(filepath.Join(dir, "root"))}, dir
}

func TestSubprocessArguments_EngineAndVerifiersTakeFixedFlagsAndPaths(t *testing.T) {
	tc, dir := fakeToolchain(t)
	lua, luaArgv := fakeTool(t, dir, "lualatex", "")
	python, pyArgv := fakeTool(t, dir, "python", "cat > /dev/null")
	pdftotext, _ := fakeTool(t, dir, "pdftotext", "")
	tc.Tools.LuaLaTeX, tc.Tools.Python, tc.Tools.PDFToText = lua, python, pdftotext
	if _, err := tc.compile(context.Background(), EngineLuaLaTeX, dir, "resume"); err != nil {
		t.Fatal(err)
	}
	want := []string{"-no-shell-escape", "-interaction=nonstopmode", "-halt-on-error", "-file-line-error", "-no-parse-first-line", "-recorder", "resume.tex"}
	if got := argvOf(t, luaArgv); !reflect.DeepEqual(got, want) {
		t.Fatalf("engine argv = %q, want %q", got, want)
	}
	if c := tc.engineCommand(lua, dir, "resume"); c.Path != lua || !filepath.IsAbs(c.Path) || filepath.Base(c.Path) == "sh" || c.Timeout != EngineTimeout {
		t.Errorf("command = %+v", c)
	}
	pdf, dump := filepath.Join(dir, "resume.pdf"), filepath.Join(dir, "resume.txt")
	if err := os.WriteFile(pdf, []byte("%PDF-1.7"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The expectations a document run builds hold numbers and paths only.
	if _, err := tc.VerifyPDF(context.Background(), pdf, PDFExpect{MaxPages: 2, MinChars: 150, ASCIIDates: true, DumpText: dump}); !errors.Is(err, ErrVerdict) {
		t.Fatalf("a tool that prints nothing must be refused: %v", err)
	}
	want = []string{"-I", "-", pdf, "--max-pages", "2", "--min-chars", "150", "--ascii-dates", "--dump-text", dump}
	if got := argvOf(t, pyArgv); !reflect.DeepEqual(got, want) {
		t.Fatalf("verify_pdf argv = %q, want %q", got, want)
	}
	_, _ = tc.VerifyLayout(context.Background(), pdf, LayoutExpect{Template: TemplateResume, Paper: assets.PaperA4})
	want = []string{"-I", "-", pdf, "--pdftotext", pdftotext, "--template", "resume", "--paper", "a4"}
	if got := argvOf(t, pyArgv); !reflect.DeepEqual(got, want) {
		t.Fatalf("verify_layout argv = %q, want %q", got, want)
	}
}

// A name or a source the service did not produce is refused before any
// process starts or any state is made.
func TestBuild_RefusesForgedNamesAndStaleSourceBeforeSpawning(t *testing.T) {
	tc, dir := fakeToolchain(t)
	exe, argv := fakeTool(t, dir, "lualatex", "")
	tc.Tools.LuaLaTeX = exe
	tex := []byte(`\documentclass{article}\begin{document}x\end{document}`)
	for _, name := range []string{"", "../escape", "a/b", "resume.tex", "Resume", "x;touch pwned", "-halt", strings.Repeat("a", 41)} {
		if _, err := tc.Build(context.Background(), BuildInput{Name: name, Engine: EngineLuaLaTeX, TeX: tex}); !errors.Is(err, ErrBadName) {
			t.Errorf("name %q: err = %v", name, err)
		}
	}
	if _, err := tc.Build(context.Background(), BuildInput{Name: "resume", Engine: "sh", TeX: tex}); err == nil {
		t.Error("an unknown engine was accepted")
	}
	_, err := tc.Build(context.Background(), BuildInput{Name: "resume", Engine: EngineLuaLaTeX, TeX: append(append([]byte(nil), tex...), '%'), TeXSHA256: sha256Hex(tex)})
	if !errors.Is(err, ErrStaleSource) {
		t.Errorf("stale source: err = %v", err)
	}
	if _, err := os.Stat(argv); !os.IsNotExist(err) {
		t.Error("a process was started for a refused build")
	}
	if _, err := os.Stat(tc.Root.Path()); !os.IsNotExist(err) {
		t.Error("a refused build created state below the root")
	}
}

// A compile that is cancelled or past its deadline stops the engine and every
// descendant it started; the test engine starts a background child.
func TestCompile_CancellationAndDeadlineStopTheEngineGroup(t *testing.T) {
	for _, want := range []error{ErrCancelled, ErrTimeout} {
		t.Run(want.Error(), func(t *testing.T) {
			tc, dir := fakeToolchain(t)
			pidFile := filepath.Join(dir, "child.pid")
			tc.Tools.LuaLaTeX, _ = fakeTool(t, dir, "lualatex", "sleep 60 &\necho $! > '"+pidFile+"'\nwait")
			// The cancel case has no deadline, so a slow start cannot turn it
			// into a timeout; it cancels once the engine wrote its child's pid.
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if want == ErrTimeout {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 3*time.Second)
				defer stop()
			} else {
				go func() {
					for i := 0; i < 1500; i++ {
						if b, err := os.ReadFile(pidFile); err == nil && len(strings.TrimSpace(string(b))) > 0 {
							break
						}
						time.Sleep(20 * time.Millisecond)
					}
					cancel()
				}()
			}
			if _, err := tc.compile(ctx, EngineLuaLaTeX, dir, "resume"); !errors.Is(err, want) {
				t.Fatalf("err = %v, want %v", err, want)
			}
			assertDead(t, pidFile)
		})
	}
}
