# Step 5: What you are looking for

## What this step does

It records the roles, locations and pay you want, and confirms the batch skip
list. Without targets nothing can be ranked and the eligibility gate has
nothing to compare against.

The batch skip list is the employers that a batch apply leaves to you. Until
you change it, it is Apple, Microsoft, Alphabet, Amazon, Meta, NVIDIA and
Tesla. The step asks you to confirm or edit it.

## Before you start

Decide: one or more target job titles (and whether each is primary or
secondary), the places you would work (or "anywhere or remote"), the currency,
the pay period (`year` or `hour`) and a minimum and target amount.

## Steps

```
applysmith setup schema targets
```

Write `targets.json`:

```json
{
  "roles": [{"title": "<job title>", "seniority": "<level>", "priority": "primary"}],
  "locations": ["<city or anywhere or remote>"],
  "compensation": {"currency": "<three letters>", "period": "year", "minimum": 0, "target": 0},
  "skip_list_confirmed": true
}
```

Replace the placeholders and the two zeros with your numbers (positive
integers, minimum not above target). To change the skip list, send
`batch_skip_companies` as the whole list; an empty list skips no one.

```
applysmith setup apply targets --file targets.json --dry-run
applysmith setup apply targets --file targets.json
```

If the stored targets are the shipped example values and you mean to keep them:

```
applysmith setup ack targets
```

## Where this is stored

In `profile.yml` (`targets` and `batch_skip_companies`). If the key
`batch_skip_companies` is absent, the seven-company default applies; if it is
present, even empty, it is the whole list.

## How to check it worked

```
applysmith doctor --only targets
```

```text
[PASS] targets.fields   targets is complete
```

## If something goes wrong

| What you see | What to do |
|---|---|
| `compensation` rejected | Currency is three letters, period is `year` or `hour`, amounts are positive whole numbers, minimum is not above target. |
| "the batch skip list needs review" | Send `"skip_list_confirmed": true` or an edited `batch_skip_companies`. |
| A company you skipped is still being applied to | A single explicit apply request for one posting still applies; the list governs batches. See [policies](../architecture/policies.md). |

Next: [Step 6: story and voice](06-story.md).
