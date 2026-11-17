# Interview preparation

Implementation notes for `internal/interview` (task 6.5). The normative text is
the interview requirements of `specs/application-documents` and the coverage
grid of `specs/knowledge-base`; this file says where things live and records
the choices the specifications leave open.

## Storage

Migration `0030_interview.sql`:

| Table | Holds |
|---|---|
| `interview_events` | the append-only log: `stage`, `pack`, `feedback` and `mock-turn` events of a candidacy, each at most 16,000 bytes (a `CHECK` and the size test before the insert); triggers refuse an update other than the candidacy link and every delete |
| `interview_packs` | one row per pack revision, written once; the body exceeds an event, so the log holds a small `pack` notice and this table the whole revision |

Both tables are linked tables of the tracker, so a merge of two candidacies
moves their history onto the survivor (the append-only triggers allow exactly
that update). Pack rows have no unique revision key for the same reason: a
merge must not fail on two packs both at revision 1.

The same migration widens `document_jobs.kind` with `interview`, because a pack
run is a document run.

## Runs

`documents.ActionInterview` makes a pack a document run: it shares the queue,
the idempotency claim, cancel, the runs board entry (kind `documents`),
`GET /api/documents/runs/{id}` (which names the stored revision in `pack`) and
the stage events. The stages are `retrieve`, `draft` and `publish`. Nothing is
written before `publish`, so a cancelled run leaves no partial pack. A candidacy has one
pack run at a time (`conflict` for a second) and one other document run at a time;
the workspace (`packRun`, with the stage it builds) and `GET /api/documents`
name the one working, so a drawer opened later follows it.
`documents.Service.Interview()` is the record keeper the stage, feedback,
mock-turn and workspace routes use.

## Sources

`documents/interviewrun.go` gathers the input; `interview.Build` never reads a
file. For a candidacy with documents, the version attached to a submitted
application (`frozen_at` set) is used, else the newest, marked `current`. The
version directory holds the posting (`posting.md`), the ranked requirements and
the company facts as they were when the pair was made (`evidence-manifest.json`),
the résumé and the letter, so a frozen dossier gives all of them frozen. With no
document, the evaluation report and the knowledge-base company facts are read
now and marked `current` and `unverified`, and the pack records the report's
number (`reportNumber`) so the drawer can say when the row has a newer one.

Source kinds: `dossier` (files of a document version), `report`,
`company-brief`, `story` and `feedback`.

## Pack

- Questions: the base set of the stage type, then one `requirement` question per
  top requirement (at most 5). `kb.Service.MatchStories` links the best stories
  (up to 3) with the same BM25 retrieval as `Search` but records no knowledge
  gap, because an unmatched requirement is a finding of the pack, not a missing
  answer. A requirement with no story becomes a gap and a next action naming
  story mining.
- Gaps and next actions are steps (`Step`): the sentence, and a kind with its
  target saying where the fix lives: `story` (the requirement), `brief` (the
  company), `evaluate`, `documents`, `confirm`, `practice` and `debrief` (the
  stage). The CLI and the Markdown copy print the sentences; the drawer links
  each kind's place, once per pack (a next action a gap already links shows no
  second link). The workspace's next actions are steps as well: `stage`
  (add one), and `pack` and `confirm` with the stage as target.
- Bounds are enforced twice: `finalize` trims lists and drops references to
  sources that did not fit (a hook left with none is dropped), and `Validate`
  refuses a pack that still breaks a bound, so `Save` stores nothing invalid.
- Model assist (task `interview-prep`, one call): it may reword requirement
  questions by index and propose hooks. A prompt must fit its bound and name an
  existing requirement. A hook must cite a source of the company facts and
  repeat at least two content terms of a fact from that source; anything else
  is dropped. A failed call leaves the deterministic pack and a warning.
  `generation` is `model` only when something from the model was used.
