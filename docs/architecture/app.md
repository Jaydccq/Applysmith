# Desktop app

The desktop app is a mygo project in `app/`. It adds a window, a tray item and
native bindings over the server that `applysmith serve` runs. `cmd/applysmith`, `internal/cli` and
`internal/` never import mygo, so the CLI builds without the app. The app binary
also is the command line when it is started as `applysmith` (below).

The menu bar uses the [cat-and-envelope template mark](../design/menu-bar-icons.md).

## Layout

| Path | Role |
|---|---|
| `app/main.go` | Event-loop wiring: single-instance lock, bindings, quit handling, tray, startup goroutine |
| `app/shell.go` | Workspace resolution, attach or start, window, `Shell` binding, shutdown |
| `app/tray.go` | Menu-bar item (status line, Open Dashboard, Show Apply Window, Restart Server, Back to My Workspace, View Logs, Quit), the application menu, the activity count, a stopped attached server, restart and workspace switching |
| `app/bindings.go` | The `Shell` binding the dashboard calls (see below) |
| `app/desktop.go` | What the bindings ask of the operating system, as one struct so tests can replace it |
| `app/links.go`, `app/workspace_path.go` | The external-link rules and the workspace-path check |
| `app/serverapi.go` | Calls to the server with the local API token (apply window), link opening, the origin lock |
| `app/smoke.js`, `app/smoke_on.go`, `app/smoke_off.go` | Page script injected only by `--smoke`, compiled in only with the `smoke` build tag |
| `app/mygo.json` | Name `Applysmith`, bundle identifier, version, output directory |
| `internal/launch` | Lock, vault, server start and lock-based attach, shared with `applysmith serve` |

Build with `make app`: it runs `make generate`, then
`go tool mygo build -skip-dmg` in `app/`. Output is
`app/build/<os>-<arch>/Applysmith.app`, ad-hoc signed. `make package` also builds
the disk image and checks the bundle; sizes, startup and keychain measurements
and the owner-only signing steps are in [packaging.md](packaging.md).
`make app-smoke` builds the same bundle with the `smoke` tag,
which compiles `smoke.js` in; a release build contains neither the script nor a
`--smoke` switch (`TestReleaseBuildsDoNotCarryTheSmokeScript`).

## Name and bundle identifier

The app name is `Applysmith`. mygo derives the user data directory
(`~/Library/Application Support/Applysmith`), the bundle name and the AppleScript
and process name from it. `Auto Job` is the legacy Electron app's name and
profile directory, so `app/name_test.go` fails if the name ever equals it.

The provisional identifier is `dev.applysmith.app`. The final one is chosen at
publication (design Open Question 4); until then keep it fixed so that
preferences and permissions stay keyed to one value.

## App Nap

