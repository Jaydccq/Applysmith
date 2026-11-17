import { leave, useSearchParams } from "../router";
import { Tabs } from "../ui/Tabs";
import { EvidenceImport } from "./intake/EvidenceImport";
import { Authorization } from "./profile/Authorization";
import { Identity } from "./profile/Identity";
import { Locations } from "./profile/Locations";
import { MarkdownTab } from "./profile/MarkdownTab";
import { Resumes } from "./profile/Resumes";
import { PageHeader } from "../ui/PageHeader";

const TABS = [
  { id: "cv", label: "CV" },
  { id: "narrative", label: "Narrative" },
  { id: "voice", label: "Voice" },
  { id: "resumes", label: "Résumés" },
  { id: "locations", label: "Locations" },
  { id: "authorization", label: "Authorization" },
  { id: "identity", label: "Identity and targets" },
] as const;

export default function Profile() {
  const [params, setParams] = useSearchParams();
  const tab = TABS.find((t) => t.id === params.get("tab"))?.id ?? "cv";
  return (
    <>
      <PageHeader path="/profile" />
      <Tabs label="Profile sections" tabs={TABS} active={tab} onSelect={(id) => leave(() => setParams({ tab: id === "cv" ? undefined : id }, { replace: true }))}>
        {tab === "cv" && (
          <>
            <MarkdownTab file="cv" label="CV" step="cv" importable structured="cv" />
            <EvidenceImport />
          </>
        )}
        {tab === "narrative" && <MarkdownTab file="narrative" label="Narrative" step="story" structured="sections" />}
        {tab === "voice" && <MarkdownTab file="voice" label="Voice samples" step="story" structured="sections" />}
        {tab === "resumes" && <Resumes />}
        {tab === "locations" && <Locations />}
        {tab === "authorization" && <Authorization />}
        {tab === "identity" && <Identity />}
      </Tabs>
    </>
  );
}
