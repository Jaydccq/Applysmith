// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Resilience to re-renders and user edits (the user-edit tracker).
import { describe, expect, test } from "vitest";
import { UserEdits } from "../src/restore";
import { scanControls } from "../src/scan";
import { byId, mount, fireTrusted } from "./helpers";

describe("The user's own edits", () => {
  test("cleared field is not rewritten", () => {
    mount(`<div id="app"><label for="a">Alpha</label><input id="a"><label for="b">Beta</label><input id="b"><label for="c">Gamma</label><textarea id="c"></textarea>
      <label for="d">Delta</label><input id="d"><label for="e">Epsilon</label><input id="e"></div>`);
    const edits = new UserEdits(document);
    edits.start();
    const info = (id: string) => scanControls(document).find((i) => i.el.id === id) as ReturnType<typeof scanControls>[number];
    // The script's own events and the page's synthetic change never count.
    byId("a").dispatchEvent(new Event("input", { bubbles: true }));
    fireTrusted(byId("a"), "change");
    expect(edits.edited(info("a"))).toBe(false);
    // A real input, key, paste or cut does, even when the page stops the event on the way.
    byId("b").addEventListener("input", (e) => e.stopPropagation());
    fireTrusted(byId("b"), "input");
    fireTrusted(byId("c"), "keydown");
    fireTrusted(byId("d"), "paste");
    fireTrusted(byId("e"), "cut");
    expect(["b", "c", "d", "e"].map((id) => edits.edited(info(id)))).toEqual([true, true, true, true]);
    // The edit is kept by selector: a re-render that replaces the node does not hand the control back.
    const fresh = document.createElement("input");
    fresh.id = "b";
    byId("b").replaceWith(fresh);
    expect(edits.edited(info("b"))).toBe(true);
    expect(edits.editedWithin(byId("app"))).toBe(true);
    edits.stop();
    fireTrusted(byId("a"), "input");
    expect(edits.edited(info("a"))).toBe(false);
  });

  test("a box is the user's by its trusted click, not by the trusted input a scripted click() fires", () => {
    mount(`<label><input type="checkbox" id="x"> East Asian</label><label><input type="checkbox" id="y"> Does not apply</label><button id="go">Go</button>`);
    const edits = new UserEdits(document);
    edits.start();
    const info = (id: string) => scanControls(document).find((i) => i.members.some((m) => m.id === id)) as ReturnType<typeof scanControls>[number];
    // Chrome marks the input and change of a scripted click() trusted (the page answering another control, Phenom).
    fireTrusted(byId("x"), "input");
    fireTrusted(byId("x"), "change");
    expect(edits.edited(info("x"))).toBe(false);
    // A person's click, on the box or its label, or a Space key, reaches the box as a trusted click.
    fireTrusted(byId("y"), "click");
    expect(edits.edited(info("y"))).toBe(true);
    // A click on another control is no edit.
    fireTrusted(byId("go"), "click");
    expect(edits.editedWithin(byId("go"))).toBe(false);
    edits.stop();
  });

  test("an ARIA switch, checkbox or radio and a pressed-state button are the user's by a trusted click, as a native box is", () => {
    mount(`<div id="sw" role="switch" aria-checked="true" tabindex="0">Remote</div><div id="cb" role="checkbox" aria-checked="false" tabindex="0"><span id="in">Relocate</span></div>
      <div id="rd" role="radio" aria-checked="false" tabindex="0">Yes</div><button id="tg" type="button" aria-pressed="false">No</button><button id="go" type="button">Add another</button><div id="un" role="switch" aria-checked="false">Night shifts</div>`);
    const edits = new UserEdits(document);
    edits.start();
    fireTrusted(byId("sw"), "click");
    fireTrusted(byId("in"), "click");
    fireTrusted(byId("rd"), "click");
    fireTrusted(byId("tg"), "click");
    fireTrusted(byId("go"), "click");
    expect(["sw", "cb", "rd", "tg", "go", "un"].map((id) => edits.editedWithin(byId(id)))).toEqual([true, true, true, true, false, false]);
    edits.stop();
  });
});
