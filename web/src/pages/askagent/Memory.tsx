import { useId, useState, type FormEvent } from "react";
import { del, get, patch, post } from "../../api/client";
import { memoryDeleted, memoryExport, memoryFact, memoryFactEdited, memoryFactList, memoryReviewList, memoryRule, memoryRuleList } from "../../api/decoders_agent";
import { useResource } from "../../hooks";
import { ConfirmDialog } from "../../ui/Modal";
import { Chip, Empty, ErrorNote, Panel } from "../../ui/parts";
import type { MemoryFact, MemoryFactPatch, MemoryReviewItem, MemoryRule, MemoryRulePatch } from "../../wire";
import { useAction, when } from "../action";

// TextEditor is the one-field form that edits a fact or a rule. The limit is
// the server's; the server checks it again.
function TextEditor({ initial, max, label, onSave, onCancel }: { initial: string; max: number; label: string; onSave: (body: string) => Promise<void>; onCancel: () => void }) {
  const [body, setBody] = useState(initial);
  const id = useId();
  const act = useAction();
  const trimmed = body.trim();
  const valid = trimmed !== "" && body.length <= max && trimmed !== initial;
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    await act.run(async () => {
      await onSave(trimmed);
      return true;
    });
  };
  return (
    <form onSubmit={submit} className="form-grid">
      <div>
        <label htmlFor={id}>{label}</label>
        <textarea id={id} value={body} onChange={(e) => setBody(e.target.value)} />
        <span className="muted">
          {body.length} of {max} characters
        </span>
      </div>
      <div className="actions">
        <button type="submit" disabled={act.busy || !valid}>
          Save
        </button>
        <button type="button" onClick={onCancel}>
          Cancel
        </button>
      </div>
      {act.error && <ErrorNote error={act.error} />}
    </form>
  );
}

const SUBJECT_TEXT = (f: { subject: string; subjectRef?: string }) => (f.subject === "user" ? "about you" : `about ${f.subject} ${f.subjectRef ?? ""}`.trim());

function FactRow({ fact, max, reload, note }: { fact: MemoryFact; max: number; reload: () => void; note: (s: string) => void }) {
  const [editing, setEditing] = useState(false);
  const [forgetting, setForgetting] = useState(false);
  const act = useAction();
  const base = `/api/agent/memory/facts/${encodeURIComponent(fact.id)}`;
  const needsReview = fact.trust === "needs_review";

  const approve = async () => {
    if (await act.run(() => post(`${base}/approve`, memoryFact))) reload();
  };
  const forget = async () => {
    setForgetting(false);
    if (await act.run(() => del(base, memoryDeleted))) reload();
  };

  return (
    <li className="panel">
      <header className="row">
        <span>
          <Chip>{fact.kind}</Chip> <Chip tone={needsReview ? "warn" : "good"}>{needsReview ? "needs your review" : "trusted"}</Chip>
        </span>
        <span className="muted">
          {fact.source === "user_declared" ? "you said it" : "the agent noticed it"} · {SUBJECT_TEXT(fact)} · {when(fact.createdAt)}
        </span>
      </header>
      {editing ? (
        <TextEditor
          initial={fact.body}
          max={max}
          label="Fact"
          onCancel={() => setEditing(false)}
          onSave={async (body) => {
            const body2: MemoryFactPatch = { body };
            const out = await patch(base, memoryFactEdited, body2);
            setEditing(false);
            if (!out.fact) note("That text reads like an instruction to the assistant, so it was not stored as a fact. It waits in the review list below.");
            reload();
          }}
        />
      ) : (
        <p>{fact.body}</p>
      )}
      <div className="actions">
        {needsReview && (
          <button type="button" disabled={act.busy} onClick={() => void approve()} aria-label={`Approve fact: ${fact.body}`}>
            Approve
          </button>
        )}
        {!editing && (
          <button type="button" onClick={() => setEditing(true)} aria-label={`Edit fact: ${fact.body}`}>
            Edit
          </button>
        )}
        <button type="button" className="danger-outline" disabled={act.busy} onClick={() => setForgetting(true)} aria-label={`Forget fact: ${fact.body}`}>
          Forget
        </button>
      </div>
      {act.error && <ErrorNote error={act.error} />}
      {forgetting && (
        <ConfirmDialog
          title="Forget this fact"
          consequence="It is deleted with its earlier versions, and the agent will not add the same text again unless you say it anew."
          verb="Forget fact"
          onCancel={() => setForgetting(false)}
          onConfirm={() => void forget()}
        />
      )}
    </li>
  );
}

const RULE_SOURCE: Record<string, string> = { agent: "proposed by the agent", consolidation: "proposed by the nightly review", user: "written by you" };
const RULE_TONE: Record<string, "good" | "warn" | "neutral"> = { active: "good", proposed: "warn" };

