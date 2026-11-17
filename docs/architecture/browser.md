# Browser lanes

Applysmith applies to jobs and reads some job boards by driving a real browser.
This page says how, in public terms; the measured record of the library and
Chrome follows under "Browser lanes: spike record". The normative text is the
`browser-lanes` spec.

## One managed Chrome, two lanes

- Only your installed Google Chrome (version 120 or later) is driven. Chromium,
  Edge, Brave, headless shells and other engines are rejected, with no fallback.
- Its own profile is `<workspace>/.applysmith/browser/`. A profile that is,
  contains or lies inside your everyday Chrome data directory is refused; your
  everyday Chrome is never driven or changed. It is read in one case only: when
  you ask to import your own sign-in cookies from it, under your own keychain
  approval (see "Importing cookies from an installed browser"). Reading is
  off by default and happens only on that explicit request.
- Chrome starts on demand, once per workspace; after a crash the next server
  adopts the running instance instead of starting a second. Its debugging port
  is a free loopback port verified to belong to the Chrome Applysmith launched.
- The **apply lane** is a normal visible window where applications are filled
  and a tab kept for you waits. The **scan lane** is a covered window per scan
  tab, for reading job boards. A tab belongs to one lane for life; an operation
  on a tab of the other lane fails before any command is sent.
- Sites see a normal Chrome: no automation flag, and
  `applysmith setup apply browser` checks that `navigator.webdriver` is false and
  no user agent is headless.

## Leases, idle stop and recovery

- A run, list or scan that drives a lane holds a **lease**, taken before its
  first operation and released however it ends. The apply lane admits one (a
  second fails with `lane-busy`, naming the holder); the scan lane admits many
  within its tab cap. Release closes the tabs the lease opened and did not keep;
  it never stops Chrome or closes a kept tab. A tab whose creation is not yet
  acknowledged still belongs to the lease, which stays held until that is known.
  `POST /api/browser/stop` and `applysmith browser stop` refuse with `lane-leased`
  while any lease is held, unless forced.
- **Idle stop** after **10 minutes** with no lease, no operation in flight
  (import, probe, print, capture) and no tab in either window: sweep session
  cookies, ask Chrome to close, remove the instance record; the next demand
  launches anew. A tab left in a window, or kept for you, keeps it running.
- **Chrome dies under a lease:** the call in flight fails at once with
  `browser-disconnected` and the event log records the exit status. The lease's
  next call performs **one** recovery (adopt the surviving process if its
  endpoint answers, else launch anew; tabs open before the failure are reported
  lost). A second failure under that lease ends it with `browser-crashed`; a
  clean exit (you quit Chrome) ends it with `browser-closed-by-user` and nothing
  relaunches. A new lease gets a new allowance. A tab creation is the one call
  made again: it acted on no page, so `OpenTabWith` spends the lease's recovery
  at once and creates the tab on the recovered browser, unless its caller was
  cancelled. A released lease never recovers, and one released while its
  recovery is opening the browser takes nothing on the new one: the keeper still reaches a kept
  tab through the handle of the run that kept it, and after a loss that call
  answers `browser-disconnected`. Why: a recovery there put the ended run's
  lease back on the adopted browser, and every later run was refused as
  `lane-busy` (2026-10-28, an ADP tab kept 30 hours earlier).
- **Only the CDP connection drops** (chromedp cancels the root context) while
  the endpoint answers: the instance is marked lost at once
  (`browser_disconnected` event), its record is written even if the scan lane
  never had a window, and the next open or the lease's recovery adopts the same
  Chrome with its kept tabs (`confirmGone`, `exitInfo.Adoptable`). A loss whose
  endpoint no longer answers keeps the record while the process runs; a server
  stop stops that browser unless it holds a kept tab (`stopLost`). Why: a dead
  root made every tab creation fail as "timed out after 45s" until the app
  restarted (seen after 11 hours of a desktop app). A loss during a tab
  creation, or while one given up on may still arrive, marks the record and
  lists the pages the instance knew, because Chrome may make that blank tab all
  the same; the adopter closes the blank pages without an opener that are none
  of those and rewrites the record without the mark (`Record.Abandoned`, flow
  round 7). A tab still `settingUp` (registered, its session not set up yet) is
  left out of that list, so a loss during its setup leaves it to be closed, not
  adopted as the user's (flow round 9, E-2).
- **Sleep:** while any lease is held on macOS, idle and disk sleep are held off
  (`caffeinate -i -m`, bound to the server process), never lid-close sleep.
- **Cookies:** session cookies get an expiry 30 days ahead every minute and
  before every stop, so a sign-in survives a Chrome restart (the site still
  decides whether it is accepted). Chrome has no command to flush its cookie
  store, so a sweep rewrites the marker cookie enough times to cross Chrome's
  batch threshold, which commits the store. The marker `applysmith_managed` is
  written at every launch and adoption and every 5 minutes.
- **Events:** the newest 300 are kept in memory (served by the status route)
  and written as JSON lines to `<workspace>/.applysmith/logs/browser-events.jsonl`,
  rotated to `browser-events.jsonl.1` past 5 MiB. Addresses are reduced to origin
  and path, every field is cut to 400 characters, and a write failure never
  fails a browser operation.

## Command line

    applysmith browser status [--json]   # state, lanes, tabs, newest events; never starts Chrome
    applysmith browser open [url]        # bring the apply window forward, or open an http(s) page in it
    applysmith browser stop [--force]    # graceful stop; refused while a run holds a lane
    applysmith browser check             # sign-in probes

`status`, `open` and `stop` need the app or `applysmith serve` for the workspace.

## Limits

| Limit | Value |
|---|---|
| Tabs | at most 24 in the apply lane and 32 in the scan lane |
| Popups | 4 per 2 seconds per window; extras are closed |
| Pages | `http:`, `https:`, `about:blank`, `blob:` and `data:` only in a tab a run drives; anything else is reverted to a blank page. A tab you opened yourself, or one kept for you, is left alone |
| Permissions | all denied without a prompt, except clipboard write and fullscreen |
| Downloads | apply lane saves to your Downloads folder under a safe name; scan lane cancels them |
| Scan-lane weight | images, media, fonts and a short tracker-host list are not loaded, except on boards that guard against that |
| Focus | runs never raise the window or take focus |

A run closes the tabs it opened when it ends, unless it kept one for you. A
kept tab is detached from automation within 2 seconds and survives until you
close it or quit the app.

## What the browser is used for

| Use | Lane | Notes |
|---|---|---|
| Fill and submit applications | apply | [apply.md](apply.md), policies in [policies.md](policies.md) |
| Read job boards and postings | scan | [browser-scans.md](browser-scans.md), [discovery.md](discovery.md) |
| Check which sites are signed in | scan | `applysmith browser check` loads account pages and reads them |
| Sign-in sync from your everyday Chrome | apply | the extension sends session cookies for chosen hosts only; an undo snapshot is kept for 7 days, in `browser/` ([extension.md](extension.md)) |
| Observe a page's responses | both | apply's submit proof and the scans' data reads |

