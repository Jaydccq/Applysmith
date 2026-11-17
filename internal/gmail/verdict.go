package gmail

import "context"

// Verdict kinds.
const (
	VerdictSignal  = "signal"
	VerdictIgnored = "ignored"
)

// Verdict is the classifier's decision about one message in storage shape.
// An ignored message carries only its reason; its body is never stored.
type Verdict struct {
	Kind   string
	Reason string

	Type, Tier, Direction string
	Employer, Role        string
	Confidence            float64
	DueMS                 int64
	RuleID                string
	ReviewReasons         []string
	DetailJSON            string

	// Snippet is kept for good (at most 220 characters); Body and Raw only
	// until the retention sweep removes them.
	Snippet string
	Body    string
	Raw     string
}

// ClassifyFunc turns a message into a verdict. mailbox is the authorized
// address, empty when unknown.
type ClassifyFunc func(m *Message, mailbox string) Verdict

// Linker resolves signals to tracker rows. It runs as the last stage of a sync;
// linking is owned by the outcomes work, and a nil Linker skips the stage.
type Linker interface {
	Link(ctx context.Context, runID int64) (linked int, err error)
}
