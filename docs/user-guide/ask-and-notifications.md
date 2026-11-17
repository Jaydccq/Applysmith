# Ask Agent and notifications

## Ask Agent

The Ask Agent page is a chat with an assistant that knows your tracker, reports,
answer bank, knowledge base and schedules. It can look things up, draft answers
and documents, run an evaluation or a scan, fill in an application ("Fill in
the application at <url>"), and propose changes for you to accept. You need a connected model provider (setup step 9) and the agent
runtime `pi` (the doctor line `agent.pi` tells you if it is missing; set
`APPLYSMITH_PI_PATH` if it is not on your `PATH`).

Things to know:

- **It submits only when you say so.** The two tools that apply (a single
  posting and a list) are the only ones that can submit, and only when your own
  message in the same turn asks for it. If the turn also read outside content (a
  mail, a posting), or the posting's site is not in your message or the day's
  list, the submit waits on a confirmation card: approve it in the dashboard,
  or ask again in a new message. A confirmation comes from you, never from the
  assistant's own text. A fill that stops before Submit needs no card: when
  you asked it to fill, it fills the postings it picked from reports or mail
  it read.
- **It keeps your tracker tidy.** Tell it to skip a posting, take a skipped
  one back or discard one you do not want ("discard Lio, it is in Munich") and it
  changes that row, with your reason in its notes. Marking a row applied,
  interview, offer or rejected stays with you and the Gmail sync.
- **Messages are drafts.** It can draft a LinkedIn note, a follow-up or a
  decode of a posting; it never sends a message, and each draft lists what to
  check before you send it.
- **"Let the agent finish."** On an application run that waits in its kept
  tab for your answers, this button (Home, the run's drawer, or under the
  line about the run in the conversation that started it) has the
  assistant answer each question from your answer bank or profile and ask you
  for the rest, saving only those values or your own words, and the run then
  stops at Submit for your confirmation.
- **It keeps your answer bank.** Tell it a question and your answer ("my
  notice period is two weeks", "on jobs.acme.com say I heard of them from a
  friend") and it saves them where forms find them; ask it to show, change or
  remove a saved answer, or to have a question left to you every time. You
  approve each change on a card first. It decides how the answer is stored
  (which entry, which wordings), and it saves nothing the autofill would not
  then use. The Answer Bank page's Add with Ask Agent opens this
  conversation; on the page you change an answer's text directly. Answers that
  come from your Profile or a Setup step (contact details, work authorization)
  are changed there.
- **Memory is reviewed.** What it learns about you goes to a review queue on
  the Memory tab before it is used.
- **It can work in your other apps.** With PersonalCU, your own computer-use
  service, running, a dashboard chat can look at and act in the apply browser
  and in an app your message names. It never presses Submit, Send, Pay, Delete
  or Return, and never acts in Applysmith, a terminal or System Settings. The
  Agent control page shows each step with the window's screenshot, and its
  Pause refuses every desktop action at once.
- **It can carry a task.** Hand it an outcome ("apply to the strong matches on
  my list and finish the ones that stop") and it keeps a task with a plan above
  the conversation: it takes its own turns while its runs go on, asks you when
  only you can answer, and never submits. Pause, resume or stop it there. One
  task per conversation, at most three open. On Home, "Let the agent do it"
  hands over up to five items in one press: evaluating the newest inbox
  postings, filling postings from the apply list without submitting, or
  drafting the follow-ups that are due.
- **Goals, schedules and triggers.** Tell it a goal with steps and a date and
  the Goals tab keeps it, with check-ins; a goal never changes what the
  assistant may do. Ask for a prompt on a cadence ("every weekday at 8:30,
  list the new strong matches") or on job mail (an interview, an assessment, a
  rejection); a scheduled turn only reads and drafts. One the assistant
  proposes runs only after you confirm it on the Triggers tab, which Telegram
  cannot do.
- **It adds to your wiki** when you ask; you approve each write on a card.
- **It improves its own guidance.** Each night it reviews its recent
  conversations and may adopt better guidance for itself, never a rule, a gate
  or a tool. Settings, Ask Agent, Harness lists each version with its evidence
  and restores an earlier one in one action.
- **It answers in the language you write in.**
- **Conversations are yours.** List, export or delete them:

```
applysmith agent sessions list
applysmith agent sessions show <id>
applysmith agent sessions export <id> --out <file>
applysmith agent sessions delete <id>
```

To run it on a subscription instead of an API key, sign in once with
`applysmith agent login` (type `/login` in pi), then route the agent to it:
`applysmith llm set agent-kernel pi:openai-codex/<model>`.

The agent works in your other windows through PersonalCU, your own
computer-use service. Home's Server panel shows it as the Computer use line:
ready, not running, missing a permission, locked or paused, with what to do.

## Telegram

If you paired a bot (setup step 12), Telegram carries notifications and a chat
with the same assistant and most of the dashboard's tools: it reads and
drafts, evaluates a posting, runs a scan and works with your job mail: it says
what is due and when, reads what a company wrote, finds mail by company,
subject or date, drafts a reply for you to copy into Gmail, and marks a thread
done, sets it aside as not job mail or moves its reminder to tomorrow when you
ask (each can be undone by asking again). It never sends, deletes or changes
anything in Gmail. It cannot start or submit an application, answer a stopped run's
questions or use your other windows: those are done in the dashboard. Only
your paired private chat is served.

The phone gets what you can act on away from the desk, or asked for there:
mail from employers and its deadlines, interview reminders, a question or a
result of a task you handed the assistant, the results of prompts you
scheduled, a spend cap or a provider that stops everything, and the morning
brief and the day summary. A run you started in the dashboard is told on the
desktop only (the briefs count what waits for you); a scan or an evaluation
you started from Telegram is told there too.

While the assistant works on a message the bot goes on answering your
commands and buttons; the answer comes as a reply to your message, also when
the app restarted in between. `/stop` stops it. To answer something the
assistant asked (a task's question, a confirmation card, one of its answers),
reply to that message in Telegram: your reply goes to the conversation that
asked, and the message you replied to goes with it as a quote. A message that
is not a reply continues the chat's own conversation, which starts afresh
after six quiet hours.

## Notifications and quiet hours

Notifications come from the runs board: a run that needs you or is blocked is
urgent; a finished run, a digest or a hiring-signal notice is normal. A run is
named by the job's role and company once your tracker, inbox or a report names
it, and one already waiting takes the names when the app starts. A finished run says what it came to (a score and its report, a scan's
counts, a scheduled job's counts, where a submission went and what confirmed
it); a scan's evaluations are told in the scan's own notice, and the same stop
of one posting is told once, also for stops already waiting when the app
starts. A schedule that stops for you again is one item, which goes away once a
later run of that schedule succeeds. At 18:00 the day summary says what was
done for you and what is left, never on an empty day; with the evening Gmail
brief it comes in the same push. Home's
Today, Telegram's `/today` and the assistant ("what did you do today?") say
the same, including what waits on employers: applications waiting for a reply,
the oldest by days. An interview stage you recorded for today or tomorrow is
named with what is left for you; at 19:00 the day before it you get a
reminder that says whether its prep pack is ready, and, without a prep pack,
another two hours before it. `/today` comes in several messages when the day
is long. Once Gmail is
connected, Applysmith reads new job mail within seconds of its arrival while it
runs, and a job email that sets a deadline (an assessment, a form to fill in, a
reply asked for) is reminded of at 19:00 the day before and at 09:00 on the
day, until you mark it done. On Telegram a Gmail alert has buttons: Done and
Not job mail (each followed by Undo), and Draft reply, which sends you a draft
to copy into Gmail; a reminder has Done and Tomorrow, which reminds you again at 09:00
the next day. Nothing is ever sent from your mailbox. Normal
notices are held during the quiet hours you set (`notifications.quiet_hours` in
`profile.yml`) and released afterwards as one summary that lists them; urgent
ones come through, and so does a Gmail alert about an interview starting within
24 hours. Each channel delivers at most three normal notices in a minute and
folds the rest into one summary. Desktop notifications open the page they are
about; a Telegram message carries the page's link while remote access is on.
The bell in the dashboard's top bar opens Notices: what quiet hours are holding
(listed at once, silently), what was announced while the app was closed, and
pairings, with an unread count and Mark read. Settings, Notifications has a "Send a test notification" button that says, for
each channel, whether it got through and what to do when it did not.

## From your phone

Remote access is off by default. If you turn it on, it is an HTTPS tunnel you
pair a phone to with a QR code; a paired phone can read and draft but cannot
start a submitting run, edit policy or control
the browser. Turn it on and off, and manage devices:

```
applysmith remote status
applysmith remote on tunnel
applysmith remote pair
applysmith remote devices
applysmith remote revoke <id>
applysmith remote off
```

Details and limits: [policies](../architecture/policies.md).

## Other assistants

Your own coding assistant can read your applications through a read-only MCP
server:

```
applysmith mcp --print-config
applysmith mcp --check
```

It can list applications, reports and the apply list, and nothing else.
