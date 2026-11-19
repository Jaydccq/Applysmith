# Provenance register

Every area of this repository whose design came from another project has a
row here. The guard (`internal/guard`, rule `provenance`) reads the table.

## How to read it

- **Paths**: path globs of the new implementation, each in backticks. `*` stays
  within one path segment, `**` crosses segments.
- **Treatment**: `clean-room`, `attributed-source`, `dependency`,
  `data-fetched` or `asset-shipped`.
- **Spec**: the capability spec the implementation is written from.
- **Review**: the provenance reviewer's sign-off, or `pending`.
- **Status**: `planned` (the area may have no files yet) or `implemented`. Before
  a phase exits, every retained row due in that phase must be `implemented`.

Rules the guard enforces:

- A clean-room area is implemented from its behavioural spec only. Every
  source file under a clean-room glob carries the header line
  `Clean-room: docs/provenance.md#<id>` within its first five lines.
- In an `implemented` row, every glob must match a tracked file.
- There is no `vendor/` directory, and `third_party/` holds licence texts,
  notices and manifests only, never source.

Clean-room implementers receive the behavioural spec alone. The old
repository's paths are on the deny-list in [development.md](development.md).

## Register

| ID | Paths | Origin and licence | Treatment | Spec | Review | Status |
|---|---|---|---|---|---|---|
| CR-FOLLOWUP | `internal/tracker/followup*.go` | legacy implementation, upstream lineage unresolved | clean-room | application-tracker | pending | implemented |
| CR-MESSAGES | `internal/messages/**` | legacy implementation (outreach and follow-up modes), upstream lineage unresolved | clean-room | application-documents | pending | implemented |
| CR-STATES | `internal/tracker/states*.go` | legacy implementation, upstream lineage unresolved | clean-room | application-tracker | pending | implemented |
| CR-LIVENESS | `internal/discovery/liveness*.go` | legacy implementation, upstream lineage unresolved | clean-room | job-discovery | pending | implemented |
| CR-SCAN-SKELETON | `internal/discovery/scan*.go` | legacy implementation, upstream lineage unresolved | clean-room | job-discovery | pending | implemented |
| CR-EVAL-RUBRIC | `internal/evaluate/**` `internal/gates/**` `internal/prompts/**` | legacy implementation, upstream lineage unresolved | clean-room | job-evaluation | pending | implemented |
| CR-DOCTOR | `internal/onboarding/doctor*.go` | legacy implementation, upstream lineage unresolved | clean-room | onboarding-setup | pending | implemented |
| CR-ENGINE | `engine/**` | legacy implementation, upstream lineage unresolved | clean-room | autofill-engine | pending | implemented |
| CR-RUNS-BOARD | `internal/notify/board*.go` `internal/notify/policy*.go` | Orca, MIT | clean-room | notifications-and-telegram | pending | implemented |
| CR-BROWSER-VIEW | `internal/browser/screencast*.go` `internal/browser/lane.go` `extension/src/widgets/picker*.ts` | Orca, MIT (screencast, popup budget, field picker) | clean-room | browser-lanes | pending | implemented |
| CR-EXTRACT | `internal/discovery/browser_detail.go` `internal/discovery/browser_scripts.go` | Lightpanda, AGPL-3.0, ideas only (detail text, JSON-LD and content wait) | clean-room | job-discovery | pending | implemented |
| CR-AGENT-HANDOVER | `internal/notify/board_resume*.go` | nanoMuse, GPL-3.0-or-later, ideas only | clean-room | notifications-and-telegram | pending | implemented |
| CR-AGENT-GRANTS | `internal/agent/approvals/*.go` `internal/agent/confirm*.go` | nanoMuse, GPL-3.0-or-later, ideas only | clean-room | ask-agent | pending | implemented |
| CR-AGENT-GOALS | `internal/agent/goals/*.go` `internal/agent/tools_goals*.go` `internal/api/goals*.go` | nanoMuse, GPL-3.0-or-later, ideas only | clean-room | ask-agent | pending | implemented |
| CR-AGENT-TRIGGERS | `internal/agent/triggers/*.go` `internal/agent/scheduled*.go` `internal/agent/tools_prompts*.go` `internal/api/prompts*.go` | nanoMuse, GPL-3.0-or-later, ideas only | clean-room | ask-agent | pending | implemented |
| CR-AGENT-TAINT | `internal/agent/taint*.go` `internal/agent/pageread/*.go` `internal/agent/tools_page*.go` `internal/agent/redact*.go` `internal/identity/publichost*.go` | nanoMuse, GPL-3.0-or-later, ideas only | clean-room | ask-agent | pending | implemented |
| CR-AGENT-LOOP | `internal/agent/pirpc/repeat*.go` `internal/agent/standin*.go` `internal/agent/stepphrase*.go` | nanoMuse, GPL-3.0-or-later, ideas only | clean-room | ask-agent | pending | implemented |
| CR-AGENT-TASKS | `internal/agent/tasks/*.go` `internal/agent/taskrun*.go` `internal/agent/tools_tasks*.go` `internal/api/agent_tasks*.go` | CopilotKit OpenMuse, MIT (github.com/CopilotKit/openmuse at a4944f4c), ideas only: a delegated task that keeps a plan, waits for the user's input and ends only by an explicit finish | clean-room | ask-agent | pending | implemented |
| CR-SUBSCRIPTIONS | `internal/llm/chatgpt*.go` | OpenAI public documentation of Sign in with ChatGPT plan usage (developers.openai.com/siwc/token-sharing-open-source and its sub-pages) and the behavioural spec in openspec/changes/subscription-providers/research.md section 1a; no client source was read | clean-room | llm-routing | pending | implemented |
| DATA-BOARD-SLUGS | `internal/discovery/registry*.go` | public board slug lists (job-board-aggregator, MIT data; freehire lineage), fetched at setup, never committed | data-fetched | job-discovery | pending | implemented |
| ASSET-DOC-FONTS | `internal/documents/assets/fonts/**` | Lato 2.015 (tyPoland, SIL OFL-1.1) and Raleway 4.101 (The Raleway Project Authors, SIL OFL-1.1), unmodified from CTAN and the Lato foundry; licence texts in third_party/licenses | asset-shipped | application-documents | pending | implemented |
| ATTR-COMPUTER-USE | `computeruse/**` | the owner's own PersonalCU; `Sources/PersonalCUCore/BackgroundInput.swift` adapts Cua, MIT (commit c797b9ba62cff0b018c30dd4a3db6c0dc1466e08), and the yabai focus-record sequence Cua derives, MIT (NOTICE; licence text in third_party/licenses) | attributed-source | computer-use | pending | implemented |

## Externally installed tools

These are installed by the user and are neither linked nor bundled. Copying any
of their binaries into the app requires a new distribution review. Versions
proven by the toolchain smoke are in
[architecture/document-toolchain.md](architecture/document-toolchain.md), which
also lists the TeX package closure with each package's licence.

| Tool | Role | Licence inventory |
|---|---|---|
| Python 3.11 or newer and pypdf 6.19.0 (private venv, hash-locked) | PDF verification | Python: PSF-2.0; pypdf: BSD-3-Clause; pip (venv bootstrap): MIT |
| Poppler 26.10.0 (`pdftotext`, `pdftoppm`) | PDF text, box and raster tools | GPL-2.0-only or GPL-3.0-only; run as a separate process, never linked |
| TeX Live 2025 (LuaTeX 1.21.0, XeTeX, moderncv 2.4.1 and the closure) | document compilation | per package, mostly LPPL-1.3c; table in the architecture document |
