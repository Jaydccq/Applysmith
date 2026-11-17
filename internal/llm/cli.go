package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
)

// cliKillAfter is how long a terminated process gets before it is killed.
var cliKillAfter = 5 * time.Second

var installHints = map[string]string{
	"codex":  "install the Codex CLI (npm install -g @openai/codex) and sign in with `codex login`",
	"claude": "install Claude Code (https://claude.com/claude-code) and sign in with `claude`",
}

// notSignedIn matches the messages the programs print when no login exists.
var notSignedIn = lazyregexp.New(`(?i)not (logged|signed) in|please (log|sign) in|run [^\n]{0,30}\b(login|/login)\b|no (credentials|login|auth)|unauthori[sz]ed|authentication (required|failed)`)

// CheckCLI finds the program on PATH and confirms it answers --version. The
// error carries the install hint.
func CheckCLI(ctx context.Context, program string) (string, error) {
	path, err := exec.LookPath(program)
	if err != nil {
		return "", fmt.Errorf("%s is not on PATH: %s", program, installHints[program])
	}
	vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(vctx, path, "--version")
	cmd.Env = cliEnv()
	if err := cmd.Run(); err != nil {
		return path, fmt.Errorf("%s did not answer --version: %s", program, installHints[program])
	}
	return path, nil
}

// cliEnv keeps only PATH, HOME, the account name, locale and proxy variables.
// The account name is needed because a signed-in program finds its login in
// the macOS keychain under USER; without it the claude program reports "Not
// logged in" although it is.
func cliEnv() []string {
	keep := map[string]bool{"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "LANG": true, "LANGUAGE": true,
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true, "ALL_PROXY": true,
		"http_proxy": true, "https_proxy": true, "no_proxy": true, "all_proxy": true}
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if keep[k] || strings.HasPrefix(k, "LC_") {
			env = append(env, kv)
		}
	}
	return env
}

// cliArgs are the fixed arguments of each program in its non-interactive mode.
// The prompt goes on standard input.
func cliArgs(program, model string) []string {
	var args []string
	switch program {
	case "codex":
		args = []string{"exec", "--sandbox", "read-only", "--skip-git-repo-check", "--ephemeral", "--color", "never"}
		if model != cliDefaultModel {
			args = append(args, "--model="+model)
		}
		return append(args, "-")
	default:
		args = []string{"-p", "--output-format", "text", "--no-session-persistence", "--tools", ""}
		if model != cliDefaultModel {
			args = append(args, "--model="+model)
		}
		return args
	}
}

func (r *Router) cliComplete(ctx context.Context, a *attempt, in attemptInput) (completion, error) {
	p := a.cand.provider
	if a.cand.model != cliDefaultModel {
		if err := ValidModelID(a.cand.model); err != nil {
			return completion{}, &Error{Kind: KindBadRequest, Message: err.Error()}
		}
	}
	path, err := exec.LookPath(p.Program)
	if err != nil {
		return completion{}, &Error{Kind: KindUnconfigured, Message: p.Program + " is not on PATH: " + installHints[p.Program]}
	}
	dir, err := os.MkdirTemp("", "applysmith-cli-")
	if err != nil {
		return completion{}, &Error{Kind: KindOther, Message: "no temporary working directory: " + err.Error()}
	}
	defer os.RemoveAll(dir)

	prompt := in.prompt
	if in.system != "" {
		prompt = in.system + "\n\n" + prompt
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, path, cliArgs(p.Program, a.cand.model)...)
	cmd.Dir, cmd.Env = dir, cliEnv()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(prompt), &stdout, &stderr
	cmd.SysProcAttr = groupAttr()
	cmd.Cancel = func() error {
		// The whole group gets the termination signal, then the kill after the
		// grace period, so a child the program started cannot outlive the call.
		time.AfterFunc(cliKillAfter, func() { signalGroup(cmd, true) })
		return signalGroup(cmd, false)
	}
	cmd.WaitDelay = cliKillAfter
	// A program that exits nonzero after ctx ended is a timeout or abort, which
	// the caller classifies from the context.
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			signalGroup(cmd, true)
			return completion{}, ctx.Err()
		}
		msg := stderr.String() + " " + stdout.String()
		var exit *exec.ExitError
		switch {
		case notSignedIn.MatchString(msg):
			return completion{}, &Error{Kind: KindUnconfigured, Message: p.Program + " is not signed in: " + installHints[p.Program]}
		case errors.As(err, &exit):
			return completion{}, &Error{Kind: KindOther, Message: boundedText(fmt.Sprintf("%s exited with status %d: %s", p.Program, exit.ExitCode(), stderr.String()))}
		}
		return completion{}, &Error{Kind: KindOther, Message: boundedText(err.Error())}
	}
	c := completion{text: strings.TrimSpace(stdout.String()), finish: "stop"}
	c.estimateUsage(prompt, stdout.String())
	return c, nil
}

// estimateUsage counts tokens from text length for programs that report none.
func estimateUsage(in, out string) Usage {
	return Usage{Input: estimatedTokens(len(in)), Output: estimatedTokens(len(out))}
}

// estimatedTokens is the token count of n bytes of text: one per four.
func estimatedTokens(n int) int { return (n + 3) / 4 }
