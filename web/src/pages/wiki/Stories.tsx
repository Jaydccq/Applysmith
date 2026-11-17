import { useEffect, useId, useRef, useState, type FormEvent, type ReactNode } from "react";
import { del, get, newRequestId, patch, post, request } from "../../api/client";
import { kbCoverage, kbDocument, kbDocumentList, kbDraft, kbMiningList, kbMiningSession, kbUndo, kbQuadrantSuggestions, kbStoryProposals, kbStorySaved } from "../../api/decoders_pages";
import { runAccepted } from "../../api/decoders_runs";
import { asApiError, useResource } from "../../hooks";
import { Link, leave } from "../../router";
import { ConfirmDialog } from "../../ui/Modal";
import { Chip, Empty, ErrorNote, Panel, UndoNote, ageLabel } from "../../ui/parts";
import { Changed, SaveBar } from "../../ui/SaveBar";
import { KBDocSnippet, type KBDocEntry, type KBMiningSession, type KBPrincipleGrade, type KBStoryProposal, type KBStorySaved, type KBStoryUse } from "../../wire";
import { useAction } from "../action";
import { useRunLines } from "../discovery/runLines";
import { Editor, TextUnsearched, Themes, UndoDelete, useTextMatches, type Gone } from "./Pages";

// The origins a story can be written from. The server accepts exactly these.
// The wire has no enum for it, so this list mirrors kb.SourceTypes in Go.
const ORIGINS = [
  ["project", "Project"],
  ["work", "Work"],
  ["team-conflict", "Team conflict"],
  ["customer", "Customer"],
  ["leadership", "Leadership"],
  ["failure-learning", "Failure and learning"],
] as const;
const ORIGIN_LABEL: Record<string, string> = Object.fromEntries(ORIGINS);

interface Draft {
  title: string;
  sourceType: string;
  principles: string[];
  situation: string;
  task: string;
  action: string;
  result: string;
}
const blank: Draft = { title: "", sourceType: "work", principles: [], situation: "", task: "", action: "", result: "" };

