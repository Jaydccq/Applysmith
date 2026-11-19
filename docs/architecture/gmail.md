# Gmail outcomes, the action inbox and the digests

Packages: `internal/gmail` (layer 45) holds the sync, the storage, the link
and outcome passes, the inbox read model, summaries, drafts, alerts and the
digests. `internal/gmail/lifecycle` and `internal/gmail/link` (layer 43) hold
the pure rules: the thread state, lane, owner and attention, and the link score.
Tables are in `internal/store/migrations/0012_gmail.sql` (sync) and
`0024_gmail_outcomes.sql` (everything below). The HTTP routes are in
`internal/api/gmailapi`; the timers are in `internal/api/gmail_timers.go`.

Everything is derived from mail the sync already stored. Nothing here makes a
Gmail request, and a model never decides a lane, an owner or a status.

## Deadline facts

The classifier validates absolute calendar dates with Go's date parser instead
of letting date construction roll an impossible day into another month. English
month names are full names, three-letter abbreviations or `Sept`, optionally
ending in a period. Days/weeks relative to the UTC received time must fit
`time.Duration`; conversion errors and overflow leave the due time absent. The yearless-date
rule still advances a date more than thirty days past to next year, but that
selected year must also admit the date. Valid leap dates and existing weekday
semantics are retained. An invalid deadline does not discard an otherwise valid
assessment signal; inbox, attention and digest logic see no invented due time.

## Rejections and receipts under low-priority wording

The low-priority families are evaluated before rejection and receipt, and a
rejection or a receipt often carries their wording: a survey invitation, "Join
our talent network", an unsubscribe link. The survey claim, the job-alert claim
and the newsletter claim an incidental offer word raises are therefore each
vetoed on the trace by `candidate_rejection` (a rejection addressed to this
applicant from a trusted sender) or `application_receipt` (a receipt phrase from
a trusted sender the taxonomy allows a receipt from, in mail with no
hard-rejection wording). A digest that lists jobs keeps its low-priority claim,
and so does a mail whose subject is a job alert. A mail is a pure newsletter, whose
job-alert claim is vetoed as `pure_newsletter`, only when it carries no hiring
word, plural forms ("jobs", "roles") included.

An offer, an interview invitation or a requested action about the applicant's
own application keeps its type under a list footer too. `candidate_offer` (an
explicit offer that passes the offer gate), `candidate_interview` (an ATS or
recruiting sender, a scheduling artifact, an interview action and "your
application" or "your interview" wording) and `candidate_action` (an
action-required phrase about "your application" from a trusted sender) veto
the low-priority claims, and for the first two an "Unsubscribe" or "manage
preferences" footer is not read as newsletter template. Digest wording
("recommended jobs", "weekly digest") or hard-rejection wording sets no guard.

