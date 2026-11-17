# Packaging

How the desktop app becomes a `.app` and a `.dmg`, what was measured about
it, and what only the owner can finish (task 7.4). The app itself is described
in [app.md](app.md).

## Build

    make package

generates the frontend and engine bundles, runs `go tool mygo build` in `app/`
(pinned in `go.mod`, mygo 0.5.5) and then `go test -run TestPackagedBundle ./app/`
against the result. Output, ad-hoc signed (identity `-`), in `app/build/darwin-arm64/`:

| File | Content |
|---|---|
| `Applysmith.app` | The bundle: `Info.plist`, `PkgInfo`, `_CodeSignature`, one Mach-O (`Contents/MacOS/Applysmith`), and in `Contents/Resources` `NOTICE`, `NOTICE-dependencies.txt` and `licenses/` |
| `Applysmith 0.0.1 arm64.dmg` | The disk image whose window invites dragging the app to Applications |

`app/build/` is ignored (`.gitignore`), and the repository guard's build-output
rule rejects `.app`, `.dmg` and files under `build/` if one is ever staged;
`git add -A -n` after a build lists nothing from `app/build`.

`TestPackagedBundle` (it skips unless `APPLYSMITH_APP_BUNDLE` names a bundle,
which `make package` sets) checks the Packaging requirement:

- the sum of the bundle's file sizes is at most 80,000,000 bytes (it fails and
  names the size otherwise; `make release-check` builds the same bundle and
  disk image from the export and enforces the same bound in `internal/release`);
- `NOTICE`, `NOTICE-dependencies.txt` and `licenses/` are in the bundle;
- no browser, Node, Python, TeX or Poppler executable, venv or database file is
  in the bundle;
- the bundle holds exactly one `.icns`, the app's own icon (see "Icon");
- `codesign -dv` reports the identifier in `app/mygo.json` and
  `codesign --verify --deep --strict` passes.

The bundle identifier is `dev.applysmith.app` (provisional until publication,
design Open Question 4) and the name is `Applysmith`. Building three times (with
the version changed in between) gave the same `Identifier=dev.applysmith.app` and
three different CDHashes. The bundle is `Applysmith.app`; mygo derives the data
directory from the app name, which must never be `Auto Job`, the legacy desktop
app's (`app/name_test.go`).

The product was called AutoJob until 2026-11-15, when the owner renamed it
Applysmith everywhere: the app and bundle (`dev.autojob.app` became
`dev.applysmith.app`, the computer-use helper `dev.applysmith.personal-cu`), the
command, the Go module, the `APPLYSMITH_` environment variables, the workspace
(`~/Applysmith`, state in `.applysmith/applysmith.db`), the Keychain service
(`applysmith`) and the application-support and log folders. No code reads the
old names. The owner's data was moved once by hand: the workspace folder, its
state folder and database renamed with the app quit, the old absolute paths in
the history columns of `apply_runs`, `evaluation_jobs` and `scan_runs` rewritten,
the Keychain entries of the workspace copied to the new service, and the support,
log and `~/.config` folders renamed. macOS asks again for the permissions it had
granted to the old bundle identifiers.

## Icon

`app/icons/icon.png` is the 1024x1024 RGBA source configured by
`app/mygo.json`. It shows the original cartoon career companion cat on a sage
rounded square, with the cat enlarged to fill the icon and only minimal
transparent outer margins. `mygo build` creates
`Contents/Resources/AppIcon.icns` and `CFBundleIconFile` from the PNG.
The dashboard avatar uses the same character. See the
[artwork prompts and visual decisions](../design/career-companion.md).
The icon was generated as raster artwork; there is no SVG source to regenerate.
Keep the alpha channel when resizing or replacing it. The directory is named
`icons` because the repository guard exempts native icons from its file-size
limit and its build-output rules do not match that directory.

