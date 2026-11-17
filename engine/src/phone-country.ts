// Clean-room: docs/provenance.md#CR-ENGINE
// The country-code picker beside a phone number. It carries no label of its
// own, so it is found by structure, and only an option that names the country
// and reads as a plus sign and digits may be committed.

import { displayMatches, phoneCountryChooser } from "./choosers";
import { collapse, normalize } from "./dom";
import type { WriteCtx } from "./env";
import { writeListbox } from "./listbox";
import { labelElements } from "./labels";
import { flatText } from "./dom";
import type { WriteOutcome } from "./writers";
import { isWorkdayPrompt, writeWorkdayPrompt } from "./workday";

const PHONE_WORD = /\b(phone|mobile|telephone|cell)\b/i;
const COUNTRY_WORD = /\b(country|dial|calling|prefix|code)\b/i;

function ownLabel(el: Element): string {
  return collapse([el.getAttribute("aria-label") ?? "", ...labelElements(el).map((l) => flatText(l))].join(" "));
}

/**
 * A searchable control with no label of its own that shares a container
 * (within five levels) with the phone input: no aria-labelledby, no phone word
 * in its label, a search placeholder or a combobox role.
 */
export function findPhoneCountryPicker(phone: HTMLInputElement): HTMLInputElement | null {
  // Ashby keeps each question in its own data-field-path container: a box in another one is that question's (Apex's location).
  const field = phone.closest("[data-field-path]");
  let scope: Element | null = phone.parentElement;
  for (let depth = 0; scope && depth < 5; depth++, scope = scope.parentElement) {
    for (const input of Array.from(scope.querySelectorAll<HTMLInputElement>("input"))) {
      if (input === phone || ["hidden", "checkbox", "radio", "file"].includes(input.type)) continue;
      // intl-tel-input keeps a search box inside its own country dropdown: that is the library's list, not a picker beside the phone box.
      if (input.closest(".iti__dropdown-content, .iti__country-list")) continue;
      if (field && input.closest("[data-field-path]") !== field) continue;
      // Workday's own picker is labelled "Country Phone Code" and is a search prompt.
      if (isWorkdayPrompt(input) && /\bcountry\b.*\bcode\b|\bphone code\b/i.test(ownLabel(input))) return input;
      if (input.hasAttribute("aria-labelledby") || PHONE_WORD.test(ownLabel(input))) continue;
      // A box with a question of its own (an address search beside the phone) is that question's, unless the label names a country or code.
      if (labelElements(input).length > 0 && !COUNTRY_WORD.test(ownLabel(input))) continue;
      const searchy = /search/i.test(input.getAttribute("placeholder") ?? "") || input.getAttribute("role") === "combobox";
      if (searchy) return input;
    }
  }
  return null;
}

/**
 * A box labelled only "Country" (or "Country code") inside a phone fieldset,
 * one whose legend names the phone and that holds the tel input, picks the
 * dial code: it is the phone's country, not the address country.
 */
export function inPhoneFieldset(el: Element, label: string): boolean {
  if (!/^country( code)?$/.test(normalize(label))) return false;
  const fs = el.closest("fieldset");
  const legend = fs?.querySelector(":scope > legend");
  return !!fs && !!legend && PHONE_WORD.test(flatText(legend)) && fs.querySelector("input[type=tel]") !== null;
}

/**
 * Whether a picker's display shows the dial code of the chosen row. The page
 * may render "+" and the digits as separate text ("+ 1"), and "+1" must not
 * read as shown inside "+1242".
 */
export function showsDialCode(display: string, rowLabel: string): boolean {
  const code = /\+\d+/.exec(rowLabel)?.[0];
  if (!code) return false;
  const shown = display.replace(/\s+/g, "");
  const at = shown.indexOf(code);
  return at >= 0 && !/\d/.test(shown.charAt(at + code.length));
}

/**
 * The country a phone's picker takes, from the calling code the server split off the number: a North American
 * number is the United States unless the address says Canada; any other number, and one written without a code,
 * goes by the address country.
 */
export function countryOfPhone(dial: string, addressCountry?: string): string | undefined {
  if (dial === "+1" && !/^\s*(canada|ca)\s*$/i.test(addressCountry ?? "")) return "United States";
  return addressCountry || undefined;
}

/**
 * Commits the picker's country. An input already showing a plus and digits is left alone, and with the phone's dial
 * code known only a row that carries it is taken: a country whose code is another one commits nothing.
 */
export async function commitPhoneCountry(picker: HTMLInputElement, country: string, ctx: WriteCtx, dial = ""): Promise<WriteOutcome> {
  if (isWorkdayPrompt(picker)) {
    // The prompt is searched by country name and a row is accepted only with the exact dial code; without one it abstains.
    if (!dial) return { ok: false, reason: "option-not-found", abstain: true };
    return writeWorkdayPrompt(picker, { answers: [country], label: "country phone code", phoneCountry: { country, dial } }, ctx);
  }
  if (/\+\d/.test(picker.value)) return { ok: true, optionLabel: picker.value };
  const named = phoneCountryChooser(country);
  return writeListbox(
    picker,
    {
      answers: [country],
      label: "phone country code",
      seeds: [country],
      chooser: dial ? (rows) => named(rows.filter((r) => showsDialCode(r.label, dial))) : named,
      accept: (display, row) => showsDialCode(display, row.label) || displayMatches(display, row.label) || normalize(display) === normalize(country),
    },
    ctx,
  );
}
