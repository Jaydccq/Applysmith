# Additional provider APIs

Goal: expose the requested providers through the existing provider, model and
routing surfaces. Success means provider instances keep separate vault keys,
requests use documented API fields, unsupported capabilities stay disabled,
and the explicitly selected OpenCode Go subscription can serve job-search tasks.

The implementation uses the existing OpenAI-compatible transport and preset
table. No Orca implementation code or browser cookies are copied. Documentation
was checked on 2026-10-24; model availability and subscription rules can change.
Local fake-server tests verify request behavior, not production authentication
or model quality. No external model request was made during implementation.

## Callable presets

| Preset | Base URL | Fast / standard / deep defaults |
| --- | --- | --- |
| `opencode-go` | `https://opencode.ai/inference/go/openai/v1` | `glm-5.3-flash` / `glm-5.3` / `glm-5.3` |
| `kimi-code` | `https://api.kimi.com/coding/v1` | `kimi-for-coding` for all classes |
| `minimax` | `https://api.minimax.io/v1` | `MiniMax-M3.1-Flash` for all classes |
| `minimax-cn` | `https://api.minimax.cn/v1` | `MiniMax-M3.1-Flash` for all classes |
| `zai` | `https://api.z.ai/api/paas/v4` | `glm-5.3-flash` / `glm-5.3` / `glm-5.3` |
| `bigmodel` | `https://open.bigmodel.cn/api/paas/v4` | `glm-5.3-flash` / `glm-5.3` / `glm-5.3` |
| `xai` | `https://api.x.ai/v1` | `grok-4.7` for all classes |

OpenCode Go uses subscription allowance; the other new presets use general API
billing. Existing CLI and
subscription integrations retain their own sign-in and usage behavior. A saved
usage-monitoring credential does not authorize an inference API automatically.
No new preset provides embeddings or the runtime's OpenRouter search plugin.
Although BigModel offers embeddings separately, that integration is not
advertised until its API and pricing path is verified.

## Transport decisions

- OpenCode Go uses the current documented OpenAI-compatible Go endpoint with
  GLM models only. Claude and GPT entries using different protocols are not
  inserted into this preset. GLM effort and output ceiling semantics match the
  direct GLM presets below. Requests identify themselves as `applysmith/1.0` and
  send `x-opencode-session`: the existing run/session ID, or a standalone call's
  stable call ID reused for retries and structured-output repair.

- MiniMax uses `max_completion_tokens`. M3.1 Flash always reasons; the app's off
  and minimal levels send `low`, and its highest level sends `max`. The server
  ignores temperature, so the client leaves it out. Full assistant
  `reasoning_content` survives tool turns, as required by the SDK guide. JSON
  schema response formats are not advertised. Its documented image input is
  allowed. M3 is not a default because it has different reasoning controls.
- GLM 5.3 and Flash always reason, support `low`, `high`, `max`, and use
  `max_tokens`. Medium maps to `high`; off/minimal map to `low`; highest maps to
  `max`. The default enabled thinking setting is retained. The gateway sets
  `tool_stream: true`. The API documents `json_object`, not JSON Schema, so
  native schema output remains disabled. Flash takes images; flagship 5.3 is
  text-only.
- Kimi Code (owner decision 2026-10-24: subscriptions as providers, after
  openspec/changes/subscription-providers, task S3) takes a member's own API
  key from the Kimi Code console on the OpenAI-compatible coding endpoint.
  Calls use the membership allowance (billing `subscription`, $0). The terms
  say "Tampering with the client identifier (User-Agent) is considered a
  violation", so every request, task or agent, sends `User-Agent: applysmith/1.0`.
  The default `kimi-for-coding` is the model every membership tier has (`k3`
  and the high-speed model need higher tiers). No thinking field is sent and
  JSON Schema output is not advertised: the coding endpoint documents neither
  on its OpenAI-compatible route. Not yet checked live with a member key.
- Grok 4.7 uses `reasoning_effort`: off/minimal map to `low`, highest to `xhigh`.
  JSON Schema output and images are supported. The existing Chat Completions
  transport is documented as a deprecated endpoint that is still offered. It
  cannot carry encrypted reasoning between tool turns, which limits multi-turn
  quality. No new Responses API integration is introduced by this change.

## Billing and regional setup

