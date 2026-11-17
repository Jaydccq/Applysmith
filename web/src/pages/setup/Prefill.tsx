import type { ReactNode } from "react";
import { get } from "../../api/client";
import { configDocument, cvDraft } from "../../api/decoders_pages";
import { useResource } from "../../hooks";
import { listOf, text, valueAt } from "../profile/shared";
import { labelOf } from "./Form";
import type { Values } from "./model";
import { Loading } from "../../ui/parts";

type Parsed = { [key: string]: unknown };

// WithProfile loads the stored profile so a form can start from what is
// already there. A profile that cannot be read leaves the form empty rather
// than blocking it: the writer still validates whatever is sent.
export function WithProfile({ children }: { children: (profile: Parsed) => ReactNode }) {
  const [res] = useResource(() => get("/api/config/profile", configDocument), [], ["configuration"]);
  if (res.status === "loading") return <Loading label="Loading stored values" />;
  return (
    <>
      {res.status === "error" && <p className="muted">The stored values could not be read, so the form starts empty.</p>}
      {children(res.data?.values ?? {})}
    </>
  );
}

const fromPath = (profile: Parsed, map: Record<string, string>): Values =>
  Object.fromEntries(Object.entries(map).map(([field, path]) => [field, text(valueAt(profile, path))]));

// Each map reads one writer field from the profile key it is stored under
// (internal/workspace/profile.go).
export const IDENTITY: Record<string, string> = {
  full_name: "candidate.full_name", preferred_name: "candidate.preferred_name", email: "candidate.email", phone: "candidate.phone", city: "candidate.address.city",
  region: "candidate.address.region", country: "candidate.address.country", county: "candidate.address.county", street_address: "candidate.address.line1",
  postal_code: "candidate.address.postal_code", date_of_birth: "candidate.date_of_birth", linkedin: "links.linkedin", github: "links.github", portfolio: "links.portfolio",
};

export const identityValues = (p: Parsed): Values => ({ ...fromPath(p, IDENTITY), other_links: listOf(valueAt(p, "links.other")).join(", ") });

export const WORK_AUTH: Record<string, string> = {
  authorization_status: "work_authorization.visa_status", authorized_countries: "work_authorization.authorized_countries",
  requires_sponsorship_now: "work_authorization.requires_sponsorship_now", requires_sponsorship_future: "work_authorization.requires_sponsorship_future",
  security_clearance: "work_authorization.security_clearance", relocation_default: "preferences.relocation.default", work_modes: "preferences.work_modes",
};
export const TARGETS: Record<string, string> = { roles: "targets.roles", locations: "targets.locations", compensation: "targets.compensation", batch_skip_companies: "batch_skip_companies" };

// pathLabels names each profile key by the label of the form field that
// edits it, in the form's order.
export const pathLabels = (map: Record<string, string>, labels: Record<string, string> = {}): Record<string, string> =>
  Object.fromEntries(Object.entries(map).map(([field, path]) => [path, labels[field] ?? labelOf(field)]));

const answered = (v: unknown): boolean | undefined => (typeof v === "boolean" ? v : undefined);

// visa_status holds the status, and for a visa or student authorization the
// class or end date after a colon ("work_visa: H-1B").
export function workAuthValues(p: Parsed, statuses: readonly string[]): Values {
  const raw = text(valueAt(p, "work_authorization.visa_status")).trim();
  const colon = raw.indexOf(":");
  const head = (colon < 0 ? raw : raw.slice(0, colon)).trim();
  const known = statuses.includes(head);
  return {
    authorization_status: known ? head : "",
    status_detail: known && colon >= 0 ? raw.slice(colon + 1).trim() : "",
    authorized_countries: listOf(valueAt(p, "work_authorization.authorized_countries")).join(", "),
    requires_sponsorship_now: answered(valueAt(p, "work_authorization.requires_sponsorship_now")),
    requires_sponsorship_future: answered(valueAt(p, "work_authorization.requires_sponsorship_future")),
    security_clearance: text(valueAt(p, "work_authorization.security_clearance")),
    relocation_default: text(valueAt(p, "preferences.relocation.default")),
    relocation_places: listOf(valueAt(p, "preferences.relocation.places")).join(", "),
    relocation_excluded: listOf(valueAt(p, "preferences.relocation.excluded")).join(", "),
    work_modes: listOf(valueAt(p, "preferences.work_modes")).join(", "),
  };
}

// FromCv offers what the saved CV says for each field of the form that is
// still empty, as the server reads it (cvparse.Identity). Using a suggestion only puts it in the form: nothing is stored
// until the form is saved, and a value already there is never replaced.
export function FromCv({ current, use, label, save }: { current: (name: string) => string | undefined; use: (found: [string, string][]) => void; label: (name: string) => string; save: string }) {
  const [res] = useResource(() => get("/api/config/cv/draft", cvDraft), [], ["configuration"]);
  const read = res.data?.identity ?? {};
  const found = Object.keys(IDENTITY).flatMap((k): [string, string][] => (read[k] && current(k)?.trim() === "" ? [[k, read[k]]] : []));
  if (found.length === 0) return null;
  return (
    <section aria-labelledby="from-cv" className="panel" data-wide>
      <header className="panel-head">
        <h3 id="from-cv">Found in your CV</h3>
      </header>
      <p className="muted">Use what is right. Nothing is stored until you press {save}.</p>
      <ul className="plain stack">
        {found.map(([k, v]) => (
          <li key={k} className="row hold">
            <span>
              {label(k)}: <strong>{v}</strong>
            </span>
            <button type="button" onClick={() => use([[k, v]])} aria-label={`Use ${v} as ${label(k)}`}>
              Use
            </button>
          </li>
        ))}
      </ul>
      {found.length > 1 && (
        <div className="actions">
          <button type="button" onClick={() => use(found)}>
            Use all {found.length}
          </button>
        </div>
      )}
    </section>
  );
}
