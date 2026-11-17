import { Link } from "../../router";
import { Chip } from "../../ui/parts";
import type { DoctorReport, DoctorResult } from "../../wire";
import { doctorGroup, phaseLabel, type Tone } from "./model";

const TONE: Record<string, Tone> = { pass: "good", warn: "warn", fail: "bad", skipped: "neutral" };

// DoctorFix is a check's next action on the dashboard: a link to the setup
// step that fixes it. The doctor's own fix text names command-line commands, so
// it is shown only for a check no step fixes. A skipped check found nothing to fix.
export function DoctorFix({ r, title }: { r: DoctorResult; title?: string }) {
  if (r.status === "pass" || r.status === "skipped") return null;
  if (r.step) {
    return (
      <Link to={`/setup?step=${r.step}`} className="doctor-fix">
        Fix it in Setup{title ? `: ${title}` : ""}
      </Link>
    );
  }
  return r.fix ? <span className="muted">Fix: {r.fix}</span> : null;
}

// DoctorResults lists the checks of "Check everything" under the first part of
// each check id, under the title of the step it belongs to, so the runtime
// checks sit in a group of their own. titles names the setup steps by id.
export function DoctorResults({ report, titles = {} }: { report: DoctorReport; titles?: Record<string, string> }) {
  const groups = new Map<string, DoctorResult[]>();
  for (const r of report.results) {
    const g = doctorGroup(r);
    groups.set(g, [...(groups.get(g) ?? []), r]);
  }
  return (
    <section aria-labelledby="setup-doctor" className="panel">
      <h2 id="setup-doctor" tabIndex={-1}>
        Check results
      </h2>
      <p className="muted">{report.platform}</p>
      {[...groups].map(([group, results]) => (
        <div key={group} data-group={group}>
          <h3>{titles[group] ?? phaseLabel(group)}</h3>
          <ul className="plain stack">
            {results.map((r) => (
              <li key={r.id}>
                <code>{r.id}</code> <Chip tone={TONE[r.status] ?? "neutral"}>{r.status}</Chip> {r.required ? <Chip>required</Chip> : <Chip>optional</Chip>}
                <p>{r.summary}</p>
                <p>
                  <DoctorFix r={r} title={r.step ? titles[r.step] : undefined} />
                </p>
              </li>
            ))}
          </ul>
        </div>
      ))}
    </section>
  );
}
