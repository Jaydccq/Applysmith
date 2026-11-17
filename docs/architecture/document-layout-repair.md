# Layout repair

Task 3.7c ([application-documents spec](../../openspec/changes/migrate-to-go-mygo/specs/application-documents/spec.md),
"Page budgets and relevance-based compaction"). It runs inside the compile stage
of `internal/documents` (`repair.go`, `repairrank.go`), after the first build and
before publication. It makes no model call and writes no TeX: a round changes the
structured content or one template flag, and the trusted templates render again.

## Trigger and bound

After the first build, a round starts when one of these failed:

| Defect | Checks | How a round sizes its cut |
|---|---|---|
| overflow | `max-pages` (résumé), `page-count` (letter) | the spill onto the pages beyond the limit (below) |
| thin last page | `final-page-thin` | the spill onto the last page, so the content fits on one page fewer |
| fit | `early-page-end`, `footer-collision`, `orphaned-entry`, `internal-hole`, `entry-gap` | no height: the spacing flag first, then one line per round |

Clipping, overlap, page size, the content checks (contact, words, order, glyphs,
dates, percent, fonts) and an unavailable verifier start no round: removing text
does not fix them, and the PDF stays a failed (or unverified) preview. A short
one-page résumé passes (`early-page-end` and `final-page-thin` need a second
page) and is never touched.

`MaxRepairRounds` is 2 and fixed. A round starts only with 120 s left of the 300 s
pair budget (an engine pass and both verifiers); otherwise the repair stops and
records why. A round that can change nothing ends the repair early. Only the
documents whose content or template input changed are built again.

## What a round may do

1. Set the résumé template's one reviewed flag, `Tight`: 1 pt instead of 3 pt
   between entries and no extra bullet spacing. It saves 2 pt per bullet
   (measured: the 2 pt between two bullets, and the entry's closing 2 pt for
   its last one), so it is tried first, and alone when that covers the spill.
   The 11 pt font and the 0.80 geometry scale are the readability bounds in
   both modes, with gaps of at least 1 pt; a test
   (`TestResume_TightAdjustmentStaysWithinTheReadabilityBounds`) holds the TeX
   to them.
2. Cut approved lines, lowest rank first, until the spill is gone: the
   smallest run of lowest-ranked lines that does it. A round that falls short
   is finished by the second, which adds one line to its target, because blocks
   the template keeps together (an entry, the closing) move as a unit.

### Sizing a round

Everything comes from the build the round repairs, not from a guess at the
page (`spillPt`, `resumeHeight` in `repair.go`):

