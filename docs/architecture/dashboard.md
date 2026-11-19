# Dashboard

`web/` is a React and Vite single-page app. `make generate` builds it into
`web/dist`, `web/embed.go` embeds it, and `applysmith serve` (built with
`-tags dashboard`) serves it. Component tests run under vitest with happy-dom
(`cd web && bun run test`); `internal/cli/dashboard_pages_test.go` serves the
embedded bundle and checks deep links, asset caching and the first-load budget.

## Structure

- `src/api/`: the one typed client (`client.ts`), runtime decoders (`decode.ts`
  for the envelope and scalars, `decoders.ts` for response shapes) and the
  board stream (`board.ts`). Types come from the generated, committed
  `src/wire/index.ts`. In `src/api/client.ts`, identical plain GETs in flight
  at once share one request; a write sends later reads to the server again,
  except the setup check and a schedule's preview (`postCheck`), which write nothing, and is counted
  (`writesSent`) as it leaves and as it ends.
- `src/router.tsx`: a history router; filters, the open row and the selected
  report live in the query string.
- `src/hooks.ts`: `useResource` reloads a request when a board topic it
  depends on moves (not on the first snapshot after load), and a failed one
  when the board stream connects again (a restarted or reachable-again
  server: the snapshot's id names the server run even before its first
  notice); a component subscribes to its own topics' revisions only. Notices
  are coalesced: the reload follows once its topics have been quiet for
  250 ms, and at least every 2 s while they keep moving. One write announces
  several topics and a working run announces each change; on the owner's log
  Home's 13-request Needs-you load ran 906 times in 12 h, 600 of them under
  2 s after the previous one (ten in one 1.8 s burst). A load
  that names itself (`{ keep: "home.day" }`; one call site per name, as the
  minified build can give two loads the same code) keeps its last answer for
  the session by name and inputs: a page opened again draws it at once and
  asks again `quietly`, a read the reveal does not wait for. After a write,
  a moved topic revision or another server run the page shows loading and
  waits instead. The refetch is never skipped: most `configuration` and
  `inbox` changes are not announced. With every read held
  400 ms (headless Chrome, demo), going back to Home or Tracker revealed in
  65 to 84 ms instead of 482 to 516 ms. `useInterval` polls only while
  the page is shown (not closed to the menu bar, minimized or covered) and asks
  once when it is shown again. The Runs page's poll (3 s while a run works)
  is `GET /api/runs/summary` alone: any read of the board syncs it with the
  run records, the board announces what changed, and that notice reads the
  board and the inbox; without a live stream the poll reads both itself. A topic that moves while the page is hidden
  waits the same way: `useResource` reloads once as the page is shown again.
  On the owner's log Home's day read ran 120 times in one night hour while
  runs announced themselves, each with the rest of Home's reads. The runs
  summary behind the count in the tab's title follows its topic while hidden
  too (`whileHidden`): a background tab is where that title is read.
- `src/desktop.ts`: in the desktop app, `followDesktopPreferences` marks the
  root `data-opaque` while macOS Reduce Transparency is on (mygo 0.5's
  `window.mygo.preferences`); the top bar then drops its blur. Contrast and
  motion follow `prefers-contrast` and `prefers-reduced-motion` on their own.
- `src/ui/Markdown.tsx`: a single-pass renderer building React elements,
  memoised on its text so a streaming answer does not reparse earlier
  messages. Report text is never set as HTML; raw markup shows as text. Every
  scan is linear and has a 500 ms test over 100 KB of each hostile shape.
- `src/pages/`: Home, Apply Next, Runs, Browser, Discovery, Leads and Signals,
  Tracker, Reports and Settings, each beyond Home a separate chunk, the
  others' chunks fetched once the opening page has settled;
  `src/pages/Setup.tsx` and `src/pages/setup/` are the setup wizard (below).
  `src/pages.ts` lists all eighteen routes, each with its page. `src/ui/`: the
  shared components (below).
