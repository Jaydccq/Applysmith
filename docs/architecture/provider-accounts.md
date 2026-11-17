# Provider accounts and subscription quota

## Behavior

Settings → Models and providers has three blocks (`web/src/pages/settings/`):

- **Accounts** (`Accounts.tsx`, `ChatGPT.tsx`, `PlanUsage.tsx`): one card per
  service, not per mechanism. Subscriptions are the presets the server marks
  `subscription` (sign-in, command-line and plan-key presets); the rest are
  pay-per-use API keys. A plan's usage windows are drawn inside its card when
  the service has answered, and its usage credentials sit under that card's
  Manage. Coding agents that cannot run a job are listed apart, collapsed, with
  one line saying so. Connecting an account (a key, adding a program, signing
  in) adds the provider only: it never applies a preset or changes a route.
  Add another account creates an independently named provider the same way.
  Each API instance uses the existing vault key `api-key-<id>`.
- **What each job uses** (`Jobs.tsx`, `JobEditor.tsx`, `Pools.tsx`): the
  default (the active preset and its backup, changed through
  `POST /api/llm/preset` after a dialog that says which jobs it moves), then the
  jobs by area. A job is Default (follows the preset) or Custom (has its own
  row); Use default clears the row. The editor is a drawer: model, ordered
  backups, and under Advanced the thinking level, limits and the daily budget.
  A change that touches only the budget is sent as `budgets` alone, so a job on
  the default stays on it. Model pools (routing groups) are under Advanced.
- **Spending**: the daily cap and the spend it counts.

A provider still referenced by a preset, route, fallback or group cannot be
removed. Endpoint changes require clearing the old key first; endpoint URLs
cannot carry embedded credentials, a query or fragment. Removal deletes the
instance's key and its manual prices, and refuses environment-managed
credentials. The layout of the whole section, every disclosure open, is checked
at 360, 768 and 1280 px by `TestModelsSectionLayoutFitsThreeWidths`
(`APPLYSMITH_LAYOUT_SHOTS=<dir>` also writes screenshots).

Model prices are USD per million tokens, scoped to the exact instance/model.
Chinese MiniMax and BigModel need these prices before inference. The table of
models, billing semantics and transport details is in
[the provider catalog](llm-provider-catalog.md).

Optional plan credentials are independent of model API credentials. Only
Refresh usage contacts the external provider. Saving or removing a credential
invalidates its quota cache, including in-flight results from the previous
credential. MiniMax keys take priority over legacy console cookies. Overseas
and China credentials never cross regions. GLM keys target only Z.AI or
BigModel as selected; OpenCode Go supports a key or a legacy console Cookie
header plus explicit workspace ID. The implementation uses fixed HTTPS
origins, rejects redirects, limits requests to 12 seconds and response bodies
to 1 MiB, and does not return remote response text or raw transport errors.
Unknown, expired or unrecognized quota is never rendered as zero usage.

OpenCode, Devin, Claude, Codex, Gemini, Antigravity, Grok and Cursor native
cards detect the installed program (and fixed login-file presence where
applicable) and show their native sign-in command/console. File existence is
not proof of a valid sign-in. Native multi-account switching and native
subscription quota are **not implemented**; these cards explicitly say so.
Existing Codex/Claude completion presets, Gemini API and Applysmith's own ChatGPT
OAuth continue to provide their existing callable capabilities. No native
token is read, imported or refreshed by this feature. The provider interface
does not claim every native coding agent is a plain model API.

The user explicitly requested OpenCode Go as a general Applysmith model interface
on 2026-10-24. It is included as a callable preset; this supersedes the initial
monitor-only implementation assumption. No live inference or quota refresh
using the user's credentials was performed during development.

## Orca investigation: observed versus inferred

Target: installed `/Applications/Orca.app/Contents/Resources/app.asar`, package
version **1.4.221**, ASAR SHA-256
`4abe9fba9e7e95b7db889d263c2abbaf84244d994acd1f745b98cd0a48c15aac`.
The main bundle SHA-256 is
`f61e7e1b43221801ded277df879c929c046a2f814b6c9f57fa956f68728f50fa`.
The target was read as inert text; no extracted code was executed or copied
into Applysmith. Credentials and account stores belonging to Orca were not read.

REA evidence:

