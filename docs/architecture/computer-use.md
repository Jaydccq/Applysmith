# Computer-use service (PersonalCU)

PersonalCU is the owner's own macOS computer-use service: a Swift MCP server
that reads one window's accessibility tree and screenshot and clicks, types,
scrolls and drags in it without raising it. Its source is `computeruse/` (owner
decision AC-1; the `computer-use` spec is normative). It is a separate app, so
Applysmith.app stays one executable and its rebuilds leave PersonalCU's grants
alone. The Applysmith server installs, starts and grants nothing; its
`desktop_action` gate decides every action before `internal/computeruse` sends
it to the running service. PersonalCU has no model; the MCP client brings one.
The third-party client it replaces was only analysed; none of its code is reused.

## Build and install

Needs macOS 14 and Swift 6.1 (the MCP SDK's manifest) or newer; observed: Swift
6.2.3 (Command Line Tools), macOS 26 arm64. The first build fetches the eight
packages in `Package.resolved`. None of these is in `make verify` or `make app`:

    make computer-use-install    # the one step: signing identity (first time), release build, replace /Applications/PersonalCU.app, open it
    make computer-use            # only the release build, signed, into computeruse/dist/PersonalCU.app (about 15 MB)
    make computer-use-test       # the 20 unit tests, swift test through scripts/swift-tool.sh
    cd computeruse && bash scripts/swift-tool.sh build && python3 scripts/check_mcp.py .build/debug/personal-cu
    cd computeruse && python3 scripts/check_manager.py .build/debug/personal-cu   # drives the real manager window (below)

The bundle holds `personal-cu`, the JavaScript worker and an `Info.plist`
(`dev.applysmith.personal-cu`, 0.2.0, menu bar only); it is not notarized. Pause
input and Allow background control live in that defaults domain, not in the
original build's. `mcp` is the stdio relay an MCP client runs; `permissions`
raises the macOS prompts. `js` runs `/opt/homebrew/bin/node` (observed 26.3.0)
or the executable `PERSONAL_CU_NODE` names, which an app opened from Finder or
at login sees only through `launchctl setenv`.

## Signing and permissions

macOS (TCC) keeps an Accessibility or Screen Recording grant against the app's
designated requirement; for ad-hoc code that is the code hash (`cdhash`), so
each rebuild is a new app and the grants drop. `make computer-use-install`
creates, the first time, a self-signed certificate, PersonalCU Local Signing, usable and
trusted for code signing only; its key stays in the login keychain, not
exportable, for `codesign` alone, and trusting it asks for the login password
once. Builds signed with it (`PERSONALCU_SIGN_IDENTITY` names another) share
the requirement "this bundle id and this certificate", which only code signed
with the owner's key meets; without it a build is signed ad hoc, with a
warning. Both use the hardened runtime; the build prints the requirement. The
first signed build may ask to let `codesign` use the key: run it yourself,
choose Always Allow. Any program the owner runs could then sign with it; such a
program can reach the socket anyway. After the first install of such a build,
grant once in System Settings, Privacy & Security: Accessibility, Screen
Recording, and Input Monitoring for `debug_tap` only (in the manager, the Open
Settings button beside a permission that is not granted asks for that one and
opens its list). An earlier build's entry (other id, or ad hoc) does
not match, and toggling it does not refresh it: remove it, add
`/Applications/PersonalCU.app`, quit and reopen. Remove the identity in
Keychain Access, or `security delete-identity -c "PersonalCU Local Signing"`.

## The manager

PersonalCU lives in the menu bar; its window opens from there or when the app is
opened again. `ServiceSummary` (PersonalCUCore, unit-tested) names the first
thing that keeps a client from acting, in the order the Applysmith client reads
the doctor: not running, a missing permission, a locked desktop, the pause,
background control off, else ready. The window, the menu's first line and the
menu bar icon (cat with a pointer when ready, with a pause or warning badge
when needed; [artwork](../design/menu-bar-icons.md))
all say that one headline.

- The window lays itself out (stack and grid views): the headline with what to
  do, then Service with the number of connected clients, the three permissions
  (Open Settings shows only beside one not granted), and the desktop session.
- Control: Pause input, Allow background control, and Start at login as one
  checkbox (a copy run outside its app bundle has no login item and says so).
  The menu has Pause input, which reads Resume control while paused, and Allow
  background control as a checked item.
- Control-Option-Command-Period pauses input from anywhere (`PauseShortcut`, a
  registered hot key: no permission, and it sees no other key). It only
  pauses; resuming is a deliberate press in the menu or the window. Two copies
  that run at once both register it and one press pauses both (seen with two
  debug copies on 2026-11-16), so macOS does not treat a second holder as a
  conflict; the window's "taken by another app" line shows only when the
  registration itself fails, which was not seen.
- The window keeps one size: the advice has two lines and the list six from the
  start. A window that resized under a client acting in it would make that
  client's snapshot stale. It is ordered front even when macOS declines to
  activate an app another program started.
- Recent actions: the latest 6 acting, raising or launching calls, each with
  its time, tool and the target app's name (`ActionLog`), in memory only. It
  holds no argument, so no typed text, key or point.

`scripts/check_manager.py <executable> [--shortcut]` starts its own copy of the
service on a socket of its own and drives that copy's window through a second
process, finding each switch by its accessibility role and title: the headline
agrees with the doctor; the service's own clicks are listed and the window
keeps its size; Pause refuses an action while reads go on; the pause is kept
across a real Quit and reopen, which removes the socket; background control off
refuses background input; with `--shortcut`, the real chord pauses and a second
press does not resume. All six passed on 2026-11-16 on a debug build, six runs
in a row. That copy had Accessibility and not Screen Recording (a program
started from a terminal uses the terminal's grants), so its headline was
"Needs Screen Recording": the Ready, Paused, Background control off, locked and
stopped headlines are covered by the unit test of their wording and order, not
on a desktop, and so are the menu and its icon. Start at login was not pressed.

## The socket

`runtime.sock` in `~/Library/Application Support/PersonalCU/` or in
`PERSONAL_CU_RUNTIME_DIR` (service and clients must agree): newline-delimited
JSON-RPC, one MCP session per connection; Applysmith's own checks are in the spec.

- The service makes the directory 0700 and refuses to start unless it is the
  user's real directory without group or other bits, or while a live service
  answers. It replaces only its own stale socket, sets it to 0600, listens with
  a backlog of 8 and drops a peer (`getpeereid`) of another uid; so does `mcp`.
- A request over 8 MiB, a response over 16 MiB, over 32 frames or 32 MiB queued,
  or 30 s without reading ends the connection. Each connection has its own
  controller, snapshots and JavaScript; all desktop calls but `doctor` pass
  one queue.

## The 22 tools

| Group | Tools |
|---|---|
| Health | `doctor`: permissions (of the running service; the `doctor` subcommand reports its own process), desktop session (locked, on console), service (running, paused, background control, clients), input backends, keys; never prompts |
| Find and look | `list_apps`, `list_windows`, `get_window`, `launch_app` (without activating), `get_app_state`, `get_window_state` |
| Raise | `activate_app`, `activate_window`; refused while the desktop is locked |
| Act | `click`, `type_text`, `paste`, `press_key`, `scroll`, `set_value`, `perform_secondary_action`, `select_text`, `drag`, `drag_paths`; with background control off, only `input_mode: "foreground"` on a frontmost target |
| Diagnose | `debug_tap`: listen-only summary of one process's events, 10 s and 500 records at most; a key event keeps only its type and time, never its key code or text |
| Script | `js` (persistent Node.js, top-level await, the other 20 tools as `cu.*`; trusted code, not a sandbox), `js_reset` |

Schemas are strict (no unknown fields; bounded strings, numbers and arrays); a
call outside them is refused before anything runs. Pause input refuses the 13
tools that act, raise or launch, cancels input in progress and resets
JavaScript; reads keep working.

## The snapshot contract

- A look (mode `ax`, `full` or `image`) returns `snapshot_id`, `pid`,
  `launch_date`, `window` (`title`, `rect` in global points, `window_id`),
  `elements`, `tree_truncated`, `ax_scope` and, with the PNG image item,
  `screenshot_id` and `screenshot` (`pixel_width`, `pixel_height`, top left).
- An element has `index`, `depth`, `parent`, `role`, `subrole`, `title`,
  `description`, `identifier`, `value` (text cut to 4,096 characters), `secure`,
  `rect` (global points, not pixels), `enabled`, `actions`, `value_writable`. A
  secure field's value is never read. A later look of the same window lists only
  changed elements (`diff: true`, `removed_indices`) unless `disable_diff: true`.
- A snapshot is bound to the CG window id, the owner's pid, the window frame and
  the process's kernel start time. The frame is the same while the window only
  gains or loses the keyboard focus: the window server then reports it one
  point larger or smaller on every side, one axis at a time (`sameWindowFrame`;
  Applysmith's `Rect.SameFrame` judges its own copy the same way). A window not
  on screen (another Space, behind a full-screen app, minimized) is still found
  (`WindowService.window`): it can be read and its controls pressed by index,
  and background mouse, key and scroll input is refused with that reason. Without a matching accessibility window the
  tree is the application's (`ax_scope: application`, `ax_error`), and an
  indexed action needs the element's centre inside the window. A tree read
  that fails is an error and saves no snapshot; only the limits below cut a
  tree short (`tree_truncated`).
- An action names `pid`, `snapshot_id`, then an `index` or screenshot-pixel
  `x`/`y` with its `screenshot_id`. It is refused when the snapshot is 30 s old,
  the process restarted, the window moved or resized, the element's role,
  labels, value or frame changed, or the snapshot was used (one attempt, failed
  or not). A new look replaces the window's old snapshot; a connection keeps 8.
  A result says `verified` (a readback matched), `performed` (accessibility
  accepted it) or `posted_unverified` (events were sent); `type_text` says
  `readback_mismatch` when its field reads back other text than expected, and
  returns that `readback` cut to 4,096 characters.

## Limits

- Tree: 4,000 nodes, depth 64, 5 s, 2 MiB of text; pages of a saved snapshot
  take `offset` up to 4,000 and `limit` up to 1,000 (4,000 when absent). A
  screenshot is one on-screen window without shadow or cursor, at most 2,048 by
  1,536 px, 100 megapixels captured, never the whole screen.
- Input: `type_text` 4,096 UTF-16 units of plain text (a control character
  such as Return or Tab is refused; Return comes only from `submit` or
  `press_key`), `paste` 2 MiB, `press_key` 256
  characters, `scroll` 1,000 px or 20 pages, `click` 1 to 3 presses, drags
  1,024 points and 20 s, a mouse stream 4,096 events and 30 s.
- JavaScript: 1,048,576 characters of code, 8 MiB of output with screenshots,
  timeout 1 ms to 10 min (default 30 s), 256 MB heap, Node's permission model
  with read access to the worker file only.
- Background input uses private SkyLight calls resolved at run time; keyboard
  and wheel need macOS 26 on arm64 and fail closed elsewhere. It never moves the
  pointer, raises a window or switches Space, and has no foreground fallback.

## Verification status

From the original record of release 0.2.0 (build `8dd6431a`); a pass covers
only what its check asserts. The repository build (bundle id, signing, hardened
runtime, English manager, `PERSONAL_CU_NODE`) is not re-accepted on the desktop.

| Check | Result |
|---|---|
| Unit tests | 13 in 3 suites pass; again in this repository (20 in 7 suites on 2026-11-16) |
| Protocol, resident `mcp` | 16 pass: 22 strict schemas, 8 invalid requests refused, unknown snapshot refused, JavaScript state, callbacks, errors and timeout reset, concurrent 6 MiB frames |
| Protocol, `standalone-mcp` | 16 pass; again in this repository, debug build and release build with the hardened runtime |
| Native fixture | 12 of 12: exact window, AX and full image, indexed `set_value` and button, coordinate click, Unicode clear and type, menu shortcut, Markdown bold paste, UTF-16 selection, right click, drag, scroll; foreground app and pointer unchanged |
| Chrome fixture | 9 pass: coordinate click, Unicode clear, keys, plain paste, right click, balanced multi-point events, pixel scroll, foreground and pointer kept. 1 fails: a held-button drag draws nothing (`move.buttons=0`); fixed since, see Known limits |
| Pause and Quit during a real drag | pass: input stops, every pressed button is released |
| Extended | 7 pass: window PNG through `js`, snapshots isolated per connection, popup menu, cursor placement, fixture reuse, window and app activation, non-empty debug tap |
| Resident lifecycle | 5 pass: JavaScript per connection, disconnect cleanup, Quit stops JavaScript and service, no relaunch after Quit, reopen and reconnect |
| Manager, automated | 7 pass on an intermediate build; the final build passed 3 (pause read and write, pause kept across reopen, background switch), then hit a stale-snapshot refusal; see Known limits for its causes, fixed on 2026-11-16 |
| Not accepted | several displays, Spaces, other Electron apps, every option combination, a real logout and login |

## Known limits and what was found

- **A drag in Chromium had `event.buttons` 0; fixed on 2026-11-16.** Chromium
  builds a mouse event's `buttons` from the system's pressed buttons
  (`NSEvent.pressedMouseButtons`; cited from knowledge of its
  `web_input_event_builders_mac.mm`, and borne out below), not from the event,
  and an event addressed to one window never changes that system state. So the
  page got mousedown, the moves and mouseup, and a page that tests
  `event.buttons` saw no drag. `SystemButtonHold` now makes the system report
  the left button as pressed for the length of each drag path in a Chromium or
  Electron window: it posts a left press at the user's pointer through the HID
  tap and removes it in its own tap at the annotated session tap, after the
  system has counted it and before any window gets it. Measured first with a
  window of the experiment's own under the pointer: pressed state 1, no
  mousedown delivered, the pointer not moved; a tap at the session tap removes
  the press before it is counted, which is why the later tap. Guards: no press
  is posted until the tap has removed a probe (the release of a button no mouse
  has), so a tap that is not working costs no click at the pointer; none while
  the user holds the real button; the release is posted when the path ends,
  fails or is cancelled; a drag refused as stale or for its input mode is
  refused before any press; the tap is enabled only from the probe until it
  has removed the release (its entry in the system's tap list read disabled
  after each run), so the user's own clicks pass through this process at no
  other time. This is the one place the service posts through the HID tap. The result says `button_state:
  left_button_reported_pressed`, or `button_state_note` with the reason the
  hold was refused, and then the old behavior applies.
  Seen with the installed build and the Chrome fixture: three runs of
  `check_browser.py --service` in a row with `event.buttons` set on the moves
  of both `drag` and `drag_paths` and the canvas drawn (two of them 11 of 11,
  one with `physical_pointer_preserved` failing while the owner used the
  mouse); a Pause from the shortcut in the middle of a 15-second drag left the
  system's pressed state at 0, and so did a Quit in the middle of one.
  A service that dies holding the button cannot release it, so a guard process
  (the executable's `button-guard` command, started at the first hold) is told
  of each press and release over a pipe and posts the release when the pipe
  closes on a press: `kill -9` of the service while the state read 1 left it
  at 0 and no guard behind. Its release is not removed by a tap, so the window
  under the pointer gets a mouse-up with no mouse-down.
  The user's own pointer moves stay moves while the button is reported
  pressed: in 45 seconds of it, with the owner moving the mouse, 1566 arrived
  as `mouseMoved` and 9 as `leftMouseDragged`; where the 9 came from is not
  known. What a real click by the user does to the reported state during a
  drag is not settled either: four runs of an experiment made while the owner
  was clicking disagreed. So a drag reads the state once more before its
  release, and when it is gone the result carries `button_state_note` in place
  of `button_state`.
  Inherent, not fixable here: a program that taps events at the HID or session
  tap sees the press (the experiment's own session tap did). Not seen: an
  Electron app (same `buttons` code as Chrome; fetching and running Electron
  for the test was not permitted in the session). Before the probe existed one drag was
  refused as stale and the fixture window was later found minimized; the cause
  was not found, it did not recur in the runs above, and the owner was at the
  desktop.
- **The stale-snapshot refusal of the manager check had three causes**, each
  reproduced with `check_manager.py` on 2026-11-16 and fixed: (1) a window's
  bounds differ by one point per side with the keyboard focus, which read as
  "the window moved" (the snapshot contract above); (2) the query for one
  window answers only for a window on screen, so a window on another Space was
  refused as changed (a regression of this repository's single-window lookup);
  (3) the old manager window was laid out by fixed frames and the new one first
  grew with its text. A refusal while reading now names its cause: the process
  exited or restarted, the window closed, or it moved or resized, with both
  rectangles.

## Acceptance evidence

The evidence (`docs/evidence`), the original docs and notices, the eight other
`check_*` scripts, `query_browser_fixture.mjs`, `terminate_app.swift` and the
fixtures (`Tests/AcceptanceFixture`, `Tests/BrowserFixture`) stay in the original
project folder on the owner's Mac: they act on real windows. On 2026-11-16
`check_service.py` and `check_held_input.py` there were changed to find the
manager by its new title and its switches by their accessibility titles, and
`check_browser.py` with its fixture to count the moves between mousedown and
mouseup and to take `event.buttons` 0 as a known limit only when the result
carries `button_state_note`; the earlier files are in `scripts/before-2026-11-16`
there.

Run on 2026-11-16 against the installed build of that day (signed PersonalCU
Local Signing, both grants; evidence in `docs/evidence/installed-2026-11-16`
there):

| Check | Result |
|---|---|
| `check_service.py`, native fixture | 7 of 7: pause refuses and reads remain, pause kept across a real quit and reopen, background switch, pause cancels a held drag and releases the button, quit drains a JavaScript drag, a quit stays quit, the service restarts and the relay reconnects |
| `check_browser.py --service`, Chrome fixture, before `SystemButtonHold` | 9 pass, among them the moves between mousedown and mouseup; `event.buttons` 0 with the note; `physical_pointer_preserved` failed with the owner at the mouse during the run, which the check cannot tell from the service moving it |
| The same, with `SystemButtonHold` (evidence `browser-held`) | 11 of 11 twice, `event.buttons` set and the canvas drawn; see Known limits |
| The pause shortcut, real chord | paused the installed app, a second press did not resume, the window read Paused |

`check_held_input.py` passed in both modes later that day, with the operator's
Pause and Quit made through System Events: the held drag ended and the fixture
counted as many mouse-ups as mouse-downs. What the run changed in those scripts:
`terminate_app.swift` named the earlier bundle id and so refused to quit the
app; `check_service.py` finds the manager by bundle id and width, on screen or
not, because a driver a terminal starts is told no window titles; the Chrome
fixture is opened from a copy outside `~/Documents`, which Chrome may not read.
A driver started from the installed executable while the service is paused
starts paused too (same defaults), so a run that stops midway is resumed in the
window or by a driver from another build. `check_mcp.py` (one
helper taken from `check_transport.py`) touches no window unless `--fixture-pid`.
