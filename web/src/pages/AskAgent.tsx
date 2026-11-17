import { get } from "../api/client";
import { confirmationList } from "../api/decoders_agent";
import { useResource } from "../hooks";
import { leave, useSearchParams } from "../router";
import { Tabs } from "../ui/Tabs";
import { StandingApprovals } from "./askagent/Approvals";
import { PendingCards } from "./askagent/Cards";
import { Chat } from "./askagent/Chat";
import { GoalsPanel } from "./askagent/Goals";
import { MemoryPanel } from "./askagent/Memory";
import { PromptsPanel } from "./askagent/Prompts";
import { PageHeader } from "../ui/PageHeader";

const TABS = [
  { id: "chat", label: "Chat" },
  { id: "approvals", label: "Approvals" },
  { id: "goals", label: "Goals" },
  { id: "triggers", label: "Triggers" },
  { id: "memory", label: "Memory" },
] as const;

export default function AskAgent() {
  const [params, setParams] = useSearchParams();
  const tab = TABS.find((t) => t.id === params.get("tab"))?.id ?? "chat";
  // Held calls wait for the user whichever tab is open, so the cards sit above
  // the tabs; those of the open conversation wait in its request panel
  // instead. They are read again when a turn changes the board.
  const [cards, reloadCards] = useResource(() => get("/api/agent/confirmations", confirmationList), [], ["runs"]);
  const conversation = tab === "chat" ? params.get("conversation") ?? undefined : undefined;
  return (
    <>
      <PageHeader path="/ask" />
      <PendingCards res={cards} reload={reloadCards} inConversation={conversation} onOpen={(id) => leave(() => setParams({ tab: undefined, conversation: id }))} />
      <Tabs label="Ask Agent sections" tabs={TABS} active={tab} onSelect={(id) => leave(() => setParams({ tab: id === "chat" ? undefined : id }, { replace: true }))}>
        {tab === "chat" && <Chat cards={cards.data?.cards ?? []} reloadCards={reloadCards} />}
        {tab === "approvals" && <StandingApprovals />}
        {tab === "goals" && <GoalsPanel />}
        {tab === "triggers" && <PromptsPanel />}
        {tab === "memory" && <MemoryPanel />}
      </Tabs>
    </>
  );
}
