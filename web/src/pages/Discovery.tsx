import { get } from "../api/client";
import { inboxPage, scanSourceList } from "../api/decoders_discovery";
import { useResource } from "../hooks";
import { leave, useSearchParams } from "../router";
import { Tabs } from "../ui/Tabs";
import { BlockedTab } from "./discovery/Blocked";
import { HistoryTab } from "./discovery/History";
import { InboxTab } from "./discovery/Inbox";
import { MonitorTab } from "./discovery/Monitor";
import { PortalsTab } from "./discovery/Portals";
import { RunTab } from "./discovery/Run";
import { SchedulesTab } from "./discovery/Schedules";
import { PageHeader } from "../ui/PageHeader";

const TABS = [
  { id: "run", label: "Run" },
  { id: "monitor", label: "Monitor" },
  { id: "inbox", label: "Inbox" },
  { id: "history", label: "History" },
  { id: "schedules", label: "Schedules" },
  { id: "sources", label: "Sources and portals" },
  { id: "blocked", label: "Blocked companies" },
] as const;

export default function Discovery() {
  const [params, setParams] = useSearchParams();
  const tab = TABS.find((t) => t.id === params.get("tab"))?.id ?? "run";
  const run = params.get("run") ?? undefined;
  // The history filter offers every source of the catalog.
  const [sources] = useResource(() => get("/api/scan-sources", scanSourceList), [], []);
  const names = sources.data?.sources.map((s) => s.name) ?? [];
  // The Inbox tab counts the postings waiting for the user, so a pile left by
  // a scan's top-N cut is seen from every tab.
  const [inbox] = useResource(() => get("/api/inbox", inboxPage, { status: "pending", pageSize: 1 }), [], ["inbox", "scans"]);
  const tabs = TABS.map((t) => (t.id === "inbox" ? { ...t, count: inbox.data?.counts.pending } : t));
  return (
    <>
      <PageHeader path="/discovery" />
      <Tabs label="Discovery sections" tabs={tabs} active={tab} onSelect={(id) => leave(() => setParams({ tab: id === "run" ? undefined : id, run: undefined, source: undefined, items: undefined, status: undefined, q: undefined, edit: undefined, page: undefined, since: undefined, from: undefined, to: undefined, view: undefined }, { replace: true }))}>
        {tab === "run" && <RunTab onStarted={(id) => setParams({ tab: "monitor", run: id }, { replace: true })} />}
        {tab === "monitor" && <MonitorTab runId={run} />}
        {tab === "inbox" && <InboxTab />}
        {tab === "history" && <HistoryTab sources={names} />}
        {tab === "schedules" && <SchedulesTab editId={params.get("edit") ?? ""} onEdit={(id) => setParams({ edit: id })} />}
        {tab === "sources" && <PortalsTab />}
        {tab === "blocked" && <BlockedTab />}
      </Tabs>
    </>
  );
}
