# Evaluation

`internal/evaluate` decides what a posting is worth to one job seeker. The model supplies evidence (hit levels per requirement, prose); Go computes every number. Spec: `openspec/changes/migrate-to-go-mygo/specs/job-evaluation/spec.md`.

## Flow

A request (CLI, pool, scan) becomes a job, which is a `runs` row of kind `evaluate` plus an `evaluation_jobs` row holding the outcome. Phases: reading (duplicate check, read, clean), gating (pure rules, no model), screening (local prechecks, then `quick-screen`), evaluating (`deep-eval`, optional answers call at 4.5 and above), writing (report files, index row and tracker upsert). A quick screen's `review` goes on to evaluating in the same job, in every mode, unless `Deps.CapReached` (the router's `EvaluationHeld`) says the day's model spend is at the daily cap or an evaluation task's spend at its budget; then the job ends `review_required` with `CapHeldReview` as its explanation, which the board's sentence repeats (F-8).

Packages: `internal/gates` (pure text predicates: sponsorship stance, US person, compensation, years, title seniority), `internal/prompts` (embedded templates, workspace overrides, tag escaping), `internal/evaluate` (gate, tier, scoring, report, pool). The clearance predicate is `discovery.ClearanceRequired`, so a scan and the gate agree.

## Facts worth knowing

