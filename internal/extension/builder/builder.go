// Package builder produces the unpacked browser extension: it bundles the
// TypeScript entries with the esbuild Go API, stamps the result with a hash of
// the extension and engine sources, and checks the footprint and egress rules.
package builder

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"applysmith/internal/engine/bundler"
	"applysmith/internal/lazyregexp"

	"github.com/evanw/esbuild/pkg/api"
)

// Footprint limits of the build.
const (
	MaxLoader    = 16 << 10
	MaxUnpacked  = 4 << 20
	MaxSourceLOC = 1500
	// StampLen is the length of the build stamp in hexadecimal characters.
	StampLen = 16
)

// apiClient is the only source file that may make a request.
const apiClient = "src/sw/api.ts"

// mainWorldDir holds the helper that runs in the page's own world. It wraps the
// page's own network functions, so it names them; it must not make a request
// of its own.
const mainWorldDir = "src/mainworld/"

type entry struct {
	src    string
	out    string
	format api.Format
}

var entries = []entry{
	{"src/loader.ts", "loader.js", api.FormatIIFE},
	{"src/sw/main.ts", "sw.js", api.FormatESModule},
	{"src/popup/popup.ts", "popup.js", api.FormatIIFE},
	{"src/panel/main.ts", "panel.js", api.FormatIIFE},
	{"src/bridge.ts", "bridge.js", api.FormatIIFE},
	{"src/probe.ts", "probe.js", api.FormatIIFE},
	{"src/advance.ts", "advance.js", api.FormatIIFE},
	{"src/frame.ts", "frame.js", api.FormatIIFE},
	{"src/mainworld/hook.ts", "hook.js", api.FormatIIFE},
}

// iconSizes are the toolbar icon sizes Chrome asks for.
var iconSizes = []string{"16", "32", "48", "128"}

// staticFiles are copied unchanged.
var staticFiles = append([]string{"popup.html"}, iconFiles()...)

func iconFiles() []string {
	var out []string
	for _, s := range iconSizes {
		out = append(out, "icons/icon-"+s+".png")
	}
	return out
}

// Output is the unpacked extension.
type Output struct {
	Files map[string][]byte
	Stamp string
}

// Build bundles the extension in extDir against the engine in engineDir.
func Build(extDir, engineDir string) (*Output, error) {
	extAbs, err := filepath.Abs(extDir)
	if err != nil {
		return nil, err
	}
	engAbs, err := filepath.Abs(engineDir)
	if err != nil {
		return nil, err
	}
	sources, err := readSources(extAbs)
	if err != nil {
		return nil, err
	}
	if err := checkSources(sources); err != nil {
		return nil, err
	}
	engineSources := map[string][]byte{}
	if err := readTree(engAbs, "src", "engine/", engineSources); err != nil {
		return nil, err
	}
	engineJS, err := bundler.Build(engAbs)
	if err != nil {
		return nil, err
	}
	st := sourceStamp(sources, engineSources)
	out := &Output{Files: map[string][]byte{"engine.js": engineJS}, Stamp: st}
	for _, e := range entries {
		body, err := bundle(extAbs, engAbs, e, st)
		if err != nil {
			return nil, err
		}
		out.Files[e.out] = body
	}
	for _, name := range staticFiles {
		out.Files[name] = sources[name]
	}
	manifest, err := stampManifest(sources["manifest.json"], st)
	if err != nil {
		return nil, err
	}
	out.Files["manifest.json"] = manifest
	if err := CheckManifest(manifest); err != nil {
		return nil, err
	}
	if n := len(out.Files["loader.js"]); n > MaxLoader {
		return nil, fmt.Errorf("extension: loader.js is %d bytes, budget is %d bytes (16 KiB)", n, MaxLoader)
	}
	total := 0
	for _, b := range out.Files {
		total += len(b)
	}
	if total > MaxUnpacked {
		return nil, fmt.Errorf("extension: unpacked output is %d bytes, budget is %d bytes (4 MiB)", total, MaxUnpacked)
	}
	return out, nil
}

// readSources returns the extension files the stamp covers, keyed by their
// path relative to the extension directory, in slash form.
func readSources(root string) (map[string][]byte, error) {
	files := map[string][]byte{}
	if err := readTree(root, "src", "", files); err != nil {
		return nil, err
	}
	for _, name := range append([]string{"manifest.json", "tsconfig.json"}, staticFiles...) {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return nil, err
		}
		files[name] = b
	}
	return files, nil
}

// readTree adds every file under root/dir to files, keyed by prefix and its
// slash-form path relative to root.
func readTree(root, dir, prefix string, files map[string][]byte) error {
	return filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		files[prefix+filepath.ToSlash(rel)] = b
		return nil
	})
}

