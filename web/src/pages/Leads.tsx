import { useSearchParams } from "../router";
import { Tabs } from "../ui/Tabs";
import { LeadsTab } from "./leads/Leads";
import { ReviewTab } from "./leads/Review";
import { SignalsTab } from "./leads/Signals";
import { PageHeader } from "../ui/PageHeader";

const TABS = [
  { id: "leads", label: "Leads" },
  { id: "signals", label: "Hiring signals" },
  { id: "review", label: "Review queue" },
] as const;

export default function LeadsAndSignals() {
  const [params, setParams] = useSearchParams();
  const tab = TABS.find((t) => t.id === params.get("tab"))?.id ?? "leads";
  return (
    <>
      <PageHeader path="/leads" />
      <Tabs label="Leads and Signals sections" tabs={TABS} active={tab} onSelect={(id) => setParams({ tab: id === "leads" ? undefined : id, state: undefined, category: undefined, source: undefined, q: undefined, sort: undefined, tier: undefined, from: undefined, to: undefined, signal: undefined, page: undefined }, { replace: true })}>
        {tab === "leads" && <LeadsTab />}
        {tab === "signals" && <SignalsTab />}
        {tab === "review" && <ReviewTab />}
      </Tabs>
    </>
  );
}
