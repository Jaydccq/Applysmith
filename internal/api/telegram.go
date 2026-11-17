package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"applysmith/internal/agent"
	"applysmith/internal/agent/approvals"
	"applysmith/internal/agent/pirpc"
	chatsessions "applysmith/internal/agent/sessions"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/gmailapi"
	"applysmith/internal/api/wire"
	"applysmith/internal/browser"
	"applysmith/internal/discovery"
	"applysmith/internal/evaluate"
	"applysmith/internal/gmail"
	"applysmith/internal/gmail/classify"
	"applysmith/internal/gmail/lifecycle"
	"applysmith/internal/notify"
	"applysmith/internal/notify/telegram"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
)

// telegramBackend is what the Telegram gateway asks of the product. Every read
// builds its text from the read models the dashboard uses.
type telegramBackend struct{ s *Server }

// MaxInboxProbe reads past the ten /inbox lists so the answer can count the rest.
const MaxInboxProbe = 50

const (
	telegramPrincipal = "telegram"
	listLimit         = 5
)

// randomKey is an idempotency key for a run the gateway starts.
func randomKey() string {
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	return hex.EncodeToString(raw[:])
}

func (b telegramBackend) dom() (*domain, error) { return b.s.dom() }

// Ask stores the message in its conversation and starts the turn on the
// telegram channel; the gateway reads the answer with Answer. The channel's
// envelope is what stops a turn from Telegram from starting or submitting an
// application.
func (b telegramBackend) Ask(ctx context.Context, req telegram.AskRequest) (telegram.Asked, error) {
	d, err := b.dom()
	if err != nil {
		return telegram.Asked{}, err
	}
	sent, err := d.agent.Send(ctx, agent.SendRequest{SessionID: req.SessionID, Channel: agent.ChannelTelegram, Text: req.Text})
	if req.SessionID != "" && errors.Is(err, store.ErrNotFound) {
		// The conversation was deleted in the dashboard: the chat goes on in
		// a new one rather than failing until the next rollover.
		sent, err = d.agent.Send(ctx, agent.SendRequest{Channel: agent.ChannelTelegram, Text: req.Text})
	}
	switch {
	case errors.Is(err, agent.ErrTurnRunning):
		return telegram.Asked{}, telegram.ErrAgentBusy
	case errors.Is(err, agent.ErrMessageTooLong):
		return telegram.Asked{}, telegram.ErrMessageTooLong
	case err != nil:
		return telegram.Asked{}, err
	}
	return telegram.Asked{SessionID: sent.SessionID, TurnID: sent.TurnID}, nil
}

// Answer reads what a turn came to. A turn whose conversation was deleted
// meanwhile is done, and says so.
func (b telegramBackend) Answer(ctx context.Context, sessionID, turnID string) (telegram.Answer, error) {
	d, err := b.dom()
	if err != nil {
		return telegram.Answer{}, err
	}
	turns, err := d.sessions.Turns(ctx, sessionID)
	if err != nil {
		return telegram.Answer{}, err
	}
	for _, t := range turns {
		if t.ID == turnID {
			if t.Status == chatsessions.TurnRunning {
				return telegram.Answer{}, nil
			}
			return telegram.Answer{Done: true, Text: answerOf(ctx, d.sessions, sessionID, t)}, nil
		}
	}
	return telegram.Answer{Done: true, Text: "That conversation was deleted in the dashboard, so its answer is gone."}, nil
}

// Stop cancels the conversation's running turn; the runs it started go on.
func (b telegramBackend) Stop(ctx context.Context, sessionID string) error {
	d, err := b.dom()
	if err != nil {
		return err
	}
	if _, err := d.agent.Cancel(sessionID); errors.Is(err, agent.ErrNoTurn) {
		return telegram.ErrNoTurn
	} else if err != nil {
		return err
	}
	return nil
}

// telegramConversation names the Ask Agent conversation an announcement on
// Telegram belongs to, so a reply to it continues there: a delegated task's
// (the conversation its link opens), or the one whose agent started the
// announced run.
func telegramConversation(st *store.Store) func(ctx context.Context, a notify.Announcement) string {
	return func(ctx context.Context, a notify.Announcement) string {
		if a.Kind == notify.EventTaskUpdate {
			if u, err := url.Parse(a.Link); err == nil && u.Path == "/ask" {
				return u.Query().Get("conversation")
			}
			return ""
		}
		if a.EntryID == "" {
			return ""
		}
		var sid string
		_ = st.Read().QueryRowContext(ctx, `SELECT session_id FROM agent_run_origins WHERE run_id = ?`, a.EntryID).Scan(&sid)
		return sid
	}
}

