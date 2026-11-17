package wire

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	repoRoot      = "../../.."
	generatorConf = "tygo.yaml"
	generatedFile = "web/src/wire/index.ts"
)

// generate runs the pinned generator into a temporary file, with the
// repository's own configuration, and returns what it wrote. The input is the
// same for every test, so the subprocess runs once per test binary.
func generate(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("the generator is a subprocess")
	}
	ts, err := generated()
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

var generated = sync.OnceValues(func() (string, error) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, generatorConf))
	if err != nil {
		return "", err
	}
	var conf struct {
		Packages []map[string]any `yaml:"packages"`
	}
	if err := yaml.Unmarshal(raw, &conf); err != nil {
		return "", err
	}
	if len(conf.Packages) != 1 {
		return "", fmt.Errorf("%s configures %d packages, want 1", generatorConf, len(conf.Packages))
	}
	dir, err := os.MkdirTemp("", "wire-ts-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "wire.ts")
	conf.Packages[0]["output_path"] = out
	tmp, err := yaml.Marshal(conf)
	if err != nil {
		return "", err
	}
	confPath := filepath.Join(dir, "tygo.yaml")
	if err := os.WriteFile(confPath, tmp, 0o600); err != nil {
		return "", err
	}
	cmd := exec.Command("go", "tool", "tygo", "generate", "--config", confPath)
	cmd.Dir = repoRoot
	if msg, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go tool tygo: %v\n%s", err, msg)
	}
	b, err := os.ReadFile(out)
	return string(b), err
})

// Every registered wire type is declared, so a route can never name a type
// the dashboard cannot import.
func TestGeneratedDeclarationsCoverTheRegistry(t *testing.T) {
	ts := generate(t)
	for name := range Types {
		if !regexp.MustCompile(`export (interface|type) ` + name + `\b`).MatchString(ts) {
			t.Errorf("%s is registered but not declared", name)
		}
	}
	// Optional fields stay optional and absent-versus-null stays visible.
	if !strings.Contains(ts, "uptimeSeconds?: SafeInt;") {
		t.Error("an omitempty field is not optional in the declarations")
	}
}

// A new stream event type must reach the event union, and the closed set of
// error codes must reach its union.
func TestGeneratedUnionsMatchTheRegistries(t *testing.T) {
	ts := generate(t)
	union := regexp.MustCompile(`(?s)export type StreamEvent =(.*?\});\n`).FindStringSubmatch(ts)
	if union == nil {
		t.Fatal("no StreamEvent union")
	}
	members := regexp.MustCompile(`\{ event: "(\w+)"; data: (\w+) \}`).FindAllStringSubmatch(union[1], -1)
	got := map[string]string{}
	for _, m := range members {
		got[m[1]] = m[2]
	}
	if len(got) != len(Events) {
		t.Errorf("the union has %d members, the registry %d", len(got), len(Events))
	}
	for event, payload := range Events {
		if got[event] != payload {
			t.Errorf("event %s: the union says %q, the registry %q", event, got[event], payload)
		}
	}
	codes := regexp.MustCompile(`typeof (Code\w+)`).FindAllStringSubmatch(regexp.MustCompile(`(?s)export type ErrorCodeName =(.*?);`).FindString(ts), -1)
	var names []string
	for _, c := range codes {
		names = append(names, c[1])
	}
	for _, code := range Codes() {
		found := false
		for _, n := range names {
			found = found || strings.Contains(ts, "export const "+n+": ErrorCode = \""+string(code)+"\"")
		}
		if !found {
			t.Errorf("error code %s is not in the ErrorCodeName union", code)
		}
	}
	if len(names) != len(Codes()) {
		t.Errorf("the union lists %d codes, the closed set has %d", len(names), len(Codes()))
	}
}

// The declarations are committed so a checkout type-checks without running
// the generator, which makes drift a failure: a missing file, a hand edit or a
// Go struct changed without running make generate.
func TestGeneratedFileHasNotDrifted(t *testing.T) {
	fresh := generate(t)
	have, err := os.ReadFile(filepath.Join(repoRoot, generatedFile))
	if err != nil {
		t.Fatalf("%s must be committed; run make generate: %v", generatedFile, err)
	}
	if string(have) != fresh {
		t.Fatalf("%s differs from the declarations generated from internal/api/wire; run make generate", generatedFile)
	}
}

// The notification stream has its own event union; it must match its registry.
func TestNotificationUnionMatchesItsRegistry(t *testing.T) {
	ts := generate(t)
	union := regexp.MustCompile(`(?s)export type NotificationStreamEvent =(.*?\});\n`).FindStringSubmatch(ts)
	if union == nil {
		t.Fatal("no NotificationStreamEvent union")
	}
	got := map[string]string{}
	for _, m := range regexp.MustCompile(`\{ event: "(\w+)"; data: (\w+) \}`).FindAllStringSubmatch(union[1], -1) {
		got[m[1]] = m[2]
	}
	for event, payload := range NotificationEvents {
		if got[event] != payload {
			t.Errorf("event %s: the union says %q, the registry %q", event, got[event], payload)
		}
	}
	if len(got) != len(NotificationEvents)+2 {
		t.Errorf("the union has %d members", len(got))
	}
}
