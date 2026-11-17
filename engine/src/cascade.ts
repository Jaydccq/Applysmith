// Clean-room: docs/provenance.md#CR-ENGINE
// Controls that depend on other controls: a country is written before its
// state and its postal code, and a state before its county.

const PARENT = /^(country|country region|country of residence)$/;
const STATE = /^(state|province|region|state province)$/;
const CHILD = /\b(state|province|region|postal|post code|zip|county|city)\b/;

/**
 * Puts a country ahead of the controls that depend on it, leaving every other
 * order alone: only a country that sits after one of its dependents moves.
 */
export function dependencyOrder<T>(items: T[], label: (item: T) => string): T[] {
  const out = items.slice();
  const norm = (item: T) => label(item).toLowerCase().replace(/[^a-z ]+/g, " ").replace(/\s+/g, " ").trim();
  const parent = out.findIndex((i) => PARENT.test(norm(i)));
  const child = out.findIndex((i) => CHILD.test(norm(i)) && !PARENT.test(norm(i)));
  if (parent >= 0 && child >= 0 && child < parent) {
    const [moved] = out.splice(parent, 1);
    out.splice(child, 0, moved as T);
  }
  // A county's options depend on the state: the state commits first.
  const county = out.findIndex((i) => norm(i) === "county");
  const state = out.findIndex((i) => STATE.test(norm(i)));
  if (county >= 0 && state > county) {
    const [moved] = out.splice(state, 1);
    out.splice(county, 0, moved as T);
  }
  return out;
}
