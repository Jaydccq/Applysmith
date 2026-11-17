// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Widget drivers for custom controls (country-to-state cascade, cascade completion, write order);
// Drivers for widget libraries (dependent native selects).
import { describe, expect, test } from "vitest";
import { dependencyOrder } from "../src/cascade";
import { ADA, PAD, byId, ctx, fakeHost, makeEngine, mount, rowFor, runFill, summaryOf, write } from "./helpers";
import { writeListbox } from "../src/listbox";
import { mountMenu } from "./widgets-fixtures";

const US_STATES = `<option value="NC">North Carolina</option><option value="SC">South Carolina</option>`;

/** A state select that holds only its placeholder until the country select commits. */
function cascadeForm(order: "state-first" | "country-first", delayMs = 30) {
  const country = `<label for="country">Country</label><select id="country"><option value="">Select</option><option value="US">United States</option><option value="CA">Canada</option></select>`;
  const state = `<label for="state">State</label><select id="state"><option value="">Select</option></select>`;
  const postal = `<label for="zip">Zip code</label><input id="zip">`;
  mount(order === "state-first" ? `${state}${country}${postal}${PAD}` : `${country}${state}${postal}${PAD}`);
  byId("country").addEventListener("change", () => {
    setTimeout(() => {
      byId<HTMLSelectElement>("state").innerHTML = `<option value="">Select</option>${US_STATES}`;
    }, delayMs);
  });
}

describe("Write order and cascades", () => {
  test("Cascade: a state select has one option until the country is set; the engine sets the country, waits for the state options and then sets the state and postal code", async () => {
    cascadeForm("state-first");
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    expect(byId("country").value).toBe("US");
    expect(byId("state").value).toBe("NC");
    expect(byId("zip").value).toBe("27701");
    expect(rowFor(s, "state").outcome).toBe("filled");
  });

  test("Dependent native select: the state select grows from one option to fifty after the country and the engine waits for the new options", async () => {
    mount(`<label for="st">State</label><select id="st"><option value="">Select</option></select>`);
    const grow = setTimeout(() => {
      const opts = Array.from({ length: 50 }, (_, i) => `<option>State ${i}</option>`).join("");
      byId<HTMLSelectElement>("st").innerHTML = `<option value="">Select</option>${opts}`;
    }, 40);
    // The wait outlasts the page's 40 ms on a loaded machine; the timer is cleared so a late one cannot reach the next test's select.
    const r = await write("st", "State 7", {}, "replace", new Map(), false, { dependentWaitMs: 5000 });
    clearTimeout(grow);
    expect(r).toMatchObject({ outcome: "filled", value: "State 7" });
  });

  test("A dependent select whose options never grow fails after the dependent wait", async () => {
    mount(`<label for="st">State</label><select id="st"><option value="">Select</option></select>`);
    const r = await write("st", "State 7");
    expect(r).toMatchObject({ outcome: "failed", reason: "option-not-found" });
  });

  test("Write order: a country that sits after its state in the page is moved ahead of it, every other order is kept", () => {
    const items = ["Name", "State", "Zip code", "Country", "Phone"];
    expect(dependencyOrder(items, (x) => x)).toEqual(["Name", "Country", "State", "Zip code", "Phone"]);
    expect(dependencyOrder(["Country", "State"], (x) => x)).toEqual(["Country", "State"]);
    expect(dependencyOrder(["Name", "Phone"], (x) => x)).toEqual(["Name", "Phone"]);
  });

  test("Matcher lane: a country that sits after its state in the answers is written first", async () => {
    mount(`${PAD}<label for="state">Which state?</label><select id="state"><option value="">Select</option></select><label for="country">Which country?</label><select id="country"><option value="">Select</option><option>United States</option></select>`);
    byId("country").addEventListener("change", () => {
      setTimeout(() => (byId<HTMLSelectElement>("state").innerHTML = `<option value="">Select</option><option>North Carolina</option>`), 20);
    });
    const host = fakeHost({ "which state?": { outcome: "filled", value: "North Carolina" }, "which country?": { outcome: "filled", value: "United States" } });
    await runFill(makeEngine(host));
    expect(byId("country").value).toBe("United States");
    expect(byId("state").value).toBe("North Carolina");
  });
});

