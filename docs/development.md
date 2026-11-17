# Development

## Tool versions

Observed on macOS 26 (Darwin 25.2.0, arm64) on 2026-10-11.

| Tool | Version | Pinned by |
|---|---|---|
| Go | 1.27.1 (host install is 1.25.6; `GOTOOLCHAIN=auto` fetches 1.27.1) | `go` line in `go.mod` |
| mygo framework and CLI | v0.5.5 (commit `4d8673cd2658a2c6e86b26c44751e57bf8174f91`, 2026-10-16) | `go.mod` requirement and `tool` directive, `go.sum` |
| Bun | 1.3.5 (`~/.bun/bin/bun`) | `packageManager` in `web/package.json`; `make generate` refuses any other version |
| Dashboard | React 19.3.0, Vite 8.3.4, TypeScript 7.0.2; component tests on vitest 5.0.3 with happy-dom 20.14.6 (dev dependency only) | exact versions in `web/package.json`, `web/bun.lock` |
| Engine tests | vitest 5.0.3, happy-dom 20.14.6, TypeScript 7.0.2 (dev dependencies only) | exact versions in `engine/package.json`, `engine/bun.lock` |

mygo's CLI is a Go tool of this module, so there is nothing to install:

    go tool mygo version      # mygo 0.5.5

Upgrade with `go get -tool github.com/egoist/mygo/cmd/mygo@<tag>` and
`go get github.com/egoist/mygo@<tag>`, then `make notices` (the dependency
list names the version) and record the commit here.

Chosen version and why: v0.5.5, picked on 2026-10-17 from v0.4.8. It built
with no change to the code we call (the one changed meaning, `App.Locale()`,
is not used here), added no Go dependency, passed the app tests, packaged, and
the hidden smoke run of the bundle passed. The dashboard uses one 0.5 addition:
`window.mygo.preferences` and its `mygo:preferences` event, to follow macOS
Reduce Transparency (see [dashboard.md](architecture/dashboard.md)). If a later
regression traces to mygo, pin v0.4.8 and record the cause here.

Earlier: v0.4.8, picked on 2026-10-12 from v0.4.1, the day it
was published. It built, passed the full test suite, packaged and ran the
packaged smoke build with no change to the code we call (the root package's
API only gained `SetWindowStateStore` and `WebView.SetBackgroundColor`), so the
fallback v0.4.6 was not needed. If a later regression traces to mygo, pin
v0.4.6 (2026-10-12, the release that added the window state store, the DMG
detach by device and `macos.icon`) and record the cause here.

## Build

Verification defaults to four Go runtime threads, two Go test packages with two
tests each, and two Vitest workers per frontend workspace. Use `GOMAXPROCS=2
make verify GO_TEST_FLAGS="-p 1 -parallel 1"` on a busy machine. Browser
acceptance (`make test-browser`) runs one package at a time with three tests in
parallel; the headed target and the layout check run one test at a time.

### One heavy run at a time

`make verify`, `verify-documents`, `test-browser`, `test-browser-headed`,
`measure` and `release-check` take one machine-wide lock
(`HEAVY_LOCK`, `~/Library/Caches/applysmith-go-heavy.lock`, through macOS
`lockf`). A second one prints that it is waiting and starts when the first
ends. Several agent sessions share this 16 GB machine: on 2026-10-24 two
concurrent `make verify` runs plus a session's own `go test` pushed the load to
88–127 with the swap full, and `internal/api`, `internal/apply` and a
timing-sensitive Telegram test failed only because of the load (they passed
alone).

While iterating, run the package you changed (`go test ./internal/llm/`), one
package at a time. Before `main` moves, run `make verify` and
`make test-browser`.

The matcher's CPU budget tests (`TestMatcher_LargeControlsStayWithinTheCPUBudget`,
`TestPerf_LargePageUnderOneSecond`) exceed their budget on the unchanged tree
too once the load average passes about 10. Judge a suspected regression by
alternating the old and new test binaries (`go test -c`) under the same load
and comparing `go tool pprof -diff_base` profiles, not by one suite run.

### Build cache

The Makefile exports `GOFLAGS=-trimpath`, so a build leaves the checkout's path
out and every worktree at the same commit reuses one cache entry. Measured on
2026-10-24: building a package in a second worktree at the same commit added
924 KB to the cache without `-trimpath` and 8 KB with it. With about 30
worktrees the cache had grown to 30 GiB in a day.

The same GOFLAGS carry `-gcflags=all=-dwarf=false`: packages compile without
DWARF debug information. Panics, test failures and `runtime.Caller` read the
line table, not DWARF, so only a debugger loses anything; for a delve session
run `go build -gcflags=all=-N` yourself, which replaces the flag. Measured on
2026-10-31: `internal/answerbank/match` compiled to 9.7 MB with DWARF and
5.6 MB without. An edit recompiles the edited package and every package that
imports it, directly or not: one function added to `internal/answerbank/match`
recompiled 17 packages (79 MB), one in `internal/apply` 7 (48 MB), one in
`internal/api` 6 (36 MB), `internal/api` alone being 20.5 MB of each. With
about 40 worktrees under active edit the cache grew about 1 GiB an hour.

