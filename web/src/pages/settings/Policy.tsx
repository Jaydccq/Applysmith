import { useEffect, useId, useState, type FormEvent } from "react";
import { get, patch } from "../../api/client";
import { documentPreferences } from "../../api/decoders";
import { scanSourceList } from "../../api/decoders_discovery";
import { configDocument, configSaved, riskView } from "../../api/decoders_pages";
import { useResource } from "../../hooks";
import { ErrorNote, Loading, Panel } from "../../ui/parts";
import type { ConfigDocument, DocumentPreferences, DocumentPreferencesPatch, RiskCooldown, ScanSource } from "../../wire";
import { useAction, when } from "../action";
import { listOf, ProfilePanel, SaveForm, saveProfile, setOp, text, TextField, unsetOp, useDraft, useRevisedDraft, valueAt, type Op } from "../profile/shared";
import { SaveBar } from "../../ui/SaveBar";
import { Link } from "../../router";
import { held } from "./Risk";
import { SENIORITY } from "../setup/model";
import { SourceKeys } from "./SourceKeys";
import { SCOPES } from "../discovery/format";

const SIGNALS: { key: string; label: string }[] = [
  { key: "captcha", label: "A captcha stopped a run" },
  { key: "rate_limited", label: "The site rate-limited the run" },
  { key: "account_locked", label: "An account was locked" },
  { key: "verification_required", label: "The site asked for verification" },
  { key: "login_redirect", label: "A page redirected away to a sign-in" },
  { key: "silent_degradation", label: "The form silently lost its fields" },
];

const MAX_HOURS = 2160;
const MAX_DAYS = 365;
const MAX_FOLLOWUPS = 10;

// The follow-up cadence of policy.yml's tracker.followup, with the defaults
// the server uses for a key the file leaves out. `max` is 0 to 10, the rest 1 to 365.
const CADENCE: { key: string; label: string; help: string; fallback: number; min: number; max: number }[] = [
  { key: "first_after_days", label: "First follow-up after (days)", help: "Days after applying before the first follow-up is due.", fallback: 8, min: 1, max: MAX_DAYS },
  { key: "repeat_every_days", label: "Repeat every (days)", help: "Days between one follow-up and the next.", fallback: 7, min: 1, max: MAX_DAYS },
  { key: "max_followups", label: "Follow-ups at most", help: "0 turns follow-ups off.", fallback: 2, min: 0, max: MAX_FOLLOWUPS },
  { key: "close_after_days", label: "Suggest closing after (days)", help: "Days after applying when a row with every follow-up sent and no reply is offered for closing.", fallback: 35, min: 1, max: MAX_DAYS },
];

const fallback = (key: string) => CADENCE.find((c) => c.key === key)!.fallback;

interface Draft {
  cooldowns: Record<string, string>;
  cadence: Record<string, string>;
}

function draftOf(doc: ConfigDocument): Draft {
  return {
    cooldowns: Object.fromEntries(SIGNALS.map((s) => [s.key, text(valueAt(doc.values, `cooldowns.${s.key}`) ?? 0)])),
    cadence: Object.fromEntries(CADENCE.map((c) => [c.key, text(valueAt(doc.values, `tracker.followup.${c.key}`) ?? c.fallback)])),
  };
}

const whole = (v: string, max: number) => /^\d+$/.test(v) && Number(v) <= max;

function problems(d: Draft): string[] {
  const out: string[] = [];
  for (const s of SIGNALS) if (!whole(d.cooldowns[s.key], MAX_HOURS)) out.push(`The "${s.label}" cooldown must be a whole number of hours from 0 to ${MAX_HOURS}.`);
  for (const c of CADENCE) if (!whole(d.cadence[c.key], c.max) || Number(d.cadence[c.key]) < c.min) out.push(`"${c.label}" must be a whole number from ${c.min} to ${c.max}.`);
  return out;
}

export type PolicyOp = { op: "set"; path: string; value: string };

// savePolicy writes ops to policy.yml at the revision the page read. With no
// revision it reads the file again first, so the edit lands on top of what the
// file holds now: the second try after a revision conflict.
export async function savePolicy(ops: PolicyOp[], revision?: string): Promise<void> {
  const expectedRevision = revision ?? (await get("/api/config/policy", configDocument)).revision;
  await patch("/api/config/policy", configSaved, { expectedRevision, ops });
}

