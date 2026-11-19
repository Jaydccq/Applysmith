# Answer bank

Implementation notes for `internal/answerbank` (task 3.2). The normative text is
`specs/answer-bank`; this file says where things live and records the choices
the spec leaves open.

## Layout

| Package or file | Concern |
|---|---|
| `answerbank/match` | the matcher's entry model: kinds, categories, input types, per-host overrides, load-time stamping, the needs-answer and stale rules. Stdlib only, no I/O |
| `parse.go`, `validate.go` | load `answer-bank.yml` into a `match.Bank`; refuse a bad save. A file-level failure is a `*LoadError` (file, key path, line); an item-level one is a warning and drops the item |
| `encode.go`, `ops.go` | a comment-preserving merge that never persists runtime stamps; the revisioned edit operations |
| `service.go` | `Service`: `Seed`, `File`, `Bank`, `Save`, `Unanswered`, `Evolve`; one mutex serialises every read-modify-write. Every save, by any writer, clears `placeholder` on an entry holding an answer (ask-me is not one): the flag only lets an entry stay blank, and a placeholder is passed over as evidence and as a knowledge fact |
| `profile.go` | profile reads: preferences, the setup statements the load stamps (`statementsFrom`), canonical links |
| `settings.go` | runtime settings in the `workspace_meta` row `answerbank.settings`, not the bank file, so sharing a bank never carries them: the answer-bank switches (`llmFallbackEnabled`, `groundingMode`, `autoAcceptLegalDeclarations`), `allowGoogleSignIn`, `watchScans` for the browser scan window (off by default; [browser-scans.md](browser-scans.md)) and `knowledgeSemanticRetrieval` |
| `defaults.go`, `defaults/answer-bank.yml` | the embedded default bank: generic content only, personal entries are blank placeholders except the owner's defaults (decisions 2026-11-01/02: `relocation-assistance-needed` Yes whenever the role means relocating, No when the applicant is local or the role is remote; `sat-score`, `act-score`, `gre-score` N/A in a box, the list's own did-not-take choice, never a score range; `ownerDefaults` in `defaults_test.go`); `withDefaults` applies it to every load |

## Seeding

`workspace.Init` does not write `answer-bank.yml`; the default bank belongs to
the answer-bank service, which `workspace` (a lower layer) cannot import.
`Service.Seed` creates the file only when absent, never overwrites, and runs
from `launch.Start` (server and desktop), `applysmith init` and the onboarding
workspace step. Every load applies the defaults in memory (`withDefaults`,
flow round 11), so no press or command brings them in: a default entry whose
id the file lacks follows the file's entries, an existing default entry takes
newer examples or input types only when every listed item is the default's
own, and a still-blank placeholder takes the default's answer (with its
`needs_review`). A shipped entry or answer is marked `Shipped`, so it is never
the user's own (`UserWritten`), and nothing the user wrote changes. A load
never writes the file; a save writes an entry the defaults changed and its
writer did not as the file holds it, or not at all (`asWritten`), so only an
edit makes one the user's, and `Evolve` appends a shipped entry that goes
stale. As no save follows, the server's first start with a new set of
defaults (`moveForDefaults`, their digest in the `workspace_meta` row
`answerbank.defaults-told`) tells each waiting run whose questions they answer
that it can be retried, choosing the runs as `MoveAnswered` does, and resumes
none, since every tab reads as kept until the browser is read. A waiting run's questions, on its run's page, in `asked` and in `run_questions`, route with the profile's facts, as the fill does (`learn.Options.Facts`), but without the knowledge base; one routed to no entry or to a skill gate is left out when the fill's own search (`bind.Binding.OwnSearch`, `api.unsettled`) finds none of what it asks about and it has a No to give (`apply.AbsenceNo`), since the fill answers it No (F-10). A default the user deleted is back,
as shipped, at the next load; a list the user trimmed to some of the default's
own items takes the whole list again.

Default entries: employer-history answers Yes/No from the employers list and
history (`employerHistoryEntry`), also into a text box asking it; a government
question is left to the government entries. `job-source`, `referral-source`
and `pronouns` accept `text` beside `single_select` (Lever's "How did you hear
about <company>?", Ashby's pronouns box; only a user-written fallback is
typed); `sponsorship-type-description` accepts `long_text` (Pinterest's
"please list the type of support").

## Decisions

- **Who the defaults are for** (owner 2026-11-16, after a review for
  applicants unlike the owner): the product's users are mainly finance,
  life-science and computer-science applicants, so degree, STEM, credential
  and role-years reading cover those fields, and US citizens and permanent
  residents are not asked what their status settles. Applicants outside the
  United States applying at home, and clinical, teaching and trade
  credentials, are not a target yet. The owner kept, for every user, the
  rulings that an unrecorded experience Yes/No answers Yes for review, an
  unrecorded number of years is one, a self-rating takes the least tier,
  relocation assistance defaults to Yes and SAT, ACT and GRE default to
  not taken.
- **A preferred channel never picks a person** (owner 2026-11-13, The
  Clearing House: "Saw on personal LinkedIn feed" then asked whose post it
  was). For `job-source` and `referral-source`, a preference that names no
  referral skips an option naming a person who referred or reached the
  applicant (`personSourceRE`: referral, current employee or worker, friend,
  recruiter, a personal feed); the list goes on to the next preference, then
  to Other. A preference that names a referral still takes one.
- **Proof of work authorization is the authorization** (owner 2026-10-27:
  "can you provide proof" and "are you authorized" are one thing). The
  recorded work-authorization answer also answers "If hired, can you provide
  proof that you are legally able to work in ...?" and "Can you show proof of
  your right to work?" (`match` `workAuthPolarity`); a trailing note that does
  not speak to the applicant is set aside.