Without DWARF the same three edits recompiled 49.7, 30.0 and 22.7 MB. Moving
the handlers out of `internal/api` into per-domain packages
([architecture/api.md](architecture/api.md), "Package layout") then took
`internal/api` from 12.8 to 10.9 MB and its test variant from 27.4 to 25.6 MB,
but the three edits still recompiled 49.5, 29.4 and 20.9 MB, measured on
2026-11-01. `internal/api` imports every handler package and keeps the server
wiring, and `cli`, `onboarding` and `launch` (9.4 MB together) import it, so an
edit in any handler package still recompiles all of them: one in
`internal/api/gmailapi` recompiled 21.6 MB. Splitting a package pays only when
its importers do not import the other parts; the DWARF flag is what moved these
numbers.

A direct `go` command outside make needs the same flags; set them once per
machine with `go env -w GOFLAGS='-trimpath -gcflags=all=-dwarf=false'`.

    make build      # generate, then go build -tags dashboard ./cmd/... into bin/
    make verify     # everything that needs no browser: generate, go vet, every Go test (the release-hygiene guard is internal/guard), dashboard, engine and extension typecheck and test
    make verify-documents   # opt-in: runs the installed LaTeX, Python and Poppler tools (architecture/document-toolchain.md)
    make notices    # regenerate third_party/NOTICE-dependencies.txt
    make test-browser   # everything that needs a real Chrome, headless: browser, engine, extension, apply, discovery, API, replay, the CLI's browser cases and the dashboard layout check at 360, 768 and 1280 px (docs/architecture/browser.md; docs/architecture/dashboard.md, "Layout check")
    make test-browser-headed   # opt-in: the focus / minimized-window tests, which open real windows
    make test-live   # opt-in: reads the real public sources once each, no key, no session, no model (internal/discovery/live_smoke_test.go)
    APPLYSMITH_LIVE_SMOKE_HEADED=1 make test-live   # opt-in: visible, isolated signed-out Chrome; no postings is a skip, not extraction acceptance
    APPLYSMITH_LIVE_CAPTCHA_ACCEPTANCE=official-demo-confirmed APPLYSMITH_SECRET_TWOCAPTCHA_KEY=<key> go test -tags browser -count=1 -run TestLiveAcceptance_CaptchaOfficialDemo ./internal/apply/   # opt-in, paid: one 2Captcha task (cents) on the vendor's public reCAPTCHA demo; APPLYSMITH_LIVE_CAPTCHA_SCREENSHOT=<file.png> keeps a screenshot
    applysmith extension export <dir>   # write the unpacked browser extension (docs/architecture/extension.md)
    make app        # generate, then build and ad-hoc sign app/build/<os>-<arch>/Applysmith.app
    make package    # the same plus the disk image, then check the bundle (architecture/packaging.md)
    make computer-use        # opt-in: build PersonalCU into computeruse/dist/PersonalCU.app, signed with the local identity or ad hoc (architecture/computer-use.md)
    make computer-use-test   # opt-in: PersonalCU's Swift unit tests
    make clean-generated     # remove web/dist, bin/ and the generated engine and extension bundles
    make computer-use-install    # by the owner, the one step: sign (first time: creates the local identity, asks for the login password), build, replace /Applications/PersonalCU.app, open it
    make measure    # opt-in, about an hour: startup, process-tree memory, scan and fill rates, sizes, suite durations into architecture/performance.md
    APPLYSMITH_MEASURE_STARTUP=1 APPLYSMITH_MEASURE_KEYCHAIN=1 APPLYSMITH_APP_BUNDLES=<a.app> go test -run PackagedStartup ./app/   # opt-in: uses the real login keychain and real HOME; restores the app's extension folder, appends to ~/Library/Logs/Applysmith (architecture/packaging.md)

Generation order (`make generate`): wire types (`go tool tygo`, pinned in
`go.mod`, configured by `tygo.yaml`, output `web/src/wire/index.ts`), then
frontend assets (`web/dist`), then Go packages that embed them. The engine
bundle (`internal/engine/generated/engine.js`, built by
`go generate ./internal/engine` with the esbuild Go API; no bun or
`node_modules`) is generated the same way, but `internal/engine` compiles
without it (an empty `Bundle()`), so vet and build of that package work on a
fresh checkout; its tests fail until the bundle is generated. The same holds
after checking out a commit that changes `engine/src` in an existing worktree:
`TestEmbeddedBundleIsCurrent` fails on the stale bundle until `make generate`
runs, which `make build`, `make verify` and `make test-browser` do and a bare `go test` does not.
`web/embed.go` embeds `web/dist`, so any `go` command over `./...`
needs `make generate` first on a fresh checkout. `cmd/applysmith` imports it only
under the `dashboard` tag, and `internal/*` never does. `web/dist` is ignored and never committed.

Cross-compile the CLI without cgo:

    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/...
    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./cmd/...

