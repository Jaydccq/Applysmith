# Final PDF verification

Task 3.7a ([application-documents spec](../../openspec/changes/migrate-to-go-mygo/specs/application-documents/spec.md),
"Verification of the final PDF artifact"). It extends the two verifier
scripts and the build path of the [document toolchain](document-toolchain.md);
it adds no second verifier. The model review that precedes it is described in
[document-review.md](document-review.md). The scripts' contract is
[document-assets.md](document-assets.md).

## Where each check runs

| Where | Code | What it needs |
|---|---|---|
| Text layer, page counts, dates, fonts | `assets/verify_pdf.py` (pypdf), script version 3 | the PDF |
| Geometry | `assets/verify_layout.py` (Poppler word boxes), script version 3 | the PDF and a threshold profile |
| Content against the source | `internal/documents/finalcheck.go`, rules version 1 | the text layer and what the template printed (`finalexpect.go`) |
| Verdict and its binding | `internal/documents/verification.go` | the checks of all three |

The final checks are Go over the text layer the verifier hashed, so their rules
are unit-tested without any tool. The expectation is built from the data the
template was rendered from (`assets.Resume`, `assets.Cover`), never from the PDF.

## The checks

Every check is `pass`, `fail` or `unverified`; all are mandatory.

| Tool | Check | Fails when |
|---|---|---|
| verify_pdf | `pdf-readable`, `text-layer`, `page-count` / `max-pages`, `ascii-dates` | unchanged from the toolchain contract; a two-page letter fails `page-count` |
| verify_layout | `page-text` | a page has no body text |
| | `page-size` | a page is not the profile's paper size (A4 595.28 x 841.89 pt, Letter 612 x 792 pt, 1.5 pt tolerance) |
| | `clipping` | a word box leaves the page, or reaches more than 14 pt past the template's measured left or right text edge, or stands in the top or bottom 18 pt |
| | `text-overlap` | the cores (middle 60 percent of the height) of two word boxes share more than 30 percent of the smaller one: text printed over text, such as a signature over a footer line |
| | `internal-hole`, `early-page-end`, `footer-collision`, `final-page-thin`, `orphaned-entry` | a 100 pt hole, 25 percent bottom space with pages following, two lines in the bottom 90 pt band, a final page more than 35 percent empty (only with more than one page), a heading or entry header split from its content |
| | `entry-gap` (résumé profile only) | the gap between a line and the next, after the document's first section heading, is over 24 pt. The template leaves at most about 11 pt between entries and sections; the 100 pt hole limit does not see a 40 pt gap |
| final | `contact` | the name, the email as a whole token, the phone number, the address or a link label the template printed is not in the text layer. The moderncv icon names ("Envelope", "MOBILE-ALT") are ignored; an address glued to its neighbour does not count; the name is looked for with addresses set aside |
| | `word-coverage` | under 90 percent of the distinct source words (two characters or more) are in the text layer; a long word split across lines counts when its letters are there. A word the layer holds only with a space inside it on one line ("T eam") fails whatever the coverage: a program searching the text does not find it |
| | `text-order` | the name, the section headings (résumé) or the date, salutation, paragraphs, list items, closing and signature (letter) are not in reading order. Matching ignores spacing and punctuation; a kerning split ("T eam") is `word-coverage`'s to report |
| | `glyphs` | the text layer has U+FFFD, `(cid:`, private-use code points or control characters |
| | `dates` | a date range the source prints (`2018-2021`, `Mar 2021 - Present`) is not in the layer exactly, with its ASCII hyphen |
| | `percent` | a percentage the source prints lost its sign |
| | `fonts` | letters are set in a font outside the template's list (résumé: Latin Modern Sans and Font Awesome; letter: Lato and Raleway), or the letter's text at 14 pt or below uses more than one font. Runs under 3 letters are ignored |

A short, truthful one-page résumé is not a failure for its white space:
`early-page-end` needs a page after it and `final-page-thin` needs more than one
page. The profile thresholds live in `PROFILES` in `verify_layout.py`, one per
template and paper size, and the result echoes the profile id.

Measured on the compiled templates: the résumé's text runs from 59.5 pt from
the paper's left and right edges on A4 (Letter 61.2), the edges of the text
area; the letter's from 79.4 pt on both sides (its 2.8 cm margins). The
largest gap between lines after the first heading is about 10.5 pt, above a
section heading.

## Entry dates

`dateRange` (`render.go`) prints an entry's dates the same way in the PDF,
`resume.md` and the model and deterministic paths: a numeric month as the
language's month name with the year ("Mar 2021" in English, which abbreviates
to three letters; "März 2021" in German), a year alone as the year, and any
other form as written ("Spring 2020"). Start and end are joined by
` - `, one ASCII hyphen between spaces: the spec's normalization rule, and
`ascii-dates` fails a Unicode dash. An experience with no end, or an entry
ending "present", ends in the language's word ("Mar 2021 - Present"); an
education with no end shows its start. A cv.md heading range may use "to", a
spaced hyphen or a dash; a bare hyphen splits only before a year
("2018-2021"), so "2021-03" stays one date.

## Unverified is not a pass

