import { Fragment, useEffect, useId, useState } from "react";
import { Link } from "../../router";
import { ErrorNote, today } from "../../ui/parts";
import { useAction } from "../action";
import { FormSection, Issued, ProfilePanel, SaveForm, TextField, defaultSkillYears, rowsOf, payHint, payNumber, saveProfile, setOp, text, unsetOp, useDraft, valueAt, listOf, type Op } from "./shared";
import { CvDraft } from "./CvDraft";
import { FromCv, IDENTITY } from "../setup/Prefill";
import { RowsEditor, held, rowsFrom, valueFrom, type Column, type Row } from "./Rows";

// The identity fields in the groups the form shows them in: path, label,
// setup step that replaces an example value, and help.
const GROUPS: readonly { title: string; fields: readonly (readonly [string, string, string, string?])[] }[] = [
  {
    title: "Name and contact",
    fields: [
      ["candidate.full_name", "Full legal name", "identity"],
      ["candidate.preferred_name", "Preferred name", "identity"],
      ["candidate.email", "Email", "identity"],
      ["candidate.phone", "Phone (international form)", "identity"],
      ["candidate.date_of_birth", "Date of birth", "", "YYYY-MM-DD. Optional."],
    ],
  },
  {
    title: "Address",
    fields: [
      ["candidate.address.line1", "Street address", ""],
      ["candidate.address.line2", "Address line 2", ""],
      ["candidate.address.county", "County", ""],
      ["candidate.address.postal_code", "Postal code", ""],
    ],
  },
  {
    title: "Links",
    fields: [
      ["links.linkedin", "LinkedIn link", ""],
      ["links.github", "GitHub link", ""],
      ["links.portfolio", "Portfolio link", ""],
    ],
  },
];
const SCALARS = GROUPS.flatMap((g) => g.fields);
// The fields that may be left to the person at each application.
const ASK_ME_PATHS = ["candidate.address.line1", "candidate.address.postal_code"];

const ROLES: readonly Column[] = [
  { key: "title", label: "Role title" },
  { key: "seniority", label: "Seniority", type: "select", options: ["intern", "junior", "mid", "senior", "lead", "staff", "principal", "manager", "director", "vp", "executive"] },
  { key: "priority", label: "Priority", type: "select", options: ["primary", "secondary"] },
];
const EMPLOYMENT: readonly Column[] = [
  { key: "employer", label: "Employer", example: "cv" },
  { key: "title", label: "Job title" },
  { key: "start", label: "Start", placeholder: "YYYY-MM" },
  { key: "end", label: "End", placeholder: "YYYY-MM or present" },
  { key: "location", label: "Location" },
  { key: "summary", label: "Summary", wide: true },
];
const EDUCATION: readonly Column[] = [
  { key: "school", label: "School", example: "cv" },
  { key: "degree", label: "Degree" },
  { key: "field", label: "Field" },
  { key: "start", label: "Start", placeholder: "YYYY-MM" },
  { key: "end", label: "End", placeholder: "YYYY-MM" },
  { key: "grade_average", label: "Grade average", placeholder: "3.8/4.0" },
];
const SKILLS: readonly Column[] = [
  { key: "name", label: "Skill" },
  { key: "years", label: "Years", type: "number", placeholder: String(defaultSkillYears) },
  { key: "level", label: "Level", type: "select", options: ["beginner", "intermediate", "advanced", "expert"] },
];

function IdentityForm({ values, revision, reload }: { values: { [key: string]: unknown }; revision: string; reload: () => void }) {
  const start = Object.fromEntries(SCALARS.map(([p]) => [p, text(valueAt(values, p))]));
  const [f, setF] = useDraft(start);
  const dirty = SCALARS.some(([p]) => f[p] !== start[p]);
  const save = async () => {
    const ops: Op[] = SCALARS.filter(([p]) => f[p] !== start[p]).map(([p]) => (f[p].trim() === "" ? unsetOp(p) : setOp(p, f[p].trim())));
    await saveProfile(revision, ops);
    reload();
  };
  return (
    <SaveForm onSubmit={save} reload={reload} onDiscard={() => setF(start)} dirty={dirty} label="Save identity" columns>
      <FromCv
        current={(k) => f[IDENTITY[k]]}
        use={(found) => setF({ ...f, ...Object.fromEntries(found.map(([k, v]) => [IDENTITY[k], v])) })}
        label={(k) => SCALARS.find(([p]) => p === IDENTITY[k])?.[1] ?? k}
        save="Save identity"
      />
      {GROUPS.map((g) => (
        <Fragment key={g.title}>
          <h4>{g.title}</h4>
          {g.title === "Address" && (
            // The rest of the address lives with the places, where scoring
            // reads it; the form says where, so a move is changed in one place.
            <p className="muted" data-wide>
              City, state and country are under <Link to="/profile?tab=locations">Locations, Home location</Link>.
            </p>
          )}
          {g.fields.map(([p, label, step, hint]) => (
            <TextField key={p} label={label} type={p === "candidate.email" ? "email" : p === "candidate.phone" ? "tel" : undefined} value={f[p]} onChange={(v) => setF({ ...f, [p]: v })} example={step || undefined} hint={hint} path={p} askMe={ASK_ME_PATHS.includes(p)} />
          ))}
        </Fragment>
      ))}
    </SaveForm>
  );
}

