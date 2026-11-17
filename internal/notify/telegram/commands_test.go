package telegram

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"applysmith/internal/notify"
)

func TestTelegram_Commands_ReadCommandsUseTheViews(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	for cmd, want := range map[string]string{"/status": "status: ok", "/tracker": "tracker: 3", "/pipeline": "pipeline: 12", "/queue": "queue: 4",
		"/gmail": "gmail: quiet", "/inbox": "inbox: nothing", "/today": "today: 2 found", "/open tracker": "open tracker"} {
		r.handle(ownerMsg(cmd))
		if got := r.bot.lastSent().Text; got != want && !strings.Contains(got, want) {
			t.Errorf("%s answered %q", cmd, got)
		}
	}
}

func TestTelegram_Commands_PlanNeverSubmits(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	r.handle(ownerMsg("/plan https://jobs.example.com/a Engineer at Example"))
	asks := r.backend.askList()
	if len(asks) != 1 || !strings.Contains(asks[0].Text, "Never submit") || !strings.Contains(asks[0].Text, "jobs.example.com") {
		t.Errorf("asks = %+v", asks)
	}
	r.handle(ownerMsg("/answer Why do you want to work here?"))
	if asks = r.backend.askList(); len(asks) != 2 || !strings.Contains(asks[1].Text, "Why do you want") {
		t.Errorf("asks = %+v", asks)
	}
}

func TestTelegram_Commands_ReplyAtMost3700Characters(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	r.backend.status = strings.Repeat("line of status text\n", 400)
	r.handle(ownerMsg("/status"))
	if n := len([]rune(r.bot.lastSent().Text)); n > 3700 {
		t.Errorf("reply has %d characters", n)
	}
}

func TestTelegram_FreeText_GoesToAskAgentOnTelegramChannel(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	r.backend.answer = "You have three applications in interview."
	u := ownerMsg("how many applications are in interview?")
	if !r.handle(u) {
		t.Fatal("the update was not handled")
	}
	r.settle()
	asks := r.backend.askList()
	m := r.bot.lastSent()
	if len(asks) != 1 || asks[0].Text != "how many applications are in interview?" || m.Text != r.backend.answer {
		t.Errorf("asks %+v, reply %q", asks, m.Text)
	}
	// The answer replies to the question, so it reads in place in a busy chat.
	if m.ReplyTo != u.Message.MessageID {
		t.Errorf("the answer replies to %d, want %d", m.ReplyTo, u.Message.MessageID)
	}
}

func TestTelegram_FreeText_AnswerIsFormattedAndPlainWhenTelegramRefusesIt(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	r.backend.answer = "**Two** interviews:\n- Acme <onsite>\n- Beta"
	if !r.handle(ownerMsg("interviews?")) {
		t.Fatal("the question was not handled")
	}
	r.settle()
	if m := r.bot.lastSent(); m.ParseMode != "HTML" || m.Text != "<b>Two</b> interviews:\n\n• Acme &lt;onsite&gt;\n• Beta" {
		t.Errorf("formatted answer = %+v", m)
	}
	r.bot.set(func() { r.bot.refuseHTML = 1 })
	if !r.handle(ownerMsg("and again?")) {
		t.Fatal("the second question was not handled")
	}
	r.settle()
	if m := r.bot.lastSent(); m.ParseMode != "" || m.Text != "Two interviews:\n\n• Acme <onsite>\n• Beta" || r.bot.sentCount() != 2 {
		t.Errorf("after the refusal = %+v (%d sent)", m, r.bot.sentCount())
	}
}

func TestTelegram_FreeText_LongAnswerFourPagesCappedAtFive(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	var b strings.Builder
	for b.Len() < 12000 {
		b.WriteString("This is one line of a long answer about the search.\n")
	}
	r.backend.answer = b.String()[:12000]
	r.handle(ownerMsg("tell me everything"))
	r.settle()
	texts := r.bot.sentTexts()
	if len(texts) != 4 {
		t.Fatalf("pages = %d", len(texts))
	}
	for i, s := range texts {
		if n := len([]rune(s)); n > 3700 {
			t.Errorf("page %d has %d characters", i, n)
		}
	}
	// Far longer: five pages and a pointer.
	r.backend.answer = strings.Repeat("word ", 10000)
	r.handle(ownerMsg("tell me even more"))
	r.settle()
	texts = r.bot.sentTexts()[4:]
	if len(texts) != 5 || !strings.Contains(texts[4], "dashboard") {
		t.Errorf("capped pages = %d, last %q", len(texts), texts[len(texts)-1])
	}
	for _, s := range texts {
		if n := len([]rune(s)); n > 3700 {
			t.Errorf("capped page has %d characters", n)
		}
	}
	// Cuts fall on a space, never inside a word.
	for _, s := range texts[:4] {
		if strings.HasSuffix(s, "wor") || strings.HasSuffix(s, "wo") {
			t.Errorf("page ends inside a word: %q", s[len(s)-6:])
		}
	}
}

