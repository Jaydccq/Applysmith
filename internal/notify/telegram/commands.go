package telegram

import (
	"applysmith/internal/vault"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxPages   = 5
	moreInDash = "\n\nThe rest is in the dashboard, on the Ask Agent page."
	moreOnHome = "\n\nThe rest is in the dashboard, under Today on Home."
	welcome    = `Hi, I am your Applysmith assistant. Write to me as you would to a person, in any language. For example:
• What should I do today?
• How many applications are in interview?
• Draft an answer to "Why do you want to work here?"
Paste a posting link and I offer to evaluate it or plan the application.

The buttons below are the usual reports; /help lists every command. Applications are started and submitted in the dashboard, never here.`
)

// commands are the commands of /help and of the menu beside the message box.
var commands = []struct{ name, args, about string }{
	{"today", "", "what was done for you today and what is left"},
	{"inbox", "", "what to do now"},
	{"status", "", "how the app is doing"},
	{"tracker", "", "applications by status"},
	{"pipeline", "", "the inbox and its best entries"},
	{"queue", "", "today's apply list"},
	{"gmail", "", "the last day of mail signals"},
	{"scan", "", "start a scan"},
	{"answer", "<question>", "draft an answer to an application question"},
	{"plan", "<posting URL, company and role, or text>", "an application plan"},
	{"open", "<screen>", "where to find a screen"},
	{"stop", "", "stop the answer I am working on"},
	{"trace", "", "what the last answer used"},
	{"help", "", "what I can do"},
}

func helpText() string {
	var b strings.Builder
	b.WriteString("Write to me in plain words, in any language, and I answer from your job search. Paste a posting link to evaluate it or plan the application.\n\nCommands:\n")
	for _, c := range commands {
		b.WriteString("/" + c.name)
		if c.args != "" {
			b.WriteString(" " + c.args)
		}
		b.WriteString("  " + c.about + "\n")
	}
	b.WriteString("\nApplications are started and submitted in the dashboard, never here.")
	return b.String()
}

func botCommands() []Command {
	var out []Command
	for _, c := range commands {
		out = append(out, Command{Command: c.name, Description: c.about})
	}
	return out
}

// menu is the button menu of /start and of an unknown command.
func menu() *Markup {
	row := func(names ...string) []Button {
		var r []Button
		for _, n := range names {
			r = append(r, Button{Text: strings.ToUpper(n[:1]) + n[1:], Data: "menu:" + n})
		}
		return r
	}
	return &Markup{Rows: [][]Button{row("status", "tracker", "pipeline"), row("queue", "gmail", "inbox"), row("scan", "help")}}
}

// splitCommand returns the command name without its slash and without the
// bot's @username, and the rest of the line.
func splitCommand(text string) (cmd, arg string) {
	word, rest, _ := strings.Cut(strings.TrimSpace(text), " ")
	word = strings.TrimPrefix(word, "/")
	word, _, _ = strings.Cut(word, "@")
	return strings.ToLower(word), strings.TrimSpace(rest)
}

func (g *Gateway) command(ctx context.Context, update int64, m *Message, text string) (string, int64, error) {
	cmd, arg := splitCommand(text)
	return g.run(ctx, update, m.Chat.ID, m.MessageID, cmd, arg, "command:"+cmd)
}

