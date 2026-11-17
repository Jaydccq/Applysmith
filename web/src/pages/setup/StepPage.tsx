import { ArrowLeft, ArrowRight } from "lucide-react";
import { useState, type ComponentType } from "react";
import { asApiError, useResource } from "../../hooks";
import { Link } from "../../router";
import { DocText, docLinks } from "../Guide";
import { Chip, ErrorNote, Loading } from "../../ui/parts";
import { Prerequisites, Workspace, decide } from "./apply";
import { AnswerBank } from "./AnswerBank";
import { Cv } from "./Cv";
import { Discovery } from "./Discovery";
import { SchemaForm } from "./Form";
import { Identity } from "./Identity";
import { Llm } from "./Llm";
import { STATUS_TONE, statusWord, type ScreenProps } from "./model";
import { NeedsHuman, Commands } from "./NeedsHuman";
import { Orientation } from "./Orientation";
import { IDENTITY, pathLabels, TARGETS, WORK_AUTH } from "./Prefill";
import { ReasonList } from "./Reasons";
import { Resumes } from "./Resumes";
import { Story } from "./Story";
import { TARGET_LABELS, Targets } from "./Targets";
import { WORK_AUTH_LABELS, WorkAuth } from "./WorkAuth";

interface Screen {
  Body: ComponentType<ScreenProps>;
  /** The screen lists the reasons itself. */
  ownsReasons?: boolean;
  /** Review reasons, by field, that the screen settles with its own control. */
  reviews?: string[];
  /** The label of the form field that edits each profile key the reasons name. */
  labels?: Record<string, string>;
}

// The purpose-built screens, by step id. A step with none gets the form its
// schema describes, or the instructions of a step a person finishes outside
// the dashboard, so a step added to the catalog is never a blank page.
const SCREENS: Record<string, Screen> = {
  prerequisites: { Body: Prerequisites },
  workspace: { Body: Workspace },
  cv: { Body: Cv, reviews: ["history"] },
  identity: { Body: Identity, labels: pathLabels(IDENTITY) },
  "work-authorization": { Body: WorkAuth, labels: pathLabels(WORK_AUTH, WORK_AUTH_LABELS) },
  targets: { Body: Targets, reviews: ["batch_skip_companies"], labels: pathLabels(TARGETS, TARGET_LABELS) },
  story: { Body: Story },
  resumes: { Body: Resumes },
  "answer-bank": { Body: AnswerBank, ownsReasons: true },
  llm: { Body: Llm },
  browser: { Body: NeedsHuman },
  gmail: { Body: NeedsHuman },
  telegram: { Body: NeedsHuman },
  credentials: { Body: NeedsHuman },
  captcha: { Body: NeedsHuman },
  discovery: { Body: Discovery },
  extension: { Body: NeedsHuman },
  // Its own "I have read this" settles the acknowledgement.
  orientation: { Body: Orientation, reviews: ["orientation"] },
};

const stepPath = (id: string) => `/setup?step=${encodeURIComponent(id)}`;

// The setup guides ship in the bundle, one chunk each loaded when opened, so a
// step's guide reads in the app instead of naming a file in the source tree.
const GUIDES = import.meta.glob<string>("../../../../docs/setup/[0-9]*.md", { query: "?raw", import: "default" });

