# Browser extension

The extension is a Manifest V3 shell for the user's everyday Chrome. It adds a
panel and page widgets to application pages and talks to the local API through
one service worker. The in-page autofill engine is not copied: the build
bundles `engine/src` into `engine.js` and the extension injects it.

## Build

    make generate                    # includes the extension
    go generate ./internal/extension # only the extension
    applysmith extension export <dir>   # write the embedded copy as an unpacked extension
    cd extension && bun install --frozen-lockfile && bun run test && bun run typecheck

`internal/extension/builder` bundles the entries with the esbuild Go API and
needs no node_modules. It fails the build when:

- the manifest is not the spec's install surface (`CheckManifest`: exact
  permissions and host access, no `externally_connectable`, no web-accessible
  resources, no main-world content script, one command);
- `loader.js` exceeds 16 KiB or the unpacked output exceeds 4 MiB;
- a TypeScript source file exceeds 1,500 lines;
- anything outside `src/sw/api.ts` calls `fetch`, `XMLHttpRequest`,
  `WebSocket`, `sendBeacon` or `EventSource`;
- a bundle takes an input from outside `extension/src` and `engine/src`.

The stamp is the first 16 hex characters of a SHA-256 over the extension
sources and every file under `engine/src` (the extension's bundles take engine
modules that `engine.js` need not contain). It is embedded in `sw.js`, `popup.js` and the
manifest's `version_name` (`<version>+<stamp>`). `internal/extension` embeds
the output (`generated/`, not committed) and `Export` writes it.

The manifest `version` changes with every build: the source version (at most
three numbers) followed by the stamp's first four hexadecimal digits as a
fourth number, 0 to 65535 (`builder.BuildVersion`; `0.1.0+51824efc5fa41498`
ships as `0.1.0.20866`). Across a browser restart Chrome re-reads an unpacked
extension's manifest but keeps running the service worker it registered while
the version is unchanged, so a re-export into the same folder ran the old
worker (reproduced 2026-10-17 in a disposable headless Chrome for Testing:
manifest `version_name` new, worker source old). With the per-build version
the new worker runs after a restart and after an extension reload (same
check, both ways). The popup, the Settings page and `GET
/api/extension/build` show the version name's version, so they still read
`0.1.0 <stamp>`. Two builds share a version only when their stamps share the
first four digits (1 in 65,536).

## Loading it unpacked