- Story ids come only from `MatchStories`, never from the model.
- Prior feedback is the candidacy's, the stage's own first and then the other
  stages' named by stage ("Stage phone-1, user: ..."), at most 10. This widens
  "prior feedback for this stage" on purpose: a debrief of one round is what
  the next round's pack most needs, and the spec's scenario (feedback of a
  stage reaches that stage's next pack) still holds.

## Mock turns

`coach.go` runs four deterministic checks against the submitted résumé and the
story bank: a figure the answer states that the sources state differently for
the same word ("12 engineers" against "5 engineers") or lack, a first-person
claim sentence with no supporting line, a behavioral answer of 20 words or more
with no figure, and "we" language against "I". A percent only supports a
percent. With no source at all the coaching says nothing could be verified
rather than flagging every claim. Every line and figure of the corpus keeps
the story it came from, so the coaching names the stories whose own lines back
a sentence or a same-word figure of the answer, or says it draws on none; a
name comes only from the bank, never from the model. A routed model adds one delivery note after
the findings and is told to state no fact about the candidate; without one
the coaching says the note is missing and why (`noteMissing`: no model for
interview prep, the offline choice included, names Settings, Models; any
other failure asks for a retry), so the checks are not taken for all of it. The coaching is
Markdown: the findings a list, the note a paragraph of its own.

Coaching is a background run of kind `interview.coach` (one per candidacy,
cancellable, a board task). The route refuses a bad answer or stage before
the `202`; the run input keeps the stage, question and answer, so the
workspace's `coachRun` lets a drawer opened later follow it with the answer
back in the form. A cancelled coaching records no turn.

## Email-proposed stages

`gmail.Pipeline.Stages` receives the stage an interview mail announces, in the
order the outcomes replay. The stage id derives from the message id, the type
from the subject and snippet, and `userConfirmed` is false until the user
records the stage again as confirmed. A proposal never replaces a recorded
stage, so a replayed mail cannot undo a confirmation or a cancel.

## Cancel and revisions

A stage's revision is the id of its latest event; the API requires
`expectedRevision` (0 for a new id) and answers `conflict` when stale. A cancel
records the stage again with `cancelled` (no migration). Cancelled stages are
listed apart from `stages`, so the next stage, Home's next interview and the
next actions skip them, and feedback, mock turns and pack runs refuse them
until restored (Undo or Restore in the drawer).

## Surfaces

- API: `GET /api/candidacies/{id}/interview` (the workspace view),
  `POST .../interview/stages`, `GET|POST .../stages/{stage}/pack` (the POST
  returns a run id), `POST .../stages/{stage}/mock-turns` (`Idempotency-Key`, `202` with a
  run id; see Mock turns), `POST .../stages/{stage}/feedback` and
  `GET /api/interviews/soon` (Home's stages today or tomorrow); all standard access,
  remote denied. The rule behind it (`interviewsSoon` in
  `internal/api/interview.go`) also gives the day's interview lines and the
  interview reminders (see [notify.md](notify.md)).
- CLI: `applysmith interview-prep --candidacy <id> --stage <stage-id> [--type]`.
- Ask Agent: `story_mining` (see [ask-agent.md](ask-agent.md)).
- Dashboard: the Interview prep drawer of a Tracker row, and the story shapes
  table beside the coverage grid on the Wiki page. The drawer's practice
  is a drill: a picker lists the pack's questions, then the questions of the
  themes no story covers (from `GET /api/kb/stories/coverage`), then the rest
  of the competency library, marks the ones already practised, and "Next
  question" walks the list. The themes with no story are named above it with a
  link to the story bank. Under the answer, its spoken length (150 words a
  minute) names the cut it lands in and what that cut keeps: 45 seconds
  (tension, action, result), 90 seconds (one trade-off, one checkable result)
  or 3 minutes (only on request: numbers and reflection). Every answer is
  still a mock turn, coached as above; the drill adds no route and no model
  task. The answer stays after coaching for another try, and can start a
  story interview: `POST /api/kb/stories/mining` with the question and answer
  as `notes`, which the model quotes word for word into the parts it states
  (the route waits up to the model deadline); the story bank continues it.
- The drawer lists stages in date order and opens on the next one still ahead.
  Edit or Confirm details records the stage again with its id (a proposed
  stage is confirmed that way). A row still `applied` or `responded` offers to
  move to `interview` with the row version read, so a newer change is refused,
  not overwritten. The pack shows when it was built and whether the stage,
  any feedback, a story or the company brief it read changed since. Once the
  shown stage is over and none is ahead, the debrief leads the stage and a
  note offers Add the next stage, Record an offer or Record a rejection
  (the same versioned PATCH). A practised pack question names its stories. A dated stage offers Add to calendar: an
  RFC 5545 `.ics` made in the browser (UID per stage, UTC start, one hour,
  the posting as URL, the drawer link in the description).

## Tests

Golden files live in `internal/interview/testdata` (`pack_deterministic`,
`pack_model` with a scripted model) and `internal/kb/testdata`
(`coverage.golden.json`); rewrite them with `go test ./internal/interview/
./internal/kb/ -update` after checking the diff.
