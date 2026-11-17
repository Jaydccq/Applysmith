# Ask Agent

The Ask Agent runs on the pi runtime. This file says what each part does and
where it lives. The wire
contract with pi is [agent-pi-contract.md](agent-pi-contract.md); requirements
are in the `ask-agent` spec. The concepts this page uses, the flow of a turn and
which part writes each record are on one page: [agent-domain.md](agent-domain.md).

## What is built

| Part | Where | Notes |
|---|---|---|
| Wire mapping, framing, repetition guard | `internal/agent/pirpc` | pure; replays recorded transcripts |
| Conversations, FTS5 recall, stand-ins, claims, audit | `internal/agent/sessions`, migration `0015_agent.sql` | tables `chat_sessions`, `chat_messages`, `chat_turns`, `agent_claims`; audit rows go to `gate_decisions` |
| Tool registry and its rules | `internal/agent/registry.go`, `schema.go`, `catalog.go` | `SpecCatalog` defines the permission class of each tool; `Registry.Missing` names what is not built |
| Read tools | `internal/agent/tools_read.go` | `tracker_query` (`sort` `updated` or `score`), `find_report`, `answer_bank_lookup`, `answer_bank_find` (`tools_bank.go`: the saved answers sharing words with a query, with ids and states), `apply_list_status`, `list_pipeline` (the inbox's count for the status, then the rows shown with the local day each was found and the day the source says it was posted when it gave one, which the agent had read page after page to check "posted this week"; `order` `score` or `newest`; a row the inbox's own gate turns away, `evaluate.ScannerGate` as the profile stands, is marked "the user's filters skip it (rules)", as the agent had sent Senior, Staff and Principal postings to evaluations that ended at the gate), `recall_session` (the 5 best snippets oldest first, without highlight marks, which the model had copied into answers); a `limit` over 25 on `tracker_query` or `list_pipeline` lists 25 instead of failing |
| Apply tools | `ApplyTools`, `applyTools` (`tools_apply.go`) | `autofill_application`, `apply_from_list`: full metadata; with `ToolDeps.Apply` set (the server sets it) they start a background run on the apply chain and return its run id, without it they are declared only and not offered; the guard chain decides and audits their calls first (`docs/architecture/apply.md`, "Server wiring") |
| Guard chain | `internal/agent/guard.go`, `intent.go` | policy floor, channel envelope, approval requirement, action confirmation by tier, submit gate (delegated to `internal/gates`), idempotency claim |
| Bridge | `internal/agent/bridge.go`, `pi_extension.mjs`, `api/agent_bridge.go` | catalog, hello, turn context, tool call; per-process credential |
| Supervisor | `internal/agent/supervisor.go`, `turnloop.go`, `turnevents.go` | version check, launch, handshake, event loop, clocks, abort, backoff, 3 processes, of which background turns hold at most 2 ([agent-pi-contract.md](agent-pi-contract.md) section 10) |
| Service | `internal/agent/agent.go`, `stack.go` | send, steer, cancel; `NewStack` assembles everything |
| Chat routes | `internal/api/chat.go` | list, detail, rename, delete, messages, events, steer, cancel, fork |
| Intent router | `internal/agent/route.go`, `routed.go` | ordered rule table; a routed call runs through `Bridge.RunRouted` |
| Packing and compaction | `internal/agent/context.go`, `compact.go` | `ContextPacker` (token budget), `Compactor` (checkpoints), migration `0018_agent_memory.sql` |
| Memory | `internal/agent/memory` | facts, rules, review queue, nightly consolidation; tools in `tools_memory.go` |
| Wiki tools | `tools_wiki.go`, `skills/wiki.md` | `wiki_find`, `wiki_read` (read) and `wiki_write` (write-local, requires approval): the Ask Agent keeps the user's wiki pages from what they say; see "Wiki maintenance from Ask" in [kb.md](kb.md) |
| More read tools | `tools_knowledge.go`, `tools_status.go` | `kb_search`, `retrieve_evidence`, `story_context`, `profile_summary` (work authorization as `profile.yml` states it; contact and identity left out), `load_prompt_mode`, `submissions_recent`, `list_schedules`, `preview_schedules`, `gmail_signals` |
| Gmail tools | `tools_gmail.go` | `gmail_action_inbox` (the inbox read model, with thread ids) and `gmail_digest` (the latest stored brief; `generate: true` calls `Digests.Generate`, which stores and delivers nowhere, inside 20 s); `gmail_deadlines`, `gmail_thread` and `gmail_search` (the open deadlines, one thread's mail, a search of the job mail read); `gmail_thread_action` (write-local: done, reopen, dismiss, restore, snooze through `gmail.Repo.Act`, no card when the user asked) and `draft_gmail_reply` (draft). The reads and the draft return email text, so they are outside content and taint the turn. See [gmail.md](gmail.md#ask-agent-tools) |
| Consolidation job | `internal/scheduler/jobs/jobs.go` | kind `memory.consolidate`, the `memory-nightly` pack at 03:30 |
| Sessions CLI | `internal/cli/agent.go` | `applysmith agent sessions list|show|export|delete` |
| Taint, redaction, status phrase | `internal/agent/taint.go`, `redact.go`, `stepphrase.go`, migration `0019_agent_taint.sql` | the fence, the redactor, the display argument; see below |
| Safe page reading | `internal/agent/pageread`, `tools_page.go`, `internal/identity/publichost.go` | tool `read_page`; the host predicate is shared with discovery |
| Delegated tasks | `internal/agent/tasks`, `taskrun.go`, `tools_tasks.go`, `internal/api/agent_tasks.go`, `web/src/pages/askagent/Task.tsx`, migrations `0056_agent_tasks.sql`, `0057_agent_task_ideas.sql` | an outcome the agent carries over several turns, typed in a conversation or handed over from Home; see "Delegated tasks" below |
| Goals | `internal/agent/goals`, `tools_goals.go`, `internal/api/goals.go`, migration `0022_agent_goals.sql` | the plan store, the goals block, `list_goals`, `manage_goal`, `propose_goal_change`, accept and dismiss, the check-in timer; see below |
| Run and write tools | `tools_actions.go`, `tools_writes.go`, `tools_bank.go` | `evaluate_job`, `scan_portal`, `prepare_registration`, `tailor_resume`, `answer_bank_save`, `answer_bank_forget`, `batch_skip_list`, `record_story_answer`, `propose_schedule_change`; services in `internal/api/agent_actions.go`; see below |
| Layer 3 tools | `tools_layer3.go` | `draft_application_answer`, `application_plan`, `application_workspace` |
| Research tool | `tools_research.go`, `internal/research` | `research_question`; see "Research" below |
| Message tools | `tools_prompts.go` | `draft_outreach`, `draft_followup` (`internal/messages`) and `decode_posting` (`evaluate.Decode`); wired in `internal/api/domain.go` |
| Disclosure gate | `disclosure.go` | the rule table and the answer envelope on every final answer; see below |
| Schedule proposals | `internal/agent/schedprop`, `internal/api/schedule_proposals.go`, migration `0027_agent_schedule_proposals.sql` | the proposal store behind `propose_schedule_change` and its accept and dismiss routes |
| Memory and switches | `internal/api/agentapi/memory.go`, `web/src/pages/askagent/Memory.tsx`, `web/src/pages/settings/AskAgent.tsx` | routes for the Memory tab and the Ask Agent switches |
| Harness and self-improvement | `internal/agent/harness`, `internal/agent/improve`, migration `0047_agent_harness.sql` | the editable guidance and tool notes each turn runs with (`chat_turns.harness_version`), side-effect-free replays, the nightly `agent.improve` kind and its `agent-improve-nightly` pack; see [ask-agent-improve.md](ask-agent-improve.md) |
| Model calls and ledger | `internal/llm/agent.go`, `gateway.go`, `affinity.go` | `AgentRoute`, `AgentCapBlocked`, `ServeAgentCall` (the gateway pi calls; [llm.md](llm.md#agent-gateway)), `RecordAgentUsage` for a `pi:` login (provider kind `pi`); the `pi:<id>` row form |

## A turn

1. `POST /api/chat/sessions/{id}/messages` (`new` starts a conversation) stores
   the user message and the turn row and answers `202`.
2. `Supervisor.RunTurn` gives orphaned calls their stand-in result, resolves the
   `agent-kernel` row (the `agent-light` row for a turn the intent classifier
   called light, when that row is routed:
   [llm.md](llm.md#agent-gateway)), checks whether the day's cap stops the agent (reached,
   with no cheaper model routed), then starts a pi process with the contract's
   flags, pointed at the gateway: no provider key reaches it. A row that is
   missing, or that the runtime cannot call (the offline simulation, a provider
   with no stored key), ends the turn as `no_model_route` with
   `agent.DetailNoModelRoute`, which names Settings, Models and no command: the
   router's own error text carries `applysmith llm configure`, which stays with the
   command line. The sentence is `llm.NoModel`, which the drafting and research
   tools and the Tracker's message drafts also give for a task with no route.
3. The handshake sets compaction off, the thinking level and (on a gateway
   route) pi's own retry off, starts a fresh pi
   session and waits for the extension's hello to report exactly the catalog.
   The message pi is given ends with a note naming the message's language
   (`languageNote`: Chinese, Japanese, Korean or English, by script; none when
   the script decides nothing), because the model otherwise answers in the
   language of the previous answer. The stored message has no note, and the
   system prompt stays the same, so its cached prefix is kept.
4. pi's events are mapped by `pirpc.Turn` to stream events; the supervisor
   stores the record and watches the clocks: 120 s silence, 10 minutes (30 for
   a turn bound to one run, `RunTurnLimit`), the repetition guard. Each model call pi makes reaches `Bridge.ModelCall`, which
   hands it to the gateway under the turn's conversation; the gateway writes
   one ledger row per attempt, and a call it refuses for the cap ends the turn
   as `cost-cap`.
5. Each tool call reaches `Bridge.ToolCall`, which counts it (12 at most;
   the twelfth adds a limitation to the answer), validates its arguments, runs
   the guard chain, executes with the tool's timeout (a tool that ignores its
   context is abandoned at the deadline; the longest is 90 s,
   `MaxToolTimeout`, inside the tool-call route's own 100 s handler deadline,
   and the silence clock does not run while a call is in flight) and
   answers with the result cut to 12,000 characters at the end of a whole line,
   followed by `[truncated: N more characters]` (a context budget, kept
   deliberately). `find_report` leaves out the report's scorecard block (the
   Fit table again as JSON, a third to over half of a report), so the verdict,
   pay, legitimacy and the other sections after it fit: before, 3 of the 5
   reports in the demo and owner workspaces lost everything after Fit. Its
   description tells the model to show the text it has when the user asks to
   see a report and to give `/reports?id=<number>`, where the whole report
   opens, and a read by number sets that link as the next action: live
   (2026-11-02), "show me report 7 in full, verbatim" got a refusal to show a
   shortened copy, and nothing at all.
   Tools that start a run answer within 5 seconds.
   A read call repeated with the same tool and arguments (the step
   phrase and key order aside, `pirpc.CallKey`) is answered with the first
   result, marked as a repeat, audited `repeated_call`, not run and not
   counted; a call of any other class (a draft writes a proposal, a task's
   step or an interview reply, or asks a model) runs each time and clears
   those results, a failed or held call is not kept, and the fourth repeat in
   a turn is told to answer (`MaxRepeats`). Live: a turn had spent all 12 calls on
   `recall_session`, several with the same query. A held call ends the turn's
   tools (`FinalOnly`), and its result says a card now waits and the model
   must stop and tell the user: the model had gone on to `read_page`. A draft
   tool (class `draft`) that fails or times out answers "Nothing was drafted
   ... do not write the draft yourself" and adds a limitation to the answer;
   the system prompt says the same for every draft and decode tool, because the
   model had written its own outreach message in place of a failed draft.
6. The turn ends with exactly one `final` or `error` event; a final answer the
   model left empty, or wrote as tool-call markup (`<tool_call>`,
   `<function=`; neither streamed nor stored), is replaced by why (the call
   limit, a tool call written out) and the lines the turn's tools returned; the process is
   closed and its credential revoked. A server that stops mid-turn ends the turn
   first (`Agent.Close` before the streams close): the stream carries the
   `error` event (`runtime_unavailable`, detail "the server stopped during the
   turn", `agent.DetailServerStopped`) and then the `shutdown` event, and the
   Ask page shows "The turn stopped: ..." instead of a running turn, even when
   the conversation cannot be read again because the server is gone. The
   stored turn row keeps the reason and detail, so every later visit shows
   the same note under that turn (`Transcript.tsx`, the reason code in
   words, a link to the model settings for `provider_error`, `cost-cap` and
   `no_model_route`),
   and the conversation's last turn offers to send its message again.

The session-detail route reads session metadata, messages and turn rows from
one SQLite read transaction. Separate reads can straddle answer completion and
show a terminal turn without its answer. A request sees one persisted snapshot;
the in-memory active-turn identifier remains advisory and can change immediately
after that snapshot.

## Intent router

`RouteMessage` looks at the message alone and tries these rules in order; a
message with more than one request ("and then", "and draft", two URLs) skips
them all and goes to the model loop, and so does a message that names words of
two stores (the tracker and the mail, the tracker and the apply list):
`ambiguous`, so no rule wins by coming first.

| Rule id | Message | Tool |
|---|---|---|
| `url-only` | one posting URL and nothing else, or "evaluate <url>" (also the Chinese "help me evaluate this posting: <url>") | `evaluate_job` |
| `recent-submissions` | "what did I submit today", "recent submissions" | `submissions_recent` |
| `apply-list-run` | "apply to today's list" | `apply_from_list` (no submit) |
| `apply-url` | "apply to <url>", "autofill <url>", also with "but don't submit it" after it | `autofill_application` (no submit) |
| `apply-list-status` | "what's on the apply list" | `apply_list_status` |
| `tracker-counts` | "how many applications are in interview", made only of counting words and statuses (`trackerCountWords`); "what's my highest scored application", advice or a company's name go to the model, which had been answered with the rows in date order | `tracker_query` |
| `scan-source` | "scan linkedin" (a source of the catalog; also the Chinese "scan jobright") | `scan_portal` |

A question mark inside a URL is part of the URL, and a URL ends at Chinese,
Japanese or Korean text or full-width punctuation glued to it, as the submit
gate's URL tokens do. A message that is a question (a `?` or `？`, a leading
question word, or a Chinese question word such as the particle "ma" or "should
or not") or that puts a past-tense auxiliary before the verb never reaches an
apply rule. Rules and verb tables know English and Chinese (owner decision A-1,
changed 2026-10-19); Chinese is matched inside runs of Han characters, never
with `\b`, and other scripts go to the model loop.

The tier check (`namesVerb` in `intent.go`) reads only the user's own words:
fenced blocks and `>` quoted lines are pasted, and a message of more than three
lines or 600 characters counts by its first non-empty line and by its last
when that line starts with a request verb and is more than a posting's "Apply
now: <url>" (each cut to 300 characters), so a pasted job description's "Apply
now" names nothing. Steering messages delivered in the turn are the user's
words too, each read by the same rules. The submit gate's URL tokens and the
correction wording read the same own words, which the run carries as
`apply.Input.OwnWords` for its correction door. Only apply and submit verbs
(and their Chinese forms) are the explicit request a submit needs; fill and
autofill start a fill with `submit` false. A
question names no action (`message.asks`, built on the router's question
predicate), except a request put as a question that starts with its verb ("can
you scan linkedin?", "evaluate <url> - what do you make of it?"), which names
that verb alone; "should I apply to <url>?" names none, so it is neither a
tier-3 intent nor the explicit request of the submit gate. A negation or past
marker before a Chinese verb in its clause, or a completion particle after it,
cancels it. A restriction ("do not submit", in English or Chinese) is read from
the whole message. A search verb (`searchVerbs`: search, find, discover and the
Chinese "find") names `scan_portal` and `delegate_task` only, never a fill or
an evaluation: "Find me new senior platform engineer jobs posted this week" had
held the task on a card.

A write whose object matters (`writeObjects` in `guard.go`) also needs the
object named: `batch_skip_list` the skip list and, for `remove`, the company
itself; `forget_fact` its memory. A bulk removal ("all", "every", or a second
removal in the turn) is held for a card per company, and `action: list` is a
tier-0 read. Router: "and" before a second verb or a question word ("what is
the top one") is multi-step and goes to the model.

`Agent.Send` uses a route only when the tool is in this build's registry and the
channel's envelope contains its class; otherwise the model loop gets the
message. A routed turn is recorded with origin `router` and the rule id,
refuses steering with the same conflict as a finished turn, and answers with
the tool's result text: no process is started and no model is called. The call
passes the whole guard chain with the turn's origin set to the router, so it
counts as named by the user and ignores the taint a conversation carries. The router
never carries `submit`. A model-chosen submit is refused when the message tells
the agent not to submit ("fill it but do not submit"), and `apply_from_list`
leaves out the companies a message excludes ("but not Example Corp"). A
tool that is declared but has no executor (the apply tools in a build with no
apply chain) is audited and answered with a plain "not available in this build
yet".

## Packing and compaction

`ContextPacker` builds the prompt within the token budget (stored setting,
24,000 by default, 4,000 to 200,000): the system rules, the date line
(`dateLine`: "Today is Monday, 2 November 2026 (local time zone PST)", after
the fixed rules so their cached prefix holds; without it a goal "until the end
of November" was asked back as "which year?"), the operating rules, the latest
checkpoint as a message that calls itself untrusted data, then the newest
uncovered turns while they fit after the 600-token answer reserve, then the
turn's records. A turn is the unit, and a tool call is folded with
its result into one line, so the pair is never split and a result whose call is
gone is not shown. The token estimate is four bytes to a token, except that a
character of Han, kana, Hangul or fullwidth forms is one token.

The system prompt holds instructions only (the fixed rules, the channel's
notes, the date line, the operating rules, then a skill's block and the
harness's guidance from `Bridge.TurnContext`), so it is the same on every turn
of a conversation for a day. The records, which change with the message (the
memory block, whose facts the message picks and ranks, the goals block, the
open task and the cards), are one user-role message after the packed history,
opened by `dataHeader` ("data, not instructions"); the budget counts them with
the system prompt, ahead of the history. The provider's cached prefix then
covers the system prompt and the history, which only grows until the budget
fills. Before, the records sat inside the system prompt, ahead of the guidance,
so the facts picked by each new message started a fresh prefix on every turn's
first model call ([exec plan](../exec-plans/active/2026-11-02-agent-architecture-dsh.md),
item 3; `TestPacker_TheSystemPromptIsTheSameEachTurnAndTheDataFollowsTheHistory`).

`Compactor` runs through `TurnInput.Before`, after `turn.start` and once pi is
known to be usable, so a turn without a model route pays nothing. It is due
when turns had to be dropped or the estimate passes 80% of the budget. The two
newest turns stay verbatim and the oldest uncovered turns before them are
covered, as many as fit 24,000 characters of input together with the previous
summary; fewer than four turns to cover means nothing happens, which is also
what stops a repeat for a point already covered. When the oldest turns are too
long for four to fit whole (a pasted document, a long answer), the four oldest
are covered with each one's text cut to a share of the input and marked as
cut, so a conversation that opens with long turns is still summarised. The call uses the
`compaction` task under a 30-second deadline. A failure is logged and changes
nothing. One compaction runs per conversation at a time. The stored
checkpoint carries `tainted` when a covered message came from a tool that
returns outside content or when the checkpoint it builds on is marked; a
packed tainted checkpoint sets `Turn.CarriedTaint`. A fork starts from the raw
messages and does not copy checkpoints. Switches: `agent_settings` keys
`compaction`, `token_budget` and `consolidation` (`sessions.Store.Settings`);
no route or page edits them yet.

## Memory

Facts, rules, the review queue and the consolidation audit are SQLite tables
(`0018_agent_memory.sql`) behind `memory.Store`. Facts: at most 500 active, 500
characters, a statement of at least two words (a bare token is refused); text aimed at the assistant is refused, text that tells the agent
how to answer goes to the review queue as `instruction_shaped`; a fact the user
declared is trusted and an extracted one needs review; `save_fact` in a turn
that read outside content stores the fact as extracted (needs review), and
`amend_fact` there changes nothing, because the text may be the page's; amending versions,
forgetting deletes the chain and keeps a hash tombstone. Rules: at most 300
characters, 20 pending and 30 active; a rule that would change a gate,
permission or confirmation is refused; only `ApproveRule` (the user's act, no
tool) activates one. The prompt gets trusted facts ranked by shared words and
recency within 2,000 tokens, company and application facts only when named, each
with its id so `amend_fact` and `forget_fact` can address it, among the turn's
records after the history; active rules go in a separate block in the system
prompt. Nothing in the guard chain reads memory, and a test
puts hostile rows into the tables to show the decisions do not move.

`memory.Consolidator` is the `memory.consolidate` kind. It reads only the user's
own messages of conversations quiet for 30 minutes since the last pass, asks
the `memory-extract` task for candidate facts and rules (once per conversation,
and once over the last 30 days for patterns), and writes needs-review facts and
proposed rules. A candidate that shares no word of four letters or more with
what the user said is dropped: it has no evidence. A skipped, failed or finished pass is a row in `memory_runs`,
and every emitted item a row in `memory_audit`. Episodes are not stored
separately: the extraction prompt takes decisions and context as fact kinds.

## Taint, redaction and the status phrase

Taint follows the text. A stored message has `outside_content` (migration
0019) when it is the result of a tool the registry marks `OutsideContent`
(the asterisk tools of the catalog) and the result holds outside text, or it
is a system-made message built from third-party text. A tool says so with
`ToolResult.Clean` when what it returns has no outside-sourced content (an
empty set, a status line the product wrote), decided from the data it
returns and never from its wording; such a result does not taint the turn or
mark the row. A refusal a tool answers with (`ErrRefused`: a full queue, a scan
already running) is clean, and so is a call the demo workspace's gate refused
(`store.DemoRefusal`, for example `gmail_digest` in the demo, which never reads
mail): the bridge answers it as `Refused: <the gate's reason>`, not as a failed
call, because nothing was read. Any other failed call, and any result that does
not say `Clean`, taints.
The mark is written with the row, so a restart does not
clear it and a fork copies it with the rows it copies. `ContextPacker.Pack`
sets `Turn.CarriedTaint` when a marked message or a tainted checkpoint is in
the packed context; `Compactor` marks a checkpoint tainted when a covered
message is marked or the checkpoint it builds on is. A call the router chose
ignores `CarriedTaint`; a model-chosen call sees it through `Turn.Untrusted`,
which is what the submit gate and the tier-3 confirmation read. The tier-3
confirmation holds a tainted call on a card, except a fill that stops at
Submit (`outsideAllowed` in `guard.go`: `autofill_application` and
`apply_from_list` with `submit` false), which runs with the code
`outside_content_allowed` when the user's message names a fill. The owner
chose this on 2026-11-12: a delegated "evaluate, pick three, fill them" task
had stopped on a card for every posting it picked from the reports it read.

Every result of a tool that ran reaches the model between
`<<untrusted-tool-result tool="...">>` and `<</untrusted-tool-result>>`
(`FenceOpen`, `FenceClose`; also around a result folded into the packed
history). Text inside a result that imitates a marker is defanged. A hold or a
refusal is the system speaking and is not fenced. The disclosure gate, when it
lands, must block the markers in final answers.

`Redactor` rewrites a result before the model, the record and the stream see
it: spans shaped like a credential become `[REDACTED:credential]` and values
of secrets the vault holds (`vault.RedactAs`) become `[REDACTED:vault]`. The
audit row (gate `result_redaction`) carries the categories and the count,
never the text. A rule that panics withholds the whole result.
`credentialShapes` in `redact.go` is the list the disclosure gate reuses. A
key or token assignment whose value is digits alone is an id, not a credential
(Greenhouse names a posting `token=4114318009`); the parameters after it in a
query string are checked on their own, and a password or secret is redacted
whatever it holds. Under any other name, a slug value (lowercase words joined by
`-` or `_`, such as an answer-bank entry id) and a lookup key name (`entry key`,
`primary_key`, `keyword`) are not credentials.

`step` is stripped by `splitStep` before validation, hashing, claims and every
gate. `StepPhrase` shows it as the call's progress line, or the tool's fixed
label when it is absent, 200 characters or longer, credential-shaped or a
protected path; otherwise it is cut to 60.

`read_page` (`pageread.Fetcher`) accepts http and https only and refuses
user-info. `identity.NonPublicHost` judges the name or literal (also decimal,
hexadecimal and octal spellings, mapped IPv6), `identity.NonPublicAddr` every
resolved address. The name is resolved once per hop and the connection goes to
a checked address; each redirect (5 at most, same scheme family) is checked the
same way; no proxy settings; 1 MiB of body, 20 s, a content-type allowlist
(html, xhtml, plain text, json). A refusal is audited under gate `page_read`
with the host only. Discovery's invalid-URL filter calls the same predicate.
Before any fetch, an address whose path, query or fragment (URL-decoded, any
case) carries the profile's email, the last seven digits of its phone or its
full name as whole words is refused (`profileInURL`). A page that answered 2xx with text
is a citation of the answer (its title, else its host, and the address the
model asked for), as a research source is; a 404 or an empty page is not. Live
(2026-11-02): an answer cited a release page it had read, and the Sources
under it did not list that page. A page with no readable text whose address is
an ATS posting (`identity.ATSKey`) is read through the ATS's public API with
the evaluation's `evaluate.ATSReader` (`ToolDeps.Postings`; title, company,
location, compensation and the description, cut as a page's text is) and cited;
a posting the API does not give leaves the page's own answer. Live, every
Ashby posting a scan had found read "The page has no readable text", and the
agent spent its turn guessing at Ashby's API.

## Scheduled prompts and Gmail triggers

Package `internal/agent/triggers`, migration `0026_agent_prompts.sql`. One row
type, `agent_prompts`, holds both kinds: a cadence (weekdays and a local time)
or a trigger (a set of signal types out of `interview_scheduled`,
`recruiter_reply_needed`, `rejection`, `online_assessment`, and an optional
employer). Ten rows in all, 2,000 characters each. A user-made prompt is
`active` at once; `manage_scheduled_prompt` creates only `proposed`, and
`POST /api/agent/prompts/{id}/confirm` is the one way to activate (no tool,
no Telegram callback, loopback only; the Triggers tab of Ask Agent). The
tool's description and result name that tab and say Telegram cannot confirm
(the agent had told a user to confirm in Telegram). A text with an apply, submit, register,
fill or write-data verb is refused (`ErrUnsupported`), and the result tells the
model not to reword it; the description and a prompt rule say `text` is the
user's words unchanged and that a scheduled turn only reads and drafts (a model
had reworded a refused prompt and promised evaluations). Because the confirm is
the user's approval, a `create` is tier 1 like `propose_schedule_change`
(`draftActions`): "Every weekday at 8:30am, give me ..." names no write verb
and had been held on a card the user approved before confirming the proposal
as well. `pause`, `resume` and `delete` stay tier 2. The stored text loses a
`languageNote` the model copied with the message (`withoutLanguageNote`; a live
proposal carried "(This message is written in English. ...)").
`active_since` is the
confirmation, the last resume or the last edit of the schedule; a slot or a
message before it never runs.

**Cadence** (`scheduled-prompts` timer): the latest slot only, due for 3 hours,
claimed by writing `last_slot` before the turn. **Trigger** (`signal-triggers`
timer, inactive while Gmail is not connected or refuses its app password): each tick reads
`gmail_signal_events` after the high-water mark (`workspace_meta` key
`agent.triggers.high_water`), matches incoming `actionable` events, skips and
records as `skipped` a message older than 24 hours, and claims at most 5
`(trigger, message)` pairs in `agent_trigger_firings` (primary key = the
exactly-once rule, so a lost mark, a restart, a resync or a re-classification
fires nothing twice). The mark moves up to the last event whose pairs are all
handled; the rest wait while still inside 24 hours. A firing that was claimed
when the server stopped is settled `failed` at start and never rerun.

One scheduled turn runs at a time (`Engine` has one worker); a trigger batch
holds the `llm:evaluation` lease (`Scheduler.TryLeases`) and is not claimed
while another job holds it.

**The turn** is `Agent.RunScheduled`: a new `scheduled` conversation, the 10
minute limit, read and draft tools only. A trigger firing also starts tainted
(`Turn.MarkOutside`, the prompt message stored with `outside_content`) and
withholds `read_page` (`Turn.Withheld`), because a link in a mail is not ours to
open. The model is given the user's own prompt, then the mail between
`<<untrusted-mail>>` markers: sender, subject and body, cut at 4,000
characters after `FilterMail` replaced verification codes and sign-in,
activation, reset and other credential-carrying links with placeholders. It
answers with a reply draft, optionally ending with `STATUS PROPOSAL: <status>
- <reason>`; only Responded, Interview, Offer and Rejected are kept. Goal
proposals come from `propose_goal_change` as usual.

**Results** are inert: the draft and the status proposal are stored on the
firing row and shown on the prompt (`firings` in `GET /api/agent/prompts`); the
proposal also raises a needs-you item; a `trigger-result` announcement (a
`prompt-result`, never held, for a cadence prompt) links to
`/ask?conversation=<id>`. Nothing is sent, submitted or written to the tracker.
`TestSignalTriggers_FiringPathImportsNoSubmitOrFormFillingEntryPoint` keeps
the package's import graph to an allowlist.

Gap: the timers run on each tick, not on a Gmail sync callback (as the alert
timer does).

## Research

`research_question` (`tools_research.go`, service in `internal/research`) is a
`draft` tool that answers directly and taints the turn (`OutsideContent`).
Arguments: `question`, optional `company`, optional `job_url` (context only,
never fetched). One `research-search` call, no sync workflow. Its description
sends a search for job postings to `list_pipeline` and `scan_portal` (whose
description says to look there first): live, "find me new jobs" went to the web,
got aggregator copies with unverified dates, and the taint held the scan the
task tried next.

- Source choice, per call: the route's provider has native web search
  (`llm.Router.NativeSearch`; OpenRouter's `web` plugin, sent as
  `plugins:[{"id":"web","engine":"exa","max_results":5}]` on an unstreamed call, citations read from the reply's
  `annotations`) -> the configured Exa key (`signals.Service.SearchProvider`,
  the key of the `exa` discovery source) -> neither: a result naming
  OpenRouter under Settings, Models and `applysmith secrets set api-key-exa` (the
  Exa key has no dashboard control), and no model call.
- Untrusted data: the system prompt says results are third-party text; on the
  Exa path each page is a `<result n= title= url=>` block with the closing tag
  defanged, capped at 3,000 characters.
- Enforcement is code (`research.finish`): the reply must cite, a number must
  name a retrieved source, and every line of five words or more needs a
  citation. On the provider path the model links by domain; links whose URL is
  among the returned citations become `[n]`, any other link is reduced to its
  text. A refused reply is not shown; the result lists the pages found. `NO_ANSWER` says
  the sources do not answer.
- Time: the whole question (search and model call) has its own 45 s deadline
  (`research.Deadline`), inside the 50 s tool limit (20 s cut the codex
  route's calls); the model call gets the time
  left. When it runs out, the result is "Not answered: the web search took too
  long (over 45s)", names the limitation, lists any pages the search had already
  returned and claims nothing; the caller's own cancel is not reported as one.
  A search source that fails or refuses (a rejected Exa key says so and how to
  fix it, the key itself is never in the text) and a failed model call are named
  the same way, with `applysmith llm test` for the second; none of these is a bare
  tool failure.
- The envelope: each source is a citation with the page title as `label` and the
  address as a separate `url` (http or https only; `wire.ChatCitation`). The
  call-local `[n]` number stays in the tool text for the model and is not part of
  the citation, so two research calls of one turn do not collide; the turn keeps
  a page once, by address. The disclosure gate reads the `url` like every other
  public field. The dashboard (`Transcript.tsx`, `Sources`) shows the title as a
  link that opens in a new tab with `rel="noopener noreferrer"`, the address next
  to it, and the limitation line; a stored address that is not http or https is
  shown as text.
- Cost: a call that asks the provider for a search adds the search fee (USD
  0.007 per request with up to ten results, USD 0.001 per further result) to the ledger row (`llm.withSearch`), under the task `research-search`,
  so the Usage page by-task report and the daily cap count it.
- Not wired to this source, on purpose or still open: `company-explainer` and
  company briefs read only the declared site and are granted no tools (spec
  "Company context from the declared website"); the answer bank's live-fact lane
  (FBB-031/032) uses this same service, see [`answer-bank.md`](answer-bank.md).

Tests: `internal/research` (deadline, search failure, citation rules),
`internal/agent/parity_tools_test.go` (`TestAgent_Tool_ResearchQuestion*`:
citations with links, two calls, slow search, failures), `internal/api`
(`TestResearchQuestion_Exa*`: a fake Exa server through the real signals client,
the adapter, the service and the tool), `web/src/pages/AskAgent.test.tsx`
(sources as links, the activity label while it runs).

Live check (2026-10-13, demo workspace, pi 1.0.4, OpenRouter route): a
dashboard-session turn asking for the latest stable Go release called
`research_question` twice and ended with the answer, three cited sources (the Go
release history and downloads pages) and the limitation line. The same turn on a
build without the tool answered from four `read_page` calls instead. A second
turn on this build (final event carrying `citations:[{"label":"Release History
- The Go Programming Language","url":"https://go.dev/doc/devel/release"}]`)
rendered in a headless Chrome on a throwaway profile: the title as a link to the
address, the address under it, the limit line, and the activity row
`research_question draft Done`. The conversation link is `/ask?conversation=<id>`
(the parameter is not named `session`, which the server refuses in any URL as a
credential); a hard load of it serves the dashboard.

A conversation is titled from its first line, collapsed to one line and clipped
at `sessions.MaxTitle` characters with a trailing ellipsis (`clipRunes`). Text a
person reads (titles, card summaries, tool-result summaries, error lines) is
clipped that way; only text the model reads keeps the `[truncated: N more
characters]` marker (`cutRunes`, `cutLines`). The list rail clips a long title
with CSS and keeps the whole title as a tooltip.

Every tool of the catalog is now registered; `Registry.Missing` is empty with
all services wired.

A tool whose service is nil is left out of the registry, so a build without it
offers no tool that cannot run (`read_page` without `Pages`, the run tools
without their services).

## Run and write tools

Each tool sits in the class the specification gives it, so the guard chain
decides it like any other call: the channel envelope, the approval
requirement, the tier, the idempotency claim. A refusal the model can act on
(a full queue, a scan already running, an unknown id) is the tool's answer, not
an error (`ErrRefused`).

| Tool | Class and rules | Service |
|---|---|---|
| `evaluate_job` | external-action, outside content (taints the turn), tier 3. Its own run-start line, a refusal and the cap's queued line are `Clean`, as for `scan_portal`: only a run's later output carries outside text. Needs a public http(s) URL or posting text. Returns the run id inside 5 seconds. A standing approval may cover its host. | `Server` pool, `evaluate.Pool.SubmitKeyed` |
| `scan_portal` | external-action, outside content, tier 3. `source` is an enum of the discovery catalog. One scan per source runs at a time. | `Server.launchScan` |
| `prepare_registration` | external-action, requires approval, idempotent, never eligible for a standing approval. Plan only: classifies the destination, asks the account creation gate whether a run the user starts now could sign up, and says whether a sign-in is stored. It creates nothing; an account is created only inside an application run the user starts, whether it submits or only fills (F-8). The card shows the exact URL. | `agentActions.PlanRegistration` |
| `tailor_resume` | write-local, tier 2. `application_id` is the tracker id; starts a document run (a pair with `include_cover_letter`). | `documents.Service.Submit` as the `agent` actor |
| `answer_bank_save` | write-local, requires approval: every call is held until the user approves the card with the exact call. The model passes the user's words only (question, answer, the form's options, a host to keep the answer to one site, an entry id `answer_bank_find` listed); `learn.Teach` decides the entry, wording, kind and site answer and writes nothing after which the matcher would not answer the question with that answer (user decision 2026-10-24: users do not edit wordings, ids, kinds or categories, which break matching). | `learn.Service` |
| `answer_bank_forget` | write-local, requires approval. `delete` (with `host`: that site's answer only), `ask_me` (the question is left to the user on every form) or `undo` (an entry deleted in the last hour, held in memory); `learn.Forget` refuses to delete a shipped default or a setup-derived entry, which come back, and offers `ask_me`. | `learn.Service` |
| `wiki_write` | write-local, requires approval, never eligible for a standing approval. Creates a page (`category`, `title`, `markdown`) or changes one (`id`, `expected_hash` from `wiki_read`, `markdown` and/or `title`); `facts` are added to `allowed_facts`. The kb service validates, scans for secrets and refuses a stale hash; injection-shaped text is refused; a call over 3,800 argument bytes is refused because the card shows the arguments cut at 4,000. Stories and company briefs are not written here. | `kb.Service` (`CreateSnippet`, `Revise`) |
| `batch_skip_list` | write-local, tier 2 (`list` is tier 0), never eligible for a standing approval. `list`, `add`, `remove` on `batch_skip_companies`; the edit keeps the profile's comments and key order, and a profile with no list of its own gets the built-in names written with the change. | `agentSkipList` over `workspace.Edit` |
| `tracker_set_status` | write-local, tier 2: the user's message states the change. Moves one row as the `agent` actor to `skipped`, back to `evaluated` or to `discarded` (owner decision 2026-11-12: the agent had said it could not record "I don't want Lio"); the tracker's transition table refuses the rest, and `applied` and the later statuses stay with the user, Gmail and the apply chain. An optional note goes to the row's notes, also when the row already has the status (once: a note already there is not added again). | `tracker.SetStatus`, `tracker.AddNote` |
| `record_story_answer` | write-local, tier 2. Records the user's own words as the answer to one open follow-up of a story draft; refuses injection-shaped text and secrets; never publishes. | `kb.Service.RecordAnswer` |
| `story_mining` | draft, tier 1. One tool, six actions: `suggest` (quadrants ranked by the empty grid cells a story from each could fill), `list`, `start` (a quadrant; optional pasted notes or a workspace notes file), `reply`, `save` and `abandon`. Everything the session records is the user's own: a `reply`, the `notes` and the name of a `notes_file` must be text of the turn's user message (whitespace and case aside), so the model cannot write the account for them; injection-shaped text and secrets are refused. A scheduled or digest turn may only `suggest` and `list`. `save` creates the story draft (idempotent per session), never publishes it, leaves its facts pending and returns the follow-up questions that `record_story_answer` answers. | `kb.Service` mining sessions |
| `propose_schedule_change` | draft. Stores `enabled` and/or a cadence for one schedule with a reason of at most 300 characters; a newer proposal for the same schedule replaces the pending one, and deleting the schedule removes it (`scheduler.Repo.Delete`), so the Undo of one dismissed before answers `not_found` and says the schedule was deleted. It changes nothing: the accept route applies it through the scheduler's own validation, and a change the scheduler refuses stays pending. No tool accepts or dismisses. | `schedprop.Store`; routes `GET /api/agent/schedule-proposals`, `POST .../{id}/accept`, `.../dismiss` and `.../restore`, the Undo of the last dismissal, which the store keeps in memory alone, so a restart forgets it (dashboard or CLI token, remote denied) |
| `draft_application_answer` | draft. Wraps the answer bank's composer (`assist.Service.Compose`), so the autofill lane and the agent refuse and abstain alike: identity, contact, work-authorization, self-identification, signature and salary values are never composed, and a question that states a fact about the application is refused before the composer runs. A refusal or an abstention is `NO_ANSWER: <reason>`, with a pointer to the answer bank for the sensitive classes. Citations are the source keys the composer reports. The stored `dangerous` grounding mode is clamped to `flex`. Trusted memory facts are not a composer source yet: they reach the model through the memory block. Without `posting_text`, a `company` (and `role`, matched by words) that names exactly one tracker row with a report gives the composer that posting: the report's posting view (`documents.Service.PostingOf`, the one tailored documents read) as the job description and the row's URL as `JobURL`, so the declared company site can supply company facts. The result starts "Posting text: from evaluation report N (...)" and taints the turn (`ToolResult.Outside`), because the posting's words are the employer's; no row or several rows means no posting, never a guess. Live (demo, 2026-10-17): before, the model wrote its own paraphrase of the posting into `posting_text`; after, the draft came from the stored posting and the company brief. | `agentActions.Draft` |
| `draft_outreach`, `draft_followup` | draft, outside content (the facts carry the posting's requirements and what the user relays about other people), tier 1. Wrap `messages.Service.Outreach` and `.Followup`: one `message-draft` call, then the deterministic checks of [evaluation.md](evaluation.md#message-drafts). A refusal (unknown kind, unknown row, used-up cadence, no message passing the checks, no route) comes back as `Not drafted: <reason>` with "do not write the draft yourself" and a limitation. Timeout 60 s (`draftTimeout`, also `draft_application_answer` and `story_mining`): on the codex route a busy `message-draft` call took 17 to 25 s and was cut at the old 25 s every time (ledger, 2026-10-19). `llm.Router.Structured` starts its one repair only when the deadline leaves at least the time the first reply took. Nothing is sent or stored; the next action tells the model to show the draft and its checklist. | `messages.Service` |
| `decode_posting` | draft, outside content, tier 1. `posting_text` of 200 to 30,000 characters; returns `Decoding.Markdown`. No score, report or tracker row. Timeout 90 s: `jd-decode` writes a reply the size of `deep-eval`'s (14 to 50 s). | `evaluate.Evaluator.Decode` |
| `application_plan` | draft, read only. System, tracker row, an earlier submission, the skip list, today's list, whether a sign-in is stored and the account gate, then the steps the chain would take. A sign-in stop is claimed only where `apply.AccountRequired` says the system needs an account (Workday); the public boards take the form signed out, and a generic site or LinkedIn shows it only on its page. It starts no browser action and has no path to the apply runner. | `agentActions.PlanApplication` |
| `application_workspace` | read, outside content. The tracker row and its last events, the report excerpt (2,500 characters), the documents and the submission record of one application. | tracker, report index, `documents.Service.List` |

## Disclosure gate and the answer envelope

Every final answer is an `Envelope`: the text, the citations, the limitations
and an optional next action. Tools add to it through `ToolResult.Citations`,
`Limitations` and `NextAction` (collected on the turn by `Turn.Collect`); the
final stream event carries them (`citations`, `limitations`, `nextAction`).
`ToolResult.RunID` is kept in the envelope column of the call's `tool_result`
row and sent as `runId` on that row and its `tool.result` event, so the
transcript links the run on the Runs page. The transcript shows a tool's
output through `ModelText` (`ui/Markdown.tsx`): output that parses as a JSON
object or array stays preformatted, anything else renders as Markdown.
`DisclosureGate.Check` reads every field of the envelope, in this order, and
the first match replaces the whole candidate with the fixed notice, audits only
the rule id and the category (gate `disclosure_gate`, verdict `deny`), and
stores nothing of the matched text:

1. `prompt-fragment` (system-prompt): verbatim passages of the prompt's rules,
   compared after lower-casing and collapsing white space.
   `protectedPromptFragments` is the list, and a test asserts that every entry
   still occurs in `systemPrompt`.
2. `tool-catalog` (system-prompt): six or more distinct catalog tool names
   (`SpecCatalog`) in one field, which a translated copy of the prompt or the
   tool list keeps; an answer naming two or three tools passes.
3. `framing-marker`: any `<<untrusted-` or `<</untrusted-` marker in any case
   or spacing, which covers the tool-result fence and the trigger mail fence.
4. `local-path`: a path under a protected root (the state directory and
   `StackConfig.ProtectedRoots`), a path with a `.applysmith` segment, and the
   names of key files, unless a tool result of the same turn reported that
   exact path (`Turn.ReportPaths`, kept per turn and never carried over).
5. `credential-shape`: the shapes of `credentialShapes` (shared with result
   redaction).
6. `vault-value`: any literal value of a stored secret.

A rule that panics is a match (category `rule-failure`). The gate runs on the
model loop's final text, on the text stored for each assistant step, on the
router's answer and on the fixed replies of a turn without a model. The stream
is gated too: a message's tokens leave only after the rules passed over the text
so far, with the word still being written held back, and once a rule matches
nothing more of that message is sent and the final event carries the notice.
The rules read the whole message, so they read it again only once it has grown
by 1/64 of its length since the last read (`streamReadShare`) and before the
rest is sent: a message of a few lines is read at every chunk, a long one
every sentence or so, and its tokens leave in steps of that size. Read at
every chunk, a 16,000-character answer cost 54 s of processor time.
Tool calls written as plain text in an answer are not executed and are gated
like any answer.

## The `pi:` login row

`pi:<pi provider id>` (for example `pi:openai-codex`) is a provider id that only
the primary of the `agent-kernel` row may use (`llm.PiLogin`; a fallback, another
task or a malformed id is refused, and `APPLYSMITH_MODEL_AGENT_KERNEL` accepts it for
that task only). The route has no vault key and no price, bills as
`subscription`, adds nothing to the day's spend (so a subscription turn is not
stopped for cost even when the day is already over the cap), and writes ledger
rows from pi's usage events with provider id `pi:<id>` and kind `pi`; pi calls
its provider itself, not through the gateway. The supervisor launches pi on its
own provider (`--provider <id>`), declares no provider in `models.json`, passes
no key variable, and before the turn asks `pi auth check --provider <id> --json`
(reused for a minute); no login ends the turn as `runtime_unavailable` with
the fix `applysmith agent login`, which runs pi on the terminal in the product's
agent directory so the user signs in with `/login`. The login stays in pi's own
files. Route it with `applysmith llm set agent-kernel pi:openai-codex/<model>`.

## Telegram ingress

Free text from the paired chat goes to `Agent.Send` on the `telegram` channel
(`telegramBackend.Ask`). The gateway keeps one conversation per chat
(`telegram_chats`) and starts a new one when the last message is more than 6
hours old (`telegram.ConversationIdle`); a conversation the user deleted in
the dashboard starts a new one at once instead of failing until the next
rollover. The answer sent back is the turn's last assistant message, which is
the gated text.

## Memory tab and agent switches

All routes are remote denied. Reads are standard; every change is
`SessionOnly`, because what is stored here reaches the model's prompt and
approving it is the one way it gets there.

| Route | Does |
|---|---|
| `GET /api/agent/memory/facts[?trust=]` | active facts of both trust levels, with the limits |
| `PATCH /api/agent/memory/facts/{id}` | edit: a new version, trusted because the user wrote it; an instruction-shaped text goes to the review list (`reviewId`), an injection-shaped one is `validation_failed` |
| `DELETE /api/agent/memory/facts/{id}` | forget, with the hash tombstone |
| `POST /api/agent/memory/facts/{id}/approve` | make a noticed fact trusted |
| `GET /api/agent/memory/rules`, `PATCH`, `DELETE`, `POST .../approve`, `POST .../retire` | list, edit (same screening as a proposal), delete, activate a proposal, take an active rule out |
| `GET /api/agent/memory/review`, `DELETE .../{id}` | the instruction-shaped statements that were not stored; dismiss |
| `GET /api/agent/memory/export` | facts of both trust levels, rules and the review list as one JSON document |
| `GET`, `PATCH /api/agent/settings` | `compaction`, `consolidation`, `selfImprove`, `tokenBudget` (4,000 to 200,000), which the compactor, the packer and the nightly passes read on every use |
| `GET /api/agent/harness`, `POST .../restore`, `POST .../round` | the self-improvement view (harness versions, rounds, lessons), restoring an earlier version, and a round now; see [ask-agent-improve.md](ask-agent-improve.md) |

The Ask Agent page has a Memory tab (facts with approve, edit, forget and
export; operating rules; the review list) and Settings has an Ask Agent section
with the three switches, the budget and the self-improvement history.

## Goals

A goal is data for the model and for check-ins. Nothing in the guard chain,
the gates package, the router or the confirmation service reads one, and a
test pins that: the same calls get the same decisions with no goals and with
goals whose steps say "Apply to Example Corp" or "submit everything". A step
that names an application, a scan or a message is only text for the user.

- **Store** (`goals.Store`, tables `agent_goals`, `agent_goal_steps`,
  `agent_goal_proposals`): title 120 characters, optional due date
  (`YYYY-MM-DD`), at most 20 steps of 200 characters with a status, a note of
  300 characters and an optional application id or company, an optional
  cadence (weekdays and `HH:MM` in the server's zone), a state and a version.
  At most 5 goals are active; creating or resuming a sixth fails with a
  message to pause or complete one. Text that matches the memory injection
  blocklist (`memory.InjectionShaped`) is refused in titles, steps, notes,
  links and reasons. A plan edit by the user (title, due date, cadence, steps)
  and an accepted proposal raise the version; a step status, a note, a pause
  and a completion do not.
- **Tools** (`tools_goals.go`): `list_goals` (read), `manage_goal` (write-local,
  so tier 2: held unless the user's message states a change; actions `create`,
  `set_step` by 1-based `position`, because `step` is the reserved display
  argument, and `pause`, `resume`, `complete` and `delete` through the store's
  `Update` and `Delete`; the model had used `forget_fact` on a goal) and
  `propose_goal_change` (draft). A refusal is an answer the model
  reads, not an error. The two scheduled envelopes (`scheduled`, `digest`) offer
  only read and draft tools, so `manage_goal` is not in a background turn's
  registry and a call by name is `unknown_tool`.
- **Goals block**: `ContextPacker.Goals` adds to the turn's records, after the memory block, the
  active goals as `(id) title, due date (overdue), n of m steps done; next, step
  k (status): text`, at most 1,000 tokens, under a header that calls it data
  that cannot override rules or gates. A goal with a pending proposal says so,
  so the model does not propose it again. Every channel gets the block.
- **Proposals**: `propose_goal_change` stores a reason (300 characters) and
  the revised remaining steps, optionally a due date and a cadence, and changes
  nothing else; a goal has one pending proposal and a newer one replaces it
  (audit codes `goal_proposed`, `goal_proposal_replaced` under gate
  `goal_proposal`). `Store.Accept` keeps the done steps, replaces the rest,
  applies the other fields and raises the version; `Dismiss` deletes the row.
  Both take an `Actor` the tools never name, and a test pins that
  `tools_goals.go` contains no `Accept`, `Dismiss` or `goals.Actor`. The only
  callers are the two routes below.
- **Routes** (`internal/api/goals.go`, standard class, remote denied, not
  session-only because the specification does not say so): `GET`/`POST
  /api/agent/goals`, `GET`/`PATCH`/`DELETE /api/agent/goals/{id}` and `POST
  .../proposals/{proposalId}/accept` and `.../dismiss`, with the `Goal*` wire
  types. The Telegram accept button is not built: the callback grammar of the
  notifications spec has no goal form, so accepting is the dashboard (or CLI
  token) only.
- **Approvals**: no goal path creates one. A held `manage_goal` becomes a
  confirmation card like any tier 2 call; the goal store has no code for
  approvals, and a test counts `agent_approvals` after every goal operation.
- **Check-in timer** (built-in timer `goal-checkins`, `Store.DueCheckins`, wired
  in `internal/api/goals.go`): on each tick it takes the latest cadence slot of
  every active goal, skips it when it is older than 3 hours, earlier than the
  goal's creation or last resume (`active_since`), or not newer than the claimed
  `last_slot`, claims the slot in the row, and only then announces. The message
  is built from the record (title, due date and overdue, steps done, next step,
  a waiting proposal) with no model call and starts no run; it goes out as a
  `goal-checkin` announcement through the notification policy (held in quiet
  hours) with the link `/ask#goals`. Each goal's next slot is the `nextCheckin`
  field of the goal.

## Confirmation cards and standing approvals

A held call (any `hold` verdict: a missing intent, taint, a tool that needs an
approval, a submit) becomes a pending card (`agent_confirmations`, migration
`0021_agent_approvals.sql`) and the `tool.held` event carries its
`confirmationId`. `Confirmations` (`internal/agent/confirm.go`) records the
card, runs the call when it is approved and answers the guard chain's `Grants`
question; `internal/agent/approvals` owns the tables and every rule about who
may approve what.

- **No card for a refusal**: before a hold becomes a card, the bridge asks
  the tool's `Conflict`, a check that writes nothing (`refusedBeforeHold`).
  A call it would refuse now gets the refusal as its result, makes no card,
  and is audited `tool_refused` under the hold's gate; the turn goes on, so
  the model can ask the user. Examples: an answer outside the question's
  choices, an answer the bank already holds, a shipped entry to delete
  (`answer_bank_save`, `answer_bank_forget` through `learn`'s `DryRun`), a
  scan of a source already scanning. Live, 2026-11-02: "Yes, within the EU"
  for a Yes/No question was held, approved, and only then refused.
- **Card**: tool, one-line summary, host (and company and role when the posting
  is on today's apply list), tier, the hold's code and reason, the arguments
  after redaction, and the purpose: the turn's user message, or the label of the
  button, cut to 500 characters with the ellipsis counted inside the 500. The
  unredacted arguments stay on the server for the one re-run. A repeated hold
  of the same call in a conversation returns the waiting card. A card expires 12
  hours after creation (swept each minute and on every read) and counts as
  declined with an audit row `hold_expired`; cards pending at a start are
  expired by `Agent.Start`. Each expiry, approval and decline puts a system
  note in the conversation; the model never reads notes. Instead
  `ContextPacker.Cards` adds to the turn's records the conversation's 10 newest
  cards with their state (waiting, approved, declined, expired; a pending
  card past its expiry reads as expired), so the next turn does not claim a
  declined card still waits.
- **Approve** runs the call once through the whole chain with a `Confirmation`
  (`Guard.DecideConfirmed`): the confirmation flag lifts the hold of the
  approval requirement, of the tier 2 or 3 confirmation step and, for a dashboard
  session only, of the submit gate; the policy floor, the channel envelope, a
  submit gate refusal and the idempotency claim still decide. The re-run keeps
  the card's channel and origin.
- **Who**: the dashboard session may approve for `once`, `conversation` or
  `today`; a local token and a remote device may approve once, and neither may
  confirm a tier 4 call; the Telegram owner may approve once, only a card of the
  Telegram channel below tier 4. `approvals.Store.Approve` enforces this, so no
  caller can widen it. Routes: `approve-standing` is `SessionOnly` and
  `RemoteDenied`; `approve` and `DELETE /api/agent/approvals/{id}` are remote
  allowed and served on the tunnel listener (the handlers treat a remote
  principal as once-only).
- **Eligible tools** are the Go table `approvals.EligibleTools`: `save_fact`,
  `amend_fact`, `record_story_answer` (the tool), `tailor_resume`
  (`application_id`), `evaluate_job` (host of `url`), `scan_portal` (`source`),
  `autofill_application` with `submit` false (the exact `url`). The argument
  names of the tools not built yet are fixed here; their owners match them. A
  card gets a target, and so offers wider scopes, only when the hold was for
  missing intent or taint; a tier 4 (submit) card offers `once` alone, and the
  store refuses a wider scope for it even with a target.
- **Lifetime**: the earliest of the scope's end and 24 hours; `today` ends at the
  next midnight of the server's zone, `conversation` is a foreign key to the
  conversation, so deleting it ends the approval and a fork is not covered. At
  most 20 are active. Every creation, use (`grant:<id>`) and revocation is a
  `gate_decisions` row under gate `standing_approval`; card decisions use gate
  `confirmation_card`.
- **Dashboard** (`web/src/pages/askagent/Cards.tsx`): the oldest card of the
  open conversation asks in place of its composer (`RequestPanel`, "1 of N"
  when more wait), which stays mounted and hidden so a draft survives. The
  title is a yes/no question naming the tool, the call and its redacted
  arguments sit in a code block, and the answers run from the narrowest scope
  to the widest: Yes, once; Yes, for the rest of this conversation; Yes, for
  today; No; then a field "No, and tell the agent what to do differently",
  which declines and then sends the text through the conversation's own send
  (`steer` while a turn runs, else a new message), so it needs no route of its
  own. Keys: 1 to 9 choose (the number after the last answer goes to the
  field), Up and Down move, Enter chooses the highlighted answer, Esc
  minimises without deciding; IME composition and modifier keys are ignored.
  A submit card (tier 4) offers only "Yes, review the submit" and No, and
  whatever presses Yes (a click, Enter or its number) only opens the same
  confirmation dialog the page-level card uses, which opens on Cancel: no key
  press approves a submit. The panel takes the keyboard when a card arrives
  only if the user was not typing in the composer or another field, so a card
  that appears mid-sentence cannot be answered by the next key. Cards of other
  conversations stay in "Waiting for your decision" above the tabs, each with
  "Open its conversation"; with a conversation open and none waiting
  elsewhere, that panel is not drawn.
- **Telegram**: a card of a Telegram turn is sent with Approve once and Decline
  (`telegram.Gateway.SendApprovalCard`); a tier 4 card is sent without buttons.
  The `approve:` and `decline:` callbacks call `Confirmations` with the Telegram
  actor.

## Background runs, Done and the cap

- **Origins**: `internal/agent/origins` ties a run to its conversation
  (`agent_run_origins`) and, for an application run, keeps the request it was
  started from (`agent_apply_requests`: caller, tool, submit, explicit request,
  whether outside content was read, whether the call ran on an approved card, the
  user's message, the posting). The evaluate, scan and `tailor_resume` actions
  record it when they start a run; the apply stack records it for every row of a
  list. A single `autofill_application` posting that is on today's apply list
  carries its company and role into the run, so the board names the job; any
  other takes the call's optional `company` and `role`. They also name the
  employer behind a job-board repost, whose own posting the run resolves to
  (live: tracker #2084's lnkd.in link led to a Hirecept repost, and the call
  had no way to say it was Abnormal Security's).
  `GET /api/runs/{id}` names the conversation (`RunView.Conversation`), and
  the run's drawer opens it ("Asked in").
- **Completion message**: see `docs/architecture/notify.md`, "Done resume".
  No gate reads a note, so `completionNotes.note` puts it through
  `agent.ScrubNote`: a protected path or one under the home directory becomes
  "a local path", and a process id is dropped.
- **Cap**: `agent.RunGate` wraps `evaluate_job`, `scan_portal`,
  `autofill_application` and `apply_from_list`. With two runs of conversations
  still going (`origins.Active`: a working board entry or a queued or running run
  record) a start is queued: the model is told so, the start is stored with its
  conversation (`agent_queued_starts`, `origins.Queue`), the Runs page shows a
  working entry titled "Queued: ..." that is touched until the start runs and
  then removed with the row, and the call goes ahead on its own when a slot
  frees (polled every 2 s, given up after 30 minutes or when the server stops).
  The count and the start that follows it are one step under a lock, so two
  starts that find the last slot together take it one at a time.
  The guard chain decided the call when it was made; only its start waits.
  Before it queues, the gate asks the tool's `Conflict` (for `scan_portal`,
  `Scans.ScanConflict`: a scan of that source already running) and answers a
  refusal at once, and a start with the same tool and arguments as one already
  waiting is not queued twice. A waiting start that is refused when it runs,
  fails, or gives up is told to its conversation as a system note
  (`RunGate.Tell`, `completionNotes.told`). One the server stops keeps its row
  and line: at the next start `RunGate.Settle` (after `Agent.Start`) tells its
  conversation "The queued start (...) did not begin: the server stopped ...;
  ask again to start it" and removes both; it is never replayed, because its
  turn is over. Before, a stop dropped it silently although the model had said
  the user would be told
  ([exec plan](../exec-plans/active/2026-11-02-agent-architecture-dsh.md), item 2).
- **Resume**: `apply.Runner.Resume` takes the kept tab back (`LaneHandle.Reclaim`,
  the inverse of `Keep`, reachable only from a Done), reads the page again and
  hands the page summary to the conversation (outside content), then walks on
  from that page. It does not sign in again: a sign-in wall still standing stops
  the run as `sign-in`. A control whose value differs from the hash taken when
  the run handed the tab over (`apply_handoffs`, salted, no value) is the user's
  and is neither answered over nor written by the engine's fill. The
  submit gate decides again: `agent.SubmitAgain` re-runs the agent submit gate
  from the stored request (a call that ran on an approved card stays allowed), and
  the runner's own submit authorization runs as for any run. Anything but an allow
  ends the run `filled` with the entry in `needs-you` / `confirmation`, tab kept.
  That hold is settled by "Submit this form" or by the conversation's own
  confirmed call for the posting (AC-6, `api` `heldForm`), which marks the
  stored request confirmed.

## Delegated tasks

A delegated task is an outcome the user handed the agent in one dashboard
conversation ("apply to the strong matches on my list and finish the ones that
stop"). The agent carries it over several turns of that conversation instead of
stopping when its turn ends (owner decisions DT-1 to DT-3; the idea comes from
CopilotKit OpenMuse, `docs/provenance.md#CR-AGENT-TASKS`).

- **Store** (`tasks.Store`, table `agent_tasks`): title, the mandate (the
  user's own message the task was started in, never text the model wrote), a
  plan of at most 12 steps (pending, doing, done, skipped, each with a note),
  the state, the question it waits on, the outcome, the turns it took by
  itself and how many of them in a row moved nothing. One open task per
  conversation, at most 3 open. Model-written text passes the memory injection
  screen.
- **States**: `working` (its next turn is due), `waiting_runs` (runs the
  conversation started are still going, their ends are not told yet, the
  evaluations its scans queued are still working (the scan's board batch: a
  scan ends with them queued), or starts it queued have not begun:
  `origins.ActiveIn`), `waiting_you` (a
  question, or a confirmation card of the conversation with no question),
  `paused`, `done`, `stopped`.
- **Tools** (`tools_tasks.go`, dashboard only): `delegate_task` (write-local;
  held unless the user's words name an action, a change, a finish or a hand-over
  such as "handle", "until", "keep", "follow up"), and the bookkeeping
  `task_update`, `task_ask` (ends the turn's calls) and `task_done` (drafts:
  they change only the task's own record).
- **Authority** (DT-2): in a turn of the task's conversation the mandate counts
  among the user's words for intent (`Turn.intentTexts`), and the task's turns
  may work the runs the conversation started (`Turn.TaskRuns`: the guard's
  `operate` answers `task_run`, as the write-local tier does for `apply_leave`
  on such a run, and `runBound` lets the apply tools through).
  The mandate is never an explicit request to submit, and a turn the task took
  by itself (`Turn.Continues`) never asks to submit, so its runs stop at Submit
  for the user (AC-6's "Submit this form"). A "do not submit" in the mandate
  binds every turn. Every other gate is unchanged: a turn that read outside
  content still holds a new external action on a card, which "Yes, for the rest
  of this conversation" covers.
- **Turn end** (`Agent.afterTurn`, `settleTask`): a task the turn finished or
  made ask stays so, and the user is told (announcement kind `task-update`,
  link `/ask?conversation=<id>`) when it was the task's own turn. Otherwise a
  failed turn makes it ask with the reason, a cancelled turn pauses it, and a
  stopped server leaves it working for the next start. A working task then
  waits for its runs, or for its cards, or works on; its second own turn in a
  row that moved nothing (no step changed, no run going) makes it ask.
- **Its own turns** (`taskLoop`, every 15 s and when `completionNotes` stored a
  run's end, `Agent.WakeTasks`): a due task whose conversation has no turn
  running starts a turn whose user message is the mandate, given the
  conversation notes since its last turn (the ends of its runs, fenced as
  untrusted; a note that is outside content taints the turn). After 20 such
  turns it asks before taking another; Resume gives it 20 more. A user message
  in the conversation answers its question and binds that turn to the task.
- **Panel** (`Task.tsx`, routes `GET /api/agent/tasks`, `POST
  /api/agent/tasks/{id}/pause|resume|stop`, remote denied): above the
  conversation, the plan, the state and the controls. Stop also ends the turn
  the task is taking; its runs go on. The panel reads the task every 5 s while it
  is open and reloads the conversation when it moved.
- **Ideas** ("Let the agent do it", `agentIdeas` in `internal/api/agent_tasks.go`,
  `POST /api/agent/ideas/{idea}`, dashboard session only): three Needs you items
  on Home hand their work over in one press: `evaluate_inbox` (the press
  itself promotes the up to 5 newest postings the inbox drain would still
  take (pending and not failed twice: one gone from its board fails every
  time) behind the inbox's own gate and ties each evaluation to the new conversation, because the agent's
  second `evaluate_job` in a turn would wait on a card once the first one's
  result tainted it; the task starts in `waiting_runs` and judges them once
  their ends are told),
  `apply_ready` (up to 5 postings from the apply list, filled and answered from
  the Answer Bank and profile, with "do not submit any of them", which the
  submit gate reads) and `draft_followups` (the up to 5 follow-ups due, by
  tracker id, drafted and never sent). The server writes the mandate from what
  waits now, with counts and tracker ids alone and never a posting's or an
  email's text, opens a conversation titled after the idea, creates the task
  with its `idea` and fixed steps (`WaitsForRuns` when the press started
  runs), and wakes the loop, so the first turn is the task's own. A press is
  refused before it does anything while 3 tasks are open, and while the agent
  has no model (`agentModelMissing`: `AgentRoute(agent-kernel)` is offline or
  unrouted, 503 `no_model_route` with the turn's own words), as is "Let the
  agent finish"; Home then offers neither button (`noAgentModel`). An idea holds one open task (`agent_tasks_open_idea`): a
  second press answers it, and Needs you shows "The agent is on it" instead of
  the press. Nothing waiting answers 409. Needs you also lists an open task's
  question ("A task the agent works on asks you").

## Desktop tools and "Let the agent finish"

`internal/agent/tools_desktop.go` and `tools_finish.go` hold the computer-use
tools (AC-2 to AC-5). The six desktop tools are built when `ToolDeps` has
`Desktop`, `DesktopPause`, `DesktopActivity` and `Sessions`; `run_questions` and
`answer_run_question` when it has `StoppedRuns`, which `applyapi.agentRuns` serves
from the records, lookups and save of the run's questions form. `Channel.offers`
keeps all eight to the dashboard and `Turn.permits` from remote turns. Each turn
keeps its latest look per window (`lookCache`): an action binds to it once,
within `computeruse.LookLifetime`, while the window keeps its rectangle
(`Rect.SameFrame`: the one-point border that comes with the keyboard focus is
not a move), finds
its control by index or `Look.ElementAt`, reads the Pause, and asks
`gates.DesktopAction`, whose `DesktopTarget` admits only the managed browser
(`ManagedBrowserPID`) or an app `gates.NamesApp` finds in the user's own message
(never the button's). No look reads which control holds the keyboard focus,
so the cache also keeps, per window, the role of the control the turn's own
click or typing last put it in (`lookCache.acted`; a Tab, Shift+Tab or Escape,
or an action that failed, makes it unknown), and the gate presses Space,
Backspace or Delete in a named app only while that role is a text field
(`DesktopActionInput.FocusRole`). The tool writes its `desktop_action` audit row before
anything reaches the service and records an Agent control event (a look's PNG,
never typed text). A step that ends before the gate is recorded too
(`desktop.step`): a list of windows, a window that is gone, a stale or used
look. An action's event names its look's screenshot with the control's place
in it (`Look.Fraction`), and `stepDetail` adds the key, in the gate's spelling
and only once the gate allowed it, or a scroll's direction and pages. In the guard, `operate` lets the five actions through a
tainted turn when the user asked: an operate verb, the button, or, for an
answer, their own words; a desktop action not asked for is denied, not held,
because its look would be stale by the time a card is approved.
`POST /api/apply/runs/{id}/takeover` (Home, the run drawer, or the note about
the run in the conversation that started it: a run's note names its run,
`Envelope.RunID`) sends `agent.FinishMessage` with
`SendRequest.FinishRun`: the turn's origin is the button, it is offered only
read, draft and computer-use tools, and its run tools act on that run alone.
Bound to one run, it may make 80 tool calls (`MaxRunToolCalls`) in 30 minutes
(`RunTurnLimit`), where another turn makes 12, or 40 after a stepwise tool, in
10: the apply skill's steps take up to six calls a page (read, fill, evidence,
answer, review, press).
`run_questions` also names the required fields its questions do not ask
(`StoppedRuns.InTab`: a value the site refused, a password, a signature, an
upload), for the user to complete in the kept tab before the run goes on; live,
Grafana's LinkedIn box refused the demo persona's link and the agent asked about
the three other fields as if they were all.
`answer_run_question` saves only an answer the lookups gave or the user typed
(`inOwnWords`: whole words, ignoring case and punctuation, with `'m`, `'re`,
`'ve` and `'ll` written out, so "I'm a human being" picks the option "I am a
human being"; live, it was held for a card) or the user approved on the call's
card, which shows it (`Turn.Confirmed`; live, the approved card's re-run was
refused as unsourced, so a held answer could never be saved),
and the last one continues the run as a press by `DoneFromAgent`, which holds
its Submit as a `confirmation` entry.

### The apply tools

Step 1 of `openspec/changes/agent-drives-apply` (the agent drives an
application page by page). `internal/agent/tools_applyrun.go` builds seven tools
when `ToolDeps.ApplyRun` is set (`api.agentApply` serves it): `apply_page` and
`apply_review` (read), `apply_fill`, `apply_answer`, `apply_press` and
`apply_continue` (external-action), `apply_leave` (write-local). They are
dashboard-only like the desktop tools (`dashboardOnly`), so Telegram, schedules,
digests and remote turns are not offered them, and a "Let the agent finish" turn
may use them. Each is bound to one run (`runBound`): the run the button's turn
finishes, or a run id the user's own words name as a whole word. The four that
change the page or carry the run on are `operates`: the button, an operate verb
or a finish verb (`finishVerbs`) beside the run id asks for them; a question
about the run does not, and a tainted turn does not hold them.

Every call takes the apply lane (`applyStack.takeLane`), takes the kept tab over
(`apply.Runner.Operate`), works it and keeps it again. The lane then stays lent
to the run (`applyStack.lend`) for a minute after the call (`lendIdle`), so no
start, queued start, other run or fill takes the window between two calls of
the turn; `apply_continue` and `apply_leave` give it back at once (`unlend`),
and a queued start begins then. What the gates decide is
the chain's own code; see "Run operations" in `apply.md`. The run stays where
the board has it, waiting for the user. `apply_continue` is the Done press by
`DoneFromAgent` (`goOn`): the run walks on from the page, checks it and stops at
Submit as a `confirmation` entry. It never submits, and the name `apply_submit`
stays on the registry's list of forbidden names. `apply_leave` writes the
agent's reason on the board entry.

The `apply_answer` schema nests an object per row, so `validateArgs` checks a
nested object, and refuses an unlisted key in one that says
`additionalProperties:false`. A row with `change` replaces a value the field
already holds; `userChanges` lets it through only when the user's own typed
words of the turn (`Turn.typedText`, pasted text and a button's message aside)
hold the value word for word, case and spacing aside. The apply side still
refuses a field left to the user ("Run operations" in `apply.md`).

## Skills

A skill is a file of `internal/agent/skills/` (`<name>.md`: front matter with
`name` and `tools`, then an instruction block of at most 60 lines), embedded in
the binary. `checkSkill` holds it to the harness's spirit: every tool is in
`SpecCatalog`, no line fails `memory.ScreenRule` (hard injection, wording that
widens permissions, at most 300 characters), none imitates the fence of a tool
result. A skill adds instructions and narrows the tools; the guard chain reads
the tool and the turn only, so a skill cannot change a class or a gate.

A turn gets a skill two ways. A turn that starts under one (the "Let the agent
finish" turn starts under `apply`, set in `Agent.Send`) carries the block in its
system prompt, after the fixed rules and before the harness's guidance
(`Bridge.TurnContext`), and the turn context names the active tools
(`AgentTurnContext.ActiveTools`: the skill's tools that the turn is offered),
which the extension sets at `before_agent_start`. A turn that calls `load_skill`
(dashboard-only, read) gets the same tool narrowing in the result
(`AgentToolResult.ActiveTools`, `pi.setActiveTools`, as `finalOnly` does) and the
block in `AgentToolResult.Skill`; pi's system prompt cannot change mid-turn, so
the extension adds the block as a user-role message ahead of the turn's own
messages in every later `context` event, and clears it at the next
`before_agent_start`. A turn that is offered none of the skill's tools loads
nothing. The `apply` skill is the page-by-page procedure and the list of what
the agent never does; its text is the file. The `answer-bank` skill is how the
agent keeps the bank from what the user says: read first, write the question as
a form asks it, the user's own answer only, one save per question, and where
the profile or a setup step owns the answer instead. The `wiki` skill is how the
agent keeps the wiki: find the page first, merge into it instead of starting a
near-duplicate, write only what the user said, link related pages, and leave a
single fixed form answer to the answer bank.

## Persisted answers and schedule suggestions

- Runs started by a tool report completion through the background-run path
  described above, including the cap of two and Done.
- Citations, limitations and next action are included in the final stream
  event and persisted with the turn's last assistant message by
  `sessions.KeepEnvelope`. Session-detail responses map the stored envelope;
  `web/src/pages/askagent/Transcript.tsx` renders it after a conversation is
  reopened as well as while it streams. A legacy message with no stored
  envelope has no extra material to display.
- Schedule proposals have list, accept and dismiss routes and appear under
  Discovery → Schedules (`web/src/pages/discovery/Proposals.tsx`). Accept applies
  the proposed change through the scheduler; dismiss leaves the schedule alone.
  A refused change stays visible with its error.


## Environment

- `APPLYSMITH_PI_PATH`: the pi executable (else `pi` on `PATH`).
- The agent directory is `<workspace>/.applysmith/agent`: `models.json`,
  `extension/applysmith.mjs`, an empty `cwd/`. The audit salt is
  `<workspace>/.applysmith/audit-salt` (mode 0600).

## Tests

`go test ./internal/agent/...` replays the recorded transcripts against a
scripted process; the time-dependent tests run on `testing/synctest`, so the
2-second cancel bound, the 1.5 second abort wait, the 120 second stall, the
10 minute clock and the restart backoff are asserted on the fake clock. The
extension is run under `node` when it is installed (the test skips otherwise).
`go test ./internal/api/...` runs a turn over HTTP against the real routes.
