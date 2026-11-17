# Performance measurements

What was measured about the packaged app, the server and the test suites, how,
and how it compares with the frozen old system (task 7.4a). The tables between
the markers below are written by `make measure` from
[performance.json](performance.json); do not edit them by hand.

**Rule.** No page, release note or message states a performance property of
Applysmith (fast, light, small, reliable) without a row here that measures it,
the tool versions, the number of runs and the load of the machine. A comparison
with the old system appears only where the old repository recorded the same
quantity for itself, and says "no comparable baseline" otherwise.

## Running it

    make measure

builds the app and disk image (`make package`) and the command line
(`make build`), then runs `cmd/measure`, which writes
`docs/architecture/performance.json` and rewrites the block below. It takes
roughly an hour: 2 targets times 14 launches, then the unit suite and the
browser-tagged suite three times each. Options (`go run ./cmd/measure -h`):
`-samples` (launches per case, 7), `-runs` (suite runs, 3), `-max-load` (the
one-minute load average at or below which a sample counts as quiet, default the
core count), `-load-wait` (the longest wait for the load to fall, 5 minutes),
`-only` (`sizes,startup,suites`).

Everything runs on synthetic data under a temporary directory: a new workspace
and a temporary `HOME` for each launch, the API token from the environment
(no keychain), `APPLYSMITH_PORT=0` on loopback, and the fixture boards and mock
sites that the test suites already serve. Nothing reads the real workspace, the
real keychain, the user's Chrome or a real job site. Every process the run
starts is stopped by its PID and, at the end, checked for leftovers.

## What each number means

- **Startup to a served dashboard.** Time from `exec` until the lock file names
  the process, `GET /api/health` answers `ok`, `GET /` without a code answers
  the sessionless landing page (401) and `GET /index.html`, which needs no
  session, comes back with the dashboard's root element. The one-time launch
  code in the lock document is never redeemed: the app's own window needs it, so
  redeeming it would leave the window on the landing page. The app is started
  with the login-item switch (`--mygo-opened-at-login`), which opens its window
  hidden, so no window appears; a menu-bar item does. *Cold* is a fresh copy of
  the bundle (or binary) and a new, empty workspace, so the first migration and
  the answer-bank seed are in it; *warm* is the same copy and workspace launched
  again after one unmeasured launch. The operating system's file cache cannot be
  purged without administrator rights, so a reboot-cold start is not measured.
  "Served" is the server being ready; it does not include the window painting
  the page.
- **Process-tree memory.** Summed `ps` RSS of the launched process and all of
  its descendants, read 4 seconds after the dashboard was served. The app's
  WebKit helpers (GPU, networking, page content) are children of launchd and so
  are not in the tree; they are found as WebKit helpers that appeared after the
  launch and are accountable to the same process as the app (the system's
  `responsibility_get_pid_responsible_for_pid`), keeping only those that exited
  with the app. Memory shared between processes is counted in each, so every
  sum is an upper bound. The server alone (`applysmith serve`) has no helpers.
- **Chrome memory.** The product starts the apply and scan Chrome only when a
  run needs it, so an idle app has none. Two figures, both the largest sum of
  all Chrome processes below the `go test` process at any one-second sample,
  both headless with throwaway profiles on fixture pages: one Chrome holding an
  apply-lane tab and a scan-lane tab for 10 seconds
  (`TestMeasure_LaneChromeHeld`), and the whole browser-tagged suite, which runs
  up to three mock-board scenarios at once. They are ceilings for fixture
  pages, not the cost of a real session on real sites, and a headed Chrome costs
  more.
- **Scan and fill completion and failure.** The workloads are the existing
  scenarios that serve fixture boards and mock applicant-tracking boards on
  loopback, selected by name and pinned in the JSON (`workloads[].tests`). A
  scenario *completes* when the assertions of its own test pass (for the fill
  matrix: the form filled without a press, one press, one application recorded
  by the board, a confirmed ledger row, a second run refused as a duplicate).
  Rates are over scenario executions across all suite runs, skipped scenarios
  excluded. They are regression pass rates of synthetic scenarios, not field
  success rates, and the scenario times include queueing behind the parallel
  slots of the suite.
