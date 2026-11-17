# Setup and doctor

`internal/onboarding` holds the step catalog, the completion detectors, the
step writers, the interactive flow and the doctor. `internal/cli` wires them to
`applysmith setup`, `applysmith llm`, `applysmith resumes` and `applysmith doctor`, and
`internal/launch` hands the same catalog to the server as `api.SetupService`
(`service.go`), which the wizard's routes call ([api.md](api.md)).

## One catalog

`Catalog()` lists the 18 steps (0 to 17) once, with title, phase, required
flag, purpose, skip consequence, storage, input schema, commands, guide path,
detector and writer. The interactive flow, `setup status`, `setup next`,
`setup schema` and the doctor all read it, so none carries a step list of its
own. The catalog test's `ValidateCatalog` fails when an available step lacks a
command, a guide path, a schema, a writer or a doctor check.

Every step has a detector, a writer, commands, a guide path and a doctor check
with its id; `ValidateCatalog` fails otherwise, and a step whose writer takes no
input says so (`Inputless`). Progress is the satisfied required steps over 12.
`TestCatalogIsCoveredByTheCommandLineTheWizardAndTheDoctor` lists the ids from
`setup status`, from the wizard's state endpoint and from the doctor, and
requires every leaf the specification names.

The CV comes before identity (decision 2026-10-23, as the original asked). Once
a CV is saved, the wizard's identity step and Profile, Identity offer the name,
email, phone, place and links read from `cv.md` (`cvparse.Identity`, served as
the CV draft's `identity`) for the fields that are still empty; a suggestion
only fills the form, per field or all at once, and Save identity stores it.
`applysmith setup` asks an empty identity question with the CV's value as its
default; a stored value stays the default. A reason whose value is simply
missing carries `blank`, so the identity, work authorization and targets screens
name those fields once by the form's own labels; the terminal keeps key paths.

| Step | Writer input | Detector reads |
|---|---|---|
| 6 `story` | `narrative`, `voice` | `narrative.md` (300 characters, no placeholder); `voice.md` is advisory (500 characters) and its skip is `skip story.voice` |
| 7 `resumes` | `roster`, or `add` (a local PDF) | the roster in `profile.yml`: each file exists, is a PDF of at most 10 MiB, one default. Rendering readiness is a separate subcheck |
| 8 `answer-bank` | `answers` (id to answer or `@ask-me`), `confirm_review` | the blocking entries of the stamped bank, the same list the Answer Bank page counts (the values steps 2 to 5 state answer their entries at load and never block: [answer-bank.md](answer-bank.md) "Setup statements"), and the defaults flagged for review. Applying writes only the given answers and the review confirmation |
| 10 `browser` | none: runs the fingerprint check | the stored check of the apply and scan windows: attached, `navigator.webdriver` false, no headless user agent |
| 11 `gmail` | none (a person acts) | an app password in the keychain for the stored address, and Gmail has not refused it |
| 12 `telegram`, 14 `captcha`, 16 `extension` | none (a person acts) | the bot token and paired chat; the 2Captcha key; the extension's first authenticated check-in |
| 13 `credentials` | none | the shared default sign-in, or Sign in with Google turned on (the answer-bank runtime setting `allowGoogleSignIn`) |
| 15 `discovery` | `pack`, `enable_edgar`, `acknowledge_edgar_disclosure`, `seed_registry`, `use_target_roles`, `enable_boards` | portals and the chosen pack (the boards needing no key or sign-in, when asked, planned before the target roles and the pack so both cover them); EDGAR stays off until the disclosure is acknowledged and the profile has an email |
| 17 `orientation` | none | the acknowledgement |

A step that only a person can finish (a Gmail app password, Telegram, 2Captcha, the
extension, the apply window without a browser) answers its writer with exit
code 3 / `needs_person` and the instruction; the wizard and `setup next` show
the same text and the page that hosts the feature.

## Detectors and decisions

A detector reads the stored data and returns `missing`, `template`, `partial`
or `complete` with one reason per outstanding field. It never writes. The
status every lane shows is `Evaluate`, which applies the stored decisions:

| Decision | Key in `workspace_meta` | Meaning |
|---|---|---|
| skip | `setup.skip.<step>` | an optional step is skipped |
| acknowledgement | `setup.ack.<step>` | persona values (for example the name "Alex Example") are the user's own |
| review | `setup.review.<step>` | the extracted CV draft was reviewed, or the batch skip list was confirmed |
| secret saved | `setup.secret-saved.<secret name>` | when a key was saved; the value is never stored here |

An acknowledgement and a review hold the hash of the data they were given for
(`Step.AckScope`) and stop applying when it changes. Seed text, placeholders,
reserved-domain emails and fictional phone numbers are never acknowledgeable.

## Writers and exit codes

`applysmith setup apply <step> --file <path|->` calls the step's writer: it
decodes JSON, rejects unknown and mistyped fields per field, validates through
the workspace schema, writes through the revisioned workspace write path and
returns the new status. A field that is not supplied keeps its stored value.
`--dry-run` validates and writes nothing; a dry run of `resumes` `add` checks
the source PDF, the id and the roster it would make, everything but the copy
itself. `resumes` `add` reads the local PDF within 15 seconds: on macOS a file
in a protected folder (Desktop, Documents, Downloads, iCloud Drive) waits while
the system asks whether Applysmith may open it, so a read that is held or refused
fails on `add.path` and names System Settings › Privacy & Security › Files and
Folders, or choosing a file elsewhere. The dashboard's upload sends the bytes
the file picker read and has no path to read. Every writer edits
`profile.yml` in place by key path (`workspace.ApplyOps`), keeping comments
and order. YAML anchors and aliases (PyYAML writes `&id001` / `*id001` for a
list that appears twice) stay as they are beside an edit; an edit of a shared
value, or one that would drop an anchor still in use, first writes every alias
out as a copy of its value, which is refused (`edit_unsupported`, naming the
key) unless the decoded data stays identical. An edit inside a flow collection
(`companies: [{name: A}, ...]`, `title: {positive: [x]}`) first rewrites the
outermost one in block layout, as a whole-value write would; an append or
remove on a flow list of mappings does too, while a flow list of scalars takes
it in place (`[a, b, c]`). `applysmith setup` with no arguments
asks the questions and calls the same writers; it needs a terminal, or
`--answers <file|->` with one answer per line. The commands that write take
the workspace lock and never go through a running server's API: while the app
or `applysmith serve` holds the workspace they write nothing and exit 4 naming its
setup wizard, which runs the same writers.

Exit codes of the setup commands: 0 done, 1 unexpected, 2 rejected input,
3 a person must act, 4 environment failure.

## Language-model step

`setup apply llm` takes one of `preset`, `cli` or `offline`. Offline mode is the
"no key yet" choice: `llm_settings.offline` routes every task to the fake
provider; the step counts as done and its warning says that evaluations and
written answers are simulated and that real evaluation needs a model. The step completes only when the latest `llm test` succeeded and the
provider, preset or offline setting has not changed since
(`llm_settings.updated_at` against the saved test time). The saved test holds
provider, model, outcome, error class, latency, time and cost, no prompt and no
reply. A reply cut at the 16-token ceiling counts as a success because the key,
the model and the route worked.
The wizard's screen opens on the stored choice (the routing view's `preset`
and `offline`), so "Save and test" repeats a failed test with one press, and
the step's reasons name that button instead of `applysmith llm test`.

