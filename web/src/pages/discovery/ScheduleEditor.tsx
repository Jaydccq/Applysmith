import { useEffect, useState, type FormEvent } from "react";
import { get, patch, post, postCheck } from "../../api/client";
import { schedule, schedulePreview, scheduleKindList } from "../../api/decoders_discovery";
import { useResource } from "../../hooks";
import { leave, Link } from "../../router";
import { Modal } from "../../ui/Modal";
import { ErrorNote, humanize } from "../../ui/parts";
import { useSaveKey, useUnsaved } from "../../ui/SaveBar";
import type { Schedule, ScheduleField, SchedulePreset } from "../../wire";
import { useAction } from "../action";
import { localZone, POLICIES, shortDay, WEEKDAYS, ZONES } from "./schedule";

interface Draft {
  name: string;
  kind: string;
  presetKind: "daily" | "weekly" | "rrule";
  time: string;
  weekdays: string[];
  rrule: string;
  timezone: string;
  enabled: boolean;
  policy: string;
  inputs: Record<string, string | boolean>;
}

function draftOf(s?: Schedule): Draft {
  return {
    name: s?.name ?? "",
    kind: s?.kind ?? "",
    presetKind: (s?.preset.kind as Draft["presetKind"]) ?? "daily",
    time: s?.preset.time ?? "07:00",
    weekdays: s?.preset.weekdays ?? ["mon"],
    rrule: s?.preset.rrule ?? "",
    timezone: s?.preset.timezone ?? localZone(),
    enabled: s?.enabled ?? true,
    policy: s?.missedPolicy ?? "skip",
    inputs: Object.fromEntries(Object.entries(s?.inputs ?? {}).map(([k, v]) => [k, typeof v === "boolean" ? v : String(v)])),
  };
}

function presetOf(d: Draft): SchedulePreset {
  if (d.presetKind === "rrule") return { kind: "rrule", rrule: d.rrule.trim(), timezone: d.timezone.trim() };
  if (d.presetKind === "weekly") return { kind: "weekly", time: d.time, weekdays: d.weekdays, timezone: d.timezone.trim() };
  return { kind: "daily", time: d.time, timezone: d.timezone.trim() };
}

// fieldLabel writes an input name such as "budgetSeconds" or "topN" as words:
// "Budget seconds", "Top N".
const fieldLabel = (name: string) =>
  humanize(
    name
      .split(/(?=[A-Z])/)
      .map((w) => (w.length > 1 ? w.toLowerCase() : w))
      .join(" "),
  );

// missingOf names what the recurrence still lacks before it can be previewed.
function missingOf(d: Draft): string {
  if (d.presetKind === "rrule" && !d.rrule.trim()) return "Write a recurrence rule to see when it runs.";
  if (d.presetKind !== "rrule" && !d.time) return "Pick a time to see when it runs.";
  if (d.presetKind === "weekly" && d.weekdays.length === 0) return "Pick at least one weekday.";
  if (!d.timezone.trim()) return "Name a time zone to see when it runs.";
  return "";
}

// inputsOf keeps the inputs the chosen kind declares, with their types.
function inputsOf(d: Draft, fields: ScheduleField[]): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const f of fields) {
    const v = d.inputs[f.name];
    if (v === undefined || v === "") continue;
    out[f.name] = f.type === "int" ? Number(v) : v;
  }
  return out;
}

const key = (d: Draft) => JSON.stringify(presetOf(d));