// run executes a command by name; the menu buttons use it too. message is the
// owner's message that named it, zero for a button.
func (g *Gateway) run(ctx context.Context, update, chat, message int64, cmd, arg, action string) (string, int64, error) {
	b := g.cfg.Backend
	view := func(f func(context.Context) (string, error)) (string, int64, error) {
		s, err := f(ctx)
		if err != nil {
			return action, chat, err
		}
		return action, chat, g.reply(ctx, chat, s, nil)
	}
	switch cmd {
	case "start":
		return action, chat, g.reply(ctx, chat, welcome, menu())
	case "help":
		return action, chat, g.reply(ctx, chat, helpText(), nil)
	case "status":
		return view(b.Status)
	case "tracker":
		return view(b.Tracker)
	case "pipeline":
		return view(b.Pipeline)
	case "queue":
		return view(b.Queue)
	case "gmail":
		return view(b.Gmail)
	case "inbox":
		return view(b.Inbox)
	case "today":
		// The day can run past one message: it goes in pages, so none of it is
		// cut off.
		s, err := b.Today(ctx)
		if err != nil {
			return action, chat, err
		}
		return action, chat, g.sendPages(ctx, chat, splitPages(s, moreOnHome))
	case "scan":
		return action, chat, g.scanKeyboard(ctx, chat)
	case "answer":
		if arg == "" {
			return action, chat, g.reply(ctx, chat, "Usage: /answer <the application question>", nil)
		}
		return g.ask(ctx, question{update: update, chat: chat, message: message, action: action,
			text: "Draft an answer to this application question from my answer bank and evidence. Only draft; do not fill or submit anything.\n\nQuestion: " + arg})
	case "plan":
		if arg == "" {
			return action, chat, g.reply(ctx, chat, "Usage: /plan <posting URL, company and role, or the posting text>", nil)
		}
		return g.ask(ctx, question{update: update, chat: chat, message: message, action: action, text: planPrompt(arg)})
	case "open":
		if arg == "" {
			return action, chat, g.reply(ctx, chat, "Usage: /open <screen>, for example /open tracker", nil)
		}
		s, err := b.Open(ctx, arg)
		if err != nil {
			return action, chat, err
		}
		return action, chat, g.reply(ctx, chat, s, nil)
	case "stop":
		return action, chat, g.stop(ctx, chat)
	case "trace":
		sid, _ := g.state.ChatSession(ctx, chat)
		s, err := b.Trace(ctx, sid)
		if err != nil {
			return action, chat, err
		}
		return action, chat, g.reply(ctx, chat, s, nil)
	}
	return "command:unknown", chat, g.reply(ctx, chat, "I do not know that command. Send /help for the list.", menu())
}

func planPrompt(what string) string {
	return "Make an application plan for this posting: what to check, which résumé and answers to use, and what is missing. Never submit or start an application; applications are confirmed in the dashboard.\n\nPosting: " + what
}

// scanKeyboard offers the enabled discovery sources as buttons.
func (g *Gateway) scanKeyboard(ctx context.Context, chat int64) error {
	sources, err := g.cfg.Backend.ScanSources(ctx)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		return g.reply(ctx, chat, "No scan source is enabled. Enable one in the dashboard first.", nil)
	}
	if len(sources) > 8 {
		sources = sources[:8]
	}
	var rows [][]Button
	for i := 0; i < len(sources); i += 2 {
		row := []Button{{Text: sources[i], Data: "scan:" + sources[i]}}
		if i+1 < len(sources) {
			row = append(row, Button{Text: sources[i+1], Data: "scan:" + sources[i+1]})
		}
		rows = append(rows, row)
	}
	return g.reply(ctx, chat, "Which scan should I start?", &Markup{Rows: rows})
}

func isURLOnly(s string) bool {
	return (strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")) && !strings.ContainsAny(s, " \t\n") && len(s) > 10
}

func (g *Gateway) offerIntents(ctx context.Context, chat int64, postingURL string) error {
	ev, err := g.state.IssueToken(ctx, chat, "eval", postingURL)
	if err != nil {
		return err
	}
	pl, err := g.state.IssueToken(ctx, chat, "plan", postingURL)
	if err != nil {
		return err
	}
	return g.reply(ctx, chat, "What should I do with this posting?\n"+postingURL, &Markup{Rows: [][]Button{{
		{Text: "Evaluate", Data: "intent:eval:" + ev}, {Text: "Plan", Data: "intent:plan:" + pl},
	}}})
}

// splitPages cuts an answer into messages of at most 3,700 characters at line
// or space boundaries, at most five, the last ending with more, the pointer to
// where the dashboard shows the whole text, when more is left.
func splitPages(text, more string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{"(No answer.)"}
	}
	var pages []string
	rest := text
	for rest != "" && len(pages) < maxPages {
		limit := MaxText
		if len(pages) == maxPages-1 {
			limit -= utf8.RuneCountInString(more)
		}
		if utf8.RuneCountInString(rest) <= limit {
			pages = append(pages, rest)
			rest = ""
			break
		}
		cut := cutAt(rest, limit)
		pages = append(pages, strings.TrimSpace(rest[:cut]))
		rest = strings.TrimSpace(rest[cut:])
	}
	if rest != "" {
		pages[len(pages)-1] += more
	}
	return pages
}