function TargetsForm({ values, revision, reload }: { values: { [key: string]: unknown }; revision: string; reload: () => void }) {
  const start = {
    roles: rowsFrom(rowsOf(valueAt(values, "targets.roles")), ROLES),
    locations: listOf(valueAt(values, "targets.locations")).join("\n"),
    currency: text(valueAt(values, "targets.compensation.currency")),
    period: text(valueAt(values, "targets.compensation.period")),
    minimum: text(valueAt(values, "targets.compensation.minimum")),
    target: text(valueAt(values, "targets.compensation.target")),
  };
  const [f, setF] = useDraft(start);
  const dirty = JSON.stringify(f) !== JSON.stringify(start);
  const save = async () => {
    const ops: Op[] = [];
    if (JSON.stringify(f.roles) !== JSON.stringify(start.roles)) ops.push(setOp("targets.roles", valueFrom(f.roles, ROLES)));
    if (f.locations !== start.locations) ops.push(setOp("targets.locations", f.locations.split("\n").map((l) => l.trim()).filter(Boolean)));
    for (const k of ["currency", "period"] as const) if (f[k] !== start[k]) ops.push(f[k].trim() === "" ? unsetOp(`targets.compensation.${k}`) : setOp(`targets.compensation.${k}`, f[k].trim()));
    for (const k of ["minimum", "target"] as const) if (f[k] !== start[k]) ops.push(f[k].trim() === "" ? unsetOp(`targets.compensation.${k}`) : setOp(`targets.compensation.${k}`, payNumber(f[k])));
    await saveProfile(revision, ops);
    reload();
  };
  return (
    <SaveForm onSubmit={save} reload={reload} onDiscard={() => setF(start)} dirty={dirty} label="Save targets" columns blocked={!!(payHint(f.minimum) || payHint(f.target))}>
      <RowsEditor title="Roles you are looking for" noun="role" columns={ROLES} rows={f.roles} onChange={(roles) => setF({ ...f, roles })} path="targets.roles" />
      <div data-wide>
        <label htmlFor="target-locations">Locations you will work from, one per line</label>
        <Issued path="targets.locations">{(p) => <textarea id="target-locations" className="editor short" value={f.locations} onChange={(e) => setF({ ...f, locations: e.target.value })} {...p} />}</Issued>
      </div>
      <h4>Pay</h4>
      <TextField label="Currency" hint="Three letters, for example GBP." value={f.currency} onChange={(currency) => setF({ ...f, currency })} path="targets.compensation.currency" />
      <div>
        <label htmlFor="target-period">Pay period</label>
        <Issued path="targets.compensation.period">
          {(p) => (
            <select id="target-period" value={f.period} onChange={(e) => setF({ ...f, period: e.target.value })} {...p}>
              <option value="">Not set</option>
              <option value="year">year</option>
              <option value="hour">hour</option>
            </select>
          )}
        </Issued>
      </div>
      <TextField label="Lowest pay you would accept" value={f.minimum} hint={payHint(f.minimum)} onChange={(minimum) => setF({ ...f, minimum })} path="targets.compensation.minimum" />
      <TextField label="Pay you are aiming for" value={f.target} hint={payHint(f.target)} onChange={(target) => setF({ ...f, target })} path="targets.compensation.target" />
    </SaveForm>
  );
}

