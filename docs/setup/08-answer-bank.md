# Step 8: Answer bank review

## What this step does

The answer bank is the list of questions application forms ask (name,
address, notice period, how you heard of us, and so on) with your answer to
each. Autofill uses it. This step makes you review it so forms are not
answered from unreviewed defaults. Entries marked blocking must be answered
or explicitly left to you with `@ask-me` (the second address line is listed
but optional; the county must be answered); the shipped defaults for
self-identification, compliance declarations and consents must be confirmed.
In the dashboard, Leave all of them to me (above the list) leaves every listed
question to you; once you have typed some answers it reads Save these and leave
the rest to me, and keeps what you typed.

## Before you start

Finish steps 2 to 5 first. Questions your earlier steps already answer (your
name, email, phone, address and links, where you live, your work authorization
and sponsorship, your current employer and job title, your schools, your pay
currency) are not asked here at all: the Answer Bank page marks them "From
Setup" and follows the step when you change it there, so an edited email or
address never leaves an old copy behind. Write your own answer on that page to
replace one. This step and the page's "Blocking" filter count the same
entries. Questions that only apply after another answer (an expected
graduation date only applies while you are a student) wait for that answer:
they block once it says they apply and are finished when it says they do not.
Leaving the first question to you with `@ask-me` leaves the ones that depend
on it to you too.

## Steps

See what is outstanding:

```
applysmith answer-bank status
```

Write `answers.json`. The keys are entry ids from the status output; each value
is your answer, or `@ask-me` to leave that question to yourself every time:

```json
{
  "answers": {
    "<entry id>": "<your answer>",
    "<another entry id>": "@ask-me"
  },
  "confirm_review": true
}
```

Set `confirm_review` only after you have read the defaults flagged for review
in the status output.

```
applysmith setup apply answer-bank --file answers.json --dry-run
applysmith setup apply answer-bank --file answers.json
```

A question with set answers (salutation, pronouns, notice period, degree
level, the self-identification questions and so on) takes one of them, in any
of its spellings; any other answer is refused and nothing is written. The
Answer Bank page lists the choices. Only entries that take one answer can be
set here. Answer the rest, and review
the answers the product learned from your corrections, on the Answer Bank page.
To add a question the bank does not hold yet, tell the Ask Agent the question
and your answer (the page's "Add with Ask Agent").
Candidates are never approved automatically:

```
applysmith answer-bank candidates list
applysmith answer-bank candidates approve <id> --answer "A former colleague"
applysmith answer-bank candidates unapprove <id>
```

`--answer` stores your own answer in place of the proposed one. `unapprove`
takes an approval back until you change the entry it wrote; `restore` puts a
rejected candidate back in the queue.

## Where this is stored

`answer-bank.yml` in your workspace. An answer you write is never overwritten
by a later run.

## How to check it worked

```
applysmith doctor --only answer-bank
applysmith answer-bank status
```

```text
[PASS] answer-bank.blocking   answer-bank is complete
```

## If something goes wrong

| What you see | What to do |
|---|---|
| `N blocking entries remain` | Each is listed with its id. Answer it or set it to `@ask-me`. |
| The defaults "need review" | Read the listed defaults, then send `"confirm_review": true`. |
| An id is rejected | Use an id exactly as `answer-bank status` prints it. |

Next: [Step 9: language model](09-llm.md).