// cutAt is the byte index at which to cut s so the head has at most limit
// runes, preferring the last line break and then the last space.
func cutAt(s string, limit int) int {
	if utf8.RuneCountInString(s) <= limit {
		return len(s)
	}
	end := len(string([]rune(s)[:limit]))
	head := s[:end]
	if i := strings.LastIndex(head, "\n"); i > end/2 {
		return i
	}
	if i := strings.LastIndex(head, " "); i > end/2 {
		return i
	}
	return end
}

// Replies of the conversation with the Ask Agent.
const (
	busyText    = "I am still working on your previous message. Send /stop to stop it, or wait for its answer."
	tooLongText = "That message is too long. Send it in the dashboard instead."
	interimText = "Working on it. I will answer here when it is done."
	idleText    = "Nothing is running."
	stoppedText = "Stopping. What it did so far follows."
)

// question is a message for the Ask Agent: the update it came in, the owner's
// message it answers (zero for a button), the conversation it goes to ("" for
// the chat's own) and its text.
type question struct {
	update  int64
	chat    int64
	message int64
	session string
	text    string
	action  string
}

// freeText hands a message that is not a command to the Ask Agent. A message
// that is only a posting URL gets buttons instead of a turn. A reply continues
// the conversation the replied-to message belongs to and carries that message
// as a quote (replied).
func (g *Gateway) freeText(ctx context.Context, update int64, m *Message, text string) (string, int64, error) {
	if isURLOnly(text) {
		return "text:url", m.Chat.ID, g.offerIntents(ctx, m.Chat.ID, text)
	}
	q := question{update: update, chat: m.Chat.ID, message: m.MessageID, text: text, action: "text"}
	if r := m.ReplyTo; r != nil {
		a, _ := g.state.AnchorOf(ctx, m.Chat.ID, r.MessageID)
		q.session, q.text, q.action = a.Session, replied(text, r.Text, a.Thread), "text:reply"
	}
	return g.ask(ctx, q)
}

// Bounds of the quote a reply carries.
const (
	quoteRunes = 600
	quoteHead  = "Replying to your message:"
)

// replied is the owner's reply with the message it answers below it as
// quoted lines, which the agent's guard and router read as pasted text, never
// as the request, and the Gmail thread that message is about, which the agent
// reads with its own tool. The quote keeps whole lines up to 600 characters.
func replied(text, quoted, thread string) string {
	var lines []string
	n := 0
	for _, l := range strings.Split(strings.TrimSpace(quoted), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if n += utf8.RuneCountInString(l); n > quoteRunes {
			if len(lines) == 0 {
				// One long line: its head, so the quote still says what it answers.
				lines = append(lines, string([]rune(l)[:quoteRunes])+"…")
			} else {
				lines = append(lines, "…")
			}
			break
		}
		lines = append(lines, l)
	}
	if thread != "" {
		lines = append(lines, "(Gmail thread "+thread+")")
	}
	if len(lines) == 0 {
		return text
	}
	return text + "\n\n> " + quoteHead + "\n> " + strings.Join(lines, "\n> ")
}

// ask starts the question's turn and returns: the poller goes on while the
// turn runs, and a follower sends the answer. A retried update whose turn was
// started already starts nothing again.
func (g *Gateway) ask(ctx context.Context, q question) (string, int64, error) {
	_, err := g.turn(ctx, q)
	return q.action, q.chat, err
}

