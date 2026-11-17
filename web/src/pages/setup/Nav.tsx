import { CircleCheck, CircleDashed, CircleDot, CircleMinus, type LucideIcon } from "lucide-react";
import { useNarrow } from "../../hooks";
import { Link } from "../../router";
import { Chip, ProgressBar } from "../../ui/parts";
import type { SetupStep } from "../../wire";
import { phaseLabel, STATUS_TONE, statusWord } from "./model";

// Progress counts the required steps that are satisfied; a count says how
// much is left where a percentage does not.
export function Progress({ percent, steps }: { percent: number; steps: readonly SetupStep[] }) {
  const required = steps.filter((s) => s.required);
  return (
    <div className="setup-percent">
      <p className="progress-line">
        <span>
          <strong>
            {required.filter((s) => s.status === "complete").length} of {required.length}
          </strong>{" "}
          required steps are done
        </span>
      </p>
      <ProgressBar percent={percent} label="Setup progress" tone={percent >= 100 ? "good" : undefined} />
    </div>
  );
}

// The icon each status is drawn with beside its step, next to the status word.
const STATUS_ICON: Record<string, LucideIcon> = { complete: CircleCheck, skipped: CircleMinus, partial: CircleDot, template: CircleDot, missing: CircleDashed };

// StepNav lists every step the state holds, under its phase, with its status
// and whether it is optional. The current step is marked and each entry is a
// link, so the choice lives in the address. On a phone the whole list folds
// behind one line above the step, and folds again when another step opens.
export function StepNav({ steps, current }: { steps: readonly SetupStep[]; current: string }) {
  const phone = useNarrow();
  const phases: { phase: string; steps: SetupStep[] }[] = [];
  for (const s of steps) {
    const last = phases[phases.length - 1];
    if (last && last.phase === s.phase) last.steps.push(s);
    else phases.push({ phase: s.phase, steps: [s] });
  }
  const list = phases.map((p) => (
    <div key={p.phase + p.steps[0].id}>
      <h3 className="group">{phaseLabel(p.phase)}</h3>
      <ul className="plain">
        {p.steps.map((s) => (
          <li key={s.id}>
            <Link to={`/setup?step=${encodeURIComponent(s.id)}`} aria-current={s.id === current ? "step" : undefined}>
              <StepIcon status={s.status} />
              <span>
                {s.n}. {s.title}
              </span>
              <span className="chips">
                <Chip tone={STATUS_TONE[s.status] ?? "neutral"}>{statusWord(s.status)}</Chip>
                {!s.required && <Chip>optional</Chip>}
              </span>
            </Link>
          </li>
        ))}
      </ul>
    </div>
  ));
  return (
    <nav aria-label="Setup steps" className="setup-nav">
      {phone ? (
        <details key={current}>
          <summary>
            Steps ({steps.filter((s) => s.status === "complete").length} of {steps.length} done)
          </summary>
          {list}
        </details>
      ) : (
        list
      )}
    </nav>
  );
}

function StepIcon({ status }: { status: string }) {
  const Icon = STATUS_ICON[status] ?? CircleDashed;
  return (
    <span className={`step-icon ${STATUS_TONE[status] ?? "neutral"}`}>
      <Icon />
    </span>
  );
}
