# Engine

The in-page autofill engine. TypeScript, no npm runtime dependencies. Written
clean-room from `openspec/changes/migrate-to-go-mygo/specs/autofill-engine/spec.md`
and its parity file (provenance row CR-ENGINE); every source file starts with the
`Clean-room:` header the repository guard checks.

    cd engine
    bun install --frozen-lockfile
    bun run test         # vitest + happy-dom, synthetic fixtures only
    bun run typecheck    # tsc --noEmit

`make verify` runs both after the dashboard typecheck.

## Shape

One engine instance serves one document. A host (Go over the debugging
protocol, or the extension) drives it through the page API in `src/engine.ts`
and answers three bindings (`src/protocol.ts`, `src/host.ts`):

| Binding | Constant | Call | Reply |
|---|---|---|---|
| match | `BINDINGS.match` | `MatchRequest` | `MatchResponse` |
| resume | `BINDINGS.resume` | `ResumeRequest` | `ResumePayload` or null |
| terminal | `BINDINGS.terminal` | `TerminalNotice` | nothing |

A binding is called with one JSON string and resolves to one JSON string, a
`BindingReply`. Every request carries the run id. The bundle entry
(`src/entry.ts`) installs the engine as a non-enumerable global named
`__applysmithEngine`, disposing an older instance first. The build step sets the
content-hash stamp with `setStamp` after bundling; `status()` reports it.

## Source map

| File | Responsibility |
|---|---|
| `engine.ts`, `run.ts` | page API, run lifecycle, cancellation, pause, deadline, diagnostic log |
| `pipeline.ts`, `stage-*.ts` | stages in order: detect, resume, question pass, profile pass, apply loop, blur, summary |
| `scan.ts`, `labels.ts`, `selector.ts`, `visibility.ts`, `sensitive.ts` | discovery, question text, required flag, selectors, concealment, never-touched controls |
| `writers.ts`, `apply.ts`, `guards.ts`, `dates.ts`, `phone.ts`, `states.ts`, `reader.ts` | native writers, read-back, overwrite decisions, value shapes |
| `detect.ts`, `frames.ts` | application-form detection, frame registry, deny-list, probe and decisions; the events for frames not reached |
| `click-guard.ts`, `guard-text.ts`, `submit-hold.ts` | the click guard (label reading, the folded word rules shared with the main-world helper, the one editor Save) and the native-submission hold |
| `profile.ts` | profile-field recognition |
| `widgets.ts`, `gestures.ts` | which driver a control needs (read from its markup, never the host); the pointer sequence, key presses, typing one key at a time |
| `popup.ts`, `committed.ts`, `placeholder.ts` | the popup that belongs to a trigger and its options; what a widget shows as its committed value; the placeholder vocabulary |
| `listbox.ts`, `choosers.ts`, `prescan.ts` | the listbox open, search, score and commit ladder; which option answers (score, geo, address, phone country); reading a closed listbox without choosing (a referral-source menu of categories read whole as "Category > Choice", and the required listboxes no lane claimed, read at the summary) |
| `select2.ts`, `libraries.ts`, `calendar.ts`, `places.ts`, `phone-country.ts` | Select2, Dojo dijit, Stencil sdf-select-simple, typeahead.js, intl-tel-input, TinyMCE (the hidden textarea reached through its editor, written through the helper), day-picker and date segments, places autocomplete, the unlabeled phone country picker |
| `toggles.ts`, `consent-sweep.ts` | choices made of buttons and ARIA widgets (Yes/No pills, role=radio, switches); the sweep over acknowledgement boxes |
| `cascade.ts`, `stage-cascade.ts`, `restore.ts`, `fragment.ts` | write order and the cascade-completion pass; the user-edit tracker and the restore pass; the redacted control snapshot |
| `refill.ts`, `background.ts`, `watchdog.ts` | form-replaced refill, identity refill, tail guard; the stop flag work after the summary shares; the stall watchdog |
| `sites.ts`, `capture.ts`, `reveal.ts`, `easyapply.ts` | the site table and job-page heuristic; the posting read from the page (description, company, title, place); the embedded form behind an overview; the LinkedIn dialog as the scan root |
| `workday.ts`, `oracle.ts`, `successfactors.ts`, `icims.ts` | the enterprise drivers: search prompts, date triple and menus; grid comboboxes and the address master; collapsed panels and the helper upload; hidden-select dropdowns |
| `lever.ts`, `ashby.ts`, `sr.ts`, `reactwidgets.ts` | the board and HR-suite drivers that need their own markup rules or the helper: the hidden-field location, the save queue's gate, component methods, react-widgets and Paylocity handlers |
| `srcards.ts` | SmartRecruiters history cards: the parse's entries replaced by one card per profile entry through the components (the entry editor's own Save is pressed there), single flight per document, and the late-parse sweep |
| `rows.ts`, `row-plan.ts`, `sections.ts`, `stale.ts` | repeated rows: where a control's row is read from, which entry it answers from, the add loop, the guarded remover, the parser-row cleanup and its sweep; stale prose |
| `learning.ts`, `corrections.ts`, `capture-filter.ts` | uncertainty events (and the observe-only `already-satisfied` record of a required control the page answered while the run wrote nothing), the watch over what the person changes, and the rules that drop a capture (shared with Go through `test/capture-vectors.json`) |
| `mainworld-client.ts`, `flight.ts`, `mainworld/` | the core's end of the channel to the main-world helper (nonce-named events carrying strings); single flight; the helper itself (submit observer, Ashby queue, uploader hook, page-object calls, the `sdf.commit` fallback for an sdf-select whose option takes no click, and `tinymce.set`, a TinyMCE editor's content from plain text), built to its own bundle under 32 KiB |

