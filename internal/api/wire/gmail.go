package wire

// GmailStage is the status of one stage of a sync run.
type GmailStage struct {
	Stage  string `json:"stage"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// GmailCounts are the per-run tallies.
type GmailCounts struct {
	Listed       SafeInt `json:"listed"`
	Fetched      SafeInt `json:"fetched"`
	Classified   SafeInt `json:"classified"`
	Deduplicated SafeInt `json:"deduplicated"`
	Stored       SafeInt `json:"stored"`
	Review       SafeInt `json:"review"`
	Ignored      SafeInt `json:"ignored"`
	Linked       SafeInt `json:"linked"`
	Tombstoned   SafeInt `json:"tombstoned"`
	Failed       SafeInt `json:"failed"`
}

// GmailRun is one durable sync record.
type GmailRun struct {
	ID               SafeInt      `json:"id"`
	Trigger          string       `json:"trigger"`
	Status           string       `json:"status"`
	Mode             string       `json:"mode"`
	StartedAt        Time         `json:"startedAt"`
	CoverageComplete bool         `json:"coverageComplete"`
	FallbackUsed     bool         `json:"fallbackUsed"`
	FailedStage      string       `json:"failedStage,omitempty"`
	Error            string       `json:"error,omitempty"`
	Stages           []GmailStage `json:"stages"`
	Counts           GmailCounts  `json:"counts"`
}

// GmailRunList answers GET /api/gmail/runs, newest first.
type GmailRunList struct {
	Runs []GmailRun `json:"runs"`
}

// GmailHealth is the offline Gmail health read model. Connected is true while
// an app password is stored; Login is how Gmail last answered it (ok, refused,
// or unknown when the keychain cannot be read; empty while not connected) and
// LoginError is Gmail's answer to a refused sign-in. Sync is never-run,
// healthy, partial or degraded. While Running is true the previous run's
// failure is not the story.
type GmailHealth struct {
	Connected        bool      `json:"connected"`
	Login            string    `json:"login"`
	LoginError       string    `json:"loginError,omitempty"`
	Sync             string    `json:"sync"`
	Running          bool      `json:"running"`
	CursorAgeSeconds *SafeInt  `json:"cursorAgeSeconds,omitempty"`
	FailedStage      string    `json:"failedStage,omitempty"`
	LatestRun        *GmailRun `json:"latestRun,omitempty"`
	Mailbox          string    `json:"mailbox,omitempty"`
}

// GmailConnect is a Gmail address and its Google app password. Gmail is
// signed in to with them first; only an accepted pair is stored, the password
// in the keychain. A refusal names a field, never a value.
type GmailConnect struct {
	Address  string `json:"address"`
	Password string `json:"password"`
}

// GmailConnected answers an accepted connection. Previous is the mailbox
// connected before when this is another account, whose sync starts again.
type GmailConnected struct {
	Previous string      `json:"previous,omitempty"`
	Health   GmailHealth `json:"health"`
}

// GmailTest answers the connection test: the messages of the last day.
type GmailTest struct {
	Messages SafeInt `json:"messages"`
}

// GmailAccountLink is a qualifying activation or reset link. The address is a
// bearer secret: it travels in this body and nowhere else.
type GmailAccountLink struct {
	URL        string `json:"url"`
	Host       string `json:"host"`
	ReceivedAt Time   `json:"receivedAt"`
	From       string `json:"from"`
}

// GmailSigninLink is a sign-in link offered for display only. The product
// never fetches or navigates to it.
type GmailSigninLink struct {
	URL         string   `json:"url"`
	Host        string   `json:"host"`
	ReceivedAt  Time     `json:"receivedAt"`
	From        string   `json:"from"`
	Subject     string   `json:"subject"`
	Reasons     []string `json:"reasons"`
	DisplayOnly bool     `json:"displayOnly"`
}

// GmailInboxThread is one row of the action inbox. Lane, owner and attention
// are derived from stored signals by fixed rules, never by a model.
type GmailInboxThread struct {
	ThreadID string `json:"threadId"`
	Employer string `json:"employer"`
	Role     string `json:"role,omitempty"`
	Subject  string `json:"subject"`
	State    string `json:"state"`
	// Kind is the type of the message that decided the thread, or review.
	Kind           string  `json:"kind"`
	Lane           string  `json:"lane"`
	Owner          string  `json:"owner"`
	Attention      string  `json:"attention"`
	NextAction     string  `json:"nextAction"`
	DraftKind      string  `json:"draftKind,omitempty"`
	StalenessDays  SafeInt `json:"stalenessDays"`
	DueAt          *Time   `json:"dueAt,omitempty"`
	LastIncomingAt Time    `json:"lastIncomingAt"`
	// Link is the link decision of the deciding signal: auto, review or unlinked.
	Link        string   `json:"link"`
	CandidacyID *SafeInt `json:"candidacyId,omitempty"`
	// Done says the user marked the thread done and no mail has come since.
	Done bool `json:"done"`
}

// GmailInboxPage answers GET /api/gmail/inbox. Next is an opaque cursor, empty
// on the last page; Total counts every thread the filters keep, so a request
// with limit=1 is a count.
type GmailInboxPage struct {
	Threads []GmailInboxThread `json:"threads"`
	Next    string             `json:"next,omitempty"`
	Total   SafeInt            `json:"total"`
}

// GmailTimelineEntry is one signal of a thread with its evidence trail. For a
// status the signal moved, RowStatus is the row's status now and PutBack is
// true while the move is still the row's latest status change (as GmailMove).
type GmailTimelineEntry struct {
	MessageID     string        `json:"messageId"`
	Type          string        `json:"type"`
	Tier          string        `json:"tier"`
	Direction     string        `json:"direction"`
	Employer      string        `json:"employer"`
	Role          string        `json:"role,omitempty"`
	Subject       string        `json:"subject"`
	Snippet       string        `json:"snippet"`
	ReceivedAt    Time          `json:"receivedAt"`
	DueAt         *Time         `json:"dueAt,omitempty"`
	RuleID        string        `json:"ruleId"`
	ReviewReasons []string      `json:"reviewReasons"`
	Link          string        `json:"link"`
	LinkReasons   []string      `json:"linkReasons"`
	Evidence      any           `json:"evidence,omitempty"`
	Outcome       *GmailOutcome `json:"outcome,omitempty"`
	RowStatus     string        `json:"rowStatus,omitempty"`
	PutBack       bool          `json:"putBack"`
}

// GmailThreadDetail answers GET /api/gmail/inbox/{threadId}.
type GmailThreadDetail struct {
	Thread   GmailInboxThread     `json:"thread"`
	Timeline []GmailTimelineEntry `json:"timeline"`
}

// GmailSummary answers GET /api/gmail/threads/{id}/summary. Method is model or
// deterministic.
type GmailSummary struct {
	Text        string `json:"text"`
	GeneratedAt Time   `json:"generatedAt"`
	Method      string `json:"method"`
	Cached      bool   `json:"cached"`
}

// GmailDraft is a reply or follow-up draft of one thread for the user to copy;
// nothing is sent. Kind is reply or follow_up. Method is model, or application
// when drafted from the linked application, with its claims. Note says why a
// linked thread's new draft rests on the thread alone.
type GmailDraft struct {
	Text        string   `json:"text"`
	Kind        string   `json:"kind"`
	GeneratedAt Time     `json:"generatedAt"`
	Method      string   `json:"method"`
	Confirmed   []string `json:"confirmed,omitempty"`
	ToConfirm   []string `json:"toConfirm,omitempty"`
	Note        string   `json:"note,omitempty"`
}

// GmailThreadDraft answers GET and POST /api/gmail/threads/{id}/draft. The GET
// holds the draft kept for the thread's current state, or none; the POST
// writes a new one and keeps it. Kind is the kind a new draft takes.
type GmailThreadDraft struct {
	Draft *GmailDraft `json:"draft,omitempty"`
	Kind  string      `json:"kind"`
}

// GmailLinkItem is one signal in the link review queue. When the tracker
// refused the signal's status change, Outcome names the refusal. Strength is
// the match in a word by the linker's thresholds (close, weak or none), and
// NearlyAsClose is true when the runner-up is within the lead it asks for.
type GmailLinkItem struct {
	SignalID      string        `json:"signalId"`
	ThreadID      string        `json:"threadId"`
	Type          string        `json:"type"`
	Tier          string        `json:"tier"`
	Employer      string        `json:"employer"`
	Role          string        `json:"role,omitempty"`
	Subject       string        `json:"subject"`
	ReceivedAt    Time          `json:"receivedAt"`
	Decision      string        `json:"decision"`
	CandidacyID   *SafeInt      `json:"candidacyId,omitempty"`
	Score         float64       `json:"score"`
	RunnerUp      float64       `json:"runnerUp"`
	Reasons       []string      `json:"reasons"`
	Strength      string        `json:"strength"`
	NearlyAsClose bool          `json:"nearlyAsClose"`
	Outcome       *GmailOutcome `json:"outcome,omitempty"`
}

// GmailOutcome is what applying a signal to the tracker did. Result is
// changed, no_change, review or confirmation; CandidacyID is the row it was
// applied to.
type GmailOutcome struct {
	Result      string   `json:"result"`
	FromStatus  string   `json:"fromStatus,omitempty"`
	ToStatus    string   `json:"toStatus,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	CandidacyID *SafeInt `json:"candidacyId,omitempty"`
}

