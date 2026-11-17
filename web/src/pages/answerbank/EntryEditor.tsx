import { useId, useState, type ReactNode } from "react";
import { ApiError } from "../../api/client";
import { asApiError } from "../../hooks";
import { ConfirmDialog, Modal } from "../../ui/Modal";
import { Chip, ErrorNote } from "../../ui/parts";
import { leave, Link } from "../../router";
import { useSaveKey, useUnsaved } from "../../ui/SaveBar";
import type { AnswerBankChoice, AnswerBankDerived, AnswerBankEntry, AnswerBankOverride, AnswerBankShape } from "../../wire";
import { ASK_ME, SETUP_STEP, askTo, bodyEmpty, canAskMe, fromLines, holdsAskMe, sourceOf, toLines, withAskMe, type SiteOverride } from "./model";

// Text is one labelled field: a line, a box or a date.
function Text({ label, value, onChange, disabled, multiline, hint, placeholder, type }: { label: ReactNode; value: string; onChange: (v: string) => void; disabled: boolean; multiline?: boolean; hint?: string; placeholder?: string; type?: string }) {
  const id = useId();
  const hintId = useId();
  const described = hint ? hintId : undefined;
  const common = { id, value, disabled, "aria-describedby": described };
  return (
    <div>
      <label htmlFor={id}>{label}</label>
      {multiline ? (
        <textarea {...common} className="editor short" placeholder={placeholder} onChange={(e) => onChange(e.target.value)} />
      ) : (
        <input {...common} type={type} placeholder={placeholder} onChange={(e) => onChange(e.target.value)} />
      )}
      {hint && (
        <span id={hintId} className="muted">
          {hint}
        </span>
      )}
    </div>
  );
}

function Check({ label, checked, onChange, disabled }: { label: ReactNode; checked: boolean; onChange: (v: boolean) => void; disabled: boolean }) {
  return (
    <label className="check">
      <input type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      {label}
    </label>
  );
}

// Pick is one answer out of a shape's choices: a switch for a few, a list for
// more, with nothing picked until the user picks. An answer a form taught
// that is none of them shows above, until a pick replaces it.
function Pick({ legend, choices, value, onPick, disabled }: { legend: string; choices: AnswerBankChoice[]; value: string; onPick: (c: AnswerBankChoice) => void; disabled: boolean }) {
  const name = useId();
  const id = useId();
  const picked = choiceFor(choices, value);
  const other = value.trim() !== "" && value !== ASK_ME && !picked;
  const note = other && <span className="muted">Now: &ldquo;{value}&rdquo;, from a form. Pick an answer to replace it.</span>;
  if (choices.length > 6)
    return (
      <div>
        <label htmlFor={id}>{legend}</label>
        <select id={id} value={picked?.label ?? ""} disabled={disabled} onChange={(e) => onPick(choices.find((c) => c.label === e.target.value)!)}>
          <option value="" disabled>
            Choose an answer
          </option>
          {choices.map((c) => (
            <option key={c.label} value={c.label}>
              {c.label}
            </option>
          ))}
        </select>
        {note}
      </div>
    );
  return (
    <fieldset className="choice">
      <legend>{legend}</legend>
      <span className="segmented">
        {choices.map((c) => (
          <label key={c.label}>
            <input type="radio" name={name} checked={picked === c} disabled={disabled} onChange={() => onPick(c)} />
            {c.label}
          </label>
        ))}
      </span>
      {note}
    </fieldset>
  );
}

// choiceFor is the choice a stored answer names: its label or a wording.
const choiceFor = (choices: AnswerBankChoice[], value: string) => choices.find((c) => [c.label, ...c.matches].some((m) => m.toLowerCase() === value.trim().toLowerCase()));

const YES_NO: AnswerBankChoice[] = [
  { label: "Yes", matches: ["Yes"] },
  { label: "No", matches: ["No"] },
];

// Lines is a list typed one per line; it keeps what is typed, blank lines
// too, while the entry holds the list.
function Lines({ label, list, onChange, disabled }: { label: string; list: string[] | null | undefined; onChange: (l: string[]) => void; disabled: boolean }) {
  const [text, setText] = useState(toLines(list));
  return <Text label={label} value={text} onChange={(t) => (setText(t), onChange(fromLines(t)))} disabled={disabled} multiline />;
}

