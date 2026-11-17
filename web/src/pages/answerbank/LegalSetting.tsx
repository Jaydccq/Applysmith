import { useState } from "react";
import { patch } from "../../api/client";
import { answerBankSettings } from "../../api/decoders_bank";
import { asApiError } from "../../hooks";
import { ConfirmDialog } from "../../ui/Modal";
import { Chip, ErrorNote } from "../../ui/parts";
import type { AnswerBankSettings } from "../../wire";

// LegalSetting is the user's own switch for required legal declarations. It
// is off until the user turns it on, and turning it on is confirmed.
export function LegalSetting({ settings, reload }: { settings: AnswerBankSettings; reload: () => void }) {
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const on = settings.autoAcceptLegalDeclarations;

  const set = async (value: boolean) => {
    setBusy(true);
    setError(null);
    try {
      await patch("/api/answer-bank/settings", answerBankSettings, { autoAcceptLegalDeclarations: value });
      reload();
    } catch (e) {
      setError(asApiError(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="setting-row">
      <div>
        <h3>
          Legal declarations <Chip tone={on ? "warn" : "neutral"}>{on ? "On" : "Off"}</Chip>
        </h3>
        <p className="muted">Tick required legal declarations (privacy and terms acknowledgements, &ldquo;I certify the information is true&rdquo;, background-check authorisation, at-will and e-signature consent) in apply and sign-up runs; never optional marketing opt-ins, never EEO or work-authorization answers, never typed signatures.</p>
      </div>
      <button type="button" className={on ? undefined : "primary"} disabled={busy} onClick={() => (on ? void set(false) : setConfirm(true))}>
        {on ? "Turn off" : "Turn on"}
      </button>
      {error != null && <ErrorNote error={error} />}
      {confirm && (
        <ConfirmDialog
          title="Tick required legal declarations automatically"
          consequence="Apply and sign-up runs will tick required privacy, terms and certification boxes for you. Optional marketing opt-ins, EEO and work-authorization answers and typed signatures are never ticked this way."
          verb="Turn on"
          busy={busy}
          onCancel={() => setConfirm(false)}
          onConfirm={() => {
            setConfirm(false);
            void set(true);
          }}
        />
      )}
    </div>
  );
}
