import { useState } from "react";
import { ConfirmDialog } from "../../ui/Modal";
import type { ApplyItem } from "../../wire";
import { SkipLine as CheckedSkipLine } from "../runs/RetryDialogs";

const LETTER = "Allow a tailored cover letter if a form asks for one";

// letterOffered is a row whose final report is below the 4.5 floor for
// tailored documents while the settings write a cover letter only above it:
// the user may override it.
const letterOffered = (i: ApplyItem) => i.letterOffer === true;

// letterRows are the rows a run over a list can take (not on the skip list,
// not left out, with a posting address) that offer the override.
const letterRows = (items: ApplyItem[]) => items.filter((i) => letterOffered(i) && !i.skipEntry && !i.leftOut && i.postingUrl);

// LetterOffers is the override box of each such row, unticked until the user
// ticks it; ticked holds the ids of the rows ticked.
function LetterOffers({ offered, ticked, onTicked }: { offered: ApplyItem[]; ticked: number[]; onTicked: (ids: number[]) => void }) {
  if (offered.length === 0) return null;
  return (
    <fieldset>
      <legend>Below the 4.5 floor for tailored documents. {LETTER} (recorded as your override for each report you tick):</legend>
      {offered.map((i) => (
        <label key={i.id}>
          <input
            type="checkbox"
            checked={ticked.includes(i.id)}
            onChange={(e) => onTicked(e.target.checked ? [...ticked, i.id] : ticked.filter((id) => id !== i.id))}
          />{" "}
          {i.company}, {i.role}
          {i.score !== undefined && ` (${i.score.toFixed(1)})`}
        </label>
      ))}
    </fieldset>
  );
}

// SkipLine says whether the employer is on the batch skip list. A single
// explicit application is not blocked by the list, so the line says that; a
// lane run leaves the row out.
function SkipLine({ item, inLane }: { item: ApplyItem; inLane?: boolean }) {
  if (!item.skipEntry) return <p>{item.company} is not on your batch skip list.</p>;
  return (
    <p>
      {item.company} is on your batch skip list (as &ldquo;{item.skipEntry}&rdquo;).{" "}
      {inLane ? "A run over a lane leaves it out." : "This is an explicit application to one posting, so the list does not block it."}
    </p>
  );
}

// RowSubmitDialog confirms one submit. It names the company, the role and the
// host, says that the final Submit is pressed, and shows the skip-list status:
// the row's own, or the server's for a start from outside the list (`checked`).
// A row below the document floor offers the cover-letter override, unticked;
// onConfirm says whether it was ticked.
export function RowSubmitDialog({ item, checked, override, busy, onConfirm, onCancel }: { item: ApplyItem; checked?: boolean; override?: string; busy: boolean; onConfirm: (allowLetter: boolean) => void; onCancel: () => void }) {
  const [letter, setLetter] = useState(false);
  return (
    <ConfirmDialog
      title={`Submit the application to ${item.company}`}
      consequence={
        <>
          Applysmith will fill the form for {item.role} at {item.company} on {item.postingHost ?? "its posting host"} and press its final Submit button. This
          sends a real application and cannot be undone.
        </>
      }
      details={
        <>
          {override && <p>{override}</p>}
          {checked ? <CheckedSkipLine company={item.company} what="application" /> : <SkipLine item={item} />}
          {letterOffered(item) && (
            <>
              <p>Its report {item.score !== undefined ? `scores ${item.score.toFixed(1)}, below` : "is below"} the 4.5 floor for tailored documents.</p>
              <label>
                <input type="checkbox" checked={letter} onChange={(e) => setLetter(e.target.checked)} /> {LETTER} (recorded as your override for this report)
              </label>
            </>
          )}
        </>
      }
      verb="Submit application"
      busy={busy}
      onConfirm={() => onConfirm(letter)}
      onCancel={onCancel}
    />
  );
}

// RetryLine asks whether today's stops run again. The server lifts only a
// same-day judgement stop: a duplicate, the skip list and a fact about the
// posting hold whatever the box says.
function RetryLine({ retry, onRetry }: { retry: boolean; onRetry: (on: boolean) => void }) {
  return (
    <label className="check">
      <input type="checkbox" checked={retry} onChange={(e) => onRetry(e.target.checked)} /> Retry today&rsquo;s stops: a row that stopped today on Applysmith&rsquo;s
      own judgement runs again. A duplicate, the skip list and a fact about the posting still hold.
    </label>
  );
}

