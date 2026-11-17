package llm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// installFake writes an executable named program that records its arguments,
// standard input, working directory and environment under dir/rec and then
// prints the transcript file. It puts the directory first on PATH.
func installFake(t *testing.T, program, transcript string, exit int, tail string) (rec string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixtures")
	}
	dir := t.TempDir()
	rec = filepath.Join(dir, "rec")
	if err := os.Mkdir(rec, 0o755); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--version" ]; then echo "fake 1.0"; exit 0; fi
printf '%%s\n' "$@" > %[1]s/args
cat > %[1]s/stdin
pwd > %[1]s/cwd
env > %[1]s/env
%[4]s
if [ -n %[2]q ]; then cat %[2]q; fi
exit %[3]d
`, rec, transcript, exit, tail)
	if err := os.WriteFile(filepath.Join(dir, program), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return rec
}

func readRec(t *testing.T, rec, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(rec, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func cliEnvSetup(t *testing.T, program string) *env {
	t.Helper()
	e := newEnv(t)
	e.provider(ProviderConfig{ID: program, Kind: KindCLI, Program: program}, "")
	e.route("narrative", Route{Provider: program, Model: map[string]string{"claude": "sonnet", "codex": cliDefaultModel}[program], Thinking: ThinkOff})
	return e
}

func TestLLM_CLI_RecordedTranscript_Claude(t *testing.T) {
	transcript, _ := filepath.Abs("testdata/cli/claude_reply.txt")
	rec := installFake(t, "claude", transcript, 0, "")
	t.Setenv("SECRET_TOKEN_FOR_TEST", "must-not-pass")
	e := cliEnvSetup(t, "claude")
	resp, err := e.r.Complete(bg(), Request{Task: "narrative", System: "Be brief.", Prompt: "Assess the role."})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(transcript)
	if resp.Text != strings.TrimSpace(string(want)) || resp.Billing != BillingSubscription || resp.CostUSD == nil || *resp.CostUSD != 0 {
		t.Fatalf("resp = %+v", resp)
	}
	args := strings.Fields(readRec(t, rec, "args"))
	if args[0] != "-p" || !strings.Contains(strings.Join(args, " "), "--model=sonnet") {
		t.Fatalf("args = %v", args)
	}
	if got := readRec(t, rec, "stdin"); got != "Be brief.\n\nAssess the role." {
		t.Fatalf("stdin = %q", got)
	}
	cwd := strings.TrimSpace(readRec(t, rec, "cwd"))
	if cwd == "" || strings.Contains(cwd, "internal/llm") {
		t.Fatalf("working directory = %q, want an empty temporary directory", cwd)
	}
	if _, err := os.Stat(cwd); err == nil {
		t.Fatal("the temporary working directory was not removed")
	}
	envText := readRec(t, rec, "env")
	if strings.Contains(envText, "SECRET_TOKEN_FOR_TEST") || !strings.Contains(envText, "PATH=") || !strings.Contains(envText, "HOME=") {
		t.Fatalf("environment = %q", envText)
	}
	rows := e.ledger()
	if len(rows) != 1 || rows[0].Billing != BillingSubscription || rows[0].TTFT != nil {
		t.Fatalf("ledger = %+v", rows)
	}
	// The program reports no usage, so the row says its counts are estimated.
	if rows[0].Input == 0 || rows[0].Output == 0 || !strings.Contains(ledgerErrorText(t, e), "usage estimated") {
		t.Fatalf("ledger = %+v, error text %q", rows, ledgerErrorText(t, e))
	}
}

func TestLLM_CLI_Codex_ReadOnlySandboxAndDefaultModel(t *testing.T) {
	rec := installFake(t, "codex", "", 0, "echo done")
	e := cliEnvSetup(t, "codex")
	if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(strings.Fields(readRec(t, rec, "args")), " ")
	if !strings.HasPrefix(args, "exec --sandbox read-only") || strings.Contains(args, " -m ") || !strings.HasSuffix(args, " -") {
		t.Fatalf("args = %q", args)
	}
}

func TestLLM_CLI_NotSignedIn_FallsBack(t *testing.T) {
	transcript, _ := filepath.Abs("testdata/cli/not_signed_in.txt")
	installFake(t, "claude", transcript, 1, "")
	e := cliEnvSetup(t, "claude")
	fake := NewFake(Fixture{Task: "narrative", Contains: "hi", Steps: []Step{{Text: "fake answer"}}})
	e.r.fake = fake
	e.provider(ProviderConfig{ID: "fk", Kind: KindFake}, "")
	e.route("narrative", Route{Provider: "claude", Model: "sonnet", Thinking: ThinkOff, Fallbacks: []Fallback{{"fk", "m"}}})
	resp, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	if err != nil || resp.Provider != "fk" {
		t.Fatalf("resp = %+v err = %v", resp, err)
	}
	if rows := e.ledger(); rows[0].Outcome != "unconfigured" {
		t.Fatalf("ledger = %+v", rows)
	}
}

func TestLLM_CLI_FindsProgramOnPATHOnly(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	e := cliEnvSetup(t, "codex")
	_, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	ce := wantKind(t, err, KindUnconfigured)
	if !strings.Contains(ce.Message, "codex login") {
		t.Fatalf("missing the install hint: %q", ce.Message)
	}
	if _, err := CheckCLI(bg(), "codex"); err == nil || !strings.Contains(err.Error(), "not on PATH") {
		t.Fatalf("CheckCLI = %v", err)
	}
}

// cliTestTimeout is the route timeout of the kill tests, in seconds. It is long
// enough for the fake program to record its pid on a loaded machine before the
// timeout fires; a shorter one kills it before there is a pid to check.
const cliTestTimeout = 3

// waitDead polls until the process is gone. A killed process is reaped
// asynchronously, so a fixed pause before the check races on a loaded machine.
func waitDead(pid int) bool {
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

func TestLLM_CLI_TimeoutKillsTheProcess(t *testing.T) {
	cliKillAfter = 300 * time.Millisecond
	t.Cleanup(func() { cliKillAfter = 5 * time.Second })
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	// The program ignores the termination signal, so only the kill ends it.
	installFake(t, "claude", "", 0, fmt.Sprintf("trap '' TERM; echo $$ > %s; exec sleep 30", pidFile))
	e := cliEnvSetup(t, "claude")
	e.route("narrative", Route{Provider: "claude", Model: "sonnet", Thinking: ThinkOff, TimeoutSeconds: cliTestTimeout})
	start := time.Now()
	_, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi"})
	wantKind(t, err, KindTimeout)
	if d := time.Since(start); d > cliTestTimeout*time.Second+cliKillAfter+5*time.Second {
		t.Fatalf("took %v", d)
	}
	b, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		t.Fatal(rerr)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if !waitDead(pid) {
		t.Fatalf("process %d is still alive", pid)
	}
	if rows := e.ledger(); len(rows) != 1 || rows[0].Outcome != "timeout" {
		t.Fatalf("ledger = %+v", rows)
	}
}

func TestLLM_CLI_ModelIDNeverBecomesAnOption(t *testing.T) {
	for _, bad := range []string{"--yolo", "-m", "--dangerously-skip-permissions", "a b", "x;rm", ""} {
		if ValidModelID(bad) == nil {
			t.Errorf("%q accepted as a model id", bad)
		}
	}
	for _, good := range []string{"sonnet", "gpt-6.1-sol", "openai/gpt-6-luna", "vendor/model:free", "claude-haiku-4-5", "models/x@v1"} {
		if err := ValidModelID(good); err != nil {
			t.Errorf("%q rejected: %v", good, err)
		}
	}
	e := newEnv(t)
	e.provider(ProviderConfig{ID: "c", Kind: KindCLI, Program: "claude"}, "")
	e.provider(ProviderConfig{ID: "f", Kind: KindFake}, "")
	for name, row := range map[string]Route{
		"primary":  {Provider: "c", Model: "--yolo"},
		"fallback": {Provider: "f", Model: "ok", Fallbacks: []Fallback{{"c", "--dangerously-skip-permissions"}}},
	} {
		if err := e.r.PatchRoutes(bg(), map[string]*Route{"narrative": &row}); err == nil {
			t.Errorf("%s: route with an option-shaped model accepted", name)
		}
	}
	// Pinned and environment-pinned values are checked too.
	if _, err := e.r.Complete(bg(), Request{Task: "narrative", Prompt: "hi", Model: "c/--yolo"}); KindOf(err) != KindBadRequest {
		t.Errorf("override err = %v", err)
	}
	e.envs["APPLYSMITH_MODEL_NARRATIVE"] = "c/--yolo"
	if _, err := e.r.Resolve(bg(), "narrative"); KindOf(err) != KindNoRoute {
		t.Errorf("env pin err = %v", err)
	}
}

func TestLLM_CLI_ModelChecksJustBeforeExec(t *testing.T) {
	rec := installFake(t, "claude", "", 0, "echo hi")
	e := newEnv(t)
	a := &attempt{cand: candidate{provider: ProviderConfig{ID: "c", Kind: KindCLI, Program: "claude"}, model: "--yolo"}}
	_, err := e.r.cliComplete(bg(), a, attemptInput{prompt: "x"})
	if KindOf(err) != KindBadRequest {
		t.Fatalf("err = %v", err)
	}
	if _, serr := os.Stat(filepath.Join(rec, "args")); serr == nil {
		t.Fatal("the program ran")
	}
}

// A child the program started must not outlive a cancelled call, whether or
// not the program honours the termination signal. A timeout ends the call on the
// same path (TestLLM_CLI_TimeoutKillsTheProcess), so the call is cancelled here
// as soon as both pids are written instead of waiting out a route timeout.
func TestLLM_CLI_KillTakesTheWholeProcessGroup(t *testing.T) {
	cliKillAfter = 300 * time.Millisecond
	t.Cleanup(func() { cliKillAfter = 5 * time.Second })
	for name, trap := range map[string]string{"terminate": "", "ignores terminate": "trap '' TERM;"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			tail := fmt.Sprintf("%s sleep 60 & echo $! > %s/child; echo $$ > %s/parent; wait", trap, dir, dir)
			installFake(t, "claude", "", 0, tail)
			e := cliEnvSetup(t, "claude")
			pid := func(f string) int {
				b, _ := os.ReadFile(filepath.Join(dir, f))
				n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
				return n
			}
			ctx, cancel := context.WithCancel(bg())
			// Cancel only once both pids exist; a fixed delay races the
			// script's start on a loaded machine and leaves no pid to check.
			go func() {
				for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && (pid("child") == 0 || pid("parent") == 0); {
					time.Sleep(20 * time.Millisecond)
				}
				cancel()
			}()
			_, err := e.r.Complete(ctx, Request{Task: "narrative", Prompt: "hi"})
			wantKind(t, err, KindAborted)
			for _, f := range []string{"child", "parent"} {
				if p := pid(f); p == 0 || !waitDead(p) {
					t.Errorf("%s process %d is still alive", f, p)
				}
			}
		})
	}
}

// A signed-in claude program reads its login from the keychain under the
// account name, so the account name must reach it.
func TestLLM_CLI_EnvKeepsTheAccountNameAndDropsSecrets(t *testing.T) {
	t.Setenv("USER", "alex")
	t.Setenv("LOGNAME", "alex")
	t.Setenv("OPENAI_API_KEY", "placeholder-not-a-key")
	got := map[string]string{}
	for _, kv := range cliEnv() {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	if got["USER"] != "alex" || got["LOGNAME"] != "alex" {
		t.Fatalf("account name not kept: USER=%q LOGNAME=%q", got["USER"], got["LOGNAME"])
	}
	if _, ok := got["OPENAI_API_KEY"]; ok {
		t.Fatal("a provider key reached the program")
	}
}
