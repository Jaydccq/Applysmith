import { get } from "../api/client";
import { answerBankFile, answerBankSettings, answerBankUnanswered, answerBankUsage, answerCandidateList } from "../api/decoders_bank";
import { useResource } from "../hooks";
import { leave, useSearchParams } from "../router";
import { Tabs } from "../ui/Tabs";
import { ErrorNote, Loading, Panel } from "../ui/parts";
import { Candidates } from "./answerbank/Candidates";
import { Employers, Entries } from "./answerbank/Entries";
import { LegalSetting } from "./answerbank/LegalSetting";
import { suggested } from "./answerbank/model";
import { Relationships } from "./profile/Identity";
import { ProfilePanel } from "./profile/shared";
import { TranscriptPanel } from "./answerbank/Transcript";
import { Usage } from "./answerbank/Usage";
import { PageHeader } from "../ui/PageHeader";

const TABS = [
  { id: "entries", label: "Entries" },
  { id: "employers", label: "Employers" },
  { id: "transcript", label: "Transcript" },
  { id: "candidates", label: "Suggestions" },
  { id: "usage", label: "Usage" },
  { id: "settings", label: "Settings" },
] as const;
const LIVE = ["answerbank"];

// Every read follows the answerbank topic, which moves on any write of the
// bank file (the CLI's too); an editor keeps its unsaved edits through it.
// Each tab reads what it shows when it shows; only the suggestions, counted on
// the tab bar, are read on every tab.
const useBankFile = () => useResource(() => get("/api/answer-bank", answerBankFile), [], LIVE);

function EntriesTab({ selected, onSelect }: { selected: string; onSelect: (id: string) => void }) {
  const [file, reloadFile] = useBankFile();
  const [unanswered, reloadUnanswered] = useResource(() => get("/api/answer-bank/unanswered", answerBankUnanswered), [], LIVE);
  const reload = () => {
    reloadFile();
    reloadUnanswered();
  };
  return (
    <Panel title="Entries" id="ab-entries" res={file} retry={reloadFile}>
      {(f) =>
        unanswered.data ? (
          <Entries file={f} unanswered={unanswered.data} selected={selected} onSelect={onSelect} reload={reload} />
        ) : unanswered.status === "error" ? (
          <ErrorNote error={unanswered.error} onRetry={reloadUnanswered} />
        ) : (
          <Loading label="Loading what still needs an answer" />
        )
      }
    </Panel>
  );
}

// Your own employers and your relatives' are apart: "Have you worked for
// Northwind?" reads the first, "Does a relative work at Northwind?" the
// profile's relative relationships.
function EmployersTab() {
  const [file, reloadFile] = useBankFile();
  return (
    <>
      <Panel title="Companies you worked for" id="ab-employers" res={file} retry={reloadFile}>
        {(f) => <Employers file={f} reload={reloadFile} />}
      </Panel>
      <ProfilePanel title="Companies where a relative works" id="ab-relatives">
        {(doc, reload) => (
          <>
            <p className="muted">
              Questions such as &ldquo;Do you have a relative who works at Northwind?&rdquo; are answered from this list. Once you mark it complete, a company that is not on it is answered No.
            </p>
            <Relationships values={doc.values ?? {}} revision={doc.revision} reload={reload} only={["relative"]} />
          </>
        )}
      </ProfilePanel>
    </>
  );
}

function UsageTab() {
  const [file] = useBankFile();
  const [usage, reloadUsage] = useResource(() => get("/api/answer-bank/usage", answerBankUsage), [], LIVE);
  return (
    <Panel title="Usage" id="ab-usage" res={usage} retry={reloadUsage}>
      {(u) => <Usage usage={u} entries={file.data?.bank.entries ?? []} />}
    </Panel>
  );
}

function SettingsTab() {
  const [settings, reloadSettings] = useResource(() => get("/api/answer-bank/settings", answerBankSettings), [], LIVE);
  return (
    <Panel title="Runtime settings" id="ab-settings" res={settings} retry={reloadSettings}>
      {(s) => <LegalSetting settings={s} reload={reloadSettings} />}
    </Panel>
  );
}

export default function AnswerBank() {
  const [params, setParams] = useSearchParams();
  const tab = TABS.find((t) => t.id === params.get("tab"))?.id ?? "entries";
  const selected = params.get("entry") ?? "";
  const [candidates, reloadCandidates] = useResource(() => get("/api/answer-bank/candidates", answerCandidateList), [], LIVE);
  const pending = (candidates.data?.candidates.filter((c) => c.state === "pending" && suggested(c)).length ?? 0) + (candidates.data?.edits?.length ?? 0);
  const tabs = TABS.map((t) => (t.id === "candidates" && pending > 0 ? { ...t, label: `Suggestions (${pending})` } : t));

  return (
    <>
      <PageHeader path="/answer-bank" />
      <Tabs label="Answer Bank sections" tabs={tabs} active={tab} onSelect={(id) => leave(() => setParams({ tab: id === "entries" ? undefined : id }, { replace: true }))}>
        {tab === "entries" && <EntriesTab selected={selected} onSelect={(id) => setParams({ entry: id || undefined })} />}
        {tab === "employers" && <EmployersTab />}
        {tab === "transcript" && <TranscriptPanel />}
        {tab === "candidates" && (
          <Panel title="Suggested answers" id="ab-candidates" res={candidates} retry={reloadCandidates}>
            {(c) => <Candidates candidates={c.candidates} edits={c.edits ?? []} reload={reloadCandidates} />}
          </Panel>
        )}
        {tab === "usage" && <UsageTab />}
        {tab === "settings" && <SettingsTab />}
      </Tabs>
    </>
  );
}