## Running the server

    go run ./cmd/applysmith serve --workspace /tmp/aj-ws --port 0   # prints the address
    go run ./cmd/applysmith open --workspace /tmp/aj-ws              # print a fresh dashboard link from the running server
    go run ./cmd/applysmith pair --workspace /tmp/aj-ws              # print the API token for the extension
    go run ./cmd/applysmith token rotate --workspace /tmp/aj-ws      # replace the API token
    go run ./cmd/applysmith doctor --workspace /tmp/aj-ws --port 0   # exit 0 ok, 1 required failure, 4 unable to run
    go run ./cmd/applysmith setup --workspace /tmp/aj-ws --answers answers.txt   # scripted walk: every required step that is not complete, then the optional ones on offer
    go run ./cmd/applysmith setup status --json --workspace /tmp/aj-ws
    go run ./cmd/applysmith llm show --workspace /tmp/aj-ws
    go run ./cmd/applysmith kb search "reef survey" --workspace /tmp/aj-ws   # evidence retrieval over knowledge/ (docs/architecture/kb.md)
    go run ./cmd/applysmith interview-prep --candidacy 1 --stage tech-1 --type technical --workspace /tmp/aj-ws   # prep pack for one interview stage (docs/architecture/interview.md)
    go run ./cmd/applysmith evaluate gaps --report 1,2,3 --workspace /tmp/aj-ws   # skill gaps over saved reports (docs/architecture/evaluation.md)
    go run ./cmd/applysmith evaluate decode --file posting.txt --workspace /tmp/aj-ws   # layer-by-layer decode, no report (docs/architecture/evaluation.md)
    go run ./cmd/applysmith followup-draft --candidacy 1 --workspace /tmp/aj-ws   # follow-up draft, nothing sent (docs/architecture/evaluation.md, Message drafts)
    go run ./cmd/applysmith tracker outcomes --from 2026-09-01 --as-of 2026-10-12 --workspace /tmp/aj-ws   # read-only outcome cohort (docs/architecture/tracker.md)
    go run ./cmd/applysmith answer-bank status --workspace /tmp/aj-ws        # what still needs an answer (docs/architecture/answer-bank.md)
    go run ./cmd/applysmith answer-bank evolve --workspace /tmp/aj-ws        # candidates and stale entries, dry run; --apply writes
    go run ./cmd/applysmith answer-bank candidates list --workspace /tmp/aj-ws   # approve <id> [--answer <text>] and reject <id> decide one; unapprove <id> and restore <id> take it back
    go run ./cmd/applysmith agent sessions list --workspace /tmp/aj-ws       # Ask Agent conversations; show <id>, export <id> --out f.json, delete <id> (docs/architecture/ask-agent.md)
    go run ./cmd/applysmith mcp --check --workspace /tmp/aj-ws       # can an MCP client reach the running server (docs/architecture/mcp.md)
    go run ./cmd/applysmith mcp --print-config                       # JSON that registers `applysmith mcp` in an MCP client
    go run ./cmd/applysmith remote on tunnel --yes --workspace /tmp/aj-ws   # opt-in HTTPS tunnel; off, status, pair, devices, revoke <id>|all, retry (docs/architecture/remote-access.md)
    go run ./cmd/applysmith routes                                   # the API route table
    go run ./cmd/applysmith backup create --out /tmp/aj-backup       # live through the server when one runs
    go run ./cmd/applysmith backup verify /tmp/aj-backup

The Ask Agent runs on the pinned pi release (`internal/agent/pirpc.PinnedVersion`),
which the user installs; `APPLYSMITH_PI_PATH` names the executable when it is not on
`PATH` (docs/architecture/ask-agent.md).

One server per workspace: it holds an exclusive lock on
`<workspace>/.applysmith/server.lock` and writes its port and origin there, so a
second `serve` exits with `server_already_running`. Without a keychain (CI,
containers) set `APPLYSMITH_SECRET_API_TOKEN` to a 43-character token. Request
logs go to `<workspace>/.applysmith/logs/server.log` (`APPLYSMITH_LOG_LEVEL`); a
refused request's line names its error `code` and, when the error has them, its
`reason` and `field`, never the message. A read
(a GET with no deadline of its own) still running after 2 s writes the stacks of
every goroutine to `logs/stall.txt`, at most once an hour, and logs the route:
the request log says how long a read took, `stall.txt` what it waited on.
`go test ./internal/api/... ./cmd/...` covers the HTTP behavior and runs the
built binary; `-short` skips the binary test.

Stopping a server interrupts the scheduled run in flight, and the occurrence is
not replayed (architecture/scheduler.md, Stop and scan failure boundaries). A
live check that restarts the server of a real workspace reads `applysmith schedule
list` first and restarts away from a schedule's NEXT time: a restart at the
owner workspace's 05:00 PST lost that day's LinkedIn feed scan.

