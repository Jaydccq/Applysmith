import { useState } from "react";
import { del, get, patch, post } from "../../api/client";
import { schedule, scheduleHistory, scheduleKindList, scheduleList, schedulePauseResult } from "../../api/decoders_discovery";
import { runAccepted } from "../../api/decoders_runs";
import { useResource } from "../../hooks";
import { Link } from "../../router";
import { ActionMenu } from "../../ui/ActionMenu";
import { ConfirmDialog, Modal } from "../../ui/Modal";
import { Chip, Empty, ErrorNote, Panel, Loading, humanize, type Tone } from "../../ui/parts";
import type { Schedule, ScheduleHistoryRow } from "../../wire";
import { useAction, when } from "../action";
import { Proposals } from "./Proposals";
import { ScheduleEditor } from "./ScheduleEditor";
import { dayLabel, describePreset, localZone, timetable, untilLabel } from "./schedule";
import { EmptyState } from "../../ui/EmptyState";
import { CalendarClock, History as HistoryIcon, Pause, Play, Plus, ShieldCheck, Trash2 } from "lucide-react";
import { Button } from "../../ui/Button";

const RESULT_TONE: Record<string, Tone> = { ok: "good", running: "info", failed: "bad", skipped: "warn" };
// endedAs names a run that did not succeed by its reason: cancelled,
// interrupted, a sign-in or other step it needs from the user, or an error.
function endedAs(reason: string): [string, Tone] {
  if (reason === "cancelled") return ["Cancelled", "neutral"];
  return reason.startsWith("needs-") ? ["Needs you", "warn"] : [reason === "interrupted" ? "Interrupted" : "Failed", "bad"];
}
// A run's row keeps the outcome fired or manual however it ended; its reason
// says how: none when it succeeded.
function outcomeOf(h: ScheduleHistoryRow): [string, Tone] {
  if (h.outcome !== "fired" && h.outcome !== "manual") return [humanize(h.outcome), h.outcome === "skipped" ? "warn" : h.outcome === "interrupted" ? "bad" : "neutral"];
  if (!h.finishedAt) return ["Running", "info"];
  if (!h.reason) return [h.outcome === "manual" ? "Ran by hand" : "Ran", "good"];
  return endedAs(h.reason);
}

function History({ s, onClose }: { s: Schedule; onClose: () => void }) {
  const [res, retry] = useResource(() => get(`/api/schedules/${s.id}/history`, scheduleHistory), [s.id], ["schedules"]);
  return (
    <Modal title={`History of ${s.name}`} variant="drawer" onClose={onClose}>
      {res.status === "loading" && <Loading label="Loading the history" />}
      {res.status === "error" && <ErrorNote error={res.error} onRetry={retry} what="the history" />}
      {res.data &&
        (res.data.history.length === 0 ? (
          <Empty>This schedule has not run yet.</Empty>
        ) : (
          <ul className="plain schedule-history">
            {res.data.history.map((h) => (
              <li key={h.id}>
                <span className="row">
                  <span>{when(h.scheduledFor)}</span>
                  <Chip tone={outcomeOf(h)[1]}>{outcomeOf(h)[0]}</Chip>
                </span>
                {(h.reason || h.summary || h.detail) && <span className="sub muted">{[h.reason && humanize(h.reason), h.summary, h.detail].filter(Boolean).join(". ")}</span>}
                {h.runId && (
                  <span className="sub">
                    <Link to={`/runs/${h.runId}`}>Open the run</Link>
                  </span>
                )}
              </li>
            ))}
          </ul>
        ))}
      <div className="actions">
        <button type="button" onClick={onClose}>
          Close
        </button>
      </div>
    </Modal>
  );
}

