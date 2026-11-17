import { ArrowDown, ArrowUp, Plus, X } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import { Empty } from "../../ui/parts";
import { Issued, ProfilePanel, SaveForm, TextField, listOf, saveProfile, setOp, text, unsetOp, useDraft, useIssue, valueAt, wholeHint, wholeNumber, type Op } from "./shared";

const HOME = [
  ["candidate.address.city", "City"],
  ["candidate.address.region", "State, province or region"],
  ["candidate.address.country", "Country (two-letter code)"],
] as const;

// The work modes profile.yml accepts, in the order it lists them.
const MODES = [
  ["remote", "Remote"],
  ["hybrid", "Hybrid"],
  ["onsite", "On site"],
] as const;

// AddPlace adds a typed place to a list. onAdd says why a place cannot be
// added (it is listed already); the typed text then stays, with the reason.
function AddPlace({ label, onAdd, path }: { label: string; onAdd: (v: string) => string | undefined; path: string }) {
  const [v, setV] = useState("");
  const [why, setWhy] = useState("");
  const id = useId();
  const issue = useIssue(path, id);
  const add = () => {
    if (v.trim() === "") return;
    const no = onAdd(v.trim());
    if (no) setWhy(no);
    else setV("");
  };
  return (
    <div className="add-line">
      <label htmlFor={id} className="sr-only">
        {label}
      </label>
      <input
        id={id}
        value={v}
        placeholder={label}
        {...issue.props}
        aria-describedby={[issue.props["aria-describedby"], why && `${id}-why`].filter(Boolean).join(" ") || undefined}
        onChange={(e) => (setV(e.target.value), setWhy(""))}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            add();
          }
        }}
      />
      <button type="button" onClick={add} disabled={v.trim() === ""}>
        <Plus aria-hidden="true" />
        Add
      </button>
      <span id={`${id}-why`} role="status" className="muted">
        {why}
      </span>
      {issue.note}
    </div>
  );
}

// OrderedPlaces is the list of places the user would move to, best first. It
// reorders by drag and by the Move up and Move down buttons, and focus stays on
// the control that was used.
function OrderedPlaces({ places, onChange }: { places: string[]; onChange: (next: string[]) => void }) {
  const focus = useRef<{ name: string; dir: "up" | "down" } | null>(null);
  const dragging = useRef<number | null>(null);
  const root = useRef<HTMLOListElement>(null);

  const move = (from: number, to: number, dir: "up" | "down") => {
    if (to < 0 || to >= places.length) return;
    const next = [...places];
    next.splice(to, 0, next.splice(from, 1)[0]);
    focus.current = { name: places[from], dir };
    onChange(next);
  };

  useEffect(() => {
    const f = focus.current;
    if (!f) return;
    focus.current = null;
    const buttons = root.current?.querySelectorAll<HTMLButtonElement>(`button[data-place="${CSS.escape(f.name)}"]`);
    const wanted = [...(buttons ?? [])].find((b) => b.dataset.dir === f.dir && !b.disabled);
    (wanted ?? [...(buttons ?? [])].find((b) => !b.disabled && b.dataset.dir !== "remove"))?.focus();
  }, [places]);

  if (places.length === 0) return <Empty>No place yet. Add the places you would move to, best first.</Empty>;
  return (
    <ol className="plain place-list" ref={root} aria-label="Places you would move to, best first">
      {places.map((name, i) => (
        <li
          key={name}
          className="place-row"
          draggable
          onDragStart={() => (dragging.current = i)}
          onDragOver={(e) => e.preventDefault()}
          onDrop={(e) => {
            e.preventDefault();
            if (dragging.current !== null && dragging.current !== i) move(dragging.current, i, "up");
            dragging.current = null;
          }}
        >
          <span>
            {i + 1}. {name}
          </span>
          <span className="actions">
            <button type="button" className="ghost small icon-only" data-place={name} data-dir="up" disabled={i === 0} onClick={() => move(i, i - 1, "up")} aria-label={`Move up ${name}`} title="Move up">
              <ArrowUp aria-hidden="true" />
            </button>
            <button type="button" className="ghost small icon-only" data-place={name} data-dir="down" disabled={i === places.length - 1} onClick={() => move(i, i + 1, "down")} aria-label={`Move down ${name}`} title="Move down">
              <ArrowDown aria-hidden="true" />
            </button>
            <button type="button" className="ghost small icon-only" data-place={name} data-dir="remove" onClick={() => onChange(places.filter((p) => p !== name))} aria-label={`Remove ${name}`} title="Remove">
              <X aria-hidden="true" />
            </button>
          </span>
        </li>
      ))}
    </ol>
  );
}

// listed says that a place is in a list already, whatever its capitals.
const listed = (list: string[], v: string, as = "") => (list.some((p) => p.toLowerCase() === v.toLowerCase()) ? `${v} is listed already${as}.` : undefined);