- A request cannot set the verification line: pasted text is `pasted unverified`, a page the system read carries the reader's value. The HTML never embeds images; a markdown image becomes a link.
- Posting text is cleaned (`CleanText`) before any rule reads it: markup, navigation, footer, and everything from a "similar jobs" heading on are dropped. Prompt-control tags are escaped (`prompts.Escape`) when text enters a prompt.
- A report is published by `store.PublishArtifactTx`: one transaction allocates the number (the `report_artifacts` counter), renders the markdown and HTML with it, stages both files (flushed), links them without replacing anything, inserts the artifact row, the `report_index` row and the tracker upsert, and commits. On any failure the transaction rolls back (the number is not consumed) and the files this call created are removed. A journal under `.applysmith/publish/` is written before linking; `RecoverArtifacts` uses it after a crash to delete orphan files whose row never committed. Numbers are never reused once committed.
- Startup: `store.ReconcileStartup` ends `evaluate` runs `failed/interrupted` (other kinds stay `review_required`); `Evaluator.RecoverInterrupted` then returns their inbox rows to pending with the attempt spent. Nothing is re-dispatched. A shutdown (`Pool.Close`) charges the job's rows, joined ones included, nothing (`discovery.ChargeNone`); every read the app's own machine stopped (`environmentFault`: no browser reader, a browser that did not answer, `browser.EnvironmentFault`) and every read the site throttled (`throttled`) is `ChargeUndecided`, which `Repo.InboxEvalFailed` leaves unspent for a row's first `freeFailures` (3, counted as `unspent-failures:N` in its stored reasons, which a row read leaves out) and spends after (flow round 9, E-1; flow round 10, EF-2, EF-3). A throttle is `discovery.ThrottleStatus` (429, LinkedIn's 999, also when `PageRead.Throttled` met it before a script moved the page) or `RateLimitPage`; `nextRead` ends its words with a later inbox run when `Inbox.Release` says the row waits for one.
- Tests use `llm.Fake` with `evaluate.FakeGenerator` (hash-derived, schema-valid replies) or scripted fixtures. The product router installs the same generator (`evaluate.NewFake`), so the setup's offline choice can report; a report made that way carries the fake-provider note. The fixtures name a posting only as their prompt does (the request's or the extraction's title, company and place, else a `Title:`, `Company:` or `Location:` line of the text) and invent none: a posting that names no employer is filed as `Unknown company`, which `internal/identity` (like `Unknown role`) reads as a placeholder that matches nothing, so two such postings stay two tracker rows. Golden reports live in `internal/evaluate/testdata`; accept changes with `go test ./internal/evaluate -update`.
- URL-only evaluation uses `ManagedReader`: supported ATS APIs first (a Lever posting's text includes its requirement and duty lists, one "- item" line each; a Greenhouse location name that is a machine slug, Hover's `san_francisconew_york`, or a site name with no comma and no remote work, BCC's `NIH-Bethesda`, is replaced by the offices' addresses when every office has one ("Bethesda, Maryland, United States"), a slug with none by the office names, so the apply chain's country guard reads a place; scans read Greenhouse listings the same way, and a row stored before keeps the name it had), otherwise the managed scan browser. Browser liveness decides live versus unverified; closed, challenge, login, failed and too-short pages fail with an explanation before the model is called. A row a scan queued, the user promoted or asked to evaluate by its id (CLI row id, API `inboxRowId`, through `Request.FillFromInbox`) carries the description the scan stored (`Request.Scanned`); it is used, as `scan unverified`, only when the page read fails without showing the posting gone (`ErrPageUnsure`: a wall, a throttle, a page that never rendered). Glassdoor answered 429 to the evaluator right after a scan read 15 of its detail pages, and both evaluations failed with the text in hand. An Indeed row evaluated by its id met a Cloudflare challenge and failed with 3,951 characters of scanned text stored, because the row's description was not passed on. A closed page still fails. A browser read that failed or showed nothing yet is read once more, its text is the posting's description where the page marks one, a job board's listing is read through the employer's page its apply link names (nothing is pressed), and a read that ends on another posting (a tracking link's landing page, by its canonical link; the employer's page) makes that address the posting's URL and identity key, with the requested key handed to the tracker as an alias (`PageRead.URL`, `jobRun.adopt`, flow round 4).
- The posting's date: a row's `posted_at` (what its source gave) goes with the request (`discovery.EvalRequest.PostedAt` from a scan, a promotion, the inbox drain or a lead; `Request.FillFromInbox` for an evaluation asked by row id), and the deterministic legitimacy signal "listing is over 60 days old" reads it. A posting with no recorded date, a pasted one included, has no age and never raises it.
- Title, company and place: the request's own (an inbox row passes its role, company and place, from a scan, a promotion or `POST /api/evaluations`) win over what the page read gives, and `jd-extract` fills what is still missing. The company differs: the employer name an ATS's own data states (Greenhouse `company_name`, SmartRecruiters `company.name`; Lever's and Ashby's APIs state none) comes first, then the request's. A request company that is only the board's slug made readable (an Exa or registry row: "Resolveai", "Sonyinteractiveentertainmentglobal" for PlayStation Global, "F7d8749a 72d6 …" for a Shield AI board whose Ashby slug is a UUID) is a guess: `jd-extract` names the employer from the text, and the guess is kept only when it names none (`slugName`). Once the company is settled, a role that names it as a tail ("Enterprise Sales Engineer @ Assembled", which the Exa source left on because the slug reads "Assembledhq") loses the tail (`identity.RoleWithoutCompany`). A page title that is the company (a careers site that heads every posting with the employer's name, such as an Avature tenant) is no role: the extraction's title replaces it, and when that is the company too the report and tracker row say "Unknown role". A text under 200 characters (`CheckText` is the rule for a caller that holds the text; `applysmith evaluate --text` rejects it with exit 2 before a job exists) and a job with no routed model fail with an explanation that says what to do, which the dashboard note, the API view, the extension and the command line show in place of the code; the command exits 3 for no model. A scan used to queue only the address, which filed one such posting as "Lenovo / Lenovo"; rows filed that way keep their role until evaluated again.
- Sponsorship is read only from the posting's own words (`gates.Sponsorship`), set against the profile's need. The USCIS H-1B petition table, its `applysmith data fetch h1b` download and the sponsor card in prompts and reports were removed (owner decision): whether this role sponsors is what its posting says, not the employer's filing history. Migration 0053 drops the table.
- Compensation ranges retain their currency from extraction through scoring. The deterministic dollar parser labels its ranges USD; the structured model fallback retains the explicitly extracted currency. Annualized amounts are compared only in the same currency (profile currency unset means USD), with no conversion; a different or missing range currency is neutral. Pay text names the range and target currencies so a neutral mismatch is visible. CAD/EUR/GBP year/month/hour report-to-tracker regressions guard the former non-USD-always-neutral defect.
- A dollar range whose annualized top is under the US federal minimum wage for a full-time year (`gates.LowestPay`, 7.25 × 2,080) is a placeholder, not pay: a GitLab posting's "$1—$2 USD" was read as 1,000 to 2,000 a year, scored compensation 1 and could block at the salary precheck. Both the text parser and the model's extracted figures drop such a range (`Range.Placeholder`), so the pay is unstated: a neutral 3, no salary blocker.
- The employer roster (`roster.tsv`) is authored from public knowledge of staffing, consulting and aggregator firms.

## Score stability (2026-10-17)

The same Mercury posting scored 1.9 to 3.4 across live `deepseek-v4-pro`
runs (QA finding F18). Causes, measured on saved scorecards: every reply
listed the seeker's skill gaps again as red flags, so the penalty sat at its
1.5 ceiling and its size followed the model's level choice (0.75 to 1.5); the
alignment list held five or six free-form items, compensation among them,
which the pay score already counts; one bullet became one to three items, so
the must-have miss caps (3.7 for one, 2.7 for two) flipped; and "knowledge of
or excitement to learn Haskell" was scored as a missed must-have. Fixes, all
in `deep-eval.tmpl` and `Check`: red flags describe the posting, never a gap;
alignment is exactly `seniority`, `domain`, `location`, `role` (schema enum,
first of each kept in that order); one item per bullet; a learn-able
requirement is a nice-to-have; hit levels have anchors (years at or above the
minimum are met, a soft skill needs an example); the profile block carries
the target locations, work modes and limits, citable as `targets:`. The
deterministic inputs (knowledge search, signals, profile view) were already
order-stable. DeepSeek reasons on deep-eval, where temperature has no effect,
so stability comes from the rubric. `evaluate --file` of the same text three
times: Mercury 2.2, 2.4, 2.8 before, 3.7, 3.7, 3.7 after; 1Password 2.1,
2.3, 2.2 before, 3.1, 3.2, 3.2 after.

## Report decision content (2026-10-17)

Compared block by block with the previous product's A to H reports (3,100 to
5,000 words, read by a separate reviewer so this clean-room area received
behaviour only), the Go report lacked the parts that carry a decision: why
the score is what it is, gap triage, level strategy and pay anchors. Added,
all grounded in the posting, the profile and the computed card, with no web
or market data: deciding factors in the Verdict (computed: misses, partial
must-haves, red flags, a binding cap, the distance to the next band), role
facts in the Snapshot (from the posting fields; its pay line is the range the
Pay section scores when the extraction left pay empty, so the report's pay
chip and its Pay section agree), a `Gaps` section (model
triage per must-have below 1.0; kind eligibility only for an honored gate,
nearest evidence without a profile source shown as none), the profile's
years and targets ahead of the level prose (the years are the time the
employment history covers, overlapping jobs once and gaps not at all; the
span from an internship to a job begun after school read as 3 years, so no
"3+ years" must-have was an experience gate), pay anchors computed from the
profile (target, walk-away minimum, range against target), and a case-study
pick in Interview prep. Not ported: market pay tables and demand trends
(unsourced even in the old reports), repost or layoff rows, LinkedIn edits,
a second requirement table. Mercury on the same text: 1,142 prose words
before, 1,523 after (2,181 with the scorecard); 1Password 1,702 to 2,413.

A positive model claim without a named profile source is checked to zero.
Its visible Fit row says "No profile source cited; claimed match needs review"
so the original positive prose cannot be mistaken for a verified match or an
observed skill gap. The scorecard keeps the original evidence, checked hit and
`unsupported_hits` id; this display annotation changes no score or gap state.

## Serving

`Pool.SubmitKeyed` is `Submit` under an `Idempotency-Key` (`Claim{Principal, Key}`): the claim, the run and its `evaluation_jobs` row commit in one transaction (nothing after the claim can fail, so a replayed key always names a run that executes), a repeat returns the original run (`Handle.Replayed`) and queues nothing, and a request that joins an active job binds the key to that job (`store.RunRequest.Attach`). A job takes joins only until its pipeline ends: it leaves the pool's in-flight set before its outcome is recorded, so a request made once the run reads finished ("Evaluate again" on a failure) is a new job, never a join of the one that ended (`TestPool_ARequestAfterTheOutcomeIsRecordedStartsANewJob`). `ListReports`, `FindReport` and `ReportRef.Markdown/HTML` are the report catalog behind `GET /api/reports*`; a path from the index is read only when it is a plain file directly under `reports/`. Routes and decisions: [api.md](api.md).

## Skill gaps and learning plans (`gaps*.go`, migration `0032_gaps.sql`)

An optional analysis over 1 to 100 final reports, started by `applysmith evaluate gaps --report <n>[,<n>...]` or `POST /api/evaluations/gaps`, shown in a Skill gaps section of the Reports page. It is a run of kind `gap-report` under the idempotency contract, and its result is a `gap_reports` row, not a posting report: no `report_index` row, no score kind, no band.

- **Evidence.** It reads the `scorecard` block of each report's Fit section (the model's items after the two deterministic checks). Must-haves are `required`, nice-to-haves `preferred`; hidden signals are the inferred list, shown apart and never counted. A met requirement (hit 1) is no gap. No model is called: the requirement extraction was `deep-eval` when the report was written.
- **Sample.** Reports that are not final, are missing or unreadable are listed as excluded with a reason. The rest are folded to one report per opening, the newest: equal identity keys, keys that resolve to one tracker row (`tracker_entries`, `tracker_aliases`), or `identity.SameOpening`. Each analyzed report is recorded with its creation time and a SHA-256 of its Markdown, and the analysis with its generation time.
- **States.** `not-evidenced` (no profile evidence; a missing CV line proves nothing), `needs-review` (a partial hit, or a claimed hit that named no profile source), `confirmed-gap` (the user pressed "I lack this"; a `gap_confirmations` row keyed by the folded label, so it holds for every analysis). States are recomputed from the stored sources when an analysis is read.
- **Order.** Distinct postings that require the skill, then distinct postings that prefer it, then the folded label. A rejected application adds no weight: tracker status is never read.
- **Plan.** `POST .../plan` takes 1 to 5 confirmed gaps; an unconfirmed gap is refused (`validation_failed`). Each item has an exercise, an observable criterion and an effort labeled "Estimate". Resources come only from a `ResourceFinder` (the research route), only on request, at most two per gap and only when verified. Production uses the configured `research-search` route through `api.gapResources`, then fetches cited HTTP(S) pages through the guarded fetcher. Missing configuration is `unavailable`; failed searches or unreachable pages are `failed`. The whole lookup shares a 20-second deadline, and the plan is saved with the original request context. `api.DomainOptions.GapFinder` allows an explicit finder in tests. The optional `narrative` wording is not used.
- **Progress.** Marking an exercise complete writes `gap_progress` only. Nothing reaches the profile, the CV, the answer bank, the knowledge base or any fit weight; the tests compare every other table and the profile file before and after.

