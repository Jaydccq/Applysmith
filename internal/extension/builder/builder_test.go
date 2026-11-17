package builder

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	extRoot    = "../../../extension"
	engineRoot = "../../../engine"
)

func buildReal(t *testing.T) *Output {
	t.Helper()
	out, err := Build(extRoot, engineRoot)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

var shared struct {
	once sync.Once
	out  *Output
	err  error
}

// builtOnce is one build of the real tree for the tests that only read it; a
// build is a full esbuild run of the extension and the engine.
func builtOnce(t *testing.T) *Output {
	t.Helper()
	shared.once.Do(func() { shared.out, shared.err = Build(extRoot, engineRoot) })
	if shared.err != nil {
		t.Fatal(shared.err)
	}
	return shared.out
}

// copyTree copies dir into a fresh temporary directory, leaving out
// node_modules, and returns the copy.
func copyTree(t *testing.T, dir, name string, into string) string {
	t.Helper()
	dst := filepath.Join(into, name)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == "test") {
			return filepath.SkipDir
		}
		if d.Name() == "node_modules" { // a worktree links node_modules in
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// sandbox copies the extension and the engine side by side, as in the
// repository, so a test can change either without touching the real tree.
func sandbox(t *testing.T) (ext, eng string) {
	t.Helper()
	tmp := t.TempDir()
	ext = copyTree(t, extRoot, "extension", tmp)
	eng = filepath.Join(tmp, "engine")
	copyTree(t, filepath.Join(engineRoot, "src"), "engine/src", tmp)
	return ext, eng
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildProducesTheUnpackedExtension(t *testing.T) {
	out := builtOnce(t)
	for _, f := range []string{"manifest.json", "sw.js", "loader.js", "popup.html", "popup.js", "panel.js", "bridge.js", "probe.js", "advance.js", "frame.js", "hook.js", "engine.js"} {
		if len(out.Files[f]) == 0 {
			t.Errorf("%s is missing or empty", f)
		}
	}
	if len(out.Stamp) != StampLen {
		t.Fatalf("stamp %q", out.Stamp)
	}
	for _, f := range []string{"sw.js", "popup.js"} {
		if !strings.Contains(string(out.Files[f]), out.Stamp) {
			t.Errorf("%s does not embed the build stamp", f)
		}
	}
}

// Chrome keeps running a registered service worker across a restart while the
// manifest version is unchanged, so every build carries its own version: the
// source version and the stamp's first four hex digits as a fourth number.
func TestManifest_VersionChangesWithEveryBuild(t *testing.T) {
	out := builtOnce(t)
	var m struct {
		Version     string `json:"version"`
		VersionName string `json:"version_name"`
	}
	if err := json.Unmarshal(out.Files["manifest.json"], &m); err != nil {
		t.Fatal(err)
	}
	base, st, _ := strings.Cut(m.VersionName, "+")
	if st != out.Stamp {
		t.Fatalf("version_name %q lacks stamp %s", m.VersionName, out.Stamp)
	}
	want, err := BuildVersion(base, out.Stamp)
	if err != nil || m.Version != want || !strings.HasPrefix(m.Version, base+".") {
		t.Fatalf("version %q, want %q (%v)", m.Version, want, err)
	}
	cases := []struct{ version, stamp, want string }{
		{"0.1.0", "3f9a0c27d41e8b65", "0.1.0.16282"},
		{"2", "0001000000000000", "2.1"},
	}
	for _, c := range cases {
		if got, err := BuildVersion(c.version, c.stamp); err != nil || got != c.want {
			t.Errorf("BuildVersion(%q, %q) = %q, %v; want %q", c.version, c.stamp, got, err, c.want)
		}
	}
	for _, bad := range []struct{ version, stamp string }{{"0.1.0.4", "3f9a0c27d41e8b65"}, {"0.1.70000", "3f9a0c27d41e8b65"}, {"0.01.0", "3f9a0c27d41e8b65"}, {"0.1.0", "zz"}} {
		if _, err := BuildVersion(bad.version, bad.stamp); err == nil {
			t.Errorf("BuildVersion(%q, %q) was accepted", bad.version, bad.stamp)
		}
	}
}

func TestManifest_PermissionTest(t *testing.T) {
	good := builtOnce(t).Files["manifest.json"]
	if err := CheckManifest(good); err != nil {
		t.Fatal(err)
	}
	mutate := func(f func(m map[string]any)) []byte {
		var m map[string]any
		if err := json.Unmarshal(good, &m); err != nil {
			t.Fatal(err)
		}
		f(m)
		b, _ := json.Marshal(m)
		return b
	}
	cases := map[string]func(m map[string]any){
		"an added permission": func(m map[string]any) {
			m["permissions"] = append(m["permissions"].([]any), "history")
		},
		"a removed permission": func(m map[string]any) { m["permissions"] = m["permissions"].([]any)[1:] },
		"all_urls":             func(m map[string]any) { m["host_permissions"] = []any{"<all_urls>"} },
		"a host added":         func(m map[string]any) { m["host_permissions"] = append(m["host_permissions"].([]any), "file:///*") },
		"externally_connectable": func(m map[string]any) {
			m["externally_connectable"] = map[string]any{"matches": []any{"https://example.com/*"}}
		},
		"web accessible resources": func(m map[string]any) {
			m["web_accessible_resources"] = []any{map[string]any{"resources": []any{"a.js"}, "matches": []any{"https://example.com/*"}}}
		},
		"a main world content script": func(m map[string]any) {
			cs := m["content_scripts"].([]any)[0].(map[string]any)
			cs["world"] = "MAIN"
		},
		"a second content script": func(m map[string]any) {
			m["content_scripts"] = append(m["content_scripts"].([]any), map[string]any{"matches": []any{"https://example.com/*"}, "js": []any{"x.js"}})
		},
		"a looser content security policy": func(m map[string]any) {
			m["content_security_policy"] = map[string]any{"extension_pages": "script-src 'self' 'unsafe-eval'; object-src 'self'"}
		},
		"an extra command": func(m map[string]any) {
			m["commands"].(map[string]any)["other"] = map[string]any{"description": "x"}
		},
		"an older minimum Chrome": func(m map[string]any) { m["minimum_chrome_version"] = "110" },
		"an optional permission":  func(m map[string]any) { m["optional_permissions"] = []any{"history"} },
		"a missing icon":          func(m map[string]any) { delete(m["icons"].(map[string]any), "48") },
		"a missing toolbar icon":  func(m map[string]any) { delete(m["action"].(map[string]any), "default_icon") },
		"the source version":      func(m map[string]any) { m["version"] = "0.1.0" },
	}
	for name, f := range cases {
		if err := CheckManifest(mutate(f)); err == nil {
			t.Errorf("%s passed the manifest test", name)
		}
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	a, b := builtOnce(t), buildReal(t)
	if a.Stamp != b.Stamp {
		t.Fatal("stamps differ")
	}
	for name, body := range a.Files {
		if string(body) != string(b.Files[name]) {
			t.Errorf("%s differs between builds", name)
		}
	}
}

func TestLoaderBudget(t *testing.T) {
	ext, eng := sandbox(t)
	write(t, filepath.Join(ext, "src", "loader.ts"), "export const ballast = \""+strings.Repeat("0123456789abcdef", 1100)+"\";\nconsole.log(ballast);\n")
	_, err := Build(ext, eng)
	if err == nil || !strings.Contains(err.Error(), "loader.js") || !strings.Contains(err.Error(), "16 KiB") {
		t.Fatalf("a 17 KiB loader gave %v", err)
	}
}

func TestSourceFileLineLimit(t *testing.T) {
	ext, eng := sandbox(t)
	write(t, filepath.Join(ext, "src", "long.ts"), strings.Repeat("export {};\n", MaxSourceLOC+1))
	_, err := Build(ext, eng)
	if err == nil || !strings.Contains(err.Error(), "long.ts") {
		t.Fatalf("a long source gave %v", err)
	}
}

// Only the API client reaches the network. The page-world helper may wrap the
// page's network functions but makes no request of its own; any other file may
// not even name them.
func TestEgress_OnlyTheAPIClientReachesTheNetwork(t *testing.T) {
	wrap := []byte("export const wrap = (w: Window) => { const original = w.fetch; const open = XMLHttpRequest.prototype.open; return [original, open]; };\n")
	if err := checkSources(map[string][]byte{apiClient: []byte("export const get = () => fetch('/x');\n"), mainWorldDir + "wrap.ts": wrap}); err != nil {
		t.Fatalf("the API client or a wrapper in the page-world helper was refused: %v", err)
	}
	for _, call := range []string{"fetch('/x')", "new XMLHttpRequest()", "new WebSocket('ws://example.com')", "navigator.sendBeacon('/x')"} {
		for _, name := range []string{"src/popup/leak.ts", mainWorldDir + "own.ts"} {
			if err := checkSources(map[string][]byte{name: []byte("export const x = () => " + call + ";\n")}); err == nil || !strings.Contains(err.Error(), name) {
				t.Errorf("%s in %s gave %v", call, name, err)
			}
		}
	}
	if err := checkSources(map[string][]byte{"src/widgets/wrap.ts": wrap}); err == nil || !strings.Contains(err.Error(), "wrap.ts") {
		t.Errorf("naming the page's network functions outside the helper gave %v", err)
	}
}

func TestRuntimeBundleImportsOnlyEngineAndExtensionSources(t *testing.T) {
	ext, eng := sandbox(t)
	write(t, filepath.Join(filepath.Dir(ext), "rogue.ts"), "export const rogue = 1;\n")
	write(t, filepath.Join(ext, "src", "popup", "popup.ts"), "import { rogue } from \"../../../rogue\";\nconsole.log(rogue);\n")
	_, err := Build(ext, eng)
	if err == nil || !strings.Contains(err.Error(), "rogue.ts") {
		t.Fatalf("an import from outside gave %v", err)
	}
}

func TestZeroRuntimePackages(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(extRoot, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pkg map[string]any
	if err := json.Unmarshal(b, &pkg); err != nil {
		t.Fatal(err)
	}
	if deps, ok := pkg["dependencies"]; ok {
		t.Fatalf("runtime dependencies are declared: %v", deps)
	}
}

func TestStampFollowsTheEngineAndWhatOnlyTheExtensionBundles(t *testing.T) {
	ext, eng := sandbox(t)
	only := filepath.Join(eng, "src", "extonly.ts")
	write(t, only, `export const only = "a";`)
	probe := filepath.Join(ext, "src", "probe.ts")
	b, _ := os.ReadFile(probe)
	write(t, probe, "import { only } from \"../../engine/src/extonly\";\n"+string(b)+"Object.defineProperty(globalThis, \"__only\", { value: only });\n")
	before, err := Build(ext, eng)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(eng, "src", "engine.ts")
	b, _ = os.ReadFile(p)
	write(t, p, strings.Replace(string(b), `stamp = "dev"`, `stamp = "dex"`, 1))
	engineChanged, err := Build(ext, eng)
	if err != nil {
		t.Fatal(err)
	}
	if before.Stamp == engineChanged.Stamp {
		t.Fatal("an engine change left the extension stamp unchanged")
	}
	write(t, only, `export const only = "b";`)
	after, err := Build(ext, eng)
	if err != nil {
		t.Fatal(err)
	}
	if engineChanged.Stamp == after.Stamp {
		t.Fatal("a change to an engine source only the extension bundles left the stamp unchanged")
	}
}
