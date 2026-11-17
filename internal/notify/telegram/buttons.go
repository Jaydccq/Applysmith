package telegram

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"applysmith/internal/lazyregexp"
)

// MaxCallbackBytes is Telegram's limit on callback data.
const MaxCallbackBytes = 64

var (
	reSource = lazyregexp.New(`^[a-z0-9][a-z0-9._-]{0,40}$`)
	reToken  = lazyregexp.New(`^[0-9a-f]{12}$`)
	reID     = lazyregexp.New(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,50}$`)
	reMenu   = lazyregexp.New(`^[a-z]{1,12}$`)
	reThread = lazyregexp.New(`^[A-Za-z0-9][A-Za-z0-9_-]{0,40}$`)
)

// Callback forms.
const (
	FormScan    = "scan"
	FormIntent  = "intent"
	FormApprove = "approve"
	FormDecline = "decline"
	FormAck     = "ack"
	FormRate    = "rate"
	FormMenu    = "menu"
	// FormMail is mail:<action>:<thread>, a button of a Gmail alert or
	// deadline reminder.
	FormMail = "mail"
)

// ParseCallback splits button data into its form and arguments. It returns an
// empty form for anything outside the closed set of forms or over 64 bytes.
func ParseCallback(data string) (form string, args []string) {
	if len(data) > MaxCallbackBytes {
		return "", nil
	}
	parts := strings.Split(data, ":")
	switch parts[0] {
	case FormScan:
		if len(parts) == 2 && reSource.MatchString(parts[1]) {
			return FormScan, parts[1:]
		}
	case FormIntent:
		if len(parts) == 3 && (parts[1] == "eval" || parts[1] == "plan") && reToken.MatchString(parts[2]) {
			return FormIntent, parts[1:]
		}
	case FormApprove, FormDecline, FormAck:
		if len(parts) == 2 && reID.MatchString(parts[1]) {
			return parts[0], parts[1:]
		}
	case FormRate:
		if len(parts) == 3 && reID.MatchString(parts[1]) && (parts[2] == "up" || parts[2] == "down") {
			return FormRate, parts[1:]
		}
	case FormMenu:
		if len(parts) == 2 && reMenu.MatchString(parts[1]) {
			return FormMenu, parts[1:]
		}
	case FormMail:
		if len(parts) == 3 && slices.Contains(MailActions, parts[1]) && reThread.MatchString(parts[2]) {
			return FormMail, parts[1:]
		}
	}
	return "", nil
}

func (g *Gateway) handleCallback(ctx context.Context, u Update) (string, int64, error) {
	cb := u.Callback
	owner, paired := g.state.Owner(ctx)
	if !paired || cb.Message == nil || cb.Message.Chat.Type != "private" || cb.From.ID != owner.UserID || cb.Message.Chat.ID != owner.ChatID {
		// Anyone but the owner gets an empty acknowledgement and nothing else.
		_ = g.client.AnswerCallbackQuery(ctx, cb.ID, "")
		return "unauthorized", 0, nil
	}
	chat := cb.Message.Chat.ID
	form, args := ParseCallback(cb.Data)
	toast := func(text string) { _ = g.client.AnswerCallbackQuery(ctx, cb.ID, text) }
	finish := func(result string) error {
		text := strings.TrimSpace(cb.Message.Text)
		if text != "" {
			text += "\n\n"
		}
		return g.sender.Edit(ctx, chat, cb.Message.MessageID, text+result, nil)
	}
	b := g.cfg.Backend
	switch form {
	case "":
		toast("That button is not valid.")
		return "callback:invalid", chat, nil
	case FormMenu:
		toast("")
		a, c, err := g.run(ctx, u.UpdateID, chat, 0, args[0], "", "callback:menu")
		return a, c, err
	case FormScan:
		sources, err := b.ScanSources(ctx)
		if err != nil {
			return "callback:scan", chat, err
		}
		if !contains(sources, args[0]) {
			toast("That source is not available.")
			return "callback:scan", chat, nil
		}
		started, err := b.StartScan(ctx, args[0])
		if err != nil {
			toast("The scan could not start.")
			return "callback:scan", chat, finish("The " + args[0] + " scan could not start: " + clipLine(err.Error()))
		}
		toast("Started.")
		return "callback:scan", chat, finish("Started the " + args[0] + " scan: " + started.Link)
	case FormIntent:
		return g.intent(ctx, u.UpdateID, chat, args, toast, finish)
	case FormMail:
		return g.mail(ctx, cb, chat, args[0], args[1], toast)
	case FormAck:
		if err := b.Ack(ctx, args[0]); err != nil {
			toast("Could not acknowledge.")
			return "callback:ack", chat, finish("That could not be acknowledged: " + clipLine(err.Error()))
		}
		toast("Acknowledged.")
		return "callback:ack", chat, finish("Acknowledged.")
	case FormApprove, FormDecline:
		msg, err := b.Approve(ctx, args[0], form == FormApprove)
		if err != nil {
			return g.unsupported(chat, "callback:"+form, err, "Held actions are confirmed in the dashboard.", toast, finish)
		}
		toast("Done.")
		if msg == "" {
			msg = "Done."
		}
		return "callback:" + form, chat, finish(clipLine(msg))
	case FormRate:
		err := b.Rate(ctx, args[0], args[1])
		return g.unsupported(chat, "callback:rate", err, "Ratings are not available here yet.", toast, finish)
	}
	return "callback:invalid", chat, nil
}

// unsupported answers a button whose capability does not exist yet honestly,
// and passes any other result on.
func (g *Gateway) unsupported(chat int64, action string, err error, msg string, toast func(string), finish func(string) error) (string, int64, error) {
	switch {
	case err == nil:
		toast("Done.")
		return action, chat, finish("Done.")
	case errors.Is(err, errors.ErrUnsupported):
		toast(msg)
		return action, chat, nil
	}
	toast("That did not work.")
	return action, chat, finish("That did not work: " + clipLine(err.Error()))
}

// planStarted is what the message of a Plan button ends with once its plan
// was asked for.
const planStarted = "Making a plan. It will arrive in the next message."

// intent does what an Evaluate or Plan button stands for, once. The token is
// used only after the evaluation or the plan's turn was started, and keeps
// what that came to. A press of a used token, a retried update whose edit
// failed or a second tap, starts nothing and edits the message to what the
// first press did, so "Already handled." is never said of work that was not
// done.
func (g *Gateway) intent(ctx context.Context, update, chat int64, args []string, toast func(string), finish func(string) error) (string, int64, error) {
	const action = "callback:intent"
	// settle edits the message to the outcome. A 400 is Telegram refusing the
	// edit it already made, or a message the bot may no longer edit.
	settle := func(tok Token) error {
		text := planStarted
		if tok.Action == "eval" {
			text = "Evaluation started: " + tok.Result
		}
		err := finish(text)
		var api *APIError
		if errors.As(err, &api) && api.Code == http.StatusBadRequest {
			return nil
		}
		return err
	}
	tok, err := g.state.Token(ctx, args[1], chat)
	switch {
	case errors.Is(err, ErrTokenUsed):
		toast("Already handled.")
		return action, chat, settle(tok)
	case errors.Is(err, ErrTokenExpired):
		toast("That button expired. Send the link again.")
		return action, chat, nil
	case errors.Is(err, ErrTokenUnknown):
		toast("That button is no longer valid.")
		return action, chat, nil
	case err != nil:
		return action, chat, err
	}
	if tok.Action != args[0] {
		toast("That button is not valid.")
		return action, chat, nil
	}
	if tok.Action == "eval" {
		started, err := g.cfg.Backend.StartEvaluation(ctx, tok.Payload)
		if err != nil {
			toast("The evaluation could not start.")
			return action, chat, finish("The evaluation could not start: " + clipLine(err.Error()))
		}
		tok.Result = started.Link
	} else {
		asked, err := g.turn(ctx, question{update: update, chat: chat, action: action, text: planPrompt(tok.Payload)})
		if err != nil {
			return action, chat, err
		}
		if !asked {
			// The agent is busy or refused the text, which the owner was told:
			// no plan is coming, so the button stays as it is.
			toast("")
			return action, chat, nil
		}
	}
	if err := g.state.UseToken(ctx, args[1], chat, tok.Result); err != nil {
		return action, chat, err
	}
	toast("On it.")
	return action, chat, settle(tok)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func clipLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 200 {
		return string(r[:199]) + "…"
	}
	return s
}