// LeftOutLines names each row in view that a list started now would leave
// out, with the reason the server gave.
function LeftOutLines({ items }: { items: ApplyItem[] }) {
  const left = items.filter((i) => i.leftOut);
  if (left.length === 0) return <p>Every row in view would run.</p>;
  return (
    <>
      <p>Left out, with the reason:</p>
      <ul>
        {left.map((i) => (
          <li key={i.id}>
            {i.company}: {i.leftOut}
          </li>
        ))}
      </ul>
    </>
  );
}

// LaneSubmitDialog confirms a run over a lane. It states the count and which
// of the rows in view the list leaves out, and why. Each row in view the run
// can take that is below the document floor offers the cover-letter override,
// unticked; onConfirm gets the rows ticked.
export function LaneSubmitDialog({ title, count, items, hidden, busy, retry, onRetry, onConfirm, onCancel }: {
  title: string;
  count: number;
  items: ApplyItem[];
  /** True when the lane holds rows beyond the page in view. */
  hidden: boolean;
  busy: boolean;
  retry: boolean;
  onRetry: (on: boolean) => void;
  onConfirm: (allowLetters: ApplyItem[]) => void;
  onCancel: () => void;
}) {
  const [letters, setLetters] = useState<number[]>([]);
  const offered = letterRows(items);
  return (
    <ConfirmDialog
      title={`Apply to up to ${count} postings in ${title}`}
      consequence={
        <>
          Applysmith will work through up to {count} postings one at a time. For each it fills the form and presses the final Submit button, so this sends
          up to {count} real applications.
        </>
      }
      details={
        <>
          <LeftOutLines items={items} />
          {hidden && <p>Rows beyond this page are checked when the run starts. The run takes the lane best score first, whatever the page's order.</p>}
          <RetryLine retry={retry} onRetry={onRetry} />
          <LetterOffers offered={offered} ticked={letters} onTicked={setLetters} />
        </>
      }
      verb={`Submit up to ${count}`}
      busy={busy}
      onConfirm={() => onConfirm(offered.filter((i) => letters.includes(i.id)))}
      onCancel={onCancel}
    />
  );
}

// ChosenSubmitDialog confirms a run over the rows the user ticked, naming the
// role, company and host of each, in the order they run. Each of them the run
// can take that is below the document floor offers the cover-letter override,
// unticked; onConfirm gets the rows ticked.
export function ChosenSubmitDialog({ items, busy, retry, onRetry, onConfirm, onCancel }: {
  items: ApplyItem[];
  busy: boolean;
  retry: boolean;
  onRetry: (on: boolean) => void;
  onConfirm: (allowLetters: ApplyItem[]) => void;
  onCancel: () => void;
}) {
  const [letters, setLetters] = useState<number[]>([]);
  const offered = letterRows(items);
  const n = items.length;
  return (
    <ConfirmDialog
      title={`Submit ${n} chosen ${n === 1 ? "application" : "applications"}`}
      consequence={
        <>
          Applysmith will work through these one at a time. For each it fills the form and presses the final Submit button, so this sends up to {n} real
          {n === 1 ? " application" : " applications"}.
        </>
      }
      details={
        <>
          <ul>
            {items.map((i) => (
              <li key={i.id}>
                {i.role} at {i.company} on {i.postingHost ?? "its posting host"}
                {i.leftOut && <> (left out: {i.leftOut})</>}
              </li>
            ))}
          </ul>
          <RetryLine retry={retry} onRetry={onRetry} />
          <LetterOffers offered={offered} ticked={letters} onTicked={setLetters} />
        </>
      }
      verb={`Submit ${n}`}
      busy={busy}
      onConfirm={() => onConfirm(offered.filter((i) => letters.includes(i.id)))}
      onCancel={onCancel}
    />
  );
}

export function MarkAppliedDialog({ item, busy, onConfirm, onCancel }: { item: ApplyItem; busy: boolean; onConfirm: () => void; onCancel: () => void }) {
  return (
    <ConfirmDialog
      title={`Mark ${item.company} applied`}
      consequence={`The tracker row for ${item.role} at ${item.company} becomes applied, recorded as applied by you. Nothing is sent to the employer.`}
      verb="Mark applied"
      busy={busy}
      onConfirm={onConfirm}
      onCancel={onCancel}
    />
  );
}
