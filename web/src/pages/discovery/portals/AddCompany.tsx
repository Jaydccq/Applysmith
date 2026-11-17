import { useState, type FormEvent } from "react";
import { post } from "../../../api/client";
import { portalDetection } from "../../../api/decoders_discovery";
import { Chip, ErrorNote } from "../../../ui/parts";
import type { ConfigDocument, PortalCandidate, PortalDetection } from "../../../wire";
import { useAction } from "../../action";
import { usePortalsWrite } from "./WriteError";
import { companiesOf, type Entry } from "./edit";

const key = (c: PortalCandidate) => `${c.ats}:${c.slug}`;

function Preview({ det, picked, onPick }: { det: PortalDetection; picked: string; onPick: (k: string) => void }) {
  return (
    <div className="note" aria-label="Detection preview">
      {det.kind === "careers_url" && (
        <p>
          No supported board matches this address. It is saved as a careers page: <code>{det.careersUrl}</code>
        </p>
      )}
      {det.kind === "board" && <p>This address names a board.</p>}
      {det.note && <p className="muted">{det.note}</p>}
      {det.candidates.length > 0 && (
        <fieldset>
          <legend>{det.candidates.length === 1 ? "Detected board" : "Boards that answered"}</legend>
          {det.candidates.map((c) => (
            <label key={key(c)}>
              <input type="radio" name="portal-board" checked={picked === key(c)} disabled={c.inPortals} onChange={() => onPick(key(c))} />{" "}
              <strong>{c.ats}</strong> slug <code>{c.slug}</code>{" "}
              {c.live ? <span>{c.jobs} {c.jobs === 1 ? "posting" : "postings"}{c.company ? `, listed as ${c.company}` : ""}</span> : <span className="muted">{c.reason || "no postings"}</span>}{" "}
              {c.inPortals && <Chip tone="warn">already in portals.yml</Chip>} {c.inRegistry && !c.inPortals && <Chip>in the registry</Chip>}
            </label>
          ))}
        </fieldset>
      )}
    </div>
  );
}

// AddCompany adds a company by name or careers address. The server previews
// what the input names (a board and its slug, or a careers page) before
// anything is written; Add then appends the entry through the portals patch.
export function AddCompany({ doc, reloadDoc }: { doc: ConfigDocument; reloadDoc: () => void }) {
  const [input, setInput] = useState("");
  const [det, setDet] = useState<PortalDetection>();
  const [picked, setPicked] = useState("");
  const [name, setName] = useState("");
  const look = useAction();
  const save = usePortalsWrite(doc, reloadDoc);
  const entries = companiesOf(doc);

  const detect = async (e: FormEvent) => {
    e.preventDefault();
    const got = await look.run(() => post("/api/portals/detect", portalDetection, { input: input.trim() }));
    if (!got) return;
    setDet(got);
    setName(got.name);
    const open = got.candidates.filter((c) => !c.inPortals);
    setPicked(open.length === 1 ? key(open[0]) : "");
    save.clear();
  };

  const chosen = det?.candidates.find((c) => key(c) === picked);
  const careers = det?.kind === "careers_url";
  const trimmed = name.trim();
  const duplicate = entries.some((x) => String(x.name ?? "").toLowerCase() === trimmed.toLowerCase());
  const ready = trimmed !== "" && !duplicate && (careers || Boolean(chosen));

  const add = async () => {
    const entry: Entry = careers ? { name: trimmed, careers_url: det!.careersUrl } : { name: trimmed, ats: chosen!.ats, slug: chosen!.slug };
    if (await save.write({ kind: "add", list: "companies", item: entry, label: trimmed })) {
      setDet(undefined);
      setInput("");
    }
  };

  return (
    <section aria-label="Add a company">
      <h3>Add a company</h3>
      <form onSubmit={detect} className="form-grid" aria-label="Find a company">
        <label>
          Company name or careers address
          <input type="text" required value={input} onChange={(e) => setInput(e.target.value)} />
        </label>
        <button type="submit" disabled={look.busy || input.trim() === ""}>
          Preview
        </button>
      </form>
      {look.error && <ErrorNote error={look.error} />}
      {det && (
        <>
          <Preview det={det} picked={picked} onPick={setPicked} />
          <div className="form-grid">
            <label>
              Name in portals.yml
              <input type="text" value={name} onChange={(e) => setName(e.target.value)} />
            </label>
            {duplicate && <p role="alert" className="note error">A company called {trimmed} is already listed.</p>}
            <div className="actions">
              <button type="button" onClick={() => void add()} disabled={!ready || save.busy}>
                Add to portals
              </button>
            </div>
          </div>
        </>
      )}
      {save.notice}
    </section>
  );
}