- **Spill** = the text on the pages after the target page (the lowest line box
  minus the first, from `verify_layout`'s `text_top_pt` and `bottom_space_pt`)
  + the space above it when it moves up (10.6 pt above a section heading,
  1.6 pt above an entry; 2 pt less at tight spacing) − the room the target page
  still has above the lowest line box the text area allows (A4 745.8 pt,
  Letter 701.9 pt). A heading's box starts 2.4 pt above an entry's at the top
  of a page (A4 64.5 pt, Letter 60.5 pt), which tells the two apart. The
  letter has no measured room: its spill is its text alone.
- **A cut line** saves its printed lines, counted in the build's text layer
  (one printed line per line), at 12 pt a bullet line, 16.3 pt a competency
  line and 13.5 pt each further line of it. A line the layer does not hold
  exactly once is estimated at 112 characters a line.

All of it was measured with `pdftotext -bbox-layout` on the compiled template
(2026-10-17), with markers set at the bottom and top of a page.

Nothing is rewritten, padded or added, and the model is not called. A thin last
page of a two-page résumé is repaired the same way, which can cut a sizeable part
of a page.

### Measured before and after (2026-10-17)

The same three demo drafts (candidacies 1, 2 and 4, `--with-cover-letter`),
each given PDFs by the old and the new sizing through the PDF action, so the
content is identical; "room" is the space page 1 has left above 745.8 pt. All
were one round, one page, résumé and letter ready.

| Draft | Old: lines cut, room | New: lines cut, room |
|---|---|---|
| Example Corp | 3, 12.8 pt | 2, 0.8 pt |
| ramp | 4, 31.7 pt | 2, 3.5 pt |
| Flexi | 4, 57.3 pt | 0 (spacing flag alone), 0.8 pt |

The old sizing ignored the room page 1 still had and the space above the moved
block, credited the spacing flag with 1.3 pt per entry (it saves 2 pt per
bullet), counted a competency line as 12 pt (16.3 pt) and wrapped bullets at
104 characters (a 113-character bullet prints on one line). A full end-to-end
`resume generate --candidacy 2 --with-cover-letter --pdf` on the same page
geometry cut 4 lines and left page 1 ending at 716.8 pt before, 2 lines and
745.0 pt after. Wall time is unchanged (one round either way, 7 to 11 s for a
PDF action under the machine's load; 49 and 53 s for the whole generation with
its model calls), well inside the 300 s pair budget.

## Ranking

Removable: résumé bullets and competency lines; letter bullets, then trailing
sentences of the evidence paragraph. Ascending tuple:

1. distinct explicit requirements the line's evidence supports (manifest coverage),
2. evidence lines no other line of the selected material cites,
3. whether the paired letter cites its evidence (false first),
4. strength: 4 for a line that states a quantified result (a figure, count or
   percentage; a magnitude word alone does not count), plus 1 for each of up to
   three posting keywords it names; a competency line starts at 2,
5. printed position, later first, as the last tie-breaker.

A bullet that carries the only quantified result its entry still has, in an
entry with unquantified bullets, ranks after every other line of the résumé
(`Rank.OnlyQuantified`, recomputed after each cut), so it is the last bullet
of its entry to go. Strength is what orders lines when the manifest's
requirement coverage is nearly empty (requirements that are mostly semantic): a
tail-first order there cut the strongest bullet of a job (the p99 latency
result, candidacy 4 v6, 2026-10-17).

The position tie-breaker applies only after all four terms tie. Printed order is the
order the user's manifest gives, so in a reverse-chronological section it cuts
the older of two equal lines; that is the manifest's order, not age as a
relevance term. A letter bullet's uniqueness counts only the letter's own
citations, because the letter is ranked against itself. A letter sentence
carries no rank: it is cut only after no bullet can go, and its record says so.

Age is not an input. Ranks are recomputed after each cut, so a line whose
duplicate was just removed becomes the only carrier of its evidence.

Never removed: contact values (not content), section headings (the template's),
the last line of each entry and of the competencies (the label the template
prints), the summary, the letter's opening, closing and signature, the last
letter bullet and the first sentence of its paragraph.

## The paired letter

A letter claim (the opening, each bullet) that the résumé backed keeps an
equivalent grounded reference while any résumé line still cites one of its
evidence ids. A letter bullet left without one is removed with the cut, and the
cut records it. A cut that would strip the opening's last reference, or the
letter's last bullet, is refused: that prose cannot be rewritten without a model.

## Gates and record

Each cut is checked with `checkResume` / `checkLetter` first. The re-rendered
document goes through the same build, verifiers and final checks as the first
render, and its verdict replaces the earlier one. A defect left after round two is
`unresolved`: the PDF is a `failed` preview, not `ready`, never attached. A
rebuild that produces no PDF is `unresolved` too.

`layout-repair.json` records each round's trigger, adjustment, cuts (text, group,
evidence, rank tuple, reason, requirements left uncovered, effect on the letter),
the gates that ran and the state of each rebuilt PDF, the résumé's coverage before
and after, and each document's content hash before and after. The bundle manifest
carries the outcome under `layoutRepair`, `application-strategy.md` follows the
coverage after the cuts and names a requirement that lost its last line, and the
run warnings say how many lines were removed.

## Source hashes

A build is given the SHA-256 of the TeX it was rendered from
(`BuildInput.TeXSHA256`) and refuses other bytes, or a build name the service did
not generate, before it makes a directory or starts a process. A round recomputes
the hash of the structured content and refuses (`stale-source`) if it differs from
the one its PDF was rendered from. An explicit PDF action still refuses a stored
version whose source changed after it was written.

## Tests

| Property | Test |
|---|---|
| ranking, age, ties, letter dependency, required lines | `repairrank_test.go` |
| the loop on a scripted builder, crashes | `repair_test.go`, `repair_fake_test.go`, `repair_crash_test.go` |
| argv, limits, names, process groups on test engines | `toolchain/argv_test.go` |
| real engine: cancellation, timeout, output cap, literal payloads | `toolchain/real_test.go` |
| a thin last page cuts no more than it needs | `repair_real_test.go` (`RealThinLastPageCutsOnlyWhatItNeeds`: the last cut line put back makes two pages) |
| real repairs and the unresolved case | `repair_real_test.go` |