`macos.icon` (mygo 0.4.6: an Icon Composer `.icon` or a compiled `Assets.car`
that macOS 26 prefers to the `.icns`) is not used: both need `actool` from
Xcode 26, and this machine has only the Command Line Tools (`xcrun actool`
fails). mygo skips a `.icon` it cannot compile and keeps the `.icns`, so the
choice would be untestable here. When Xcode 26 is available, draw
`AppIcon.icon` in Icon Composer, add `macos.icon`, and keep the PNG as the
fallback.

## Measurements

Machine: macOS 26.2 (Darwin 25.2.0) arm64, 8 cores, 16 GB, Go 1.27.1, mygo
0.4.1, measured 2026-10-12. The machine was shared with other builds, so the
load average (printed with every sample) was between 28 and 40 for the
numbers below; they are upper bounds, not a performance claim.

### Size

| Artifact | Size |
|---|---|
| `Applysmith.app` (sum of file bytes) | 39,890,680 bytes (39.9 MB; `du` 38,980 KiB) |
| of which `Contents/MacOS/Applysmith` | 39,860,512 bytes |
| `Applysmith 0.0.1 arm64.dmg` (lzma) | 11,468,230 bytes (11.5 MB) |

| Bound for the bundle | 80,000,000 bytes |

The table is the 2026-10-12 measurement before the mygo upgrade, at an earlier
commit. After the upgrade to 0.4.8 and the app icon, `make package` gave a bundle of 41,085,277 bytes
(binary 40,699,776) and a disk image of 12,178,852 bytes (12.2 MB). Moving from mygo 0.4.1 to 0.4.8 changed the stripped app binary by 288 bytes
(41,051,858 to 41,052,146 bytes; the tree itself had grown since the first
measurement); the icon adds 355,019 bytes. `hdiutil info` lists no image after
the build (0.4.6 detaches the disk image by its device).

The binary carries the dashboard bundle, the engine, the extension and the
document templates and fonts. The `applysmith` CLI built with `-s -w` is 39.1 MB;
it is not in the bundle (see "The `applysmith` command" below).

### Startup to a served dashboard

`TestPackagedStartup` (opt-in, `APPLYSMITH_MEASURE_STARTUP=1`, see the test's doc
comment for the command) launches the bundle's binary with a temporary
workspace and a temporary `HOME`, `APPLYSMITH_PORT=0` and the API token from the
environment, and measures the time from process start until the lock names the
process, `GET /api/health` answers `ok` and the dashboard page comes back for
the launch code. Every process is stopped by its PID with SIGTERM and checked
to be gone. A window and a menu-bar item appear for a moment on the screen.

| Case | Samples | Time to a served dashboard |
|---|---|---|
| Cold: a fresh copy of the bundle, a new workspace | 670, 737, 973 ms | median 737 ms |
| Cold, very first launch of a just-built bundle (signature and page-in) | 1 sample: 2,812 ms | |
| Warm: the same workspace, bundle already launched | 199, 214, 223, 273, 387, 405, 412 ms | median 273 ms |

"Cold" here means a new workspace (first migration, answer-bank seed) and a
bundle file the system has not read recently; the operating system's file cache
cannot be purged without administrator rights, so a reboot-cold start was not
measured. Resident memory of the app process 4 seconds after launch was 129 to
141 MB. The WebView's helper processes are separate and were not counted, so
this is not the full process-tree figure; that one, with the other measurements,
is in [performance.md](performance.md).

### Keychain prompts and upgrades

The vault reaches the keychain through `/usr/bin/security` (go-keyring), so the
keychain item belongs to that tool and not to the app's signature. Measured
with the real login keychain and a workspace id used by nothing else: an
ad-hoc build A (0.0.1) created the API token, then ad-hoc builds B (0.0.2) and
C (0.0.3) with different CDHashes and the same identifier, then A again, then A
once more, each launched against the same workspace, each reading the token
(`APPLYSMITH_MEASURE_KEYCHAIN=1` in the same test). Two upgrades, one downgrade:

