# Discovery

Implementation notes for `internal/discovery` (task 2.5). The normative text is
`specs/job-discovery`; this file records the choices the spec leaves open and
what is not built yet.

## In this build

- Sources: `registry` and `tracked`, over the five public ATS board APIs
  (Greenhouse, Lever, Ashby, SmartRecruiters, Workable), and the feed sources
  `builtin`, `amazon`, `a16z`, `theirstack`, `github`, `speedrun` and `exa`
  (task 6.3, below). The browser sources (task 6.2) read pages in the
  scan window; see [browser-scans.md](browser-scans.md). A keyed source reports
  `needs key` first.
- `scan*.go` is clean-room (CR-SCAN-SKELETON): the runner, the sweep, the
  shared pipeline (block, title, location, scope, collapse, de-duplicate,
  enrich, rank, insert) and the run records.
- Storage is in migration `0001` (a clearly separated section): `ats_registry`,
  `blocked_companies`, `inbox`, `scan_history`, `scan_runs`
  and the shared `data_sources`. A partial unique index allows one `running`
  scan per source.
  Evaluation outcomes live in each scan record as job-id entries, so a shared
  evaluation can settle multiple scans exactly once.
- One wall-clock deadline covers a whole scan: preparation, browser host
  preflight, collection, detail enrichment and evaluation admission (default
  thirty minutes). Per-request limits may shorten it. Budget exhaustion closes
  as `completed/budget-reached`; a user cancel remains `cancelled` and an earlier
  caller deadline/server stop remains `interrupted`. Unfinished boards stay due.
  Observed independent failures survive concurrent cancellation, including typed
  HTTP errors without wrapped causes and joined context/independent errors.
  Terminal sign-in/challenge outcomes retain precedence over earlier query or
  layout errors. Scheduled summaries disclose budget exhaustion.
- Inbox admission honors the caller's cancellation. After the queued transition,
  the pool's acceptance or refusal is recorded with a separate ten-second
  deadline: refusal restores pending and the attempt, acceptance records the
  job id without changing a fast worker's terminal state. A cancelled scan
  cannot leave a refused row queued merely by cancelling this accounting.
  The queued transaction captures its attempt number; final writes predicate
  on that attempt so an older admission cannot overwrite a newer retry, and
  refusal only returns a still-queued row to pending. An accepted job consumes
  its queued count even when persisting its job link fails; the scan still
  fails on that write error. Accepted evaluations belong to the shared pool and
  can finish after discovery/admission closes; their report outcomes settle the
  scan record separately.
- Progress snapshots, final scan accounting and Exa checkpoints use detached,
  ten-second deadlines to preserve completed work after cancellation. If the
  final record write fails, `Scanner.Run` returns a failed record and an error;
  both manual and scheduled runs fail. A database row left running by a refused
  terminal write is reconciled as interrupted at restart, after the database is
  writable again. It is never treated as a successful durable commit.
- A scan loads the inbox, tracker (live, evaluated-only and archived rows) and
  scan history into memory once, keeps the index current as it inserts, and
  matches with `identity.SameOpening`. Discovery reads the tracker tables
  directly; the tracker package exposes no read model for this.
- Store attribution for a duplicate: `run` (an earlier row of the same run),
  `inbox`, `tracker`, `report` (a live tracker row that exists only because of
  an evaluated or skipped report), `archive`, `history`. `--retry-known` skips
  only `history`.
- Within one board's batch, postings collapse by `SameOpening`, so two entries
  with unequal ATS-native ids are two postings even with the same title; only
  entries that share an identity key (a Workable shortcode listed twice, a
  multi-location posting) collapse, to the most recently posted one.
- The sweep runs 16 workers; the client caps requests per host
  (`concurrency.list`), keeps 100 ms between requests to one host, retries
  network errors, 5xx and 429 three times with 1, 2 and 4 second backoff
  (`Retry-After` honoured up to 60 s) and opens a per-host breaker after 429s
  survive their retries. Strikes (404, 410, 401, 403, a JSON payload of the
  wrong shape, empty list) are held back until some visit has succeeded and are
  dropped when none does, so a run that ends `host-offline`, or that only
  reached a captive portal, strikes nothing. The client follows redirects only
  within the requested host; a redirect off it, a 200 whose body is not JSON,
  and a body over 32 MiB are transient failures, never strikes.
