import { get } from "../api/client";
import { gmailConfirmationList, gmailLinkList } from "../api/decoders_gmail";
import { useResource } from "../hooks";
import { useSearchParams } from "../router";
import { Tabs } from "../ui/Tabs";
import { Briefs } from "./gmail/Briefs";
import { HealthBanner, loadHealth } from "./gmail/Health";
import { DecisionNotes, Inbox, useDecisions } from "./gmail/Inbox";
import { Confirmations, LinkQueue, Moves } from "./gmail/Links";
import { ThreadDrawer } from "./gmail/Thread";
import { PageHeader } from "../ui/PageHeader";

const TABS = [
  { id: "inbox", label: "Inbox" },
  { id: "briefs", label: "Briefs" },
  { id: "review", label: "Review" },
] as const;

export default function Gmail() {
  const [params, setParams] = useSearchParams();
  // A notification link carries the brief id and opens the Briefs tab.
  const tab = TABS.find((t) => t.id === params.get("tab"))?.id ?? (params.get("brief") ? "briefs" : "inbox");
  const thread = params.get("thread") ?? undefined;
  const decisions = useDecisions();
  const [health, reloadHealth] = useResource(loadHealth, [], ["gmail"]);
  // A mailbox never connected has nothing to list; the banner says how to
  // connect it. One connected once keeps what it read.
  const h = health.data;
  const never = h !== undefined && !h.connected && h.sync === "never-run" && !h.latestRun;
  // The Review tab counts what waits for a decision there (an offer to
  // confirm, a link to settle), so an offer never hides behind the Inbox.
  const [waiting] = useResource(async () => {
    const [c, r] = await Promise.all([get("/api/gmail/confirmations", gmailConfirmationList), get("/api/gmail/links", gmailLinkList, { decision: "review" })]);
    return c.confirmations.length + r.items.length;
  }, [], ["gmail"]);
  const tabs = TABS.map((t) => (t.id === "review" ? { ...t, count: waiting.data } : t));
  return (
    <>
      <PageHeader path="/gmail" />
      <HealthBanner res={health} reload={reloadHealth} />
      <DecisionNotes d={decisions} />
      {!never && (
        <Tabs label="Gmail sections" tabs={tabs} active={tab} onSelect={(id) => setParams({ tab: id === "inbox" ? undefined : id, thread: undefined }, { replace: true })}>
          {tab === "inbox" && <Inbox d={decisions} stale={h?.connected === true && h.login === "refused"} syncing={h?.running === true} />}
          {tab === "briefs" && <Briefs />}
          {tab === "review" && (
            <>
              <Confirmations />
              <LinkQueue />
              <Moves />
            </>
          )}
        </Tabs>
      )}
      {thread && <ThreadDrawer key={thread} threadId={thread} d={decisions} onClose={() => setParams({ thread: undefined })} />}
    </>
  );
}
