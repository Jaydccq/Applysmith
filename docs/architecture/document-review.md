# Independent content review

Task 3.7b ([application-documents spec](../../openspec/changes/migrate-to-go-mygo/specs/application-documents/spec.md),
"Bounded independent content review"). It sits between drafting and compiling in
`internal/documents` (`review.go`, `review_prompt.go`) and adds no new task: the
calls run under the registered `document-review` task, and the one revision under
`resume-write` (or `cover-letter` for a letter-only run), so the routing table,
cost cap and call ledger apply. Every preset routes `document-review`.

## One review, at most one revision

| Stage | What happens |
|---|---|
| `review` | One `document-review` call with only the frozen drafts (addressed span by span), the job requirements, the selected evidence, the company facts, and the language and page limit. No drafting prompt, rationale or run warning is sent. |
| screening | Each factual finding must name a real span, cite manifest evidence ids, and carry no figure, name or relocation claim the cited evidence does not hold. An edit never states a company fact (the system prints the letter's chosen facts), an edit of the closing is held to the back-reference rule, and an edit of any earlier part to the forward-pointer check. A finding that reads a personality trait into the writer is dropped. Rejected findings are recorded with their code. Narrative suggestions are recorded and never applied; one may cite the ids and span addresses it was shown ("E13", "R4", "X1.2"), open with an imperative, count the draft's parts, and name a posting term in a sentence that reports a gap or cites a requirement without asking to add or say it ("R8 (Git) is not covered"; `gapSentence`). |
| `revise` | If a factual finding survived, one revision call returns the whole pair again. Both documents go back through `checkResume` and `checkLetter`; either failing keeps the pre-revision draft for both. |
| `compile`, `verify` | Unchanged. They build from whichever draft survived, so the grounding and PDF checks run on the revision. |

## Unavailable is not approved

No provider, a failed or timed-out review call, or a run deadline too near for
the review all record `status: unavailable` with the reason. The review is never
retried, and the run still gates, renders and publishes. The reserve it leaves
for the later steps is 30 s, plus the toolchain's pair budget when a PDF was
asked for.

## The record

`document-review.json` holds the status (`unavailable`, `reviewed`, `revised`),
the accepted factual findings, the narrative suggestions, the rejected findings,
the revision outcome with both grounding gates, the ledger call ids and the run
time left around the review. The documents list carries a summary of it, and a
running run view carries `remainingSeconds`.

## Refining a version on request

`POST /api/documents/{id}/refine` (code `documents/refine.go`) asks the model
for a revised draft of one stored version, given an instruction of at most
2,000 characters and up to 8 earlier turns (instructions, and accept or reject
decisions). It is a document run (`ActionRefine`) like the others, so it takes
the queue, returns a run id and ends in the run view.

- **Same chain.** The run reads the stored version back (hash-checked), uses the
  drafting task (`resume-write`, `cover-letter`) and the drafting rules
  (`resumeRules`, `letterRules`, shared with the first draft, with the same
  unheld-names rule and the requirements marked as gaps), quotes the posting,
  evidence and current draft as data, and keeps the instruction outside the data
  tags as the user's own request. It states that the instruction is not
  evidence. The answer goes through `checkResume` / `checkLetter` and is
  refused whole if either fails: no per-bullet replacement, and no new review.
- **A proposal, not a version.** An accepted answer is stored in
  `document_proposals` (migration 0042) with its readable text and a line diff
  against the version; the version, its PDF and its verification are untouched.
  One proposal per version can be pending. A failed or unchanged revision is
  stored as a `failed` row, shown as an error turn.
- **Decisions.** `POST .../refine/accept` runs an edit with the proposal's
  content (a new draft version, no PDF for the revised document, generation
  `model`; the proposal turns `accepted` in the same transaction that
  publishes the version).
  `POST .../refine/discard` marks it `discarded` and the next request carries the
  rejected decision, never the rejected text. `GET /api/documents/refine/{id}`
  returns the pending proposal and the last 8 turns. Only the newest version of
  a kind that is not attached to a submitted application can be revised.
- **PDF.** While a proposal is pending, a PDF request for that version is
  refused with `conflict` and reason `proposal-pending`, at admission and again
  when the run starts. Documents list `refinePending`.
