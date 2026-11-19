package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"applysmith/internal/agent"
	"applysmith/internal/agent/approvals"
	"applysmith/internal/answerbank/assist"
	"applysmith/internal/answerbank/learn"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/gmail"
	"applysmith/internal/llm"
	"applysmith/internal/notify"
	"applysmith/internal/notify/telegram"
	"applysmith/internal/remote/qr"
	"applysmith/internal/scheduler"
	"applysmith/internal/scheduler/jobs"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

// notifyStack is the runs board, the announcement policy, the desktop sink and
// the Telegram gateway of one workspace.
type notifyStack struct {
	board    *notify.Board
	policy   *notify.Policy
	settings *notify.SettingsStore
	desktop  *notify.Desktop
	tg       *telegram.Gateway
	tgState  *telegram.State
	tgMgr    *telegram.Manager
	now      func() time.Time

	// workers run beside the gateway until the stack closes.
	workers []func(ctx context.Context)

	syncMu sync.Mutex
	// ctx ends when the stack closes. The workers and the card sends run under
	// it, and wg counts them.
	ctx  context.Context
	stop context.CancelFunc
	wg   sync.WaitGroup
}

// buildNotify wires the notification stack over the open store. Its workers start
// with start and end with close.
func (s *Server) buildNotify(st *store.Store) *notifyStack {
	ns := &notifyStack{now: s.now}
	ns.ctx, ns.stop = context.WithCancel(context.Background())
	ns.desktop = notify.NewDesktop(notify.DesktopOptions{Store: st, Now: s.now, Log: s.log, Publish: s.notifs.publish, Attached: s.notifs.listening,
		Changed: func() { s.notify("notifications") }})
	ns.board = notify.NewBoard(st, notify.BoardOptions{Now: s.now, Log: s.log, Changed: s.notifyTopics, ScanTitle: sourceTitle, Idle: jobs.Idle})
	ns.settings = notify.NewSettingsStore(st, s.now)
	ns.tgState = telegram.NewState(st, s.now)
	ns.tg = telegram.New(telegram.Config{
		State: ns.tgState, Token: telegram.TokenSource(s.cfg.Vault), API: s.cfg.Domain.TelegramAPI, Clock: s.cfg.Domain.TelegramClock, Backend: telegramBackend{s: s}, Digest: s.telegramDigest, Log: s.log,
		Mail:         s.telegramMail,
		Conversation: telegramConversation(st),
		LinkBase:     func() (string, bool) { return s.remote.publicBase() },
		OnPaired: func() {
			ns.desktop.Text(s.bg, "Telegram paired", "This chat can now talk to the app.", "/settings")
		},
	})
	ns.policy = notify.NewPolicy(notify.PolicyOptions{Store: st, Board: ns.board, Settings: ns.settings, Quiet: s.quietHours,
		Sinks: []notify.Sink{ns.desktop, ns.tg.Sink()}, Now: s.now, Log: s.log, Asked: askedFromPhone(st)})
	ns.board.SetObserver(ns.policy)
	ns.tgMgr = &telegram.Manager{State: ns.tgState, Vault: s.cfg.Vault, API: s.cfg.Domain.TelegramAPI, Changed: func() {
		ns.tg.Reconfigure()
		s.notify("notifications")
	}}
	return ns
}

// start settles what a stopped process left behind and runs the workers.
func (ns *notifyStack) start(ctx context.Context, log func(string, ...any)) {
	if n, err := ns.board.Restart(ctx); err != nil {
		log("the runs board could not be settled at start", "error", err.Error())
	} else if n > 0 {
		log("entries of a stopped process are now interrupted", "count", n)
	}
	if n, err := ns.policy.FoldRepeats(ctx); err != nil {
		log("repeated stops could not be folded at start", "error", err.Error())
	} else if n > 0 {
		log("repeated stops of one piece of work are now one entry", "count", n)
	}
	if n, err := ns.board.RenameWaits(ctx); err != nil {
		log("waiting entries could not be renamed at start", "error", err.Error())
	} else if n > 0 {
		log("waiting entries now carry the names their postings are known by", "count", n)
	}
	ns.policy.Start(ns.ctx)
	ns.wg.Go(func() { ns.tg.Run(ns.ctx) })
	for _, w := range ns.workers {
		ns.wg.Go(func() { w(ns.ctx) })
	}
}