function HistoryForm({ values, revision, reload, onDirty }: { values: { [key: string]: unknown }; revision: string; reload: () => void; onDirty: (dirty: boolean) => void }) {
  const start = {
    employment: rowsFrom(rowsOf(valueAt(values, "history.employment")), EMPLOYMENT),
    education: rowsFrom(rowsOf(valueAt(values, "history.education")), EDUCATION),
    skills: rowsFrom(rowsOf(valueAt(values, "skills")), SKILLS),
  };
  const [f, setF] = useDraft(start);
  const dirty = JSON.stringify(f) !== JSON.stringify(start);
  useEffect(() => onDirty(dirty), [dirty, onDirty]);
  const save = async () => {
    const ops: Op[] = [];
    if (JSON.stringify(f.employment) !== JSON.stringify(start.employment)) ops.push(setOp("history.employment", valueFrom(f.employment, EMPLOYMENT)));
    if (JSON.stringify(f.education) !== JSON.stringify(start.education)) ops.push(setOp("history.education", valueFrom(f.education, EDUCATION)));
    if (JSON.stringify(f.skills) !== JSON.stringify(start.skills)) ops.push(setOp("skills", valueFrom(f.skills, SKILLS)));
    await saveProfile(revision, ops);
    reload();
  };
  return (
    <SaveForm onSubmit={save} reload={reload} onDiscard={() => setF(start)} dirty={dirty} label="Save history and skills" blocked={held(f.skills, SKILLS)}>
      <RowsEditor title="Employment" noun="job" columns={EMPLOYMENT} rows={f.employment} onChange={(employment) => setF({ ...f, employment })} path="history.employment" />
      <RowsEditor title="Education" noun="school" columns={EDUCATION} rows={f.education} onChange={(education) => setF({ ...f, education })} path="history.education" />
      <RowsEditor title="Skills" noun="skill" columns={SKILLS} rows={f.skills} onChange={(skills) => setF({ ...f, skills })} path="skills" />
      <p className="muted">A skill with no Years counts as {defaultSkillYears} years when a form asks.</p>
    </SaveForm>
  );
}

// History keeps the saved lists and the CV draft side by side. A confirmed
// draft entry appears in the editor through its draft; the draft cannot be
// confirmed over unsaved edits of the same lists.
function History({ values, revision, reload }: { values: { [key: string]: unknown }; revision: string; reload: () => void }) {
  const [dirty, setDirty] = useState(false);
  return (
    <>
      <CvDraft values={values} revision={revision} reload={reload} locked={dirty} />
      <HistoryForm values={values} revision={revision} reload={reload} onDirty={setDirty} />
    </>
  );
}

// company_relationships by the matcher's families (workspace
// CompanyRelationship): the key, the list's title and the noun of its rows.
const FAMILIES = [
  ["employment", "Companies you worked for", "employer"],
  ["relative", "Companies where a relative works", "relative"],
  ["friend", "Companies where a friend works", "friend"],
  ["prior-application", "Companies you applied to before", "application"],
  ["prior-interview", "Companies that interviewed you", "interview"],
] as const;
type Family = (typeof FAMILIES)[number][0];
const RELATIONSHIP: readonly Column[] = [
  { key: "company", label: "Company" },
  { key: "aliases", label: "Other names", placeholder: "Separated by semicolons" },
  { key: "current", label: "Current", type: "select", options: ["yes", "no"] },
  { key: "person_name", label: "Person's name" },
  { key: "relation", label: "Relation", placeholder: "Sibling" },
  { key: "recorded", label: "Recorded on", placeholder: "YYYY-MM-DD" },
];

// A family's saved relationships as rows: the aliases on one line, separated by
// semicolons because a company name may hold a comma; current as yes or no.
function relRows(values: { [key: string]: unknown }, family: Family): Row[] {
  const list = rowsOf(valueAt(values, "company_relationships.relationships")).filter((r) => r.family === family);
  return rowsFrom(list.map((r) => ({ ...r, aliases: listOf(r.aliases).join("; "), current: r.current === true ? "yes" : r.current === false ? "no" : "" })), RELATIONSHIP);
}

// The relationships as the file keeps them, family by family.
function relList(f: Record<Family, Row[]>) {
  return FAMILIES.flatMap(([family]) =>
    valueFrom(f[family], RELATIONSHIP).map((r) => ({
      family,
      ...r,
      ...(r.aliases !== undefined && { aliases: String(r.aliases).split(";").map((a) => a.trim()).filter(Boolean) }),
      ...(r.current !== undefined && { current: r.current === "yes" }),
    })),
  );
}

