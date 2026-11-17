package guard

import (
	"regexp"
	"strings"
)

const ruleLoopbackListener = "loopback-listener"

var (
	// loopbackListenRE is a net.Listen call whose address is a loopback
	// literal. Anything else must go through the helper below.
	loopbackListenRE = regexp.MustCompile(`net\.Listen\(\s*"[a-z0-9]+",\s*"(?:127\.0\.0\.1|\[::1\]):`)

	// unixListenRE is a net.Listen call on a Unix-domain socket, a file that
	// no network reaches. Only a test may open one, to stand in for a local
	// service the product dials, such as the computer-use service.
	unixListenRE = regexp.MustCompile(`net\.Listen\(\s*"unix",`)

	// anyListenRE is every net.Listen call.
	anyListenRE = regexp.MustCompile(`net\.Listen\(`)

	// otherListenRE are the other ways to open a server socket. The product
	// has no use for them: remote access arrives through the tunnel child,
	// which connects to a loopback listener.
	otherListenRE = regexp.MustCompile(`\b(?:ListenAndServe|ListenAndServeTLS|ServeTLS|ListenConfig|ListenTCP|ListenUDP|ListenPacket|ListenIP|ListenUnix)\b|\btls\.Listen\(|\bhttp\.Serve\(`)

	// stringLitRE is a one-line string literal. Its contents are blanked
	// before matching, so a test that names a banned call as text (a source
	// scan) is not read as a call.
	stringLitRE = regexp.MustCompile("\"(?:[^\"\\\\]|\\\\.)*\"|`[^`]*`")
)

// listenHelperPath is the one file that binds from a configured host. It
// validates the host to a loopback address before it listens.
const listenHelperPath = "internal/api/listen.go"

// checkLoopbackListeners keeps every server socket on a loopback address, so
// that no credential-bearing listener is reachable from the network. The rule
// reads Go source outside the guard package, tests included.
func checkLoopbackListeners(t *Tree) ([]Finding, error) {
	var out []Finding
	keep := func(p string) bool { return ext(p) == ".go" && !strings.HasPrefix(p, "internal/guard/") }
	err := t.eachText(keep, func(f textFile) {
		for i, line := range f.Lines {
			code := line
			if c := strings.Index(code, "//"); c >= 0 && !strings.Contains(code[:c], `"`) {
				code = code[:c]
			}
			calls := stringLitRE.ReplaceAllStringFunc(code, func(lit string) string { return strings.Repeat(" ", len(lit)) })
			if loc := otherListenRE.FindStringIndex(calls); loc != nil {
				out = append(out, Finding{Rule: ruleLoopbackListener, Path: f.Path, Line: i + 1, Col: runeCol(line, loc[0]),
					Detail: "opens a server socket outside net.Listen on a loopback literal; remote access goes through the tunnel's loopback listener"})
				continue
			}
			unixInTest := strings.HasSuffix(f.Path, "_test.go") && unixListenRE.MatchString(code)
			if loc := anyListenRE.FindStringIndex(calls); loc != nil && !loopbackListenRE.MatchString(code) && !unixInTest && f.Path != listenHelperPath {
				out = append(out, Finding{Rule: ruleLoopbackListener, Path: f.Path, Line: i + 1, Col: runeCol(line, loc[0]),
					Detail: "net.Listen on an address that is not a 127.0.0.1 or [::1] literal; bind through " + listenHelperPath})
			}
		}
	})
	return out, err
}