function RuleRow({ rule, max, reload }: { rule: MemoryRule; max: number; reload: () => void }) {
  const [editing, setEditing] = useState(false);
  const [removing, setRemoving] = useState(false);
  const act = useAction();
  const base = `/api/agent/memory/rules/${encodeURIComponent(rule.id)}`;
  const move = async (verb: "approve" | "retire") => {
    if (await act.run(() => post(`${base}/${verb}`, memoryRule))) reload();
  };
  const remove = async () => {
    setRemoving(false);
    if (await act.run(() => del(base, memoryDeleted))) reload();
  };
  return (
    <li className="panel">
      <header className="row">
        <Chip tone={RULE_TONE[rule.state] ?? "neutral"}>{rule.state}</Chip>
        <span className="muted">
          {RULE_SOURCE[rule.source] ?? rule.source} · {when(rule.createdAt)}
        </span>
      </header>
      {editing ? (
        <TextEditor
          initial={rule.body}
          max={max}
          label="Rule"
          onCancel={() => setEditing(false)}
          onSave={async (body) => {
            const patchBody: MemoryRulePatch = { body };
            await patch(base, memoryRule, patchBody);
            setEditing(false);
            reload();
          }}
        />
      ) : (
        <p>{rule.body}</p>
      )}
      <div className="actions">
        {rule.state === "proposed" && (
          <button type="button" disabled={act.busy} onClick={() => void move("approve")} aria-label={`Approve rule: ${rule.body}`}>
            Approve rule
          </button>
        )}
        {rule.state === "active" && (
          <button type="button" disabled={act.busy} onClick={() => void move("retire")} aria-label={`Retire rule: ${rule.body}`}>
            Retire
          </button>
        )}
        {!editing && (
          <button type="button" onClick={() => setEditing(true)} aria-label={`Edit rule: ${rule.body}`}>
            Edit
          </button>
        )}
        <button type="button" className="danger-outline" disabled={act.busy} onClick={() => setRemoving(true)} aria-label={`Delete rule: ${rule.body}`}>
          Delete
        </button>
      </div>
      {act.error && <ErrorNote error={act.error} />}
      {removing && (
        <ConfirmDialog title="Delete this rule" consequence="The rule is removed. If it was active, the agent stops following it from the next turn." verb="Delete rule" onCancel={() => setRemoving(false)} onConfirm={() => void remove()} />
      )}
    </li>
  );
}

function ReviewRow({ item, reload }: { item: MemoryReviewItem; reload: () => void }) {
  const act = useAction();
  const dismiss = async () => {
    if (await act.run(() => del(`/api/agent/memory/review/${encodeURIComponent(item.id)}`, memoryDeleted))) reload();
  };
  return (
    <li className="row">
      <span>
        {item.body} <Chip tone="warn">{item.reason.replace("_", " ")}</Chip>
        {act.error && <ErrorNote error={act.error} />}
      </span>
      <button type="button" disabled={act.busy} onClick={() => void dismiss()} aria-label={`Dismiss: ${item.body}`}>
        Dismiss
      </button>
    </li>
  );
}

// download hands the user a file made in the page; nothing is uploaded.
function download(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "application/json" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

function ExportButton() {
  const act = useAction();
  const run = async () => {
    const all = await act.run(() => get("/api/agent/memory/export", memoryExport));
    if (all) download(`applysmith-memory-${all.exportedAt.slice(0, 10)}.json`, JSON.stringify(all, null, 2));
  };
  return (
    <>
      <button type="button" disabled={act.busy} onClick={() => void run()}>
        Export memory
      </button>
      {act.error && <ErrorNote error={act.error} />}
    </>
  );
}

function Facts() {
  const [res, reload] = useResource(() => get("/api/agent/memory/facts", memoryFactList), []);
  const [said, setSaid] = useState("");
  return (
    <Panel title="Facts" id="memory-facts" res={res} retry={reload} actions={<ExportButton />}>
      {(list) => (
        <>
          <p className="muted">
            Only trusted facts are given to the agent. A fact it noticed on its own waits here until you approve it. {list.facts.length} of {list.maxFacts} stored.
          </p>
          {said && (
            <p className="note" role="status">
              {said}
            </p>
          )}
          {list.facts.length === 0 ? (
            <Empty>Nothing is remembered yet. Tell the agent to remember something, or wait for the nightly review.</Empty>
          ) : (
            <ul className="plain stack">
              {list.facts.map((f) => (
                <FactRow key={f.id} fact={f} max={list.maxFactBody} reload={reload} note={setSaid} />
              ))}
            </ul>
          )}
        </>
      )}
    </Panel>
  );
}

function Rules() {
  const [res, reload] = useResource(() => get("/api/agent/memory/rules", memoryRuleList), []);
  return (
    <Panel title="Operating rules" id="memory-rules" res={res} retry={reload}>
      {(list) => (
        <>
          <p className="muted">
            A rule is guidance on how you want the agent to work. It applies only after you approve it, and it can never change what the agent may do. Up to {list.maxActive} can be active.
          </p>
          {list.rules.length === 0 ? (
            <Empty>No rule has been proposed.</Empty>
          ) : (
            <ul className="plain stack">
              {list.rules.map((r) => (
                <RuleRow key={r.id} rule={r} max={list.maxRuleBody} reload={reload} />
              ))}
            </ul>
          )}
        </>
      )}
    </Panel>
  );
}

function Review() {
  const [res, reload] = useResource(() => get("/api/agent/memory/review", memoryReviewList), []);
  return (
    <Panel title="Held for your review" id="memory-review" res={res} retry={reload}>
      {(list) =>
        list.items.length === 0 ? (
          <Empty>Nothing is waiting.</Empty>
        ) : (
          <>
            <p className="muted">These read like instructions about how to answer, so they are not stored as facts. Dismiss one, or write it as an answer in the answer bank if you mean it.</p>
            <ul className="plain stack">
              {list.items.map((i) => (
                <ReviewRow key={i.id} item={i} reload={reload} />
              ))}
            </ul>
          </>
        )
      }
    </Panel>
  );
}

// MemoryPanel is the Memory tab: what the agent remembers about the user, the
// rules waiting for approval and the statements it refused to store as facts.
export function MemoryPanel() {
  return (
    <>
      <Facts />
      <Rules />
      <Review />
    </>
  );
}
