# The daily loop

Setup is in [the setup guides](../setup/README.md). To see the loop working before
you set anything up, walk through the [demo workspace tutorial](tutorial.md)
with the fictional persona Alex Example. Once setup is done, a day with
Applysmith is a loop of eight moves. Each move works from the dashboard and, where
noted, from the `applysmith` command line. The dashboard is the place for
anything that submits an application or needs a decision; the command line is
for looking things up and for scripts.

| # | Move | Where | Page |
|---|---|---|---|
| 1 | Scan: find new postings | Discovery page, `applysmith scan` | [Find and evaluate](find-and-evaluate.md) |
| 2 | Inbox: decide what is worth a look | Discovery page, `applysmith inbox` | [Find and evaluate](find-and-evaluate.md) |
| 3 | Evaluate: score a posting against you | Reports page, `applysmith evaluate` | [Find and evaluate](find-and-evaluate.md) |
| 4 | Apply Next: fill, review, submit | Apply Next page, `applysmith apply` | [Apply](apply.md) |
| 5 | Runs board and handoff: finish what a run left for you | Runs page | [Apply](apply.md) |
| 6 | Tracker: keep the status of every application true | Tracker page, `applysmith tracker` | [Track outcomes](track.md) |
| 7 | Gmail outcomes: let replies move statuses | Gmail page, `applysmith gmail` | [Track outcomes](track.md) |
| 8 | Ask Agent and Telegram: ask, draft, get nudged | Ask Agent page, Telegram | [Ask and notifications](ask-and-notifications.md) |

## Starting and stopping

Open the Applysmith app, or run `applysmith serve` in a Terminal window. Either one
is the server for your workspace; only one can run at a time. The app attaches
to a server that is already running and leaves it alone when you quit.

```
applysmith serve
applysmith open
```

`applysmith open` prints a fresh link to the dashboard. The dashboard has eighteen
pages in five groups:

| Group | Pages |
|---|---|
| Today | Home, Apply Next, Tracker, Reports |
| Find | Discovery, Leads and Signals |
| Prepare | Answer Bank, Profile, Wiki |
| Assist | Ask Agent, Agent control, Runs, Browser, Gmail |
| System | Usage, Settings, Setup, User guide |

Press Command-K to jump to any page. Filters, the open row and the open drawer
live in the address, so a reload brings you back to the same place.

Some commands open the workspace themselves instead of asking the server, and
then the server must be stopped; their usage text says so (the `applysmith kb` and
`applysmith documents` writes). `applysmith evaluate`, `applysmith scan` and `applysmith inbox promote`
run in the app when it is open. Others need the server running (`applysmith apply`,
`applysmith autofill`). `applysmith tracker`, `kb search|list|show`, `documents list|show`, and
listing, adding and dismissing inbox rows, leads and blocked companies, work beside a
running server. When in doubt use the dashboard, which always works. A command
refuses a `--workspace` folder that does not exist rather than starting a new
workspace there; `applysmith init` creates one.

## What runs by itself

Schedules (step 15 of setup) run scans and the hiring-signal collection while
a server is running. Gmail needs no schedule: the server reads new mail as it
arrives. A schedule can scan, read mail and send digests; it can never fill a
form, create an account, solve a captcha or submit. Nothing is applied to
without you asking for that posting or that list.

## Rules that always hold

The product enforces these rather than asking you to remember them; the list
and the reasons are in [policies](../architecture/policies.md).

- A dry run fills and stops before Submit. Submitting needs your explicit
  request for that application, or a confirmation you complete in the
  dashboard.
- Employers on your batch skip list are left to you in a batch.
- Your work-authorization and self-identification answers come from you; the
  product leaves such a question blank rather than invent one. Required legal
  declarations (the information is true, a background check, electronic
  signature) are ticked unless you turn off "Tick required legal declarations"
  in Settings, Runtime settings; optional marketing opt-ins never are.
- A posting you apply to twice is stopped.

Applysmith backs up the workspace by itself every night at 02:00 (or when the
computer next wakes): a verified snapshot in a folder beside the workspace,
`~/Applysmith-backups` for `~/Applysmith`, keeping up to the newest 14. Back up by
hand as well before an upgrade or a move:

```
applysmith backup create --out <new folder>
applysmith backup verify <folder>
```

A backup or a nightly snapshot is restored into a new workspace folder:

```
applysmith backup restore <folder> --to <new workspace folder>
```