// GmailLinkList answers GET /api/gmail/links.
type GmailLinkList struct {
	Items []GmailLinkItem `json:"items"`
}

// GmailLinkConfirm confirms a candidacy for the thread of a signal.
type GmailLinkConfirm struct {
	CandidacyID SafeInt `json:"candidacyId"`
}

// GmailLinkConfirmed answers a confirmation. Outcome is the tracker outcome of
// the confirmed signal, when it has one.
type GmailLinkConfirmed struct {
	SignalID    string        `json:"signalId"`
	ThreadID    string        `json:"threadId"`
	CandidacyID SafeInt       `json:"candidacyId"`
	Outcome     *GmailOutcome `json:"outcome,omitempty"`
}

// GmailLinkRejected answers a rejection: the signal is suppressed.
type GmailLinkRejected struct {
	SignalID string `json:"signalId"`
}

// GmailLinkRestored answers the undo of a rejection: the signal is back where
// it was.
type GmailLinkRestored struct {
	SignalID string `json:"signalId"`
}

// GmailThreadDone answers marking a thread done and the undo of it.
type GmailThreadDone struct {
	ThreadID string `json:"threadId"`
}

// GmailThreadDismissed answers dismissing a thread: the ids of the signals now
// marked not job related, which restoring them (the link restore route) takes
// off again.
type GmailThreadDismissed struct {
	ThreadID  string   `json:"threadId"`
	SignalIDs []string `json:"signalIds"`
}