// opsFor lists only what the edits changed in the values they were made over.
function opsFor(saved: Draft, d: Draft) {
  const ops: PolicyOp[] = [];
  for (const s of SIGNALS) if (d.cooldowns[s.key] !== saved.cooldowns[s.key]) ops.push({ op: "set", path: `cooldowns.${s.key}`, value: d.cooldowns[s.key] });
  for (const c of CADENCE) if (d.cadence[c.key] !== saved.cadence[c.key]) ops.push({ op: "set", path: `tracker.followup.${c.key}`, value: d.cadence[c.key] });
  return ops;
}

// AccountCreation says that a run you start creates an account when the
// application needs one, and lists the cooldowns holding a site now.
function AccountCreation({ cooling }: { cooling: RiskCooldown[] }) {
  return (
    <>
      <h3>Account creation</h3>
      <p>A run you start creates an account with your profile email when the application needs one, whether it submits or only fills.</p>
      {cooling.length > 0 && <p>Cooling down: {cooling.map((c) => `${held(c)} until ${when(c.until)}`).join("; ")}.</p>}
    </>
  );
}

function PolicyForm({ doc, reload }: { doc: ConfigDocument; reload: () => void }) {
  // The form is not remounted on a new revision, so the saved note survives it.
  const form = useRevisedDraft(doc.revision, draftOf(doc));
  const { draft, setDraft, dirty } = form;
  const [savedAt, setSavedAt] = useState("");
  const act = useAction();
  const ids = useId();
  const bad = problems(draft);

  const write = (revision?: string) => savePolicy(opsFor(form.base, draft), revision);
  const saved = () => {
    form.sent(draft);
    setSavedAt(new Date().toLocaleTimeString());
    reload();
  };
  const save = async (e: FormEvent) => {
    e.preventDefault();
    setSavedAt("");
    const ok = await act.run(async () => {
      await write(form.revision);
      return true;
    });
    if (ok) saved();
  };
  const conflicted = act.error?.kind === "revision_conflict";
  const num = (id: string, label: string, value: string, set: (v: string) => void, max: number) => (
    <div key={id}>
      <label htmlFor={id}>{label}</label>
      <input id={id} type="number" inputMode="numeric" min={0} max={max} step={1} value={value} onChange={(e) => set(e.target.value)} />
    </div>
  );

  return (
    <form onSubmit={save} className="form-grid">
      <h3>Cooldown after a risk signal</h3>
      <p className="muted">Hours a site is left alone for account creation after the signal. 0 disables that cooldown.</p>
      {SIGNALS.map((s) => num(`${ids}-cd-${s.key}`, `${s.label} (hours)`, draft.cooldowns[s.key], (v) => setDraft({ ...draft, cooldowns: { ...draft.cooldowns, [s.key]: v } }), MAX_HOURS))}
      <h3>Follow-up cadence</h3>
      <p className="muted">When the Tracker offers a follow-up on an application with no reply. Nothing is sent; the Tracker only reminds you.</p>
      {CADENCE.map((c) => (
        <div key={c.key}>
          <label htmlFor={`${ids}-fu-${c.key}`}>{c.label}</label>
          <input id={`${ids}-fu-${c.key}`} type="number" inputMode="numeric" min={c.min} max={c.max} step={1} value={draft.cadence[c.key]} aria-describedby={`${ids}-fu-${c.key}-help`} onChange={(e) => setDraft({ ...draft, cadence: { ...draft.cadence, [c.key]: e.target.value } })} />
          <p id={`${ids}-fu-${c.key}-help`} className="help">{c.help}</p>
        </div>
      ))}
      {bad.length > 0 && (
        <ul className="note error">
          {bad.map((b) => (
            <li key={b}>{b}</li>
          ))}
        </ul>
      )}
      <SaveBar dirty={dirty} savedAt={savedAt}>
        <button type="submit" disabled={act.busy || !dirty || bad.length > 0}>
          Save policy
        </button>
      </SaveBar>
      {act.error && !conflicted && <ErrorNote error={act.error} />}
      {conflicted && (
        <div role="alert" className="note error">
          <p>The policy changed since this page loaded, so nothing was saved.</p>
          <div className="actions">
            <button type="button" onClick={() => { form.reset(); act.clear(); reload(); }}>
              Reload the current version
            </button>
            <button
              type="button"
              disabled={act.busy}
              onClick={() => {
                // The edit is applied again on top of what the file holds now.
                void act
                  .run(async () => {
                    await write();
                    return true;
                  })
                  .then((ok) => ok && saved());
              }}
            >
              Apply my edit again
            </button>
          </div>
        </div>
      )}
    </form>
  );
}

