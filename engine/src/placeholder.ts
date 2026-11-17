// Clean-room: docs/provenance.md#CR-ENGINE
// The placeholder vocabulary: text a widget shows when nothing is chosen.

import { collapse } from "./dom";

const PLACEHOLDER =
  /^(select( one| an? (option|item|value))?|choose( one| an? (option|value))?|please (select|choose)( one)?( an? option)?|no selection|none selected|no answer|make a selection|[-–—]+\s*.*\s*[-–—]+)[\s.…:-]*$/i;

export function isPlaceholderText(text: string): boolean {
  const t = collapse(text);
  return t === "" || PLACEHOLDER.test(t);
}

const SAMPLE =
  /^(e\.?g\.?|ex\.?|example|for example|i\.e\.?)(\s|:|$)|@|\d{2,}|^(https?:\/\/|www\.)|\.(com|org|net|io)\b|^(john|jane) (doe|smith)$|^(start typing|type (here|to|your answer|something)|click (here|to)|tap to|write (here|something)|your answer)\b/i;

/** Text a field shows as a sample value or an instruction, never as the name of what it asks. */
export function isSampleValue(text: string): boolean {
  return SAMPLE.test(collapse(text));
}
