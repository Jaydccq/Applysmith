# Notifications and the Telegram gateway

Packages: `internal/notify` (layer 40) holds the runs board, the announcement
policy, the desktop sink and the settings. `internal/notify/telegram` (layer 41)
holds the Bot API client, pairing, the poller lease, update receipts, commands,
buttons and the outbound queue. `internal/api/notify.go` and
`internal/api/telegram.go` wire them to the server. Tables are in
`internal/store/migrations/0020_notify.sql`.

## Runs board

One row per unit of work (`board_entries`), written only through `notify.Board`.
States and the permitted transitions are closed (`model.go`); a rejected change
is logged and leaves the row alone. `epoch` grows on each state or reason
change and is part of an announcement's identity.

Producers:

- Evaluations, scans, document runs and scheduled jobs have run records in
  `runs`. `Board.SyncRuns` mirrors them (entry id = run id). It runs on every
  `Server.notify("runs")`, so a read after a notice sees the entry, and on each
  scheduler tick (timer `runs-board`), which also touches the working entries
  and finalizes silent ones after five minutes (`Board.SweepStale`; after
  two sweeps more than two minutes apart, a host that slept or starved, it
  finalizes nothing for five minutes, so a live producer touches first). A
  working entry is read
  again on every sync until it moves: a sync out of time can write a
  record's stamp and not yet its move. A sync reads the records added since
  the last one that reached every record (at the first sync of a process, all
  inside the 14-day window) and the records of the board's own entries; it
  does not walk `runs`, which is never pruned (the board keeps the rowid of
  the newest record it placed, and a test reads the query's plan). The entry's origin is
  `schedule` for a scheduled run, `agent` for a run with an
  `agent_run_origins` row (an evaluation, scan or document run the Ask Agent
  started) and `user` otherwise; the agent notes the origin just after the
  run exists, so an entry a sync opened in between as the user's is corrected
  on the next sync. A scheduled job's entry is titled by its schedule's name
  when that schedule is of the run's own kind (`Board.scheduleName`), else,
  as for the follow-up after a scan, by the kind's human name (`TaskTitle`,
  which every registered kind but a scan's has). A scan, scheduled
  (`discover.<source>`) or started by hand (`source` in its request), is
  "Scan <source title>", the readable name the server passes in
  (`BoardOptions.ScanTitle`, as notify cannot import discovery), so its
  announcement and the source alarm ("Scan Indeed stopped producing") name the
  source as Discovery does.
- Tasks that have no run record use `Board.Raise` / `Board.Resolve`: a scheduled
  job that needs the user (`scheduler.NeedsYou`) and a Gmail app password
  Gmail refuses (`gmail.Hooks`). A scheduled job's item is keyed by schedule
  and reason (`needs-schedule-<id>-<reason>`, F-2) and resolved by
  `Options.Recovered` when a later run of that schedule succeeds. A job site
  the app browser is signed out of, found as a list starts, is keyed by site
  (`site-sign-in-<domain>`) and resolved by a sign-in check or a list that
  finds the site signed in ([apply.md](apply.md)).
- The apply chain and the agent write through the same interface when they are
  wired to the server; nothing else keeps a display status.

A run's failure code becomes the entry's detail in words that say what to do
(`failureCodes` in `board_runs.go`: a refused key, an unreachable or
rate-limiting provider, a timeout, an unreadable posting, a report that could
not be saved); a failure recorded as a sentence is shown as it is, and a
scheduled job, whose run records only the code `error`, shows its history
detail (for example "the github scan failed: … connection refused"); one that
succeeded shows the summary it returned (the inbox drain's counts). An
evaluation is titled "Evaluate <role> at <company>" from its row's names, else
from what the workspace knows of the posting (`Board.PostingNames`: tracker row,
inbox row, newest report), read again when it ends; an application entry takes
the same names when it opens and, from the report the run bound, at its end
(`applyStack.named`, `finish`). A run started from a bare address also takes,
last, the role and employer its first page's JobPosting data states
(`apply.PageStated`, held on `Run.Stated` and never in the run record, which a
retry reads back as the request); a stored role that is a page's document title
is shown without the job site and the company (`tracker.RoleName`, applied by
`PostingNames`, `Named` and the evaluation's page read); and a title over 120
characters shortens its role, never its company (`fitRole`). Where a
posting has no known name the title falls back to its address (`PostingSlug`:
an ATS board name, a Workday tenant, "Workday" for Workday's own careers site,
else the host; an evaluation of a posting on a job board reads "on Glassdoor",
never a raw host), and the workspace's name ranks over the request's in an
evaluation's title. Every read that leaves the
process shows an apply or evaluation entry by the names its posting is known by
at the time of the read: the listings (`List`, `Inbox`, `Members`) and the
single entries an answer carries (`Board.Shown`: the run page and the answers
to seen, acknowledge, done and cancel), so an entry opened before its tracker
row existed reads like the rest. It reads once per kind and source, not per
entry (`knownTitles`, `postingNames`: about 6 ms of CPU for 500 entries). The
stored title stays what the run and `RenameWaits` wrote; board notices carry
only a topic, so the web refetches them.
The
evaluator's result also names the provider and the one-line fix
(`llm.ClassifyTest`), which the CLI prints and the inbox note carries.

A run's end carries what it came to: `BoardOptions.Outcome` (set by
`internal/api/narrator.go`, which reads `evaluation_jobs`, `scan_runs` and
`document_jobs`) is written as the entry's detail before the move that
announces it (the versions a documents run wrote, "Résumé version 2 and cover
letter version 2 are ready to review.", with the Documents page's "Written
without a model" when none wrote them, or "Prep pack revision 2 is ready."; a scan's
stop reason in words, a skipped scan's own reason as it is, the task a
finished scan found with no model when it sent none to evaluation
(`Record.Unrouted`, "0 queued for evaluation (the jd-extract task has no
model it can use; choose one under Settings, Models)"), never the
record's error text, which can quote the page), and the apply
chain's finish writes the submitted sentence (`submittedSentence`). Without
one, a scheduled job that succeeded says the summary it returned, and a
dashboard pass (a company brief, story proposals, a knowledge ingest, answer
coaching) the line it ended with, which `runPass` stores in `runs.outcome`
before the end, so a notice never reads only "It finished." when the pass
said more. A scheduled job that succeeded and did nothing (`BoardOptions.Idle`,
`jobs.Idle`: a Gmail sync whose summary is "0 listed, 0 stored, 0 for review,
0 linked" or "a sync was already running") is acknowledged as it ends, so it
is neither a finished run to see nor announced: in the owner's workspace the
ten-minute sync made 850 of a week's succeeded runs, 806 of them idle, each an
unacknowledged finished entry. A failed sync stays. An
evaluation whose input names a `ScanRun` joins that scan's batch
(`ScanBatch` + the scan's run id) with its origin; an evaluation that ends
well resolves the review entries of the same identity key, before its own
entry moves, so a sync out of time in between resolves them at the next. An
evaluation whose
input names a `Drain` (the `inbox.evaluate` run that sent it) joins that run's
batch instead, with its origin, and the policy announces that scheduled run
once it and its evaluations ended (`Policy.scanEnded`, F-2); one that names a
`Review` closes the review into itself when its entry opens
(`Board.closeReview`, F-8).

`GET /api/runs/{id}` returns the run view with the board entry in `entry`; an
entry without a run record is returned as a synthesized run view.

At server start every `working` entry becomes `done/interrupted`; waiting
entries stay. The daily retention sweep deletes finished entries after 14 days
and finalizes waiting ones after 30.

## Policy

`Policy` is the board's observer. It queues announcements in `notify_outbox`
and a pass (`Process`, on the worker, on every scheduler tick as the
`quiet-hours-release` timer, or after a kick) decides:

- Events are a closed list. Urgent means an entry entering `needs-you` or
  `blocked`, or a Gmail alert about an interview starting within 24 hours.
- Each kind has a reach (`eventKinds`), which decides whether the phone (the
  Telegram sink, `PhoneSink`) gets it; the desktop gets every announcement.
  Everywhere: employer mail and deadlines (`gmail-alert`, `gmail-reminder`),
  `interview-reminder`, the agent asking or reporting (`task-update`,
  `goal-checkin`), what the user scheduled (`prompt-result`,
  `trigger-result`), everything stopping (`cost-cap`, `provider-refused`),
  the briefs (`morning-brief`, `day-summary`, `gmail-digest`,
  `signal-digest`), `signal-hot` and the burst summary. Asker: `needs-you`,
  `blocked` and `done` reach the phone only for work asked for from it
  (`PolicyOptions.Asked`, `askedFromPhone`: a run under the `telegram`
  principal, or the agent's run started in a turn on the `telegram` channel,
  whatever the conversation's channel: the latest turn of its conversation
  that started by the time `agent_run_origins` recorded it, so a reply from
  the phone into a task's conversation counts and a queued start counts with
  its turn). Desk: `batch`,
  `model-gone`. A quiet-hours summary for the phone lists only what reaches
  it. Why (the owner's workspace, read-only copy of 2026-11-04): paired on
  2026-11-01, the chat got 1,702 messages in five days, 280 to 498 a day at
  every hour, and the owner never pressed a button or wrote a message. 1,672
  were stops and ends of work started at the desk (applications stopped at an
  answer or at Submit, evaluations and documents just asked for in the
  dashboard), none started from Telegram; `needs-you` is urgent, so it passed
  the burst limit and quiet hours. An application stop is handled only in the
  desktop's kept tab. Replayed under this rule the same days give 16
  messages: the briefs and three hot hiring signals.
- A `done` entry is announced by the origin and outcome rule, 20 seconds after it
  finished, and cancelled by an acknowledgement or leaving `done`. Entries of
  one batch produce one rollup instead.
- Quiet hours come from `notifications.quiet_hours` in `profile.yml`, read at
  each decision; any defect reads as off, and so does a start equal to the
  end (Settings refuses to save one, and says when a window crosses midnight). Normal announcements are stored as
  `held` and listed under Notices at once (see Desktop sink). At release (`foldHeld`), when
  two or more held items wait for a sink, they reach it as one summary ("N
  updates while quiet hours were on", their titles in id order, the shared link
  or `/runs`) and each is recorded `summarized` for that sink; a single item is
  delivered as itself. A Gmail digest is never folded: its pages are the
  content. Held or failed items older than 24 hours are dropped with a warning.
- A scan's members are never announced alone: the scan's `done` announcement
  waits until none of them works and its body is `PolicyOptions.ScanRollup`
  (what they came to, by lane). A `needs-you` or `blocked` stop folds into the
  earlier unacknowledged one of the same work, state and reason (`fold`; an
  application or an evaluation by its posting's identity key, other kinds by title):
  the earlier entry is acknowledged, its outbox row and Notice move to the new
  one, and a stop already told is not told again. At every start, for what a
  process stopped between a commit and its fold or retirement left, `FoldRepeats` first
  acknowledges an application's stop whose posting has a later run, as that
  run's start does (`LaterRunRetires`), then folds the waits already on the
  board the same way, newest kept, silently, and `Board.RenameWaits` gives
  those left the names now known of their postings.
- Each sink delivers at most three normal announcements in 60 seconds; the rest
  are marked `coalesced` and become one "N more updates" summary.
- `notify_outbox.key` is unique, so an announcement is made once across
  restarts. A newer announcement of an entry supersedes the earlier one
  (desktop: same identifier; Telegram: new message, old one edited). Leaving the
  state, acknowledging or deleting withdraws what is still shown.
- A failing sink never stops another; `ErrGone` (an unpaired chat) drops the
  item for that sink.

`POST /api/notifications/test` (`Policy.Test`) sends a test notification to
each sink at once, past quiet hours and the burst limit, and answers per
channel `sent`, `off`, `unavailable` or `failed` with what to do (no token, no
paired chat, a refused token, a network error). Settings, Notifications shows
the answer beside its button.

Producers outside the board call `Policy.Announce`, `AnnounceGmailAlert` and
`AnnounceCostCap`. The cost-cap alert (`llm.WithAlertSink`) and
`gmail.Stack.RaiseAlert` (called by the Gmail alert evaluation) both go through
it. The Gmail digest announces its headline to the desktop through
`Policy.Announce` with the kind `gmail-digest`. The deadline reminders
(`gmail.Repo.Reminders`) are announced by the `gmail-reminders` timer with the
kind `gmail-reminder`.

## Day view and day summary

`GET /api/day` (`dayView`, local only) builds the day from the board, scans,
evaluation jobs, tracker events, follow-ups, lanes and the Gmail inbox; Home's
Today, `/today`, `activity_today` and the summary use it. `activity_today`'s
text gives each line its dashboard link (`dayText(v, true)`), so the agent has
a waiting run's id for `run_questions`; the brief, the summary and `/today`
stay without links. Its counts are
postings added to the inbox today from any source ("new"), postings that got a
report or a screening, each once ("evaluated"); a scan line's "found" counts
what that scan saw, repeats included, and the evaluation line ("Ran N
evaluations") counts every evaluation that ended, skips and failures too, so
the lines run higher than the counts. That line ends with the day's best
reports by name, as a scan's roll-up names its own: the best band first, the
higher score first, each report once, at most three ("Best: Software Engineer
I at Twitch 4.6, ..."). `daySummaryTimer`
announces `day-summary` once per local day at 18:00 (normal class), claimed in
`workspace_meta` (`notify.day-summary`), nothing on an empty day. While the
evening digest is due it waits 15 minutes; the digest that comes first claims
the day and carries the summary (`carryDay`, its title plus "and your day"),
and Telegram sends the summary's pages before the digest's. A held summary
folded with others at release reaches Telegram as the fold's summary line.
Today's applications and documents that came to the same words are one
"Done for you" line with their count ("Apply to X (3 runs) stopped before it
finished."), opening the newest run; a stopped application whose posting a
later run got past (it filled, submitted or is still running, by the record's
identity key) is left out, as the inbox drops that stop (2026-10-31). Waiting runs are told by stop as `fold`
keys them (`stopKeys`) and counted one by one; `Counts.Waiting` (interviews, waiting runs, due follow-ups,
`do_now` mail) is the title's number, ready postings apart. `Left` leads with
the interviews Home's Needs you lists, from the same rule (`interviewsSoon`,
behind `GET /api/interviews/soon`): a stage ahead today or tomorrow at its own
time, and one over today until its debrief is written, each linking
`/tracker?interview=<id>`. Home's Today shows only `Did` and `Employers`, so an
interview is shown once. Before the waiting runs comes `AskedLine`: the question
`AskedQuestions` (the Answer Bank's "Questions your runs wait on") counts on
the most waiting runs, once two or more do, linking `/answer-bank?show=needs`;
one answer there answers it on all of them (2026-10-30). It routes every waiting
run's questions, so only the day told in words carries it (`dayView(..., told)`:
the brief, the summary, `/today`, `activity_today`); `GET /api/day`, which Home
re-reads on every run event and whose left lines Home does not show, skips it. `Employers` holds the reply line (`applied` rows by
`applied_at`); `/today` is sent with `splitPages`.

`morningBriefTimer` (timer `morning-brief`) announces `morning-brief` once per
local day at 08:00 (normal class, so quiet hours hold it), claimed in
`workspace_meta` (`notify.morning-brief`): the day view's `Left` lines as its
body (the interviews, the question most waiting runs ask, the waiting runs by
name, Apply now, due follow-ups, `do_now` mail), titled "This morning: N things wait for you, M ready to apply".
Nothing goes out on a morning when nothing waits, and a brief missed by more
than four hours (past noon) is not sent. It is a plain announcement, not
carried by the morning digest. Added 2026-10-28 for the owner's request that
the system remind them of what matters now without being asked.

## Interview reminders

`interviewReminderTimer` (timer `interview-reminders`, every tick) reads
`interviewsSoon` and announces `interview-reminder` (normal class, so quiet
hours hold it) for a stage from 19:00 of the local calendar day before it
until that day starts ("eve"), and again in its last two hours while it has no
prep pack ("soon"); never once it began (`reminderDue`). The title names the
stage's date and time, the company and the stage type ("Interview Fri Oct 23
at 14:00: Fabrikam, Technical"), not "tomorrow", because a held or kept notice
is read later; the body says whether the prep pack is ready, and the link is
`/tracker?interview=<id>`. The key is candidacy, stage, reminder and the
stage's time, so each is made once and a stage moved to another time is
reminded of again. A cancelled stage and an archived, rejected or discarded
application are outside `interviewsSoon`, so they get none.

## Desktop sink

`GET /api/notifications/events` carries `announce` and `withdraw`.
The desktop app subscribes with `internal/notify/stream` (no framework import;
`app/notifications.go` supplies the native calls): it reads the stream over
HTTP with the API token from the keychain, whether the server is attached or
in-process, shows each announcement as a native notification with the entry id
as identifier (so a newer one replaces it), closes it on `withdraw`, skips an
event id it already handled, resumes with `Last-Event-ID` and reconnects after
1, 2, 4, ... 30 seconds. Clicking a notification opens the entry's link; a
finished run (non-urgent) is also acknowledged through
`POST /api/notifications/{id}/ack`, while `needs-you` and `blocked` stay in the
inbox until the user acts. Only an announcement of a board entry (`entry` in
the event) is acknowledged; a Gmail alert, a cost cap, a summary or a test has
an `n-<id>` identifier of its own and nothing to acknowledge. A notification is skipped while the focused main
window already shows its page; a denied permission shows in `getShellInfo`
(`notificationsPermitted`), and Settings, Notifications then says so. Clicking an
apply entry whose application tab was kept also brings that tab forward
(`show-tab`).

The sink is available only while a client is subscribed to the stream
(`eventStream.listening`; in practice the desktop app). The desktop channel
also keeps the Notices list (`notify_notices`, migration 0048), shown by the
dashboard's top bar: a bell with the unread count, opening a drawer, newest
first. The policy (`Noticer`) puts there, at once and silently, a normal
announcement quiet hours hold, and, instead of a delivery, an announcement the
sink is unavailable for, so nothing is recorded as delivered with no app to
show it; the pairing confirmations ("Telegram paired", "New device paired")
go there directly (`Desktop.Text`). A notice carries its announcement's handle,
so a newer one of an entry replaces it (the same text keeps its read state)
and the entry's withdrawal removes it. A notice whose announcement the desktop
shows after all (a held item released after quiet hours, alone or in their
summary) is marked read (`Noticer.Seen`), so the morning's notification does
not leave it unread as well. The list keeps 100 notices and the daily
sweep deletes those older than 14 days. `GET /api/notices` reads it and
`POST /api/notices/read` marks the named ones (or all) read; a change notifies
the `notifications` topic. Desktop notifications switched off keep nothing
there either. Test notification answers `unavailable` without the app and says
where announcements go.

Decision (owner, 2026-10-20): notices that reached no one (the stream's
`notice` event had no consumer, and with only a browser the desktop channel
recorded deliveries nothing showed) are shown in the dashboard. A passive list
was chosen over toasts: quiet hours must still hold, so a held item may make
no sound or banner, and an in-page toast would vanish with the page while the
list survives reloads and is shared by every window. The `notice` event was
removed.

## Telegram

The token lives in the keychain (`telegram-bot-token`, override
`APPLYSMITH_SECRET_TELEGRAM_BOT_TOKEN`). `Manager` validates it with `getMe`, stores
it and records the bot name; a token stored without one (imported, or saved by
an older build) gets it from `getMe` when polling starts. Pairing hashes a 10-hex code; five wrong codes close
the window; claim binds chat and user and bumps the pairing generation, which
invalidates every button token.

Every Bot API call ends at a deadline of the client's own (`CallTimeout`, 10
seconds; the long poll `LongPollTimeout`, its 25 seconds and 10 more), so a
host that stops answering costs one call and not the outbound queue.
One poller per token: a lease row (`telegram_lease`) with a fencing generation,
renewed each loop and valid for `LeaseTTL` (125 seconds: a long poll that ran
to its deadline, the longest back-off of 75 seconds and a margin, so the lease
of a poller that is alive and waiting never lapses). Updates pass a receipt (`received`, `processing`, `processed`,
`failed-retryable`, `terminal`); the offset moves only after the receipt commits.
Only the paired private chat is served; everyone else is counted and ignored.
Free text goes to `Backend.Ask`, which stores it and starts its turn on the
`telegram` channel and returns at once; the guard chain refuses both apply
tools for that channel, and the channel's system prompt asks for short chat
answers without headings or tables. The update is done once the turn started
and is recorded (`telegram_turns`, keyed by update id, so a retried update
finds its turn and asks nothing again): the poller goes on answering commands
and buttons while the agent works (`ask`, `follow` and `deliver` in
`commands.go`). A follower per turn shows
"typing", sends one interim message after a minute, reads the turn with
`Backend.Answer` after 250 ms and then at pauses that double to 2 seconds, and sends its answer page by page, the first as
a reply to the owner's message, recording each page Telegram accepted. A page
that fails is tried again, backing off to a minute, for as long as the app
runs (an outage delays the answer and loses nothing); only a refusal, a 400
for the plain text or a 403 (the owner blocked the bot), gives the answer up
with a warning. A follower reads the turn's record before sending, so a
second one sends nothing again, and sends nothing to a chat that is no longer
the paired one; unpairing deletes the owed turns and the anchors. At each
start of polling the followers of turns not yet delivered resume, so an
answer a stopped process owed is sent once. A message while its conversation's turn
runs is told to wait or send `/stop`, which cancels the chat's running turn
(`Backend.Stop`; its runs go on) and is answered with what the turn had,
"Stopped before it had an answer" when nothing.

Replies reach whoever asked. Each message the bot sends that belongs to a
conversation or a Gmail thread is recorded (`telegram_anchors`, kept 30
days): every page of an answer (its conversation), a held call's card (the
conversation that held it), and an announcement (`Config.Conversation`,
`telegramConversation`: a delegated task's conversation from its link, or the
conversation whose agent started the announced run; a Gmail alert's or
reminder's thread). A message that is not a reply goes to the chat's own
conversation (rolling, new after 6 idle hours). A reply goes to the
conversation of the message it answers, so a task's question answered from the
phone lands in the task's conversation, answers it and binds the turn to the
task; the turn runs on the `telegram` channel, so the apply tools stay
refused. The chat's own conversation stays what it was. A reply also carries
the message it answers as quoted lines (whole lines, up to 600 characters,
under "Replying to your message:") and, for mail, "(Gmail thread <id>)", which
the agent reads with `gmail_thread`. Quoted lines are pasted text: the guard's
own words (`ownWords`) and the intent router (`prepare`) both leave them out,
so text from a mail or a posting in a quote never counts as the owner's
request.
The agent writes Markdown; each page of its answer is sent in Telegram's HTML
(`formatted` in `pages.go`, a goldmark walk: headings and table headers bold,
list items bulleted or numbered, table rows as lines of cells, code dropped
from inside bold or a link because Telegram refuses that nesting), and a 400
from Telegram sends the same page as plain text instead. A message without text
(a photo, voice note or sticker) is answered that only text is read. On each
start of polling the gateway sets the command menu (`setMyCommands`) from the
same table as `/help`; a failure is logged and the commands still work. Pairing
and `/start` send the welcome, with example questions, and the button menu.

A message that is only a posting's link gets two buttons, Evaluate and Plan
(`intent:<eval|plan>:<token>`; `telegram_tokens`, single use, 20 minutes, bound
to the pairing). A press does the work first and uses the token after it:
`Backend.StartEvaluation`, or the plan's turn (`turn` in `commands.go`, one per
update id). The used token keeps what that came to (the run's link), and the
message is edited last. An evaluation that could not start, an agent that is
busy or a turn that could not be asked for leaves the token unused, so the
retry of the update or another tap does the work. A press of a used token (the
retry of an update whose edit failed, or a second tap) starts nothing, answers
"Already handled." and makes the same edit again; Telegram's 400 for an edit
it already made counts as made.

An announcement of an entry carries one button, `ack:<entry id>` ("Got it").
The Done button that resumed an agent's application from the phone was
removed: under the reach rule no application stop reaches the phone, since an
application cannot be started from Telegram, and a `done:` button left in the
chat by an earlier build is answered "That button is not valid."
An announcement's message is its title and body and, while remote access gives
the dashboard a public address (`Config.LinkBase`), the link to its page.
Health is `stopped`, `configured`, `healthy`, `stale` (no answer for a poll
cycle), `conflicted` (another poller) or `refused` (Telegram refuses the saved
token, from the poller's 401 until a poll succeeds); Settings, Telegram says
what each one means and what to do. The start of each conflict is logged once,
naming its kind: Telegram's 409 (another program polls the same bot, from
anywhere) or this workspace's poller lease (another process of this app), so
the other poller can be traced after the health word has cleared.

`/status` reads the server components, the apply and scan windows of the managed
browser, the board counts, the next three schedule occurrences, Gmail health and
the day's spend; `/open <screen>` gives the dashboard address while the remote
tunnel runs and names the screen otherwise.
`/inbox` lists the do-now lane of the Gmail action inbox (at most ten); a `rate:` tap
records or clears the rating of a digest item (`docs/architecture/gmail.md`).
The routed-model watch announces `model-gone` (a routed OpenRouter model left the
catalog) through the same policy: normal class, so quiet hours hold it, and the key
names the task, the model and the episode, so a repeated check is ignored.
The provider watch announces `provider-refused` (a model provider refused at least
three calls in a row since its last success, as out of credit or with a dead key;
OpenRouter's 403 "Key limit exceeded" is out of credit) the same way: normal class,
and the key names the provider and the episode's first refusal, so it is announced
once until a call to the provider succeeds.
Hiring signals announce through the same policy with two kinds, `signal-hot` (a company entering the top tier) and `signal-digest` (once a day); both are normal class, so quiet hours hold them. See [signals.md](signals.md).

A Gmail digest reaches the chat only through the policy: `Policy.AnnounceTo` queues
it for the Telegram sink, which pages it with `Gateway.SendPages` (keyboard on
the last page), so quiet hours hold it and release it later. Each page Telegram
accepted is recorded (`telegram_meta`, key `pages:<outbox id>`, until the last
page is out; unpairing and the 14 day retention drop it), so an attempt that
ran out of its 15 seconds or failed on a later page goes on from that page at
the next pass and sends none twice.
A Gmail alert (`gmail-alert`) and a deadline reminder (`gmail-reminder`, timer
`gmail-reminders`; both normal class) carry one row of `mail:<action>:<thread>`
buttons: the sink asks `Config.Mail` which actions the linked thread offers, and
`Backend.Mail` runs them (done, reopen, dismiss, restore, draft, snooze; see
`docs/architecture/gmail.md`, "Acting from Telegram").

CLI: `applysmith telegram set-token --stdin | clear-token | pair | unpair | status`.
Doctor: `telegram.token` and `telegram.pairing` warn, never fail.

## Done resume

`notify.CanResume(entry)` is the one rule: an `apply` entry with origin `agent`
(the Ask Agent started it) in `needs-you` with reason `sign-in`, `answer`,
`challenge`, `review`, `one-time-code` or `other`. It drives the `canDone` field of
a board entry and the Done buttons of the Runs page.
`confirmation` and `may-have-submitted` never offer it.

`POST /api/runs/{id}/done` calls `applyStack.resumeRun`
(`internal/api/apply_resume.go`):

1. `Board.Resume` moves the entry to `working` in one write that re-checks
   `CanResume`, so of two presses the second gets `conflict`. Leaving `needs-you`
   takes the item out of the inbox and withdraws its announcement.
2. The press (who, when) is stored with the run's request
   (`agent_apply_requests.pressed_by`, `pressed_at`).
3. The apply window is reserved like any run (`lane-busy` when another run
   drives it; the entry is untouched then). No kept tab: the entry becomes
   `blocked` / `lane-unavailable`.
4. `apply.Runner.Resume` continues the run in the background under the same run
   id. A press by the Ask Agent (its last saved answer) sets `HoldSubmit`.

A typed "done" is an ordinary message and never reaches this path.

Completion messages: `completionNotes` is a second board observer
(`notify.Observers{policy, completion}`). When an entry that has an
`agent_run_origins` row reaches `done`, `needs-you` or `blocked` it appends a
system-made note to the conversation, in order, from one worker; a note that
carries the entry's detail (page text) is stored as outside content. A finished
evaluation's note adds its outcome, score, recommendation and report number
(`evaluate.JobOutcome`), a finished scan's its counts (`Repo.ScanByRun`). A Telegram
conversation is told through the policy's announcement of the same entry, so quiet
hours, de-duplication and withdrawal apply; there is no second direct send.