export function PolicySection() {
  const [doc, reloadDoc] = useResource(() => get("/api/config/policy", configDocument), [], ["configuration"], { keep: "settings.policy" });
  const [risk] = useResource(() => get("/api/policy/risk", riskView), [], ["configuration", "runs"], { keep: "settings.policy-risk" });
  const cooling = risk.data?.cooldowns.filter((c) => c.kind === "apply") ?? [];
  return (
    <Panel title="Applications" id="settings-applications" res={doc} retry={reloadDoc} what="the application policy">
      {(d) =>
        // A missing file reads as the restrictive policy (workspace
        // RestrictivePolicy) and an edit of it is refused, so nothing here
        // can be saved until Setup creates it from its template.
        !d.exists ? (
          <div role="status" className="note">
            <p>
              policy.yml does not exist, so the most restrictive policy applies: the captcha solver and remote access are off. Follow-ups use the default cadence: the first after {fallback("first_after_days")} days, then every {fallback("repeat_every_days")}, at most {fallback("max_followups")}, and closing is suggested after {fallback("close_after_days")} days.
            </p>
            <p>
              <Link to="/setup?step=workspace">Create the missing files in Setup</Link>: policy.yml is written from its template and every existing file stays as it is.
            </p>
          </div>
        ) : d.issues.length > 0 ? (
          <div role="alert" className="note error">
            <p>policy.yml has errors, so the policy is treated as the most restrictive one and cannot be edited here:</p>
            <ul>
              {d.issues.map((i) => (
                <li key={`${i.path}${i.message}`}>{i.path ? `${i.path}: ` : ""}{i.message}</li>
              ))}
            </ul>
          </div>
        ) : (
          <>
            <AccountCreation cooling={cooling} />
            <PolicyForm doc={d} reload={reloadDoc} />
          </>
        )
      }
    </Panel>
  );
}

// The languages the document templates are written in (internal/documents).
const LANGUAGES: [string, string][] = [["en", "English"], ["de", "German"], ["fr", "French"], ["es", "Spanish"], ["it", "Italian"], ["pt", "Portuguese"], ["nl", "Dutch"]];

function DocumentsForm({ prefs, reload }: { prefs: DocumentPreferences; reload: () => void }) {
  const [draft, setDraft] = useState(prefs);
  useEffect(() => setDraft(prefs), [prefs.revision]);
  const [savedAt, setSavedAt] = useState("");
  const act = useAction();
  const ids = useId();
  const dirty = draft.paper !== prefs.paper || draft.resumePages !== prefs.resumePages || draft.language !== prefs.language || draft.coverWhenAsked !== prefs.coverWhenAsked;
  const save = async (e: FormEvent) => {
    e.preventDefault();
    setSavedAt("");
    const body: DocumentPreferencesPatch = { revision: prefs.revision, paper: draft.paper, resumePages: draft.resumePages, language: draft.language, coverWhenAsked: draft.coverWhenAsked };
    if (await act.run(() => patch("/api/documents/preferences", documentPreferences, body))) {
      setSavedAt(new Date().toLocaleTimeString());
      reload();
    }
  };
  return (
    <form onSubmit={save} className="form-grid">
      <p className="muted">The defaults for every new résumé, cover letter and PDF. A version already made keeps the paper, page limit and language it was made with.</p>
      <div>
        <label htmlFor={`${ids}-paper`}>Paper size</label>
        <select id={`${ids}-paper`} value={draft.paper} onChange={(e) => setDraft({ ...draft, paper: e.target.value })}>
          <option value="letter">US Letter</option>
          <option value="a4">A4</option>
        </select>
      </div>
      <div>
        <label htmlFor={`${ids}-pages`}>Résumé length</label>
        <select id={`${ids}-pages`} value={draft.resumePages} onChange={(e) => setDraft({ ...draft, resumePages: Number(e.target.value) })}>
          <option value={1}>1 page</option>
          <option value={2}>Up to 2 pages</option>
        </select>
      </div>
      <div>
        <label htmlFor={`${ids}-language`}>Language</label>
        <select id={`${ids}-language`} value={draft.language} onChange={(e) => setDraft({ ...draft, language: e.target.value })}>
          {LANGUAGES.map(([tag, name]) => (
            <option key={tag} value={tag}>
              {name}
            </option>
          ))}
        </select>
      </div>
      <div>
        <label htmlFor={`${ids}-cover`}>When an application asks for a cover letter</label>
        <select id={`${ids}-cover`} value={draft.coverWhenAsked} aria-describedby={`${ids}-cover-note`} onChange={(e) => setDraft({ ...draft, coverWhenAsked: e.target.value })}>
          <option value="always">Write one</option>
          <option value="eligible">Only for postings scored 4.5 or higher</option>
        </select>
        <p id={`${ids}-cover-note`} className="muted">
          {draft.coverWhenAsked === "always"
            ? "The default. An application run writes a tailored cover letter whenever the form has a slot for one, whatever the posting scored."
            : "A posting scored under 4.5 gets no letter unless you allow its documents; the form's slot is left for you."}
        </p>
      </div>
      <SaveBar dirty={dirty} savedAt={savedAt}>
        <button type="submit" disabled={act.busy || !dirty}>
          Save document defaults
        </button>
      </SaveBar>
      {act.error?.kind === "conflict" ? (
        <div role="alert" className="note error">
          <p>The defaults changed since this page loaded, so nothing was saved.</p>
          <button type="button" onClick={() => { act.clear(); reload(); }}>
            Reload the current defaults
          </button>
        </div>
      ) : (
        act.error && <ErrorNote error={act.error} />
      )}
    </form>
  );
}