// Mark is a family's complete date as the matcher reads it
// (match/resolve_profile_employer.go, relAttested): it covers the family while
// no saved relationship of the family was recorded after it. Ticking writes
// today's date, clearing removes it; both wait while the list has unsaved edits.
function Mark({ family, title, saved, complete, revision, reload, locked }: { family: Family; title: string; saved: Row[]; complete: { [family: string]: unknown }; revision: string; reload: () => void; locked: boolean }) {
  const act = useAction();
  const id = useId();
  const asOf = text(complete[family]);
  const newer = saved.map((r) => r.recorded).filter((d) => d > asOf).sort().pop();
  const confirmed = asOf.trim() !== "" && newer === undefined;
  const write = async (on: boolean) => {
    const next = { ...complete };
    if (on) next[family] = today();
    else delete next[family];
    const done = await act.run(async () => {
      await saveProfile(revision, [setOp("company_relationships.complete", next)]);
      return true;
    });
    if (done) reload();
  };
  return (
    <div>
      <label htmlFor={id}>
        <input id={id} type="checkbox" checked={confirmed} disabled={locked} aria-describedby={`${id}-state`} onChange={(e) => void write(e.target.checked)} /> This list is complete
        <span className="sr-only">: {title.toLowerCase()}</span>
      </label>
      <span id={`${id}-state`} className="muted">
        {" "}
        {confirmed ? `Confirmed on ${asOf}.` : asOf.trim() === "" ? "Not confirmed." : `Not confirmed: a relationship recorded on ${newer} is newer than your confirmation of ${asOf}.`}
        {locked && " Save the changes to this list first."}
      </span>
      {act.error && <ErrorNote error={act.error} />}
    </div>
  );
}

// Relationships edits the families in only, every family by default; a save
// writes the whole list, so the families not shown keep their rows.
export function Relationships({ values, revision, reload, only = FAMILIES.map(([family]) => family) }: { values: { [key: string]: unknown }; revision: string; reload: () => void; only?: readonly Family[] }) {
  const start = Object.fromEntries(FAMILIES.map(([family]) => [family, relRows(values, family)])) as Record<Family, Row[]>;
  const [f, setF] = useDraft(start);
  const changed = (family: Family) => JSON.stringify(f[family]) !== JSON.stringify(start[family]);
  const complete = (valueAt(values, "company_relationships.complete") ?? {}) as { [family: string]: unknown };
  const save = async () => {
    await saveProfile(revision, [setOp("company_relationships.relationships", relList(f))]);
    reload();
  };
  // A row added now is recorded today.
  const edit = (family: Family) => (rows: Row[]) => setF({ ...f, [family]: rows.map((r, i) => (i < f[family].length ? r : { ...r, recorded: r.recorded || today() })) });
  return (
    <SaveForm onSubmit={save} reload={reload} onDiscard={() => setF(start)} dirty={FAMILIES.some(([family]) => changed(family))} label="Save relationships">
      {FAMILIES.map(([family, title, noun], k) => only.includes(family) && (
        <Fragment key={family}>
          {/* Rows are saved family by family in one list (relList). */}
          <RowsEditor title={title} noun={noun} columns={RELATIONSHIP} rows={f[family]} onChange={edit(family)} path="company_relationships.relationships" from={FAMILIES.slice(0, k).reduce((n, [x]) => n + f[x].length, 0)} />
          <Mark family={family} title={title} saved={start[family]} complete={complete} revision={revision} reload={reload} locked={changed(family)} />
        </Fragment>
      ))}
    </SaveForm>
  );
}

export function Identity() {
  return (
    <ProfilePanel title="Identity and targets" id="profile-identity">
      {(doc, reload) => {
        const values = doc.values ?? {};
        return (
          <>
            <FormSection title="Identity" about="Typed into every application exactly as written here.">
              <IdentityForm values={values} revision={doc.revision} reload={reload} />
            </FormSection>
            <FormSection title="Targets" about="The roles, places and pay that discovery and scoring look for.">
              <TargetsForm values={values} revision={doc.revision} reload={reload} />
            </FormSection>
            <FormSection title="History and skills" about="The jobs, schools and skills that forms ask for.">
              <History values={values} revision={doc.revision} reload={reload} />
            </FormSection>
            <FormSection title="Company relationships" about="When a list is marked complete, a question about a company that is not on it is answered No. Jobs under History and skills count as companies you worked for.">
              <Relationships values={values} revision={doc.revision} reload={reload} />
            </FormSection>
          </>
        );
      }}
    </ProfilePanel>
  );
}