func TestTelegram_FreeText_UrlOnlyOffersEvaluateAndPlan(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	r.handle(ownerMsg("https://jobs.example.com/openings/123"))
	if len(r.backend.askList()) != 0 || len(r.backend.evals) != 0 {
		t.Fatal("something ran before a button was tapped")
	}
	m := r.bot.lastSent()
	if m.Markup == nil || len(m.Markup.Rows[0]) != 2 || m.Markup.Rows[0][0].Text != "Evaluate" || m.Markup.Rows[0][1].Text != "Plan" {
		t.Fatalf("buttons = %+v", m.Markup)
	}
	for _, b := range m.Markup.Rows[0] {
		if len(b.Data) > 64 {
			t.Errorf("callback data %q is %d bytes", b.Data, len(b.Data))
		}
	}
	// Tapping Evaluate starts the evaluation; tapping it again is "already handled".
	tap := cbUpdate(ownerUser, m.Markup.Rows[0][0].Data, m.Text)
	r.handle(tap)
	if len(r.backend.evals) != 1 || r.backend.evals[0] != "https://jobs.example.com/openings/123" {
		t.Fatalf("evaluations = %v", r.backend.evals)
	}
	again := cbUpdate(ownerUser, m.Markup.Rows[0][0].Data, m.Text)
	r.handle(again)
	if len(r.backend.evals) != 1 {
		t.Error("the second tap started a second evaluation")
	}
	r.bot.mu.Lock()
	last := r.bot.answerTexts[len(r.bot.answerTexts)-1]
	r.bot.mu.Unlock()
	if !strings.Contains(last, "Already handled") {
		t.Errorf("second tap answered %q", last)
	}
}

// offered sends a posting's link and returns the message with its Evaluate and
// Plan buttons.
func (r *rig) offered() sentMsg {
	r.t.Helper()
	r.handle(ownerMsg("https://jobs.example.com/openings/123"))
	m := r.bot.lastSent()
	if m.Markup == nil || len(m.Markup.Rows[0]) != 2 {
		r.t.Fatalf("buttons = %+v", m.Markup)
	}
	return m
}

func (f *fakeBot) lastEdit() editedMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.edits) == 0 {
		return editedMsg{}
	}
	return f.edits[len(f.edits)-1]
}

func (f *fakeBot) lastToast() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.answerTexts) == 0 {
		return ""
	}
	return f.answerTexts[len(f.answerTexts)-1]
}

// The Plan button whose message could not be edited: Telegram gives the update
// again, and the retry finishes the edit of the plan that was asked for once.
func TestTelegram_Intent_APlanWhoseEditFailedIsFinishedByTheRetry(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	r.backend.answer = "The plan."
	m := r.offered()
	tap := cbUpdate(ownerUser, m.Markup.Rows[0][1].Data, m.Text)
	r.bot.set(func() { r.bot.failEdits = 1 })
	if r.handle(tap) {
		t.Fatal("a tap whose message could not be edited counted as handled")
	}
	if !r.handle(tap) {
		t.Fatal("the retry failed")
	}
	r.settle()
	if n := len(r.backend.askList()); n != 1 {
		t.Fatalf("the plan was asked for %d times, want once", n)
	}
	if e := r.bot.lastEdit(); !strings.Contains(e.Text, "Making a plan") || e.Markup == nil || len(e.Markup.Rows) != 0 {
		t.Errorf("the message reads %+v", e)
	}
	if got := r.bot.lastSent().Text; got != "The plan." {
		t.Errorf("the chat ends with %q, want the plan", got)
	}
	// Telegram had made the edit although its answer was lost: the same edit
	// is refused as not modified, which is the edit made.
	m = r.offered()
	tap = cbUpdate(ownerUser, m.Markup.Rows[0][1].Data, m.Text)
	r.bot.set(func() { r.bot.failEdits = 1 })
	r.handle(tap)
	r.bot.set(func() { r.bot.editStatus = 400 })
	if !r.handle(tap) {
		t.Error("an edit Telegram refuses as already made failed the retry")
	}
	r.bot.set(func() { r.bot.editStatus = 0 })
	r.settle()
	if n := len(r.backend.askList()); n != 2 {
		t.Errorf("two plans were asked for %d times", n-1)
	}
}

