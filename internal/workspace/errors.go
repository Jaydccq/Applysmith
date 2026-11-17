package workspace

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Stable error codes shared with the API layer.
const (
	CodeWorkspaceInvalid    = "workspace_invalid"
	CodePathOutside         = "path_outside_workspace"
	CodeRevisionConflict    = "revision_conflict"
	CodePayloadTooLarge     = "payload_too_large"
	CodeConfigInvalid       = "config_invalid"
	CodeSecretInConfig      = "secret_in_config"
	CodeUnknownKey          = "unknown_key"
	CodeDuplicateKey        = "duplicate_key"
	CodeTypeMismatch        = "type_mismatch"
	CodeNotAllowed          = "not_allowed"
	CodeFileExists          = "file_exists"
	CodeOutOfRange          = "out_of_range"
	CodeInvalidFormat       = "invalid_format"
	CodeRequired            = "required"
	CodeConstraint          = "constraint"
	CodeInvalidYAML         = "invalid_yaml"
	CodeFileMissing         = "file_missing"
	CodeEditPath            = "edit_path"
	CodeEditUnsupported     = "edit_unsupported"
	CodeWorkspaceIDInvalid  = "workspace_id_invalid"
	maxReportedIssues       = 50
	secretInConfigRemediate = "store secrets in the OS keychain through the setup flow or `applysmith secrets set`, never in a workspace file"
)

// Error is a typed workspace failure.
type Error struct {
	Code    string
	Path    string
	Message string
}

func (e *Error) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("%s: %s: %s", e.Code, e.Path, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Is matches on the code alone, so errors.Is(err, &Error{Code: ...}) works.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

func newError(code, path, format string, args ...any) *Error {
	return &Error{Code: code, Path: path, Message: fmt.Sprintf(format, args...)}
}

// ConflictError is the revision_conflict failure; it carries the latest
// on-disk content so the caller can merge.
type ConflictError struct {
	File     string
	Revision string
	Content  []byte
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%s: %s changed on disk (revision %s)", CodeRevisionConflict, e.File, e.Revision)
}

func (e *ConflictError) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == CodeRevisionConflict
}

// Issue is one validation finding.
type Issue struct {
	File    string
	Path    string
	Line    int
	Column  int
	Code    string
	Message string
	Allowed []string
}

func (i Issue) Error() string {
	var b strings.Builder
	b.WriteString(i.File)
	if i.Line > 0 {
		fmt.Fprintf(&b, ":%d:%d", i.Line, i.Column)
	}
	if i.Path != "" {
		b.WriteString(" " + i.Path)
	}
	fmt.Fprintf(&b, ": %s: %s", i.Code, i.Message)
	if len(i.Allowed) > 0 {
		fmt.Fprintf(&b, " (allowed: %s)", strings.Join(i.Allowed, ", "))
	}
	return b.String()
}

// Report holds up to 50 issues ordered by line, plus the count of those cut.
type Report struct {
	Issues []Issue
	More   int
}

func (r Report) OK() bool { return len(r.Issues) == 0 }

// First returns the first issue, for config_invalid messages.
func (r Report) First() (Issue, bool) {
	if len(r.Issues) == 0 {
		return Issue{}, false
	}
	return r.Issues[0], true
}

func (r Report) String() string {
	lines := make([]string, 0, len(r.Issues)+1)
	for _, i := range r.Issues {
		lines = append(lines, i.Error())
	}
	if r.More > 0 {
		lines = append(lines, fmt.Sprintf("%d more errors not shown", r.More))
	}
	return strings.Join(lines, "\n")
}

// InvalidError is returned by writes that fail validation; the file on disk
// is unchanged.
type InvalidError struct{ Report Report }

func (e *InvalidError) Error() string {
	if first, ok := e.Report.First(); ok {
		return CodeConfigInvalid + ": " + first.Error()
	}
	return CodeConfigInvalid
}

func (e *InvalidError) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == CodeConfigInvalid
}

// IsCode reports whether err carries the given typed code.
func IsCode(err error, code string) bool {
	return errors.Is(err, &Error{Code: code})
}

func finishReport(issues []Issue) Report {
	sort.SliceStable(issues, func(a, b int) bool {
		if issues[a].Line != issues[b].Line {
			return issues[a].Line < issues[b].Line
		}
		return issues[a].Column < issues[b].Column
	})
	r := Report{Issues: issues}
	if len(issues) > maxReportedIssues {
		r.Issues = issues[:maxReportedIssues]
		r.More = len(issues) - maxReportedIssues
	}
	return r
}
