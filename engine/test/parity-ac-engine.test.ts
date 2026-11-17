// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Profile-field pass; Page API (value reading); answer-bank prepass; Named applicant-tracking adapters.
import { describe, expect, test } from "vitest";
import { scanControls } from "../src/scan";
import { ADA, PAD, byId, fakeHost, makeEngine, mount, runFill, summaryOf } from "./helpers";

const fill = async () => summaryOf(await runFill(makeEngine(fakeHost()), { applicant: ADA }));

describe("Résumé-parse review screen", () => {
  test("Country carries no required attribute, only an asterisk class: it is written, and the State column it reveals is filled after it", async () => {
    mount(`<label for="fn">First name</label><input id="fn"><label for="ln">Last name</label><input id="ln">
      <label for="co">Country<span class="required-asterisk">*</span></label>
      <select id="co"><option value="">Select</option><option>Canada</option><option>United States</option></select>
      <div id="col"></div>${PAD}`);
    byId("co").addEventListener("change", () => {
      if ((byId("co") as HTMLSelectElement).value === "United States") {
        byId<HTMLElement>("col").innerHTML = `<label for="st">State</label><select id="st"><option value="">Select</option><option>GA</option><option>NC</option></select>`;
      }
    });
    const s = await fill();
    expect((byId("co") as HTMLSelectElement).value).toBe("United States");
    expect((byId("st") as HTMLSelectElement).value).toBe("NC");
    expect(s.counts.filled).toBeGreaterThanOrEqual(4);
  });
});

describe("Value reading", () => {
  test("A select left at a whitespace-valued prompt reads as empty, and a FormValidation not-empty message element makes the control required", async () => {
    mount(`<div class="form-group"><label for="src">How did you hear about us</label>
      <select id="src" name="source"><option value=" ">Please choose</option><option value="a">A friend</option></select>
      <small class="help-block" data-fv-validator="notEmpty" data-fv-for="source" style="display:none">Required</small></div>
      <div class="form-group"><label for="x">Nickname</label><input id="x" name="nick" data-fv-notempty="true"></div>
      <label for="o">Hobby</label><input id="o" name="hobby">${PAD}`);
    const infos = scanControls(document);
    const by = (id: string) => infos.find((i) => i.el.id === id)!;
    expect(by("src").answered).toBe(false);
    expect(by("src").required).toBe(true);
    expect(by("x").required).toBe(true);
  });
});

describe("Wire shape of questions", () => {
  test("A caption that is only a unit suffix is skipped so the real question is the label", async () => {
    mount(`<div><label for="rate">Desired hourly rate</label><span id="u">/hour</span><input id="rate"></div>
      <div><label for="notice">Notice period</label><span id="u2">days</span><input id="notice"></div>`);
    const host = fakeHost();
    await runFill(makeEngine(host), { applicant: ADA });
    const asked = host.matchRequests.flatMap((r) => r.questions);
    const texts = asked.map((q) => q.text);
    expect(texts).toEqual(expect.arrayContaining(["Desired hourly rate", "Notice period"]));
    expect(texts.some((t) => /^(\/hour|days)$/.test(t))).toBe(false);
  });
});

describe("Answer-bank prepass claims", () => {
  test("A text claim the bank lane did not honour goes back to the profile lane; a refusal and a non-text kind keep their claim", async () => {
    mount(`<label for="e">Best address to reach you</label><input id="e" autocomplete="email">
      <label for="r">Home ZIP code?</label><input id="r">
      <label for="s">Sponsorship needed?</label><select id="s"><option value="">Select</option><option>Yes</option><option>No</option></select>${PAD}`);
    const host = fakeHost();
    const real = host.match.bind(host);
    host.match = async (req) => {
      const res = await real(req);
      return {
        results: res.results
          .filter((r) => r.selector !== "#e")
          .map((r) => (r.selector === "#r" ? { ...r, outcome: "abstained", reason: "context-policy", terminal: true } : r)),
      };
    };
    const s = summaryOf(await runFill(makeEngine(host), { applicant: ADA }));
    // #e has no result row: the claim is released, and the profile lane writes the email.
    expect(byId("e").value).toBe("ada@example.com");
    // A terminal refusal keeps its claim: nothing writes it afterwards.
    expect(byId("r").value).toBe("");
    // A select with no usable answer stays with the bank lane.
    expect((byId("s") as HTMLSelectElement).value).toBe("");
    expect(s.rows.length).toBeGreaterThanOrEqual(3);
  });
});