- The browser panel (`BrowserPanel` in `src/pages/browser/Lanes.tsx`, beside
  every page; the top bar's button or Mod+Shift+B) shows the apply window's
  tabs like an agent browser's side panel: a tab strip naming each tab's holder
  (a run's tab gets a cursor icon), Back, Forward, Reload, a centred address
  that expands while editing, and a live view inert behind "Click anywhere to
  use the browser" until clicked. A tab a run drives is never activated: a blue
  glow, "Applysmith is using this tab" with the run's link, and the run's cursor
  gliding to each click. A tab a stopped run kept says "A run stopped here and
  left this tab to you" with "What the run did" (`/runs/{id}`, where Done and
  what comes next live). A failed browser names its error above Start
  browsing. The strip is one tab stop: arrows, Home and End move between
  tabs. A status read that fails keeps the tabs last read, greyed, under the
  error and "Last read at ..." (`lastRead` in `ui/parts.tsx`, as `Panel`
  does). A run's kept tab opens here from "Show it in the browser panel" on
  Home's Needs you, the Runs inbox and the run drawer (the board entry's
  `focusTab`), or from the deep link `/browser?target=<tab id>`. Open state
  and width live in `localStorage` (`applysmith-browser-panel`, wrapped in
  try/catch). It docks beside the page,
  narrowed so the page keeps 560 px; below 440 px it covers the page; on a
  phone it is full screen (`useDockedWidth` in `src/Shell.tsx`). Routes and
  limits: [browser.md](browser.md#browser-panel-live-view).
- The Tracker row drawer opens the Interview prep drawer (`?interview=<id>`):
  stages in date order with their time and day ("tomorrow"), opening on the
  next one still ahead; Edit or Confirm details records a stage again; a row
  still `applied` or `responded` offers Move it to Interview (versioned PATCH);
  after the last stage, the debrief first and the next stage, offer or
  rejection; a stage that is over with no feedback reads "No debrief yet" in
  the list; a stage's pack with its build time, what changed since (stage,
  feedback, stories, its brief, a newer evaluation report than the one it read), its sources and their
  verification state; the debrief; and a practice drill (pack, story-gap and
  library questions) with coaching, a run the drawer follows (again after a
  reload, the answer back in the form; a failed one keeps it), whose answer stays for another try and can
  start a story interview (`POST /api/kb/stories/mining` with `notes`). Close,
  a switch to another stage, Cancel stage and Next question ask before
  dropping an unsent debrief or answer. A pack
  run names its stage in words, and the stage it builds when another is shown
  (also for one followed on opening: `packRun.stage`); its new revision is
  shown only on that stage. A pack's gaps and next actions link where each is
  settled: the story bank, the brief form filled with the company and the
  posting (`/wiki?tab=briefs&company=&url=`, built on the press), Home's
  posting box, the row's documents, or the drawer's stage details, debrief
  box and practice (its question picker); a next action whose fix (kind and
  target) a gap already links keeps its sentence without a second link. The
  workspace's next actions are
  steps too: add a stage (the Add a stage form), and generate the pack of or
  confirm a stage other than the one shown (it is shown, at its Generate
  button or details form); the shown stage's own are its buttons. A pack copies as Markdown
  with its stage and each question's stories; coaching renders as Markdown, and
  earlier practice keeps each answer beside its coaching. A pack run is a document run: started with an `Idempotency-Key`
  and followed through `GET /api/documents/runs/{id}` until it ends
  ([interview.md](interview.md)). Wiki shows the story shapes beside the
  coverage grid, under the bank; a theme's story count filters the bank, which is searchable and
  shows per story when it was edited and the applications whose prep used it
  (`uses`, linked to `/tracker?interview=<id>`) or that none has; its Pages tab (the Knowledge page is named Wiki, `/wiki`; a page read is `?page=<id>`, which renders the Markdown with links to other pages followed inside the Wiki and lists "Linked from"; "Add with Ask" opens Ask with "Add to my wiki: " in the composer) leads with "Drafts to review" (every
  unpublished draft, imports included: Confirm per pending statement, Publish,
  Edit, Reject; a story draft also picks its themes there, and its Publish
  waits for two and says how many are missing) and ends with "GitHub repositories" (`/api/kb/sources`): each
  source with its state, the commit and time of the last read, what that read
  used, skipped and left out for secrets (paths only), how many drafts it wrote
  and its notes (a failed read's reason). "Ingest now" starts a background run
  (`POST .../{id}/ingest`, with an `Idempotency-Key`; 202 and a run id, the work
  on the server's own context, so leaving the page does not stop it). The row
  reads "Reading..." with a link to the run, follows its stream, and shows a
  failure's reason; an `up-to-date` outcome offers "Read again anyway"
  (`force`). The board shows a `kb.ingest` task, "Read owner/repo", ending
  with "Read owner/repo at abc1234: 12 drafts". The sources and the draft
  queue reload when the run ends and on the `runs` topic. Its drafts appear in
  Drafts to review.
  "Remove" asks first: the source, its cached file text and its unpublished
  drafts go, published snippets stay; it is disabled while that repository is
  being read. Above the list sits the GitHub account (`/api/kb/github`): signed
  out it offers "Sign in with GitHub" (the device flow: the code with a copy
  button and an "Open github.com/login/device" link, polled every 2 s while it
  waits; a build without an OAuth App says so), signed in it reads "Signed in as
  login (via the app | via the gh command)" with Sign out for the app's own
  token (the gh command cannot be signed out here, and the card says so). Signed
  in, "Choose one of your repositories" opens a picker (a live GitHub read, so
  not on every visit) that lists them (`/api/kb/github/repos`, search
  over the loaded ones, a Private chip, "Added" for sources, Load more) and
  "Read into the wiki" adds the repository and starts its read as one step. "Add a repository" takes owner/repo or a
  GitHub address (the server refuses anything else). The optional GitHub token
  (vault `github-token`, else the `gh` command) has its own form there
  (`PUT/DELETE /api/secrets/github-token`): stored or not, never shown again,
  not removable when only the environment sets it. A write of the page's own
  reloads the list.
- Optional panels, closed until opened, never a top-level page and never a fit
  band: the Tracker's Outcome review (`GET /api/tracker/outcomes`: cutoff, each
  rate as numerator and denominator, open and unknown counts, why a small or
  unfinished group cannot be compared) and Reports' Skill gaps
  (`POST /api/evaluations/gaps`: sample, exclusions, gaps by state, inferred
  signals apart, a confirm per gap, a learning plan for up to five confirmed
  gaps).
- Tracker: one field searches company, role and note text (`q`, the list's
  `query`: any word, best matches first, a company or role hit above a note-only
  one; the `tracker_note_text` SQL function drops each note line's
  `{date} {actor}: ` prefix so "agent" finds only what a note says; a row found
  only through a note shows that note under its role; export takes it too), the Company, Role (`role_asc`) and Status (`status_asc`, table order) headers
  sort, ties broken by recency then id, and a company a link named is shown;
  status chips keep the other filters, and "In flight" is applied + responded
  + interview + offer in one chip (`status` takes a comma-separated list, as
  the list route, export and `applysmith tracker list --status` do); "Applied"
  keeps rows applied to in the last 7, 30 or 90 days (`appliedWithin`, days
  back from today by the application date; rows with none never match) and
  "Source" matches the row's source text, and export takes both; `reached` or
  `movedBy` (a status or an actor) with `since` (a local day) keeps the rows
  moved to that status or by that actor on or after that day, shown as a
  `ClearChip` that clears it: the links of Home's Today applied count, its
  "Gmail moved N tracker rows" line and the weekly funnel's tracker stages, so
  each opens exactly the rows it counts (archived rows are neither counted nor
  listed); "Follow-ups due" (only
  when one is; the first three, so the applications start in the first
  screen, the rest behind "Show more") records, snoozes (3, 7 or 14 days, behind More)
  or drafts a follow-up to copy (as `applysmith followup-draft`; nothing is sent); a row with
  every follow-up sent, or past `close_after_days`, offers "Close as no reply" (to discarded, the status for a row
  closed without a rejection) with Undo back to applied; a reply check offers "I answered" (a
  note on the row, which ends the check; Undo takes the note back); both presses quote the row's
  version (the card's, then the close's), so a row that moved since the card was drawn is
  refused and the list reloads; and the three cadence numbers
  and the follow-up limit are edited in Settings, Applications ("Follow-up cadence",
  `tracker.followup.*` in policy.yml, saved with the same revision as the quotas).
  A status change in the drawer takes an optional note and, for a change to applied,
  "Applied on" (`on`, today by default, not later than today), with an Undo that puts the old
  status back against the version the change made; the typed note stays
  through other writes to the row (a note added below, Undo) until its own change; "Add application" takes
  "Applied on" (`appliedAt`, for the statuses of a row applied for) and "Source". The
  drawer's Archive / Unarchive (`POST /api/candidacies/{id}/archive` and `/unarchive`, rejected,
  discarded and skipped rows only) says what it did with an Undo; a mass sweep stays on the CLI. The list links the archived rows its filters also match, and the status counts include archived rows when the list does. "Possible duplicates" (only when dedup finds a group) merges one group after a confirm, with an Undo for 15 minutes, the keyboard then on the kept row (or on Undo when the page does not list it), or says why it is left to the user; Not duplicates keeps a group apart until its rows change, with an Undo. The drawer's History reads newest first, ten events then Show more, each note with its words, a refused automatic move with the tracker's reason. The export links carry the list's filters and sort; they close the
  selection bar's line after the row count, so they show only while rows are listed. A row's drawer drafts a follow-up (rows
  after applying) or an outreach note (as `applysmith outreach`): the kind or
  contact, a name and what the user remembers are the only inputs about the
  person; each message and alternative shows its character count against the
  limit, its Copy button and what its claims rest on. Ticked rows, on any page until the filters or sort change, take
  one status in one request (`POST /api/candidacies/bulk-status`), confirmed
  above 5 rows; Undo sends each changed row back to its own old status, one versioned PATCH each; a row that changed since is left and counted.
  Archive / Unarchive N selected (`POST /api/candidacies/bulk-archive`) leaves and counts rows still in flight, with an Undo for the rows it moved.
  With the page ticked, Select all N matching ticks every row the filters match (up to 100, one change's limit).
  The same Undo note (`UndoNote`) follows every one-press move: it holds focus while the
  row it came from is gone, and keeps it when the move is undone. A form that is
  ready for the next entry (Add a note, Change status) keeps focus instead
  (`keepFocus`); its live region still reads the note.
- The Tracker's table reads a row's source under its date, so the role keeps
  the room a column of its own would take, and the row count closes the
  selection bar's line.
- "Last 7 days" (`WeekFunnel`, under Home's tiles and on the Tracker): a tile
  per stage with the change on the week before and the share of the previous
  stage (left out above 100 %), each opening its list.

## Design system

One stylesheet, `src/styles.css`, holds the whole visual language; pages use
its classes and the `src/ui/` components, never styles of their own.
`src/styles.test.ts` and `src/pages/setup/styles.test.ts` hold it to its
contract.

- **Visual language.** The [career companion design](../design/career-companion.md)
  pairs original cartoon cat artwork with cream paper, sage surfaces and forest
  actions. Shared tokens in `src/styles.css` control every page in light and
  dark modes. The Home cover is `src/ui/CareerCover.tsx` (only on a workspace
  with no tracker row, see Home and Ask Agent); its links open
  Discovery and Ask Agent without starting work. The cover is a decorative WebP
  with real, selectable interface text over it; narrow containers place the
  illustration below the copy. Native icon artwork and web avatar share the
  same character. Interface type uses locally available rounded sans fonts,
  without fetching fonts. Cards use quiet edges and rounded corners; controls
  keep a raised boundary distinct from disabled fills. Forest identifies the
  primary action and selected navigation; amber means waiting, red an error,
  blue information, and green a positive state. Text and control contrast is
  checked by the existing stylesheet tests. Route names, live counts, the
  command palette, browser panel, theme control and phone navigation retain
  their behavior. Dialogs, drawers and existing transitions continue to respect
  reduced motion, contrast and desktop transparency preferences.
- **Ask Agent.** Your messages are right-hand bubbles with the fork action on
  hover or focus (always on touch); answers are plain text; the live turn has a
  shine over its heading and three rising dots; the page follows a streaming
  answer while you are at its end; the composer is one rounded box fixed to the
  window bottom. Enter with key code 229 (WebKit confirming an input-method
  candidate, sent with `isComposing` already false) does not send. A finished
  turn that used tools folds them behind "Worked for 2m 14s" (user message to
  last row, from `createdAt`) with the failed count and one sentence ("Used
  list_applications (2 calls) and find_report."), then a divider and the
  answer. The running turn stays open; a live step reads as its phrase, then
  its past tense ("Read the profile") or "... failed". A held call asks in
  place of the composer
  ([ask-agent.md](ask-agent.md#confirmation-cards-and-standing-approvals)).
- **Long forms.** A profile tab is a column of titled sections (`FormSection`:
  title and purpose beside the fields from 1180 px, stacked below); fields sit
  two to a row (`SaveForm columns`, `.form-grid.two`) under small uppercase
  group headings; a long field takes the row (`data-wide`). `RowsEditor` shows
  one quiet card per record with an icon remove button; a record of up to four
  short fields (a skill, a role) is a table row, column names on the first row
  only from 640 px. A yes or no question is a segmented switch with nothing
  picked until the user picks. "Still an example" is one small line under its
  field.
- **Answer Bank.**
  - List: grouped by category under sticky headings (names through
    `humanize`; a search makes one list). A row shows the stored answer under
    the question and opens on a click anywhere; one needing an answer has a
    warn or bad bar; off-screen rows skip layout (`content-visibility`). Row
    chips are states only (to answer or blocking, ask me, from Setup,
    different on N sites, to review). The drawer names who wrote the answer
    (`Source:` user, default, learned or setup); an entry nobody answered
    yet shows its row's to answer or blocking chip instead. One `FilterChips` row
    filters by state (`?show=needs|blocking|ask|review`, kept grouped;
    each counts what it lists under the category and search in force; chips
    empty in the whole bank left out), so a phone spends one scrolling line on it. The
    search reads the question, id, phrasings, stored or setup answer of every
    kind and the hosts of its site overrides. The category rail is sticky when
    wide, a chip row when narrow; its choice is `?category=`, which the state
    chips keep.
  - Live: every read follows the `answerbank` board topic (any write of the
    file, the CLI's too). An open editor without an edit of its own takes a
    change made elsewhere; one with an edit keeps it, as do unsaved employer
    rows.
  - Who edits what (user decision 2026-10-24): the user edits answers; the
    Ask Agent edits what the matcher reads. The drawer holds the answer
    fields of the entry's kind and nothing else: no question, id, kind,
    category, phrasings, pin or site-override fields, since a hand edit of
    those breaks matching. "Add with Ask Agent" (and the drawer's "tell the
    Ask Agent") open
    `/ask?draft=` with the message started; the agent's `answer_bank_save` /
    `answer_bank_forget` go through `learn.Teach` / `learn.Forget`, which
    decide the entry, wording and site and write nothing the matcher would
    not answer with ([ask-agent.md](ask-agent.md), [answer-bank.md](answer-bank.md)).
    A pinned entry's answer stays editable: a pin holds back automatic
    changes only.
  - Editor: the answer, in the control its shape gives (`shapes` of
    `GET /api/answer-bank`, [answer-bank.md](answer-bank.md) "Answer
    shapes"; user decision 2026-10-25: common questions take set answers):
    yes or no a segmented switch, a listed choice radios (a select past six;
    picking one writes its wordings), several choices toggles, a language its
    levels, a range numbers and a currency, a date, link, number or e-mail
    field; only a free-answer entry takes typed text. An answer a form taught
    earlier that is not one of the choices is shown as a note until a choice
    replaces it. A blank answer a setup step fills shows that answer as
    placeholder; "Ask me each time" becomes a note once chosen; a blank entry
    is saved as a placeholder (the server refuses it otherwise). The sites the entry answers
    differently on are listed, read-only.
  - Saving: Save (or Mod+S in the drawer) waits for a change; "Discard
    changes" restores; closing over unsaved edits asks; a revision conflict reloads the bank under the kept
    edit; success says "Saved: ..." above the list. Every change is one
    `PUT /api/answer-bank` of the whole bank at the loaded revision. Delete asks, then
    offers Undo (overrides included), which reads the bank again and puts the
    entry back into it as it is now, or says an entry took its id since. A shipped default (`defaultIds` of
    `GET /api/answer-bank`) notes in the delete dialog that it is back, as
    shipped, at the next load. A `?entry=` id the bank no longer has (deleted
    since the link was made) says so.
  - Employers (`?tab=employers`) keeps two lists apart (owner 2026-10-25):
    "Companies you worked for", the bank's employer list the employer-history
    entries answer from, and "Companies where a relative works", the
    profile's `company_relationships` of family `relative` (the same editor
    and complete mark as Profile, the other families kept on save), which the
    relationship lane answers relatives questions from. Each is a save-bar
    form (Mod+S; a tab switch over unsaved rows asks). There is no question
    tester (owner 2026-10-25: removed with `POST /api/answer-bank/test`). The learned-answer queue is "Suggestions", Home's
    word (tab id stays `candidates`); approved and rejected are a chip away
    (`&state=approved|rejected`), an approved one linking its entry. Only
    unreviewed and `reusable` suggestions are listed and counted (the tab,
    Home); the review's others sit under a collapsed "Not suggested (n)" with
    their reason and Reject. A reusable one's draft opens in the answer box,
    labelled "Drafted from your profile and CV", with its sources; Approve
    sends the box's text as the user's own answer, a bulk approval the draft.
    With no draft the row says the profile and CV don't say.
    Approve, "Edit and approve" (one request with the user's answer; the box
    takes focus, Enter approves) and Reject each leave the shared Undo note; a
    bulk approval refused part way says how many it approved and keeps the rest
    selected; Undo of an approval removes what
    it wrote, and one the user changed since is refused with the reason. Once
    the note is gone, an approved row's Take back (after a confirmation) and a
    rejected row's Restore do the same; a refusal stays on the row. Use and
    Keep under "Answers you changed" leave the Undo note too
    (`.../manual-fills/{id}/undo`, refused once the entry changed after the
    press or the server restarted).
    Usage opens on entries used in the window, most matched first, with unused
    and all a chip away and a search; an entry's name opens it in Entries.
  - Transcript (`?tab=transcript`): the one academic transcript PDF that
    application runs attach to a form's transcript upload. It shows the
    stored file name, takes a PDF by drop or "Choose PDF" (the résumé upload's
    rules: a PDF of at most 10 MiB, its name made a lowercase slug), replaces
    it by a new upload and removes it after a confirmation
    (`/api/answer-bank/transcript`, [answer-bank.md](answer-bank.md)).
- **Discovery sources** are one `.source-row` each (name and kind, last run and
  what it needs, readiness, Start and Options) in catalog order, so a row does
  not move when its scan starts. A line above names the three latest runs,
  because sources that ran sat below a screen of "Never run." rows. While none
  has run, the line says what to start with instead: the company job boards
  (`registry`, titled in plain words), with the active board count of
  `GET /api/registry` and that it needs no key or sign-in; with the registry
  scan switched off, the source needing a key or sign-in, or no board to read,
  it says only that nothing has run. With no active board and no tracked
  company the server marks the row `needs boards`, and the row itself says the
  registry is empty and offers Download the board lists (`POST
  /api/registry/seed`) in place of a Start that would read nothing. A registry
  scan that an empty registry skipped (reason `registry-empty`) is worded in
  Monitor and History (`ScanWhy`) with a link to the download on Sources and
  portals, where the command line names `applysmith registry seed`. A source
  that runs only saved searches and has none reads `needs searches`: Start is
  disabled, Options still runs a search typed there, and the row links Sources
  and portals; the tracked source with no enabled company reads `needs
  companies` with the same link, since either Start would end skipped.
- **Profile documents are edited structured, Markdown on request.** The CV is
  a form (`CvForm`) beside its résumé preview (`CvSheet`): name, labelled
  contact details, per section its entries (what, when, role, place, a bullet
  per line), each movable and removable. Both read cv.md through `parseSheet`;
  `sheetToMarkdown` writes it back in its original shape (dates stay in a
  heading that had them), so an unchanged CV is byte for byte equal. The
  preview is one column of plain text, as an applicant tracking system reads
  it. A CV with what the form cannot keep (comments, code, tables, nested
  lists) opens as Markdown, and the form says what it would drop. Narrative and
  voice are one card per level-two section (`DocSections`), rendered without
  HTML comments, edited one at a time, tables as a grid; untouched parts keep
  their bytes. Every document can switch to plain Markdown with live preview.
  "Earlier versions" lists the texts the last saves replaced
  (`GET /api/config/{file}/backups`, the workspace keeps 10); opening one puts
  it in the editor as an unsaved edit, so bringing it back is a normal save.
  A form drawn again (an opened version, an import, a discard) keeps the page
  where it was: WebKit clamps the scroll to the briefly short form otherwise.
  The Wiki editor for pages and stories does the same with the one
  text the last save replaced (`GET /api/kb/documents/{id}/previous`).
  A list a form edits is written whole, so every key of an item has a column
  (education keeps `grade_average`); the Locations tab also edits the work
  arrangement (`work_modes`, office days, travel) and `relocation.default`.
- **Tokens** (custom properties on `:root`): surfaces (`--bg`, `--bg-sidebar`,
  `--surface`, `--surface-subtle`, `--surface-hover`, `--surface-active`),
  text (`--text`, `--muted`, `--placeholder` for the hint inside an empty field, 4.5:1 and fainter than `--muted`), lines (`--border`, `--border-strong`, `--control`
  for input boundaries at 3:1), soft fills (`--fill`, `--fill-hover`,
  `--fill-active`, `--thumb` for a segmented thumb), buttons (`--button`,
  `--button-hover`; `--primary`, `--primary-hover`, `--primary-text` for the
  filled one), one accent (`--accent`,
  `--accent-hover`, `--accent-soft`, `--accent-ink`, `--brand`, `--focus`),
  the hyperlink colour (`--link`),
  semantic colours with a soft fill each (`--good`, `--warn`, `--bad`,
  `--info`, plus `--violet` and `--teal` for tracker statuses), spacing
  (`--space-1` 4 px to `--space-10` 40 px), radii (`--radius-sm`, `--radius`,
  `--radius-lg`, `--radius-full`), shadows (`--ring`, `--shadow-sm`,
  `--shadow`, `--shadow-hover`, `--shadow-lg`, `--shadow-composer`), curves and
  durations (`--ease-swift`, `--ease-snap`, `--ease-enter`, `--ease-out`,
  `--spring`, `--dur-fast` 120 ms, `--dur` 200 ms, `--dur-slow` 320 ms), type
  (`--text-xs` 12 px to `--text-3xl` 30 px). Dark mode redefines the colours
  under `@media (prefers-color-scheme: dark)` for
  `:root:not([data-theme="light"])` and under `:root[data-theme="dark"]`, so
  the system setting and the per-viewer toggle both work. Text tokens keep
  4.5:1 on their surfaces.
- **Font.** The system stack (`-apple-system`, SF Pro on macOS); titles in the
  system serif (`ui-serif`, New York on macOS). Nothing loads from another
  origin; the stylesheet has no URL and no `@import`.
- **Icons.** `lucide-react` (ISC, pinned in `web/package.json`, listed in
  `third_party/NOTICE-dependencies.txt` by `make notices`), imported by name so
  only used icons ship; they render `aria-hidden`, sized by `svg.lucide`. Each
  page's icon and purpose line live in `src/pages.ts`.
- **Shell.** A sidebar (logo mark, five groups, page icons); a sticky top bar
  ("Narrow sidebar", breadcrumb, remote-access chip, "Go to page" = the
  command palette, Cmd+K, and a theme toggle cycling auto, light, dark).
  The Notices bell carries its unread count on its shoulder ("99+" past 99;
  the button's label says the whole count), where it used to cover the bell.
  Narrowed (`.shell.slim`, kept in `localStorage` like the theme, also a
  palette action) the sidebar is 60 px of icons: names stay for screen
  readers and as each link's tooltip, a keyboard-focused link shows its name
  beside it in a fixed label (the sidebar scrolls and would clip one inside
  it), and the browser panel docks from a
  narrower window. Below 768 px the sidebar is a
  bottom tab bar (Home, Apply Next, Tracker, Runs for its badge, More).
  Under 480 px "Go to page" is its icon alone, so a long page name keeps its room.
  `PageBoundary` keeps a page that throws, or whose chunk fails to load, to the
  page, with Try again and Reload the app; a chunk that did not arrive (a
  busy server's 429, a newer build) offers Reload alone, since the browser
  keeps a failed module import for the document. A server that has not
  answered the board stream for 5 s gets one banner (reconnects by itself
  and keeps what was typed; if it does not come back, start the app or
  `applysmith serve`, or in the app the menu-bar Restart Server; Try now skips
  the backoff). The Runs badge counts needs-you plus
  blocked (`GET /api/runs/summary`, warn tone); its title and label give the
  whole state ("2 running, 1 needs you, 3 finished unseen", the last being
  unacknowledged finished runs); the count prefixes `document.title` as "(n) ".
- **Report numbers.** The Reports Company field and the palette's Reports group
  accept `12` or `#012` as a report number: the page opens that report
  (`?id=`), the palette reads `GET /api/reports/{number}` (a 404 is no row).
- **Shortcuts.** One registry, `commands()` in `src/ui/Shortcuts.tsx`, drives
  the keyboard, the palette's Actions and the shortcuts dialog (Mod+/). Tests
  reject two commands on one chord and any binding without Mod (WCAG 2.1.4; a
  bare N once opened a run's drawer during clicks). Mod+K search, Mod+1 to
  Mod+9 the first nine pages, Mod+, Settings, Mod+[ and ] back and forward,
  Mod+Shift+N next item needing you, Mod+Shift+B the browser panel (the
  original's Toggle Browser; the app menu claims no chord of these, since the
  page answers a key first: its own are Cmd+R Reload and Cmd+Shift+D Open
  Dashboard), Mod+Shift+F
  the page's own search field (the one marked `data-page-search`: Tracker,
  Reports, Answer Bank entries and usage, Discovery inbox, Wiki pages,
  Settings; Mod+F stays the browser's). A shortcut
  waits in a field (except Mod+K and Mod+Shift+F), during IME composition, after a widget
  handled the key (`defaultPrevented`, e.g. the Ask request panel's digits)
  and while a dialog is open; only Mod+Shift+N may leave a drawer the page
  opened (a run, an interview's prep, a job email; leaving still asks over
  unsaved edits), so one repeated chord walks Home's Needs you list in its
  order (`loadNeedsYou` and `needsYouItems` in `pages/Home.tsx`, the one source
  of both), each waiting run and Ask conversation by name, two items on one
  page one stop, wrapping. The palette's "Start a scan" opens Discovery and
  focuses the Start of the first source that can start; "Show notices" opens
  the notices drawer.
  No command submits or approves anything. The palette's Needs you group is
  that same list: its first five and one "N more need you" row to Home, so a
  busy day leaves the pages in reach. A page is found by its title first, then
  by its group, blurb and `terms` in `src/pages.ts`: what a person calls the
  things it holds ("add application" and "cover letter" find Tracker, "salary"
  Profile and Answer Bank); a Settings section by its label and `terms`, which
  must be words the section draws (Settings.test checks).
- **Components** (`src/ui/`):
  - `PageHeader`: the page's only `h1` with its purpose line beside it on one
    baseline (under it where the page is narrow) and the page actions; no icon
    tile (2026-11-15: the sidebar already shows the page's icon, and the
    two-line header with its tile cost every page some 40 px above its content).
  - `Panel`: a card with its own skeleton, error and retry; optional icon and
    actions. `Loading` takes a `label` (a polite status) and draws `Skeleton`
    lines; no page shows a bare "Loading". `ErrorNote` uses the Codex error
    shape: a failed load leads with "Couldn't load {what}." (a `Panel` passes
    its `what`, else "this section", since "Running now" is no object of
    "load"), then the server's reason, the request id and "Try again" where
    retrying is safe; a failed action passes no `what` and shows the reason.
    A load the server never saw (`network`) is a calm status instead ("the
    server is not answering; this loads again once it is back"), without a
    request id; while the banner shows it is a quiet line ("Waiting for the
    server to load {what}"), no live region and no Try again, so a page with
    twenty cards announces one outage, not twenty;
    the `unavailable` answer with reason `server-starting` reads the same way
    ("the server is starting"). The reason of a failed action is drawn with its
    first letter upper case (`.sentence`, CSS only), so tests and assistive
    technology still read the server's text as sent. A reason
    `no_model_route` names Settings, Models, and that name is the link there
    (the place is said once); the server's text for it always names it.
    A retry, poll or topic reload keeps the note (`useResource` does not drop
    back to loading from an error) and a repeat of the same failure keeps the
    first request id, so the alert is announced once; Try again is disabled
    while its attempt runs. The answer a failed reload leaves is greyed under
    "Last read at 14:05; it may have changed since." (`Resource.at`, the time
    it came; a day before names the date), and its `Empty` lines are left out
    there, so "Nothing moved" from before an outage is never shown as now; an
    answer that was only empty lines leaves the note alone.
  - `Button`: primary, secondary, ghost, danger; two sizes; optional icon. A
    plain `<button>` is secondary and a submit button primary, so forms need no
    class; `a.button` styles a link.
  - `EmptyState` for a page's main list (icon, sentence, help, action);
    `Empty` for a one-line empty state in a panel.
  - `Chip` (good, warn, bad, info, neutral; first letter drawn upper case,
    text kept as sent; `Chip raw` keeps an identifier such as a tool name
    exact), `StatusPill` (tracker status in its colour, word always shown),
    `ScoreBadge` (bands at 3.5 and 4.5), `ProgressBar`.
  - `Tabs`, `Modal` (dialog, drawer, palette; a drawer header has its title and
    an icon Close), `Palette`, `Pager` (a paged list a page its last rows
    left, or a stale address, goes to the last page with rows:
    `usePageInRange` on the Inbox, Leads, Reports, Tracker, Apply Next and
    scan History). `Modal` focuses `initialFocus` or the
    first control, wraps Tab and Shift+Tab, closes on Escape (innermost only; a
    dialog opened from a drawer answers Escape alone), and returns focus to its
    opener (the control pressed, though WebKit leaves focus on the page or the
    drawer around it), else the control its `returnFocus` names (a Tracker row,
    Documents or Interview prep drawer an address opened goes back to its row's
    Open button), else `#main`.
    The Tracker row drawer is drawn again when the Documents or Interview drawer
    closes over it, and puts focus back on that drawer's button once the row has
    loaded. `ShowMore` hands focus to the first control the rows it reveals
    bring. `Palette` is a combobox: focus stays in the field, Up
    and Down move `aria-activedescendant` over a `listbox` (wrapping across
    groups), Enter opens the active row (accent fill and bar). Groups (each
    `role="group"` with a heading): Needs you, Pages (by name first, then by
    group or purpose line), Settings (sections by name or the settings they
    hold, `SECTIONS`), Recent runs (last 100 of
    `GET /api/runs`, matched locally, leaving out every run a Needs you row
    stands for, each repeat of a grouped one included), Applications (`GET /api/candidacies?query=`,
    company or role, opens the row), Reports (`GET /api/reports?company=`, opens
    the report), Conversations (`GET /api/chat/sessions?q=`), the last three
    matched by the server after a 150 ms pause, five each; Actions (commands
    with keys; a pasted posting address adds "Evaluate this posting", which
    opens `/?post=` to fill Home's box); every group but Needs you, Pages and
    Actions needs typed text. Local groups match accents loosely and forgive one
    slipped letter in a word of four or more ("trakcer"). The
    active row is kept by key so late results do not move it; an unreadable
    list is named in the status line.
  - `SelectionBar` holds the actions on ticked rows (Apply Next, the Discovery
    inbox): sticky after the list, above the phone tab bar like `SaveBar`, its
    count a polite live region that is on the page before the first tick (the
    bar shows only while a row is ticked), so the first count is announced. `html`
    carries `scroll-padding` for the top bar, the tab bar and a visible save or
    selection bar, whose height, one line or wrapped onto two, `barRoom` keeps in
    `--bar-h` (WCAG 2.4.11).
  - Lists: `FilterChips` (counted filter links, `aria-current` on the one in
    force, optional tracker status dot); `SortHeader` (`aria-sort` and an
    arrow); `Truncate` (one line, whole value in `title`); `ActionMenu` (a
    row's secondary actions behind one "More": a `role="menu"` popup fixed at
    the button, arrow keys, Home, End, Escape back to the button; a disabled
    item keeps its reason in `title`).
  - `SaveBar` closes a long form, sticks to the view bottom, says "Unsaved
    changes." or "Saved at …" in a polite live region and holds the save
    button. While unsaved it holds the page (`useUnsaved` in `SaveBar.tsx`,
    `holdPage` and `leave` in `router.tsx`): a link, the palette or a
    shortcut that changes the path or the query, and a tab switch or drawer
    close made through `leave`, ask "Leave without saving?"; Back and Forward
    to another path or query put the address back and ask the same way (the
    history entry Back came from is replaced by the restored one); a change of
    the fragment alone never asks (Settings sections stay mounted). A page's
    own `useSearchParams` writes never ask. Closing or reloading the window
    raises the browser's prompt; the desktop app's WebView shows none, so the
    app's Reload and its own page loads ask the page first (`applysmithHeld`,
    `applysmithLeave`, [app.md](app.md)), and a workspace switch button asks through `leave`.
    Forms without a bar hold the page the same way: the setup steps, the
    Tracker row drawer's notes and Add application, the schedule editor, the
    answer bank entry editor, the interview debrief and practice forms, the
    Ask composer's unsent message, Home's posting box and a document's
    revision instruction. Mod+S
    presses the bar's last button (its save) when enabled: the bar whose form
    has the keyboard, else the only unsaved one. An editor in a dialog or
    drawer gets the same through `useSaveKey` on its save button (the answer
    bank entry, the schedule editor, the Tracker row's status change and note
    and Add application, the interview stage and debrief forms): it presses
    it once something changed, only in the topmost dialog, and in a dialog
    with several only for the form that has the keyboard.
  - Words: `humanize` turns a token into words (`never-run` to "Never run",
    `no-ats` to "No ATS"); `formatDateTime` writes `YYYY-MM-DD HH:MM` and
    `formatDate` the day, on this computer's clock (a plain date stays as
    written); `when` in `pages/action.ts` is the same form, so "today" is the
    user's. Tests run on Los Angeles time (`vitest.config.ts`) so the shift is
    visible and results match on every machine. Every state shown passes
    through one of these or a fixed label, so it reads the same everywhere.
    A date inside wrapping text is a `<time>` element, which never breaks
    (the Reports catalog wrapped "2026-10-" from "29").
- **Layout classes.** Widths are the page's own (`main` is the `page` size
  container), so a page beside the browser panel lays out for its room; only
  the phone shell (tab bar, touch sizes, full-screen panel) follows the window.
  `main` is at most 1280 px wide and centred in the room a wider window
  leaves.
  On a phone the Tracker and Reports filters show only their search, with a
  More filters button (`useMoreFilters` in `ui/parts.tsx`), so the list starts
  on the first screen; the rest shows once pressed or while any is set.
  `.stats` (stat tiles), `.grid` (`.grid.board` for run cards), `.home-grid`
  with two `.home-col` columns and the server strip under them (one column
  below 700 px, ordered by `:has()`),
  `.row` (`.row.hold` keeps its button beside a long first part, which wraps),
  `.split`, `.with-rail` and `.subnav` (a rail that becomes a scrolling row
  below 660 px), `.item-list` with `.status-icon`, `.filters`
  (`.filters.sized` for selects holding words: as wide as the longest value;
  from 960 px the Tracker's seven fields are one row, with `.filters-end` under them: the checkbox at the start, Apply at the end; `.filters.compact` for two or three choices: each a choice's width, on the page with no card), `.form-grid` and `.form-cols` (two columns where they
  fit; a field keeps its control's width, a number, time or date input at most
  14 rem), `div.stack` (settings rows), `table.cards` (rows become cards below
  800 px; headers stick under the top bar).
- **Tables.** `th.num`/`td.num` right-align (all cells use tabular figures);
  `.nowrap` keeps dates and short tokens on one line; `td.fill` (30 %) takes
  the leftover width and cuts its `Truncate` text to it, so a table never
  outgrows its card; `td.key` names the row (a company)
  and keeps up to 11 rem while long-text columns give way; `table.dense`
  trades padding for room; `td.row-actions` holds the row's buttons at the end.
  A smaller second line is `.sub` (a tracker row's newest note, the applied
  date, an Apply Next row's links in `.row-links`); `td.state-col` clamps a
  sentence to three lines (`.clamp`), the whole in its title. Below 1100 px a
  card drops empty cells and is named by its `td.key` cell without a label (the
  header row is hidden so no invisible sort button takes the keyboard; the
  Tracker's Sort select does that job), and `td.num` and `td.nowrap` values
  share a line. Below 640 px `FilterChips` is one scrolling row and a
  `.filters` bar puts two controls to a line. `.count-line` gives the row
  count, `.toolbar` sits above a list (search, export), `.panel-title` and
  `.panel-actions` put a heading and its batch buttons on one line.
- **Keyboard.** `:focus-visible` everywhere (a 2 px `--focus` outline; a field
  gets a 1 px `--focus` ring and a soft halo). "Skip to content" focuses
  `#main` without changing the address, because Settings reads its fragment.
  `ui/focus.ts` `keepFocus` (on `main`) restores focus the browser drops to the
  body: a button disabled for its write regains it once enabled; a control
  removed by a reload sends it to the next row, else the previous, else the
  list, else `#main`. Focus the user moved is left alone. `main` is named for
  the page, so the focus a new page moves there says which page it is; Escape
  closes the tab bar's More sheet and returns focus to More, and a press outside the sheet closes it too. A confirmation
  after a press ("Saved", "Stopped") renders inside a `role="status"` region
  already on the page, so it is announced; the run drawer keeps an unseen
  status with the run's state and stage, said once per change (its summary
  ticks with the clock).
- **Links in text** (`p`, `dd`, a non-plain list item, `.stat-sub`, a `.row`
  line, `.muted` text) are underlined, because the link colour cannot reach
  3:1 against body and muted text while keeping 4.5:1 on the surface. On a
  phone a page's link in `p`, `dd`, such a list item, `.muted` text or a note (in a
  dialog too) keeps the line's height instead of the 44 px target of a link
  on its own.
- **Feedback.** `button.danger-outline` marks a destructive action that still
  confirms (Delete, Remove, Revoke, Cancel run); `button.danger` is the
  confirming press. `.note.info.with-icon` is an informational note; `.check`
  makes a whole checkbox label the target (28 px tall, 44 px on a phone).
  A card row's actions (`td.row-actions`) reach 44 px on a phone only.
- **Settings** shows one section at a time, picked by the fragment
  (`/settings#settings-gmail`, as the setup catalog links); a fragment naming
  something inside a section opens that section. All sections stay mounted
  (others `hidden`), so their data loads at once and switching never waits.
  The rail groups sections under Applying (Documents holds the paper size, résumé length and language of new documents, and whether a cover letter a form asks for is always written or only at 4.5 or higher), Finding jobs (Scan defaults, Eligibility gate), Assistant, Alerts and System; a
  search above it filters over an index (`terms` on each `SECTIONS`
  entry in `pages/Settings.tsx`: the section's headings, labels and
  descriptions, plus the preset and provider names the Models section has
  loaded) and names the matching setting under a link. A word matches as
  written, run together ("timezone") or with another last letter ("spend"
  finds "Spent today"). An entry with `to` is a setting on another page
  (Schedules, under Discovery), listed only as a search result. Enter opens the
  first match, Escape clears, no match says "No setting matches ..." with Clear
  search. A test holds every term to text the page draws; text drawn only in
  another state is not indexed. The Models route, budget and daily cap editors
  hold the page while edited (a save bar; Cancel, another row's Edit or Budget
  and leaving ask; Mod+S saves); quiet hours offer the schedule editor's
  time-zone list.

## Decisions

### Cross-cutting

- Accessibility is checked three ways. `src/styles.test.ts` computes token
  contrast in light and dark (text and semantic colours 4.5:1 on every surface
  and their soft fills; `--focus` and `--control` 3:1). Component tests call
  `testing/axe.ts` `expectAccessible()` (axe-core; serious and critical fail),
  and `bun run test:a11y` runs it after every component test so every reached
  state is audited (about a minute, hence a separate script). Rules needing
  layout or computed colour (contrast, link in text block, target size) are off
  in happy-dom and covered by a headless-browser pass over every page in both
  themes. A frame (the PDF preview) is checked as an element, title included,
  not inside: happy-dom cannot carry axe into it, which failed two tests of
  the script before. `axe-core` is a `web/` devDependency imported only under
  `testing/`,
  so it never reaches the bundle (held by the first-load budget test in
  `internal/cli`). The 2026-10-20 re-audit ran axe (WCAG 2.2 AA and best
  practice, contrast and target size on) over every route, tab and the
  drawers and dialogs, at 1280 and 390 px in both themes. Three habits came
  from it: an effect callback never returns a call whose value may be a
  promise (`window.scrollTo` is one in Chrome, and its cleanup then threw
  and blanked the page on a guide tab switch); a live region is in the page
  before it has anything to say; and a text colour is never dimmed with
  `opacity` (use `--muted`). A dialog keeps focus when a control inside it
  goes (`keepFocus` in `Modal`).
- Every write button is disabled while its request is in flight, and
  `useAction` (`pages/action.ts`) refuses a second run meanwhile, so a key
  firing twice before the redraw (Enter in the chat composer) cannot send
  twice. A list waits for every request that decides its layout (the Runs board
  waits for the needs-you inbox); a self-loading section draws a `Loading`
  skeleton rather than push the page down later.
- A page does not move once visible. `ui/Settle.tsx` keeps a page at opacity 0
  (so an address-opened drawer can still take focus) until the GETs it started
  on opening have arrived and none followed for 60 ms, at most 1.5 s. The count
  is `readsInFlight()` in `api/client.ts`; writes and quiet reads are not counted (Home's
  setup check takes about a second). What the page does not draw from waits
  for the reveal (`Settled`): the Home setup check, the top bar's Notices
  list and the other pages' chunks. Over loopback HTTP/1.1 the browser keeps six connections to the
  server and the board stream holds one; the check held another for most of
  a second and the chunks queued ahead of Home's reads. The setup state, the
  read that set Home's reveal, ran `sw_vers` per request (60 to 130 ms) and
  validated the answer bank each time; both now run once (per process, per
  revision). Measured over 20 interleaved cold loads of the demo Home in
  headless Chrome: the reads took 122 ms to 40 ms (median), the reveal 282 ms
  to 200 ms; 1,000 tracker rows changed no read's cost. Skeletons draw out
  of sight, so the demo banner, the Home setup card and Apply Next's runs
  panel appear with the page; the Home setup card keeps a line for the late
  check ("Checking the setup.", then the result). Accepted: a server slower than 1.5 s, and the Home card for
  a workspace whose steps are done but whose check fails.
- Layout shift is measured per page in headless Chrome (`PerformanceObserver`
  on `layout-shift`, a fresh load with back/forward cache off, 1264 px, the
  seeded demo and non-demo workspaces). Chrome stops scoring an element that
  began at opacity 0, so the measurement holds the gate at 1 % opacity and sums
  only shifts after the reveal. Before the gate 16 of 42 demo pages and 13 of
  42 others scored 0.02 or more (worst: Reports 0.38, Tracker 0.33, Home 0.23,
  Usage 0.17 to 0.85 by run, Knowledge stories 0.41); after it all score
  0.0000.
- A wheel over a dialog or drawer is cancelled and scrolls the innermost
  movable scroller under it (`containWheel` in `ui/Modal.tsx`); WebKit
  scrolled the page behind a drawer (178 and 394 px) and, in the app, not its
  body.
- The board stream closes on `pagehide` and reopens on a back/forward-cache
  `pageshow` (`api/board.ts`): cached documents holding streams used up the
  origin's connections, and after six full loads the next page waited about
  47 s for the stream's 45 s silence rule.
- No toast system: every write confirms beside its control in a polite live
  region (`.note.ok`); a failure stays as an error note with its request id
  until the next attempt. No message needs to outlive its section, and a toast
  would move the confirmation away from the action. A message from the server
  that belongs to no section (an update quiet hours hold, an announcement made
  while the desktop app was closed, a pairing) goes to the top bar's Notices
  list instead (owner decision, 2026-10-20): a bell with the unread count
  (read out by a polite status), opening a drawer with Mark read, Mark all as
  read and Open (which marks it read). It is stored server-side, so it
  survives reloads and windows, and it never interrupts, so quiet hours still
  hold ([notify.md](notify.md#desktop-sink)).
- Every count opens exactly what it counts. A filter only a link sets (a day,
  a lane) shows above the list as `ClearChip` (`ui/FilterChips.tsx`), which
  names it and clears it: Tracker `reached`/`movedBy` + `since`, Inbox
  `since`, Reports `since`, Runs `since`, Apply Next `lane`. Every day a page
  shows or a filter takes is a local day (this computer's clock): Home, the
  filters' `since`, `from` and `to`, Usage's cost by day and call dates, and
  days ago (`ageLabel`). A sidebar badge counting part
  of a page is shown on that page as a link to that part (Apply Next's Apply
  now lane, Gmail's "N need you now").
- Lists hide what is empty rather than count it (the Runs "Needs you" inbox
  shows only while a run waits; Leads category filters list only categories
  holding a lead, plus the one in force). Exception: Apply Next lanes.
- "Nothing yet" and "nothing matches" differ: a filter hiding every row says
  "No runs match these filters." with Clear filters, not "Nothing is
  running." (likewise the Tracker, Leads and Discovery inbox status views; the
  unfiltered empty Tracker says "No applications are tracked yet.").
- `client.ts` holds the GET retry, the 30 second deadline (covering the body
  read) and the never-retry rule for writes. A call that waits on a model
  (drafts, a composed answer, a thread summary) uses `postModel`
  or `getModel`, which wait `MODEL_TIMEOUT_MS` (130 seconds, above the server's
  2 minute `modelDeadline`).
- Story proposals, company briefs and "Make a brief now" are background runs
  like "Ingest now": the button reads as busy with a link to the run, the list
  reloads when the run ends, and a failure shows the run's reason. A proposal
  request with no model routed shows the error, its Settings, Models a link;
  the company brief form links Settings, Models in its hint. The proposals page
  loads what the last run found when it opens, without what was dismissed
  (kept by the server, with Undo) or saved.
- A secret field is uncontrolled: React never mirrors the typed key into an
  attribute, and the field is emptied after every attempt.
- The index document has `Cache-Control: no-cache` (dashboard-ui spec), so a
  new build shows at once; hashed assets are immutable. A bundle text file
  (JavaScript, CSS, HTML, SVG, JSON) of 1 KiB or more goes out gzipped when
  `Accept-Encoding` allows, with `Vary: Accept-Encoding` and the same caching
  (`internal/api/static.go`), compressed once at the best level on first
  request and kept in memory (the embedded files cannot change). Serve-time
  gzip rather than precompressed files: Vite, not the esbuild pipeline, builds
  the bundle, and the standard library needs no new build step, dependency or
  tracked file. Measured in [performance.md](performance.md).
- **Layout check.** Part of `make test-browser` (`internal/cli/dashboard_layout_test.go`,
  build tags `dashboard browser`) serves a demo workspace with long tracker
  rows and opens every page route, each tab or section the address can hold,
  the Tracker drawer, the command palette and, on a phone, the tab bar's More
  sheet, at 360, 768 and 1280 px in headless Chrome with a throwaway profile.
  That Chrome is kept off the macOS Keychain (`--password-store=basic
  --use-mock-keychain`, as the product's own launch is): without the two
  flags, storing the session cookie waited on a Keychain prompt and the check
  hung at its first page whenever the screen was locked (2026-11-11).
  It fails on a document wider than its window, a control beyond the window
  with nothing to scroll it into reach (a scrolling ancestor makes it
  reachable), a control cut off by an ancestor hiding overflow, and a button or
  link showing less than its label without an ellipsis.
  `TestDashboardLayoutFitsBesideTheBrowserPanel` repeats every page at 1280
  and 1440 px with the panel docked, where a control under the panel fails too
  (the CSS before container queries had 50).
  `TestLayoutCheckReportsOverflowAndClippedControls` proves it fails on pages
  built to break it. `APPLYSMITH_LAYOUT_WORKSPACE=<throwaway copy of a richer
  workspace>` checks that workspace instead. About a minute. Its first run
  moved the card layout from 640 to 1100 px (seven- and eight-column tables
  spilled at 768 px), hid card-layout sort buttons from the keyboard, made
  `code` wrap anywhere (a workspace path made Settings, Files 824 px wide on a
  phone) and fixed the Setup step list's visually hidden "complete" chip at
  320 px.

### Home and Ask Agent

- Home is the workspace overview, built from existing routes: three
  tiles (ready to apply: the Apply now and Selective lanes, where Apply Next's
  Next up comes from; tracker rows by every status holding one, so the
  parts sum to the total; model spend today), then two columns. "Needs you"
  leads with interview stages today or tomorrow (`GET /api/interviews/soon`,
  each opening its Interview prep drawer and saying whether a pack exists; one
  over today asks for its debrief until feedback is recorded), then lists runs waiting for the user (`GET /api/needs-you`, first three by name,
  each one's Show it in the browser panel and Let the agent finish under its
  text as one group indented to where the text starts, as every item's
  actions are, so a half-width card keeps the text readable and an action
  that wraps keeps its place),
  Ask conversations holding a call (`GET /api/agent/confirmations`; one by
  name, several as one line to Ask), Gmail `do_now` threads, model tasks without a route, email status changes to
  confirm, suggested answers waiting for review, the hand edits offered to keep,
  wiki drafts and held hiring signals (these five are only counted, in one read
  of `GET /api/needs-you/counts`), postings ready to apply, inbox rows (`status=pending`) and due
  follow-ups (`GET /api/tracker/followups`, kinds `follow_up` and
  `reply_check`); a failing source is named under it with Try again. Below 700 px it comes above the
  tiles; the DOM runs in that order and a wide page lifts the tiles with CSS
  `order`, so Tab follows what a phone shows; "Last 7 days" stays under the
  columns there, so Needs you is in the first screen. The cover leads only
  while the tracker holds no row (the Tracker tile's summary read, lifted to
  the page so it is asked once); a workspace in use opens on Needs you, which
  the cover pushed below the fold at 1440x900. From 800 px the setup
  card above them is a strip (its button beside the title, the steps left to
  do on their label's line) for the same reason. The palette's "Evaluate a job post"
  opens Home and focuses its paste box. A broken Server component links to its setup
  (Computer use to Agent control); while the shell's banner says the server is
  not answering, the card says "Not answering" and greys its last known parts.
  Ready to apply at 0 links Discovery even when other lanes hold rows. "Running now" is
  `GET /api/runs?state=working&limit=5`, asked again every 5 s while a run works
  (progress inside a state is silent on the board stream), its count and "All
  N running" from `GET /api/runs/summary`; Needs you reads its ten
  sources once and, on a notice, again only those whose topic moved (`SOURCES`
  in `Home.tsx` names each source's topics; a failed source, a write, another
  server run or the day turning reads them all); Needs you and Today are read again
  when the local day turns (`useLocalDay`), so tomorrow's interview becomes
  today's on a page left open, and Needs you draws again when an interview's
  time comes (one timer to the next), so it asks for that debrief; "Today" is one read of `GET /api/day`
  (`dayView` in `internal/api/narrator.go`): "Done for you" and "Waiting on
  employers" lines, each a link (the first six of a part, the rest behind Show
  more), and today's counts, each opening what it
  counts: new `/discovery?tab=inbox&since={date}`, evaluated
  `/reports?since={date}` (postings, each once: the newest report of each),
  ready the two lanes, applied `/tracker?reached=applied&since={date}`; the
  "Ran N evaluations" line, which counts every evaluation that ended (a failure
  and a repeat too), opens `/runs?kind=evaluate&since={date}`, the board's
  runs that ended since, acknowledged ones included. The view's `left` lines
  are what Needs you lists above, so Home does not tell them twice; the day
  summary, Telegram's `/today` and the Ask Agent keep them; "Server"
  names each health state in words, a part's detail under its name. "Start from a job post" heads the second
  column: one web address goes as `{url}`, anything else as `{text}`, to
  `POST /api/evaluations` (the route of `applysmith evaluate`; never the Ask
  Agent), and its note follows the run to its report.
- The Ask Agent keeps one `Conversation` across the id change of creating a
  conversation: the first message shows at once as "You", the live turn is
  followed once the server accepts it, and the composer stays the same element.
  Opening another conversation, Back or a fork still remounts it.
  Its list draws the 20 most recent conversations and folds the rest behind
  "Show more", but always draws the open one; search asks the server, so it
  still reaches every conversation. With no route for `agent-kernel`, or under
  the offline simulation, a note above the conversations says so before a
  message is typed (`noAgentModel`, the server's words) and links Settings,
  Models; every turn would end at `no_model_route`.

### Runs

- A waiting run is listed once: the inbox (waiting, unacknowledged) is its
  home; the board's Still waiting column holds only acknowledged runs that
  still wait (left out when empty). Repeats of one posting stopped for one
  reason are one row (`groupRepeats` in `pages/runs/format.ts`: a "6 runs"
  chip, "Show 5 earlier runs", "Acknowledge 6"); Home, Apply Next and the
  Still waiting column fold them alike. An inbox row's step (Answer,
  Review, Resolve, Show tab or the page holding the fix) is its one filled
  button; Done and Acknowledge are quiet (ghost) beside it, so the eye lands
  on the next step down a long list. A waiting card reads "Waiting 4 h, worked 41s"; a finished card in a
  day group names only the time.
- A quick screen's review (an evaluate run waiting with reason `review`) is
  decided in its Runs inbox row, with no drawer: the row reads the run's
  evaluation (`GET /api/evaluations/{id}`) for the screen's score, the reason in
  words and the report, and offers "Evaluate in full" (the deep run of the
  drawer's route, which leaves the row listed as "Evaluating in full" until that
  evaluation ends) and "Skip" (dismisses the inbox row, acknowledges every run
  of the row). A "Report N" button reads the report in the Apply Next drawer
  with the same two decisions at its foot. A count line says how many reviews
  are left to decide, and focus moves to the next undecided row's primary
  button. Home's overflow line names reviews as postings waiting for a decision.
  The drawer in `runs/Detail.tsx` keeps its own copy of the same two actions.
- A run's detail is said in words, never raw (`plainDetail` in
  `pages/runs/format.ts`): readiness failures (`page N "label": words`, from
  `internal/gates/review.go`, even when cut at 300 characters), a disabled
  Submit's blank rows, and error chains as one deduplicated sentence. Lists
  show one line ("Answer 3 questions: ..."); the drawer a heading, the
  questions as a list, the next step, and the machine text under "Technical
  detail". Parsed on the client because the structured failures live in the
  gate verdict and carrying them would change the board schema. The next step
  links where a stop is fixed (Answer Bank, sign-in credentials, Browser page);
  an application's Show tab and Retry precede its stage timeline. A demo
  workspace offers no Retry and submit, and says why.
- A waiting entry raised for another page (a Gmail app password, a schedule's
  need, a hiring signal) is resolved where its `link` points: with no server
  `fix`, `fixOf` in `pages/runs/format.ts` makes that link the row's, the
  drawer's and Home's action ("Open Gmail"); the kept-tab next steps are said
  to application runs only. Every `Link` and schedule `Artifact` the server
  writes names a page of `pages.ts` (`TestNotify_LinksNameDashboardPages`).
- An evaluation's drawer links its report and posting; a failed or cancelled
  one offers "Evaluate again" (`POST /api/evaluations/{id}/retry`, the stored
  request with its mode and pasted text) and follows the new run. An
  evaluation a scan or inbox pass queued (batch `scan:<run>`) links that run
  as "Queued by". A run record the board no longer lists says its status in
  words.
- Stage names are user words (`stageName`, `stageDoing`), an evaluation's
  phases and a document run's stages included; record and learn run
  after every outcome, so a run never names them (the server sets a finished
  entry's stage to its outcome's, `applyStack.finish`). The drawer opens on one
  sentence of how long the run worked and where it stands, and says once each
  how long a waiting run worked and has waited, who started it and from where.
- From the Codex task list: a running card breathes a dot and its duration
  ticks each second from the client clock; Finished groups cards under Today,
  Yesterday and Earlier this week (local calendar) with "Acknowledge all" (one
  `POST /api/runs/{id}/ack` each, reported "Acknowledged N; M failed."); an
  unseen inbox item has a dot labelled "Unseen", not a word chip. A card says
  its run's detail in plain words (`plainDetail`): why for a waiting or failed
  run, what it came to for a finished one (the narrator's "7 found, 3 new, 3
  queued for evaluation", a report's score).
- A stopped run's drawer lists the questions in its record (`questions` of
  `GET /api/apply/runs/{id}`; an application board entry's id is its run id):
  a choice shows the form's options (radios up to six, a dropdown beyond),
  anything else a text box. "Save to Answer Bank" posts one answer to
  `POST /api/apply/runs/{id}/answers` (scoped to this question on this site
  unless "Use this answer on every site" is ticked, which it starts as the
  server's `everywhere` says: unless the question is about the employer or the
  posting, its name, "this company", "this role", "here", "join us", an
  employee or a referrer, `learn.NamesCompany`, F-4); a blank entry the
  question reached is filled instead of a new one being added; the row then
  reads "Your Answer Bank answers this now". While the run keeps its tab, the
  last saved answer continues it there (`continued`, F-3); without a tab
  "Retry with these answers" opens the retry dialog of the run's own mode. The
  same save moves every other waiting run it completes (`MoveAnswered`): one
  waiting on answers alone with its tab kept goes on, one whose tab is gone
  says "Every question has an answer now" and keeps its retry. A changed
  profile save does the same (`openBefore`/`moveAfter` in `api/config.go`):
  ticking "This list is complete" on Profile answers what runs left about
  their own company, since a run's questions are routed with its company and
  role. The Runs inbox calls that action "Answer".
- A free-text question the composer would draft (`draftable` on the run's
  question, from `assist.Draftable`, the same refusal rules `POST
  /api/answers/compose` applies) offers "Draft with evidence" in the drawer: it
  posts the question, the run's host, company and role, puts the draft in the
  answer box, names the facts it cites in the user's words ("Written from: your CV (Experience), ..."), and then offers a
  "How to change it" line with Rewrite (the text in the box is sent as
  `current`). A trap, a "write it yourself" or no-AI question, a statement about
  the application, a structured fact and every choice are not draftable and show
  no button. A draft is never saved or sent: Save to Answer Bank stays the
  user's press. With no model on the narrative task the row says so and links
  Settings, Models. The posting's text and address are not on the run record, so
  the draft rests on the user's facts, not on the job description. The Answer
  Bank's "Questions your runs wait on" form offers no draft.
- The Answer Bank's "To answer" view starts with "Questions your runs wait
  on" (`asked` of `GET /api/answer-bank/unanswered`): each question once, the
  number of waiting runs, and the same form, posted to the newest waiting
  run's answers route; a saved answer that did not continue a kept tab links
  that run, where it is retried. A blank entry such a question's answer is
  written into is not listed again below it. A stopped run's "answer" step links there.
  "Files the form asks for" lists `uploads`, the required upload slots left
  empty (a file is no Answer Bank entry); with `letterOverride` (a letter slot
  or box left while the row's final report is below the 4.5 floor and the
  Documents settings write such a letter only above it) "Allow a tailored
  cover letter and retry" posts the override, then the retry.
  A filled form waiting in its kept tab (`canSubmit`) offers "Submit
  this form", whose dialog names company, role and host and posts
  `POST /api/apply/runs/{id}/submit`; Apply Next shows that row as filled and
  waiting with Submit this form and Jump to tab (`formWaiting`).

### Apply Next

- Under the offline simulation, Apply Next, Reports and the Tracker carry a
  note that the scores there are simulated (no model read the postings) and
  link Settings, Models (`OfflineScoresNote`); a score badge does not say it by
  itself. A simulated evaluation gives a row its score but never its status
  (`tracker.Report.Simulated`): a new row is Evaluated whatever the
  recommendation, and a skipped row stays skipped.
- A lane's table names a row in one Posting column: the company, its role
  under it and the posting's links under that (host, report, source check,
  skip list), so a window beside the browser panel still shows the role in
  words; a long host is cut with an ellipsis rather than running into the
  score.
- The first page leads with "Next up", then the application runs running or
  waiting unacknowledged (`applynext/Progress.tsx`, titled "Running now",
  "Waiting runs" or both): at most three rows, running first, then "N more runs
  are on the Runs page" and a link to all application runs.
- "Next up" is the top Apply now or Selective row, in the page's order, a run
  can start on (a posting address, not on the skip list, state `idle`), shown
  while both lanes are on their first page; it says why it leads ("Best
  score in" or "Newest in" the lane), and has two
  actions: "Autofill only" (primary; stops before Submit) and "Autofill and
  submit..." (the row's `RowSubmitDialog`). A label ending in an ellipsis
  always opens a confirmation; a lane's batch pair is "Autofill all" and
  "Submit all...". A start's note (its run link, Cancel while queued) takes
  the keyboard with it, since it is scrolled into view and a start of ticked
  rows clears the bar its press came from. A posting under the reserved `.example` domain (the demo's
  samples) never loads, so it leads only when no other row can, its starts are
  disabled with the reason, a lane of them has no batch pair, and its report
  offers no autofill.
- A row whose final report is below the 4.5 document floor while the
  Documents settings write an asked-for cover letter only above it
  (`letterOffer`; never a screening report, since no override covers it) gets
  an unticked "Allow a tailored cover letter if a form asks for
  one" in `RowSubmitDialog`, one per row the run can take in
  `LaneSubmitDialog` and `ChosenSubmitDialog`. A tick posts `POST /api/candidacies/{id}/documents-override`
  before the run starts; a refused override starts nothing.
- `lane` (one or a comma-separated list) keeps those lanes, with a chip that
  clears it: Home's ready count opens Apply now and Selective, each lane count
  its lane.
- "Order" in the header is Best score (the default) or Newest (`sort=newest`:
  report date first, since postings close quickly and a fresh one should not
  wait pages behind older ones); a change starts every lane on page 1. A
  lane's batch still takes its rows best score first, and its dialog says so
  when the lane holds rows beyond the page. Each
  lane has its own pager and `page.<lane>` in the address (as on the
  server), so one table's page leaves the others where they are; a lane left
  empty on a later page moves to its last page with rows. There is no text
  search: the server pages the list, so a filter in the page would see 10
  rows.
- Each lane says why its rows are there in one `.lane-why` line; an empty lane
  keeps its heading and that line (`.lane-empty`) on purpose; with every lane
  empty the page shows one `EmptyState` pointing to Discovery, and with only
  Apply now and Selective empty one "Find postings in Discovery" line above
  the lanes. The list reads again on the `tracker` topic and once the `runs`
  topic has been still for 1 s (`useTopicsSettled`): a run starting, stopping
  or ending moves a row's state and is announced only on `runs`, which a scan
  also moves on each evaluation phase. A row whose `sourceCheck` is set shows
  a "Source unverified" chip (`bad` when the check failed, `warn` when none
  saw the posting live) whose title says why. No state strip
  that would only count unstarted rows; a Marked applied row reads "Applied by
  you", and a run that stopped before is "Earlier run" there, never "failed";
  an idle row whose last run filled the form reads "Filled earlier"
  (neutral), so "Not started" is only a row never run.
  A start or refusal shows above the lanes and is scrolled into view, since the
  press may be far down; the start note follows its run (`GET /api/runs/{id}`)
  and says how it ended or stopped for you. A start that waits for the apply
  window (`ahead`, entry stage `queued`) shows the entry's place line and a
  Cancel (`POST /api/runs/{id}/cancel`); the rows a batch left out are listed
  as "Left out (n)" with the server's reason for each. A batch's note says how
  far it has got ("2 of 5 done (1 submitted, 1 stopped). Now: X.", from
  `GET /api/apply/batches/{id}`, asked again every 3 s while the list is
  open), and a run that stopped with a fix (`entry.fix`) links it.
- "Skip this posting" (the row's More menu and Next up; disabled while a run is
  queued or running on the row) sets the row skipped through
  `PATCH /api/candidacies/{id}` quoting the item's `version`, so it leaves the
  lanes and Next up moves on; Undo makes it evaluated again, quoting the
  version the skip returned. A row that moved since the list was read (applied
  by a run, another tab or the extension) is refused with the server's
  `revision_conflict` message and the list reads again; the bulk route would
  have moved an applied row to skipped as a user correction. The Reports viewer
  offers the same for an evaluated row, quoting the tracker lookup's version.
- A row's `leftOut` reads "Left out: <reason>" under its state. Apply now and
  Selective rows carry a box; the ticked rows (kept in tick order, at most 100,
  across pages) get "Autofill selected" and "Submit selected..."
  (`ChosenSubmitDialog`, naming role, company and host of each), sent as
  `candidacies`; a ticked row skipped or marked applied here, or read back
  outside the runnable lanes, leaves the selection. The lane and chosen-rows dialogs carry "Retry today's stops"
  (`retryToday`, unticked each time a dialog opens). An Awaiting receipt row
  offers "Confirm submitted..." (`POST /api/candidacies/{id}/confirm-submitted`).
- The report drawer steps through the page's reports (Previous / Next, "2 of
  8", in lane order) without closing, replacing the address's `report`; the
  Reports page offers the same over the page's catalog rows at every width
  ("Back to the catalog" only on a narrow screen, where the catalog is
  hidden); the rows name an unclear or doubtful legitimacy.
- Report and document drawers open through `report` and `documents` in the
  query string. The report drawer reads the body without header lines
  (`reports/body.ts`) and, for a runnable row, ends on its two starts in a
  sticky footer; a row whose run kept a tab leads with "Jump to tab" (Autofill
  only moves into More); a tried row links "Open the run" ("Answer the
  questions" after an answer-gap stop). "Jump to tab" and Runs "Show tab" are
  enabled from `keptTab` or `canFocusTab` and call
  `POST /api/apply/runs/{id}/show-tab`; "Show it in the browser panel" shows
  the board entry's `focusTab` in the panel instead.
- The skip-list drawer puts the add field above the list and counts the
  employers; it writes the whole list with the revision it read, and a
  `revision_conflict` offers a reload that starts a new draft.

### Reports and documents

- `since` (a local day, the link of Home's and the funnel's evaluated counts)
  lists the newest report of each posting evaluated since, so the list holds
  the postings counted, with a chip that clears it.
- Reports opens with four quick-view chips setting the address filters:
  To decide (`undecided`: each posting's current report whose tracker row is
  still evaluated, highest first), Score 4+ (minimum 4, highest first),
  Manual review (legitimacy unclear), Newest (no filter); a view is marked
  while the address holds exactly its filters. Each catalog row shows its
  posting's tracker status. Previous and Next step across pages (the first or
  last report of the next or previous page) and count the place in the whole
  view. In To decide, a decision (Skip, an Autofill start, Evaluate again)
  reads the page again and opens the report after it (a skipped report has
  left, so the next holds its place), its note carried above and the keyboard
  on the report's heading. A report's chips (here and in
  the drawer) add the pay its Snapshot states and its age ("Evaluated 3 days
  ago"). "Evaluate again" ("Evaluate in full" for a screening or gated
  report) posts `POST /api/evaluations` with `force` and `mode: deep`, so a
  new report is written beside this one, and links the run. Above a report sit Apply Next's two starts (the submit dialog reads
  the skip list from the server), none once the tracker row was sent, and how
  the posting was read ("Posting live on ..." only for a browser read that saw
  it open), and "Résumé and cover letter", the row's document drawer. On a
  skipped posting (row skipped or discarded, or no row and a skip
  recommendation) the starts read "Autofill anyway" and "Autofill and submit
  anyway..." under a note that says who skipped it and when (`skippedWhy`),
  repeated in the submit dialog; the server takes an archived row out of the
  archive when the run records the submission ([apply.md](apply.md), owner
  decision 2026-10-23).
- Reports render as Markdown, not the sandboxed HTML report in an iframe,
  because every response carries `X-Frame-Options: DENY`.
- The document drawer (`pages/Documents.tsx`, `pages/documents/`) shows a
  version's text (`GET /api/documents/source/{id}`) with Copy and, folded,
  the line of the user's material behind each sentence, and edits it through
  `POST /api/documents/{id}/edit`, a run that publishes a new draft version
  whose edited document has no PDF (checks "pending" until a PDF run; the
  other document keeps its passed PDF when unchanged, see
  [document-verification.md](document-verification.md); the source version untouched;
  a submitted version and one a newer version replaced are refused with
  `conflict`, so Edit is off on earlier versions). The text passes the generated
  draft's grounding gate (`validation_failed` names the findings), so an edit
  keeps each sentence's evidence ids. Unsent edits sit in a SaveBar: Cancel,
  the drawer's Close or Escape, a panel toggle and leaving the page ask first. "Make" and "Regenerate" post
  `POST /api/documents/resume` with `includeCoverLetter` and `pdf`, so one run
  makes the pair and both PDFs; a refusal below the floor offers "Allow a PDF
  anyway" beside the button. "Make PDF" posts
  `POST /api/documents/{id}/pdf`, storing the PDF in the next version under
  `documents/` (no save route); a document there that already passed is not
  built again. Only a current version offers it: a PDF run of an earlier one
  is refused with `conflict`, as an edit is, since it would make the older
  text current. A current version whose PDF did not pass
  (failed, unverified, stale verdict) offers "Make the PDF again" there with
  the failing checks named; it regates and rechecks the same text in a new
  version, which fixes a check that could not run but not a text failure
  (clipping, a missing line), so the note sends those to an edit first. A
  failed run publishes nothing and says earlier versions are unchanged. The
  editor keeps what was typed until the run has made the version, so a run that
  ends without one leaves the text to fix, and it has Cancel. A drawer opened
  while the application's run is working (`activeRunId` of the list, and
  `packRun` of the interview workspace for a pack) follows that run, links `/runs/{id}` and keeps
  Regenerate off. New runs send no paper or length: the stored defaults, edited in
  Settings, Documents, apply. A PDF
  refused below the 4.5 floor offers "Allow a PDF anyway", the override of
  [tracker.md](tracker.md), then asks for the PDF again; the refusal's own
  text (it names the command-line override) is replaced by a plain line.
  Mechanical checks that all passed fold under "Show the checks" beneath
  their count; any failure or unverified check shows the table open.
  A current version made before `cv.md`, `profile.yml`, `voice.md` (a letter
  the model wrote or revised from it) or the evaluation report changed says
  which, beside Regenerate (`sourcesChanged`); when it is the only current one
  out of date, its note offers to regenerate it alone (`.../cover-letter`, or
  `.../resume` without the letter), the pair staying on offer. An edit,
  revision or PDF of the other document then writes that document alone,
  never the replaced partner of its older pair.
  Current versions lead with the résumé; a version written without a model
  says so on its "Made" line. A working run names its stage in words and the
  time left before its deadline (`remainingSeconds`). A run refused for want
  of a posting (`details.field` `posting`: no report) offers a box to paste
  the job description, sent as `posting` without a PDF, which the floor
  refuses to a posting no report scored; an empty list then points at that
  box, and while a run works, at the run. While the row has no report
  (`noReport` of the list) no version offers "Make PDF": a line says the
  posting must be evaluated first, as there is no floor decision to
  override (that refusal carries no `below-score-floor` reason), and links
  Home's posting box filled with the row's address (`/?post=`). A report
  under the floor with no override (`belowFloor` of the list) offers "Allow a
  PDF anyway" when the drawer opens, before any run is refused. Missing PDF
  tools (`document-toolchain-unavailable`, at the request or at the run's
  end) link Setup's Résumé PDFs step, which lists them with their installs. Suggested revision asks differ for a
  résumé and a letter.
- "Revise with an instruction" (`pages/documents/Refine.tsx`): the model's
  answer waits as a pending proposal with a line diff
  (`GET /api/documents/refine/{id}`); Accept makes a new draft version through
  its own run, Discard changes nothing, and "Make PDF" is disabled with its
  reason meanwhile ([document-review.md](document-review.md)).
- Preview fetches `GET /api/documents/pdf/{id}` with the page's credentials and
  frames a blob URL (a frame cannot send the session key), hence
  `frame-src 'self' blob:` in the page policy; Download PDF saves those bytes
  as `<bundle>-<kind>-v<version>.pdf`. Each kind's current version leads;
  older ones fold under "Earlier versions", hashes and the version's notes
  (each run warning about that document or the whole run, never the other
  document's) under "Revision details".
  The GET routes sit under `source/`, `pdf/` and `refine/` because
  `documents/{id}/...` would collide with `documents/runs/{id}` in the router.

### Discovery

`pages/Discovery.tsx` and `pages/discovery/` keep the tab in `tab`.

| Tab | Address keys | Behaviour |
|---|---|---|
| Monitor | `run` (a scan just started), `items` (a finished scan's new items) | Reads `GET /api/scans?status=running` every 3 seconds while any runs; follows each dashboard-started scan on its run stream (`api/runstream.ts`: last 200 progress lines, re-attached by `Last-Event-ID`). A scheduler-started scan carries the scheduler's run id in `runId`, so it has a log and Cancel; cancelling stops the scheduler's job, which records the run as cancelled. "New items" reads `GET /api/inbox?runId=<scan id>` in the inbox's words (Waiting, rank), shows a gate's note under a waiting row and offers "Evaluate the N waiting" (one promote, refusals counted). |
| History | `source`, `status`, `from`, `to`, `page` | A 30-day source funnel (`GET /api/discovery/funnel`) above the table. |
| Run | `source` (the open options form) | Start is disabled with its reason while that source scans or its key is missing. A ready, session-gated source (LinkedIn, Glassdoor, Jobright) shows it needs a sign-in when `GET /api/browser/sessions` reports its site signed out or at a checkpoint, with the check's time; if that route is unreadable the scan route's readiness stands. Named presets live per source in this browser's local storage: the specification stores them nowhere else, and a blocked store only loses them. |
| Inbox | `status`, `q`, `since`, `page` | Below. |
| Sources and portals | `view` | Below. |

- Inbox (`pages/discovery/Inbox.tsx`, `/discovery?tab=inbox`):
  `GET /api/inbox?status=` (pending, queued, evaluated, dismissed, expired;
  pending by default; Expired only while it holds a row), reloaded on the
  `inbox`, `scans` and `runs` topics. Search asks the server (`q`: company,
  role, location or source) 300 ms after typing stops; status chips keep it.
  `since` (a local day, the link of Home's and the funnel's found counts)
  lists the rows first seen since in every status, with an All chip, the
  status counts of that window and a chip that clears it.
  Waiting rows are those a scan did not evaluate (it queues only its top
  `evaluate_top_n`); the tab bar counts them and the Waiting view says so
  beside "Evaluate the N on this page" (one promote of the page's ids), or,
  while an evaluation task has no model (`unrouted`), that nothing is sent to
  evaluation, linking Settings, Models. Waiting
  rows have checkboxes (labelled by company); "Select all N" ticks the page;
  ticked rows get "Evaluate N selected" (one promote) and "Dismiss N selected"
  (a dismiss each, one Undo for all). Ticks clear after a batch and whenever
  other rows show; the keyboard goes to Undo, or to a batch evaluation's
  "See them evaluating", as the rows and the bar it was in leave. A batch's
  note names each row the gate kept waiting with its rule (the answer is the
  only place it is kept). A page its last rows left goes to the last page
  that has rows (`usePageInRange`). A row names its posting in one Posting
  column (the company beside its tick box, the role and location under it,
  then its status and notes), so the role stays readable beside the browser
  panel.
- Evaluate posts `{ids:[id]}` to `POST /api/inbox/promote`, not
  `POST /api/evaluations`: promotion moves a row from pending to queued, and
  the finishing run moves only a queued row to evaluated, so a bare evaluation
  would leave it waiting. A row the eligibility gate refuses stays waiting with
  the rule named. An evaluating row links its run (`jobId`); an evaluated row
  links its report (`/reports?id=`) with the score, all carried on the row
  (`outcome`, `reportNumber`, `reportScore`), so a page of 25 asks for no run.
  Dismiss posts `POST /api/inbox/{id}/dismiss` with Undo; a dismissed row
  offers Restore (`POST /api/inbox/{id}/restore`, back to waiting). Rank is the
  local rank, a plain number, not the 0 to 5 evaluation score; "What is rank?"
  (closed) and the header tooltip say what moves it.
- Schedules: a `table.cards` of the user's schedules (name, recurrence in
  words under it and what its job does, from `GET /api/schedule-kinds`, in the
  one column that takes the share; at 1180 px a column of its own for the
  recurrence left the description two words a line and ran a time zone into
  the next run; next run and how
  far off ("in 9 h 30 min"), or Paused with why (`pausedBy`: "By Pause all", or
  until Gmail is connected again with a link to it), last result at
  `lastResultAt` (its run's start, not the cursor a resume or an edit moves)
  with its run (`lastRunId`; none after a skip) and, for one that did not
  succeed, how it ended and why (`lastReason`, `lastDetail`; the words under a
  run column's time wrap within 10rem, so a long reason never widens the
  table past its panel), the hygiene report's
  `attention` lines (duplicate, dead lane, never ran), an Enabled box, Run now, Edit, and More with History and Delete; a History
  row of a run reads Ran, Failed, Needs you, Cancelled or Running from its
  reason and end, as the outcome stays `fired`) plus the read-only built-in timers. A scheduler-written rule (frequency,
  interval, days, times) is put in words, any other shown as written; a time
  zone is named only when not this computer's. "Next 7 days" lays out enabled
  schedules from today in local time (daily every day, weekly on its weekdays,
  others where upcoming fires fall, so a monthly job shows only in its week);
  one firing more than once a day is named once above the table. The editor
  saves only after `POST /api/schedules/preview` listed the next five fire
  times of the exact recurrence on the form: asked 300 ms after a change, with
  what is missing said instead while incomplete; Save stays disabled until the
  form's answer arrives. Its Job list names each kind by `title` (a
  scan by its source's title, any other kind by `notify.TaskTitle`; the id
  shows beside its description); a name left empty saves as the job's title,
  and each input is labelled from its name in words ("topN" reads "Top N")
  over the doc the kind gives it. Run now confirms with a link to its run. Pause all and
  Resume all sit in the panel header (`POST /api/schedules/pause-all` and
  `/resume-all`); Resume all shows its count (`pausedAll`) and is disabled at
  zero, so it never enables a schedule the user switched off; each names the
  schedules it switched. The editor's time zone says quiet hours hold only a
  schedule's notifications, never the schedule. Ask Agent
  proposals (`GET /api/agent/schedule-proposals`) sit above with Accept and
  Dismiss, which has Undo (absent while none waits); one the scheduler refuses stays pending
  with the reason.
- Sources and portals (`pages/discovery/portals/`): Forms (default) and Raw
  text. Forms show the registry count, last seed date and a seed button (a
  `registry` run, streamed and cancellable, `POST /api/registry/seed`), the
  company table (`GET /api/portals/companies`: board, enabled box, last scan,
  job count, status, confirmed Remove), the add form, a source's search
  queries, and "Filters and speed": the global title lists, seniority weights
  (`term: weight` lines), skipped companies, location lists and
  `concurrency.list`/`detail` (blank shows and keeps the default). Add previews
  first: `POST /api/portals/detect` reports a board (ATS and slug, posting
  count, already in registry or file), several matched boards to pick from, or a
  careers page; nothing is written until Add. The company and search writes
  (`portals/edit.ts` `opsFor`) touch one list item each through `PATCH
  /api/config/portals` with the revision read: `append`, `remove` by index, or
  `set` of `companies[i].enabled`, so every other entry keeps its bytes and
  comments; a flow list of entries (`[{...}, ...]`) the edit reaches into is
  first rewritten in block layout by the server. The change is kept as what the user meant (the item as
  read, not its position): on `revision_conflict` the page re-reads the file,
  works it out again there and offers the shared `Changed` notice ("Keep my
  change" saves it on the new revision, "Load the new version" drops it); an
  item removed or edited on disk meanwhile is not brought back, and the note
  says so. "Use my target roles" re-reads its plan on a conflict. "Filters and speed" sends one operation per
  changed key (`keyOps`; a missing or empty map is written whole), so comments
  elsewhere keep their bytes, and uses the profile forms' `SaveForm`: unsaved
  guard, and on `revision_conflict` the shared `Changed` notice ("Keep my text"
  reloads under the edits, "Load the new version" drops them). The server
  validates the result and names a refused key (terms must not be blank,
  weights -10 to 10, concurrency 1 to 16). Forms stand down when the
  file is missing or has problems and link to Raw text, which is Settings'
  Files editor (`RawEditor` in `settings/Files.tsx`) on `portals.yml`, under
  the same rules (an invalid save is refused with line and key; the saved
  file's findings list under the editor).
- The signal drawer's "Add to portals" reads the config route's parsed
  `values` and writes the whole `companies` list with a `set` operation, like
  the profile pages.

### Leads, Gmail and Browser

The Leads and Signals page (`pages/Leads.tsx`, `pages/leads/`):

| Tab | Contents |
|---|---|
| Leads | State defaults to new; Search (`q` in the address) asks the server 300 ms after typing stops or on Enter, every word in the company, poster, roles or evidence title, and Clear filters drops it; category counts are links, each the count of what it lists (the state, source and search in force); Sort puts the highest confidence first (kept in the address as `sort=confidence`, and in the category links); switching tab clears every filter. A row names who posted it (and whether a person or company page) with a link to the original post, and a lead promoted to the inbox links that inbox row, its report and its tracker row; a row's evidence opens in place, and a contact address is a mail link. Evaluate (offered only where `evaluable` says a run acts on the lead), Read careers page, Dismiss and Block company act on one lead and confirm beside the list (a started pass links its run). Dismiss offers Undo (`POST /api/leads/{id}/restore`, back to new) and a dismissed lead Restore; Block offers Undo only when that press added the entry, so it never lifts an earlier block. The two batch buttons pass over all new leads and are disabled while the unfiltered New view is empty. While a pass runs (`evaluating`/`expanding`), its buttons are disabled and the row that started it reads "Evaluating..." or "Reading...". A row's actions keep one place beside it, level with its first line, and go under it on a narrow page. |
| Hiring signals | Status strip, tier counts as links, tier, source, state and date-range filters in the address, a count line and server-side pages of 25; the Sources fold (open until the first collection) has a switch per source that writes `enabled` under its `signals-*` entry of `sources` in `portals.yml` on the file as it is then (switching on a lane that carries a `disclosure`, the SEC source's `signals.EdgarDisclosure`, asks first with that text), shows a switched-on source's last collection when it was skipped or failed (`skipped: no queries`), and links `portals.yml` (Discovery, Sources and portals, Raw text) for the `queries` some sources read; the empty list points at those switches; Collect now (its note links the run), Rescore, and a drawer with the evidence chain (source item, event, assessment with verified quotes), the five factors, postings and outcomes. Dismiss, on a row or in the drawer, has Undo; the drawer's evaluation note links the evaluating inbox rows. |
| Review queue | Approve and Reject on a held assessment; Approve confirms what it did, Reject has Undo. |

- `GET /api/gmail/health` carries `connected` (an app password is stored),
  `login` (`ok`, `refused` once Gmail refused the stored password, `unknown`
  when the keychain cannot be read) with Gmail's own `loginError`, and the
  `mailbox`. Not connected, the Gmail page shows "Not connected" with Sync now
  disabled; refused, it shows a banner (create a new app password and enter
  it, the inbox below is stale) and Sync now is off until a new one is
  entered. Not connected or refused, the page draws the connection form in
  place (Settings' control, three numbered steps with the 2-Step Verification
  and App passwords links); a healthy one changes it from Settings. The form
  takes the Gmail address and the app password (a write-only box emptied as it
  is sent) and Connect (`PUT /api/gmail/account`) signs in to Gmail before
  anything is stored: a refusal comes back on the password field, a stored
  connection says "Gmail is connected as <address>." and starts the first sync
  (`POST /api/gmail/sync`). Another address than the stored one is noted
  before Connect (it syncs from its own start). Connected, the form adds "Test
  the connection" (`POST /api/gmail/account/test`, the messages of the last
  day counted, none opened) and Disconnect behind a confirmation (`DELETE
  /api/gmail/account`). The status reads "Connected", "Password refused" or
  "Keychain not readable", "Read-only (IMAP with an app password)" as access,
  and the last run as "Last sync". Settings, Gmail and the Setup step's Gmail
  screen draw the same form.
- The sidebar's Gmail entry carries a warn badge with the number of do-now
  threads (`GET /api/gmail/inbox?lane=do_now&limit=1`, read from `total`), on the
  inbox's topics; it is the set Home's "job emails need you now" line and the
  Gmail page's do-now lane use, and the inbox with every lane listed says
  "N need you now" as a link to that lane. The Tracker row drawer's Email section lists the
  threads linked to the row (`?candidacy=<id>`), each opening
  `/gmail?thread=<id>` with its lane, next action, whose move it is and the age
  of its latest message, or says none is linked. A Tracker row also shows, under
  its status, its most pressing open thread as "next action, age" (for example
  "Reply to the message, 2d"), opening that thread; the list reads it from the
  `mail` field of `GET /api/candidacies`, the same link and lane the badge and
  the inbox use. The Tracker's Email filter (`mail=needs_me` or `waiting`, kept
  in the URL and the export) keeps the rows whose most pressing thread is the
  user's move or theirs; the server reads the mailbox before paging so the total
  and pages count the narrowed rows, and a remote client is refused it (422),
  since it never gets mail. The Gmail thread drawer's Reply (or Follow-up) draft shows the kept
  draft with what its claims rest on, writes one on request, Regenerate (the
  replaced draft stays folded with its own Copy, kept per thread in the tab's
  session storage across reopening) and Copy; with no model routed its error links
  Settings, Models; nothing is sent. I replied notes it on the linked row,
  records a follow-up on an applied row and marks a thread that asked for the
  reply done; Undo takes back all of them on the server.
- The Gmail inbox searches (`q`: employer, role and subject; applied 300 ms after
  typing stops or on Enter) and filters by kind (plain-word names of the
  classifier's types), both in the address. Each row and the thread drawer offer
  Done (only in the do-now, prepare and review lanes) and Dismiss; a note above
  the tabs says what was decided and has Undo, focus moves to the next row (after Undo, to the
  restored row, or to the notes when the filter hides it), and
  a new message on the thread brings it back (see [gmail.md](gmail.md)).
  A row's attention chip is left out when the thread asks nothing of the
  user: an offer or a rejection still sorts first as urgent, but a red
  "Urgent" beside "Nothing to do" would contradict itself.
- The Gmail Link review shows ten rows, then Show more: each row reads the
  tracker for its employer's applications, and a mailbox held 400 to decide
  (200 of each half of the queue, the server's cap), all drawn at once with a
  tracker read each.
- The Gmail Link review's "Not job related" leaves a note with Undo in the
  panel (`POST /api/gmail/links/{signalId}/restore`, which takes that one
  rejection off); the note stays until the panel is left or Undo is pressed.
  An offer's Dismiss or Confirm under Confirmations has Undo the same way
  (`POST /api/gmail/confirmations/{id}/restore`, which puts a confirmed row
  back in the same call). The last sync's "N for review" links the Review tab.
- The Browser page (`pages/browser/`) polls `GET /api/browser/status` every
  5 seconds while visible and on the `browser` board topic. A tab's holder is
  "Applysmith is using it" (a run owns it, not kept), "Waiting for you" (a
  stopped run kept it) or "Yours" (no run); a run's tabs list first and link
  `/runs/{id}`. Every apply tab has "Show in panel"; one no run uses has "Show
  in Chrome" (posts its `tabId` to the show route); the two wrap together,
  never one beside the chips and one under them. A blank tab reads "New tab"
  with no address; a lane counts "3 tabs (up to 24)". A lane's picture follows
  the tab a run uses, else the active, else the first; the same picture is
  "Page" in an apply run's drawer (polled every 5 s while the run works). It
  is fetched from the screenshot route as a blob (an image element cannot send
  the session key), one at a time, no sooner than 2 seconds after the previous
  started, never while hidden. "Show apply window" posts to the route (no
  desktop binding yet; the route raises the window). Stop confirms; a
  `lane-leased` refusal offers "Stop anyway". Session sync lists sites the
  extension counted (`sourceCookies` above zero) with both cookie counts, never
  a name or value, syncs one site or, with "Sync all", every counted site in
  one request, polls while a request is pending, and offers a confirmed undo
  that names the last sync's sites (`lastImport.domains`). Sign-in rows are every site the check reads (`GET /api/browser/sessions`
  rows with a sign-in address: state, last probe time) and open its sign-in
  page in a new apply tab shown in the browser panel; a row not signed in
  makes that the primary button. A check counts what it found ("2 signed in, 3
  need you") and puts each site's result on its row ("This check: ..."). A
  stopped browser says Chrome starts when needed. A lane reads Closed, "A
  run is using it", "A tab waits for you" or Idle by its tabs' holders.
  "Autofill this tab" is disabled, with the reason as its title and beside
  it, while the status read shows the apply window with no tab (stopped, or
  running with none); an unread status leaves it to the server's refusal.

### Agent control

Agent control (`pages/AgentControl.tsx`, owner decision AC-5) shows the
agent's desktop control through PersonalCU. From 860 px of page width the
service and the controls form a sticky column beside the activity; narrower,
they come first.

- PersonalCU (`GET /api/computer-use`, every 5 s) names its health with what
  to do for each one in a user's words (not running: open PersonalCU from the
  Applications folder; no build command; paused names which of three it is,
  each with what turns it off: PersonalCU's own pause (`servicePaused`), its
  background control switched off (`backgroundOff`), else this page's Pause),
  once: the server's
  `reason` restates it and is drawn only for a service that does not answer,
  where it is the error, the two permissions (not known while it is not running)
  and where screenshots go (`imagesToModel`, AC-3). Settings → Computer use
  shows the same block, a link here and that PersonalCU keeps both permissions
  across rebuilds once signed with its local signing identity
  ([computer-use.md](computer-use.md#signing-and-permissions)).
- Pause posts `POST /api/computer-use/pause` and draws the state the server
  answered. Stop follows the conversation of the newest step on its event
  stream and, while that turn runs, cancels it (`POST
  /api/chat/sessions/{id}/cancel`).
- The activity (`GET /api/computer-use/activity`, every 1.5 s while visible)
  lists steps newest first in their turns: time, tool, app, window, the
  control's role and label, verdict and reason; a Keys step shows its key on
  key caps and a Scroll its direction and pages (`detail`). A list of windows
  and a call refused before the gate (window gone, stale look) are steps too.
  When a step was refused or failed, buttons narrow the list to All, Refused
  or Failed, each with its count. An action's step shows the screenshot of
  the look it was decided on with an outline around its control (`target`,
  fractions of the picture; red when refused); a look and its action share
  one read of the picture (`holdShot`). A look's screenshot is fetched
  as a blob from `/api/computer-use/shots/{id}` and opens larger in a dialog;
  one gone from the server reads "No longer kept", one that failed otherwise
  is a "Not loaded. Retry" button that fetches it again.
  The wire holds no typed text, and the page has no field for any.
- "Let the agent finish" (`pages/runs/Takeover.tsx`, AC-4), on Home's Needs
  you, in the run drawer and under the latest note about a run in the
  conversation that started it (`RunNote` in `askagent/Transcript.tsx`: the
  note's `runId`, the run read again on each runs change, nothing while the
  conversation runs a turn), shows for a run waiting on answers whose tab is
  kept and that Done can resume (`canFocusTab`, `canDone`). It posts `POST
  /api/apply/runs/{id}/takeover` and opens `/ask?conversation=<sessionId>`.
  Home offers it and "Let the agent do it" only while the agent has a model
  (`noAgentModel`: not the offline simulation, `agent-kernel` routed).

### Settings and profile

- Settings asks `GET /api/remote/me` first (`identity.ts`) and draws nothing
  until it answers. A paired remote device gets the heading and one sentence,
  no section and no section request. A failed answer is read as the computer
  itself, because a device that cannot ask is refused everywhere. Remote access
  polls its status (3 s) and devices (15 s) only while its section is shown,
  and reads both once as it opens again.
- Models shows each configured provider as untested, working or failing from
  the routing view's `lastTest` (only the newest is stored) and follows a
  running test through `testing`. The routing table gives each task's `purpose`
  (task registry) and 30-day cost (`GET /api/llm/cost?days=30`, `byTask`:
  spend, calls, unpriced calls), and its Budget column the daily budget, the
  spend today and "Reached"; the Budget action edits it. The daily cap, Home's
  "Model spend today" and Usage's "Today" all show the routing view's
  `capSpentUsd`, the metered spend of this computer's day the cap is held to;
  Usage also shows 7 and 30 days, says what the cap does, and names each task
  at its budget. Its Model calls table puts the time under the task (an id
  that never breaks at its hyphen), the provider under the model, whose whole
  id wraps in its column, and tokens in and out in one column, so a call's row
  fits a 1180 px window; the Live table puts its time under the task too (it
  was 1214 px wide at 1180).
- Runtime settings write each change at once through
  `PATCH /api/answer-bank/settings`; the legal toggle and the least strict
  grounding mode ask first, and the server audits the legal change. Watch scans
  (`watchScans`) writes at once with no dialog. Detection and cooldowns
  (`settings/Risk.tsx`) is read-only from `GET /api/policy/risk`: running
  cooldowns, 30-day challenge rate per browser source, recent detection
  signals.
- Files edits `profile.yml`, `portals.yml`, `policy.yml` and `cv.md` as text
  (`RawEditor`, which follows the `configuration` topic through
  `useRevisedDraft`: text with no edit shows the file as it is now, an edit is
  kept and saved at the revision it was made at); a changed file is never
  overwritten unless the user
  presses "Keep my text" in the shared `Changed` notice (`ui/SaveBar.tsx`),
  which loads the latest revision under the draft so the next save replaces it;
  "Load the new version" drops the draft (CV, narrative, voice and every
  `SaveForm` show the same notice, scrolled into view; a profile form reloads
  under its kept edits through `useDraft` and sends only them). A save of
  profile.yml from one form (`saveProfile`, Files' text with no unsaved edits
  of its own) makes every other open form of it read the file again
  (`useProfileSaves`), so Scan defaults saved after the gate is not refused as
  someone else's change. Quiet hours and the Policy form follow the
  `configuration` topic through `useRevisedDraft`: a form with no edit shows
  the file as it is now; an edit is kept, and once the values under it differ
  from the ones it was made over its save quotes the older revision, so the
  server refuses it and the form offers the current version or the edit again.
  Both ways to save the policy are session-only: the Policy page sends
  `PATCH /api/config/policy` (key-path ops, quoting the revision it read), and
  the Files text editor sends `PUT /api/config/policy` (the whole file). The
  Captcha solver switch sends its one key through the same `savePolicy`, and
  again at the revision read afresh after a conflict, since one key set by the
  owner's press overwrites nothing else. In the
  desktop app (`window.mygo` exists; `web/src/desktop.ts`) Files adds "Reveal
  in Finder" for the workspace folder and the start-at-login checkbox (read
  from the system each time the section shows), and the document drawer has
  "Reveal in Finder" by the saved PDF path; a browser shows none. Advanced
  shows the versions from `/api/health`, the doctor
  (`POST /api/setup/doctor`), the redacted log and the diagnostics export.
- Scan defaults and Eligibility gate (`settings/Policy.tsx`) edit profile.yml's
  `scan` and `eligibility_gate` like the profile forms; a blank field unsets
  its key so the default it names applies, and a top N replaces the feed's
  `evaluate_limit`, which would win.
  Under Scan defaults, Scan source API keys (`settings/SourceKeys.tsx`)
  saves and removes the key of each source the catalog gives a `keyName`
  (`PUT`/`DELETE /api/secrets/{name}`, write-only like the captcha key); a
  `needs key` row on Discovery links there (`#settings-scan`).
- Answer Bank, Profile and Wiki keep their tab in the query string.
  Profile pages write through `PATCH /api/config/profile` with whole-list `set`
  operations in block YAML (`api/yaml.ts`). Every string is written quoted.
  Company relationships writes `company_relationships.complete` whole as well,
  so a family's mark also lands on a `complete` mapping written in flow style.
- The identity tab shows the CV parser's draft (`GET /api/config/cv/draft`,
  `internal/cvparse`) with Confirm and Discard per entry. Confirm writes the
  saved list plus that entry through the profile patch; Discard records the
  entry's key on the server (`POST /api/config/cv/draft/dismiss`), so it stays
  gone, and writes nothing to the profile; its Undo (`restore`) puts the
  entry back. Setup's review records the entries it removed the same way
  (`draftKey`). Entries the profile holds are not
  offered; Confirm waits while the history editor has unsaved edits (a save's
  reload would drop them). A year-only date arrives as `YYYY` and is typed as
  `YYYY-MM` in the same `EntryInput` as setup's review; Confirm stays disabled
  until it is one. "Confirm all N skills" writes every skill left after the
  discards in one patch, after the saved ones (a CV listed 105 skills, each a
  press of its own).
- A refused profile save names fields by profile path (`details.fields`).
  `SaveForm` shows each beside the control that names that path (`data-path`;
  a list's cells own `list[i].key`, a company relationship counting the
  families saved before its own), marks it `aria-invalid`, focuses the
  first, and keeps a one-line summary below with any finding no control owns;
  an edit of a field takes its finding away. Pay is typed as text read as
  people write it, "120,000", "$120,000" or "150k" (`payNumber`), on Profile
  and in setup step 5 alike; office days and travel as a whole number
  (`wholeNumber`), a skill's years as a number that may have a fraction
  (`amount`); text that is none shows a hint and holds the save. A
  sponsorship answer can be set back to Not answered, which unsets the key. A
  place already in either list stays in the box with a note.
- A résumé upload meeting a PDF left on disk after a roster delete gets
  `conflict` with reason `file_exists`; the page tries the next suffixed name,
  at most 20 uploads in all, and never replaces the file.

## Lessons from the Codex desktop app

The Codex desktop renderer (inside `/Applications/ChatGPT.app`, Codex 26.1013)
was read for interaction design. Its tokens already match ours (4 px spacing,
13 to 14 px text, similar radii and motion), so only behaviour was taken, each
stated where it applies (Runs, Ask Agent, Shortcuts, Components, Settings, the
browser panel). One more: a decision request sits where the user types,
answered by number keys from the narrowest scope to the widest, the last being
"No, and tell the agent what to do differently"; unlike Codex, no key ever
approves a submit, which stays in its dialog. Not taken: app-wide Enter to
approve and Escape to decline (too risky next to Submit), toasts, and a token
or density rework.

## Setup wizard

`/setup` renders `GET /api/setup/state`, the one catalog of
[setup.md](setup.md); the page has no step list, title or required flag of
its own. It opens on the state's `next` step (orientation when every other
required step is satisfied) and keeps the choice in `?step=`, so a reload
resumes. Every step has a screen: a schema-driven form over its `fields` (only
changed fields sent; per-field errors from `details.fields` beside their
inputs) or a purpose-built one (CV draft review, résumé roster with the
rendering subcheck and the external install budget kept apart from the app's
own, answer-bank entries by group, model provider with a write-only key,
discovery with the SEC disclosure). Steps needing a person outside the
dashboard (a Gmail app password, Telegram, 2Captcha, the extension, the apply window)
show the instruction, exact commands and a link to the hosting page, and
refresh on the `setup`, `gmail`, `configuration`, `answerbank`, `schedules`,
`notifications` and `browser` board topics. "Check everything" posts to
`/api/setup/doctor`; a result a setup step fixes shows "Fix it in Setup: <step
title>" linking there (Settings → Advanced's doctor too), never command-line
fix text.

Home shows a progress card until every required step is satisfied and the
doctor reports no required failure; the doctor runs once the page has
settled (`Settled` in `ui/Settle.tsx`).
The card names a failing check by its step's title (`identity.fields` is "Your
identity and contact details") or, with no step, by its own summary, never by
id. Required steps not done are under "Left to do"; a failing check inside one
is expected and not named as a problem.

A form starts from what is stored: the profile's values, and on the story step
`narrative.md` and `voice.md` (the seed text and a missing file start empty),
each box counting characters against its minimum as the step counts them, so
a narrative saved short is finished rather than typed again.

A save says "Saved." above the writer's notes (a check that writes nothing
shows only its notes). The generic Acknowledge is left out for a review a
screen settles with its own control (the CV draft's Confirm, the skip list's
"Keep this list").

The step list is a stepper: a status icon beside the status word
(`statusWord`; visually hidden for complete steps, whose reasons read "Done
because"). Each step ends with the previous step and Next, or from a done step
"Continue to" the first unfinished one. Below 660 px the step precedes the
list; on a phone the whole list folds instead into one "Steps (n of N done)"
line above the step, which folds again when another step opens. "What is left" renders reason text unchanged; it names a field in words
("city is empty") and the configuration path travels in the reason's `field`,
shown as a small detail when the sentence lacks it. The identity form uses two
columns where they fit.

The CV step and the Profile CV tab end with a collapsed "Add more documents
(optional)" section (`src/pages/intake`). It reads the chosen files in the
browser, sends their text to `POST /api/setup/intake/preview`, and lists new
entries, conflicts, matches and, apart, "Suggestions, not facts". Nothing is
ticked on arrival; a conflict needs a side per field, a year-only date its
month, a suggestion its own confirmation. Applying posts the revision, the
documents and only finished selections; a `revision_conflict` asks for a new
preview. No request is made until a preview is asked for.

Above the wizard, Setup shows "Try the demo" (`src/pages/setup/Demo.tsx`,
`POST /api/setup/demo`; Home's setup card links it until the identity step is
done, and from then on Setup folds it to its heading) or, in a demo workspace, the short demo tutorial, one linked step per
page of the loop. Orientation's daily loop links each step's page. The
shell reads `GET /api/setup/status` and in a demo workspace shows the "Demo
workspace" banner on every page, linking that tutorial; what the demo never does
 sits under a "What the demo never does" disclosure to keep it short on a phone. There is no workspace
switcher; the demo opens as its own server
([setup.md](setup.md#demo-workspace)). The demo's Run tab says what a scan
does there; the sample-form run is named after its posting. A step's guide
(`docs/setup`, one lazy chunk each) opens in "Guide for this step".

The user guide reads in the app, so no screen names a repo path to a person who
has no docs folder. **User guide** (`/guide`, last of System, so under More on a
phone and in the palette; `?doc=` names the open document, the tabs are the six
files of `docs/user-guide`) is `src/pages/Guide.tsx`: each document is a lazy
`?raw` chunk and each screenshot a Vite asset from `docs/user-guide/images`
(`?url`, fetched when its document opens, never copied), drawn by the same
`Markdown` as the setup guides. `docLinks` rewrites a link between guides: to
`/guide?doc=` or to `/setup?step=` when the app carries that file, otherwise
the label alone (a document outside `docs/user-guide` and the numbered setup
guides, such as `policies`, is not bundled). Setup's guides use the same
rewrite, and a click on such a link navigates in the app. A screenshot is a link
to its own file (`a.md-shot`); in the guide a plain click opens it at full size in
a scrolling `Modal` (`wide`), because the 1280 px pictures cannot be read at
phone width (a modified click follows the link). Orientation, the demo
tutorial's last line and the shortcuts dialog link the guide. A guide edit
rebuilds the bundle through `WEB_SRC` in the Makefile.

## Deferred against the dashboard spec

Each item names the `tasks.md` task that owns it; "no owner" means none does.

| Item | Spec requirement | Owner |
|---|---|---|
| The Tracker per-thread summaries in the row drawer | Tracker | no owner; the drawer lists the linked threads (subject, lane, next action, owner, age) and the row shows its most pressing one |
| Tracker: a status multi-select beyond the "In flight" chip, a custom date range, sort on Source | Tracker | no owner; multi-status, the 7/30/90-day application window, source text and Role/Status sort are done |
| Reports: PDF-threshold override | Reports | no owner for the override route |
| Per-run streams other than a scan's (the Runs drawer polls every 3 seconds) | Live updates | no owner |
| Revision conflict dialog with compare beyond Notifications and Policy (the other editors keep or drop the draft) | API client | no owner |
| Toast system, with polite and assertive live regions (inline notes and the Notices list stand in; see Decisions) | Accessibility | no owner |
| Cross-page canary test that a stored secret appears nowhere | No secret value is rendered | no owner (needs every page) |
| The file picker binding has no dashboard use yet (nothing in the pages asks for a local file path) | Embedded app | no owner |