export function DocumentsSection() {
  const [res, reload] = useResource(() => get("/api/documents/preferences", documentPreferences), [], [], { keep: "settings.documents" });
  return (
    <Panel title="Documents" id="settings-documents" res={res} retry={reload} what="the document defaults">
      {(p) => <DocumentsForm prefs={p} reload={reload} />}
    </Panel>
  );
}

type Values = { [key: string]: unknown };

function Pick({ label, value, onChange, options, hint }: { label: string; value: string; onChange: (v: string) => void; options: string[][]; hint?: string }) {
  const id = useId();
  return (
    <div>
      <label htmlFor={id}>{label}</label>
      <select id={id} value={value} onChange={(e) => onChange(e.target.value)}>
        {options.map(([v, l]) => <option key={v} value={v}>{l ?? v}</option>)}
      </select>
      {hint && <span className="muted">{hint}</span>}
    </div>
  );
}

// Scan defaults are profile.yml's scan.posted_within and scan.defaults.<source>.
// A blank field leaves its key out, so the default it shows applies; the
// server checks every value and names the key it refuses.
const WINDOWS = [["", "Each source's own window"], ["1h", "The last hour"], ["24h", "The last 24 hours"], ["3d", "The last 3 days"], ["week", "The last week"], ["month", "The last month"], ["any", "Any time"]];
// The sources screened first unless a mode is named (discovery evalMode).
const QUICK = new Set(["linkedin", "linkedin-feed", "indeed", "ziprecruiter", "google-jobs", "glassdoor", "newgrad", "jobright", "github", "speedrun", "theirstack"]);
const SCAN_KEYS = { topN: "evaluate_top_n", mode: "evaluation_mode", cooldown: "cooldown_hours", scope: "location_scope", posters: "blocked_posters" };

