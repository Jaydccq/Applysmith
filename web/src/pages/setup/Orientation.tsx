import { useState } from "react";
import { asApiError } from "../../hooks";
import { Link } from "../../router";
import { Chip, ErrorNote } from "../../ui/parts";
import type { SetupStep } from "../../wire";
import { decide } from "./apply";
import { STATUS_TONE, statusWord, type ScreenProps } from "./model";

// Each step of the daily loop links to the page that does it.
const LOOP: [string, string, string][] = [
  ["Find a posting", "/discovery", ": a scan, a pasted link or a signal you follow."],
  ["Evaluate it", "/discovery?tab=inbox", ": the app scores it against your profile."],
  ["Read the report", "/reports", ": the score, the reasons and what could not be checked."],
  ["Prepare documents", "/tracker", ": a résumé and cover letter built from your own facts, from the application's Documents."],
  ["Autofill the application", "/apply-next", ": you review and press submit."],
  ["Track the outcome", "/tracker", ": the tracker and Gmail follow what comes back, and follow-ups come due there."],
];

// Orientation shows the daily loop and what was left out. The optional steps
// that are skipped or unfinished come from the state, each with what it costs.
export function Orientation({ step, state, reload }: ScreenProps) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  // A step done with a warning, such as the offline model, is left out in part.
  const open = state.steps.filter((s) => (!s.required && s.status !== "complete") || (s.status === "complete" && s.warnings.length > 0));
  const note = (s: SetupStep) => (s.status === "complete" ? s.warnings.join(" ") : s.skipCost);

  const read = async () => {
    setBusy(true);
    setError(null);
    try {
      await decide(step.id, "ack");
      reload();
    } catch (e) {
      setError(asApiError(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <p>
        New here? The <Link to="/guide">user guide</Link> explains each move of the loop below in order, and its <Link to="/guide?doc=tutorial">demo tutorial</Link> walks
        through one with screenshots.
      </p>
      <h3>Your daily loop</h3>
      <ol>
        {LOOP.map(([head, to, rest]) => (
          <li key={head}>
            <Link to={to}>{head}</Link>
            {rest}
          </li>
        ))}
      </ol>
      <h3>Left out for now</h3>
      {open.length === 0 ? (
        <p className="muted">Every optional step is set up.</p>
      ) : (
        <ul className="plain stack">
          {open.map((s) => (
            <li key={s.id} data-skipped={s.id}>
              <Link to={`/setup?step=${encodeURIComponent(s.id)}`}>{s.title}</Link> <Chip tone={STATUS_TONE[s.status] ?? "neutral"}>{statusWord(s.status)}</Chip>
              {note(s) && <p className="muted">{note(s)}</p>}
            </li>
          ))}
        </ul>
      )}
      <div className="actions">
        <button type="button" disabled={busy || step.status === "complete"} onClick={() => void read()}>
          I have read this
        </button>
      </div>
      {step.status === "complete" && !state.next && (
        <>
          <p role="status">
            Setup is done. Each day starts on <Link to="/">Home</Link>, which shows what needs you. Start with a posting you would apply to.
          </p>
          <div className="actions">
            <Link to="/?post=" className="button primary">
              Evaluate a posting
            </Link>
            <Link to="/discovery" className="button">
              Find postings
            </Link>
          </div>
        </>
      )}
      {error != null && <ErrorNote error={error} />}
    </div>
  );
}
