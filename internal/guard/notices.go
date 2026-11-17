package guard

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const (
	ruleNotice      = "notice"
	ruleNoticeDrift = "notice-drift"

	noticeFile     = "NOTICE"
	depNoticeFile  = "third_party/NOTICE-dependencies.txt"
	licenseDir     = "third_party/licenses/"
	sectionGo      = "[go]"
	sectionWeb     = "[dashboard]"
	envUpdateNotes = "APPLYSMITH_UPDATE_NOTICES"
)

var fontExts = map[string]bool{".woff": true, ".woff2": true, ".ttf": true, ".otf": true}

// checkNotice requires the NOTICE file, the dependency listing, and an entry
// plus licence text for every tracked font.
func checkNotice(t *Tree) ([]Finding, error) {
	var out []Finding
	for _, p := range []string{noticeFile, depNoticeFile} {
		if !t.Has(p) {
			out = append(out, Finding{Rule: ruleNotice, Path: p, Detail: "required file is missing"})
		}
	}
	var notice string
	if t.Has(noticeFile) {
		data, err := t.Read(noticeFile)
		if err != nil {
			return nil, err
		}
		notice = string(data)
	}
	licences := 0
	for _, p := range t.Paths {
		if strings.HasPrefix(p, licenseDir) {
			licences++
		}
	}
	for _, p := range t.Paths {
		name := strings.TrimSuffix(p, gzSuffix) // a font stored gzip-compressed is still a font
		if !fontExts[ext(name)] {
			continue
		}
		if !strings.Contains(notice, filepath.Base(name)) {
			out = append(out, Finding{Rule: ruleNotice, Path: p, Detail: "font is not listed by file name in NOTICE"})
		}
		if licences == 0 {
			out = append(out, Finding{Rule: ruleNotice, Path: p, Detail: "no licence text under " + licenseDir})
		}
	}
	return out, nil
}

// ---- licence classification ----

var copyleftMarkers = []string{
	"gnu general public license", "gnu affero general public license", "gnu lesser general public license",
	"gnu library general public license", "server side public license",
}

// classifyLicense maps a licence text to an SPDX identifier. Copyleft and
// unrecognised texts are errors.
func classifyLicense(text string) (string, error) {
	t := strings.Join(strings.Fields(strings.ToLower(text)), " ")
	for _, m := range copyleftMarkers {
		if strings.Contains(t, m) {
			return "", fmt.Errorf("copyleft licence")
		}
	}
	has := func(parts ...string) bool {
		for _, p := range parts {
			if !strings.Contains(t, p) {
				return false
			}
		}
		return true
	}
	switch {
	case has("apache license", "version 2.0"):
		return "Apache-2.0", nil
	case has("mozilla public license", "2.0"):
		return "MPL-2.0", nil
	case has("permission is hereby granted, free of charge", "the software is provided \"as is\""):
		return "MIT", nil
	case has("permission to use, copy, modify, and/or distribute this software for any purpose with or without fee"),
		has("permission to use, copy, modify, and distribute this software for any purpose with or without fee"):
		return "ISC", nil
	case has("redistribution and use in source and binary forms", "neither the name"):
		return "BSD-3-Clause", nil
	case has("redistribution and use in source and binary forms", "this list of conditions and the following disclaimer"):
		return "BSD-2-Clause", nil
	case has("free and unencumbered software released into the public domain"):
		return "Unlicense", nil
	}
	return "", fmt.Errorf("unrecognised licence text")
}

var licenseNames = []string{"LICENSE", "LICENSE.md", "LICENSE.txt", "LICENCE", "COPYING", "COPYING.md", "LICENSE-MIT", "LICENSE.mit"}

func moduleLicense(dir string) (string, error) {
	for _, n := range licenseNames {
		data, err := os.ReadFile(filepath.Join(dir, n))
		if err == nil {
			return classifyLicense(string(data))
		}
	}
	return "", errors.New("no licence file")
}

// goNoticeLines lists the third-party Go modules linked into the shipped
// binaries (cmd/ and app/), one "module version SPDX" line each.
func goNoticeLines(root string) ([]string, error) {
	cmd := exec.Command("go", "list", "-e", "-deps", "-f",
		`{{with .Module}}{{if not .Main}}{{.Path}}{{"\t"}}{{.Version}}{{"\t"}}{{.Dir}}{{end}}{{end}}`, "./cmd/...", "./app/...")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w", err)
	}
	seen := map[string]bool{}
	var lines []string
	var errs []error
	for _, l := range strings.Split(string(out), "\n") {
		cols := strings.Split(l, "\t")
		if len(cols) != 3 || seen[cols[0]] {
			continue
		}
		seen[cols[0]] = true
		if cols[2] == "" {
			errs = append(errs, fmt.Errorf("module %s is not downloaded", cols[0]))
			continue
		}
		spdx, err := moduleLicense(cols[2])
		if err != nil {
			errs = append(errs, fmt.Errorf("module %s: %w", cols[0], err))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s %s %s", cols[0], cols[1], spdx))
	}
	sort.Strings(lines)
	return lines, errors.Join(errs...)
}