// turn is ask for a caller that must know whether the question has a turn:
// started is false when the agent was busy or refused the text, which the owner
// was told.
func (g *Gateway) turn(ctx context.Context, q question) (started bool, err error) {
	if t, ok := g.state.TurnOfUpdate(ctx, q.update); ok {
		g.follow(t)
		return true, nil
	}
	cur, last := g.state.ChatSession(ctx, q.chat)
	if cur != "" && g.now().Sub(last) > ConversationIdle {
		cur = ""
	}
	sid := q.session
	own := sid == "" || sid == cur
	if sid == "" {
		sid = cur
	}
	asked, err := g.cfg.Backend.Ask(ctx, AskRequest{SessionID: sid, Text: q.text})
	switch {
	case errors.Is(err, ErrAgentBusy):
		return false, g.reply(ctx, q.chat, busyText, nil)
	case errors.Is(err, ErrMessageTooLong):
		return false, g.reply(ctx, q.chat, tooLongText, nil)
	case err != nil:
		return false, err
	}
	// The turn runs now, so the update is done whatever follows: a retry
	// would find the conversation busy and its answer would have no
	// follower. A turn that could not be recorded is followed all the same;
	// only a restart before its answer loses it.
	t := Turn{ID: asked.TurnID, Update: q.update, Chat: q.chat, Session: asked.SessionID, ReplyTo: q.message}
	if err := g.state.AddTurn(ctx, t); err != nil {
		g.cfg.Log.Warn("a Telegram turn could not be recorded; its answer is sent unless the app stops first", "turn", t.ID, "error", err.Error())
	}
	g.follow(t)
	// The chat's own conversation is the one its messages that are not
	// replies continue; a reply into another conversation leaves it as it
	// was.
	if own {
		if err := g.state.SetChatSession(ctx, q.chat, asked.SessionID); err != nil {
			g.cfg.Log.Warn("the chat's conversation could not be recorded", "error", err.Error())
		}
	}
	return true, nil
}

// stop cancels the turn of the chat that is still running.
func (g *Gateway) stop(ctx context.Context, chat int64) error {
	t, ok := g.state.RunningTurn(ctx, chat)
	if !ok {
		return g.reply(ctx, chat, idleText, nil)
	}
	if err := g.cfg.Backend.Stop(ctx, t.Session); errors.Is(err, ErrNoTurn) {
		// It ended as the command came in: its answer is on its way.
		return nil
	} else if err != nil {
		return err
	}
	return g.reply(ctx, chat, stoppedText, nil)
}

// resume follows every turn whose answer is not in the chat yet: a stopped
// process left them.
func (g *Gateway) resume(ctx context.Context) {
	open, err := g.state.OpenTurns(ctx)
	if err != nil {
		g.cfg.Log.Warn("the Telegram turns waiting for their answer could not be read", "error", err.Error())
		return
	}
	for _, t := range open {
		g.follow(t)
	}
}

// follow sends a turn's answer once the turn ended, unless a follower does
// already. Followers end with the gateway (Close).
func (g *Gateway) follow(t Turn) {
	g.mu.Lock()
	if g.following[t.ID] {
		g.mu.Unlock()
		return
	}
	g.following[t.ID] = true
	g.wg.Add(1)
	g.mu.Unlock()
	go func() {
		defer g.wg.Done()
		defer func() {
			g.mu.Lock()
			delete(g.following, t.ID)
			g.mu.Unlock()
		}()
		g.deliver(g.life, t)
	}()
}