The profile is excluded from backups and diagnostics ([storage.md](storage.md)).

## Browser panel (live view)

The dashboard's browser panel shows the apply window's tabs beside every page.
The pages stay in Chrome; the panel is a live picture of one tab plus the
user's own input. The routes (`.../live`, `.../input`, `.../navigate`, `POST
/api/browser/lanes/apply/tabs`) and their rules are the `browser-lanes`
requirement "Browser panel live view and user input". Beyond it:

- The stream is `Page.startScreencast` through one viewer session per tab
  (`internal/browser/screencast.go`), shared by every live view of the tab and
  ended with the last; a still every 2 s until the first screencast frame. The
  panel fits the tab before its first frame (a window paints once resized).
- The input route has its own rate class (3,000 a minute) because typing and
  pointing outrun every other control; the panel batches up to 64 events, keeps
  one request in flight and sends hover moves at most every 50 ms. Each event
  of a batch has its own bound: Chrome answers once the page handled it, up to
  a second an event on a busy machine.
- **A tab a run drives takes no input** (`conflict`, `tab-driven`); the panel
  only watches it, and nothing in the panel presses anything for the user.

Limits of a picture with forwarded input, shown in the panel where they bite:

- **File pickers** cannot be used through the panel. A user press turns
  file-chooser interception on in the viewer session for one second; a picker
  it opens is announced (`filechooser`) and the panel offers "Open in Chrome
  window", where the picker works as usual (interception is off by then).
- **Text input.** A hidden text field is the source of text: what it holds once
  no composition runs (typed, composed, pasted) is inserted (`Input.insertText`)
  and cleared, so composed input arrives whole. The field's own listeners read
  it (input, beforeinput, compositionend a task later, a key up outside a
  composition), so a lost compositionend never holds text back; it is fixed in
  place, so WebKit's caret reveal scrolls nothing behind the panel. Only Enter,
  Backspace, Delete, Tab, Space, arrows, Home, End, Page Up/Down and Ctrl or Cmd
  shortcuts go as keys, never inside a composition (229).
- **Native pop-ups** (a `<select>` list, a page's `alert`, a permission prompt,
  Chrome's own autofill) are drawn outside the page, so not in the picture; the
  keyboard still works on a focused `<select>`; the rest need the Chrome window.
- **Fit.** `fit` sets the apply window's contents to the panel's size
  (`Browser.setContentsSize`), a real window, because an emulated viewport's
  metrics would disagree with the window's and bot checks see that. Never while
  a run drives the tab or holds the apply lane; a run taking the lane, or a
  stop, restores the earlier size. A tab that is not its window's active one
  keeps its size (measured headless) and is scaled.
- **Wheel** never scrolls the page behind; an inert view ignores it.

## Verify

`applysmith doctor --only browser` shows the apply window, the fingerprint check and
the sign-ins. `make test-browser` runs the opt-in headless proofs across browser,
engine, extension, apply, discovery and API packages and the replay command; the API cases exercise
active-tab autofill and real-profile session import/undo through real routes,
plus process-group shutdown through the actual server lifecycle; the command
line's cases check that a one-shot command stops the Chrome it launched,
checking and stopping only the PIDs its shim recorded. Extension cases
require Chrome for Testing ([extension.md](extension.md#real-browser-check));
missing test browsers are skips, not acceptance. Visible-window and
dashboard-layout checks have separate targets.

Each `make test-browser` (and `-headed`) run sets `TMPDIR` to its own
`/tmp/applysmith-tb.*` directory, holding every test profile, and its final sweep
kills only Chromes whose `--user-data-dir` is inside it, so two runs at once
(two worktrees) leave each other's browsers alone. The path is short because
Chrome may put its singleton socket there and a socket path is limited to 104
bytes on macOS.

Panel typing in the desktop app is checked with System Events `keystroke`, app
frontmost (it passes the input method). Keys posted to the process (a
computer-use tool's `press_key`) skip the input method or reach no field, and a
System Events `click at` is an accessibility press at the element's centre, so
pointing goes through a computer-use tool's `click`; the result is read from the page over the apply
window's DevTools port. A refused input batch is a WARN line ("the panel's
input was not taken") with the browser's cause in `server.log`.

Wheel input has its own params type (`wheelParams`): Chrome refuses a
`mouseWheel` without both `deltaX` and `deltaY` (-32602), and cdproto's
`DispatchMouseEventParams` omits a zero delta, which broke every straight
vertical scroll. A headless test Chrome never acknowledges a wheel, so the e2e
tests send none; `TestBrowser_WheelSendsBothDeltas` checks the params.

---

## Lessons from an agent browser (Codex desktop, studied 2026-10-18)

The Codex desktop app's (Codex 26.1013) bundle and agent rules were read
without running anything; its agent drives a browser beside the conversation.
Its pages are `<webview>`s in the app's DOM, so it draws over the page. Ours
live in Chrome, which our web view cannot embed, and nothing may be injected
into an ATS page, so the panel draws over a live picture of the tab, as Codex
does for its cloud browser.

- **Adopted** (`docs/architecture/dashboard.md`): the panel's tab strip marks a
  run-driven tab with a cursor and highlight (Codex swaps the favicon for a
  cursor glyph) and the Browser page names each tab's holder; a live view is
  inert behind "Click anywhere to use the browser" until clicked, and a
  run-driven tab is never activated.
- **To keep:** after a take-over (observing, requesting, controlling; "Take
  over", "Return control") Codex reads only the final page and acts on nothing
  in between; our resume after a hand-over should keep that rule.
- **Also seen:** the agent's cursor is paced only when watched (up to 1.5 s per
  move, window focused, tab visible); a budget of 32 live pages never suspends
  the active, shown, driven, loading, audible or downloading tab.
- **Candidates, not built, by value:**
  1. A submitted application's receipt tab kept as the result. Codex tabs close
     at turn end unless `handoff` (kept for a later step) or `deliverable`
     (kept as the result); ours has the first two as `owner` and `kept`.
  2. A held submit's confirmation carrying the review page's screenshot and the
     data sent. Codex confirms high-stakes submissions (its rules name job
     applications) at the moment of action, in one grouped confirmation showing
     exactly what is approved, the data and the destination.
  3. An approval card for the Gmail code fill. In Codex secrets never reach the
     model: sign-in sends field labels and selectors, the app fills the values
     and blocks the agent's reads meanwhile; an e-mailed code needs a card
     ("... cannot see the code. You can also enter it yourself.").
  4. Failure names that keep network errors out of the bot-block counts. Codex
     bot blocks (`captcha_failed`, `access_denied`, `challenge_loop`,
     `sign_in_blocked`) never include DNS, TLS, time-outs or a bare 403, and
     load errors get a page per cause.

# Browser lanes: spike record

Record of the Phase 1 browser spike (tasks 1.2 to 1.4) for `internal/browser`,
measured on the machine and builds named here; nothing is carried over from the
previous product.

## Gate status

| Task | Status |
|---|---|
| 1.2 launch and attach | proofs pass |
| 1.3 two windows, lane ownership, caps, allowlist, permissions, popup budget, downloads | proofs pass |
| 1.4 spike gate | proofs (a) to (h) pass with the covered policy and the amended requirements (below). Locked-screen evidence for the covered policy is recorded under the policy table. |

The browser-lanes spec was amended to what Chrome 155 does: window per scan
tab; runs never activate (only the explicit show-window operation does); scan-lane
stubs for popups and dialogs; clipboard write only inside a gesture; page
navigation to a local scheme detected through the console error; permission
list limited to the names `Browser.setPermission` accepts.

Reproduce: `make test-browser` (needs Google Chrome; the tests carry the
`browser` build tag, so `go test ./...` stays offline). The harness passes
`--headless=new` through a test-only option (`Options.WithHeadless`, compiled
only under the `browser` tag), so no window opens; the product launch never
passes it. Tests about the window itself (focus, minimized windows, a visible
Chrome's identity) run only under `make test-browser-headed`.

## Environment

| Item | Value |
|---|---|
| Google Chrome | 155.0.8059.40 (`/Applications/Google Chrome.app`) |
| DevTools protocol | 1.3 (`/json/version`), V8 15.5.35.20 |
| macOS | 26.2 (Darwin 25.2.0, arm64) |
| Go | 1.27.1 |
| chromedp | v0.20.1 (MIT) |
| cdproto | v0.157.9 (MIT) |
| chromedp/remote | v0.2.0 (MIT); brings gobwas/ws v1.4.0 (MIT) |
| Screen state | unlocked for every focus result below; the locked-screen run is described under the policy table |

chromedp's websocket transport is the separate `chromedp/remote` module, so
chromedp, cdproto and chromedp/remote are direct dependencies. go-rod was not
needed: no proof failed with chromedp and passed elsewhere. Every limitation
below is a Chrome behaviour, reached through the DevTools commands any client
sends.

## Chrome version floor

Only branded Google Chrome of major 120 or newer is accepted (`MinChromeMajor`
in `internal/browser/chrome.go`, the single constant); the spike ran on 155.
Chromium, Chrome for Testing, Edge and headless shells are `chrome-unsupported`,
older Chrome `chrome-too-old`. The version probe (`--version`) runs with the
browser's allowlisted environment.

## Decisions

- **Library:** chromedp, attached through `remote.NewAllocator` to a Chrome the
  server launched. No exec allocator, so none of its default flags
  (`--enable-automation`, headless, password store, remote debugging port zero,
  pipe) reach the browser. The package exposes no library types.
- **Scan-lane background policy: covered, unminimized, one window per scan
  tab.** Each scan tab gets its own normal-state window from
  `Target.createTarget` (`newWindow`, `background`); Chrome orders a background
  window behind every other window, so it stays covered by the user's work and
  the foreground application does not change. Focus emulation is on for every
  managed tab. Minimized was rejected (proof g): it renders but cannot be
  captured. A shared scan window (the spec's fallback is one window per tab) is
  not reachable without raising Chrome: a script-opened tab and
  `Target.activateTarget` both bring Chrome forward, and `Target.createTarget`
  with `newWindow` false uses the most recently active window, not necessarily
  the lane's.
- **Scan-lane stubs.** Chrome activates the tab of a script dialog or
  script-opened window before DevTools can answer, raising Chrome. Each scan tab
  (invisible to the user) gets a document-start script
  (`Page.addScriptToEvaluateOnNewDocument`): `alert` does nothing, `confirm`
  returns false, `prompt` null, `beforeunload` handlers are ignored, and
  `window.open` returns null after reporting the URL through a binding whose
  random name the script deletes from the page. The answers equal the dialog
  policy. It covers the tab's own process (an out-of-process iframe needs the
  script once the engine attaches to it); a `beforeunload` set through a body
  attribute is not intercepted. The apply lane keeps real dialogs and popups,
  which may bring Chrome forward (accepted).
- **Apply tabs** join the apply window with `newWindow` false and `focus` false;
  the window is checked and a misplaced tab is replaced by one in a new apply
  window. An apply window whose last tab closed before the server heard of it
  ("no browser is open") gets the tab a new apply window too.
- **No run ever activates a tab** (`Target.activateTarget` always takes the
  foreground). `LaneHandle.ShowWindow` is the one activation, for an explicit
  user action only; it refuses scan tabs.
- **Graceful stop:** chromedp refuses `Browser.close` on an attached browser, so
  the server re-verifies the process start time, sends SIGTERM, waits 10 s,
  kills the process group, waits 5 s more, and reports `closed`, `killed` or
  `detached`.

## Launch argument list

Built by `launchArgs`, which pairs each argument with its reason (the table
below is written by hand); a test checks that no argument is forbidden. Chrome is started detached
(own session), with an allowlisted environment (`PATH`, `HOME`, `USER`,
`LOGNAME`, `LANG`, `LANGUAGE`, `LC_*`, temporary-directory and display
variables; anything named `APPLYSMITH_SECRET_*` or ending in `KEY`, `TOKEN`,
`SECRET`, `PASSWORD` is dropped) and with standard error captured.

| Argument | Reason |
|---|---|
| `--user-data-dir=<profile>` | Dedicated profile under the workspace; Chrome 136 and newer refuses a debugging port on the default data directory. |
| `--remote-debugging-port=<port>` | Explicit nonzero loopback port chosen by the server. Port zero turns on the automation indicator and makes `navigator.webdriver` true. |
| `--no-startup-window` | No window at launch, so a scan run never shows an apply window and each lane creates its own window on demand. |
| `--no-first-run` | No first-run experience on an unattended launch. |
| `--no-default-browser-check` | No default-browser prompt. |
| `--disable-background-timer-throttling` | Background tabs keep full-rate timers. |
| `--disable-renderer-backgrounding` | Renderers of background tabs keep normal priority. |
| `--disable-backgrounding-occluded-windows` | A covered window is not treated as hidden. |
| `--disable-features=MacWebContentsOcclusion,WebContentsOcclusion,CalculateNativeWinOcclusion,Translate,MacAppCodeSignClone` | Window-occlusion detection off so a covered or locked-screen window keeps rendering; no translate bar; no app-bundle copy at launch (below). One flag, because Chrome keeps only the last `--disable-features`. |
| `--password-store=basic` | No Keychain use on an unattended first launch. |
| `--use-mock-keychain` | Pairs with the previous flag. |
| `--hide-crash-restore-bubble` | No restore prompt after an unclean exit. |

`MacAppCodeSignClone`: at every launch Chrome copies its whole app bundle
(about 1,340 files) to
`$(getconf DARWIN_USER_TEMP_DIR)/../X/com.google.Chrome.code_sign_clone/`.
Only a normal quit (`Browser.close` or quitting the app) starts the
`--type=code-sign-clone-cleanup` helper that deletes the copy. SIGTERM,
SIGKILL or a crash leave it there for good. The stop paths that end in a
signal (the `askToClose` fallback, `retire`, `terminate`) and the test
harnesses that kill Chrome on purpose had left 10,614 copies by 2026-11-06
(14 million files, more than half of the disk's files), adding about 20 an
hour while the browser suites ran. With the feature disabled Chrome makes no
copy, so no exit path can leave one. The chromedp harnesses in
`internal/extension` and `internal/cli` pass the same feature.

Refused outright (tests assert it; only the test-only headless option adds `--headless=new` after the check): `--enable-automation`, `--headless*`,
`--remote-debugging-pipe`, `--remote-debugging-port=0`,
`--remote-debugging-address`, `--remote-allow-origins`, `--load-extension`,
proxy flags, `--user-agent`, `--lang`, `--disable-blink-features`,
`--no-sandbox`. `--host-resolver-rules` is added only by tests.

Keychain flags: an unattended first launch without them was not observed, so
their removal is not shown to be safe and they stay.

Consequences:

- `--password-store=basic` with the mock keychain stores saved passwords
  without operating-system encryption, protected only by the profile
  directory's mode 0700 (set at every launch) and the account's file
  permissions; the managed profile should not hold credentials the user would
  not keep in a file of that mode.
- The DevTools port has no authentication: any process of the same user can
  drive the browser. Web pages cannot: the endpoint answers on loopback only
  (checked at launch and adoption) and `--remote-allow-origins` is refused, so
  a page's WebSocket handshake is rejected (proof (h), `TestSpike_H4_...`).
- chromedp enables `Runtime` on every session it attaches; `Runtime.enable` is a
  known bot-detection signal. Accepted until a site is shown to react to it; the
  fingerprint document, when it exists, must list it among what the browser
  tells a site.

## Proofs

Each row is an automated test in `internal/browser` unless marked manual.

| Proof | Result | Evidence |
|---|---|---|
| Task 1.2: `navigator.webdriver === false`, Chrome's own UA | pass | `TestE2E_Browser_WebdriverFalseOnExplicitPort`: webdriver `false`, UA `...Chrome/155.0.0.0 Safari/537.36`. `TestE2E_Browser_IdentityIsNotRewritten`: `User-Agent` header equals `navigator.userAgent`; `Sec-CH-UA`, `-Mobile`, `-Platform` present. |
| (a) injection into a cross-origin iframe | pass | `TestHooks_CrossOriginIframeAndBindingRoundTrip`: the other-site iframe is its own `iframe` target; an isolated-world script fills `#name`, sees `isTop` false and the frame's origin; the embedding page cannot read the frame. Hosts must differ in registrable domain (`a.example.test`, `b.sitetwo.test`); two names under one parent are one site and share a process. |
| (b) `Runtime.addBinding` round trip | pass | `TestHooks_CrossOriginIframeAndBindingRoundTrip`: binding bound to an isolated world by name; 1 MiB payload in, 1 MiB reply out; handler error rejects the page promise; the main world has no binding and finds none; the same binding in the iframe reports that frame's id and origin. |
| (c) `DOM.setFileInputFiles` | pass | `TestHooks_SetFilesInCrossOriginFrame`: a file input in the cross-origin frame; it reports name, size 19, type `text/plain`; `change` fires. |
| (d) trusted click and typing | pass | `TestSpike_D_...`: `isTrusted` true for click and `keydown`; inside the iframe through top-level coordinates plus the frame offset. The first click on a freshly loaded out-of-process frame can be dropped, so the click primitive must verify and repeat. |
| (f) response observation | pass | `TestE2E_Browser_ObservationRecordsBounded`: POST 200 with body, POST 422 with the error body; the record holds no header, cookie or request body. Network events are off outside the observed window. |
| (g) background policy without stealing focus | pass for covered; fail for minimized | See the policy table. |
| (h) concurrent background tabs keep rendering | pass | `TestSpike_H1_...`: four scan tabs in four windows, each DOM update + extraction + screenshot 108 to 167 ms after load. The spike run measured 60 fps per tab; the test does not sample the frame rate, and the foreground is checked by the headed tests (`TestSpike_G_...`, `TestSpike_H3_...`). |
| (h) shared window | not used (amended) | Measured once: an inactive tab of a shared window renders (63 fps) under focus emulation, but the window cannot be built without raising Chrome. |
| (h) a run never activates a tab (amended from "activation never raises") | pass | `TestSpike_H3_...`: a second apply tab opens in the background, foreground unchanged; the inactive first tab still renders, takes input and is captured. Measured once why: foreground "Claude" before, "Google Chrome" after `Target.activateTarget` on an apply tab. |
| (h) a web page cannot reach the DevTools endpoint | pass | `TestSpike_H4_...`: cross-origin `fetch` of `/json/version` blocked; `no-cors` gives an opaque response; `new WebSocket` to the real browser address refused; raw upgrade with a foreign `Origin` answered `403`; foreign `Host` answered `500` ("Host header is specified and is not an IP address or localhost"); GET `/json/new` answered `405` (PUT only). A plain GET with a foreign `Origin` is answered `200`: Chrome relies on the browser's CORS to withhold the body from a page, so only a non-browser client can read it. |
| (h) no auto-attached target left paused | pass | `TestSpike_H5_...`: a popup, an out-of-process iframe and an apply tab created together all ran their first script within 2 s; no attached-to-target event had `waitingForDebugger` set. |