## Doctor

Every leaf is `<step-id>.<name>` or in the groups `runtime` and `agent`;
`Checks()` returns them in catalog order. A leaf of an optional step the user
skipped reports `skipped`, and a failing leaf of an optional step is a warning.
`story.voice` and `browser.signins` are never required. `browser.apply-window`
and `browser.fingerprint` read the stored check, or with `--live` run it once
for both under a shared 45-second budget; `gmail.fetch` runs only with
`--live`. The wizard's "Check everything" runs the same leaves offline.
A result's `fix` is worded for the command line; a leaf whose group is a
catalog step also carries `step` (set once in `runOne`), and the dashboard
renders that as a link to the step instead of the command. Only a leaf no step
fixes (`runtime.*`, `agent.pi`) shows its `fix` text there, so the doctor's
own lines (a leaf that timed out or crashed, the keychain probe's re-run) are
worded for the dashboard when it runs them (`Env.InServer`). The text report
aligns the ids and ends with the count of required failures and warnings.

## Résumé rendering leaf

`applysmith doctor --only resumes.toolchain` runs a read-only, never
required leaf over `internal/documents/toolchain`, beside `resumes.files`. It is
a warning, with the exact missing components, the fix for each and the install
sizes of what the user installs outside the app (TeX, Poppler, Python), when new
PDFs cannot be generated; the wizard shows the same through
`GET /api/documents/toolchain`, apart from the roster. The résumé step itself
needs only an uploaded résumé. The explicit actions are
`applysmith documents toolchain setup --yes` and `applysmith documents toolchain
smoke` ([document-toolchain.md](document-toolchain.md)).

## Optional multi-document evidence import

`internal/intake` compares up to 20 selected `.md` or `.txt` documents with
`profile.yml` and writes nothing; `internal/onboarding/intake.go` reads the
profile, calls it and writes through `editProfileAt`, the same revisioned edit
the CV step uses. The CV step itself is unchanged and stays the minimal path.

- **Parsing.** Documents of kind `cv`, `profile-export` and `education-record`
  go through `cvparse.ParseLocated` (the setup's own CV parser, now also
  reporting the line and heading of each entry). Kinds `project`, `reference`,
  `course` and `repository` are signal-only: `signals.go` finds technology names
  (a short case-sensitive list, plus items under a skills, tools or stack
  heading) and each becomes a question.
- **Change set.** Entries from all documents are grouped (same organisation and
  role; same organisation and the anchor date; a different organisation with the
  same role and both dates). Per group and field the profile value and the
  documents' values are merged where they agree (a bare year agrees with any
  month of it). More than one value left is a conflict, with every side and its
  sources; none left and a profile entry is a match; none left and no profile
  entry is an addition. A field the profile leaves blank is not filled from a
  document. Modification time is not an input.
