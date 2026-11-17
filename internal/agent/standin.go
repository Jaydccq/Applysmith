// Clean-room: docs/provenance.md#CR-AGENT-LOOP

package agent

import (
	"context"

	"applysmith/internal/agent/sessions"
)

// StandInText is the result recorded for a tool call that never got one
// because the process ended before it finished. It says nothing about retrying.
const StandInText = "The server restarted before this call finished. The call may or may not have run. Check the runs board for its state."

// RecordStandIns gives every orphaned call of a conversation its stand-in
// result. It runs before a conversation is next sent to a model, for calls
// abandoned by a pi restart and by a server restart alike; the result is
// stored as system-made so the record shows the tool did not produce it.
func RecordStandIns(ctx context.Context, s *sessions.Store, sessionID string) (int, error) {
	return s.RecordStandIns(ctx, sessionID, StandInText)
}