// The Plan button whose turn could not start keeps its token: the retry asks
// again and the plan arrives.
func TestTelegram_Intent_APlanThatCouldNotBeAskedForIsAskedForByTheRetry(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	r.backend.answer = "The plan."
	m := r.offered()
	tap := cbUpdate(ownerUser, m.Markup.Rows[0][1].Data, m.Text)
	r.backend.askErr = errors.New("the agent is not running")
	if r.handle(tap) {
		t.Fatal("a plan that was not asked for counted as handled")
	}
	if e := r.bot.lastEdit(); e.Text != "" {
		t.Errorf("the message was edited to %q before a plan was asked for", e.Text)
	}
	r.backend.askErr = nil
	if !r.handle(tap) {
		t.Fatal("the retry failed")
	}
	r.settle()
	if toast := r.bot.lastToast(); toast != "On it." {
		t.Errorf("the retry answered %q", toast)
	}
	if !strings.Contains(r.bot.lastEdit().Text, "Making a plan") || r.bot.lastSent().Text != "The plan." {
		t.Errorf("message %q, chat ends with %q", r.bot.lastEdit().Text, r.bot.lastSent().Text)
	}
}

// The Evaluate button has the same shape: an edit that failed is finished by
// the retry without a second evaluation, and an evaluation that could not
// start leaves the token for another tap.
func TestTelegram_Intent_AnEvaluationIsStartedOnceAndItsMessageSaysSo(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	m := r.offered()
	data := m.Markup.Rows[0][0].Data
	r.backend.evalErr = errors.New("the evaluator is not running")
	if !r.handle(cbUpdate(ownerUser, data, m.Text)) {
		t.Fatal("the tap failed")
	}
	if e := r.bot.lastEdit(); !strings.Contains(e.Text, "could not start") {
		t.Errorf("the message reads %q", e.Text)
	}
	r.backend.evalErr = nil
	tap := cbUpdate(ownerUser, data, m.Text)
	r.bot.set(func() { r.bot.failEdits = 1 })
	if r.handle(tap) {
		t.Fatal("a tap whose message could not be edited counted as handled")
	}
	if !r.handle(tap) {
		t.Fatal("the retry failed")
	}
	if n := len(r.backend.evals); n != 1 {
		t.Errorf("%d evaluations were started, want one", n)
	}
	if e := r.bot.lastEdit(); !strings.Contains(e.Text, "Evaluation started: /runs/run-eval") {
		t.Errorf("the message reads %q", e.Text)
	}
}

func TestTelegram_FreeText_AgentBusyAnswersHonestly(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	r.backend.askErr = ErrAgentBusy
	r.handle(ownerMsg("hello"))
	if got := r.bot.lastSent().Text; !strings.Contains(got, "still working") || !strings.Contains(got, "/stop") {
		t.Errorf("reply = %q", got)
	}
	open, _ := r.state.OpenTurns(bg)
	if len(open) != 0 {
		t.Errorf("a refused message left a turn: %+v", open)
	}
}

// A page Telegram refuses once is sent again by the follower; the update was
// handled when the turn started, so nothing asks the agent twice and no page
// goes out twice.
func TestTelegram_Delivery_APageThatFailsIsSentAgainWithoutRepeat(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	r.backend.answer = strings.Repeat("a line of text\n", 600) // three pages
	u := ownerMsg("long one")
	r.bot.set(func() { r.bot.failNth = 2 }) // the second page fails once
	if !r.handle(u) {
		t.Fatal("the question was not handled")
	}
	r.settle()
	if r.backend.askCalls != 1 {
		t.Errorf("the agent was asked %d times", r.backend.askCalls)
	}
	sent := r.bot.sent
	if len(sent) != 3 || sent[0].ReplyTo != u.Message.MessageID || sent[1].ReplyTo != 0 {
		t.Errorf("pages sent: %+v", sent)
	}
	if r.state.ReceiptState(bg, u.UpdateID) != ReceiptProcessed {
		t.Errorf("receipt = %s", r.state.ReceiptState(bg, u.UpdateID))
	}
}