- **Artifact sizes.** Sum of file bytes of the bundle, the size of the disk
  image, the unstripped `applysmith` command built by `make build`, and the
  generated bundles that are embedded in both.
- **Test-suite durations.** Wall clock of `go test -tags dashboard ./...` and of
  the `make test-browser` package set, run under `go test -json`, which is also
  where the pass, fail and skip counts come from.
- **Load.** The one-minute load average is read before every sample. A sample
  waits until it is at or below the limit, up to the longest wait, and is taken
  and counted as "above the load limit" if it never falls. The suites load the
  machine themselves, so the load seen during a suite run is reported as a
  maximum and only the load before the run is gated.

<!-- measure:begin -->

Measured at commit `5560993ae` plus uncommitted changes on Apple M3 (8 cores, 16 GB), macOS 26.2 (25C56), Darwin 25.2.0.

| Tool | Version |
|---|---|
| Go | go1.27.1 darwin/arm64 |
| bun | 1.3.5 |
| Chrome | Google Chrome 155.0.8059.40 |
| mygo | mygo 0.4.1 |

One-minute load average at the start and end of the run: 12.5 and 4.3 (limit for a quiet sample: 8.0, 8 cores).

### Startup to a served dashboard

Time from process start until the lock names the process, `/api/health` answers `ok`, the document address answers the sessionless landing page (401) and the dashboard's index document comes back with its root element. The one-time launch code is never redeemed, because the app's own window needs it. The app is started with the login-item switch, so its window stays hidden. 7 samples per case; median and 90th percentile (nearest rank) in milliseconds. Cold is a fresh copy of the artifact and a new workspace, warm is the same workspace and copy launched again.

| Target | Cold median | Cold p90 | Warm median | Warm p90 | Samples above the load limit |
|---|---|---|---|---|---|
| Applysmith.app | 770 | 1492 | 216 | 251 | 1 of 14 |
| applysmith serve | 765 | 1145 | 77 | 87 | 0 of 14 |

### Resident memory of the whole process tree

Summed `ps` RSS of the process and everything below it, read 4 s after the dashboard was served, in MB. The app's WebKit helpers (GPU, networking, page content) are children of launchd, so they are found by being new since the launch, accountable to the same process, and gone after the app quits. Pages shared between processes are counted once per process, so a sum is an upper bound.

| Target | Cold median | Cold p90 | Warm median | Warm p90 | Warm median, by kind |
|---|---|---|---|---|---|
| Applysmith.app | 265 | 345 | 256 | 265 | app 157, webview 99 (4 processes) |
| applysmith serve | 69 | 71 | 60 | 61 | app 60 (1 process) |


Chrome while the whole browser-tagged suite runs (up to three mock-board scenarios at once, each in its own tabs): the largest sum was median 2935 MB, p90 3315 MB, max 3315 MB over 3 runs; the whole `go test` tree peaked at median 3151 MB, max 3524 MB.

Chrome with one apply-lane tab and one scan-lane tab open on a fixture page (headless, a throwaway profile, held 10 s): the largest sum of all Chrome processes at any one-second sample was median 1447 MB, p90 1537 MB, max 1537 MB over 3 runs.

### Synthetic scan and fill workloads

Each scenario is an existing test that serves a fixture board or a mock applicant-tracking board on loopback. A scenario completes when its own assertions pass; the executions are the scenarios times the browser-suite or unit-suite runs, skipped scenarios excluded.

| Workload | Scenarios | Executions | Completed | Failed | Skipped | Completion | Failure | Scenario time median / p90 (ms) |
|---|---|---|---|---|---|---|---|---|
| fill: mock-ATS board, dry run then submit | 5 | 15 | 15 | 0 | 0 | 100.0% | 0.0% | 19970 / 31370 |
| fill: mock-ATS board, service refusal | 5 | 15 | 15 | 0 | 0 | 100.0% | 0.0% | 22260 / 27710 |
| fill: whole mock-ATS suite | 64 | 192 | 192 | 0 | 0 | 100.0% | 0.0% | 1780 / 13100 |
| scan: fixture boards served over HTTP | 33 | 99 | 99 | 0 | 0 | 100.0% | 0.0% | 170 / 620 |
| scan: fixture boards read in headless Chrome | 12 | 36 | 36 | 0 | 0 | 100.0% | 0.0% | 725 / 2570 |


