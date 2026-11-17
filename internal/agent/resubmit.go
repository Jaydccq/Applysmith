package agent

import (
	"context"

	"applysmith/internal/agent/origins"
	"applysmith/internal/gates"
)

// SubmitAgain decides an application run's submit gate once more, when a Done
// resumes the run. It reads only the facts of the turn that started the run: the
// user's message, whether outside content had been read, the posting and
// whether the call ran on an approved card. Done is not one of them, so a run
// whose submit was held without a confirmation stays held.
func SubmitAgain(ctx context.Context, r origins.ApplyRequest, lanes ApplyLanes) gates.Decision {
	if r.Confirmed {
		return allowTrace(gates.GateAgentSubmit, "confirmed:card", "the user confirmed this run's submit on its dashboard card")
	}
	var list []string
	if r.Tool == gates.ToolAutofillApplication {
		if get := applyListURLs(lanes); get != nil {
			var err error
			if list, err = get(ctx); err != nil {
				list = nil // an unreadable list vouches for nothing
			}
		}
	}
	return gates.AgentSubmit(gates.AgentSubmitInput{Caller: gates.Caller(r.Caller), Tool: r.Tool, PostingURL: r.URL, UserMessage: OwnText(r.UserMessage),
		OutsideContentRead: r.Outside, ListURLs: list})
}