function Saved({ saved }: { saved: KBStorySaved }) {
  return (
    <div className={saved.published ? "note ok" : "note error"} role="status">
      <p>{saved.published ? "Saved to your story bank." : "Saved as a draft. It cannot be published until the findings below are fixed."}</p>
      {saved.findings.length > 0 && (
        <ul>
          {saved.findings.map((f) => (
            <li key={f.code + f.message}>
              {f.severity}: {f.message}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function AddStory({ themes, initial, onSaved, onClose }: { themes: KBPrincipleGrade[]; initial: Draft; onSaved: () => void; onClose: () => void }) {
  const [f, setF] = useState(initial);
  const [base, setBase] = useState(initial);
  const [savedAt, setSavedAt] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [saved, setSaved] = useState<KBStorySaved | null>(null);
  const first = useRef<HTMLInputElement>(null);
  useEffect(() => first.current?.focus(), []);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      setSaved(await post("/api/kb/stories", kbStorySaved, f));
      setBase(f);
      setSavedAt(new Date().toLocaleTimeString());
      onSaved();
    } catch (err) {
      setError(asApiError(err));
    } finally {
      setBusy(false);
    }
  };
  const field = (key: keyof Draft, label: string, multiline = true) => (
    <label>
      {label}
      {multiline ? <textarea className="editor" value={f[key] as string} onChange={(e) => setF({ ...f, [key]: e.target.value })} /> : <input value={f[key] as string} onChange={(e) => setF({ ...f, [key]: e.target.value })} />}
    </label>
  );
  const ready = [f.title, f.situation, f.task, f.action, f.result].every((s) => s.trim() !== "");
  return (
    <form onSubmit={submit} className="form-grid panel" aria-label="Add a story">
      <h3>Add a story</h3>
      <label>
        Title
        <input ref={first} value={f.title} onChange={(e) => setF({ ...f, title: e.target.value })} />
      </label>
      <label>
        Origin
        <select value={f.sourceType} onChange={(e) => setF({ ...f, sourceType: e.target.value })}>
          {ORIGINS.map(([id, name]) => (
            <option key={id} value={id}>
              {name}
            </option>
          ))}
        </select>
      </label>
      <Themes themes={themes} picked={f.principles} onChange={(principles) => setF({ ...f, principles })} />
      {field("situation", "Situation")}
      {field("task", "Task")}
      {field("action", "Action")}
      {field("result", "Result")}
      {error != null && <ErrorNote error={error} />}
      {saved && <Saved saved={saved} />}
      <SaveBar dirty={JSON.stringify(f) !== JSON.stringify(base)} savedAt={savedAt}>
        <button type="button" onClick={() => leave(onClose)}>
          Close
        </button>
        <button type="submit" disabled={busy || !ready}>
          Save story
        </button>
      </SaveBar>
    </form>
  );
}

function Proposals({ themes, onSaved }: { themes: KBPrincipleGrade[]; onSaved: () => void }) {
  const [list, setList] = useState<KBStoryProposal[] | null>(null);
  const [run, setRun] = useState<string>();
  const [error, setError] = useState<unknown>(null);
  const [saved, setSaved] = useState<Record<string, KBStorySaved>>({});
  const [approving, setApproving] = useState<string | null>(null);
  // The last dismissal, keyed by the press so each one offers its own Undo.
  const [dismissed, setDismissed] = useState<{ p: KBStoryProposal; n: number }>();
  // A proposal carries no origin, so each starts as work and the user changes it.
  const [origin, setOrigin] = useState<Record<string, string>>({});
  const names = new Map(themes.map((t) => [t.id, t.name]));
  const act = useAction();

  // The server keeps what the last run found, so a visit after the run ended
  // (or after leaving the page) still shows it.
  const load = () => {
    get("/api/kb/stories/proposals", kbStoryProposals).then(
      (r) => {
        setList(r.proposals);
        setSaved({});
      },
      (e) => setError(asApiError(e)),
    );
  };
  useEffect(load, []);
  const { final } = useRunLines(run, load);
  const working = run !== undefined && !final;
  const ask = async () => {
    setError(null);
    setRun(undefined);
    setDismissed(undefined);
    const out = await act.run(() => request("POST", "/api/kb/stories/proposals", runAccepted, { headers: { "Idempotency-Key": newRequestId() } }));
    if (out) setRun(out.runId);
  };
  const approve = async (p: KBStoryProposal) => {
    setError(null);
    setApproving(p.title);
    try {
      const r = await post("/api/kb/stories", kbStorySaved, { ...p, sourceType: origin[p.title] ?? blank.sourceType });
      setSaved((s) => ({ ...s, [p.title]: r }));
      onSaved();
    } catch (e) {
      setError(asApiError(e));
    } finally {
      setApproving(null);
    }
  };
  // A dismissal is kept by the server, so a later visit or run does not bring
  // the proposal back; Undo puts it back in place.
  const pick = async (p: KBStoryProposal, verb: "dismiss" | "restore") => {
    setError(null);
    try {
      setList((await post(`/api/kb/stories/proposals/${verb}`, kbStoryProposals, { title: p.title })).proposals);
      if (verb === "dismiss") setDismissed((d) => ({ p, n: (d?.n ?? 0) + 1 }));
      return true;
    } catch (e) {
      setError(asApiError(e));
      return false;
    }
  };

  return (
    <div>
      <p className="actions">
        <button type="button" disabled={act.busy || working} onClick={() => void ask()}>
          {working ? "Reading your CV..." : "Propose stories from my CV"}
        </button>
        <span className="muted">Nothing is saved until you approve a proposal.</span>
      </p>
      <div aria-live="polite">
        {working && <p className="muted" role="status">The model is reading your CV. It goes on if you leave this page. <Link to={`/runs/${run}`}>Follow the run</Link></p>}
        {final?.status === "failed" && <p className="note">{final.reason}</p>}
        {final?.status === "cancelled" && <p className="muted">The run was cancelled.</p>}
      </div>
      {act.error && <ErrorNote error={act.error} />}
      {error != null && <ErrorNote error={error} />}
      <div role="status">{dismissed && <UndoNote key={dismissed.n} done={`Dismissed ${dismissed.p.title}.`} undone={`${dismissed.p.title} is back in the list.`} onUndo={() => pick(dismissed.p, "restore")} />}</div>
      {final?.status === "succeeded" && list?.length === 0 && !dismissed && <Empty>The model found no story your CV supports.</Empty>}
      {list && list.length > 0 && (
        <ul className="plain stack" aria-label="Proposed stories">
          {list.map((p) => (
            <li key={p.title} className="panel">
              <strong>{p.title}</strong>
              <p className="chips">{p.principles.map((id) => <Chip key={id}>{names.get(id) ?? id}</Chip>)}</p>
              <dl className="facts">
                <dt>Situation</dt><dd>{p.situation}</dd>
                <dt>Task</dt><dd>{p.task}</dd>
                <dt>Action</dt><dd>{p.action}</dd>
                <dt>Result</dt><dd>{p.result}</dd>
              </dl>
              {saved[p.title] ? (
                <Saved saved={saved[p.title]} />
              ) : (
                <div className="actions">
                  <label>
                    Origin of this story
                    <select value={origin[p.title] ?? blank.sourceType} onChange={(e) => setOrigin((o) => ({ ...o, [p.title]: e.target.value }))} aria-label={`Origin of ${p.title}`}>
                      {ORIGINS.map(([id, label]) => (
                        <option key={id} value={id}>
                          {label}
                        </option>
                      ))}
                    </select>
                  </label>
                  <button type="button" disabled={approving !== null} onClick={() => void approve(p)}>
                    Approve and save
                  </button>
                  <button type="button" onClick={() => void pick(p, "dismiss")} aria-label={`Dismiss ${p.title}`}>
                    Dismiss
                  </button>
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

const QUADRANTS: Record<string, string> = { project: "A project", team: "A team conflict", customer: "A customer", failure: "A failure and its lesson" };
const PARTS = [["situation", "Situation"], ["task", "Task"], ["tension", "What made it hard"], ["action", "Action"], ["result", "Result"]];
const STATES: Record<string, string> = { collecting: "in progress", ready: "every part answered", saved: "saved as a draft", abandoned: "stopped" };
const unfinished = (m: KBMiningSession) => m.state === "collecting" || m.state === "ready";

// Interview asks for a story one part at a time; the server keeps each answer,
// so it continues later, and an answered part can be edited until the story is
// saved. A finished one is saved as a draft and, with two themes picked, also
// confirmed and published.
function Interview({ themes, onSaved }: { themes: KBPrincipleGrade[]; onSaved: () => void }) {
  const [list, reloadList] = useResource(() => get("/api/kb/stories/mining", kbMiningList), []);
  const [sug] = useResource(() => get("/api/kb/stories/mining/suggestions", kbQuadrantSuggestions), []);
  const [quad, setQuad] = useState("");
  const [picked, setPicked] = useState<KBMiningSession | null>(null);
  const [answer, setAnswer] = useState("");
  const [chosen, setChosen] = useState<string[]>([]);
  const [said, setSaid] = useState<ReactNode>(null);
  const [doomed, setDoomed] = useState<KBMiningSession | null>(null);
  const [gone, setGone] = useState<Gone | null>(null);
  // The part being edited, its text and the transcript length it was opened
  // against (the server refuses the edit when the session moved on since).
  const [ed, setEd] = useState<{ field: string; text: string; base: string; rev: number }>();
  const edits = useRef<Record<string, HTMLButtonElement | null>>({});
  const editBox = useRef<HTMLTextAreaElement>(null);
  const after = useRef<() => HTMLElement | null | undefined>(undefined);
  const box = useRef<HTMLTextAreaElement>(null);
  const card = useRef<HTMLElement>(null);
  const begin = useRef<HTMLButtonElement>(null);
  const focus = useRef(false);
  const field = useId();
  const act = useAction();
  const stale = ed !== undefined && act.error?.kind === "revision_conflict";
  const q = quad || (sug.data?.quadrants[0]?.quadrant ?? "project");
  const reasons = sug.data?.quadrants.find((x) => x.quadrant === q)?.reasons ?? [];
  const sessions = list.data?.sessions ?? [];
  const m = picked ?? sessions.find(unfinished) ?? null;
  const others = sessions.filter((s) => s.id !== m?.id);
  const name = (s: KBMiningSession) => QUADRANTS[s.quadrant] ?? s.quadrant;
  // The keyboard follows the interview after a press, never on a page load.
  useEffect(() => {
    if (after.current) after.current()?.focus();
    else if (focus.current) (box.current ?? card.current ?? begin.current)?.focus();
    focus.current = false;
    after.current = undefined;
  });
  const show = (s: KBMiningSession | null) => ((focus.current = true), setPicked(s), setAnswer(""), s?.id !== m?.id && setEd(undefined));
  const start = async (e: FormEvent) => {
    e.preventDefault();
    setSaid(null);
    const s = await act.run(() => post("/api/kb/stories/mining", kbMiningSession, { quadrant: q }));
    if (s) (show(s), reloadList());
  };
  const reply = async (e: FormEvent) => {
    e.preventDefault();
    const s = await act.run(() => post(`/api/kb/stories/mining/${m!.id}/replies`, kbMiningSession, { text: answer, expectedRevision: m!.log.length }));
    if (s) show(s);
  };
  const part = (id: string) => PARTS.find(([p]) => p === id)?.[1] ?? id.charAt(0).toUpperCase() + id.slice(1);
  // A session started from an application question opens its transcript with
  // it and asks that story's own parts, in the order of the transcript.
  const target = (s: KBMiningSession) => (s.log[0]?.role === "target" ? s.log[0].text : "");
  const answered = (s: KBMiningSession) => (target(s) ? [...new Set(s.log.slice(1).map((e) => e.field))] : PARTS.map(([p]) => p)).filter((id) => s.fields[id]?.text);
  const editPart = (id: string) => (setSaid(null), act.clear(), (after.current = () => editBox.current), setEd({ field: id, text: m!.fields[id].text, base: m!.fields[id].text, rev: m!.log.length }));
  const closeEdit = (field: string) => ((after.current = () => edits.current[field]), setEd(undefined), act.clear());
  const revise = async (e: FormEvent) => {
    e.preventDefault();
    const { field, text, rev } = ed!;
    const s = await act.run(() => post(`/api/kb/stories/mining/${m!.id}/replies`, kbMiningSession, { field, text, expectedRevision: rev }));
    if (s) (setPicked(s), setSaid(`Saved your new answer for ${part(field)}.`), closeEdit(field));
  };
  // After a refused edit: keep the text over the session as it is now, so the
  // next save replaces that, or drop the edit and show the session as it is.
  const reread = async (keep: boolean) => {
    const s = await act.run(() => get(`/api/kb/stories/mining/${m!.id}`, kbMiningSession));
    if (!s) return;
    setPicked(s);
    if (keep) ((after.current = () => editBox.current), setEd({ ...ed!, rev: s.log.length }));
    else closeEdit(ed!.field);
  };
  const finish = async () => {
    setSaid(null);
    const done = await act.run(async () => {
      let d = await post(`/api/kb/stories/mining/${m!.id}/save`, kbDraft);
      setSaid(<>Saved as a draft. It waits under <Link to="/wiki">Pages, Drafts to review</Link>.</>);
      if (chosen.length < 2) return true;
      d = await patch(`/api/kb/drafts/${d.id}`, kbDraft, { principles: chosen });
      d = await post(`/api/kb/drafts/${d.id}/confirm`, kbDraft, {});
      await post(`/api/kb/drafts/${d.id}/publish`, kbDocument);
      setSaid("Saved to your story bank.");
      return true;
    });
    // The saved note is announced; the keyboard goes on to the next interview.
    if (done) ((after.current = () => begin.current), setChosen([]), onSaved());
    show(null);
    reloadList();
  };
  const remove = async (s: KBMiningSession) => {
    setSaid(null);
    const out = await act.run(() => del(`/api/kb/stories/mining/${s.id}`, kbUndo));
    if (out) (setGone({ undo: out.undo, done: "Deleted the interview.", undone: "The interview is back." }), show(null), reloadList());
  };
  const stop = (s: KBMiningSession) => <button type="button" className="danger-outline" onClick={() => setDoomed(s)}>Stop and delete</button>;

  return <div className="form-grid">
    <h3 id="kb-interview">Story interview</h3>
    <p className="muted">Answer five short questions and your answers become the story, in your own words. Each answer is kept as you go, so you can stop and continue later.</p>
    <form onSubmit={start} className="actions">
      <label>Kind of story <select value={q} onChange={(e) => setQuad(e.target.value)}>{Object.entries(QUADRANTS).map(([id, n]) => <option key={id} value={id}>{n}</option>)}</select></label>
      <button type="submit" ref={begin} disabled={act.busy}>Start an interview</button>
    </form>
    {reasons.length > 0 && <p className="muted">Suggested: {reasons.slice(0, 2).join("; ")}.</p>}
    <div aria-live="polite">{said && <p className="note ok">{said}</p>}{gone && <UndoDelete key={gone.undo} gone={gone} onBack={reloadList} />}</div>
    {act.error && !stale && <ErrorNote error={act.error} />}
    {list.status === "error" && <ErrorNote error={list.error} onRetry={reloadList} what="the interviews" />}
    {others.length > 0 && <ul className="plain item-list" aria-label="Other interviews">{others.map((s) => <li key={s.id}>
      <span className="item-main">{name(s)}<span className="item-detail"> {STATES[s.state] ?? s.state}, started {ageLabel(s.createdAt)}</span></span>
      {unfinished(s) && <button type="button" className="small" onClick={() => leave(() => show(s))}>Continue</button>}
      <button type="button" className="small danger-outline" onClick={() => setDoomed(s)} aria-label={`Delete the interview started ${ageLabel(s.createdAt)}`}>Delete</button>
    </li>)}</ul>}
    {m && <section ref={card} tabIndex={-1} className="panel" aria-label={`Interview: ${name(m)}`}>
      <strong>{name(m)}</strong>
      {target(m) && <p className="muted">For the question: {target(m)}</p>}
      <dl className="facts">{answered(m).map((id) => [id, part(id)]).map(([id, n]) => <div key={id} style={{ display: "contents" }}><dt>{n}</dt>
        {ed?.field === id ? <dd><form onSubmit={revise} className="form-grid">
          <textarea ref={editBox} className="editor" aria-label={`Your answer: ${n}`} value={ed.text} onChange={(e) => setEd({ ...ed, text: e.target.value })} />
          {stale && <Changed onKeep={() => void reread(true)} onLoad={() => void reread(false)} />}
          <SaveBar dirty={ed.text.trim() !== ed.base.trim()} savedAt=""><button type="button" onClick={() => closeEdit(id)}>Cancel</button><button type="submit" disabled={act.busy || ed.text.trim() === "" || ed.text.trim() === ed.base.trim()}>Save answer</button></SaveBar>
        </form></dd> : <dd className="pre">{m.fields[id].text} <button type="button" className="small" ref={(b) => { edits.current[id] = b; }} aria-label={`Edit ${n}`} onClick={() => (ed && ed.text.trim() !== ed.base.trim() ? leave(() => editPart(id)) : editPart(id))}>Edit</button></dd>}</div>)}</dl>
      {m.next ? <form onSubmit={reply} className="form-grid">
        <label htmlFor={field} aria-live="polite">{m.next.text}</label>
        <textarea id={field} ref={box} className="editor" value={answer} onChange={(e) => setAnswer(e.target.value)} />
        <SaveBar dirty={answer.trim() !== ""} savedAt="">{stop(m)}<button type="submit" disabled={act.busy || answer.trim() === ""}>Answer</button></SaveBar>
      </form> : <div className="form-grid">
        <p>Every part is answered. Pick at least two themes the story shows to put it in the story bank now, or save it as a draft and pick them later.</p>
        <Themes themes={themes} picked={chosen} onChange={setChosen} />
        <div className="actions"><button type="button" className="primary" disabled={act.busy} onClick={() => void finish()}>{chosen.length < 2 ? "Save as a draft" : "Save to the story bank"}</button>{stop(m)}</div>
      </div>}
    </section>}
    {doomed && <ConfirmDialog title="Delete this interview" verb="Delete interview" consequence="Its answers are deleted, and so is a draft made from them that is not published yet. A story already in the bank stays. You can undo it for a few minutes."
      onCancel={() => setDoomed(null)} onConfirm={() => { const gone = doomed; setDoomed(null); void remove(gone); }} />}
  </div>;
}

function StoryRow({ s, names, uses, onDelete, onSaved }: { s: KBDocEntry; names: Map<string, string>; uses: KBStoryUse[]; onDelete: () => void; onSaved: () => void }) {
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState(false);
  const [res, reload] = useResource(() => (open ? get(`/api/kb/documents/${encodeURIComponent(s.id)}`, kbDocument) : Promise.resolve(null)), [open, s.id]);
  return (
    <li className="panel">
      <div className="row">
        <strong>{s.title || s.id}</strong>
        <Chip>{ORIGIN_LABEL[s.sourceType] ?? (s.sourceType || "unknown origin")}</Chip>
      </div>
      <p className="chips">{s.principles.map((id) => <Chip key={id}>{names.get(id) ?? id}</Chip>)}</p>
      <p className="muted">
        Edited {ageLabel(s.modifiedAt)}.{" "}
        {uses.length === 0 ? "Not used in any interview prep yet." : <>Used in interview prep for {uses.map((u, i) => <span key={u.candidacyId}>{i > 0 && ", "}<Link to={`/tracker?interview=${u.candidacyId}`}>{u.company}, {u.role}</Link></span>)}.</>}
      </p>
      {s.lint.length > 0 && <ul className="note">{s.lint.map((l) => <li key={l.code + l.message}>{l.message}</li>)}</ul>}
      <div className="actions">
        <button type="button" aria-expanded={open} onClick={() => setOpen(!open)}>
          {open ? "Hide the story" : "Show the story"}
        </button>
        <button type="button" aria-expanded={editing} onClick={() => (editing ? leave(() => setEditing(false)) : setEditing(true))} aria-label={`Edit ${s.title || s.id}`}>
          Edit
        </button>
        <button type="button" className="danger-outline" onClick={onDelete} aria-label={`Delete ${s.title || s.id}`}>
          Delete
        </button>
      </div>
      {open && res.status === "error" && <ErrorNote error={res.error} onRetry={reload} />}
      {open && res.data && (
        <dl className="facts">
          {res.data.sections.map((sec) => (
            <div key={sec.heading} style={{ display: "contents" }}>
              <dt>{sec.heading}</dt>
              <dd className="pre">{sec.text}</dd>
            </div>
          ))}
        </dl>
      )}
      {editing && <Editor entry={s} noun="story" onClose={() => setEditing(false)} onSaved={() => (onSaved(), open && reload())} />}
    </li>
  );
}

export function Stories() {
  const [cov, reloadCov] = useResource(() => get("/api/kb/stories/coverage", kbCoverage), []);
  const [docs, reloadDocs] = useResource(() => get("/api/kb/documents", kbDocumentList), []);
  const [form, setForm] = useState<Draft | null>(null);
  const [doomed, setDoomed] = useState<KBDocEntry | null>(null);
  const [gone, setGone] = useState<Gone | null>(null);
  const [removeError, setRemoveError] = useState<unknown>(null);
  const [query, setQuery] = useState("");
  const inText = useTextMatches(query.trim());
  const search = useRef<HTMLInputElement>(null);
  const reload = () => {
    reloadCov();
    reloadDocs();
  };
  const themes = cov.data?.principles ?? [];
  const names = new Map(themes.map((t) => [t.id, t.name]));
  // The first allowed origin that evidences the theme; the grade suggests more.
  const originFor = (t: KBPrincipleGrade) => ORIGINS.find(([id]) => t.suggest.includes(id))?.[0] ?? "work";

  const remove = async (s: KBDocEntry) => {
    setRemoveError(null);
    try {
      const { undo } = await del(`/api/kb/documents/${encodeURIComponent(s.id)}`, kbUndo);
      setGone({ undo, done: `Deleted ${s.title || s.id}.`, undone: `${s.title || s.id} is back in the story bank.` });
      reload();
    } catch (e) {
      setGone(null);
      setRemoveError(asApiError(e));
    }
  };

  return (
    <div>
      <Panel title="Story bank" id="kb-bank" res={docs} retry={reloadDocs}>
        {(list) => {
          const delivered = (
            <div aria-live="polite">
              {gone && <UndoDelete key={gone.undo} gone={gone} onBack={reload} />}
              {removeError != null && <ErrorNote error={removeError} />}
            </div>
          );
          const stories = list.entries.filter((e) => e.kind === KBDocSnippet && e.category === "behavioral");
          const usesOf = (s: KBDocEntry) => list.uses.filter((u) => u.storyId === s.id);
          const q = query.trim().toLowerCase();
          const shown = stories.filter((s) => q === "" || [s.title, s.id, ORIGIN_LABEL[s.sourceType] ?? "", ...s.tags, ...s.principles.map((id) => names.get(id) ?? id), ...usesOf(s).map((u) => u.company)].some((t) => t.toLowerCase().includes(q)) || inText?.ids.has(s.id));
          return (
            <>
              {stories.length > 0 && (
                <div className="toolbar">
                  <label className="search-field">
                    Search stories by title, theme, company or text
                    <input ref={search} type="search" data-page-search value={query} onChange={(e) => setQuery(e.target.value)} />
                  </label>
                  <span className="count-line">{q === "" ? stories.length : `${shown.length} of ${stories.length}`} {stories.length === 1 ? "story" : "stories"}</span>
                </div>
              )}
              {delivered}
              {q !== "" && <TextUnsearched matches={inText} />}
              {stories.length === 0 ? (
                <Empty>No story is banked yet. Add one for a theme in Coverage below, propose stories from your CV, or answer five short questions in the <a href="#kb-interview">Story interview</a>.</Empty>
              ) : shown.length === 0 ? (
                <Empty>{inText === null ? "Searching the text of your stories..." : "No story matches the search."}</Empty>
              ) : (
                <ul className="plain stack">
                  {shown.map((s) => (
                    <StoryRow key={s.id} s={s} names={names} uses={usesOf(s)} onDelete={() => setDoomed(s)} onSaved={reload} />
                  ))}
                </ul>
              )}
            </>
          );
        }}
      </Panel>
      <Panel title="Coverage" id="kb-coverage" res={cov} retry={reloadCov}>
        {(c) => (
          <>
          <p className="muted">
            {c.principles.filter((t) => t.grade === "green").length} of {c.principles.length} themes are covered. A theme is covered when the bank holds a story for it with every part a behavioral answer needs.
          </p>
          <table className="cards coverage">
            <caption className="sr-only">Competency themes against story counts</caption>
            <thead>
              <tr>
                <th scope="col">Theme</th>
                <th scope="col">Stories</th>
                <th scope="col">Status</th>
                <th scope="col">
                  <span className="sr-only">Action</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {c.principles.map((t) => {
                const gap = t.stories.length === 0;
                return (
                  <tr key={t.id}>
                    <td data-label="Theme">{t.name}</td>
                    <td data-label="Stories">
                      {t.stories.length === 0 ? 0 : <span className="row-links"><button type="button" className="small" onClick={() => (setQuery(t.name), search.current?.focus())} aria-label={`Show the ${t.name} stories in the bank`}>{t.stories.length}</button></span>}
                    </td>
                    <td data-label="Status">
                      <Chip tone={gap ? "bad" : t.grade === "green" ? "good" : "warn"}>{gap ? "Gap" : t.grade === "green" ? "Covered" : `Needs ${t.missing.join(", ")}`}</Chip>
                    </td>
                    <td className="row-actions">
                      {t.grade !== "green" && (
                        <button type="button" onClick={() => leave(() => setForm({ ...blank, sourceType: originFor(t), principles: [t.id] }))} aria-label={`Add a story for ${t.name}`}>
                          Add a story
                        </button>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          </>
        )}
      </Panel>
      <Panel title="Story shapes" id="kb-shapes" res={cov} retry={reloadCov}>
        {(c) => (
          <table className="cards shapes">
            <caption className="sr-only">Story shapes against the stories the bank holds</caption>
            <thead>
              <tr>
                <th scope="col">Shape</th>
                <th scope="col">Stories</th>
                <th scope="col">Status</th>
              </tr>
            </thead>
            <tbody>
              {c.shapes.map((sh) => (
                <tr key={sh.id}>
                  <td data-label="Shape">{sh.name}</td>
                  <td data-label="Stories">
                    {sh.have} of {sh.need}
                  </td>
                  <td data-label="Status">
                    <Chip tone={sh.grade === "green" ? "good" : "bad"}>{sh.grade === "green" ? "Covered" : "Gap"}</Chip>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Panel>
      <section className="panel" aria-labelledby="kb-add">
        <header className="panel-head">
          <h2 id="kb-add">New stories</h2>
        </header>
        <div className="actions">
          <button type="button" onClick={() => leave(() => setForm({ ...blank }))}>
            Add a story
          </button>
        </div>
        <Proposals themes={themes} onSaved={reload} />
        <hr />
        <Interview themes={themes} onSaved={reload} />
        {form && <AddStory key={JSON.stringify([form.principles, form.sourceType])} themes={themes} initial={form} onSaved={reload} onClose={() => setForm(null)} />}
      </section>
      {doomed && (
        <ConfirmDialog
          title={`Delete ${doomed.title || doomed.id}`}
          consequence="The story and everything the search index holds from it are removed. You can undo it for a few minutes."
          verb="Delete story"
          onCancel={() => setDoomed(null)}
          onConfirm={() => {
            const gone = doomed;
            setDoomed(null);
            void remove(gone);
          }}
        />
      )}
    </div>
  );
}
