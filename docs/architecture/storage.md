# Storage

Three places hold data, and nothing else does: files you edit, runtime state,
and the operating-system keychain. The detail is in the `workspace-and-config`,
`runtime-storage` and `credentials-vault` specs.

## The workspace

One folder you choose (default `~/Applysmith`). It must be on a local disk, not
inside iCloud Drive, and not your home folder or a source checkout. It holds:

| Item | What it is | Edited by |
|---|---|---|
| `profile.yml` | Identity, work authorization, targets, history, skills, preferences | you, the setup writers, the dashboard |
| `answer-bank.yml` | Your answers to form questions | you, the dashboard |
| `portals.yml` | Sources, filters and employers to watch | you |
| `policy.yml` | Safety settings: cooldowns, captcha switch, remote access | only you |
| `cv.md`, `narrative.md`, `voice.md` | Your CV, story and writing samples (`voice.md` optional) | you |
| `resumes/` | Registered résumé PDFs | you |
| `transcript/` | Your academic transcript: one PDF, attached to a form's transcript upload | you, the dashboard |
| `knowledge/` | Your knowledge base: snippets, briefs, stories | you, the dashboard |
| `reports/` | Evaluation reports, each written once | the application |
| `documents/` | Generated résumé and cover-letter versions with their checks | the application |

Files with personal data are created readable by you only. Edits go through a
revisioned path that keeps comments, validates against a schema and refuses a
stale write; a change you make by hand in an editor is noticed. A keyed edit
changes only the lines of the key it names, and is refused (use the raw
editor) when it cannot prove that: a file with mixed or unusual line breaks,
or an edit whose decoded result differs anywhere outside that key. A file that
fails validation is reported with the key that is wrong and nothing else is
changed. A key whose name ends in `password`, `secret`, `token` or `api_key`
is refused: secrets never live in these files. A path in a file or a request is
resolved inside the workspace; `..`, absolute paths and links that leave it are
rejected.

## The state directory

`<workspace>/.applysmith/` is runtime state and nothing else is outside it:

| Item | Purpose |
|---|---|
| `applysmith.db` | SQLite in write-ahead-log mode: the tracker and its events, the report index, scans, the inbox, runs, the submission ledger, schedules, Gmail, hiring signals, agent sessions, memory, the model-call ledger |
| `browser/` | The apply window's Chrome profile |
| `logs/`, `snapshots/`, `downloads/`, `backups/` | Logs, page snapshots of apply pages, downloads in progress, backups: the earlier contents of each workspace file a save replaced (newest 10 per file), and the database copy taken before each schema upgrade (`pre-upgrade-v<N>-<time>.db`, newest 3; each is a whole database) |
| `server.lock` | Which server owns the workspace |
| `workspace-id` | A random identifier that names this workspace's keychain entries |
| `tunnel.json` | Present only while a tunnel runs |

The directory is created private to you (mode `0700`, files `0600`). SQLite is
the only runtime source of truth: the tracker is not a text file, and reports
are readable Markdown files that are indexed in the database and never
rewritten for a status change. Safety facts are enforced by the database, not
only by callers: a second claim for the same posting is refused while one is
submitting or submitted (the submission ledger reads and writes the claim in
one transaction of the single writer); tracker status and its event commit together; at every
start, runs left running are marked interrupted and are never resumed
automatically. As it starts, the server opens its four query-only connections in
the background, each parsing the schema, so the first page load does not wait
for them.

## Secrets

API keys, passwords, the Gmail app password and the Telegram bot token live in
the macOS keychain under the service `applysmith`, account
`<workspace id>:<name>`. A fixed registry lists every kind of secret; the
reference page [setup/reference.md](../setup/reference.md) lists them with the
setup step that uses each. A host without a keychain can supply a secret
through the `APPLYSMITH_SECRET_<NAME>` environment variable. A secret value is
never written to the workspace, the database, a log, an API response, a
notification or a model prompt, and a redactor replaces any occurrence in
outgoing text. Whether a secret is set is a run of the keychain tool (tens of
milliseconds, seconds on a busy machine: the process starts one tool at a
time), so a vault keeps that answer: after 30 seconds it is still given while
one lookup runs beside the caller, and only the first ask for a name waits. A
write or delete through the same vault forgets the answer at once; one made by
another process shows at the first ask after those 30 seconds and its lookup.
Values are never kept.

Chrome's own profile under `browser/` is a credential-bearing exception; it is
excluded from backups and diagnostics.

## Backups

```
applysmith backup create --out <new folder>
applysmith backup verify <folder>
applysmith backup restore <folder> --to <new workspace>
```

A backup takes a consistent snapshot of the database, copies your files and
records hashes in a manifest. It leaves out the browser profile, cookies,
keychain values, logs and temporary downloads, so restored credentials must be
set up again. Restore goes only into a new empty workspace, which gets a new
identifier; remote devices are signed out, schedules and the desktop and
Telegram notices are off until you turn them on (Discovery, Schedules and
Settings, Notifications), interrupted runs end as a restart ends them (an
application run waits for your review), and a submission that was being pressed
is marked may-have-submitted. Nothing is replayed automatically. A backup of a demo workspace records that in its manifest and
the restored workspace carries the demo marker, so the fictional persona stays
on the machine.

While the app or `applysmith serve` runs, the scheduler also writes one such
backup a day, from 02:00 local time, to a `<workspace folder>-backups` folder
next to the workspace, and keeps the newest 14 of them (fewer if they pass
2 GiB, never fewer than two). It deletes only the `snapshot-<date>-<time>`
folders it made. See [scheduler.md](scheduler.md).

See also [setup.md](setup.md), [api.md](api.md) and [development.md](../development.md).
