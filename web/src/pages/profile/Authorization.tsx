import { useId } from "react";
import { ProfilePanel, SaveForm, TextField, listOf, saveProfile, setOp, text, unsetOp, useDraft, useIssue, valueAt, type Op } from "./shared";

type Answer = "yes" | "no" | "";

const answerOf = (v: unknown): Answer => (v === true ? "yes" : v === false ? "no" : "");

// Choice is a yes or no question that starts not answered: blank is never
// read as no, and an answer can be taken back to not answered.
function Choice({ legend, value, onChange, path }: { legend: string; value: Answer; onChange: (v: Answer) => void; path: string }) {
  const issue = useIssue(path, useId());
  return (
    <fieldset className="choice">
      <legend>{legend}</legend>
      <span className="segmented">
        {(["yes", "no", ""] as const).map((a) => (
          <label key={a}>
            <input type="radio" name={legend} checked={value === a} onChange={() => onChange(a)} {...issue.props} />
            {a === "yes" ? "Yes" : a === "no" ? "No" : "Not answered"}
          </label>
        ))}
      </span>
      {issue.note}
      {value === "" && <span className="muted">Not answered yet. Forms that ask are left for you.</span>}
    </fieldset>
  );
}

function Body({ values, revision, reload }: { values: { [key: string]: unknown }; revision: string; reload: () => void }) {
  const start = {
    countries: listOf(valueAt(values, "work_authorization.authorized_countries")).join(", "),
    citizenships: listOf(valueAt(values, "work_authorization.citizenships")).join(", "),
    now: answerOf(valueAt(values, "work_authorization.requires_sponsorship_now")),
    future: answerOf(valueAt(values, "work_authorization.requires_sponsorship_future")),
    visa: text(valueAt(values, "work_authorization.visa_status")),
    clearance: text(valueAt(values, "work_authorization.security_clearance")),
  };
  const [f, setF] = useDraft(start);
  const dirty = JSON.stringify(f) !== JSON.stringify(start);

  const save = async () => {
    const ops: Op[] = [];
    if (f.countries !== start.countries) ops.push(setOp("work_authorization.authorized_countries", f.countries.split(",").map((c) => c.trim().toUpperCase()).filter(Boolean)));
    if (f.citizenships !== start.citizenships) ops.push(setOp("work_authorization.citizenships", f.citizenships.split(",").map((c) => c.trim().toUpperCase()).filter(Boolean)));
    for (const k of ["now", "future"] as const) {
      const path = `work_authorization.requires_sponsorship_${k}`;
      if (f[k] !== start[k]) ops.push(f[k] ? setOp(path, f[k] === "yes") : unsetOp(path));
    }
    if (f.visa !== start.visa) ops.push(setOp("work_authorization.visa_status", f.visa.trim()));
    if (f.clearance !== start.clearance) ops.push(setOp("work_authorization.security_clearance", f.clearance.trim()));
    await saveProfile(revision, ops);
    reload();
  };

  return (
    <SaveForm onSubmit={save} reload={reload} onDiscard={() => setF(start)} dirty={dirty} label="Save authorization" columns>
      <TextField label="Countries you are authorized to work in" hint="Two-letter codes, separated by commas." value={f.countries} onChange={(countries) => setF({ ...f, countries })} wide path="work_authorization.authorized_countries" />
      <TextField label="Countries of citizenship" hint="Two-letter codes, separated by commas; every one for a dual national. Sanctions questions are answered from it." value={f.citizenships} onChange={(citizenships) => setF({ ...f, citizenships })} wide path="work_authorization.citizenships" />
      <Choice legend="Do you need sponsorship now?" value={f.now} onChange={(now) => setF({ ...f, now })} path="work_authorization.requires_sponsorship_now" />
      <Choice legend="Will you need sponsorship in the future?" value={f.future} onChange={(future) => setF({ ...f, future })} path="work_authorization.requires_sponsorship_future" />
      <TextField label="Visa or work status" value={f.visa} onChange={(visa) => setF({ ...f, visa })} path="work_authorization.visa_status" />
      <TextField label="Security clearance" value={f.clearance} onChange={(clearance) => setF({ ...f, clearance })} path="work_authorization.security_clearance" />
    </SaveForm>
  );
}

export function Authorization() {
  return <ProfilePanel title="Work authorization" id="profile-authorization">{(doc, reload) => <Body values={doc.values ?? {}} revision={doc.revision} reload={reload} />}</ProfilePanel>;
}
