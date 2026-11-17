import { useId } from "react";
import { SchemaForm, type Control, type Custom } from "./Form";
import { text, type ScreenProps } from "./model";
import { WithProfile, workAuthValues } from "./Prefill";

// Choice is a yes or no question with nothing preselected: blank means not
// answered, and it is never read as no.
function Choice({ control, label }: { control: Control; label: string }) {
  const name = useId();
  return (
    <div role="group" aria-label={label} className="chips">
      {([true, false] as const).map((answer) => (
        <label key={String(answer)} className="check">
          <input type="radio" name={name} checked={control.value === answer} onChange={() => control.set(answer)} />
          {answer ? "Yes" : "No"}
        </label>
      ))}
      {control.value === undefined && <span className="muted">Not answered yet. Forms that ask are left for you.</span>}
    </div>
  );
}

export const WORK_AUTH_LABELS = { status_detail: "Visa class or end date", requires_sponsorship_now: "Sponsorship needed now", requires_sponsorship_future: "Sponsorship needed in the future" };

// WorkAuth shows the visa class or the end date only where the status needs
// one, and never preselects a sponsorship answer.
export function WorkAuth({ step, reload }: ScreenProps) {
  const statuses = step.fields.find((f) => f.name === "authorization_status")?.enum ?? [];
  return (
    <WithProfile>
      {(profile) => (
        <SchemaForm
          step={step}
          reload={reload}
          initial={workAuthValues(profile, statuses)}
          submit="Save authorization"
          labels={WORK_AUTH_LABELS}
          visible={(name, form) => name !== "status_detail" || ["work_visa", "student_work_authorization"].includes(text(form.values.authorization_status))}
          custom={(form): Custom => ({
            requires_sponsorship_now: (c) => <Choice control={c} label="Sponsorship needed now" />,
            requires_sponsorship_future: (c) => <Choice control={c} label="Sponsorship needed in the future" />,
            ...(text(form.values.authorization_status) === "student_work_authorization"
              ? { status_detail: (c: Control) => <input id={c.id} type="text" placeholder="YYYY-MM-DD" value={text(c.value)} onChange={(e) => c.set(e.target.value)} aria-describedby={c.describedBy} /> }
              : {}),
          })}
        />
      )}
    </WithProfile>
  );
}