// GmailConfirmation is a request to confirm an offer. The status moves only
// when the user confirms. Company and Role are the linked tracker row's.
type GmailConfirmation struct {
	ID          SafeInt `json:"id"`
	SignalID    string  `json:"signalId"`
	ThreadID    string  `json:"threadId"`
	CandidacyID SafeInt `json:"candidacyId"`
	Company     string  `json:"company,omitempty"`
	Role        string  `json:"role,omitempty"`
	Status      string  `json:"status"`
	State       string  `json:"state"`
	CreatedAt   Time    `json:"createdAt"`
}

// GmailConfirmationList answers GET /api/gmail/confirmations.
type GmailConfirmationList struct {
	Confirmations []GmailConfirmation `json:"confirmations"`
}

// GmailMove is a status a mail moved on its own: the tracker outcome of one
// message, the row's company and role, and the status the row has now.
// PutBack is true while the move is still the row's latest status change, the
// only time the page offers to put it back.
type GmailMove struct {
	ThreadID string       `json:"threadId"`
	Subject  string       `json:"subject"`
	At       Time         `json:"at"`
	Outcome  GmailOutcome `json:"outcome"`
	Company  string       `json:"company"`
	Role     string       `json:"role"`
	Status   string       `json:"status"`
	PutBack  bool         `json:"putBack"`
}