func (ns *notifyStack) close() {
	ns.stop()
	ns.wg.Wait()
}

// quietHours reads the profile's quiet-hours group. Every failure reads as no
// quiet hours: a broken file never silences anything.
func (s *Server) quietHours(context.Context) notify.QuietHours {
	raw, err := s.cfg.Workspace.Read(workspace.FileProfile)
	if err != nil || len(raw.Data) == 0 {
		return notify.QuietHours{}
	}
	p, rep := workspace.ParseProfile(raw.Data)
	if !rep.OK() {
		return notify.QuietHours{}
	}
	q := p.Notifications.QuietHours
	return notify.QuietHours{Enabled: q.Enabled != nil && *q.Enabled, Start: q.Start, End: q.End, Timezone: q.Timezone}
}

// boardSyncWait bounds how long a caller waits for the board to follow the run
// records. The mirror writes, so a busy writer would otherwise hold every read
// that syncs first for the writer's whole busy timeout, past the three seconds
// the tray's quit check allows. Entries not mirrored in time follow at the next
// sync.
const boardSyncWait = time.Second

// syncBoard mirrors the run records onto the board. Syncs run one at a time; the
// board's own change notices never start another.
func (s *Server) syncBoard() {
	d := s.domain.Load()
	if d == nil || d.notify == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.bg), boardSyncWait)
	defer cancel()
	d.notify.syncMu.Lock()
	defer d.notify.syncMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	// A sync that ran out of its wait while the writer was busy is expected;
	// the next sync follows.
	if _, err := d.notify.board.SyncRuns(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) && s.log != nil {
		s.log.Warn("the runs board could not follow the run records", "error", err.Error())
	}
}

// capAlertSink delivers the daily cost-cap alert through the notification
// policy. It reports whether the policy accepted it for delivery. It is bound
// to the stack, not looked up on the server, because the scheduler's first
// tick can raise the alert before the domain is published.
func (ns *notifyStack) capAlertSink(ctx context.Context, a llm.CapAlert) bool {
	return ns.policy.AnnounceCostCap(ctx, a.Day, a.Message)
}

// gmailHooks turn a Gmail refusal of the stored app password into one item
// of the inbox until the user connects again.
func (s *Server) gmailHooks(ns *notifyStack) gmail.Hooks {
	return gmail.Hooks{
		OnRefused: func(ctx context.Context, reason string) {
			if err := ns.raiseGmailSignIn(ctx, reason); err != nil {
				s.log.Warn("the Gmail sign-in item could not be raised", "error", err.Error())
			}
		},
		OnRestored: func(ctx context.Context) {
			if err := ns.board.Resolve(ctx, gmailSignInItem); err != nil {
				s.log.Warn("the Gmail sign-in item could not be resolved", "error", err.Error())
			}
		},
	}
}

// gmailSignInItem is the id of the one inbox item of a Gmail that cannot be
// signed in to.
const gmailSignInItem = "gmail-sign-in"

// raiseGmailSignIn raises the one inbox item of a Gmail that cannot be signed
// in to, whichever side saw it: the Gmail stack or the sync's schedule.
func (ns *notifyStack) raiseGmailSignIn(ctx context.Context, failure string) error {
	_, err := ns.board.Raise(ctx, notify.RaiseArgs{ID: gmailSignInItem, Kind: notify.KindTask, Origin: notify.OriginSchedule,
		Title: "Gmail needs its app password", State: notify.NeedsYou, Reason: "sign-in", Detail: gmail.SignInDetail(failure), Link: "/gmail"})
	return err
}

// scheduleNeedsID prefixes the inbox items a schedule's failures raise; the
// scheduler's reason follows.
func scheduleNeedsID(scheduleID int64) string {
	return "needs-schedule-" + strconv.FormatInt(scheduleID, 10) + "-"
}

