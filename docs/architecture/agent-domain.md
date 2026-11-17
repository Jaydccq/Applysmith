# Ask Agent: concepts and flow

This page names the Ask Agent's concepts once: what each is, the rule it keeps
and the one part that writes it. Read it before [ask-agent.md](ask-agent.md),
which holds the detail, and [agent-pi-contract.md](agent-pi-contract.md), the
wire contract with pi. A test (`internal/agent/domain_doc_test.go`) fails when
a path this page names no longer exists.

## Flow

1. **A way in starts a turn.** A message typed in the dashboard, a Telegram
   message and the "Let the agent finish" button call `Agent.Send`
   (`internal/api/chat.go`, `internal/api/telegram.go`,
   `internal/api/applyapi/views.go`), each naming its channel. A scheduled
   prompt or a Gmail trigger calls `Agent.RunScheduled`
   (`internal/agent/triggers`), and a delegated task takes its own turns
   (`taskLoop`, `internal/agent/taskrun.go`). Each of them starts its turn
   through `Agent.start`, which records the turn with its channel and origin
   and its first message, and `Agent.run` finishes it
   (`internal/agent/agent.go`). A conversation runs one turn at a time: in the
   dashboard a message during a turn steers it; Telegram answers that the agent
   is still working.
2. **The intent router** answers a plain request ("scan linkedin",
   "evaluate <url>") with one tool and no model (`internal/agent/route.go`).
   Everything else goes to the model loop.
3. **The model loop.** The classifier picks the light or the heavy model row
   (`internal/agent/tier.go`), the day's cap is checked, and the turn takes a
   pi process slot: three in all, of which background turns hold at most two
   (`internal/agent/supervisor.go`). `ContextPacker`
   (`internal/agent/context.go`) gives pi the instructions as the system
   prompt, then the packed history, then the turn's records as one message.
4. **Each tool call** reaches `Bridge.ToolCall` (`internal/agent/bridge.go`):
   at most 12 calls a turn, a repeated read answered from its first result,
   then the guard chain (`internal/agent/guard.go`): policy floor, channel
   envelope, approval requirement, confirmation by tier, submit gate
   (`internal/gates`), idempotency claim. One function carries out the
   decision for the model's and the router's calls (`Bridge.carryOut`): a
   hold first asks the tool whether it would refuse the call, and only then
   becomes a confirmation card; an allowed call runs, and its result is
   redacted, fenced as untrusted, and taints the turn when it carries outside
   text.
5. **A run tool** starts a background run through `RunGate`
   (`internal/agent/rungate.go`): two runs of conversations at once, a start
   past that stored until it begins. The run's origin is recorded, and its end
   becomes a note in the conversation that wakes a task waiting for it
   (`internal/api/apply_resume.go`).
6. **The answer** is an envelope (text, citations, limitations, next action)
   that the disclosure gate (`internal/agent/disclosure.go`) checks before it
   is stored, streamed and sent back the way the message came.

## Concepts

