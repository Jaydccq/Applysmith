// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Uncertainty events and the user's own edits; Sensitive controls and redacted fragments.
import { describe, expect, test } from "vitest";
import { classifySensitivity } from "../src/sensitive";
import { scanControls } from "../src/scan";
import { ADA, byId, fakeHost, makeEngine, mount, PAD, runFill, setUrl, summaryOf, tick, trusted } from "./helpers";

/** A person types into a control: trusted input events, then the value. */
function type(el: HTMLInputElement | HTMLTextAreaElement, text: string): void {
  el.focus();
  el.value = text;
  el.dispatchEvent(trusted(new Event("input", { bubbles: true })));
}

function leave(el: HTMLElement): void {
  el.dispatchEvent(trusted(new Event("focusout", { bubbles: true })));
}

async function filled(html: string, table = {}) {
  mount(html);
  const engine = makeEngine(fakeHost(table));
  const s = summaryOf(await runFill(engine, { applicant: ADA }));
  return { engine, s };
}

describe("Uncertainty events: which controls raise one", () => {
  test("an unclaimed control pre-filled by the page emits no event while an empty required one does", async () => {
    const { s } = await filled(`<input id="pre" aria-label="" value="kept by the page"><input id="req" required>${PAD}`);
    const events = s.events ?? [];
    expect(events).toHaveLength(1);
    expect(events[0]).toMatchObject({ kind: "uncertainty", reason: "unrecognised", required: true, selector: '[id="req"]' });
    // The server refuses an empty question, so a control no label can name still carries one.
    expect(events[0]?.question).toBe("(unlabeled control)");
    expect(JSON.stringify(events)).not.toContain("kept by the page");
  });

});

describe("Sensitive controls and the URLs an event records", () => {
  test("a green card holder is an immigration status, not a payment card: the question is scanned; a card holder's name stays never", () => {
    const gc = "Are you either a U.S. Citizen, a lawful permanent resident (green card holder), or have you been granted asylum or refugee status in the United States?";
    mount(`<label for="gc">${gc}</label><input id="gc"><label for="ch">Card holder name</label><input id="ch"><label for="ch2">Cardholder</label><input id="ch2">`);
    expect(scanControls(document).map((i) => i.el.id)).toEqual(["gc"]);
    expect(classifySensitivity(byId("ch"), "Card holder name")).toBe("never");
    expect(classifySensitivity(byId("ch2"), "Cardholder")).toBe("never");
  });

  test("a citizenship question that names a passport only to say which countries count is scanned (HP); a passport's number, date or country stays never", () => {
    const hp = "we need you to copy and paste in the name of the country(ies) for which you are a citizen/permanent resident (detain a passport for).";
    mount(`<label for="hp">${hp}</label><textarea id="hp"></textarea><label for="pn">Passport number</label><input id="pn">
      <label for="pe">Passport expiry date</label><input id="pe"><label for="pc">Passport country</label><input id="pc">
      <label for="pi">Country of citizenship and passport no.</label><input id="pi">`);
    expect(scanControls(document).map((i) => i.el.id)).toEqual(["hp"]);
    for (const [id, label] of [["pn", "Passport number"], ["pe", "Passport expiry date"], ["pc", "Passport country"], ["pi", "Country of citizenship and passport no."]] as const) {
      expect(classifySensitivity(byId(id), label)).toBe("never");
    }
  });

  test("a driver's licence document is never read in any spelling; a question about holding one is scanned", () => {
    const held = "Do you have a valid driver's license?";
    const needed = "This role requires a valid driving licence and a clean record. Do you meet this requirement?";
    mount(`<fieldset><legend>${held}</legend><label><input type="radio" name="dl" id="dy">Yes</label><label><input type="radio" name="dl">No</label></fieldset>
      <label for="rq">${needed}</label><select id="rq"><option></option><option>Yes</option><option>No</option></select>
      <label for="n1">Driver License Number</label><input id="n1"><label for="n2">Driving licence number</label><input id="n2">
      <label for="n3">Driver's license #</label><input id="n3"><label for="n4">Drivers License Expiration</label><input id="n4">
      <label for="n5">Do you have a driver's license? If so, enter your license number</label><input id="n5">`);
    expect(scanControls(document).map((i) => i.el.id)).toEqual(["dy", "rq"]);
    expect(classifySensitivity(byId("dy"), held)).toBeNull();
    for (const id of ["n1", "n2", "n3", "n4", "n5"]) expect(classifySensitivity(byId(id), document.querySelector(`label[for=${id}]`)?.textContent ?? ""), id).toBe("never");
  });

  test("an event raised on a URL with query and fragment records only origin and path", async () => {
    setUrl("https://jobs.example.test/apply/42?token=abc123#step-2");
    try {
      const { engine, s } = await filled(`<label for="school">School</label><input id="school"><button id="go" type="button">Submit application</button>${PAD}`, { school: { outcome: "abstained", reason: "no-entry" } });
      const uncertainty = s.events?.find((e) => e.question === "School");
      type(byId<HTMLInputElement>("school"), "Example University");
      leave(byId("school"));
      byId("go").dispatchEvent(trusted(new MouseEvent("click", { bubbles: true })));
      const events = engine.drainEvents();
      expect(uncertainty?.url).toBe("https://jobs.example.test/apply/42");
      expect(events.map((e) => e.kind).sort()).toEqual(["manual-fill", "unsafe-control"]);
      for (const e of events) expect(e.url).toBe("https://jobs.example.test/apply/42");
      expect(JSON.stringify([s.events, events])).not.toMatch(/abc123|step-2|\?token/);
    } finally {
      setUrl("http://localhost:3000/");
    }
  });
});