func TestTelegram_Callbacks_ClosedFormsAndSixtyFourByteLimit(t *testing.T) {
	good := []string{"scan:greenhouse", "intent:eval:0123456789ab", "intent:plan:0123456789ab", "approve:conf_123", "decline:conf_123",
		"ack:ent_0123456789abcdef", "rate:item1:up", "menu:status"}
	for _, d := range good {
		if form, _ := ParseCallback(d); form == "" {
			t.Errorf("%q refused", d)
		}
	}
	bad := []string{"", "scan:", "scan:a b", "intent:eval:xyz", "intent:run:0123456789ab", "review:1", "plan:x", "gdr:1:2:u", "self-heal:x",
		"ack:" + strings.Repeat("x", 80), "menu:Status", "rate:item:sideways", "scan:green:house",
		// The Done button of an earlier build: no application stop reaches the
		// phone, so nothing resumes from it.
		"done:ent_0123456789abcdef"}
	for _, d := range bad {
		if form, _ := ParseCallback(d); form != "" {
			t.Errorf("%q accepted as %s", d, form)
		}
	}
}

func TestTelegram_Callbacks_ScanTapStartsRunWithLink(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	r.handle(cbUpdate(ownerUser, "scan:greenhouse", "Which scan should I start?"))
	if len(r.backend.started) != 1 || r.backend.started[0] != "greenhouse" {
		t.Fatalf("started = %v", r.backend.started)
	}
	r.bot.mu.Lock()
	defer r.bot.mu.Unlock()
	if len(r.bot.edits) != 1 || !strings.Contains(r.bot.edits[0].Text, "/runs/run-scan") {
		t.Errorf("edits = %+v", r.bot.edits)
	}
	// The keyboard is removed by the edit.
	if m := r.bot.edits[0].Markup; m == nil || len(m.Rows) != 0 {
		t.Errorf("the keyboard stayed: %+v", m)
	}
}

func TestTelegram_Callbacks_UnknownSourceAndUnsupportedFormsAreHonest(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	r.handle(cbUpdate(ownerUser, "scan:nowhere", "x"))
	if len(r.backend.started) != 0 {
		t.Error("an unlisted source started")
	}
	for _, d := range []string{"approve:conf_1", "rate:12.3:up"} {
		r.handle(cbUpdate(ownerUser, d, "x"))
	}
	r.bot.mu.Lock()
	defer r.bot.mu.Unlock()
	joined := strings.Join(r.bot.answerTexts, "|")
	for _, want := range []string{"not available", "confirmed in the dashboard", "Ratings are not available"} {
		if !strings.Contains(joined, want) {
			t.Errorf("toasts %q lack %q", joined, want)
		}
	}
	if len(r.bot.edits) != 0 {
		t.Error("an unsupported button edited the message")
	}
}

func TestTelegram_Callbacks_DigestRatingTapIsHandedToTheBackend(t *testing.T) {
	// A digest item reference is "<digest id>.<item number>".
	for _, d := range []string{"rate:12.3:up", "rate:12.8:down"} {
		if form, args := ParseCallback(d); form != FormRate || len(args) != 2 {
			t.Errorf("%q: %s %v", d, form, args)
		}
	}
	r := newRig(t)
	r.pair()
	r.serve()
	r.backend.ratings = true
	r.handle(cbUpdate(ownerUser, "rate:12.3:up", "Gmail digest"))
	r.backend.mu.Lock()
	rates := append([]string(nil), r.backend.rates...)
	r.backend.mu.Unlock()
	if len(rates) != 1 || rates[0] != "12.3:up" {
		t.Fatalf("rates = %v", rates)
	}
	r.bot.mu.Lock()
	if len(r.bot.edits) != 1 || !strings.HasSuffix(r.bot.edits[0].Text, "Done.") {
		t.Errorf("edits = %+v", r.bot.edits)
	} else if m := r.bot.edits[0].Markup; m == nil || len(m.Rows) != 0 {
		t.Errorf("the keyboard stayed after a tap: %+v", m)
	}
	r.bot.mu.Unlock()
	// A stale button is answered without claiming success.
	r.backend.rateErr = errors.New("that rating button is out of date")
	r.handle(cbUpdate(ownerUser, "rate:99.1:down", "Gmail digest"))
	r.bot.mu.Lock()
	defer r.bot.mu.Unlock()
	if last := r.bot.edits[len(r.bot.edits)-1].Text; !strings.Contains(last, "That did not work") || !strings.Contains(last, "out of date") {
		t.Errorf("stale tap: %q", last)
	}
}

