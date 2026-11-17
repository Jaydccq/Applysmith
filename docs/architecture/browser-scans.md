# Browser scans

Task 6.2, the browser half: `linkedin`, `linkedin-feed`, `indeed`,
`ziprecruiter`, `google-jobs`, `glassdoor`, `newgrad` and `jobright`. Code is in
`internal/discovery/browser_*.go`; the leads they file are in `leads.md`.

| File | What it holds |
|---|---|
| `browser_window.go` | The `Window` and `Tab` seams. A tab can navigate, run a read-only script and press a Cloudflare challenge checkbox; it cannot type |
| `internal/launch/scanwindow.go` | The one `Window` of the product: scan-lane tabs of the managed browser, nothing else |
| `browser_scan.go` | `BrowserEnv` (clock, random source, host probe), the cooldown, the host-pressure preflight, freshness, listing-only rejections |
| `browser_page.go` | One list page: wait for cards, retry, classify a wall (sign-in, challenge, rate limit), the attempt record |
| `browser_board.go` | The contract shared by the list sources: paging, empty versus broken, warm-up, frontier |
| `browser_detail.go` | Detail reads: parallel, four per company, partial results kept, the bounded Apply follow-through |
| `browser_scripts.go` | Every page script. Each starts with an `/*aj:name*/` marker; none writes |
| `browser_linkedin.go`, `browser_boards.go`, `browser_newgrad.go`, `browser_jobright.go`, `browser_feed.go` | The sources: URL builders, card extractors, per-board detail selectors |
| `browser_host.go` | The memory-pressure and load probe |

## Decisions

- A source adds a `boardSpec` (URL builder, extractor, card-to-posting) and gets
  the whole contract. Extraction runs as a script in the page and returns JSON;
  Go parses it, so the parsing is tested against canned JSON and the scripts
  against synthetic pages in Chrome.
- Clicks go through one script (`clickJS`) that refuses a control whose text,
  label or class names a write action, after the selectors already name only
  pagination, show-more, card, the overlay's own close control, the Apply
  follow-through and a comment thread. There is no typing path.