### Artifact sizes

| Artifact | Size |
|---|---|
| `Applysmith.app` (sum of file bytes) | 40.3 MB (40,277,496 bytes) |
| `Contents/MacOS/Applysmith` | 40.2 MB (40,247,328 bytes) |
| disk image | 11.5 MB (11,531,442 bytes) |
| `applysmith` command (`make build`, not stripped) | 53.6 MB (53,605,874 bytes) |
| embedded engine bundle (`internal/engine/generated`) | 0.4 MB (423,159 bytes) |
| embedded extension build (`internal/extension/generated`) | 0.8 MB (827,036 bytes) |
| embedded dashboard assets (`web/dist`) | 0.7 MB (723,077 bytes) |

### Comparison with the frozen old system

| Metric | Old system, as it recorded itself | Source at commit `c0cb9598b` | Comparable? | This system |
|---|---|---|---|---|
| Disk image | `Applysmith-1.3.0-arm64.dmg`: 160 MB (2026-07-11 record); 178 MB (2026-08-07 record) | docs/exec-plans/active/2026-07-11-operator-projection-cutover-1-apply-next.md; 2026-08-07-desktop-package-fsevents-bundle-failure.md | Yes: the same kind of artifact (the arm64 disk image of the desktop app). Built with different tools on different dates. | 11.5 MB (11,531,442 bytes) |
| Startup to a served dashboard | none. The old documents record `/v1/health` latency (250 ms before and 1.7 ms after a cache, on a real data set), watchdog boots of 25 to 343 s on a starved host, and a 60 s Chrome connect timeout, but no cold or warm time to a served dashboard on a clean workspace. | docs/exec-plans/active/2026-09-08-health-probe-startup-contention.md; docs/runbooks/perf-debugging.md | No comparable baseline. | cold 770 ms, warm 216 ms (medians, empty workspace) |
| Resident memory of the idle app | Electron alone: 363 MB idle, 1,034 MB peak during an Indeed scan, 446 MB after with no tabs, in a harness with the in-app browser. The bridge process: 94 to 275 MB on a host holding a 268 MB database. | docs/exec-plans/active/2026-09-30-in-app-browser.md; 2026-09-16-scale-bridge-data-layer.md | No comparable baseline: the old figures exclude the bridge or exclude the shell, were taken with live data and a live scan, and are not a whole-tree reading of an idle app on an empty workspace. | 256 MB idle, app and WebView helpers (median, warm) |
| Scan or apply Chrome | 479 MB after a clean headless restart; 3.6 to 4.0 GB and 7.0 GB when renderers were not reaped. The architecture document budgeted 200 to 400 MB. | docs/exec-plans/active/2026-07-27-schedule-plan-live-verification.md; 2026-07-20-linkedin-newgrad-deepseek-rerun.md | No comparable baseline: live LinkedIn and ATS sessions against fixture pages. | 1447 MB, two lane tabs on a fixture page (peak, median of 3 runs, headless) |
| Synthetic scan and fill completion | none. The old documents record live runs (for example 88 rows, a fill of 7 of 7 fields in 12.5 s on one live form), not completion rates over fixture boards. | docs/exec-plans/active/2026-07-27-schedule-plan-live-verification.md; 2026-09-30-in-app-browser.md | No comparable baseline. | see the workload table above |
| Test-suite duration | none. The full-verify wall clock was recorded as not measured. | docs/exec-plans/active/2026-07-28-bound-verify-concurrency.md | No comparable baseline. | unit 233 s, browser 1071 s, lane-chrome 12 s (median) |

### Test-suite durations

Wall-clock seconds of the whole command, 3 runs, with the largest one-minute load average seen during a run.

