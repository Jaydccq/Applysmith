import { useState, type ReactNode } from "react";
import { get, patch, post } from "../../api/client";
import { configDocument, configSaved } from "../../api/decoders_pages";
import { signalDetail, signalEvaluateResult, signalRow as signalRowDecoder } from "../../api/decoders_signals";
import { useResource } from "../../hooks";
import { Link } from "../../router";
import { Modal } from "../../ui/Modal";
import { Chip, Empty, ErrorNote, ExternalLink, Loading, UndoNote, humanize } from "../../ui/parts";
import type { SignalDetail, SignalEvent } from "../../wire";
import { useAction, when } from "../action";
import { rowsOf, setOp, text, valueAt } from "../profile/shared";
import { AssessmentFacts, Quotes } from "./Review";
import { tierTone } from "./tiers";

const PORTAL_ATS = ["greenhouse", "lever", "ashby", "smartrecruiters", "workable"];

function EventItem({ e }: { e: SignalEvent }) {
  return (
    <li>
      <p>
        <strong>{humanize(e.type)}</strong> <span className="muted">{when(e.time)}{e.amount ? `, ${e.amount.toLocaleString()}` : ""}</span> {e.held && <Chip tone="warn">Held for review</Chip>}
      </p>
      <ul className="plain" aria-label="Source items">
        {e.documents.map((d) => (
          <li key={d.id}>
            <ExternalLink href={d.url}>{d.title || d.url}</ExternalLink> <span className="muted">{d.adapter}, {when(d.date)}</span>
          </li>
        ))}
      </ul>
      {e.assessment && (
        <>
          <p>
            Assessment <Chip tone={e.assessment.status === "ok" ? "good" : "warn"}>{humanize(e.assessment.status)}</Chip>
          </p>
          <AssessmentFacts a={e.assessment} />
          <Quotes a={e.assessment} />
        </>
      )}
    </li>
  );
}

function Facts({ d }: { d: SignalDetail }) {
  return (
    <dl className="facts">
      <dt>Tier</dt>
      <dd>
        <Chip tone={tierTone(d.row.tier)}>{d.row.tier}</Chip> momentum {d.row.momentum.toFixed(2)}, scan priority {d.row.scanPriority.toFixed(2)}
      </dd>
      <dt>State</dt>
      <dd>
        {humanize(d.row.state)}
        {d.row.blocked ? `, blocked${d.blockExpires ? ` until ${when(d.blockExpires)}` : ""}` : ""}
      </dd>
      {d.identity && (
        <>
          <dt>Careers</dt>
          <dd>
            {d.identity.status}
            {d.identity.ats ? `, ${d.identity.ats}/${d.identity.slug}` : ""} {d.identity.careersUrl && <ExternalLink href={d.identity.careersUrl}>{d.identity.careersUrl}</ExternalLink>}
          </dd>
        </>
      )}
    </dl>
  );
}

