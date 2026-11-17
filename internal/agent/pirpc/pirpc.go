// Package pirpc is the wire contract with the pinned pi process in RPC mode:
// the launch flags, the version check, line framing, event decoding, the
// mapping of pi events to session stream events, usage extraction and
// repetition detection. It does no process or network I/O, so the contract
// can be replayed from a recorded transcript. The supervisor that owns the
// process is built on top of it.
package pirpc

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// PinnedVersion is the only pi release the model loop runs on. Its RPC event
// shapes differ from older releases, so a different version at start makes
// the loop unavailable instead of being tried.
const PinnedVersion = "1.0.4"

// CheckVersion compares the output of `pi --version` with the pin.
func CheckVersion(output string) error {
	if got := strings.TrimSpace(output); got != PinnedVersion {
		return fmt.Errorf("pi version %q found, %s required", got, PinnedVersion)
	}
	return nil
}

// LaunchArgs returns the pi arguments for the supervised process. Every
// resource pi would discover on its own is switched off, so the only tools
// are the ones the embedded extension registers.
func LaunchArgs(extensionPath, provider, model string) []string {
	return []string{
		"--mode", "rpc",
		"--no-session",
		"--no-builtin-tools",
		"--no-extensions",
		"--no-mcp",
		"--no-skills",
		"--no-prompt-templates",
		"--no-themes",
		"--no-context-files",
		"--no-approve",
		"-e", extensionPath,
		"--provider", provider,
		"--model", model,
	}
}

// ChildEnv returns the variables added to the supervised process:
// the product-owned agent directory (where pi keeps its logins) and no
// update check, telemetry or catalog refresh.
func ChildEnv(agentDir string) []string {
	return []string{
		"PI_CODING_AGENT_DIR=" + agentDir,
		"PI_SKIP_VERSION_CHECK=1",
		"PI_TELEMETRY=0",
		"PI_OFFLINE=1",
	}
}

// maxLine bounds one record; agent_end repeats every message of the run.
const maxLine = 16 << 20

// ReadRecords decodes pi's stdout until it ends. Records are split on LF only
// (bufio's line scanner does not treat U+2028 or U+2029 as breaks, which are
// valid inside JSON strings). A line that is not a record is an error and
// ends the read.
func ReadRecords(r io.Reader, fn func(Record)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		rec, err := Decode(line)
		if err != nil {
			return err
		}
		fn(rec)
	}
	return sc.Err()
}