## Built in this phase

Run lifecycle (start, status, stop, pause, resume, scoped stop, page deadline
and stage and write budgets without paused time, unwinding refusal, terminal notification), bindings
contract, batching and size bounds, field discovery with shadow-root piercing,
label, required and section resolution, selectors, visibility and decoy
skipping, sensitive-control exclusion, native writers (text, textarea,
select, radio, checkbox, content-editable, file hand-off), framework-safe
setting with read-back, value-shape guards, overwrite rules, free-text review,
résumé-first with parse settle, frame decisions and probe, form detection,
click guard, submit hold, inspect, and a minimal profile pass.

Widget drivers (task 3.8b): the custom listbox ladder with popup ownership
and read-back, typeahead, geo and address autocomplete, places, phone country
pickers, Select2, react-select, ARIA 1.1 wrappers, tag inputs, Dojo, Stencil,
typeahead.js, intl-tel-input, day-pickers, switches and button choices, the
consent sweep with its early and late ticks, the cascade-completion pass,
dependent native selects, the restore pass with the user-edit tracker, the
refill stage (extra fill passes when the page swapped the block; identity
refill, a state or a country only on a plain control and a country only when
no state, city or postal code holds a value), the tail guard and the stall watchdog.

Application-system drivers (task 3.8c): the site table (37 patterns), the job-page heuristic, the embedded-form
reveal, the wizard advance test and the page posting read (description as the longer of the page text and the
structured description, company, title, place); Workday search prompts (scoped to their own widget, results waited
for, unambiguous picks, wrong pills replaced, skills as serial chips, responsive Done, phone code by country),
the questionnaire date as one question typed per segment, and hierarchical menus drilled two levels; Oracle grid
comboboxes (hover then click, reversible option read, re-seeding), the address master and its cascade, country
first; SuccessFactors expand-all, parser junk rows and the uploader reached through the helper; iCIMS hidden-select
dropdowns; Dayforce re-render retry and rows; ADP phone binding; Taleo with a listing of what the generic passes
left; Ashby (save queue in the page's world, echo gate, `ashby-save-lost`, `savedFormGaps`, the repair round,
datepicker dates); Lever location, JazzHR checkbox pair, Breezy and Paylocity row cleanup, SmartRecruiters
component writes with single flight, react-widgets and Paylocity handlers; repeated rows with entry binding;
the LinkedIn Easy Apply dialog scope; uncertainty events and the capture of the person's own edits as manual fills; stale prose and
state refill on plain controls.