func TestTelegram_Callbacks_TokenSingleUseExpiryAndRepairing(t *testing.T) {
	r := newRig(t)
	r.pair()
	tok, _ := r.state.IssueToken(bg, ownerChat, "eval", "u")
	if err := r.state.UseToken(bg, tok, ownerChat, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.state.UseToken(bg, tok, ownerChat, ""); !errors.Is(err, ErrTokenUsed) {
		t.Errorf("second use: %v", err)
	}
	old, _ := r.state.IssueToken(bg, ownerChat, "eval", "u")
	r.clk.advance(21 * time.Minute)
	if err := r.state.UseToken(bg, old, ownerChat, ""); !errors.Is(err, ErrTokenExpired) {
		t.Errorf("after 21 minutes: %v", err)
	}
	other, _ := r.state.IssueToken(bg, ownerChat, "eval", "u")
	if err := r.state.UseToken(bg, other, 999, ""); !errors.Is(err, ErrTokenUnknown) {
		t.Errorf("another chat: %v", err)
	}
	if err := r.state.UseToken(bg, "0123456789ab", ownerChat, ""); !errors.Is(err, ErrTokenUnknown) {
		t.Errorf("unknown token: %v", err)
	}
}

func TestTelegram_Callbacks_AckAcknowledgesTheEntryAndRemovesTheKeyboard(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	r.handle(cbUpdate(ownerUser, "ack:ent_abc", "Entry needs you"))
	if len(r.backend.acks) != 1 || r.backend.acks[0] != "ent_abc" {
		t.Errorf("acks = %v", r.backend.acks)
	}
	r.bot.mu.Lock()
	defer r.bot.mu.Unlock()
	if len(r.bot.edits) != 1 || !strings.Contains(r.bot.edits[0].Text, "Acknowledged") || len(r.bot.edits[0].Markup.Rows) != 0 {
		t.Errorf("edits = %+v", r.bot.edits)
	}
}

// TestTelegram_Limits_InboundTwentyPerMinuteOutboundQueueRetryAfter covers the
// three limits of NT-095: inbound messages, the outbound queue, and a 429.
func TestTelegram_Limits_InboundTwentyPerMinuteOutboundQueueRetryAfter(t *testing.T) {
	t.Run("inbound twenty per minute", limitsInbound)
	t.Run("outbound twenty per minute and retry_after", limitsOutboundPerMinuteAndRetryAfter)
}

func limitsInbound(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	at := r.clk.Now()
	for i := 0; i < 30; i++ {
		i := i
		r.gw.now = func() time.Time { return at.Add(time.Duration(i) * time.Second) } // 30 messages in 30 seconds
		r.handle(ownerMsg("/tracker"))
	}
	eventually(t, "the slow-down notice", func() bool { return r.bot.sentCount() == 21 })
	texts := r.bot.sentTexts()
	slow := 0
	for _, s := range texts {
		if strings.Contains(s, "Slow down") {
			slow++
		}
	}
	if len(texts)-slow != 20 || slow != 1 {
		t.Errorf("handled %d, slow-down notices %d", len(texts)-slow, slow)
	}
}

func limitsOutboundPerMinuteAndRetryAfter(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	sink := r.gw.Sink()
	for i := 0; i < 20; i++ {
		if _, err := sink.Announce(bg, notify.Announcement{Title: "m"}); err != nil {
			t.Fatal(err)
		}
		r.clk.advance(1500 * time.Millisecond) // so the per-second spacing never waits
	}
	start := r.clk.Now()
	if _, err := sink.Announce(bg, notify.Announcement{Title: "21st"}); err != nil {
		t.Fatal(err)
	}
	if waited := r.clk.Now().Sub(start); waited < 25*time.Second {
		t.Errorf("the 21st message in a minute waited only %v", waited)
	}
	// A 429 is waited out for the retry_after it states, then the same message goes.
	r.bot.set(func() { r.bot.retryAfter = 7 })
	start = r.clk.Now()
	before := r.bot.sentCount()
	if _, err := sink.Announce(bg, notify.Announcement{Title: "after 429"}); err != nil {
		t.Fatal(err)
	}
	if waited := r.clk.Now().Sub(start); waited < 7*time.Second {
		t.Errorf("waited %v after a 429 with retry_after 7", waited)
	}
	if r.bot.sentCount() != before+1 {
		t.Errorf("sent %d messages, want %d", r.bot.sentCount(), before+1)
	}
}

func TestTelegram_Sink_AnnounceCarriesAckButtonAndWithdrawEditsWithoutButtons(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	sink := r.gw.Sink()
	a := notify.Announcement{ID: 1, EntryID: "ent_abc", Title: "Apply to Example", Body: "Sign in to continue.", Urgent: true}
	ext, err := sink.Announce(bg, a)
	if err != nil {
		t.Fatal(err)
	}
	m := r.bot.lastSent()
	if m.Chat != ownerChat || m.Text != "Apply to Example\nSign in to continue." || m.Markup == nil || m.Markup.Rows[0][0].Data != "ack:ent_abc" {
		t.Errorf("sent = %+v", m)
	}
	if err := sink.Withdraw(bg, a, ext, notify.Handled); err != nil {
		t.Fatal(err)
	}
	r.bot.mu.Lock()
	e := r.bot.edits[0]
	r.bot.mu.Unlock()
	if e.Msg != m.ID || !strings.HasSuffix(e.Text, "Handled.") || e.Markup == nil || len(e.Markup.Rows) != 0 {
		t.Errorf("edit = %+v", e)
	}
	if err := sink.Withdraw(bg, a, ext, notify.Superseded); err != nil {
		t.Fatal(err)
	}
	// Telegram no longer allows the edit: skipped, not an error.
	r.bot.set(func() { r.bot.editStatus = 400 })
	if err := sink.Withdraw(bg, a, ext, notify.Handled); err != nil {
		t.Errorf("an edit Telegram refuses must be skipped: %v", err)
	}
}

// A digest whose later page could not be sent goes on from that page at the
// next attempt: the pages already in the chat are not sent again.
func TestTelegram_Sink_ADigestGoesOnAfterThePagesAlreadySent(t *testing.T) {
	pages := []string{"page one", "page two", "page three"}
	keyboard := &Markup{Rows: [][]Button{{{Text: "Useful", Data: "rate:1:up"}}}}
	r := newRig(t, func(c *Config) {
		c.Digest = func(context.Context, string) ([]string, *Markup, error) { return pages, keyboard, nil }
	})
	r.pair()
	r.serve()
	sink := r.gw.Sink()
	a := notify.Announcement{ID: 41, Kind: notify.EventGmailDigest, Title: "Digest", Link: "/gmail/digests/1"}
	r.bot.set(func() { r.bot.failNth = 2 })
	if _, err := sink.Announce(bg, a); err == nil {
		t.Fatal("the attempt whose second page failed reported a delivery")
	}
	if got := r.bot.sentTexts(); len(got) != 1 || got[0] != "page one" {
		t.Fatalf("after the failed attempt the chat holds %q", got)
	}
	ext, err := sink.Announce(bg, a)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.bot.sentTexts(), "|"); got != "page one|page two|page three" {
		t.Errorf("the chat holds %q", got)
	}
	last := r.bot.lastSent()
	if last.Markup == nil || ext != formatID(ownerChat, last.ID) {
		t.Errorf("last page %+v, handle %q", last, ext)
	}
	// Another digest starts at its own first page.
	if _, err := sink.Announce(bg, notify.Announcement{ID: 42, Kind: notify.EventGmailDigest, Title: "Digest", Link: "/gmail/digests/2"}); err != nil {
		t.Fatal(err)
	}
	if n := r.bot.sentCount(); n != 6 {
		t.Errorf("%d messages after a second digest, want 6", n)
	}
	// What an unpaired chat was owed is forgotten with it.
	r.bot.set(func() { r.bot.failNth = 2 })
	if _, err := sink.Announce(bg, notify.Announcement{ID: 43, Kind: notify.EventGmailDigest, Title: "Digest", Link: "/gmail/digests/3"}); err == nil {
		t.Fatal("the attempt whose second page failed reported a delivery")
	}
	if err := r.state.Unpair(bg); err != nil {
		t.Fatal(err)
	}
	if n, _ := r.state.PagesSent(bg, 43); n != 0 {
		t.Errorf("unpairing kept %d sent pages on record", n)
	}
}

