# Step 6: Your story and voice

## What this step does

It stores two short Markdown documents. The narrative is how you describe
yourself: a one-line headline, your strengths with proof, what you want next,
what you will not accept and how you prefer to be described. The voice file is
optional samples of your own writing. Cover letters and free-text answers are
drafted from the narrative and checked against the voice. Without a narrative
they have nothing of yours to draw on.

## Before you start

Write the narrative yourself; nothing is composed for you. It needs at least
300 characters and no leftover placeholders. For the voice, paste at least 500
characters of your own writing (emails, notes, a past cover letter you wrote).
The voice is optional; skipping it only means drafts are not checked against
your style.

## Steps

```
applysmith setup schema story
```

Write `story.json`:

```json
{
  "narrative": "<your narrative, markdown, at least 300 characters>",
  "voice": "<optional: samples of your own writing, at least 500 characters>"
}
```

```
applysmith setup apply story --file story.json --dry-run
applysmith setup apply story --file story.json
```

To skip the voice samples on purpose:

```
applysmith setup skip story.voice
```

`applysmith setup unskip story.voice` takes the decision back.

## Where this is stored

`narrative.md` and `voice.md` in your workspace (each limited to 256 KiB of
UTF-8 text). `voice.md` does not exist until you give samples.

## How to check it worked

```
applysmith doctor --only story
```

```text
[PASS] story.narrative   story is complete
[PASS] story.voice       voice samples are in place
```

A missing voice file shows `WARN` on `story.voice`; it is never required.

## If something goes wrong

| What you see | What to do |
|---|---|
| `narrative.md still holds the seed text` | Send your own narrative. |
| A placeholder finding | Remove tokens such as `[Your Name]`, `TODO`, `TBD` or `<company>`. |
| `story.voice` warns "no samples yet" | Add samples, or choose `applysmith setup skip story.voice`. |

Next: [Step 7: résumé PDFs](07-resumes.md).
