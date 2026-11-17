# Track outcomes

## The tracker

Every application is a row with one of eight statuses: `evaluated`, `skipped`,
`applied`, `responded`, `interview`, `offer`, `rejected`, `discarded`. A status
only moves along allowed steps, and every change is recorded as an event with
who made it (you, the agent or Gmail). A report is written once; its status
lives in the tracker.

On the Tracker page the chips at the top filter by status, and the form below
them by company, score, date applied, email and source. Open a row for its
drawer: Change status to offers only the steps allowed from its status
(corrections are listed apart), with an optional note and Undo; its notes, the
mail linked to it, its history, and drafts of an outreach note or, once you
have applied, a follow-up are in the same drawer. Add application records one you sent outside Applysmith. From
the command line:

```
applysmith tracker list --status applied
applysmith tracker list --moved-by gmail --since <YYYY-MM-DD>
applysmith tracker show <id>
applysmith tracker set-status <id> interview --note "<what happened>"
applysmith tracker note <id> <text>
applysmith tracker note <id> --undo
applysmith tracker add --company "<name>" --role "<title>" --status applied
```

`note --undo` takes back your latest note on the row and prints it; the
history keeps both. `--reached <status>` and `--moved-by <actor>` (`user`,
`gmail`, `apply_chain`, `agent`, `evaluation`) list the rows moved to that
status, or by that actor, on or after the `--since` day: the rows behind a
count on Home. `tracker add` is for something you applied to outside Auto
Job. The Tracker page has the same, with filters, sorting and a drawer per row
(history, documents, interview stages and prep packs).

Related commands:

```
applysmith tracker followups --due
applysmith tracker followup <id> --snooze 7d
applysmith tracker dedup
applysmith tracker archive --older-than 30d --dry-run
applysmith tracker export --format csv --out applications.csv
```

`followups` lists applications due for a follow-up by the cadence in
`policy.yml` (the first after 8 days, then every 7 days, at most 2 by default);
nothing is sent for you; `followup <id> --undo` takes back your latest follow-up
or snooze. `dedup` shows duplicate groups and changes nothing without `--apply`.

To write the follow-up or a LinkedIn note, draft it, then send it yourself:

```
applysmith followup-draft --candidacy <id> --context "what you remember of the last call"
applysmith outreach --candidacy <id> --contact recruiter --name "Sam" --about "what you know about them"
```

`followup-draft` picks a recruiter ping, a status check or a thank-you from the
row's status (`--kind` overrides) and follows the cadence above. `outreach`
writes a three-sentence note (300 characters at most) for a `recruiter`,
`hiring_manager`, `peer` or `interviewer`, with two other angles. Both use only
your CV, profile, tracker row, evaluation and what you type, in the style of
`voice.md` if you keep one, and list each claim as backed by your material or to
check before sending. Nothing is sent or saved; after sending a follow-up, record
it with `applysmith tracker followup <id>`. In Ask Agent, ask for "a follow-up for
application 12" or "a LinkedIn note to the hiring manager".

For an interview, prepare per stage:

```
applysmith interview-prep --candidacy <id> --stage <stage-id> --type technical
```

## Outcomes

How are applications going? A read-only summary of a cohort, with each rate as
a numerator and denominator and no verdict on groups too small to compare:

```
applysmith tracker outcomes --from 2026-01-01
```

The Tracker page has the same as an optional Outcome review panel.

## Gmail outcomes

If you connected Gmail (setup step 11), mail from employers moves statuses for
you. A reply that is clearly an interview invitation or a rejection, linked to
exactly one application with enough confidence, moves that row as the `gmail`
actor. Weak links wait on the Gmail page for you to confirm or reject. An
offer never moves a row by itself: you confirm it.

```
applysmith gmail status
applysmith gmail sync
```

The Gmail page shows the action inbox (what needs a reply, what is waiting on
them, what is stale), and a brief at 08:00 and 18:00 local time (the Sunday evening one is a weekly
review). Mail text is shown as
text and never followed: a link in an email is for you to open.

Next: [Ask and notifications](ask-and-notifications.md).