- A vendor URL is canonicalized before the pipeline sees it (the vendor's form
  is kept as the row's original URL). A Greenhouse application frame
  (`job-boards.greenhouse.io/embed/job_app?for=X&token=N`, what Jobright links)
  is stored as the posting page it embeds, `job-boards.greenhouse.io/X/jobs/N`;
  its identity key was already the same. A URL that is not absolute http(s), or
  names a loopback, private, link-local or single-label host, is counted under
  `filtered:invalid-url` and never stored. `applysmith inbox add` refuses the same.
- A posting read from a vendor API carries the ATS-native key from its board
  and job id, so a Greenhouse posting whose URL is on the employer's own site
  still matches a later add by its Greenhouse URL. An inbox insert that loses
  the race for an identity key counts as a duplicate of `inbox`.
- Rows tied on score are enqueued in the order the boards and their postings
  were listed, whatever order the workers finished in.
- Greenhouse is the only ATS that needs a detail request for survivors
  (description plus the application form's questions). Lever, Ashby and
  Workable carry text in the listing; SmartRecruiters carries none, so a
  scanned row's description stays empty and evaluation reads the page. A
  posting lead is evaluated only on the board's own text, so its recovery
  reads a SmartRecruiters posting's record (`FetchDescription`,
  `/v1/companies/{slug}/postings/{id}`).
- A board of `jobs.eu.lever.co` is served by Lever's EU API only
  (`api.eu.lever.co`; the global one answers 404). A registry row is
  `ats:slug` with no region, so `FetchBoard` asks the EU API for a Lever board
  the global API answers 404 for (`Endpoints.leverEUListURL`); a board both
  miss is a strike as before.
- Ranking, filters, scope classification and the three signals are plain Go with
  no model, browser or clock reads (the clock is injected). A test fails if the
  package imports `internal/llm`, `internal/agent` or `internal/browser`.
- An inbox row keeps the score it was admitted with, except its target terms
  (`target-role`, `senior-wording`, `early-career`): a listing by score of
  pending rows (the Inbox page, promote top N, the drain, the CLI, Ask Agent,
  Telegram) first re-scores them when the target roles differ from the ones
  the rows were scored under (`Repo.RankWith`, digest in `workspace_meta`
  `discovery.inbox_targets`), so roles saved after a scan reorder the rows
  already waiting.
- The eligibility gate and the evaluation queue are injected: `Scanner.Gate`
  (nil rejects nothing; task 2.6 supplies the real gate) and `Scanner.Enqueuer`.
  The CLI enqueues by creating a `runs` row of kind `evaluate`; while a server
  holds the workspace, `applysmith scan <source>` posts the scan to it
  (`POST /api/scans`) and prints its record when the run ends. A scan and a
  promotion pass the row's role, company and place with its address, so the
  evaluation does not take the page's heading as the role.

## Decisions

- An empty location text fails a non-empty `locations.include` (the literal
  rule); the scope check keeps it as `location-unverified`.
- The scope classifier (`scope.go`) reads place names and, after a comma, a
  US state, Canadian province or country code; a code shared by a state and a
  country ("DE", "IN", "ID") names neither, so "Wilmington, DE" stays unknown.
  A place outside the five scopes (Latin America, the Middle East, Africa)
  classifies `elsewhere`, which only `worldwide` passes; an unknown place
  passes every scope as unverified. Cities are added only when no well-known
  US town shares the name.
- A scan's scope with no flag and no `scan.defaults` comes from
  `eligibility_gate.allowed_countries`, else from the countries setup step 4
  records (`work_authorization.authorized_countries`), the order the
  evaluation's geography rule reads. A new user never sets the first, and
  without the second a US-only user's scans read London postings (release
  check 2026-11-15).
