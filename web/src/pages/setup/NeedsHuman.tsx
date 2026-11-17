import { useResource } from "../../hooks";
import { pageFor } from "../../pages";
import { Link } from "../../router";
import { ErrorNote, Loading } from "../../ui/parts";
import { Connect, loadHealth } from "../gmail/Health";
import { ApplyButton } from "./apply";
import type { ScreenProps } from "./model";

// Commands lists the command line that does what the screen describes, as the
// catalog gives it.
export function Commands({ commands }: { commands: readonly string[] }) {
  if (commands.length === 0) return null;
  return (
    <div>
      <h3>Command line</h3>
      <ul className="plain stack">
        {commands.map((c) => (
          <li key={c}>
            <code className="pre">{c}</code>
          </li>
        ))}
      </ul>
    </div>
  );
}

// A dashboard path only: anything else the catalog sends is shown as text.
const dashboardPath = (p: string) => p.startsWith("/") && !p.startsWith("//");

// GmailSteps does the Gmail step in place: the address and app password
// form, as Settings, Gmail draws it.
function GmailSteps() {
  const [health, reload] = useResource(loadHealth, [], ["gmail"]);
  if (health.status === "error") return <ErrorNote error={health.error} onRetry={reload} what="the Gmail connection" />;
  return health.data ? <Connect h={health.data} onChanged={reload} /> : <Loading />;
}

// NeedsHuman is the screen of a step a person finishes outside the dashboard:
// what to do, the command line for it, and where the feature lives. The step
// completes by itself once the action is seen on the event stream.
export function NeedsHuman(props: ScreenProps) {
  const { step } = props;
  const page = step.page && dashboardPath(step.page) ? step.page : undefined;
  return (
    <div>
      {step.instruction && <p>{step.instruction}</p>}
      {step.id === "gmail" && <GmailSteps />}
      <Commands commands={step.commands} />
      {step.page && (
        <p>
          {page ? <Link to={page}>Open {pageFor(page.split("#")[0])?.title ?? "the page"} for this step</Link> : <code>{step.page}</code>}
        </p>
      )}
      <p className="muted" role="status">
        This page updates by itself when the action is detected.
      </p>
      {step.id === "browser" && <ApplyButton step={step} reload={props.reload} label="Check the apply window" />}
    </div>
  );
}
