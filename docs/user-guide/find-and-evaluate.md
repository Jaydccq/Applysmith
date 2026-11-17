# Find and evaluate

## 1. Scan

A scan reads a source and puts new postings in your inbox. Open Discovery: the
Run tab lists every source with whether it can run now. Start runs one; Options
runs it once with a search, place or limit you type. A source that cannot run
says what it needs and links where to put it right: an API key in Settings, a
sign-in on the Browser page, the board lists to download (Company job boards),
or a saved search or tracked company on the Sources and portals tab. Monitor
follows a running scan and History lists the finished ones.

From the command line, list the sources and whether each is ready:

```
applysmith scan --list
```

Run one:

```
applysmith scan registry
```

A scan on a schedule does the same; set it with the schedule pack of setup step
15, see what ran with `applysmith scan --runs`, and open the Discovery page to
start one, watch it or change the sources. The browser-driven sources
(`linkedin`, `indeed`, `glassdoor`, `google-jobs`, `ziprecruiter`, `newgrad`,
`jobright`) read pages in the scan window and click nothing that writes. A site
that blocks a scan cools down for a while; do not force it unless you must.

Filters live in `portals.yml` in your workspace (titles to include or reject,
locations, employers to skip). "Use my target roles" (setup step 15, or the
Sources and portals forms) adds your profile's target roles to the title filter
and as the saved search of each enabled board that has none; it shows what it
writes first. Employers you never want to see are a separate list:

```
applysmith blocked add <name>
applysmith blocked list
```

## 2. Inbox

The inbox holds postings waiting for a decision: Discovery, Inbox. Evaluate
scores one now, Dismiss says no thanks and remembers it (Restore takes that
back), and ticked rows are evaluated or dismissed together. From the command
line:

```
applysmith inbox list
applysmith inbox show <id>
applysmith inbox add <url>
applysmith inbox dismiss <id>
applysmith inbox promote <id>
```

`add` puts a posting you found yourself in the inbox. `promote` queues a posting
for evaluation ahead of the scans' own (`--top N` promotes the N best-ranked).
`dismiss` says no thanks and remembers it. `applysmith inbox export` writes the
inbox to a file.

You do not have to promote: the daily and twice-daily packs end with an inbox
evaluation that scores up to 20 waiting postings a run, best first, until the
day's model spend cap, or an evaluation task's own budget, is reached (Settings,
Models, or `applysmith llm cap` and `applysmith llm budget`). A scan
schedule you made yourself is followed by the same inbox evaluation while no
inbox evaluation schedule is on, a pack's or your own. A posting that failed twice waits for you, and one whose
page says it is closed leaves the inbox as expired. A scan schedule in `screen`
mode never fills Apply now; the schedule editor and `applysmith schedule add` say so.

The Leads and Signals page holds companies the system noticed before they post
(from public filings and feeds), with the evidence for each. Watch a company
you like, add its job board to your portals (Add to portals) or evaluate a
lead now (Evaluate). Each signals pass keeps the end of its time for checking
the hottest companies' job boards and putting their postings in your inbox.

## 3. Evaluate

Evaluation reads the posting, checks the hard rules (sponsorship stance,
location, pay, seniority) without a model, screens it cheaply, and only then
runs a deeper model evaluation. The result is a report with a score from the
evidence, not a guess.

On Home, paste a posting's address or its text into Start from a job post and
press Evaluate. The note under the box follows the run and links the report
when it is written; every report is on the Reports page. From the command line:

```
applysmith evaluate <url>
applysmith evaluate --text "<pasted posting text>"
applysmith evaluate <inbox-row-id> --mode deep
```

`--mode` is `auto`, `screen` or `deep`. A page that cannot be read without the
browser, when the browser cannot start (no usable Google Chrome), makes the
command exit 4 and say why; paste the text instead. The text goes in
`--text` or `--file` (a bare argument must be a posting URL or an inbox row id)
and holds at least 200 characters. With no model set up the command exits 3 and
says how to set one up. Reports are written
once to `reports/` in your workspace and listed on the Reports page, each with
its score, band and how the posting was verified (a pasted posting is marked
unverified).

How the score is used:

- 4.0 and above, and passing the eligibility gate: the row appears in the Apply
  now lane of Apply Next.
- 3.5 up to 4.0: the Selective lane, for a closer look.
- Below 3.5: the product recommends skipping and the row never appears in
  Apply Next.

To read a posting closely before deciding, decode it: what the team needs, the
must-haves and nice-to-haves, hidden signals in the wording, the level and team
stage, red flags, and how your profile meets each requirement, with
deal-breakers marked. It is not a score and writes no report.

```
applysmith evaluate decode --file posting.txt
```

Skill gaps across several reports, on the Reports page or:

```
applysmith evaluate gaps --report <number>,<number>
```

Next: [Apply](apply.md).