The app lives in the tray with no visible window, which macOS treats as idle
and drops to the background priority band (`ps -o pri` shows 4, the band
`taskpolicy -b` gives; a foreground process shows 31). In that band the process
stalls whenever the machine is busy: a store write test at 25 s with 16 CPU
hogs measured a longest write of 77 ms at normal priority and 35 s under
`taskpolicy -b`. The server and its single SQLite writer live in this process,
so the stall surfaced as `store: busy: writer not available within the busy
timeout`, `a built-in timer failed ... context deadline exceeded`, a
`GET /api/runs/summary` 503 after 5.6 s and a 30 s sleep acknowledgement. The
process declares a user-initiated activity at start (`app/appnap_darwin.go`,
`NSProcessInfo beginActivityWithOptions:reason:` with
`NSActivityUserInitiatedAllowingIdleSystemSleep`, through purego's objc) and
never ends it, so the system never naps it. The Info.plist keys do not do
this: `NSAppSleepDisabled` is a user default, and with `LSAppNapIsDisabled` in
the bundle the hidden app still fell to priority 4 within half an hour on a
locked screen. Check it with `ps -o pri= -p $(pgrep -x Applysmith)` after the
window has been hidden a while (a foreground app reads 31 or more).
System sleep is unaffected: errors logged within a minute of a
wake (`pmset -g log`) come from the host having slept, not from a held writer.

## Startup

1. Resolve the workspace (`APPLYSMITH_WORKSPACE`, recorded setting, or `~/Applysmith`).
2. `launch.AttachOrStart`. `launch.Attach`: if the workspace lock is held and
   the lock document names a loopback origin whose `GET /api/health` answers
   `ok` for the same workspace key, use that origin and start nothing. A holder
   that never turns healthy within 15 s is an error, never a reason to start a
   second server.
3. Otherwise `launch.Start` takes the lock, binds, migrates and serves the
   embedded `web.Dist()` dashboard in-process. If another process took the lock
   in between (`server_already_running`), the look is repeated and the winner is
   attached to.
4. The window loads `<origin>/?launch=<code>`: the one-time launch code comes
   from the in-process server, or, for an attached one, a fresh code minted
   through `POST /api/launch-codes` (the lock document's code is the fallback).
   The server redeems it for the session cookie and redirects to `/#k=<key>`;
   the page stores the key and sends it with every request.
   `TrustedOrigins` is the server origin, so only pages of the server may call
   the bindings.
5. After the event loop ends, an in-process server is shut down (lock
   released); an attached server is left alone.

A recorded workspace (the settings file, not a folder picked on the
diagnosis screen or `APPLYSMITH_WORKSPACE`) whose folder is gone stops the
start as "Workspace not usable", naming the folder: it started before, so it
was moved, deleted or sits on a drive that is not connected. Initialising it
would have put an empty workspace (and a new workspace id, so no keychain
secrets) in its place, which reads as all the user's data gone. Retry after
reconnecting the drive, or Change Workspace to the moved folder
(`TestARecordedWorkspaceThatIsGoneIsNotRecreated`).

Bindings are the specified `getShellInfo`, `quitApp`, `revealInFinder`,
`setStartAtLogin`, `pickFile`, `pickFolder`, `openExternal`,
`openInApplyWindow`, `showApplyWindow`, `reopenDashboard` and `switchWorkspace`, as `Shell.<Method>` (table in
[packaging.md](packaging.md#native-bindings)); `TestShellBindingsAreTheSpecifiedOnes`
fails if the set changes. The dashboard uses them only when `window.mygo`
exists. The shell log is
`<workspace>/.applysmith/logs/desktop.log`, rotated like the server log (10 MiB,
5 files).

The desktop defaults to a system-assigned loopback port, so the original app
or another workspace may keep using its own listener. Clients locate this
server through its workspace lock; the CLI's standalone default remains 47319.
The browser extension cannot read the lock, so the desktop first asks for the
port its workspace's last server had (read from the lock document before the
lock is taken; `launch.Options.ReuseLastPort`), and takes a free one only when
that port is held. 47319 is never reused that way. Before this, every launch
moved to a new port and a paired extension showed Offline until its address was
typed again (nine ports in nine launches in the owner's desktop log on
2026-10-17). Settings, Browser extension names the current address, and its
pairing code carries the port, so a new code reconnects the extension.
On 2026-10-15, a packaged launch reproduced a collision with the original
TypeScript server on 47319. `TestDesktopPortDefaultsToAnAvailableListenerAndHonorsExplicitPorts`
covers the desktop default, explicit ports and invalid values.
The rebuilt release was then launched normally with the original server
still holding 47319. It served the configured default workspace on a distinct
system-assigned port, and the native Reports page displayed the saved real
Zip evaluation with its 3.3 score and report contents.

Environment: `APPLYSMITH_PORT` selects the desktop port (0 for any), and
`APPLYSMITH_SECRET_API_TOKEN` supplies the API token instead of the keychain.

## Window background and window state

Both windows (the dashboard and the diagnosis screen) are created with a
`BackgroundColor` of the form `light-dark(<light>, <dark>)`, which mygo shows
until the page paints: `#f6f5f1` / `#161615` for the dashboard and `#f6f6f4` /
`#1b1b1a` for the diagnosis screen, each the page's own `--bg`. mygo reads the
system appearance when the window is created and re-applies the matching colour
when the appearance changes while the app runs, so a dark-mode user sees no
white flash and nothing in `app/` follows appearance changes by hand. The pairs
in `app/shell.go` are kept equal to `web/src/styles.css` and `diagnosis.html`
by hand. This is `WindowOptions.BackgroundColor`
(present since mygo 0.4.1, with `light-dark`); the 0.4.6 `SetBackgroundColor` is
a method of `WebView`, the native-UI element for an embedded page, which the app
does not use. Limit: a user who forces a theme in the dashboard against the
system appearance sees the system colour for the instant before the page paints.

Window position and size are remembered by mygo under `StateKey: "main"` in
`window-state.json` in the user data directory
(`~/Library/Application Support/Applysmith`). The same directory holds `extension`,
the unpacked extension the app keeps current for Chrome
([extension.md](extension.md)). The desktop-shell spec says the position and
size persist "in the per-user settings, clamped to the visible
displays"; that file is the per-user settings location and mygo clamps to the
connected displays, so the default is kept. mygo 0.4.6 added
`App.SetWindowStateStore`, which would move the states into the workspace
database; the spec does not ask for that and a per-window state in a per-workspace
database would follow the workspace instead of the user, so it is not used.
Smoke runs and tests give the process a throwaway `HOME`, which also keeps the
file away from the real directory.

## Startup diagnosis screen

A start that fails (or an attach that never turns healthy) shows the diagnosis
screen instead of the dashboard (`app/diagnosis.go`, `diagnosis.html`). The page
is one embedded document with no outside reference (its content security policy
allows only itself), so it needs no server. It names one cause, chosen by
`classify` from the typed failures the start returns:

| Category | Comes from |
|---|---|
| Port held by another program | `port_in_use` (also `EADDRINUSE`); the screen shows the port and, as far as this account may see it, the `lsof -nP -iTCP:<port> -sTCP:LISTEN` listener |
| Port held by a server of another workspace | `server_already_running` with `OtherWorkspace`; the screen shows the version and build the server there reports, and the listener |
| Workspace not usable | `workspace_invalid`, a missing or unreadable folder, a corrupt or unwritable database |
| Database migration failure | a schema from a newer build, a changed migration, a failed migration step |
| Keychain access denied | the vault's `keychain_denied` or `keychain_unavailable` |
| Lock held by an unhealthy server of the same workspace | `workspace_locked`, or `launch.ErrHolderUnhealthy`; the screen shows the process and origin in the lock document |
| Unknown | anything else; the error text and the log say what happened |

Below the cause the screen shows the error and the last 50 lines of the
relevant log (`desktop.log`; the lock holder's `server.log` first for an
unhealthy lock; the lines this run logged when no file exists yet). A port that
the legacy app holds shows as "port held by another program": it does not answer
as an Applysmith server.

The page's chrome is not selectable (`user-select: none`) so a stray select-all
paints no page-wide highlight, while the error and log blocks stay copyable. The
buttons show a focus ring only after a key press, so the button focused when the
window opens (or one clicked) draws none.

The four buttons are the `Diagnosis` service (`Retry`, `OpenLogs`,
`ChangeWorkspace`, `Quit`), separate from the dashboard's `Shell` bindings.
Each call is refused with `diagnosis_screen_only` unless it comes from the
diagnosis window's own page, so the dashboard cannot use them.

- Retry runs the startup sequence once per click. A second click while a start
  runs answers `busy`. Nothing retries by itself: a failed attempt only redraws
  the screen.
- Open Logs shows the logs folder (disabled until a workspace is known).
- Change Workspace opens the folder picker and checks the choice against the
  workspace rules (`workspace.Resolve`); a refused folder is reported on the
  screen. An accepted one is used for this and later attempts in preference to
  `APPLYSMITH_WORKSPACE`, and is recorded as the per-user workspace setting once the
  start works. The app has no other workspace switcher.
- Quit ends the app.

`TestStartupFailuresAreClassifiedByTheirCause` produces each category with the
real failure (a held port, a second workspace's server, a newer schema, a denied
keychain, a lock whose server does not answer). In the smoke build
(`--smoke`) the diagnosis window is created hidden; the run logs what the page
shows and presses Retry once. Checked in the packaged smoke build with the
default-port situation reproduced on a throwaway port: the page loaded with the
bridge (`window.mygo.call` a function), cause `port-held-by-program`, the four
buttons, 50 log lines at most, Retry went through the binding once per click and
logged `startup retried`, and after the port was freed a Retry started the server
in-process and opened the dashboard.

## Single instance

`main` calls `mygo.App.RequestSingleInstanceLock()` before anything else. mygo
owns the lock: a Unix socket in the per-user temporary directory keyed by the
bundle identifier and the app name (so `mygo dev` and the installed app do not
collide), released on quit and replaced when a crash left it stale. A second
process hands its arguments over the socket and gets `false`; `main` returns,
so it creates no server, no window and no tray. In the first instance
`OnSecondInstance` calls `shell.secondLaunch`: restore a minimized window, show
and focus it, activate the app, and log `second launch: window shown` with the
window's `visible` and `focused` state. Before the window exists (the first
instance is still starting) there is nothing to show; the window opens shown
when startup finishes, and only one server start happens because the lock is
taken before `WhenReady`. The arguments are not used: the spec asks only for a
focus request. A relaunch through LaunchServices (`open Applysmith.app`, the Dock
icon) never starts a second process on macOS; it reaches the same window
through `OnActivate`, which also covers the Dock reopen.

## Phase-0 feasibility smoke test (task 0.2a)

Run on 2026-10-11. Machine: macOS 26.2 (Darwin 25.2.0, build 25C56), arm64.
Go 1.27.1 darwin/arm64, mygo 0.4.1 (`go tool mygo version`), Bun 1.3.5.
Each run used a throwaway workspace under the session scratch directory and
`APPLYSMITH_SECRET_API_TOKEN`, so no real workspace and no keychain entry was
touched. `--smoke` makes the shell inject `smoke.js` and log what the page
saw (this needs the `smoke` build tag, `make app-smoke`); the log line is `smoke result` in `<workspace>/.applysmith/logs/desktop.log`.

| # | Proof | Result | Evidence |
|---|---|---|---|
| 1 | Local-origin dashboard auth | pass | The WKWebView loaded `http://127.0.0.1:<port>/` and the server issued the session cookie. From the page, `fetch('/api/health')` returned 200 with `detailVisible: true` (uptime and components are returned only to an authenticated principal). A cookie-authenticated `POST /api/pairing/codes` (admin, session-only class, needs a matching `Origin`) returned 200. `document.cookie` did not contain the session (HttpOnly). `server.log` records `principal=dashboard` for both API calls. |
| 2 | Native binding round trip | pass | `window.mygo.call("Shell.Info")` (since renamed `Shell.GetShellInfo`) from the page returned `{version, buildId, mode, pid, packaged}`; the returned `pid` equalled the process id from `ps`, and `packaged` was `true` in the bundle. The page origin is `http://127.0.0.1:<port>`, listed in `PageOptions.TrustedOrigins`. A call from a foreign origin was not exercised. |
| 3 | Tray | pass | The macOS accessibility tree of the running process shows a menu-bar item with help text "Applysmith" and the menu `Running` (disabled), separator, `Open Dashboard`, separator, `Quit`. Clicking `Quit` through System Events ended the process. `Open Dashboard` was not clicked. |
| 4 | Main-thread startup | pass | `mygo.App.Run` runs in `main`; startup work runs in a goroutine started from `WhenReady`. The window was created from that goroutine (mygo hops to the main thread), reported `IsVisible() == true` with bounds 1440x875 (the display height clamps 900), and `CGWindowListCopyWindowInfo` listed it on screen, layer 0. No crash or main-thread panic in the raw binary or the bundle. |
| 5a | Attach, quit leaves the external server | pass | `applysmith serve` held the lock; the app logged `attached to a running server` with the server's origin, and the page smoke ran against it (`mode: attached`, authenticated health). Quitting the app (SIGTERM, and tray Quit) logged `no server of this app to stop`; the `applysmith serve` process kept running and answered health. |
| 5b | Owner quit stops the server and frees the lock | pass | With the app owning the server, a second `applysmith serve` exited 1 with `server_already_running` naming the app's origin and pid. After tray Quit, `desktop.log` shows `server stopped and lock released`, `server.log` shows `server stopped`, and `applysmith serve` then started on the same workspace. |
| 6 | Packaged `.app` launch | pass | `make app` built `Applysmith.app` (15.8 MB, about 16 MB on disk). `codesign -dv`: `Identifier=dev.applysmith.app`, `Signature=adhoc`; `codesign --verify --deep --strict` passes. `open -n --env ... "Applysmith.app" --args --smoke` started it; the process list showed `.../Applysmith.app/Contents/MacOS/Applysmith --smoke`, `lock` named its pid, health returned `ok` over HTTP, and the in-bundle smoke result matched proofs 1 and 2. Quit through the tray ended the process. Bundle contents: `Info.plist`, `PkgInfo`, `_CodeSignature`, one Mach-O binary. |
| 7 | CLI builds independently | pass | `go list -deps ./cmd/...` lists no mygo package. `CGO_ENABLED=0 go build ./cmd/applysmith` passes for darwin/arm64, linux/amd64 and windows/amd64. `go test ./cmd/... ./internal/launch/... ./internal/guard/...` pass. |

All seven proofs pass, so the mygo feasibility gate for macOS arm64 is closed.
Other operating systems and architectures were not run.

## Task 2.9 manual run (single instance and shell checklist)

Run on 2026-10-11, macOS 26.2 (arm64), packaged bundle from
`go tool mygo build -skip-dmg` in `app/`, ad-hoc signed. Each run used a
throwaway workspace and `APPLYSMITH_SECRET_API_TOKEN`; no real workspace or
keychain entry was touched. The screen was locked for the whole session, which
has two consequences: the app never became the active application (so
`focused` is false in every log line and `frontmost` is false in System
Events), and the window could not be closed with a keystroke. Windows were
therefore hidden through the File menu item Close All, which goes through the
same close handler as the red button, and on-screen state was read from
`CGWindowListCopyWindowInfo` (`kCGWindowIsOnscreen`) and mygo's own getters
(logged by `secondLaunch`). Focus itself is unverified.

| # | Item | Result | Evidence |
|---|---|---|---|
| 1 | Second launch with the window hidden (direct binary, same environment) | pass | Window hidden: `CGWindowList` onscreen false. Second process exited with status 0 in 0.038 s (limit 2 s); the pid list held only the first process; the window became onscreen; `desktop.log`: `second launch: window shown visible=true focused=false`. |
| 2 | Second launch through LaunchServices (`open -n`) | pass | Window hidden first. The new process was not left running (only the first pid in `pgrep`), the window became onscreen. |
| 3 | Relaunch of the bundle (`open`, the Finder and Dock path) | pass | Window hidden first. No second process; the window became onscreen through `OnActivate` (no `second launch` line, so the Dock-reopen handler did the work). |
| 4 | Second launch during startup | pass | The first instance was started and three more processes ran 0.15 s later in sequence. They exited with status 0 in 0.037 s, 0.116 s and 0.044 s. `desktop.log` holds one `shell starting` and one `started the server`; the first of the three arrived before the window existed (no log line) and the other two logged `second launch: window shown`. |
| 5 | Second launch while attached to `applysmith serve` | pass | Second process exited 0; one `second launch` line; the headless server was untouched. |
| 6 | Hide on close | pass | File > Close All hid the window (`onscreen` false) and the process kept running with its tray item. Cmd-W could not be sent (screen locked). |
| 7 | Window on the dashboard | pass | An on-screen layer-0 window of 1440x875 owned by the app, loading `http://127.0.0.1:<port>/?launch=<code>` of the in-process server. Page content was not seen (no screenshots); the dashboard load is proof 1 above. |
| 8 | Tray items | pass | Accessibility tree of the status item: status line (`Running`, or `Attached to a running server`), separator, `Open Dashboard`, separator, `Quit`. Open Dashboard showed the hidden window (`onscreen` true). |
| 9 | Attach to a running `applysmith serve` | pass | `desktop.log`: `attached to a running server` with the server's origin; tray status `Attached to a running server`. |
| 10 | Quit, in-process server | pass | Tray Quit: process gone, `quit; server stopped and lock released`, the single-instance socket file removed from the temporary directory. |
| 11 | Quit, attached | pass | Tray Quit: `quit; no server of this app to stop mode=attached`; the `applysmith serve` process stayed alive until it was killed by the test. |
| 12 | Cleanup | pass | No `Applysmith` or `applysmith serve` process left; no `mygo-*.sock` left; the throwaway workspaces were deleted. |

Unit test: `TestSecondLaunchDuringStartupShowsNothing` (`app/shell_test.go`)
covers a second launch before the window exists. The lock and the focus path
need a GUI session and are covered by the table above, not by a test.

## Hidden smoke run

`make app-smoke` builds the bundle with `smoke.js`; run it without showing a window
by starting the binary directly with mygo's login argument, which makes
`WasOpenedAtLogin` true so the dashboard window is created hidden (the diagnosis
window is hidden by `--smoke` itself), and with a throwaway workspace, `HOME` and
`TMPDIR` (the single-instance socket lives there, so another running app does not
collide; keep the path short, a socket path is limited to 104 bytes):

    HOME=$H TMPDIR=$T/ APPLYSMITH_WORKSPACE=$W APPLYSMITH_PORT=0 \
      APPLYSMITH_SECRET_API_TOKEN=<32 random bytes, base64url> \
      Applysmith.app/Contents/MacOS/Applysmith --mygo-opened-at-login --smoke

The result is the `smoke result` line of `$W/.applysmith/logs/desktop.log`;
SIGTERM ends the app (`quit; server stopped and lock released`). A token shorter
than 32 bytes is refused and shows the diagnosis screen, which the smoke run reads
back too.

Run with mygo 0.5.5 on 2026-10-17: the same results, and
`desktopPreferences` `{reduceTransparency: false, opaque: false}`, so the 0.5
preferences reach the dashboard page at its `http://127.0.0.1` origin.

Run with mygo 0.4.8 on 2026-10-12: health 200 `ok` with `detailVisible: true`,
the session-only write 200, the cookie not visible to script, `Shell.GetShellInfo`
returned the process's own pid with `packaged: true` and `mode: in-process`, the
three refusals (`path_outside_workspace`, `link_refused`, `invalid_extension`),
and `visible=false` for the window.

## Findings and limits

- **Screenshots are unavailable to the test harness.** `screencapture -l`
  failed ("could not create image from window"), so rendering was not seen.
  Window state comes from mygo's own getters, `CGWindowList` and the
  accessibility tree. What the page showed is established by the script
  result read back through `Page.Eval`, not by pixels.
- **Name collision found and fixed.** The first runs used the name
  `Applysmith`, which put `window-state.json` into the legacy app's profile
  directory (the file was removed afterwards). The app is now `Applysmith`; a
  repeat packaged launch wrote only to `~/Library/Application Support/Applysmith`
  and left the legacy directory untouched.
- **Headless dashboard.** `cmd/applysmith/dashboard_on.go` (build tag
  `dashboard`) passes `web.Dist()` to `cli.Main`, which hands it to the server; `dashboard_off.go` leaves
  the placeholder so a plain `go build ./cmd/...` needs no generated bundle.
  `make build` and `make verify` pass `-tags dashboard`. With the
  tag, `TestServeWithDashboardTagServesBundle` checks that `GET /` returns the
  bundle's `index.html`.
- **Keychain not exercised.** All runs supplied the token through the
  environment. The vault reaches the keychain through `/usr/bin/security`
  (not through the app's own signature), but behaviour of the packaged app
  against the real keychain and across an upgrade is still to be recorded.
- **`open --env` passes environment** to a bundle on this macOS, which is how
  the packaged app received its workspace and token. A bundle started from
  the Finder gets no such variables.
- **Not covered by this smoke test** (hide-on-close, Dock reopen and single
  instance are covered by the 2.9 run above): navigation lock and external links, the full bindings list,
  tray count and the other tray items, start at login,
  notifications, log rotation, menus, the disk image and
  the 80 MB release check, Developer ID signing and notarization.

## Native activity and quit (2026-10-15)

Use the existing runs summary as the source for the tray's pending count and
Dock badge, including first load, resolution and connection loss. Before an
owned server quits, fetch fresh activity and offer cancellation when work is
running or the status cannot be checked. Attached servers keep running when
the shell quits. Notification clicks must open their dashboard route and,
when the run still owns a kept tab, focus it through the existing apply API;
an unavailable tab must not prevent opening the run. Verify these paths with
local authenticated API fixtures and a packaged native smoke. Keep workspace
settings and personal runtime files intact. No new model or browser dependency
is required.

Real-model acceptance uses a separate workspace unless an existing configured
workspace is selected. Record the URL-only API result, report, retrieved
Knowledge evidence and model ledger; fixture output is not real-model proof.

The shell reads the authenticated `/api/runs/summary` immediately, then follows
`/api/board/events` and reads it again only on a `runs` notice (a burst within
250 ms is one read) and at each reconnect of the stream (1 s doubling to 30 s).
An idle app sends no request; the code polled every second until 2026-10-17,
which ran the keychain tool for the token each time and was 82 % of the
owner's server log. Needs-you plus blocked entries drive the Dock badge, tray title
and clickable "N need you" ("1 needs you") menu item; the status line also shows running work,
and during a visit of the demo workspace says so (`Running · demo workspace`).
Resolution clears the counters. A failed read clears stale counts and shows
"Activity unavailable" until the next successful read.

**An attached server that stops.** The same reads tell when an attached
server (`applysmith serve`) went away; an in-process one cannot. After 4 failed
reads in a row (they come at each reconnect, 1, 2, 4 and 8 s apart, so about
15 s) and a silent `GET /api/health`, the mode is `lost`: the status line
says "Server not answering", Restart Server is enabled, and a dialog on the
dashboard window says what happened, with Restart Server, View Logs and Wait
(the default). A server restart or a wake from sleep is back within those
reads, and a server that answers its health route but refuses the read (a
token problem) is not lost, so neither shows anything. The dialog comes once
per loss and never brings a hidden window forward (the user may have stopped
that server on purpose); it appears when the window is next shown. Restart
Server runs the startup again: it attaches to the server if it is back, else
starts one in-process (a new port, since the extension's port belonged to the
old one); the page is asked first, as for any restart. Reload and the shell's
other page loads show the dialog again instead of trading a page that
recovers by itself for an error page, and Show Apply Window shows it instead
of a connection error. A read that works again returns the
mode to `attached` and leaves the page alone: sessions outlive a server
restart, so the page's own stream reconnects and reloads what failed
(`TestAnAttachedServerThatStopsIsToldOnceAndABlipIsNot`). Not covered: a
server that comes back on a different port; Restart Server attaches to it. Quit performs a fresh,
bounded read independently of the display. The warning defaults to Keep Running;
Escape, dialog errors and cancellation preserve the app. All native quit entry
points pass through this check, except the system's own quit at log out or shut
down (below); closing the dashboard still hides it.

`TestActivityCountsRefreshAndClearAfterResolutionOrFailure`,
`TestActivityStartsImmediatelyAndStopsWithTheShell`, `TestActivityFollowsTheBoardStreamInsteadOfPolling`,
`TestQuitReadsFreshActivityAndKeepsCancellation` and
`TestBeforeQuitDefersOnceThenAllowsTheConfirmedNativeQuit` cover authenticated
snapshots, clearing, shutdown, active/idle/unknown activity and quit re-entry.

Notification clicks refresh the runs board, open the announcement's route and
focus only that entry's kept application tab through
`POST /api/apply/runs/{id}/show-tab`. If the tab disappeared the run still opens.
An announcement from a previous process recovers its link from the board when
the entry is still present. `TestNotificationClickRecoversItsRouteAndFocusesOnlyItsKeptTab`
covers both current and old clicks, missing tabs, unavailable board reads,
non-application runs and foreign links.

Native smoke builds accept `APPLYSMITH_SMOKE_USER_DATA` to isolate mygo window
state without changing the real user's settings. Set `XDG_RUNTIME_DIR` to a
private temporary directory as well: mygo (0.4.8 to 0.5.5) stores the single-instance
socket there, independently of window state. Keep its full socket path under
100 bytes. The window-state override is absent from release builds.

Packaged native acceptance on 2026-10-15 used an isolated workspace and three
activity fixtures. The actual quit shortcut displayed the warning for one
running task, with Keep Running focused by default; clicking it preserved the
window and server. After the three fixtures were completed, the authenticated
summary showed zero running, pending and blocked entries, and the same quit
shortcut ended the process without a warning. Desktop automation timed out
while inspecting the Dock itself, so visible Dock/tray counter changes and a
fresh OS-notification click remain unverified. The HTTP fixture tests cover
counter updates and kept-tab focus, including a tab disappearing before the
click; those tests are not evidence of a visible native notification.

Further packaged acceptance on 2026-10-15 used release build `19bdc4e` and
only the isolated workspace. A uniquely named notice reached the desktop
stream and was recorded delivered. System Settings automation then stalled
for more than two hours, so neither visible banner delivery nor an OS click
was established. Do not repeat that automation call for acceptance.

The same run opened two local pages in the real managed browser. Calling
`POST /api/apply/runs/{id}/show-tab` with no body changed the active target
from the second page to the first page kept by the test board entry. This
exposed a production contract bug: the desktop notification path sent `{}`
to this body-free route, which the real router rejects with HTTP 400. The
desktop now sends no body. Its regression fixture rejects any nonempty body;
that test failed before the correction and passed afterward. This verifies
real kept-tab activation and the corrected request contract, not an OS click.
The test notice was acknowledged through the API and its managed browser
closed afterward. Source/report data and the formal workspace were untouched.

## Switching to the demo

Owner decision 2026-10-20: the demo workspace must be switchable to and from the
real one without the user stopping a server by hand. Before it, Setup's "Try the
demo" ended with "stop this server, then start the demo's".

- **Home and visit.** Home is the workspace the settings file, the environment
  or the default name (`workspace.Resolve`), as before. A visit to the demo is
  `shell.visit`, in memory only: it beats the chosen folder, the environment and
  the settings file while it lasts, and it is never recorded (`recordChosen`
  skips while visiting), so the settings pointer is not read or written by a
  visit. Picking a workspace on the diagnosis screen ends a visit.
- **After a crash or a quit.** A new process has no visit, so it opens home. A
  Restart Server while visiting restarts the demo (it restarts what is shown).
- **The action.** `Shell.SwitchWorkspace("demo" | "home")` (the target is one of
  two words, never a path; the demo is the default `~/Applysmith-Demo` and must
  carry the demo marker) refuses while the server is starting, errored or being
  restarted, and under a fixed `APPLYSMITH_PORT`. Only the trusted origin can call
  it, as with `ReopenDashboard`. When the app owns the server and the board
  shows work in the `working` state, a native dialog names up to three titles
  and asks; Stay Here is the default and "cancelled" comes back. Then the
  server is stopped, the app starts on the other folder through the same
  startup sequence as a restart, and the window is replaced by one on the new
  origin. An attached server (`applysmith serve` holding home) is left running and
  attached again on the way back. A demo that cannot start puts the app back on
  home with a notice.
- **Entry points.** Setup's "Try the demo" result shows "Open the demo
  workspace" in the app; the demo banner (every page) and the tutorial's last
  step show "Back to my workspace"; the tray and the Window menu show Back to My
  Workspace while the demo is shown. In a plain browser there is no process to
  restart, so the result shows the `applysmith serve --workspace <demo>` command
  and the banner shows `applysmith serve`, each with a Copy button.
- **The extension.** A paired extension holds one address and one token; the
  token belongs to the workspace's database. The new server therefore never
  takes the port of the one it replaced (`launch.Options.AvoidPort`; a
  rejected token would make the extension drop its pairing), and each workspace
  reuses its own last port. While the demo is shown the extension reads
  Offline; after Back to My Workspace the home server takes its old port again
  and the extension reconnects with its pairing intact. Pairing it while in the
  demo pairs it to the demo (its own token and port); going home then shows it
  Offline until a code from Settings pairs it again.

## Reload and the shell's page loads

The WKWebView never shows the question a page asks on `beforeunload` (mygo
has no panel for it), so a native reload or load dropped unsaved text without
asking. Every action of the shell that reloads, replaces or closes the
dashboard therefore asks the page first through `mayLeave` (`app/tray.go`):
View → Reload (Command-R, an item of the app's own, not mygo's Reload role),
the loads of `openPage` (the tray's "N need you", Help → Open Setup Guide, the
Chrome notice's setup step, a notification click, a same-origin
`window.open`), Restart Server and Back to My Workspace from the tray and the
Window menu, and every Quit (Command-Q, the app menu, the tray, the Dock, a
script). `mayLeave` reads `applysmithHeld()` (`web/src/router.tsx`) within 3 s:
when nothing holds the page, or the page has no such function (the diagnosis
screen, a page that failed to load) or does not answer, the action runs at
once. On a held page it shows the window (the question must be seen when the
action came from the tray) and waits on `applysmithLeave()`, the form's own
"Leave without saving?": Leave runs the action, Stay drops it. So the question
always comes before a restart or switch stops the server. The wait is bounded
at 5 minutes, so a page that dies while asking holds nothing for good; a newer
question takes the dialog over and the older one answers Stay
(`TestOpenPageActsOnlyAfterLeaveOrWhenNothingHoldsThePage`, and the page's
side in `web/src/ui/system.test.tsx`). A switch the page starts itself (the
demo banner) asks through `leave()` before it calls `SwitchWorkspace`, so the
binding does not ask again.

- **Log out, restart, shut down.** The system quits apps with the quit Apple
  Event and a reason (`kAEQuitReason`); mygo answers it while the quit
  listeners run, and a held quit is answered "user cancelled", which stops the
  log out. `beforeQuit` therefore holds no quit that carries a reason
  (`sessionEnding`, read in `app/appnap_darwin.go`): neither the page nor the
  running-work question is asked, and the server is stopped after the loop as
  on any quit. Before this every log out was interrupted, as the quit was held
  even with nothing running. Command-Q, the tray, the Dock and a script's
  `quit` carry no reason and ask as above. SIGTERM quits without asking.
- **Closing the window** (the red button, File → Close Window, Command-W) only
  hides it, so the page and its text stay; nothing is asked. The File menu
  exists for that item: without it Command-W did nothing. View also lists
  Enter Full Screen (Control-Command-F).
- **The context menu.** WKWebView's own right-click menu has Reload on the
  page's background and Open Link on a link, both native loads past the
  question; mygo neither configures nor filters it. The shell gives mygo's
  `MyGoWebView` class a `willOpenMenu:withEvent:` that removes the items with
  WebKit's identifiers `WKMenuItemIdentifierReload` and
  `WKMenuItemIdentifierOpenLink` (and any separator left at an edge or
  doubled), then runs the inherited one
  (`TestContextMenusLoseTheEntriesThatLoadThePage`). Copy, Look Up, Back,
  Forward and Open Link in New Window (a same-origin `window.open`, which asks)
  stay.
- **A dead page process.** When WebKit's page process ends (a crash, or macOS
  reclaiming a hidden window's memory) the window would stay blank; the shell
  reloads it once and logs `dashboard page process ended`. A second end
  within a minute is left for the user (Command-R), so a page that crashes as
  it loads does not loop.

## Deferred against the desktop-shell spec

Not implemented yet; each belongs to a later task and none is hidden by a stub.

- Focus on a second launch is unverified: the 2.9 run happened on a locked
  screen. Re-check that the window becomes key and the app frontmost on an
  unlocked session.
- Start at login is wired and tested against a fake system and through the
  smoke build's read of its state; turning it on in a real account, and the
  hidden-window start through the login item, are unverified (owner step in
  [packaging.md](packaging.md)).
- Reopening the dashboard after the server restarted underneath an attached
  window needs a new launch code: `applysmith open` prints one (the open page
  says so too).
- The shell log before a workspace is known (user log directory).
- A foreign-origin frame calling a binding is refused by mygo's trusted
  origins; it has no test of its own.

## Notification click and Dock acceptance completed (2026-10-15)

Release build `b67193f` closed the two earlier native acceptance gaps, using
only local fixtures in the isolated workspace. The initial system setting for
`dev.applysmith.app` was **Allow notifications: off**; Badge application icon was
checked but ineffective under that switch. Enabling notifications for Applysmith
alone made its Dock counter visible. Global notification settings and other
applications were not changed. A delivered stream event alone does not prove
that macOS displayed it.

| Check | Native evidence | Result |
|---|---|---|
| Pending activity | The real Dock item's `AXStatusLabel` changed from absent to `2` for one needs-you and one blocked entry. The app's tray title and menu showed `2 need you`. | pass |
| Fresh notification click | A uniquely titled local notice appeared in Notification Center. AXPress expanded its group; AXPress on that exact single notice opened `/runs/native-click-165906` in the native WebView. The test did not call the app's notification handler or show-tab endpoint directly. | pass |
| Kept browser tab | Before the OS click, the second local tab was active. After the click, authenticated browser status identified the first tab retained by that task as active. | pass |
| Urgent notice semantics | Clicking opened the task and marked it seen; it remained pending until acknowledged. | pass |
| Resolution | Acknowledging the blocked fixture changed the real Dock badge to `1`. Pressing Acknowledge in the remaining task's actual UI cleared the badge; the tray title cleared and summary returned zero pending entries. | pass |

Use bounded native probes if a high-level desktop controller stalls: set the
AX messaging timeout (this run used 0.3 seconds) and execute each probe in a
separate process with a six-second kill timeout. Read Dock via its own AX
application, and identify Applysmith by both its title and bundle URL so the older
Applysmith application cannot be confused with it. The notification center can
be expanded through Control Center's Clock AXPress action. Match a notice's
unique test title before pressing it; a stacked notice takes a separate press
to open its group. Do not clear other notifications. A direct notification
settings link plus bounded AX reads can inspect an app's permission without
waiting on the stalled controller.

Private raw AX snapshots, the Dock screenshot and an asserted JSON summary
were kept under `/private/tmp/applysmith-native-20261015/`. Their summary file is
`desktop-acceptance-evidence.json`; the durable findings and procedure are
recorded here because temporary evidence may be removed. Test events were
acknowledged and their managed browser closed. The formal workspace was
restored and its health endpoint returned ok; all 26 model routes resolved,
and a grounded semantic search reused existing vectors without another
embedding call. No model calls or application submissions were needed for
this acceptance. The repository guard and diff whitespace check also passed.
