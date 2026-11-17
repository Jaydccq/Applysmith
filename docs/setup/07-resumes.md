# Step 7: Résumé PDFs

## What this step does

It registers the résumé PDFs that can be uploaded to applications. Without at
least one registered PDF there is nothing to upload. Exactly one is the
default. A résumé tailored to a posting is generated later from your CV and
narrative; that needs the document tools described below, but registering a
PDF you already have does not.

## Before you start

Have a PDF of your résumé, at most 10 MiB.

## Steps

Copy a PDF into the workspace and register it:

```
applysmith resumes add <file.pdf> --label "<label>" [--tags go,backend] [--default]
```

The first résumé you add is the default anyway. If the profile refuses the new
entry, the copied file is removed again. The rest of the roster:

```
applysmith resumes list
applysmith resumes set-default <id>
applysmith resumes remove <id>
```

`remove` deletes the entry and its file in `resumes/` (never a file elsewhere),
and refuses to remove the default while others remain; make another résumé the
default first. To replace the whole roster or edit from a JSON description
instead:

```
applysmith setup schema resumes
applysmith setup apply resumes --file resumes.json --dry-run
```

`resumes.json` holds either `roster` (the whole roster: `id`, `label`, `file`
as `resumes/<name>.pdf`, optional `tags`, `description`, `default`) or `add`
(one local PDF: `path`, `id`, `label`, optional `tags` and `default`), `remove`
(`id`) or `set_default` (`id`).

### Optional: tools for generating new PDFs

Tailored résumés and cover letters are typeset with tools that are installed
outside the app: TeX, Poppler and a private Python environment. Check what is
missing and what installing it costs in disk space:

```
applysmith doctor --only resumes.toolchain
applysmith documents toolchain check
```

Prepare the private Python environment (it shows the download and disk size
first, and installs nothing without `--yes`; it never installs TeX or Poppler,
which you install yourself as the check instructs):

```
applysmith documents toolchain setup --yes
applysmith documents toolchain smoke
```

With every tool installed, a tool or template change (a Homebrew upgrade of
Poppler, or an app update with a changed template) needs the render test again
before new PDFs are made: Setup, Résumé PDFs then reads `verification-needed`
and offers Run the render test, and so does the Documents drawer when it
refuses a PDF for it. Both are the same as `applysmith documents toolchain smoke`.

## Where this is stored

PDFs are copied to `resumes/` in the workspace and listed in the roster in
`profile.yml` (key `resumes`). Generated documents are written to `documents/`.
The TeX, Poppler and Python tools stay outside the workspace and the app.

## How to check it worked

```
applysmith doctor --only resumes
```

```text
[PASS] resumes.files       resumes is complete
[WARN] resumes.toolchain   document-toolchain-unavailable: ...
```

The toolchain line is a warning, never a failure; the step is satisfied by a
registered PDF alone.

## If something goes wrong

| What you see | What to do |
|---|---|
| `no résumé PDF is registered` | Run `applysmith resumes add <file.pdf>`. |
| The file is refused | It must be a PDF of at most 10 MiB, and a roster `file` must sit directly inside `resumes/` (not in a subfolder). |
| `path_outside_workspace` | A roster entry or link points outside the workspace; copy the file into `resumes/`. |
| Exactly one default is required | Mark one entry `default`; zero or several defaults are a validation error. |
| `toolchain setup` or `smoke` fails | Run `applysmith documents toolchain check` and install what it names, then repeat; the app and every other feature work meanwhile. |

Next: [Step 8: answer bank](08-answer-bank.md).