- **Sources.** Each source holds the document label, the SHA-256 of its text,
  `line N, under "<heading>"` and `direct` or `inferred`. Identical text in one
  batch is read once (`duplicate` on the document).
- **Acceptance.** `Select` takes the person's selections: an addition (a bare
  year needs `months`), a conflict (`choices` for every field, `keep` for the
  profile's value; no default), a signal (`confirm`). A match, an item outside
  the set and an unconfirmed signal are refused per field. Unselected items,
  conflicts included, change nothing. The profile edit is `append` for new
  entries and `set` on `history.*[i].<field>` for a chosen side.
- **Revision.** The request carries the `profile.yml` revision the preview
  reported and the documents again; the change set is rebuilt from them. A
  different revision is `StaleError` (`revision_conflict`, exit code 2) and
  writes nothing, because list positions in the edit would no longer mean the
  same entries.
- **Idempotence.** Accepted facts are in the profile, so a second import shows
  them as matches and an acceptance with no selection writes nothing. No
  separate ledger of imported hashes is kept.
- **Never evidence.** Generated drafts and reports are refused by path in
  `LoadIntakeFiles` (the workspace's `documents`, `reports` and state folders).
  The import never touches `cv.md`, the answer bank or published knowledge; a
  confirmed signal becomes a profile skill with no years or level.
- **Interfaces.** `POST /api/setup/intake/preview` and `/accept` (standard
  access, remote denied, 4 MiB JSON body, `IntakeRequest`/`IntakePreview` and
  `IntakeAcceptRequest`/`IntakeResult`), `applysmith setup intake preview|apply`,
  and the "Add more documents (optional)" section of the Setup CV step and of
  the Profile CV tab (`web/src/pages/intake`). The interactive `applysmith setup`
  flow does not ask for it.

## Demo workspace

The demo (onboarding-setup "Demo workspace with a fictional identity") is a
workspace of its own, by default `~/Applysmith-Demo`, seeded with the shipped
example persona and nothing else. It is both the user tutorial
([tutorial.md](../user-guide/tutorial.md)) and the identity of live dry-run
checks.

- **Content.** `internal/demo` embeds the persona: `profile.yml`, `cv.md`,
  `narrative.md`, `voice.md` and the answers to the bank questions the profile
  does not answer (`answers.json`), all for Alex Example (`alex@example.com`,
  `+1 919 555 0142`, Northwind Labs, Contoso Cloud, Fabrikam Robotics, Example
  University). It also holds three sample postings of fictional companies on
  `.example` hosts and the sample application form. The persona's résumé PDF is
  laid out from `cv.md` at init (`demo.ResumePDF`: one US Letter page in
  Helvetica, no dependency, same bytes every time), so no binary is committed.
- **Init.** `applysmith demo init [--workspace <dir>]` and the Setup page's "Try
  the demo" (`POST /api/setup/demo`, session-only, remote denied) run
  `onboarding.PrepareDemo` then `SeedDemo`. The folder is resolved with the
  workspace rules, but never from `APPLYSMITH_WORKSPACE` or the recorded
  workspace. A folder that holds a workspace without the marker, or that sits
  inside one, is refused with `demo_not_over_workspace` and nothing is written
  (exit code 2 at the command line). Otherwise the marker is written first,
  `Init` seeds `portals.yml` and `policy.yml`, and each persona file replaces
  only a template or a missing file, so a repeat never overwrites an edit.
  `SeedDemo` admits the postings through `discovery.Repo.AdmitPosting` (source
  `demo`, the scan's own canonicalization, duplicate and blocked-company
  checks) and fills the bank through the answer-bank step's writer: the
  persona's answers, `@ask-me` for any other blocking entry, and the reviewed
  defaults confirmed. A repeat admits nothing twice.
- **Marker.** `.applysmith/demo-workspace` in the state directory, beside the
  workspace identifier. No edited file holds it, so no config route, setup
  writer or command reaches it; `workspace` offers `MarkDemo` and no way to
  remove it, and any file under the name counts. `store.Demo` reads it from the
  database's own state directory on every call, which is how the gates see it
  ([gates.md](gates.md#the-demo-workspace)).
- **Mail.** The demo reads mail for one purpose: a site's emailed
  verification code, when the persona's address is a plus alias of the owner's
  own connected Gmail account (owner decision 2026-10-13; rules in
  [gates.md](gates.md#the-demo-workspace)). `applysmith gmail connect`, `test`
  and `status` work with `--workspace` the demo (the test
  counts message ids and fetches none); `applysmith demo email <address>` sets the
  persona's address in the demo's profile after checking it is a plus alias
  (and of the connected account, when one is connected). The shipped persona
  keeps `alex@example.com`. Nothing else about Gmail runs in the demo.
- **Surfaces.** `GET /api/setup/status` returns `{demo}`; the dashboard shows a
  banner on every page (`web/src/ui/DemoBanner.tsx`) and the Setup page shows a
  short tutorial in place of "Try the demo"; every command that resolves a
  workspace prints the demo line to standard error.
- **Setup in the demo.** `Evaluate` drops the reasons that only say a value is
  the persona's (persona name, employer or school, reserved-domain email,
  fictional phone); a step left with no reason is `complete` with the reason
  "demo persona". Seed text, placeholders and every other reason still count.
  The workspace step never records the demo as the user's workspace. The
  provider step is the normal one; its routing lives in the demo's database
  and its key in the keychain account of the demo's workspace identifier.
- **Opening it.** Owner decision 2026-10-20: the demo is switchable to and from
  the real workspace. In the app, the "Try the demo" result has "Open the demo
  workspace" and the demo banner has "Back to my workspace"; the app restarts
  its server on the other folder ([app.md](app.md#switching-to-the-demo)). The
  app opens the recorded workspace after any restart of the process, which the
  demo never is. In a plain browser there is no process to restart: the result
  answers with `applysmith serve --workspace <demo>` and the banner with `applysmith
  serve`, both copyable.
- **Sample form.** `GET /demo/sample-application` (public, remote denied)
  serves the form only in a demo workspace, under a policy that loads nothing
  and submits nowhere. A dry run of it attaches the résumé, because the page
  is on loopback.
- **Sample postings.** Their `.example` hosts never resolve, so in a demo
  workspace the evaluator reads a sample posting's URL from the embedded
  fixture (`evaluate.demoReader`, wrapped around every reader by
  `evaluate.New`) with the verification `fixture unverified`; any other URL,
  and every URL outside a demo, goes to the real reader. The dashboard has no
  inbox list of its own yet, so the tutorial evaluates them with `applysmith
  evaluate <inbox-row-id>`.

## Not yet covered

- `applysmith cv import` and PDF or DOCX extraction. The flow and the evidence
  import read `.md` and `.txt`; the CV parser in `cvparse.go` is the setup's own
  heuristic until the document toolchain provides the shared parser.
- The citizenship, earliest start date and notice period answers of step 4,
  the visa class and end date as separate keys: the profile schema does not
  hold them. The visa class and end date are stored inside `visa_status`
  (`work_visa: H-1B`).
- Starting the server at the end of step 1. The step reports whether one runs
  and where its setup wizard is (`<origin>/setup`; `applysmith open` prints a
  sign-in link), or prints the command that starts one.
- The résumé advisory finding (the PDF text lacks the identity email): no PDF
  text extractor is wired into setup.
- Storing the shared default sign-in and the 2Captcha key from the wizard:
  those forms belong to the Settings work (the steps show their instructions
  and link to Settings). The 2Captcha balance is read only inside an apply run.
  The Gmail step draws Settings, Gmail's own controls (address, app password,
  Connect, Test, Disconnect) in place.
- The fingerprint check is proven by `TestE2E_Browser_CheckFingerprintReadsBothWindows`
  (headless, `make test-browser`); the product's own windows are not run in tests.
- Nothing is created before the workspace step: a workspace without
  `profile.yml` gets neither lock nor database from `setup`, and the
  interactive run takes both only after step 1 has created the workspace.