function ScanForm({ values, revision, reload, sources }: { values: Values; revision: string; reload: () => void; sources: ScanSource[] }) {
  const saved = (valueAt(values, "scan.defaults") ?? {}) as { [source: string]: Values };
  const start: { [key: string]: string } = { window: text(valueAt(values, "scan.posted_within")) };
  // The feed's evaluate_limit wins over evaluate_top_n, so the field shows it.
  for (const s of sources) for (const [k, key] of Object.entries(SCAN_KEYS)) start[`${s.name} ${k}`] = k === "posters" ? listOf(saved[s.name]?.[key]).join("\n") : text(k === "topN" ? (saved[s.name]?.evaluate_limit ?? saved[s.name]?.[key]) : saved[s.name]?.[key]);
  const [f, setF] = useDraft(start);
  const [source, setSource] = useState(sources[0]?.name ?? "");
  const at = (k: string) => f[`${source} ${k}`] ?? "";
  const put = (k: string) => (v: string) => setF({ ...f, [`${source} ${k}`]: v });
  const save = async () => {
    const ops: Op[] = [];
    if (f.window !== start.window) ops.push(f.window ? setOp("scan.posted_within", f.window) : unsetOp("scan.posted_within"));
    const next = { ...saved };
    const changed = sources.map((s) => s.name).filter((s) => Object.keys(SCAN_KEYS).some((k) => f[`${s} ${k}`] !== start[`${s} ${k}`]));
    for (const s of changed) {
      const e: Values = { ...saved[s] };
      for (const [k, key] of Object.entries(SCAN_KEYS)) {
        const v = f[`${s} ${k}`].trim();
        if (v === start[`${s} ${k}`]) continue;
        if (k === "topN") delete e.evaluate_limit;
        if (v === "") delete e[key];
        else e[key] = k === "posters" ? v.split("\n").map((t) => t.trim()).filter(Boolean) : k === "topN" || k === "cooldown" ? Number(v) : v;
      }
      if (Object.keys(e).length > 0) next[s] = e;
      else delete next[s];
    }
    // An empty map may be written {}, which a key path cannot enter.
    if (changed.length > 0 && (Object.keys(saved).length === 0 || Object.keys(next).length === 0)) ops.push(setOp("scan.defaults", next));
    else for (const s of changed) ops.push(next[s] ? setOp(`scan.defaults.${s}`, next[s]) : unsetOp(`scan.defaults.${s}`));
    await saveProfile(revision, ops);
    reload();
  };
  return (
    <SaveForm onSubmit={save} reload={reload} onDiscard={() => setF(start)} dirty={JSON.stringify(f) !== JSON.stringify(start)} label="Save scan defaults" columns>
      <Pick label="Postings no older than" value={f.window} onChange={(window) => setF({ ...f, window })} options={WINDOWS} hint="Left to each source, browser boards read the last 24 hours." />
      <Pick label="Source" value={source} onChange={setSource} options={sources.map((s) => [s.name, saved[s.name] ? `${s.title} (has its own defaults)` : s.title])} hint="The fields below are this source's." />
      <TextField type="number" label="Send the top N to evaluation" value={at("topN")} onChange={put("topN")} placeholder={source === "linkedin-feed" ? "10 (default)" : "15 (default)"} hint="0 to 500." />
      <Pick label="Evaluation mode" value={at("mode")} onChange={put("mode")} options={[["", `Default (${QUICK.has(source) ? "quick screen first" : "deep"})`], ["auto", "Quick screen first"], ["screen", "Screen only"], ["deep", "Deep"]]} />
      <TextField type="number" label="Cooldown after a blocked run (hours)" value={at("cooldown")} onChange={put("cooldown")} placeholder="6 (default)" hint="0 turns it off." />
      <Pick label="Location scope" value={at("scope")} onChange={put("scope")} options={[["", "Default (from the eligibility gate's countries)"], ...SCOPES]} />
      {source === "linkedin-feed" && (
        <div data-wide>
          <label htmlFor="scan-posters">Blocked posters</label>
          <textarea id="scan-posters" rows={4} value={at("posters")} onChange={(e) => put("posters")(e.target.value)} />
          <span className="muted">One per line: a post whose author or headline holds one is dropped unread. Start a line with re: for a regular expression.</span>
        </div>
      )}
    </SaveForm>
  );
}

export function ScanSection() {
  const [catalog, retry] = useResource(() => get("/api/scan-sources", scanSourceList), [], [], { keep: "settings.scan-sources" });
  return (
    <ProfilePanel title="Scan defaults" id="settings-scan">
      {(doc, reload) => (
        <>
          <p className="muted">What a scan uses when neither you, a schedule nor the Ask Agent names a value for that run.</p>
          {catalog.status === "ready" ? (
            <>
              <ScanForm values={doc.values ?? {}} revision={doc.revision} reload={reload} sources={catalog.data.sources} />
              <SourceKeys sources={catalog.data.sources} />
            </>
          ) : catalog.status === "error" ? (
            <ErrorNote error={catalog.error} onRetry={retry} what="the scan sources" />
          ) : (
            <Loading lines={3} />
          )}
        </>
      )}
    </ProfilePanel>
  );
}