function Body({ values, revision, reload }: { values: { [key: string]: unknown }; revision: string; reload: () => void }) {
  const start = {
    home: Object.fromEntries(HOME.map(([p]) => [p, text(valueAt(values, p))])),
    places: listOf(valueAt(values, "preferences.relocation.places")),
    excluded: listOf(valueAt(values, "preferences.relocation.excluded")),
    relocate: text(valueAt(values, "preferences.relocation.default")),
    modes: listOf(valueAt(values, "preferences.work_modes")),
    office: text(valueAt(values, "preferences.max_days_in_office")),
    travel: text(valueAt(values, "preferences.max_travel_percent")),
  };
  const [f, setF] = useDraft(start);
  const { home, places, excluded, modes } = f;
  const dirty = JSON.stringify(f) !== JSON.stringify(start);

  const save = async () => {
    const ops: Op[] = HOME.filter(([p]) => home[p] !== start.home[p]).map(([p]) => (home[p].trim() === "" ? unsetOp(p) : setOp(p, home[p].trim())));
    if (places.join("|") !== start.places.join("|")) ops.push(setOp("preferences.relocation.places", places));
    if (excluded.join("|") !== start.excluded.join("|")) ops.push(setOp("preferences.relocation.excluded", excluded));
    if (f.relocate !== start.relocate) ops.push(f.relocate === "" ? unsetOp("preferences.relocation.default") : setOp("preferences.relocation.default", f.relocate));
    if (modes.join("|") !== start.modes.join("|")) ops.push(setOp("preferences.work_modes", modes));
    for (const [k, path] of [["office", "preferences.max_days_in_office"], ["travel", "preferences.max_travel_percent"]] as const)
      if (f[k] !== start[k]) ops.push(f[k].trim() === "" ? unsetOp(path) : setOp(path, wholeNumber(f[k])));
    await saveProfile(revision, ops);
    reload();
  };

  return (
    <SaveForm
      onSubmit={save}
      reload={reload}
      onDiscard={() => setF(start)}
      dirty={dirty} label="Save locations" columns blocked={!!(wholeHint(f.office, "3") || wholeHint(f.travel, "25"))}>
      <h4>Home location</h4>
      {HOME.map(([p, label]) => (
        <TextField key={p} label={label} value={home[p]} onChange={(v) => setF({ ...f, home: { ...home, [p]: v } })} path={p} />
      ))}
      <h4>Work arrangement</h4>
      <fieldset className="choice" data-wide>
        <legend>Ways of working you accept</legend>
        {MODES.map(([m, label]) => (
          <label key={m} className="check">
            <input type="checkbox" checked={modes.includes(m)} onChange={(e) => setF({ ...f, modes: MODES.map(([x]) => x).filter((x) => (x === m ? e.target.checked : modes.includes(x))) })} />
            {label}
          </label>
        ))}
      </fieldset>
      <TextField label="Most days a week in an office" hint={wholeHint(f.office, "3") ?? "0 to 7. Scoring and forms that ask about office days read it."} value={f.office} onChange={(office) => setF({ ...f, office })} path="preferences.max_days_in_office" />
      <TextField label="Most travel, percent of the time" hint={wholeHint(f.travel, "25") ?? "0 to 100. Scoring and forms that ask about travel read it."} value={f.travel} onChange={(travel) => setF({ ...f, travel })} path="preferences.max_travel_percent" />
      <h4>Relocation</h4>
      <div data-wide>
        <label htmlFor="relocate-default">Moving for a job</label>
        <Issued path="preferences.relocation.default">
          {(p) => (
            <select id="relocate-default" value={f.relocate} onChange={(e) => setF({ ...f, relocate: e.target.value })} {...p}>
              <option value="">Not answered</option>
              <option value="open">Open to anywhere I do not refuse</option>
              <option value="closed">Only to the places listed below</option>
            </select>
          )}
        </Issued>
      </div>
      <section className="place-col" aria-labelledby="places-move">
        <h4 id="places-move">Places you would move to</h4>
        <p className="muted">Best first. Drag a place, or use its arrows.</p>
        <OrderedPlaces places={places} onChange={(next) => setF({ ...f, places: next })} />
        <AddPlace label="Add a place to move to" path="preferences.relocation.places" onAdd={(v) => listed(places, v) ?? listed(excluded, v, " as a place you refuse") ?? void setF({ ...f, places: [...places, v] })} />
      </section>
      <section className="place-col" aria-labelledby="places-refuse">
        <h4 id="places-refuse">Places you refuse</h4>
        {excluded.length === 0 ? (
          <Empty>No place is refused.</Empty>
        ) : (
          <ul className="plain tag-list">
            {excluded.map((name) => (
              <li key={name} className="tag">
                <span>{name}</span>
                <button type="button" className="ghost small icon-only" onClick={() => setF({ ...f, excluded: excluded.filter((p) => p !== name) })} aria-label={`Stop refusing ${name}`} title="Stop refusing">
                  <X aria-hidden="true" />
                </button>
              </li>
            ))}
          </ul>
        )}
        <AddPlace label="Add a place you refuse" path="preferences.relocation.excluded" onAdd={(v) => listed(excluded, v) ?? listed(places, v, " as a place you would move to") ?? void setF({ ...f, excluded: [...excluded, v] })} />
      </section>
    </SaveForm>
  );
}

export function Locations() {
  return <ProfilePanel title="Locations" id="profile-locations">{(doc, reload) => <Body values={doc.values ?? {}} revision={doc.revision} reload={reload} />}</ProfilePanel>;
}