A check that cannot run is `unverified`: no pypdf, no usable `pdftotext`, a text
layer that is not the one the verifier hashed, no fonts, nothing to compare
with. If a verifier crashes or prints something the Go parser refuses, `Build`
keeps the PDF and records every check of that verifier as unverified instead of
discarding it. Cancellation and timeouts still stop the build.

The PDF state (`decide` in `verification.go`) is `failed` when any check failed,
else `unverified` when any check is unverified or a tool reported no checks,
else `ready`. Only `ready` is selected as the attachable version; a failed or
unverified PDF is kept for preview and does not replace an earlier ready one.

## The stored verdict

`pdf-verification.json` holds one record per kind: state, the SHA-256 of the
PDF, the TeX and the text, the content hash of the structured source and the
evidence manifest hash, the template version, the script and rules versions, the
Poppler and pypdf versions, every check with its tool and reason, the preview
files with their hashes, and the visual review.

`Service.Verification` returns it only while it still describes the stored
files: the PDF on disk must hash to the recorded value and to the row, and the
source and manifest hashes and the state must equal the row's. Otherwise the
record comes back with `stale` set, state `unverified` and no checks, so an
edited or replaced document never shows an old pass. A new edit or render is a
new version with its own record.

A version holds the pair, so an edit, an accepted revision or a PDF run of one
document also writes the other. A document the new version holds
byte-identical (structured source hash, evidence manifest, paper and page
limit) keeps the passed PDF of the version it was loaded from: the PDF, TeX,
text and previews are copied, each checked against the hash the verdict
records, and the verdict (with a visual review of that PDF) is copied with
them (`keptPDF`, decided by the coordinator under the owner's delegation,
2026-10-22). A PDF run builds only the document without one. Anything short
of an exact match, or a verdict that is stale or not a pass, leaves the
document without a PDF in the new version.

The visual review is separate. Mechanical checks never set it; it is `pending`
or `performed` for one PDF hash, and a review of another hash reads as pending.
Nothing records a performed review yet; that is part of the exit inspection of
task 3.10.

## The drawer

`GET /api/documents` carries `verification` on each version that holds a PDF.
The dashboard's documents drawer (`web/src/pages/Documents.tsx`, opened from the
Tracker row drawer, `?documents=<id>`) shows the source revision, the PDF
revision, the checks with their results, the preview files and, apart from
them, the visual review. It words the checks as parseability and layout checks,
not a guarantee of acceptance by every applicant tracking system, and shows a
passed badge only when the row and a matching stored verdict both say ready.
The spec defines no route that serves the preview images, so the drawer lists
the files and their hashes.

## Fixtures

Each fixture compiles a real template from fictional data (persona Alex
Example, example.com), changes the TeX the way a regression would, and runs the
product's build and checks. `make verify-documents` runs them
(`TestDocuments_RealVerify_*`).

| Defect | Change | Check that must stop it | Test |
|---|---|---|---|
| email missing, words preserved | résumé printed without its email | `contact` only | `RealVerify_EmailMissingDespiteWordCoverage` |
| orphan heading | `\newpage` after `\section{Education}` | `orphaned-entry` | `RealVerify_OrphanHeading` |
| clipping | an unbreakable 125-character word in a bullet and in the letter | `clipping` only | `RealVerify_ClippedContent` |
| large entry gap | `\vspace{40pt}` between two entries | `entry-gap` only (`internal-hole` passes) | `RealVerify_ResumeEntryGap` |
| signature over the footer line | a second line printed 13 pt up | `text-overlap` only | `RealVerify_FooterSignatureCollision` |
| signature in the footer band | page numbers on, the page enlarged by 80 pt and 450 pt of space before the closing | `footer-collision` | same |
| letter text in a foreign font | Latin Modern Roman selected before the list | `fonts` | `RealVerify_CoverForeignFont` |
| percent sign lost | `40\%` written `40` | `percent` only | `RealVerify_PercentAndDateEncoding` |
| double hyphen date | `2018--2021` | `dates`, `ascii-dates` | same |
| unescaped percent | `99.9%` in a bullet | the rest of the bullet is lost: `percent` fails and the PDF is not ready | same |
| tool missing | `pdftotext` or Python replaced by a missing path | the tool's checks are `unverified`, the PDF is kept and not ready | `RealVerify_MissingToolLeavesThePDFUnverified` |
| stale verdict | PDF bytes or source hash changed | the record reads as stale | `RealVerify_RecordBindsTheArtifact`, `TestVerify_StoredVerdictBindsPDFAndSource` |

The glyph rule has no PDF fixture: no template can be made to emit U+FFFD,
`(cid:` or a private-use code point, so it is tested on text.

## Letter layout

- Paragraphs are 9 pt apart, and a list has the same 9 pt above and below it.
- The contact separator disappears where a contact line wraps, so no line
  starts or ends with "|".
- The header ends with its rule whether or not the letter has a date.
- A block with no text prints its list alone: an empty paragraph stops XeLaTeX
  ("There's no line here to end"). With no body the bullets also follow the
  opening (`buildCover`).

Each template change gives a new smoke key: run `applysmith documents toolchain
smoke` once after installing a build with it, or PDFs report
`document-toolchain-unavailable`.

## Not covered

- A visual review is never recorded by the product yet.
- Preview images are not served over HTTP.
- Rules are tuned on the synthetic documents; real résumés with many entries
  and other languages are not measured here.