func TestTelegram_Optional_NoTokenDesktopOnlyDoctorWarns(t *testing.T) {
	r := newRig(t)
	s := r.gw.Sink()
	if s.Available(bg) {
		t.Error("the sink is available without a token")
	}
	r.setToken()
	if s.Available(bg) {
		t.Error("the sink is available without a pairing")
	}
	if _, err := s.Announce(bg, notify.Announcement{Title: "x"}); !errors.Is(err, notify.ErrGone) {
		t.Errorf("announce to an unpaired chat: %v", err)
	}
	r.pair()
	if !s.Available(bg) {
		t.Error("the sink is not available when paired")
	}
}

func TestTelegram_ApprovalCard_OffersOnlyAOneTimeApprovalAndNeverForASubmit(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	card := ApprovalCard{ID: "conf_0123456789abcdef0123", Summary: "Evaluate a posting on boards.example.test", Purpose: "evaluate the two postings", Reason: "outside content was read", Tier: 3}
	if err := r.gw.SendApprovalCard(bg, card); err != nil {
		t.Fatal(err)
	}
	m := r.bot.lastSent()
	if m.Markup == nil || len(m.Markup.Rows) != 1 || len(m.Markup.Rows[0]) != 2 {
		t.Fatalf("keyboard = %+v", m.Markup)
	}
	approve, decline := m.Markup.Rows[0][0], m.Markup.Rows[0][1]
	if approve.Data != "approve:"+card.ID || decline.Data != "decline:"+card.ID || !strings.Contains(approve.Text, "once") {
		t.Errorf("buttons = %+v %+v", approve, decline)
	}
	for _, d := range []string{approve.Data, decline.Data} {
		if form, args := ParseCallback(d); form == "" || args[0] != card.ID {
			t.Errorf("%q does not parse", d)
		}
	}
	for _, want := range []string{"waiting for your confirmation", "evaluate the two postings", "Approving runs it once"} {
		if !strings.Contains(m.Text, want) {
			t.Errorf("card text lacks %q: %q", want, m.Text)
		}
	}
	if strings.Contains(strings.ToLower(m.Text), "today") || strings.Contains(strings.ToLower(m.Text), "conversation") {
		t.Errorf("the card hints at a wider scope: %q", m.Text)
	}

	submit := card
	submit.ID, submit.Tier = "conf_submit", 4
	if err := r.gw.SendApprovalCard(bg, submit); err != nil {
		t.Fatal(err)
	}
	m = r.bot.lastSent()
	if m.Markup != nil || !strings.Contains(m.Text, "dashboard") {
		t.Errorf("a held submit got buttons or no dashboard pointer: %+v", m)
	}
}

