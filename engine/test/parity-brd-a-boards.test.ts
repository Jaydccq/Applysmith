// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Drivers for application boards; Profile-field pass; Question pass; Pipeline stages and their order;
// Control writers and commit verification; Widget drivers for custom controls.
import { describe, expect, test } from "vitest";
import { ADA, byId, fakeHost, makeEngine, mount, PAD, runFill } from "./helpers";
import { scanControls, toQuestion } from "../src/scan";


describe("Board confirm-email controls", () => {
  test("Every confirm-your-email phrasing is filled from the email value and never sent to the matcher, while a consent box that mentions the email is not diverted", async () => {
    mount(`<label for="e">Email</label><input id="e"><label for="a">Re-enter your email</label><input id="a"><label for="b">Retype email</label><input id="b">
      <label for="c">Verify email address</label><input id="c"><label><input type="checkbox" id="k"> I agree that you may contact me at my email</label>`);
    const host = fakeHost();
    await runFill(makeEngine(host), { applicant: ADA });
    expect(["e", "a", "b", "c"].map((id) => byId(id).value)).toEqual(Array(4).fill("ada@example.com"));
    const asked = host.matchRequests.flatMap((r) => r.questions.map((q) => q.text.toLowerCase()));
    expect(asked.some((t) => /re-enter|retype|verify/.test(t))).toBe(false);
    expect(byId<HTMLInputElement>("k").checked).toBe(false);
  });
});

describe("Board labels", () => {
  test("A Lever question reads its application label rather than the placeholder, and a long question is kept whole", () => {
    const long = `Please describe in detail ${["operated services", "measured their reliability", "handled incidents", "planned capacity", "reviewed designs", "mentored colleagues", "shipped migrations"].join(" and how you ")} over the last few years?`;
    mount(`<li class="application-question"><label><div class="application-label">${long}</div><div class="application-field"><input id="q1" type="text" placeholder="Type your response"></div></label></li>
      <li class="application-question"><div class="application-label">Pronouns</div><div class="application-field"><input id="q2" placeholder="Optional"></div></li>`);
    const [a, b] = scanControls(document).map((i) => toQuestion(i)?.text);
    expect(long.length).toBeGreaterThan(180);
    expect(a).toBe(long);
    expect(a).toContain("Please describe in detail");
    expect(a).not.toContain("Type your response");
    expect(b).toBe("Pronouns");
  });
});

describe("Board pipeline", () => {
  test("A control written by the question pass gets the same focus and blur gesture as one written by the profile pass", async () => {
    mount(`<label for="q">Years of experience</label><input id="q"><label for="fn">First name</label><input id="fn">${PAD}`);
    const seen: Record<string, string[]> = { q: [], fn: [] };
    for (const id of ["q", "fn"]) for (const t of ["focusin", "focusout"]) byId(id).addEventListener(t, () => seen[id]!.push(t));
    const engine = makeEngine(fakeHost({ "years of experience": { outcome: "filled", value: "6" } }));
    await runFill(engine, { applicant: ADA });
    expect(byId("q").value).toBe("6");
    expect(byId("fn").value).toBe("Ada");
    expect(seen.q).toContain("focusout");
    expect(seen.q).toEqual(seen.fn);
  });
});

describe("Board choices and groups", () => {
  test("A multi-option checkbox group under a consent-shaped question is not ticked", async () => {
    mount(`${PAD}<fieldset><legend>I consent to processing of my data. How may we contact you?</legend>
      <label><input type="checkbox" id="c1" required> Email</label><label><input type="checkbox" id="c2"> Phone</label><label><input type="checkbox" id="c3"> Post</label></fieldset>`);
    await runFill(makeEngine(fakeHost()), { acceptLegalDeclarations: true });
    expect(["c1", "c2", "c3"].map((id) => byId(id).checked)).toEqual([false, false, false]);
  });

  test("Radio options projected through a slot are collected with the group", () => {
    mount(`<div id="g"><span slot="q">Work authorization</span><label><input type="radio" name="wa" id="r1" value="Yes"> Yes</label><label><input type="radio" name="wa" id="r2" value="No"> No</label></div>`);
    const root = byId("g").attachShadow({ mode: "open" });
    root.innerHTML = `<fieldset role="radiogroup"><legend><slot name="q"></slot></legend><slot></slot></fieldset>`;
    const infos = scanControls(document);
    expect(infos).toHaveLength(1);
    const q = toQuestion(infos[0]!);
    expect(q?.text).toBe("Work authorization");
    expect(q?.options).toEqual(["Yes", "No"]);
  });

  test("A control answered by the question pass is excluded from the profile pass across the passes of one run", async () => {
    mount(`<label for="c">Do you live in the US?</label><select id="c"><option value="">Select</option><option>Yes</option><option>No</option></select><div id="slot"></div>
      <label for="pe">Preferred contact email</label><input id="pe">`);
    byId("c").addEventListener("change", () => {
      byId("slot").innerHTML = `<label for="st">Which US state?</label><input id="st">`;
    });
    let writes = 0;
    byId("pe").addEventListener("input", () => writes++);
    const host = fakeHost({
      "do you live in the us?": { outcome: "filled", value: "Yes" },
      "which us state?": { outcome: "filled", value: "North Carolina" },
      "preferred contact email": { outcome: "filled", value: "contact@example.com" },
    });
    await runFill(makeEngine(host), { applicant: ADA });
    expect(byId("st").value).toBe("North Carolina");
    expect(byId("pe").value).toBe("contact@example.com");
    expect(writes).toBe(1);
  });
});

describe("Ashby-shaped field entries", () => {
  const ENTRY = `<aside>Senior Engineer Example Corp Remote Full time Apply now</aside>
    <div class="_fieldEntry_ab12"><label class="_heading_ab" for="nm">Preferred name</label><input id="nm" placeholder="Type here"></div>
    <div class="_fieldEntry_ab12"><fieldset><legend>Are you authorized to work in the US?</legend><div class="_yesno_x"><button type="button" id="y" class="_opt_1 _active_9z8y7x">Yes</button><button type="button" id="n" class="_opt_1">No</button></div></fieldset></div>
    <div class="_fieldEntry_ab12"><fieldset><legend>Will you need sponsorship?</legend><div class="_yesno_x"><button type="button" id="y2" class="_opt_1">Yes</button><button type="button" id="n2" class="_opt_1">No</button></div></fieldset></div>
    <fieldset class="_fieldEntry_ab12"><legend>Location</legend><div class="_description_ab">Choose the country you live in</div><input id="loc" role="combobox" aria-autocomplete="list" placeholder="Start typing"></fieldset>`;

  test("A Yes/No group already answered through a class token is not asked again", async () => {
    mount(`${ENTRY}${PAD}`);
    const host = fakeHost();
    await runFill(makeEngine(host), { applicant: ADA });
    const asked = host.matchRequests.flatMap((r) => r.questions.map((q) => q.text));
    expect(asked).not.toContain("Are you authorized to work in the US?");
    expect(asked).toContain("Will you need sponsorship?");
  });

});

