# Agent runtime contract: pi over RPC

The Ask Agent's model loop runs in `pi`, supervised by Go (design D8, owner
decisions Q9 and "Ask Agent direction"). This file is the wire contract between
the two. Requirements live in `ask-agent` ("Agent runtime on pi over RPC",
"Repetition guard", "Stand-in results after a restart", "Steer, cancel and
fork") and `llm-routing` ("Agent turns run on pi"); this file says how they are
met. `internal/agent/pirpc` implements the pure half of it (launch flags,
version check, framing, decoding, event mapping, usage extraction, repetition
detection) and replays recorded transcripts in `internal/agent/pirpc/testdata`.
Task 5.2 builds the supervisor on top.

Each statement is marked:

- **verified**: observed by running the pinned release against a scripted model
  server (method below);
- **documented**: stated in the pinned release's own docs, not exercised;
- **unverified**: assumed, with the check that would settle it.

## 1. What was checked, and how

| Item | Value |
|---|---|
| Project | `earendil-works/pi` (npm `@earendil-works/pi-coding-agent`; the old `@mariozechner/` name is deprecated) |
| Pinned release | **1.0.4**, the version the old product's pi libraries were pinned to at the frozen commit |
| Newest release seen | 1.1.0. The CLI installed on the audit machine is 0.83.0. |
| Why a pin matters | 0.83.0 and 1.0.4 differ on the wire: 1.0.4 `message_update` records are delta-only and carry cumulative `usage`; `prompt`, `steer` and `follow_up` responses carry `data.disposition`; `clear_queue` exists. A client written for one misreads the other. |
| Artifact run | `pi-darwin-arm64.tar.gz` from the 1.0.4 GitHub release (31 MB, a Bun-compiled single executable; no Node needed), checksum compared with the release's `SHA256SUMS` |
| Method | A scripted OpenAI-compatible server (synthetic content) behind a `models.json` provider, an extension that fetches the catalog from a local HTTP stand-in and forwards calls to it, and a driver speaking RPC over stdio. No login, no real key, no real provider. Linux, Windows and Intel macOS binaries were not run. |
| Docs read | the 1.0.4 tarball's `docs/` (`rpc.md`, `rpc-commands.md`, `json.md`, `extensions.md`, `cli.md`, `providers.md`, `custom-provider.md`, `models.md`, `configuration.md`, `environment-variables.md`) |

Release assets for 1.0.4 (checksums are in the release's `SHA256SUMS`):
`pi-darwin-arm64.tar.gz` 31.0 MB, `pi-darwin-x64.tar.gz` 33.5 MB,
`pi-linux-x64.tar.gz` 42.6 MB, `pi-linux-arm64.tar.gz` 42.7 MB,
`pi-windows-x64.zip` 45.1 MB, `pi-windows-arm64.zip` 43.7 MB. The npm package
needs Node 22.19 or newer; the compiled binary does not.

## 2. Pinned version and the start check

- `pirpc.PinnedVersion` is `"1.0.4"`. It is the only record of the pin in the
  repository; this file and `applysmith doctor` quote it.
- Before the first spawn, and again after any failed spawn, the supervisor runs
  `<pi> --version` with a 10 second limit. The output is a bare version
  string (**verified**: `1.0.4`). `pirpc.CheckVersion` accepts exactly the
  pin; anything else, including a newer release, makes the model loop
  unavailable. The router keeps answering and `doctor` prints both versions.
- The binary comes from `APPLYSMITH_PI_PATH` or `pi` on `PATH`. The product does not
  download or update it, as with `cloudflared` (remote-access). First run on a
  new machine is slow (about 8 seconds, macOS verifies the unsigned binary);
  later starts are not. Whether to add a verified one-step install of the
  pinned release asset is an owner decision (section 13, O-AG-3).
- Process start to first command response was 430-670 ms, including the
  extension's catalog fetch (**verified**, warm).

## 3. Launch

```
pi --mode rpc --no-session --no-builtin-tools --no-extensions --no-mcp
   --no-skills --no-prompt-templates --no-themes --no-context-files
   --no-approve -e <embedded extension> --provider <p> --model <m>
env PI_CODING_AGENT_DIR=<product agent dir> PI_SKIP_VERSION_CHECK=1
    PI_TELEMETRY=0 PI_OFFLINE=1
    APPLYSMITH_AGENT_URL=<loopback base> APPLYSMITH_AGENT_TOKEN=<process token>
cwd <empty product-owned directory>
```

`pirpc.LaunchArgs` and `pirpc.ChildEnv` build the first two lines and a test
pins them. All flags were accepted by 1.0.4 (**verified**).

- `--no-builtin-tools` leaves pi's `read`, `bash`, `edit`, `write`, `grep`,
  `find`, `ls` and `powershell` registered but inactive. The tool list pi
  reports is the extension's `pi.getActiveTools()`, sent to Go in a `hello`
  call at `session_start`; it held exactly the catalog tools, and the model
  request carried only those (**verified**). `pi.getAllTools()` still lists the
  built-ins, so the check reads the active list.
- `--no-extensions` also switches off pi's built-in extensions (`mcp`,
  `codemode`, `tool_search`); the explicit `-e` still loads (**verified**).
  `--no-mcp` is added so no MCP server can connect. Skills, prompt templates,
  themes and `AGENTS.md`/`CLAUDE.md` discovery are off so nothing outside the
  product shapes the prompt. `--no-approve` ignores project-local pi files;
  the working directory is empty and product-owned anyway.
- `--no-session` keeps pi's own session in memory (**verified**). pi's session
  is never authoritative (section 7).
- The agent directory holds pi's `auth.json` and a generated `models.json`
  (section 11). It is separate from any pi the user runs for themselves.
- `PI_OFFLINE=1` stops pi's automatic network use (catalog refresh). Provider
  calls work with it set, to a loopback server and to a remote provider
  (**verified**: the live acceptance of 2026-10-16, section 13).
- stdout carries only protocol records; diagnostics go to stderr, which the
  supervisor captures to its log with the credential shapes of the disclosure
  gate redacted (**documented**).

## 4. Wire protocol (the parts used)

Framing is JSON lines with LF as the only record break (**documented**).
`pirpc.ReadRecords` splits on LF, strips a trailing CR, accepts U+2028 inside a
string and rejects a line over 16 MiB; `agent_end` repeats every message of the
run, so lines can be large.

Commands the supervisor sends (every command carries a unique `id`):

| Command | Use | Status |
|---|---|---|
| `get_state` | readiness probe after spawn | verified |
| `set_auto_compaction {enabled:false}` | compaction stays in Go (`compaction` task); pi's default is on | verified |
| `set_model {provider, modelId}` | apply the `agent-kernel` row when it differs from the current model; an unknown pair answers `success:false`, `Model not found: <provider>/<id>` | verified |
| `set_thinking_level {level}` | the row's thinking level; clamped to what the model supports | verified |
| `new_session` | start of every turn (section 7) | verified, `data.cancelled:false` |
| `prompt {message}` | the user's message; response `data.disposition` is `started` | verified |
| `steer {message}` | mid-turn steering | verified, `disposition:queued` |
| `clear_queue` | drop queued steering on cancel; returns the texts | verified |
| `abort` | cancel; answers after the session is idle | verified |
| `get_session_stats` | cross-check of tokens and cost in tests only | verified |

`prompt` while a run is active without `streamingBehavior` answers
`success:false` ("Agent is already processing...") (**verified**). The
supervisor never does that: it uses `steer`. On a gateway route the handshake
sends `set_auto_retry {enabled}` (**documented** in `rpc-commands.md`): `false` for
a row whose candidates share one wire, because the gateway walks them inside each
call and a retry by pi would walk them again; `true` for a row that spans wires,
where pi's retry is what moves a turn to the next wire (section 11, "Rows that
span wires"). A `pi:` login keeps pi's retry on.

Events used (names as in the table in section 8). Everything else decodes and
is counted, never fatal: `pirpc.Record.Known` is false for a type the contract
does not name (**verified** with a made-up type in the edge transcript).

## 5. Tools: registration, forwarding, built-ins off

The embedded extension is one self-contained `.mjs` file in the Go binary,
written to the agent directory before each spawn so its content always matches
the binary (**verified**: an async default export is awaited before pi starts
the run, and a plain JSON Schema object is accepted as `parameters`, so the
extension needs no imports).

Registration:

1. The factory calls `GET {APPLYSMITH_AGENT_URL}/internal/agent/catalog` with the
   process token. The answer is the catalog from the Go registry: name, label,
   description and a JSON Schema for each tool, with the optional string
   argument `step` added to every schema (no `maxLength`: Go cuts it, and pi
   would reject an over-long value as a tool error).
2. For each entry it calls `pi.registerTool` with an `execute` that forwards.
3. On `session_start` it sends `pi.getActiveTools()` to
   `POST /internal/agent/hello`. A set other than the catalog marks the process
   unusable (it is killed and the turn ends `runtime_unavailable`).

Forwarding (`POST /internal/agent/tool-call`, bearer token):

```
request  {toolCallId, name, args}              args exactly as pi validated them
response {isError, text, finalOnly?, activeTools?, skill?}   text is already redacted and cut to 12,000 characters
                                               activeTools/skill: set by a call that loaded a skill (ask-agent.md, Skills)
```

The extension returns `{content:[{type:"text", text}], details:{}}`, throws on
`isError` (pi then sets the result's `isError`), passes pi's abort signal to
`fetch`, and never returns `usage` (a tool's own model calls are ledgered by
Go under their own tasks, so returning them would count twice). **Verified**:
the call arrived with the bearer token and the model saw the returned text.

Go side of the route:

- The token is random per process, valid until the process exits, and accepted
  only on `/internal/agent/*`. It is not the user's API token. It maps to the
  process and so to its current turn.
- The registry, the guard chain, the idempotency claim, execution and audit run
  here. The tool limit (12) and the `step` handling (strip, cut to 60, replace
  by the fixed label when unsafe) are applied here. The route holds the
  connection while a held call waits for confirmation only if the tool's
  contract says it blocks; otherwise it answers at once with a result that says
  the call is held (`tool.held` is emitted by this route).
- A name outside the registry is denied with reason `unknown_tool` and an audit
  row. **Verified, and a correction to the obvious design:** a model call to a
  name pi has not registered (the probe used `shell`) never reaches the
  extension, not even its `tool_call` hook; pi answers the model itself with
  `Tool shell not found` and emits `tool_execution_start` and `_end` with
  `isError:true`. The audit row for an unknown tool therefore comes from the
  supervisor watching `tool_execution_start` for a name outside the catalog, and
  such a call counts toward the 12-call limit. The route's own denial covers a
  forwarded name that is not in the registry (a stale catalog after a reload).
- At the 12th call's answer the route returns `finalOnly:true`; the extension
  calls `pi.setActiveTools([])` and the next model request carries no tools
  (**verified**). The extension restores the full set at `before_agent_start`
  of the next turn. A 13th call, if one arrives, is refused with the result
  "give your final answer".
- Plain text that looks like a tool call is just assistant text. pi runs native
  tool calls only (**documented**), which is what the spec scenario needs.
- Calls within one assistant message run one at a time in the model's order.
  **Verified:** pi runs sibling calls concurrently (two calls in one message,
  the first blocked 600 ms, finished in reverse order), though their
  `execute` functions start in the model's order. The extension therefore
  chains `execute` on one promise queue, so the forwarded requests reach Go
  one at a time in order (re-run: the order became call one, then call two).
  A call that was aborted while waiting in the queue returns without
  forwarding.

## 6. Prompt packing and turn start

Go packs the prompt; pi's session is rebuilt from the Go record every turn and
is never read back. Per turn the supervisor sends `new_session`, then `prompt`.
Two extension hooks bring in the packed content (**verified**):

- `before_agent_start` returns `{systemPrompt}` fetched from
  `GET /internal/agent/turn-context`; pi's default coding-assistant prompt is
  fully replaced. (Without it the default prompt reaches the model.)
- `context` runs before every model call and receives pi's messages. The
  extension replaces everything before the turn's own user message with the
  Go-packed prefix (checkpoint summary, earlier turns as plain user and
  assistant text, then one message of the turn's records: memory, goals, task,
  cards; [ask-agent.md](ask-agent.md), "Packing and compaction") and keeps the
  in-turn messages, including this
  turn's assistant tool calls and tool results, as pi holds them. A prefix
  message needs only `role`, `content` and `timestamp` (**verified**).

Earlier turns' tool traffic is summarised as text in the prefix, matching what
Go's context packing and compaction already do. The user message handed to
`prompt` is the new message only.

**Which row the turn starts on.** Before pi is started, `chooseRoute`
(`internal/agent/tier.go`) picks the row. With `agent-light` unrouted the turn
runs on `agent-kernel`, as before. With it routed, the intent classifier (task
`agent-intent`: fast class, thinking off, 64 output tokens, 4 second attempt
timeout, 5 seconds in all, conversation as the ledger run id) reads the new
message and at most 300 characters of the previous answer, and must answer
exactly `{"tier":"light"}` or `{"tier":"heavy"}`; a failed call, a late one and
any other output is `heavy`, and a turn that finishes a stopped run or has no
message is `heavy` without a call. Steering never reclassifies: the tier is the
turn's for good. A light turn runs on the `agent-light` row, a heavy one on
`agent-kernel`, and that row alone supplies pi's provider and model (`--provider
applysmith-light` or `applysmith`, `--model`), the `set_thinking_level` level, the
`AgentCapBlocked` pre-turn check, the `task` the bridge hands the gateway with
each model call (`AgentCall.Task`) and the task of the ledger rows. The tier is
kept on the turn row (`chat_turns.tier`, `ChatTurn.tier`, `applysmith agent
sessions show`).

## 7. Steering, cancellation, fork, and pi dying

**Steering.** `steer` while a tool call runs queues the text; pi delivers it
after the step's tool calls finish and before the next model call, as a `user`
message, and reports `queue_update` (**verified**: the model's second request
ended with the steering text after the tool result). The supervisor maps the
`user` `message_start` whose text matches a text that left the queue to the
`steer` stream event (`pirpc.Turn`). At most 5 are queued by Go; a turn answered
by the router never reaches pi, so the API answers `conflict` itself.

**Cancellation, within 2 seconds.** On `POST .../cancel` the supervisor, in
this order and without waiting between the first two: emits the terminal
`error` event with reason `cancelled` (`Turn.StopWith`), sends `clear_queue` then
`abort`, and cancels the request contexts of in-flight forwarded calls. Read
and draft tools stop with their context; a run an external-action tool already
started continues on the runs board and is named in the response. `abort`
answered in 21 ms with the model stream hung and in 42 ms with a tool call
blocked 20 seconds, the extension's `fetch` aborted, and the tool result came
back `isError` "The operation was aborted." (**verified**). If `abort` has no
answer after 1.5 seconds the process is killed, which also ends the turn.
Afterwards pi emits a final assistant message with `stopReason:"error"` and
text "The operation was aborted."; after a hung stream the stop reason was
`aborted`. The mapper therefore never derives cancellation from the stop
reason: the system's own `Stop` decides, later records only yield usage rows.

**Fork.** A fork is a new Go conversation. It starts as a fresh pi context
because every turn begins with `new_session` and gets its packed prefix from
its own record. pi's `fork`, `clone`, `switch_session` and session files are not
used. Nothing about pending confirmations or idempotency claims is copied
because pi holds none.

**pi dies mid-turn.** stdout ends (and the exit status is signal or non-zero)
with no `agent_settled` (**verified**: SIGKILL while a tool call was in
flight). The supervisor ends the turn with `error` reason
`runtime_unavailable` (`Turn.StopWith`), replays no tool call, marks the process
slot dead, and respawns with backoff 1, 2, 4, 8, 16 then 30 seconds, reset
after 60 seconds of healthy running. The in-flight forwarded request is cancelled by the
dead connection; a call that had already claimed an idempotency key keeps the
claim, and the conversation gets the stand-in result (next section). After 10
consecutive failed starts the loop is unavailable until `doctor` or a settings
change retries.

**Stall.** pi has no documented idle timeout for a live process that stops
emitting. The supervisor ends a turn with `runtime_unavailable` (detail
"stalled") and kills the process when no record has arrived for 120 seconds and
no forwarded call is in flight; a call still in flight re-arms the 120 seconds.
The ask-agent spec states the rule. **Unverified**: pi's own provider timeouts
were not exercised; check in the live acceptance test.

## 8. Event mapping

`pirpc.Turn` implements this table. Stream events are those of the loop
requirement (`turn.start`, `token`, `tool.call`, `tool.result`, `tool.held`,
`steer`, `compaction`, `final`, `error`).

| pi record | Stream effect | Notes |
|---|---|---|
| `response` (to a command) | none | matched by `id`; `success:false` fails the command |
| `agent_start`, `turn_start`, `turn_end`, `agent_end` | none | `turn.start` is emitted by Go when it accepts the user message; `agent_end` can be followed by a retry, so it never ends a turn |
| `message_update` `text_delta` | `token` | `thinking_*`, `toolcall_*` and the `*_start`/`*_end` markers are not streamed |
| `message_update.usage` | none | cumulative preview, never recorded |
| `message_start`/`message_end` role `user` | `steer` when the text left the steering queue | the turn's own prompt matches nothing |
| `message_end` role `assistant` | usage row; one repetition-guard step when `stopReason` is `stop` or `toolUse` | authoritative per-response usage; an `error` message inside a retry does not end the turn |
| `message_end` role `toolResult` | usage row only if the result carries `usage` | the extension never sets it |
| `tool_execution_start` | `tool.call` with the sanitised `step` phrase | also the unknown-tool and call-count observation |
| `tool_execution_update` | none | |
| `tool_execution_end` | `tool.result` (`ok = !isError`, text as returned, already redacted) | |
| `queue_update` | none (bookkeeping for `steer`) | |
| `compaction_start`/`_end` | none; the usage of a `compaction_end` result is recorded | pi's auto-compaction is off; seeing one is logged as an anomaly |
| `auto_retry_start` | none; marks a retry in flight | pi's own provider retry and fallback |
| `auto_retry_end` `success:false` | `error` reason `provider_error`, detail `finalError` | |
| `agent_settled` | `final` (text of the last assistant message, `stopReason:"stop"`) or `error` (`truncated` for `length`, `provider_error` otherwise) | the one place a turn ends normally |
| `extension_error` | none; audited | |
| `entry_appended`, `session_info_changed`, `thinking_level_changed`, summarization retry events, `bash_execution_update` | none | decode as known |
| any other type | none | |
| stdout ends | `error` reason `runtime_unavailable` | `Turn.StopWith` |

Exactly one terminal event per turn: `Turn` refuses a second `final` or `error`,
and after a terminal event only usage rows are produced. Error reasons used:
the spec's `time-limit`, `cost-cap`, `stuck` and `runtime_unavailable`, plus
`provider_error`, `truncated` and `cancelled`, which the spec should list
(section 13).

The 10 minute clock, the 12-call limit and the `time-limit` stop belong to the
supervisor, not to pi, so they hold on every channel.

### Usage events and the ledger

On a gateway route (section 11 A) the ledger rows are the gateway's: one per
attempt, with the provider's own kind, usage and price
([llm.md](llm.md#agent-gateway)); the turn writes none from pi's events, which
then feed only the repetition guard and the stream. The rest of this section is
the `pi:` login, which pi calls itself.

pi's usage is on each assistant `message_end`: `input`, `output`, `cacheRead`,
`cacheWrite`, `reasoning`, `totalTokens` and `cost` in USD from pi's price
tables (**verified**: 120 input and 18 output tokens at 1 and 2 USD per million
priced 0.000156). One ledger row per assistant message:

| Ledger field | Source |
|---|---|
| task | `agent-kernel` |
| provider id, model | the row's provider and model; `pi:<id>` for a pi login (section 11) |
| outcome | `stop`/`toolUse` ok; `length` truncated; `aborted` aborted; `error` an error kind classified from `errorMessage` |
| tokens | `input`, `output`, `cacheRead` as cached input, `reasoning` |
| cost | 0: a login is a subscription |
| billing class | `subscription`; pi does not report one |
| latency, time to first token | measured by Go from `message_start` to `message_end` and to the first `message_update` |
| finish reason | `stopReason` |
| session | the conversation id |

When pi reports no counts on an `ok` response (`pirpc.UsageRecord.Estimated`),
Go estimates from text length and flags the row, as for any provider. Error
responses produced inside a pi retry are rows too (outcome `error`, zero
tokens), so the ledger shows every attempt. `message_end` is the only usage
source; `turn_end` and `agent_end` repeat messages and are not read for usage
(**verified**: a two-response run gives exactly two rows).

The generated `models.json` declares the gateway's model at price 0: the
gateway prices each attempt itself, and for a subscription Go records 0
whatever pi reports.

## 9. Stuck detection and the restart stand-in

A step is one assistant response that finished with `stop` or `toolUse`: its
text and its ordered tool calls. Identity ignores call ids, the reserved
`step` argument and object key order, and compares numbers as written
(`pirpc.RepetitionGuard`). The third identical consecutive step returns
`Nudge`: the supervisor sends `steer` with `pirpc.NudgeText`. A step that
differs resets the count, so a model that changes course is never ended; the
identical step right after the nudge returns `Stuck`: `Turn` emits `error`
reason `stuck` and asks for an abort. A repeated call still goes through the
guard chain each time. Replaying a recorded run in which pi was never nudged
gives the nudge at step 3 and the stop at step 4 (`TestPiRPC_Stuck_*`).

The stand-in is Go's, not pi's, because pi's session is not the record. On
loading a conversation, a recorded tool call with no result gets a result of a
distinct type (`stand_in`) before the conversation is next sent to a model. Text:
the server restarted before the call finished, the call may or may not have
run, check the runs board; no suggestion to retry. It is created for calls
abandoned by a pi restart and by a Go restart alike. Because the next turn
starts from `new_session` and the packed prefix, the model only ever sees the
stand-in through the prefix text, and tool-call and result pairing is
guaranteed by Go's record, which is also what closes the audit's open question on
call and result pairing after a context drop.

## 10. Process model

A turn owns one pi process for its duration: the supervisor starts it for the
turn and ends it with the turn, and processes are never shared or reused. At
most 3 run at once (a dashboard turn, a Telegram turn and a scheduled turn can
overlap) and a fourth turn waits for a slot. A single process would put an
interactive turn behind a scheduled one for up to 10 minutes; the spec now says
one process per concurrent turn (section 13, O-AG-4, taken as the default).
Background turns, which nobody waits at (`Turn.background`: a schedule's, a
digest's, or one a delegated task took by itself), hold at most 2 of the 3, so
a person's turn always finds a slot; a third background turn waits for a
background turn to end. Before, three working delegated tasks held every slot
and a person's message waited behind them, saying nothing
([exec plan](../exec-plans/active/2026-11-02-agent-architecture-dsh.md), item 1).
Each process has its own token, so the forwarding route can tell which turn a
call belongs to without a turn id in the protocol. Startup costs about half a
second warm (section 2).

A start that fails, or a process that exits mid-turn, is counted: the next
start is refused for 1, 2, 4, 8, 16, then 30 seconds, the version is checked
again before it, and after 10 failures in a row the loop stays unavailable
until `applysmith doctor` (or `Supervisor.Reset`). A turn that pi settled clears
the count. The refusal ends the turn as `runtime_unavailable` with the wait
in its detail.

## 11. Login ownership and how the `agent-kernel` row selects the model

The spec gives pi ownership of the agent's provider and subscription login. Two
sources exist; this contract uses both and is explicit where it departs from the
spec's sentence.

**A. API-key providers (default, no extra login), through the gateway.** Owner
decision 2026-10-23 ([llm.md](llm.md#agent-gateway)). `<agent dir>/models.json`
is written on each spawn with one provider per row served, `applysmith` for
`agent-kernel` and `applysmith-light` for `agent-light` (the file holds the latest
route of every row started so far and is replaced whole, so two turns of
different tiers starting together both find their model): `baseUrl` is the bridge's
`/internal/agent/llm` (pi appends `/chat/completions`, `/v1/messages` or
`/responses`), the wire is the one of the row's primary kind
(`openai-completions`, `anthropic-messages` or, for a ChatGPT plan primary,
`openai-responses`; **verified** with pi 1.0.4 against a fake plan), the one
model is the row's primary with `cost` 0,
`contextWindow` and `maxTokens` (the row's maximum output), and the key is
`$APPLYSMITH_AGENT_TOKEN`, the process credential, which `"authHeader": true` sends
as `Authorization: Bearer` on both wires (pi 1.0.4 `provider-composer.js`
`withConfiguredAuth`). Provider keys stay in Go: the gateway reads them from the
vault per attempt, and none is in pi's environment, argv, `models.json` or
`auth.json`. The model entry carries the two `compat` settings pi would have
detected from the primary's own base URL and that shape the history it sends
(`requiresReasoningContentOnAssistantMessages` for DeepSeek, `cacheControlFormat`
for Claude through OpenRouter); the gateway sets every other endpoint
difference per candidate. The name `applysmith` is none of pi's built-in providers,
so pi detects nothing from it: in particular pi does not take the Responses
request for a Sign in with ChatGPT one, so it still sends the fields the plan
refuses (`max_output_tokens`, `temperature`, `prompt_cache_retention`, ...) and a
`system` item, and the gateway removes them. This keeps the one-key presets true and the vault
the only secret store.

**Rows that span wires (virtual model).** The gateway never translates between
wires, so a row whose candidates are on more than one (a ChatGPT plan primary
with a DeepSeek fallback) is declared as one physical provider per wire
(`applysmith-responses`, `applysmith-chat`, `applysmith-messages`; `applysmith-light-...` for
the light row), each with the `api` and `compat` of the row's first candidate on
that wire. pi is started on `--provider applysmith --model agent`, a virtual model
the extension registers (`pi.registerVirtualModel`, pi `docs/virtual-models.md`)
when `APPLYSMITH_AGENT_ROUTE` (JSON: the virtual model and the physical models in
the order to try) is set. Its `route(request, ctx)` returns the first model that
has not failed in this process (which is also the model that answered the turn's
earlier requests, so a tool result stays on it); for `reason: "retry"` it first
marks `request.failed.model` as failed. With nothing left it throws, and the turn ends with the last error.
**Verified** with pi 1.0.4: the virtual model starts from `--model`, a retry
switches the physical model, and pi replays the history across the two APIs
(a Responses tool call and its result reached the Chat Completions model as an
assistant `tool_calls` message and a `tool` message, the call id rewritten by pi).
Pi's retry is bounded by `<agent dir>/settings.json`
(`retry.maxRetries 2`, `baseDelayMs 0`, `maxAgentDelayMs 0`), written before each
start with the same content for every process, and switched on or off per process
by the handshake's `set_auto_retry` (sent for every start: `true` for a row that
spans wires or a `pi:` login, `false` otherwise; pi checks the switch before the
counts, so the shared file cannot turn a process's retry on). A retry re-sends the failed model request
only: `agent-loop.js` ends the run on an assistant message with stop reason
`error` before any of its tool calls runs, so no tool side effect is repeated
(**verified**: a plan limit after a tool ran made one tool run in all). pi decides
by the error text whether to retry (`isRetryableAssistantError` in `utils/retry.js`:
a 429 or 5xx status and many phrases retry, a bad request does not), and refuses the
phrases of limits that last, among them `subscription_sharing_usage_limit_exceeded`;
`llm.AgentErrorText` rewords them in the gateway's error. A stream that fails after
its first content event reaches pi as the provider sent it, so a plan limit that
arrives then ends the turn.

**B. Subscription logins (pi's own).** OAuth sign-ins (the ChatGPT/Codex plan,
Copilot, an Anthropic plan) are made by pi's interactive `/login` in the
product agent directory (`applysmith agent login` runs `pi` attached to the
terminal with `PI_CODING_AGENT_DIR` set; there is no login command in RPC mode)
and live in pi's `auth.json`. Status comes from
`pi auth check --provider <id> --json`: it printed
`{"status":"not_ready","reason":"credentials_not_configured"}` with exit 1 and
`{"status":"ready","authType":"api_key"}` with exit 0 (**verified**); the
`authType` value for an OAuth login is **unverified**. A subscription is
billed `subscription` (cost 0).

**Row selection.** The `agent-kernel` row stays `provider`, `model`, optional
thinking and maximum output. Per turn the supervisor resolves it with the usual
order (table, `APPLYSMITH_MODEL_AGENT_KERNEL`, preset), and when the model differs
from the process's current one sends `set_model {provider, modelId}` (the full
model id, including a vendor prefix) and `set_thinking_level`. The row's
fallbacks on the primary's wire are walked by the gateway inside each call, those
on another wire by pi's retry ("Rows that span wires"), and `applysmith llm show` says so. The row's maximum output is
the generated model's `maxTokens`; its timeout is not a pi setting, the 10
minute turn clock and the 120 second stall rule replace it.

To route the agent to a pi-owned login, the row's provider is `pi:<pi provider
id>` (for example `pi:openai-codex`). That form is allowed on `agent-kernel`
only, validated with `pi auth check`, and skips the vault. The llm-routing rule
"every provider id names a configured provider" has this exception; the
llm-routing spec states it under "Agent turns run on pi" (amended with task 5.7).
The `pi:` row is built (task 5.2; see ask-agent.md, "The `pi:` login row"), the
owner may still veto it.

**CLI presets (`codex`, `claude`).** The preset default for `agent-kernel` stays
unrouted and the router answers alone, as specified (C-3). An explicit
`agent-kernel` table row (a `pi:` row once O-AG-1 is confirmed) takes
precedence over the preset default, so a user on the `codex` preset can still run
the agent on their subscription deliberately.

## 12. Cost cap

- **Before the turn.** On a gateway route the supervisor asks the router
  whether the spend limits stop the agent (`AgentCapBlocked`): the call path's
  own rule (`overLimit`: the local day's metered spend is at or over an enabled
  cap, read through the shared day read that is reused for at most 15 seconds
  and never across midnight) moves the agent's calls, the primary is metered,
  and no candidate is cheaper. Only then is the message answered by the router
  alone with a reply naming the cap, and no pi process is started. The agent
  kernel takes no per-task budget (`Budgetable`), so only the cap applies. A
  `pi:` login is a subscription and is never stopped.
- **During the turn.** Every call reads the same rule. Over it, the gateway
  serves the call from the downgraded order; with nothing cheaper it refuses
  the call (`cost-cap`), the bridge marks the turn, and the provider error pi
  then reports ends the turn as `cost-cap`. Calls already made stay in the
  ledger.
- **Alert.** The cost-cap alert fires from the scheduler tick as for any other
  task; agent rows are in the same ledger so they count.
- `compaction` and `memory-extract` calls are made by Go's own clients, are
  ledgered under their own tasks, and are subject to downgrade as usual.

## 13. Open items and what the specs still need

Owner decisions:

- **O-AG-1 Login for API keys and the `pi:` row.** Section 11 serves vault keys
  to pi through the gateway instead of making the user sign in to pi
  separately, and adds a `pi:<id>` row form for subscription logins. Both
  differ from the sentence "pi owns the agent's provider login". Confirm, or
  require the agent to use only pi's own `auth.json` (then a one-key preset
  does not reach the agent).
- **O-AG-2 Node and the pinned binary.** The compiled 1.0.4 executable removes
  the need for Node. The design says no Node at run time; this holds only for
  the binary, not the npm package.
- **O-AG-3 Installing pi.** User-installed (matches `cloudflared`) or a
  `applysmith agent install-pi` that downloads the pinned release asset and checks
  its SHA-256 against the release's `SHA256SUMS`.
- **O-AG-4 One live process or a small pool.** Section 10.

Spec follow-ups: the error reasons `provider_error`, `truncated` and
`cancelled`, the 120 second stall rule, the process-per-turn rule and the
sentence on API keys in "Agent runtime on pi over RPC" are in the ask-agent spec.
The ledger's provider kind for pi turns is `pi` (`llm.LedgerKindPi`). The
`pi:` row exception in `llm-routing` (a subscription login) is built; the spec
text of that capability still has to be amended.

Where the binary is found: `APPLYSMITH_PI_PATH` when set, else `pi` on `PATH`.
There is no workspace key `agent.pi_path`; use the environment variable.
Keep the complete extracted release directory, including `package.json` and
resources, together. Copying only the macOS executable caused `--version` to
report `0.0.0`; the intact release reported the pinned version.

Live acceptance on 2026-10-16 used isolated synthetic workspaces and the
existing DeepSeek key through child environment only. Both the installed npm
pi and the official standalone macOS arm64 release completed a dashboard turn:
one native `tracker_query` call, successful tool result, persisted final answer,
and two successful `agent-kernel` ledger entries. `PI_OFFLINE=1` allowed the
remote provider. The standalone archive checksum matched the official
`SHA256SUMS` (`717dcd38a03849e919f9dec9daa96f5ca102e15ea33d804e5db57b1d47e513bc`);
its executable was Mach-O arm64. The standalone turn recorded 18,824 input
and 117 output tokens, USD 0.000332136. This verifies observed provider usage
and ledger accounting, not independent billing or every cache-price variant.
No key was present in the generated `models.json`.

Still unverified with live accounts (task 5.7): Linux and Windows binaries,
subscription OAuth and its `authType`, all cache-pricing variants, pi's stall
behaviour, and two processes sharing one `auth.json`.

## 14. The contract test

`go test ./internal/agent/pirpc/...` replays JSON-lines transcripts from
`testdata/`:

| File | Origin |
|---|---|
| `turn-tool-then-answer.jsonl` | recorded from pi 1.0.4: one tool call, one answer, usage on both responses |
| `turn-steer.jsonl` | recorded: a steering message queued during a 3 second tool call |
| `turn-repeat.jsonl` | recorded: the model repeats one call six times (pi was never nudged) |
| `turn-abort.jsonl` | recorded: the model stream hangs and `abort` is sent |
| `turn-died.jsonl` | recorded: the process is killed while a tool call is in flight |
| `turn-edge-events.jsonl`, `turn-provider-error.jsonl` | written by hand from the 1.0.4 event reference: thinking blocks, tool progress, retry, compaction, extension error, a record type the contract does not name, and a retry that fails |

The recordings come from the scripted model server, with the system prompt
replaced by a short synthetic one and local paths removed. They contain no
personal data. To re-record after a pin change, run the same flags (section 3)
against the new release and check the diff of event shapes before changing the
pin. The test covers: every event type the contract names decoding (and an
unknown one being counted), the stream mapping, usage extraction (one row per
response, subscription zeroing, estimation flag, cap abort), the repetition guard
(three identical steps, nudge then stuck, key order and `step` ignored, reset on
change), cancellation, pi dying, a retried provider error, framing, the version
check and the launch flags.
