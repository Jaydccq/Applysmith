# Scheduler

Package `internal/scheduler` (layer 65) runs the user's schedules and the
built-in timers inside the server that holds the workspace lock. The job kind
adapters are in `internal/scheduler/jobs` (layer 66). Normative text:
`openspec/changes/migrate-to-go-mygo/specs/scheduler/spec.md`.

## Shape

- Store: `schedules`, `runs`, `resource_leases` (0001) and `schedule_history`
  (0016). `Manager` edits schedules and works without a server; `Scheduler`
  decides and runs.
- Decision: a tick re-reads the enabled rows, computes the occurrences after
  `last_fired_at`, and decides each once. `ClaimRun` inserts the run row (unique
  on schedule and scheduled instant), the history row and moves the cursor in
  one transaction. Admission order: disabled, unknown-kind, validation, offline,
  busy (same schedule), lease, busy (queue). Offline, lease and a full queue are
  deferred (retried each tick for 6 hours); the rest are consumed. Enabling a
  schedule or changing its recurrence (preset or start) moves the cursor to
  that instant (`Repo.Update`): the disabled period is never replayed, and an
  edited time that is already past today waits for its next occurrence.
- Missed fire: at start and after a gap over 5 minutes the policy applies
  (`skip`, `fire-once`, `fire-all` capped at 24 per pass, replayed outside the
  tick). Reason: `host-asleep` unless the monotonic clock covered at least 75
  percent of the gap (`host-starved`). Under `skip` an occurrence inside the
  500 ms grace ahead of the pass did not fall due in the gap and is decided as
  on any tick. A pass looks back at most 7 days and
  decides the newest 1,000 occurrences; a stop that arrives while `Start` runs
  its first tick wins, and no tick loop starts after it.
- Leases live in `resource_leases` with a 5-minute TTL. Local active holders
  (jobs and leased timer workers) retain exclusion across a longer clock gap;
  tick renewal and acquisition renew them before sweeping expired rows. Release
  ends local liveness even if its database delete fails, so abandoned rows
  expire instead of renewing forever. Timer release callbacks are idempotent;
  an active holder label cannot be reused. Other-instance expired rows remain
  eligible for takeover; the workspace file lock excludes two live schedulers.
  `signals.collect` holds `llm:evaluation`, like every kind that calls a model
  for evaluation; the registry refuses a kind whose leases and capabilities
  disagree. `browser` is held by a kind that drives the scan window and by one
  that queues evaluations of postings (`Drives.EvaluationReadsPages`), which read
  a page in that window when no ATS API serves the URL: `leads.evaluate` holds
  `browser` and `llm:evaluation`, as the spec says, and so does `inbox.evaluate`
  (F-1), which sends waiting inbox rows to evaluation (input `limit`, 1 to 100,
  default 20; it stops at the LLM daily cap). Every `discover.*` kind names it
  as its follow-up (`Kind.Then`, F-1): `Scheduler.followUp` runs it once after a
  scan that succeeds, when `discovery.Repo.HasWaiting` finds rows or
  `evaluate.OpenReviews` finds a review (`api.inboxWaiting`, F-8) and no enabled
  `inbox.evaluate` schedule exists, as a run on the scan's schedule with no
  `scheduled_for` (`Repo.ClaimFollowUp`).
- Recurrence: daily and weekly are computed here; `rrule` uses
  `github.com/teambition/rrule-go` for calendar arithmetic on wall-clock
  readings, and `resolveWall` maps each reading to an instant (a gap fires after
  the gap, a repeated hour fires once).

A manual run that collides with an already decided occurrence retains the
existing single-decision constraint and returns HTTP409 (`conflict`), rather
than an internal server error. A later distinct occurrence can run normally.

## Decisions taken where the spec was silent

- Retention sweep: runs at the first tick of a server and then once per 04:00
  slot; a machine asleep at 04:00 runs it on wake (parity Q-12). Rules register
  with `AddRetention`.
- Packs: `daily`, `twice-daily`, `hiring-signals-daily`, `memory-nightly`,
  `agent-improve-nightly` and `maintenance` use `fire-once`; `browser-boards`
  uses `skip` (parity Q-13). An entry whose kind is not registered is reported as
  unavailable, never created. `daily` and `twice-daily` end each round with
  `inbox.evaluate` 10 minutes after its last scan (keys `inbox.evaluate`,
  `.am`, `.pm`); a pack with no enabled source creates nothing.
