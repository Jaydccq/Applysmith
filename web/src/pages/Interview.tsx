import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { get, newRequestId, patch, post, request } from "../api/client";
import { candidacyDetail, statusChangeResult } from "../api/decoders";
import { documentRun, interviewPack, interviewStage, interviewWorkspace } from "../api/decoders_interview";
import { runAccepted } from "../api/decoders_runs";
import { kbCoverage, kbDocumentList } from "../api/decoders_pages";
import { useInterval, useResource } from "../hooks";
import { CopyButton } from "../ui/CopyButton";
import { leave, Link } from "../router";
import { Modal } from "../ui/Modal";
import { useSaveKey } from "../ui/SaveBar";
import { Chip, Empty, ErrorNote, formatDateTime, humanize, Loading, UndoNote } from "../ui/parts";
import { useAction } from "./action";
import { runReason, stageWords } from "./documents/run";
import { slug } from "./answerbank/model";
import { asText, PackView } from "./interview/PackView";
import { FeedbackForm, MockPractice } from "./interview/Practice";
import type { InterviewStage, InterviewStep } from "../wire";

const TYPES = [
  ["phone_screen", "Phone screen"],
  ["technical", "Technical"],
  ["system_design", "System design"],
  ["behavioral", "Behavioral"],
  ["case", "Case"],
  ["onsite", "Onsite"],
  ["final", "Final"],
  ["other", "Other"],
] as const;
const TYPE_LABEL: Record<string, string> = Object.fromEntries(TYPES);
const FORMATS = [["video", "Video"], ["phone", "Phone"], ["onsite", "On site"], ["unknown", "Not known yet"]] as const;
const STAGE_ID = /^[a-z0-9-]{2,64}$/;
const DAY_MS = 86_400_000;

// when is a stage's date and time with how far off that day is.
function when(at: string, now = new Date()): string {
  const day = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  const n = Math.round((day(new Date(at)) - day(now)) / DAY_MS);
  if (Number.isNaN(n)) return at;
  return `${formatDateTime(at)} (${n === 0 ? "today" : n === 1 ? "tomorrow" : n === -1 ? "yesterday" : n > 0 ? `in ${n} days` : `${-n} days ago`})`;
}

// ics is a stage as an RFC 5545 calendar event: TEXT escaped (3.3.11), CRLF
// line ends, lines folded at 75 octets (3.1), the UID stable per stage.
const icsText = (v: string) => v.replace(/[\\;,]/g, "\\$&").replace(/\r?\n/g, "\\n");
const icsTime = (d: Date) => d.toISOString().replace(/[-:]|\.\d+/g, "");
function fold(line: string): string {
  let out = "", n = 0;
  for (const ch of line) {
    const b = new TextEncoder().encode(ch).length;
    if (n + b > 75) {
      out += "\r\n ";
      n = 1;
    }
    out += ch;
    n += b;
  }
  return out;
}
export function ics(candidacyId: number, st: InterviewStage, summary: string, description: string, url: string, now = new Date()): string {
  return [
    "BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//Applysmith//Interview prep//EN", "BEGIN:VEVENT", `UID:${candidacyId}-${st.id}@applysmith`,
    `DTSTAMP:${icsTime(now)}`, `DTSTART:${icsTime(new Date(st.at!))}`, "DURATION:PT1H", `SUMMARY:${icsText(summary)}`,
    `DESCRIPTION:${icsText(description)}`, ...(url ? [`URL:${url}`] : []), "END:VEVENT", "END:VCALENDAR",
  ].map(fold).join("\r\n") + "\r\n";
}

// local is an instant as the value of a datetime-local input.
const local = (at: string) => (at ? formatDateTime(at).replace(" ", "T") : "");

