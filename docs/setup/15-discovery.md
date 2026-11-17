# Step 15: Job sources and schedule (optional)

## What this step does

It chooses where postings come from and how often Applysmith looks. Sources are
public job-board lists (the ATS registry), employer career pages you add,
aggregators and the browser-driven boards. A schedule pack runs the scans for
you every day. If you skip it, no postings are found automatically; add them
by hand with `applysmith inbox add <url>`. `applysmith setup skip discovery`
records the decision.

## Before you start

Know the employers or boards you care about. Browser-driven sources
(`linkedin`, `indeed`, `glassdoor` and the others) read pages in the scan
window and click nothing that writes; the ones marked "session" need you to be
signed in there ([step 10](10-browser.md)).

## Steps

See every source and whether it is ready:

```
applysmith scan --list
```

Choose a schedule pack and, if you want, fetch the public board lists now.
Pack ids: `none` creates nothing, `daily` scans each enabled source from 07:00
local time and then evaluates the postings the scans left waiting,
`twice-daily` adds a second round from 17:00. `use_target_roles` puts your
target roles from [step 5](05-targets.md) into `portals.yml`: the title filter,
and the saved search of each enabled board that has none (a dry run lists what
it writes). A new workspace enables no job board, so without more the daily
scans read only the registry. `enable_boards` (with `use_target_roles`) turns
on the boards that need no key or sign-in, Built In, Amazon Jobs, Indeed,
ZipRecruiter and Google Jobs, each searching for your primary target role; the
pack then scans them too. LinkedIn, Glassdoor, Jobright, TheirStack and Exa
need a sign-in or a key and are turned on in Discovery.

```json
{"pack": "daily", "seed_registry": true, "use_target_roles": true, "enable_boards": true}
```

```
applysmith setup apply discovery --file discovery.json --dry-run
applysmith setup apply discovery --file discovery.json
```

The same packs, and more, from the command line:

```
applysmith schedule packs
applysmith schedule apply-pack <pack>
```

Add employers and filters by editing `portals.yml` (one shape: `filters`,
`companies`, `sources`). Run one scan by hand:

```
applysmith scan registry
```

### Optional: the SEC source

The hiring-signals source `signals-edgar` sends your contact email with every
request to the SEC, as its fair-access policy requires. It stays off until you
acknowledge that disclosure and your profile holds an email:

```json
{"enable_edgar": true, "acknowledge_edgar_disclosure": true}
```

## Where this is stored

Sources, filters and employers are in `portals.yml` in your workspace. The
registry, the schedules and scan history are in the workspace database.

## How to check it worked

```
applysmith doctor --only discovery
```

```text
[PASS] discovery.schedules   1 schedules, 1 enabled
[PASS] discovery.registry    <n> boards, <n> active
[PASS] discovery.schedule-hygiene   no duplicate, dead or idle schedule
[PASS] discovery.schedule-audit     <date>: <n> due, <n> ran, 0 skipped, 0 deferred, 0 missing
```

The last two read your schedules and never change them. `schedule-hygiene`
warns about twin schedules, a disabled schedule whose last run failed, an
enabled schedule whose newest three runs failed, and one that came due but has
never run in 14 days. `schedule-audit` compares today's due times with what the
scheduler recorded and warns about a time with no record, or one that was used
up while another job held the resource.

## If something goes wrong

| What you see | What to do |
|---|---|
| `no schedule pack was chosen` | Send a pack, including `none` to say you will scan by hand. |
| `the registry is not seeded`, or Company job boards says `needs boards` | Press Download the board lists on that row of Discovery, Run, or Seed the registry on Discovery, Sources and portals. At a terminal, send `"seed_registry": true` or run `applysmith registry seed`. A list that cannot be fetched is reported and does not fail the step. |
| A source says `needs searches` or `needs companies` | It reads only saved searches, or the companies you track, and has none yet. Save a search or add a company on Discovery, Sources and portals ("Use my target roles" saves one search per source from your target roles), or type a search under Options on that row to run one now. |
| A source says `needs key` | It needs an API key for that provider. Save it in Settings, Scan defaults, Scan source API keys (the link on its Discovery row goes there); it is stored as `api-key-<provider>` ([reference.md](reference.md)). |
| A browser source is `blocked` | A site challenged the scan window. It cools down; `--ignore-cooldown` forces a run. |
| Schedules do not fire | They fire only while a server runs. Start the app or `applysmith serve`. |
| You want everything off for a while | `applysmith schedule pause-all` (or Pause all on the Schedules tab); `resume-all` turns on only what it paused. |
| You want to see the times before applying a pack | `applysmith schedule preview <pack>` lists the next fire times and saves nothing. |
| `portals.yml` is rejected | The message names the key; there is exactly one accepted shape. |

Next: [Step 16: Chrome extension](16-extension.md).
