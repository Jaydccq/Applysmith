# Document grounding

The deterministic gate (`internal/documents/ground.go`, `names.go`,
`quantity.go` and the `quant*.go` files) decides whether a résumé or letter draft may claim what it says.
It runs on every model draft, every review revision and every stored version
before a PDF run. The free-text answer composer draws on the same evidence lines
(`ApplicantEvidence`, see answer-bank.md, Composer) under its own grounding check. Spec: `application-documents`, the content boundary and the
quantified-claims requirement.

## Names and technologies

- The user's own sources ground a name: CV and profile lines, kept knowledge
  snippets, dated entries and the contact block (`Manifest.userTexts`). The
  posting never grounds one. The target role and company are not sources: `withoutReferences`
  blanks the places a letter names them, so "I have built Kafka pipelines" fails
  under the role "Senior Kafka Engineer" while "apply for the Senior Kafka
  Engineer role at Example Corp" passes, and so does a role ending in a bracket
  ("Engineer (Remote - US)"). A partial mention of the role is
  checked like any other text. The role's spellings are the full text, the
  text without a page title's trailing "Application", "Opening", "Posting" or
  "Vacancy", the part before a separator, and a part after one that ends in a
  job noun ("Flexi - Junior Full-Stack Software Engineer Application" is also
  "Junior Full-Stack Software Engineer"; "Platform Engineer - Kafka" never makes
  "Kafka" a spelling of the role).
- A word of the role (`roleTitle`, without the company segment) that no user
  source holds is watched outside the role's name: under "Junior Full-Stack
  Software Engineer", "I bring full-stack experience" fails for a backend CV.
  The seniority word, the job noun and the posting's generic words
  (`genericWords`: "position", "job", "opportunity" ...) are not watched, so a
  general-pool title ("Submit Your Resume for Future Opportunities at ...",
  Alnylam) leaves a closing's "welcome the opportunity" alone.
- A watched word the posting spells only in capitals (`caps`: "AM" for
  additive manufacturing, "NDE") is named by a draft only in capitals: the
  lower-case "am" of "I am writing" is another word (Relativity Space).
- Matching is by whole token (`key`): Java is not found in JavaScript, Go not in
  good, Rust not in trust. C++, C#, .NET and Node.js are single tokens, and a
  verb prefix joins its verb (`Co-led`, `Re-architected`).
- A token fails when it is a name that no user source holds. Names are:
  capitalized words in mid-sentence, camel case, acronyms, tokens with a symbol,
  a dot or a digit, and the first word of a sentence unless it is a function
  word, a verb (`verbLike`, which strips `co-`, `re-`, `pre-`, `de-`, `un-`), a
  stock opener such as `Responsible`, or an unwatched word the posting writes in
  lower case ("Relational data modeling").
- Lower-case technologies are caught by a watch list: the names the posting's
  requirements, nice-to-haves and keywords use in any case (a word whose only
  mark of a name is the capital that opens a line, "Production experience",
  "Track record", or the capital of a keyword, which is read alone, is not
  one; it was the cause of 4 of 12 refused letters in the measurement below;
  nor is a word whose only mark is a capital and that is plain English, written
  in lower case elsewhere in the posting or with an ordinary ending: "New
  Jersey" and "Computer Science, Engineering" refused 4 of 8 TixTrack drafts
  for "new" and "engineering"), the lower-case
  single words the posting lists after "with", "in" or a comma (`listedTech`;
  a word with an ordinary English ending is not a product), and `builtinTech`.
  A watched token in any case must be held by the user's sources. A lower-case
  name that is in none of those lists and has no digit, dot or inner capital
  cannot be told from an ordinary word, so the floor list is the limit of this
  check; extend `builtinTech` when a product slips through.
- A skills line (competency) lists an item only when a selected evidence line
  holds its words side by side.

## Company facts in a letter

`slots.go`, `reference.go`. A letter has four parts: opening, evidence
paragraph (body and bullets), company paragraph, closing. The model never
writes the company paragraph: it lists 1 or 2 ids in `companyFacts` and
`companyParagraph` prints the lead-in of the letter's language (`lang.go`:
"What draws me to {Company}:", "Was mich an {Company} anspricht:", ...) plus each
fact's text word for word (`buildCover`, `letterMarkdown` and the word count use it). Nothing
reads model-written clauses to decide which are "about the company"; six review
rounds showed that cannot be made sound.

- Company facts come from the company briefs the knowledge base retrieves for
  "company role". Retrieval is lexical, so a brief is kept only when its text
  names the company or its file is named after the company's domain
  (`briefAbout`): the demo's 1Password brief came back for Ramp and Flexi on
  the words "Software Engineering". The interview pack reads briefs the same
  way. With no eligible fact the prompt says `companyFacts` must be empty (a
  model invented "C1" when the rule mentioned facts that were not listed).
- Everything the model writes is the applicant's text. Figures and names rest on
  the user's sources alone (`checkLetterOwn`). No company fact grounds a
  model-written word. A typed `[[` marker is `bad-slot`.
