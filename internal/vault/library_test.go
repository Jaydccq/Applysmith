package vault

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// keyringDarwinSource reads the pinned library's macOS backend, which is the
// code that runs /usr/bin/security. The checks below hold the library to what
// the vault promises, so an upgrade that changes how a value travels fails here
// rather than in the field.
func keyringDarwinSource(t *testing.T) string {
	t.Helper()
	src, err := readKeyringDarwin()
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// readKeyringDarwin locates the library once per test binary. The vault
// imports it, so its module is in the module cache whenever this package
// builds; the test runs on every OS because it only reads the source.
var readKeyringDarwin = sync.OnceValues(func() (string, error) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/zalando/go-keyring").Output()
	if err != nil {
		return "", fmt.Errorf("go list: %w", err)
	}
	b, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(out)), "keyring_darwin.go"))
	return string(b), err
})

func TestKeychainLibrary_ValueTravelsOnStandardInputNeverInArgv(t *testing.T) {
	src := keyringDarwinSource(t)
	// The one place a value is written is the interactive tool's standard
	// input; no exec.Command call names the add command.
	if !strings.Contains(src, `exec.Command(execPathKeychain, "-i")`) || !strings.Contains(src, "StdinPipe()") {
		t.Fatal("the library no longer feeds the security tool through standard input")
	}
	for i, line := range strings.Split(src, "\n") {
		if strings.Contains(line, "exec.Command(") && strings.Contains(line, "add-generic-password") {
			t.Fatalf("line %d puts the add command, and so the value, on a command line: %s", i+1, line)
		}
	}
	if strings.Contains(src, `"-w", password`) || strings.Contains(src, `"-w",password`) {
		t.Fatal("the library passes the value with -w in argv")
	}
}

func TestKeychainLibrary_SetReplacesAndNeverAllowsEveryApplication(t *testing.T) {
	src := keyringDarwinSource(t)
	var add string
	for _, line := range strings.Split(src, "\n") {
		if strings.Contains(line, "add-generic-password") {
			add = line
		}
	}
	if !strings.Contains(add, "add-generic-password -U ") {
		t.Fatalf("the add command no longer updates an existing entry: %q", add)
	}
	// -A lets every application read the entry; -T names applications.
	for _, flag := range []string{" -A ", " -A\n", " -T ", " -T\n"} {
		if strings.Contains(add+"\n", flag) {
			t.Fatalf("the add command carries %q: %q", flag, add)
		}
	}
}

// The vault is the only reader and writer of secrets: it writes no file, sets
// no environment variable (a saved key is not copied into the process
// environment) and starts no program of its own.
func TestVaultSource_WritesNoFileAndSetsNoEnvironment(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range []string{"os.WriteFile", "os.Create(", "os.OpenFile", "os.Setenv", "os.Rename", "exec.Command"} {
			if strings.Contains(string(b), call) {
				t.Errorf("%s uses %s", f, call)
			}
		}
	}
}

func TestSet_NeverCopiesAValueIntoTheProcessEnvironment(t *testing.T) {
	v, err := New("ws", NewMemory()) // the real os.Getenv
	if err != nil {
		t.Fatal(err)
	}
	const canary = "env-copy-canary-0001"
	// A machine may export the variable itself, a documented way to supply
	// the secret; only what Set adds or changes counts.
	before := os.Environ()
	if _, err := v.Set("github-token", canary); err != nil {
		t.Fatal(err)
	}
	for _, kv := range os.Environ() {
		if strings.Contains(kv, canary) || strings.HasPrefix(kv, EnvName("github-token")+"=") && !slices.Contains(before, kv) {
			t.Fatalf("the environment holds a stored secret: %s", strings.SplitN(kv, "=", 2)[0])
		}
	}
}