// askedFromPhone reports whether a board entry's work was asked for from
// Telegram, so the policy tells the phone of its stops and its end: a run
// started under the telegram principal (a scan or evaluation button; an
// evaluation the button joined counts too), or by the agent in a turn on the
// telegram channel, whatever the conversation's own channel (a reply from the
// phone into a task's conversation counts). The turn is the latest of the
// conversation that started by the time the run was recorded, so a queued start
// that began after its turn ended counts too; a run recorded before any turn
// (a dashboard press that hands work to a new conversation) is the desk's.
func askedFromPhone(st *store.Store) func(ctx context.Context, entryID string) bool {
	return func(ctx context.Context, entryID string) bool {
		var asked bool
		err := st.Read().QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM idempotency_keys WHERE run_id = ? AND principal = ?)
			OR COALESCE((SELECT t.channel FROM agent_run_origins o JOIN chat_turns t ON t.session_id = o.session_id AND t.started_at <= o.created_at
				WHERE o.run_id = ? ORDER BY t.started_at DESC, t.id DESC LIMIT 1), '') = ?`,
			entryID, telegramPrincipal, entryID, string(agent.ChannelTelegram)).Scan(&asked)
		return err == nil && asked
	}
}

// answerOf is the text the turn answered with, or an honest sentence when it
// ended without one.
func answerOf(ctx context.Context, ss *chatsessions.Store, sessionID string, turn chatsessions.Turn) string {
	msgs, err := ss.Messages(ctx, sessionID)
	if err == nil {
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].TurnID == turn.ID && msgs[i].Role == chatsessions.RoleAssistant && strings.TrimSpace(msgs[i].Body) != "" {
				return msgs[i].Body
			}
		}
	}
	if turn.Status == chatsessions.TurnError {
		switch {
		case turn.Reason == pirpc.ReasonCancelled:
			// The owner sent /stop, or stopped it in the dashboard.
			return "Stopped before it had an answer."
		case turn.Detail == agent.DetailServerStopped:
			// The answer a restart owed the chat.
			return "The app stopped while I was working on this, so there is no answer. Ask again."
		}
		if turn.Reason == wire.ReasonNoModelRoute && turn.Detail != "" {
			// A second try meets the same wall.
			return "I cannot answer that: " + turn.Detail + "."
		}
		reason := "no reason given"
		if turn.Reason != "" {
			reason = strings.ReplaceAll(turn.Reason, "_", " ")
		}
		return "I could not finish that (" + reason + "). Try again in a moment."
	}
	return "I have nothing to add."
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (b telegramBackend) StartScan(ctx context.Context, source string) (telegram.Started, error) {
	ref, err := b.s.launchScan(ctx, telegramPrincipal, "tg-"+randomKey(), wire.ScanRequest{Source: source})
	if err != nil {
		return telegram.Started{}, err
	}
	return telegram.Started{RunID: ref.RunID, Link: "/runs/" + ref.RunID}, nil
}

func (b telegramBackend) StartEvaluation(ctx context.Context, postingURL string) (telegram.Started, error) {
	d, err := b.dom()
	if err != nil {
		return telegram.Started{}, err
	}
	req := evaluate.Request{URL: strings.TrimSpace(postingURL), Source: telegramPrincipal, Priority: evaluate.PriorityUser}
	if e := apikit.CheckPostingURL(req.URL); e != nil {
		return telegram.Started{}, e
	}
	h, err := b.s.submitByURL(ctx, d, evaluate.Claim{Principal: telegramPrincipal, Key: "tg-" + randomKey()}, req)
	if err != nil {
		return telegram.Started{}, err
	}
	b.s.notify("runs")
	return telegram.Started{RunID: h.RunID, Link: "/runs/" + h.RunID}, nil
}

func (b telegramBackend) Ack(ctx context.Context, entryID string) error {
	ns, err := b.s.notifications()
	if err != nil {
		return err
	}
	_, err = ns.board.Ack(ctx, entryID)
	return err
}

// Approve answers an Approve or Decline button of a held action's card. It is a
// one-time approval and nothing more: the actor is Telegram, which the store
// refuses any wider scope and any submit.
func (b telegramBackend) Approve(ctx context.Context, id string, approve bool) (string, error) {
	conf, err := b.s.confirmations()
	if err != nil {
		return "", err
	}
	if !approve {
		if _, err := conf.Decline(ctx, id, approvals.ActorTelegram); err != nil {
			return "", telegramCardError(err)
		}
		return "Declined. It did not run.", nil
	}
	res, err := conf.Approve(ctx, id, approvals.ActorTelegram, approvals.Once)
	if err != nil {
		return "", telegramCardError(err)
	}
	return "Approved once. " + res.Result, nil
}

func telegramCardError(err error) error {
	switch {
	case errors.Is(err, approvals.ErrNotFound):
		return errors.New("that card no longer exists")
	case errors.Is(err, approvals.ErrConflict):
		return errors.New("that card expired or was already handled")
	case errors.Is(err, approvals.ErrNotPermitted):
		return errors.New("this one is confirmed in the dashboard")
	}
	return err
}

// Rate applies a rating button of a digest: a second tap on the verdict in
// force clears it, and a stale or malformed button records nothing.
func (b telegramBackend) Rate(ctx context.Context, item, verdict string) error {
	d, err := b.dom()
	if err != nil {
		return err
	}
	return d.gmail.Digests.RateItem(ctx, item, verdict)
}

// Mail does a Gmail alert's or reminder's button on its thread with the
// operations of the Gmail page and says what happened. A draft is shown to
// copy; nothing is sent.
func (b telegramBackend) Mail(ctx context.Context, action, thread string) (string, error) {
	d, err := b.dom()
	if err != nil {
		return "", err
	}
	if action == telegram.MailDraft {
		return b.draft(ctx, d, thread)
	}
	// The button names are the thread actions'; a refusal reads as its reason.
	reply, err := d.gmail.Repo.Act(ctx, action, thread, time.Local)
	if err != nil {
		return "", err
	}
	b.s.notify("gmail")
	return reply, nil
}

// draft writes a reply draft for the thread as the Gmail page's Draft button
// does and words it as a message to copy.
func (b telegramBackend) draft(ctx context.Context, d *domain, thread string) (string, error) {
	dr, err := d.gmail.DraftReply(ctx, thread)
	if err != nil {
		return "", errors.New(gmailapi.DraftError(dr.Kind, err).Message)
	}
	b.s.notify("gmail")
	what := "Reply"
	if dr.Kind == lifecycle.DraftFollowUp {
		what = "Follow-up"
	}
	text := what + " draft (nothing is sent; copy it into Gmail):\n\n" + dr.Text
	if len(dr.ToConfirm) > 0 {
		text += "\n\nCheck before sending: " + strings.Join(dr.ToConfirm, "; ")
	}
	if dr.Note != "" {
		text += "\n\n(" + dr.Note + ")"
	}
	return text, nil
}

// telegramMail is the Telegram sink's view of a Gmail alert or deadline
// reminder: its thread, from the link, and the buttons the thread offers now.
// A reminder offers Done and Tomorrow; an alert Done while the thread asks
// something of the user, Not job mail and Draft reply.
func (s *Server) telegramMail(ctx context.Context, kind, link string) (string, []string) {
	id, ok := gmail.ParseThreadLink(link)
	d, err := s.dom()
	if !ok || err != nil {
		return "", nil
	}
	if kind == notify.EventGmailReminder {
		return id, []string{telegram.MailDone, telegram.MailSnooze}
	}
	td, found, err := d.gmail.Repo.ThreadDetail(ctx, id, s.now())
	if err != nil || !found {
		return "", nil
	}
	var actions []string
	if td.View.Actionable() && !td.Done {
		actions = append(actions, telegram.MailDone)
	}
	return id, append(actions, telegram.MailDismiss, telegram.MailDraft)
}

// ScanSources lists the enabled discovery sources, the most recently run first.
func (b telegramBackend) ScanSources(ctx context.Context) ([]string, error) {
	d, err := b.dom()
	if err != nil {
		return nil, err
	}
	var enabled []string
	for _, src := range discovery.Sources {
		if src.Readiness(b.s.cfg.Vault.Has) == "ready" {
			enabled = append(enabled, src.Name)
		}
	}
	recent, err := d.repo.ScanList(ctx, "", 50)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range recent {
		if slices.Contains(enabled, r.Source) && !slices.Contains(out, r.Source) {
			out = append(out, r.Source)
		}
	}
	for _, name := range enabled {
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out[:min(len(out), 8)], nil
}

func (b telegramBackend) Status(ctx context.Context) (string, error) {
	d, err := b.dom()
	if err != nil {
		return "", err
	}
	var w strings.Builder
	w.WriteString("Status\n")
	status, detail := "ok", ""
	if comps := b.s.components.Load(); comps != nil {
		for _, c := range comps.list {
			// A component that was never set up is not a fault.
			if c.Status == wire.ComponentDegraded || c.Status == wire.ComponentDown {
				status = "degraded"
				detail += fmt.Sprintf("\n- %s: %s", c.Name, c.Status)
			}
		}
	}
	fmt.Fprintf(&w, "Server: %s (version %s)%s\n", status, b.s.cfg.Version, detail)
	sum, err := d.notify.board.Summary(ctx)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&w, "Runs: %d working, %d need you, %d blocked\n", sum.Working, sum.NeedsYou, sum.Blocked)
	w.WriteString(browserWindows(ctx, b.s.cfg.BrowserControl))
	views, err := d.sched.Manager.List(ctx)
	if err == nil {
		type occ struct {
			name string
			at   time.Time
		}
		var next []occ
		for _, v := range views {
			if v.Enabled && len(v.Next) > 0 {
				next = append(next, occ{v.Name, v.Next[0]})
			}
		}
		slices.SortFunc(next, func(a, c occ) int { return a.at.Compare(c.at) })
		if len(next) == 0 {
			w.WriteString("Next schedules: none\n")
		}
		for i, o := range next[:min(3, len(next))] {
			if i == 0 {
				w.WriteString("Next schedules:\n")
			}
			fmt.Fprintf(&w, "- %s at %s\n", o.name, o.at.Local().Format("Jan 2 15:04"))
		}
	}
	if h, err := d.gmail.Service.Health(ctx); err == nil {
		fmt.Fprintf(&w, "Gmail: sign-in %s, sync %s\n", gmailLogin(h), h.Sync)
	}
	spend, capUSD := d.router.DailySpend(ctx)
	if capUSD > 0 {
		fmt.Fprintf(&w, "Model spend today: USD %.2f of USD %.2f\n", spend, capUSD)
	} else {
		fmt.Fprintf(&w, "Model spend today: USD %.2f (no cap)\n", spend)
	}
	return w.String(), nil
}

// browserWindows says what state the managed browser and its two windows are
// in. A server without a browser reports it stopped.
func browserWindows(ctx context.Context, bc BrowserControl) string {
	if bc == nil {
		return "Browser: stopped\n"
	}
	st := bc.Status(ctx)
	lane := func(name string, l browser.LaneStatus) string {
		if !l.Window {
			return name + " window: closed"
		}
		return fmt.Sprintf("%s window: open, %d tab%s", name, len(l.Tabs), plural(len(l.Tabs)))
	}
	return fmt.Sprintf("Browser: %s. %s. %s.\n", st.State, lane("Apply", st.Apply), lane("Scan", st.Scan))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (b telegramBackend) Tracker(ctx context.Context) (string, error) {
	d, err := b.dom()
	if err != nil {
		return "", err
	}
	counts, err := d.tracker.Summary(ctx)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, st := range tracker.Statuses() {
		if n := counts[st]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", st, n))
		}
	}
	page, err := d.tracker.List(ctx, tracker.ListQuery{Sort: tracker.SortUpdatedDesc, Page: 1, PageSize: listLimit})
	if err != nil {
		return "", err
	}
	var w strings.Builder
	w.WriteString("Tracker\n")
	if len(parts) == 0 {
		w.WriteString("No applications yet.\n")
	} else {
		w.WriteString(strings.Join(parts, ", ") + "\n")
	}
	for i, e := range page.Entries {
		if i == 0 {
			w.WriteString("Most recently changed:\n")
		}
		fmt.Fprintf(&w, "- %s, %s (%s)\n", e.Company, e.Role, e.Status)
	}
	return w.String(), nil
}

func (b telegramBackend) Pipeline(ctx context.Context) (string, error) {
	d, err := b.dom()
	if err != nil {
		return "", err
	}
	counts, err := d.repo.InboxCounts(ctx, time.Time{})
	if err != nil {
		return "", err
	}
	rows, err := d.repo.InboxList(ctx, discovery.InboxQuery{Status: wire.InboxPending, Limit: listLimit})
	if err != nil {
		return "", err
	}
	var w strings.Builder
	fmt.Fprintf(&w, "Pipeline\n%d postings waiting, %d queued for evaluation\n", counts[wire.InboxPending], counts[wire.InboxQueued])
	for i, r := range rows {
		if i == 0 {
			w.WriteString("Best waiting:\n")
		}
		fmt.Fprintf(&w, "- %s, %s (score %d)\n", r.Company, r.Role, r.Score)
	}
	return w.String(), nil
}

func (b telegramBackend) Queue(ctx context.Context) (string, error) {
	lanes, err := b.s.apply.AgentApplyLanes()(ctx)
	if err != nil {
		return "", err
	}
	var w strings.Builder
	w.WriteString("Apply list\n")
	empty := true
	for _, l := range lanes {
		fmt.Fprintf(&w, "%s: %d\n", l.Lane, l.Total)
		for _, it := range l.Items[:min(len(l.Items), listLimit)] {
			fmt.Fprintf(&w, "- %s, %s\n", it.Company, it.Role)
		}
		empty = empty && l.Total == 0
	}
	if empty {
		w.WriteString("Nothing is on the list today.\n")
	}
	return w.String(), nil
}

func (b telegramBackend) Gmail(ctx context.Context) (string, error) {
	d, err := b.dom()
	if err != nil {
		return "", err
	}
	h, err := d.gmail.Service.Health(ctx)
	if err != nil {
		return "", err
	}
	signals, err := d.gmail.Repo.Signals(ctx)
	if err != nil {
		return "", err
	}
	cut := b.s.now().Add(-24 * time.Hour).UnixMilli()
	var w strings.Builder
	fmt.Fprintf(&w, "Gmail\nSign-in %s, sync %s\n", gmailLogin(h), h.Sync)
	n := 0
	for _, sig := range signals {
		if sig.ReceivedMS < cut {
			continue
		}
		if n == 0 {
			w.WriteString("Last 24 hours:\n")
		}
		n++
		if n <= 8 {
			fmt.Fprintf(&w, "- %s, %s: %s\n", orDash(sig.Employer), sig.Type, sig.Subject)
		}
	}
	if n == 0 {
		w.WriteString("No signals in the last 24 hours.\n")
	}
	return w.String(), nil
}

// inboxLimit is how many do-now threads /inbox lists.
const inboxLimit = 10

// Today answers /today with the day summary's text for now.
func (b telegramBackend) Today(ctx context.Context) (string, error) {
	d, err := b.dom()
	if err != nil {
		return "", err
	}
	v, err := b.s.dayView(ctx, d, b.s.now(), true)
	if err != nil {
		return "", err
	}
	return dayText(v, false), nil
}

// Inbox lists the action inbox's do-now lane, at most ten threads, from the
// same read model as the dashboard.
func (b telegramBackend) Inbox(ctx context.Context) (string, error) {
	d, err := b.dom()
	if err != nil {
		return "", err
	}
	page, err := d.gmail.Repo.Inbox(ctx, gmail.InboxQuery{Lane: lifecycle.LaneDoNow, Limit: MaxInboxProbe}, b.s.now())
	if err != nil {
		return "", err
	}
	if len(page.Threads) == 0 {
		return "Nothing to do now in your job mail.", nil
	}
	var w strings.Builder
	fmt.Fprintf(&w, "Do now (%d)\n", len(page.Threads))
	for i, t := range page.Threads {
		if i == inboxLimit {
			fmt.Fprintf(&w, "and %d more in the dashboard\n", len(page.Threads)-inboxLimit)
			break
		}
		who := t.Employer
		if t.Role != "" && t.Role != classify.UnknownRole {
			who += ", " + t.Role
		}
		fmt.Fprintf(&w, "%d. %s: %s", i+1, who, t.View.NextAction)
		if !t.View.Due.IsZero() {
			fmt.Fprintf(&w, " (due %s)", t.View.Due.Local().Format("Mon Jan 2 15:04"))
		}
		w.WriteString("\n")
	}
	return w.String(), nil
}

// screens are the dashboard screens /open names: the name to say and the
// dashboard path.
var screens = map[string]struct{ name, path string }{
	"tracker": {"Tracker", "/tracker"}, "pipeline": {"Discovery", "/discovery"}, "discovery": {"Discovery", "/discovery"},
	"reports": {"Reports", "/reports"}, "documents": {"Documents", "/apply-next"}, "ask": {"Ask Agent", "/ask"}, "agent": {"Ask Agent", "/ask"},
	"gmail": {"Gmail", "/gmail"}, "usage": {"Usage", "/usage"}, "settings": {"Settings", "/settings"}, "schedules": {"Schedules", "/discovery?tab=schedules"},
	"runs": {"Runs", "/runs"}, "wiki": {"Wiki", "/wiki"},
}

// Open gives the dashboard address of a screen while remote access is on, and
// otherwise names the screen to open on the computer.
func (b telegramBackend) Open(_ context.Context, screen string) (string, error) {
	sc, ok := screens[strings.ToLower(strings.TrimSpace(screen))]
	if !ok {
		var names []string
		for k := range screens {
			names = append(names, k)
		}
		slices.Sort(names)
		return "I do not know that screen. Try one of: " + strings.Join(names, ", ") + ".", nil
	}
	if base, on := b.s.remote.publicBase(); on {
		return sc.name + ": " + strings.TrimRight(base, "/") + sc.path, nil
	}
	return "Open " + sc.name + " on your computer. Remote access is off, so I cannot give you a link.", nil
}

// Trace summarises the most recent turn of this chat's conversation: the tools
// it used, the gate decisions and the model.
func (b telegramBackend) Trace(ctx context.Context, sessionID string) (string, error) {
	d, err := b.dom()
	if err != nil {
		return "", err
	}
	if sessionID == "" {
		return "There is no conversation with me in this chat yet.", nil
	}
	turns, err := d.sessions.Turns(ctx, sessionID)
	if err != nil || len(turns) == 0 {
		return "There is no turn to trace yet.", nil
	}
	turn := turns[len(turns)-1]
	msgs, err := d.sessions.Messages(ctx, sessionID)
	if err != nil {
		return "", err
	}
	var tools []string
	for _, m := range msgs {
		if m.TurnID == turn.ID && m.Role == chatsessions.RoleToolCall {
			tools = append(tools, m.ToolName)
		}
	}
	var w strings.Builder
	fmt.Fprintf(&w, "Last turn: %s (%s)\n", turn.Status, turn.Origin)
	if len(tools) == 0 {
		w.WriteString("Tools used: none\n")
	} else {
		fmt.Fprintf(&w, "Tools used: %s\n", strings.Join(tools, ", "))
	}
	gates, err := b.gateDecisions(ctx, turn.ID)
	if err != nil {
		return "", err
	}
	if len(gates) == 0 {
		w.WriteString("Gate decisions: none\n")
	} else {
		w.WriteString("Gate decisions:\n" + strings.Join(gates, "\n") + "\n")
	}
	var model string
	_ = b.s.store.Load().Read().QueryRowContext(ctx, `SELECT model FROM llm_ledger WHERE run_id = ? AND started_at >= ? ORDER BY id DESC LIMIT 1`,
		sessionID, turn.StartedAt.UTC().Format("2006-01-02T15:04:05.000Z")).Scan(&model)
	fmt.Fprintf(&w, "Model: %s\n", orDash(model))
	return w.String(), nil
}

func (b telegramBackend) gateDecisions(ctx context.Context, turnID string) ([]string, error) {
	rows, err := b.s.store.Load().Read().QueryContext(ctx, `SELECT COALESCE(tool, ''), gate, verdict, code FROM gate_decisions WHERE turn_id = ? ORDER BY id LIMIT 20`, turnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var tool, gate, verdict, code string
		if err := rows.Scan(&tool, &gate, &verdict, &code); err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("- %s: %s %s (%s)", orDash(tool), gate, verdict, code))
	}
	return out, rows.Err()
}

// gmailLogin is the sign-in state a status reply names.
func gmailLogin(h gmail.Health) string {
	if !h.Connected {
		return "not connected"
	}
	return h.Login
}