// guideText fits a guide under the step: the step page carries its title, and
// a link to another guide goes to the page that shows it.
const guideText = (md: string) => docLinks(md.replace(/^# .*\n/, ""), "setup");

function Guide({ path }: { path: string }) {
  const [open, setOpen] = useState(false);
  const load = GUIDES[`../../../../${path}`];
  if (!load) return null;
  return (
    <details className="step-guide" onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>Guide for this step</summary>
      {open && <GuideText load={load} />}
    </details>
  );
}

function GuideText({ load }: { load: () => Promise<string> }) {
  const [res, retry] = useResource(load, [load]);
  if (res.status === "error") return <ErrorNote error={res.error} onRetry={retry} />;
  return res.data === undefined ? <Loading label="Loading the guide" /> : <DocText source={guideText(res.data)} />;
}

function Generic(props: ScreenProps) {
  if (props.step.needsHuman) return <NeedsHuman {...props} />;
  return props.step.fields.length > 0 ? <SchemaForm step={props.step} reload={props.reload} /> : null;
}

// StepPage is one step: what it is for, why it is not done, the screen that
// does it, and the decisions the person can take about it.
export function StepPage({ step, state, reload }: ScreenProps) {
  const screen = SCREENS[step.id];
  const Body = screen?.Body ?? Generic;
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const ackable = step.reasons.some((r) => r.ackable && !screen?.reviews?.includes(r.field ?? ""));
  const next = state.steps.find((s) => s.id === state.next);
  // The pager walks the catalog in order; once this step is done and the first
  // unfinished one lies elsewhere, it goes there instead.
  const at = state.steps.findIndex((s) => s.id === step.id);
  const [prev, after] = [state.steps[at - 1], state.steps[at + 1]];
  const done = step.status === "complete" || step.status === "skipped";
  const jump = done && next && next.id !== step.id && next.id !== after?.id ? next : undefined;

  const take = async (kind: "ack" | "skip" | "unskip") => {
    setBusy(true);
    setError(null);
    try {
      await decide(step.id, kind);
      reload();
    } catch (e) {
      setError(asApiError(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <article aria-labelledby={`step-${step.id}`} data-step={step.id} className="panel step-page">
      <header className="row">
        <h2 id={`step-${step.id}`} tabIndex={-1}>
          {step.n}. {step.title}
        </h2>
        <span className="chips">
          <Chip tone={STATUS_TONE[step.status] ?? "neutral"}>{statusWord(step.status)}</Chip>
          {!step.required && <Chip>optional</Chip>}
        </span>
      </header>
      <p className="step-purpose">{step.purpose}</p>
      {step.status === "skipped" && <p className="note">This step is skipped.{step.skipCost ? ` ${step.skipCost}` : ""}</p>}
      {!screen?.ownsReasons && step.reasons.length > 0 && (
        <section aria-label={step.status === "complete" ? "Done because" : "What is left"} className={step.status === "complete" ? "reasons-box done" : "reasons-box"}>
          <h3>{step.status === "complete" ? "Done because" : "What is left"}</h3>
          <ReasonList reasons={step.reasons} labels={screen?.labels} />
        </section>
      )}
      {step.warnings.length > 0 && (
        <ul className="plain stack" aria-label="Warnings">
          {step.warnings.map((w) => (
            <li key={w} className="note">
              {w}
            </li>
          ))}
        </ul>
      )}
      {ackable && (
        <div>
          <p>Some of these values match the shipped example or still need your review. If they are really yours, acknowledge them; if you change them later the acknowledgement stops applying.</p>
          <div className="actions">
            <button type="button" disabled={busy} onClick={() => void take("ack")}>
              Acknowledge
            </button>
          </div>
        </div>
      )}
      <Body step={step} state={state} reload={reload} />
      {Body !== NeedsHuman && step.commands.length > 0 && (
        <details>
          <summary>Command line</summary>
          <Commands commands={step.commands} />
        </details>
      )}
      {!step.required && (
        <section aria-label="Skip">
          <h3>Skipping</h3>
          {step.skipCost && <p>{step.skipCost}</p>}
          <div className="actions">
            {step.status === "skipped" ? (
              <button type="button" disabled={busy} onClick={() => void take("unskip")}>
                Unskip this step
              </button>
            ) : (
              <button type="button" disabled={busy} onClick={() => void take("skip")}>
                Skip this step
              </button>
            )}
          </div>
        </section>
      )}
      {error != null && <ErrorNote error={error} />}
      <Guide path={step.guide} />
      <dl className="facts">
        <dt>Stored in</dt>
        <dd>{step.storage}</dd>
      </dl>
      <nav aria-label="Other steps" className="actions step-pager">
        {prev && (
          <Link to={stepPath(prev.id)} className="button">
            <ArrowLeft />
            {prev.title}
          </Link>
        )}
        <span className="spacer" />
        {jump ? (
          <Link to={stepPath(jump.id)} className="button primary">
            Continue to {jump.title}
            <ArrowRight />
          </Link>
        ) : (
          after && (
            <Link to={stepPath(after.id)} className={done ? "button primary" : "button"}>
              Next: {after.title}
              <ArrowRight />
            </Link>
          )
        )}
      </nav>
    </article>
  );
}
