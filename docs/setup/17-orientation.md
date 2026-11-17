# Step 17: Orientation

## What this step does

It shows you the daily loop and the optional steps you skipped, and asks you
to confirm that you read them. Without it you would never be shown what the
product does day to day or what each skipped step costs.

## Before you start

Read the [daily loop](../user-guide/README.md). List what is still open:

```
applysmith setup status
```

## Steps

Acknowledge:

```
applysmith setup ack orientation
```

The acknowledgement is bound to the list of steps you skipped. If you skip or
unskip a step later, acknowledge again.

## Where this is stored

The acknowledgement is in the workspace database, with a hash of what it
covered.

## How to check it worked

```
applysmith doctor --only orientation
applysmith setup status
```

```text
progress 100%
```

Progress is the number of finished required steps over 12; optional steps never
count.

## If something goes wrong

| What you see | What to do |
|---|---|
| `read the daily loop and the steps you skipped, then acknowledge` | Run the command above. |
| It reads undone after you skipped another step | Acknowledge again; the scope changed. |
| Something earlier is not complete | `applysmith setup next` names the first incomplete required step and its commands. |

You are ready for the [daily loop](../user-guide/README.md).