// scheduledNeedsYou puts a scheduled job's failure that waits for the user in
// the inbox: one item per schedule and reason, until a later run of that
// schedule succeeds (scheduledRecovered).
func (ns *notifyStack) scheduledNeedsYou(ctx context.Context, it scheduler.NeedsYouItem, log func(string, ...any)) {
	if it.Reason == jobs.ReasonNeedsGmailSignIn {
		// One cause, one item: the Gmail stack raises the same one.
		if err := ns.raiseGmailSignIn(ctx, it.Message); err != nil {
			log("a scheduled job's need could not be raised", "schedule", it.Schedule, "error", err.Error())
		}
		return
	}
	reason := "other"
	if it.Reason == scheduler.ReasonNeedsSignIn {
		reason = "sign-in"
	}
	id := scheduleNeedsID(it.ScheduleID) + it.Reason
	if it.Reason == jobs.ReasonNeedsKey {
		// A source that is skipped for want of its key every day raises one
		// item a day, not one per run.
		id = "needs-key-" + it.Kind + "-" + ns.now().UTC().Format("2006-01-02")
	}
	_, err := ns.board.Raise(ctx, notify.RaiseArgs{ID: id, Kind: notify.KindTask, Origin: notify.OriginSchedule,
		Title: it.Schedule + " needs you", State: notify.NeedsYou, Reason: reason, Detail: it.Message, Link: "/discovery?tab=schedules"})
	if err != nil {
		log("a scheduled job's need could not be raised", "schedule", it.Schedule, "error", err.Error())
	}
}

// scheduledRecovered resolves the items the failures of a schedule raised once
// a later run of it succeeded.
func (ns *notifyStack) scheduledRecovered(ctx context.Context, scheduleID int64, log func(string, ...any)) {
	waiting, err := ns.board.List(ctx, notify.Filter{States: []notify.State{notify.NeedsYou}, Kinds: []notify.Kind{notify.KindTask}, Limit: notify.MaxLimit})
	if err != nil {
		log("a schedule's waiting items could not be read", "schedule", scheduleID, "error", err.Error())
		return
	}
	prefix := scheduleNeedsID(scheduleID)
	for _, e := range waiting {
		if !strings.HasPrefix(e.ID, prefix) {
			continue
		}
		if err := ns.board.Resolve(ctx, e.ID); err != nil {
			log("a schedule's waiting item could not be resolved", "entry", e.ID, "error", err.Error())
		}
	}
}

// timers registers the notification work on the scheduler's tick and its daily
// sweep.
func (ns *notifyStack) timers(sc *scheduler.Scheduler, s *Server) {
	sc.AddTimer(scheduler.EveryTick("runs-board", "On each tick: mirrors run records onto the runs board and finalizes entries no producer touched for five minutes",
		func(ctx context.Context) error {
			s.syncBoard()
			_, err := ns.board.SweepStale(ctx)
			return err
		}))
	sc.AddTimer(scheduler.EveryTick("quiet-hours-release", "On each tick: delivers announcements that fell due, retries failed ones and releases those held in quiet hours",
		func(ctx context.Context) error { return ns.policy.Process(ctx) }))
	sc.AddRetention("runs board", ns.board.Retain)
	sc.AddRetention("announcements", ns.policy.Prune)
	sc.AddRetention("telegram receipts and events", ns.tgState.Retain)
}

// ---- wire

func toBoardEntry(e notify.Entry) wire.BoardEntry {
	out := wire.BoardEntry{ID: e.ID, Kind: string(e.Kind), Origin: string(e.Origin), Title: e.Title, Stage: e.Stage, Detail: e.Detail,
		State: string(e.State), Reason: e.Reason, Outcome: e.Outcome, BatchID: e.BatchID, Link: e.Link, Ref: e.Ref, CanFocusTab: e.FocusTab != "", FocusTab: e.FocusTab, CanDone: notify.CanResume(e), CanSubmit: notify.CanSubmitKept(e),
		Revision: apikit.Safe64(e.Revision), StartedAt: wire.Time{Time: e.StartedAt}, UpdatedAt: wire.Time{Time: e.UpdatedAt}, StateAt: wire.Time{Time: e.StateAt},
		FinishedAt: apikit.TimePtr(e.FinishedAt), SeenAt: apikit.TimePtr(e.SeenAt), AcknowledgedAt: apikit.TimePtr(e.AckedAt)}
	if out.Link == "" {
		out.Link = "/runs/" + e.ID
	}
	return out
}