| Evidence | Scope / result |
| --- | --- |
| `ev_c159121f81123450d0f2163c71601480331fd09cfbdd9ece561f31bd0ab4d196` | Initial ASAR selection including the main bundle; 60-second timeout, partial analysis; cannot establish complete main-process behavior. |
| `ev_3b0b8062145c940d5715e2fb61b918753f01d05f672786bb08175d6be5ed9c85` | ASAR package + exact `out/renderer/assets/Settings-B0NB96ds.js`; parsed 214,239 AST nodes / 1,103 findings. Remaining files and dynamic relationships are unknown; semantic retention was limited. |
| `ev_727cdf6957b93f635ab7235bcb393455f7a5f0168d5e69d20e4365a2cfb7129a` | Seven small exact main-bundle declarations extracted for focused REA parsing; 7 parsed files, 803 AST nodes, no parse failures. Complete slice inventory, not complete app coverage; 148 semantic unknowns. |
| `ev_23c853e6523ecdae07acae603cffc3de081b7ab3ad532d83b92bce101fd72231` | Retained module evidence for the OpenCode Go legacy workspace/status fetch function; original function slice digest retained. |

[Source provenance](../audits/2026-10-24-orca-source-provenance.json) maps each
slice back to the main bundle by JavaScript UTF-16 start/end offsets, source
UTF-8 byte start and SHA-256. The retained paths identify the development
snapshot, not a required runtime dependency. No source bodies are committed.

The focused main declarations show these **static observations**, not live
network captures:

- `YTi`, UTF-16 offset 5,354,700: main IPC handlers connect rate-limit refresh
  channels to refresh operations, including Claude/Codex target selection,
  inactive-account fetches, MiniMax and Grok.
- Offset 5,935,769: MiniMax legacy console quota endpoint constants.
- `I3i`, offset 5,943,666: GLM quota configuration constructs
  `/api/monitor/usage/quota/limit` after checking its allowed HTTPS origin.
- `Zwa`, offset 6,472,584: OpenCode Go validates an explicit workspace or
  discovers workspaces, requests console quota with `x-org-id`, and reports
  missing-cookie/unknown-data failures. Cookie-name constants include
  `auth`, `__Host-auth`, `__Host-console_session`.

Inference used in Applysmith design: account selection, inference credentials and
subscription monitoring are separate concerns despite appearing on one Orca
settings page. Applysmith implements them as provider configurations, vault-only
secrets, and a separate quota package. This is an independent implementation.
Orca's actual native account switch behavior, active-session isolation and
current remote API acceptance were not proven by these static excerpts.

## Current API sources and limits

The installed Orca bundle is older than current provider documentation. MiniMax
key-based monitoring therefore uses current `www.minimax.io` or
`www.minimaxi.com` `/v1/token_plan/remains`; legacy cookie mode uses the older
console endpoint observed in Orca. Explicit remaining-percentage fields are
used; ambiguous legacy count fields do not produce fake zero bars.

- [MiniMax Global Token Plan](https://platform.minimax.io/subscribe/token-plan)
  and [China plan](https://platform.minimaxi.com/subscribe/coding-plan).
- [MiniMax CLI issue 249](https://github.com/MiniMax-AI/cli/issues/249),
  [165](https://github.com/MiniMax-AI/cli/issues/165), and
  [53](https://github.com/MiniMax-AI/cli/issues/53) record current percentage
  fields and ambiguity of old count values; issue reports are narrower
  evidence than a versioned API contract.
- [ZCode usage statistics](https://zcode.z.ai/en/docs/usage-stats) describes
  Coding Plan quota; the quota HTTP shape is an observed internal API.
- [OpenCode Go](https://dev.opencode.ai/v2/docs/console/go) documents model
  endpoints and plan scope; monitoring HTTP shapes remain observed internal
  APIs and may change. Failures stay unknown rather than silently substituting
  a different endpoint or account.
- Native sign-in instructions link to each provider's official documentation
  in the account cards. CLI installation and file detection alone do not
  validate those remote accounts.

## Verification

Tests exercise provider CRUD with separate keys and preserved routes, URL and
capability validation, referenced-provider refusal, keychain/environment errors,
manual price persistence, local-only mutations, quota schema parsing, credential
precedence, fixed destinations, redirect refusal, response limits, unknown
quota, concurrent refresh rejection and invalidation during credential changes.
Frontend tests exercise add/remove, secret input clearing, explicit refresh,
unknown-versus-zero usage, native sign-in boundaries and price entry.

Real provider sign-in, account-specific model access and live quota remain
unverified without a user-initiated credential-backed run. Native multiaccount
switching and native quota are explicit capability gaps, not hidden successes.