describe("Native country-to-state cascade", () => {
  test("Two State options that both contain the value: nothing is guessed and the state stays unset", async () => {
    cascadeForm("country-first");
    const applicant = { candidate: { ...ADA.candidate, address: { ...ADA.candidate.address, state: "Carolina" } } };
    const s = summaryOf(await runFill(makeEngine(fakeHost()), { applicant }));
    expect(byId("country").value).toBe("US");
    expect(byId("state").value).toBe("");
    expect(rowFor(s, "state").outcome).not.toBe("filled");
    expect(rowFor(s, "state").reason).toBe("option-not-found");
  });
});

describe("Cascade completion pass", () => {
  const KEYS: [string, string][] = [
    ["First name", "fn"],
    ["Last name", "ln"],
    ["Email", "em"],
    ["Phone", "ph"],
    ["LinkedIn profile", "li"],
    ["GitHub profile", "gh"],
    ["Portfolio website", "pf"],
    ["Address", "a1"],
    ["Address line 2", "a2"],
    ["City", "ct"],
    ["Zip code", "zp"],
    ["County", "cn"],
  ];

  test("Controls revealed by an earlier write are picked up by one extra pass that writes at most eight", async () => {
    mount(`<label for="reveal">Country</label><select id="reveal"><option value="">Select</option><option>United States</option></select><div id="block"></div>${PAD}`);
    byId("reveal").addEventListener("change", () => {
      byId<HTMLElement>("block").innerHTML = KEYS.map(([l, id]) => `<label for="${id}">${l}</label><input id="${id}">`).join("");
    });
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    const filled = KEYS.filter(([, id]) => byId(id).value !== "");
    expect(filled).toHaveLength(8);
    expect(s.counts.filled).toBeGreaterThanOrEqual(9);
  });

  test("A disabled state list that the country enables is filled by the cascade pass", async () => {
    mount(`<label for="country">Country</label><select id="country"><option value="">Select</option><option value="US">United States</option></select><label for="state">State</label><select id="state" disabled><option value="">Select</option><option value="NC">North Carolina</option></select>${PAD}`);
    byId("country").addEventListener("change", () => ((byId("state") as HTMLSelectElement).disabled = false));
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    expect(byId("state").value).toBe("NC");
    expect(rowFor(s, "state").outcome).toBe("filled");
  });

  test("An enabled combobox that opens with no options is an unsatisfied dependency, reported as such", async () => {
    mount("");
    const m = mountMenu({ id: "lvl", label: "Level", options: [] });
    m.list.hidden = false;
    const out = await writeListbox(m.trigger, { answers: ["Senior"], label: "Level" }, ctx());
    expect(out.ok).toBe(false);
    if (!out.ok) {
      expect(out.reason).toBe("listbox-not-opened");
      expect(out.detail).toContain("stage=no-options");
    }
  });

  test("An answer that found a dependent select empty is retried once by the cascade pass after the country is set", async () => {
    mount(`${PAD}<label for="state">Which state?</label><select id="state"><option value="">Select</option></select><label for="country">Country</label><select id="country"><option value="">Select</option><option>United States</option></select>`);
    byId("country").addEventListener("change", () => {
      setTimeout(() => (byId<HTMLSelectElement>("state").innerHTML = `<option value="">Select</option><option>North Carolina</option>`), 20);
    });
    const s = summaryOf(await runFill(makeEngine(fakeHost({ "which state?": { outcome: "filled", value: "North Carolina" } }))));
    expect(byId("state").value).toBe("North Carolina");
    expect(rowFor(s, "which state").outcome).toBe("filled");
  });
});