| Suite | Command | Fastest | Median | p90 | Slowest | Max load | Scenarios passed / failed / skipped (last run) | Failed packages |
|---|---|---|---|---|---|---|---|---|
| unit | `go test -tags dashboard ./...` | 212 | 233 | 387 | 387 | 79.2 | 5953 / 0 / 33 | 0 |
| browser | `go test -tags browser -p 1 -timeout 25m ./internal/browser/... ./internal/engine/... ./internal/extension/... ./internal/apply/... ./internal/discovery/...` | 801 | 1071 | 1127 | 1127 | 74.6 | 1093 / 0 / 10 | 0 |
| lane-chrome | `go test -tags browser -run ^TestMeasure_LaneChromeHeld$ ./internal/browser/` | 12 | 12 | 13 | 13 | 4.8 | 1 / 0 / 0 | 0 |

Runs of unit: 1: 233 s, 5953 passed, 0 failed, load 4.7 at the start and up to 30.2 during; 2: 387 s, 5953 passed, 0 failed, load 7.9 at the start and up to 79.2 during; 3: 212 s, 5953 passed, 0 failed, load 7.8 at the start and up to 23.2 during.


Runs of browser: 1: 1071 s, 1092 passed, 1 failed, load 7.9 at the start and up to 74.6 during; 2: 1127 s, 1093 passed, 0 failed, load 30.8 at the start (above the limit) and up to 58.9 during; 3: 801 s, 1093 passed, 0 failed, load 8.0 at the start and up to 29.7 during.

- browser run 1, applysmith/internal/extension, TestBrowser_StartHandledAfterTheReloadCommitsIsDropped: `    browser_flows_test.go:502: a run was issued for a Start sent from a document that was gone (1) | --- FAIL: TestBrowser_StartHandledAfterTheReloadCommitsIsDropped (10.80s)`
- browser run 1, applysmith/internal/extension, the whole package: `FAIL | FAIL	applysmith/internal/extension	178.057s`

Runs of lane-chrome: 1: 12 s, 1 passed, 0 failed, load 4.8 at the start and up to 4.8 during; 2: 12 s, 1 passed, 0 failed, load 4.3 at the start and up to 4.5 during; 3: 13 s, 1 passed, 0 failed, load 4.5 at the start and up to 4.5 during.


After the run no test Chrome and no app process started by it was left running.

<!-- measure:end -->

## Observations from the recorded run (2026-10-12)

- Two complete runs of the harness were made on the same commit. The
  deterministic figures agreed (bundle bytes identical, disk image within 400
  bytes, every scenario count identical). Cold start of the app was 803 and
  770 ms, warm 239 and 216 ms, and the warm resident memory of the app tree 263
  and 256 MB. Timing that depends on the machine moved with its load: the unit
  suite took 534 s at a start load of 12.8 and 233 s at 4.7, and the warm start
  of `applysmith serve` was 121 and 77 ms. The warm resident memory of `applysmith
  serve` was 36 MB in one run and 60 MB in the other, so treat it as a range.
  The block above is the second run.
- Browser-tagged scenarios failed only while the machine was heavily loaded.
  The first run had a package that did not finish in one of its three browser
  runs, and four failed scenarios in the other two (among them the two list
  scenarios and the password-snapshot scenario), at a one-minute load of 58 to
  120. The second run had one failed extension scenario, and with it its
  package, at a load of 75. The three named scenarios and the extension
  scenario passed when rerun alone at a load of 4 to 9. Failures are counted in
  the tables, not removed.

## Dashboard, API and idle measurements (2026-10-17)

Measured by hand, not by `make measure`, so they live here and not in
`performance.json` (whose schema the harness owns). Apple M3, 16 GB, swap
nearly full and other test runs on the machine (one-minute load 6 to 12).
Data: a copy of the demo workspace (4 tracker rows, 23 runs, 160 apply runs,
183 board entries, 404 model-ledger rows, 886 gate decisions) and a copy of
the owner's workspace for setup and doctor timing. Scripts and raw output:
the session scratch directory `onboard/tools` (Playwright with the cached
headless shell, Node fetch, Python).