| Step | Keychain access | Authorisation windows before and after launch |
|---|---|---|
| A (creates the token) | allowed, token written | 1 -> 1 |
| B, upgrade 1 | allowed, token read | 1 -> 1 |
| C, upgrade 2 | allowed, token read | 1 -> 1 |
| A, downgrade | allowed, token read | 1 -> 1 |

The one authorisation window is the lock screen's (the session was locked); a
keychain prompt would have raised the count and blocked the launch until
answered. Neither happened. The test deletes the entry afterwards and checks it
is gone. Keychain mode keeps the real `HOME`, because the token must reach the
user's login keychain: the test copies the app's extension folder
(`~/Library/Application Support/Applysmith/extension`) aside and puts it back when
it ends, and the app's startup lines stay appended to the real
`~/Library/Logs/Applysmith`. This was measured with ad-hoc signing only: the result follows from
the `security` indirection. The selected distribution is ad-hoc signed; a
Developer ID upgrade measurement is outside that distribution policy. A prompt
can still appear for an app that reads the keychain directly. The setup guide
says the same.

### External document tools (not in the bundle)

From `applysmith documents toolchain setup` (the toolchain's own budget, printed
without installing anything):

| Tool | Install size |
|---|---|
| Private venv, TeX cache, smoke record | about 30 MiB written by setup; one 0.4 MB pypdf wheel downloaded |
| TeX | a full TeX Live about 9.1 GiB, of which the templates read about 19.7 MiB; a minimal install was not measured |
| Poppler | about 81 MiB, about 206 MiB when Homebrew upgrades shared libraries |
| Python 3.11 or newer | Homebrew python@3.14 about 88 MiB when none is installed |

These are separate from the 80 MB app bound and are shown to the user before
setup runs.

## Native bindings

The dashboard calls `window.mygo.call("Shell.<Method>", ...)`; the shell
injects it only into pages of the server's origin. In a plain browser it does
not exist and the dashboard hides every control that needs it (`web/src/desktop.ts`).

| Binding | Behaviour |
|---|---|
| `GetShellInfo` | version, build, mode, pid, packaged, notifications permitted, log directory, and start at login read from the system on every call |
| `RevealInFinder(path)` | shows a file or folder of the workspace in Finder; a relative path is relative to the workspace; a path that resolves outside it (including through a symbolic link) is refused with `path_outside_workspace`, a missing one with `path_not_found` |
| `SetStartAtLogin(bool)` | the system's login item (mygo `SetOpenAtLogin`); off by default; an app started by the login item opens with the window hidden and the tray visible |
| `PickFile(extensions)`, `PickFolder()` | native chooser; the extensions are letters and digits only, none is `invalid_extension`; cancel returns an empty string |
| `OpenExternal(url)` | `http`/`https` up to 4,096 characters without credentials, and `mailto`; anything else is refused as `link_refused` and logged without its query; a launch failure shows a notice with the address and a Copy action |
| `OpenInApplyWindow(url)`, `ShowApplyWindow()` | call the server with the local API token (`/api/browser/apply-window/open` and `/show`); a missing Chrome shows a notice with Open Setup Guide, which loads the Apply window step |
| `ReopenDashboard()` | mints a launch code with the local API token (`POST /api/launch-codes`) and loads the dashboard with it in the page that asked, so the signed-out page can start a new session; the page never receives the code |
| `SwitchWorkspace("demo" \| "home")` | shows the demo workspace or the user's own by restarting the app's server on the other folder and loading the dashboard there (see "Switching to the demo" in [app.md](app.md)); answers `started` or `cancelled`; refused as `invalid_workspace_target`, `demo_not_found` (no demo folder, or one without the marker), `switch_busy`, `server_not_ready` or `fixed_port` |
| `QuitApp()` | quits |

