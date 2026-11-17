import { leave, useSearchParams } from "../router";
import { Tabs } from "../ui/Tabs";
import { Briefs } from "./wiki/Briefs";
import { PageView } from "./wiki/PageView";
import { Pages } from "./wiki/Pages";
import { Stories } from "./wiki/Stories";
import { PageHeader } from "../ui/PageHeader";

const TABS = [
  { id: "pages", label: "Pages" },
  { id: "briefs", label: "Company briefs" },
  { id: "stories", label: "Story bank" },
] as const;

export default function Wiki() {
  const [params, setParams] = useSearchParams();
  const tab = TABS.find((t) => t.id === params.get("tab"))?.id ?? "pages";
  const page = params.get("page");
  return (
    <>
      <PageHeader path="/wiki" />
      <Tabs label="Wiki sections" tabs={TABS} active={tab} onSelect={(id) => leave(() => setParams({ tab: id === "pages" ? undefined : id, page: undefined }, { replace: true }))}>
        {tab === "pages" && (page ? <PageView id={page} onOpen={(id) => setParams({ page: id })} onBack={() => setParams({ page: undefined })} /> : <Pages />)}
        {tab === "briefs" && <Briefs />}
        {tab === "stories" && <Stories />}
      </Tabs>
    </>
  );
}
