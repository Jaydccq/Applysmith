// Clean-room: docs/provenance.md#CR-AGENT-GRANTS

// Package approvals stores the confirmation cards of held Ask Agent calls and
// the standing approvals a user may grant from a card. It decides who may
// approve what and for how long; the guard chain asks it whether an approval
// covers a call, and nothing else can create one.
package approvals

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Kind says what a standing approval for a tool is bound to.
type Kind string

const (
	// ByTool binds to the tool itself.
	ByTool Kind = "tool"
	// ByValue binds to the string value of one argument.
	ByValue Kind = "value"
	// ByHost binds to the host of a URL argument.
	ByHost Kind = "host"
	// ByURL binds to the exact URL argument.
	ByURL Kind = "url"
)

// Eligible names a tool a standing approval may cover and what it is bound to.
type Eligible struct {
	Tool string
	Kind Kind
	// Arg is the argument the target is read from; empty for ByTool.
	Arg string
}

// EligibleTools is the whole list. Every other tool, every call with submit
// true, and anything in a scheduled or digest turn is ineligible: the card
// offers those one-time approval only.
var EligibleTools = []Eligible{
	{Tool: "save_fact", Kind: ByTool},
	{Tool: "amend_fact", Kind: ByTool},
	{Tool: "record_story_answer", Kind: ByTool},
	{Tool: "tailor_resume", Kind: ByValue, Arg: "application_id"},
	{Tool: "evaluate_job", Kind: ByHost, Arg: "url"},
	{Tool: "scan_portal", Kind: ByValue, Arg: "source"},
	{Tool: "autofill_application", Kind: ByURL, Arg: "url"},
}

// Target returns what a standing approval for the call would be bound to, and
// false when the call is not eligible for one. The target is derived from the
// arguments only.
func Target(tool string, args json.RawMessage) (string, bool) {
	var e *Eligible
	for i := range EligibleTools {
		if EligibleTools[i].Tool == tool {
			e = &EligibleTools[i]
			break
		}
	}
	if e == nil {
		return "", false
	}
	var obj map[string]json.RawMessage
	if len(args) > 0 && json.Unmarshal(args, &obj) != nil {
		return "", false
	}
	if raw, ok := obj["submit"]; ok && string(raw) != "false" {
		return "", false
	}
	if e.Kind == ByTool {
		return tool, true
	}
	val, ok := stringArg(obj[e.Arg])
	if !ok {
		return "", false
	}
	switch e.Kind {
	case ByValue:
		return val, true
	case ByHost:
		return PostingHost(val)
	}
	return PostingURL(val)
}

// stringArg reads a JSON string or whole number as text.
func stringArg(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		s = strings.TrimSpace(s)
		return s, s != ""
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil && !strings.ContainsAny(n.String(), ".eE") {
		return n.String(), true
	}
	return "", false
}

// PostingHost is the lower-case host of an http or https URL without a leading
// "www.", or false for anything else.
func PostingHost(raw string) (string, bool) {
	u, ok := parseHTTP(raw)
	if !ok {
		return "", false
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."), true
}

// PostingURL is the canonical text of an http or https URL: lower-case scheme
// and host without a leading "www.", the port, the path and the query, and no
// fragment or user-info. A URL with user-info is not a posting.
func PostingURL(raw string) (string, bool) {
	u, ok := parseHTTP(raw)
	if !ok {
		return "", false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if p := u.Port(); p != "" {
		host += ":" + p
	}
	out := fmt.Sprintf("%s://%s%s", strings.ToLower(u.Scheme), host, u.EscapedPath())
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out, true
}

func parseHTTP(raw string) (*url.URL, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User != nil || u.Hostname() == "" {
		return nil, false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u, true
	}
	return nil, false
}