Hard-rejection wording found only inside a condition ("If you are not selected
for this position you will hear from us") is vetoed as `conditional_rejection`,
so a receipt that says it stays a receipt. A receipt phrase in the body vetoes
a soft rejection ("unfortunately", "other candidates") as `receipt_in_body`,
unless the "unfortunately" sentence states the decision ("Thank you for your
application. Unfortunately, we have decided not to take it further", "...
the role is no longer available"); that mail is a rejection.

## Classifier version

The rules are versioned as `gmail-classifier-go-6`. A normal versioned sync or
an explicitly confirmed local reclassification can create a new decision from
retained mail; the previous decision remains audit history. Readers select the
newest decision by decision time and then insertion order, so a same-millisecond
reclassification also replaces the old signal and timeline evidence. No schema
migration or automatic personal-mail reclassification runs as part of this source change.

The role is read from the subject, then the body. A body capture must hold a
title noun (`associate` included) and starts at a capital letter, so "the next
round for our Platform Reliability Engineer role" reads "Platform Reliability
Engineer"; a mail whose role is not read links on company, subject and sender
alone, and the role conflict below cannot stop it.

## Linking and outcomes

`Pipeline.Link` is the link stage of a sync: `Resolve`, then `ApplyOutcomes`.

- `link.Decide` scores a signal against every tracker row, archived ones
  included (company 0.5, role overlap up to 0.4, subject 0.2, sender 0.15, a
  role conflict minus 0.3). `auto` needs 0.5 and a lead of 0.25;
  `review` needs 0.2. Two named roles conflict when, without generic nouns
  (engineer, senior, ...), neither covers 60 percent of the other, or they
  share no title-noun family (engineer with developer, analyst, manager,
  associate, ...) and no word but a level (graduate, entry, early career,
  ...): "Entry Level Backend Engineer" against "Entry Level Analyst". A conflict holds an auto score for
  `review` (`role_qualifier_mismatch`). Decisions record `link-2`. A thread already linked to one candidacy inherits it; to
  two, it is `review`. A signal whose review reasons include
  `sender_unverified` or `signup_window_unknown` is `review` whatever its
  score. A failed load of the rows fails the stage: an empty list would be
  written down as "no viable candidate".
- A link the user confirms (`POST /api/gmail/links/{signalId}/confirm`) covers
  the whole thread and is final (`gmail_thread_links`). It also withdraws the
  thread's `review` outcomes, so the pass that follows asks the tracker again
  for each: a change the tracker now makes leaves the Link review queue, and one
  it still refuses stays there with the reason. A rejection suppresses
  the signal through a table that survives reclassification, until the user
  undoes it (`POST /api/gmail/links/{signalId}/restore` deletes that one row and
  writes nothing else).
- `ApplyOutcomes` replays the signals of a candidacy in received order against
  a running status that starts from the row's current status and asks the
  tracker for each change as the `gmail` actor, with the source message id and
  signal type in the event. `gmail_outcomes` is the ledger: a message is
  applied once, and a refused one again only when the user confirms its link. A step to the same or an earlier status is `no_change`; a step
  the tracker refuses is `review`, with the tracker's reason. Only auto links of
  tier `actionable` write, and links the user confirmed.
- An offer never changes the status. It creates a row in `gmail_confirmations`,
  listed by `GET /api/gmail/confirmations` with the linked tracker row's company
  and role; confirming it (`POST /api/gmail/confirmations/{id}/confirm`) moves the row to
  `offer` as the user, because the tracker's table gives the `gmail` actor no
  such transition. Dismissing it is kept (a later sync never asks again for
  that message); `POST .../{id}/restore` puts a dismissed or confirmed one back
  to pending. For a confirmed one it also puts the row back, in the same
  transaction (`tracker.RevertStatus`), to the status before the confirmation's
  status event, and answers it as `rowStatus`, and takes back the note the
  confirmation wrote ("Offer confirmed from a Gmail message."); once a later
  status change was made, or the row no longer has `offer`, it is `conflict`
  and nothing changes.
- `GET /api/gmail/moves` lists the `changed` outcomes of the last 14 days,
  newest first, at most 10, each with its thread, subject, the row's company
  and role, the status the row has now, and `putBack`: the move is still the
  row's latest status change and the row still has the status it set
  (`tracker.Detail.Revertible`, the rule `RevertStatus` applies). The Review tab
  shows them as "Moved by your mail" and offers the drawer's "Put it back" only
  on `putBack`; the Tracker row's history offers the same on a Gmail status
  change that is still the row's latest.
- The Link review rows say the match in words from the item's `strength`
  (`link.Strength`: `close` from `link.AutoScore`, `weak` from
  `link.ReviewFloor`, else `none`) and `nearlyAsClose` (`link.NearlyAsClose`:
  the runner-up is within `link.AutoLead`), so the page holds no thresholds;
  the type, tier, score and runner-up stay in the line's tooltip.
- `RelinkIfRowsChanged` runs on the `gmail-alerts` timer and resolves old
  review and unlinked signals again when a tracker row appeared, whoever added
  it. A pass scores every such signal against every tracker row, so
  `link.NewPool` tokenizes each row once per pass and `Decide` each signal
  once, never per pair. Measured on 2026-11-12 on the owner's workspace (1,827
  signals, 2,054 rows): 44 s per pass when both sides were tokenized per pair,
  which under load passed the timer's 60 s limit, failed, and left the pass to
  run again on the next tick (207 `gmail-alerts` failures in one log); 1.7 s
  with the pool, every decision unchanged.

### Repairing links made before the role rule

`Resolve` never decides an auto link again, so a link an older rule made
(`linker_version` `link-1`) to a row of another role stays, and so does the
status it moved. To list the candidates, read the auto, unconfirmed `link-1`
rows of `gmail_links` with the row's role and the mail's subject, snippet and
newest decision's role; the snippet often names the role the classifier did
not read. The owner corrects each from the product. The status first: "Put it back"
while the move is still the row's latest, or else `PATCH /api/candidacies/{id}`
or `applysmith tracker set-status <id> <status>`; a step back by the user is
recorded as `status_corrected`. Then the link: confirm the thread to the row
the mail is about (`POST /api/gmail/links/{signalId}/confirm`, after
`applysmith tracker add` when there is none), which is final for the thread.
A message that already moved a row is not applied again, so the right row's
status is set by hand. Reject (`.../reject`) is for mail that is not about
an application at all.

## Thread lifecycle and the inbox

`lifecycle.Derive` reads the signals of one Gmail thread. The latest incoming
event of tier `actionable` decides the state (ties by rank offer, rejected,
interview, assessment, responded, applied); a `review` tier incoming signal
counts as `thread_continuity` when the thread also holds an actionable one,
except an assessment-submitted confirmation. Lane, owner and next action follow
fixed rules; thresholds are constants in the package (14 days stale, 7 days past
a due time, 14 and 28 days to expire user-owned work, 48 hours urgent).
`GET /api/gmail/inbox` filters by lane, owner, link decision, `kind` and `q`, and `candidacy` (the
threads linked to that tracker row by an automatic or a confirmed link, as the
Tracker row drawer lists them), sorts by attention and then recency, pages with
an opaque cursor and returns at most 200 rows. `total` counts every thread the
filters keep, not the page: the sidebar's Gmail badge asks for `lane=do_now&limit=1`
and reads it, so it always equals the do-now lane the Gmail page lists, and the
page's pager reads it as "N threads, page x of y". `GET /api/gmail/inbox/{threadId}`
returns the timeline with evidence and, per message, its `gmail_outcomes` row as
`outcome` (with the row's `candidacyId`); a `changed` one also carries the
row's status now as `rowStatus` and `putBack` by the rule of `/api/gmail/moves`.
The drawer names a status the mail moved and offers "Put it back to <status>"
only on `putBack`: a user correction (`PATCH /api/candidacies/{id}`) sent only
while the row still has the status the mail gave it; the outcome ledger keeps
the message from being applied again.

`kind` is the type of the message that decided the thread (`recruiter_reply_needed`,
`action_required`, `online_assessment`, `interview_scheduled`, `interview_reminder`,
`offer`, `rejection`, `applied_confirmation`), or `review` for a thread no
message decided; the page names them "Reply needed", "Action required",
"Assessment", "Interview", "Interview reminder", "Offer", "Rejection",
"Application received" and "Needs review". `q` keeps the threads whose employer,
role and subject, taken together, hold every word of it in any case; stored
bodies and snippets are never searched. Both filters sit in the page address
like the others, and `total` counts what they keep.

### Deciding a thread

The lane is derived from mail, so a thread whose request the user has handled
would stay in do-now until a reply is seen or it expires. Two decisions end it
early, and both cover only the mail seen so far: **a message that arrives after
the decision brings the thread back**, in whatever lane the rules give it
(promotional or other low-tier mail does not).

- **Done** (`POST /api/gmail/inbox/{threadId}/done`) is for a thread in the
  do-now, prepare or review lane (anything else is `conflict`). It writes the
  thread and the received time of its newest shown message, of any direction (not
  low-priority or suppressed mail, which asks nothing of the user), to
  `gmail_thread_done` (migration `0050`). While no newer message exists the
  thread reads as `closed`, owner `none`, attention `info`, next action "Marked
  done", `done: true`, its state and kind kept, so it leaves the do-now lane, the
  sidebar badge, Home's "needs you now" line, the alerts and the briefs, and
  stays readable under the closed lane. `POST .../reopen` deletes the row (the
  undo); marking a done thread again changes nothing.
- **Dismiss** (`POST /api/gmail/inbox/{threadId}/dismiss`) means not a job email
  or not for the user. It is the "Not job related" mark of the link review
  (`gmail_link_rejections`), applied to every visible incoming signal of the
  thread, and answers the ids it marked. The undo is the existing
  `POST /api/gmail/links/{signalId}/restore` for each id, so it takes off those
  marks and no mark the user made earlier. A later message is a new signal and
  is not marked.

The page puts Done and Dismiss on each inbox row and in the thread drawer
(a drawer decision closes it); a note above the tabs names the decision with
Undo, and focus goes to the row that took the decided one's place. A done row
shows Reopen instead. The drawer's I replied marks a thread in those lanes done
too, and its Undo reopens it with the note it took back. The page names lanes,
owners, links and match reasons in plain words ("Yours to do", "Matched to
your application"); a message's rule id is in its line's tooltip. There is no watch decision: the lane model has no lane
for a thread the user follows but owes nothing on.

## Summaries and drafts

`GET /api/gmail/threads/{id}/summary` uses the `gmail-summary` task. The prompt
holds the stored facts inside `<<<MAIL ... MAIL>>>` blocks, scrubbed of links,
codes and tokens. A text is accepted only when it has 30 characters, ends in
sentence punctuation, is at most 600 characters, holds no link or token and no
number absent from the facts. One retry; then the deterministic text, which is
not cached. A cached model text is reused while the newest message and the
classifier version are unchanged. Reply drafts use the `gmail-digest` task, are
cached the same way, and are shown only; the product has no send path.

A draft is also written on request for any thread: `POST
/api/gmail/threads/{id}/draft` (dashboard session only, one model call) writes
a new one and keeps it in place of the stored one (Regenerate is the same call),
and `GET` on the same path returns the draft kept for the thread's current state,
whether a digest or a request wrote it, or none; a newer message makes it stale.
The kind is the lifecycle's draft kind, else a follow-up while the company owes
the next move, else a reply. A thread linked to an in-flight application is
drafted by the Tracker's follow-up drafter (`message-draft`) from the row's fact
book (CV, profile, report, voice notes, tracker) plus the thread; its claims,
each naming its source, are kept with it (`method: application`, column `claims`, migration `0051`). Other threads
get this thread's facts only (signals, snippets, retained bodies, scrubbed).
With no `message-draft` route a linked thread gets the thread-only draft and
the response's `note` says why. Either must pass the never-invent checks, else
`unavailable` in plain words. With no model routed the reply is `unavailable` with
reason `no_model_route`, as for the follow-up drafter. Nothing is sent.

## Alerts

`Stack.EvaluateAlerts` runs on each tick (timer `gmail-alerts`) and after a
completed sync. The alert state of a thread is the latest incoming event plus
owner plus lane; it is written to `gmail_alert_states` before anything is
delivered. Nothing is evaluated before a sync has finished, the first
evaluation seeds silently, states are recorded while alerts are off, and only
mail from the last 24 hours alerts. The alert goes to `Stack.RaiseAlert`, which
the server wires to the notification policy. Its link is the thread's
(`ThreadLink`, `/gmail?thread=<id>`), and a due time that is a date alone (the
classifier reads it as 00:00 UTC) is worded as that date, not as the local time
of its UTC midnight. On Telegram an alert carries buttons for its thread (see
"Acting from Telegram").

## Deadline reminders

After mail-agent's todos: an open job thread whose deciding message is an
actionable assessment, action or reply with a due time, in the do-now or prepare
lane, is a todo (`todoOf`, `internal/gmail/reminders.go`). Interviews are left
to the interview reminders. Nothing about a todo is stored: the lane, the done
mark and the newest mail decide it, so marking a thread done, dismissing it or a
later message closing it ends its reminders.

`Repo.Reminders` plans two per due day in local time: 19:00 the evening before
until the day begins (`eve`), and 09:00 on the day until it ends or until the
deadline when it has a time of day (`day`). The timer `gmail-reminders`
(`internal/api/gmail_timers.go`, each tick) announces each one due as
`gmail-reminder`, keyed `gmail-reminder|<thread>|<due day>|<slot>`, so each is
made once and a new due day is reminded of again. The title is the employer,
what is due and when ("Acme: online assessment, due tomorrow"); the link is
the thread's. Normal class, so quiet hours hold them. The timer is inactive in a
demo workspace and while the alerts switch is off.

`Repo.Snooze` (the Telegram "Tomorrow" button) stores one row in
`gmail_reminder_snoozes`: the next reminder is 09:00 tomorrow, in place of the
slots up to then; it is still sent up to 12 hours late (`SnoozeCatchUp`) for a
computer that was off. A thread that is not a todo is `ErrNoDeadline`.

## Thread actions

`Repo.Act` (`internal/gmail/inbox.go`) is the one place a thread is changed from
outside the Gmail page, for the Telegram buttons and the Ask Agent alike: `done`
(`MarkDone`), `reopen` (`ReopenDone`), `dismiss` (`DismissThread`, not job
mail), `restore` (`RestoreThread`, which takes off the rejection marks the
thread's newest dismissal wrote, as `RestoreLink` does one by one) and `snooze`
(`Repo.Snooze`). Each says what it did in a sentence for the user; an action
that would change nothing (a thread gone, one that asks nothing of the user,
one with no open deadline, nothing to set aside or put back) is a `*Refusal`
with its reason. They change only Applysmith's reading of the thread; Gmail is
never written.

## Acting from Telegram

An alert and a reminder reach Telegram with one row of buttons for their thread
(callback `mail:<action>:<thread id>`, a closed set): an alert offers Done (while
the thread asks something of the user), Not job mail and Draft reply; a
reminder offers Done and Tomorrow. `Backend.Mail` runs Draft reply through
`Stack.DraftReply` and every other button through `Repo.Act` (the buttons carry
the thread actions' names). Done and Not job mail rewrite the message with what
happened and offer Undo (`reopen`, `restore`); a draft comes as a message of its
own to copy into Gmail. Nothing is sent from the account.

## Digests

`Digests.Tick` (timer `gmail-digest`) runs the slot at 08:00 or 18:00 local
time, the Sunday 18:00 slot being the weekly review. A row in `gmail_digests`
with a unique `slot` claims the slot before anything is produced, so a failed
slot is never retried; a slot is caught up within six hours. The window starts
at the previous delivered digest, never more than 36 hours back. The model
writes the headline and one line per thread as JSON; any failure, invented
thread number or number absent from the facts keeps the deterministic text.
Reply drafts (at most 3, one 45-second budget) are produced beside it.
A digest line words a due time by `DueWords`, and the deterministic thread
summary names its day by the same rule, so a date alone is that date.
A thread waiting on the company drops a due time already past (the interview
that set it is over), and the weekly review's list of threads quiet for over
7 days says when each company last wrote, so a follow-up worth sending stands
apart from a thread long gone quiet. Neither weekly list holds a thread when a
later thread linked to the same candidacy holds a rejection (the company's
answer comes in a thread of its own); an unlinked thread is never closed by
the employer's name alone, which cannot tell two applications apart.
Telegram and the desktop are reached only through the notification policy
(`Policy.AnnounceTo`, one announcement of kind `gmail-digest` per sink, keyed by
the digest id), so quiet hours hold them and release them later, the key makes
each once-only and the Telegram sink's rate limit applies. The Telegram sink
sends the pages (under 3,700 characters, `Gateway.SendPages`) with the rating
keyboard on the last (callback `rate:<digest id>.<item>:<up|down>`); the
desktop gets the headline. The delivery record of those two says `sent` when
the announcement was queued. An Ask Agent conversation of channel `digest`
holds the text. Each channel's failure is recorded on its own.
An empty digest is stored and pushed nowhere. A digest made for the Ask Agent
is stored and delivered nowhere and starts no window.

The two switches (`GET`/`PATCH /api/gmail/settings`) gate the instant alerts
with the deadline reminders, and the scheduled digests; a manual run (`POST /api/gmail/digests/run`) is never
gated and is refused with `conflict` while another is being produced. It is a
background run (202 with the run id; the brief composes a model answer), and the
run ends with the new brief's id as its outcome, so the page opens it. The board
does not show the run: the brief announces itself.

## Dashboard controls

`POST /api/gmail/sync` starts a `manual` sync in the server's background and
answers `202`; a second request while one runs is `conflict`. The page reads the
outcome from the health read model. The dashboard connects Gmail through the
command line's own code (owner decision, round 47: build the whole connection
in the dashboard).

### Connection: IMAP with an app password

Gmail is read over IMAP (`imap.gmail.com:993`, TLS) with the user's address
and a Google app password (owner decision O-GM-2 in the migration design;
there is no OAuth client, consent flow or token). `gmail.Account` keeps the
address in the Gmail state and the password as the keychain secret
`gmail-app-password`; `gmail.Client` opens one session per run or lookup, and
the watcher (below) keeps one more open.

- **Connecting.** `PUT /api/gmail/account` (`{address, password}`) and
  `applysmith gmail connect --address <a> --password-stdin` call
  `Service.Connect`: the address must be one plain address and the password 16
  letters (Google's four groups of four, spaces dropped); then the session signs
  in, finds All Mail and opens it, and only then are both stored. A malformed
  field is `validation_failed` on `address` or `password`; a password Gmail
  refuses is `validation_failed` on `password` with Gmail's text; an unreachable
  Gmail is `unavailable` (`gmail_unreachable`, retryable). Nothing is stored on
  any of them, and no answer, log line or row carries the password. A connect
  wakes the watcher, which signs in again with what was stored, and switches
  back on the `gmail.sync` schedules a disconnect paused; it adds none.
- **Another account.** A connect to another address than the stored one starts
  that mailbox's sync afresh (coordinator decision, round 48): the cursor, a
  resume token and a bootstrap's mark are cleared, the reset time is recorded,
  and the next sync bootstraps with the usual lookback. The mail and links read
  from the old account stay. The answer's `previous` names the old address, and
  the page and the command line say "This is a different account from <old>.
  Mail already read stays; syncing starts again for <new>." Connecting the same
  account keeps its cursor. A sync that was running when the connection changed
  does not move the cursor (`Syncer.finish` compares `connected_at`).
- **Testing and disconnecting.** `POST /api/gmail/account/test` (and
  `applysmith gmail test`) signs in with the stored password and counts the
  messages of the last day, reading the ids of the newest five.
  `DELETE /api/gmail/account` (and `applysmith gmail disconnect`) removes the
  password from the keychain, resolves an open sign-in item, wakes the watcher,
  which closes its connection and waits for a password, and pauses every
  `gmail.sync` schedule, so none fails and raises the item
  (`scheduler.Manager.PauseGmailSync`); the address and the stored mail stay. The three routes are admin-class and denied on
  remote listeners; connect and disconnect are secret-class.
- **Sign-in state.** A LOGIN that Gmail answers `NO` is a `LoginError` with
  Gmail's own text, unless the text is a transient one ("too many
  connections", "try again"), which is unreachable like a failed connection.
  The first refusal of the stored password records `login_status: refused`
  with the text and fires `Hooks.OnRefused` once per outage, which raises the
  needs-you item "Gmail needs its app password" (id `gmail-sign-in`,
  reason `sign-in`, `RefusalDetail`); the next accepted sign-in, a connect or a
  disconnect clears it and fires `OnRestored`, which resolves the item. Health
  reads it offline as `login`: `ok`, `refused` (with `loginError`) or
  `unknown` when the keychain cannot be read. The schedule reason is
  `needs-gmail-sign-in`; the lookup reason is `gmail_not_signed_in`.
- **Read-only by construction.** A session sends only LOGIN, LIST, EXAMINE,
  UID SEARCH, UID FETCH, IDLE and LOGOUT and refuses any other command before it
  is sent; IDLE only waits for Gmail to announce changes. Folders are opened with EXAMINE and bodies fetched as
  `BODY.PEEK[]<0.4194304>`, so nothing is marked read and a message is cut at
  4 MiB. Each command has a 30-second deadline, which a FETCH gets again for
  each message it answers (one FETCH asks for up to fifty bodies), and a
  cancelled context closes the connection, also during the closing LOGOUT.
- **Folders, ids and labels.** All Mail is the folder whose LIST attributes
  hold `\All` (its name is localized), Spam the one with `\Junk`. Message and
  thread ids are `X-GM-MSGID` and `X-GM-THRID` in hexadecimal, the ids Gmail's
  web pages use; INTERNALDATE (whole seconds) is the receipt time. Labels come
  from `X-GM-LABELS`: `\Inbox`, `\Sent`, `\Important`, `\Starred`,
  `\Draft`, `\Spam` and `\Trash` map to `INBOX`, `SENT`, `IMPORTANT`,
  `STARRED`, `DRAFT`, `SPAM` and `TRASH`, and a user label keeps its name.
  Only `SENT` is read (the direction of a message).
- **Searches.** Gmail's search syntax goes through `UID SEARCH X-GM-RAW`. The
  bounded lookups (codes, links, receipts) search All Mail and then Spam,
  because All Mail excludes Spam; Gmail answers a Spam search only for a query
  that asks for it (`in:anywhere`, `in:spam`), so every lookup's query carries
  `in:anywhere`. Results are newest first by
  receipt time. `after` and `since` stay exclusive, at second resolution.
  Identical concurrent code lookups share one search (`internal/flight`); it
  runs without the cancellation of the request that started it, so a caller
  that gives up is answered at once and the others still get the code.
- **Sync cursor.** The cursor is `UIDVALIDITY:UID` of All Mail. A bootstrap
  reads the mark `UIDNEXT - 1` before it lists (`bootstrap_mark`), runs the
  three bootstrap searches newest first below the mark and sets the cursor to
  the mark after a complete drain; a capped bootstrap keeps a resume token
  (query index and the UID it stopped below). An incremental run lists the
  UIDs above the cursor (`UID SEARCH UID n:*`, whose answer can be the highest
  UID even below `n`, which is skipped), takes at most the cap, and moves the
  cursor to the last UID it took when every stage succeeded;
  `cursor_updated_at` moves only on complete coverage. A cursor of another
  UIDVALIDITY, or one that cannot be read, falls back to the bootstrap searches
  (`fallbackUsed`), clears the cursor and records the reset; such a run can
  complete. A UID above the cursor is exactly the new mail, so nothing is
  listed again as an overlap. A message expunged between its search and its
  fetch is tombstoned.
- **Demo workspace.** The demo gate is applied at the session's door: a
  session without a scope is refused before it connects; the connect scope
  allows the sign-in, the folder list and a date search whose ids are counted;
  a verification-code grant allows exactly its own search and a fetch of the
  newest UIDs that search answered ([gates.md](gates.md#the-demo-workspace)).

### Watching for new mail

`gmail.Watcher` (`internal/gmail/watch.go`) is the mailbox's automatic reader,
after the owner's mail-agent project's IMAP IDLE watcher
(owner decision GM-1, 2026-11-02). The server
starts it with its domain and stops it first on shutdown; the command line
never runs it. There is no Gmail sync pack: a `gmail.sync` schedule is one the
user made, and it answers "a sync was already running" when the watcher's run
holds the slot.

- **One idling connection.** It signs in, examines All Mail and holds IDLE for
  up to 9 minutes (Gmail ends an IDLE after about 29). Gmail's `EXISTS` ends the
  hold at once. The session's command timeout is the hold plus a minute, since
  the IDLE command's deadline bounds the wait.
- **When it syncs.** After every IDLE it examines All Mail again and runs
  `Syncer.Run` (recorded as `scheduled`) when UIDNEXT or UIDVALIDITY differs
  from what the last complete sync started from, and once on every new
  connection. UIDNEXT decides, not the cursor: Gmail's draft autosave adds UIDs
  and expunges them again, and no sync can move the cursor past those. A change
  waits 3 seconds for the rest of a burst, and syncs start at least 20 seconds
  apart. Each sync signs in on its own connection: go-imap ends a session whose
  read deadline passes, and a sync pauses between commands longer than a
  command's deadline allows.
- **Failures.** A connection that breaks is opened again after 5 seconds,
  doubling to 5 minutes; a failed sync is retried on the same schedule. A
  refused app password is recorded by the sign-in itself (`login_status`), and
  the watcher then looks every 30 seconds for one Gmail accepts. It never reads
  a demo workspace.
- **Starting over.** A connect or disconnect from the dashboard wakes it; it
  also notices a changed `connected_at` (a connect from the command line) after
  each IDLE. Timers and read deadlines run on the monotonic clock, which stands
  still while the computer sleeps, so every 15 seconds an IDLE compares it with
  the wall clock; after a sleep it closes the connection and signs in again.

Reply drafts are made with digests or on request (see Summaries and
drafts).

The Tracker list carries each row's mail state: `GET /api/candidacies` sets
`mail` (thread id, lane, owner, next action, age in days, open count) on a row
with an open linked thread. One read of the mailbox store serves the whole page
(`Repo.MailByCandidacy`), through the same links (`threadLinks`) and lifecycle
lane as the inbox and its badge; a remote client's list carries no `mail`, since
every `/api/gmail` route is denied to it; the most pressing open thread is the first lane
in digest order, then attention, then the newest. Closed (done, dismissed,
expired) and receipt-only threads are not open. A 100-row page with about 50
linked threads lists in about 12 ms, of which the mail read is about 6 ms.

## Signal events

`gmail_signal_events` gets one row per message the first time it has an
incoming classified signal, written in the transaction that stores the
decision. Reclassification and a changed type or tier never write another. The
row holds identifiers and classification fields only and is deleted after 30
days. The signal triggers of the Ask Agent read it with
`Repo.SignalEvents(afterSeq, limit)` and keep their own high-water mark, and read
the message text with `Repo.StoredMail` (sender, subject and the stored body, or the
snippet once the retention sweep has removed it).

## Ask Agent tools

`gmail_action_inbox` reads the inbox read model, each row with its thread id,
and `gmail_digest` the latest stored brief (or, with `generate: true`,
`Digests.Generate`: that call asks a model and stores a digest, so the guard
decides it as a draft, tier 1, and it is never answered from the turn's
repeated reads). Owner decision GM-4 added, over the same store
(`internal/agent/tools_gmail.go`):

- `gmail_deadlines`: the open deadline todos (`Repo.Todos`, the reminders'
  own), soonest first, with when each is due and a snoozed reminder.
- `gmail_thread`: one thread by id, a header worded for the user's zone, then
  `Repo.ThreadFacts` (every message with its type, due time, subject, snippet
  and the stored body excerpt), cut at 8,000 characters.
- `gmail_search`: the threads whose employer, role, subject, sender or snippet
  hold every word of the query, optionally from a day and in one lane, newest
  first. Job mail Applysmith has read only; Gmail is not searched.
- `gmail_thread_action` (write-local, tier 2, no approval: owner decision GM-5):
  `Repo.Act`, and the result names the call that undoes it. The tier-2 rule
  decides it: run when the user's own message asks for a change (the write
  verbs include dismiss, snooze, undo, reopen, restore, remind and postpone, and
  their Chinese forms), held for a card otherwise; scheduled and digest turns
  cannot use it.
- `draft_gmail_reply` (draft): `Stack.DraftReply`, refused as the other draft
  tools refuse ("Not drafted: ..."), never written by the model in its place.

The reads and the draft return email text, so they are outside content: they
taint the turn like `gmail_signals`. Due times are worded by `DueWords` in the
machine's zone (`ToolDeps.Local` in tests), so a date is that date.