- A `registry` scan with no active registry rows but enabled tracked companies
  still runs, and skips only when both are empty.
- Slug lists for `registry seed` are `ats,slug[,company]` lines, or, when the
  source names its ATS, a JSON array of bare slugs or one slug per line. The
  default lists (`DefaultSeedSources`) are three raw files of a public MIT
  list, one per ATS, embedded as addresses only; each fetch records its URL,
  hash and licence in `data_sources` and the list address and date on every new
  row. `registry.refresh` re-runs them (new rows only) and probes the dead
  boards that are due.
- The 15-second request bound, 16 in flight, 100 ms gap and the seed limits
  (120 s, 50 MB, 200,000 rows) are constants asserted by tests.

## Feed sources (6.3)

- A feed run (`source_feed.go`) is the second run shape beside the board
  sweep: it reads saved queries (`sources.<s>.queries`, a string or a mapping)
  or, for an ad hoc run, the one search the flags build (search, title, city,
  state or url; `--limit` and catalog defaults never make a run ad hoc), hands
  each query's rows to the same pipeline and ends `skipped`, `failed`
  (`layout-changed`, `no-cards`, `empty-extraction`), `blocked` (a challenge or
  rate limit on a list page, no retry) or `completed`. One failing query does
  not stop the others; every query failing fails the run.
  A query that fails after it gave rows (a Speedrun list page past the first,
  a LinkedIn feed scroll part way down) keeps those rows and is not a failed
  query: `run.partialRead` counts it in `BoardErrors` and notes it in the
  record's errors, so a completed run that read less than it should says so.
- Source numbers that are not counts live in `Record.Detail` (sweep `changed` and
  `unchanged`; Exa spend and dropped hits). Each HTML list page appends a bounded `Attempt` (200, no query).
- Row admission refuses listing, search and recommendation URLs
  (`filtered:non-posting-url`) from a deny-list kept in `posting_url.go`. A
  listing path whose query names a posting (`/careers?gh_jid=`, Eightfold's
  `/careers?pid=`) is a posting.
- Built In cards carry the location after the pin icon, and a card with several
  locations lists them in that text's tooltip attribute.
- Mirror rows go through `Scanner.Mirror`, an interface the API fills with the
  apply chain's `MirrorSources.Resolve`: discovery cannot import `apply`
  (apply imports discovery), so the adapter lives in `internal/api` and both
  callers run one implementation. The mirror stays as an alias; an unresolved
  one is flagged `mirror-unresolved` with the resolver's reason.
