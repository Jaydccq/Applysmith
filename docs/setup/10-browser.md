# Step 10: Apply window

## What this step does

Applications and some scans run in a Chrome window that Applysmith controls: the
apply window, with a second, hidden scan window beside it. It has its own
Chrome profile, so your everyday Chrome (its tabs, passwords and extensions)
is never touched. Without the apply window nothing can be applied to or
scanned in a browser.

## Before you start

Finish [step 0](00-prerequisites.md) (Google Chrome 120 or later).

## Steps

Run the check. It opens the windows if they are not open and verifies that they
do not advertise automation to websites (the page's `navigator.webdriver` flag
and the browser's user agent). In the app, or any dashboard, press Check the
apply window on this step. At a terminal, while no app or server holds the
workspace:

```
applysmith setup apply browser
```

Then see which job sites the window is signed in to. It only loads and reads
account pages; it clicks nothing:

```
applysmith browser check
```

Sign in to the job sites you use directly in the apply window yourself (for
example a board you scan or an employer's candidate account). This is optional.
Sign-ins stay in the window's own Chrome profile.

## Where this is stored

The Chrome profile is in `~/Applysmith/.applysmith/browser/` (cookies and sign-ins
of the apply window). It is excluded from backups. The last check result is in
the workspace database.

## How to check it worked

```
applysmith doctor --only browser
```

```text
[PASS] browser.apply-window   the apply window is attached (just now)
[PASS] browser.fingerprint    navigator.webdriver is false in every window and no user agent is headless (just now)
[WARN] browser.signins        ...
```

`browser.signins` is never required. With `--live` the doctor re-runs the check
for the first two lines under a shared 45-second limit.

## If something goes wrong

| What you see | What to do |
|---|---|
| `the apply window has not been checked` | Press Check the apply window on this step; at a terminal with no app or server holding the workspace, run `applysmith setup apply browser`. |
| `chrome-missing`, `chrome-too-old`, `chrome-unsupported` | Install or update Google Chrome; only Google Chrome 120 or later is used, never another browser. |
| The window shows a bot check | Clear it yourself in the window. Solving it automatically is a separate, limited option; see [policies](../architecture/policies.md). |
| The fingerprint line fails | Update Chrome, restart the app and re-run the check. |
| A second window opens on top of your work | Runs never raise or focus the window; if one does, report it. |

Next: [Step 11: Gmail](11-gmail.md).