// Timetable shows the next seven days at a glance; the list above it is
// where a schedule is changed.
function Timetable({ schedules }: { schedules: Schedule[] }) {
  const { days, often } = timetable(schedules);
  return (
    <section aria-labelledby="timetable-h" className="panel">
      <h2 id="timetable-h">Next 7 days</h2>
      <p className="muted">Times are in your local time zone, {localZone()}.</p>
      {often.length > 0 && (
        <div className="timetable-often">
          <p className="muted">Several times a day, not drawn below:</p>
          <ul className="plain">
            {often.map((s) => (
              <li key={s.id}>
                <strong>{s.name}</strong> <span className="muted">{describePreset(s.preset)}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
      <table className="cards timetable">
        <caption className="sr-only">The next seven days, today first</caption>
        <thead>
          <tr>
            {days.map((d) => (
              <th key={d.day} scope="col" aria-current={d.today ? "date" : undefined}>
                {d.today ? "Today" : dayLabel(d.day)}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          <tr>
            {days.map((d) => (
              <td key={d.day} data-label={d.today ? "Today" : dayLabel(d.day)} className={d.today ? "today" : undefined}>
                {d.cells.length === 0 ? (
                  <span className="muted">Nothing</span>
                ) : (
                  <ul className="plain">
                    {d.cells.map((c) => (
                      <li key={`${c.schedule.id}-${c.time}`}>
                        <span className="tt-time">{c.time}</span> {c.schedule.name}
                      </li>
                    ))}
                  </ul>
                )}
              </td>
            ))}
          </tr>
        </tbody>
      </table>
    </section>
  );
}

function LastRun({ s }: { s: Schedule }) {
  if (s.activeRunId) {
    return (
      <>
        <Chip tone="info">Running</Chip>
        <span className="sub">
          <Link to={`/runs/${s.activeRunId}`}>Follow the run</Link>
        </span>
      </>
    );
  }
  if (!s.lastResult) return <span className="muted">Never</span>;
  // A run that did not succeed says how it ended and why, as History does.
  const failed = s.lastResult === "failed" && s.lastReason;
  const [label, tone] = failed ? endedAs(s.lastReason!) : [humanize(s.lastResult), RESULT_TONE[s.lastResult] ?? "neutral"];
  const why = failed && (s.lastDetail || humanize(s.lastReason!));
  // A skip started no run, so the newest run is an older one: History has it.
  const run = s.lastResult !== "skipped" && s.lastRunId;
  return (
    <>
      <Chip tone={tone}>{label}</Chip>
      {why && <span className="sub muted">{why}</span>}
      {(s.lastSkipReason || s.lastResultAt) && (
        <span className="sub muted">
          {s.lastSkipReason && humanize(s.lastSkipReason)}
          {s.lastSkipReason && s.lastResultAt && ", "}
          {s.lastResultAt && <time dateTime={s.lastResultAt}>{when(s.lastResultAt)}</time>}
        </span>
      )}
      {run && (
        <span className="sub">
          <Link to={`/runs/${run}`}>Open the run</Link>
        </span>
      )}
    </>
  );
}

// PausedWhy says which bulk pause switched a schedule off, and how it comes back.
function PausedWhy({ by }: { by?: string }) {
  if (by === "all") return <span className="sub muted">By Pause all</span>;
  if (by !== "gmail.sync") return null;
  return (
    <span className="sub muted">
      Until Gmail is connected again. <Link to="/settings#settings-gmail">Connect Gmail</Link>
    </span>
  );
}

export function SchedulesTab({ editId, onEdit }: { editId: string; onEdit: (id: string | undefined) => void }) {
  const [res, retry] = useResource(() => get("/api/schedules", scheduleList), [], ["schedules"]);
  const [kinds] = useResource(() => get("/api/schedule-kinds", scheduleKindList), []);
  const [removing, setRemoving] = useState<Schedule | null>(null);
  const [history, setHistory] = useState<Schedule | null>(null);
  // started is what the last Run now or bulk switch did; a run is linked.
  const [started, setStarted] = useState<{ text: string; runId?: string } | null>(null);
  const act = useAction();
  const items = res.data?.schedules ?? [];
  const editing = editId === "new" ? undefined : items.find((s) => String(s.id) === editId);
  const paused = items.filter((s) => !s.enabled).length;

  const toggle = async (s: Schedule) => {
    await act.run(() => patch(`/api/schedules/${s.id}`, schedule, { enabled: !s.enabled }));
    retry();
  };
  const runNow = async (s: Schedule) => {
    const done = await act.run(() => post(`/api/schedules/${s.id}/run`, runAccepted));
    setStarted(done ? { text: `${s.name} started.`, runId: done.runId } : null);
    retry();
  };
  // Pause all remembers what it switched off; resume all switches on only
  // those, never a schedule that was off before.
  const bulk = async (what: "pause" | "resume") => {
    const done = await act.run(() => post(`/api/schedules/${what}-all`, schedulePauseResult));
    const n = done?.changed.length ?? 0;
    const names = `${n} schedule${n === 1 ? "" : "s"}: ${done?.changed.join(", ")}.`;
    setStarted(done ? { text: n === 0 ? `No schedule to ${what}.` : what === "pause" ? `Paused ${names} Resume all switches on only these.` : `Resumed ${names}` } : null);
    retry();
  };
  const remove = async () => {
    if (!removing) return;
    await act.run(() => del(`/api/schedules/${removing.id}`, scheduleList));
    setRemoving(null);
    retry();
  };

  return (
    <>
      <p className="note info with-icon">
        <ShieldCheck aria-hidden="true" />
        <span>No schedule can submit an application. A scheduled job scans, reads mail, refreshes data or writes a report, and nothing else.</span>
      </p>
      <Proposals onAccepted={retry} />
      <Panel
        title="Schedules"
        id="schedule-list"
        res={res}
        retry={retry}
        actions={
          <div className="panel-actions">
            <Button size="sm" icon={Pause} disabled={act.busy || !items.some((s) => s.enabled)} onClick={() => void bulk("pause")}>
              Pause all
            </Button>
            <Button size="sm" icon={Play} disabled={act.busy || !res.data?.pausedAll} onClick={() => void bulk("resume")}>
              Resume all{res.data?.pausedAll ? ` (${res.data.pausedAll})` : ""}
            </Button>
            <Button variant="primary" size="sm" icon={Plus} onClick={() => onEdit("new")}>
              New schedule
            </Button>
          </div>
        }
      >
        {(data) => (
          <>
            {act.error && <ErrorNote error={act.error} />}
            <div aria-live="polite">
              {started && (
                <p className="note ok">
                  {started.text} {started.runId && <Link to={`/runs/${started.runId}`}>Follow the run</Link>}
                </p>
              )}
            </div>
            {data.schedules.length === 0 ? (
              <EmptyState
                icon={CalendarClock}
                title="No schedule yet."
                action={
                  <>
                    <Button variant="primary" icon={Plus} onClick={() => onEdit("new")}>
                      New schedule
                    </Button>
                    <Link to="/setup?step=discovery" className="button">
                      Choose a schedule pack
                    </Link>
                  </>
                }
              >
                A schedule runs a scan, a mail check or a report at the times you pick. A pack sets up a common set in one step.
              </EmptyState>
            ) : (
              <>
                <p className="count-line">
                  {data.schedules.length} schedule{data.schedules.length === 1 ? "" : "s"}
                  {paused > 0 ? `, ${paused} paused` : ""}.
                </p>
                <table className="cards dense schedules">
                  <caption className="sr-only">Schedules</caption>
                  <thead>
                    <tr>
                      {/* The name, how it repeats and what its job does take the share, so a job's description is not drawn two words a line beside the run columns. */}
                      <th scope="col" className="fill">
                        Schedule
                      </th>
                      <th scope="col" className="nowrap">
                        Next run
                      </th>
                      <th scope="col" className="nowrap">
                        Last run
                      </th>
                      <th scope="col" className="nowrap">
                        Enabled
                      </th>
                      <th scope="col">
                        <span className="sr-only">Actions</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.schedules.map((s) => (
                      <tr key={s.id} data-state={s.enabled ? undefined : "paused"}>
                        <td data-label="Schedule" className="key fill">
                          <strong>{s.name}</strong>
                          {s.unknownKind && (
                            <>
                              {" "}
                              <Chip tone="bad">Unknown job</Chip>
                            </>
                          )}
                          <span className="sub">{describePreset(s.preset)}</span>
                          <span className="sub muted" title={s.kind}>
                            {kinds.data?.kinds.find((k) => k.name === s.kind)?.doc ?? s.kind}
                          </span>
                          {s.problem && <span className="note compact error">{s.problem}</span>}
                          {s.attention?.map((a) => (
                            <span key={a} className="note compact warn">
                              {a}
                            </span>
                          ))}
                        </td>
                        <td data-label="Next run" className="nowrap">
                          {!s.enabled ? (
                            <>
                              <Chip>Paused</Chip>
                              <PausedWhy by={s.pausedBy} />
                            </>
                          ) : s.next[0] ? (
                            <>
                              <time dateTime={s.next[0]}>{when(s.next[0])}</time>
                              <span className="sub muted">{untilLabel(s.next[0])}</span>
                            </>
                          ) : (
                            <span className="muted">None upcoming</span>
                          )}
                        </td>
                        <td data-label="Last run" className="nowrap">
                          <LastRun s={s} />
                        </td>
                        <td data-label="Enabled" className="nowrap">
                          <label className="check">
                            <input type="checkbox" checked={s.enabled} disabled={act.busy} onChange={() => void toggle(s)} />
                            <span className="sr-only">Enabled for {s.name}</span>
                          </label>
                        </td>
                        <td className="row-actions">
                          <div className="row-actions-wrap">
                            <button
                              type="button"
                              className="small"
                              disabled={act.busy || Boolean(s.activeRunId)}
                              title={s.activeRunId ? "It is running now." : undefined}
                              onClick={() => void runNow(s)}
                              aria-label={`Run ${s.name} now`}
                            >
                              <Play />
                              Run now
                            </button>
                            <button type="button" className="small" onClick={() => onEdit(String(s.id))} aria-label={`Edit ${s.name}`}>
                              Edit
                            </button>
                            <ActionMenu
                              label={`More actions: ${s.name}`}
                              items={[
                                { label: "History", ariaLabel: `History of ${s.name}`, icon: HistoryIcon, onSelect: () => setHistory(s) },
                                { label: "Delete", ariaLabel: `Delete ${s.name}`, icon: Trash2, onSelect: () => setRemoving(s) },
                              ]}
                            />
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </>
            )}
            {data.timers.length > 0 && (
              <details className="timers">
                <summary>Built-in timers ({data.timers.length})</summary>
                <ul className="plain stack">
                  {data.timers.map((t) => (
                    <li key={t.name}>
                      <strong>{t.name}</strong> <Chip tone={t.active ? "good" : "neutral"}>{t.active ? "Active" : "Inactive"}</Chip>
                      <span className="sub muted">
                        {t.description}
                        {t.reason ? ` (${t.reason})` : ""}
                      </span>
                    </li>
                  ))}
                </ul>
              </details>
            )}
          </>
        )}
      </Panel>
      {items.length > 0 && <Timetable schedules={items} />}
      {(editId === "new" || editing) && (
        <ScheduleEditor
          key={editId}
          existing={editing}
          onClose={() => onEdit(undefined)}
          onSaved={() => {
            onEdit(undefined);
            retry();
          }}
        />
      )}
      {history && <History s={history} onClose={() => setHistory(null)} />}
      {removing && (
        <ConfirmDialog title={`Delete ${removing.name}`} consequence="The schedule and its history are removed. Nothing it already ran is undone." verb="Delete" busy={act.busy} onConfirm={() => void remove()} onCancel={() => setRemoving(null)} />
      )}
    </>
  );
}
