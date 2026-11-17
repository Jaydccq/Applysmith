package wire

// Board and notification types of the runs board, the needs-you inbox, the
// notification stream and the Telegram gateway.

// Entry actions in the inbox.
const (
	ActionOpenRun  = "open-run"
	ActionFocusTab = "focus-tab"
)

// BoardEntry is one row of the runs board. CanSubmit offers "Submit this
// form": the run's filled form waits at Submit in the tab it kept. FocusTab is
// that kept tab's id, the one the browser panel shows; CanFocusTab is true
// while it is set.
type BoardEntry struct {
	ID             string  `json:"id"`
	Kind           string  `json:"kind"`
	Origin         string  `json:"origin"`
	Title          string  `json:"title"`
	Stage          string  `json:"stage,omitempty"`
	Detail         string  `json:"detail,omitempty"`
	State          string  `json:"state"`
	Reason         string  `json:"reason,omitempty"`
	Outcome        string  `json:"outcome,omitempty"`
	BatchID        string  `json:"batchId,omitempty"`
	Link           string  `json:"link"`
	Ref            string  `json:"ref,omitempty"`
	CanFocusTab    bool    `json:"canFocusTab,omitempty"`
	FocusTab       string  `json:"focusTab,omitempty"`
	CanDone        bool    `json:"canDone,omitempty"`
	CanSubmit      bool    `json:"canSubmit,omitempty"`
	Revision       SafeInt `json:"revision"`
	StartedAt      Time    `json:"startedAt"`
	UpdatedAt      Time    `json:"updatedAt"`
	StateAt        Time    `json:"stateAt"`
	FinishedAt     *Time   `json:"finishedAt,omitempty"`
	SeenAt         *Time   `json:"seenAt,omitempty"`
	AcknowledgedAt *Time   `json:"acknowledgedAt,omitempty"`
	// Fix is the one page where the user makes the fix of a waiting entry,
	// when it is not the kept tab; absent otherwise.
	Fix *EntryFix `json:"fix,omitempty"`
}

// EntryFix is where the fix of a waiting entry is made: a dashboard route
// and what pressing it does. Scan names the source whose scan the item offers
// to run again once the fix is made.
type EntryFix struct {
	To    string `json:"to"`
	Label string `json:"label"`
	Scan  string `json:"scan,omitempty"`
}

// BoardStageQueued is the stage of an application start that waits for the
// apply window; the entry becomes its first run's when it starts.
const BoardStageQueued = "queued"

// BoardEntryList answers GET /api/runs.
type BoardEntryList struct {
	Entries []BoardEntry `json:"entries"`
}

// BoardSummary counts the board for the navigation badge and the tray.
type BoardSummary struct {
	Working            SafeInt `json:"working"`
	NeedsYou           SafeInt `json:"needsYou"`
	Blocked            SafeInt `json:"blocked"`
	DoneUnacknowledged SafeInt `json:"doneUnacknowledged"`
	Unseen             SafeInt `json:"unseen"`
}

// NeedsYouItem is one inbox item with its direct action.
type NeedsYouItem struct {
	Entry          BoardEntry `json:"entry"`
	WaitingSeconds SafeInt    `json:"waitingSeconds"`
	Action         string     `json:"action"`
}

// NeedsYouInbox answers GET /api/needs-you.
type NeedsYouInbox struct {
	Items  []NeedsYouItem `json:"items"`
	Unseen SafeInt        `json:"unseen"`
}

// NeedsYouCounts answers GET /api/needs-you/counts: what Needs you counts
// without listing. EmailConfirmations are the status changes a mail suggests,
// WikiDrafts the unpublished drafts, HeldSignals the hiring signals held for
// review, SuggestedAnswers the waiting suggestions the Answer Bank page offers
// (not sorted yet, or sorted reusable) and AnswerEdits the hand edits it offers
// to keep.
type NeedsYouCounts struct {
	EmailConfirmations SafeInt `json:"emailConfirmations"`
	WikiDrafts         SafeInt `json:"wikiDrafts"`
	HeldSignals        SafeInt `json:"heldSignals"`
	SuggestedAnswers   SafeInt `json:"suggestedAnswers"`
	AnswerEdits        SafeInt `json:"answerEdits"`
}

// NotificationSettings are the runtime switches. TelegramPaired is for display
// only: the Telegram switch is effective only while a chat is paired.
type NotificationSettings struct {
	Desktop        bool `json:"desktop"`
	Telegram       bool `json:"telegram"`
	FailedScans    bool `json:"failedScans"`
	TelegramPaired bool `json:"telegramPaired"`
}

// NotificationSettingsPatch changes the settings that are present. An unknown
// key or a value that is not a boolean is validation_failed and nothing
// changes.
type NotificationSettingsPatch struct {
	Desktop     *bool `json:"desktop,omitempty"`
	Telegram    *bool `json:"telegram,omitempty"`
	FailedScans *bool `json:"failedScans,omitempty"`
}

