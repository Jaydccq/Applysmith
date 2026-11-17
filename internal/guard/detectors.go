package guard

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	ruleEmail    = "email"
	rulePhone    = "phone"
	ruleHomePath = "home-path"
	ruleSecret   = "secret"
	ruleLanguage = "english-only"
)

type hit struct {
	off    int
	text   string
	detail string // overrides the detector's detail when set
}

// lineDetector applies a per-line matcher to every text file it does not skip.
type lineDetector struct {
	rule   string
	detail string
	skip   func(path string) bool
	match  func(line string) []hit
}

func (d lineDetector) check(t *Tree) ([]Finding, error) {
	var out []Finding
	err := t.eachText(func(p string) bool { return d.skip == nil || !d.skip(p) }, func(f textFile) {
		for i, line := range f.Lines {
			for _, h := range d.match(line) {
				detail := d.detail
				if h.detail != "" {
					detail = h.detail
				}
				out = append(out, Finding{
					Rule: d.rule, Path: f.Path, Line: i + 1, Col: runeCol(line, h.off),
					Detail: detail, Excerpt: mask(h.text), Value: h.text,
				})
			}
		}
	})
	return out, err
}

// lockFile reports generated lock files, which are exempt from the email and
// phone detectors (and from nothing else).
func lockFile(p string) bool {
	return p == "go.sum" || strings.HasSuffix(p, "/go.sum") ||
		strings.HasSuffix(p, "bun.lock") || strings.HasSuffix(p, "bun.lockb")
}

// ---- email ----

var emailRE = regexp.MustCompile(`[A-Za-z0-9._%+-]+@([A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,})`)

// versionDomainRE matches what follows the @ of a pinned module reference.
var versionDomainRE = regexp.MustCompile(`^v\d+(\.|$)`)

var reservedSuffixes = []string{".test", ".example", ".invalid", ".localhost"}

func reservedDomain(d string) bool {
	d = strings.ToLower(d)
	for _, base := range []string{"example.com", "example.org", "example.net"} {
		if d == base || strings.HasSuffix(d, "."+base) {
			return true
		}
	}
	for _, s := range reservedSuffixes {
		if strings.HasSuffix(d, s) {
			return true
		}
	}
	return false
}

func emailHits(line string) []hit {
	var out []hit
	for _, m := range emailRE.FindAllStringSubmatchIndex(line, -1) {
		domain := line[m[2]:m[3]]
		if versionDomainRE.MatchString(domain) || reservedDomain(domain) {
			continue
		}
		out = append(out, hit{off: m[0], text: line[m[0]:m[1]]})
	}
	return out
}

var emailDetector = lineDetector{rule: ruleEmail, detail: "email address outside reserved domains", skip: lockFile, match: emailHits}

func checkEmail(t *Tree) ([]Finding, error) { return emailDetector.check(t) }

// ---- phone ----

