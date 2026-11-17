// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
)

const (
	// AuditRetentionDays is how long a gate_decisions row must be kept; the
	// store's prune removes only rows older than that (AuditPruneBefore).
	AuditRetentionDays = 180
	// maxTargetPath caps the recorded path.
	maxTargetPath = 160
	// opaqueSegmentMin is the length from which a path segment holding a digit
	// is treated as an identifier and replaced.
	opaqueSegmentMin = 24
	messageHashHex   = 16
)

// AuditPruneBefore is the instant before which a gate_decisions row may be
// removed. The runtime never deletes a row that is not older than the
// retention period, so a row exactly that old is at the instant and is kept.
func AuditPruneBefore(now int64) int64 { return now - AuditRetentionDays*Day }

// AuditContext carries what the caller knows besides the decision. Empty
// strings are stored as null.
type AuditContext struct {
	RunID, Tool, TurnID, ConversationID, RiskTier string
	Caller                                        Caller
	// ArgFingerprint is the caller's hash of the tool arguments.
	ArgFingerprint string
	// Salt is the per-install random salt; UserMessage is hashed with it and
	// never stored.
	Salt, UserMessage string
	// UserMessageHash stands in for UserMessage when only the hash survives,
	// as for a row that runs after a restart.
	UserMessageHash string
	// TargetURL is the URL the action touches; only its redacted form is kept.
	TargetURL string
	// Trace marks an allow written by the confirmation step.
	Trace bool
}

// AuditRow is one append-only gate_decisions row. It has no field for message
// text or a query string, so neither can be stored.
type AuditRow struct {
	At                                     int64
	Gate                                   Gate
	Verdict                                Verdict
	Code, Reason                           string
	RunID                                  string
	Caller                                 Caller
	Tool, TurnID, ConversationID, RiskTier string
	ArgHash, UserMessageHash               string
	TargetHost, TargetPath, TargetScheme   string
}

// NewAuditRow builds the row for a decision. There is no parameter that turns
// auditing off: a decision always yields a row.
func NewAuditRow(at int64, d Decision, c AuditContext) AuditRow {
	r := AuditRow{
		At: at, Gate: d.Gate, Verdict: d.Verdict, Code: d.Code, Reason: capReason(d.Reason),
		RunID: c.RunID, Caller: c.Caller, Tool: c.Tool, TurnID: c.TurnID, ConversationID: c.ConversationID,
		RiskTier: c.RiskTier, ArgHash: c.ArgFingerprint,
	}
	if c.Trace && d.Verdict == Allow {
		r.Verdict = AllowWithTrace
	}
	if c.UserMessage == "" {
		r.UserMessageHash = c.UserMessageHash
	} else {
		r.UserMessageHash = MessageHash(c.Salt, c.UserMessage)
	}
	if c.TargetURL != "" {
		r.TargetHost, r.TargetPath, r.TargetScheme = RedactTarget(c.TargetURL)
	}
	return r
}

// MessageHash is the first 16 hex characters of SHA-256 over the salt followed
// by the message.
func MessageHash(salt, message string) string {
	sum := sha256.Sum256([]byte(salt + message))
	return hex.EncodeToString(sum[:])[:messageHashHex]
}

// RedactTarget reduces a URL to what the audit may keep. An http(s) URL
// yields host and path with query and fragment removed, long opaque path
// segments replaced by "~" and the path capped; any other scheme yields the
// scheme alone, and an unparsable value yields "invalid".
func RedactTarget(raw string) (host, path, scheme string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" {
		return "", "", "invalid"
	}
	s := strings.ToLower(u.Scheme)
	if s != "http" && s != "https" {
		return "", "", s
	}
	segs := strings.Split(u.EscapedPath(), "/")
	for i, seg := range segs {
		if len(seg) >= opaqueSegmentMin && strings.ContainsAny(seg, "0123456789") {
			segs[i] = "~"
		}
	}
	path = strings.Join(segs, "/")
	if len(path) > maxTargetPath {
		path = path[:maxTargetPath]
	}
	return strings.ToLower(u.Host), path, s
}

// TurnAudit admits one row per distinct gate, tool, argument fingerprint and
// verdict within an Ask Agent turn. The zero value is ready.
type TurnAudit struct{ seen map[string]bool }

// Admit reports whether the row is the first of its kind this turn.
func (t *TurnAudit) Admit(r AuditRow) bool {
	if t.seen == nil {
		t.seen = map[string]bool{}
	}
	k := strings.Join([]string{string(r.Gate), r.Tool, r.ArgHash, string(r.Verdict)}, "\x00")
	if t.seen[k] {
		return false
	}
	t.seen[k] = true
	return true
}

// ValidCaller reports whether c is one of the audit table's callers.
func ValidCaller(c Caller) bool {
	switch c {
	case CallerAskAgent, CallerApplyList, CallerCLI, CallerDashboard, CallerScheduler, CallerTelegram:
		return true
	}
	return false
}
