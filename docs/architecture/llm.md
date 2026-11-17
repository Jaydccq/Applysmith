# LLM routing

`internal/llm` is the only code that calls a model for evaluation, discovery and
document tasks. The Ask Agent loop runs on pi (design D7), and pi's model calls
come back here through the agent gateway (below), so they are walked, rested,
capped and ledgered like every other task's.

## Layout

| File | Role |
|---|---|
| `providers.go`, `presets.go`, `tasks.go` | Provider kinds and capabilities, the shipped preset table, the 26-task registry |
| `settings.go`, `settings_ops.go`, `resolve.go` | Settings in SQLite (`llm_settings`), validation, per-call resolution (table, environment, preset) |
| `groups.go` | Routing groups: validation, the patch, and the mode that orders a group's members for a call |
| `call.go`, `walk.go`, `limiter.go` | `Complete`, the candidate walk, deadlines, per-provider concurrency |
| `openai.go`, `anthropic.go`, `anthropic_schema.go`, `cli.go`, `fake.go` | The four provider kinds over `net/http` and `os/exec`; no provider SDK (design D7) |
| `cap.go`, `ledger.go`, `report.go` | Daily cap and downgrade, the cap alert claim, the per-call ledger, the cost report |
| `prices.go`, `prices_update.go` | Embedded price table, user and refreshed prices, `UpdatePrices`, drift check |
| `structured.go`, `jsonschema.go`, `embed.go`, `watch.go` | Structured output with one repair, embeddings with the SQLite cache, the routed-model catalog check |
| `chatgpt_auth.go`, `chatgpt_oidc.go`, `chatgpt_client.go` | The `chatgpt` kind: Sign in with ChatGPT (loopback PKCE, the token family, the one refresher, sign-out), ID-token validation, and the Responses client with its error mapping ([ChatGPT plan usage](#chatgpt-plan-usage)); clean-room, register row `CR-SUBSCRIPTIONS` |
| `keys.go` | Provider keys through `internal/vault` (`api-key-<provider id>`) and key validation |
| `agent.go`, `gateway.go`, `affinity.go` | The agent-kernel route a pi process is launched with, the gateway that serves its model calls, and the conversations' last answerers |
| `test.go` | `Router.Test`, the fixed live test with error classes, and `TestStatus` for the saved result (`llm_settings.last_test`) |

Schema: `internal/store/migrations/0002_llm.sql`.

## Rules worth knowing

- Settings, the environment and the active preset are read on every call. The
  router remembers only the rests of candidates that failed lately (below) and
  which candidate answered each agent conversation last (Agent gateway), in
  memory only.
- A failed candidate rests, typed by why it failed, and a resting candidate
  moves behind the others for the next calls but is never dropped (see "Rests").
- A provider key has one path: the vault, which also owns the
  `APPLYSMITH_SECRET_<NAME>` override. A guard test fails if the package imports a
  keychain library or names the override variable.
- The walk advances only on the kinds listed in the `llm-routing` spec. A reply
  that holds text and hit the ceiling is returned as `truncated`; one with no
  text advances as `empty-completion`.
- A row's `first_token_seconds` (0 = off) arms a second watchdog on streaming
  API attempts: no stream event within it fails the attempt as
  `first-token-late`, the walk advances (ledger reason `first-token`), and the
  candidate does not rest. The first event is the first SSE data line, as in
  the ledger's `ttft_ms`, so a reasoning model that streams nothing while it
  thinks needs a deadline longer than its thinking. It never applies to
  web-search calls (not streamed), `cli` providers or the fake provider.
- A stream that is silent for the stall budget (45 s, or the row timeout when
  that is shorter) fails as `stream-stalled`; `timeout` therefore needs a stream
  that stays alive past the row timeout.
- Over the cap, a call runs on the cheap head, then on candidates no dearer
  than it; the configured primary is reachable only after an auth failure on
  the head, so no fallback kind hands an over-cap call to the dear model.
- The cap and the task budgets count the metered spend of this computer's day,
  from local midnight to the next (23 or 25 hours across a daylight-saving
  change), the same "today" as the rest of the app (decision 2026-10-23; it
  was the UTC day). The 15-second reuse of the read ends at midnight.
- A task's daily budget (`task_budgets`, USD, above 0 and at most 1000) is the
  cap for that task alone (decision 2026-10-23): at or over it, its calls take
  the downgrade above, and over the budget of `jd-extract`, `quick-screen` or
  `deep-eval`, `EvaluationHeld` holds unattended evaluation as the cap does. A
  task the cap never moves (`exemptFromDowngrade`, embedding, the agent
  kernel) takes no budget. A reached budget raises no alert.
- A cli provider runs in its own process group; cancel and timeout signal the
  group (terminate, then kill), so children of the program go too. On Windows
  only the program itself is ended.
- Model ids must match `ValidModelID` (never a leading hyphen) at route
  validation, for env pins and overrides, and again before a cli exec.
- Ledger writes are best effort and bounded by two seconds.
- The structured-output validator supports a JSON Schema subset and refuses a
  schema with any other keyword, a pattern that does not compile, or an
  `additionalProperties` that is a schema (a map: both providers' sent forms
  close every object, so a map's entries are listed as an array of objects),
  before a request is made. `llm.CheckSchema` lets a fake model refuse the same.
- A stream that ends without its terminator (`[DONE]` or a finish reason;
  `message_stop`) fails as `network` and advances. When a provider sends no
  usage, tokens are estimated from text length and the ledger row's error text
  says "usage estimated"; there is no separate column.
- A structured repair is part of the first call: same call id, attempt numbers
  continue, and the response's usage and cost are the sum.
- Prompt-cache writes (`cache_creation_input_tokens`) are carried in
  `Usage.CacheWrite` and priced at 1.25x the input rate (the five-minute
  cache).
- The plan route caches the agent gateway's requests (agent-kernel on
  gpt-6.1-sol: 59% of 7.9 million input tokens read from the cache, 2026-11-01
  to 11-08) but almost none of the router's own task requests on the same
  model (apply-complete 0.3%, deep-eval 0.1%). Measured 2026-11-08 on two
  byte-identical apply-complete prompts of 6,333 tokens sent 30 seconds apart:
  no cached token with `prompt_cache_key` set per task, nor with the system
  text sent as a developer message instead of `instructions`; neither change
  was kept. The cause is not known; prompt size is the lever that is.
  Prefix order is not the cause either: deep-eval's prompt opens with about
  4 thousand static tokens (the system line, the template's head, the CV), and
  79 of its 119 consecutive calls on 2026-11-07 and 11-08 came within five
  minutes of the one before, yet none read the cache. What pi sends and the
  router does not is untested: `include: ["reasoning.encrypted_content"]`,
  `tools`, input content as typed parts, and `reasoning.summary`.
- Outside back-to-back batches the agent gateway's share was lower: 23% on
  2026-11-13 and 32% on 11-14, and a turn's second request, about 4 seconds
  after its first, read no cached token in about 70% of calls. A hit was
  nearly always 10 to 11 thousand tokens, the whole static prefix, so the
  prefix itself was stable. Since 2026-11-16 the gateway also names the
  conversation in the `session-id` and `x-client-request-id` headers, which
  pi's own plan client sends and the gateway had not. Not yet measured live:
  read the cached share of `agent-kernel` in `applysmith llm cost` after a day of
  use on a build with the change.
- `applysmith llm cost` reports, per task and per provider, the input, output and
  cached token totals, the tokens of failed attempts (WASTED: an aborted,
  timed-out or refused attempt is billed and its reply thrown away), the runs
  the calls carried and the tokens per run. Measured 2026-11-06 to 11-08 (two
  days, 4.5 thousand calls): 9.45 million input and 1.23 million output tokens,
  63.5 thousand wasted (50 thousand of them research-search calls the caller
  cancelled); about 7.3 thousand tokens per application run, and per evaluated
  posting 13.3 thousand on deep-eval, 5.4 thousand on cover-letter and 3
  thousand on document-review.

- Native web search is a provider capability (`CapWebSearch`, from the preset's
  `WebSearch` flag; OpenRouter only). `Request.WebSearch` (max results) adds the
  `web` plugin with `engine: "exa"` to the chat-completions body, sends the call
  unstreamed, keeps only candidates that have the capability, and returns the
  cited pages as `Response.Sources` (`url_citation` annotations of the reply
  message). The engine is named because a measured call without it was served by
  the model vendor's own search and came back with no annotations, so nothing
  could be checked; streamed replies carried none either.
  `Router.NativeSearch(task)` says whether the task's primary route has it.
  The users of the search are `research_question` and the answer bank's
  live-fact lane, both through `internal/research`.
  A successful attempt that asked for results adds the search fee to its ledger
  cost (`Costing.withSearch`; OpenRouter's list price for the exa engine: USD
  0.007 per request with up to ten results, USD 0.001 per further result), an
  unpriced model stays unpriced, and a failed attempt is not charged.

## Rests

Learned from magpie's gateway routing (`gateway/routing.go`), without its
account pools or quota strategies. `noteFailure` in `walk.go` records a
`Rest{Why, Until, Failures}` per `provider/model` (per provider for an
account-wide failure) and `restOrder` stable-sorts the candidates of the next
call: not resting, then resting, with a downgraded call's auth-only primary
last. Nothing is dropped, so when every candidate rests the configured order
stands and a call still makes its attempts.

| Failure | Rest |
|---|---|
| out of credit or plan (`quota-exhausted`: 402, or a 429 or 403 body such as `insufficient_quota` or OpenRouter's "Key limit exceeded") | the provider, 30 min, or the reset time the reply named (header, `resets_in_seconds`, `retryDelay`), up to 24 h |
| 429 with a delay | that long, up to 1 h |
| 429 without one | 1 min, doubling to 30 min |
| timeout, 5xx, a connection that broke, stalled stream | 1 min, doubling to 10 min (a delay on the reply is honored, up to 1 h) |
| the address cannot be reached (the name did not resolve, the dial failed, or the system reports the path broken: packets never acknowledged, `ETIMEDOUT`, or no route; `Error.Unreached`) | the provider, the same lengths: every model shares the address. A reset is not one: the server answered |
| model gone | 30 min |
| 401, or a body that says the key is revoked or invalid | the provider, 30 min |
| other 403, bad request, truncated, runaway, empty completion, missing key | none: a rest would hide a configuration error or punish a model for a prompt |

A connection is given 10 seconds (`dialTimeout` in `router.go`; the provider
SDKs give 5). With Go's default of 30, an unreachable provider cost every
turn's first model call 30 seconds before the fallback (live, 2026-11-02:
30 of a 44-second turn), and its other models each paid it again.

The count restarts after 30 quiet minutes; a success clears the model's rest
and its provider's; `SetKey` clears the provider's. The ledger's `reason`
column records why an attempt ran where it did (`fallback`, `rested`,
`cap-downgrade`, `affinity`, `retry` for the one wait on the same candidate
after a short 429 delay, which the cost report does not count as a fallback,
and a routing group's `rotate` and `usage`; migration 0046). Rests are listed in `GET /api/llm/routing`
(`rests`) and lost on restart, which is deliberate: a restarted app asks
every candidate again.

## Routing groups

Owner request 2026-10-23, after yetone/magpie's route groups: a task row can name
a group instead of one model, and each call spreads over the group's members.
A group (`groups.go`; `llm_settings` key `groups`) has a name (lowercase letters,
digits, `-` and `_`, 1 to 40, starting with a letter or digit), a mode and 1 to 8
members, each a configured `provider/model`. A `pi:` login and another group
are refused as members (no nesting), as is a member listed twice. A row names a
group with provider `group` and the group's name as the model (`group/<name>`);
its thinking, ceiling and timeouts apply to every member, and its fallbacks
follow the members. A group can be the primary only, and no embedding task takes
one, because vectors of different models cannot be compared. `group` is not a
valid provider id.

| Mode | First member of a call |
|---|---|
| `order` | The first listed; the rest as listed |
| `rotate` | The next one each call (a per-group counter in the router, in memory, from the first member after a restart), then the rest in list order |
| `usage` | The one with the fewest ledger attempts of the local day, any task and outcome; ties in list order |

`usage` reads the day's counts with the cap's 15-second spend read
(`capCache.calls`, one grouped query beside the spend query), and adds each call
it routes at once, so a burst inside the 15 seconds still spreads instead of all
going to the member that was least used at the read.

**Order of steps.** The mode comes first (`callCandidates`): members as the mode
orders them, then the row's fallbacks. Then, exactly as for any candidates, the
cap's `downgradeOrder` (which sees the members like any candidates, so over the
cap the cheapest member leads), `restOrder` (a resting member goes behind
whatever the mode chose), and for agent calls `affinityOrder` (a conversation
stays on the member that answered it, so a rotation never moves a turn that is
worth keeping where it is). The rotation counter advances once per call the
rotation placed: a task call always, an agent call only when affinity did not
keep it (magpie rotates per round; advancing on kept calls sent every new
two-call conversation to the first member, seen live 2026-10-23), and never for
a look at the route such as `AgentCapBlocked`. A usage group counts an agent
call on the member that answered it.

**Ledger reason.** `rotate` or `usage` is recorded when the mode put a member
other than the first listed in front and nothing later moved it (`cap-downgrade`,
`rested`, `affinity` and `fallback` win). A call the mode left at the first
listed member has no reason, as with `order`.

**Agent.** The agent gateway reads the row like any task's: pi is launched with
the first listed member (its model id and wire; `TakesImages` is asked of that
model), and the gateway serves each call from the members whose provider kind
serves the call's wire. Members of another wire are reached by pi's own routing
([Rows that span wires](#rows-that-span-wires)).

**Edit.** `applysmith llm group set <name> --mode order|rotate|usage --members
p/m,p/m`, `applysmith llm group rm <name>`, `applysmith llm set <task> group/<name>`;
`llm show` lists groups. `PATCH /api/llm/routing` takes `groups` (name to group,
null deletes) beside `routes` and validates the rows against the groups of the
same patch; `GET` carries `groups`. A group a row still names cannot be deleted:
the refusal names the rows. Settings has a Groups section, and `group` is offered
in a row's primary provider picker.

## Agent gateway

Owner decision 2026-10-23 (Ask Agent kernel, after magpie): pi's model calls go
through this package instead of straight to the provider. Before, pi called the
`agent-kernel` row's primary itself with the key in its environment: no rests,
the row's fallbacks never walked, and over the daily cap the turn was refused.
Measured the week before (ledger, 2026-10-15 to 10-22): 207 agent calls on
`deepseek-flash` cost USD 0.144 in all (USD 0.0005 to 0.005 a turn, 92% of input
read from DeepSeek's cache), so the change is for routing, not for price.

pi gets one provider, `applysmith`, in its `models.json`: the bridge's model route
(`/internal/agent/llm`, pi appends `/chat/completions`, `/v1/messages` or
`/responses`) as the base URL, the wire of the row's primary
(`openai-completions`, `anthropic-messages` for an Anthropic primary, or
`openai-responses` for a ChatGPT plan primary), the primary model at price 0,
and `$APPLYSMITH_AGENT_TOKEN` as the key, which `authHeader` sends as a bearer
token on every wire. No provider key reaches pi's environment or disk, and the
plan's token never does: the gateway adds it. A row whose candidates speak more
than one wire is declared differently ([Rows that span wires](#rows-that-span-wires)).
`ServeAgentCall` (`gateway.go`) then serves each request:

| Step | Rule |
|---|---|
| Candidates | The row's primary and fallbacks whose provider kind serves the wire the call arrived on; the gateway never translates between wires; a request that carries an image skips the models `TakesImages` does not list |
| Order | A routing group's mode (below); over the cap, `downgradeOrder`; then `restOrder`; then affinity |
| Over the cap with nothing cheaper | A metered primary refuses the call (`cost-cap`); `AgentCapBlocked` makes the turn refuse before pi starts, and a refusal mid-turn ends the turn as `cost-cap` |
| Request | pi's body, passed through, with `model` set; on the Responses wire the plan's rejected fields removed, `store` false, `stream` true, `prompt_cache_key` the conversation, the same id in the `session-id` and `x-client-request-id` headers (as pi's own plan client sends them) and system items sent as developer items (the plan route refuses the role), and the plan's token as the bearer; on the OpenAI wire also the output ceiling in the preset's field, the row's thinking level as `reasoningField` sends it, `stream_options.include_usage`, `developer` sent as `system`, `store` dropped, `reasoning_content` kept on assistant messages for DeepSeek only, and `prompt_cache_key` (the conversation) for OpenAI only |
| Moving on | The walk's rules, while nothing reached pi: the first data event that is not an error commits the attempt (events before it are held; on the Responses wire `response.created` and `response.in_progress` are held too, because the plan reports a usage limit as `response.failed` right after them); an error event before it advances like a failed status |
| After commit | The rest of the stream is copied as it comes; a failure is pi's to see and the ledger row records it. pi may stop reading once it has the whole reply (the terminator, or on the OpenAI wire a finish reason) before the usage that follows; that call is `ok` with its finish, its answerer is kept, and its usage is estimated when none came. Live, an OpenRouter call pi had in full was recorded `aborted` that way, and its answerer was dropped |
| Ledger | One row per attempt, task `agent-kernel` (`agent-light` for a light turn), the conversation as run id, the provider's own kind and price; never debug-captured (agent turns carry the user's records) |

For a row of one wire pi's own retry is off (`set_auto_retry false` in the
handshake): it would walk the candidates again. A `pi:` login is still served
by pi itself and ledgered from its usage events (`RecordAgentUsage`).

### Rows that span wires

The gateway never translates between Chat Completions, Messages and Responses
(owner decision 2026-10-24, replacing the translation plan of the subscription
change): a call is walked among the candidates of its own wire. A row such as
`chatgpt/gpt-6.1-sol` with `deepseek/deepseek-flash` behind it has two wires, and
moving a turn from one to the other is pi's work, which it already does when a
conversation changes model (it replays the history across providers itself).

- **Declaration.** `AgentRoute.Heads` lists the row's first candidate on each wire,
  a wire whose first candidate rests last. `models.json` then holds one physical
  provider per wire (`applysmith-responses`, `applysmith-chat`, `applysmith-messages`, with
  `-light` after `applysmith` for the light row), each with the `api` and the
  `compat` of its head. pi is started on `applysmith/agent`, a virtual model the
  embedded extension registers when `APPLYSMITH_AGENT_ROUTE` is set (the order of the
  physical models). A row of one wire is started as before.
- **Routing.** The extension's `route()` returns the first model that has not
  failed in this process (one process is one turn), for a user message, a tool
  result and a retry alike: the models are tried in order, so that is also the
  model that answered the turn's earlier requests. After a failure the next one
  follows, and a failed model is never asked again in the turn, a steering message
  included. When none is left, `route()` throws and the turn ends with the last
  error.
- **Retry.** `settings.json` in the agent directory is the same for every
  process (`retry.maxRetries 2`, `baseDelayMs 0`, `maxAgentDelayMs 0`: at most one
  retry per other wire and no wait, since another provider is asked). Whether a
  process retries is decided by its handshake alone: `set_auto_retry` is sent
  explicitly, `true` for a row that spans wires and for a `pi:` login, `false`
  for a row of one wire. pi checks that switch before the counts
  (`_prepareRetry` and `_willRetryAfterAgentEnd` return at once when it is off), so
  no process depends on what another wrote. A `pi:` login therefore retries up to
  twice with no wait, not three times with backoff. A retry
  repeats the failed model request only; pi never runs the tools of a failed
  response (`agent-loop.js` ends the turn on an error stop reason before it), so a
  retry cannot repeat a side effect.
- **What pi retries.** pi retries a failed response whose text looks transient
  (`retry.js`: a 429 or 5xx status, "overloaded", a stream that ended early) unless
  it holds a phrase it takes for a limit that lasts, and the ChatGPT plan's
  `subscription_sharing_usage_limit_exceeded` is one of them. `AgentErrorText`
  rewords those phrases, so a plan limit moves the turn to the next wire; a bad
  request (400) is not retried. A failure after the reply has begun reaches pi as
  the provider sent it, and ends the turn if pi takes it for a lasting limit.
- **Rests.** A candidate that failed a call rests as for any task, and the next
  turn's `Heads` put its wire last, so a turn after a plan limit starts on the
  other wire. Affinity is applied within a wire only.

Live check, 2026-10-24 (pi 1.0.4, a fake plan and a fake Chat Completions
provider; a throwaway test, not kept): a turn on the plan ran a tool call and
answered with no rejected field in the plan's request; a 429 limit before the
first byte moved the turn to Chat Completions with the history intact, and the
next turn started there with the plan not asked; a 429 on the second call of a
turn (after a tool ran on the plan) moved that call to Chat Completions with the
tool call and its result replayed, the tool run once; with every model failing
each was asked once; a 400 ended the turn; a row of one wire kept pi's retry
off.

pi chooses some request fields from its provider name and base URL
(`detectCompat` in pi-ai's `api/openai-completions.js`, pi 1.0.4), which behind
the gateway match nothing. The gateway sets the fields above per candidate, and
`models.json` carries the two settings that shape the history pi sends:
`requiresReasoningContentOnAssistantMessages` for a DeepSeek primary and
`cacheControlFormat: anthropic` for Claude through OpenRouter.

**Intent routing** (owner request 2026-10-23, after magpie's intent routing: a small model
reads each new message, a simple question goes to the cheap model and the strong
model keeps the work). It is on only while `agent-light` has a route, which no
preset sets (`optIn`; `llm show` mentions it and does not count it missing):
route it with `applysmith llm set agent-light <provider>/<model>` and nothing else
changes until then. `agent-light` follows `agent-kernel`'s rules: provider rows
only, no `pi:` login, no task budget, the cap rules above, and its fallbacks are
served as those of `agent-kernel` are (the gateway within a wire, pi across wires). Before pi starts, task
`agent-intent` (fast class, thinking off, 64 output tokens, 4 second attempt
timeout, a 5 second deadline) reads the new message and at most 300 characters
of the previous answer and must reply exactly `{"tier":"light"}` or
`{"tier":"heavy"}`: a lookup, a short answer or one simple tool is light;
planning, several steps or tools, writing, applying and anything unclear is
heavy. Any failure, timeout or other output is heavy, and a "Let the agent
finish" turn, steering and an empty message never call it. The tier picks the
row for the whole turn: pi's provider and model, its thinking level, the pre-turn
cap check and the ledger task (`agent-light` or `agent-kernel`) follow it, and
`AgentCall.Task` carries it to the gateway. The classifier is ledgered like any
task call with the conversation as run id. A conversation that moves between
rows keeps a stick only when its candidate is on the new turn's row. Detail:
[agent-pi-contract.md](agent-pi-contract.md) section 6.

**Affinity** (`affinity.go`, magpie's `gateway/affinity.go` in its default mode):
a conversation stays on the candidate that answered it last, so the prefix that
provider cached is read again. Within a turn it always stays; on a new turn it
stays while that answer read or wrote at least 1,024 cached tokens (a provider
that reports no cache write, such as DeepSeek or OpenAI, counts the prompt it was
sent) and less than 5 minutes have passed. A resting candidate, the
authentication-only primary behind a downgrade, or a candidate the cap keeps out
is never kept. The ledger reason is `affinity`. At most 512 conversations are
remembered, in memory, and a restart forgets them like the rests.

**Live check, 2026-10-23** (pi 1.0.4, the real DeepSeek behind a switchable
proxy, the real OpenRouter; a throwaway test, not kept): a healthy two-turn
conversation with a tool call stayed on `deepseek-flash` with cache reads; a 503
moved the call to the OpenRouter fallback (`fallback`), the turn's next call
and the next turn stayed there (`affinity`, the second turn reading 1,024
cached tokens), and DeepSeek was called once; a next turn whose fallback answer
used under 1,024 tokens went back to the primary; a stream cut after its first
event ended the turn as pi's `provider_error` with one call and no retry; a
cancel closed the upstream call (`aborted`); two conversations at once kept
their own rows; over the cap `deepseek-v4-pro` ran as `deepseek-flash`
(`cap-downgrade`), and with nothing cheaper no pi started and the reply named
the cap. Found on the way: the owner's `agent-kernel` fallback
`openrouter/nvidia/nemotron-3-super-120b-a12b:free` answered 404 (NVIDIA's
function not found), so it walks to nothing; and OpenRouter's
`openai/gpt-oss-20b` had no price here, so its rows were `unpriced` and counted 0
toward the cap — the owner then ruled the model out (2026-10-23, below).

## ChatGPT plan usage

Owner decision 2026-10-23: the user's own ChatGPT plan is a provider (kind
`chatgpt`, preset `chatgpt`) through OpenAI's Sign in with ChatGPT plan usage,
which OpenAI documents for open-source and locally hosted apps. Written
clean-room from OpenAI's public pages (developers.openai.com/siwc/token-sharing-open-source
and its sub-pages) and `openspec/changes/subscription-providers/research.md`
section 1a. The Codex CLI's and pi's login files are never read.

**Sign in.** `Router.BeginChatGPTSignIn` (server route `POST /api/llm/chatgpt/signin`)
listens on `127.0.0.1:0`, builds the authorize address and returns it; the
dashboard opens it with `window.open`, the command line prints it
(`applysmith llm signin chatgpt [--open]`). The browser comes back to
`http://127.0.0.1:<port>/auth/callback` (only the port varies, as the page
allows); the listener checks `state`, exchanges the code, validates the ID token
(signature against the issuer's JWKS from the OpenID configuration, issuer,
audience = the issued client id, expiry, nonce; RS256 and ES256 only), stores
the family and shuts down. One attempt waits at most ten minutes; a second start
replaces it. Progress is `GET /api/llm/chatgpt` (`pending`, `signedIn`,
`planUsage`, `account`, `message`). A sign-in with plan usage adds the `chatgpt`
provider when it is missing; the active preset and every task row stay as they
are, so a row, a group or `applysmith llm test chatgpt` can name it at once.

**What is kept where.** The vault holds `chatgpt-refresh` and `chatgpt-access`.
The vault caps a value at 2,048 bytes, so the ID token is not stored (the saved
email is sent as `login_hint` on a later sign-in instead of `id_token_hint`), and
an access token the keychain refuses stays in memory only (the server logs a
warning; other processes then cannot use it). Measured on the owner's account
on 2026-10-24: the keychain refused the access token, so it lives in the
server's memory, and the refresh token is stored. The database row `llm_settings`
key `chatgpt` holds what is not secret and must survive a sign-out: the host id
(`urn:uuid:<v4>`, generated once and never changed), the issued client id, the
account `sub` and email, the granted scopes and the reason a sign-in ended.

**Who refreshes.** Refresh tokens rotate on every use, so exactly one process
refreshes: the server's router is built with `WithChatGPTRefresh`. Its refresher
is serialized by one mutex, renews two minutes before expiry, saves the rotated
refresh token before the access token, and runs on a context that is not
cancelled with the caller (cancelling a refresh mid-flight loses the rotated
token). Any other router (every command-line command) reads the stored access
token while it is valid and otherwise fails the attempt as `unconfigured` saying
that the running app renews it, so the walk moves on to a fallback;
`applysmith llm test chatgpt` goes through the running server and renews it. The
sign-in and sign-out commands require the running server for the same reason.
A terminal refresh code ends the sign-in (tokens deleted, registration kept,
the status message says to sign in again); a network failure, 5xx or 429 keeps
it. An access token the Responses API refuses with 401 is dropped, so the next
call refreshes or learns that the sign-in is over. `earliest_refresh_at` in the
token response is ignored: the docs list it without saying what it means.

**Sign out.** `applysmith llm signout chatgpt [--forget]` / `POST /api/llm/chatgpt/signout`
revokes the refresh token at the `revocation_endpoint` of the OpenID
configuration (three tries for a network failure or 5xx), deletes both tokens and
keeps the host id and the issued client id. `forget` also drops the client id and
the account, which is how another ChatGPT account is signed in (a sign-in as a
different `sub` is refused). When the revocation is not confirmed the tokens are
gone from this computer all the same and the user is told to disconnect the app
in ChatGPT settings.

**Calls.** `POST {base}/responses` (base `https://api.openai.com/v1`): `store:false`,
`stream:true`, system text in `instructions`, the prompt as one `input` item,
`reasoning.effort` from the row (same effort mapping as the `openai` preset),
and for JSON tasks `text.format` of type `json_schema` in the strict form that
`openai.go` already builds. The plan route rejects `max_output_tokens`,
`temperature`, `top_p`, `user` and the rest of the list in `preview-limitations`,
so none is built: **a row's output ceiling cannot be sent**. The client leaves
a stream whose own text passes twice the row's ceiling (by the ledger's length
estimate) as `runaway`, which advances the walk (unlike `truncated`: a
candidate that is sent the ceiling may answer within it) and rests nothing;
before, such a reply ran into the attempt timeout, which advanced too. The
model's own limit and the attempt timeout bound the rest. Measured
2026-11-08 over 14 days of the owner workspace: the largest successful reply of
every task stayed under 1.1 times its ceiling (an `apply-check` of 546 tokens
against 512), while one `deep-eval` streamed about 178k tokens against 24,000
until its 600 s timeout, half of that task's wasted tokens in the week. Reads: text deltas, a
terminal `response.completed` (the only success), usage (`input_tokens` includes
the cached count; `output_tokens` includes the reasoning count), and the time to
the first stream event.

**Errors, by exact code.**

| Code or status | Kind | Effect |
|---|---|---|
| `subscription_sharing_usage_limit_exceeded` (429, also as `response.failed`) | `quota-exhausted` | the whole provider rests (30 minutes, or the `Retry-After` header if one is sent; no reset time is inferred) and the walk advances |
| `..._usage_unavailable`, `..._user_unavailable` (503) | `overloaded` | rests and advances |
| `..._user_not_eligible` (403), `..._invalid_user` (401), `..._route_not_supported` (403), `chatpass_v2_*` (403), a bare `{"detail"}` 401 or 403 | `auth` | the walk stops (a configuration error); a 401 also rests the provider as a dead key |
| `..._unsupported_capability` (400) | `bad-request` | names `error.param`; never resent unchanged |
| `model_not_found`, 404 | `model-gone` | advances |
| other 429 | `rate-limited` | with `Retry-After` |
| a stream that ends without a terminal event | `network` | advances unless text was already streamed |
| a reply whose text passes twice the row's ceiling | `runaway` | the client stops reading; advances, rests nothing |
| `response.incomplete` for `max_output_tokens` | `truncated` | returned with the partial text |

**Billing and the cap.** The kind is billed `subscription` at cost 0 (as `cli`),
so a call never counts toward the daily cap or a task budget. The cap's cheaper
head is chosen among priced candidates only (`pairCost` is false for this kind,
even when a user price exists), so the plan is never a downgrade target and a
plan primary keeps its order over the cap.

**Models.** `GET {base}/models` with the same token (`ChatGPTModels`, route
`GET /api/llm/chatgpt/models`): entries with `visibility: "list"`, the server's
order, reused for ten minutes and dropped on sign-in and sign-out. Settings uses
it for the model field beside Test. The preset's defaults are the `openai`
preset's model names (`gpt-6-luna`, `gpt-6.1-sol`); an account that lacks one gets
`model-gone` and the walk advances, so the first live check (task 1.5) should
confirm them against the account's list.

**Agent rows.** `agent-kernel` and `agent-light` may name a `chatgpt` candidate in
any position. pi speaks the Responses API itself (`api: "openai-responses"`), so the
gateway passes its request to `POST {base}/responses` and relays the stream
unchanged, with no translation ([Agent gateway](#agent-gateway)). pi omits the fields the
plan rejects only when it believes it talks to `api.openai.com`, so the gateway
removes them (`chatgptRejected`, the list above) and forces `store:false` and
`stream:true`; usage is read from the terminal `response.completed` (input including
cached, cached, output), billed `subscription` at cost 0, and the errors map as in
the table above, so a limit or an authentication failure rests the provider.
When the row also has candidates on another wire, pi moves the turn to them
([Rows that span wires](#rows-that-span-wires)). The `chatgpt` preset routes
`agent-kernel` like any other task.

## Measured selection (`applysmith llm bench`)

`applysmith llm bench option-pick --candidates provider/model,provider/model
[--cases file.json] [--apply] [--json]` replays cases through each candidate
as pinned calls of the task (`Request.Model`), one at a time, so the ledger
holds them under `option-pick` with run id `bench` and the task's own thinking,
ceiling and timeout apply. A case is a question, its options, optional facts
and the expected option or `NONE` (no fact supports any option; abstaining is
then correct). A reply is scored:

| Reply | Expected an option | Expected `NONE` |
|---|---|---|
| that option | correct | wrong |
| another option | wrong | wrong |
| abstain, empty, or a failed call | no answer | correct (a failed call is no answer) |

Candidates are ranked by fewest wrong, then most correct, then lowest median
latency, then lowest cost (unpriced last). The result is stored as the
`bench:<task>` key of `llm_settings` (latest only, no history), shown beside
the task's route in Settings, and `--apply` routes the winner as the task's
primary, keeping the rest of the row (thinking, limits, first-token deadline,
fallbacks minus the winner); a winner with no correct case is refused. Without
`--cases` the built-in set of 12 invented cases is used (an invented applicant,
three questions no fact answers). A bench is 12 calls per candidate, at most
100 cases and 8 candidates. Only `option-pick` is supported: another task needs
its own prompt and scoring, and is added when one is measured.

## Anthropic structured output

The Messages API accepts only a subset of JSON Schema in
`output_config.format` and answers 400 to the rest. `anthropicSchema` sends a
reduced copy; the router still validates the reply against the full schema the
caller gave, so a removed bound is enforced after the response. Source:
https://platform.claude.com/docs/en/build-with-claude/structured-outputs (read
2026-10-11; the page applies to the current Claude models, `output_format` is
deprecated in favour of `output_config.format`, no beta header).

| Rule from the page | What the transform does |
|---|---|
| Every object needs `additionalProperties: false`; any other value is rejected | Set on every schema with `properties` or `type: object` |
| `minimum`, `maximum`, `multipleOf`, `minLength`, `maxLength` unsupported | Removed, and named in the node's `description` |
| Array constraints beyond `minItems` 0 or 1 unsupported | `maxItems` and larger `minItems` removed |
| `pattern` allowed only in a restricted regex dialect | Removed (RE2 patterns the local validator checks may use features the API rejects) |
| `oneOf` not listed; `anyOf` is | Rewritten as `anyOf` |
| Only some string `format` values | Others removed |
| At most 24 optional properties and 16 union types across the request's strict schemas | Not reducible; a test asserts `DeepSchema` and `ScreenSchema` stay inside (21 and 3 optional, 2 unions) |

`DeepSchema` allows unknown top-level keys locally so a stray model-supplied
score can be logged and dropped; the sent copy is closed, so an Anthropic model
cannot emit one at all. The evaluation side is unchanged.

## Refreshing the preset table

Model ids and prices in `presets.go` and `prices.go` are the shipped defaults,
not live facts. To refresh a preset:

1. Edit the model ids in `presets.go`; keep them off `retiredModels`.
2. Run `applysmith llm prices update` for the OpenRouter ids and add the other
   rows to `embeddedPrices` from the provider's price page.
3. Run `applysmith llm test <provider> --model <id>` once per class model. A model that does not
   answer within its task timeout is not a preset default.
4. `go test ./internal/llm/` checks the table against the retired list and the
   class coverage.

## Preset models and prices

Checked 2026-10-11. Prices are USD per million tokens: input, cached input,
output. "Verified" means read from the named source on the check date;
"unverified" means taken from a neighbouring source or left as it was and not
confirmed against the provider. Re-run the checks in "Refreshing the preset
table" when a provider announces a retirement.

| Preset | Class | Model id | Price (in / cached / out) | Source | Status |
|---|---|---|---|---|---|
| openai | fast | `gpt-6-luna` | 0.10 / 0.01 / 0.50 | OpenAI pricing page (developers.openai.com/api/docs/pricing); agrees with the OpenRouter catalog | verified |
| openai | standard, deep | `gpt-6.1-sol` | 2 / 0.10 / 10 | OpenAI pricing page; model page confirms reasoning effort `low` to `max`, default `medium`, `none` and `minimal` unsupported, 1,050,000 context | verified |
| openai | embedding | `text-embedding-3-small` | 0.02 | OpenAI pricing page | verified |
| openrouter | fast | `openai/gpt-6-luna` | 0.10 / 0.01 / 0.50 | OpenRouter `GET /api/v1/models` | verified |
| openrouter | standard, deep | `openai/gpt-6.1-sol` | 2 / 0.10 / 10 | OpenRouter `GET /api/v1/models` | verified |
| openrouter | embedding | `openai/text-embedding-3-small` | 0.02 | OpenRouter `GET /api/v1/embeddings/models` | verified |
| anthropic | fast | `claude-haiku-5-5` | 0.10 / 0.01 / 0.50 (prompts up to 100K tokens; 0.50 / 2.50 above) | `claude-api` skill, model table and Haiku 5.5 notes (cache read 0.1x input); OpenRouter agrees on 0.10 / 0.50 | verified; the long-prompt rate is not modelled |
| anthropic | standard | `claude-sonnet-5-5` | 2 / 0.10 / 10 | `claude-api` skill (cache reads 0.10: 0.05x input, not Haiku's 0.1x); OpenRouter agrees on 2 / 10 | verified 2026-11-10 |
| anthropic | deep | `claude-opus-5-5` | 4 / 0.20 / 20 | `claude-api` skill (cache reads 0.20: 0.05x input, not Haiku's 0.1x); OpenRouter agrees | verified 2026-11-10 |
| deepseek | fast, standard | `deepseek-flash` | 0.30 / 0.006 / 1.20 (peak; off-peak is half) | DeepSeek pricing page (api-docs.deepseek.com/quick_start/pricing); the page names `deepseek-flash` (V4.1-Flash) as the model to use and says the legacy `deepseek-v4-flash` alias is served by it | verified |
| deepseek | deep | `deepseek-v4-pro` | 1.32 / 0.044 / 3.96 (peak; off-peak is half) | DeepSeek pricing page (V4-Pro-0813) | verified id and price; quality for deep tasks not measured |
| gemini | fast | `gemini-3.5-flash-lite` | 0.30 / 0.03 / 2.50 | Google pricing page (ai.google.dev/gemini-api/docs/pricing), not labelled preview | verified |
| gemini | standard, deep | `gemini-3.8-flash` | 0.75 / 0.075 / 3.75 through 2026-12-31, then 1.50 / 0.15 / 7.50 | Google pricing page, not labelled preview | verified; update the row on 2027-01-01 |
| gemini | embedding | `gemini-embedding-001` | 0.15 | OpenRouter embeddings catalog; named as the text-only embedding model on Google's OpenAI-compatibility page | price from OpenRouter, not Google's page, which lists only `gemini-embedding-2` ($0.20) |
| nvidia | fast | `minimaxai/minimax-m3.1-flash` | unpriced | NIM `GET /v1/models`; answered the provider test on the owner's key on 2026-10-23 in 0.4 s within 16 tokens | answers; quality not measured |
| nvidia | standard | `nvidia/nemotron-3.5-lightning-30b-a3b` | unpriced | same test, 1.2 s, the reply reasons first (cut at the test's 16 tokens) | answers; quality not measured |
| nvidia | deep | `nvidia/nemotron-3-ultra-550b-a55b` | unpriced | same test, 1.6 s, reasons first | answers; quality not measured |

The NIM catalog lists more models than a key may call. On 2026-10-23 the
owner's key got `model-not-found` from `nvidia/nemotron-3-super-120b-a12b`
(the earlier fast and standard choice), `nvidia/nemotron-nano-3-30b-a3b` and
`moonshotai/kimi-k2.6`, and no answer within 30 s from
`deepseek-ai/deepseek-v4.1-flash`, `z-ai/glm-5.3` and `z-ai/glm-5.3-flash`;
`nvidia/nemotron-3.5-bolt-e4b` answered but is a 4B model. Check a candidate
with `applysmith llm test nvidia --model <id>` before naming it here.
| codex | all | `default` (the tool picks) | subscription, cost 0 | design | n/a |
| claude | fast, standard, deep | `haiku`, `sonnet`, `opus` (aliases of the installed tool) | subscription, cost 0 | design | unverified: aliases resolve in the user's installed Claude Code, not checked here |

Request quirks recorded in `presets.go`, with their sources:

- OpenAI `gpt-6.1-sol`: efforts `low` to `max`, reasoning cannot be turned off
  (model page). `gpt-6-luna` is assumed to match (the local Codex model cache
  lists the same levels) and its mandatory-reasoning flag is unverified. The
  `max_completion_tokens` field name for these models is unverified; the model
  page does not name the parameter.
- OpenRouter forwards `reasoning.effort`; the preset sends `low` and `high`
  only, which every model behind it accepts (unverified for the gpt-6 family).
  Thinking off on a model that is not reasoning-mandatory is sent as
  `effort: none`, which OpenRouter documents as "disables reasoning entirely";
  omitting the field left reasoning models at their default.
- NVIDIA NIM: its Nemotron reasoning models think unless the chat template's
  `enable_thinking` switch is off (model card and vLLM recipe for
  `nemotron-3-nano-30b-a3b`: `chat_template_kwargs: {"enable_thinking":
  false}`). The preset was `none` (no field), so the apply rows that fall back
  to `nemotron-3.5-lightning-30b-a3b` with thinking `off` reasoned anyway: on
  2026-10-29, with `api.deepseek.com` unreachable from this machine (connect
  timeouts, `llm test deepseek` timing out at 30 s), all 32 NVIDIA fallback
  attempts that day returned no text at `finish: length`, every one of the
  512/2,048 output tokens spent reasoning, and each composed answer (a
  Greenhouse personal-story box, for one) went back to the user. The preset
  now sends `chat_template_kwargs.enable_thinking` (`false` for off or unset,
  `true` for any level); the hosted endpoint's acceptance of the field is
  checked by the next apply call's ledger row (reasoning tokens 0, text
  returned).
- DeepSeek (`api-docs.deepseek.com/guides/thinking_mode`, read 2026-10-17):
  `deepseek-flash` and `deepseek-v4-pro` think at effort `high` when the field
  is omitted. `reasoning_effort` takes `none` (off), `low`, `high`, `max`;
  `minimal` maps to `low`, `medium` and `xhigh` to `high`. Temperature has no
  effect in thinking mode. The preset was `none` (no field), so `off` and `low`
  tasks reasoned at `high`: the 16-token `llm test` call spent 10 to 14
  reasoning tokens and was truncated once in six, and the 256/512-token apply
  checks ran out of room (QA finding F26). With the field sent, the same test
  spends 0. Measured 2026-10-17 on `cover-letter` (thinking `low`) at a
  4,096 ceiling: empty completions fell from 4 of 9 calls to 2 of 8, mean
  reasoning 1,643 to 1,169 tokens; `low` still reasons, so the row's 6,144
  ceiling (0 empty in 17 calls) is what keeps the letter whole.
- DeepSeek `low` is honoured and is the provider's lowest thinking level
  (re-checked 2026-10-17 against the same guide, which lists no token budget):
  on one short prompt, sent 2x each, top-level `reasoning_effort: low` spent
  170 and 175 reasoning tokens, the nested `reasoning: {effort: low}` 158 and
  165, `low` with `thinking: {type: enabled}` 164 and 135, the omitted field
  (high) 415 and 109, while `reasoning_effort: none` and
  `thinking: {type: disabled}` spent 0. So the request carries a setting the
  provider applies; what remains is that `low` reasons long on the grounded
  prose tasks. Replaying a captured cover-letter prompt (3 calls each, 6,144
  ceiling): `low` 4,210 to 6,144 reasoning tokens in 22 to 27 s, one reply
  empty; `none` 0 tokens in 2.0 to 2.4 s. Running the whole `applysmith
  cover-letter` (demo candidacy 1, 3 runs each): `low` 26 to 68 s and USD
  0.006 to 0.024 per letter, one primary attempt empty at the ceiling; `off`
  10 to 11 s and about USD 0.0025, but the letter gate rejected 6 of 6 model
  drafts (`unsupported-figure`, `unsupported-name`) and all three runs fell
  back to the deterministic letter, against 1 rejected draft in 3 `low` runs.
  Thinking off is therefore not used for the grounded document tasks; the
  cost of `low` is its latency and an occasional ceiling-bound empty reply,
  which the fallback now has time to replace (below).

### Letter refusals in the owner workspace (2026-11-08)

The ledger of 2026-11-06 to 08 held 370 `cover-letter` calls for 210 letters:
123 letters took a second or third call, the letter gate refused 103 drafts
(100 `unsupported-figure`, 3 `unsupported-name`) and 14 letters fell back to
the template. Three letters were rerun with capture on (the CLI fell back to
`deepseek-flash`; the ChatGPT token is renewed only by the running app) and
every captured draft was put through the figure check: 5 refused figures in 4
of 8 drafts, each a restatement of the cited line with the same figure and
subject. "improving processing throughput by 22%" for "a 22% improvement in
processing throughput", "removing 70%" for "eliminating 70%", "held p95 query
latency under 180 ms" for "holding ...", and "backed by a retrieval pipeline
over 400k documents" for "Designed a retrieval pipeline over 400k documents".
The four rules are fixed in the gate ([document-grounding.md](document-grounding.md),
`TestQuantity_ALetterMayRestateTheResultInItsOwnWords`, with the changed verb,
subject and figure still refused); with them none of the 8 drafts has a
refused figure.

### Image input of the agent model

A desktop look sends the model the window's screenshot only when
`imageModels` in `internal/llm/agent.go` lists the agent route's model, and
models.json declares image input for those models alone (AC-3); the gateway
gives a request with an image only to candidates on that list. Every other
model gets the control list as text. The list holds the Claude, GPT-6 and
Gemini preset models, which pi 1.0.4's own catalog gives text and image input.
DeepSeek is left out (checked 2026-10-21). pi's catalog gives `deepseek-flash`
image input, but DeepSeek's documentation found that day names image input
only for a V4-Flash vision variant, and says the text-only models answer an
image with HTTP 400. A wrong entry fails every request that carries a
screenshot. A missing entry only drops the screenshot. Only the newest
screenshot stays in the model's context; the pi extension replaces older ones
with a note.

### Per-task token audit (2026-10-17)

Source: the ledger rows of every demo workspace copy of the QA rounds (683
attempts, de-duplicated by call and attempt; fake-provider rows ignored).

| Task (DeepSeek unless named) | Calls | Mean in / cached | Mean out / reasoning | Finding |
|---|---|---|---|---|
| deep-eval (`deepseek-v4-pro`, medium = high) | 44 | 4,387 / 63% | 11,389 / 8,590 | 123 s mean, the largest cost (USD 0.047 a call); reasoning is the evaluation itself, unchanged |
| cover-letter (thinking low) | 46 | 1,345 / 58% | 2,737 / 2,524 | 14 of 46 ended at the ceiling (4,096 then 6,144); see the letter deadline |
| document-review (low) | 20 | 1,755 / 42% | 2,655 / 2,146 | none truncated |
| apply-check, -fix, -page-decision, -complete (off) | 276 | 330 to 746 / 35 to 43% | 95 to 444 / 93 to 417 before the `none` fix, 0 after (rows from 14:00 UTC) | the `none` effort fixed them; no change needed |
| agent-kernel (`deepseek-v4-pro`) | 14 | 9,854 / 79% | 136 / 72 | the packed context is bounded by the token budget and cached by DeepSeek's prefix cache |
| feed-classify (off) | 23 | 25 / 0% | 8 / 7 | the 7 ceiling hits were all before the `none` fix |

Ask Agent thinking level (measured 2026-10-17, demo copy, pi 1.0.4,
`deepseek-v4-pro`; pi sends the level as `reasoning_effort`, and DeepSeek
reads `medium` as `high`). The same three messages twice at each level:

| Message | `medium` (the preset) | `low` |
|---|---|---|
| "Hi! What can you help me with?" (no tool) | 4.1, 4.2 s; 37, 28 reasoning tokens | 4.2, 4.1 s; 33, 29 |
| "How many applications are in my tracker ...?" | routed (`tracker-counts`): no model call, 0 s | the same |
| "Which evaluated posting is the best fit, and its two biggest gaps?" | 16.5, 21.6 s; 4 and 5 calls; 851, 1,240 reasoning tokens; USD 0.010, 0.013 | 14.5, 19.5 s; 4 calls each; 509, 1,069; USD 0.009, 0.011 |

Both levels called the same tools and gave the same answer, and the gap is
within the run-to-run spread, so the preset stays `medium` and a trivial turn
gets no separate level (an intent-routed turn calls no model at all). A turn
is slow because of its tool calls, not its effort: the one heavy turn in these
copies (7 model calls, 3,030 reasoning tokens, 55 s) was a draft request in
which the model rebuilt the posting from reports in 9 tool calls; with the
stored posting given to the composer it takes one call and 11 to 18 s. The
71 s / 6,600-token trivial turn reported in the first QA round was not
reproduced.

Prompt prefixes: DeepSeek and OpenAI cache automatically and the ledger's
`cached_input_tokens` shows it (35 to 79% of input). Anthropic caches only
with `cache_control`, which `anthropic.go` never sends; not added, because the
shared system prompts are under Anthropic's minimum cacheable length (the
documents system prompt is about 50 tokens) and a cache write costs 1.25x
for a prefix that sporadic calls would not read again; no workspace measured
here routes a task to the Anthropic API.

Double spend fixed: the cover letter's call deadline (`documents.LetterTimeout`)
was 45 s for the primary attempt and its fallbacks together. A DeepSeek letter
that reasons to its ceiling ends empty after 20 to 31 s; the OpenRouter
fallback needs 14 to 16 s (13 calls), so it was started with about 15 s left,
billed (403 output tokens, USD 0.006 in the measured run) and cancelled, and
the deterministic letter was used. The deadline is now 90 s
(`TestModelLetterDeadlineHoldsACeilingBoundAttemptAndAFallback`).
### Owner-workspace token audit (2026-11-08)

Source: the owner workspace's ledger and debug capture, fill-only batches on
the ChatGPT plan (billed `subscription`, so the cost is plan quota, not USD).

| Measure | Before | After | Change |
|---|---|---|---|
| apply-complete input per run | 9,283 (2ed20f10) | 3,728 (2fe02e55) | the grounded rung shows only the bank entries relevant to the page's questions (16f89b7c), not up to 100 |
| apply-check, apply-fix input per call | 700 to 800, 550 to 650 | unchanged | already small; a cut value now ends with an ellipsis inside the same 120-character bound (6a0440e0) |
| whole run, apply and documents | | about 15,800 in and 3,000 out per open posting (queue313) | deep-eval is 28% of the input and 67% of the output |

deep-eval (about 9,000 in and 4,000 to 5,000 out) runs once per posting that
has a letter slot and no full report; its candidate profile block is about
22,000 characters (the whole CV and the career context). The owner
reviewed two cuts, leaving the compensation and negotiation sections out of
the deep-eval profile and letting a letter be written without a full report,
and declined both (the token use is acceptable, nothing to cut): the report's inputs and the
letter's report requirement stay as they are.

### Document drafting without reasoning (2026-10-17, second round)

The `off` letters above were refused 12 of 12 times. Each refusal was read and
classed (demo persona, DeepSeek `deepseek-flash`, postings Example Corp, Ramp
and Flexi, 2 runs each, 2 attempts per run). False positives of the gate, now
fixed and tested with the refused sentence ([document-grounding.md](document-grounding.md)):
"production" (4 drafts; the capital that opened a requirement line made it a
product), the role named as "Junior Full-Stack Software Engineer" (4; the
tracker role was "Flexi - ... Application"), "used by / serving forty product
teams" (2), a body ending in a colon before its bullets (1). True rejections:
"JavaScript", "React" on a CV without them (4), "five years" / "several years"
taken from the posting's "5+" (3), "based in Atlanta", "eligible to work in
the United States" (2), "Two projects show" (1); "full-stack experience" on a
backend CV (4) is now refused by rule rather than by accident. With the gate
fixes alone 1 of the 12 drafts would pass: the drafts were mostly ungrounded,
so the rest is the prompt. It now names the posting's names no source holds,
the role's words that are not the applicant's, marks requirements without
evidence, shows the summary and skills lines (the CV's "nine years"), and says
`companyFacts` must be empty when no fact is listed (a draft invented "C1").
The demo's 1Password brief was retrieved as Ramp's and Flexi's company facts;
briefs are now kept only when they are about the employer.

Letters, 6 runs per row (`applysmith cover-letter`, one run = up to 2 letter
calls, the review and its revision):

| Setting | Gate and prompt | Template fallback | First draft passed | Run wall time, mean (range) | Letter call | Run cost, mean |
|---|---|---|---|---|---|---|
| `off` | before | 6 of 6 (12 of 12 drafts refused) | 0 | 16.7 s (11 to 19) | 2.2 s | USD 0.0038 |
| `low` | before (previous measurement, 3 runs) | 0 of 3 | 2 of 3 | 26 to 68 s | 22 to 27 s | USD 0.006 to 0.024 |
| `off` | after | 0 of 6 | 4 of 6 | 17.3 s (11 to 34) | 2.1 s | USD 0.0045 |
| `low` | after | 0 of 6 | 4 of 6 | 41.7 s (26 to 65) | 10.5 s, 1,771 reasoning tokens | USD 0.0112 |
| `off` | after, briefs of the employer only | 0 of 6 | 4 of 6 | 18.0 s (13 to 30) | 2.3 s | USD 0.0044 |

Résumés, 6 runs per row (`applysmith resume generate`, `deepseek-v4-pro`):

| Setting | Template fallback | Drafts with a bullet replaced by its source line | Run wall time, mean (range) | Résumé call | Run cost, mean |
|---|---|---|---|---|---|
| `low` | 0 of 6 | 3 of 6 (all "**forty** product teams": bold split the figure from its noun, fixed; replayed, 6 of 6 pass) | 74.5 s (49 to 104) | 37.2 s, 3,265 reasoning tokens | USD 0.0328 |
| `off` | 0 of 6 | 0 of 6 | 18.5 s (13 to 25) | 6.2 s | USD 0.0085 |

Both settings copied the evidence lines nearly verbatim; the reasoning bought
no visible tailoring. `cover-letter` and `resume-write` therefore default to
thinking `off` (a quarter to a half of the time and cost, the same pass
rate); a model that cannot turn reasoning off runs at its lowest effort, and
the command-line presets ignore the level. `document-review` keeps `low`: its
ceiling is 6,144 now, because it ended empty at 4,096 on 2 of 30 calls (22 s
each, then the fallback). Paired runs with PDFs (3 postings, then 4 reruns)
published both documents every time; one letter had no PDF because its body
was empty (fixed, [document-verification.md](document-verification.md)), and
every other PDF was `ready` after one layout-repair round.

- Temperature: the evaluation tasks named in `deterministicTasks`
  (`openai.go`) send `temperature: 0` when reasoning is off on a preset with
  `Temperature` (DeepSeek, OpenRouter). Reasoning calls never carry it.
- Gemini: reasoning cannot be turned off for Gemini 3 models, and
  `reasoning_effort` takes `minimal`, `low`, `medium`, `high` (Google's OpenAI
  compatibility page). The preset sends `low` as the lowest because `minimal`
  is listed only for 3 Flash and 3.1 Flash-Lite.
- Anthropic: thinking is adaptive with `output_config.effort` on 4.6 and later
  models (`claude-api` skill); a model id before 4.6 (Claude 3, 4 to 4.5) is
  sent a `budget_tokens` budget instead, which is all those models take. The request builder never sends a disabled thinking
  value or a forced tool choice, which the 5.5 models reject.

Candidate ids that were checked and not chosen: OpenAI `gpt-6-astra` ($10 / $50,
too costly for the $2 daily cap), `gpt-5.6-*` and `gpt-5.5` (superseded by the
gpt-6 line on the same page); Gemini `gemini-3.1-pro-preview` (preview) and
`gemini-2.5-*` (still listed, older).

Retired or unlisted ids (`retiredModels`), with the evidence:

| Id | Evidence |
|---|---|
| `deepseek-chat`, `deepseek-reasoner` | Absent from DeepSeek's pricing page, which names only `deepseek-flash` and `deepseek-v4-pro`. Not independently confirmed as shut down: marked retired because no preset may rely on an unlisted name. |
| `deepseek-v4-flash` | DeepSeek page: the corresponding model "has been retired"; requests are served by `deepseek-flash`. |
| `deepseek-v4-flash-0731` | End of life on NVIDIA NIM on 2026-09-21 (HTTP 410), recorded in the previous product's routing document at commit `c0cb9598b`. |
| `gpt-oss-120b` | Retired on NVIDIA NIM on 2026-09-03 (HTTP 410), same document. |
| `gpt-oss-20b` | Owner decision 2026-10-23 ("do not use openrouter/openai/gpt-oss-20b"), applied on every provider: it had no price, so its calls counted 0 toward the daily cap. The NIM fast preset uses `minimaxai/minimax-m3.1-flash` instead (table above); the owner's live `quick-screen` and `apply-fix` fallbacks were moved to `deepseek/deepseek-flash` the same day. |
| `llama-3.3-70b-instruct`, `deepseek-r1` | Not in the NIM catalog on 2026-10-11 (`GET https://integrate.api.nvidia.com/v1/models`, 80 ids). |
| `claude-3-5-haiku-20241022`, `claude-3-7-sonnet-20250219`, `claude-2.0` | `claude-api` skill retired-models table. |
| the older OpenAI, Claude 2/3 and Gemini 1.x ids | Carried over from the first table; not re-checked. |

Other sources consulted: the local Codex model cache lists `gpt-5.5`,
`gpt-5.6-{sol,terra,luna}`, `gpt-6-{astra,sol,luna}`, `gpt-6.1-sol` and
`deepseek/deepseek-flash` (ids and descriptions only), and the previous
product's routing document confirms `deepseek-flash` as DeepSeek's real id.

## Served settings

The Models section of Settings edits a task's row in place: provider and model pickers (the configured providers, and `knownModels`, the preset, priced and already-routed models of each; any valid model id may still be typed), thinking, output ceiling, timeout, first-token deadline and up to four fallbacks, sent as one `PATCH /api/llm/routing` row. The server's validation message is shown beside the form. `GET /api/llm/routing` also lists the current `rests`. The Usage call table and trace detail show each attempt's ledger reason (fallback after an error, the same model again after its rate-limit delay, late first token, an earlier model resting, cap downgrade, an agent conversation kept on its model). The Usage page has a Cost tab (all of the above) and a Live tab (`?tab=live`): it reads the newest 100 attempts once, then every 2 seconds asks `GET /api/llm/calls?after=<last ledger id>` (the timer stops while the page is hidden, while paused and when the tab is left), groups attempts by `callId` into one row per call (the chain of attempts, why the answering model was chosen, first-token time, latency, cached share, cost, a link to the trace) and keeps the newest 100 calls. Its strip covers the last 15 minutes of those. A reason the page has no wording for is shown as the ledger wrote it.

`applysmith llm cap <usd>` and `applysmith llm budget <task> <usd>` call `SetDailyCap` and `SetBudgets` after `ValidateBudget`, as the routing patch does. `GET/PATCH /api/llm/routing`, `POST /api/llm/preset`, `PUT/DELETE /api/llm/keys/{provider}` and `GET /api/llm/cost` wrap `Show`, `PatchRoutes`/`SetDailyCap`/`SetBudgets`, `ConfigurePreset`, `SetKey`/`ClearKey` and `CostReport` ([api.md](api.md)). `CostReport` rows also carry the rate-limited share (outcome `rate-limited`; per call for a task, per attempt for a provider) and the cached share of input tokens, left out when no cached token was recorded. An attempt the offline simulation had no script for (`fake-fixture-missing`) asked no model, so the report leaves it out instead of counting it as a failed call; the Usage call list still shows it. A key is accepted only for a configured provider or a preset of an API kind and is never part of a response, a log line or an error. `POST /api/llm/providers/{provider}/test` starts `Router.Test` in the background (a command-line provider may take 90 seconds, longer than a request) and answers `202`. `Router.Test` sends the preset's fast model unless a model is named: the body's `model`, `applysmith llm test [provider] --model <id>`, or the model field beside Test in Settings, so a model can be checked on the account before a task is routed to it. A named model must pass `ValidModelID` and must not be retired. While a server holds the workspace, `applysmith llm test` asks that server to run the test and waits for the result; the routing view reports `testing` while it runs and `lastTest` afterwards, which is how Settings shows each provider as untested, working or failing. A test that cannot start for a named provider (not configured, a malformed or retired model, no model known) is saved as failed with class `setup` and the reason as its fix, so Settings and the command show it like any other failure. Only the newest test is stored, so a provider other than the one tested last reads as untested. The doctor checks and the `llm` health component are not changed by this.

## Background work

The server's scheduler runs `Router.Tick` (the cap check) on every tick, at most a minute apart, and `Router.Prune` (ledger rows older than 180 days, captured texts older than 24 hours) in its daily retention sweep at 04:00 local time; both are registered in `internal/api/domain.go`. The cap alert goes through the notification policy (`capAlertSink` in `internal/api/notify.go`). `applysmith llm debug-capture on [--minutes n] | off | status` switches capture in the shared database; the CLI never installs an alert sink or prunes.

## Provider accounts and plan quota

[Provider API catalog](llm-provider-catalog.md) describes MiniMax, GLM, Grok and
OpenCode model endpoints, model defaults, transport behavior and regional USD
price requirements. [Provider account investigation](provider-accounts.md)
records Orca REA evidence, the configuration/usage boundaries, and verification.
