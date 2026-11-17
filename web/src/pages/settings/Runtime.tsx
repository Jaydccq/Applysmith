import { useId, useState } from "react";
import { get, patch } from "../../api/client";
import { answerBankSettings } from "../../api/decoders_bank";
import { useResource } from "../../hooks";
import { ConfirmDialog } from "../../ui/Modal";
import { ErrorNote, Panel } from "../../ui/parts";
import type { AnswerBankSettings, AnswerBankSettingsPatch } from "../../wire";
import { useAction } from "../action";

// What the legal-declarations toggle does, stated in full where it is turned
// on and beside the switch.
export const LEGAL_TEXT = [
  "When on, apply and sign-up runs tick the required privacy and terms acknowledgements and the legal declarations: certifying that the information is true, authorizing a background check, an at-will acknowledgement and electronic-signature consent.",
  "When off, each of those is left for you.",
  "Optional marketing opt-ins are never ticked, whichever way this is set.",
  "Self-identification answers, work-authorization answers and typed signatures follow their own rules and are not affected.",
  "On by default. Each change is written to the audit log with its time.",
];

const GROUNDING: { value: string; label: string; note: string }[] = [
  { value: "strict", label: "Strict", note: "The default. An answer may name only what your profile and the bank hold." },
  { value: "flex", label: "Flexible", note: "Allows wording around the same facts; an unsupported name or figure still blocks." },
  { value: "dangerous", label: "Least strict", note: "Reports an unsupported name or figure and blocks nothing. Autofill never uses it and treats it as flexible." },
];

function Body({ s, reload }: { s: AnswerBankSettings; reload: () => void }) {
  const act = useAction();
  const [ask, setAsk] = useState<"legal" | "grounding" | null>(null);
  const fallbackId = useId();
  const groundingId = useId();
  const legalId = useId();
  const watchId = useId();
  const semanticId = useId();
  const write = async (body: AnswerBankSettingsPatch) => {
    setAsk(null);
    if (await act.run(() => patch("/api/answer-bank/settings", answerBankSettings, body))) reload();
  };
  return (
    <>
      <p className="muted">Each change applies to the next question or scan that reads it; nothing needs a restart.</p>
      <div className="stack">
        <div>
          <label htmlFor={fallbackId}>
            <input id={fallbackId} type="checkbox" checked={s.llmFallbackEnabled} disabled={act.busy} onChange={(e) => void write({ llmFallbackEnabled: e.target.checked })} /> Model
            fallback: let the model compose an answer when the answer bank has none
          </label>
          <p className="muted">Off by default. On, a choice question (a menu or a set of options) that the answer bank cannot answer is picked by the model from your profile, within the grounding mode below; off, it is left for you.</p>
        </div>

        <div>
          <label htmlFor={groundingId}>Grounding mode</label>{" "}
          <select
            id={groundingId}
            value={s.groundingMode}
            disabled={act.busy}
            aria-describedby={`${groundingId}-note`}
            onChange={(e) => (e.target.value === "dangerous" ? setAsk("grounding") : void write({ groundingMode: e.target.value }))}
          >
            {GROUNDING.map((g) => (
              <option key={g.value} value={g.value}>
                {g.label}
              </option>
            ))}
          </select>
          <p id={`${groundingId}-note`} className="muted">
            {GROUNDING.find((g) => g.value === s.groundingMode)?.note}
          </p>
        </div>

        <div>
          <label htmlFor={legalId}>
            <input
              id={legalId}
              type="checkbox"
              checked={s.autoAcceptLegalDeclarations}
              disabled={act.busy}
              onChange={(e) => (e.target.checked ? setAsk("legal") : void write({ autoAcceptLegalDeclarations: false }))}
            />{" "}
            Tick required legal declarations
          </label>
          {LEGAL_TEXT.map((line) => (
            <p key={line} className="muted">
              {line}
            </p>
          ))}
        </div>

        <div>
          <label htmlFor={semanticId}>
            <input id={semanticId} type="checkbox" checked={s.knowledgeSemanticRetrieval} disabled={act.busy} onChange={(e) => void write({ knowledgeSemanticRetrieval: e.target.checked })} /> Semantic knowledge search
          </label>
          <p className="muted">Uses the configured embedding model for questions that keyword search cannot answer. Sends the question and published snippet text to that provider. Off by default; keyword search stays available.</p>
        </div>
        <div>
          <label htmlFor={watchId}>
            <input id={watchId} type="checkbox" checked={s.watchScans} disabled={act.busy} onChange={(e) => void write({ watchScans: e.target.checked })} /> Watch scans in the
            browser window
          </label>
          <p className="muted">
            Off, browser scans read in a hidden window. On, they run in the visible window with nothing blocked, so you can see them work. Indeed, ZipRecruiter, Glassdoor, Google Jobs and LinkedIn
            always stay hidden. While a watched scan is open, an apply run waits for the window.
          </p>
        </div>
      </div>
      {act.error && <ErrorNote error={act.error} />}
      {ask === "legal" && (
        <ConfirmDialog
          title="Tick required legal declarations"
          consequence={LEGAL_TEXT.slice(0, 4).join(" ")}
          verb="Turn on"
          busy={act.busy}
          onCancel={() => setAsk(null)}
          onConfirm={() => void write({ autoAcceptLegalDeclarations: true })}
        />
      )}
      {ask === "grounding" && (
        <ConfirmDialog
          title="Use the least strict grounding"
          consequence="Answers may then name a company, a figure or a skill that your profile and the bank do not hold. They are reported but nothing is blocked. Autofill never uses this mode and treats it as flexible."
          verb="Use least strict"
          busy={act.busy}
          onCancel={() => setAsk(null)}
          onConfirm={() => void write({ groundingMode: "dangerous" })}
        />
      )}
    </>
  );
}

// RuntimeSection edits the runtime settings: Google sign-in, the model
// fallback, the grounding mode, the legal declarations, semantic knowledge
// search and the watch-scans switch. They live in the workspace database, not
// in answer-bank.yml, so sharing a bank file never carries them.
export function RuntimeSection() {
  const [res, reload] = useResource(() => get("/api/answer-bank/settings", answerBankSettings), [], ["answerbank"], { keep: "settings.runtime" });
  return (
    <Panel title="Runtime settings" id="settings-runtime" res={res} retry={reload} what="the runtime settings">
      {(s) => <Body s={s} reload={reload} />}
    </Panel>
  );
}
