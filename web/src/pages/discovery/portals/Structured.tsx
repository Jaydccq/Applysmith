import { get } from "../../../api/client";
import { configDocument } from "../../../api/decoders_pages";
import { scanSourceList } from "../../../api/decoders_discovery";
import { useResource } from "../../../hooks";
import { Panel } from "../../../ui/parts";
import { Link } from "../../../router";
import { AddCompany } from "./AddCompany";
import { Companies } from "./Companies";
import { Registry } from "./Registry";
import { Searches } from "./Searches";
import { Filters, TargetRoles } from "./TitleFilters";

// Sources that read no saved search.
const NO_QUERIES = new Set(["registry", "tracked"]);

// StructuredPortals edits portals.yml through forms. Every write goes through
// the revision-checked patch of the file, which validates the whole result, and
// the form stands down when the file is missing or has problems: the raw text
// tab is the way to repair it.
export function StructuredPortals() {
  const [res, reload] = useResource(() => get("/api/config/portals", configDocument), [], ["configuration"]);
  const [catalog] = useResource(() => get("/api/scan-sources", scanSourceList), [], []);
  const sources = (catalog.data?.sources ?? []).filter((s) => !NO_QUERIES.has(s.name));
  return (
    <>
      <Registry />
      <Panel title="Sources and portals" id="portals-structured" res={res} retry={reload}>
        {(doc) =>
          doc.issues.length > 0 || !doc.exists || !doc.values ? (
            <div role="alert" className="note error">
              <p>{doc.exists ? "portals.yml has problems, so the forms cannot edit it." : "portals.yml does not exist yet."}</p>
              <p>
                <Link to="/discovery?tab=sources&view=raw">Open the raw text editor</Link> to {doc.exists ? "fix it" : "create it"}.
              </p>
            </div>
          ) : (
            <>
              <Companies doc={doc} reloadDoc={reload} />
              <AddCompany doc={doc} reloadDoc={reload} />
              <TargetRoles reloadDoc={reload} />
              <Searches doc={doc} sources={sources} reloadDoc={reload} />
              <Filters doc={doc} reloadDoc={reload} />
            </>
          )
        }
      </Panel>
    </>
  );
}