- **The source question never stops a run** (owner 2026-10-27: "just pick
  sensibly, the How Did You Hear question does not matter"). When "How did you hear about us?"
  does not offer the answer, a near option is taken as a review fill: the
  category that holds it (social network, job board), else a bare Other, else,
  for an online answer, the company's own website, then "internet / online"
  (`match` `sourceChannelIDs`). A Workday referral menu whose categories hold
  "No Items." is left after `openWaitMs` per category, and the engine searches
  Job Board, Website, Internet, Other in turn (`engine/src/apply.ts`
  `SOURCE_FALLBACK`).
- **Needs an answer.** One rule (`match.NeedsAnswer`): an active entry with an
  empty body, not blank by design, not a satisfied conditional slot, not
  ask-me. `Blocking` is the subset in identity, contact, address,
  work-authorization, employment, education, compensation and
  self-identification, minus `-reason` slots, the `years-experience-` prefix,
  entries accepting `long_text` and `optionalIDs` (`address-line2`: owner
  decision 2026-10-30, the second address line is optional and the county
  stays required; the clearance entries `willing-to-obtain-clearance` and
  `clearance-citizenship-gated`, `under-18-work-permit`,
  `countries-lived-outside-us` and `lived-outside-us-12-consecutive-months`
  concern few applicants, and `live-in-listed-states` is answered from the
  address when a form asks it, so none of them stops setup for any status). `GET /api/answer-bank/unanswered` and
  `applysmith answer-bank status` serve it. Besides `-reason` slots,
  `conditionalSlots` (`bank_needs.go`) names entries that apply only after a
  Yes gate: "pursuing" and expected graduation (`currently-enrolled-student`),
  STEM OPT (`currently-in-opt`), `return-to-us-before-start`
  (`currently-located-outside-us`), clearance status and agency
  (`security-clearance-history`), `pr-another-country-after-citizenship`
  (`citizenship-in-another-country`), `sponsorship-type-description`
  (`h1b-sponsorship`); `work-authorization-expiry`, `held-h1b-past-6-years`,
  `ever-held-j1-status` and `active-immigration-case` apply unless
  `permanent-work-authorization` is Yes (a naturalized citizen or a green-card
  holder may once have held H-1B or J-1 status, so their status states no
  answer: setup does not ask them, and a form that asks leaves them to the
  user). A slot whose gate is answered the
  other way is finished while blank; an unanswered or ask-me gate keeps it
  listed, and it blocks only once the gate says it applies: an ask-me gate
  leaves both to the application, and a blank gate that blocks is asked first
  and still stops setup (`openGate`).
- **Stamping.** Profile preferences (numeric thresholds, location policy) are
  stamped at load, never written back. A `salary-expectation` holding a figure
  but no currency (setup's answer-bank step writes the figure alone) takes the
  targets step's pay currency, so a question naming that currency is answered
  instead of abstaining `local_currency`; a save writes the currency blank
  again (`StampedCurrency`). A days-a-week question above the
  stamped office-days maximum is answered from it even with a blank body; one
  within it is not; with no maximum nothing is.
- **Setup statements.** A fact the user states in a structured setup step is
  never asked again as a bank entry (`statementsFrom`, `profile.go`). The load
  stamps it onto the matching entry while that body is blank; the entry
  carries `DerivedFrom` (the step id), a save never writes it, and
  `GET /api/answer-bank/unanswered` lists it under `derived` (the page shows
  "From Setup" with the step). A user answer or ask-me replaces it; a later
  step change is followed. Nothing is copied into the file: a copied "needs
  sponsorship: Yes" would outlive a step change on a signed application, and a
  copied email went stale the same way. Profile reads are lenient (no schema
  validation), so a link without a scheme is still canonicalised. Derived,
  only from a value the profile holds:
  - identity name, preferred name, email, phone, street, second line, city,
    county, postal code: their fixed entries as written (a recorded `@ask-me`
    leaves the entry to the user); links: the link entries, canonical
    (`https://www.linkedin.com/in/<slug>/`; others gain `https://`); state and
    country: rank the `address-state` and `address-country` pick lists (a US
    state as "North Carolina" and "North Carolina (NC)", the country's English
    name); residence country: where the user is based and lives, and the
    work country (the fill answers a blank one with it); a phone written with
    its `+` code: the dialing-code list (a US or Canadian address with `+1`
    names the country too);
  - the most recent role's title (`present` first, else latest end or start
    month): current job title; a role ending `present` (one employer): current
    company; any role ending `present`: currently employed Yes;
  - authorized countries, status, the two sponsorship answers: `work-auth-us`;
    `h1b-sponsorship` from now-or-future by the matcher's rule (a true fact
    decides Yes, two false ones No); citizen or permanent resident with the US
    as the only country: permanent, US person and any-employer Yes, and
    `us-citizen-status` the matching choice (a citizen also "active
    immigration case" No; a permanent resident may have one, such as a
    naturalization, so it is not stated); work visa
    or student status: permanent and US person No; a visa class held in the
    applicant's own right: any-employer, unrestricted and spouse-based No,
    H-1B the held-H-1B question; "OPT"/"CPT" wording: the practical-training
    questions; the status answers `currently-employer-sponsored` by the
    matcher's rule (citizen, permanent resident, student or not authorized
    No; a company-sponsored visa class Yes); a security clearance of `none`:
    the clearance level; a student authorization's end date
    (`student_work_authorization: 2027-06-30`): the authorization expiry;
    recorded citizenships: whether one is Cuban, Iranian, North Korean or
    Syrian (export control, owner decision 2026-11-09);
  - dated education: enrollment, most recent school, its grade average, the
    bachelor school and its field (the undergraduate major), the most recent
    field (field of study), a graduate's graduation month and "did you
    graduate" Yes, the highest completed degree's level, and a STEM degree
    Yes when a completed field is on the DHS STEM Designated Degree Program
    List (the July 2024 edition; `stemDegreeFieldRE`: the sciences, biology
    and its branches, engineering branches, operations research, actuarial
    science, financial mathematics with its synonyms quantitative finance and
    financial engineering, econometrics), never a No: general finance,
    accounting, business and economics are not on the list and stay the
    user's. The list is wider than the fields a question routes to
    `has-stem-degree` by (`pickStemField`), so the stated Yes answers "a STEM
    degree?" but not a question that names fields unless a recorded field is
    one it names (`stemFieldsNamed`). Pay currency: the currency
    question. The fill reads the major, field and level from the same rows
    (`fieldOfStudy`, `userEducationLevel`).
    Setup's CV step stores a degree's field apart ("B.S. Computer Science" is
    degree B.S., field Computer Science). A degree's level is read from the
    level words, else from the abbreviation the degree field opens with
    (`degreeNameLevel`): BBA, B.Com, B.Tech, B.E., B.Sc., LLB; M.Phil, M.Res,
    MFin, MAcc, MSF, MFE, MPH, M.Tech, M.Sc., LLM, M.E.; PharmD, MD, JD, DVM,
    DDS, EdD. A professional doctorate is stated as Doctorate (the bank's
    level choices bundle no professional rung) but never as "Ph.D.": the
    doctorate's "Ph.D." label is preferred only for a degree naming one, and
    the fill's ladder (`degreeRung`) does not place a doctorate written as
    initials, so it never picks a "Ph.D." rung or answers "Do you hold a PhD?"
    for one; the ladder also stands aside while a completed degree is unread
    (`highestEducationLevel`, `educationUnread`) rather than answer a lower
    rung. The abbreviations are read only in the degree field, and the
    two-letter ones only in capitals or with dots, so "be", "me" or "md" is no
    degree; MBBS stays unread. Question text and option labels keep the older
    rule, which still reads "MA" and "MS" (as in "Boston, MA") as a master's
    (`TestStatementsFrom_DegreeAbbreviationsAndDesignatedSTEMFields`).

  At match time a blank `us-person-itar-ear` is derived as well: a user who
  needs sponsorship now or in the future is not a U.S. person (citizens,
  permanent residents and protected individuals need none); no sponsorship need
  decides nothing. The user's own Yes or No wins. The same status answers an
  unrouted picker whose options state it, such as Greenhouse's "EXPORT
  COMPLIANCE" (`resolve_disclosure_export.go`). A bare Yes or No follows the
  question's polarity (`usPersonPolarity`): asked about the opposite status
  ("Are you a non-U.S. person?", "a foreign person"), the answer is the
  complement; a question that names both statuses, or negates before the one
  it names, is left to the user. A note beside the question turns nothing.

  A blank `currently-employed` ("Are you currently employed?", which names no
  employer) is Yes when a `history.employment` position has no end
  (`deriveCurrentlyEmployed`, `match/resolve_profile_emp.go`); a history whose
  every position ended is left to the user, and the user's own answer wins.

  `us-citizen-status` holds the applicant's U.S. status: a U.S. citizen, a U.S.
  national, a lawful permanent resident, a refugee or asylee, or neither, and
  the page offers each as a choice (`defaults/answer-choices.yml`). It
  sits after `country-of-citizenship` with `us-citizen-lpr-asylee-refugee` and
  `us-person-itar-ear`, and answers the export-control lists of U.S. person
  classes (`ownUSStatus`); a bare Yes or No answers citizenship only. A
  question that names other countries and not the United States ("Are you a
  Canadian citizen or permanent resident?") is none of these three entries':
  they abstain `no-entry` there (`foreignStatusGuard`), which is not terminal
  (`TestBind_Preview_ApplicantsUnlikeTheOwner`).

  The owner's blanks (owner decision 2026-11-09): a blank `google-scholar-url`
  or `publications` answers a Yes/No question No and a required box "No"; an
  optional box stays blank (`deriveBlankIsNo`,
  `match/resolve_owner_answers.go`). A required portfolio box with no unused
  link of the user's left is "No" too (`requiredWebsite`). A blank
  `competing-offer-deadline` is "N/A" while `competing-offers` says No, and
  setup asks it only once `competing-offers` says Yes (`conditionalSlots`).
  `salary-range-basis` (base pay or on-target earnings) ships blank like every
  new entry.

  Owner decisions 2026-11-09, later: `occasional-night-shifts` answers night
  shifts asked of the role (the owner's Yes); which shifts one works stays
  `shift-availability`. `company-types-worked-at`, `product-types-worked-on`
  and `content-types-created` hold the kinds of companies, products and
  content of the user's experience, the owner's selections written from the
  history. "When would you ideally be open to your next opportunity?" asks the
  notice period (`nextOpportunityTiming`): a notice that ends before the
  soonest later window ("in the next 3-5 months") is the option of looking
  now.

  Never derived: date of birth (age questions only), any self-identification
  answer, the pay figure (the user's disclosure decision; the original
  excluded it too), pick lists whose labels need judgment (education level,
  citizenship country), anything from a status the classifier cannot name.
  `history.employment` employers are not copied into the bank's `employers`
  list; the matcher reads the profile on every match. The original copied
  identity values after a preview (`profile-derived-answers.ts`, "a wrong
  guess here goes out on a real application"); each line above is a
  restatement, not a guess. Setup step 8 counts
  `NeedsAnswer(stamped).Blocking()`, the list `GET /api/answer-bank/unanswered`
  serves the page, so both are one number. Measured 2026-10-17 (fresh
  workspace, steps 1 to 7, fictional H-1B persona): blocking went 83 to 50 on
  the step and 92 to 59 in the bank (identity copies counted on one side only);
  with identity stamped the step, `applysmith answer-bank status` and the page's
  "Blocking only" filter all read 50.
- **Sponsorship wording.** The closed polarity reading sets aside what carries
  no polarity: a parenthesised `(e.g., ...)` list of visa categories only (F1,
  H-1B, OPT; a "transfer" or negation keeps it, leaving the question unread);
  the company's "we do not sponsor" notice sentences (one that asks,
  conditions or addresses the applicant is kept); a capitalised title run into
  the question ("US Lawful Worker Required Are you ...", Teamtailor). Fixed
  phrases read as one: "do you currently require, or will you need in the
  future", "sponsorship or other employer assistance", "obtain, extend, or
  maintain", commence ("sponsor") an immigration case ... to employ you; "to
  remain in" reads like "to maintain"; "your current authorization" is the
  one held ("Does your current authorization require sponsorship in the
  future?"). A list that names a transfer or a renewal beside the sponsorship
  ("renewal or sponsorship") is answered only for a recorded need, which is
  one of the list: Yes; a recorded No says nothing of the renewal and is left
  unread. The answer still comes only from the two recorded sponsorship facts. A Yes split by visa (GitLab: "No" and six
  "Yes, <visa>" options) arrives as a single select; `sponsorship-now-or-future`
  takes it (`yesSplitByDetail`: one No-led option, two or more Yes-led, nothing
  else), a recorded No picks the lone No, and a recorded need is left to the
  user, who knows which visa.
- **Work-authorization status pickers.** On `us-work-authorization-status` and
  the `work_authorized` pickers, when no preferred value reaches an option the
  recorded sponsorship fact picks the one option that states it, for review:
  the only option stating a requirement when sponsorship is needed, the only
  authorized option stating none when it is not. "Authorized ... without any
  renewal or sponsorship" restates the authorization, so it is not read as a
  negation of it; an unset fact picks nothing.
- **Time-zone range question.** "Are you located within a ... timezone?
  (Between GMT-7 and GMT-3)" as a Yes/No is answered from the stored
  `time-zone` (`timeZoneRangeStage`): Yes when every offset the US zone
  observes over a year is in the range, No when none is. A zone in the range
  for only part of the year (Pacific time against GMT-7), a value that names no
  single US zone and a blank one are left for review, never guessed.
- **Captured scope.** Host folding is the matcher's `foldText` (NFKC,
  whitespace collapse, lowercase) plus a trailing-dot strip. A scope item
  without host or question is kept and admits nothing: an unreadable scope
  fails closed.
- **Ids and old keys.** The default bank keeps its ids (routes name targets
  by id; `answer_key` and `category` are metadata). Old-format keys
  (`starter`, `value_kind`, `allowed_options_file`) load as a warning and are
  ignored; no curated list file ships; free-typed values are allowed. An
  unknown `state` loads as `active` with a warning; a save refuses it.
- **Context-policy details.** The terminal `context-policy` abstain names its
  guard in `detail`: `posting_country` (work authorization or sponsorship for
  a country other than the recorded one), `local_currency` (a named currency
  or "local currency" the entry does not use; example figures in several
  currencies name none and hold the entry only to a posting known to use
  another currency), `held_compensation`,
  `employer_detail`, `sponsorship_conjunction`, `disqualifying_disclosure`,
  `marketing_opt_in`, `legal_declaration_off`, `application_statement`,
  `counterparty_employer` (whether the applicant or a family member works for
  a client, prime contractor or partner of the employer; only the assist
  lane's cited research on the user's employers answers it, `textlane.go`
  `counterparty`), `residence_claim` (a question that asks where the
  applicant lives whose every Yes names a place the profile's home is not). A
  programme no user entry names is an open `no-entry` question: the evidence
  lanes and the F-10 search of the user's information answer it. With
  semantic retrieval on, `bind` also runs `kb.Search` for each question about
  the user's own experience and passes the snippet sections its semantic stage
  ranks near it (`Facts.KnowledgeNear`), which count as a mention (F-10).
- **A posting open in several countries.** Work-authorization questions on
  "US or Canada" stay `ambiguous`. Only the sponsorship need is answered, for
  review, when it is the same in every country the location can mean
  (`uniformSponsorship`, `locationCountries`): a recorded authorized country
  takes the recorded need, any other needs sponsorship, one must be recorded.
  On Mercury's "SF, NY, Portland, or Remote within Canada or United States" an
  F-1 OPT applicant answers Yes; a US citizen stays ambiguous (owner decision).
  A town the table does not know before a two-letter code ("Marina del Rey,
  CA", Zefr) is in a country the code can mean, the United States or Canada
  here (`codeCountries`).
  A location that names North America and otherwise only a work arrangement
  is the United States, Canada and Mexico for this read (`northAmerica`). Any
  other region, like a location that names no place, takes only a recorded
  Yes, which is the same in each of its countries ("Americas Remote",
  Customer.io); with a No it stays `ambiguous`.
- **Gates that leave the question to the composer.** The long-form fixed
  answer, named-network (LinkedIn or GitHub) and short-answer-shape gates
  abstain `no-entry`, not `low-confidence` or `input-mismatch`, so the composer
  may still take it. A fuzzy tie abstains `ambiguous` and skips the
  acknowledgement-by-option rescue.
- **Tokens.** Fuzzy and coverage scores split the normalized label (NFKC,
  entities decoded, lowercase) on non-letters and non-numbers; tokens under
  two characters or in the fixed 70-word stop list are dropped; stemmer length
  cutoffs count characters, not bytes (`TestIntake_StemmerBoundariesAndStopList`
  pins list and cutoffs).
- **Acknowledgement negatives.** Option scans and sole-option shapes read
  normalized contraction tokens (supported entities, compatibility quotes,
  zero-width layout characters). A negative within four tokens before an
  acknowledgement blocks it in both paths. Agreement with a later negative ("I
  agree not to use AI tools") still fills; an affirmative lead sets scope
  before a later negated verb. Plain negative restatements cannot return
  through a shape. Stance/polarity readers use the same expansion, including
  layout inside contractions, without deleting ordinary BOM word boundaries.
  The engine's automatic legal sweep separately checks the box's own
  native/role label before combining an affirmative legend; explicit bank
  writes, inherited checks and user edits keep their paths.
- **Own answers beat the marketing rule.** `marketing_opt_in` refuses only the
  matcher's own acknowledgement entries; a user-written fixed or pick-option
  entry answers an optional marketing question as written.
- **Places.** A city the metro table does not know beside a state ("Round Rock,
  TX") is its own place: home only when the policy says so, never because its
  state is the home state. A metro alias that is also a state's code or name
  (NY, LA, New York, Washington) is the state when it stands after a city and
  a comma, so "Buffalo, NY" is not New York City and "Baton Rouge, LA" not Los
  Angeles (`namesMetro`); DC is Washington wherever it stands. The state or
  country a licence, registration, certification or bar admission was granted
  in, or a document issued by, is not the address (`guardCredentialPlace` on
  routes 507, 513 and 515): "In which state are you registered with FINRA?",
  "State of licensure" and "Which country issued your licence?" are left to
  the user (`TestBind_Preview_ApplicantsUnlikeTheOwner`).
- **One first day to start.** Every start lane reads the same day: the typed
  `earliest-start-date`, the lead-time options and text of
  `full-time-start-availability`, the start window, the start period and the
  apply check's month-and-year box (`apply.AvailabilityMonthYearAnswer`). It is
  `StartDate` of the stored start (blank or passed: thirty days from today),
  moved to the last day of any education entry that has not finished
  (`AfterEducation`, `match/resolve_derived_window.go`); an education entry
  without an end month leaves the question to the user.
- **Residence and degree from the profile.** Two derived lanes answer a Yes/No
  question from the profile before any entry is classified, and stand aside
  when the profile is silent so the user's entry decides. US residence
  (`usResidence`): the whole question must be "Do you (currently) reside /
  live / are you located in the (Continental) United States" or "a resident
  of" it (a second place, willingness, time or "outside" is left to the
  routes); the address country answers, and the continental part (also
  "contiguous", "mainland", "lower 48") the state: the 48 contiguous states
  and DC Yes, Alaska, Hawaii and the territories No, a US address without a
  state abstains. Degree held (`degreeHeld`): "Do you have / hold a bachelor's
  (master's, PhD, associate, college) degree (or higher)?" from the highest
  completed degree; "from an accredited institution" is part of holding one;
  a trailing alternative ("or equivalent experience") is met by a degree, else
  left to the user; a second level ("Bachelor's or Master's degree") is part of
  the degree asked, the lowest counting, never such an alternative; a field of
  study, any other condition or no recorded education stands aside. Degree in a
  listed field (`degreeInField`): "Do you have a Bachelor's or Master's degree
  in <fields>[, or a related field][, with a graduation date between <month
  year> and <month year>]?" is Yes for an entry of a level named, whose field
  is one listed, ending in the window (or, with none, ended); anything else is
  left to the user, who judges what a related field covers (Gem, 2026-10-31).
  The graduation time-frame picker stands aside on a plain Yes and No.
  Graduation window (`graduationWindow`): "Is your graduation date between
  <month year> and <month year>?" or "Will you graduate between ...?" with Yes
  and No is Yes when the latest education end is inside the window, No outside
  it; an open entry with no end month stands aside (Agave, 2026-10-31). A bare
  "Graduation Date" box is the engine's row filler's (each row its own entry's
  end), never this lane's.
- **Applied-for country, work-from place, location.** A remote, hybrid,
  office or "outside" wording is an arrangement question left to other routes
  (for the located and work-country wordings, so are relocation, preference
  "would you prefer"/"do you want", and authorization).
  - "The country for which you applied" ("you have applied to", Workable's
    "eligible to work in the country you are applying?") reads as "the country
    you are applying to": the work-authorization entry for the posting's
    country, answered only when the location names exactly one country the
    profile records (Greenhouse's San Francisco, CA, New York, NY and United
    States is the US); several is `ambiguous`, none the terminal
    `posting_country` abstain. A posting with no location at all is presumed
    in the United States (`presumedInUS`, detail `posting_country_presumed`)
    when its title and description head name no other country and its title
    no region (owner decision, flow round 8). With exactly one country
    recorded as authorized, a location that names no country ("Remote", a
    place the table does not know) is placed in it unless the title or
    description head names another, and a location naming several countries
    or a region that lists it is too, for review (`recordedPlace`; the work
    facts and the sponsorship need alike). A question about the country an
    earlier answer picked ("that country") stays the picker's. Owner decision
    on the Datadog fill: an unknown posting country is answered, never left.
  - "From where do you intend to work?" / "Where will you be working from?":
    the city entry, widened to city, state and country like "Location"; an
    engine-appended description naming parts ("Please list city and state.")
    gets those parts ("Durham, NC").
  - Free-text "Where are you located?" ("Where are you based?", "What is your
    location?"): "City, ST" in the United States, "City, Country" elsewhere; a
    US place without a state abstains. Free-text "In which country will you
    work?" / "Which country will you be working from?": the user's
    work-country answer, else the residence country. Both are composites
    (step 4) and open the label; the city route (508) leaves located wording
    alone, so an option list ("Where are you located?" with United States /
    Canada / Other) and an eligibility sentence keep their routes, and route
    119 keeps "Where are you currently based?" as the country entry.
- **Questions the options ask.** Two form signatures (`classify_signature.go`).
  A "Where are you located?" ("where do you live / reside", "state of
  residence", "current state") select whose labels are US states (at least
  ten, four in five a state name or code) is `address-state`, so the profile's
  state picks its option in the list's form (`SC` picks "South Carolina" and
  the reverse); relocation, willingness, preference and licence wording is left
  alone. The signature owns its question (`signature.Owns`): a state the list
  leaves out, or a recorded shape cut to 25 options, abstains on
  `address-state` instead of reaching an unrelated entry by a later stage.
  The Lever list spells "West Virgina": a label that is the whole answer with
  one slip scores 0.85 (rule 12 of the score ladder), above the 0.75 of the
  shorter "Virginia" the answer contains (flow round 8).
  A bare "Work Authorization" heading with radios "Yes, I am authorized
  to work in the United States" / "No, I am not ..." is `work-auth-us`, from
  the authorized countries and still bound by the posting-country guard; an
  option adding sponsorship, an employer, a visa or the future is left to the
  routes.
- **A Yes/No pair is no evidence for a weak match.** Review lift (b), a clear
  winner among the options, does not apply to a Yes/No control after a fuzzy
  or topic classification (`finalize`): the option only restates the entry's
  Yes or No. On 268 hand-labelled corpus rows of the owner's bank: wrong fills
  4 to 3 ("appropriate work authorization" from permanent work authorization
  was one), at the cost of 9 lucky right fills now `low-confidence`, which
  entry recognition (Model-assisted lanes) mostly wins back with the matcher's
  guards intact.
- **Route fixes from the labelled corpus.** Rules-only baseline 68 right / 6
  wrong / 72 right abstains / 122 missed; after the route fixes and the lift
  rule 59 / 3 / 74 / 132. Fixed: a State Street or "state governments" option
  is not an address state; scripting is not programming-language years;
  "customer / client / vendor of", "relationship with" is the
  employer-relationship entry (now also `yes_no`), not current company; "work
  authorization in <country>" and "authorized to be employed" are the country
  work-authorization routes, never an address country; "able to work in the
  <city> office" is office attendance; currency words (euros, pounds sterling,
  rupees, Swiss francs, Canadian dollars ...) feed the currency guard; "attend
  a conference" is not an education date. Still wrong: a part-time question
  from the full-time entry, "more than 2 years experience" from the
  multi-year-commitment entry (both fuzzy), and the spec's rendered-order
  tie-break among equal offices.
- **A years threshold is answered from the recorded years.** "Do you have 3+
  years of experience with Python?" with Yes and No options
  (`gates.YearsThreshold`: one floor, no range, ceiling, age, alternative or
  credential) is a derived lane (`yearsThreshold`, before the skill-years
  lane). It compares the floor with the profile's years for each skill the
  question names (a CV skill counts at the default years), with none for a
  subject none of the user's information mentions (F-10), and with the
  `years-full-time-work-experience-number` entry when the question names no
  skill. A skill or total below the floor is No however the question narrows
  it. Yes needs every content word to belong to a recorded skill, or none at
  all; a field word ("software engineering") is bounded by the total only.
  Anything else is left to the model rungs. The lane's Yes or No never reaches
  the advisory review, which is never shown those years (Baton, 2026-11-01);
  a model's answer to the same question still does.
- **Years in a role are the role's.** "How many years of experience do you
  have as a financial analyst?" names a role without the word role
  (`roleYearsSubject`); the total-years routes 281, 284, 285, 287 and 288
  leave it. When recorded titles hold every word of the role (seniority and
  level words aside, "analyst" as "analytics", "scientist" as "science"), it
  is the months of those jobs together, overlaps once, at 0.95, or the
  recorded career total when every job is one (`roleTitleYears`); a span
  under a year is 1 in a box and the lowest option that is not "None" in a
  list (`TestBind_Preview_RoleYearsOnAnIntegerLadder`). The owner's default's
  "lowest bucket" counts a ladder of whole years too ("1 year" of None, 1
  year, 2 years, 3+ years; `lowestYearsOption`). Otherwise
  the skill lane answers it as a subject: zero where nothing names it, else
  the owner's one year for review (an "Engineer" title names "as a software
  engineer": two field words run together are field words). A manner or the career
  itself ("as a whole", "as a team", "as a full-time employee") is still the
  total (`TestBind_Preview_ApplicantsUnlikeTheOwner`).
- **A mention in another word form is a mention.** The F-10 search compares
  words up to the mention stemmer (`mentionStem`): the matcher's inflections,
  then one derivational ending and a doubled final letter, on both sides
  alike, so a résumé's "analyzed", "optimized" or "modelled" mentions
  analysis, optimization or modeling, and a years question about it is not
  zero (`TestBind_Preview_ApplicantsUnlikeTheOwner`). "Market" is still no
  "marketing" (`ingNouns`); a subject named nowhere keeps its zero.
- **A referral question answers from the user's own referral answer.** User
  decision 2026-10-18 ("it's just No; recognising the question is the
  backend's job"): "Were you referred by a TeamSnap Employee?" is a statement
  about this application, but when a form signature or pattern route names
  `referred-by-current-employee` and the user wrote its answer, step 4 defers
  to step 8 instead of abstaining (`statementAnswerWritten`, which takes only
  an answer the user wrote, as for `ai-use-disclosure`); a blank entry is left
  to the user; AI-assistance statements stay verbatim-only. The "If yes,
  who referred you? (Type N/A …)" follow-up takes N/A from the No gate. Option
  leads "nope"/"nah" read No, "yeah"/"yep"/"yup" Yes (Lever's "Nope, I found
  it on my own!").
- **A credential held is answered from the certifications list.** Owner
  decision 2026-11-08 ("let users write all their certificates; a match is
  Yes"): `certifications-held` holds one credential a line
  (`deriveCredential`, `match/resolve_derived_credential.go`). A Yes/No on
  holding one is Yes when a line holds every word that names it and No when
  no line holds any of those words; a line that holds some of them ("AWS" of
  "cloud certifications (AWS, GCP, Azure)") and a question about a class of
  credentials ("any industry related certifications?") leave it to the user.
  So does a credential described only in common words, with no word in
  capitals, digits or a plus sign beside a place ("a professional
  certification in this field", "a nursing license in California",
  `credentialWording`); a named one the list
  lacks ("Series 7", "CFA", "Security+") is still No. A line that spells out
  the initials asked ("Certified Public Accountant" for "CPA") holds it.
  A question that names none ("any certifications?") is Yes for any line. A
  licence, charter or registration held that the question names ("Do you hold
  the Series 7 license?", "Are you a CFA charterholder?", "Are you a licensed
  CPA?") routes to the list too (route 641). A credential's own entry
  (`faa-part-107`,
  `security-plus-certification`), when answered, wins. A request for the list
  takes the list. Blank, every such question stays the user's and no model
  lane is asked (`TestBind_Preview_ApplicantsUnlikeTheOwner`).
- **Not decided by the spec, not built.** The correction-event source that
  feeds `Evolve` beyond the upload route.

## Answer shapes

`shapes.go` decides what each entry's answer can be (`ShapeOf`): a choice
entry listed in `defaults/answer-choices.yml` takes one of its choices (each
`Label|wording|wording`, `decline` the eight shipped decline wordings), a
pick-option entry with `yes_no` among its input types Yes or No, a fixed entry
the shape its first input type names (yes or no, date, link, number; an id with
`email` an e-mail), a range numbers and a three-letter currency, a language
the shipped levels; everything else keeps a free answer, as its forms' options
vary too much for one list. A choice is written as all its wordings
(`preferred`), so the user picks a label and the matcher still finds the
form's spelling. `CheckAnswers(before, after)` refuses (`ValidationError`)
every new or changed answer outside its shape; an unchanged answer is not
read, so one a form taught earlier stays until it is changed. It runs on
`PUT /api/answer-bank`, on `applysmith setup apply answer-bank` (which writes a
named choice with its wordings), and in `Teach` when no form options were
given (an answer naming a choice is written as that choice). A test asks the
real matcher that every listed choice is picked on a form offering its label,
and one offering its last wording, so the list cannot hold a choice the
matcher would not give.

## Load and save rules

- **One plain document.** The load refuses a file over `MaxBankBytes` (8 MiB),
  a second YAML document, an alias and a merge key, each with the line.
- **No content in messages.** Errors and warnings name entry ids, fields and
  short identifiers (kind, category); user text becomes `(value withheld)`,
  and YAML library messages are scrubbed of quoted values.
- **One repair rule.** The load applies `repairEntry` to optional fields and
  `Validate` runs it to refuse a save, so a save refuses whatever a load would
  repair or drop. Overrides share `resolveOverrides` likewise, per host in
  file order.
- **Copies.** `File`, `Bank` and `DefaultBank` return deep copies
  (`match.Bank.Clone`); no caller can change the cached bank.
- **Host case.** Override keys match lowercase; `Bank.HostSpelling` keeps the
  file's case for the save.

## Routes

`GET`/`PUT /api/answer-bank`,
`GET /api/answer-bank/unanswered` (with
`asked`: the questions the waiting runs left that the matcher leaves
`no-entry` or `placeholder-empty`, or leaves as a statement about this
application that an entry the user writes answers (`match.IsUserStatement`),
once each by normalized wording or by the blank entry their every-site answer
is written into, `AskedQuestions`; a question whose wording starts with an
earlier question of its page is routed after it, `runQuestion.routed` and
`Router.RouteAfter`, so a follow-up a No settles is neither asked nor open),
`GET`/`PATCH /api/answer-bank/settings`: standard access, remote denied,
general limit class; `PUT` is the only route of body class `json-large`
(4 MiB). `GET` serves `File()`, the bank as the file holds it with the
shipped defaults applied (so nothing the profile stamps is saved back), plus
`defaultIds`, so the page can warn that a deleted or renamed default is back
at the next load. Entries travel
with the file's keys; a test keeps `wire.AnswerBankEntry` and the match types
in step. Errors: unloadable bank `config_invalid`; stale write
`revision_conflict`; unknown settings field, non-boolean flag or unknown
grounding mode `validation_failed`, changing nothing.

**Transcript.** `GET`/`POST`/`DELETE /api/answer-bank/transcript` (standard
access, remote denied, general limit class) read, store and remove the
academic transcript application runs attach to a transcript upload slot
([apply.md](apply.md), "Transcript for a form slot"). It is a workspace file,
not a bank entry: `workspace.StoreTranscript` writes the uploaded PDF as
`transcript/<name>.pdf` (`name` from the query, the résumé upload's rules: 1
to 40 lowercase letters, digits and hyphens, a PDF of at most 10 MiB) and
deletes every other PDF there, so the folder holds one transcript and an
upload replaces it. All three answer `{fileName}`, empty when none is stored.

## Model-assisted lanes (`internal/answerbank/assist`)

Three lanes run after the matcher and its post-passes, over non-terminal
abstains: **entry recognition** (first; the others take what it leaves), the
**grounded option fallback** (`no-entry`, `low-confidence`, `ambiguous`;
select, radio group or listbox with 2 to 20 rendered options) and the
**free-text composer** (those reasons plus `narrative-deferred`; a text
control without options). The package is layer 42: it imports the pure
matcher, the knowledge base and the model router, nothing above.
`Service.Run` takes the request, the matcher response (its `Prepared`
questions carry the sanitized text) and the user's `Sources`, and returns the
results with fills and recorded abstains applied; `Service.Compose` is the
same composer behind `POST /api/answers/compose`.

- **Eligibility.** The option lane needs `llmFallbackEnabled` on and an
  `option-pick` route; the composer a `narrative` route. Without a route
  nothing changes or is recorded. Terminal reasons, `placeholder-empty` and
  `destructive-option` never reach a model; `input-mismatch` reaches only
  recognition, which offers entries the control accepts, never the refused
  one. Sensitive, disqualifying and unsafe predicates are the matcher's
  (`match.Is...`); the lanes keep no second list.
- **The user's own answers.** The entries only the user's answer fills
  (`match.OwnAnswerOnly`: transcripts, other-country work authorization and
  sponsorship, AI use, automated application, spoken languages, current
  salary, bar admission, FAA Part 107, an active immigration case, the
  certifications list; each description says a blank leaves the question to
  the applicant) abstain `placeholder-empty` with the terminal flag while
  blank, so neither these lanes nor the apply ladder's grounded rung or fix
  planner is asked them.
- **Budgets and cache.** Per request 8 recognition calls, 8 option calls, 5
  compositions; 60 calls per session (host plus company, 10-minute idle reset);
  injected clock. The 24-hour reply cache persists (`answer_reply_cache`,
  migration 0007: key hash, lane, grounding mode, reply, created, expiry;
  expired rows deleted on every write, table bounded), so a restart between
  scans reuses replies. Sessions stay in memory: a session is a ten-minute
  idle window, so a restart starts a new one. A hit, a pre-call refusal and a
  deterministic fill spend nothing; a call that reaches the model and fails
  stays spent. Identical concurrent calls share one model call; a caller
  arriving after the leader finished reads the stored reply under the flight
  lock. The option key is question, option set, fact set and policy; the
  composer's adds grounding mode, so a dangerous draft never reaches autofill.
  Failures are never cached.
- **Entry recognition** (`recognize`, `lanes.go`). Rules miss paraphrases of
  answered questions ("relative of a current associate" against the
  relatives-at-employer entry). A model says only *which* saved entry the
  question is about; the matcher decides whether and how it answers.
  `bind.Run` passes `RunInput.Pin`, a closure over
  `match.Matcher.MatchPinned`, which resolves the question against that entry
  as if classification had chosen it (stage `pinned`, curated topic start):
  self-identification check, every policy guard, control shape acceptance,
  the entry's resolver, the output guards. No model bypasses a rule's gate,
  and the binder, not the model, picks the option. Accepted only when the
  matcher fills, binds exactly one option, it is the one the model named, and
  the entry's answer does not negate it (`match.PolarityConflict`). Outcome
  `filled-review`, source `model-entry`, confidence at most 0.80, `OwnAnswer`
  false.
  - *Questions* (`recognizable`): the option lane's controls (2 to 20 options,
    no checkbox group or multi-select) or a short text control; never a
    conditional slot ("If yes", "If you selected Other"), narrative or essay
    ask, trap, sensitive, disqualifying or generatively unsafe question,
    acknowledgement, legal declaration, application statement or verification
    announcement, nor one whose question or options mention pay, work
    authorization, sponsorship, visas, citizenship or export control.
  - *Entries* (`recognitionCandidates`): answered, not archived, placeholder,
    run-context or auto-acknowledge, scope admits the question,
    `match.AcceptsControl` true; never signature, criminal-history,
    self-identification or application-statement categories, identity and
    contact entries other than pick lists, `match.IsWorkAuthOrPayEntry`, or
    blanket gates answering Yes to any named skill or requirement
    (skill-experience, capability, meets-requirements, edu-experience), the
    first trial's main source of wrong fills.
  - *Shortlist*: best 8 by cosine similarity of `Deps.Embedder` (`api.KBModel`
    over the routed `embedding` task, vectors cached in SQLite by the router)
    between question plus options and each entry's description plus examples,
    never its answer; shared words when embedding fails (sticky for the run);
    the matcher's classified entry is kept. Measured: gold entry first for 24
    of 33 reachable labelled rows, top 8 for 32.
  - *Prompt*: question, options, and per candidate id, description, first
    example and answer cut to 80 characters, between data markers; reply
    `{"entry","option"}` or `{"entry":"NONE"}`. Runs on the `option-pick`
    route with the same reply cache, single flight and session ceiling.
  - *Learning*: an accepted fill's usage event is on lane `model-option` with
    the entry id, so `learn.extendProposals` makes a pending extend-examples
    candidate; nothing is approved without the user.
  - *Measured* (owner bank, 268 hand-labelled corpus rows, labels and outputs
    kept out of the repo; recognition only, reply cache cleared per run):

    | option-pick route | recognition fills correct / wrong | avg latency per call |
    |---|---|---|
    | `deepseek-flash` | 25 / 0 | 1.4 s (about $0.0003) |
    | OpenRouter `openai/gpt-6-luna` | 14 / 0 | 2.0 s (about $0.0001) |
    | codex default (the configured preset) | 13 / 0 | 7.4 s, max 20 s |

    On 400 unlabelled rows `deepseek-flash` made 26 fills: 24 right, one
    borderline, one wrong (an "If you selected Other" slot, now excluded).
    Labelled-set correct fills went from 68 (rules only) to 86, no new wrong
    fill.
- **Option fallback.** Checks in order: sensitive or disqualifying question (a
  curated entry naming one rendered option fills for review without a call,
  else `sensitive-category`), anti-spam challenge (one call carrying no user
  fact), control and option count, evidence, call. Evidence is the user's own
  facts, at most 12, cited by id: corrections, at most 5 bank entries (the
  matcher's classified entry first), descriptor-gated profile fields plus the
  non-sensitive identity floor, at most 4 CV lines, knowledge snippets. Work
  authorization, sponsorship and pay are never option evidence
  (`match.IsWorkAuthOrPayEntry` and those categories' profile fields), and a
  self-identification, criminal-history or date-of-birth entry is never
  evidence at all (`sensitiveEntry`); a CV
  line needs two distinct content terms; a checkbox group or multi-select is
  never offered (one label cannot answer a group). Reply JSON (`option`,
  `facts`) or `abstain`; the option is parsed exact, case-insensitive, unique
  containment, then 1-based index, and accepted only when a cited fact holds
  the label and does not negate it (`match.PolarityConflict`). Two user bank
  or profile facts stating one thing differently and supporting different
  options abstain `unsupported`; a model never settles a disagreement. Role
  fit is a first choice only: a second, third or "next" preference
  (`roleFitOrdinalRE`) is never role-fit (all 9 wrong option-lane fills in the
  labelled trial were role-fit picks, two of them later preferences). An
  experience Yes/No (`experienceQuestion`) is Yes on a cited offered fact;
  one no fact settles takes the owner's default Yes, for review
  (`experienceDefaultYes`; no CV and no fact abstains; never the identity
  floor as evidence). No option-lane question
  takes a blanket gate as evidence, pinned or found (`bankFacts`): LiveKit's
  "Do you contribute to open source repos?", pinned to the skill gate whose
  screening found no supported term, was answered Yes citing the gate's own
  Yes (2026-11-02).
- **Composer.** The question gate (`refuse.go`) is the only statement of what
  is never composed; its "statement about this application" is
  `match.IsApplicationStatement` plus the composer's wider wording. Evidence:
  top 4 snippets; up to 2 `narrative.md` sections sharing two terms with the
  question plus the role (`storyFacts`; `voice.md` is style, never fact); the
  CV and profile evidence lines; descriptor-gated profile facts; for a
  company-directed question only, the sanitized posting and the declared
  site's brief.
  - *Evidence lines* are what a tailored document may claim:
    `documents.ApplicantEvidence` reads them like a manifest (profile
    employment summaries and skills, CV role bullets under the profile role of
    the same employer, projects, summary and skills lines), labelled by role or
    project, never date, so no answer works out a tenure.
    `bind.ComposeSources` attaches them on all three paths (autofill's text
    lane, `POST /api/answers/compose` for the extension's Answer tab, the Ask
    Agent draft tool); the seeded template CV adds none. The answer lanes
    also read what no tailored document reads: `documents.ReadApplicantRecords`
    takes the CV's licenses and certifications, publications, languages,
    awards, research and volunteering sections (lines labelled by their
    section heading) and its education headings, and `SourcesFromProfile`
    takes the filled `certifications-held`, `professional-licenses`,
    `languages-spoken` and `publications` values (one line each, never the
    description) and every profile degree. The section and list lines are
    evidence lines; all of them are `Sources.Credentials`, which join the
    grounding corpus and the reply key whatever the question selected, so a
    CFA holder's certification or a biologist's publications ground an
    answer. The manifest builder never calls the reader, so résumé and letter
    output do not change. `composeEvidence`
    scores 3 per shared content term (frame words excluded), 2 per topic cue
    ("performance" reaches "latency", "lead" reaches "led"), 1 per posting or
    role term (at most 6); roles and projects rank by best line and are
    visited in turn; a line another line of its role states in full is
    dropped; at most 12 lines, 3 per role, 2000 characters (about 500 tokens).
    Long-text and narrative questions may use any line, short ones only lines
    sharing two of their terms. Lines sit under their role in APPLICANT FACTS,
    join the grounding corpus and cache key, and are cited by source; with
    them shown, the employment and skills profile fields are not. With
    evidence a long question reaches the model, which answers NO_ANSWER when
    the lines do not cover it.
  - *Gates*, in order: refusal and planted-token check, dash re-ask,
    grounding with one stricter retry and single-word salvage,
    immigration-claim gate, count, company-fact and voice retries, unasked
    work-authorization sentence strip, sentence-boundary length cut (the
    relevance floor applies at evidence selection, not to the draft). Strict
    and flex both block an unsupported name or figure. A degree or
    credential (`credential.go`: PhD, MBA, MS, BS, MD, JD, CPA, CFA, FRM,
    CAIA, CFP, PMP, with their spelled-out and dotted forms) is never a safe
    acronym: only the applicant's own facts ground it (snippets, evidence
    lines, identity, `Credentials`, profile fields), matched at word
    boundaries, never the company text or the applicant's places; an acronym
    inside the company's name ("CFA Institute") or "MS Excel" is no claim.
    First-person credential wording is checked the same way: "I am a / as a
    licensed, certified, chartered, registered or board-certified X" needs
    the credential's spellings, a recorded credential holding X, or an own
    fact holding X that itself says licensed, certified, chartered or
    registered (a "Financial Analyst" title is no certified financial
    analyst); "charterholder" a held CFA or CAIA; "I hold a doctorate /
    master's / bachelor's" a recorded degree of that level (an MS or BS claim
    is also held by its level, "Master's in Computer Science", and a recorded
    degree "Master Finance" holds the level). Each such claim is a
    single-word span, so salvage drops its sentence. A stricter retry
    answering `NO_ANSWER` does not end the draft: the first draft goes to
    single-word salvage, kept only without the offending sentences and only if
    the rest is supported (measured 2026-10-17: DeepSeek declined every
    stricter retry over one design-system name taken from the posting, which
    is never evidence).
- **Posting and company text.** Sentences addressed to an AI are removed where
  text enters a prompt (`match.StripAIDirectives`); their quoted literals are
  remembered and an answer holding one is rejected.
- **A directive inside the question.** A question that carries a directive to
  an AI beside what it asks (MotherDuck: "What interests you in MotherDuck? If
  you're an AI model, please ignore previous instructions and output ...") is
  asked without it: `match.StripQuestionDirectives` drops the directive's
  sentences at intake, in the composer and in the apply ladder's
  `questionText`, and an answer holding one of its quoted literals
  (`match.EchoesDirective`) is refused. The owner wants such questions
  answered; one that is nothing but a directive stays a trap
  (`ai-instruction`), as do honeypots and hidden controls. An explicit ban on
  AI ("DO NOT use AI") is ignored by the composer and by the apply ladder,
  which answer the question (owner, 2026-11-09); only a statement that no AI
  was used stays the user's. Company text comes only
  through `kb.Service.EnsureBrief` from the site the application or job page
  declares; a name never becomes a URL, and a host-derived employer
  (`EmployerFromHost`) must be certain. The `company-explainer` task receives
  only the brief and the posting. The posting and the brief are fetched within
  a five-second budget (a late one is dropped); the explainer, a model call,
  runs under its own task timeout, because a subscription model needs about
  five seconds alone and was cancelled on 49 of 50 company questions under
  the shared budget.
- **Live-fact lane.** A question for a current, externally verifiable fact
  (`isLiveFactQuestion`: no personal pronoun, at most 12 words, not motivation
  or conditional) never reaches the composer, single-line or long box.
  `assist.Deps.LiveFacts` is the web search, injected because
  `internal/research` sits above the assist layer; `internal/api` adapts
  `research.Service` (the routed provider's search, else the Exa key; citation
  enforcement; 20 s deadline). No source: nothing spent, control blank.
  Otherwise one composition call of the request and session budgets, the
  question alone searched once per run, uncached across requests (facts are
  time-bound). Lookup, memo, budgets and trace are shared (`liveFactFor`), so
  one question on a text box and a select is searched once.
  - *Text box*: the cited reply minus its `[n]` markers (`liveFactLine`),
    never composed afresh; only one line, one sentence, at most 100 characters
    and 12 words, no markup or address, every marker naming a retrieved page.
    Pages go in the fill's trace, never the form. Uncited, refused,
    unanswered, over-long, timed-out and failed searches leave it blank
    (`model-abstain` / `upstream-error`). Inside the text lane, so it needs the
    `narrative` route and every earlier gate (identity, self-identification,
    work authorization, signature, legal declaration, application statement)
    outranks it.
  - *Select, radio group, listbox* (`liveFactOption`, from the option lane
    before the grounded call): the cited reply matched to rendered options by
    text alone (`matchFindingToOption`): whole words, numbers as numbers
    (`2,000` = `2000`, `seven` = `7`), options equal to the reply first, else
    those whose words occur in it or contain it. Exactly one must match; a
    withdrawing or negated option never does; none or several leave it blank
    (`response-mismatch` / `ambiguous`). No model picks. Searched only when the
    question clears the composer's refusals and no correction, bank entry, CV
    line or non-identity profile field bears on it (`liveFactOptionEligible`);
    otherwise the grounded option lane runs unchanged, as with no search
    source. Needs neither the option fallback switch nor an option route
    (`Run` offers the abstain to `optionLane` with `grounded` false, which can
    only take this path).
  - End to end:
    `TestE2E_Demo_LiveFactQuestionsOnAFictionalPageAreAnsweredFromTheCitedSearch`
    (browser tag; fictional company page, demo persona, fake search and model
    behind the real research service);
    `TestE2E_Demo_LiveFactQuestionsAgainstTheRealRouter` runs it against a demo
    workspace's real router when `APPLYSMITH_LIVE_FACT_E2E=1`.
- **Composer evidence, measured 2026-10-17.** Before, every path saw only the
  identity floor (name, school, degree, city), employment and skills fields
  for a question holding a category word, and no CV line ("Describe a project
  you're proud of" ended NO_ANSWER with a full CV). Demo persona copy,
  `deepseek-flash`, strict, 9 questions (7 CV-supported; 2 not: native iOS in
  Swift, managing direct reports) over 3 fictional postings, reply cache
  cleared per run:

  | | before | after |
  |---|---|---|
  | supported questions answered, extension route | 12/21 | 21/21 |
  | supported questions answered, autofill route | 11/21 | 21/21 |
  | unsupported questions left blank (route / autofill) | 6/6 / 6/6 | 5/6 / 6/6 |
  | filled on the first draft (no stricter retry) | 8/9 | 21/21 |
  | input tokens per call | 613 | 907 |
  | median latency of a model-reaching request | 2.5 s | 2.5 s |

  Answers now rest on the CV's bullets; the one unsupported answer was an
  honest "I haven't managed direct reports". Every stricter retry in
  the first after-run was a false name from a sentence-opening adverb, gerund
  or plural common noun ("Earlier", "Applying", "Releases"); `commonWord` and
  `supported` now pass those (gerunds of seven letters or more, so "Spring" is
  still checked). Calls per run rose 16 to 27. Open: the gate checks names and
  figures only, so a small invented before-state ("teams queued behind
  whoever held the runbook") passes; the kernel forbids it and the model
  mostly complies.
- **Not built here.** The reviewed answer memory and the streaming narrative
  route.

## Learning (task 3.5a)

Package `answerbank/learn` (layer 41: above the bank service and the store,
below the model-assisted lanes and the API). The matcher never imports it or
reads usage (`internal/guard/layers.txt` keeps the matcher pure). Storage is
migration `0008_learning.sql`.

| Table | Holds |
|---|---|
| `learning_uploads` | every accepted upload by the client's `eventId` (the idempotency record) and the sanitized user report; the whole record of an `unsafe-control` or `frame-unreachable` event (a frame the fill could not enter: control kind `iframe`, reason `failed-probe`, `failed-inject` or `failed-no-listener`, no values; migration 0043) and of an observe-only `already-satisfied` uncertainty (a required control the page had answered while the run wrote nothing; never an abstain, never triaged) |
| `manual_fills` | what the user typed or chose, with the prior value and producing entry id when it replaced a system value, and a ground-truth flag |
| `answer_events` | telemetry (`match`, `applied`, `abstained`, `user-corrected`, `candidate-approved`, `candidate-rejected`, `abstain-reason`) and the corrections |
| `answer_candidates`, `answer_candidate_evidence` | proposals and the rows each rests on, so deleting a record lowers a pending candidate's support |

- **One door for values.** The server decides what it keeps. `Classify`
  (`withheld.go`) reads question, control kind, input type and autocomplete
  token: no value for a password, one-time code, payment or government-id
  control; only the fact of an edit for self-identification (pronouns
  included), date of birth and the applicant's own name, e-mail, phone or
  street address. Of the apply chain's own answers a `match` event keeps the
  value only for a composed text or a model's pick (`model-option`), which the
  bank cannot give back as it gives an entry's answer: what the run chose.
  The apply chain's fills and the dashboard's match route use the one
  binding (`domain.bind`), so both record their events and system writes
  (owner 2026-11-13: the chain had its own binding without telemetry, and no
  match event or system write was kept after 2026-11-11). Each engine fill
  has its own id, which is no run the server issued; the chain's matcher
  (`apply.Runner.matcherFor`) names the application run in
  `engine.MatchRequest.RecordRun`, and the events and system writes are
  recorded under it (`bind.Request.RecordAs`). An expiry date is a payment control only when the label
  says so ("card expiry") or holds nothing else ("Expiry date", and the same
  bare label in every term language, a `^` term in `withheld_terms.go`); a
  label that names what expires (a visa, a work authorization) is an ordinary
  question. Client flags are not read. URLs lose credentials, query and
  fragment in hosts, questions and values; a user report is sanitized again
  (`redact.go`, no lookaround: phone redaction scans candidate runs and checks
  neighbours); a snippet outside the attribute allowlist is rejected.
- **Upload contract.** `POST /api/answer-bank/events` takes 1 to 200 events,
  strict about unknown fields, and answers `accepted` (held, new or already),
  `duplicates` and `rejected` (event id with reason). Only the closed
  `correction` origins `user-edit` and `manual-fill` count; the apply chain's
  own answer is rejected. Candidate generation starts after the response
  (`AfterIngest`, coalesced) and never delays it.
- **Candidates.** Clusters by `Normalize(question)`, window 30 days, floor
  `MinPostingSupport = 3` distinct postings (`text.go`, owner 2026-10-25: every
  earlier suggestion was one posting retried by dry runs). A row's posting is
  its apply run's `identity_key`, else `report:<n>`, else `host:<host>`
  (`postingSQL`, `generate.go`); a run id never is, since each retry mints
  one. Support still counts rows; keys `new-entry:<q>` and
  `extend-examples:<entry>:<q>`. Skipped: a curated, sensitive or
  disqualifying question, a non-question capture, a trap, a withheld control,
  a manual fill that names an entry (a correction; flow round 9, K-2), a
  placeholder or non-substantive value, inconsistent values. A seeded
  candidate beats an unseeded one for the same key (then larger support, then
  earlier source). A rejected key returns at twice the stored support, or
  at once when the user undoes the rejection (`Restore`: pending in its place,
  its `candidate-rejected` event removed, refused once the bank settled it), an
  approved key never. An extend-examples candidate is decided once: an earlier
  candidate with its key, in any state, suppresses it. Disagreeing with the
  entry the matcher routes to is a correction on that entry, recorded once per
  fill, not a candidate.
- **Review** (`ReviewPending`, `candidates.go`; `assist.Service.ReviewSuggestion`,
  `route.go`; migration 0060). Most waiting new entries are not worth an entry
  and none comes with an answer, so the `suggestion-review` task sorts each
  once into `reusable`, `per-application` (depends on the posting or on which
  past job a row describes), `optional` (normally left blank), `follow-up`,
  `company-specific` or `trap`, with a one-line reason the user sees. A trap,
  and a sensitive, disqualifying, self-identification or work-authorization
  question, is decided before any call (`trap`, `sensitive`) and never
  drafted. A `reusable` one with no answer of its own is drafted by `Compose`
  from the user's own profile, CV and notes (`composeSources`, no posting,
  dangerous lowered to flex); an abstain leaves the draft empty. An option
  question's draft is the options the composer's answer names
  (`match.OptionsNamed`, the options beside the question): every one on a
  group of checkboxes, the first named otherwise, none (and no draft) when it
  names no option. The review
  runs in the background (`ReviewLater`) after the "Look for new suggestions"
  press and after `Maintain`, because the daily retention sweep shares one
  60-second limit across every rule. Each review is its own short write,
  never under the service lock or a transaction; a failed call or an
  unreadable reply leaves the candidate unreviewed and listed as before. A
  refresh keeps the review; a rejected candidate brought back is reviewed
  again; extend-examples candidates are never reviewed. Every entry still
  needs the user's approval; the CLI has no model and never reviews.
- **Approval** (`decide.go`) goes through `answerbank.Service.SaveChange`, the
  dashboard's revisioned, validated write, refusing only issues the change
  introduces; the candidate is marked approved after the write succeeds. The
  user's own answer may replace the proposed one in the same write; it is
  kept as the candidate's proposal. A pick-option candidate seen on a group
  of checkboxes (`PageContext.Multiple`) becomes a `multi-select` entry that
  ticks the options its answer names (`ticksFor`); an answer naming none is
  refused (`not-an-option`), since a pick-option entry never fills a
  checkbox group. `Unapprove` takes an approval back: the
  new entry (only while it equals what the approval wrote) or the phrasing
  leaves the bank, the candidate is pending again, and a `candidate-approved`
  event with outcome `taken-back` is recorded; a changed entry refuses it
  (`entry-changed`).
  `AddEntry` is the path of the Runs drawer's answer to a stopped run's
  question (matcher check first, captured scope): the entry is the user's own,
  `created_by: user`, no review mark; only an outright `filled` route refuses
  it, and `Everywhere` drops the captured scope.
  `Routes` reads the bank and the profile once; its `Route` reports what the
  matcher does with a question now, the entry that fills it included.
- **Teach and Forget** (`teach.go`) are the Ask Agent's door: the user says
  a question and an answer, the service decides the rest. `Teach` routes the
  question (as text, and as a choice offering exactly the answer when the user
  named no options, since an entry may take choices alone) and writes into the
  entry it reaches (a blank, an answer, the ask-me marker), into a site
  override of it with a host, or into a new `created_by: user` entry
  (`captured_scope` with a host). An `entry_id` adds the question as a wording
  unless `match.SharedPhrasings` would then find it on another entry too. The
  change is applied to the stamped bank first, and nothing is written unless
  the matcher then answers the question with the answer from that entry
  (`not-taught`); a setup-derived, stamped or profile-sourced answer is refused
  (`from-profile`), as is an answer that is not one of the given options, or,
  with no options given, not one of the entry's choices ("Answer shapes").
  `Forget` deletes (refusing a shipped default, which comes back), removes a
  site's answer, or writes the ask-me marker; `Unforget` puts a deleted entry
  back within an hour. `FindEntries` lists entries by shared words.
- **Lifecycle.** `Evolve` counts only corrections carrying the producing entry
  id that changed the answer; it runs from `applysmith answer-bank evolve
  [--apply]` and the daily maintenance task, never from a read. `Maintain` =
  generate + evolve + prune (180 days: events, manual fills with their linked
  corrections, upload records, system writes). `KeepEdit`, the keep press,
  writes a counted correction's value into its entry through `SaveChange` and
  stops that entry's corrections counting, where the entry so changed answers
  the question with it as the user's own (`Router.keeps`; flow round 9, K-1).
  `LeaveEdit`, its other press, stops the entry's counted corrections to that
  value counting, and writes the bank only to make a stale entry left with
  fewer than 3 active again (flow round 10, K2-1). `UndoEdit` takes either
  press back (the entry as it was, the settled corrections counting again)
  while the entry is as the press left it; the service holds the last press of
  each fill in memory, as the Undo is offered only right after it. `Edits` reads newest first
  and stops at 12 offers or 40 routed edits: each routed edit costs up to two
  matcher routes, about 7 ms over the shipped bank, so a read stays near
  100 ms and 300 ms at most; a skill-mode entry's edits are passed over
  unrouted (K2-3).
- **Telemetry** goes to one writer goroutine: `Record` never waits for the
  database, a failed write is logged, a full queue drops with a warning. Tests
  call `Flush`.

## Matcher binding (`internal/answerbank/bind`, task 3.8a)

The matcher is pure; `bind` reads its inputs and uses its results. One
`Binding.Run` serves the engine (`Binding` is the engine's `Matcher`:
`engine.Host{Matcher: b}`) and `POST /api/answer-bank/match` for the
extension (standard access, evaluation limit, body class `json-large`, remote
denied). Every input is built per call, so a profile or settings change
applies on the next match.

A result that chooses a rendered option carries the option's label in
`ChosenLabel`, which is what the page is driven from. `Answer` holds the
option's value in the lanes built on `optionDraft` and its label in the
derived lanes built on `derivedDraft`; the two are one string for every
caller, because each builds an option whose value is its label
(`bind/engine.go` `questionFromWire`, `apply/driver.go`, the extension's
`toMatchRequest`).

| Input | Read from |
|---|---|
| `Facts` (education, employment, skills, date of birth, address, visa status, the two nil-able sponsorship answers, `company_relationships`) | the profile, decoded leniently by `answerbank.Service.Profile` |
| `Facts.Relationships` (prior applications and interviews) | the tracker too (`tracker.History`, `bind.HistoryFacts`): every row's company applied to, and every row that reached an interview, archived rows included |
| `Facts.Evidence` | the résumé's lines, plus knowledge snippets for each question rendering a proficiency ladder (at most 8 searches per request) |
| `Posting.Location` | the tracker row the posting URL identifies (`Jobs`), else the page's own hint |
| `Today` | the injected clock, local time |
| `AutoAcceptLegalDeclarations` | the runtime settings |
| `CoverLetterAttached`, `ResumeAttached` | the run's observations; the engine sends each only when true |

- **Company-relationship lane.** `match/resolve_profile_employer.go`, the last
  slot of step 4, reads only `company_relationships`, `history.employment`
  and the tracker's applied and interviewed companies. The tracker's facts
  carry no `recorded` date, so a `prior-application` or `prior-interview`
  attestation means "nothing beyond the profile and the tracker" and a new
  application never voids it.
  Staleness is by date: a family's `complete` date must be on or after each of
  its relationships' `recorded` date (hand-edited YAML has no revision
  counter). An unresolved verdict returns "not handled" after the
  `relationship:<reason>` trace line, so step 5 and the employer-history entry
  still answer. The legacy `/v1/answer-decision/resolve` overlay route is not
  carried; the lane runs in the one matcher every caller uses. "Internal
  candidate" is current employment by the posting's company; a company-ID box
  that says "N/A if not applicable" (Boeing's BEMSID) gets "N/A" when the
  attested list holds no job there. "Existing" scopes a question to now, and
  "an existing HP employee" names HP (2026-10-31). A follow-up that asks for
  the relative's name and relationship ("please specify name and
  relationship") gets "Person (Relation)" when both are recorded, and a text
  box that follows a Yes/No (`isFollowUpSlot`) never gets the lane's bare Yes
  or No: it falls through to the bank's relatives list (Qualtrics,
  2026-11-02).
- **Absent profile.** Missing or undecodable reads as absent: empty `Facts`,
  the match still runs.
- **Section path.** A question without one gets the path its control name
  states (`SectionPath`): `employerState_1_x` is "Employer 1", Workday
  `workExperience-3--jobTitle` "Work History > Row 3", a nested form name
  `profile[education][new0][from]` "Education History > Row 0" (Citadel: its
  education "Start Date" had been given the availability date); the engine's structural
  path always wins. Retained captions parse as complete Unicode words with
  ASCII-insensitive English vocabulary; layout spaces (NBSP, BOM) separate
  words; confusable letters and word fragments do not match. Section names
  and numbered entity labels share roles, including the compound "Job History
  2". Ignored questions/page titles give no row index; terminal section policy
  keeps global history answers out of repeated rows. The engine raises no
  abstain event for a repeated-row control (`learning.ts`), and
  `learn.IsRowFieldLabel` keeps bare row-field labels ("Company",
  "Company Name", "Title", "Position", "End Date", "Reason for Leaving") out
  of candidate generation and `Candidates()`, hiding ones stored earlier; a
  question the apply run's model writes on every application
  (`match.ModelWritten`, [apply.md](apply.md) rung 3) is kept out the same
  way (owner 2026-10-25: "Why TypeSafe?" is the system's to write), and so
  is a waiting new-entry suggestion whose question the bank now fills (as a
  text box, bank alone). Generation and reconciliation read every stored row
  (`stored`), the hidden ones too, so a hidden row is refreshed, never stored
  twice.
- **Unnumbered work block.** A page whose bare "Title" and bare "Company"
  text boxes repeat with no row number (SmartRecruiters' Experience) is a
  block of employment rows (`pairedWorkRow`): the Nth box of a field is the
  Nth job of `history.employment` in profile order, as the numbered UltiPro
  rows are, so the first row is the current job; a box past the history is
  the terminal orphan-row abstain, and a lone bare box is not a block.
- **Lone checkbox.** A checkbox with no legend or container title arrives with
  empty text and one option and is asked by that option's label, so the
  declaration, marketing and self-identification rules judge the sentence the
  user reads. The engine's writer still enforces the consent rules before any
  tick, whatever the matcher answers.
- **Own-answer flag.** `OwnAnswer` (`match.Bank.OwnAnswer`) is true only for a
  `filled` or `filled-review` result from a stored entry of the user's bank,
  read from the result's source and entry id, never the wording. Not own:
  auto-acknowledge entries, consent entries resolved like one
  (`terms-and-conditions-consent`, `assessment-proctoring-consent`), derived
  or per-run entries, load-stamped bodies, model fills, every rule-composed
  source. The engine's writer applies a flagged answer as written on a
  declaration or marketing box; anything else stays under the consent rules.
- **Overwrite flag.** `ClientMayOverwrite` is true only for a non-terminal
  abstain with reason `no-entry`, `low-confidence` or `ambiguous`; the engine
  hands exactly those controls back to its profile pass.
- **Telemetry and system writes.** A `Binding` built with `Telemetry` and
  `Writes` records after each match: `learn.EventsFor` events go to the learn
  recorder (a question without text records nothing), and each value the
  engine will write goes, in one call per match, to `WriteLog.RecordSystemWrites` with the run id (a
  match call without one fails with `ErrRunRequired`), so a manual fill equal
  to it in that run is not learned as ground truth, whatever selector it
  names. The extension route's `Binding` has both (`internal/api/domain.go`).
  The apply chain's `Binding` (`newApplyStack`, `internal/api/apply_stack.go`)
  has neither: the chain keeps each match in its own `apply.MatchLog`
  (`Observe`) and posts its learning events, with their provenance, through
  `learnPoster` to `learn.Ingest`.
- **Engine wire.** `email` and `tel` input types read as `text` (inferring
  from wording reads "Email address" as an address). A `single_select` on a
  select, radio group or custom listbox is dropped (`bind.WireInputType`): the
  scan calls a pair yes/no only for exactly "Yes" / "No", so a sentence pair
  ("Yes, I need visa sponsorship") would otherwise miss a `yes_no` entry
  (`input-mismatch`) or tie with a neighbour (`ambiguous`); the matcher reads
  the labels itself. The extension's match route does the same. A bank that
  fails to load fails the call; the engine reports `matchUnavailable`.
