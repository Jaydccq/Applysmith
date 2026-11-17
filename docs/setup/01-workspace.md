# Step 1: Create the workspace

## What this step does

It creates the workspace: one folder that holds everything that is yours (your
profile, CV, answer bank, policy, résumé PDFs, reports and generated
documents) and a hidden `.applysmith/` folder for runtime state (the database,
the browser profile, logs and backups). Without it there is nowhere to keep
your profile, CV and applications.

The default location is `~/Applysmith` (a folder named Applysmith in your home
folder; `~` means your home folder).

## Before you start

Pick a location on the local disk. These are refused: a network share, a folder
inside iCloud Drive, your home folder itself, and a folder inside a checkout of
the Applysmith source code. The reason for the iCloud rule is that file syncing
can rewrite a database while it is open.

## Steps

Create the workspace in the default place:

```
applysmith init
```

Or in a folder of your choice:

```
applysmith init --workspace <folder>
```

Expected output:

```text
created profile.yml
created portals.yml
created policy.yml
created cv.md
created narrative.md
created answer-bank.yml
workspace ~/Applysmith: 5 of 5 files are still templates; fill them in step by step with `applysmith setup`, or on the Setup page of `applysmith serve`
```

`init` also creates the database. It never overwrites a file that exists, so
running it again is safe.

Every other command finds the workspace in this order: the `--workspace` flag,
the `APPLYSMITH_WORKSPACE` environment variable, the location recorded when you
chose one in the setup flow, then `~/Applysmith`.

To do the later steps on the dashboard's Setup page, start the server:

```
applysmith serve
```

`applysmith serve` prints the address of the dashboard and keeps running; leave it
open in its own Terminal window. The desktop app does the same thing when you
open it, so you need only one of the two. To print a fresh sign-in link for the
running dashboard:

```
applysmith open
```

## Where this is stored

| What | Where |
|---|---|
| Your files | `~/Applysmith/profile.yml`, `answer-bank.yml`, `portals.yml`, `policy.yml`, `cv.md`, `narrative.md`, and the folders `resumes/`, `transcript/`, `knowledge/`, `reports/`, `documents/` |
| Runtime state | `~/Applysmith/.applysmith/`: `applysmith.db` (the database), `browser/` (the apply window's Chrome profile), `logs/`, `backups/`, `snapshots/`, `downloads/`, `workspace-id` |
| The chosen location | A small per-user settings file named `workspace.json` in the macOS user configuration folder; it holds the path only. Creating the files of another workspace (for example from the Setup page of a server started with `--workspace`) does not replace a recorded workspace that still exists; only choosing a path, or a recorded workspace that is gone, changes it |

Files that hold personal data are created readable by you only. Passwords and
API keys are never written into the workspace; they go to the macOS keychain
(see [reference.md](reference.md)).

## How to check it worked

```
applysmith doctor --only workspace
```

```text
[PASS] workspace.location       ~/Applysmith
[PASS] workspace.files          7 workspace files readable
[PASS] workspace.migrations     schema 36, every migration applied
```

## If something goes wrong

| What you see | What to do |
|---|---|
| `workspace_invalid` naming "home directory", "network volume", "iCloud" or "source checkout" | Choose another folder with `--workspace`; see Before you start. |
| `workspace.migrations` warns "schema 0 applied" | Start `applysmith serve` (or open the app) once; it applies the database migrations. |
| `server_already_running` | One server per workspace. Use the one that runs, or quit it first. |
| A file reports a validation error | The message names the file and the key. Fix that key; no other file is touched. |
| You deleted the `.applysmith` folder | The database, browser profile and workspace identity are gone and secrets saved for the old identity are orphaned. Restore the newest nightly snapshot from the folder beside the workspace (`applysmith backup restore ~/Applysmith-backups/snapshot-<date>-<time> --to <new-workspace>`), another backup, or start again. |

Next: [Step 2: CV](02-cv.md).