// ScheduleEditor saves only a recurrence whose next fire times it has shown:
// the preview asks the server again a moment after the recurrence changes,
// and Save waits for the answer about exactly what is on the form.
export function ScheduleEditor({ existing, onClose, onSaved }: { existing?: Schedule; onClose: () => void; onSaved: () => void }) {
  const [draft, setDraft] = useState(() => draftOf(existing));
  const [kinds] = useResource(() => get("/api/schedule-kinds", scheduleKindList), []);
  const [previewed, setPreviewed] = useState<{ key: string; next?: string[]; error?: unknown } | null>(null);
  const save = useAction();
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setDraft((d) => ({ ...d, [k]: v }));
  const kind = kinds.data?.kinds.find((k) => k.name === draft.kind);
  useEffect(() => {
    if (!existing && !draft.kind && kinds.data?.kinds[0]) set("kind", kinds.data.kinds[0].name);
  }, [kinds.data]);

  const recurrence = key(draft);
  const missing = missingOf(draft);
  useEffect(() => {
    if (missing) return;
    let live = true;
    const timer = setTimeout(() => {
      postCheck("/api/schedules/preview", schedulePreview, { preset: presetOf(draft) }).then(
        (got) => live && setPreviewed({ key: recurrence, next: got.next }),
        (error: unknown) => live && setPreviewed({ key: recurrence, error }),
      );
    }, 300);
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [recurrence, missing]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const body = { name: draft.name.trim() || (kind?.title ?? ""), kind: draft.kind, inputs: inputsOf(draft, kind?.inputs ?? []), preset: presetOf(draft), enabled: draft.enabled, missedPolicy: draft.policy };
    const done = await save.run(() => (existing ? patch(`/api/schedules/${existing.id}`, schedule, body) : post("/api/schedules", schedule, body)));
    if (done) onSaved();
  };
  const fresh = !missing && previewed?.key === recurrence ? previewed : null;
  const setInput = (name: string, v: string | boolean) => set("inputs", { ...draft.inputs, [name]: v });
  // A new schedule starts on the first kind, which is no edit of the user's.
  const start = { ...draftOf(existing), kind: existing?.kind ?? kinds.data?.kinds[0]?.name ?? "" };
  const changed = JSON.stringify(draft) !== JSON.stringify(start);
  const ask = useUnsaved(!save.busy && changed);
  const saveKey = useSaveKey(changed);
  const close = () => leave(onClose);

  return (
    <Modal title={existing ? `Edit ${existing.name}` : "New schedule"} onClose={close}>
      <form onSubmit={submit} className="form-grid schedule-form" aria-label="Schedule">
        <h4>What runs</h4>
        <label>
          Name
          <input type="text" required={!kind} placeholder={kind?.title} value={draft.name} onChange={(e) => set("name", e.target.value)} />
        </label>
        <label>
          Job
          <select value={draft.kind} onChange={(e) => set("kind", e.target.value)}>
            {(kinds.data?.kinds ?? []).map((k) => (
              <option key={k.name} value={k.name}>
                {k.title}
              </option>
            ))}
          </select>
          {kind && (
            <span className="muted">
              {kind.doc} <code>{kind.name}</code>
            </span>
          )}
        </label>
        {(kind?.inputs ?? []).map((f) =>
          f.type === "bool" ? (
            <div key={f.name}>
              <label className="check">
                <input type="checkbox" checked={draft.inputs[f.name] === true} onChange={(e) => setInput(f.name, e.target.checked)} />
                {fieldLabel(f.name)}
              </label>
              {f.doc && <span className="help">{f.doc}</span>}
            </div>
          ) : (
            <label key={f.name}>
              {fieldLabel(f.name)}
              {f.enum ? (
                <select value={String(draft.inputs[f.name] ?? "")} onChange={(e) => setInput(f.name, e.target.value)}>
                  <option value="">Default</option>
                  {f.enum.map((v) => (
                    <option key={v}>{v}</option>
                  ))}
                </select>
              ) : (
                <input type={f.type === "int" ? "number" : "text"} min={f.min} max={f.max} value={String(draft.inputs[f.name] ?? "")} onChange={(e) => setInput(f.name, e.target.value)} />
              )}
              {f.doc && <span className="muted">{f.doc}</span>}
            </label>
          ),
        )}
        {draft.kind.startsWith("discover.") && draft.inputs.mode === "screen" && (
          <p role="note" className="note warn">
            Mode screen stops every evaluation at the quick screen, so this scan never adds a row to Apply now. Choose auto or deep to get rows to apply to.
          </p>
        )}
        <h4>When</h4>
        <label>
          Repeats
          <select value={draft.presetKind} onChange={(e) => set("presetKind", e.target.value as Draft["presetKind"])}>
            <option value="daily">Every day</option>
            <option value="weekly">On chosen weekdays</option>
            <option value="rrule">Custom recurrence rule</option>
          </select>
        </label>
        {draft.presetKind === "weekly" && (
          <fieldset className="choice">
            <legend>Weekdays</legend>
            <div className="chips weekday-picks">
              {WEEKDAYS.map((d) => (
                <label key={d} className="check">
                  <input type="checkbox" checked={draft.weekdays.includes(d)} onChange={(e) => set("weekdays", e.target.checked ? [...draft.weekdays, d] : draft.weekdays.filter((x) => x !== d))} />
                  {shortDay(d)}
                </label>
              ))}
            </div>
          </fieldset>
        )}
        {draft.presetKind !== "rrule" && (
          <label>
            Time
            <input type="time" required value={draft.time} onChange={(e) => set("time", e.target.value)} />
          </label>
        )}
        {draft.presetKind === "rrule" && (
          <label>
            Recurrence rule
            <input type="text" required placeholder="FREQ=DAILY;INTERVAL=2" value={draft.rrule} onChange={(e) => set("rrule", e.target.value)} />
            <span className="muted">An iCalendar RRULE, such as FREQ=WEEKLY;BYDAY=MO,TH;BYHOUR=9;BYMINUTE=0.</span>
          </label>
        )}
        <label>
          Time zone
          <input type="text" required list="schedule-zones" value={draft.timezone} onChange={(e) => set("timezone", e.target.value)} />
          <datalist id="schedule-zones">
            {ZONES.map((z) => (
              <option key={z} value={z} />
            ))}
          </datalist>
          <span className="muted">
            Times above are in this zone. <Link to="/settings#settings-quiet">Quiet hours</Link> do not stop a schedule; they hold its usual notifications until they end.
          </span>
        </label>
        <label>
          If the app was closed at the time
          <select value={draft.policy} onChange={(e) => set("policy", e.target.value)}>
            {POLICIES.map((p) => (
              <option key={p.id} value={p.id}>
                {p.label}
              </option>
            ))}
          </select>
        </label>
        <label className="check">
          <input type="checkbox" checked={draft.enabled} onChange={(e) => set("enabled", e.target.checked)} />
          Enabled
        </label>
        <h4 id="schedule-next-h">Next runs</h4>
        <div className="schedule-preview" aria-live="polite" aria-labelledby="schedule-next-h">
          {missing ? (
            <p className="muted">{missing}</p>
          ) : !fresh ? (
            <p className="muted">Checking when it runs…</p>
          ) : fresh.error ? (
            <ErrorNote error={fresh.error} />
          ) : (
            <>
              <ol aria-label="Next fire times">
                {(fresh.next ?? []).map((t) => (
                  <li key={t}>{new Date(t).toLocaleString([], { dateStyle: "full", timeStyle: "short" })}</li>
                ))}
              </ol>
              {draft.timezone.trim() !== localZone() && <p className="muted">Shown on this computer's clock, {localZone()}.</p>}
            </>
          )}
        </div>
        {save.error && <ErrorNote error={save.error} />}
        <div className="actions">
          <button type="button" onClick={close}>
            Cancel
          </button>
          <button type="submit" disabled={!fresh?.next || save.busy || !kinds.data} ref={saveKey}>
            Save schedule
          </button>
        </div>
      </form>
      {ask}
    </Modal>
  );
}