// deliver shows "typing" until the turn ends, then sends its answer page by
// page, the first as a reply to the owner's message, each page recorded once
// Telegram accepted it. A page that fails is tried again, backing off to a
// minute, for as long as the gateway runs, and the next start goes on with it;
// only a refusal (refused) gives the answer up. Nothing goes to a chat that is
// no longer the paired one.
func (g *Gateway) deliver(ctx context.Context, t Turn) {
	stop := g.working(ctx, t.Chat)
	ans, ok := g.await(ctx, t)
	stop()
	if !ok {
		return
	}
	// The row and not the snapshot: an earlier follower may have sent pages
	// since, or finished.
	if cur, done, found := g.state.TurnOf(ctx, t.ID); found {
		if done {
			return
		}
		t = cur
	}
	pages := splitPages(ans.Text, moreInDash)
	for i := t.Sent; i < len(pages); i++ {
		if o, paired := g.state.Owner(ctx); !paired || o.ChatID != t.Chat {
			g.cfg.Log.Info("an Ask Agent answer was dropped: its chat is no longer paired", "turn", t.ID)
			break
		}
		replyTo := int64(0)
		if i == 0 {
			replyTo = t.ReplyTo
		}
		htmlText, plain := formatted(vault.Redact(pages[i]))
		var id int64
		var err error
		for attempt := 0; ; attempt++ {
			if id, err = g.sender.SendFormatted(ctx, t.Chat, htmlText, plain, replyTo); err == nil || ctx.Err() != nil || refused(err) {
				break
			}
			if attempt == 0 {
				g.cfg.Log.Warn("an Ask Agent answer could not be sent to Telegram; trying again", "turn", t.ID, "error", err.Error())
			}
			if g.cfg.Clock.Sleep(ctx, g.backoff(g.cfg.Pause, FailureCap, attempt)) != nil {
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			g.cfg.Log.Warn("Telegram refused an Ask Agent answer, which was given up", "turn", t.ID, "error", err.Error())
			break
		}
		if err := g.state.SetAnchor(ctx, t.Chat, id, Anchor{Session: t.Session}); err != nil {
			g.cfg.Log.Warn("a Telegram answer's conversation could not be recorded", "turn", t.ID, "error", err.Error())
		}
		if err := g.state.TurnSent(ctx, t.ID, i+1); err != nil {
			g.cfg.Log.Warn("a sent Telegram page could not be recorded", "turn", t.ID, "error", err.Error())
		}
	}
	if err := g.state.TurnDone(ctx, t.ID); err != nil {
		g.cfg.Log.Warn("a delivered Telegram turn could not be recorded", "turn", t.ID, "error", err.Error())
	}
}

// refused reports whether Telegram refused a message for good: a 400 for its
// plain text (the HTML was already retried as plain text) or a 403, the owner
// blocked the bot. No connection, a 5xx or a revoked token pass in time.
func refused(err error) bool {
	var api *APIError
	return errors.As(err, &api) && (api.Code == http.StatusBadRequest || api.Code == http.StatusForbidden)
}

// await reads the turn until it ended. It reports false when the gateway
// stops first. The pause between two reads starts at AnswerPoll and doubles
// to AnswerPollSlowest times that: a short turn is answered at once and a
// long one costs a read every two seconds.
func (g *Gateway) await(ctx context.Context, t Turn) (Answer, bool) {
	pause := g.cfg.AnswerPoll
	tick := time.NewTimer(pause)
	defer tick.Stop()
	warned := false
	for {
		a, err := g.cfg.Backend.Answer(ctx, t.Session, t.ID)
		switch {
		case err == nil && a.Done:
			return a, true
		case err != nil && !warned && ctx.Err() == nil:
			warned = true
			g.cfg.Log.Warn("an Ask Agent turn could not be read; trying again", "turn", t.ID, "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return Answer{}, false
		case <-tick.C:
		}
		pause = min(2*pause, AnswerPollSlowest*g.cfg.AnswerPoll)
		tick.Reset(pause)
	}
}

// working shows the typing indicator while a turn runs and sends one interim
// message when the turn lasts longer than InterimAfter. The returned function
// stops both.
func (g *Gateway) working(ctx context.Context, chat int64) (stop func()) {
	c, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = g.client.SendChatAction(c, chat)
		typing := time.NewTicker(g.cfg.TypingEvery)
		defer typing.Stop()
		interim := time.NewTimer(g.cfg.InterimAfter)
		defer interim.Stop()
		for {
			select {
			case <-c.Done():
				return
			case <-typing.C:
				_ = g.client.SendChatAction(c, chat)
			case <-interim.C:
				_, _ = g.sender.Send(c, chat, interimText, nil, false)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// sendPages sends plain pages in order.
func (g *Gateway) sendPages(ctx context.Context, chat int64, pages []string) error {
	for _, p := range pages {
		if err := g.reply(ctx, chat, p, nil); err != nil {
			return err
		}
	}
	return nil
}