## Host side

`internal/engine` (Go) injects the bundle into the isolated world `applysmith-engine` of each admitted frame, drives
`start`, `status` and `stop`, serves the three bindings and walks the sub-frames. Besides the page API above it uses
three read-only helpers: `probe()` (control count of a frame), `decideFrame(request)` (the registry, deny-list and
probe rules for one frame, so Go carries no copy of them) and `locate(selector)` (the element a selector names, for the
native file set).

With `FillOptions.MainWorld` it also evaluates the main-world helper into each admitted frame (the bundle in
`generated/mainworld.js`, its nonce and stamp filled in) and tells the engine the nonce with `useHelper`. The helper
holds no profile data and no binding. `Page.ObserveSubmissions` registers the pass-through submit observer at document
start of the tab (`Hooks.AddInitScript`), `Page.SubmitObservations` reads what it saw, and `Page.DrainEvents` takes the
learning events the engine of each document of the tab collected after the fill (`ErrNoEngine` once none holds it). The engine also answers `savedFormGaps`, `refreshSavedFormGaps`,
`repairSavedForm` (the Ashby repair round) and `site()`.

## Later

| Piece | Task |
|---|---|
| Oracle work and education editor rows: the timeline add buttons open an editor and its footer Save press ends it; the engine presses no Save of an Oracle editor (it presses Save only through `pressEditorSave`, inside a SmartRecruiters entry editor), so the page walk fills and saves them (parity ENT-68) | 4.4 |
| Handoff recorder for a kept tab | 4.6 |

Known partial rows (the parity rows whose Disposition still says "not yet covered"), each stated in its parity Disposition column: a digits-only text box whose markup does not say so (ENT-19),
and options that carry only aria-labels (BRD-106).

Question text ladder, last rungs: a text-like box with no label, title or `aria-label` reads its own placeholder
(never a sample value, instruction, menu prompt or combobox hint; it does not exempt a faded box from the decoy
rule). A live region or screen-reader-only node in an `aria-labelledby` list is left out when another id says
something, and a wrapping label with no words of its own is read through its own `aria-labelledby`. A lone
checkbox reads its own label after the container title. A résumé input in a menu the page keeps closed is filled
only when no résumé slot is shown and a visible résumé menu button (`aria-haspopup`) stands for it.

Live-survey writer fixes (demo persona, one run per ATS): a select whose option value carries a framework type prefix ("string:NC") or whose text sits only in the `label` attribute is matched by the state code or name; a BambooHR Fabric select (a `button[data-menu-id]` with a bare `aria-haspopup`, its label pointing at the hidden select) is a listbox trigger; a phone box beside a country-code picker takes the national digits and a digits-only reformat of a phone box (`inputmode="tel"`) counts as held; an address box that is the whole address (label "Address", not "Address Line 1") falls back to the applicant city and state when the street finds no suggestion; Paylocity rows (`workHistory.<field>.<n>`) are rows of their section and an Add that mounts no recognised row is the last press, for later runs on the same page too; a label element alone in its field names the box, so a parser host's different guess in an identity box is replaced from the profile, including a box the page fills mid-run.

Second live-survey pass (required controls left unanswered): a slot the page shows a document in (a file name beside a hidden input) is a held row, never an empty required box; the SuccessFactors uploader is a form of the file alone, not any form named `form_...` (JazzHR), and a closed upload behind a visible "Attach resume" opener is opened once and filled; a custom question whose box says only "Select" in its `aria-label` reads the title above its styling wrappers, and the list the box has open is no other control; an intl-tel-input dropdown search or a labelled address search beside the phone box is no country picker, so the international placeholder gets the +1 number; a street box whose search lists nothing for the street keeps the street typed.