## Posting decode (`posting.go`)

`Evaluator.Decode` reads one posting without evaluating it: template `jd-decode`, task `jd-decode`, no job row, no report, no tracker row and no score. The reply has five layers (needs, must-haves, nice-to-haves, hidden signals, level and team stage) plus wording readings and red flags. Requirements and hidden signals are the evaluation's `Item` shape, so the same `Check` runs on them: a hit without a profile source becomes 0 (`unsupported_hits`), and only a missed must-have whose gate class the requirement triggers is a deal-breaker. A need or item whose quoted source is not in the posting (compared over folded letters and digits) is listed under `unquoted` as an inference. Text under 200 characters is refused before any model call. `Decoding.Markdown` renders the layers and the match matrix. The fake provider answers with a fixture that quotes the posting's first long line. Entry points: `applysmith evaluate decode` and the Ask Agent tool `decode_posting`. `ReportMatches` reads a final report's scorecard and returns the requirements met at least in part, with evidence; the message drafts use it ([Message drafts](#message-drafts)).

One path runs queued work: `Pool`. The server owns one; a command line that scans or promotes (`applysmith scan`, `applysmith inbox promote`) starts its own over the workspace it has locked, queues through `PoolEnqueuer`, and `Drain`s before it exits. No other process can see those runs, so nothing adopts runs from the table. At start `RecoverInterrupted` ends non-terminal evaluation runs as `interrupted` and returns every still-`queued` inbox row to `pending`; a job's outcome reaches the scan run that queued it (`Request.ScanRun`, `Deps.ScanOutcome`) when it ends.