CLI commands use the database in one of three ways (`internal/cli/openstore.go`):
read-only without a lock (`tracker list|show|export|followups`, `inbox list|show|export`, `scan runs`, `leads list`,
`blocked list`, `registry stats`, `signals status|replay|decompose`, `agent sessions list|show|export`, `tracker dedup`
without `--apply`, `tracker archive --dry-run`, `documents list|show`, `setup status|next`, `llm show|cost`,
`llm debug-capture status`);
as one more writer beside a running server (the other `tracker` commands, `inbox add|dismiss`, `blocked add|remove`,
`leads dismiss`, `registry add|revive|block`, `agent sessions delete`, and the `kb` reads (`search`, `list`, `show`, `lint`,
`drafts`, `export`, `repo list`, `mine suggest|show`), which bring the
knowledge index up to date with the files as the server's reads do); or
under the exclusive workspace lock, which fails while a server runs
(`scan` with no server, the other discovery commands, the other `kb` and `documents` commands, writes of `setup`, and
`llm configure|keys|test|bench`, `llm prices update` and `llm debug-capture on|off`). Every command but `init` and `setup` refuses a
workspace folder that does not exist (exit 2, naming `applysmith init`), so a mistyped `--workspace`
never becomes a new workspace; a read of a workspace with no database creates nothing and names
`applysmith init` too. Tests start from `newTestEnv`'s empty workspace folder (`newBareTestEnv` has none). The exclusive ones run an
in-process worker pool or change the workspace's identity, so they assume
nothing else works on it: starting a pool ends every evaluation a running
server holds as interrupted. `setup` takes the lock only once the workspace exists.

`apply`, `evaluate`, `scan <source>` and `inbox promote` do not open the database when a server holds the
workspace: they send the request to it (`attachLive`) and wait for the run, so
they work with the app open. `evaluate` and `scan` open the workspace themselves, under the
exclusive lock, only when no server runs. `llm set|group|reset|budget|cap`, `backup create` and
`token rotate` do the same: the running server makes the change, and with no server the command
takes the lock and makes it itself.

The wire shapes live in `internal/api/wire`; `testdata/wire/` holds the golden
documents that the Go tests and the TypeScript tests (`cd web && bun run test`)
decode. The route table and its decisions are in
[architecture/api.md](architecture/api.md); the dashboard is described in [architecture/dashboard.md](architecture/dashboard.md).

## Form replay

    go run -tags browser ./cmd/replay [-workspace DIR] [-baseline FILE] [-only RUNID,...]

A developer tool, not an `applysmith` command. Every stopped apply run leaves
its last page at `.applysmith/apply/snapshots/<run>-stop.html` beside its
`apply_runs` row. The replay serves each such snapshot, at the address of the
page the run stopped on, in one throwaway headless Chrome (temporary profile,
never the app's lanes or the user's Chrome), and measures what the current
engine and answer bank make of the form:

- No network: the tab's requests are answered by interception, the snapshot
  for its own address and a refusal for everything else, and no host name
  resolves in that Chrome. Scripts, links and meta directives are stripped,
  a `script-src 'none'` policy stops inline handlers, and every form control
  is emptied (attribute and property) and every pressed or checked ARIA
  button turned off before the fill, so the replay measures the build and not
  what the stopped run left in the copy.
- The fill is the one the dashboard's fill of an open tab runs
  (`apply.Runner.OpenFill`): one page fill, the page check and its
  deterministic fixes, never a press; a document request after the load
  fails the run as `navigated`, and a panic is that run's `panic` (its stack
  goes to stderr) while the replay goes on. It reads the answer bank, the profile and the
  answer-bank settings of the workspace, opens the database read-only, and
  uses no model, no board read, no documents, no tracker and no learning
  upload.
- Each required control is `answered`, `left:<reason code>` (from the check
  stage's own readiness verdict, `gates.SubmitReadiness`), or
  `needs-live-page`: a widget only the site's scripts drive (a list that did
  not open, or offered no option its answer could take, scripted choice
  buttons whose state never moved, a component driver or binding that is
  missing, a native upload),
  and every file control, since the replay attaches no document, with a
  text box that bears a file control's label (the upload widget's own state,
  which only the site's stylesheet hides). A control
  the original run left that the build answers is listed per run.
- Output: `.applysmith/replay/<UTC stamp>/results.json` (schema 1) and
  `RESULTS.md` (counts by ATS, left fields by reason, one line per run).
  `-baseline` names an earlier `results.json`: answered there and not now is
  a regression, printed, and the command exits 1.

Why this and not engine-call speed (decision 2026-10-22, from reading moejs,
a pure-Go JavaScript engine, ideas only): moejs proves each change on a
conformance suite plus recorded real calls and compares against another
engine. Our engine needs a DOM, so it cannot run in Go, and a one-off probe
(headless Chrome, a 120-field form) put the injection of the bundle at about
11 ms per new document and a status call at 0.12 ms, under 1% of a page
fill. So the borrowed part is the corpus: the real forms the runs stopped on,
replayed per build, with `-baseline` as the differential.

Privacy: the output holds field labels, kinds, reason codes and counts only,
never a value, a profile field, a URL or the snapshot. Run it on the owner's
workspace only when the owner asks; tests use fictional fixtures
(`go test -tags browser ./cmd/replay/`, part of `make test-browser`).