// sourceStamp hashes the extension sources and the whole engine source tree:
// the extension's bundles take engine modules that engine.js may not contain.
func sourceStamp(sources, engineSources map[string][]byte) string {
	h := sha256.New()
	for _, set := range []map[string][]byte{sources, engineSources} {
		names := make([]string, 0, len(set))
		for n := range set {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(h, "\x00%s\x00%d\x00", n, len(set[n]))
			h.Write(set[n])
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:StampLen]
}

var network = lazyregexp.New(`\b(fetch\s*\(|XMLHttpRequest|WebSocket|sendBeacon|EventSource)`)

// ownRequest is what even a wrapper of the page's functions must never contain.
var ownRequest = lazyregexp.New(`\b(fetch\s*\(|new\s+XMLHttpRequest|WebSocket|sendBeacon|EventSource)`)

// checkSources enforces the source rules: the line limit and the single API
// client as the only place that touches the network.
func checkSources(sources map[string][]byte) error {
	var errs []error
	for name, b := range sources {
		if !strings.HasSuffix(name, ".ts") {
			continue
		}
		if n := strings.Count(string(b), "\n"); n > MaxSourceLOC {
			errs = append(errs, fmt.Errorf("extension: %s has %d lines, limit is %d", name, n, MaxSourceLOC))
		}
		switch {
		case name == apiClient:
		case strings.HasPrefix(name, mainWorldDir):
			if ownRequest.Match(b) {
				errs = append(errs, fmt.Errorf("extension: %s makes a request of its own; the page-world helper may only wrap the page's functions", name))
			}
		case network.Match(b):
			errs = append(errs, fmt.Errorf("extension: %s reaches the network outside %s", name, apiClient))
		}
	}
	sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
	return errors.Join(errs...)
}

func bundle(extAbs, engAbs string, e entry, st string) ([]byte, error) {
	res := api.Build(api.BuildOptions{
		AbsWorkingDir: extAbs,
		EntryPoints:   []string{filepath.Join(extAbs, filepath.FromSlash(e.src))},
		Bundle:        true,
		Format:        e.format,
		Platform:      api.PlatformBrowser,
		Engines:       []api.Engine{{Name: api.EngineChrome, Version: "120"}},
		Define:        map[string]string{"__BUILD_STAMP__": fmt.Sprintf("%q", st)},
		LegalComments: api.LegalCommentsNone,
		Sourcemap:     api.SourceMapNone,
		Metafile:      true,
		LogLevel:      api.LogLevelSilent,
		Write:         false,
	})
	if len(res.Errors) > 0 {
		msgs := api.FormatMessages(res.Errors, api.FormatMessagesOptions{Kind: api.ErrorMessage})
		errs := make([]error, len(msgs))
		for i, m := range msgs {
			errs[i] = errors.New(m)
		}
		return nil, fmt.Errorf("bundle %s: %w", e.src, errors.Join(errs...))
	}
	if len(res.OutputFiles) != 1 {
		return nil, fmt.Errorf("bundle %s: %d output files, want 1", e.src, len(res.OutputFiles))
	}
	if err := checkInputs(res.Metafile, extAbs, engAbs); err != nil {
		return nil, fmt.Errorf("bundle %s: %w", e.src, err)
	}
	return res.OutputFiles[0].Contents, nil
}

// checkInputs fails when the bundle took a file from outside the extension's
// and the engine's source trees.
func checkInputs(metafile, extAbs, engAbs string) error {
	var meta struct {
		Inputs map[string]json.RawMessage `json:"inputs"`
	}
	if err := json.Unmarshal([]byte(metafile), &meta); err != nil {
		return err
	}
	var bad []string
	for in := range meta.Inputs {
		abs := in
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(extAbs, filepath.FromSlash(in))
		}
		abs = filepath.Clean(abs)
		if !within(abs, filepath.Join(extAbs, "src")) && !within(abs, filepath.Join(engAbs, "src")) {
			bad = append(bad, in)
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		return fmt.Errorf("runtime bundle imports outside extension/src and engine/src: %s", strings.Join(bad, ", "))
	}
	return nil
}

func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// stampManifest gives each build its own manifest version and keeps the
// version a person reads in version_name (<version>+<stamp>). Across a browser
// restart Chrome keeps running the service worker it registered while the
// manifest version is unchanged, so a re-export into the same folder would
// still run the old worker.
func stampManifest(src []byte, st string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(src, &m); err != nil {
		return nil, fmt.Errorf("manifest.json: %w", err)
	}
	version, _ := m["version"].(string)
	if version == "" {
		return nil, errors.New("manifest.json: version is missing")
	}
	built, err := BuildVersion(version, st)
	if err != nil {
		return nil, fmt.Errorf("manifest.json: %w", err)
	}
	m["version"] = built
	m["version_name"] = version + "+" + st
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// BuildVersion is the manifest version of the build with stamp st: the source
// version, at most three numbers, followed by the stamp's first four
// hexadecimal digits as a fourth number. Chrome takes up to four
// dot-separated integers from 0 to 65535, without leading zeros.
func BuildVersion(version, st string) (string, error) {
	parts := strings.Split(version, ".")
	if len(parts) > 3 {
		return "", fmt.Errorf("version %q leaves no room for the build number: use at most three numbers", version)
	}
	for _, p := range parts {
		if n, err := strconv.ParseUint(p, 10, 16); err != nil || strconv.FormatUint(n, 10) != p {
			return "", fmt.Errorf("version %q: each part is a number from 0 to 65535 without leading zeros", version)
		}
	}
	if len(st) < 4 {
		return "", fmt.Errorf("stamp %q is too short for a build number", st)
	}
	n, err := strconv.ParseUint(st[:4], 16, 16)
	if err != nil {
		return "", fmt.Errorf("stamp %q is not hexadecimal", st)
	}
	return version + "." + strconv.FormatUint(n, 10), nil
}