var (
	// Three-three-four groups with at least one separator or parentheses.
	nanpRE = regexp.MustCompile(`(?:\+?1[\s.-]?)?(?:\(\d{3}\)[\s.-]?|\d{3}[\s.-])\d{3}[\s.-]\d{4}`)
	// A plus sign, then 8 to 15 digits with optional separators.
	intlRE = regexp.MustCompile(`\+\d(?:[\s.()-]?\d){7,14}`)
)

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func isWordByte(b byte) bool {
	return isDigit(b) || b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func digitsOf(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if isDigit(s[i]) {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// reservedPhone reports the fictional range: exchange 555, line 0100-0199.
func reservedPhone(digits string) bool {
	if len(digits) < 7 {
		return false
	}
	local := digits[len(digits)-7:]
	return local[:3] == "555" && local[3:5] == "01"
}

func phoneHits(line string) []hit {
	var out []hit
	for _, re := range []*regexp.Regexp{nanpRE, intlRE} {
		for _, m := range re.FindAllStringIndex(line, -1) {
			s, e := m[0], m[1]
			if s > 0 && isWordByte(line[s-1]) {
				continue
			}
			if e < len(line) && isDigit(line[e]) {
				continue
			}
			text := line[s:e]
			d := digitsOf(text)
			// The fictional range is only recognised for +1 / NANP numbers.
			nanp := re == nanpRE || strings.HasPrefix(d, "1")
			if nanp && reservedPhone(d) {
				continue
			}
			if overlaps(out, s, e) {
				continue
			}
			out = append(out, hit{off: s, text: text})
		}
	}
	return out
}

func overlaps(hs []hit, s, e int) bool {
	for _, h := range hs {
		if s < h.off+len(h.text) && h.off < e {
			return true
		}
	}
	return false
}

var phoneDetector = lineDetector{rule: rulePhone, detail: "telephone number outside the reserved fictional range", skip: lockFile, match: phoneHits}

func checkPhone(t *Tree) ([]Finding, error) { return phoneDetector.check(t) }

// ---- absolute home paths ----

// The prefixes are assembled so this file does not match its own detector.
var homeRE = regexp.MustCompile(`(?i)(?:/Us` + `ers/|/ho` + `me/|[A-Z]:[\\/]Us` + `ers[\\/])([^/\\\s"'` + "`" + `,;)\]}]+)`)

var homePlaceholders = map[string]bool{
	"alex": true, "example": true, "user": true, "username": true, "you": true, "me": true, "name": true,
}

func homeHits(line string) []hit {
	var out []hit
	for _, m := range homeRE.FindAllStringSubmatchIndex(line, -1) {
		name := strings.ToLower(line[m[2]:m[3]])
		if homePlaceholders[name] || strings.ContainsAny(name[:1], "$<{%*~") {
			continue
		}
		out = append(out, hit{off: m[0], text: line[m[0]:m[1]]})
	}
	return out
}

var homeDetector = lineDetector{rule: ruleHomePath, detail: "absolute path under a user home directory", match: homeHits}

func checkHomePath(t *Tree) ([]Finding, error) { return homeDetector.check(t) }

// ---- secrets ----

var (
	privateKeyRE = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----`)
	// Well-known provider prefixes followed by at least 20 key characters.
	providerRE = regexp.MustCompile(`\b(?:` + strings.Join([]string{
		`sk-ant-[A-Za-z0-9_-]{20,}`,
		`sk-(?:proj-)?[A-Za-z0-9_-]{20,}`,
		`gh[pousr]_[A-Za-z0-9]{20,}`,
		`github_pat_[A-Za-z0-9_]{20,}`,
		`glpat-[A-Za-z0-9_-]{20,}`,
		`xox[abprs]-[A-Za-z0-9-]{20,}`,
		`(?:sk|pk|rk)_live_[A-Za-z0-9]{20,}`,
		`AIza[A-Za-z0-9_-]{35}`,
		`AKIA[A-Z0-9]{16}`,
		`hf_[A-Za-z0-9]{20,}`,
		`npm_[A-Za-z0-9]{20,}`,
		`SG\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}`,
		`eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`,
	}, "|") + `)`)
)

func secretHits(line string) []hit {
	var out []hit
	for _, m := range privateKeyRE.FindAllStringIndex(line, -1) {
		out = append(out, hit{off: m[0], text: line[m[0]:m[1]]})
	}
	for _, m := range providerRE.FindAllStringIndex(line, -1) {
		v := line[m[0]:m[1]]
		if strings.Contains(v, "FAKE") || strings.Contains(v, "EXAMPLE") {
			continue
		}
		out = append(out, hit{off: m[0], text: v})
	}
	return out
}

var secretDetector = lineDetector{rule: ruleSecret, detail: "private key block or provider credential shape", match: secretHits}

func checkSecrets(t *Tree) ([]Finding, error) { return secretDetector.check(t) }

// ---- English-only text ----

var foreignScripts = []*unicode.RangeTable{
	unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul, unicode.Cyrillic,
	unicode.Arabic, unicode.Hebrew, unicode.Devanagari, unicode.Thai,
}

func languageHits(line string) []hit {
	for off, r := range line {
		if r > unicode.MaxASCII && unicode.IsOneOf(foreignScripts, r) {
			return []hit{{off: off, text: string(r)}}
		}
	}
	return nil
}

var languageDetector = lineDetector{rule: ruleLanguage, detail: "character from a non-English script", match: languageHits}

func checkLanguage(t *Tree) ([]Finding, error) { return languageDetector.check(t) }

// GitleaksConfig retains upstream rules and the specification's explicit fake
// markers. SuccessFactors is an ATS name, not the similarly shaped client ID.
const GitleaksConfig = `
[extend]
useDefault = true
[[allowlists]]
description = "Explicit synthetic credential markers"
regexTarget = "secret"
regexes = ['FAKE', 'EXAMPLE']
[[rules]]
id = "linkedin-client-id"
[[rules.allowlists]]
description = "Public ATS name in frame documentation"
regexTarget = "secret"
regexes = ['^SuccessFactors$']
`

// checkGitleaks scans exactly the guard's file set. Ignored local secrets,
// generated output and parent repositories never enter the source snapshot.
func checkGitleaks(t *Tree) ([]Finding, error) {
	binary, err := exec.LookPath("gitleaks")
	if err != nil {
		if t.Release {
			return nil, fmt.Errorf("gitleaks is required for release but is not on PATH")
		}
		return nil, Skipped("gitleaks is not on PATH; this scan did NOT run")
	}
	scratch, err := os.MkdirTemp("", "applysmith-gitleaks-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	snapshot := filepath.Join(scratch, "tree")
	if err := os.Mkdir(snapshot, 0o700); err != nil {
		return nil, err
	}
	for _, path := range t.Paths {
		data, err := t.Read(path)
		if err != nil {
			return nil, err
		}
		dest := filepath.Join(snapshot, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dest, data, 0o600); err != nil {
			return nil, err
		}
	}
	config, report := filepath.Join(scratch, "config.toml"), filepath.Join(scratch, "report.json")
	if err := os.WriteFile(config, []byte(GitleaksConfig), 0o600); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "dir", snapshot, "--config", config, "--redact", "--ignore-gitleaks-allow", "--gitleaks-ignore-path", scratch, "--no-banner", "--report-format", "json", "--report-path", report)
	cmd.Dir = scratch
	runErr := cmd.Run()
	data, err := os.ReadFile(report)
	if err != nil {
		if runErr != nil {
			return nil, fmt.Errorf("gitleaks did not produce a scan report: %w", runErr)
		}
		return nil, fmt.Errorf("gitleaks did not produce a scan report")
	}
	var rows []struct {
		RuleID, File string
		StartLine    int
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("gitleaks produced an invalid scan report")
	}
	var found []Finding
	for _, row := range rows {
		file := row.File
		if filepath.IsAbs(file) {
			file, err = filepath.Rel(snapshot, file)
			if err != nil {
				return nil, err
			}
		}
		file = filepath.ToSlash(file)
		if !t.Has(file) {
			return nil, fmt.Errorf("gitleaks reported a file outside the source snapshot")
		}
		found = append(found, Finding{Rule: ruleGitleaks, Path: file, Line: row.StartLine, Detail: "external secret detector: " + row.RuleID})
	}
	if runErr != nil && len(found) == 0 {
		return nil, fmt.Errorf("gitleaks did not complete successfully: %w", runErr)
	}
	return found, nil
}