// CURRENCIES are the codes the range's currency offers; one the entry holds
// already is offered too.
const CURRENCIES = ["USD", "EUR", "GBP", "CAD", "AUD", "NZD", "CHF", "SEK", "NOK", "DKK", "CNY", "HKD", "SGD", "JPY", "KRW", "INR", "BRL", "MXN"];

function Currency({ value, onChange, disabled }: { value: string; onChange: (v: string) => void; disabled: boolean }) {
  const id = useId();
  const codes = value && !CURRENCIES.includes(value) ? [value, ...CURRENCIES] : CURRENCIES;
  return (
    <div>
      <label htmlFor={id}>Currency</label>
      <select id={id} value={value} disabled={disabled} onChange={(x) => onChange(x.target.value)}>
        <option value="">Not set</option>
        {codes.map((c) => (
          <option key={c}>{c}</option>
        ))}
      </select>
    </div>
  );
}

// fallbackShape is the shape of an entry the bank read gives none for.
const fallbackShape = (kind: string): AnswerBankShape => ({ type: { "multi-select": "list", "compensation-range": "range", "employer-history": "employer", "language-proficiency": "language", "auto-acknowledge": "none" }[kind] ?? "text", choices: [] });

// AnswerFields shows what the entry's answer can be (the shape the server
// gives it) and nothing else: a choice is picked, a yes or no is a switch, a
// date, link, number or e-mail its own field. Picking a choice writes the
// wordings the matcher looks for on forms. A blank answer a setup step fills
// shows that answer as its placeholder.
function AnswerFields({ e, shape, set, locked, derived }: { e: AnswerBankEntry; shape: AnswerBankShape; set: (p: Partial<AnswerBankEntry>) => void; locked: boolean; derived?: string }): ReactNode {
  const shown = (v: string | undefined) => (v === ASK_ME ? "" : (v ?? ""));
  const typed = { date: "date", url: "url", number: "number", email: "email" }[shape.type];
  switch (shape.type) {
    case "yes-no":
      return <Pick legend="Answer" choices={YES_NO} value={shown(e.answer)} onPick={(c) => set({ answer: c.label })} disabled={locked} />;
    case "choice":
      return <Pick legend="Answer" choices={shape.choices} value={shown(e.preferred?.[0] ?? e.fallback_text)} onPick={(c) => set({ preferred: c.matches, fallback_text: c.label })} disabled={locked} />;
    case "choices": {
      const picked = e.selected ?? [];
      return (
        <fieldset className="choice">
          <legend>Answers</legend>
          {shape.choices.map((c) => (
            <Check key={c.label} label={c.label} checked={picked.includes(c.label)} onChange={(on) => set({ selected: on ? [...picked.filter((x) => x !== ASK_ME), c.label] : picked.filter((x) => x !== c.label) })} disabled={locked} />
          ))}
          <Check label="None of them is my answer" checked={e.selected?.length === 0} onChange={(on) => set({ selected: on ? [] : null })} disabled={locked} />
        </fieldset>
      );
    }
    case "date":
    case "url":
    case "number":
    case "email":
      return <Text label="Answer" type={typed} value={shown(e.answer)} onChange={(answer) => set({ answer })} disabled={locked} placeholder={derived} />;
    case "text":
    case "long-text":
      if (e.kind === "pick-option") {
        return <Text label="Answer" value={shown(e.preferred?.[0] ?? e.fallback_text)} onChange={(v) => set({ preferred: v.trim() ? [v] : [], fallback_text: v })} disabled={locked} placeholder={derived} />;
      }
      return <Text label="Answer" value={shown(e.answer)} onChange={(answer) => set({ answer })} disabled={locked} multiline placeholder={derived} />;
    case "list":
      return <Lines label="Answers, one per line" list={e.selected} onChange={(selected) => set({ selected: selected.length > 0 ? selected : null })} disabled={locked} />;
    case "range":
      return (
        <>
          <Text label="Minimum" type="number" value={e.minimum ?? ""} onChange={(minimum) => set({ minimum })} disabled={locked} />
          <Text label="Maximum" type="number" value={e.maximum ?? ""} onChange={(maximum) => set({ maximum })} disabled={locked} />
          <Currency value={e.currency ?? ""} onChange={(currency) => set({ currency })} disabled={locked} />
          <Text label="Answer to a written question" value={shown(e.default_answer)} onChange={(default_answer) => set({ default_answer })} disabled={locked} />
        </>
      );
    case "employer":
      return (
        <>
          <Pick legend="Answer when the employer matches" choices={YES_NO} value={e.answer_if_match ?? ""} onPick={(c) => set({ answer_if_match: c.label })} disabled={locked} />
          <Pick legend="Answer when it does not" choices={YES_NO} value={e.answer_if_no_match ?? ""} onPick={(c) => set({ answer_if_no_match: c.label })} disabled={locked} />
          <Check label="Leave the control to me when the employer is ambiguous" checked={e.abstain_if_ambiguous ?? true} onChange={(abstain_if_ambiguous) => set({ abstain_if_ambiguous })} disabled={locked} />
        </>
      );
    case "language":
      return (
        <>
          <Text label="Language" value={e.language ?? ""} onChange={(language) => set({ language })} disabled={locked} />
          {(["speaking", "listening", "reading", "writing"] as const).map((k) => (
            <Pick key={k} legend={k[0].toUpperCase() + k.slice(1)} choices={shape.choices} value={e[k] ?? ""} onPick={(c) => set({ [k]: c.label })} disabled={locked} />
          ))}
        </>
      );
    default:
      return <p className="muted">This entry has no answer of its own: it ticks the box it is asked about.</p>;
  }
}