func (s *Server) notifications() (*notifyStack, error) {
	d, err := s.dom()
	if err != nil {
		return nil, err
	}
	return d.notify, nil
}

func boardError(err error) error {
	switch {
	case errors.Is(err, notify.ErrNotFound):
		return apikit.NewErr(wire.CodeNotFound, "no such board entry")
	case errors.Is(err, notify.ErrNotAckable):
		return apikit.NewErr(wire.CodeConflict, "only a finished or waiting entry can be acknowledged")
	}
	return err
}

func (s *Server) listRuns(c *apikit.Call) (apikit.Reply, error) {
	ns, err := s.notifications()
	if err != nil {
		return apikit.Reply{}, err
	}
	q := apikit.QueryOf(c)
	f := notify.Filter{Batch: q.Str("batch"), Since: q.Day("since"), Limit: q.Integer("limit", notify.DefaultLimit)}
	for _, v := range c.R.URL.Query()["state"] {
		for _, part := range strings.Split(v, ",") {
			switch st := notify.State(strings.TrimSpace(part)); st {
			case notify.Working, notify.NeedsYou, notify.Blocked, notify.Done, notify.Idle:
				f.States = append(f.States, st)
			case "":
			default:
				q.Fail(apikit.InvalidField("state", "state must be working, needs-you, blocked, done or idle"))
			}
		}
	}
	for _, v := range c.R.URL.Query()["kind"] {
		for _, part := range strings.Split(v, ",") {
			switch k := notify.Kind(strings.TrimSpace(part)); k {
			case notify.KindApply, notify.KindScan, notify.KindEvaluate, notify.KindDocuments, notify.KindAgent, notify.KindTask:
				f.Kinds = append(f.Kinds, k)
			case "":
			default:
				q.Fail(apikit.InvalidField("kind", "kind must be apply, scan, evaluate, documents, agent or task"))
			}
		}
	}
	switch q.Str("acknowledged") {
	case "":
	case "true":
		f.Acked = new(bool)
		*f.Acked = true
	case "false":
		f.Acked = new(bool)
	default:
		q.Fail(apikit.InvalidField("acknowledged", "acknowledged must be true or false"))
	}
	if q.Err == nil && f.Limit < 1 {
		q.Fail(apikit.InvalidField("limit", "limit must be at least 1"))
	}
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	s.syncBoard()
	entries, err := ns.board.List(c.R.Context(), f)
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.BoardEntryList{Entries: make([]wire.BoardEntry, len(entries))}
	for i, e := range entries {
		out.Entries[i] = toBoardEntry(e)
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Server) runsSummary(c *apikit.Call) (apikit.Reply, error) {
	ns, err := s.notifications()
	if err != nil {
		return apikit.Reply{}, err
	}
	s.syncBoard()
	sum, err := ns.board.Summary(c.R.Context())
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: wire.BoardSummary{Working: apikit.Safe(sum.Working), NeedsYou: apikit.Safe(sum.NeedsYou), Blocked: apikit.Safe(sum.Blocked),
		DoneUnacknowledged: apikit.Safe(sum.DoneUnack), Unseen: apikit.Safe(sum.Unseen)}}, nil
}