- A fact is eligible (`slotEligible`) only without a first-person or
  second-person word and without a word that describes the wanted candidate
  (`candidateRE`). The prompt lists only eligible facts. At most 2, no repeats,
  unknown ids fail.
- Before the facts nothing is checked for back-references. `checkBeforeFacts`
  reads every sentence of the opening, the body and the bullets for a forward
  pointer (`pointsAhead`: below, following, as follows, further down, after
  this, and next or later after a word of showing or telling or before a layout
  noun) and rejects it when the sentence is the applicant's own: a first
  person, a self-reference or a bullet. "Examples are below." has no one in it
  and passes. "My next role", "work on next" and "I later became lead" are not
  pointers. A last sentence that ends in a colon is rejected the same way.
- In the closing, an applicant's-own sentence (first person, no stated subject)
  is rejected for a closed-class pointer (it, they, there, so, pronoun uses of
  this/that, same, such, "so did", "did too") and for reusing a name of a chosen
  fact (`refersBack`; a technology, product or proper name by the rules of
  `nameKey`, whole token; ordinary words and figures are free). this/these/those before a noun other than role,
  position, job, opening, opportunity, posting, letter or application is
  rejected for any subject. A plain noun of a fact ("I know the platform inside
  out") and a synonym of a fact word are accepted residuals, since neither
  carries a name or a figure; a relative "that" next to a first person is
  rejected.
- A fact pasted into the text fails like any unsupported claim; the finding adds
  `company-fact-without-slot` and says to list the id in `companyFacts`.
- The review path (`screenFinding`) runs the same checks on an edit. The
  deterministic letter chooses the first eligible fact. Résumé content never
  quotes a fact.
- The deterministic letter's three bullets are the résumé's evidence lines
  sharing the most terms with the posting's requirements, then with its
  responsibilities (Quillfern lists "Go on PostgreSQL" as a duty); on a tie a
  CV achievement goes before a profile summary of the role. Prose after a
  posting section's list ends the section, so "Salary $125,000" or "on-site,
  five days a week" is not a requirement. A number is not a shared term: the
  "000" of "$400,000" does not match "3,000 robots".

## Statements only the user makes

A letter's own text may not state relocation (`relocRE`; the system adds the
relocation sentence from the profile) or the right to work (`authRE`:
"eligible to work", "authorized to work", "work permit", "sponsorship", "green
card", "citizenship", "permanent resident"); both fail with their own code,
in a draft and in a review edit. A measured draft wrote "I am eligible to
work in the United States and based in Atlanta, Georgia" because the posting
asked for it.

A body that ends in a colon with bullets under it introduces them and is not a
cut-off paragraph; with no bullets under it, it is.

## Quantified claims

The principle: any token that states a magnitude, a count, a ratio or an
approximation is a quantity, and an unplaced quantity is rejected. Recognized
(`quantfigures.go`, `quantvague.go`): digit numbers with a currency sign, a
magnitude suffix (`5K`, `2.5M`, `3 billion`), a percent sign or word, a
duration (`ms`, `s`, `min`, `h`, scaled to milliseconds) or a counted noun;
number words (zero to a billion, `twenty-five`, `two hundred`); ratio words
(`halved`, `doubled`, `twice`, `fivefold`, `3x`, `three times`); fractions
(`half`, `a third`, `two thirds`, but not `third-party`); `a dozen`, `two
dozen`; count words (`dozens`, `hundreds of`, `several`, `many`, `multiple`,
`numerous`, `countless`, `a few`, `a couple`); intensifiers (`significantly`,
`dramatically`, `massively`, `substantial`, `an order of magnitude`); and the
approximation before a figure (`about`/`~`, `nearly`, `over`/`5K+`). Digits
inside a name (`p99`, `S3`, `ISO-27001`) or a version are not figures.

Bold markers are removed before figures are read: "**forty** product teams"
is "forty product teams" (3 of 6 measured model résumés lost bullets to the
markers). A preposition or conjunction after a figure is never its counted
noun ("from 31 to 9 through incident review").

Before a counted figure, "used by", "serving", "adopted by" and "relied on by"
say whom the work is for, as "for" does (`usageVerbs`), so "a pipeline used by
forty product teams" rests on "forty product teams now ship on their own"; the
counted noun must still match and any other verb still needs its evidence. A
rate is read only up to the next clause or phrase word: "mentored four
engineers through weekly incident review" states no weekly rate.

A count of the letter's own parts is not a claim (`discourseCount`): a vague or
small count is a claim only when it is bound to a measurable subject. "A few
examples", "two reasons", "three results" and the like (example, point, reason,
paragraph, thing, way, item, bullet, section, question, result, highlight, outcome, pair, pass, body, sentence, span, requirement, also after one modifier, and "bullet 3"), a vague count
with no noun ("a few are below") and "the first" that stands alone or ranks a
part of the letter or a stretch of time ("the first example", "the first week")
pass. "A few services", "two teams", "cut incidents by a few", "improved the
score by 3 points", "I was the first" and "the first to ship" still need
grounding.

