// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Question pass (what is sent to the matcher): hidden and decoy controls are filtered before any
// question is built, because the wire carries no hidden flag; a lone checkbox is sent as one option holding its label.
import { afterEach, describe, expect, test } from "vitest";
import { PAD, fakeHost, makeEngine, mount, runFill } from "./helpers";

const asked = (host: ReturnType<typeof fakeHost>) => host.matchRequests.flatMap((r) => r.questions);

describe("Hidden and decoy controls are never sent", () => {
  const realRects = Element.prototype.getClientRects;
  const realBox = document.documentElement.getBoundingClientRect;
  afterEach(() => {
    Element.prototype.getClientRects = realRects;
    document.documentElement.getBoundingClientRect = realBox;
  });

  test("Not visible, zero size and off-screen controls produce no question; the visible one does", async () => {
    mount(`${PAD}
      <label for="shown">Visible question</label><input id="shown">
      <label for="d1">Display none question</label><input id="d1" style="display:none">
      <label for="d2">Visibility hidden question</label><input id="d2" style="visibility:hidden">
      <label for="d3">Hidden attribute question</label><input id="d3" hidden>
      <div style="display:none"><label for="d4">Hidden ancestor question</label><input id="d4"></div>
      <label for="d6">Off screen question</label><input id="d6" style="position:absolute;left:-5000px">
      <label for="d7">Zero size question</label><input id="d7">`);
    // Give the page a layout so zero-size boxes can be told from boxes that simply have no layout engine.
    document.documentElement.getBoundingClientRect = () => ({ width: 800, height: 600, top: 0, left: 0, right: 800, bottom: 600, x: 0, y: 0, toJSON: () => ({}) });
    Element.prototype.getClientRects = function (this: Element) {
      return (this.id === "d7" ? [] : [{ width: 10, height: 10 }]) as unknown as DOMRectList;
    };
    const host = fakeHost();
    await runFill(makeEngine(host));
    const texts = asked(host).map((q) => q.text);
    expect(texts).toContain("Visible question");
    for (const hidden of ["Display none", "Visibility hidden", "Hidden attribute", "Hidden ancestor", "Off screen", "Zero size"]) {
      expect(texts.some((t) => t.startsWith(hidden)), hidden).toBe(false);
    }
  });

  test("A control scrolled out of an inner scroll pane is a field, a box far outside the page is still a decoy", () => {
    // Ashby (Zania): the form scrolls inside its own pane; once the start date is reached, the boxes at the top sit
    // hundreds of pixels above the viewport while the window itself is not scrolled.
    mount(`<div id="pane" style="overflow:auto;height:600px">
      <label for="li">LinkedIn</label><input id="li" type="url">
      <label for="far">Far question</label><input id="far">
    </div>`);
    document.documentElement.getBoundingClientRect = () => ({ width: 800, height: 600, top: 0, left: 0, right: 800, bottom: 600, x: 0, y: 0, toJSON: () => ({}) });
    Element.prototype.getClientRects = function () {
      return [{ width: 10, height: 10 }] as unknown as DOMRectList;
    };
    const box = (top: number) => () => ({ width: 600, height: 54, top, left: 400, right: 1000, bottom: top + 54, x: 400, y: top, toJSON: () => ({}) });
    const pane = document.getElementById("pane") as HTMLElement;
    Object.defineProperty(pane, "scrollTop", { value: 900, configurable: true });
    (document.getElementById("li") as HTMLElement).getBoundingClientRect = box(-723);
    (document.getElementById("far") as HTMLElement).getBoundingClientRect = box(-3000);
    const labels = makeEngine(fakeHost()).inspect().map((r) => r.label);
    expect(labels).toContain("LinkedIn");
    expect(labels).not.toContain("Far question");
  });

  test("A hidden custom listbox is not opened and not asked", async () => {
    mount(`${PAD}<label for="lb">Hidden listbox question</label><button id="lb" type="button" role="combobox" aria-haspopup="listbox" style="display:none">Select one</button>`);
    let pointer = 0;
    document.getElementById("lb")?.addEventListener("pointerdown", () => pointer++);
    const host = fakeHost();
    await runFill(makeEngine(host));
    expect(asked(host).some((q) => q.text.startsWith("Hidden listbox"))).toBe(false);
    expect(pointer).toBe(0);
  });
});

describe("A lone checkbox is sent as one option carrying its label", () => {
  test("Lone native checkbox: options[0] is the box's own label", async () => {
    mount(`${PAD}<fieldset><legend>Work authorization</legend><label><input type="checkbox" id="auth" required> I am authorized to work here</label></fieldset>`);
    const host = fakeHost();
    await runFill(makeEngine(host));
    const q = asked(host).find((x) => x.kind === "checkbox");
    expect(q).toBeDefined();
    expect(q?.options).toEqual(["I am authorized to work here"]);
  });

  test("Lone switch: options[0] is the switch's label", async () => {
    mount(`${PAD}<label for="n2">Nickname</label><input id="n2"><fieldset><legend>Relocation</legend><span id="sl">Willing to relocate</span><button type="button" role="switch" id="sw" aria-checked="false" aria-required="true" aria-labelledby="sl"></button></fieldset>`);
    const host = fakeHost();
    await runFill(makeEngine(host));
    const q = asked(host).find((x) => x.kind === "checkbox");
    expect(q?.options).toEqual(["Willing to relocate"]);
  });
});
