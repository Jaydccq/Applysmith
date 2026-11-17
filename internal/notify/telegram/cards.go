package telegram

import (
	"context"
	"strings"

	"applysmith/internal/vault"
)

// ApprovalCard is a held Ask Agent action put in front of the owner. Only a
// one-time approval can come back from it: the card has no wider choice.
type ApprovalCard struct {
	ID string
	// SessionID is the conversation that held the call: a reply to the card
	// goes there.
	SessionID string
	Summary   string
	Purpose   string
	Reason    string
	Tier      int
}

// SendApprovalCard sends the card to the paired chat with Approve and Decline
// buttons. A held application submission (tier 4) is never given an approve
// button: it is confirmed in the dashboard. Nothing is sent when no chat is
// paired.
func (g *Gateway) SendApprovalCard(ctx context.Context, c ApprovalCard) error {
	o, ok := g.state.Owner(ctx)
	if !ok {
		return nil
	}
	var text strings.Builder
	text.WriteString("This action is waiting for your confirmation.\n")
	text.WriteString(c.Summary + "\n")
	if c.Purpose != "" {
		text.WriteString("You asked: " + clipLine(c.Purpose) + "\n")
	}
	if c.Reason != "" {
		text.WriteString("Held because: " + clipLine(c.Reason) + "\n")
	}
	var markup *Markup
	if c.Tier >= 4 {
		text.WriteString("Open it in the dashboard to confirm.")
	} else {
		text.WriteString("Approving runs it once.")
		approve, decline := FormApprove+":"+c.ID, FormDecline+":"+c.ID
		if len(approve) <= MaxCallbackBytes && len(decline) <= MaxCallbackBytes {
			markup = &Markup{Rows: [][]Button{{{Text: "Approve once", Data: approve}, {Text: "Decline", Data: decline}}}}
		}
	}
	id, err := g.sender.Send(ctx, o.ChatID, vault.Redact(text.String()), markup, false)
	if err != nil {
		return err
	}
	return g.state.SetAnchor(ctx, o.ChatID, id, Anchor{Session: c.SessionID})
}