// NotificationTest answers POST /api/notifications/test: what a test
// notification did on each channel.
type NotificationTest struct {
	Channels []NotificationTestChannel `json:"channels"`
}

// NotificationTestChannel is one channel's result. State is sent, off,
// unavailable or failed; Detail says what happened and what to do.
type NotificationTestChannel struct {
	Channel string `json:"channel"`
	State   string `json:"state"`
	Detail  string `json:"detail"`
}

// Events of the notification stream.
const (
	NotifyAnnounce = "announce"
	NotifyWithdraw = "withdraw"
)

// NotificationAnnounce shows or replaces the notification with this identifier.
// Entry is set when the identifier is a board entry's id, the only kind a click
// acknowledges; a Gmail alert, a cost cap or a summary has an id of its own.
type NotificationAnnounce struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Link   string `json:"link"`
	Urgent bool   `json:"urgent,omitempty"`
	Entry  bool   `json:"entry,omitempty"`
}

// NotificationWithdraw closes the notification with this identifier.
type NotificationWithdraw struct {
	ID string `json:"id"`
}

// Notice is one message of the dashboard's Notices list: what no desktop
// notification showed (held by quiet hours, or announced while no app was
// attached) and plain messages such as a pairing.
type Notice struct {
	ID        SafeInt `json:"id"`
	Title     string  `json:"title"`
	Body      string  `json:"body"`
	Link      string  `json:"link,omitempty"`
	CreatedAt Time    `json:"createdAt"`
	Read      bool    `json:"read"`
}

// NoticeList answers GET /api/notices: newest first, at most 100.
type NoticeList struct {
	Notices []Notice `json:"notices"`
	Unread  SafeInt  `json:"unread"`
}

// NoticesRead asks POST /api/notices/read to mark the named notices read, or
// every one when IDs is empty.
type NoticesRead struct {
	IDs []SafeInt `json:"ids,omitempty"`
}

// Health values of the Telegram gateway.
const (
	TelegramConfigured = "configured"
	TelegramHealthy    = "healthy"
	TelegramStale      = "stale"
	TelegramConflicted = "conflicted"
	TelegramStopped    = "stopped"
	// TelegramRefused: Telegram refuses the saved token (it was revoked).
	TelegramRefused = "refused"
)

// TelegramStatus answers GET /api/telegram/status. It never carries the token
// or the owner's identifiers.
type TelegramStatus struct {
	TokenSet     bool    `json:"tokenSet"`
	BotUsername  string  `json:"botUsername,omitempty"`
	Paired       bool    `json:"paired"`
	Health       string  `json:"health"`
	Unauthorized SafeInt `json:"unauthorizedAttempts"`
}

// TelegramTokenRequest stores a bot token. The token is validated with Telegram
// and kept in the keychain.
type TelegramTokenRequest struct {
	Token string `json:"token"`
}

// TelegramPairingCode is a one-time code the owner sends to the bot.
type TelegramPairingCode struct {
	Code        string `json:"code"`
	BotUsername string `json:"botUsername"`
	ExpiresAt   Time   `json:"expiresAt"`
	// Link opens the bot with the code as its start parameter, so pressing
	// Start in Telegram pairs the chat; QR is that link for a phone. Both are
	// left out while the bot's name is unknown.
	Link string    `json:"link,omitempty"`
	QR   *RemoteQR `json:"qr,omitempty"`
}

// DayView answers GET /api/day: what was done for the user today, what is left
// for them and what waits on employers, each line with the page it is about,
// and today's counts. Home's Today, the daily summary, Telegram's /today and
// the Ask Agent's activity_today say the same.
type DayView struct {
	// Date is the local day, YYYY-MM-DD.
	Date string    `json:"date"`
	Did  []DayLine `json:"did"`
	Left []DayLine `json:"left"`
	// Employers are the applications waiting for a reply and the interviews.
	Employers []DayLine `json:"employers"`
	Counts    DayCounts `json:"counts"`
}

// DayLine is one sentence of the day and the dashboard route it is about.
type DayLine struct {
	Text string `json:"text"`
	Link string `json:"link"`
}

// DayCounts are today's numbers: postings found and evaluated, postings ready to
// apply (Apply now and Selective, at this moment), applications made today by
// the user and by the agent, and the things that wait for the user now (every
// waiting run, follow-up due and job email to act on).
type DayCounts struct {
	Found          SafeInt `json:"found"`
	Evaluated      SafeInt `json:"evaluated"`
	Ready          SafeInt `json:"ready"`
	AppliedByUser  SafeInt `json:"appliedByUser"`
	AppliedByAgent SafeInt `json:"appliedByAgent"`
	Waiting        SafeInt `json:"waiting"`
}
