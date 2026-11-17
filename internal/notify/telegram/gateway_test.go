package telegram

import (
	"applysmith/internal/notify"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTelegram_Auth_StrangerGetsNoReplyAndIsCounted(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	r.handle(msgUpdate(555, 666, "private", "/tracker"))
	r.handle(msgUpdate(ownerChat, 666, "private", "/tracker")) // right chat, wrong user
	if r.bot.sentCount() != 0 {
		t.Errorf("a stranger got %d replies", r.bot.sentCount())
	}
	if n := r.state.Unauthorized(bg); n != 2 {
		t.Errorf("unauthorized attempts = %d", n)
	}
}

func TestTelegram_Auth_PrivateChatAndExactPair(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	r.handle(msgUpdate(-100, ownerUser, "supergroup", "/tracker"))
	r.handle(msgUpdate(-100, ownerUser, "group", "hello"))
	r.handle(msgUpdate(-100, ownerUser, "channel", "/status"))
	if r.bot.sentCount() != 0 {
		t.Errorf("a group message was answered: %v", r.bot.sentTexts())
	}
	r.handle(ownerMsg("/tracker"))
	if r.bot.sentCount() != 1 {
		t.Errorf("the owner was not answered")
	}
}

func TestTelegram_Auth_UnpairedGatewayIgnoresEverything(t *testing.T) {
	r := newRig(t)
	r.setToken()
	r.serve()
	for _, text := range []string{"/tracker", "/status", "hello", "/start", "0123456789"} {
		r.handle(ownerMsg(text))
	}
	if r.bot.sentCount() != 0 {
		t.Errorf("the unpaired gateway replied: %v", r.bot.sentTexts())
	}
	if _, ok := r.state.Owner(bg); ok {
		t.Error("an owner appeared")
	}
}

func TestTelegram_Pairing_ClaimThroughTheGatewayConfirmsAndNotifies(t *testing.T) {
	notified := 0
	r := newRig(t, func(c *Config) { c.OnPaired = func() { notified++ } })
	r.setToken()
	r.serve()
	code, _ := r.state.BeginPairing(bg)
	// A group cannot claim.
	r.handle(msgUpdate(-5, 77, "group", code.Code))
	if _, ok := r.state.Owner(bg); ok {
		t.Fatal("a group claimed the pairing")
	}
	r.handle(msgUpdate(ownerChat, ownerUser, "private", "/start "+code.Code))
	o, ok := r.state.Owner(bg)
	if !ok || o.ChatID != ownerChat || o.UserID != ownerUser {
		t.Fatalf("owner = %+v", o)
	}
	if m := r.bot.lastSent(); r.bot.sentCount() != 1 || !strings.Contains(m.Text, "Paired") || m.Markup == nil || notified != 1 {
		t.Errorf("confirmation %v, notified %d", r.bot.sentTexts(), notified)
	}
}

func TestTelegram_Auth_CallbackFromAnyoneButTheOwnerGetsAnEmptyAnswer(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	u := cbUpdate(666, "menu:status", "x")
	r.handle(u)
	r.bot.mu.Lock()
	defer r.bot.mu.Unlock()
	if len(r.bot.answers) != 1 || r.bot.answerTexts[0] != "" || len(r.bot.sent) != 0 || len(r.bot.edits) != 0 {
		t.Errorf("answers %v texts %v sent %d", r.bot.answers, r.bot.answerTexts, len(r.bot.sent))
	}
}

func TestTelegram_Poll_409BackoffDoublesToSixtyWithJitter(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.bot.set(func() { r.bot.conflicts = 8 })
	r.run()
	// The wait after the last conflict is recorded after the fake has
	// answered it, so wait for the eighth wait rather than for the counter.
	var waits []time.Duration
	eventually(t, "the conflicts to be waited out", func() bool {
		waits = waits[:0]
		for _, d := range r.clk.slept() {
			if d > time.Second {
				waits = append(waits, d)
			}
		}
		return len(waits) >= 8
	})
	if len(waits) < 8 {
		t.Fatalf("waits = %v", waits)
	}
	base := []time.Duration{5, 10, 20, 40, 60, 60, 60, 60}
	for i, b := range base {
		lo, hi := b*time.Second, b*time.Second+b*time.Second/4
		if waits[i] < lo || waits[i] > hi {
			t.Errorf("wait %d = %v, want %v..%v", i, waits[i], lo, hi)
		}
	}
	eventually(t, "a healthy poller again", func() bool { return r.gw.Health() == "healthy" })
}

// A Bot API host that never answers costs a call its deadline, not the sender:
// the next message still goes out.
func TestTelegram_Client_AHungCallEndsAtItsDeadline(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	c := NewClient(srv.URL, func() (string, error) { return fakeToken(), nil }, nil)
	c.timeout, c.pollTimeout = 30*time.Millisecond, 60*time.Millisecond
	for name, call := range map[string]func() error{
		"sendMessage":         func() error { _, err := c.SendMessage(bg, ownerChat, "hello", "", 0, nil); return err },
		"editMessageText":     func() error { return c.EditMessageText(bg, ownerChat, 1, "hello", nil) },
		"answerCallbackQuery": func() error { return c.AnswerCallbackQuery(bg, "cb", "") },
		"sendChatAction":      func() error { return c.SendChatAction(bg, ownerChat) },
		"getUpdates":          func() error { _, err := c.GetUpdates(bg, 0); return err },
	} {
		done := make(chan error, 1)
		go func() { done <- call() }()
		select {
		case err := <-done:
			if err == nil || strings.Contains(err.Error(), fakeToken()) {
				t.Errorf("%s against a host that never answers: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s is still waiting for a host that never answers", name)
		}
	}
	if CallTimeout <= 0 || LongPollTimeout <= LongPollSeconds*time.Second {
		t.Errorf("deadlines %v and %v: a long poll needs longer than the %d s it asks Telegram to wait", CallTimeout, LongPollTimeout, LongPollSeconds)
	}
}

func TestTelegram_Lease_StaleGenerationRenewFencesPoller(t *testing.T) {
	r := newRig(t)
	a, b := r.state.NewLease("a"), r.state.NewLease("b")
	a.Acquire(bg)
	r.clk.advance(LeaseTTL + time.Second)
	b.Acquire(bg)
	if err := a.Renew(bg); !errors.Is(err, ErrFenced) {
		t.Fatalf("a's renewal: %v", err)
	}
	if err := b.Renew(bg); err != nil {
		t.Errorf("b's renewal: %v", err)
	}
}

func TestTelegram_Status_RevokedTokenReadsRefusedUntilAPollSucceeds(t *testing.T) {
	r := newRig(t, func(c *Config) { c.Pause = time.Millisecond })
	r.setToken()
	r.run()
	eventually(t, "a healthy poller", func() bool { return r.gw.Health() == "healthy" })
	r.bot.mu.Lock()
	r.bot.token = "revoked"
	r.bot.mu.Unlock()
	eventually(t, "the refusal to show", func() bool { return r.gw.Health() == "refused" })
	r.bot.mu.Lock()
	r.bot.token = fakeToken()
	r.bot.mu.Unlock()
	r.gw.Reconfigure()
	eventually(t, "a healthy poller again", func() bool { return r.gw.Health() == "healthy" })
}

func TestTelegram_Sink_AnnouncementLinksItsPageOnlyWhileRemoteIsOn(t *testing.T) {
	base, on := "https://example.trycloudflare.com/", false
	r := newRig(t, func(c *Config) { c.LinkBase = func() (string, bool) { return base, on } })
	r.pair()
	r.serve()
	sink := r.gw.Sink()
	a := notify.Announcement{Title: "Evaluate Engineer at Acme", Body: "It finished.", Link: "/runs/r1"}
	if _, err := sink.Announce(bg, a); err != nil {
		t.Fatal(err)
	}
	on = true
	if _, err := sink.Announce(bg, a); err != nil {
		t.Fatal(err)
	}
	got := r.bot.sentTexts()
	if len(got) != 2 || got[0] != "Evaluate Engineer at Acme\nIt finished." || got[1] != got[0]+"\nhttps://example.trycloudflare.com/runs/r1" {
		t.Errorf("sent = %q", got)
	}
}

func TestTelegram_Receipts_CrashMidHandlingRetriesOnceDuplicateSuppressed(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	u := ownerMsg("/tracker")
	// The process stopped while the update was being handled: its receipt says
	// processing and nothing was sent.
	if skip, err := r.state.Begin(bg, u.UpdateID, ""); err != nil || skip {
		t.Fatal(skip, err)
	}
	if !r.handle(u) {
		t.Fatal("the update was not handled after the restart")
	}
	if r.bot.sentCount() != 1 {
		t.Errorf("replies = %d", r.bot.sentCount())
	}
	// Telegram delivers it again: it is processed, so it is ignored.
	if !r.handle(u) || r.bot.sentCount() != 1 {
		t.Errorf("a duplicate was handled: %d replies", r.bot.sentCount())
	}
	if r.state.ReceiptState(bg, u.UpdateID) != ReceiptProcessed {
		t.Errorf("state = %s", r.state.ReceiptState(bg, u.UpdateID))
	}
}

func TestTelegram_Receipts_ThirdFailureGoesTerminalWithOneMessage(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	r.backend.askErr = errors.New("the model is down")
	u := ownerMsg("what is in my tracker")
	if r.handle(u) || r.handle(u) {
		t.Fatal("a failed update let the offset move before its third attempt")
	}
	if r.state.ReceiptState(bg, u.UpdateID) != ReceiptRetryable {
		t.Errorf("state = %s", r.state.ReceiptState(bg, u.UpdateID))
	}
	if !r.handle(u) {
		t.Fatal("the third failure did not release the update")
	}
	if r.state.ReceiptState(bg, u.UpdateID) != ReceiptTerminal {
		t.Errorf("state = %s", r.state.ReceiptState(bg, u.UpdateID))
	}
	if r.bot.sentCount() != 1 || !strings.Contains(r.bot.lastSent().Text, "failed") {
		t.Errorf("messages = %v", r.bot.sentTexts())
	}
	r.handle(u)
	if r.bot.sentCount() != 1 {
		t.Error("a terminal update was handled again")
	}
}

func TestTelegram_Receipts_CallbackDedupById(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	u := cbUpdate(ownerUser, "scan:greenhouse", "Which scan?")
	r.handle(u)
	again := u
	again.UpdateID += 1000 // the same tap arrives under another update id
	r.handle(again)
	if len(r.backend.started) != 1 {
		t.Errorf("scans started = %d", len(r.backend.started))
	}
}

func TestTelegram_Privacy_EventRowsHoldNoMessageText(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	r.handle(ownerMsg("hello this is a private sentence about my salary"))
	r.handle(msgUpdate(5, 6, "private", "another private sentence"))
	for _, table := range []string{"telegram_events", "telegram_receipts", "telegram_chats"} {
		rows, err := r.st.Read().Query(`SELECT * FROM ` + table)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			rows.Scan(ptrs...)
			for _, v := range vals {
				if s, ok := v.(string); ok && (strings.Contains(s, "salary") || strings.Contains(s, "sentence")) {
					t.Errorf("%s stores message text: %q", table, s)
				}
			}
		}
		rows.Close()
	}
	if got := strings.Join(r.actions(), ","); got != "text,unauthorized" {
		t.Errorf("actions = %s", got)
	}
}

func TestTelegram_Privacy_StatusLeaksNoTokenOrIds(t *testing.T) {
	r := newRig(t)
	r.pair()
	st := r.gw.Status(bg)
	if !st.TokenSet || !st.Paired {
		t.Fatalf("status = %+v", st)
	}
	dump := strings.Join([]string{st.Bot, st.Health}, " ")
	for _, bad := range []string{fakeToken(), "1001", "2002", "123456789"} {
		if strings.Contains(dump, bad) {
			t.Errorf("status leaks %q", bad)
		}
	}
}

// openTurns counts the turns whose answer is not in the chat yet.
func (r *rig) openTurns() int {
	r.t.Helper()
	open, err := r.state.OpenTurns(bg)
	if err != nil {
		r.t.Fatal(err)
	}
	return len(open)
}

// While the agent works on a message, the bot keeps answering: a command and
// a button are handled before the turn ends, and the answer follows.
func TestTelegram_Ask_TheBotAnswersCommandsAndButtonsWhileATurnRuns(t *testing.T) {
	r := newRig(t)
	r.pair()
	gate := make(chan struct{})
	r.backend.gate = gate
	r.backend.answer = "Three are in interview."
	r.run()
	r.bot.push(ownerMsg("how many interviews?"))
	eventually(t, "the turn to start", func() bool { return len(r.backend.askList()) == 1 })
	r.bot.push(ownerMsg("/status"))
	r.bot.push(cbUpdate(ownerUser, "ack:ent_1", "Evaluate Engineer at Acme"))
	eventually(t, "the command and the button to be answered", func() bool {
		r.backend.mu.Lock()
		defer r.backend.mu.Unlock()
		return slices.Contains(r.bot.sentTexts(), "status: ok") && len(r.backend.acks) == 1
	})
	if r.openTurns() != 1 || slices.Contains(r.bot.sentTexts(), "Three are in interview.") {
		t.Fatalf("the turn ended early: open %d, sent %q", r.openTurns(), r.bot.sentTexts())
	}
	close(gate)
	eventually(t, "the answer", func() bool { return slices.Contains(r.bot.sentTexts(), "Three are in interview.") })
	eventually(t, "the turn to be done", func() bool { return r.openTurns() == 0 })
}

func TestTelegram_Ask_ARestartSendsTheOwedAnswerOnce(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	gate := make(chan struct{})
	r.backend.gate = gate
	r.backend.answer = "Your next interview is on Friday."
	u := ownerMsg("when is my next interview?")
	if !r.handle(u) {
		t.Fatal("the question was not handled")
	}
	r.restart()
	close(gate)
	if r.openTurns() != 1 || r.bot.sentCount() != 0 {
		t.Fatalf("before the start: open %d, sent %q", r.openTurns(), r.bot.sentTexts())
	}
	r.run()
	eventually(t, "the owed answer", func() bool { return r.openTurns() == 0 })
	sent := r.bot.sent
	if len(sent) != 1 || sent[0].Text != "Your next interview is on Friday." || sent[0].ReplyTo != u.Message.MessageID {
		t.Fatalf("sent %+v", sent)
	}
	if r.backend.askCalls != 1 {
		t.Errorf("the agent was asked %d times", r.backend.askCalls)
	}
	// The answer belongs to its conversation: a reply to it goes there.
	if a, ok := r.state.AnchorOf(bg, ownerChat, sent[0].ID); !ok || a.Session != "sess-1" {
		t.Errorf("anchor = %+v %v", a, ok)
	}
}

// /stop cancels the chat's running turn, and says so when nothing runs.
func TestTelegram_Ask_StopCancelsTheRunningTurn(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	r.handle(ownerMsg("/stop"))
	if got := r.bot.lastSent().Text; got != idleText {
		t.Fatalf("stop with nothing running = %q", got)
	}
	r.backend.gate = make(chan struct{})
	r.handle(ownerMsg("draft my cover letters for every role"))
	r.handle(ownerMsg("/stop"))
	r.settle()
	if !slices.Equal(r.backend.stops, []string{"sess-1"}) {
		t.Errorf("stops = %v", r.backend.stops)
	}
	texts := r.bot.sentTexts()
	if len(texts) != 3 || texts[1] != stoppedText || texts[2] != "Stopped." {
		t.Errorf("sent %q", texts)
	}
}

// replyTo is the owner's message replying to a message the bot sent.
func replyTo(sent sentMsg, text string) Update {
	u := ownerMsg(text)
	u.Message.ReplyTo = &Message{MessageID: sent.ID, Chat: Chat{ID: ownerChat, Type: "private"}, Text: sent.Text}
	return u
}

// A delegated task asks on the phone; the owner replies to it there, and the
// reply reaches the task's conversation with the question quoted, while the
// chat's own conversation stays as it was.
func TestTelegram_Reply_ToATaskQuestionReachesTheTasksConversation(t *testing.T) {
	r := newRig(t, func(c *Config) {
		c.Conversation = func(_ context.Context, a notify.Announcement) string {
			if a.Kind == notify.EventTaskUpdate {
				return "sess-task"
			}
			return ""
		}
	})
	r.pair()
	r.serve()
	r.handle(ownerMsg("good morning"))
	r.settle()
	if _, err := r.gw.Sink().Announce(bg, notify.Announcement{ID: 1, Kind: notify.EventTaskUpdate, Title: "A task needs you: follow-ups",
		Body: "Which of the three should go first?", Link: "/ask?conversation=sess-task"}); err != nil {
		t.Fatal(err)
	}
	question := r.bot.lastSent()
	r.handle(replyTo(question, "Acme first, then Beta"))
	r.settle()
	asks := r.backend.askList()
	got := asks[len(asks)-1]
	if got.SessionID != "sess-task" {
		t.Fatalf("the reply went to %q", got.SessionID)
	}
	want := "Acme first, then Beta\n\n> Replying to your message:\n> A task needs you: follow-ups\n> Which of the three should go first?"
	if got.Text != want {
		t.Errorf("text = %q\nwant  %q", got.Text, want)
	}
	if sid, _ := r.state.ChatSession(bg, ownerChat); sid != "sess-1" {
		t.Errorf("the chat's own conversation became %q", sid)
	}
	// The answer replies to the reply and belongs to the task's conversation.
	ans := r.bot.lastSent()
	if a, ok := r.state.AnchorOf(bg, ownerChat, ans.ID); !ok || a.Session != "sess-task" {
		t.Errorf("anchor of the answer = %+v %v", a, ok)
	}
	// A message that is not a reply still goes to the chat's own conversation.
	r.handle(ownerMsg("and what else is new?"))
	r.settle()
	if asks := r.backend.askList(); asks[len(asks)-1].SessionID != "sess-1" {
		t.Errorf("a plain message went to %q", asks[len(asks)-1].SessionID)
	}
}

// A reply to a mail alert names its thread, so the agent reads the mail with
// its own tool instead of taking the alert's words as the owner's.
func TestTelegram_Reply_ToAMailAlertNamesItsThread(t *testing.T) {
	r := newRig(t, func(c *Config) {
		c.Mail = func(context.Context, string, string) (string, []string) { return "18f3a2b", []string{MailDone} }
	})
	r.pair()
	r.serve()
	if _, err := r.gw.Sink().Announce(bg, notify.Announcement{ID: 2, Kind: notify.EventGmailAlert, Title: "Acme asks for your availability",
		Body: "Reply by Friday.", Link: "/gmail?thread=18f3a2b"}); err != nil {
		t.Fatal(err)
	}
	r.handle(replyTo(r.bot.lastSent(), "draft a reply saying Tuesday works"))
	r.settle()
	got := r.backend.askList()[0]
	if got.SessionID != "" || !strings.HasSuffix(got.Text, "\n> Reply by Friday.\n> (Gmail thread 18f3a2b)") || !strings.HasPrefix(got.Text, "draft a reply saying Tuesday works\n\n> ") {
		t.Errorf("ask = %+v", got)
	}
}

// A reply to an answer continues that answer's conversation, also after the
// chat moved on to a new one; a reply to a message the bot never anchored
// goes to the chat's own conversation with the message quoted.
func TestTelegram_Reply_ToAnAnswerContinuesItsConversation(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	if err := r.state.SetAnchor(bg, ownerChat, 4242, Anchor{Session: "sess-old"}); err != nil {
		t.Fatal(err)
	}
	r.handle(replyTo(sentMsg{ID: 4242, Text: "You have three interviews this week."}, "which is first?"))
	r.handle(replyTo(sentMsg{ID: 9999, Text: "Status\nRuns: 0 working"}, "why nothing?"))
	r.settle()
	asks := r.backend.askList()
	if len(asks) != 2 || asks[0].SessionID != "sess-old" || !strings.Contains(asks[0].Text, "> You have three interviews this week.") {
		t.Errorf("reply to an answer = %+v", asks[0])
	}
	if asks[1].SessionID != "" || asks[1].Text != "why nothing?\n\n> Replying to your message:\n> Status\n> Runs: 0 working" {
		t.Errorf("reply to an unanchored message = %+v", asks[1])
	}
}

// Telegram unreachable for longer than a few tries loses no answer: the
// follower keeps trying, backing off to a minute. A refusal (the owner
// blocked the bot) gives it up.
func TestTelegram_Ask_AnOutageDelaysTheAnswerAndARefusalGivesItUp(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	r.backend.answer = "Three are in interview."
	r.bot.set(func() { r.bot.failSends = 12 })
	r.handle(ownerMsg("how many interviews?"))
	r.settle()
	if texts := r.bot.sentTexts(); len(texts) != 1 || texts[0] != "Three are in interview." {
		t.Fatalf("after the outage: %q", texts)
	}
	slept := r.clk.slept()
	if longest := slices.Max(slept); longest < FailureCap || longest > FailureCap*5/4 {
		t.Errorf("the backoff reached %v, want about %v", longest, FailureCap)
	}
	r.bot.set(func() { r.bot.forbid = 1 })
	r.handle(ownerMsg("and offers?"))
	r.settle()
	if n := r.bot.sentCount(); n != 1 {
		t.Errorf("a refused answer was sent after all: %q", r.bot.sentTexts())
	}
}

// Unpairing drops what the chat was still owed: the answer of a turn that
// ends afterwards goes nowhere, and nothing waits for the next start.
func TestTelegram_Ask_UnpairingDropsTheAnswerStillOwed(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	gate := make(chan struct{})
	r.backend.gate = gate
	r.backend.answer = "Your offers: Acme."
	r.handle(ownerMsg("what offers do I have?"))
	if err := r.state.Unpair(bg); err != nil {
		t.Fatal(err)
	}
	if r.openTurns() != 0 {
		t.Fatal("an owed answer survived the unpairing")
	}
	close(gate)
	eventually(t, "the follower to end", func() bool {
		r.gw.mu.Lock()
		defer r.gw.mu.Unlock()
		return len(r.gw.following) == 0
	})
	if r.bot.sentCount() != 0 {
		t.Errorf("the unpaired chat got %q", r.bot.sentTexts())
	}
}

// A follower that starts from an old copy of a delivered turn sends nothing
// again: the turn's record says it is done.
func TestTelegram_Ask_AStaleFollowerResendsNothing(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	u := ownerMsg("first question")
	r.handle(u)
	r.settle()
	stale, ok := r.state.TurnOfUpdate(bg, u.UpdateID)
	if !ok || r.bot.sentCount() != 1 {
		t.Fatalf("turn %+v %v, sent %d", stale, ok, r.bot.sentCount())
	}
	stale.Sent = 0
	r.gw.follow(stale)
	eventually(t, "the stale follower to end", func() bool {
		r.gw.mu.Lock()
		defer r.gw.mu.Unlock()
		return len(r.gw.following) == 0
	})
	if n := r.bot.sentCount(); n != 1 {
		t.Errorf("sent %d times: %q", n, r.bot.sentTexts())
	}
}

func TestTelegram_Receipts_RetentionFourteenDays(t *testing.T) {
	r := newRig(t)
	r.state.Begin(bg, 1, "")
	r.state.Complete(bg, 1, "x", 1)
	r.clk.advance(13 * 24 * time.Hour)
	r.state.Begin(bg, 2, "")
	r.state.Complete(bg, 2, "x", 1)
	r.clk.advance(2 * 24 * time.Hour)
	if err := r.state.Retain(bg); err != nil {
		t.Fatal(err)
	}
	if r.state.ReceiptState(bg, 1) != "" || r.state.ReceiptState(bg, 2) == "" {
		t.Errorf("receipts: %q %q", r.state.ReceiptState(bg, 1), r.state.ReceiptState(bg, 2))
	}
}

// parkingClock holds a conflict back-off sleep until the poller is stopped.
// The fake clock otherwise runs through the back-offs in milliseconds, so the
// other process's lease expires, the poller takes it back and the fenced state
// lasts only a few milliseconds: a test polling for it misses it on a loaded
// machine.
type parkingClock struct {
	Clock
	park atomic.Bool
}

func (c *parkingClock) Sleep(ctx context.Context, d time.Duration) error {
	if c.park.Load() && d >= ConflictBase {
		<-ctx.Done()
		return ctx.Err()
	}
	return c.Clock.Sleep(ctx, d)
}

func TestTelegram_Lease_FencedPollerStopsPolling(t *testing.T) {
	var clock *parkingClock
	r := newRig(t, func(c *Config) { clock = &parkingClock{Clock: c.Clock}; c.Clock = clock })
	r.pair()
	r.run()
	eventually(t, "polling", func() bool { r.bot.mu.Lock(); defer r.bot.mu.Unlock(); return r.bot.polls >= 1 })
	// Another process takes the lease after it expired. Holding the fake
	// Bot API stalls the poller inside getUpdates, so it renews at most once
	// more; each attempt lets the lease expire again before acquiring.
	other := r.state.NewLease("gw-b")
	r.bot.mu.Lock()
	eventually(t, "the other process to take the lease", func() bool {
		r.clk.advance(LeaseTTL + time.Second)
		ok, _ := other.Acquire(bg)
		return ok
	})
	// The fenced poller waits out its conflict back-off instead of racing
	// through it and taking the lease back.
	clock.park.Store(true)
	r.bot.mu.Unlock()
	eventually(t, "the poller to be fenced", func() bool {
		s := r.gw.Health()
		return s == "conflicted" || s == "stopped"
	})
	// It stays fenced and makes no further request: the other process still
	// holds the lease.
	r.bot.mu.Lock()
	before := r.bot.polls
	r.bot.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	r.bot.mu.Lock()
	after := r.bot.polls
	r.bot.mu.Unlock()
	if s := r.gw.Health(); (s != "conflicted" && s != "stopped") || after != before {
		t.Errorf("the fenced poller is %s and polled %d more times", s, after-before)
	}
}