Embedded standard USD prices per million tokens are input / cached input /
output: MiniMax Flash `0.15 / 0.015 / 0.60`; Z.AI Flash
`0.15 / 0.03 / 0.50`; Z.AI 5.3 `1.40 / 0.26 / 4.40`; Grok 4.7
`2.00 / 0.50 / 6.00`. These are standard listed rates, not a guarantee of the
account's invoice. Provider priority tiers, special regions and long-context
pricing may differ. The existing user-price override wins.

China MiniMax and BigModel publish CNY rates. Those numbers are not inserted
into the USD ledger, and international prices are not reused. Configure the
provider instance and key first, then save that instance's per-model USD prices
under Settings → Models. Until then, inference fails as `unconfigured` before
network access. `RequireUserPrice` in the preset table enforces this check for
every candidate, including a fallback or named model override. A price for a
different instance does not satisfy it. Configuring the provider itself is
allowed so the user can reach the existing price editor without a dependency
cycle. No exchange rate is guessed or silently refreshed.

OpenCode Go also publishes token rates for valuing included allowance:
Flash `0.15 / 0.03 / 0.50`, flagship `1.40 / 0.26 / 4.40`. These entries remain
in the reference catalog, but inference ledger rows use `subscription` with
`$0` incremental cash cost, including when a user adds a price override. They
do not count as metered daily spend or enter metered downgrade comparisons.
Plan quota usage is reported separately by the account usage monitor; monthly
subscription fees are not amortized into token calls.

## Subscription boundaries

On 2026-10-24 the user explicitly requested OpenCode as Applysmith's general
job-search model API (translated: add OpenCode directly; it may serve as
Applysmith's general job-search model interface).
The callable `opencode-go` preset implements that choice. OpenCode's current
public guide still describes coding-agent traffic as its intended scope; the
user's choice does not establish provider approval for non-coding traffic.
That remains a documented service-scope caveat, not an application routing
block. The client truthfully identifies Applysmith and does not impersonate
OpenCode or another approved client.

GLM standard API keys use `/api/paas/v4`; the Coding Plan
endpoint `/api/coding/paas/v4` is not substituted. A plan-monitoring card does
not change task routes or copy its key to a general API provider. MiniMax
subscription monitoring and regular API billing likewise remain distinct.

## Primary sources

- [MiniMax international OpenAI SDK](https://platform.minimax.io/docs/api-reference/text-openai-api)
  and [Chat Completions parameters](https://platform.minimax.io/docs/api-reference/text-chat-openai):
  endpoint, model IDs, reasoning, output ceiling, tools and history preservation.
- [MiniMax China OpenAI SDK](https://platform.minimax.cn/docs/api-reference/text-openai-api):
  China endpoint and current model IDs.
- [MiniMax international pricing](https://platform.minimax.io/docs/pricing/overview)
  and [China pricing](https://platform.minimax.cn/docs/pricing/overview): USD and
  CNY are different billing units.
- [Z.AI Chat Completion API](https://docs.z.ai/api-reference/llm/chat-completion),
  [GLM 5.3](https://docs.z.ai/guides/llm/glm-5.3),
  [standard API key setup](https://docs.z.ai/guides/overview/quick-start) and
  [USD pricing](https://docs.z.ai/guides/overview/pricing).
- [BigModel GLM 5.3](https://docs.bigmodel.cn/cn/guide/models/text/glm-5.3),
  [Flash](https://docs.bigmodel.cn/cn/guide/models/vlm/glm-5.3-flash), and
  [CNY pricing](https://docs.bigmodel.cn/cn/guide/start/pricing).
- [Grok 4.7](https://docs.x.ai/developers/models/grok-4.7),
  [reasoning](https://docs.x.ai/developers/model-capabilities/text/reasoning),
  [JSON Schema](https://docs.x.ai/developers/model-capabilities/text/structured-outputs),
  [Chat Completions](https://docs.x.ai/developers/model-capabilities/legacy/chat-completions),
  [parameter comparison](https://docs.x.ai/developers/model-capabilities/text/comparison),
  and [best practices](https://docs.x.ai/developers/model-capabilities/text/best-practices).
- [OpenCode Go](https://dev.opencode.ai/v2/docs/console/go): client traffic
  requirements and subscription scope.