### Background policy evidence

| Policy | Rendering | DOM update, extraction | Screenshot | Foreground |
|---|---|---|---|---|
| Minimized window, focus emulation | 61 fps in the probe, but 3 fps right after creation in one run | works | **times out (5 s and 10 s)** with `fromSurface` true or false | unchanged |
| Minimized window, no focus emulation (screen locked) | no frames at all | works | not tried | n/a |
| Covered, unminimized, focus emulation (selected) | 59.9 to 62 fps | 108 to 582 ms after load | works, 4 colours in the probe page | unchanged (Terminal before and after; also with Claude in front) |

The minimized rows were measured once and are not repeated by a test.
`TestSpike_G_...` and `TestE2E_Browser_LaunchDoesNotStealFocus` assert the
selected policy and that launching, creating windows in both lanes and
navigating leave the foreground application alone (skipped on a locked screen,
where the login window is frontmost).

Locked screen (manual evidence): the final `make test-browser` and a focused
rerun ran locked (`locked=true` in the probe log). The covered policy passed:
one tab 156 ms from load to DOM update, extraction and non-blank screenshot at
59.8 fps; four concurrent tabs 123 to 176 ms at 60 fps; trusted input and
every other test passed. The minimized window again rendered (62
fps) and failed the screenshot (5 s timeout). Frontmost-application checks are
vacuous when locked and are skipped, so focus results come from the unlocked
runs. The Phase 1 exit check may repeat the locked run.

