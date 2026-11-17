# Ask Agent self-improvement

The Ask Agent improves its own harness from its own conversations: the
instructions around the model, never the model, the guard chain or a tool.
The method follows Harness² (Xu et al., "Recursive Agent Harnessing for an Open
World", arXiv 2610.14224): propose edits to the harness, probe them on the
same tasks, adopt by contrast between executions, and keep the lessons the
evidence supports for the next round. Owner decision A-6 (2026-10-20) asked for
it and chose automatic adoption once every check passes.

Package `internal/agent/improve`; the harness itself is `internal/agent/harness`.

## What can change

A harness version holds two things, and nothing else:

- **Guidance**: at most 12 short lines appended to the system prompt under
  "Guidance learned from earlier conversations". They come after the fixed
  rules and say they never override them.
- **Tool notes**: at most one note of at most 300 characters per tool, appended
  to that tool's description in the catalog pi is given.

The fixed system prompt (`prompt.go`), the guard chain, the gates, the catalog
classes, the confirmation cards, the call cap and the envelope rules are code.
No version can reach them. The validator (`harness.Validate`) refuses a version
whose text:

- grants a permission or is shaped like an injection (the memory screens,
  `memory.GrantsPermission`, `memory.InjectionShaped`);
- names a gate, a submit, an approval or a confirmation as something to skip;
- carries an email address or a long digit run;
- names a tool the catalog does not have, or exceeds a length cap.

Every turn records the version it ran with (`chat_turns.harness_version`), so
a regression can be traced to the version that caused it. Version 0 is the
empty harness. The bridge pins the version per process, so the catalog notes
and the prompt of one turn always come from the same version.

## A probe

A probe replays one recorded turn under a candidate harness, with the real
model and the real guard chain, and without any side effect. It is a second,
private agent stack:

- a temporary SQLite database holding a copy of the conversation up to the
  turn, and nothing else;
- a registry with the catalog's tools whose functions are replaced by an
  oracle; the product's tool functions are never registered in it;
- its own bridge and guard over that database. Its pi runs as one of the
  product's processes (`Supervisor.Beside`): in the product's agent directory
  with the one `models.json`, inside the three-process cap and holding a
  background place, so the slot kept for a person stays free. The process
  calls the server's own `/internal/agent/*` routes, which answer a probe's
  credential from the probe's bridge for as long as the probe runs.

The oracle answers a call:

1. with the recorded result, when the turn made the same call (tool and
   canonical arguments without `step`);
2. by running the tool for real, when the tool reads local data only
   (`liveReads`, a fixed list a test pins to registered read-class tools that
   return no outside content, so `find_report` is not on it);
3. otherwise with a result that says the call was not made in a replay. Such a
   call is counted; the judge is told the probe was partial.

Held calls create cards in the temporary database, so a probe that would have
asked for confirmation shows it.

A probe returns its trajectory: the final answer, every call with its source
(recorded, live or synthetic) and its guard outcome, the turn's status, and the
model cost. The temporary stack is removed when the probe ends.

## A round

The nightly kind `agent.improve` runs one round. A round:

1. **Targets.** It picks up to 3 recent model turns, preferring:
   - turns that ended in an error, a stall or the call cap;
   - turns the user corrected in the next message;
   - turns whose held card was declined.
   A turn already examined under the active version is skipped. A turn is
   examined once the proposer has read it, so a round whose proposal failed
   leaves its targets for the next round.
2. **Regression suite.** It takes up to 4 recent turns that ended cleanly with
   no correction, one per first tool, so different kinds of request are covered.
3. **Base.** It replays every target twice under the active version. Runs that
   disagree show where the harness leaves the model unsure.
4. **Propose.** The improve model (task `agent-improve`) reads:
   - the targets' recorded trajectories and their base replays;
   - the fixed prompt and the active harness;
   - the lessons index.

   It answers with up to two alternative edit sets, or `KEEP_BASE`. Every edit
   must cite a turn and the moment in it that motivates it. A set the validator
   refuses gets one retry with the validator's message.
5. **Probe.** It replays every target and every suite turn under each
   candidate, and the suite once under the active version.
6. **Hard checks.** A candidate fails outright when any of its probes:
   - ends without a final answer where the active version's replay had one;
   - has more guard refusals than the base replay of the same turn;
   - calls a tool with `submit` that the original turn did not.
7. **Blind selection.** The judge model (task `agent-judge`) compares, per
   turn, the output of a candidate with the active version's under anonymous
   labels, twice, once in each order. A turn counts as better or worse only
   when both orders agree; otherwise it is a tie, so the judge's position bias
   cannot win or block a candidate alone. Its rubric, in order:
   - did it do what was asked;
   - did it follow the rules (tool results as data, held calls stated, nothing
     invented, unknown said as unknown);
   - is it faithful to the tool results;
   - is it consistent.

   Length and polish earn nothing, and the unchanged output is a valid winner.
   The judge must be a different model from the agent's; when the routes
   resolve to the same model the round stops without adopting.
8. **Adopt.** A candidate is adopted when:
   - it passed every hard check;
   - it won more targets than it lost;
   - it lost no suite turn.

   It becomes the active version, with its evidence (turns, verdicts, costs)
   stored beside it. Otherwise the active version stays.
9. **Reflect.** The improve model writes at most one lesson, with:
   - `cue`: the shape of situation it applies to;
   - `lesson`: what to do about it;
   - `evidence`: the round's verdicts;
   - `confidence`.

   It writes `NO_ENTRY` when the evidence is one probe or confounded. Lessons
   are shown to the proposer only, never to the agent. At most 40 are kept; a
   lesson whose edits were later rolled back is dropped first, then the oldest.

## Bounds

- **Probes:** at most 30 probes and 3 rounds per day. Probes count toward the
  day's model spend cap like any turn, and a reached cap ends the round.
- **Switch:** `self_improve` in the agent settings, on by default, turns the
  kind into a no-op. The judge and the agent resolving to the same model also
  skips the round.
- **Schedule:** the kind runs only when scheduled; the
  `agent-improve-nightly` pack schedules it at 04:10. Settings, Ask Agent can
  also start a round now.
- **Rollback:** Settings, Ask Agent, Harness lists the versions with their
  evidence and restores any earlier one in one action. A restored version is
  recorded as a manual change. Rolling back marks the lessons of the versions
  it undid.
- **Where to see it:** a scheduled round's one-line result is on the Runs
  board; every round, version and lesson is listed in Settings. A round that
  stops before its first probe (nothing to improve, the day's budget spent,
  the judge on the agent's own model) is listed as skipped with its reason,
  and does not count toward the day's rounds.

## What it learned from the original

The original project's self-heal and learning lanes left these rules, and they
apply here:

- Machine output is never a user correction.
- A single run is never proof; agreement is support, not independent evidence.
- An LLM judge alone misses failure modes, so deterministic checks come first.
- The judge differs from the model judged.
- An honest "no change" is a valid outcome.