func TestTelegram_ApprovalCard_NothingIsSentWithoutAPairedChat(t *testing.T) {
	r := newRig(t)
	if err := r.gw.SendApprovalCard(bg, ApprovalCard{ID: "conf_1", Summary: "x", Tier: 2}); err != nil {
		t.Fatal(err)
	}
	if r.bot.sentCount() != 0 {
		t.Error("a card was sent with no paired chat")
	}
}

func TestTelegram_ApproveAndDeclineButtonsCallTheBackendOnceAndShowTheResult(t *testing.T) {
	r := newRig(t)
	r.backend.approvals = true
	r.backend.approveMsg = "Approved once. Result: done"
	r.pair()
	r.serve()
	r.handle(cbUpdate(ownerUser, "approve:conf_1", "Held action"))
	r.backend.approveMsg = "Declined. It did not run."
	r.handle(cbUpdate(ownerUser, "decline:conf_2", "Held action"))
	r.backend.mu.Lock()
	got := append([]string(nil), r.backend.approved...)
	r.backend.mu.Unlock()
	if len(got) != 2 || got[0] != "conf_1:true" || got[1] != "conf_2:false" {
		t.Fatalf("backend calls = %v", got)
	}
	r.bot.mu.Lock()
	defer r.bot.mu.Unlock()
	if len(r.bot.edits) != 2 || !strings.HasSuffix(r.bot.edits[0].Text, "Approved once. Result: done") || !strings.HasSuffix(r.bot.edits[1].Text, "Declined. It did not run.") {
		t.Fatalf("edits = %+v", r.bot.edits)
	}
	for _, e := range r.bot.edits {
		if e.Markup == nil || len(e.Markup.Rows) != 0 {
			t.Errorf("the keyboard stayed after a tap: %+v", e.Markup)
		}
	}
}