// The eligibility gate is profile.yml's eligibility_gate; a blank field leaves
// its key out, and the default it names is the one evaluate.ResolveGate derives
// from the rest of the profile, worked out here so each field can say it.
const TIERS = [["direct", "Direct employers"], ["staffing", "Staffing agencies"], ["consultancy", "Consultancies"], ["aggregator", "Job aggregators"], ["unknown", "Unclassified employers"]];
const YES_NO = [["true", "Yes"], ["false", "No"]];

function gateDefaults(values: Values) {
  const wa = (valueAt(values, "work_authorization") ?? {}) as Values;
  const roles = valueAt(values, "targets.roles");
  const top = Math.max(-1, ...(Array.isArray(roles) ? roles : []).map((r) => (SENIORITY as readonly unknown[]).indexOf((r as Values | null)?.seniority)));
  const above = top < 0 ? [] : SENIORITY.slice(top + 1);
  const sponsor = wa.requires_sponsorship_now === true || wa.requires_sponsorship_future === true;
  const noClearance = text(wa.security_clearance).trim().toLowerCase() === "none";
  return {
    seniority: above.length > 0 ? `Nothing ticked rejects ${above.join(", ")}: the levels above your ${SENIORITY[top]} target.` : top < 0 ? "Nothing ticked rejects no level, as no target role names one." : "Nothing ticked rejects no level, as you target the top one.",
    countries: listOf(wa.authorized_countries).map((c) => c.toUpperCase()),
    reject_clearance: [sponsor || noClearance, sponsor ? "You need sponsorship." : noClearance ? "Your security clearance is none." : "Your profile does not say you need sponsorship, and your clearance is not none."] as const,
    reject_us_person_requirement: [sponsor, sponsor ? "You need sponsorship." : "Your profile does not say you need sponsorship."] as const,
  };
}

const codes = (v: string, upper: boolean) => v.split(",").map((x) => x.trim()).filter(Boolean).map((x) => (upper ? x.toUpperCase() : x));