describe("The user's own edits: what is kept", () => {
  test("a fill started with watchCorrections false records no later change", async () => {
    mount(`<label for="s">Team choice</label><select id="s"><option value="">Select</option><option value="b1">Beta option</option></select>${PAD}`);
    const engine = makeEngine(fakeHost({ "team choice": { outcome: "abstained", reason: "low-confidence", terminal: true } }));
    await runFill(engine, { applicant: ADA, watchCorrections: false });
    const sel = byId<HTMLSelectElement>("s");
    sel.value = "b1";
    sel.dispatchEvent(trusted(new Event("change", { bubbles: true })));
    await tick(90);
    expect(engine.drainEvents()).toEqual([]);
  });

  test("a portalled option click is attributed to its listbox when the widget removes aria-controls on commit", async () => {
    mount(`<label id="l1">Primary school</label><button id="a" type="button" role="combobox" aria-haspopup="listbox" aria-labelledby="l1" aria-controls="menu-a">Select one</button>
      <label id="l2">Secondary school</label><button id="b" type="button" role="combobox" aria-haspopup="listbox" aria-labelledby="l2" aria-controls="menu-b">Select one</button>${PAD}`);
    // The popups live at the end of the body, away from their triggers.
    const portal = document.createElement("div");
    portal.innerHTML = `<ul id="menu-a" role="listbox"><li role="option">Example Primary</li></ul><ul id="menu-b" role="listbox"><li role="option">Example Secondary</li></ul>`;
    document.body.appendChild(portal);
    const engine = makeEngine(fakeHost());
    await runFill(engine, { applicant: ADA });
    const trigger = byId("a");
    portal.querySelector("#menu-a li")!.addEventListener("click", () => {
      trigger.textContent = "Example Primary";
      trigger.removeAttribute("aria-controls");
      portal.querySelector("#menu-a")!.remove();
    });
    document.dispatchEvent(trusted(new Event("pointerdown", { bubbles: true })));
    (portal.querySelector("#menu-a li") as HTMLElement).click();
    await tick(90);
    const events = engine.drainEvents();
    expect(events.map((e) => [e.question, e.after])).toEqual([["Primary school", "Example Primary"]]);
  });

  test("a submit click delivers pending manual fills before the submission record is sent", async () => {
    const { engine } = await filled(`<label for="school">School</label><input id="school"><button id="go" type="button">Submit application</button>${PAD}`, { school: { outcome: "abstained", reason: "no-entry" } });
    // Stands in for whatever records the submission: it runs after the engine's own capture-phase listener.
    let seen: ReturnType<typeof engine.drainEvents> = [];
    const record = () => (seen = engine.drainEvents());
    document.addEventListener("click", record);
    try {
      type(byId<HTMLInputElement>("school"), "West");
      byId("go").dispatchEvent(trusted(new MouseEvent("click", { bubbles: true })));
    } finally {
      document.removeEventListener("click", record);
    }
    expect(seen.map((e) => [e.kind, e.after ?? e.question])).toEqual([
      ["manual-fill", "West"],
      ["unsafe-control", "submit"],
    ]);
    // The listeners stay: the person may still correct the field before the page leaves.
    type(byId<HTMLInputElement>("school"), "Westbridge");
    leave(byId("school"));
    expect(engine.drainEvents().map((e) => e.after)).toEqual(["Westbridge"]);
  });
});