// needsYouCounts counts what Needs you shows only as a number. The pending
// confirmations, unpublished drafts and open held signals are counted in SQL,
// over the rows their lists read; the suggested answers and hand edits come
// from the learning service, whose list drops questions SQL cannot judge (a
// non-question, model-written text, one the bank already answers).
func (s *Server) needsYouCounts(c *apikit.Call) (apikit.Reply, error) {
	st, err := s.db()
	if err != nil {
		return apikit.Reply{}, err
	}
	ctx := c.R.Context()
	var out wire.NeedsYouCounts
	err = st.Read().QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM gmail_confirmations WHERE state = 'pending'),
		(SELECT count(*) FROM kb_drafts WHERE status NOT IN ('superseded', 'published')),
		(SELECT count(*) FROM signal_review v JOIN signal_assessments a ON a.id = v.assessment_id JOIN signal_events e ON e.id = a.event_id
			JOIN signal_companies co ON co.id = e.company_id WHERE v.state = 'open')`).Scan(&out.EmailConfirmations, &out.WikiDrafts, &out.HeldSignals)
	if err != nil {
		return apikit.Reply{}, err
	}
	svc, err := s.learn.Learning()
	if err != nil {
		return apikit.Reply{}, err
	}
	cands, err := svc.Candidates(ctx, learn.StatePending)
	if err != nil {
		return apikit.Reply{}, s.learn.LearnError(err)
	}
	for _, cand := range cands {
		if cand.Review.Verdict == "" || cand.Review.Verdict == assist.VerdictReusable {
			out.SuggestedAnswers++
		}
	}
	edits, err := svc.Edits(ctx, "")
	if err != nil {
		return apikit.Reply{}, s.learn.LearnError(err)
	}
	out.AnswerEdits = apikit.Safe(len(edits))
	return apikit.Reply{Data: out}, nil
}

func (s *Server) needsYou(c *apikit.Call) (apikit.Reply, error) {
	ns, err := s.notifications()
	if err != nil {
		return apikit.Reply{}, err
	}
	s.syncBoard()
	items, err := ns.board.Inbox(c.R.Context())
	if err != nil {
		return apikit.Reply{}, err
	}
	now := s.now()
	out := wire.NeedsYouInbox{Items: make([]wire.NeedsYouItem, len(items))}
	for i, e := range items {
		action := wire.ActionOpenRun
		if e.FocusTab != "" {
			action = wire.ActionFocusTab
		}
		out.Items[i] = wire.NeedsYouItem{Entry: toBoardEntry(e), WaitingSeconds: apikit.Safe64(int64(now.Sub(e.StateAt).Seconds())), Action: action}
		out.Items[i].Entry.Fix = s.fixOf(c.R.Context(), e)
		if e.SeenAt.IsZero() {
			out.Unseen++
		}
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Server) markEntry(seen bool) apikit.Handler {
	return func(c *apikit.Call) (apikit.Reply, error) {
		ns, err := s.notifications()
		if err != nil {
			return apikit.Reply{}, err
		}
		id := c.R.PathValue("id")
		var e notify.Entry
		if seen {
			e, err = ns.board.Seen(c.R.Context(), id)
		} else {
			e, err = ns.board.Ack(c.R.Context(), id)
		}
		if err != nil {
			return apikit.Reply{}, boardError(err)
		}
		return apikit.Reply{Data: toBoardEntry(ns.board.Shown(c.R.Context(), e))}, nil
	}
}

func (s *Server) ackAnnouncement(c *apikit.Call) (apikit.Reply, error) {
	ns, err := s.notifications()
	if err != nil {
		return apikit.Reply{}, err
	}
	// An announcement's identifier is its entry's id.
	e, err := ns.board.Ack(c.R.Context(), c.R.PathValue("announcementId"))
	if err != nil {
		return apikit.Reply{}, boardError(err)
	}
	return apikit.Reply{Data: toBoardEntry(ns.board.Shown(c.R.Context(), e))}, nil
}

func (s *Server) notificationEvents(c *apikit.Call) (apikit.Reply, error) {
	err := s.serveStream(c, s.notifs, func() ([]sseEvent, bool) { return nil, false })
	return apikit.Reply{}, err
}

func (s *Server) listNotices(c *apikit.Call) (apikit.Reply, error) {
	ns, err := s.notifications()
	if err != nil {
		return apikit.Reply{}, err
	}
	out, err := ns.desktop.Notices(c.R.Context())
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Server) readNotices(c *apikit.Call) (apikit.Reply, error) {
	ns, err := s.notifications()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.NoticesRead
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	ids := make([]int64, len(req.IDs))
	for i, id := range req.IDs {
		ids[i] = int64(id)
	}
	if err := ns.desktop.Read(c.R.Context(), ids); err != nil {
		return apikit.Reply{}, err
	}
	return s.listNotices(c)
}

func (s *Server) notificationSettings(ctx context.Context, ns *notifyStack) wire.NotificationSettings {
	got := ns.settings.Get(ctx)
	_, paired := ns.tgState.Owner(ctx)
	return wire.NotificationSettings{Desktop: got.Desktop, Telegram: got.Telegram, FailedScans: got.FailedScans, TelegramPaired: paired}
}

func (s *Server) getNotificationSettings(c *apikit.Call) (apikit.Reply, error) {
	ns, err := s.notifications()
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: s.notificationSettings(c.R.Context(), ns)}, nil
}

func (s *Server) patchNotificationSettings(c *apikit.Call) (apikit.Reply, error) {
	ns, err := s.notifications()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.NotificationSettingsPatch
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if _, err := ns.settings.Patch(c.R.Context(), notify.SettingsPatch{Desktop: req.Desktop, Telegram: req.Telegram, FailedScans: req.FailedScans}); err != nil {
		return apikit.Reply{}, err
	}
	s.notify("notifications")
	return apikit.Reply{Data: s.notificationSettings(c.R.Context(), ns)}, nil
}

// testNotification sends a test notification to every channel at once and says,
// for each, what happened and what to do about it.
func (s *Server) testNotification(c *apikit.Call) (apikit.Reply, error) {
	ns, err := s.notifications()
	if err != nil {
		return apikit.Reply{}, err
	}
	ctx := c.R.Context()
	out := wire.NotificationTest{Channels: []wire.NotificationTestChannel{}}
	for _, r := range ns.policy.Test(ctx) {
		ch := wire.NotificationTestChannel{Channel: r.Sink, State: r.State}
		var api *telegram.APIError
		switch {
		case r.State == "off":
			ch.Detail = "Switched off above, so nothing was sent."
		case r.Sink == "desktop" && r.State == "sent":
			ch.Detail = "Sent to the Applysmith app. Nothing showing? Allow Applysmith in System Settings, Notifications."
		case r.Sink == "desktop" && r.State == "unavailable":
			ch.Detail = "The Applysmith app is not open, so nothing can show a desktop notification. Until it is, announcements go to Notices in the top bar."
		case r.State == "sent":
			ch.Detail = "Sent to the paired chat."
		case r.State == "unavailable" && !ns.tg.Status(ctx).TokenSet:
			ch.Detail = "No bot token is saved. Add one in the Telegram section."
		case r.State == "unavailable":
			ch.Detail = "No chat is paired. Get a pairing code in the Telegram section."
		case errors.As(r.Err, &api) && api.IsAuth():
			ch.Detail = "Telegram refused the bot token. Save a new token from BotFather in the Telegram section."
		default:
			ch.Detail = "It could not be sent: " + r.Err.Error() + ". Check the connection and try again."
		}
		out.Channels = append(out.Channels, ch)
	}
	return apikit.Reply{Data: out}, nil
}

// ---- Telegram setup routes

func (s *Server) telegramStatusOf(ctx context.Context, ns *notifyStack) wire.TelegramStatus {
	st := ns.tg.Status(ctx)
	return wire.TelegramStatus{TokenSet: st.TokenSet, BotUsername: st.Bot, Paired: st.Paired, Health: st.Health, Unauthorized: apikit.Safe(st.Unauthorized)}
}

func (s *Server) telegramStatus(c *apikit.Call) (apikit.Reply, error) {
	ns, err := s.notifications()
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: s.telegramStatusOf(c.R.Context(), ns)}, nil
}

func (s *Server) setTelegramToken(c *apikit.Call) (apikit.Reply, error) {
	ns, err := s.notifications()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.TelegramTokenRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if _, err := ns.tgMgr.SetToken(c.R.Context(), strings.TrimSpace(req.Token)); err != nil {
		switch {
		case errors.Is(err, telegram.ErrTokenShape):
			return apikit.Reply{}, apikit.InvalidField("token", "%s", err.Error())
		case errors.Is(err, telegram.ErrAuth):
			return apikit.Reply{}, apikit.NewErr(wire.CodeValidationFailed, "Telegram refused the token (auth)")
		}
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: s.telegramStatusOf(c.R.Context(), ns)}, nil
}

func (s *Server) clearTelegramToken(c *apikit.Call) (apikit.Reply, error) {
	ns, err := s.notifications()
	if err != nil {
		return apikit.Reply{}, err
	}
	if err := ns.tgMgr.ClearToken(c.R.Context()); err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: s.telegramStatusOf(c.R.Context(), ns)}, nil
}

func (s *Server) telegramPairingCode(c *apikit.Call) (apikit.Reply, error) {
	ns, err := s.notifications()
	if err != nil {
		return apikit.Reply{}, err
	}
	code, bot, err := ns.tgMgr.BeginPairing(c.R.Context())
	switch {
	case errors.Is(err, telegram.ErrPaired):
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "a chat is already paired; unpair it first")
	case errors.Is(err, telegram.ErrNoToken):
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "store the bot token first")
	case err != nil:
		return apikit.Reply{}, err
	}
	out := wire.TelegramPairingCode{Code: code.Code, BotUsername: bot, ExpiresAt: wire.Time{Time: code.ExpiresAt.UTC()}}
	if out.Link = telegram.StartLink(bot, code.Code); out.Link != "" {
		if modules, err := qr.Encode(out.Link); err == nil {
			out.QR = &wire.RemoteQR{Size: wire.SafeInt(len(modules)), Rows: qr.Rows(modules)}
		}
	}
	return apikit.Reply{Status: http.StatusCreated, Data: out}, nil
}

func (s *Server) telegramUnpair(c *apikit.Call) (apikit.Reply, error) {
	ns, err := s.notifications()
	if err != nil {
		return apikit.Reply{}, err
	}
	if err := ns.tgMgr.Unpair(c.R.Context()); err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: s.telegramStatusOf(c.R.Context(), ns)}, nil
}

// announceCard puts a held call of a Telegram turn in front of the owner on
// Telegram. The cards of dashboard turns wait in the dashboard.
func (n *notifyStack) announceCard(_ context.Context, c approvals.Card) {
	if c.Channel != string(agent.ChannelTelegram) {
		return
	}
	card := telegram.ApprovalCard{ID: c.ID, SessionID: c.SessionID, Summary: c.Summary, Purpose: c.Purpose, Reason: c.Reason, Tier: c.Tier}
	// The send waits for Telegram's rate limit; the held call must not. It
	// belongs to the stack: closing the stack ends it and waits for it.
	n.wg.Go(func() {
		sendCtx, cancel := context.WithTimeout(n.ctx, time.Minute)
		defer cancel()
		_ = n.tg.SendApprovalCard(sendCtx, card)
	})
}

// scanSignInID prefixes the inbox item of a browser scan stopped at a sign-in
// wall; the source's name follows.
const scanSignInID = "scan-sign-in-"

// scanNeedsSignIn puts a browser scan's stop at a sign-in wall in the inbox,
// one item per source until a scan of that source gets past the sign-in page.
// Its fix is the session sync of the source's site and a scan run again.
func (s *Server) scanNeedsSignIn(ctx context.Context, source, detail string) {
	ns, err := s.notifications()
	if err != nil {
		return
	}
	_, err = ns.board.Raise(ctx, notify.RaiseArgs{ID: scanSignInID + source, Kind: notify.KindTask, Origin: notify.OriginSchedule,
		Title: "Sign in for the " + source + " scan", State: notify.NeedsYou, Reason: "sign-in", Detail: detail, Link: scanSignInFix(source).To})
	if err != nil {
		s.log.Warn("the scan sign-in item could not be raised", "source", source, "error", err.Error())
	}
}

// scanPastSignIn resolves a source's sign-in item once one of its scans got
// past the sign-in page.
func (s *Server) scanPastSignIn(ctx context.Context, source string) {
	ns, err := s.notifications()
	if err != nil {
		return
	}
	if err := ns.board.Resolve(ctx, scanSignInID+source); err != nil {
		s.log.Warn("the scan sign-in item could not be resolved", "source", source, "error", err.Error())
	}
}