- `applysmith schedule add/edit` print a warning (`jobs.ScreenWarning`), and the
  dashboard editor shows one, for a `discover.*` schedule in `screen` mode.
- `archive.sweep` (`tracker:write`) runs the default archive rules as the
  tracker's `system` actor, an actor that can only archive and unarchive. It
  previews unless its schedule sets the input `apply` to true: the summary says
  how many rows would move and nothing changes. The maintenance pack schedules it
  monthly without `apply`; the owner turns archiving on by editing that schedule
  (`applysmith schedule edit <id> --input apply=true`).
- `model-watch` is a built-in timer (`internal/api/model_watch.go`): once a local
  day, when an OpenRouter provider is configured with a stored key, it reads the
  public catalog and announces each routed model that left it (`model-gone`,
  normal class) once until the row is fixed; an unreachable catalog is retried
  after an hour and announces nothing. Without a provider or key it makes no
  request and the timer list says why. Its state is the `llm.model-watch` record
  in `workspace_meta`. Tests serve the catalog from `httptest`.
- `provider-watch` is a built-in timer (`internal/api/provider_watch.go`): on
  each tick it reads the last day of the model ledger (`Router.RefusedProviders`)
  and announces a provider whose calls since its last success include three
  refusals as out of credit (`quota-exhausted`) or a dead key (401)
  (`provider-refused`, normal class), once per episode; it never changes a key
  or a row. Its state is the `llm.provider-watch` record in `workspace_meta`, so
  an episode keeps its start while the day-long window slides past it.
- `user-data-snapshot` is a built-in timer (`internal/scheduler/backup.go`): the
  first tick at or after 02:00 local time that follows the last completed slot
  (so a machine asleep at 02:00 snapshots on waking) writes the same verified
  backup as `applysmith backup create` through `Store.Backup`, into
  `<workspace>-backups/snapshot-YYYYMMDD-HHMMSS` beside the workspace
  (`launch.SnapshotDir`; the API server runs the timer only when its config
  names a directory, so tests and embedded servers write nothing). It has a
  10-minute limit (a timer may declare `Limit()`), stops and removes a partial
  snapshot when the scheduler stops, and keeps the newest 14 of its own
  directories within a 2 GiB soft budget, never fewer than two: the old backup
  script's rule (parity Q-15). `PruneSnapshots` touches only directories named
  like a snapshot that hold a manifest. The outcome is the `scheduler.snapshot`
  record in `workspace_meta` (last slot, time, path, last error); a failure is
  logged, leaves the slot open and is retried after an hour.
