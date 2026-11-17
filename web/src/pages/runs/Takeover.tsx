import { Bot } from "lucide-react";
import { post } from "../../api/client";
import { takeoverStarted } from "../../api/decoders_agent";
import { navigate } from "../../router";
import { Button } from "../../ui/Button";
import { ErrorNote } from "../../ui/parts";
import type { BoardEntry } from "../../wire";
import { useAction } from "../action";

// canLetAgentFinish is the run the server hands to the agent (owner decision
// AC-4): one stopped on questions for the user whose tab is still kept, and
// that Done can resume, because the agent's last saved answer continues the
// run in that tab the way Done does. The server offers Done (canDone) only on
// a waiting application run that the user or the agent started.
export const canLetAgentFinish = (e: BoardEntry): boolean => e.reason === "answer" && e.canFocusTab === true && e.canDone === true;

// LetAgentFinish starts an Ask Agent turn for one stopped run and opens its
// conversation. The agent answers from the Answer Bank and the profile, asks
// the user for the rest, and the run then stops at Submit for a confirmation.
// Compact is the form beside a Needs you item; the drawer says what happens.
export function LetAgentFinish({ entry, compact }: { entry: BoardEntry; compact?: boolean }) {
  const act = useAction();
  if (!canLetAgentFinish(entry)) return null;
  const start = async () => {
    const out = await act.run(() => post(`/api/apply/runs/${encodeURIComponent(entry.id)}/takeover`, takeoverStarted));
    if (out) navigate(`/ask?conversation=${encodeURIComponent(out.sessionId)}`);
  };
  return (
    <div className={compact ? "takeover compact" : "takeover"}>
      <Button variant={compact ? undefined : "primary"} size={compact ? "sm" : undefined} icon={Bot} disabled={act.busy} onClick={() => void start()}>
        {act.busy ? "Starting the agent…" : "Let the agent finish"}
      </Button>
      {!compact && (
        <p className="help">
          The Ask Agent answers each question from your Answer Bank or profile and asks you for the rest. The run then goes on in its tab and stops at
          Submit for your confirmation.
        </p>
      )}
      {act.error && <ErrorNote error={act.error} />}
    </div>
  );
}
