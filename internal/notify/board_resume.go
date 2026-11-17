// Clean-room: docs/provenance.md#CR-RUNS-BOARD
// Clean-room: docs/provenance.md#CR-AGENT-HANDOVER

package notify

import (
	"context"
	"slices"
)

// resumeReasons are the reasons a waiting application run can be resumed from
// with Done. A held confirmation or a possible submission is never one.
var resumeReasons = []string{"sign-in", "answer", "challenge", "review", "one-time-code", "other"}

// CanResume reports whether Done applies to an entry: an application run the
// user or the Ask Agent started that waits for the user for a reason they can
// clear in the page.
func CanResume(e Entry) bool {
	return e.Kind == KindApply && (e.Origin == OriginUser || e.Origin == OriginAgent) && e.State == NeedsYou && slices.Contains(resumeReasons, e.Reason)
}

// CanSubmitKept reports whether "Submit this form" applies to an entry: an
// application run the user or the Ask Agent started whose filled form waits at
// Submit, in the tab it kept, for the user's confirmation (F-3, AC-6).
func CanSubmitKept(e Entry) bool {
	return e.Kind == KindApply && (e.Origin == OriginUser || e.Origin == OriginAgent) && e.State == NeedsYou && e.Reason == "confirmation" && e.FocusTab != ""
}

// Resume moves a waiting entry back to working, which leaves the inbox and
// withdraws its announcement. The check and the move are one write, so of two
// simultaneous presses the second finds the entry working and fails with
// ErrStateChanged.
func (b *Board) Resume(ctx context.Context, id, detail string) (Entry, error) {
	return b.Move(ctx, id, Working, MoveArgs{Detail: detail, If: CanResume})
}

// ResumeToSubmit is Resume for a confirmed "Submit this form". It is the one
// write that settles a form's hold, whether the board or the run's
// conversation confirmed it, so the second confirmation fails with
// ErrStateChanged and presses nothing.
func (b *Board) ResumeToSubmit(ctx context.Context, id, detail string) (Entry, error) {
	return b.Move(ctx, id, Working, MoveArgs{Detail: detail, If: CanSubmitKept})
}
