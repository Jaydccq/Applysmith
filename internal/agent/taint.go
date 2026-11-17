// Clean-room: docs/provenance.md#CR-AGENT-TAINT

package agent

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// The fence that delimits a tool result. The system prompt declares everything
// between the markers to be untrusted data. The markers are internal framing:
// the disclosure gate must not let them into a final answer.
const (
	FenceOpen  = "<<untrusted-tool-result"
	FenceClose = "<</untrusted-tool-result>>"
)

// fenceMarker finds text that imitates a marker, whatever its case or spacing,
// so a result cannot close its own fence.
var fenceMarker = lazyregexp.New(`(?i)<<\s*/?\s*untrusted-tool-result`)

// fenceText wraps a tool's output in the fence. Text inside that looks like a
// marker is defanged first. inline keeps the block on one line, for a result
// folded into a summary line.
func fenceText(tool, text string, inline bool) string {
	text = fenceMarker.ReplaceAllString(text, "<(untrusted-tool-result")
	sep := "\n"
	if inline {
		sep = " "
	}
	return FenceOpen + ` tool="` + strings.Map(safeToolRune, tool) + `">>` + sep + text + sep + FenceClose
}

func safeToolRune(r rune) rune {
	if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
		return r
	}
	return '_'
}
