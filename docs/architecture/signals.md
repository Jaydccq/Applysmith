# Hiring signals

Package `internal/signals` (layer 48). Normative text: the `hiring-signals`
capability of `openspec/changes/migrate-to-go-mygo`. One pipeline in the main
process and the main database; there is no second process, database or
dashboard server. Tables are `signal_*` in `0035_signals.sql`.

## Pipeline

A tick (`Service.Tick`) runs: collect, ingest, identify companies, merge events,
assess, score and tier, check the postings of admitted companies, hand off,
outcomes, lane alarms, notify, daily digest. Entry points: the scheduler kind
`signals.collect` (the `hiring-signals-daily` pack, 06:30), `applysmith signals
tick`, `POST /api/signals/ticks` (`Idempotency-Key`, `202` with a run id).

- One active tick: `signal_ticks` has a unique index on `status = 'running'`.
  A second start fails naming the running id. A claim with no heartbeat for
  longer than the budget plus ten minutes is taken over; the server closes a
  stopped process's tick at start (`SweepOrphans`).
- One 20 minute work deadline starts after the active claim is acquired and
  reaches settings, HTTP, model calls, checks and notifications. Budget
  exhaustion ends `completed` with `budgetReached`, reached counts and unfinished
  work still due. An independent error at the deadline remains `failed`, including
  a joined error that also contains a cancellation cause. A caller's cancellation
  or earlier deadline is `failed`; limited collection and best-effort notification
  paths obey the same rule. Final summary persistence gets a separate, cancellation-
  independent allowance of at most ten seconds; a write failure is returned.
- Manual ticks are cancelled through the Runs route's scan-run owner. Scheduled
  `signals.collect` runs retain the scheduler's cancellation owner and history.
- Assessment runs four events at a time (`assessWorkers`; the router still caps
  each provider's calls in flight): one at a time, at about 15 seconds a call,
  a first tick of 266 Hacker News posts spent its whole budget assessing 66.
  With four, two live ticks still spent the whole budget assessing and no
  company was ever checked. So identification and assessment run under a
  deadline a quarter of the budget early, and the check under one a twentieth
  early (`tickReserves`, F-1); a stage whose share ran out ends only itself,
  and scoring, the check and the hand-over of what it reached still run.
- Identification (300 documents a tick) and assessment (200 events) each keep
  the place their last pass stopped at (`signal_collector_state`, source
  `pipeline`, scopes `identify` and `assess`). A pass starts after it, oldest
  first, and comes back to what lies before it only when nothing newer is
  left: a document or event that fails every time is tried once a round and
  never keeps the ones after it waiting.
- Only finished company checks (`ok` or `failed`) count toward cadence. A
  `started` revision left by an interrupted tick can be resumed by the next tick
  under the same key, retaining already stored postings; completed revisions and
  repeat claims by the same tick remain deduplicated.
- The summary (`TickSummary`) is filled in place and saved at the end and after
  each stage; a failed stage returns the error and the summary keeps the
  counters reached, status `failed`.
- `--sources` / the `sources` input collects and ingests those collectors only.
- Every document emitted by a collector must be durable before its cursor or
  conditional-request validators advance. A failed insert marks that source
  failed and preserves all its checkpoints; successful inserts remain stored
  and deduplicate on retry. Other collectors and later stages continue. The
  tick can complete with failed sources, so its one-line run/history summary
  includes their count, and its source results retain the redacted cause.

## Files

| File | What |
|---|---|
| `fetch.go` | one limiter per host (gap by call class), 30 s timeout, two retries on 429/5xx |
| `collect.go`, `edgar.go`, `alerts.go`, `hn.go`, `web.go`, `x.go` | the six collectors, each failure-isolated; cursors and validators advance only after the documents were stored |
| `names.go`, `geo.go` | rule canonicalization, entity guard, roundup, hiring-thread title rule, geography filter |
| `identify.go`, `repo.go` | document to company events; merge within 7 days |
| `assess.go` | `signal-assess` under rubric `r1`, verbatim quote check, funding-use guard, held statuses, review queue |
| `policy.go`, `policy/v1.json`, `score.go` | the versioned policy, five-factor score, momentum, tiers, blocks, rescore, replay, decompose |
| `resolve.go`, `gate.go`, `check.go` | ATS identity lanes, the pure posting gate, tier budgets and revision keys |
| `handoff.go` | inbox admission (`discovery.Repo.AdmitPosting`), posting and review leads |
| `notify.go`, `outcomes.go`, `health.go` | hot notice and digest, outcome ledger, lane alarms |
| `views.go` | read models and user actions behind the API and CLI |

## Decisions that are not in the spec text

- Source defaults: `signals-hn-hiring` is on; every other source is off until
  `enabled: true`, which the switches of Leads, Hiring signals, Sources write
  (the SEC source also needs `candidate.email`, and every surface that turns it
  on shows `EdgarDisclosure` first: setup's step, and the switch's confirm
  from the lane's `disclosure`). Alerts feeds
  are entries of `sources.signals-alerts.queries` with a `url`; other sources
  take query strings.
- Adapters (the `source factor` keys) are `edgar`, `alerts`, `hn`, `hn-hiring`,
  `web`, `x`.
- Provider choices (kept on purpose): web search is Exa, with the key of the `exa` discovery source
  (`api-key-exa`); no key means `no key`. The X collector speaks the paid
  search API of `api.twitterapi.io` (`queryType` Top and Latest, key
  `api-key-x`). Both are behind interfaces and tests use httptest fakes.
- A user's slug override and website are the entries of `companies` in
  `portals.yml` whose name matches (`ats` + `slug`, or `careers_url`).
- Tier changes enter `hot` once: a hot company is announced when it has no
  delivered notice since it entered hot. A channel that is not configured
  records a no-op row and leaves the key free.
- `signals-edgar` sends the profile's `candidate.email` to the SEC and nowhere
  else. Any surface that enables it must first show: "Every request to the SEC
  carries your contact email, as the SEC fair-access policy requires." (`EdgarDisclosure`).
- Leads: the pipeline writes through `signals.LeadSink`; `signals.DiscoveryLeads` puts them in the leads store of job discovery (`GET /api/leads`). A posting lead carries its inbox row; a company with no board gets one `no-ats` lead, or one `careers-page` lead when its careers page was reached, written once per company and evidence link. The completion key is written only after durable lead admission; interruption
  before that key retries through the leads store's own de-duplication, so an
  unfinished no-ATS/careers lead is neither lost nor duplicated.
- `signals-edgar` keeps the newest index day it read (`signal_collector_state`,
  scope `day`). A tick reads the newest published index and then every
  published one newer than that day, inside its walk of today and four
  business days back, so a business day on which no tick ran is still
  collected; a gap longer than the walk needs a backfill (`--days`). The first
  run reads the newest index only.
- Retention: bodies are kept 180 days after last use; `Repo.PruneBodies` is run
  by the scheduler's retention pass. The same pass deletes the audit log's
  deferral lines after 180 days (`Repo.PruneDeferrals`): a tick writes one per
  company its scan budget left over. Every other audit line is kept; the
  `state` lines are what a restore reads.
