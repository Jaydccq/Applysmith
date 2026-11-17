import { AskMeBox, labelOf, SchemaForm, type Control } from "./Form";
import { ASK_ME } from "../answerbank/model";
import { text, type ScreenProps } from "./model";
import { FromCv, identityValues, WithProfile } from "./Prefill";

// The street address and the postal code may be left to the person: the box
// beside the input stores the ask-me marker the schema's text names.
const askMeText = (c: Control) => {
  const mine = text(c.value) === ASK_ME;
  return (
    <>
      <input id={c.id} type="text" aria-invalid={c.invalid || undefined} aria-describedby={c.describedBy} disabled={mine} value={mine ? "" : text(c.value)} onChange={(e) => c.set(e.target.value)} />
      <AskMeBox value={text(c.value)} onChange={c.set} />
    </>
  );
};
const ASK_ME_FIELDS = { street_address: askMeText, postal_code: askMeText };
const ASK_ME_HINTS = { street_address: "", postal_code: "" };

// Identity starts from the stored profile. Nothing is invented: an empty field
// stays empty and the writer names what is missing. What the saved CV says is
// offered for the empty fields, and is only stored when the form is saved.
export function Identity({ step, reload }: ScreenProps) {
  return (
    <WithProfile>
      {(profile) => (
        <SchemaForm
          step={step}
          reload={reload}
          initial={identityValues(profile)}
          submit="Save identity"
          columns
          custom={ASK_ME_FIELDS}
          hints={ASK_ME_HINTS}
          lead={(form) => (
            <FromCv
              current={(k) => (k in form.values ? text(form.values[k]) : undefined)}
              use={(found) => found.forEach(([k, v]) => form.set(k, v))}
              label={labelOf}
              save="Save identity"
            />
          )}
        />
      )}
    </WithProfile>
  );
}