- Pause all / resume all (`Repo.PauseAll`, `Repo.ResumeAll`): pause-all switches
  off every enabled schedule in one transaction and records them in
  `workspace_meta` key `scheduler.paused-all` as id plus creation instant
  (ids are reused after a delete). Resume-all enables only recorded schedules
  that still exist and are still off, moves their cursor to now so the pause is
  never replayed, and clears the record. Both work without a server.
  `Repo.PauseKind` / `ResumeKind` do the same for one job kind under the key
  `scheduler.paused-kind.<kind>`: disconnecting Gmail (`Manager.PauseGmailSync`)
  pauses every `gmail.sync` schedule, and connecting (`ResumeGmailSync`)
  resumes them. No pack reads Gmail: the server's watcher reads mail as it
  arrives ([gmail.md](gmail.md#watching-for-new-mail)), and a `gmail.sync`
  schedule is one the user made, which skips with "a sync was already running"
  while the watcher's sync holds the slot. `Repo.Paused` names the record holding
  each schedule still off (the list's `pausedBy`: `all` or the kind; pause-all's
  wins, as resume-all brings it back), and a schedule carries the run of its
  newest occurrence that started one (`lastRunId`) and when its newest
  decision ran or was skipped (`lastResultAt`; `lastFiredAt` is the cursor),
  with that decision's reason and detail (`lastReason`, `lastDetail`). A cancel
  of a running scheduled run (`POST /api/runs/{id}/cancel`) ends as a failed
  result with reason `cancelled` and is never counted toward a dead lane
  (parity Q-3, Q-14).
- Previews write nothing: `PreviewFires` validates a preset and lists its next
  fires. `POST /api/schedules/preview` serves a draft, each entry of
  `GET /api/schedule-packs` carries three `next` times as if applied now, and
  `applysmith schedule preview <pack>` or `--at/--days/--rrule` print them.
- Hygiene and the day audit (`internal/scheduler/hygiene.go`) are read-only
  and surface as the doctor leaves `discovery.schedule-hygiene` and
  `discovery.schedule-audit` (parity Q-10); `GET /api/schedules` also puts
  each finding's action on the schedules it names (`attention`). A dead lane is the newest three
  finished runs failing (of the latest five; cancelled and interrupted runs do
  not count); the audit's expected slots start at the later of local midnight
  and the schedule's last edit and stop two minutes before now, so a slot the
  tick has not decided yet is not reported.
- `gmail-digest`, `gmail-alerts` and `gmail-reminders` are real timers
  (`internal/api/gmail_timers.go`, `docs/architecture/gmail.md`); they list as
  inactive with the reason "Gmail is not connected or refuses its app password" until Gmail is connected.

## CLI and setup

- `applysmith schedule` (including `pause-all`, `resume-all` and `preview`)
  writes the schedule tables directly through the manager,
  so it works with no server; `jobs.Catalog` supplies the kind definitions for
  validation and never runs a job. `run` and `delete --cancel` call the running
  server, because only its scheduler starts and cancels jobs. A change made
  from the CLI while a server runs takes effect on the next tick; the dashboard
  shows it on its next refresh.
- Setup step 15 (`discovery`) takes `{"pack": "none"|"daily"|"twice-daily"}`
  and an optional `use_target_roles` and `enable_boards`
  ([discovery.md](discovery.md)); the pack is built after their edit of
  `portals.yml`, so a board they turned on is scheduled.
  It applies the chosen pack, removes the schedules the other setup packs
  created, and records the choice, `none` included. Entries whose kind is not
  built are reported with `jobs.UnavailableReason`. The interactive flow offers
  it after the required steps; an empty answer or an exhausted script leaves it
  undecided.

## Not registered yet

`jobs.Pending()` lists each kind that is missing and why; it is empty now, every
kind of the spec's registry has a service. A task that adds a kind whose service
is not ready lists it there.

## Stop and scan failure boundaries

Graceful stop closes admission for manual runs and missed-fire replay before
draining queued work. Every queued run is finished as failed/interrupted,
including its history, completion signal and end event; running work receives
cancellation and the existing ten-second bound. Stop/cancel causes take precedence
over a late nil return, so the end event agrees with the interrupted/cancelled
record. An admission waiting at the
stop boundary cannot leave a new queued/running row behind. Queued cancellation
is included in the wait group, so waiting for work also waits for its final
store write.

Discovery adapters preserve the scanner's durable `Retryable` classification
as a transient job error. Sign-in and challenge/rate-limit/access walls remain
needs-you outcomes with no automatic retry. Tests cover actual synthetic
HTTP503 scanner output through the registered scheduled kind, cold scan-record
reads, mixed permanent/temporary pages and queries, and terminal-status
precedence. These tests do not establish real host availability or sleep/wake
acceptance.

Checked live on a demo copy (2026-10-17): a server killed with `kill -9` (and
with `SIGQUIT`) mid-run restarts with the scheduled run `failed/interrupted`,
its history row `interrupted`, the schedule's active run cleared and the scan
record `interrupted/process-exit`; the occurrence is not replayed, so nothing
runs twice. Occurrences that fell due while no server ran are decided once at
start with reason `host-asleep`: `fire-once` and `fire-all` fire, `skip` skips.
An 18-minute soak (2,576 read requests, 250 short board-stream connections,
seven scheduled scans) kept 29 goroutines (the same as a fresh start), 30
open files, RSS between 16 and 76 MB with no upward trend and a WAL under
132 KB. `go test -race` (one package at a time) passed for scheduler, store,
llm, notify, gmail, remote, mcpserver and browser (after the close-retry fix)
and for api, whose two timing-sensitive tests now measure only what they mean
to under the race detector's slower start.