// AddStage records a new stage or, given one, the stage's details again,
// which is how a proposed stage is confirmed.
function AddStage({ base, initial, onSaved }: { base: string; initial?: InterviewStage; onSaved: (id: string) => void }) {
  const [id, setId] = useState(initial?.id ?? "");
  const [type, setType] = useState(initial?.type ?? "technical");
  const [at, setAt] = useState(local(initial?.at ?? ""));
  const [format, setFormat] = useState(initial?.format ?? "video");
  const [names, setNames] = useState(initial?.interviewers.join(", ") ?? "");
  // Confirming is the user's own decision: a stage with no date or
  // interviewers yet is recorded as unconfirmed, and the prep pack says so.
  // Opening a stage's details to confirm them starts with the box ticked.
  const [confirmed, setConfirmed] = useState(initial !== undefined);
  const a = useAction();
  const hintId = useId();
  // The name is made from what is typed; a stage left unnamed is named after its type.
  const name = initial?.id ?? (id.trim() ? slug(id) : type.replaceAll("_", "-"));
  const valid = STAGE_ID.test(name);
  // Mod+S saves once a field differs from what the form opened with or last added.
  const fields = JSON.stringify([id, type, at, format, names, confirmed]);
  const [start, setStart] = useState(fields);
  const saveKey = useSaveKey(fields !== start);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const body = {
      id: name, type, format, userConfirmed: confirmed, expectedRevision: initial?.revision ?? 0,
      ...(at ? { at: new Date(at).toISOString() } : {}),
      interviewers: names.split(",").map((n) => n.trim()).filter(Boolean),
    };
    if ((await a.run(() => post(`${base}/stages`, interviewStage, body))) !== undefined) {
      setId("");
      setStart(JSON.stringify(["", type, at, format, names, confirmed]));
      onSaved(name);
    }
  };
  return (
    <form onSubmit={submit} className="form-grid" aria-label={initial ? `Details of stage ${name}` : "Add a stage"}>
      {!initial && (
        <>
          <label>
            Stage name
            <input value={id} onChange={(e) => setId(e.target.value)} placeholder={name} maxLength={64} aria-invalid={id.trim() !== "" && !valid} aria-describedby={hintId} />
          </label>
          <p id={hintId} className="muted">
            {id.trim() && !valid ? "A name needs at least 2 letters or digits." : `Optional. Saved as ${name}; capitals and spaces become lowercase and hyphens.`}
          </p>
        </>
      )}
      <label>
        Type
        <select value={type} onChange={(e) => setType(e.target.value)}>
          {TYPES.map(([v, l]) => (
            <option key={v} value={v}>
              {l}
            </option>
          ))}
        </select>
      </label>
      <label>
        Date and time
        <input type="datetime-local" value={at} onChange={(e) => setAt(e.target.value)} />
      </label>
      <label>
        Format
        <select value={format} onChange={(e) => setFormat(e.target.value)}>
          {FORMATS.map(([v, l]) => (
            <option key={v} value={v}>
              {l}
            </option>
          ))}
        </select>
      </label>
      <label>
        Interviewers, separated by commas
        <input value={names} onChange={(e) => setNames(e.target.value)} />
      </label>
      <label className="check">
        <input type="checkbox" checked={confirmed} onChange={(e) => setConfirmed(e.target.checked)} /> These details are right
      </label>
      <p className="muted">Tick it once the date and interviewers are settled. Until then the stage shows as not confirmed.</p>
      <div className="actions">
        <button type="submit" disabled={a.busy || !valid} ref={saveKey}>
          {initial ? "Save details" : "Add stage"}
        </button>
      </div>
      {a.error && <ErrorNote error={a.error} />}
    </form>
  );
}

// A stage that is over leads with its missing debrief: the packs of the later
// stages read it, and a pack of a stage already held matters no more.
function StageRow({ st, selected, packs, debriefed, onSelect }: { st: InterviewStage; selected: boolean; packs: number; debriefed: boolean; onSelect: () => void }) {
  const over = !!st.at && Date.parse(st.at) < Date.now();
  return (
    <li>
      <button type="button" aria-pressed={selected} onClick={onSelect}>
        {st.id}
      </button>{" "}
      {TYPE_LABEL[st.type] ?? st.type}
      {st.at && `, ${when(st.at)}`}
      {st.format !== "unknown" && st.format !== st.type && `, ${st.format}`}
      {st.interviewers.length > 0 && `, with ${st.interviewers.join(", ")}`}{" "}
      {!st.userConfirmed && <Chip tone="warn">Not confirmed</Chip>}{" "}
      {over && !debriefed ? <Chip tone="warn">No debrief yet</Chip> : <span className="muted">{packs === 0 ? "no pack yet" : `${packs} ${packs === 1 ? "revision" : "revisions"}`}</span>}
    </li>
  );
}

// Tracker statuses a recorded stage moves on to interview.
const BEFORE_INTERVIEW = new Set(["applied", "responded"]);

