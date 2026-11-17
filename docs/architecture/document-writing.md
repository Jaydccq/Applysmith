# Document writing

How the words and the order of a tailored résumé and cover letter are decided
(`internal/documents`: `draft.go`, `render.go`, `manifestbuild.go`,
`lang.go`). What a draft may claim is [document-grounding.md](document-grounding.md);
how the PDF is typeset and checked is [document-toolchain.md](document-toolchain.md)
and [document-verification.md](document-verification.md).

## What is written by rule and what by a model

| Written by Go and the templates | Written by a model, then gated |
|---|---|
| Contact block, name, PDF title and author | Résumé summary |
| Entry titles, organizations, places, dates, grade | Résumé bullets and competency lines |
| Section headings, the order of entries | Letter opening, body sentence, bullets, closing |
| Letter date, recipient lines, salutation, sign-off | |
| Company paragraph (quoted facts), relocation sentence | |

The model supplies wording only, in a fixed JSON shape, and every sentence
passes the grounding gate or is replaced. With no usable model draft the
deterministic builders (`deterministic.go`) write both documents from the
approved source lines, so the frame never depends on a model.

## What a screening program reads

- Entries print newest first within experience and within education
  (`datedEntries`): a current entry, then by end date, then by start date;
  an entry with no end on record is placed by its start.
  The order of `profile.yml` no longer decides it; equal dates keep it.
- A numeric grade average prints with its label ("GPA 3.8", `gradeNote`);
  a grade in words prints as written.
- The PDF title is the applicant's name and the document's name
  ("Alex Example - Resume"), not a class default.
- The letter prints the company and the role as a subject line ("Re: Backend
  Engineer") between the date and the salutation, and the profile links on a
  line of their own, so the email line never ends in a separator.
- Both documents are set without pair kerning, and the letter with lining
  figures;
  the reason and the check that holds it are in the toolchain and verification
  notes.

## Projects

- A CV group whose heading names both groups ("Project Experience") holds
  projects. Read as roles its sections matched no employer, so every line
  under them belonged to no entry and no résumé could print it: in the
  owner's workspace all eight CV projects were lost this way.
- The sections of one knowledge document under `knowledge/projects/` are one
  project, named by the document's title (`kb.Fact.Title`), then by the file
  name. A section heading ("Why I am proud of it") was printed as a project
  title before.

## The drafting rules

`resumeRules` and `letterRules` hold the grounding rules and, under "How to
write it", the style rules. A refinement carries the same text. In short:

- Résumé: a summary of two or three sentences without "I"; three to five
  competency lines of three to ten items; a bullet is one sentence of at most
  30 words that opens with a past-tense verb and keeps the verb, the figures
  and the counted nouns of its evidence line; bullets that answer a
  requirement come first.
- Letter: an opening of at most two sentences that names the role and the
  company and gives one result; one body sentence; bullets of one sentence, at
  most 35 words, each for a different requirement; a closing of at most two
  sentences. Nothing is said twice, and a list of stock phrases is named as
  filler.
- Both: a word of amount (many, several, significantly) is a figure and is
  never copied from a requirement (`vagueRule`), which is how the gate reads
  it.
- An English letter aims for `letterMinWords` to `letterMaxWords` words (120 to
  260); the prompt and the gate's `length` warning read the same constants.
  The earlier target of 250 to 300 was met with a second sentence under every
  bullet that restated the first.
- A second letter attempt is told why the first was refused (`retryNote`),
  instead of being sent the same prompt again.
- The letter's overflow repair adds the two empty lines over the signature
  to its last round (`letterSignGapPt`): a signature alone on a second page
  measures as one line of spill but needs three to come back.

## Measured, 2026-11-17

Four frozen manifests of the owner's workspace were replayed through the
letter prompt by a model run inside a coding agent, and each reply was put
through `checkLetter`.

| Prompt | Gate passes | Words |
|---|---|---|
| Before | 3 of 4 | 103 to 208 |
| First rewrite (closing "in the words of a requirement") | 1 of 4 | 120 to 176 |
| Final | 12 of 12 over three runs | 98 to 153 |

The first rewrite failed on amounts copied from the posting ("running many
workflows at once") and on reworded figures; the amount rule, "shorten the
evidence line rather than rewording it" and a closing "with no number or
amount" removed both. The stored letters of the same four runs each carry a second
sentence per bullet that restates the first; the replay did not reproduce
that with either prompt, so whether the new rules end it is known only once
letters are generated through the router.

The same four postings were then rebuilt from the current CV with projects
grouped (two pages): three projects were selected each time, the evidence a
letter may cite went from 38-40 lines to 29-50, three postings lost
requirement gaps and one gained one, and all four letters drafted on the new
manifests passed the gate (126 to 147 words). On one page a single project
is selected, so a letter has 18 to 39 lines to cite. A CV project and a
knowledge document about the same project are still two projects.

Thirty stored letters were recompiled with the new font options: 24 of the
stored PDFs held a split word in the pypdf text layer, none of the 30
recompiled ones did.

The résumé prompt was replayed the same way through `checkResume`: 3 of 4
passed before and 4 of 4 with the final text. An intermediate wording that
asked for "three to five" and "one to three" bullets returned fewer lines
than before and once left both selected projects out (`missing-project`);
the final rule says that every listed entry gets bullets.

The replay did not go through the router, so it shows the direction of the
change, not the live pass rate. The live figure is
the share of `generation: deterministic` letters under `documents/` (42 of
699 before this change).

## Not done

- The fixed sentences of a letter (the application sentence, the offer to
  talk) stay in the model's text. Writing them by rule means a new stored
  letter shape for six languages, for sentences the gate already holds.
- The résumé keeps the moderncv banking layout and its list marker, which the
  layout verifier measures.
