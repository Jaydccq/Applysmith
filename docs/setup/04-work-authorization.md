# Step 4: Work authorization and location

## What this step does

It records whether you may work where you are applying, whether you need
sponsorship, whether you hold a security clearance, and where you will work
and relocate. Without it, sponsorship and authorization questions are left
blank and postings you cannot take are not filtered out.

These answers are never inferred. A form question about legal status is
answered only with what you wrote here (or in your own answer bank); otherwise
it is left for you.

## Before you start

You need to know your status (for example citizen, permanent resident, work
visa), whether you need sponsorship now and in the future (two separate
questions), and your clearance (write `none` if you hold none).

## Steps

```
applysmith setup schema work-authorization
```

Write `work-auth.json` with your own answers:

```json
{
  "authorization_status": "<citizen | permanent_resident | work_visa | student_work_authorization | other | not_authorized | prefer_not_to_say>",
  "authorized_countries": ["<two-letter country code>"],
  "citizenships": ["<two-letter country code>"],
  "requires_sponsorship_now": false,
  "requires_sponsorship_future": false,
  "security_clearance": "none",
  "relocation_default": "<open | closed>",
  "work_modes": ["remote", "hybrid", "onsite"]
}
```

For a work visa add `status_detail` (the visa class, for example `H-1B`); for
student work authorization it is the end date `YYYY-MM-DD`. A `visa_status`
written by hand as a familiar wording that names one status (`F-1 OPT`,
`H-1B`, `green card holder`, `U.S. citizen`) also counts as answered and is
kept as written; forms read it in those words. A wording that names two
statuses (`Canadian citizen, TN`), negates or postpones the one it names (`not
a US citizen`, `green card pending`) or names another country's citizenship
names none, and the step asks for the status again. Countries may be given by name
(`United States`) or two-letter code. `citizenships` lists every country you
are a citizen of (both, for a dual national); sanctions and export-control
questions ("citizen of Cuba, Iran, North Korea or Syria ...") are answered from
it and from your address, and left for you while it is empty. Optional lists:
`relocation_places` and `relocation_excluded`. Replace the placeholders with
your values before you run:

```
applysmith setup apply work-authorization --file work-auth.json --dry-run
applysmith setup apply work-authorization --file work-auth.json
```

## Where this is stored

In `profile.yml` (`work_authorization` and `preferences`). The answer bank
answers the questions that ask the same facts (authorized to work in the US,
sponsorship, permanent authorization, on OPT) from it, so step 8 does not ask
them again; an answer you write in the bank replaces the derived one.

## How to check it worked

```
applysmith doctor --only work-authorization
```

```text
[PASS] work-authorization.answers   work-authorization is complete
```

## If something goes wrong

| What you see | What to do |
|---|---|
| `... is unanswered` | The line names the key. A `false` is an answer, a missing key is not: send the boolean explicitly. |
| The status is rejected | Use exactly one of the listed values; there is no default. |
| The visa class is rejected | Use one of `H-1B`, `H-1B1`, `E-3`, `E-2`, `L-1`, `O-1`, `TN`, `other`. |

Next: [Step 5: targets](05-targets.md).