## What raises Chrome, measured

| Action | Raises Chrome over another application |
|---|---|
| Launch with `--no-startup-window`, `Target.createTarget` with `newWindow` and `background` (scan) or `focus` false (apply) | no |
| `Target.createTarget` with `newWindow` false and `focus` false | no |
| Trusted mouse and key input, file chooser interception, navigation, evaluation, screenshots | no |
| Script-opened tab (`window.open`) or a page popup in an apply tab, or opened from an isolated world in a scan tab | **yes** (H2a) |
| `Target.activateTarget` | **yes** (H2b) |
| Any JavaScript dialog (`alert`, `confirm`, `prompt`) in an apply tab | **yes**, though DevTools answers within milliseconds: Chrome activates the tab first |
| The same dialogs and `window.open` in a scan tab | no: stubbed (`TestE2E_Browser_ScanStubsKeepDialogsAndPopupsQuiet`) |

That test loads a page opening windows and raising dialogs on load and from a
timer, and asserts nothing opens, the answers are null, nothing, false and
null, both popup URLs are recorded and the foreground is unchanged. Apply-lane
popup and dialog tests are in `zz_popups_test.go`, the last file, so the
foreground checks elsewhere run first.

## Windows, permissions, downloads, dialogs

| Check | Result | Test |
|---|---|---|
| Apply and scan tabs land in different windows; each scan tab has a window; a second apply tab joins the apply window; all windows are normal state | pass | `TestE2E_Browser_TabPlacement` |
| Window closed by the user is recreated by the next tab | pass | `TestE2E_Browser_WindowClosedByUserIsRecreated` |
| `wrong-lane` before any command, for navigate, evaluate, capture, show, close | pass | `TestE2E_Browser_LaneHandle_WrongLane` |
| Apply popup takes the opener's lane, run and window; a window opened from a kept tab is the user's (kept, no run, no lease, no popup budget) | pass | `TestE2E_Browser_PopupInheritsLane`, `TestBrowser_APopupOfAKeptTabIsTheUsers` (labelling only) |
| User-opened tab is apply lane, ownerless, survives run cleanup; kept tab survives | pass | `TestE2E_Browser_UserOpenedTabIsApplyLaneAndSurvivesCleanup` |
| A kept tab refuses commands; `Reclaim` (the user's Done) takes it back with its values, `Keep` detaches it again, a closed tab cannot be reclaimed | pass | `TestBrowser_Reclaim_ATabKeptForTheUserTakesCommandsAgainAndKeepsItsValues` |
| Tab cap (`lane-tab-limit`, no tab created) | pass at a lowered cap; caps are 24 and 32 | `TestE2E_Browser_TabCapReached` |
| Popup budget, 4 per 2 s per window | pass: 4 of 10 remain | `TestE2E_Browser_PopupFlood` |
| URL allowlist: system navigation refused for `file`, `chrome`, `devtools`, `chrome-extension`, `view-source`, `javascript`, `ftp`; `http`, `data`, `about:blank` allowed | pass | `TestE2E_Browser_URLSchemeAllowlist`, `TestBrowser_SchemeAllowlist` |
| Page navigation to `chrome://` or `file://` | Chrome refuses silently, with no navigation event; on the only signal, the console error `Not allowed to load local resource`, the tab goes to `about:blank` and `navigation_blocked` is recorded | `TestE2E_Browser_PageNavigationToBlockedSchemeReverted` |
| Permissions: 33 descriptor names Chrome accepts are denied; geolocation error code 1; `permissions.query` reports `denied` | pass | `TestE2E_Browser_PermissionsDeniedAndClipboardWriteAllowed` |
| Clipboard write | per the amended spec: refused without a user gesture, allowed inside one. Chrome ties a gesture-free write to the read-write permission, which would also allow reads. | same test |
| Dialogs (apply lane): `confirm` and `prompt` dismissed within 1 s, `alert` accepted, answers recorded; a dialog gone before the answer is swallowed; a kept tab's dialog is left alone | pass | `TestE2E_Browser_DialogPolicy`, `...DialogAlreadyGoneIsSwallowed`, `...KeptTabDialogLeftToUser` |
| File chooser interception | pass (event recorded; no operating-system dialog is observable from a test) | `TestE2E_Browser_FileChooserIntercepted` |
| Downloads by lane: apply saves `offer-letter.pdf`, `offer-letter (1).pdf`; scan is cancelled and leaves no file; staging stays empty, and a launch (not an adoption) empties what an earlier browser left staged | pass | `TestE2E_Browser_DownloadPolicyByLane`, `TestBrowser_DownloadStaging_AFreshLaunchRemovesWhatWasLeft` |
| Hostile suggested name | Chrome already rewrites `../../.ssh/config` to `_.._.ssh_config` before the event (no separator, no leading dot, inside Downloads). `SafeDownloadName` itself maps `../../.ssh/config` to `config`. | same test, `TestBrowser_SafeDownloadName` |
| Interrupted download leaves nothing in Downloads | pass | `TestE2E_Browser_IncompleteDownloadLeavesNoFile` |

## Ownership, adoption and the bind race

| Check | Result | Test |
|---|---|---|
| Endpoint found by probing `/json/version` on the selected port only; no `DevToolsActivePort` needed | pass | all e2e launches |
| Owner check: every listener on the port is the launched pid (`lsof`) and bound to a loopback address (`lsof -Fn`), not only the advertised `ws://127.0.0.1:<port>/...`; start time unchanged (`ps lstart`); the command line carries `--user-data-dir=<profile> --remote-debugging-port=<port>` as whole adjacent arguments (port 9222 does not match 92220) | pass | `verifyEndpoint` |
| Port taken before Chrome binds: the foreign listener receives no bytes and stays up, the child that lost the race is terminated, the retry uses another port | pass | `TestE2E_Browser_PortStolenBeforeChromeBinds` |
| Reused pid, other start time, other profile, other port, pid that is not the listener, dead pid: adoption fails closed | pass | `TestE2E_Browser_ReusedProcessIdentifierRefused` |
| Abrupt server death: Chrome keeps running, the next server adopts it, the apply tab and lane labels survive, orphan scan tabs close after 5 s | pass | `TestE2E_Browser_AdoptionAfterAbruptServerDeath` |
| Early exit reports the first 400 characters of standard error; deadline kills the child | pass | `TestBrowser_Launch_EarlyExitReportsStderr`, `...TimeoutTerminatesChild` |
| Stale `Singleton*` removed, live holder is `profile-in-use`, everyday profile (equal, inside, containing, symlinked) is `profile-unsafe` | pass | `internal/browser/profile_test.go` |
| Graceful stop `closed` in about 70 ms; ignores SIGTERM then `killed` | pass | `TestBrowser_Stop_*` |

## chromedp behaviours to keep in mind

- A browser connection and a tab session live as long as the context of the
  first command that starts them, so that command carries the long-lived
  context and the wait is bounded around it (`Instance.connect`, `attachWithin`).
- Every attached tab gets Log, Network, Inspector, Page, DOM, CSS and Runtime
  enabled. Network is switched off right after attaching and on only around a
  navigation or observation. Chrome answers the CSS enable only once the page's
  resources have loaded, so attaching to a loading page takes the rest of the
  load (up to 15 s for a one-line page on a busy machine): an attach is bounded
  like a page load (`attachDeadline`) and by its caller.
- `WithDetachOnCancel` leaves the tab open when the context ends, as a kept tab
  needs.
- A redirect is a second `Network.requestWillBeSent` of the same request,
  carrying the answer to the hop before it; only the last hop gets an answer
  event. A response watch records every hop, so a POST answered 302 stays a
  write with that status and the page it led to is a second record. Event
  kinds are handled in any order, so a write whose answer came from another
  address than it was sent to is held until the hop's event is in, never
  recorded with that page's status.
- A response watch listens on the tab's own session only. A request made
  inside an out-of-process frame goes to that frame's session, where the hooks
  switch Network off, so the submit POST of a form embedded cross-process is
  not seen: for such a form a missing write is no evidence either way.
- `wrapOp`, which types every failed DevTools operation of a tab, reports a
  script that threw (`*chromedp.ExceptionError`) as `eval-failed` before it
  checks the browser, and a navigation under the call ("Inspected target
  navigated or closed", or V8's "Execution context was destroyed" and "Cannot
  find context with specified id") as `document-replaced`, never as the
  `browser-disconnected` a page's own TypeError on a reloading challenge page
  was once reported as (flow round 9, E-3). Any other failure of a browser
  and tab still there is `operation-failed`, which spends an evaluation's
  attempt (flow round 10, EF-1); `Hooks.mapErr` files it the same way, so a
  fill counts it as its own defect and only a lost browser as the machine's.

## Not covered by tasks 1.2 to 1.4

Left for later tasks: session-cookie persistence and import, the marker cookie,
idle shutdown, crash recovery and leases, the scan resource policy, the sign-in
probe, the event log file, doctor checks, kept-tab detachment, credentialed
fetch, popup URL capture, the power assertion, viewport repair for a zero-area
tab, and the HTTP routes. `tabs.go` and `targets.go` hold the per-tab policy
these extend.

## Hardening after review

- **Environment:** every Chrome exec, the version probe included, gets an
  allowlisted environment that is never nil (a nil `Env` inherits the whole
  parent environment). Tests cover the probe, the launch and an empty allowlist.
- **Everyday profile:** compared case-insensitively on macOS and Windows and by
  file identity (`os.SameFile` up the ancestors), so
  `application support/google/chrome/x` is refused. A record whose profile is
  not the configured canonical profile or overlaps the everyday one is ignored
  and removed, and a fresh launch follows.
- **Owner check:** tightened as in the ownership table above.
- **Signals:** a reaped launch is never killed; a force kill re-verifies the
  pid's start time and signals the group only when the process leads it. A stop
  whose context is already cancelled still sends SIGTERM and escalates at once
  instead of waiting 10 s.
- **Error text:** a launch failure's standard-error excerpt has any WebSocket
  address replaced by `ws://[redacted]`.
- **Windows:** unsupported for now. A launch and an adoption fail with
  `platform-unsupported` before spawning anything, because the process identity
  and group checks have no Windows implementation. The package still
  cross-compiles.
- **First use:** `Open` is single-flight per profile, keyed by the canonical
  profile directory (a link or `..` spelling shares the launch); concurrent
  callers share one launch and one `Instance`. The dashboard's show and
  open-address paths put one 30 s budget (`firstUseBudget`) over the launch and
  the first window and fail as `launch-timeout`. The
  instance record is written once both lane windows exist, per the spec. With
  no record, the live `SingletonLock` holder is adopted (or retired) when its
  command line carries this profile and a debugging port whose endpoint is its
  own (Chrome writes no `DevToolsActivePort` for an explicit port); any other
  holder is `profile-in-use` and never signalled.
- **Adoption failures:** the record is removed only when the recorded process is
  gone or is not the one recorded (another start time, command line or
  profile). A process the record proves ours but unusable (another Chrome major
  after an on-disk update, a debugging port another process owns) is asked to
  quit, then killed after the identity check so its profile lock does not
  outlive it, and a fresh launch follows; the retirement is a `browser_retired`
  event with its reason, and one that will not quit (`StopDetached`) keeps its
  record and fails the open with `profile-in-use`. A process-tool failure or a
  slow version request says nothing about the browser: the record stays and the
  caller gets `browser-disconnected`. An adopted process's exit watcher checks
  the pid's start time every few seconds and stops when the connection is lost or dropped, so a recycled
  pid neither keeps it polling nor reports a false exit. When the browser exits
  on its own, every remaining tab fires its `OnTabClosed` callbacks.
- **Known limitation:** a browser alive but hung (holds the profile, answers
  nothing) is not escalated: the version request times out as
  `browser-disconnected`, the record stays, and every open fails so until the
  process is killed by hand.
- **Test-only host mapping:** `hostRules` is unexported; tests in other packages
  set it through `Options.WithHostRules`, which exists only under the `browser`
  build tag.

## Frame-aware hooks (task 1.7)

`LaneHandle.Hooks(tab)` returns the tab's hooks (`hooks.go`, `hooks_eval.go`),
the engine package's only way into a page; `internal/browser` stays the only
importer of chromedp.

- **Frames:** the tab's frame tree plus that of every `iframe` target descending
  from it (a cross-origin frame is a separate target whose id is its frame id;
  the page session's tree lists only same-process frames). A frame carries id,
  parent, origin, origin-and-path URL and `IsTop`.
- **Worlds:** `Page.createIsolatedWorld` returns the same context for the same
  document, so a world is created on every call and only a new context id
  installs the binding wrappers. A destroyed context surfaces as
  `document-replaced`. A same-process child frame's main world needs its context
  id, learned by switching `Runtime` reporting off and on once. That switch
  drops the session's bindings, so each is removed and added again after it
  (a failed switch included), and a scan tab's popup binding is added again
  and its name deleted from the loaded main worlds. Unit-tested on the
  command sequence only (`TestHooks_BindingsAreRegisteredAfresh...`).
- **Bindings:** `Runtime.addBinding` with `executionContextName` creates a raw
  function (`<name>$n`) in the named world only; a per-world wrapper turns it
  into the promise-returning function the engine calls and keeps pending calls
  inside the world. Replies are a settle call evaluated in the calling context.
  A call is bounded by 120 s and 10 MiB each way; a late reply is dropped.
  Registering a binding again replaces its handler (one owner, the newest page);
  the same name for another world is `binding-conflict`.
- **Release:** the hooks end with the tab session; out-of-process frame sessions
  end with the hooks. `Hooks.OnTabClosed` is separate: it runs once when the tab
  closes (by the caller, the user, or a browser stop), also for a kept tab
  detached earlier. Creating one tab's hooks is serialized, so a lost race
  leaves no event listeners behind.
- **Deadlines:** listing out-of-process frame targets carries the caller's
  deadline, so a stalled connection ends a frame lookup with the call.
- **Remote objects:** `SetFiles` evaluates its locator in its own object group
  (unique per call) and releases it after the file is set.
- **Document-start scripts:** `Hooks.AddInitScript` registers a main-world
  script for every document of the tab and of its out-of-process frames
  (`Page.addScriptToEvaluateOnNewDocument`, run at once in the documents
  already there); the engine's submit observer (`Page.ObserveSubmissions`)
  and `ConsoleLines`, whose script records what a page prints, use it. The
  hooks themselves subscribe to no console events.

## Engine host rules (internal/engine)

- **Origin pin:** a sub-frame is admitted with its document's origin
  (`location.origin`, as the binding wrappers report it). The engine starts there
  through one evaluation that compares the origin first and answers
  `origin-changed` without starting, so a document that moved between decision
  and start never receives the applicant snapshot. A binding call from a frame
  whose origin differs from the admitted one is rejected. A frame whose origin
  cannot pin it (opaque `null`, empty, or an unreadable location reply) is never
  admitted: the guard would pass for any other such document.
- **Top frame pin:** a fill's first attempt pins the top document's origin;
  every later attempt (after a replaced document) starts only where the tab
  still is, with the same single-evaluation guard and binding check. The test
  is the origin (scheme, host, port), not the site: a move between two hosts of
  one company is refused too. A moved tab ends the fill with
  `top-origin-changed` and nothing is typed there. A top page with an opaque
  origin is not filled (`inject-failed`).
- **Registry frames are decided again:** a registry-admitted frame whose
  document is replaced during a run is classified afresh. Only another registry
  document is filled again; any other outcome, a probe-admissible form
  included, ends the frame (reported unreachable when a form is there) rather
  than reusing the earlier admission.
- **Frames not reached:** after the walk the host hands every frame decision to
  the top frame's engine (`frameReport`), which logs the roll-up and one line per
  frame not reached and returns one `frame-unreachable` learning event (tag
  `iframe`, the frame's origin and path, the failure outcome) per frame the fill
  could not enter; these join the top summary's events. A page with no sub-frame
  skips the call; a failed call is logged and costs the fill nothing.
- **Native uploads:** a file the engine cannot attach itself is written to
  `<StageDir>/<run id>/<n>/<name>` (directories 0700, files 0600; one directory
  per document so equal names never collide; separators and control characters
  replaced, never empty, `.` or `..`) and set with `DOM.setFileInputFiles`.
  Chrome reads it when the page uses it, possibly long after the fill (an upload
  request, a native form submit), so it stays until the tab closes
  (`Hooks.OnTabClosed`). A kept tab's input keeps pointing at the staged file, so
  an owner calls `Page.Close` only for a tab it is closing: a tab that stays with
  the user (kept, handed back by a resumed run's `reclaimedTab.Close`, or the
  user's own tab an `adoptedTab.Close` lets go of) keeps its files until it
  closes, and the browser stop when the app quits marks every tab closed, which
  runs the same callbacks (flow round 5). Staging after `Close` is an error. A
  page removes run directories older than 24 hours when created (a server that
  died with a tab open), never one a live page of the same process owns, however
  old. `StageDir` belongs under the workspace state directory.
- **Hand-over:** an isolated world outlives the DevTools session that made it
  (a later session reaches the same engine), so before the lane lets go of a
  tab (`laneTab.Keep`, and `reclaimedTab.Close` when a resumed run ends)
  `Page.Dispose` stops and removes the engine and the main-world helper in every
  frame, and a run that takes the tab back injects both again
  (`TestE2E_Handoff_AKeptTabRunsNoneOfTheRunsScriptsAndTheRecorderStillRecords`).
- **Failure codes** of a fill: `navigation-limit`, `engine-hung`, `tab-closed`,
  `browser-lost`, `applicant-unavailable`, `start-refused`, `start-lost`,
  `inject-failed`, `poll-failed`, `bundle-missing`, `fill-in-progress` and
  `top-origin-changed`.
- **Page deadline:** counted once from the start of the fill; a reinjection gets
  what is left.

## Sign-in sync (task 4.4)

`Sessions` (`sessions.go`) copies sign-ins the extension read from the everyday
Chrome into the managed profile; it satisfies `api.SessionBrowser`. It reaches
the profile only through `Storage.getCookies` and `Storage.setCookies`
(measured on Chrome 155): with no browser-level delete, a cookie is deleted by
setting it with a past expiry; a domain without a leading dot makes a host-only
cookie; and `setCookies` acknowledges a `SameSite=None` cookie without `Secure`
and then drops it, so every import reads the profile back.

- **Sites:** the probe's site data (`SignInSites`, each with its sign-in and
  account address), stored sign-in accounts (the credential index's
  `StoredAccounts`, wired in `internal/launch/sessions.go` once the database is
  open) and the hosts holding cookies in a running profile. Listing never starts
  Chrome. A stored Workday tenant resolves from
  `<tenant>.wd<N>.myworkdayjobs.com` among the profile's cookie domains and is
  listed without a domain until one shows up.
- **Sign-in probe:** `Sessions.CheckSignIn` loads a site's account page in a
  scan tab (load and read only, 30 s per site); `ClassifySignIn` applies the
  spec's rules in order (rate limit, captcha, checkpoint, closed posting,
  sign-in form, own address plus the proof cookies). The wording rules read
  the first 400 characters of the page text, since further down a feed the
  same words are a post, and a challenge frame beside a sign-in form is the
  form's widget (signed_out). State and time go to
  `signin_probes` (migration 0028). A signed-in or rate-limited result under
  five minutes old is served as is; a signed-out, checkpoint, captcha or
  unknown one is read again, since signing in or clearing the challenge is
  what the user checks for. `Sites` reports the stored state, `unknown` before any probe.
  Triggered by `applysmith browser check` and the Check sign-ins button of the
  Browser page. `NextAction` projects a state to proceed, retry-later, needs-user or
  blocked-by-safety. Proof: `TestSignInProbeAgainstARealProfile`.
- **Import:** scope check; not-in-source domains (no cookies, or none of the
  site's sign-in cookies) left alone; snapshot (`session-snapshot.json`, mode
  0600, temp file then rename, in the directory `Sessions` is given; the server
  uses `.applysmith/browser/`); delete the in-scope cookies (restore and
  `replace-failed` on a refusal); set; read back. The destructive phase runs
  detached from the request with a 2-minute budget, so a caller that gives up
  cannot leave the profile half replaced. Session cookies get a 30-day expiry.
- **Undo:** delete the in-scope cookies, set the snapshot's, read back; the
  snapshot is removed only when every cookie came back. A snapshot older than 7
  days is removed the next time any operation or `UndoAvailable` looks at it.
- **Apply window:** `POST /api/browser/apply-window/open`
  (`internal/api/browser_lanes.go`) opens a user-owned apply tab and raises the
  window; while a run drives the window, a page of a site on the sync list is
  refused with `apply-run-live`.

Real-browser proof: `TestSessionsAgainstARealProfile` (`go test -tags browser ./internal/browser/`) and
`TestSessionSyncThroughTheRoutesAgainstARealProfile` (`./internal/api/`), both on throwaway profiles.

## Importing cookies from an installed browser

Sign-in sync needs the extension running in your everyday Chrome. The direct
import is the same outcome without it: `internal/browser/browserimport` reads a
Chromium-family browser's own cookie store on this computer and hands the chosen
sites' cookies to the very same `Sessions.Import`, so the replace, read-back,
undo snapshot and manage list all apply unchanged.

- **macOS only.** A Chromium browser on macOS encrypts each cookie value with a
  key derived from a password in the login keychain (`<Browser> Safe Storage`).
  Reading that password prompts you (Allow / your Mac login password), so no
  value is decrypted without your keychain approval. Windows (DPAPI) and Linux
  (Secret Service) lock their stores against other processes; there the
  extension's sync is the path, and the import reports that it is unsupported.
- **Sources.** The browsers `browserimport` knows (Chrome, Chrome Beta, Edge,
  Brave, Chromium, Vivaldi) and each of their profiles that holds a `Cookies`
  store (`<profile>/Network/Cookies`, or `Cookies` on older builds). Listing
  sources and a source's sites reads host names and counts only, from a
  read-only copy of the store; it never touches the keychain or a value.
- **Decrypt.** The key is PBKDF2-HMAC-SHA1 of the Safe Storage password (salt
  `saltysalt`, 1003 iterations, 16 bytes). A `v10` value is AES-128-CBC with a
  16-space IV; current Chromium prepends `sha256(host_key)` to the plaintext,
  which is stripped when it matches (and confirms the key decrypted the value).
  One unreadable cookie is skipped, not fatal: a sign-in sets several.
- **Scope and write.** Only the sites you choose are read, and a cookie is
  included when its host is one of them or lies below it. The decrypted cookies
  go through `Sessions.Import`, which keeps the undo snapshot, replaces the
  in-scope cookies, reads the profile back and records the sites in the manage
  ledger (`imported-sites.json`). `Forget` clears one imported site's cookies
  from the managed profile and drops its ledger row; unlike Undo it restores
  nothing, since the sign-in came from outside.
- **Routes.** `GET /api/browser/import/sources`, `GET
  /api/browser/import/sources/{id}/sites`, `POST /api/browser/import`
  (admin, secret, refused while a run drives the apply window), `GET
  /api/browser/imported` and `POST /api/browser/imported/forget`. The Browser
  page's "Import from your browser" and "Brought over" panels drive them.
  Cookie values never reach a response, a log or the page.

Proof: `internal/browser/browserimport` (decrypt round-trip, synthetic profile,
scope and host-prefix stripping), `TestImportedSitesListsBroughtOverSitesAndForgetClearsOne`
(`internal/browser`), `internal/api/browser_import_test.go` (routes, value never
returned, refusal while a run is live) and `CookieImport.test.tsx` (the panels).

## Lane status, screenshots and window controls (task 6.8)

`Control` (`control.go`, built by `launch` over the workspace's browser options)
serves the Browser page and `GET /api/browser/status`,
`GET /api/browser/lanes/{lane}/tabs/{tabId}/screenshot`,
`POST /api/browser/apply-window/show` and `POST /api/browser/stop`. It shares the
instance registry with sign-in sync and the apply window, so every component
sees one Chrome.

- **Status** never starts Chrome. State is `stopped`, `starting` (a launch
  pending in the registry), `running`, `stopping` (a stop from `Control`) or
  `failed` (the last start from `Control` failed and nothing runs). Titles and
  addresses come from one `Target.getTargets` call, reduced to origin and path.
  `loading` and the window's active tab come from asking each tab with a session
  for `document.readyState` and `visibilityState` (1 s each, in parallel). A
  kept tab has no session and is not asked: never loading, and active when
  `ShowWindow` activated it last, else the window's newest tab (the one the
  autofill button picks). The newest 100 events are returned, newest first; a
  scan stub records the address a popup would have loaded (resolved against
  the document), and the event log reduces it to origin and path like any other.
- **Screenshots:** JPEG at quality 70 (PNG on request), at most 1600 CSS pixels
  wide (default 1024) via the clip scale of `Page.captureScreenshot`, cached in
  the tab record for one second per format and width under a per-tab mutex, so
  concurrent requests share one capture. A kept tab is read through a session
  of the observer's kind (non-flat attach, no domain enabled) that lasts only
  for the capture. A minimized
  or unrenderable tab answers `screenshot-unavailable`.
- **Show** starts the browser if needed, opens a blank user-owned (kept) tab
  when the apply window has none, un-minimizes the window, and activates the
  named tab, else the last shown, else the newest (through `ShowWindow`).
- **Stop** refuses with `lane-leased` while a run, list or scan holds a lease
  (or, in the API, while an apply run is live), unless `force`. A browser this
  process lost is stopped too (`stopLost`); stopping one that is not running
  answers `not-running`.

Real-browser proof: `TestE2E_Control_StatusScreenshotAndStop` (`go test -tags browser -run TestE2E_Control ./internal/browser/`),
a headless Chrome on a throwaway profile. Route tests: `internal/api/browser_lanes_test.go` with a scripted control.