The window is also locked to the server origin: a navigation or `window.open`
to another origin goes through the same link rules, and camera, microphone and
location requests are denied. The tray has Show Apply Window and View Logs.
The Window menu has Open Dashboard (Cmd+Shift+D), Restart Server and, only
while the demo is shown, Back to My Workspace, the same entries and handlers as
the tray; Restart Server is enabled only while this app
owns the server. Every window sets `WKPreferences.tabFocusesLinks`
(`reachLinksWithTab`), because a WKWebView otherwise skips links in the Tab
order; mygo does not set it.

Dashboard side: Settings, Files shows "Reveal in Finder" for the workspace
folder and a "Start Applysmith when I log in" checkbox (re-read from the system
each time the section is shown); the document drawer shows a "Reveal in Finder"
button beside the saved PDF path. Component tests cover both the presence and
the absence of the bridge (`web/src/desktop.test.tsx`); the Go rules are tested
in `app/bindings_test.go` with a fake of the operating system. The real
IPC path was exercised with the smoke build (`make app-smoke`, `--smoke`): from
the dashboard page `Shell.RevealInFinder("/etc/hosts")` rejected with
`path_outside_workspace`, `Shell.OpenExternal("javascript:...")` with
`link_refused`, `Shell.PickFile([])` with `invalid_extension`, and
`Shell.GetShellInfo` returned the log directory and `startAtLogin: false`.
Not run against the real system: turning start at login on (it would add a
login item to the account running the tests), Reveal in Finder on a real file
(it opens a Finder window), and the pickers.

## The `applysmith` command

The setup guide expects `applysmith` to answer in Terminal after the drag-install.
The bundle holds one executable, the app, and it is also the command line:
`app/main.go` checks the base name of `argv[0]` first and, when it is `applysmith`,
calls `cli.Main` (package `internal/cli`, which `cmd/applysmith` calls as well) and
exits, before the single-instance lock, the window or any framework call. A
process started as `Applysmith` starts the app. This adds no second executable, so
the bundle stays well under the 80 MB bound (owner decision R-3, 2026-10-12).

Settings, Advanced, Command line makes or removes the symlink
`~/.local/bin/applysmith` to the running binary's resolved path
(`GET`/`PUT`/`DELETE /api/system/cli`, `internal/cliinstall`). Only the
dashboard session on the loopback listener may change it; a file or symlink at
that path that is not an app binary is refused and left alone; each change and
refusal is a `cli_install` row in the gate audit. Whether `~/.local/bin` is on
`PATH` is read from the server's own `PATH` and, because a program started from
the Finder has a short one, from the login shell (`$SHELL -i -l`, 3 s). When it
is not, the page shows the line to add to the shell profile; the app never edits
a profile. The link is refused when the server does not run from a `.app`
bundle (a loose binary, `go run`).

Because the app binary is not called `applysmith`, `applysmith mcp --print-config`
prints the path the command was started by (the link) rather than the app
binary it resolves to; a client started with the app path would open the app.

Tests: the `runs as the applysmith command` subtest of `TestPackagedBundle` (the
bundle's own binary through a symlink named `applysmith`: version, help, routes
and an unknown command; `make package` runs it, so the plain suite does not
build the app), the `internal/cliinstall` and `internal/api` `CLIInstallAPI`
tests, and the web `CommandLine` tests. The subtest opens no window: the
process exits as the command line before the framework starts.

## Remaining distribution acceptance (7.4 stays open)

The owner selected ad-hoc signed, unnotarized distribution on 2026-10-12
(desktop-shell specification). Developer ID signing, notarization and stapling
are not release requirements. The following checks still need an interactive
owner session or a fresh macOS account:

- verify the exact downloaded, quarantined artifact with `codesign --verify`,
  record Gatekeeper behavior, then launch it through the guide's per-app
  Open Anyway step;
- first launch of that artifact in a fresh macOS user account;
- the fresh-user guided setup and generation of both PDFs, with TeX, Python and
  Poppler installed by the guide;
- turning start at login on and off on a real account, and Reveal in Finder on
  a real file.

Until they are done, source and local builds are ready and the public binary
release is not. The setup guide must not recommend disabling Gatekeeper.