// SignalDrawer shows how a company reached its tier: each source item, the
// event it supports, the assessment with its verified quotes, the five factors
// and the postings seen on its board.
export function SignalDrawer({ companyId, onClose, onChange }: { companyId: number; onClose: () => void; onChange: () => void }) {
  const [res, retry] = useResource(() => get(`/api/signals/${companyId}`, signalDetail), [companyId], ["scans"]);
  const [note, setNote] = useState<ReactNode>("");
  // dismissed counts the dismissals, so each one gets its own Undo.
  const [dismissed, setDismissed] = useState(0);
  const act = useAction();
  const d = res.data;

  const simple = async (verb: "watch" | "dismiss" | "lift-block", done: string) => {
    if (await act.run(() => post(`/api/signals/${companyId}/${verb}`, signalRowDecoder))) {
      setNote(verb === "dismiss" ? "" : done);
      setDismissed((n) => (verb === "dismiss" ? n + 1 : 0));
      retry();
      onChange();
    }
  };
  const restore = async () => {
    const done = await act.run(() => post(`/api/signals/${companyId}/restore`, signalRowDecoder));
    retry();
    onChange();
    return !!done;
  };
  const evaluate = async () => {
    const got = await act.run(() => post(`/api/signals/${companyId}/evaluate-postings`, signalEvaluateResult));
    const n = got?.queued.length ?? 0;
    if (got)
      setNote(
        n === 0 ? (
          "No matching posting was waiting for evaluation."
        ) : (
          <>
            {n} posting{n === 1 ? "" : "s"} queued for evaluation. <Link to="/discovery?tab=inbox&status=queued">See them evaluating</Link>
          </>
        ),
      );
  };
  const addToPortals = async () => {
    if (!d) return;
    const done = await act.run(async () => {
      const doc = await get("/api/config/portals", configDocument);
      if (!doc.values) throw new Error("portals.yml has problems. Fix them on the Sources and portals tab of Discovery first.");
      const existing = rowsOf(valueAt(doc.values, "companies"));
      const name = d.row.company;
      if (existing.some((c) => text(c.name).toLowerCase() === name.toLowerCase())) return `${name} is already in portals.yml.`;
      const id = d.identity;
      const entry = id?.ats && id.slug && PORTAL_ATS.includes(id.ats) ? { name, ats: id.ats, slug: id.slug } : id?.careersUrl ? { name, careers_url: id.careersUrl } : null;
      if (!entry) throw new Error("No board or careers page is known for this company yet.");
      await patch("/api/config/portals", configSaved, { expectedRevision: doc.revision, ops: [setOp("companies", [...existing, entry])] });
      return `${name} was added to portals.yml.`;
    });
    if (done) setNote(done);
  };

  return (
    <Modal title={d ? d.row.company : "Hiring signal"} variant="drawer" onClose={onClose}>
      {res.status === "loading" && <Loading />}
      {res.status === "error" && <ErrorNote error={res.error} onRetry={retry} />}
      {d && (
        <>
          <Facts d={d} />
          <div className="actions">
            <button type="button" disabled={act.busy} onClick={() => void evaluate()}>
              Evaluate its matching postings
            </button>
            <button type="button" disabled={act.busy} onClick={() => void addToPortals()}>
              Add to portals
            </button>
            {d.row.state !== "watching" && (
              <button type="button" disabled={act.busy} onClick={() => void simple("watch", "Watching this company.")}>
                Watch
              </button>
            )}
            {d.row.state !== "dismissed" && (
              <button type="button" disabled={act.busy} onClick={() => void simple("dismiss", "Dismissed. It is left out of notices until you watch it again.")}>
                Dismiss
              </button>
            )}
            {d.row.blocked && (
              <button type="button" disabled={act.busy} onClick={() => void simple("lift-block", "The block was lifted.")}>
                Lift the block
              </button>
            )}
          </div>
          {act.error && <ErrorNote error={act.error} />}
          <div aria-live="polite">{note && <p className="note ok">{note}</p>}</div>
          <div role="status">{dismissed > 0 && <UndoNote key={dismissed} done="Dismissed. It is left out of notices until you watch it again." undone="It is back as it was." onUndo={restore} />}</div>
          <h3>Evidence chain</h3>
          {d.events.length === 0 ? <Empty>No event supports a tier yet.</Empty> : <ol aria-label="Evidence chain">{d.events.map((e) => <EventItem key={e.id} e={e} />)}</ol>}
          {d.factors.length > 0 && (
            <>
              <h3>How the tier was scored</h3>
              <table className="cards">
                <caption className="sr-only">Five-factor decomposition</caption>
                <thead>
                  <tr>
                    <th scope="col">Event</th>
                    <th scope="col">Type weight</th>
                    <th scope="col">Strength</th>
                    <th scope="col">Confidence</th>
                    <th scope="col">Source</th>
                    <th scope="col">Recency</th>
                    <th scope="col">Contribution</th>
                  </tr>
                </thead>
                <tbody>
                  {d.factors.map((f) => (
                    <tr key={f.eventId}>
                      <td data-label="Event">{f.eventType}{f.weak ? " (weak)" : ""}</td>
                      <td data-label="Type weight">{f.typeWeight.toFixed(2)}</td>
                      <td data-label="Strength">{f.strengthFactor.toFixed(2)}</td>
                      <td data-label="Confidence">{f.confidence.toFixed(2)}</td>
                      <td data-label="Source">{f.sourceFactor.toFixed(2)}</td>
                      <td data-label="Recency">{f.timeDecay.toFixed(2)}</td>
                      <td data-label="Contribution">{f.contribution.toFixed(2)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </>
          )}
          <h3>Postings on its board</h3>
          {d.postings.length === 0 ? (
            <>
              <Empty>No posting has been seen.</Empty>
              {d.searchLinks.length > 0 && (
                <ul className="plain" aria-label="Search for people">
                  {d.searchLinks.map((l, i) => (
                    <li key={l}>
                      <ExternalLink href={l}>{i === 0 ? "Search for recruiters or talent acquisition" : "Search for engineering managers or VPs of engineering"}</ExternalLink>
                    </li>
                  ))}
                </ul>
              )}
            </>
          ) : (
            <ul className="entry-list plain">
              {d.postings.map((p) => (
                <li key={p.id}>
                  <ExternalLink href={p.url}>{p.title}</ExternalLink> <span className="muted">{p.location}</span> <Chip tone={p.verdict === "pass" ? "good" : "warn"}>{p.verdict}</Chip>
                  {p.reasons.length > 0 && <span className="muted"> {p.reasons.join(", ")}</span>}
                  {p.handedOff && <span className="muted"> handed to the pipeline {when(p.handedOff)}</span>}
                </li>
              ))}
            </ul>
          )}
          {d.outcomes.length > 0 && (
            <>
              <h3>Outcomes</h3>
              <ul className="plain">
                {d.outcomes.map((o) => (
                  <li key={o.eventId}>
                    {o.type}: {o.status}
                    {o.leadDays !== undefined ? `, ${o.leadDays} days ahead` : ""} <span className="muted">window ends {when(o.windowEnd)}</span>
                  </li>
                ))}
              </ul>
            </>
          )}
          <p>
            <Link to="/discovery?tab=sources">Edit portals</Link>
          </p>
        </>
      )}
      <div className="actions">
        <button type="button" onClick={onClose}>
          Close
        </button>
      </div>
    </Modal>
  );
}