## Wire types

`internal/api/wire` imports nothing else, so `go tool tygo generate --config
tygo.yaml` runs on a fresh checkout before any bundle exists. The output,
`web/src/wire/index.ts`, is committed with its generated-code header, so the
dashboard type-checks from a checkout; `make generate` rebuilds it whenever a
wire file changes, and the diff belongs in the same commit as the Go change.
`scalars.go` is excluded from the generation and written by hand in the `frontmatter` of `tygo.yaml`, because the
generator is syntactic and cannot see how `SafeInt` and `Time` travel as
JSON. Two unions that it cannot derive (`StreamEvent`,
`ErrorCodeName`) are emitted from `//tygo:emit` variables and checked against
the registries by `go test ./internal/api/wire`, which also regenerates into a
temporary file and fails when `web/src/wire/index.ts` is missing or differs
from it.
Generated types are not validation: `web/src/api/decode.ts` checks the rules at
runtime and is tested against the shared fixtures.

## Database migrations

Migrations under `internal/store/migrations/` are append-only: every schema
change is a new file numbered one past the last, and a file once committed is
never edited. The store records each applied migration's checksum and refuses
to open a database whose applied migration differs from the file
(`TestChangedMigrationChecksumIsRefused`), and a gap in the numbering is refused
at load (`TestMigrationFilesMustBeContiguous`).

## Dead code

`go tool deadcode -tags dashboard ./...` lists the Go functions no build of the
product reaches. Run it when cleaning up and delete what it names, with the
tests that were its only callers; the bundlers `go generate` runs and test
doubles kept in non-test files are the expected remainder. Unused TypeScript
symbols fail the typecheck in `web`, `engine` and `extension`.

## Release-hygiene guard

`internal/guard` holds the release-hygiene rules as ordinary Go tests. They
read the files git would track (tracked files plus untracked files that are not
ignored) and need no network and no generated assets:

    go test ./internal/guard/...                              # unit tests of each rule plus the whole-repository run
    go test -count=1 -v -run '^TestRepository$' ./internal/guard/   # the whole-repository run only, verbose

A failure prints `rule: path:line:col: detail [masked excerpt]`. The excerpt
keeps the first character of a match and replaces the rest with asterisks;
a personal-token match prints the file, position and the token's ordinal in the
list, never the token.