The desktop app keeps one copy in `extension` in its user data directory
(`~/Library/Application Support/Applysmith/extension`): each startup attempt
replaces it whole, through an export beside it and a rename, when its manifest
is not the embedded build's (`extension.Keep`), and Settings, Browser extension
names it in the app (`ShellInfo.extensionDir`) in place of the export command.
Open `chrome://extensions`, switch on developer mode, press Load unpacked and
pick that folder or the exported directory, then type the code from Settings,
Browser extension into the popup. The dashboard shows it as `<port>-<code>`
(its own port), so the claim goes to `http://127.0.0.1:<port>` and that address is
saved once paired (`parseCode` in `src/sw/pairing.ts`); a failed claim changes
nothing, and a bare code goes to the saved address (`http://127.0.0.1:47319`,
the CLI's default, until one is saved). The address is not written into the
export: the CLI exports without a running application, and a baked-in port
would go stale with no fix short of a new export. The desktop keeps its port
across restarts while it is free ([app.md](app.md)); when the port moved, the
Offline popup offers the code field, and a new code reconnects in one step.
A workspace switch in the app (demo and back) moves the server to another
port and never onto the one just left, so a pairing is never presented to a
server that rejects it; the popup reads Offline meanwhile ([app.md](app.md#switching-to-the-demo)).
Every authenticated request also carries `X-Applysmith-Extension: id=<random
install id>; version=<v>; build=<stamp>; managed=0|1` (`client` in
`src/sw/state.ts`). Settings, Browser extension lists those installs with when
each was last seen; every paired extension holds the same token, so the list is
presence since the server started (kept in memory; the 30-second check-in
refills it), and Unpair extensions, after a confirmation, replaces that token
for all of them.
One shared token, not one per install, is a decision (the owner left it to
the maintainer on 2026-10-23): the application has one user, an install is
removed by removing the extension from its browser, and a lost or leaked
token is closed by Unpair for every install at once, which re-pairing with a
fresh code undoes in seconds. Per-install tokens would add a table, a
migration and a re-pair of every install for revoking one of a person's own
browsers, which nothing asks for.
Application address stays for entering an address by hand. An address that
accepts the connection but does not answer health within 3 s is Offline. Chrome branded builds ignore `--load-extension`,
so the real-browser check (below) uses Chrome for Testing or Chromium.

## Contexts and who may do what

| Context | Code | May |
|---|---|---|
| Service worker | `src/sw/*` | Hold the token, call the API (`src/sw/api.ts` only), inject scripts, read cookies. |
| Popup | `src/popup/*` | Pair, change the address and the switches, push a site's sign-in. Forget pairing asks first (Forget or Keep pairing), since only a new code undoes it. A switch whose save fails goes back and says why. A form's button is held while its request is on its way (Pair reads Pairing...), so a double press claims a code once. Only the status lines are live regions, not the whole popup. Only the popup page may send `pair.*`, `settings.*`, `site.set` and `sync.host`; the worker checks `sender.url`. |
| Loader | `src/loader.ts` | Top frame of an http(s) page, once after idle, at most 16 KiB. Asks the worker whether the host is switched off, then classifies the URL (engine site table, permissive heuristic) or watches for a form for 30 s. Makes no network call. |
| Panel | `src/panel/*` | Injected by the worker into the top frame on demand. A closed shadow root, ARIA tabs, floating or bottom sheet at 480 px and narrower. Opens by itself once per tab and host on a supported site: an application system of the engine's site table (Workday, SmartRecruiters and the rest, not only the hosts the frame registry admits) or a registry host. Starts, pauses and stops a fill; never calls the API itself. A tab drawn again (each poll of a running fill, another card's draft arriving) keeps keyboard focus on the same control, and a text field's caret (`redraw` in `shared/dom.ts`); Escape closes the panel except while an input method is composing. Not connected, each tab says why and offers Check again, whose answer says "Checked just now." as the popup does (the connection is also read again when the page regains focus); once the extension was reloaded under the page, the panel says to reload the page. A fill's result leads with the required fields still empty, gives each unanswered field its reason in words (every engine row reason and matcher abstain reason has wording in `WHY`; an unknown one falls back to its code with spaces) and, for a top-frame field, Show (scroll, focus, a brief outline), and lists fields not filled on purpose apart unless the page requires them; a field the person edited (`user-edited`) is always listed there. |
| Bridge and engine | `src/bridge.ts`, `engine.js` | Injected into each admitted frame by a fill. The bridge answers the engine's three bindings through the worker and forwards the person's own committed values. |

Storage (`chrome.storage.local` and `.session`) is restricted to trusted contexts when the worker starts, so a content
script can neither read the token nor anything else; `test/custody.test.ts` also fails the build of any other file
that names `chrome.storage`, `chrome.cookies` or the bearer header. Every call to the API is the worker's: a content
script asks by message and the worker answers only `sender.id === chrome.runtime.id`.

## A fill

1. The panel's Start button sends `fill.start` (top frame of a tab only) with one reading of the page
   (`shared/capture.ts` `readPosting`). The worker resolves the tab's job from it (`sw/fill.ts`, see below) and
   passes that posting to the engine and the résumé ranking.
2. The worker reads `GET /api/autofill/applicant` and `GET /api/answer-bank/settings`, then obtains an issued run with
   `POST /api/autofill/runs`, and walks `webNavigation.getAllFrames` with the engine's ladder: registry frames are
   injected, deny-listed ones never touched, unknown ones are counted by `probe.js` and get the engine only when they
   qualify. The top frame always runs the engine.
3. The engine runs in each admitted frame's isolated world. Its match binding becomes `POST /api/answer-bank/match`
   with the issued `runId` (options are sent as value and label pairs; the response is mapped to the engine's
   `Resolution`). Its résumé binding hands over the Résumé tab's chosen variant (see below) and answers `null` when there is none.
   The engine's drivers for a component that keeps its state in the page's world only (ADP's `sdf.commit`, the
   react-widgets dropdowns, Ashby's save queue) act through the engine's main-world helper. The extension installs no
   such helper and never calls `useHelper`, so those drivers run only under the Go host (`FillOptions.MainWorld`, see
   `engine/README.md`). In an extension fill an ADP select whose click commits nothing stays `write-not-committed`, a
   react-widgets dropdown ends `binding-unavailable`, and Ashby's save queue is not used.
4. When every frame has ended the worker reports each profile-lane and attestation-lane write through
   `POST /api/answer-bank/system-writes` (run id, selector, value), once, before any user edit counts, and then ends
   the run with `POST /api/autofill/runs/{id}/end`.
5. After that, a trusted `change` the person makes in an admitted frame reaches the worker through the bridge. The
   worker turns it into a `manual-fill` event with `trustedInput: true`, the run id, the selector, the system's value
   as `before` and the bank entry that produced it, the section captions (contacts masked as in the engine's events)
   and the options the engine's inspection read for the control (so the server knows a pick by its options too, flow
   round 6, M-1), and uploads events in batches of at most 50 after two quiet seconds (one retry, then dropped).

Never sent: a password, a one-time code, a file, a value the engine withholds (self-identification, date of birth),
or a value equal to what the system wrote. The bridge asks the engine's own classifier (`engine/src/sensitive.ts`)
and the engine's inspection, which leaves the value off a withheld control.

A fill never presses Submit. The engine's click guard and submit hold apply; the worker adds no click. The one press the extension can make is the step walker's final control, only when the person ticked the walk's submit option, and only with a press claim (see Tabs and widgets).

## Server routes the extension calls

All are access class `standard` and remote denied: the paired extension's bearer token and a dashboard session may
call them, a device on a remote listener may not.

| Route | Notes |
|---|---|
| `GET /api/health`, `POST /api/pairing/claim`, `GET/PATCH /api/answer-bank/settings`, `POST /api/answer-bank/match`, `POST /api/answer-bank/events`, `POST /api/answer-bank/system-writes` | built earlier |
| `GET /api/autofill/applicant` | The snapshot the engine starts with: names (first, middle, last derived by whitespace), email, phone with its calling code and national digits, address, links, employment and education with `YYYY-MM` dates (no invented day: a whole-date box is filled only from a stated day) and the current and in-progress flags. Profile values win; a blank one is filled from the bank's identity entries. Nothing sensitive and no answer other than identity. |
| `POST /api/autofill/runs` `{host, url}` | Issues the run that match, system-writes and events name. Needs an `Idempotency-Key`; the page URL is kept without query or fragment; at most 16 are open (`too-many-fills`). The run has kind `extension-fill`, no work and no submit authority. |
| `POST /api/autofill/runs/{id}/end` `{status}` | `succeeded` when every frame ended, `cancelled` when the person stopped it, the engine failed or the page navigated away. Ending an ended run changes nothing. A run with no match, system-write or events call for 15 minutes is ended `cancelled` with reason `idle-timeout`; a run left open by a stopped server becomes `failed` (`interrupted`). |
| `GET /api/autofill/posting?url=` | `{title, location, text}` of a Lever or Ashby posting read from its public board, for a page that holds only the form (see The job a tab is about). |
| `GET /api/extension/build` | `{stamp, version}` of the extension embedded in this binary; `unavailable` with reason `extension-not-built` when it was not generated. |
| `POST /api/browser/sessions/checkin` `{sites: [{domain, cookies}]}` | Counts only. Replies `{sites, pending}`; `pending` is `{requestId, domains, expiresAt}` or null. |
| `POST /api/browser/sessions/imports` `{requestId, cookies, skipped, truncated}`, `POST /api/browser/sessions/pushes` `{site, cookies, truncated}` | Secret routes (the cookie values are never logged, stored by the API or returned). Reply with counts `{imported, failed, replaced, notInSource, truncated}`. While a run drives the apply window both are refused with `apply-run-live`, except the answer to the request that run queued for its own host; a refused answer leaves the request waiting. |

Dashboard routes of the same sync (admin class, `remote: denied`): `GET /api/browser/sessions` (the sites list with
the managed profile's and the everyday Chrome's cookie counts, the last check-in, the pending request, the last outcome
and import, and whether an undo is available; counts and times only), `POST /api/browser/sessions/sync-requests`
`{sites}` (202 with the pending request; `not-allowed`, `not-listed`, `apply-run-live`) and `POST
/api/browser/sessions/undo` (`{restored, failed}`; `no-snapshot`, `apply-run-live`, `busy`).

Sign-in sync stops at the browser: the API checks the request id, scope, forbidden domains and sizes, then hands the
cookies to a `SessionBrowser`, which `internal/browser` implements (`Sessions`; see `browser.md`, "Sign-in sync").
The apply chain asks for its own host through `Server.SyncOwnHost`, which waits at most 45 s and withdraws the request
when the wait ends. A server without a managed browser answers `unavailable` with reason `browser-unavailable`.

## The job a tab is about

`engine/src/capture.ts` holds the order and the page reading; `sw/fill.ts` keeps, per tab in session
storage, the person's pin, the anchor and the last fill's context. The order: pin; then the page (the live description
on a posting page that is not a form; else the anchor; else, on a form page, a description from JobPosting data or a
known description container, never the form's rendered text); then the last fill. Only a posting page that is not a
form, with at least 200 characters of description, writes the anchor, so a form never overwrites it with its own
text. The panel reads the page when it boots and again after the Analyze tab's capture, so a posting leaves its
anchor; a tab opened from it starts with the same anchor; an anchor older than 14 days is ignored. The Autofill tab
names the job and its source and has Pin this job / Unpin. The Answer tab, the Résumé tab and Edit with AI ask the
worker for the same context (`context.resolve`) and fall back to the page alone when the worker does not answer.

A Lever `/apply` or Ashby `/application` page opened directly holds only the form, so when nothing above gives a
description (or only the last fill does) the worker asks `GET /api/autofill/posting?url=<page without query>` once
(`sw/fill.ts` `boardAnchor`). The server reads the posting from the board's public API with the evaluation's
`evaluate.ATSReader` (the same read the apply chain makes for these pages: one request, at most 10 s, Lever and Ashby
addresses only, `validation_failed` for any other, `unavailable` with reason `board-unreadable` when the board does not
answer). The board's title, location and text become the tab's anchor with source `board` ("read from the job
board"), so the next reading uses it without asking again; the company stays the page's. A board the server could
not read is remembered for the tab and not asked again; an unreachable or unpaired server is asked again on the next
reading. Verified 2026-10-17 in a disposable headless Chrome for Testing paired to a served demo copy: a Palantir and
a Spotify Lever `/apply` page and a Notion Ashby `/application` page opened directly named their job from the board,
and the Lever forms were filled without a press (no Submit).

## Tabs and widgets

The panel has four tabs (Analyze, Answer, Résumé, Autofill). Page widgets start with the panel bundle in the top frame
(`src/widgets/main.ts`); a host switched off in the popup starts none of them. Sign-in, one-time code, sign-in link,
Edit with AI and the applied banner (only while it is shown) share one document observer and re-read the page once per
quiet burst of changes (400 ms for the banner, 500 ms for the rest).

| Piece | Where | What it does and what it never does |
|---|---|---|
| Analyze | `panel/analyze.ts`, `sw/analyze.ts` | Reads the page (`shared/capture.ts`: text capped at 200,000 characters, JSON-LD posting data, a detection label, a 5 s re-read while the page hydrates), shows an existing evaluation from `GET /api/applications/lookup` (Open report opens that report in the dashboard, `/reports?id=<n>`), else evaluates with `POST /api/evaluations` (Idempotency-Key) and follows `GET /api/runs/{id}/events`. A page whose own wording says it is closed, or that is not a posting, needs one more yes. Add to tracker is `POST /api/candidacies` without a status, one request at a time; a `duplicate_candidacy` answer (an evaluation's report already puts the posting in the tracker) reads as tracked. |
| Answer | `panel/answer.ts`, `sw/answers.ts` | Lists the questions the engine's scan finds; each free-text one has a card (Draft, Preview, Fill, Rewrite, Skip) over `POST /api/answers/compose`. Every textarea gets a card, however short (Edit with AI still skips boxes under a few lines). The draft is an editable box: Fill and Rewrite take the person's edited text, and the count follows it. A draft is written only by Fill, through the engine's writer with read-back, cut at a sentence boundary to the control's limit. One request per card, Cancel aborts it. A question with no card that needs the person offers Show (as in the Autofill tab) and Add to answer bank, which opens the dashboard's Answer Bank page (`/answer-bank`; the page takes no question to prefill). |
| Résumé | `panel/resume.ts`, `sw/resumes.ts` | `GET /api/resumes`, `POST /api/resumes/rank`, a per-tab pin in session storage (resolved by id, then by file name; a stale pin falls back to the ranking), and `GET /api/resumes/{id}/file` only when the engine's résumé binding asks (pin, else ranked first, else default). Bytes pass straight to the engine and are not stored. |
| Documents (Résumé tab) | `panel/resume.ts`, `sw/resumes.ts`, `shared/site.ts` `surfaceDocuments` | The report the tab works on: a page whose own lookup finds an evaluated report binds it to the tab (session storage `docs:<tabId>`); a tab the page opens (`webNavigation.onCreatedNavigationTarget`, the posting's Apply hop), or the same tab on another page, carries it as inherited. An inherited record shows only on a page with both a résumé upload and an identity field (email, name or phone), with "Not this job? Dismiss"; the page's own record is trusted. Write tailored résumé and Write cover letter start `POST /api/documents/resume` / `cover-letter` `{candidacyId}`; the documents are followed in the application. The application runs one document at a time for a job, so both buttons hold from the press until the run is no longer queued or running (`GET /api/runs/{id}` every 5 s; only its status leaves the worker), and the tab then says whether it was written. A run already working for the job (started in the dashboard) is refused by the server with reason `document-run-active` and `details.runId`; the worker hands that run back (`already`), and the tab says so in plain words, links the run in the dashboard and follows it like its own. |
| Point at a field | `widgets/picker.ts`, `report.field` | Armed from the Autofill tab. Consumes clicks while armed, resolves labels and wrappers to the control, refuses password and secret controls, cancels on Escape, Cancel, navigation and after 60 s, and sends one `user-reported` event with no value. |
| Edit with AI | `widgets/editai.ts` | A button at the corner of each free-text control on a supported or URL-heuristic application page (the address is read again on every refresh, so a single-page site that routes to its form gets the buttons); Generate is the only thing that composes; one step of Undo; closed shadow root. |
| Sign-in | `widgets/signin.ts`, `signin.resolve` | One inert button. On click: `POST /api/signin/resolve`, engine writers, a press guard (sign-in and, on registration, create-account words only; submit, apply, withdraw, delete and social labels refused), an 8 s verdict, one outcome. Registration ticks required agreement boxes only when the legal-declaration setting is on and never an optional opt-in. The password is a local variable of the run. The outcome is shown in the widget; no route receives it. |
| One-time code | `widgets/otp.ts`, `otp.latest` | A button per code input or row of single-digit boxes. Reads `GET /api/gmail/otp/latest` (mail from the 10 minutes before the click on, bounded by `fromDomain` to the site's known code sender as the apply chain's `codeSenders` lists it: ADP, Greenhouse, Workday; any sender elsewhere) after the click (or inside the 210 s window a sign-in click opens), every 1.5 s for 180 s; writes the code, shows sender domain and arrival time, Undo; never clicks the page and keeps the code in memory only. |
| Applied banner | `widgets/banner.ts`, `banner.get` | Shown when the tracker status is applied, responded, interview, offer or rejected, or the lookup carries a submitted ledger row (`submission`: the confirmed row over an unconfirmed one, then the most recent, `ledger.PriorFor`). The words never claim more than the record: "Applied by Applysmith agent on M/D/YY." for the apply chain's submission, "You applied to this job on M/D/YY." otherwise, "(unconfirmed)" for a click never confirmed, "(from your application ledger)" for a record the extension did not see being made. Dismissals are kept by the identity key the lookup returns (200 at most). |
| Submit guard | `widgets/guard.ts` | Only while a draft is pending and the panel is open: holds the person's own click on a submit control and offers Wait, Submit anyway and Skip the pending fields. |
| Submission recorder | `widgets/recorder.ts`, `sw/submissions.ts`, `mainworld/hook.ts`, `frame.ts`, `sw/widgets.ts` | Starts with the top frame's widgets, panel open or not. Once they run, `sw/widgets.ts` puts the recorder (`frame.js`) and the hook into every application iframe of the tab (registry frame, or a probed frame that qualifies; later frames on `webNavigation.onCompleted`), and a same-document address change (`onHistoryStateUpdated`) runs `loader.js` again, which does nothing where the panel already is; every install is idempotent. Observes the person's own trusted click (never prevents it), one entry per posting per 30 s with no query, hash or form value, confirmed within 20 s by a thank-you address or phrase, a 2xx POST, PUT or PATCH seen by the page-world hook to a path (never a host name) that reads like the submission once no submit control is left on the page (a bounced submit, an autosave or an upload answers 2xx too, so the call alone never confirms; on Ashby only the words do), or the next document's thank-you address; anything less stays unconfirmed. `POST /api/submissions`; a failed send waits in a queue of 50 for 30 days, one attempt per entry per minute. |
| Step walker | `widgets/easyapply.ts` | Started from the Autofill tab. Fills a step with the engine, presses only Continue, Next, Review or Save and continue, at most 8 times. After a press it waits 700 ms on LinkedIn (its modal swaps in place); on every other site it checks every 400 ms, for up to 8 s, until a step other than the pressed one shows a control. The final Submit control is not pressed while an Answer-tab draft is pending or any step's fill composed a free-text draft it left unwritten (`draft-pending`). It is pressed once, only when the person ticked the option for this walk (off by default, forgotten after the walk) and only after `POST /api/submissions/claims` returns a claim id. The claim runs the server's duplicate guard and press claim; a refusal, an error, an unreachable application, an unpaired extension or an answer without an id means no press, and the panel says why. The press is then reported through `POST /api/submissions` with the claim id, through the recorder's queue, unconfirmed, and watched for 20 s by the recorder's own confirmation rules (the recorder does not see the walker's untrusted click); a confirmation is the same entry and claim sent again with `confirmed`. Withdraw, Discard, Delete and the terminal Apply labels are never pressed. |
| Next step button | `widgets/nextstep.ts`, `panel/autofill.ts`, `sw/advance.ts`, `advance.ts` | In the Autofill tab, shown only while the page has a progress control (the walker's own finder, so Continue, Next, Review or Save and continue, or the textless Dojo pager) or an iframe that could hold the application form (captcha, analytics and video frames do not count). Disabled while a fill or a walk runs. One click presses once: the control in this frame, else `step.advance`, where the worker injects `advance.js` into each other http(s) frame, not the deny-listed ones, and stops at the first frame that pressed. The press is the walker's: a plain click, or the full pointer gesture for the pager, after the same never-label check, so it never presses Submit, Withdraw, Discard or Delete and widens nothing. |

The page-world hook is injected by the worker with `world: "MAIN"` on demand, wraps the page's own request functions as
a pass-through, posts only method, status and path to the page's own origin, and is the one source file allowed to name
those functions (the builder checks that it makes no request of its own).

## Fill state across a worker restart

The worker keeps one record per tab in `chrome.storage.session` (restricted to trusted contexts before the first write):
run id, host, page address without query, the posting's company, title and address (no job text), frame decisions, the
frame ids, the phase and, once the fill ended, the counts and the labels, outcomes and (top frame only) selectors of its rows. It holds no applicant
data and no written value. After a restart the record is read when the panel asks for the status or an engine binding
arrives; the engines in the pages keep running and their own status finishes the fill. When the fill ends, the top
frame's engine is handed the sub-frame decisions (`frameReport`): it logs them and returns one `frame-unreachable` event
per frame that could not be entered, which the worker uploads with the run. Learning events that were still
waiting for their upload and the map of what the system wrote are not kept, so an edit made after a restart is judged by
the server against the run's recorded system writes alone. A fill belongs to the document it was started on: the panel stamps its document with a random token in the page's
isolated world, the start message names it, the record keeps it, and the top frame's start refuses inside the page when
the token is not that document's (the browser's own frame and document ids can still name the old page for a moment
while a navigation commits, so they are not trusted). When the top frame commits another document, or the worker reads
back a record whose document no longer carries the token, the run ends `cancelled` once and the record becomes a stopped
result saying the page changed, which the panel of the new page shows until it is dismissed. A finished result is dropped
when the top frame navigates, as is any record when the tab closes or the person dismisses it.

## Frames

An unknown sub-frame is counted by `probe.js`, a small script that runs the engine's own control count and changes
nothing in the page. The engine is injected only into frames the registry names, the top frame, and frames whose count
qualifies (two controls, or one the page marks required). A refused probe is `failed-probe`, a refused engine injection
`failed-inject`; only failed frames make the panel say a part of the page could not be reached.

## Server routes the widgets call

| Route | State |
|---|---|
| `GET /api/applications/lookup`, `GET /api/reports/{n}`, `POST /api/evaluations`, `GET /api/evaluations/{id}`, `GET /api/runs/{id}/events`, `POST /api/candidacies`, `POST /api/answers/compose`, `POST /api/answer-bank/events` | built |
| `POST /api/documents/resume`, `POST /api/documents/cover-letter` `{candidacyId}` (Idempotency-Key), `GET /api/runs/{id}` | built (application-documents): the Documents section's two buttons; `202` with a run id, whose status holds them. |
| `GET /api/resumes`, `GET /api/resumes/{id}/file`, `POST /api/resumes/rank` | built (application-documents). The roster is `{resumes: [{id, label, tags, default, description, fileName, size}]}`; the file route serves only the roster's PDFs from `resumes/` (PDF signature, at most 10 MiB, no link out of the workspace) with `Content-Type: application/pdf`, and the worker refuses a body of any other type. Rank takes `{title, company, description}` (description at most 20,000 characters) and answers `{chosen, ranking: [{id, score, reason}]}` from a deterministic word match (no model); the company does not score. |
| `POST /api/submissions` `{entryId, url, host, company, role, title, score, reportNumber, observedAt, confirmed, claimId}` | built (apply-chain, "user-observed submission"). Strict body, no form value; answers `{ledgerId, result, tracker}`; a repeated `entryId` or a posting with a confirmed row attaches instead of adding. Ledger: `internal/ledger`, table `submissions`. |
| `POST /api/submissions/claims` `{url, host, company, role}` | built (`internal/api/submissions.go`, `ledgerClaimer`). Once the domain is ready the claim runs the duplicate guard and the press claim over the submissions ledger (`ledger.Claim`) and answers `{claimId, expiresAt}`; a posting the guard blocks answers `conflict` with the reason `duplicate_submit_blocked`, and a ledger that cannot be read, or a server whose domain is not ready, answers `unavailable` with `claim_unavailable` (not retryable). Anything but a grant means no press. |
| `POST /api/signin/resolve` `{url}` to `{email, password, source}` | served (credentials-vault): the paired token on loopback only, the site override else the shared default for the page's exact host. `not_found` with `secret_not_found`, `unavailable` with `keychain_unavailable` or `keychain_denied`, and `conflict` with `signin_held` while the host's passwords are on hold for the day (the widget reports `signin-held`, shows the application's message and types nothing). |
| `GET /api/gmail/otp/latest?since=&fromDomain=` to `{code, receivedAt, from}` | built (gmail-integration, `internal/api/gmailapi/gmail.go`). The server makes its own bounded Gmail lookup, newest first, strictly newer than `since`, and answers `not_found` when no mail qualifies (the widget polls on it), `bad_request` for a bad `since` or `fromDomain`, and `unavailable` with the reason `gmail_not_signed_in` (not connected, or Gmail refuses the app password; not retryable) or `gmail_unreachable` (retryable). The widget shows the message of any error. |

A route that is not served yet answers `unavailable` with its own reason, and its widget shows that; nothing is faked.

## Real-browser check

`make test-browser` includes `internal/extension` (build tag `browser`). It loads the built extension unpacked into
Chrome for Testing with a throwaway profile (`applysmith-browser-test-ext-*`), because branded Chrome 137 and newer ignores
`--load-extension`. Set `APPLYSMITH_TEST_CHROMIUM` to a Chrome for Testing binary, or install one with Playwright
(`~/Library/Caches/ms-playwright/chromium-*`); the test skips when it finds none. A stub API on a random loopback port
stands in for the application, with the same checks the real one makes (bearer token, `Idempotency-Key` on the run
request, issued run id, strict bodies for the run end and the sign-in sync, lowercase event ids). It is not the real
server because `internal/api` imports this package, so the routes themselves are tested in `internal/api`. The test pairs in the popup, checks that a content script
cannot read extension storage, opens the panel, fills a synthetic form (names and email from the profile, a select from
the matcher), checks that Submit was never pressed, types into fields, checks the reported events and that the run
ended `succeeded`, and pushes a site's cookies. The panel opens on the Analyze tab, so the test selects the Autofill tab
before it looks for Start. It then stops the service worker for real (`ServiceWorker.stopAllWorkers`) and asks the
restarted one for the tab's fill, which must still be `done` with the same run id from session storage. Last, it
starts the step walker with the submit option on; the stub refuses the press claim with a 409, and the test checks the
panel shows the reason and says Submit was not pressed, the claim carried a URL without query, and the page was never
submitted. A loaded machine makes the run slow, so the browser context allows 200 s inside the 300 s test timeout. The other
tests in `browser_flows_test.go` each start their own Chrome and are listed under the manual checklist.

## Manual checklist (everyday Chrome)

Most of the original checklist now runs in `make test-browser` against Chrome for Testing with a throwaway profile
(tests in `internal/extension/browser_test.go` and `browser_flows_test.go`, Chrome and helpers in
`browser_harness_test.go`). The tests also stand in for the person: a fixture host mapped to a registry host
(`job-boards.greenhouse.io`) makes the panel open by itself, a frame from another origin plays an embedded form, and a
matcher request held at the stub keeps a fill in flight. What each check covers:

| # | Item | Covered by |
|---|---|---|
| 1 | The card's version is `0.1.0+<stamp>` and the stamp equals the popup's. The extensions page shows the manifest's `version_name`. | `TestBrowser_PairingMessagesVersionRotationAndOffline` |
| 2 | Not paired, then Connected after the code; a wrong code says wrong, expired or used; the sixth try shows the wait. | `TestBrowser_PairingMessagesVersionRotationAndOffline` |
| 3 | The launcher, the single automatic open without taking focus (and none after a reload), the launcher taking focus, the shortcut's toggle path, "Turn off on this host" (no panel, no automatic open, the explicit open still works, no launcher). | `TestBrowser_LauncherAutoOpenOnceAndSwitchOff` |
| 4 | Analyze first, arrow keys between tabs, a plain message in the Answer and Résumé tabs, Start fills and nothing is submitted. | `TestBrowser_PairAndFillASyntheticPage` |
| 5 | A fill in flight survives a stopped service worker: Pause, Resume and Stop reach it and the result appears. | `TestBrowser_FillSurvivesAStoppedWorkerForPauseResumeAndStop` (the idle-fill read-back is also in `TestBrowser_PairAndFillASyntheticPage`) |
| 6 | The walk without the submit option stops before Submit; with it, the refused press claim is explained and nothing is pressed. | `TestBrowser_PairAndFillASyntheticPage` |
| 7 | A typed value reaches the manual-fill events after the two quiet seconds; a password, a one-time code and a self-identification field never do. | `TestBrowser_PairAndFillASyntheticPage` |
| 8 | Quit the application: the popup shows Offline with the address; Start is gone from the panel. | `TestBrowser_PairingMessagesVersionRotationAndOffline` |
| 9 | After a token rotation the next request drops the pairing, the popup asks to pair, and the new code pairs again; after a port move the Offline popup takes a `<port>-<code>` code, connects and saves that address. | `TestBrowser_PairingMessagesVersionRotationAndOffline` |
| 10 | Next step in the panel presses an embedded form's Next once, in the frame that owns it, and never Submit. | `TestBrowser_NextStepPressesTheEmbeddedFormOnce` |
| 11 | Reloading the page while a fill runs ends it `cancelled`, the new page's panel says so, and nothing is written into the new document; a reload while the fill is still starting issues no run. | `TestBrowser_ReloadDuringAFillEndsItCancelled`, `TestBrowser_ReloadWhileTheFillStartsWritesNothing`, `TestBrowser_ReloadWhileTheRunIsIssuedCancelsItAndWritesNothing`, `TestBrowser_StartHandledAfterTheReloadCommitsIsDropped` |

Live check in a branded Google Chrome (2026-10-12, throwaway profile, driven by
computer use with screenshots; fictional profile; the posting host mapped to a
local fixture with `--host-resolver-rules`; builds cc0e52aac6fcdf94 and
c24b790c641d9f2b):

- Load unpacked: the card shows `0.1.0+<stamp>`, equal to the manifest and the popup. Pass.
- Popup: a wrong code is refused with its message; the right code connects and both stamps match. Pass.
- The panel opens by itself once on a supported host, and not after a reload. Pass.
- The real Alt+Shift+C closes the panel to the launcher and reopens it. Pass.
- Fill: the profile fields are written, the unanswered question is listed under Needs you, and Submit is not pressed. Pass.
- A manual edit reaches the manual fills list as ground truth with its run id. Pass.
- Offline after the application stops, with the address. Pass.
- Reload while the fill starts: the first build refilled the new page (a run ended `succeeded`). After the fix the new page stays empty and the run ends `cancelled`. Pass.
- Settings, Browser extension: Pair the extension shows a code and the shipped build. Pass.

Still owner, live:

- Reloading the extension from `chrome://extensions` while a fill runs ends the fill.
- A real posting on a supported application system with a real account: the fill on a live form and the entries in the answer bank's manual fills list.