## Message drafts

`internal/messages` drafts the two short messages a user sends by hand: a
LinkedIn outreach note and a follow-up on an application in flight. Nothing is
sent, stored or recorded; the requirement is "Outreach and follow-up message
drafts" in `specs/application-documents`.

| Piece | Where | What |
|---|---|---|
| Templates | `internal/prompts/templates/outreach.tmpl`, `followup.tmpl` | rendered with `prompts.Set`, so a workspace override in `prompts/` replaces them and `applysmith prompts check` validates it |
| Facts | `messages.go` (`factBook`) | the numbered source lines the model may cite |
| Draft and checks | `messages.go` | one `message-draft` call, the checks, the checklist |
| Text | `messages.go` | `Draft.Text` for the CLI and Ask Agent |
| Entry points | `internal/cli/followup.go`, `internal/agent/tools_prompts.go` | `applysmith outreach`, `applysmith followup-draft`, tools `draft_outreach` and `draft_followup`, the Tracker's `POST /api/candidacies/{id}/followup-draft` and `outreach-draft` |

### Facts

Ids by source: `T` the tracker row (applied date, status since, recorded
follow-ups), `R` the final report's requirements met at least in part with their
evidence (`evaluate.ReportMatches`; the level, pay and negotiation sections never
reach a message), `F` the CV lines of four or more words and the profile's
employment, education, skills, target locations and work modes, `U` what the
user typed about the contact or the last exchange. A CV line with a phone
number, an email address or a link is skipped, and the profile's identity,
contact and work-authorization values are never facts. `voice.md` is style, not
a fact. Every text entering the prompt goes through `prompts.Escape`, which also
neutralizes the templates' own tags (`role`, `contact`, `facts`, `voice`,
`prior_exchange`).