// GmailMoveList answers GET /api/gmail/moves: the moves of the last two weeks,
// newest first, at most ten.
type GmailMoveList struct {
	Moves []GmailMove `json:"moves"`
}

// GmailConfirmationDecided answers a confirm, dismiss or restore. RowStatus is
// the status the undo of a confirmation put the linked row back to.
type GmailConfirmationDecided struct {
	ID        SafeInt `json:"id"`
	State     string  `json:"state"`
	RowStatus string  `json:"rowStatus,omitempty"`
}

// GmailDigestItem is one thread line of a digest. Draft is a reply draft the
// product never sends.
type GmailDigestItem struct {
	Key        string `json:"key"`
	Section    string `json:"section"`
	Company    string `json:"company"`
	Role       string `json:"role,omitempty"`
	Lane       string `json:"lane"`
	State      string `json:"state"`
	Owner      string `json:"owner"`
	NextAction string `json:"nextAction"`
	Due        string `json:"due,omitempty"`
	Line       string `json:"line"`
	DraftKind  string `json:"draftKind,omitempty"`
	Draft      string `json:"draft,omitempty"`
}

// GmailDigest is a stored digest or weekly review. Delivery says per channel
// sent, skipped or failed.
type GmailDigest struct {
	ID          SafeInt            `json:"id"`
	Kind        string             `json:"kind"`
	Trigger     string             `json:"trigger"`
	Status      string             `json:"status"`
	CreatedAt   Time               `json:"createdAt"`
	WindowStart Time               `json:"windowStart"`
	WindowEnd   Time               `json:"windowEnd"`
	Method      string             `json:"method"`
	Headline    string             `json:"headline"`
	Text        string             `json:"text"`
	Items       []GmailDigestItem  `json:"items"`
	Muted       []GmailDigestItem  `json:"muted"`
	Omitted     SafeInt            `json:"omitted"`
	Receipts    SafeInt            `json:"receipts"`
	Funnel      map[string]SafeInt `json:"funnel,omitempty"`
	Narrative   string             `json:"narrative,omitempty"`
	Delivery    map[string]string  `json:"delivery"`
	Error       string             `json:"error,omitempty"`
}

// GmailDigestList answers GET /api/gmail/digests, newest first.
type GmailDigestList struct {
	Digests []GmailDigest `json:"digests"`
}

// GmailDigestVerdicts answers GET /api/gmail/digests/feedback: the verdict in
// force per thread, up or down.
type GmailDigestVerdicts struct {
	Verdicts map[string]string `json:"verdicts"`
}

// GmailDigestFeedback rates a thread of a digest: up, down or clear.
type GmailDigestFeedback struct {
	ThreadKey string `json:"threadKey"`
	Verdict   string `json:"verdict"`
}

// GmailDigestRated answers a rating.
type GmailDigestRated struct {
	DigestID  SafeInt `json:"digestId"`
	ThreadKey string  `json:"threadKey"`
	Verdict   string  `json:"verdict"`
}

// GmailSettings are the switches of the unattended Gmail work.
type GmailSettings struct {
	Alerts           bool `json:"alerts"`
	ScheduledDigests bool `json:"scheduledDigests"`
}

// GmailSettingsPatch changes the switches that are present.
type GmailSettingsPatch struct {
	Alerts           *bool `json:"alerts,omitempty"`
	ScheduledDigests *bool `json:"scheduledDigests,omitempty"`
}

// GmailSyncStarted answers POST /api/gmail/sync: the run goes on in the
// background and its result reaches the health read model and the runs list.
type GmailSyncStarted struct {
	Started bool `json:"started"`
}