- A Cloudflare managed challenge on a list page ("Just a moment...",
  "Additional Verification Required") is cleared the way the old Indeed lane
  did, by owner decision 2026-10-25 (`internal/browser/cloudflare.go`,
  `bscan.clearChallenge`). The checkbox is measured, never guessed: one
  `DOM.getDocument` pierce of the page finds the widget frame through the
  closed shadow root (`src` on `challenges.cloudflare.com` or an id starting
  `cf-chl-widget`); the frame is another site, so Chrome runs it out of
  process and the page pierce stops at it; the frame's own target is attached
  and pierced for the `input[type=checkbox]` in its second closed shadow root,
  and that box is moved by the frame's content origin. The press is trusted
  input (`Input.dispatchMouseEvent`); Turnstile ignores a script click. An
  attempt is up to 3 presses within 25 s, re-measured before each (the widget
  re-renders), the first one 4 s after the checkbox appears, and passes when
  the challenge or its token state changes. A run makes at most 2 attempts: a
  page that went clear is read again, and a failed attempt opens the page again
  so the second one meets a fresh widget. Live check (the app's own browser,
  Indeed): presses 1.5 s after the frame appeared went unanswered three times,
  while a press about 7 s after the load passed at once; the measured box was
  the widget's label, which forwards the press to the checkbox. An
  unmeasurable checkbox is `target-not-resolved` and nothing is pressed, since
  a missed press reads as Cloudflare refusing a real one (old lane, 2026-08-12).
  A failed attempt's verdict is in the run's error ("checkbox
  clicked-no-pass"); the run detail counts `challenge-clicks` and
  `challenges-cleared`. Press-and-hold, image puzzles, and any other wall
  still end the run `blocked`. `challenges.cloudflare.com` is HSTS-preloaded,
  so the Chrome fixture serves its frame from another host and names it by id.
- Lane choice: a scan tab opens through `browser.SelectScanLane` with the
  source's main host (`SourceInfo.Host`). By default it is a scan-lane tab. With
  the runtime setting `watchScans` on (Settings, Runtime settings; read per tab
  by `launch.ChromeWindow`) it is an unfiltered tab in the apply window under
  one lease that the watched tabs of the process share and the last one
  releases. A guarded board (Indeed, ZipRecruiter, Glassdoor, Google, LinkedIn)
  stays in the scan lane whatever the setting says, and when a run holds the
  apply lane (`lane-busy`) the scan quietly stays in the scan lane. The reverse
  also holds: while a watched scan is open an apply run waits for the window.
- The challenge-rate baseline (`Repo.ChallengeBaseline`) counts, per browser
  source and period, the pages a scan opened (`list-opens` plus `detail-opens`
  in the run record) against the scans that ended `blocked` with reason
  `challenge` or `rate-limited`; skipped and running runs are left out. Settings,
  Detection and cooldowns shows it with `GET /api/policy/risk`. The source
  funnel (`Repo.SourceFunnel`) joins scan runs, evaluation jobs and tracker rows
  into fetched (a posting read by two scans counts twice), new to the inbox,
  evaluated, failed or gated, strong (4.5 or more), applied,
  responded and interview per source; it is the top of Discovery's History tab
  (`GET /api/discovery/funnel`).
- The cooldown is derived, not stored: the latest run that was not skipped ended
  `blocked` and its end plus `cooldown_hours` is still ahead.
- A posting is opened at the URL the board gave (`Posting.fetchURL`), not at its
  canonical form, so a canonical `https` URL never replaces a page's own scheme.
- `newgrad` reads Jobright's minisite page, not newgrad-jobs.com: that site is
  a Webflow shell (category tabs, an iframe) and carries no jobs. The page
  `<jobright>/minisites-jobs/newgrad/<country>/<category>` is server-rendered;
  `ngListJS` returns the 50 jobs of its `__NEXT_DATA__` in the shared card
  shape. The page has no date filter and no further pages in the markup, so the
  scan window is applied locally. The source stays in the browser lane because
  the Jobright detail enrichment needs a rendered page. Fixture:
  `internal/discovery/testdata/newgrad_minisite.html`, run through the real
  script in headless Chrome by `TestNewGrad_Chrome_ReadsTheMinisiteDataAndEnrichesFromJobright`.
- Jobright posting pages carry their facts in `__NEXT_DATA__`
  (`props.pageProps.dataSource.jobResult`): the apply link, `salaryDesc`,
  `publishTime` (UTC, exact; the card only says "3 hours ago"), and the
  booleans `isH1bSponsor`, `isCitizenOnly` and `isClearanceRequired`. A
  citizen-only posting is worded "no sponsorship" so the profile's sponsorship
  filter drops it; a flagged clearance is worded for the clearance signal. The
  recommend feed renders two links per card (body and match score), so the list
  script keeps one row per posting id. Checked against the live pages 2026-10-17.
- Ad hoc `--city`/`--state` set the search location of Indeed, ZipRecruiter,
  Google Jobs, Glassdoor and LinkedIn as a saved search's `location` does.
- Glassdoor's results page ignores a location keyword (it redirects to "United
  States" and drops it); it applies a location id (`locT=C&locId=1139761`).
  `glassdoorLocate` (the board's `locate` hook, run before the first page)
  opens `/Job/index.htm` in the scan tab and asks the site's own typeahead,
  `/autocomplete/location?locationTypeFilters=CITY,STATE,COUNTRY&caller=site-header&term=<place>`,
  from inside that page; the exact name match wins, else the site's best match.
  A place it cannot resolve (empty answer, lookup refused) fails that query
  rather than searching the whole country; a saved `url` is left as written.
  The old `findPopularLocationAjax.htm` answers 404 now (checked 2026-10-17);
  the typeahead needs no sign-in, but it does need a page of the site, so a
  signed-out plain HTTP request (Cloudflare "Just a moment") and a headless
  browser that announces itself as `HeadlessChrome` cannot call it.
- Glassdoor cards print no age. The results page's own Next.js data carries
  `jobview.header.ageInDays` for each listing, keyed by `jobview.job.listingId`
  (the card's `data-jobid`); the list script reads it and the row is dated
  from it.
- Glassdoor results are addressed by the site's own search path (checked live
  2026-10-18): `/Job/<words>-jobs-SRCH_KO0,<n>.htm`, or with a place
  `/Job/<place>-<words>-jobs-SRCH_IL.0,<p>_I<locT><locId>_KO<p+1>,<p+1+n>.htm`,
  the offsets naming the place and the words in the slug, plus `fromAge`.
  `/Job/jobs.htm?sc.keyword=...&fromAge=7` redirects there and drops
  `fromAge` and `p`: 90,026 listings of every age (up to 177 days) instead of
  5,540 from the last week, which is where the old "a fromAge=7 page lists
  220-day-old listings" came from. No address turns a page (`p=2` and
  `_IP2.htm` serve the first page again), so the board reads one page
  (`maxPages` 1). The words come from the slug alone; `sc.keyword` is ignored,
  so "c++ developer" searches "c developer". A posting page names the
  employer in `EmployerProfile_employerNameHeading`, which the detail read
  takes when a card has none.
- Live check of the other session boards (2026-10-18, the app's browser over
  CDP): Jobright signed out serves its landing page at `/jobs/recommend`
  (then rewrites the address to `/`), and the landing marquee links 20
  postings; they were read as the feed. The list script now skips links in
  the landing page or marquee, and a live address off `stayOn` after the
  load is a sign-in wall too. ZipRecruiter renders every card twice (desktop
  and a hidden mobile copy) and links no posting; the row is the results
  address with `lk=<card id>` (the page shows that posting in its pane; a
  bare `?lk=` does not), `lk` names a posting on `/jobs-search` for the
  non-posting filter, and the detail read takes the pane
  (`serp-job-details-title`, `employer-details-header`, the
  `whitespace-pre-line` description), since the page's h1 is the result
  count. Before, every row's address was the page's own (`ABS('')`) and all
  of them were filtered as `non-posting-url`. Checked again live 2026-10-25
  (the app's browser): ZipRecruiter now renders one `li` per posting holding
  `article[data-testid="job-card-<id>"]`, the title in
  `h2[data-testid="serp-job-card-title"]`, the company a `/co/` link and the
  place a `location=` link; the list script reads both layouts. A search with
  no place searched 25 miles around the place ZipRecruiter guessed from the
  IP (0 results around Myrtle Beach, SC, against 116 in the United States),
  so `zipURL` sends `location=United States` when the query names none.
  Google Jobs ignores the 3-day chip (cards 2 to 6 days old) and also places
  results by IP, so a 24-hour window often keeps none of its 10 cards; that
  is the board, not a parsing gap. Google Jobs (`udm=8`) cards are
  `div[data-share-url]` (`.tNxQIb` title, `.a3jPc` company, `.FqK3wc` place
  with "• via <source>" cut, the age an unclassed span); the detail viewer is
  the group `[data-is-preview="false"][aria-label^="Job details for"]`
  beside hidden previews of the neighbouring postings, under a feedback
  dialog's h1. The viewer lists one "Apply on X" link per site that carries
  the posting, each a `google.com/goto?url=<opaque token>` anchor with
  `target=_blank`: the destination is unreadable from the page and a click
  opens a real tab the popup stub never sees, so following the first link
  learned nothing (an employer-URL gap) or, where the first link was an
  aggregator, the aggregator. The detail script now returns every apply
  control (`data-aj-apply="<index>"`), the one whose site names the posting's
  company ("Apply on Truist Careers - Truist Bank", "Apply on Delta Careers"
  for Delta Systems; `employerApply`) is followed ahead of the first, and a
  link to the board's own redirect is opened in the throwaway page instead of
  clicked; the landing address is screened as before. With no
  company-named link the first is followed as before. Live sample (30
  postings, three queries): Google listed the employer's site first whenever
  it offered one (3 of 20 in the measured pair), and a direct open of its
  goto link landed on the employer's careers page. The LinkedIn feed and content search render posts as
  `[componentkey^="expanded"][componentkey*="FeedType_"]` with no urn and no
  permalink: the key stands for the urn, the body is
  `[data-testid="expandable-text-box"]`, the author and headline are the
  paragraphs beside the first profile link (degree and age dropped). Before:
  0 posts on both pages, now 7 and 6. The thread of a post that says the link
  is in the comments opens through the action bar's `button[aria-label="Comment"]`
  (its text is the count; there is no separate count control on this layout)
  inside `[componentkey^="expanded<key>"]`; the press loads the comments and
  an empty reply box, and nothing is typed or sent (the click guard still
  refuses like, reply, send, follow and the rest). A comment is a
  `CommentComponentReference_<urn>`: the name is the first text of the
  aria-hidden span in its profile link, its links are those of its
  expandable text box, and the post author's own comment is recognised by
  the same profile address (the author's comment carries an "Author" badge
  that breaks the name), else by the name. The read polls up to six times
  for the first comment. Live (owner's feed, 8 threads opened): every press
  loaded the thread (1 to 4 comments), names came out clean, and one
  author's two comments with two links were read; before, the toggle named
  `data-urn` and no thread opened. A posting page's JSON-LD
  `datePosted`, when it has one, dates a row the card left undated (posting
  pages were behind a Cloudflare challenge for the isolated browser used to
  check this, so that path is covered by a fixture only).
- LinkedIn's signed-in results page (checked live 2026-10-18) renders each
  card as `[componentkey="job-card-component-ref-<id>"]`, a `role=button`
  with no link and only hashed class names. `linkedInListJS` takes the id
  from the componentkey, the title from the first paragraph's aria-hidden
  span (the visible text adds "(Verified job)"), company and place from the
  next two paragraphs, and the age from the "Posted … ago" paragraph; a card
  the account already opened says "Viewed" instead and stays undated. The
  older linked cards are still read. The posting page keeps its description
  in `[componentkey^="JobDetails_AboutTheJob_"] [data-testid="expandable-text-box"]`
  and the company in `[aria-label^="Company, "]`; its title is a bare
  paragraph, so the card's title stands. Its "similar jobs" cards are whole
  links ending in "· Easy Apply", which is why the detail script counts a
  quick-apply control only when its label starts with the quick-apply words.
  The old layout yielded no rows, which a scan with a freshness window
  reported as `empty-window` and one without as `no-cards`.
- The same page rewrites its address: it keeps `keywords`, `f_TPR`, `f_AL`
  and `geoId`, drops `location` and `f_WT`, and then searches the account's
  own region ("Greater Richmond Region, 30 results"). It reads place and
  workplace from the words instead, so `linkedInURL` writes them there
  ("remote backend engineer in Austin, TX" answers "Austin, TX (50 mi)");
  this holds for a saved classic URL too. A search with no place stays in
  the account's region. The classic `/jobs/search/` page still honours the
  parameters but announces it is being retired.
- Inspect LinkedIn through the app's own browser (its CDP port), never from a
  separate headless Chrome on the same profile: on 2026-10-18 the first such
  `HeadlessChrome` visit found the session revoked (`li_at` gone, the page
  `/uas/login`), and nothing else that day had touched LinkedIn but the
  managed browser.
- Challenges are never answered. A blocked page ends the run; the old Indeed
  challenge-clearing is not ported.

## Tests

`go test ./internal/discovery` runs the contract on a fake window
(`browser_fake_test.go`). `make test-browser` also runs `browser_chrome_test.go`
(`-tags browser`): headless Chrome, a throwaway profile, `--host-resolver-rules
"MAP * 127.0.0.1"` so no real site is requested, and a local server that plays
every board.

The selectors come from the synthetic pages. The live smoke check of each board
(task 6.9, `internal/discovery/live_smoke_test.go`, 2026-10-12) found that
from a headless throwaway profile Indeed,
ZipRecruiter and Google Jobs answer with a challenge, new-grad-jobs.com has been
rebuilt and yields no cards, and the session boards were not run. A headed run of
the first three is still owed to the owner.