| Concept | What it is | Rules it keeps | Written by |
|---|---|---|---|
| Conversation | A chat session and its messages (`chat_sessions`, `chat_messages`) | Runs one turn at a time (`ErrTurnRunning`); the outside-content marks are on its rows, so a restart keeps a conversation's taint | `sessions.Store` (`internal/agent/sessions`) |
| Turn | One answer to one message (`chat_turns`): its channel, origin, model tier and harness version | Ends with exactly one final or error event; a turn running when the server stopped ends as `runtime_unavailable` at the next start (`Agent.Start`) | `sessions.Store`, for `Agent` and the Supervisor |
| Channel | Where a turn came from: `dashboard`, `telegram`, `scheduled`, `digest` | Selects the permission envelope (`Channel.allows`): scheduled and digest turns read and draft only | Named by the way in |
| Origin | Who chose a call: `model`, `router` or `button` | A router's or a button's call counts as the user's own request; a model's does not; the turn's row keeps it | Named by the way in |
| Background turn | A turn nobody waits at: a schedule's, a digest's, or one a task took by itself (`Turn.background`) | Holds at most two of the three pi slots, so a person's turn always finds one | Derived from the turn |
| Tool and class | One of the closed catalog's tools (`internal/agent/catalog.go`), each `read`, `draft`, `write-local` or `external-action` | A tool whose service is missing is not offered; the class, never the wording, decides which gates apply | The registry (`internal/agent/registry.go`) |
| Guard decision | Allow, hold or deny for one call, with the gate and the reason | Every decision is a `gate_decisions` row; text inside results, memory, goals or skills never moves one | `Guard` (`internal/agent/guard.go`) |
| Confirmation card | A held call waiting for the user (`agent_confirmations`) | Expires after 12 hours; approval runs the whole chain once more with the confirmation | `approvals.Store` (`internal/agent/approvals`), through `Confirmations` (`internal/agent/confirm.go`) |
| Standing approval | An approval for once, this conversation or today, for the tools of `approvals.EligibleTools` | Lasts at most 24 hours, at most 20 active; never covers a submit | `approvals.Store` |
| Taint | The mark of outside text on a stored row, and the untrusted state of a turn that read it | Follows the text across turns, checkpoints and restarts; makes a model-chosen action wait on a card | The row's writer, through `sessions.Store` (`internal/agent/taint.go`) |
| Envelope | The final answer with its citations, limitations and next action | The disclosure gate replaces any envelope that leaks the prompt, a fence, a protected path or a secret | The turn (`Turn.Collect`), checked by `DisclosureGate` |
| Run origin and application request | Which conversation started a run, and the end of it the conversation was last told (`agent_run_origins`); for an application, the request it started from (`agent_apply_requests`) | A resumed application decides its submit again from the stored request (`SubmitAgain`); an end counts for the conversation's task until it is told, and one the queue missed is told by a pass at start and every 10 s (`completionNotes`, `internal/api/apply_resume.go`) | `origins.Store` (`internal/agent/origins`) |
| Queued start | A run start that waits for one of the agent's two runs to end (`agent_queued_starts`) | Counts as work going for its conversation's task; one a stopped server left is told to its conversation at the next start and never replayed | `origins.Store`, for `RunGate` |
| Board entry | A unit of work on the Runs board (`board_entries`) | Closed states and transitions; working entries become `done/interrupted` at a start | `notify.Board` (`internal/notify/board.go`) |
| Delegated task | An outcome the user handed the agent in one conversation (`agent_tasks`) | Its mandate is the user's own message; one open per conversation, three in all; its own turns never ask to submit | `tasks.Store` (`internal/agent/tasks`) |
| Goal | The user's plan with steps and a cadence (`agent_goals`) | Data only: no gate reads it; its plan changes when the user's own words ask (`manage_goal`) or the user accepts the agent's proposal | `goals.Store` (`internal/agent/goals`) |
| Memory fact and operating rule | A statement about the user (fact) or a line of guidance (rule) | Facts are records after the history; a rule reaches the system prompt only once the user approves it, and cannot change a gate | `memory.Store` (`internal/agent/memory`) |
| Harness guidance | Lines and tool notes learned from earlier conversations | Versioned; each turn records the version it ran with; ranks below every fixed rule | `harness.Store` (`internal/agent/harness`) |
| Skill | A procedure file (`internal/agent/skills`) | Narrows the tools a turn is offered and adds instructions; cannot change a class or a gate | The repository |
| Scheduled prompt and trigger firing | A prompt on a cadence or on Gmail signals (`agent_prompts`), and one firing of it (`agent_trigger_firings`) | One the agent proposes is active only once the user confirms it; a firing happens once by its key; its turn reads and drafts | `triggers.Store` (`internal/agent/triggers`) |
| Checkpoint | A summary of older turns that compaction wrote (`internal/agent/compact.go`) | Calls itself untrusted data; carries the taint of what it covers | `sessions.Store` |

## The prompt

The system prompt holds instructions only, the same on every turn of a
conversation for a day: the fixed rules, the channel's notes, the date line,
the operating rules, a skill's block and the harness guidance. The turn's
records, which change with each message (memory facts, goals, the open task,
the cards), follow the packed history as one message marked as data. The
provider's cached prefix then covers the system prompt and the history.

## Names to watch

- **"Tier" means two things.** The confirmation tier (0 to 4) of a call comes
  from its class and arguments (`tierOf`, `internal/agent/guard.go`); the
  model tier (`light` or `heavy`) of a turn picks its model row
  (`internal/agent/tier.go`). Say which one.
- **No turn runs on the `digest` channel today.** Gmail digests are posted into
  conversations of the `digest` kind, but a reply typed there runs as a
  `dashboard` turn, and nothing starts a turn on that channel; the channel's
  rules are kept for when one does.

## Ideas not adopted

The ideas taken from dsh-superagent and the ones left out, with the reasons, are
in [the exec plan](../exec-plans/active/2026-11-02-agent-architecture-dsh.md).