Rules in `rules.go`: personal tokens, retired names, email addresses, phone
numbers, absolute home paths, secret shapes, English-only text, comment rules,
repository budgets, source file length, Go formatting (`gofmt`: every Go file
is as the module toolchain's `go/format` prints it; format with `go fmt`, not
the host's older `gofmt`), build output and binaries, internal
material paths, dependency budgets, regular expressions compiled at process start
or on every call (`regexp-compile`, see
[performance](architecture/performance.md#process-start)), package layers, pure
packages, provenance, the document-source manifest
(`third_party/document-sources.json`: exact files, shipped hashes, licence
texts, notices), NOTICE and the dependency-notice drift check,
reference-source similarity, the captcha solver's scope (`captcha-scope` in
`captcha.go`: the provider and its setting are named only at the approved call
sites, and no other solving service anywhere), and loopback listeners
(`loopback-listener` in `listeners.go`: every server socket binds a loopback
address).

The external `gitleaks` rule runs when the scanner is on `PATH`; development
mode prints a skip when it is missing and release mode fails. Acceptance used
Gitleaks 8.30.1 with the supported `dir`/`git` commands. The rule scans a private
temporary snapshot containing exactly the guard's file set, so ignored local
`.env` files, generated output and parent repositories are not source inputs.
The snapshot and redacted JSON report are removed after the scan. Diagnostics
expose only rule, source path and line, never matched values or raw tool output.
`GitleaksConfig` in `detectors.go` extends the upstream defaults: uppercase
`FAKE`/`EXAMPLE` mark synthetic credentials, and the exact ATS name
`SuccessFactors` is excluded only from the client-ID rule. No file or directory
is exempted. Inline suppression comments and `.gitleaksignore` are disabled;
`gitleaks` findings cannot be suppressed through `allow.yml`.
See the [official configuration reference](https://github.com/gitleaks/gitleaks#configuration).

The `similarity` rule also runs through `make verify` and the
exported release guard. Supply read-only reference directories with
`APPLYSMITH_SIMILARITY_REFERENCES` (OS path-list separator), then run:

    go test ./internal/guard -run Similarity

Unset references produce an explicit development skip and a release failure.
Invalid, empty or overlapping reference roots fail. Runtime source, prompts,
templates and data are checked regardless of extension; recognized source/data
formats elsewhere include `.tmpl`, `.jsonl` and `.golden`. Documentation prose
and licence inventories are metadata. Reference snapshots retain files their
own `.gitignore` would hide; symlinks, Git metadata, installed dependency folders
and virtualenv/cache directories are not traversed. Binary files are skipped.
The exact hash-validated attributed document files are exempt through the
manifest gate; neighboring files remain checked. Lockfiles (`go.sum`,
`bun.lock`) are not compared: the package managers write them from `go.mod`
and `package.json`, which are.

Lowercased alphanumeric/underscore tokens include comments. Findings report
only file pairs and scores: at least 15 percent shared distinct word 5-grams
when the candidate has at least 60, any exact 30-token run (with exact longest
length), or identifier Jaccard at least 0.5 with at least 20 identifiers per
side. Keywords, standard-library names and, in HTML files, HTML's element and
attribute words are excluded from identifier scores.
Thresholds remain those in `release-hygiene`. Only a provenance reviewer reads
reference implementations; implementers receive finding metadata and behavioral
corrections, never reference code or excerpts. A finding requires review and
cannot be resolved by renaming literals to evade the comparison.

Files that configure the guard:

| File | Purpose |
|---|---|
| `internal/guard/layers.txt` | Layer number of every package under `internal/`; imports must go to a strictly lower layer. A package flagged `pure` is held to the no-I/O, no-clock, no-randomness rule. |
| `internal/guard/allow.yml` | The only place for exceptions: at most 50 entries, each with a rule, one exact file, the exact value and a reason. Entries that match nothing, or name a missing file, fail. Token, retired-name, internal-material, secret and `gitleaks` findings cannot be listed. |
| `docs/provenance.md` | Provenance register read by the `provenance` rule. |
| `NOTICE`, `third_party/NOTICE-dependencies.txt` | Third-party notices. The second file is generated by `make notices` (needs `bun install`) and compared with a fresh generation on every run. |

Budgets (constants in the guard, changed only with a spec change): 3,300 tracked
files (raised from 3,001 to 3,020 on 2026-10-20 by owner decision A-6, for the Ask
Agent self-improvement packages, and to 3,060 on 2026-10-21 by owner decision AC-1,
for the PersonalCU source and the agent control layer, to 3,160 on 2026-10-22
by owner decision BG-2, "raise the cap", as the tree had reached 3,060, and to
3,180 on 2026-10-29 by owner decision DT-3, for the Ask Agent's delegated tasks,
to 3,200 on 2026-11-02 by owner decision GM-3, for the Gmail watcher and
deadline reminders, and to 3,300 on 2026-11-06 by owner decision PF-1, "the
limit can be raised", as the tree had reached 3,200), 40,000,000 tracked bytes
(raised from 34,000,000 on 2026-10-22 by owner decision BG-1, "the cap can be raised a bit"), no file over 1 MB except icons, Go and Swift files up to
1,200 lines and TypeScript files up to 1,500, at most 20 direct Go
dependencies, at most 10 dashboard runtime dependencies, none for the engine or
the extension.

### Release check and publication export

    make release-check               # verify, scan history, export, guard the export, build, test, package
    make publish-tree OUT=/tmp/aj-export   # only the export and the release-mode guard on it

Both run `cmd/releasecheck` (`internal/release`) on the committed tree and push
nothing. The tree must be clean, `APPLYSMITH_PII_TOKENS` and
`APPLYSMITH_RETIRED_NAMES` must name the two lists below, reference directories
must be supplied through `APPLYSMITH_SIMILARITY_REFERENCES`, and `release-check` also
needs `gitleaks` on `PATH`. The steps, in order: `tools`, `clean-tree`,
`history-scan` (the guard's secret rules over every blob reachable from any
ref), `gitleaks-history` (`git` over all refs and full history), `export`,
`install` (locked dashboard dependencies), `guard-release`, `gitleaks-export`
(`dir` with the same scanner configuration), `verify-private` (`make verify`
on the private tree in development mode), `build-export`, `test-export` (vet, `go
test`, dashboard, engine and extension tests), `package-export`.

- The export is the tracked files without Class B paths (`guard.ClassBReason`:
  `openspec/`, agent instruction files and tool directories, execution plans,
  research and memory notes, parity-audit files). It has no `.git`; the guard
  reads it with `APPLYSMITH_GUARD_ROOT=<dir>` and `APPLYSMITH_GUARD_RELEASE=1`, which
  `go test ./internal/guard/` honors, and applies the export's own `.gitignore`.
- A private tree that keeps `openspec/` verifies in development mode and does
  not break the run; an `openspec/` that reaches the export fails
  `guard-release`; a file the build needs that the filter dropped fails
  `build-export` even though the private tree builds.
- Independent steps (tools, history, private verify) run even after another
  step fails; steps that depend on the export are skipped once one of them
  fails. The exit code is 1 when any step failed.
- `package-export` builds the ad-hoc signed `.app` and `.dmg` from the export
  with `go tool mygo build`, fails above 80 MB (decimal) and lists quarantined
  Open Anyway and fresh macOS-user acceptance as the remaining steps of task 7.4.
- The temporary export is removed after a passing run; `go run ./cmd/releasecheck
  -keep` keeps it.
- Not run here: the fresh-history identity checks (one root commit, no-reply
  author addresses) belong to the repository that is published, not to this
  private history.

### Publishing to GitHub

The public GitHub repository holds only exports, never this history. From a
clean checkout of `main`:

    APPLYSMITH_RETIRED_NAMES=~/.config/applysmith/retired-names.txt \
    APPLYSMITH_SIMILARITY_REFERENCES=<reference checkouts> make publish-tree OUT=<new dir>

then commit the export in a clone of the public repository as
`Applysmith <noreply@applysmith.example>` and push its `main`. The first
publication (2026-11-16) is one root commit; a later one replaces the clone's
tracked files with a fresh export and adds one commit. The owner chose the MIT
licence (`LICENSE`) for the public repository on 2026-11-16.

### Inputs kept outside the repository

Two lists live outside the worktree and are never committed. Each is a UTF-8
file with one entry per line; blank lines and lines starting with `#` are
ignored, a line starting with `re:` is a Go (RE2) regular expression, and any
other line is a case-insensitive literal. Every entry is at least 3 characters.

| Variable | List |
|---|---|
| `APPLYSMITH_PII_TOKENS` | Personal tokens that must not appear in any file content or path. |
| `APPLYSMITH_RETIRED_NAMES` | Identifiers, mode names and labels retired by the rewrite. Outside release mode the private `openspec/` plan is not scanned, because it records the migration away from these names. |

When a variable is unset the matching rule is reported as `SKIPPED` (a
`GUARD NOTICE` line on stderr and a skipped subtest under `-v`), the other rules
still run, and the scan did not happen. A list that sits inside the repository,
is unreadable or holds no usable entry fails the run. With
`APPLYSMITH_GUARD_RELEASE=1` an unset list fails instead of skipping.

The Makefile sets `APPLYSMITH_PII_TOKENS` to `~/.config/applysmith/pii-tokens.txt`
when that file exists, so `make verify` on the owner's machine always runs the
personal-token scan. The list holds the owner's real name, mailboxes and account
names, and the CV items that would identify them (the employer, the research
institute, project names); tests and docs use the fictional persona's values
instead. Generic items such as a large university stay out of the list.

On 2026-11-15 the owner had the history cleaned with `git filter-repo`, and on
2026-11-16, after the rename to Applysmith, rewritten once more: every
commit is authored and committed by `Applysmith <noreply@applysmith.example>` (the
repository's local `user.name` and `user.email` are set to it, so new commits
keep it), and every listed item was replaced in every file version and commit
message by the fictional values the tests use. Commit hashes cited in documents
from before that day no longer resolve; the rules and the old-to-new commit map
live beside the token list (`~/.config/applysmith/`), not in the repository.

## Agent execution model

Work proceeds capability by capability inside each phase. Each capability
passes four roles in order:

1. Parity auditor (read-only on the old repository, never on clean-room
   paths). Writes the capability's list of behaviours, edge cases and
   incident-derived rules as shapes only, never personal values; the spec is
   amended where it lacks one.
2. Implementer. Works from the spec and the parity file, in the new repository
   only. A clean-room capability receives the spec alone.
3. Verifier. Runs `make verify` and `make test-browser`, checks the result
   against the spec, and reports gaps as a list. It fixes nothing.
4. Reviewer, at the end of each phase. Reviews against the specs, runs the
   similarity check for clean-room areas and a personal-data spot check.

Rules:

- A capability is done when each retained requirement and scenario maps to an
  executable check and its evidence, the verifier report has no unresolved
  blocking finding, and its tasks are ticked. A model review or a coverage
  percentage alone is not an acceptance result.
- Roles describe responsibilities. Resolve the available model for each role
  before a run; when the preferred model is unavailable, record the
  substitute. Never claim a role ran on a model that did not run it.
- Parity audits use a frozen reference commit. Later changes in the old product
  go to a delta list and are re-evaluated before cut-over.
- A phase is done when its exit check passes on a clean checkout.

## Build cache and disk space

Before a full verification, check free space with `df -h .` and the Go build
cache with `du -sh "$(go env GOCACHE)"`. Repeated builds, race tests and native
packaging share that cache. Stop this session's verification before clearing
it with `go clean -cache`; source files, downloaded modules, workspace settings
and runtime data do not belong to this cleanup.

For a disposable acceptance run, create a cache with `mktemp -d`, pass that
directory through `GOCACHE` for the whole run, and use the same `GOCACHE` with
`go clean -cache` after its processes exit. Keep its logs and reports separate
from the disposable cache. Do not immediately start another cold full build
after a disk-space cleanup.

The 2026-10-15 disk-space check found about 31 GiB in the shared Go build
cache with only 2.7 GiB free. Stopping the current verification and clearing
that cache restored about 34 GiB free. That verification was interrupted,
so it is not a passing full-suite result; completed focused checks and the
acceptance reports remain available.

API integration fixtures wait for the scheduler's startup timers before a
test changes their inputs. Without this barrier the startup cost-cap timer
can observe a spend row inserted by the test before its explicit tick, and
the model watch can overlap a routing edit. The two affected tests reproduced
the race; both passed 20 repetitions with the startup barrier.

A test moves the fake clock past an announcement's delay only once the
announcement is queued: the delay counts from the queueing, so an earlier move
is never reached. A scan's finish is queued only after the evaluations it
queued have ended, which can be well after the scan's own run on a loaded
machine. On 2026-10-21 `TestTelegram_Scan_FinishReachesTelegramThroughPolicy`
failed this way while the desktop app ran a scan on the same machine. The test
opened no connection beyond its own loopback servers and no file outside its
workspace; idle, its evaluation ended 6 to 37 ms after the scan's run, and the
test moved the clock 1 to 135 ms after that, so load on the machine decided
the order. It now holds its scan's evaluation until the scan has ended and
waits for the finish to be queued.

## Clean-room rules

Areas listed with treatment `clean-room` in [provenance.md](provenance.md) are
written from their behavioural spec alone.

- The implementer prompt carries the spec and nothing else. It names no
  upstream source file and quotes no upstream code, identifier list or
  comparison excerpt.
- Deny-list for clean-room implementers, checked by reviewers: the old
  repository in full (its source, prompts, templates, tests and data); the
  upstream projects named in the register, in particular their follow-up,
  liveness, scan, evaluation-rubric, doctor, run-status, notification,
  screencast, field-picker and content-extraction code; the nanoMuse source
  tree (GPL-3.0-or-later: its Python runtime, prompts, tests, web console,
  phone apps and relay), whose behavior reaches implementers only through the
  specs; and the reference implementations supplied to the similarity check.
- A provenance reviewer, not an implementer, reads reference code when the
  similarity check reports a finding. The implementer receives the finding
  identifier and a behavioural correction, never code. Renaming identifiers to
  dodge a match does not resolve a finding.
- Every source file in a clean-room area starts with
  `Clean-room: docs/provenance.md#<id>` within its first five lines (the guard
  enforces it). New names, new report structures, new prompt text and new
  templates are expected; do not mirror an upstream layout.
- Exact-file reuse is allowed only where the register says
  `attributed-source`, with a reviewed manifest and notices; it is never
  labelled clean-room.

## Comment rules

- A comment says why the code is the way it is, in neutral terms. It does not
  say what the next line does.
- A comment contains no calendar date, no pull-request, issue or ticket
  reference, and no phrase that attributes a decision to a person or role
  ("approved by", "requested by", "decided by", "at the request of").
  A bare year in a copyright line is fine.
- The rationale of a product policy belongs in `docs/architecture/policies.md`,
  not beside the code.
- All comments, Markdown, prompts and fixtures are English. Test data that
  needs other scripts lives under `testdata/intl/` and is listed in
  `allow.yml`.
- Documentation and tests use reserved example data only: `example.com`,
  `example.org`, `example.net`, `.test` hosts, phone numbers `555-0100` to
  `555-0199`, and the user name `alex` in home paths. Build a synthetic
  address or key inside a test from concatenated pieces so the test source does
  not match its own detector, and mark credential-shaped fixtures with `FAKE`
  or `EXAMPLE`.

## Which tests we keep

The owner's rule (2026-11-10): keep only the tests that are necessary. A test
is necessary when it is the one place that would catch

1. a wrong action on the outside world: an answer written on an application
   form, a submit, a sign-in, a password reset, mail sent, money spent,
   something deleted;
2. loss or corruption of the user's data: migrations, backup and restore,
   workspace and answer-bank writes;
3. a broken contract between parts built separately: the Go and TypeScript
   wire types and shared vectors, the engine and host protocol, CLI exit codes
   and JSON envelopes, MCP tools, the status and shape of HTTP routes;
4. a real incident coming back: one test per incident, at the lowest layer
   that reproduces it;
5. the main flow of a feature end to end: one happy path and its most
   important failure.

Everything else is not written, and is deleted when found:

- the same behaviour asserted again at another layer: the lowest layer that
  proves it keeps the test, and at most one end-to-end test repeats it;
- several tests, or many table rows, that take the same branch: one positive
  and one negative case per rule, in one table;
- tests of trivial code (getters, formatting, constants, string lists) that
  is not a contract;
- tests of code only tests call: the code goes with them;
- tests that pin incidental detail: log wording, internal call order, prose
  the user never sees, timing constants;
- wall-clock sleeps: use the injected clock and bounded waits on a condition.

A matcher route's example table (the questions it answers and the ones it
must leave) is the route's specification and stays. In the gates, the
submission ledger, credentials and the vault, the matcher's guards, store
migrations, backup and restore, and the engine's never-touch rules, a test is
removed only when it is an exact duplicate or cannot fail.

A bug fix starts with one test that reproduces it at the owning layer and
fails before the fix. A gate or refusal test seeds the work the gate would
allow and asserts that nothing happened. Configuration, documentation and
scripts add no tests.

No list in the repository names tests: a test may be renamed, merged or
deleted whenever the behaviour it protects stays covered.

The parity tables under `openspec/changes/migrate-to-go-mygo/parity/` are the
record of the migration. Nothing checks them, and the test names in their
cells are not maintained.
