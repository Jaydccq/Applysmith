import type { IntakeItem } from "../../wire";
import { emptyPick, monthFields, sourceText, type Pick } from "./model";

interface Props {
  items: readonly IntakeItem[];
  picks: Record<string, Pick>;
  setPick: (id: string, p: Pick) => void;
}

const sources = (it: IntakeItem) => (
  <ul className="plain muted">
    {it.sources.map((s) => (
      <li key={s.hash + s.locator}>{sourceText(s)}</li>
    ))}
  </ul>
);

function Months({ item, pick, set }: { item: IntakeItem; pick: Pick; set: (p: Pick) => void }) {
  return (
    <>
      {monthFields(item, pick).map(({ field, year }) => (
        <label key={field}>
          Month for the {field} year {year} (YYYY-MM)
          <input type="text" placeholder={`${year}-MM`} value={pick.months[field] ?? ""} onChange={(e) => set({ ...pick, months: { ...pick.months, [field]: e.target.value.trim() } })} />
        </label>
      ))}
    </>
  );
}

function Addition({ item, pick, set }: { item: IntakeItem; pick: Pick; set: (p: Pick) => void }) {
  return (
    <li data-item={item.id}>
      <label className="check">
        <input type="checkbox" checked={pick.on} onChange={(e) => set({ ...pick, on: e.target.checked })} />
        <span>
          {item.kind === "employment" ? "Job" : item.kind === "education" ? "Study" : "Skill"}: {item.summary}
        </span>
      </label>
      {sources(item)}
      {pick.on && <Months item={item} pick={pick} set={set} />}
    </li>
  );
}

function Conflict({ item, pick, set }: { item: IntakeItem; pick: Pick; set: (p: Pick) => void }) {
  return (
    <li data-item={item.id}>
      <label className="check">
        <input type="checkbox" checked={pick.on} onChange={(e) => set({ ...pick, on: e.target.checked })} />
        <span>
          {item.kind === "employment" ? "Job" : "Study"}: {item.summary}
        </span>
      </label>
      {item.fields.map((f) => (
        <fieldset key={f.field} disabled={!pick.on}>
          <legend>
            Your documents disagree about the {f.field}. Pick a side.
          </legend>
          {f.profile && (
            <label className="check">
              <input type="radio" name={`${item.id}-${f.field}`} checked={pick.choices[f.field] === "keep"} onChange={() => set({ ...pick, choices: { ...pick.choices, [f.field]: "keep" } })} />
              <span>Keep what your profile says: {f.profile}</span>
            </label>
          )}
          {f.options.map((o) => (
            <label key={o.value} className="check">
              <input type="radio" name={`${item.id}-${f.field}`} checked={pick.choices[f.field] === o.value} onChange={() => set({ ...pick, choices: { ...pick.choices, [f.field]: o.value } })} />
              <span>
                {o.value}, from {o.sources.map(sourceText).join("; ")}
              </span>
            </label>
          ))}
        </fieldset>
      ))}
      {pick.on && <Months item={item} pick={pick} set={set} />}
    </li>
  );
}

function Suggestion({ item, pick, set }: { item: IntakeItem; pick: Pick; set: (p: Pick) => void }) {
  return (
    <li data-item={item.id}>
      <p>{item.question}</p>
      {sources(item)}
      <label className="check">
        <input type="checkbox" checked={pick.on} onChange={(e) => set({ ...pick, on: e.target.checked })} />
        <span>This experience is mine; add {item.summary} to my skills</span>
      </label>
    </li>
  );
}

// ChangeSetReview lists what the documents would change, in four groups. A
// match is only shown; an addition, a conflict and a suggestion each need the
// person's own tick, and nothing is ticked on arrival.
export function ChangeSetReview({ items, picks, setPick }: Props) {
  const of = (t: string) => items.filter((i) => i.type === t);
  const row = (it: IntakeItem) => {
    const pick = picks[it.id] ?? emptyPick;
    const set = (p: Pick) => setPick(it.id, p);
    return it.type === "conflict" ? <Conflict key={it.id} item={it} pick={pick} set={set} /> : it.type === "signal" ? <Suggestion key={it.id} item={it} pick={pick} set={set} /> : <Addition key={it.id} item={it} pick={pick} set={set} />;
  };
  return (
    <div>
      <section aria-labelledby="intake-additions">
        <h4 id="intake-additions">New to your profile</h4>
        {of("addition").length === 0 ? <p className="muted">Nothing new.</p> : <ul className="plain stack">{of("addition").map(row)}</ul>}
      </section>
      <section aria-labelledby="intake-conflicts">
        <h4 id="intake-conflicts">Conflicts</h4>
        {of("conflict").length === 0 ? <p className="muted">No conflicts.</p> : (
          <>
            <p className="muted">Your approved value stays until you pick a side and apply the item.</p>
            <ul className="plain stack">{of("conflict").map(row)}</ul>
          </>
        )}
      </section>
      <section aria-labelledby="intake-matches">
        <h4 id="intake-matches">Already in your profile</h4>
        {of("match").length === 0 ? <p className="muted">No matches.</p> : (
          <ul className="plain stack">
            {of("match").map((it) => (
              <li key={it.id} data-item={it.id}>
                {it.summary}
                {sources(it)}
              </li>
            ))}
          </ul>
        )}
      </section>
      {of("signal").length > 0 && (
        <section aria-labelledby="intake-signals">
          <h4 id="intake-signals">Suggestions, not facts</h4>
          <p className="muted">These documents mention a skill. That does not show you have it, so nothing here is used by autofill or your résumé unless you confirm it.</p>
          <ul className="plain stack">{of("signal").map(row)}</ul>
        </section>
      )}
    </div>
  );
}
