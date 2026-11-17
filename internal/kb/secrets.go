package kb

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"applysmith/internal/lazyregexp"
)

// ErrSecret marks text that holds a secret. Its message names a path and line
// and never the value.
var ErrSecret = errors.New("kb: text contains a secret")

// SecretFinding is one rejected line.
type SecretFinding struct {
	Line int
	Kind string
}

// SecretError is returned when a scan rejects text.
type SecretError struct {
	Path     string
	Findings []SecretFinding
}

func (e *SecretError) Error() string {
	f := e.Findings[0]
	msg := fmt.Sprintf("kb: %s:%d looks like %s; edit the line and save again", e.Path, f.Line, f.Kind)
	if n := len(e.Findings); n > 1 {
		msg += fmt.Sprintf(" (%d more lines)", n-1)
	}
	return msg
}

func (e *SecretError) Is(target error) bool { return target == ErrSecret }

var secretShapes = []struct {
	kind string
	re   *lazyregexp.Regexp
}{
	{"a private key block", lazyregexp.New(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY`)},
	{"an access token", lazyregexp.New(`(?:^|[^A-Za-z0-9])(?:sk-[A-Za-z0-9_-]{20,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|(?:sk|pk|rk)_(?:live|test)_[A-Za-z0-9]{16,}|hf_[A-Za-z0-9]{20,}|npm_[A-Za-z0-9]{20,}|gsk_[A-Za-z0-9]{20,}|xai-[A-Za-z0-9]{20,}|pplx-[A-Za-z0-9]{20,}|tvly-[A-Za-z0-9-]{16,})`)},
	{"a cloud access key", lazyregexp.New(`\b(?:AKIA[0-9A-Z]{16}|ASIA[0-9A-Z]{16}|AIza[0-9A-Za-z_-]{30,})`)},
	{"a signed token", lazyregexp.New(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)},
	{"a bot token", lazyregexp.New(`\b[0-9]{8,10}:[A-Za-z0-9_-]{35}\b`)},
	{"a bearer token", lazyregexp.New(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{20,}`)},
	{"a connection string with a password", lazyregexp.New(`(?i)\b[a-z][a-z0-9+.-]*://[^\s:/@]+:[^\s@/]{3,}@[^\s/]+`)},
}

// secretName matches a name such as password, secret, token or api_key.
const secretName = `[\w.-]*(?:password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key)[\w.-]*`

// assignRE finds a name assigned a value on one line.
var assignRE = lazyregexp.New(`(?i)\b` + secretName + `["']?\s*[:=]\s*["']?([^\s"',;]{12,})`)

// nameOnlyRE finds a name whose value continues on the next line, as in YAML
// (`password:` or `password: |`).
var nameOnlyRE = lazyregexp.New(`(?i)^\s*(?:-\s+)?` + secretName + `["']?\s*:\s*(?:[|>][+-]?)?\s*$`)

// ScanSecrets reports the lines of text that hold a secret.
func ScanSecrets(text string) []SecretFinding {
	var out []SecretFinding
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if kind := secretKind(line); kind != "" {
			out = append(out, SecretFinding{Line: i + 1, Kind: kind})
			continue
		}
		if !nameOnlyRE.MatchString(line) {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			next := strings.TrimSpace(lines[j])
			if next == "" {
				continue
			}
			v := strings.Trim(strings.TrimPrefix(next, "- "), `"'`)
			if !strings.ContainsAny(v, " \t") && secretValue(v) {
				out = append(out, SecretFinding{Line: j + 1, Kind: "a high-entropy value assigned to a secret name"})
			}
			break
		}
	}
	return out
}

func secretKind(line string) string {
	for _, s := range secretShapes {
		if s.re.MatchString(line) {
			return s.kind
		}
	}
	for _, m := range assignRE.FindAllStringSubmatch(line, -1) {
		if secretValue(m[1]) {
			return "a high-entropy value assigned to a secret name"
		}
	}
	return ""
}

func entropy(v string) float64 {
	counts := map[rune]int{}
	n := 0
	for _, r := range v {
		counts[r]++
		n++
	}
	var h float64
	for _, c := range counts {
		p := float64(c) / float64(n)
		h -= p * math.Log2(p)
	}
	return h
}

// secretValue tells a credential from a word or an identifier. A value that
// mixes letters and digits needs a wide spread of characters; a value of
// letters only must be long and either unmixed-case with a very wide spread or
// mixed-case with a case change every few letters, which separates it from
// ordinary words and camelCase identifiers.
func secretValue(v string) bool {
	if len(v) < 12 {
		return false
	}
	var digit, letter, other, upper, lower bool
	transitions := 0
	var prev rune
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case r >= 'a' && r <= 'z':
			letter, lower = true, true
		case r >= 'A' && r <= 'Z':
			letter, upper = true, true
			if prev >= 'a' && prev <= 'z' {
				transitions++
			}
		default:
			other = true
		}
		prev = r
	}
	h := entropy(v)
	switch {
	case digit && letter:
		return h >= 3.2
	case letter && !digit && !other && len(v) >= 16:
		if upper && lower {
			return h >= 3.6 && transitions*4 >= len(v)
		}
		return h >= 3.9
	}
	return false
}

// CheckSecrets returns a *SecretError naming path and line when text holds a
// secret, else nil.
func CheckSecrets(path, text string) error {
	if f := ScanSecrets(text); len(f) > 0 {
		return &SecretError{Path: path, Findings: f}
	}
	return nil
}
