import { useState, type FormEvent } from "react";
import { get } from "../../api/client";
import { trackerOutcomes } from "../../api/decoders_insights";
import { useResource } from "../../hooks";
import { Chip, ErrorNote, Loading } from "../../ui/parts";
import type { OutcomeGroup, TrackerOutcomes } from "../../wire";

const STAGE_LABEL: Record<string, string> = { responded: "Replied", interview: "Interview", offer: "Offer", rejected: "Rejected" };

function GroupTable({ title, groups }: { title: string; groups: OutcomeGroup[] }) {
  if (groups.length === 0) return null;
  const stages = groups[0].stages.map((s) => s.stage);
  return (
    <table className="cards">
      <caption>{title}</caption>
      <thead>
        <tr>
          <th scope="col">Group</th>
          <th scope="col">Applications</th>
          <th scope="col">With event data</th>
          <th scope="col">Unknown</th>
          <th scope="col">Open</th>
          {stages.map((s) => (
            <th scope="col" key={s}>
              {STAGE_LABEL[s] ?? s}
            </th>
          ))}
          <th scope="col">Reliability</th>
        </tr>
      </thead>
      <tbody>
        {groups.map((g) => (
          <tr key={g.key}>
            <td data-label="Group">{g.key || "Not recorded"}</td>
            <td data-label="Applications">{g.applications}</td>
            <td data-label="With event data">{g.known}</td>
            <td data-label="Unknown">{g.unknown}</td>
            <td data-label="Open">{g.open}</td>
            {g.stages.map((s) => (
              <td key={s.stage} data-label={STAGE_LABEL[s.stage] ?? s.stage}>
                {s.numerator} of {s.denominator}
                {s.open > 0 && <span className="muted"> ({s.open} still open)</span>}
              </td>
            ))}
            <td data-label="Reliability">{g.insufficient.length === 0 ? "Enough to compare" : <Chip tone="warn">Insufficient: {g.insufficient.join("; ")}</Chip>}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function Summary({ o }: { o: TrackerOutcomes }) {
  return (
    <div>
      <p>
        Applications dated {o.from || "the start"} to {o.to || "the cutoff"}, read as of <strong>{o.asOf}</strong>. Duplicate rows of one posting count once ({o.folded} folded).
        {o.noApplicationDate > 0 && ` ${o.noApplicationDate} submitted ${o.noApplicationDate === 1 ? "row has" : "rows have"} no application date and ${o.noApplicationDate === 1 ? "is" : "are"} left out.`}
      </p>
      <GroupTable title="All applications in the cohort" groups={[o.overall]} />
      <GroupTable title="By source channel" groups={o.bySource} />
      <GroupTable title="By role family" groups={o.byRoleFamily} />
      <p className="muted">
        Each rate counts the applications with event data. Applications still open may yet advance, and applications with no event history are shown as unknown instead of being guessed. A rejection is never explained
        by a missing skill. This view only reads: it never changes scores, your profile or your preferences.
      </p>
    </div>
  );
}

// OutcomeReview is the optional descriptive summary of an application-date
// cohort. It loads only when opened.
export function OutcomeReview() {
  const [open, setOpen] = useState(false);
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [asOf, setAsOf] = useState("");
  const [applied, setApplied] = useState({ from: "", to: "", asOf: "" });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setApplied({ from, to, asOf });
  };
  return (
    <section aria-labelledby="outcome-review" className="panel">
      <header className="panel-head">
        <h2 id="outcome-review">Outcome review</h2>
        <button type="button" aria-expanded={open} onClick={() => setOpen(!open)}>
          {open ? "Hide" : "Show"}
        </button>
      </header>
      {open && (
        <>
          <form className="filters" onSubmit={submit} aria-label="Cohort">
            <label>
              Applied from
              <input type="date" value={from} onChange={(e) => setFrom(e.target.value)} />
            </label>
            <label>
              Applied to
              <input type="date" value={to} onChange={(e) => setTo(e.target.value)} />
            </label>
            <label>
              Observation cutoff
              <input type="date" value={asOf} onChange={(e) => setAsOf(e.target.value)} />
            </label>
            <button type="submit">Update</button>
          </form>
          <OutcomeResult {...applied} />
        </>
      )}
    </section>
  );
}

function OutcomeResult({ from, to, asOf }: { from: string; to: string; asOf: string }) {
  const [res, retry] = useResource(() => get("/api/tracker/outcomes", trackerOutcomes, { from, to, asOf }), [from, to, asOf], ["tracker"]);
  if (res.status === "loading") return <Loading />;
  if (res.status === "error") return <ErrorNote error={res.error} onRetry={retry} />;
  return <Summary o={res.data!} />;
}