### Checks

Each message (the primary and each alternative) is removed with a reason when it
is empty, longer than 300 characters (outreach, the connection-request limit) or
600 (follow-up), holds a phone number (nine or more digits, so a date passes),
an email address or a link, uses a stock phrase ("passionate about",
"results-oriented", "circling back", "just checking in", "touching base"), or
states a number that no fact and no user input states. A failed primary is
replaced by the first passing alternative; with none left the draft is refused
(`ErrNoDraft`). The model lists its claims with fact ids: a claim whose every id
was given is "rests on your own material", anything else and every declared
assumption is "check before sending".

### Follow-up kind and cadence

Without `--kind`: `applied` gives `recruiter_ping`, `responded` gives
`status_check`, `interview` gives `thank_you`; `offer` needs the kind named, and
other statuses are refused. A recruiter ping on an `applied` row reads
`tracker.Followups`: the draft notes when the next follow-up is or was due, and
a used-up cadence is refused (`ErrCadence`, CLI exit 3) unless the user named
`recruiter_ping`. Recording the sent message stays `applysmith tracker followup
<id>`.

## URL ingestion completion (2026-10-15)

Goal: the authenticated evaluations API accepts a URL without pasted text,
reads the actual posting, runs the configured evaluation model and exposes a
structured outcome. The same reader construction serves CLI evaluation.
`POST /api/evaluations` remains asynchronous and idempotent: return 202 and a
run ID, poll `GET /api/evaluations/{id}` for the outcome, score,
recommendation and report number, then read `GET /api/reports/{number}`.
Supported ATS APIs avoid Chrome; other hosts use the managed scan reader.
An ATS API that answers 404 does not know the board, which says nothing about
the posting (Lime turned its Ashby posting API off while its posting pages
stay live), so the page is read and decides. So is a posting an Ashby board
no longer lists or SmartRecruiters marks inactive: the page shows it gone
(closed, its inbox row expired) or still open and unlisted (read). Reading
neither as a failure kept three gone Ashby postings failing on every scan.
Ashby answers a posting only with its whole board, so the evaluation reader
of the server and of the CLI keeps each fetched board for two minutes
(`evaluate.BoardCache`): the evaluations of one board's postings in a batch
share one fetch, concurrent reads wait for the one in flight, and a failed
fetch is not kept.
Closed, login, challenge, failed and too-short pages must not become live
reports. A readable page without an apply control remains unverified.

Implementation slices: URL reader and API result; mirror search dependencies;
bounded PDF-to-draft import; opt-in semantic retrieval and curated evidence;
a16z's current public search contract. Verify each slice with default API/CLI
construction tests and local service fixtures; validate a16z with a bounded
public scan. No employer submit, account creation, private-data migration or
paid acceptance call is part of this work. Existing user files are preserved.
Public source contracts may have changed; inspect the real source before
changing its parser. Semantic retrieval requires an explicit user setting and
a configured embedding route; provider errors preserve lexical retrieval.

### Calling the URL evaluation API

Send `POST /api/evaluations` with the workspace bearer token,
`Content-Type: application/json` and a unique `Idempotency-Key`, for example:

```json
{"url":"https://employer.example/careers/software-engineer","mode":"auto"}
```