// An approval the backend refuses is shown as refused on the card.
func TestTelegram_ApproveRefusedByTheBackendIsShownAndNothingElseHappens(t *testing.T) {
	r := newRig(t)
	r.backend.approvals = true
	r.backend.approveErr = errors.New("that card expired or was already handled")
	r.pair()
	r.serve()
	r.handle(cbUpdate(ownerUser, "approve:conf_old", "Held action"))
	r.bot.mu.Lock()
	defer r.bot.mu.Unlock()
	if len(r.bot.edits) != 1 || !strings.Contains(r.bot.edits[0].Text, "expired or was already handled") {
		t.Fatalf("edits = %+v", r.bot.edits)
	}
}

func TestTelegram_ApproveFromAnotherAccountDoesNothing(t *testing.T) {
	r := newRig(t)
	r.backend.approvals = true
	r.pair()
	r.serve()
	r.handle(cbUpdate(ownerUser+1, "approve:conf_1", "Held action"))
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if len(r.backend.approved) != 0 {
		t.Fatalf("a stranger approved: %v", r.backend.approved)
	}
}

// The agent failing to start a turn fails the update, which Telegram gives
// again; a retried update whose turn started asks nothing again.
func TestTelegram_Delivery_ARetriedUpdateNeverAsksTwice(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	r.backend.answer = "You have three applications in interview."
	u := ownerMsg("how many applications are in interview?")
	r.backend.askErr = errors.New("the agent is not running")
	if r.handle(u) {
		t.Fatal("a turn that did not start let the update count as handled")
	}
	if got := r.state.ReceiptState(bg, u.UpdateID); got != ReceiptRetryable {
		t.Fatalf("receipt is %s, want %s", got, ReceiptRetryable)
	}
	r.backend.askErr = nil
	if !r.handle(u) {
		t.Fatal("the retry failed")
	}
	r.settle()
	// Telegram gives the update once more (the offset was lost in a crash).
	if err := r.st.Write(bg, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE telegram_receipts SET state = 'processing' WHERE update_id = ?`, u.UpdateID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !r.handle(u) {
		t.Fatal("the redelivery failed")
	}
	r.settle()
	if r.backend.askCalls != 2 || r.state.ReceiptState(bg, u.UpdateID) != ReceiptProcessed {
		t.Errorf("agent asked %d times (one refused), receipt %s", r.backend.askCalls, r.state.ReceiptState(bg, u.UpdateID))
	}
	if texts := r.bot.sentTexts(); len(texts) != 1 || texts[0] != r.backend.answer {
		t.Errorf("sent %q", texts)
	}
}

// The agent writes Markdown for the dashboard. Telegram shows bold, italics,
// code, links and preformatted text only, in its HTML subset, and refuses code
// nested inside another entity; the plain text says the same without markup.
func TestTelegram_Format_MarkdownBecomesTelegramHTMLAndPlainText(t *testing.T) {
	cases := []struct{ name, md, html, plain string }{
		{"heading, bold and code", "## Plan\n\n**Acme** has `onsite` tomorrow.",
			"<b>Plan</b>\n\n<b>Acme</b> has <code>onsite</code> tomorrow.", "Plan\n\nAcme has onsite tomorrow."},
		{"lists", "- one\n- *two*\n  1. sub\n  2. sub two",
			"• one\n• <i>two</i>\n  1. sub\n  2. sub two", "• one\n• two\n  1. sub\n  2. sub two"},
		{"table", "| Company | Stage |\n|---|---|\n| Acme | `onsite` |",
			"<b>Company · Stage</b>\nAcme · <code>onsite</code>", "Company · Stage\nAcme · onsite"},
		{"code inside bold", "**see `x` here**", "<b>see x here</b>", "see x here"},
		{"links", "[the report](https://e.test/r?a=1&b=2), [home](/home), [https://e.test](https://e.test)",
			`<a href="https://e.test/r?a=1&amp;b=2">the report</a>, home, <a href="https://e.test">https://e.test</a>`,
			"the report (https://e.test/r?a=1&b=2), home, https://e.test"},
		{"markup characters", "5 < 6 & <company> and 5 * 3", "5 &lt; 6 &amp; &lt;company&gt; and 5 * 3", "5 < 6 & <company> and 5 * 3"},
		{"code block", "Run:\n\n```\nx <y>\n```", "Run:\n\n<pre>x &lt;y&gt;</pre>", "Run:\n\nx <y>"},
	}
	for _, c := range cases {
		h, p := formatted(c.md)
		if h != c.html || p != c.plain {
			t.Errorf("%s:\nhtml  %q\nwant  %q\nplain %q\nwant  %q", c.name, h, c.html, p, c.plain)
		}
	}
}
