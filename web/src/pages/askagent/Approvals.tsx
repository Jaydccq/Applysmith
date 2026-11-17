import { useState } from "react";
import { del, get } from "../../api/client";
import { standingApprovalList, standingApprovalRevoked } from "../../api/decoders_agent";
import { useResource } from "../../hooks";
import { ConfirmDialog } from "../../ui/Modal";
import { Chip, Empty, ErrorNote, Panel } from "../../ui/parts";
import type { StandingApproval } from "../../wire";
import { useAction, when } from "../action";

const SCOPE_TEXT: Record<string, string> = { conversation: "this conversation", today: "today" };

function Row({ a, onRevoke }: { a: StandingApproval; onRevoke: (a: StandingApproval) => void }) {
  return (
    <li className="row">
      <span>
        <strong>{a.tool}</strong> on {a.target} <Chip>{SCOPE_TEXT[a.scope] ?? a.scope}</Chip>
        <span className="muted">
          {" "}
          · used {a.uses} {a.uses === 1 ? "time" : "times"}
          {a.lastUsedAt ? `, last ${when(a.lastUsedAt)}` : ""} · ends {when(a.expiresAt)}
        </span>
      </span>
      <button type="button" className="danger-outline" onClick={() => onRevoke(a)} aria-label={`Revoke ${a.tool} on ${a.target}`}>
        Revoke
      </button>
    </li>
  );
}

// StandingApprovals lists the approvals that let a tool run on one target
// without asking again. Revoking one is confirmed; the next matching call is
// held again.
export function StandingApprovals() {
  const [res, reload] = useResource(() => get("/api/agent/approvals", standingApprovalList), []);
  const [pending, setPending] = useState<StandingApproval | null>(null);
  const act = useAction();

  const revoke = async (a: StandingApproval) => {
    setPending(null);
    const done = await act.run(() => del(`/api/agent/approvals/${encodeURIComponent(a.id)}`, standingApprovalRevoked));
    if (done) reload();
  };

  return (
    <Panel title="Standing approvals" id="ask-approvals" res={res} retry={reload}>
      {(list) => (
        <>
          <p className="muted">A standing approval is made only from a confirmation card here. It covers one tool on one target and ends at the earliest of its scope and 24 hours.</p>
          {act.error && <ErrorNote error={act.error} />}
          {list.approvals.length === 0 ? (
            <Empty>No standing approval is active.</Empty>
          ) : (
            <ul className="plain stack">
              {list.approvals.map((a) => (
                <Row key={a.id} a={a} onRevoke={setPending} />
              ))}
            </ul>
          )}
          {pending && (
            <ConfirmDialog
              title={`Revoke the ${pending.tool} approval`}
              consequence={`The next ${pending.tool} call on ${pending.target} is held for your decision again.`}
              verb="Revoke"
              busy={act.busy}
              onCancel={() => setPending(null)}
              onConfirm={() => void revoke(pending)}
            />
          )}
        </>
      )}
    </Panel>
  );
}