The 202 response contains `runId`. Poll `GET /api/evaluations/{runId}` once
per second until `status` is terminal; a completed report exposes `score`,
`recommendation` and `reportNumber`. Retrieve its full analysis through
`GET /api/reports/{reportNumber}`. Keep the same idempotency key on a transport
retry; changing the request with that key is a conflict. An unreadable posting
returns a failed run with its reason and no report;
`POST /api/evaluations/{id}/retry` starts a failed or cancelled run again with
its stored request. Public ATS reads need no
browser; other pages require the managed browser and a configured model route.

### Acceptance evidence

- Default API and CLI construction tests cover URL-only reads, score/result
  retrieval, closed pages, login/challenge walls and insufficient text.
- `TestURLOnlyEvaluationRenderedBrowser` (internal/launch, run by `make test-browser`
  with `APPLYSMITH_TEST_EVALUATION_BROWSER=1`) passed against
  a local JavaScript-rendered posting and a temporary headless Chrome. The URL
  was the only evaluation input; scoring/report creation used offline fixture
  models. Real closed, login and challenge pages produced failed runs without
  reports. The test polls within the API's existing request limit.
- Poppler extracted a real synthetic PDF; API tests cover missing Poppler,
  malformed/oversized uploads and draft visibility before/after publishing.
- Knowledge opt-in/out and approved-only corrections are tested through the
  default server assembly; a model capture verifies published evidence reaches
  the evaluation prompt.
- a16z's live two-page scan returned 30 postings, with 11 inserted and 19
  filtered in a fresh temporary database, with zero board errors. Counts are a
  dated observation, not a promise about future source contents.
- Full dashboard tests: 75 files, 944 cases passed; TypeScript check passed.
  Go vet, dashboard command build and repository guards passed. The optional
  private PII/retired-name guard inputs were absent, so those two scans skipped.
- The full Go suite retains an existing failure in
  `TestSetupDocsOfTheRepositoryMatchTheCatalogAndTheCLI`: the Gmail guide's
  expanded `Before you start` heading does not match the catalog's exact
  heading. Both the guide and test predate this change; no Gmail configuration
  or documentation is changed here.

The fixture/browser/source acceptance above used no paid provider calls,
employer submissions, saved credentials or personal workspace contents.

### Real-model acceptance (2026-10-15)

An independent workspace at `/private/tmp/applysmith-live-model-20261015` used
the signed-in Codex CLI preset, never a fake provider. The saved desktop
workspace points to an unconfigured temporary workspace, so its selection and
settings were left intact. The test persona is explicitly synthetic and the
score must not be read as an assessment of the owner.

The production API received only the URL of Ashby's Senior Software Engineer,
Product Engineering — UK (`472eef28-6e52-43b4-9bff-9113522890f5`) plus `mode: deep`.
Run `run_d15cd03f5c388c7c57821082` succeeded with report 1, score **2.4 / 5**, and
recommendation `skip`. `GET /api/reports/1` returned the saved analysis; the
Markdown and HTML reports were created. The ledger records one successful
`deep-eval` call, provider `codex`, kind `cli`, model `default` (the CLI's model
selection), 67,373 ms, subscription billing. The CLI token counts are estimates;
zero ledger API cost does not assert that the subscription is free.

The Knowledge import/publish API created `acceptance-product-evidence` before
evaluation. Search returned `grounded`, with that snippet as a lexical hit;
the captured real provider prompt contains its `Acceptance Knowledge Canary`
text. Debug capture was then turned off. Codex CLI has no embeddings API, so
this proves real-model evaluation with lexical retrieval, not semantic provider
acceptance. Raw run/report data are in `acceptance-result.json`, search data in
`acceptance-knowledge.json`, and the call/capture rows in the test database.
No employer submission or personal-data write was part of this test.

### Existing-owner production acceptance (2026-10-15)

The default `Applysmith` workspace was created from the owner's existing CV,
profile and narrative, keeping the old project and the fictional demo intact.
The requested `.env` copy is byte-identical and mode 0600 in the Go checkout;
it is ignored by Git. Copying it alone does not configure this runtime: the
recognized provider/search/solver/bot secrets were imported through the CLI
into the workspace's OS keychain. No bot, schedule or application submission
was started. Original environment switches without a Go equivalent remain
inactive. Acceptance kept the stored desktop workspace selection intact; after
verification the setup workspace action records the configured default
workspace, keeping a backup of the previous temporary-workspace choice.

