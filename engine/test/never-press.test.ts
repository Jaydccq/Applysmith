// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Controls the engine never writes or presses, for every widget driver: a target whose label
// holds submit, withdraw, discard, delete or a terminal apply phrase is refused with unsafe-click and nothing
// is dispatched at it.
import { describe, expect, test } from "vitest";
import { safePress } from "../src/gestures";
import { writeItiCountry } from "../src/libraries";
import { PAD, byId, ctx, fakeHost, makeEngine, mount, runFill, summaryOf, write } from "./helpers";
import { mountDayPicker, mountReactSelect } from "./library-fixtures";
import { mountCombobox, mountMenu } from "./widgets-fixtures";

/** Counts every pointer, mouse, click and key event a node receives. */
function spy(el: Element): { events: string[] } {
  const events: string[] = [];
  for (const t of ["pointerdown", "mousedown", "pointerup", "mouseup", "click", "keydown", "keyup"]) el.addEventListener(t, () => events.push(t), true);
  return { events };
}

describe("Controls the engine never presses: every driver", () => {
  test("Listbox: an option named Delete application is refused, however it would be activated", async () => {
    mount("");
    const m = mountMenu({ id: "l", label: "Action", options: ["Delete application", "Keep"] });
    const s = spy(m.list.firstElementChild as Element);
    const r = await write("l", "Delete application");
    expect(r).toMatchObject({ outcome: "failed", reason: "unsafe-click" });
    expect(s.events).toEqual([]);
    expect(m.clicked).toEqual([]);
  });

  test("Listbox: the trigger itself is not pressed when its label names a destructive action", async () => {
    mount("");
    const m = mountMenu({ id: "l", label: "Account", options: ["Close"] });
    m.trigger.setAttribute("aria-label", "Delete account menu");
    const s = spy(m.trigger);
    const r = await write("l", "Close");
    expect(r).toMatchObject({ outcome: "failed", reason: "unsafe-click" });
    expect(s.events.filter((e) => e !== "keydown" && e !== "keyup")).toEqual([]);
  });

  test("Listbox: a text box that opens it is pressed whatever its question says (a click on a text box submits nothing)", async () => {
    // Eightfold: "Can you, upon employment, submit verification of your legal right to work ...?" over Yes / No.
    mount(`<label id="q-lbl">Can you, upon employment, submit verification of your legal right to work in the countries to which you have applied?</label>
<input id="q" type="text" role="combobox" aria-labelledby="q-lbl" aria-controls="q-list" aria-expanded="false" aria-required="true" placeholder="Select">
<ul id="q-list" role="listbox" hidden><li role="option" id="q-0">Yes</li><li role="option" id="q-1">No</li></ul>${PAD}`);
    const input = byId("q") as HTMLInputElement;
    const list = byId("q-list");
    input.addEventListener("click", () => {
      list.hidden = false;
      input.setAttribute("aria-expanded", "true");
    });
    for (const li of Array.from(list.children)) li.addEventListener("click", () => {
      input.value = li.textContent ?? "";
      list.hidden = true;
      input.setAttribute("aria-expanded", "false");
    });
    expect(await write("q", "Yes")).toMatchObject({ outcome: "filled" });
    expect(input.value).toBe("Yes");
  });

  test("Listbox: a trigger whose question uses a page-advancing word is opened (opening a list advances nothing)", async () => {
    // Visa (Workday): the button's aria-label is the whole question, "... candidates daily apply their knowledge ... Are you a veteran?".
    mount("");
    const m = mountMenu({ id: "l", label: "Veteran", options: ["I am not a veteran", "I am a veteran"] });
    m.trigger.setAttribute("aria-label", "Here at Example, military connected candidates daily apply their knowledge. We review progress. Are you a veteran? Select One Required");
    expect(await write("l", "I am not a veteran")).toMatchObject({ outcome: "filled" });
    expect(m.clicked).toEqual(["I am not a veteran"]);
  });

  test("Listbox: a trigger whose question says what a person submits is opened; one named for a submit is not", async () => {
    // Cadence (Workday): the VEVRAA notice in the button's aria-label says "The information you submit will be kept confidential".
    mount("");
    const m = mountMenu({ id: "l", label: "Veteran", options: ["I am not a veteran", "I am a veteran"] });
    m.trigger.setAttribute("aria-label", "This employer is a Government contractor. The information you submit will be kept confidential. Select One Required");
    expect(await write("l", "I am not a veteran")).toMatchObject({ outcome: "filled" });
    expect(m.clicked).toEqual(["I am not a veteran"]);
    mount("");
    const n = mountMenu({ id: "k", label: "Veteran", options: ["No"] });
    n.trigger.setAttribute("aria-label", "Click to submit");
    expect(await write("k", "No")).toMatchObject({ outcome: "failed", reason: "unsafe-click" });
  });

  test("Listbox: a trigger that submits its form is still read by the page-advancing words", async () => {
    mount("");
    const m = mountMenu({ id: "l", label: "Veteran", options: ["No"] });
    m.trigger.setAttribute("type", "submit");
    m.trigger.setAttribute("aria-label", "Apply now");
    const s = spy(m.trigger);
    expect(await write("l", "No")).toMatchObject({ outcome: "failed", reason: "unsafe-click" });
    expect(s.events.filter((e) => e !== "keydown" && e !== "keyup")).toEqual([]);
  });

  test("React-Select: an option named Withdraw application is not pressed", async () => {
    mount("");
    const rs = mountReactSelect({ inputId: "react-select-7-input", label: "Next step", options: ["Withdraw application", "Wait"] });
    const r = await write("react-select-7-input", "Withdraw application");
    expect(r).toMatchObject({ outcome: "failed", reason: "unsafe-click" });
    expect(rs.clicked).toEqual([]);
  });

  test("React-Select: an option is chosen though it says review; one naming Submit is not (Roblox)", async () => {
    mount("");
    const ack = "I acknowledge that I received and had the opportunity to review Roblox's Candidate Arbitration Agreement, and I agree to be bound by its terms.";
    const rs = mountReactSelect({ inputId: "question_68627570", label: "Please review and acknowledge Roblox's Candidate Arbitration Agreement*", options: [ack] });
    // The user's legal-declaration setting is on, as it was for the live run.
    expect(await write("question_68627570", ack, {}, "replace", new Map(), true)).toMatchObject({ outcome: "filled" });
    expect(rs.clicked).toEqual([ack]);
    mount("");
    const sub = mountReactSelect({ inputId: "react-select-8-input", label: "Next step", options: ["Submit application", "Wait"] });
    expect(await write("react-select-8-input", "Submit application")).toMatchObject({ outcome: "failed", reason: "unsafe-click" });
    expect(sub.clicked).toEqual([]);
  });

  test("Typeahead, geo and address suggestions: a suggestion that names Submit is not pressed", async () => {
    mount("");
    const c = mountCombobox({ id: "t", label: "School", suggest: () => ["Submit Example University"] });
    expect(await write("t", "Submit Example University")).toMatchObject({ outcome: "failed", reason: "unsafe-click" });
    expect(c.clicked).toEqual([]);
    mount("");
    const a = mountCombobox({ id: "a", label: "Street address", suggest: () => ["Submit 1 Example Street, Durham, NC 27701"] });
    expect(await write("a", "1 Example Street", { address: { city: "Durham", state: "NC" } })).toMatchObject({ outcome: "failed", reason: "unsafe-click" });
    expect(a.clicked).toEqual([]);
  });

  test("Button choices: a Yes/No pill named Discard draft is not pressed", async () => {
    mount(`<div role="group" aria-label="Keep your draft?"><button type="button" id="y" aria-pressed="false">Keep draft</button><button type="button" id="n" aria-pressed="false">Discard draft</button></div>${PAD}`);
    const s = spy(byId("n"));
    const r = await write("y", "Discard draft");
    expect(r).toMatchObject({ outcome: "failed", reason: "unsafe-click" });
    expect(s.events).toEqual([]);
  });

  test("Switch: a switch named Delete everything is not pressed", async () => {
    mount(`<button type="button" role="switch" id="sw" aria-checked="false" aria-label="Delete everything" aria-required="true"></button>${PAD}`);
    const s = spy(byId("sw"));
    const r = await write("sw", "Yes");
    expect(r).toMatchObject({ outcome: "failed", reason: "unsafe-click" });
    expect(s.events).toEqual([]);
  });

  test("Calendar: a trigger named Submit date is not pressed", async () => {
    mount("");
    const dp = mountDayPicker({ id: "d", label: "Start", start: { y: 2026, m: 10 } });
    dp.trigger.setAttribute("aria-label", "Submit date");
    const s = spy(dp.trigger);
    const r = await write("d", "2026-11-02");
    expect(r).toMatchObject({ outcome: "failed", reason: "unsafe-click" });
    expect(s.events).toEqual([]);
  });

  test("Dojo, Stencil and typeahead.js: options named Delete record, Discard and Submit application are not pressed", async () => {
    mount(`<label id="st-lbl">State</label><table id="st" role="listbox" widgetid="dijit_form_Select_0" aria-haspopup="true" aria-labelledby="st-lbl" class="dijit dijitSelect"><tbody><tr><td class="dijitButtonText">— Please Specify —</td></tr></tbody></table><input type="hidden" value=" ">`);
    const popup = document.createElement("div");
    popup.id = "dijit_form_Select_0_dropdown";
    popup.innerHTML = `<div class="dijitMenuItem" role="option">Delete record</div>`;
    document.body.appendChild(popup);
    const s = spy(popup.firstElementChild as Element);
    expect(await write("st", "Delete record")).toMatchObject({ outcome: "failed", reason: "unsafe-click" });
    expect(s.events).toEqual([]);

    mount(`<label for="sdf">Pronouns</label><sdf-select-simple id="sdf"></sdf-select-simple>`);
    const host = byId("sdf");
    const root = host.attachShadow({ mode: "open" });
    root.innerHTML = `<button class="trigger-button" type="button">Choose an option</button><sdf-select-item role="option">Discard</sdf-select-item><sdf-select-item role="option">Keep</sdf-select-item>`;
    const item = root.querySelector("sdf-select-item") as Element;
    const s2 = spy(item);
    expect(await write("sdf", "Discard")).toMatchObject({ outcome: "failed", reason: "unsafe-click" });
    expect(s2.events).toEqual([]);

    mount(`<label for="school">School</label><span class="twitter-typeahead"><input id="school" class="tt-input"><div class="tt-menu"></div></span>`);
    const input = byId("school");
    input.addEventListener("input", () => {
      (document.querySelector(".tt-menu") as HTMLElement).innerHTML = `<div class="tt-suggestion" role="option">Submit application</div>`;
    });
    const s3 = spy(document.body);
    expect(await write("school", "Submit application")).toMatchObject({ outcome: "failed", reason: "unsafe-click" });
    expect(s3.events.filter((e) => e === "click")).toEqual([]);
  });

  test("intl-tel-input: a country row named Withdrawland is not pressed", async () => {
    mount(`<div class="iti"><div class="iti__flag-container"><div class="iti__selected-flag" role="combobox"></div><ul class="iti__country-list iti__hide"><li class="iti__country" data-country-code="zz"><span class="iti__country-name">Withdrawland</span></li></ul></div><input type="hidden" id="country"></div>`);
    const root = document.querySelector(".iti") as HTMLElement;
    root.querySelector(".iti__selected-flag")?.addEventListener("click", () => root.querySelector(".iti__country-list")?.classList.toggle("iti__hide"));
    const s = spy(root.querySelector("li") as Element);
    const out = await writeItiCountry(root, "Withdrawland", ctx());
    expect(out).toMatchObject({ ok: false, reason: "unsafe-click" });
    expect(s.events).toEqual([]);
  });

  test("Consent sweep: a box whose label names Submit is not ticked", async () => {
    mount(`${PAD}<label><input type="checkbox" id="c" required> I agree to the terms and submit my application</label>`);
    const s = spy(byId("c"));
    const out = summaryOf(await runFill(makeEngine(fakeHost()), { acceptLegalDeclarations: true }));
    expect(byId("c").checked).toBe(false);
    expect(s.events.filter((e) => e === "click")).toEqual([]);
    expect(out.rows.some((r) => r.reason === "unsafe-click")).toBe(true);
  });

  test("Consent sweep: a required declaration that reads 'By submitting this application' is ticked, the optional opt-in beside it is not", async () => {
    mount(
      `${PAD}<div><input type="checkbox" id="c" name="consent"><label for="c"><span class="sr-only">Required.</span> By submitting this application, I agree that I have read the <a href="#privacy">Privacy Policy</a> and that Example Tickets stores my details to process my application.<sup aria-hidden="true">*</sup></label></div>` +
        `<div><input type="checkbox" id="f" name="future"><label for="f">Yes, Example Tickets can contact me about future job opportunities.</label></div>`,
    );
    const out = summaryOf(await runFill(makeEngine(fakeHost()), { acceptLegalDeclarations: true }));
    expect(byId("c").checked).toBe(true);
    expect(byId("f").checked).toBe(false);
    expect(out.rows.some((r) => r.reason === "unsafe-click")).toBe(false);
    expect(location.hash).toBe("");
  });

  test("Consent sweep: a required declaration that says what the person submits (HPE: 'the resume you submit to us') is ticked", async () => {
    mount(`${PAD}<label><input type="checkbox" id="c" required><span>I agree to the processing of my personal data. Please ensure the resume you submit to us does not include sensitive personal data; if it does, you consent to its processing.</span></label>`);
    const out = summaryOf(await runFill(makeEngine(fakeHost()), { acceptLegalDeclarations: true }));
    expect(byId("c").checked).toBe(true);
    expect(out.rows.some((r) => r.reason === "unsafe-click")).toBe(false);
  });

  test("Consent sweep: a role=checkbox declaration that reads 'By submitting' is ticked; the same widget as a form's submit button is not pressed", async () => {
    const box = (id: string, type: string) =>
      `<fieldset><legend>Declaration *</legend><button ${type} id="${id}" role="checkbox" aria-checked="false" aria-required="true">By submitting this application, I confirm that the information I have given Example Tickets is true.</button></fieldset>`;
    mount(`${box("c", 'type="button"')}<form id="f" action="#sent">${box("s", "")}</form><label for="fn">First name</label><input id="fn">${PAD}`);
    let sent = 0;
    byId("f").addEventListener("submit", (e) => (sent++, e.preventDefault()));
    for (const id of ["c", "s"]) byId(id).addEventListener("click", () => byId(id).setAttribute("aria-checked", "true"));
    const out = summaryOf(await runFill(makeEngine(fakeHost({})), { acceptLegalDeclarations: true }));
    expect(byId("c").getAttribute("aria-checked")).toBe("true");
    expect(byId("s").getAttribute("aria-checked")).toBe("false");
    expect(sent).toBe(0);
    expect(out.rows.filter((r) => r.reason === "unsafe-click")).toHaveLength(1);
  });

  test("Enter and Space follow the click rule: a places box named Submit address gets no Enter", async () => {
    mount(`<label for="addr">Street address</label><input id="addr" class="pac-target-input" aria-label="Submit address"><div class="pac-container"><div class="pac-item">1 Example Street, Durham, NC</div></div>`);
    const keys: string[] = [];
    byId("addr").addEventListener("keydown", (e) => keys.push((e as KeyboardEvent).key));
    const r = await write("addr", "1 Example Street");
    expect(r).toMatchObject({ outcome: "failed", reason: "unsafe-click" });
    expect(keys).toContain("ArrowDown");
    expect(keys).not.toContain("Enter");
  });

  test("safePress reads a text box by its names and a button input by its value", () => {
    mount(`<input id="t" value="Deleted Items Inc"><input id="b" type="submit" value="Submit"><button id="w" type="button">Withdraw application</button><button id="ok" type="button">Add another</button>`);
    const seen: string[] = [];
    for (const id of ["t", "b", "w", "ok"]) byId(id).addEventListener("keydown", () => seen.push(id));
    expect(["t", "b", "w", "ok"].map((id) => safePress(byId(id), "Enter"))).toEqual(["ok", "unsafe-click", "unsafe-click", "ok"]);
    expect(seen).toEqual(["t", "ok"]);
  });

  test("Prescan: a listbox whose trigger names a destructive action is not opened to read its options", async () => {
    mount(PAD);
    const m = mountMenu({ id: "x", label: "Choice", options: ["One"] });
    m.trigger.setAttribute("aria-label", "Delete choice menu");
    const s = spy(m.trigger);
    await runFill(makeEngine(fakeHost()));
    expect(s.events.filter((e) => e !== "keydown" && e !== "keyup")).toEqual([]);
  });
});
