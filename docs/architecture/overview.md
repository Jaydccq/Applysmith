# Architecture overview

This page is the public map of Applysmith: what the parts are and how they fit.
The other pages in this folder are design notes for each part; this page links
to them rather than repeating them. The normative requirements are the specs
under `openspec/changes/migrate-to-go-mygo/specs/`.

## What it is

Applysmith is a local-first job-search tool. Everything runs on your own computer;
the only network traffic is to the services you connect (a language-model
provider, Gmail, Telegram, job boards, the sites you apply to). The data
belongs to you, in a folder you choose.

## The pieces

| Piece | What it is | Where |
|---|---|---|
| Server | One Go process per workspace: the local API, the dashboard, the scheduler, the apply chain, the agent. Loopback only. | `internal/api`, `internal/launch` |
| Command line | `applysmith`: setup, doctor, scans, evaluation, tracker, apply, backup. Builds without any GUI code. | `internal/cli`, started by `cmd/applysmith` or by the app binary named `applysmith` |
| Desktop app | A window, a menu-bar item and notifications around the same server. If a server already runs, the app attaches to it. | `app/`, [app.md](app.md), [packaging.md](packaging.md) |
| Dashboard | A React single-page app served by the server: eighteen pages. | `web/`, [dashboard.md](dashboard.md) |
| Browser lanes | A dedicated Google Chrome the server drives: a visible apply window and scan windows. | [browser.md](browser.md) |
| Autofill engine | TypeScript that runs inside the page to find and fill form controls. | `engine/`, [apply.md](apply.md) |
| Chrome extension | A side panel and page widgets for your everyday Chrome, paired to the server. | `extension/`, [extension.md](extension.md) |
| Computer-use service | PersonalCU, a separate macOS app the agent's desktop tools reach over a local socket; built and granted on its own. | `computeruse/`, [computer-use.md](computer-use.md) |
| Storage | SQLite for runtime state, plain files for what you edit, the OS keychain for secrets. | [storage.md](storage.md) |
| Policies | The safety rules the product enforces. | [policies.md](policies.md) |

One server owns a workspace at a time, decided by a lock file in the state
directory. `applysmith serve` and the desktop app start the same server; there is
no takeover between them.

## How a request is classified

The server opens listeners only on loopback addresses. Who is calling is decided
by the listener, never by a header:

- the primary listener (port 47319 by default) accepts the dashboard session
  cookie and the local bearer token;
- when remote access is on, a second loopback listener (port 47321) is the
  entrance of an HTTPS tunnel and accepts only paired-device tokens, on an
  explicit allowlist of routes.

Routes are a table; each route declares its access class, whether a remote
device may call it, and its rate-limit class. See [api.md](api.md) and
[remote-access.md](remote-access.md).

## The work

Long work runs as a **run** with a state, a stage timeline and an audit trail:
evaluations, scans, document generation, applications, scheduled jobs. The
Runs board and notifications are views over the same records.

| Capability | Note |
|---|---|
| Discovery | Sources, the board registry, hiring signals, leads, the inbox: [discovery.md](discovery.md), [signals.md](signals.md), [leads.md](leads.md) |
| Evaluation | Gates, screening, scoring computed in Go, reports: [evaluation.md](evaluation.md) |
| Messages | Outreach and follow-up drafts that are never sent: [evaluation.md](evaluation.md#message-drafts) |
| Documents | Tailored résumé and cover letter with evidence grounding, typeset by external tools: [document-toolchain.md](document-toolchain.md), [document-grounding.md](document-grounding.md) |
| Apply | One staged runner shared by the CLI, the dashboard and the agent; safety gates decide every Submit: [apply.md](apply.md), [gates.md](gates.md) |
| Tracker | Statuses, events and follow-ups: [tracker.md](tracker.md) |
| Gmail | Outcomes, the action inbox and briefs: [gmail.md](gmail.md) |
| Ask Agent | A chat assistant with a closed set of tools and guard chain: [agent-domain.md](agent-domain.md) (its concepts and flow, first), [ask-agent.md](ask-agent.md), [mcp.md](mcp.md) |
| Notifications | Runs board, quiet hours, Telegram: [notify.md](notify.md) |
| Scheduler | Time-based jobs that can never submit: [scheduler.md](scheduler.md) |

Language-model calls go through one router that maps each task to a provider
and model, records every call's cost, and works with a single API key or a
signed-in command-line tool: [llm.md](llm.md).

## Layering

Go packages sit in numbered layers; a package may import only packages in lower
layers. Pure packages (the safety gates, the identity rules, the answer
matcher) do no I/O and read no clock. A guard test fails on a violation. The map
is `internal/guard/layers.txt`.

## What stays out of the repository

A repository guard (`go test ./internal/guard/...`) keeps personal data out:
real email addresses and phone numbers, absolute home paths, non-English text
in code and docs, credential-shaped strings, captured pages and real mail. All
fixtures are synthetic. A local list of personal tokens that lives outside the
repository is scanned as well.

## Setup and development

- Using the product: [docs/setup](../setup/README.md), [docs/user-guide](../user-guide/README.md).
- Building it: [development.md](../development.md).
- Where each idea came from: [provenance.md](../provenance.md).
