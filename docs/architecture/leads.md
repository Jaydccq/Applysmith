# Leads

Task 6.2, the leads half. A lead is a hiring signal that is not yet one
posting: a LinkedIn feed post, a careers page, a company with no ATS board.
Code is in `internal/discovery/leads*.go`; the table is in the leads migration.

| File | What it holds |
|---|---|
| `leads.go` | The `leads` table: add, get, list, count, patch, dismiss; careers pages (`LeadSink`) and `no-ats` leads |
| `leads_dedup.go` | Feed keys: permalink, else SHA-256 of author and body; the 14-day company and role repost check |
| `leads_feed.go` | `addFeedLead`: dedup, category, per-role gating, promotion to the inbox |
| `leads_gate.go` | `LeadChecker` (every failing rule, role by role) and the gate audit table |
| `leads_expand.go` | Careers-page expansion ladder, the ledger, the 20-URL cap |
| `leads_recover.go` | Recovery of a posting lead from its board: open, closed, unknown; one listing request per board |
| `leads_eval.go` | `leads.evaluate`: recovery, then a deep evaluation, 25 a run |
| `leads_health.go` | The silent-source alarm for scheduled discovery runs |

## Decisions

- A lead stands on evidence: text capped at 4,000 characters, or the reason
  there is none. The table refuses a row with neither.
- The dedup key is stored, not derived from model output. `LeadAdd` returns the
  stored id with `ErrLeadDuplicate` when the key exists. The repost check uses
  the same-opening predicate with location ignored.
- Expansion and evaluation reuse the scan pipeline through `Scanner.leadRun`,
  so the title filter, blocked list, duplicate check and inbox insert are the
  scans' own. Postings the filters refuse are written to `lead_gate_audit`
  with every failing rule and an ingress; nothing reads the log back.
- The expansion ledger (`lead_expansions`) records successfully completed pages,
  including those that yielded nothing. Admission, transport or completion
  failures leave the page retryable; completion and lead state commit together.
  A board's permanent failure (`BoardFailure`) completes the page: unreachable
  for a board that is gone or refuses the reader, none for one that lists
  nothing or answers an unparsable payload, so such a lead does not hold a
  place in every later run's 20-URL cap.
  `applysmith leads expand --force <id>` reads it again.
- A lead that is not evaluable stays `new`. `eval-cap` leads are taken first
  on the next run. `eval-failed` leads wait for a person; explicit dashboard
  `ids` or CLI IDs can retry them. Ordinary and scheduled passes do not retry failed leads.
- Schema 0044 stores `evaluation_category`, the original posting/no-ats/email/
  dm/comment category. It is written atomically with `eval-cap`/`eval-failed`,
  selects board recovery versus own-text evaluation and is restored on success.
  A link in a text lead never changes this mode. Historical review rows without
  provenance are reported as actionable failures and left for a person; they
  do not prevent independent healthy leads from running.
- Admission and persistence failures return joined per-lead errors after the
  bounded pass. A failed route or schedule cannot report success merely because
  its summary counted failures. Work already accepted by the evaluation pool
  consumes the cap even if its final lead update fails. The lead/inbox link is
  finalized under a ten-second context independent of caller cancellation, and
  accepted counts remain visible on cancellation.
- Recovery never invents text: a board that lists no description gives
  `unknown`, and the lead is not evaluated from its title.
- The silent-source alarm is decided by `SourceAlarmFor` after a scheduled run
  (runs are marked with `MarkScheduled`) and raised as one needs-you item per
  source per day.

## Surfaces

`applysmith leads list|expand|evaluate|dismiss`, `GET /api/leads`,
`POST /api/leads/{id}/dismiss`, and the scheduler kinds `leads.expand` and
`leads.evaluate`. The dashboard runs them on demand through `POST
/api/leads/expand` and `POST /api/leads/evaluate` (`Idempotency-Key`, `202`
with a run id of kind `leads`, one pass of each kind at a time; `POST
/api/runs/{id}/cancel` stops a pass between leads, and the lead being read
when the cancel arrives is the last one touched). `ids` limits
an evaluation to those leads and forces an expansion of their pages, as
`applysmith leads expand --force` does. The Leads and Signals page is described in
[dashboard.md](dashboard.md).