// dashboardNoticeLines lists the runtime packages of web/package.json and
// their runtime closure from web/node_modules.
func dashboardNoticeLines(root string) ([]string, error) {
	type pkgJSON struct {
		Name         string            `json:"name"`
		Version      string            `json:"version"`
		License      string            `json:"license"`
		Dependencies map[string]string `json:"dependencies"`
	}
	load := func(file string) (pkgJSON, error) {
		var p pkgJSON
		data, err := os.ReadFile(file)
		if err != nil {
			return p, err
		}
		return p, json.Unmarshal(data, &p)
	}
	top, err := load(filepath.Join(root, "web", "package.json"))
	if err != nil {
		return nil, err
	}
	var queue []string
	for n := range top.Dependencies {
		queue = append(queue, n)
	}
	seen := map[string]bool{}
	var lines []string
	var errs []error
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		p, err := load(filepath.Join(root, "web", "node_modules", filepath.FromSlash(name), "package.json"))
		if err != nil {
			errs = append(errs, fmt.Errorf("package %s is not installed (run bun install): %w", name, err))
			continue
		}
		upper := strings.ToUpper(p.License)
		if p.License == "" || strings.Contains(upper, "GPL") || strings.Contains(upper, "SSPL") || strings.Contains(upper, "UNLICENSED") {
			errs = append(errs, fmt.Errorf("package %s: unknown or copyleft licence", name))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s %s %s", name, p.Version, p.License))
		for dep := range p.Dependencies {
			queue = append(queue, dep)
		}
	}
	sort.Strings(lines)
	return lines, errors.Join(errs...)
}

const depNoticeHeader = `# Third-party packages linked into the shipped app, with SPDX licence
# identifiers. Generated by "make notices"; do not edit by hand.
`

func renderNotices(goLines, webLines []string) string {
	var b strings.Builder
	b.WriteString(depNoticeHeader)
	b.WriteString("\n" + sectionGo + "\n" + strings.Join(goLines, "\n") + "\n")
	b.WriteString("\n" + sectionWeb + "\n" + strings.Join(webLines, "\n") + "\n")
	return b.String()
}

// noticeSection returns the lines of one section of a rendered listing.
func noticeSection(content, name string) []string {
	var out []string
	in := false
	for _, l := range strings.Split(content, "\n") {
		switch {
		case strings.HasPrefix(l, "["):
			in = l == name
		case in && l != "":
			out = append(out, l)
		}
	}
	return out
}

// UpdateNotices regenerates third_party/NOTICE-dependencies.txt in root. It
// fails on an unknown or copyleft licence in the shipped set.
func UpdateNotices(root string) error {
	goLines, err := goNoticeLines(root)
	if err != nil {
		return err
	}
	webLines, err := dashboardNoticeLines(root)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, filepath.FromSlash(depNoticeFile)), []byte(renderNotices(goLines, webLines)), 0o644)
}

func sameLines(a, b []string) bool { return strings.Join(a, "\n") == strings.Join(b, "\n") }

// checkNoticeDrift compares the committed listing with a fresh generation. The
// dashboard section needs web/node_modules; without it the rule is skipped in
// development and fails in release mode.
func checkNoticeDrift(t *Tree) ([]Finding, error) {
	if !t.Has(depNoticeFile) {
		return nil, nil // reported by the notice rule
	}
	data, err := t.Read(depNoticeFile)
	if err != nil {
		return nil, err
	}
	var out []Finding
	goLines, err := goNoticeLines(t.Root)
	if err != nil {
		return nil, err
	}
	if !sameLines(goLines, noticeSection(string(data), sectionGo)) {
		out = append(out, Finding{Rule: ruleNoticeDrift, Path: depNoticeFile, Detail: "[go] section differs from a fresh generation; run make notices"})
	}
	if _, statErr := os.Stat(filepath.Join(t.Root, "web", "node_modules")); statErr != nil {
		if t.Release {
			return append(out, Finding{Rule: ruleNoticeDrift, Path: depNoticeFile, Detail: "web/node_modules is required for release to check the [dashboard] section"}), nil
		}
		return out, skipPartial(out, "web/node_modules is absent; the [dashboard] section was NOT compared")
	}
	webLines, err := dashboardNoticeLines(t.Root)
	if err != nil {
		return nil, err
	}
	if !sameLines(webLines, noticeSection(string(data), sectionWeb)) {
		out = append(out, Finding{Rule: ruleNoticeDrift, Path: depNoticeFile, Detail: "[dashboard] section differs from a fresh generation; run make notices"})
	}
	return out, nil
}

// skipPartial keeps findings already made: a partial check that found a
// problem fails, and only a clean partial check reports as skipped.
func skipPartial(found []Finding, reason string) error {
	if len(found) > 0 {
		return nil
	}
	return Skipped(reason)
}
