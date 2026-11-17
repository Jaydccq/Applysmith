# Apply

## Apply Next

The Apply Next page lists the rows ready for action in four lanes: Apply now,
Selective, Marked applied and Awaiting receipt. The server decides which lane
a row belongs to: an employer on your batch skip list is in Selective, a
posting known to be closed or already applied to is in no lane, and Awaiting
receipt holds what Applysmith submitted until the employer's receipt arrives. A
row shows the company, role, score, age, the latest attempt, a badge when the
employer is on your batch skip list, and "Left out: <reason>" when a list
started now would not run it.

For a row you can:

- Open its report and its tailored documents. Edit the résumé or cover letter
  text, regenerate it, preview the PDF and save it to `documents/`. Every
  sentence keeps its evidence; an edit that loses it is refused.
- Autofill only: fill the form in the apply window and stop. Nothing is
  submitted.
- Autofill and submit: after a confirmation that names the company, role and
  posting host.
- A posting you or the evaluation skipped, or you discarded, never runs in a
  list, but its report still offers "Autofill anyway": the report says why it
  was skipped, and a submission moves the tracker row to applied.
- Apply all in a lane, confirmed the same way. The rows a list leaves out are
  named with the reason. Tick "Retry today's stops" to run again the rows that
  stopped today on Applysmith's own judgement.
- Tick rows in Apply now and Selective, then Autofill selected, or Submit
  selected after a confirmation that names each role, company and host.
- Mark applied, if you applied yourself. The row shows who applied, you or the
  agent.
- Confirm submitted, on an Awaiting receipt row, once you have seen that the
  application arrived: the row becomes applied by the agent.

A start while another run holds the apply window waits its turn; the page says
how many go before it ("Queued (2 ahead)") and Cancel takes it off the queue.
A stop that waits for your answer, profile or CV runs again once you save it,
and so does one after you save a new résumé or cover letter for the row.

The page never offers to withdraw an application.

## From the command line

Fill an application and stop before the final Submit (this is the default):

```
applysmith apply <url>
```

Submit only when you mean it; the flag is the confirmation:

```
applysmith apply <url> --submit
```

A list from the lanes, with a cap:

```
applysmith apply --list apply-now --limit 5
```

Fill the tab the apply window already shows, once:

```
applysmith autofill
```

These need the app or `applysmith serve` running and the apply window set up
(step 10). Tailored documents without applying:

```
applysmith resume generate --candidacy <id> --pdf
applysmith cover-letter --candidacy <id> --pdf
applysmith documents list --candidacy <id>
```

Each makes one document (`--with-cover-letter` makes the pair). `documents
list` names a version made before `cv.md`, `profile.yml`, `voice.md` or the
evaluation report changed, with the command that makes that one again.

## What a run does

A run walks the application in the apply window: it signs in if needed (first
with the site's sign-in synced from your everyday Chrome, for that exact site
only, at every sign-in page, job boards and LinkedIn included), fills each
page, checks it, compares what is on the page with your profile (a mismatch
blocks Submit), and presses Submit only if you asked and every gate passes. When it cannot finish it stops and tells you why. Totals and recurring
problems:

```
applysmith apply scorecard
```

## The Runs board and handoff

The Runs page has the columns Running, Needs you and Finished (the last seven
days). A card is one scan, evaluation, document run or application. The
needs-you list is what is waiting on you, longest waiting first, each with the
reason and one action.

When a run stops on something you can finish in the page (a bot check, a sign-in
it cannot do, a question only you can answer, a challenge at Submit), it keeps
that browser tab for you. Use Jump to tab on the Apply Next row or Show tab on
the Runs card, finish the step yourself, then press Done: the run goes on in
that tab and presses Submit only if you asked it to when you started it.
Saving the last answer to the questions it left does the same. A dry run of
one posting that filled every page waits at Submit in its tab: Submit this
form asks the same confirmation as Autofill and submit, checks the form again
and presses Submit there. An answer you save starts as an answer for every
site unless the question is about the employer or the posting, and when the
question reached a blank entry of your Answer Bank the answer fills that
entry. Starting a posting again closes the tabs its earlier stops kept. At
most three tabs are kept; the oldest goes first. Kept tabs
are no longer driven by automation. Applysmith watches what you change in a kept
tab (only values you commit yourself, for at most two hours, and never
passwords, one-time codes, card or national-identifier fields, your own name,
email, phone or date of birth, or self-identification answers), so it can learn
the answer for next time.

A question left unanswered or filled by hand three times becomes a suggestion
on Answer Bank, Suggestions. Each night, and after you press Look for new
suggestions, a model sorts the waiting ones. One that is not a single answer
for every application (a field about one job such as a manager's name, an
optional or follow-up box, a question about one company, a trap or a sensitive
question) moves under Not suggested with its reason. For the rest it drafts an
answer from your profile and CV and names what it used; work authorization,
self-identification and other sensitive questions are never drafted. Nothing
enters the bank until you approve it, and an approved answer waits for your
review.

When the fix is a setting rather than the page, the item links it: Settings,
Applications when a site is cooling down after a risk signal, Runtime settings
when a required legal declaration waits for "Tick required legal declarations",
Settings, Gmail when a code or activation mail could not be read because Gmail
is not connected, the site's saved password in Settings, Sign-in credentials
when the site refused it, and the Browser page's session sync when a scan needs
your sign-in, with Run the scan again beside it. In a list, a site's later rows
are skipped only after a saved password was really refused or the site locked
the account, and the note says which. A captcha or block on a site that is not
a named application system holds account creation on that site only.

A stop that nobody can finish in the page (a closed posting) closes its tab.
Retry and Cancel on a card are confirmed.

## Documents

Documents (résumé and cover letter PDFs) need the document tools of
[setup step 7](../setup/07-resumes.md) for new PDFs. A report links its
application's Documents as "Résumé and cover letter"; making them makes both
PDFs in one run. A posting that scored below 4.5 gets a tailored PDF only after
you press "Allow a PDF anyway" there (`applysmith report override <number>`). A
cover letter an application form asks for is written whatever the score; to
keep the floor for those letters too, choose "Only for postings scored 4.5 or
higher" in Settings, Documents. With that choice, confirming a submit of one
row, a lane or the rows you chose offers "Allow a tailored cover letter if a
form asks for one", and a stopped run that needed one offers "Allow a tailored
cover letter and retry". A run uploads your tailored résumé when one passed its checks, else the
roster résumé that best matches the posting, and says which and why. A form
that asks for a transcript gets the PDF you put on Answer Bank, Transcript;
without one, that upload is left for you. The
Documents drawer shows the checks of each version; a PDF you have not looked at
reads as unreviewed:

```
applysmith documents review <id> --performed --sha256 <pdf-hash>
```

Next: [Track outcomes](track.md).
