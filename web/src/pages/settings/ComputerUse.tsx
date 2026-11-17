import { Info } from "lucide-react";
import { get } from "../../api/client";
import { computerUseStatus } from "../../api/decoders_agent";
import { useResource } from "../../hooks";
import { Link } from "../../router";
import { Panel } from "../../ui/parts";
import { ServiceHealth } from "../AgentControl";

// ComputerUseSection shows PersonalCU, the computer-use service the agent's
// desktop tools go through, as Agent control does. Its activity, Pause and
// Stop are on that page.
export function ComputerUseSection() {
  const [res, reload] = useResource(() => get("/api/computer-use", computerUseStatus), [], [], { keep: "settings.computer-use" });
  return (
    <Panel title="Computer use" id="settings-computeruse" res={res} retry={reload} what="the state of PersonalCU" actions={<Link to="/agent-control">Open Agent control</Link>}>
      {(s) => (
        <>
          <ServiceHealth status={s} />
          {/* macOS ties a grant to the app's signature: the local signing identity
              gives every build the same one, an ad hoc build a new one each time
              (docs/architecture/computer-use.md). */}
          <p className="note info with-icon">
            <Info aria-hidden="true" />
            <span>
              PersonalCU keeps Accessibility and Screen Recording across rebuilds once it is signed with its local signing identity. A build without that identity, or the
              first build signed with it, needs both granted again. Applysmith asks for neither.
            </span>
          </p>
        </>
      )}
    </Panel>
  );
}
