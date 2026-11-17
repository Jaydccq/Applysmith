package apply

import (
	"strconv"
	"strings"

	"applysmith/internal/browser"
	"applysmith/internal/lazyregexp"
)

// Ashby answers its application write with HTTP 200 whether it took the
// application or bounced it: success returns a FormSubmitSuccess object and a
// bounce returns the form again (FormRender) with the missing entries in
// errorMessages. The status alone therefore proves nothing.
const (
	ashbySuccessType = "FormSubmitSuccess"
	ashbyBounceType  = "FormRender"
)

var (
	ashbyMessagesRE = lazyregexp.New(`"errorMessages"\s*:\s*\[`)
	ashbyStringRE   = lazyregexp.New(`^\s*,?\s*("(?:[^"\\]|\\.)*")`)
)

// ashbyAccepted reports whether an Ashby write answered with the success type.
func ashbyAccepted(rs []browser.Response) bool {
	for _, r := range rs {
		if isWrite(r.Method) && !ignoredWrite(r.URL) && r.Status >= 200 && r.Status < 300 && strings.Contains(r.Body, ashbySuccessType) {
			return true
		}
	}
	return false
}

// ashbyBounce returns the write that came back as the form again, if any.
func ashbyBounce(rs []browser.Response) (browser.Response, bool) {
	for _, r := range rs {
		if isWrite(r.Method) && !ignoredWrite(r.URL) && r.Status >= 200 && r.Status < 300 &&
			strings.Contains(r.Body, ashbyBounceType) && !strings.Contains(r.Body, ashbySuccessType) {
			return r, true
		}
	}
	return browser.Response{}, false
}

// ashbyMissing lists the questions a bounced form names as missing. The body
// is cut at a fixed length, so the array is read string by string up to the
// cut and no closing bracket is needed.
func ashbyMissing(body string) []string {
	loc := ashbyMessagesRE.FindStringIndex(body)
	if loc == nil {
		return nil
	}
	rest := body[loc[1]:]
	const prefix = "Missing entry for required field:"
	var out []string
	for {
		m := ashbyStringRE.FindStringSubmatchIndex(rest)
		if m == nil {
			return out
		}
		msg, err := strconv.Unquote(rest[m[2]:m[3]])
		rest = rest[m[1]:]
		if err != nil || !strings.HasPrefix(msg, prefix) {
			continue
		}
		if q := strings.TrimSpace(strings.TrimPrefix(msg, prefix)); q != "" {
			out = append(out, q)
		}
	}
}

// ashbyBounceEvidence is the stop detail of a bounced application: the status
// the service gave, that it was a bounce, and the questions it named.
func ashbyBounceEvidence(r browser.Response) string {
	d := "the application service answered HTTP " + strconv.Itoa(r.Status) + " but returned the form again, so nothing was accepted"
	if q := ashbyMissing(r.Body); len(q) > 0 {
		d += "; missing entry for required field: " + strings.Join(q, "; ")
	}
	return d
}
