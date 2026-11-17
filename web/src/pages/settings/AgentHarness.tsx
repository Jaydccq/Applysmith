import { useState } from "react";
import { get, post } from "../../api/client";
import { agentHarness } from "../../api/decoders_agent";
import { useInterval, useResource } from "../../hooks";
import { Chip, ErrorNote, Loading, type Tone } from "../../ui/parts";
import type { AgentHarness, AgentHarnessVersion, AgentImproveRound, AgentLesson } from "../../wire";
import { useAction, when } from "../action";

const POLL_MS = 5000;

const outcomeTone: Record<string, Tone> = { adopted: "good", kept: "neutral", skipped: "neutral", failed: "bad", running: "info" };

function ActiveVersion({ h }: { h: AgentHarness }) {
  const v = h.versions.find((x) => x.id === h.active);
  if (h.active === 0 || !v) return <p className="muted">No learned guidance (version 0). The agent runs on its fixed instructions only.</p>;
  const notes = Object.entries(v.toolNotes);
  return (
    <div>
      <p>
        <strong>Version {v.id}</strong>
        {v.summary && <span className="muted"> {v.summary}</span>}
      </p>
      {v.guidance.length > 0 && (
        <ul>
          {v.guidance.map((g, i) => (
            <li key={i}>{g}</li>
          ))}
        </ul>
      )}
      {notes.length > 0 && (
        <ul className="plain">
          {notes.map(([tool, note]) => (
            <li key={tool}>
              <code>{tool}</code> {"—"} {note}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function VersionRow({ v, busy, restore }: { v: AgentHarnessVersion; busy: boolean; restore: (id: number) => void }) {
  return (
    <li>
      <div className="item-main">
        Version {v.id}: {v.summary || "No summary"}
        <span className="item-detail">
          {v.source === "improve" ? "learned" : "restored by you"}, {when(v.createdAt)}
        </span>
      </div>
      {v.active ? (
        <Chip tone="good">active</Chip>
      ) : (
        <button type="button" disabled={busy} onClick={() => restore(v.id)} aria-label={`Restore version ${v.id}`}>
          Restore
        </button>
      )}
    </li>
  );
}

function RoundRow({ r }: { r: AgentImproveRound }) {
  return (
    <li>
      <div className="item-main">
        {r.detail || "No detail"}
        <span className="item-detail">
          {when(r.startedAt)}, {r.probes} {r.probes === 1 ? "probe" : "probes"}
        </span>
      </div>
      <Chip tone={outcomeTone[r.outcome] ?? "neutral"}>{r.outcome}</Chip>
    </li>
  );
}

function LessonRow({ l }: { l: AgentLesson }) {
  return (
    <li className={l.undone ? "undone" : undefined}>
      <div className="item-main">
        {l.cue} {"→"} {l.lesson}
        <span className="item-detail">{l.undone ? "undone" : `${l.confidence} confidence`}</span>
      </div>
    </li>
  );
}

// HarnessView shows what the nightly self-improvement rounds changed in the
// Ask Agent's guidance and undoes it in one press. A restore or a started
// round answers with the whole view, so the page draws from the answer.
export function HarnessView() {
  const [res, reload] = useResource(() => get("/api/agent/harness", agentHarness), [], [], { keep: "settings.harness" });
  const [fresh, setFresh] = useState<AgentHarness | null>(null);
  const act = useAction();
  const h = fresh ?? res.data;

  // A failed poll is dropped: the next one asks again.
  useInterval(() => void get("/api/agent/harness", agentHarness).then(setFresh, () => undefined), POLL_MS, h?.running === true);

  const send = async (path: string, body?: { id: number }) => {
    const next = await act.run(() => post(path, agentHarness, body));
    if (next) setFresh(next);
  };

  if (!h) {
    if (res.status === "error") return <ErrorNote error={res.error} onRetry={reload} what="the self-improvement history" />;
    return <Loading lines={2} label="Loading the self-improvement history" />;
  }
  return (
    <div>
      <div className="actions">
        <button type="button" disabled={act.busy || h.running} onClick={() => void send("/api/agent/harness/round")}>
          Run a round now
        </button>
        {h.running && (
          <span role="status" className="muted">
            A round is running…
          </span>
        )}
      </div>
      {act.error && <ErrorNote error={act.error} />}

      <h4>Active guidance</h4>
      <ActiveVersion h={h} />
      {h.active !== 0 && (
        <div className="actions">
          <button type="button" disabled={act.busy} onClick={() => void send("/api/agent/harness/restore", { id: 0 })}>
            Clear learned guidance
          </button>
        </div>
      )}

      <h4>Versions</h4>
      {h.versions.length === 0 ? (
        <p className="muted">No version has been learned yet.</p>
      ) : (
        <ul className="plain item-list">
          {h.versions.map((v) => (
            <VersionRow key={v.id} v={v} busy={act.busy} restore={(id) => void send("/api/agent/harness/restore", { id })} />
          ))}
        </ul>
      )}

      <h4>Recent rounds</h4>
      {h.rounds.length === 0 ? (
        <p className="muted">No round has run yet.</p>
      ) : (
        <ul className="plain item-list">
          {h.rounds.map((r) => (
            <RoundRow key={r.id} r={r} />
          ))}
        </ul>
      )}

      <h4>Lessons</h4>
      {h.lessons.length === 0 ? (
        <p className="muted">No lessons kept yet.</p>
      ) : (
        <ul className="plain item-list">
          {h.lessons.map((l) => (
            <LessonRow key={l.id} l={l} />
          ))}
        </ul>
      )}
    </div>
  );
}