| What | Before | After | How |
|---|---|---|---|
| `applysmith serve`, new empty workspace, exec to `/api/health` 200 | median 389 ms (5 runs: 347 to 734) | unchanged | `coldstart.py`, fresh folder each run |
| `applysmith serve`, existing demo copy, same | median 246 ms (5 runs: 199 to 545) | unchanged | same, warm |
| Dashboard first load (Home), loopback | document `load` 69 ms, Home content 159 ms, 75 requests, 53 JS files, 808 KB JS uncompressed, 19 API calls, last API answer 1,579 ms (the doctor) | document `load` 42 ms, Home content 117 ms, last API answer 802 ms; the same 75 requests | `firstload.mjs`, 3 runs, new browser context each |
| `GET /api/setup/state` (every Home and Setup load) | median 301 ms, p90 388 ms | median 166 ms | `lat.mjs`, 8 calls |
| `POST /api/setup/doctor` (after the Home card paints) | median 1,567 ms, p90 2,133 ms | median 541 ms, p90 864 ms | same |
| 14 other Home, Apply Next, Runs and Tracker reads | median 1.6 to 2.7 ms each, largest body 61 KB (`/api/needs-you`) | unchanged | `lat.mjs`, 10 to 15 calls each |
| Dashboard assets over the wire (gzip, 2026-10-17, `qa2/setup2`) | all 59 JS and CSS files 889,704 bytes; first load (Home, new workspace, headless Chrome) 58 JS files, the 808 KB uncompressed | 277,097 bytes for the same 59 files; first load 262,615 bytes transferred (38 of the 58 files gzip, the other 20 under 1 KiB); a warm pass over every JS file 17 ms | `curl`-style fetch of each asset with and without `Accept-Encoding: gzip`; Playwright `request.sizes()` |
| Idle `applysmith serve`, no page open | 0.01 s CPU in 30 s | unchanged | `ps -o time` |
| Idle dashboard page (Home, Runs, Apply Next, Ask Agent) open 60 s | 0 requests (the board stream pushes changes) | unchanged | Playwright request log |
| Idle desktop app (owner's app, 3 h 25 min up) | one `/api/runs/summary` read per second, each running `/usr/bin/security` for the token: 0.46 s CPU per 30 s (1.5 %); 33,145 of 40,264 server-log lines | no request while nothing changes | `ps -o time`, server log; `TestActivityFollowsTheBoardStreamInsteadOfPolling` |

Where the setup time went (per detector or check, keychain backend):
Chrome `--version` 44 ms on every call, each keychain read about 16 ms
(the Gmail step reads three, the language-model, Telegram, credential and
2Captcha steps one or more each), the TeX toolchain check 326 ms, the
keychain write-read-delete probe 329 ms, `pi --version` 225 ms. They ran one
after another; they now run at once, and Chrome's version is kept per
executable path, size and modification time. The keychain reads are not
cached: a secret changed by another process must be seen at once.

SQLite: every static `SELECT` in `internal/` was planned with `EXPLAIN QUERY
PLAN` against the demo copy (514 found, 374 plannable; the rest are built at
run time). 27 scan a growing table without an index; none is on a per-row
request path. They load whole small tables once (the submit ledger, the
report index, tracker rows for duplicate checks), run in the background
(dedup, outcomes, liveness), or read one row of a run view. The hot reads above
answer in about 2 ms, so no index was added.

Answer-bank matcher on large selects (`TestMatcher_LargeControlsStayWithinTheCPUBudget`,
process CPU, best of three, five 500-option controls; budget 1,000 ms): the
twenty-word cases took 971 ms (same question on every control) and up to
1,050 ms under load, so the test failed intermittently. Two changes, no
behaviour change: option-label profiles are shared by every control of a
request that repeats a label under the same question text (they hold the label
and the question, not the option value), and text normalisation skips work a
plain-ASCII string or one without an apostrophe cannot need (`foldSpaces`, the
contracted-negative rewrite). After: 150 / 365 / 153 / 370 ms for the four
cases (three-word and twenty-word, same and different questions).

Compared with the original product, as observed the same day: its bridge
(`:47319`) did not answer `/v1/health` within 5 s while the Go server answered
in 2.5 ms; its Electron tree held 102 + 59 + 53 + 42 MB resident against the
Go app's 52 MB (without its WebKit helpers). The original's own records give
`/v1/health` 250 ms before and 1.7 ms after a cache on its real data set.
These are different workloads and are indicative only.

## Dashboard page loads and the setup state (2026-10-20)

By hand: headless Chrome and an in-process `applysmith serve` on the layout
check's workspace, load 9 to 62, old and new build interleaved; CPU
is the serve process's own (`getrusage`), which the load moves least.

| What | Before | After |
|---|---|---|
| `GET /api/setup/state`, server CPU per call (5 runs each) | median 49 ms (43 to 53) | median 26 ms (22 to 28) |
| Home's 20 reads sent at once, server CPU per burst | median 71 ms (51 to 90) | median 43 ms (26 to 53) |
| same, wall time of the burst | median 37 ms | median 28 ms |
| Home requests on a fresh load | 22, `apply/list?lane=apply-now` twice | 20 or 21, none twice |
| Home revealed (Settle) in Chrome, 18 loads each | median 232 ms | median 231 ms (no change visible under this load) |

Causes: the read pool kept two of its four connections idle, so each burst
opened new ones (a new SQLite connection reads the whole schema first); the
setup state opened its own connection for the migration check, parsed
`answer-bank.yml` with a new service each call, parsed `profile.yml` per
detector and ran a query per setup decision; Home's setup check (a POST)
dropped the shared reads. Left: `workspace.Status` validates every file on
each call.

Other pages, fresh load or in-app navigation: every read answers in 2 to 50
ms; a page is revealed 60 ms after its last read (`QUIET_MS` in
`ui/Settle.tsx`; follow-up reads started 0 to 11 ms after the previous one
ended, once 36 ms) and then fades in over 120 ms. Typing in the search fields
of the Answer Bank (entries, usage), Knowledge snippets, the Discovery inbox
and Settings takes one frame per key (16 to 18 ms). Build: `index` 251 KB
(80 KB gzip), largest route chunk Settings 85 KB; the other route chunks are
fetched after the first render. A dashboard session may send 1,200 reads and
240 writes a minute (`internal/api/limits.go`); a Home load sends about 22.

Page fade 320 to 120 ms, 13 pages x 5 fresh loads, 1264 px, load 9 to 17: fully visible at median 457 then 259 ms;
revealed at 121 then 120 ms; layout shift after the reveal 0.0000 on every load both times.

## The owner's live data (2026-11-02)

Read-only harnesses over the live database (`store.OpenReadOnly`; durations,
counts and function names printed, nothing copied) and the request log of
`server.log`; 2,462 runs, 4,362 mail signals, 1,270 apply runs, 209 evaluated
rows, 278 bank entries; load 7 to 15. Warm cost of one call:

| Read | Cost | Where it goes |
|---|---|---|
| `GET /api/day` | ~120 ms | apply lanes 30 to 50, mail lines (the Gmail inbox) 25 to 30, stage counts 11 to 19, run lines 12, evaluation lines 8, the rest 3 or less each |
| apply lanes (`applyapi.ReadLanes`, also `GET /api/apply/list`) | 30 to 50 ms | list evidence ~25 (`loadRuns`: SQLite stepping through `stages_json`) |
| `GET /api/gmail/inbox` | ~22 ms | reading 4,362 signals ~19; the query alone 12 to 16 ms in the `sqlite3` shell, a window-function form 20 |
| `GET /api/answer-bank/candidates` | CPU 43 → 23 ms | one matcher for the list and one index per override key (be849a59); left: classifying each question |
| `GET /api/runs/summary` | ~6 ms | the runs sync 4, the counts 2, board idle |
| `GET /api/discovery/funnel/week` | 2 × stage counts | ~5 ms each in the `sqlite3` shell |

The first reads after a start cost 5 to 20 times more. From the request log,
07:00 to 12:07 with four read connections against 12:08 to 12:38 with eight:
`/api/day` p99 14.5 s → 2.2 s and max 30.9 s → 2.2 s, and the same for
apply list, Gmail inbox, suggestions and needs-you; the slow reads left were
the first three minutes after the start, while another session's apply runs
announced every stage (0.4 to 2.2 s), and from then on 190 to 480 ms.

Not done: one read shared by concurrent identical requests behind a commit
counter (apply lanes in the day and the apply list; the Gmail inbox in the
day, Home and the menu badge). The day reaches its lanes ~30 ms and its mail
~80 ms in, so it overlaps the apply list only in part and Home's mail read
not at all: at most ~40 ms of CPU per Home load.

## Start, matching, new databases and the unit suite (2026-11-06)

Measured by hand on the shared machine (Apple M3, 8 cores, 16 GB, swap nearly
full, other sessions' builds and suites running), Go 1.27.1, `GOMAXPROCS=4`
unless stated. Each timed figure carries the one-minute load it was taken at;
bytes and allocations do not depend on it. CPU time is the process's user plus
system time (`getrusage`), which a loaded machine stretches less than wall
time. Each part names its "before"; before and after were built from the same
generated assets and, except for the suite, run alternately.

### Process start

About 2,400 regular expressions are declared at package level. Compiled by
`regexp.MustCompile` while the packages initialised, they cost every process
(the server, the app, each command-line run) before `main`, and most processes
use few of them. They are declared with `lazyregexp.New`
(`internal/lazyregexp`), which compiles an expression once, on its first use.
A test binary compiles each where it is declared, so a malformed pattern still
fails the tests of the package that declares it. `applysmith version` built with
`CGO_ENABLED=0 go build -trimpath`, before at the change's parent (694d0863),
three runs each, load 19 to 22:

| | Before | After |
|---|---|---|
| Allocated by package initialisation (`GODEBUG=inittrace=1`, summed) | 62.18 MiB, 446,925 allocations | 6.71 MiB, 90,040 allocations |
| Clock of package initialisation (summed) | 76 to 106 ms | 15 to 73 ms |
| Live heap after initialisation and one GC (`MemStats.HeapAlloc`) | 22.3 MB | 3.6 MB |
| Peak resident memory (`/usr/bin/time -l`) | 56.6 to 61.3 MiB | 30.0 to 30.5 MiB |
| Binary | 51,117,090 bytes | 50,952,114 bytes |

Two checks keep it so. The guard rule `regexp-compile`
(`internal/guard/budgets.go`) reports a package-level `regexp.MustCompile` or
`Compile`, and a fixed pattern compiled inside a function, which compiles again
on every call; a pattern built at run time is compiled where it is built, and
test files and the packages below `lazyregexp`'s layer (guard, parity, release)
are exempt. `TestBuiltBinaryServeHealthLockAndShutdown` fails when the built
binary's package initialisation allocates more than 10 MiB.

The 41 fixed patterns that were compiled on every call are declared once the
same way (99472f61). Per call, before at that change's parent, `GOMAXPROCS=1`,
2,000 iterations, three runs each, load 21: the mail classifier's
`isGenericRole` 88 to 171 µs and 307 allocations, then 6.4 to 7.5 µs and none;
`CanonicalCompany` 20 to 31 µs and 93 allocations, then 8.0 to 9.9 µs and 26;
`parseAddress` 4.4 to 6.4 µs and 24 allocations, then 1.8 to 2.6 µs and 3.

### Matching a page

The pairwise rungs read each label and each answer once per control
(104c0ab6), a control's shape verdict is kept per entry as its binding is
(eb9108b6), the placeholder check skips a label that cannot open as a prompt
(9742b213), and a label's stance, duration and deferral are read only when a
rung needs them (3da2ca46). The CPU budgets of the matcher tests, best of three
runs of process CPU time, before (main at 4d12bd8c) and after alternating three
times, load 22 to 36:

| Test | Budget | Before | After |
|---|---|---|---|
| `TestPerf_LargePageUnderOneSecond` | 1,000 ms | 733 to 1,178 ms | 160 to 280 ms |
| `TestMatcher_LargeControlsStayWithinTheCPUBudget`, 500 options of 3 words, five controls | 1,000 ms | 387 to 624 ms | 172 to 300 ms |
| the same, 500 options of 20 words, five controls | 1,000 ms | 963 to 1,586 ms | 441 to 801 ms |
| the same, 3 words, five different questions | 1,000 ms | 412 to 648 ms | 168 to 295 ms |
| the same, 20 words, five different questions | 1,000 ms | 1,097 to 1,564 ms | 465 to 814 ms |

The last case is the one closest to its budget: in the full suite at load 15
to 21 it took 1,057 ms once before the stance and duration change. What is left
is per-label regular-expression work spread over the form signatures and the
binding scores, about forty checks over each long label.

### New databases

A new database is migrated from the first migration on, which costs about
200 ms, nearly all of it SQLite reading the whole schema again after each
`ALTER TABLE`. A process's second new database of a migration set keeps the
file the migrations left (`VACUUM INTO`, before the workspace id is stamped),
and each later one starts as a copy of it (`internal/store/migrate.go`,
`newImages`), which costs about a millisecond. The server and the command line
create at most one new database per process and keep nothing; the agent's
improvement probes and the test binaries create many. `store.Open` of a new
database, 20 per process, three runs alternating, load 22 to 36: 145 to 375 ms
before, 24 to 50 ms after, on average.

### The unit suite

`go test -p 2 -parallel 2 -tags dashboard -count=1 ./...` (the command of
`make test`, without its cache), `GOMAXPROCS=4`, before on main at bd02341d and
after on this work at 27ec5e83 (everything above except the stance and duration
change):

| | Before | After |
|---|---|---|
| Wall, user, system | 1,484 s, 1,397 s, 308 s (load 48 falling to 9) | 640 s, 447 s, 166 s (load 15 to 21) |
| Sum of the packages' run times | 2,763 s | 1,133 s |
| Packages that failed | answerbank and answerbank/match (CPU budgets), apply (killed at the 10-minute timeout after 423 of its 605 tests), parity | answerbank (the 20-word case above, 1,057 ms), parity |

The parity failure is main's own: the audit cells ENT-88 and WID-37 name an
engine test that was renamed. `apply` runs all 605 tests in 267 s: its scripted
walk looks 1 s after a form page instead of 5 s, since a scripted tab changes a
page within 600 ms of the action that changes it, and one test that draws its
Apply after a second of reads keeps a 3 s look. The other large packages
(api 515 to 231 s, cli 165 to 71 s, answerbank/assist 127 to 16 s, discovery
122 to 26 s, agent 112 to 16 s) open a new database per test; part of each
difference is the load, since the run before started at 48.

## The overnight writer stalls (2026-11-17)

The owner's database was 282 MB (`gmail_bodies` 104 MB, `inbox` 23 MB,
`llm_embeddings` 18 MB, `apply_runs` 17 MB). Two built-in timers starved the
single writer, and the fixes are in [scheduler.md](scheduler.md):

- The 02:00 snapshot held the writer for its `VACUUM INTO`: about 15 s inside
  the app (the same copy took 2.2 s with the `sqlite3` command line, which is
  C SQLite and an idle file). Every write waiting longer than the 5 s busy
  timeout failed; one of them was the end of a `gmail.sync` run, which then
  blocked that schedule until a restart. The snapshot now reads beside the
  writer; a live backup through the server measured 15 s with no busy error.
- The 04:00 retention sweep gave all its rules one minute. The runs board rule
  withdrew 160 Telegram messages of entries finished over 14 days earlier, at
  one a second, so it used the minute and every later rule failed; the slot
  record failed with the expired context too, so the sweep repeated every
  minute for about ten minutes each night. Each rule now has its own budget.

Not explained: `GET /api/runs/summary` answered 503 (no reader within its
wait) at 00:40, 01:00 and 01:10, the `wal-checkpoint` timer missed its
deadline at those times, and the `gmail-alerts` and `gmail-reminders` timers
both ran out their minute at 01:12. The host did not sleep (`pmset -g log`),
App Nap is held off (`app/appnap_darwin.go`), and the visible-signals query
that both Gmail timers start with took 144 ms on a copy (4,983 signals). The
server log of that hour holds no other request or error to explain it; the
goroutine dump written at a stall (`.applysmith/logs/stall.txt`) is where to
look when it recurs.

## Limits of these numbers

The machine is shared with other builds and test runs, which is why every
sample carries its load. Seven samples give a coarse 90th percentile. Ad-hoc
signing is what the local bundle has; a Developer ID build adds a first-launch
check that is not measured here. Nothing in this page has been measured on a
second machine, on another macOS version, or against a real workspace with
years of data.
