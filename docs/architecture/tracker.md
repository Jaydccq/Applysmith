# Tracker and posting identity

Implementation notes for `internal/identity` (task 2.4a) and `internal/tracker`
(task 2.7). The normative text is `specs/application-tracker` and
`specs/job-discovery`; this file records the choices the specs leave open.

## Posting identity (`internal/identity`)

- `Posting.IdentityKey` prefers the ATS-native key, then the canonical URL,
  then `{source}:{source job id}`, then company, role and place, then a hash of
  the text. The company and role fallback appends the sorted place tokens when
  the posting names a place, so one role in two places is two postings (the
  spec's "same role, disjoint locations" scenario) while equal keys still mean
  one posting.
- ATS URL shapes read: Greenhouse (board, job-board, EU, per-company host, embed
  `for`/`token`, `gh_jid`, board API, and an employer's page whose query
  names the board beside the `gh_jid`: CoreWeave's `?board=coreweave&gh_jid=…`,
  or `for`), Lever (jobs, EU jobs, API), Ashby
  (path and `ashby_jid`), SmartRecruiters (jobs, careers, one-click, API) and
  Workable (apply host `/{slug}/j/{code}`, `{slug}.workable.com/jobs/{id}`).
  Slugs and ids are lowercased in the key. A short link without a board
  (`apply.workable.com/j/{code}`), an infrastructure host or a company page
  yields no ATS key and falls back to the canonical URL.
- A query glued onto the last path segment with `&` instead of `?`
  (`/jobs/4718788005&gh_src=...`, an employer's apply link as LinkedIn hands it
  over) is read as the query before any rule runs, so the canonical URL, the
  stored inbox row and the ATS key name the posting alone. Before, the inbox
  stored the glued form, no ATS key was read, and Greenhouse took
  `4718788005&gh_src=...` as the job token. A path word with `&` and no `=`
  (`/r&d/`) stays. Rows stored before this keep their URL.
- `SameOpening` serves discovery and tracker matching, `SameCandidacyStrict`
  serves the apply duplicate guard, and `SameForDedup` serves the tracker's
  `dedup` command, which deletes rows and therefore has no overlap threshold.
  All three treat equal identity keys as the same posting first; the first two
  apply the placeholder-company rule afterwards. Only `SameOpening` and
  `SameForDedup` refuse two unequal ATS-native keys.
- Placeholder companies are compared in compact form (letters and digits only):
  unknown, n/a, tbd, none, confidential, stealth startup and a few similar
  words. They match nothing.
- The package is free of I/O, clock and randomness but is not flagged `pure` in
  `internal/guard/layers.txt`, because the package map (D12) does not call for
  it.

## Tracker (`internal/tracker`)

- `states*.go` is clean-room (CR-STATES): the status table, the groups derived
  from it, the actors and the transition table. A test compares the table with
  the `CHECK` on `tracker_entries.status`.
- `apply_chain` may move a `discarded` row to `applied`, the one exit from
  `discarded` for an automated actor: the user may apply anyway to a posting
  they discarded (owner decision 2026-10-23, [apply.md](apply.md)), and the
  chain records the submission it made. Every other automated move out of
  `discarded` or `rejected` stays refused.
- `agent` may move any open row to `discarded` (owner decision 2026-11-12):
  the Ask Agent's `tracker_set_status` records the user's "I don't want this
  one" ([ask-agent.md](ask-agent.md)). Like every automated actor it cannot
  take a row back out of `discarded`.
- An archived row refuses every automated change unless the change sets
  `StatusChange.Unarchive`: then an allowed change also clears `archived_at`
  in its transaction, with an `unarchived` event under the same actor. Only
  the apply chain sets it, recording a single-posting start's submission
  ([apply.md](apply.md)); a refused change leaves the row archived.
- `ActorSystem` (`system`, a scheduled job) is outside the five actors of the
  table: `Archive` and `Unarchive` accept it, everything else refuses it as an
  unknown actor. The scheduled `archive.sweep` archives under it
  ([scheduler.md](scheduler.md)).
- Every write runs in one `store.Write` transaction (immediate lock, 5 s busy
  timeout). Status guards read the row inside that transaction. A refused
  automated change commits its `transition_refused` event (once per row, actor
  and target per UTC day) and then returns `*RefusedError`.
- Schema (migration `0001`, rewritten in place before the Phase 2 exit check):
  `location` on the row (the spec's location-conflict rule needs it), unique
  indexes on `identity_key` and on `report_number` where set, the
  `tracker_aliases` table, and triggers that make `tracker_events` append-only
  except for re-pointing an event to a merge survivor. Migrations must be
  numbered without gaps, so a separate `0003` file was not possible.
- A row with nothing to key on (a placeholder company and no URL) gets a unique
  `unkeyed:` key; it can never be matched, which is what a placeholder means.
- A report's `Aliases` are the keys of the addresses the evaluation reached its
  posting through (a tracking link, a job board's listing): rule 3 matches them,
  the row keeps them unless another row holds one, and a row whose key is one
  of them moves to the report's URL and key (flow round 4).
- `dedup` forms groups by shared key or alias, or by the same company with
  `SameForDedup` roles. Groups are pairwise compatible, never a transitive
  chain: rows sharing a key or alias form a unit; units are taken in order of
  their lowest row id and each joins the first group it matches in full
  (`SameForDedup` with every row in it), else starts a new one. So two rows
  with unequal ATS-native keys, differing requisition numbers or conflicting
  places are never in one group, whatever keyless row sits between them. A unit
  that fits a second group as well is an ambiguous bridge: the group holding
  it is listed with `needs_user` and a reason, and is not merged by `--apply`
  (the other requisition stays untouched). The merged event records the
  removed rows, their keys and their report numbers. The survivor holds the status of the most
  recent status event (calendar day, status order breaks a same-day tie), then
  the best score under the score rules, then the lowest id. Tables owned by
  other capabilities that point at a row (Gmail message links, documents) are
  registered through `Options.LinkedTables` and re-pointed in the merge
  transaction. No table points at `tracker_entries` yet, so nothing registers
  one; a table that does and is left unregistered makes the merge fail on its
  foreign key and roll back, which a test pins (the failure is loud, not a
  silent loss). A group whose latest status is not `submitted` but which holds
  a `submitted` row is never merged: the survivor would erase the record of a
  submission, so the group is listed with `needs_user` and the reason.
- A `user` or `gmail` change to `applied` that asks for `applied_by` = `agent`
  is accepted only when the posting (its key or an alias) holds an apply-chain
  submission that is submitted or may have submitted, read from `submissions`
  in the same transaction (`chainSubmitted`); it is how "Confirm submitted" and
  a matched receipt keep the agent's mark (F-5). The event's actor stays the
  user or Gmail; any other such request is `ErrInvalid`.
- Dates in `applied_at` use the calendar day of the injected clock's location;
  event times are UTC.
- The CLI (`applysmith tracker ...`) is a concurrent writer beside a running
  server, not an exclusive one. Reads (`list`, `show`, `export`, `followups`,
  `dedup` and `archive --dry-run`) open the database read-only, take no lock and
  write nothing. Writes join the database in WAL mode with the busy timeout and
  no lock file (`store.Options.Shared`); only a database that still needs
  migrating makes that one command take the workspace lock. `List` and
  `Summary` read in one transaction, so a page and its total agree. A running
  server serves the same operations as routes ([api.md](api.md)); the actor of
  a routed change is derived from the credential.
- `DedupMerge` (the dashboard's Merge) re-plans in its write transaction and
  merges the group whose rows are exactly the named ones; anything else is
  `store.ErrConflict`, a `needs_user` group `ErrInvalid`. It returns a token for
  `UndoMerge`, which puts back every member row verbatim (same id and key),
  re-points the events, aliases and linked rows the merge moved (by rowid),
  drops the aliases the merge added, restores the kept row's columns and
  appends a `merged` event naming the one it undoes. The copy lives in the
  server's memory only (`MergeUndoWindow`, 15 minutes, at most 20 merges): the
  rowids of moved links only mean something briefly, and a durable copy would
  keep deleted rows forever; a restart or the window's end makes the merge
  final, as the CLI's `dedup --apply` is. An undo after the kept row changed
  (its version moved) is `store.ErrConflict` and writes nothing.
- `DismissDuplicates` records that one group's exact rows are not duplicates
  (a `workspace_meta` key `tracker.not_duplicates:<ids>`); `planDedup` marks
  such a group `dismissed`, `DedupApply` and the dashboard leave it, and a row
  joining or leaving the group lists it again. `undo` deletes the key. `Page.Archived`
  counts the archived rows a filter also matches; `SummaryOf` can count them.
- The export carries every field a row shows, location, applied date, source
  and archived time included, in the list's order when the filter names a sort
  (the dashboard always does), else by id (the CLI).
- `BulkArchive` archives or unarchives ticked rows in one transaction and
  names the rows still in flight it left.
- A refusal's reason (stored in the `transition_refused` event and shown in the
  drawer's History and the Gmail review queue) is written in plain words.
- `ArchiveRow` archives one rejected, discarded or skipped row for the dashboard's Archive button
  (`ErrInvalid` for a row still open; an archived row is left as it is). An applied date on a row
  whose status was never applied for is `ErrInvalid` on `Add` (it was dropped silently before).
- `set-status --on` is refused with `ErrInvalid` for the user actor when the
  target is not `applied` or the row is already applied, because the date would
  be dropped silently. A refused automated attempt (`transition_refused`) does
  not count as a change when deciding whether a closed row has aged out.
- `export --out` writes a temporary file and renames it, so a failed export
  keeps the file that was there.
- `applysmith report override <number>` (`Tracker.OverrideDocuments`) is the
  user's override of the 4.5 document floor: it sets the report's
  `documents_eligible` and appends a user `documents_override` event to the
  row linked to that report, in one transaction. A screening or gated report
  is refused, as is a report no row links to; overriding twice writes once.
  The dashboard control is Allow a PDF anyway in an application's Documents,
  shown when a PDF is refused below the floor
  (`POST /api/candidacies/{id}/documents-override`, session-only).

## Follow-up cadence (`followup.go`, CR-FOLLOWUP)

Computed on demand from the rows, the events and `policy.yml`
(`tracker.followup`); nothing is stored as a suggestion and nothing is sent.
`applysmith tracker followups [--due] [--json]` lists, `applysmith tracker followup
<id> [--snooze Nd | --undo]` records, snoozes or undoes; so do `GET /api/tracker/followups`
(due today; each suggestion carries the row's `version`) and `POST /api/candidacies/{id}/followup` (`snoozeDays`, or
`undo`; `expectedVersion` required, a stale one is `revision_conflict`), the
Tracker's "Follow-ups due" panel, whose every press says what it did with an Undo; its Draft a follow-up is
`POST /api/candidacies/{id}/followup-draft` (session-only, nothing recorded; the body may name the kind, the contact and the last exchange). The drafter, also the row's Outreach note, keeps its form, draft and the draft it replaced in the tab's session storage (reopening spends no model call); each claim names the source line it rests on, a note says whether `voice.md` shaped it, and I sent this adds the message to the row's notes and, for a recruiter ping on an applied row, then records the follow-up with the version the note made; its Undo takes back the follow-up, then the note (`POST /api/candidacies/{id}/notes` with `undo` and `expectedVersion`). Choices the spec leaves open:

- A snooze moves the due day to N days past the later of the due day and today,
  so snoozing an overdue row counts from today.
- Recording a follow-up resets earlier snoozes.
- An undo takes back the latest follow-up or snooze still in force. Events are
  append-only, so it is an event of the same kind whose detail names the one it
  undoes (`undoes`); the cadence, the follow-up list, Home, the narrator and the
  drafts' "follow-ups already sent" leave both out, and the drawer and
  `applysmith tracker show` print the pair as an undo. A note undo is the same: a
  `note_added` event naming the caller's latest note in force, whose line leaves
  the row's notes (the drawer's Add note offers it, and `applysmith tracker note
  <id> --undo` quotes the version it read); `reply_check` reads the row as if neither was written.
- `max_followups` 0 disables the whole applied-row cadence (no
  `cadence_exhausted`, no `consider_closing`); `reply_check` is independent of
  it.
- A row is listed once. Once an exhausted row is past `close_after_days`,
  `consider_closing` replaces `cadence_exhausted` (one card, one Close); the
  consumers that count `follow_up` and `reply_check` are unaffected, and a
  draft that reads the cadence treats both kinds as used up.
- `reply_check` stops once the user has any later event on the row in force
  (the panel's I answered is a note); the 3-day interval is fixed and `reply_check` is not a `policy.yml` key.

## Outcome cohorts (`outcomes.go`)

`applysmith tracker outcomes [--from d] [--to d] [--as-of d] [--json]` and `GET /api/tracker/outcomes` are one read-only summary (`Tracker.Outcomes` opens a read transaction and writes nothing). It never feeds scoring, the profile or any preference.

- **Cohort.** Applications whose `applied_at` falls in the date range and on or before the observation cutoff (`asOf`, default today). Rows of one posting count once: the rows are clustered by the dedup command's test (`identity.SameForDedup`, shared keys and aliases), the earliest application date stands for the cluster and the histories are combined (`folded` counts the extra rows). A submitted row with no application date is counted in `noApplicationDate` and belongs to no cohort.
- **Reach.** From `created`, `status_changed` and `status_corrected` events up to the end of the cutoff day. Every status a row held is reached, so interview then rejected counts as both; a correction withdraws the status it replaced, because the user said it was wrong. Rates: replied (any reply from the employer, a rejection included), interview, offer and rejected, each as numerator, denominator (applications with event data), applications still open that have not reached the stage, and the cohort's unknown count.
- **Unknown, open and closed.** A row in the cohort with no status event up to the cutoff is unknown and is not reconstructed from the current status. A row whose events show no submission (a correction took it back to evaluated) is not an application. Closed means rejected or discarded at the cutoff; the rest of the applications with event data are open, and nothing infers a cause for any outcome.
- **Groups.** Overall, by `tracker_entries.source` and by the `role_family` of the row's report (read from `report_index`); an empty key is "not recorded". A group with fewer than 10 applications with event data, or with more than half still open, lists why it is insufficient for comparison.

## Not implemented here

The edit path that would recompute a row's identity key after a rename (the spec has no edit
command or event kind for it), and the Gmail and document tables that the
merge re-points.
