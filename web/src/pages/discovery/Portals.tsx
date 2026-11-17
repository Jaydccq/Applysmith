import { leave, useSearchParams } from "../../router";
import { Tabs } from "../../ui/Tabs";
import { RawEditor } from "../settings/Files";
import { StructuredPortals } from "./portals/Structured";

const VIEWS = [
  { id: "forms", label: "Forms" },
  { id: "raw", label: "Raw text" },
] as const;

// RawPortals is portals.yml as text, in the editor Settings' Files uses and
// under its rules. It is the way to repair a file the forms refuse to read.
function RawPortals() {
  return (
    <section aria-labelledby="portals-editor" className="panel">
      <header className="panel-head">
        <h2 id="portals-editor">Sources and portals</h2>
      </header>
      <p className="muted">
        This is <code>portals.yml</code>: the companies and boards to scan, search queries and title filters.
      </p>
      <RawEditor file="portals" name="portals.yml" />
    </section>
  );
}

// PortalsTab edits portals.yml: forms for the company table, search queries and
// title filters, and the raw text as the fallback that also shows validation
// problems by line.
export function PortalsTab() {
  const [params, setParams] = useSearchParams();
  const view = VIEWS.find((v) => v.id === params.get("view"))?.id ?? "forms";
  return (
    <Tabs label="Portals editor" tabs={VIEWS} active={view} onSelect={(id) => leave(() => setParams({ view: id === "forms" ? undefined : id }, { replace: true }))}>
      {view === "forms" ? <StructuredPortals /> : <RawPortals />}
    </Tabs>
  );
}