Superlatives and firsts are quantities of the same kind (`rankFigures`): "a
first", "first-ever", "the only", "#1", "number one", "new/company record",
"record-breaking", "best-ever", "best-in-class" pass only when an evidence line
says the same. Each is a phrase, so "first-party" and "kept a record of" are no
claim; a bare "record uptime" is not caught (the noun is too common).

A quantity is supported (`quantmatch.go`) only by a quantity of one evidence
line with:

- the same value after normalizing the spelling: `$1.2M` is `1.2 million
  dollars`, `5 h` is `5 hours` and `300 min`, `%` is `percent` and `per cent`,
  `3x` is `three times` and `3-fold`, `half` is `50%`; a range shares its unit
  ("from 12 to 4 minutes");
- the same unit, where a measure (`%`, `x`, a duration, a currency) never
  agrees with no unit, and a counted noun (`12 services`) is its own subject;
- the same rate: `a year`, `per year`, `annual` and `/yr` are one rate, and
  `$1.2M` is not `$1.2M a month`;
- the same approximation: dropping a "plus" only weakens a claim, dropping
  "about" or "nearly" does not;
- for a vague word, the same word (the adjective and the adverb of one root are
  one word): an intensifier is rejected unless the evidence line uses it;
- the same subject: the head noun of the claim equals the head noun of the
  evidence clause (the noun nearest the figure before it, the last noun of the
  phrase after it; a noun behind an earlier verb counts last), the verb class of
  the claim is in the evidence line (`cut`, `reduced`, `removed` and `saved`
  are one direction; `built`, `developed` and `added` are one class; `mentored`
  and `coached` are one; an irregular past is its verb, so `held` is
  `holding`; support verbs such as `delivered`, `made` and `backed by` carry no
  claim; a noun of change is read like its verb, so "a 22% improvement in
  processing throughput" is about throughput, and since it names no direction
  either direction restates it), and a modifier the claim adds is allowed only
  when the evidence line or a user source uses that word;
- the direction of change (cut against increased) must not be opposite.

The summary alone relaxes the subject: a result with no named subject ("saved
$1.2M per year") passes on any evidence line that holds the figure, rate and the
claim's verb class or direction. A bullet still names its subject. A summary may
also open with a role noun from the profile's target roles (`TargetRoles`,
`summaryNames`); a role noun no source holds is rejected, and a target role
never makes a technology on the floor list known.

When the gate is unsure it rejects, and the repair or the deterministic builder
drops the line. "One" counts only where it is a count, and a count the system
itself writes in prose is avoided ("Examples ..." not "Two examples ...").
Known limits, all on the rejecting side: a subject named before a comma
("Improved latency, from 800 ms to 120 ms"), a head noun dropped from its
compound ("shrank the image" for "image size"), a verb outside every class
("rewrote" for "migrated"), and a counted noun behind a coordination ("12M+
events" for "12M+ gameplay and solver events", whose counted noun reads as
"gameplay"; seen in an owner-workspace letter, 2026-11-08) drop a line that a
person would accept.

## Replacing a rejected part

`replace.go`. A model résumé that fails the gate is not thrown away.
`replaceRejected` gates each part with the same checks as `checkResume`
(`resumeGround`): a rejected bullet becomes the selected evidence line it cites
(same entry, clean shape, not already in the entry), a bullet with no usable
source is dropped, a rejected summary becomes the summary of the user's sources,
a rejected skills line is dropped, and a selected project the draft left out is
filled from its source lines. The result is gated again as a whole, one warning
per swap is recorded, and the generation stays `model`. A draft with no
surviving part, or a structural fault (unknown entry, duplicate entry), still
falls back to the deterministic builder. Letters are not repaired this way: their
prose has no source line, a removed clause changes the argument, and the spec
fixes regenerate-once-then-deterministic.

## False-rejection measure

`grounding_fixture_test.go` holds a synthetic CV (Alex Example) of 22 lines and
46 legitimate restatements of them (reordered, with a synonym verb, another
spelling of a unit, an extra modifier the CV uses elsewhere). The test fails
unless all 46 pass and every CV line grounds itself. The first run of the first
set passed 39 of 46 (85 percent); the misses were generic and were fixed in the
gate, not by listing the bullets. A second set written afterwards without
looking at the gate passed 26 of 35 on its first run. The test keeps 34 of
them: 28 pass, and 6 stay rejected as the known limits above (an unsupported
modifier "production incidents", a subject before a comma, a head cut from its
compound, "rewrote" and "sped up" outside every verb class, "report generation
time"). The test fails if one of the 6 starts to pass, so the limits stay
visible.

## Warnings

`evidence-not-reflected`: a selected evidence line of which the draft holds fewer
than the smaller of 40 percent of its distinctive words and 6.
