// Clean-room: docs/provenance.md#CR-AGENT-TRIGGERS

package agent

import (
	"context"
	"errors"
	"strings"

	"applysmith/internal/agent/pirpc"
	"applysmith/internal/agent/sessions"
	"applysmith/internal/store"
)

// ScheduledRequest is a turn nobody typed: a cadence prompt at its slot or a
// signal trigger firing. It runs in the scheduled envelope, so only read and
// draft tools exist for it.
type ScheduledRequest struct {
	// Title names the new conversation.
	Title  string
	Prompt string
	// Untrusted starts the turn tainted because its input is outside text; the
	// prompt message is stored marked, so a follow-up in the conversation
	// starts tainted too.
	Untrusted bool
	// Withhold names tools the turn is not offered besides those the envelope
	// already leaves out.
	Withhold []string
}

// Scheduled turn statuses.
const (
	ScheduledAnswered = "answered"
	ScheduledFailed   = "failed"
	ScheduledTimedOut = "timed_out"
)

// ScheduledResult is how a scheduled turn ended. Answer is the final text.
type ScheduledResult struct {
	SessionID string
	TurnID    string
	Status    string
	Reason    string
	Detail    string
	Answer    string
}

// RunScheduled runs one scheduled turn in a new conversation and returns when
// it is over. The turn ends after the 10-minute limit like any other.
func (a *Agent) RunScheduled(ctx context.Context, req ScheduledRequest) (ScheduledResult, error) {
	text := strings.TrimSpace(req.Prompt)
	if text == "" {
		return ScheduledResult{}, ErrEmptyMessage
	}
	sess, err := a.cfg.Sessions.Create(ctx, sessions.Scheduled, clipRunes(oneLine(req.Title), sessions.MaxTitle))
	if err != nil {
		return ScheduledResult{}, err
	}
	turn := &Turn{ID: store.NewID("turn"), SessionID: sess.ID, Channel: ChannelScheduled, Origin: OriginModel, UserMessage: text, Withheld: req.Withhold}
	if req.Untrusted {
		turn.MarkOutside()
	}
	active, err := a.start(ctx, turn, "", func() (sessions.Message, error) {
		return sessions.Message{Role: sessions.RoleUser, Body: text, OK: true, OutsideContent: req.Untrusted}, nil
	})
	if err != nil {
		return ScheduledResult{}, err
	}
	a.wg.Add(1)
	defer a.wg.Done()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(a.ctx, cancel)()
	res := a.run(runCtx, turn, text, active, Route{})

	out := ScheduledResult{SessionID: sess.ID, TurnID: turn.ID, Reason: res.Reason, Detail: res.Detail}
	switch {
	case res.Status == sessions.TurnFinal:
		out.Status = ScheduledAnswered
		if out.Answer, err = a.finalAnswer(ctx, sess.ID, turn.ID); err != nil {
			return out, err
		}
	case res.Reason == pirpc.ReasonTimeLimit:
		out.Status = ScheduledTimedOut
	default:
		out.Status = ScheduledFailed
	}
	return out, nil
}

// finalAnswer is the last thing the assistant said in the turn.
func (a *Agent) finalAnswer(ctx context.Context, sessionID, turnID string) (string, error) {
	msgs, err := a.cfg.Sessions.Messages(ctx, sessionID)
	if err != nil {
		return "", err
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if m := msgs[i]; m.TurnID == turnID && m.Role == sessions.RoleAssistant {
			return strings.TrimSpace(m.Body), nil
		}
	}
	return "", errors.New("agent: the scheduled turn ended without an answer")
}
