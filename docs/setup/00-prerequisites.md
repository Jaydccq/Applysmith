# Step 0: Check this computer

## What this step does

It checks that this computer can run Applysmith. Applysmith supports macOS 13 or
later; other operating systems are unverified. It needs Google Chrome 120 or
later (the apply window is a Chrome window of its own) and at least 2 GiB of
free disk space for the database and the browser profile.

Nothing is installed or stored by this step. It only looks.

## Before you start

Install Google Chrome from google.com/chrome if it is not in the Applications
folder. Chromium, Edge, Brave and Chrome for Testing are not accepted.

## Steps

Run the check:

```
applysmith setup apply prerequisites
```

Expected output:

```text
prerequisites: complete
```

The same facts, one line each:

```
applysmith doctor --only prerequisites
```

```text
[PASS] prerequisites.platform   macOS 15.0
[PASS] prerequisites.chrome     Google Chrome 150.0.0.0
[PASS] prerequisites.disk       40.0 GiB free
```

The version numbers on your machine will differ.

## Where this is stored

Nowhere. The step probes the machine every time and keeps no record.

## How to check it worked

```
applysmith setup status
```

Step 0 reads `complete`. `applysmith doctor --only prerequisites` shows three
`PASS` lines.

## If something goes wrong

| What you see | What to do |
|---|---|
| `prerequisites.platform` fails | Update macOS to 13 or later. |
| `prerequisites.chrome` fails with "not found" | Install Google Chrome into the Applications folder, or point the `APPLYSMITH_CHROME_BIN` environment variable at the Chrome executable. |
| `prerequisites.chrome` fails with "too old" | Open Chrome, choose Chrome > About Google Chrome, and let it update. |
| `prerequisites.disk` fails | Free at least 2 GiB on the volume that holds your home folder. |

Other errors: [reference.md](reference.md) lists the exit codes. Next:
[Step 1: workspace](01-workspace.md).

Want to see the product working first? Once this step passes, `applysmith demo
init` creates a separate demo workspace with a fictional persona; the
[tutorial](../user-guide/tutorial.md) walks through it. It does not count as
your workspace and changes nothing of yours.