function GateForm({ values, revision, reload }: { values: Values; revision: string; reload: () => void }) {
  const g = (valueAt(values, "eligibility_gate") ?? {}) as Values;
  const tiers = (g.employer_tiers ?? {}) as { [tier: string]: Values | undefined };
  const saved = listOf(g.reject_seniority);
  const onLadder = (t: string) => (SENIORITY as readonly string[]).includes(t.toLowerCase());
  const start: { [key: string]: string } = {
    seniority: SENIORITY.filter((t) => saved.some((s) => s.toLowerCase() === t)).join(","),
    seniority_other: saved.filter((t) => !onLadder(t)).join(", "),
    allowed_countries: listOf(g.allowed_countries).join(", "),
    allowed_currencies: listOf(g.allowed_currencies).join(", "),
    max_required_years: text(g.max_required_years),
  };
  for (const k of ["allow_remote", "reject_clearance", "reject_us_person_requirement"]) start[k] = text(g[k]);
  for (const [t] of TIERS) {
    start[`${t} policy`] = tiers[t]?.policy === "allow" ? "" : text(tiers[t]?.policy);
    start[`${t} delta`] = text(tiers[t]?.delta);
  }
  const [f, setF] = useDraft(start);
  const put = (k: string) => (v: string) => setF({ ...f, [k]: v });
  const ticked = new Set(f.seniority.split(",").filter(Boolean));
  const tick = (t: string) => setF({ ...f, seniority: SENIORITY.filter((s) => (s === t ? !ticked.has(s) : ticked.has(s))).join(",") });
  const d = gateDefaults(values);
  const countries = f.allowed_countries.trim() ? codes(f.allowed_countries, true) : d.countries;
  const save = async () => {
    const ops: Op[] = [];
    const path = (k: string) => `eligibility_gate.${k}`;
    if (f.seniority !== start.seniority || f.seniority_other !== start.seniority_other) ops.push(setOp(path("reject_seniority"), [...ticked, ...codes(f.seniority_other, false)]));
    for (const k of ["allowed_countries", "allowed_currencies"]) if (f[k] !== start[k]) ops.push(setOp(path(k), codes(f[k], true)));
    for (const k of ["max_required_years", "allow_remote", "reject_clearance", "reject_us_person_requirement"]) if (f[k] !== start[k]) ops.push(f[k] === "" ? unsetOp(path(k)) : setOp(path(k), f[k] === "true" ? true : f[k] === "false" ? false : Number(f[k])));
    if (TIERS.some(([t]) => f[`${t} policy`] !== start[`${t} policy`] || f[`${t} delta`] !== start[`${t} delta`])) {
      const map = Object.fromEntries(TIERS.filter(([t]) => f[`${t} policy`]).map(([t]) => [t, f[`${t} policy`] === "penalize" ? { policy: "penalize", delta: Number(f[`${t} delta`]) } : { policy: f[`${t} policy`] }]));
      ops.push(Object.keys(map).length > 0 ? setOp(path("employer_tiers"), map) : unsetOp(path("employer_tiers")));
    }
    await saveProfile(revision, ops);
    reload();
  };
  const restriction = (k: "reject_clearance" | "reject_us_person_requirement", label: string) => (
    <Pick label={label} value={f[k]} onChange={put(k)} options={[["", `Default (${d[k][0] ? "yes" : "no"})`], ...YES_NO]} hint={f[k] === "" ? d[k][1] : undefined} />
  );
  return (
    <SaveForm onSubmit={save} reload={reload} onDiscard={() => setF(start)} dirty={JSON.stringify(f) !== JSON.stringify(start)} label="Save the gate" columns>
      <h3 data-wide>Level</h3>
      <fieldset className="choice" data-wide>
        <legend>Reject titles that name</legend>
        <div className="chips">
          {SENIORITY.map((t) => (
            <label key={t} className="check">
              <input type="checkbox" checked={ticked.has(t)} onChange={() => tick(t)} /> {t === "vp" ? "VP" : t[0].toUpperCase() + t.slice(1)}
            </label>
          ))}
        </div>
        <span className="muted">{ticked.size > 0 || f.seniority_other.trim() ? "Only these are rejected." : d.seniority}</span>
      </fieldset>
      <TextField label="Other title words to reject" value={f.seniority_other} onChange={put("seniority_other")} placeholder="sr, head of" hint="Separated by commas. Matched as whole words." />
      <TextField type="number" label="Most years of experience a posting may ask for" value={f.max_required_years} onChange={put("max_required_years")} placeholder="No limit" />
      <h3 data-wide>Location and pay</h3>
      <TextField label="Allowed countries" value={f.allowed_countries} onChange={put("allowed_countries")} placeholder={d.countries.length > 0 ? `Blank: ${d.countries.join(", ")}` : "Blank: any country"} hint={`Two-letter codes, separated by commas.${d.countries.length > 0 ? " Blank uses your work authorization." : ""}`} />
      <TextField label="Allowed pay currencies" value={f.allowed_currencies} onChange={put("allowed_currencies")} placeholder={countries.length > 0 ? `Blank: those of ${countries.join(", ")}` : "Blank: any currency"} hint="Three-letter codes, separated by commas." />
      <Pick label="Keep remote postings" value={f.allow_remote} onChange={put("allow_remote")} options={[["", "Default (yes)"], ...YES_NO]} />
      <h3 data-wide>Restrictions</h3>
      {restriction("reject_clearance", "Reject postings that need a security clearance")}
      {restriction("reject_us_person_requirement", "Reject postings open only to US persons")}
      <h3 data-wide>Employer types</h3>
      <p className="muted">A blocked type is kept out; a lowered one is still evaluated, with points taken off its score.</p>
      {TIERS.map(([t, name]) => (
        <div key={t} className="tier-policy">
          <Pick label={name} value={f[`${t} policy`]} onChange={put(`${t} policy`)} options={[["", "Keep (default)"], ["penalize", "Lower the score"], ["block", "Block"]]} />
          {f[`${t} policy`] === "penalize" && <TextField type="number" label={`Score taken off ${name.toLowerCase()}`} value={f[`${t} delta`]} onChange={put(`${t} delta`)} hint="Above 0, at most 5." />}
        </div>
      ))}
    </SaveForm>
  );
}

export function GateSection() {
  return (
    <ProfilePanel title="Eligibility gate" id="settings-gate">
      {(doc, reload) => (
        <>
          <p className="muted">Rules a posting must pass to be evaluated. One that fails is kept out, and its row names the rule.</p>
          <GateForm values={doc.values ?? {}} revision={doc.revision} reload={reload} />
        </>
      )}
    </ProfilePanel>
  );
}