// finish marks an entry left without an answer a placeholder, as the server
// requires.
function finish(e: AnswerBankEntry): AnswerBankEntry {
  return bodyEmpty(e) ? { ...e, placeholder: true } : e;
}

type Body = Record<string, string | string[] | boolean | null | undefined>;
const valueOf = (o: AnswerBankOverride, f: string) => (o.body as unknown as Body)[f];

// viewOf is the entry as a save would send it.
const viewOf = (e: AnswerBankEntry) => JSON.stringify(finish(e));

// SiteAnswers lists the sites the entry answers differently on: on pages of
// that host the fields an override states replace the entry's.
function SiteAnswers({ list }: { list: SiteOverride[] }) {
  if (list.length === 0) return null;
  return (
    <div className="form-grid">
      <h4>Different on these sites</h4>
      <ul className="plain">
        {list.map(({ host, o }, i) => (
          <li key={host + i}>
            <code>{host}</code>:{" "}
            {o.fields
              .map((f) => valueOf(o, f))
              .map((v) => (Array.isArray(v) ? v.join(" / ") : typeof v === "boolean" ? "" : (v ?? "")))
              .filter((v) => v.trim() !== "")
              .join("; ")}
          </li>
        ))}
      </ul>
    </div>
  );
}

// EntryEditor shows one entry and edits its answer. What the matcher reads to
// reach the entry and to pick its answer (the wordings it answers, its id,
// kind and category, the sites it answers differently on) is not edited here:
// the user tells the Ask Agent, and the bank decides them from what they say,
// so the drawer links that conversation instead. Delete asks first and the
// list offers Undo. A shipped default says that it comes back at the next
// load.
export function EntryEditor({ entry, shape, isDefault, overrides, employers, derived, gap, onSave, onDelete, onClose, onReload }: {
  entry: AnswerBankEntry;
  /** What the entry's answer can be, as the bank read says. */
  shape?: AnswerBankShape;
  /** The entry's id is one of the shipped defaults. */
  isDefault?: boolean;
  /** The site overrides that target this entry. */
  overrides: SiteOverride[];
  /** How many companies the bank's employer list holds. */
  employers: number;
  derived?: AnswerBankDerived;
  /** The entry still needs the user's answer, and whether setup waits on it. */
  gap?: "to answer" | "blocking";
  onSave: (e: AnswerBankEntry) => Promise<void>;
  onDelete: () => Promise<void>;
  onClose: () => void;
  onReload: () => void;
}) {
  const [draft, setDraft] = useState(entry);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const [deleting, setDeleting] = useState(false);
  const set = (p: Partial<AnswerBankEntry>) => setDraft((d) => ({ ...d, ...p }));
  const asked = holdsAskMe(finish(draft));
  // A change made elsewhere (Ask Agent, a run, the CLI) comes into a drawer
  // that holds no edit; an edit stays until it is saved or discarded.
  const stored = viewOf(entry);
  const [base, setBase] = useState(stored);
  if (stored !== base) {
    if (viewOf(draft) === base) setDraft(entry);
    setBase(stored);
  }
  const dirty = viewOf(draft) !== stored;
  const ask = useUnsaved(dirty && !busy);
  // A discard also starts the fields over, a list's typed lines too.
  const [round, setRound] = useState(0);
  const discard = () => (setDraft(entry), setError(null), setRound((r) => r + 1));

  const act = async (go: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    try {
      await go();
    } catch (e) {
      setError(asApiError(e));
    } finally {
      setBusy(false);
    }
  };

  const canSave = !busy && dirty;
  const save = () => void act(() => onSave(finish(draft)));
  const saveKey = useSaveKey(dirty);

  const name = entry.description || entry.id;
  const source = derived ? "setup" : sourceOf(entry);
  return (
    <Modal variant="drawer" title={name} onClose={() => leave(onClose)}>
      <p className="chips">
        {/* An entry nobody has answered yet has no source; it says what it waits for, as its row does. */}
        {gap ? <Chip tone={gap === "blocking" ? "bad" : "warn"}>{gap === "blocking" ? "Blocking" : "To answer"}</Chip> : <Chip tone={source === "learned" ? "warn" : "neutral"}>Source: {source}</Chip>}
        {entry.needs_review && <Chip tone="warn">To review</Chip>}
        {asked && <Chip tone="info">Ask me</Chip>}
      </p>
      {draft.kind === "employer-history" && (
        <p className="note">
          Answers from your <Link to="/answer-bank?tab=employers">employer list</Link> ({employers} {employers === 1 ? "company" : "companies"}) and the jobs in your work history.
        </p>
      )}
      {derived && (
        <p className="note" data-derived={derived.step}>
          Your <Link to={`/setup?step=${derived.step}`}>{SETUP_STEP[derived.step] ?? derived.step}</Link> setup answers this: <strong>{derived.answer}</strong>. It follows that step when you change it there; an answer you save here replaces it.
        </p>
      )}
      <div className="form-grid">
        <AnswerFields key={`${base}:${round}`} e={draft} shape={shape ?? fallbackShape(entry.kind)} set={set} locked={busy} derived={derived?.answer} />
      </div>
      <SiteAnswers list={overrides} />
      <p className="muted">
        Forms reach this answer by the wording of their question. To answer another wording with it, give one site a different answer or remove a site&rsquo;s answer,{" "}
        <Link to={askTo(`About my saved answer "${name}": `)}>tell the Ask Agent</Link>.
      </p>
      {asked && <p className="note info">You chose to be asked each time: forms that ask this are left for you. Type an answer to replace the decision.</p>}
      {error && <ErrorNote error={error} />}
      {error?.kind === "revision_conflict" && (
        <button type="button" onClick={onReload}>
          Reload the bank, keeping your edits
        </button>
      )}
      {ask}
      {deleting && (
        <ConfirmDialog
          title="Delete this entry?"
          consequence={`Forms that ask "${name}" are left for you${overrides.length > 0 ? `, and its ${overrides.length === 1 ? "site answer goes" : `${overrides.length} site answers go`} with it` : ""}. You can undo it from the list.`}
          details={
            isDefault && (
              <p className="note warn">
                This is a shipped default: it comes back, as shipped, the next time the bank loads. To keep forms that ask it left for you, choose Ask me each time instead.
              </p>
            )
          }
          verb="Delete entry"
          busy={busy}
          onCancel={() => setDeleting(false)}
          onConfirm={() => (setDeleting(false), void act(onDelete))}
        />
      )}
      <div className="actions">
        <button type="button" className="primary" onClick={save} disabled={!canSave} ref={saveKey}>
          Save answer
        </button>
        {dirty && (
          <button type="button" className="ghost" onClick={discard} disabled={busy}>
            Discard changes
          </button>
        )}
        {draft.needs_review && (
          <button type="button" onClick={() => set({ needs_review: false })} disabled={busy}>
            Mark as reviewed
          </button>
        )}
        {canAskMe(draft.kind) && !asked && (
          <button type="button" onClick={() => setDraft(withAskMe(finish(draft)))} disabled={busy}>
            Ask me each time
          </button>
        )}
        <button type="button" className="danger-outline" onClick={() => setDeleting(true)} disabled={busy}>
          Delete entry
        </button>
      </div>
    </Modal>
  );
}
