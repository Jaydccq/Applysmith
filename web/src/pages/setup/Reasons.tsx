import { CircleAlert } from "lucide-react";
import { Fragment } from "react";
import type { SetupReason } from "../../wire";
import { groupReasons } from "./model";

// A dotted configuration path, such as candidate.address.city, that the
// reason's own words do not already show. It is drawn under the sentence as
// a detail for someone editing the file by hand.
const PATH = /^[a-z_]+(\.[a-z_0-9[\]]+)+$/;
function pathOf(r: SetupReason): string | undefined {
  return r.field && PATH.test(r.field) && !r.text.includes(r.field) ? r.field : undefined;
}

// A command the detector quotes in backticks is drawn as code, not with its
// backticks showing.
export function withCode(text: string) {
  return text.split(/`([^`]+)`/).map((part, i) => (i % 2 === 1 ? <code key={i}>{part}</code> : <Fragment key={i}>{part}</Fragment>));
}

function Reason({ reason, named }: { reason: SetupReason; named?: boolean }) {
  const path = named ? undefined : pathOf(reason);
  return (
    <span className="reason-text">
      {withCode(reason.text)}
      {path && <span className="reason-field">{path}</span>}
    </span>
  );
}

// labelIn finds the form label of a reason's key: a key under a field's own,
// such as targets.roles[0].title under targets.roles, takes that field's label.
const labelIn = (labels: Record<string, string>, field = ""): string | undefined =>
  Object.entries(labels).find(([path]) => field === path || field.startsWith(path + ".") || field.startsWith(path + "["))?.[1];

// ReasonList shows why a step is not complete, in the words the detector chose.
// Reasons that share a field are grouped under it with their count. With the
// labels of the form below it, the blank fields are named once, by the form's
// own labels and in its order, and no key path is shown.
export function ReasonList({ reasons, labels = {} }: { reasons: readonly SetupReason[]; labels?: Record<string, string> }) {
  const order = Object.values(labels);
  const blank = [...new Set(reasons.flatMap((r) => (r.blank ? (labelIn(labels, r.field) ?? []) : [])))].sort((a, b) => order.indexOf(a) - order.indexOf(b));
  const rest = reasons.filter((r) => !(r.blank && labelIn(labels, r.field)));
  const listed = blank.length > 1 ? `${blank.slice(0, -1).join(", ")} and ${blank[blank.length - 1]}` : blank[0];
  return (
    <ul className="plain stack reasons">
      {blank.length > 0 && (
        <li>
          <CircleAlert />
          <span className="reason-text">
            {blank.length === 1 ? "1 field" : `${blank.length} fields`} to fill in: {listed}.
          </span>
        </li>
      )}
      {groupReasons(rest).map((g) =>
        "reason" in g ? (
          <li key={g.reason.text}>
            <CircleAlert />
            <Reason reason={g.reason} named={!!labelIn(labels, g.reason.field)} />
          </li>
        ) : (
          <li key={g.field}>
            <CircleAlert />
            <span className="reason-text">
              <strong>
                {g.field} ({g.items.length})
              </strong>
              <ul className="stack">
                {g.items.map((r) => (
                  <li key={r.text}>
                    <Reason reason={r} />
                  </li>
                ))}
              </ul>
            </span>
          </li>
        ),
      )}
    </ul>
  );
}