// InterviewDrawer is the interview workspace of one application: its stages,
// the prep pack of each, the feedback that feeds the next packs and mock
// answers with coaching. It opens on the next stage still ahead. returnFocus
// is where it sends focus when it closes after the address opened it (Modal).
export function InterviewDrawer({ candidacyId, label, onClose, returnFocus }: { candidacyId: number; label: string; onClose: () => void; returnFocus?: () => HTMLElement | null | undefined }) {
  const base = `/api/candidacies/${candidacyId}/interview`;
  const [ws, reloadWs] = useResource(() => get(base, interviewWorkspace), [base], ["tracker", "runs"]);
  const [row, reloadRow] = useResource(() => get(`/api/candidacies/${candidacyId}`, candidacyDetail), [candidacyId], ["tracker"]);
  const [stories] = useResource(() => get("/api/kb/documents", kbDocumentList).catch(() => ({ entries: [], uses: [] })), []);
  const [coverage] = useResource(() => get("/api/kb/stories/coverage", kbCoverage).catch(() => ({ principles: [], shapes: [] })), []);
  const [picked, setPicked] = useState("");
  const [revision, setRevision] = useState(0);
  const [runId, setRunId] = useState("");
  // runFor is the stage the followed pack run builds.
  const [runFor, setRunFor] = useState("");
  const attached = useRef("");
  const [stage, setStage] = useState("");
  const [failure, setFailure] = useState("");
  const [practice, setPractice] = useState<{ q: string; n: number } | null>(null);
  const [editing, setEditing] = useState(false);
  const [moved, setMoved] = useState("");
  const start = useAction();
  const move = useAction();
  const cancel = useAction();
  const [undo, setUndo] = useState<{ st: InterviewStage; n: number }>();
  const addSummary = useRef<HTMLElement>(null);
  const editButton = useRef<HTMLButtonElement>(null);
  const movedNote = useRef<HTMLParagraphElement>(null);
  const stageAt = useRef<HTMLElement>(null);

  // Stages in date order, the undated last; the drawer opens on the first
  // still ahead today, else on the one recorded last.
  const stages = [...(ws.data?.stages ?? [])].sort((x, y) => (x.at ? Date.parse(x.at) : Infinity) - (y.at ? Date.parse(y.at) : Infinity));
  const today = new Date().setHours(0, 0, 0, 0);
  const current = stages.find((s) => s.id === picked) ?? stages.find((s) => s.at && Date.parse(s.at) >= today) ?? ws.data?.stages[0];
  const infos = (ws.data?.packs ?? []).filter((p) => p.stage === current?.id);
  const revisions = infos.map((p) => p.revision).sort((x, y) => y - x);
  const shown = revision > 0 && revisions.includes(revision) ? revision : (revisions[0] ?? 0);
  const [pack, reloadPack] = useResource(
    async () => (current && shown > 0 ? get(`${base}/stages/${current.id}/pack`, interviewPack, { revision: shown }) : null),
    [base, current?.id, shown],
  );
  const titles = new Map((stories.data?.entries ?? []).map((e) => [e.id, e.title || e.id]));
  const entry = row.data?.entry;
  // What changed after the shown pack was built: it is not in that pack.
  const built = pack.data?.createdAt ?? "";
  const after = (at: string) => Date.parse(at) > Date.parse(built);
  const briefs = new Set(pack.data?.sources.filter((s) => s.kind === "company-brief").map((s) => s.ref));
  const kb = stories.data?.entries ?? [];
  // A pack that read the evaluation report names it; the row points at a newer one after an evaluation again.
  const report = entry?.reportNumber, read = pack.data?.reportNumber;
  const since = !built ? [] : [
    ...(read && report && report > read ? [`a newer evaluation report (report ${report}) replaced report ${read}`] : []),
    ...(current && after(current.recordedAt) ? ["the stage details changed"] : []),
    ...((ws.data?.feedback ?? []).some((f) => after(f.recordedAt)) ? ["feedback was recorded"] : []),
    ...(kb.some((e) => e.kind === "snippet" && e.category === "behavioral" && after(e.modifiedAt)) ? ["your story bank changed"] : []),
    ...(kb.some((e) => briefs.has(e.path) && after(e.modifiedAt)) ? ["the company brief changed"] : []),
  ];
  // A stage that is over with none ahead waits on its debrief and what came next.
  const over = !!current?.at && Date.parse(current.at) < Date.now();
  const ahead = stages.some((s) => s.at && Date.parse(s.at) >= Date.now());

  // A pack run that was working when the drawer opened is followed like one
  // started here.
  const active = ws.data?.packRun;
  const coachRun = ws.data?.coachRun;
  useEffect(() => {
    if (coachRun && !picked) setPicked(coachRun.stage);
  }, [coachRun?.runId]);
  useEffect(() => {
    if (active && !runId && attached.current !== active.runId) {
      attached.current = active.runId;
      setRunId(active.runId);
      setRunFor(active.stage);
    }
  }, [active?.runId, runId]);
  useEffect(() => {
    if (moved) movedNote.current?.focus();
  }, [moved]);

  useInterval(
    async () => {
      try {
        const run = await get(`/api/documents/runs/${runId}`, documentRun);
        if (run.status === "queued" || run.status === "running") {
          setStage(stageWords(run.stage));
          return;
        }
        setRunId("");
        setFailure(run.status === "succeeded" ? "" : `The pack was not built (${runReason(run.reason, run.status)}). Earlier revisions are unchanged.`);
        // The new revision is shown when its stage is the one on screen; another
        // stage keeps the revision it shows.
        if (run.pack && run.pack.stage === current?.id) setRevision(run.pack.revision);
        reloadWs();
        reloadPack();
      } catch {
        setRunId("");
        setFailure("The pack run could not be read.");
      }
    },
    600,
    runId !== "",
  );

  const generate = async () => {
    if (!current) return;
    setFailure("");
    const accepted = await start.run(() =>
      request("POST", `${base}/stages/${current.id}/pack`, runAccepted, { body: {}, headers: { "Idempotency-Key": newRequestId() } }),
    );
    if (accepted) {
      setRevision(0);
      attached.current = accepted.runId;
      setRunId(accepted.runId);
      setRunFor(current.id);
    }
  };
  // The status change quotes the row version read, so a change made elsewhere
  // since is refused rather than overwritten.
  const moveTo = async (status: string) => {
    if (entry && (await move.run(() => patch(`/api/candidacies/${candidacyId}`, statusChangeResult, { status, expectedVersion: entry.version })))) {
      setMoved(status);
      reloadRow();
    }
  };
  const addNext = () => {
    const d = addSummary.current?.parentElement as HTMLDetailsElement | undefined;
    if (d) d.open = true;
    addSummary.current?.focus();
  };
  // A cancel or restore records the stage again against the revision read.
  const setCancelled = async (st: InterviewStage, cancelled: boolean) => {
    const { recordedAt: _, revision, ...rest } = st;
    const saved = await cancel.run(() => post(`${base}/stages`, interviewStage, { ...rest, cancelled, expectedRevision: revision }));
    if (saved) {
      if (cancelled) setUndo((u) => ({ st: saved, n: (u?.n ?? 0) + 1 }));
      setPicked(cancelled ? "" : st.id);
      reloadWs();
    }
    return saved !== undefined;
  };
  const calendar = (st: InterviewStage) => {
    const about = `Prep pack: ${location.origin}/tracker?interview=${candidacyId}${st.interviewers.length ? `\nWith ${st.interviewers.join(", ")}` : ""}`;
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([ics(candidacyId, st, `${label}: ${TYPE_LABEL[st.type] ?? st.type}`, about, entry?.postingUrl ?? "")], { type: "text/calendar" }));
    a.download = `${st.id}.ics`;
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 0);
  };
  // A pack's gap or next action links the page that settles it, or moves to
  // the part of this drawer that does (the stage is the one shown).
  const focusIn = (field: string) => {
    const at = stageAt.current?.querySelector<HTMLElement>(field);
    at?.scrollIntoView?.({ block: "center" });
    at?.focus();
  };
  const posting = encodeURIComponent(entry?.postingUrl ?? "");
  // A workspace step names its stage (target), which is shown first. Another
  // stage has its own debrief and practice forms, so the switch asks first
  // while the shown ones hold unsent text; so does cancelling the shown stage,
  // which takes its forms away.
  const show = (id = "", then: () => void) => {
    const go = () => {
      if (id && id !== current?.id) (setPicked(id), setRevision(0));
      setEditing(false);
      then();
    };
    if (id && id !== current?.id) leave(go);
    else go();
  };
  const named = (s: InterviewStep) => stages.find((st) => st.id === (s.target || current?.id));
  const fix = (s: InterviewStep) => {
    switch (s.kind) {
      case "stage":
        return <button type="button" className="small" onClick={addNext}>Add a stage</button>;
      case "pack":
        return <button type="button" className="small" onClick={() => show(s.target, () => setTimeout(() => focusIn("[data-generate]")))}>Open stage {s.target}</button>;
      case "story":
        return <Link to="/wiki?tab=stories">Open the story bank</Link>;
      case "brief":
        return <Link to={`/wiki?tab=briefs&company=${encodeURIComponent(s.target ?? "")}&url=${posting}`}>Build the company brief</Link>;
      case "evaluate":
        return <Link to={`/?post=${posting}`}>Evaluate the posting</Link>;
      case "documents":
        return <Link to={`/tracker?row=${candidacyId}&documents=${candidacyId}`}>Open the résumé and cover letter</Link>;
      case "confirm":
        return named(s)?.userConfirmed !== false ? null : <button type="button" className="small" onClick={() => show(s.target, () => (setEditing(true), setTimeout(() => focusIn("form input"))))}>Confirm the stage details</button>;
      case "practice":
        return <button type="button" className="small" onClick={() => focusIn("[aria-label=Practice] :is(select, textarea)")}>Go to practice</button>;
      case "debrief":
        return <button type="button" className="small" onClick={() => focusIn("[aria-label=Feedback] textarea")}>Record the debrief</button>;
    }
    return null;
  };
  // The debrief leads a stage that is over; before it, the pack does.
  const feedback = current && ws.data && <FeedbackForm key={`feedback:${current.id}`} base={`${base}/stages/${current.id}`} feedback={ws.data.feedback.filter((f) => f.stage === current.id)} onSaved={reloadWs} />;
  // The debrief and practice forms hold the drawer while they have unsent text.
  const close = () => leave(onClose);

  return (
    <Modal title={`Interview prep: ${label}`} variant="drawer" onClose={close} returnFocus={returnFocus}>
      {ws.status === "loading" && <Loading />}
      {ws.status === "error" && <ErrorNote error={ws.error} onRetry={reloadWs} what="the interview stages" />}
      {ws.data?.degraded && (
        <p role="note" className="note error">
          {ws.data.repair}
        </p>
      )}
      {ws.data && (
        <>
          {stages.length === 0 ? <Empty>No interview stage is recorded for this application.</Empty> : (
            <ul className="plain stack" aria-label="Stages">
              {stages.map((s) => (
                <StageRow key={s.id} st={s} selected={s.id === current?.id} packs={ws.data!.packs.filter((p) => p.stage === s.id).length} debriefed={ws.data!.feedback.some((f) => f.stage === s.id)} onSelect={() => show(s.id, () => setRevision(0))} />
              ))}
            </ul>
          )}
          <div role="status">
            {undo && <UndoNote key={undo.n} done={`Cancelled stage ${undo.st.id}.`} undone={`Stage ${undo.st.id} is back.`} onUndo={() => setCancelled(undo.st, false)} />}
          </div>
          {cancel.error && <ErrorNote error={cancel.error} />}
          {ws.data.cancelled.length > 0 && (
            <details>
              <summary>Cancelled stages ({ws.data.cancelled.length})</summary>
              <ul className="plain stack">
                {ws.data.cancelled.map((s) => (
                  <li key={s.id}>
                    {s.id}, {TYPE_LABEL[s.type] ?? s.type}{s.at && `, ${when(s.at)}`}{" "}
                    <button type="button" className="small" disabled={cancel.busy} onClick={() => void setCancelled(s, false)}>
                      Restore
                    </button>
                  </li>
                ))}
              </ul>
            </details>
          )}
          {/* The shown stage's own steps are its Confirm details and Generate buttons. */}
          {ws.data.nextActions.filter((s) => !s.target || s.target !== current?.id).map((s) => (
            <p key={s.text} className="muted">
              {s.text} {fix(s)}
            </p>
          ))}
          {stages.length > 0 && !moved && entry && BEFORE_INTERVIEW.has(entry.status) && !(over && !ahead) && (
            <div className="note" role="note">
              The tracker still shows this application as {entry.status}.{" "}
              <button type="button" className="small" onClick={() => void moveTo("interview")} disabled={move.busy}>
                Move it to Interview
              </button>
              {move.error && <ErrorNote error={move.error} onRetry={reloadRow} what="the application" />}
            </div>
          )}
          {over && !ahead && !moved && entry && (BEFORE_INTERVIEW.has(entry.status) || entry.status === "interview") && (
            <div className="note" role="note">
              This interview is over. Record how it went below, then:{" "}
              <button type="button" className="small" onClick={addNext}>
                Add the next stage
              </button>{" "}
              <button type="button" className="small" onClick={() => void moveTo("offer")} disabled={move.busy}>
                Record an offer
              </button>{" "}
              <button type="button" className="small" onClick={() => void moveTo("rejected")} disabled={move.busy}>
                Record a rejection
              </button>
              {move.error && <ErrorNote error={move.error} onRetry={reloadRow} what="the application" />}
            </div>
          )}
          {moved && (
            <p className="muted" role="status" tabIndex={-1} ref={movedNote}>
              The tracker shows this application as {humanize(moved)} now.
            </p>
          )}
          {current && (
            <section aria-label={`Stage ${current.id}`} ref={stageAt}>
              <h3>
                {current.id} <span className="muted">{TYPE_LABEL[current.type] ?? humanize(current.type)}</span>
              </h3>
              <p>
                {current.at ? when(current.at) : "No date yet."}{" "}
                <button type="button" className="small" ref={editButton} aria-expanded={editing} onClick={() => setEditing(!editing)}>
                  {current.userConfirmed ? "Edit details" : "Confirm details"}
                </button>{" "}
                {current.at && (
                  <button type="button" className="small" onClick={() => calendar(current)}>
                    Add to calendar
                  </button>
                )}{" "}
                <button type="button" className="small" disabled={cancel.busy} onClick={() => leave(() => void setCancelled(current, true))}>
                  Cancel stage
                </button>
              </p>
              {editing && (
                <AddStage key={current.id + current.recordedAt} base={base} initial={current} onSaved={() => { setEditing(false); reloadWs(); editButton.current?.focus(); }} />
              )}
              <p className="muted">
                {runId ? <>Building the pack{runFor && runFor !== current.id ? ` of stage ${runFor}` : ""}{stage ? ` (${stage})` : ""}. It goes on if you close this. <Link to={`/runs/${runId}`}>Follow the run</Link></> : "A pack is built from what you submitted, the company brief, your story bank and the feedback recorded so far."}
              </p>
              <div className="actions">
                <button type="button" data-generate onClick={generate} disabled={start.busy || runId !== ""}>
                  {revisions.length === 0 ? "Generate prep pack" : "Generate a new revision"}
                </button>
                {revisions.length > 1 && (
                  <label>
                    Revision{" "}
                    <select value={shown} onChange={(e) => setRevision(Number(e.target.value))}>
                      {revisions.map((r) => (
                        <option key={r} value={r}>
                          {r}, {formatDateTime(infos.find((p) => p.revision === r)!.createdAt)}
                        </option>
                      ))}
                    </select>
                  </label>
                )}
                {pack.data && <CopyButton text={asText(pack.data, current, titles)} label={`Copy prep pack revision ${pack.data.revision} as Markdown`} />}
              </div>
              <div aria-live="polite">
                {start.error && <ErrorNote error={start.error} />}
                {failure && <p className="note error">{failure}</p>}
              </div>
              {pack.data && (
                <p className="muted">
                  Revision {pack.data.revision}, built {formatDateTime(built)}{pack.data.generation === "model" ? ", worded with a model" : ""}.
                  {since.length > 0 && ` Since then ${since.join(" and ")}; generate a new revision to include it.`}
                </p>
              )}
              {pack.status === "error" && <ErrorNote error={pack.error} onRetry={reloadPack} what="the prep pack" />}
              {pack.status === "loading" && <Loading label="Loading the prep pack" />}
              {over && feedback}
              {pack.data && <PackView pack={pack.data} storyTitles={titles} onPractice={(q) => setPractice({ q, n: (practice?.n ?? 0) + 1 })} fix={fix} />}
              {!over && feedback}
              <MockPractice key={`practice:${current.id}`} base={`${base}/stages/${current.id}`} prompts={pack.data?.questions.map((q) => q.prompt) ?? []} storiesOf={(q) => pack.data?.questions.find((x) => x.prompt === q)?.storyIds.map((id) => titles.get(id) ?? id) ?? []} themes={coverage.data?.principles ?? []} turns={ws.data.mockTurns.filter((t) => t.stage === current.id)} coachRun={coachRun?.stage === current.id ? coachRun : undefined} seed={practice} onSaved={reloadWs} />
            </section>
          )}
          {/* Adding a stage is occasional, so its form waits behind a summary
              once a stage exists and the selected stage leads the drawer. */}
          <details open={stages.length === 0} className="add-stage">
            <summary ref={addSummary}>Add a stage</summary>
            <AddStage base={base} onSaved={(id) => { show(id, () => setRevision(0)); reloadWs(); addSummary.current?.focus(); }} />
          </details>
        </>
      )}
      <div className="actions">
        <button type="button" onClick={close}>
          Close
        </button>
      </div>
    </Modal>
  );
}