The original evaluation routes resolve to `codex/gpt-6-luna`, with the
configured API alternatives. The Agent keeps the original OpenRouter route
and DeepSeek alternative; its old CLI alternative is excluded because it
cannot supply native tool calls. The explicit embedding route is
`openrouter/openai/text-embedding-3-small`; configuring a route does not enable
semantic retrieval or transmit documents. All 26 task routes resolve with
no broken or unrouted entries.

The Home language-model component now reads the local routing table during
the existing component refresh (startup and every five minutes), instead of
always claiming "not set up". Its detail describes configured routes and
explicitly says provider availability was not probed. Missing/broken routes
and offline simulation are degraded; an optional unrouted embedding task alone
does not degrade an otherwise configured runtime. Health initiates no remote
model call. `TestLLMComponentReflectsConfiguredRoutingWithoutProviderCalls`
and the cheap, side-effect-free health test cover this boundary.

Thirty-seven published Knowledge files were copied; archived material,
drafts and conversation databases were excluded. Twenty files used object
triggers. Their copied metadata was converted to the current string rule
format, preserving the original bodies and allowed facts. All 37 then
indexed, and the local search API returned `grounded` with four facts.
The private copy/hash manifest and run data are kept separately from source.

An initial UK senior posting correctly stopped at the owner profile's
seniority, geography and currency gates, producing a screening report and
zero model calls. A second URL-only request used Zip's Software Engineer,
Backend (All Levels), San Francisco posting at
`https://jobs.ashbyhq.com/zip/e7bcd788-ca5f-4e90-9728-0de7b57026a1`.
Run `run_80002f9b5bcfa7e24fceb98d` finished `succeeded` / `reported`, score
**3.3 / 5**, recommendation `skip`, report 2. Both saved Markdown and HTML and
the report API contain the result. The ledger records a real successful
`deep-eval` call to `codex/gpt-6-luna`, 37,006 ms, subscription billing.
Four published Knowledge IDs occur in the captured actual model prompt.
Debug capture was turned off and the acceptance server stopped afterward.
This verifies the owner's real-model URL, lexical retrieval and report path.
No employer submission was performed.

The owner then explicitly authorized sending published Knowledge text to
OpenRouter/OpenAI for embeddings. Semantic retrieval was enabled through the
settings API. Real `openai/text-embedding-3-small` calls succeeded and cached
103 sections, each 1,536 dimensions. A Chinese question about splitting,
coalescing and aligning heap blocks returned no lexical facts; with semantic
retrieval enabled it returned grounded facts, led by the published dynamic
memory-manager section, with provenance and allowed facts intact. The same
test returned four semantic facts. A more ambiguous circuit-breaker question
was refused because the leading candidate did not stand out. This proves the
real embedding and semantic retrieval path, not general retrieval precision.
The authorized setting remains enabled; the private `semantic-evidence.json`
records results, vectors and successful provider ledger rows.
After restarting into the packaged desktop, the same semantic question still
returned the expected leading source. The embedding ledger stayed at four
calls before and after that search, proving the persisted vectors and query
cache were reused. The desktop resolved the recorded default workspace and
all 26 routes without environment overrides.

Private acceptance files live under `/private/tmp/applysmith-production-20261015`:
`acceptance-result.json`, `model-evidence.json`, and `acceptance.log`.
Reports belong to the owner's workspace, not the source tree.

The final standard `make verify` passed: Go vet, all 74 tested Go packages,
repository guards, dashboard 75 files / 944 tests, engine 85 files / 1,075
tests, extension 39 files / 470 tests, and all three TypeScript checks.
The exact Gmail guide heading now matches the setup catalog. The guards'
optional private token/retired-name input scans remained skipped because
those inputs were absent; that is not a completed personal-data audit.
After the desktop-port and model-health corrections, the affected API and
desktop packages passed again, as did their Go vet checks and the repository
guards. Desktop race tests also passed. The Telegram status fixture now
expects degraded model health while explicitly running offline simulation.

### Learning-plan qualification limits

A gap can describe work experience, a credential or availability as well as a
technical skill. The default plan asks the user to scope a relevant project,
scenario walkthrough or written analysis; it does not assume every requirement
is software. The displayed effort estimates scoping only. Practice time and the
time needed to satisfy the actual qualification remain unknown until reviewed.
Completing an exercise records study progress and does not establish experience,
credentials, availability or proficiency, or close the confirmed qualification
gap. Real resource lookup verifies that cited pages are reachable; it does not
certify their quality or that studying them satisfies the posting.
