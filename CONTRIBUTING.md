# Contributing to Applysmith

Thanks for helping. Applysmith handles people's job applications, so the bar is
correctness and privacy first.

## Before you start

- Bugs and ideas go in an issue first; use the templates. For a large change,
  agree on the approach in the issue before writing code.
- Never paste personal data into an issue, a test or a commit: no real names,
  email addresses, phone numbers, CVs, mail or captured pages. Use the reserved
  example data listed in [docs/development.md](docs/development.md#comment-rules)
  (`example.com`, phone numbers `555-0100` to `555-0199`, the user `alex`).
- Do not copy code from other projects. Areas that came from another project
  are listed in [docs/provenance.md](docs/provenance.md); several are clean-room
  implementations written from a behavioural spec alone.

## Build and test

Tool versions and every command are in [docs/development.md](docs/development.md).
In short (macOS, Go through `GOTOOLCHAIN=auto`, Bun 1.3.5, Google Chrome):

    make build          # generated assets, then bin/
    make verify         # vet, Go tests, the repository guard, dashboard, engine and extension tests
    make test-browser   # the tests that need a real Chrome

`make verify` includes the repository guard (`internal/guard`): package
layering, file and size budgets, English-only text, and scans for personal data
and credential-shaped strings. A guard failure is a real failure; fix the cause
rather than renaming things to get past it.

## Pull requests

- One topic per pull request, with a short description of what changed and how
  you checked it.
- Add a test only where it is the one place that would catch a real problem;
  the rule is in [docs/development.md](docs/development.md#which-tests-we-keep).
- Keep docs in step with behaviour. Detailed docs live under `docs/`; the
  README stays short.
- `make verify` passes, and `make test-browser` too when you touched the
  browser, apply or extension code.

By contributing you agree that your contribution is licensed under the
[MIT License](LICENSE).