- A board row's Apply link that is followed (a button's popup, the board's own
  redirect) is read where the page ends, until it shows the posting's title,
  at most the content wait: a click tracker moves on by script seconds after
  the load (appcast through a redirect page to the employer's Oracle posting
  took 8 s). A landing that never shows the title is another posting (an ad
  network sent ZipRecruiter's DTCC and Tillster rows to one PTC internship);
  the row keeps the board's address and the run counts
  `apply-landing-other-posting`.
- Vendor shapes for a16z, TheirStack and Exa are written from the vendors'
  public APIs and the spec and are covered by fixtures only. The live smoke
  (2026-10-12) confirmed Amazon and Built In and could
  not run a16z (a POST) or the keyed sources. OpenJobs was dropped (its host
  stopped resolving); a workspace file that still names it fails validation
  with `"openjobs" is not an allowed key`.
- An Exa hit's title is its page title, which names the employer as a tail
  (Ashby "… @ expensify", Greenhouse "… at Chime"); a tail naming the board's
  company is left out of the posting's title (`identity.RoleWithoutCompany`).
- Exa keeps its watermark, seen set, suspensions and owed windows in
  `exa_state` (one JSON row); careers-page hits go to `Scanner.Leads` when the
  leads store (6.2) exists, and are counted otherwise.
- Exa's spend cap (USD 0.25) is lower than a full plan costs, so a run can stop
  with searches unrun. The watermark still advances when every search that ran
  succeeded; each search the cap (or `--limit`) left is owed the window it
  missed (`ExaState.Owed`: the time its next search starts from) and runs
  first on the next run, so every search has its turn and none loses a window.
  The run ends `completed/budget-reached`, the code of the time budget;
  `Record.SpendCapped` tells the two apart, and the scheduler's summary and
  the dashboard say "spend cap".
- `linkkind.go` classifies links and expands shorteners (3 hops, 10 s a hop,
  no cookies, stops at a login or challenge page, 5 a lead). A hop follows a
  redirect, a meta refresh, a script redirect, or the `external_url_click`
  link of lnkd.in's leave-LinkedIn page, which answers 200 rather than
  redirecting; before that was read, every lnkd.in link of a feed post was
  dropped, as ranking removes an unexpanded shortener. That link is read
  before the challenge words, which the page's
  `data-recaptcha-v3-integration-lix-value` flag would otherwise match. The leads store that
  consumes it is task 6.2.
- A posting lead on an employer's custom host may recover its owning board
  from its linked inbox row's validated ATS-native identity. The lead URL must
  canonically match that row's URL, original URL or aliases. Recovery still
  queries the vendor listing for current facts; a cached description or a
  company name cannot establish ownership or liveness. Unlinked, mismatched
  and non-native rows stay unknown. This uses existing stored identity, with
  no schema change or invented vendor URL (2026-10-16).
- Liveness (`liveness*.go`, CR-LIVENESS): `ClassifyLiveness` applies the six
  rules in order over a `PageRead`; `Checker` stores the result in
  `liveness_checks`, audits old inbox rows and unapplied tracker rows (2 at a
  time, one request per host every 2 s, 30-day recheck, 200 a run) and applies
  the effects. The real reader is `launch.LivenessReader`, a scan tab of the
  managed browser; `applysmith liveness <url>` and the scheduled audit use it.
  A Workday shell is read until it carries posting data (JobPosting JSON-LD or
  `data-automation-id="jobPostingHeader"`, 8 s at most); without it the read is
  `uncertain` / `posting-not-rendered` and the site tagline is never a title. A
  redirect that drops the posting's id (Workday `_JR123` included) and lands on
  a `/careers` path is `redirect-lost-job`. An evaluation whose own browser
  read is `expired` hands the verdict to `Checker.Record` (`evaluate.ClosedError`,
  `Deps.Closed`), so the row expires instead of returning to pending. An
  apply run that finds the posting closed does the same (`apply.Deps.PostingClosed`:
  HTTP 404 or 410 is `http-404` / `http-410`, a redirect to the site's error
  page or careers home `redirect-lost-job`), so a closed posting leaves the
  inbox and is not offered to the next batch. The
  reader leaves cookie or consent notices and other postings' cards out of the
  text and the banner, reads a page whose content sits in a same-origin frame
  (iCIMS) in that frame, reports the address and status of the document it
  read, and returns the board's or the JobPosting description, the canonical
  link and the apply links (flow round 4).
- Inbox drain (F-1): `Repo.EvaluateWaiting` is the body of the scheduler kind
  `inbox.evaluate`. It first sends the reviews `evaluate.OpenReviews` lists
  (quick-screen reviews waiting on the board whose posting was not evaluated in
  depth) as evaluations in depth naming the review (`EvalRequest.Review`),
  inside the same limit and cap (F-8). It then walks pending rows best score
  first, skips rows with two
  failed attempts, stops at `limit` (the kind's default is 20) or the LLM
  daily cap, queues in `auto` mode at scan priority, and writes a gate
  rejection into the row's reasons as `gate:<rule>`. A user's promotion
  (dashboard, `applysmith inbox promote`) queues at user priority; `evalMode`
  treats `jobright-recommend`, the source Jobright rows are stored under, as
  `jobright`. The Ask Agent and Telegram look up the waiting row by URL
  (`Repo.InboxRowFor`) and queue it with their request.
- No model, no queueing: while an evaluation task (`jd-extract`,
  `quick-screen`, `deep-eval`) has no route (`llm.Router.EvaluationUnrouted`),
  a scan's top rows (`Scanner.Unrouted`) and the `inbox.evaluate` drain queue
  nothing, so no row spends an attempt on a no-route failure that says nothing
  about its posting. The drain's summary, the scan's progress line and its
  record (`Record.Unrouted`, worded in the scan's notice and Home's day line)
  name the task; `GET /api/inbox` carries it as `unrouted`, and the Waiting view says
  so with the Settings, Models link in place of its top-rows sentence. The
  dashboard's presses (`POST /api/evaluations`, its retry, `POST
  /api/inbox/promote`) are refused then with 503 `no_model_route`
  (`evaluationModelMissing`), and an evaluation another way starts (the CLI,
  the agent, Telegram) that fails at no-route releases its row with
  `ChargeNone`, so the row keeps both attempts.
- "Use my target roles" (`PlanTargetRoles`, `scan_sources.go`): adds the
  profile's target role titles to `filters.title.positive` and the primary
  role as the one saved search of each enabled `SavedSearch` source without
  one. Setup step 15 writes it; the dashboard reads
  `GET /api/portals/target-roles` and writes through `PATCH /api/config/portals`.

## Not built yet

The apply list's use of a stored liveness result. The server-side `Scanner.Gate` and `Enqueuer`
wiring is in [api.md](api.md): a scan started over HTTP enqueues into the
evaluation pool.

## GitHub lists (2026-10-17)

- `plainCell` drops symbol characters (the lists' emoji markers), so names
  reach the inbox without them; letters of any script stay.
- A list row whose link is a redirector (`zapply.jobs/l/d/workday-...`, and
  `go.vansh.nyc/<code>?s=A` for every row of the vanshb03 list) is
  resolved to the employer's posting URL before the pipeline derives its
  identity (`linkResolver`, `redirectLinkHosts`): the same job listed by
  SimplifyJobs and by the zapply list is one posting. Eight links at a time,
  four hops at most, ten-second requests, one visit per link per scan, plain
  HTTP and no browser (the redirector answers with a 302 to the employer; the
  employer's page is not requested here). A link the redirector sends to its
  own site (about half of the zapply list today: the posting is gone) is
  dropped as `filtered:dead-link`; one that cannot be followed as
  `filtered:unresolved-link`. Neither is stored with a redirect address, and a
  later scan tries the unresolved ones again.
- A page every company shares is not a description. After the application
  pages are read, a text identical across rows of two or more companies is
  cleared (counted as `enrichment_degraded`), and a page whose title names an
  outage, block or not-found page is not read as text at all. One company
  repeating its text across requisitions is kept.
- An Oracle Recruiting posting (`*.oraclecloud.com/hcmUI/CandidateExperience/
  .../[sites/<site>/]job/<id>`) is read from the site's public requisition
  endpoint (`recruitingCEJobRequisitionDetails`, finder `ById` with the site
  number), one GET under the same bound as a page, its description,
  responsibilities and qualifications joined (`oracleRequisitionText`). The
  page itself is a script shell whose only text is the site's name (before,
  "JPMC Candidate Experience page" or "Honeywell" was stored as the
  description); a requisition the endpoint does not list leaves the row
  without one. Workday postings need nothing of their own: the server-rendered
  page carries the posting as JSON-LD. The Workday rows of the 2026-10-17 QA
  scan had no description only because every tenant served the maintenance
  page that morning, which the title check refuses (71 of 72 rows); a re-read
  of the same links that afternoon gave the full text. Doing this at scan time
  is the long-term choice: it costs one bounded request per surviving row, the
  same as any other row, and the rank and the evaluation queue read the text
  before any evaluation opens a browser.
- A location cell with several places (`<details><summary>4 locations</summary>
  A</br>B</details>`) becomes `A; B; ...`, the first place first; repeats and a
  place restated in fuller form in the same cell collapse
  (`cleanLocations`, shared with a16z).

## a16z public board (2026-10-15)

The current `jobs.a16z.com` deployment has replaced its Getro API. The source
reads the public page's Next Flight `initialData`, then discovers the current
`loadMorePublicJobs` action id in same-origin deployment scripts for pagination.
It does not pin a deployment hash or retry the removed `/api-boards/search-jobs`.
Each request has a ten-second deadline; scans read at most five 25-row pages,
or the caller's smaller page/result bounds, and deduplicate by application URL.
The employer, direct application URL, location, posted date and description are
read from the published listing state. A listing's location text restates the
same place ("San Francisco · San Francisco, California, United States"); the
restatements collapse and the places join with `; `. Missing state/action and unusable job
rows are layout errors, never a successful empty scan.

`TestA16Z_TenSecondTimeoutPerRequest` also covers dynamic action lookup,
actual request shape and duplicate rows. The opt-in live smoke, with
`APPLYSMITH_LIVE_SMOKE_SOURCES=a16z APPLYSMITH_LIVE_SMOKE_POST=1`, reads two pages/30
postings in a temporary database without models or user sessions. Source or
page failures fail the smoke test, even if earlier pages produced results.

## Scan construction and durable failures

The API constructs both manual and scheduled scans through `newScanner`.
It passes the configured managed scan window, the router's `feed-classify`
task and the sign-in callback; a browser reader alone does not establish that
these production entry points can use it. The retained API tests carry one
synthetic LinkedIn posting through each route into evaluation, a saved report
and the tracker, and verify the first manual sign-in wall raises needs-you.
The feed test covers router and ledger wiring, not live feed extraction.

`Record.Retryable` persists the typed temporary-failure classification in the
existing scan JSON, before the error becomes text. Ordinary HTTP 5xx, typed
network/deadline failures and host-offline failures can retain it. All failed
pages of one query are joined, and all failed queries retain whether any cause
was temporary; a later permanent failure cannot erase that evidence. A
completed, blocked, needs-sign-in, cancelled or layout-failed run does not
inherit retryability from an earlier page/query. Scheduler adapters consume
this field rather than guessing from error prose.

## Effective evaluation count

Scan initialization resolves the evaluation count before recording parameters.
An explicit request `topN` (CLI `--top-n`), including zero, wins over saved
settings. Without one, `linkedin-feed` uses its `evaluate_limit`, then its
`evaluate_top_n`, then ten; other sources use `evaluate_top_n`, then fifteen.
The feed reader never changes the resolved count. The returned and stored
`evaluate_top_n` therefore name the actual enqueue bound.

Zero leaves eligible feed postings pending in the inbox and runs no posting
evaluation; it does not disable the separate, bounded `feed-classify` task.
Retained API tests cover manual and scheduled feed scans through the real model
router, lead/inbox storage, worker pool, default ManagedReader, report and tracker.
The browser extraction and PageReader boundaries are owned doubles; no real
LinkedIn session or browser-DOM acceptance is implied by these tests.

## Careers expansion completion

`leads.expand` counts every attempted careers page against its read bound,
including a failed fetch. Read, admission and persistence errors are returned
with their typed causes and per-lead context after the remaining bounded work;
successful inbox inserts are retained. The scheduled adapter classifies typed
HTTP temporary/rate-limit failures for the existing bounded network retries.
A failed expansion is a failed run, even when another page succeeded.

A page becomes settled only after its accepted postings reach the inbox. Its
ledger entry and lead state/expanded text commit in one transaction. A failed
admission or either final write leaves completion unset so a later pass can
retry; already saved postings are deduplicated. The final pending count comes
from the durable unsettled careers leads, including failed and unread work.
Expansion itself leaves new inbox rows pending; inbox promotion starts posting
evaluation. The retained API fixture tests failure, recovery on an actual timed
tick, promotion to saved report/tracker and repeat without duplicate entries,
using an owned HTTP transport, temporary SQLite trigger and fake model.

Forced rereads take priority over the ordinary backlog and use the same bound;
unfinished forced requests remain in that run's pending count even if an older
ledger entry exists. Final accounting runs on every exit with a ten-second
context independent of caller cancellation, so committed inserts remain
visible after cancellation. Board errors at the direct, redirect and linked
rungs retain their typed causes. When linked boards partly succeed, their
postings reach the inbox while the incomplete page stays retryable.
