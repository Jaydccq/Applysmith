// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Control writers and commit verification; Write mechanics for hostile frameworks; Placeholders and overwrite decisions.
import { describe, expect, test } from "vitest";
import { safeToggleClick } from "../src/click-guard";
import { scoreOption } from "../src/writers";
import { byId, fakeHost, infoOf, makeEngine, mount, noEdits, runFill, write } from "./helpers";

function controlledInput(id: string): { state: () => string } {
  // A framework that only believes values set through its own handler: the instance
  // `value` property records into a tracker, and the input handler ignores an event
  // whose value the tracker already holds, then re-renders from its own state.
  const el = byId(id);
  const proto = Object.getPrototypeOf(el) as object;
  const desc = Object.getOwnPropertyDescriptor(proto, "value") as PropertyDescriptor;
  let tracked = "";
  let state = "";
  Object.defineProperty(el, "value", {
    configurable: true,
    get: () => desc.get?.call(el) as string,
    set: (v: string) => {
      tracked = v;
      desc.set?.call(el, v);
    },
  });
  el.addEventListener("input", () => {
    const now = desc.get?.call(el) as string;
    if (now !== tracked) {
      state = now;
      tracked = now;
    }
    desc.set?.call(el, state);
  });
  return { state: () => state };
}

describe("Control writers and commit verification", () => {
  test("Framework-controlled input: the native setter plus events is accepted and the read-back equals the value", async () => {
    mount(`<label for="e">Nickname</label><input id="e">`);
    const fw = controlledInput("e");
    const r = await write("e", "Ada");
    expect(r.outcome).toBe("filled");
    expect(byId("e").value).toBe("Ada");
    expect(fw.state()).toBe("Ada");
  });

  test("An answer of several lines in a one-line box is written on one line (PostHog's Ashby text input)", async () => {
    // A one-line box drops a line break instead of turning it into a space, so the
    // paragraphs of a composed answer would run together and never read back as written.
    mount(`<label for="e">Have you built anything agents use?</label><input id="e" type="text"><label for="t">Tell us more</label><textarea id="t"></textarea>`);
    const r = await write("e", "Yes. I built an MCP server.\n\nIt serves evals.");
    expect(r.outcome).toBe("filled");
    expect(byId("e").value).toBe("Yes. I built an MCP server. It serves evals.");
    await write("t", "First.\n\nSecond.");
    expect((byId("t") as unknown as HTMLTextAreaElement).value).toBe("First.\n\nSecond.");
  });

  test("Value not kept: a control that accepts events but whose page script resets the value is failed write-not-committed", async () => {
    mount(`<label for="e">Nickname</label><input id="e">`);
    byId("e").addEventListener("input", () => {
      (Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set as (v: string) => void).call(byId("e"), "");
    });
    const r = await write("e", "Ada");
    expect(r).toMatchObject({ outcome: "failed", reason: "write-not-committed" });
  });

  test("Reverted write falls back to insertText when the control reads empty and is not a places input", async () => {
    mount(`<label for="e">Nickname</label><input id="e">`);
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set as (v: string) => void;
    byId("e").addEventListener("input", (ev) => {
      if ((ev as InputEvent).inputType === "insertReplacementText") setter.call(byId("e"), "");
    });
    const types: string[] = [];
    byId("e").addEventListener("input", (ev) => types.push((ev as InputEvent).inputType));
    const r = await write("e", "Ada");
    expect(r.outcome).toBe("filled");
    expect(byId("e").value).toBe("Ada");
    expect(types).toContain("insertText");
  });

  test("A places autocomplete input is not retried through insertText", async () => {
    mount(`<label for="e">Address</label><input id="e" placeholder="Enter a location">`);
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set as (v: string) => void;
    byId("e").addEventListener("input", () => setter.call(byId("e"), ""));
    const r = await write("e", "1 Example Street");
    expect(r).toMatchObject({ outcome: "failed", reason: "write-not-committed" });
  });

  test("Select by label: two containment matches are never guessed between", async () => {
    mount(`<label for="c">Team</label><select id="c"><option value="">Select</option><option>Data Engineering</option><option>Data Engineering Platform</option></select>`);
    const r = await write("c", "Engineering");
    expect(r).toMatchObject({ outcome: "failed", reason: "option-not-found" });
    expect(byId("c").value).toBe("");
  });

  test("Select: a country answered by its code is its name in a list of every country (Avature)", async () => {
    const filler = Array.from({ length: 120 }, (_, i) => `<option value="${i + 1}">Country ${i + 1}</option>`).join("");
    mount(`<label for="c">Country</label><select id="c"><option value="">Select a country</option>${filler}<option value="233">United States</option><option value="234">United States Minor Outlying Islands</option></select>`);
    const r = await write("c", "US");
    expect(r.outcome).toBe("filled");
    expect(byId("c").value).toBe("233");
  });

  test("Select by label: an exact label, an alias and a single containment match are all accepted", async () => {
    mount(`<label for="a">Authorized?</label><select id="a"><option value="">Select</option><option value="1">Yes</option><option value="0">No</option></select>
      <label for="b">Level</label><select id="b"><option value="">Select</option><option>Bachelor's Degree</option><option>Master's Degree</option></select>
      <label for="c">Choice</label><select id="c"><option value="">Select</option><option>Prefer not to say</option><option>Male</option></select>`);
    expect((await write("a", "yes")).outcome).toBe("filled");
    expect(byId("a").value).toBe("1");
    expect((await write("b", "Master's")).outcome).toBe("filled");
    expect(byId("b").value).toBe("Master's Degree");
    expect((await write("c", "Decline to answer")).outcome).toBe("filled");
    expect(byId("c").value).toBe("Prefer not to say");
  });

  test("Select: a degree spelled out takes the native list's one option of its level (Citadel's Bachelors and Masters), and none on a tie", async () => {
    const citadel = ["Associate", "Bachelors", "Certificate", "Diploma", "Doctorate", "Post-Doctorate", "Masters", "J.D.", "M.D.", "LL.M.", "Other"].map((o) => `<option>${o}</option>`).join("");
    mount(`<label for="d0">Degree</label><select id="d0"><option value="">- Select-</option>${citadel}</select>
      <label for="d1">Degree</label><select id="d1"><option value="">- Select-</option>${citadel}</select>
      <label for="d2">Degree</label><select id="d2"><option value="">Select</option><option>Master of Arts</option><option>Master of Engineering</option></select>`);
    expect(await write("d0", "Bachelor of Science (B.S)")).toMatchObject({ outcome: "filled" });
    expect(byId("d0").value).toBe("Bachelors");
    expect(await write("d1", "Master of Science (M.S)")).toMatchObject({ outcome: "filled" });
    expect(byId("d1").value).toBe("Masters");
    expect(await write("d2", "Master of Science (M.S)")).toMatchObject({ outcome: "failed", reason: "option-not-found" });
    expect(byId("d2").value).toBe("");
  });

  test("Select: a re-commit happens once when a controlled select reverts within the window", async () => {
    mount(`<label for="c">Level</label><select id="c"><option value="">Select</option><option>Senior</option></select>`);
    let reverted = 0;
    byId("c").addEventListener("change", () => {
      if (reverted++ === 0) setTimeout(() => ((byId("c") as unknown as HTMLSelectElement).selectedIndex = 0), 0);
    });
    const r = await write("c", "Senior");
    expect(r.outcome).toBe("filled");
    expect(byId("c").value).toBe("Senior");
  });

  test("Detached control: the page replaces an input node between selection and write, the engine re-resolves the selector and writes once", async () => {
    mount(`<div id="w"><label for="e">Nickname</label><input id="e"></div>`);
    const info = infoOf("e");
    byId("w").innerHTML = `<label for="e">Nickname</label><input id="e">`;
    const { applyValue } = await import("../src/apply");
    const { ctx } = await import("./helpers");
    const r = await applyValue(info, { value: "Ada", replaceDefault: false }, { ctx: ctx(), written: new Map(), mode: "replace", acceptLegalDeclarations: false, edits: noEdits() });
    expect(r.outcome).toBe("filled");
    expect(byId("e").value).toBe("Ada");
  });

  test("A control that vanished and does not come back fails with control-detached", async () => {
    mount(`<div id="w"><label for="e">Nickname</label><input id="e"></div>`);
    const info = infoOf("e");
    byId("w").innerHTML = "";
    const { applyValue } = await import("../src/apply");
    const { ctx } = await import("./helpers");
    const r = await applyValue(info, { value: "Ada", replaceDefault: false }, { ctx: ctx(), written: new Map(), mode: "replace", acceptLegalDeclarations: false, edits: noEdits() });
    expect(r).toMatchObject({ outcome: "failed", reason: "control-detached" });
  });

  test("Stuck field: a write that never settles fails alone with timeout and the next field still runs", async () => {
    mount(`<label for="a">First</label><input id="a"><label for="b">Second</label><input id="b">`);
    const { applyValue } = await import("../src/apply");
    const { ctx } = await import("./helpers");
    const stuck = ctx({ fieldBudgetMs: 30 });
    const sleep = stuck.sleep;
    let first = true;
    stuck.sleep = (ms) => {
      if (first) {
        first = false;
        return new Promise(() => {});
      }
      return sleep(ms);
    };
    const env = { ctx: stuck, written: new Map<string, string>(), mode: "replace" as const, acceptLegalDeclarations: false, edits: noEdits() };
    expect(await applyValue(infoOf("a"), { value: "x", replaceDefault: false }, env)).toMatchObject({ outcome: "failed", reason: "timeout" });
    expect((await applyValue(infoOf("b"), { value: "y", replaceDefault: false }, env)).outcome).toBe("filled");
  });

  test("Hidden, disabled and read-only controls are not written", async () => {
    mount(`<label for="a">Hidden</label><input id="a"><label for="b">Locked</label><input id="b">`);
    const a = infoOf("a");
    const b = infoOf("b");
    byId("a").style.display = "none";
    byId("b").setAttribute("readonly", "");
    const { applyValue } = await import("../src/apply");
    const { ctx } = await import("./helpers");
    const env = { ctx: ctx(), written: new Map<string, string>(), mode: "replace" as const, acceptLegalDeclarations: false, edits: noEdits() };
    expect(await applyValue(a, { value: "x", replaceDefault: false }, env)).toMatchObject({ outcome: "skipped", reason: "hidden" });
    expect(await applyValue(b, { value: "x", replaceDefault: false }, env)).toMatchObject({ outcome: "skipped", reason: "readonly" });
    expect(byId("a").value + byId("b").value).toBe("");
  });

  test("Textarea and content-editable regions are written by insertion and read back", async () => {
    mount(`<label for="t">About you</label><textarea id="t"></textarea><div id="ce" contenteditable="true" aria-label="Notes"></div>`);
    expect((await write("t", "Hello there")).outcome).toBe("filled");
    expect(byId("t").value).toBe("Hello there");
    // The browser's own insertion, which happy-dom lacks.
    const doc = document;
    const own = Object.getOwnPropertyDescriptor(doc, "execCommand");
    const inserted: string[] = [];
    doc.execCommand = (command, _ui, value = "") => {
      if (command !== "insertText") return false;
      inserted.push(value);
      byId("ce").textContent = value;
      return true;
    };
    try {
      expect((await write("ce", "Some notes")).outcome).toBe("filled");
    } finally {
      if (own) Object.defineProperty(doc, "execCommand", own);
      else Reflect.deleteProperty(doc, "execCommand");
    }
    expect(inserted).toEqual(["Some notes"]);
    expect(byId("ce").textContent).toBe("Some notes");
  });

  test("An answer of several lines the browser's own editing splits into one block a line is read back as held", async () => {
    mount(`<div id="ce" contenteditable="true" aria-label="Notes"></div>`);
    const doc = document;
    const own = Object.getOwnPropertyDescriptor(doc, "execCommand");
    // The browser's insertText: the selected content is replaced, each line in a block of its own.
    doc.execCommand = (command, _ui, value = "") => {
      if (command !== "insertText") return false;
      byId("ce").innerHTML = value.split("\n").map((line) => `<div>${line === "" ? "<br>" : line}</div>`).join("");
      return true;
    };
    try {
      expect((await write("ce", "First line\nSecond line\n\nLast line")).outcome).toBe("filled");
      expect(byId("ce").textContent).toBe("First lineSecond lineLast line");
      // A region that took other text is still not held.
      doc.execCommand = () => ((byId("ce").innerHTML = "<div>First line</div>"), true);
      expect(await write("ce", "First line\nSecond line")).toMatchObject({ outcome: "failed", reason: "write-not-committed" });
    } finally {
      if (own) Object.defineProperty(doc, "execCommand", own);
      else Reflect.deleteProperty(doc, "execCommand");
    }
  });

  test("A control that already holds the desired value is prefilled and left alone", async () => {
    mount(`<label for="e">Nickname</label><input id="e" value="Ada">`);
    const events: string[] = [];
    byId("e").addEventListener("input", () => events.push("input"));
    const r = await write("e", "Ada");
    expect(r.outcome).toBe("prefilled");
    expect(events).toEqual([]);
  });

  test("Password, hidden-type and file inputs are never written by a value write", async () => {
    mount(`<label for="e">Nickname</label><input id="e"><input id="pw" type="password" aria-label="Password"><input id="h" type="hidden" aria-label="Access token" value="">
      <label for="f">Portfolio file</label><input id="f" type="file">`);
    const { scanControls } = await import("../src/scan");
    // A password and a hidden input are no controls; a file input is one, of its own kind, which no value write reaches.
    expect(scanControls(document).map((i) => [i.el.id, i.kind])).toEqual([["e", "text"], ["f", "file"]]);
    const answer = { outcome: "filled" as const, value: "x-value" };
    await runFill(makeEngine(fakeHost({ password: answer, "access token": answer, "portfolio file": answer })));
    expect([byId("pw").value, byId("h").value, byId("f").value]).toEqual(["", "", ""]);
  });
});

describe("Choice controls", () => {
  const RADIO = `<fieldset><legend>Do you need sponsorship?</legend>
    <label><input type="radio" name="sp" value="on" id="r1"> Yes</label><label><input type="radio" name="sp" value="on" id="r2"> No</label></fieldset>`;

  test("Hidden radio toggled by label: an invisible radio whose label click is the framework's only handler", async () => {
    mount(`<fieldset><legend>Are you 18 or older?</legend>
      <label for="r1" id="l1">Yes</label><input type="radio" name="age" id="r1" style="display:none" value="1">
      <label for="r2" id="l2">No</label><input type="radio" name="age" id="r2" style="display:none" value="0"></fieldset>`);
    let labelClicks = 0;
    byId("l1").addEventListener("click", () => labelClicks++);
    const r = await write("r1", "Yes");
    expect(r.outcome).toBe("filled");
    expect(labelClicks).toBe(1);
    expect(byId("r1").checked).toBe(true);
  });

  test("Radio group: identical-value radios are bound by label, and a different checked sibling is not fought", async () => {
    mount(RADIO);
    expect((await write("r1", "No")).outcome).toBe("filled");
    expect(byId("r2").checked).toBe(true);
    expect(byId("r1").checked).toBe(false);
    const again = await write("r1", "Yes");
    expect(again).toMatchObject({ outcome: "prefilled", reason: "existing-value" });
    expect(byId("r2").checked).toBe(true);
    // A page default the matcher flagged for review is replaced.
    expect((await write("r1", "Yes", { replaceDefault: true })).outcome).toBe("filled");
    expect(byId("r1").checked).toBe(true);
  });

  test("Sponsorship pair: two nameless checkboxes share one question wrapper and only the answer is ticked, on every run", async () => {
    mount(`<div class="q"><p>Will you require sponsorship?</p>
      <label><input type="checkbox" id="c1"> Yes</label><label><input type="checkbox" id="c2"> No</label></div>`);
    const info = infoOf("c1");
    expect(info.members).toHaveLength(2);
    expect((await write("c1", "Yes")).outcome).toBe("filled");
    expect(byId("c1").checked).toBe(true);
    expect(byId("c2").checked).toBe(false);
    const again = await write("c1", "Yes");
    expect(again.outcome).toBe("prefilled");
    expect(byId("c1").checked).toBe(true);
    expect(byId("c2").checked).toBe(false);
  });

  test("Ladder tie: when options tie for the top score no box is ticked", async () => {
    mount(`<fieldset><legend>Rate your skill</legend>
      <label><input type="checkbox" id="c1"> Advanced beginner level</label><label><input type="checkbox" id="c2"> Advanced expert level</label></fieldset>`);
    const r = await write("c1", "Advanced");
    expect(r).toMatchObject({ outcome: "failed", reason: "option-not-found" });
    expect(byId("c1").checked || byId("c2").checked).toBe(false);
  });

  test("A multi-select checkbox group ticks each bound label and skips unknown ones", async () => {
    mount(`<fieldset><legend>Which languages do you use? Select all that apply</legend>
      <label><input type="checkbox" id="g" name="lang[]" value="go"> Go</label><label><input type="checkbox" id="t" name="lang[]" value="ts"> TypeScript</label>
      <label><input type="checkbox" id="r" name="lang[]" value="rs"> Rust</label></fieldset>`);
    const r = await write("g", "Go", { optionLabels: ["Go", "Rust", "Cobol"] });
    expect(r.outcome).toBe("filled");
    expect([byId("g").checked, byId("t").checked, byId("r").checked]).toEqual([true, false, true]);
  });

  test("A lone required acknowledgement box is ticked by Yes only when legal declarations are accepted", async () => {
    mount(`<label><input type="checkbox" id="a" required> I certify that the information is true</label>`);
    expect((await write("a", "Yes", {}, "replace", new Map(), true)).outcome).toBe("filled");
    expect(byId("a").checked).toBe(true);
    expect((await write("a", "maybe", {}, "replace", new Map(), true)).outcome).not.toBe("filled");
  });

  test("A radio or checkbox click passes the click guard", async () => {
    mount(`<fieldset><legend>Pick one</legend><label><input type="radio" name="p" id="p1"> Delete my data after 30 days</label><label><input type="radio" name="p" id="p2"> Keep it</label></fieldset>`);
    expect(await write("p1", "Delete my data after 30 days")).toMatchObject({ outcome: "failed", reason: "unsafe-click" });
    expect(byId("p1").checked).toBe(false);
  });

  test("A radio whose sentence says apply is a choice (RWE's profile visibility); a box that opens with Apply is still refused", async () => {
    mount(`<fieldset><legend>Allow My Profile and Resume to be visible or shared with:</legend>
      <input type="radio" name="v" id="v1" value="1" aria-label="Any RWE company recruiter worldwide"><label for="v1">Any RWE company recruiter worldwide</label>
      <input type="radio" name="v" id="v2" value="2" aria-label="Only RWE recruiters managing jobs I apply to"><label for="v2">Only RWE recruiters managing jobs I apply to</label></fieldset>
      <label><input type="checkbox" id="a1"> Apply with my LinkedIn profile</label>`);
    expect(await write("v1", "Only RWE recruiters managing jobs I apply to")).toMatchObject({ outcome: "filled" });
    expect(byId("v2").checked).toBe(true);
    expect(safeToggleClick(byId("a1"), byId("a1"))).toBe("unsafe-click");
    expect(byId("a1").checked).toBe(false);
  });

  test("A radio named only Continue is a choice (GC AI's one-option notice); a longer Continue name or a box is still refused", async () => {
    mount(`<fieldset><label for="g">By continuing, you acknowledge the following: GC AI will collect and use the personal data you provide.</label>
      <span><input type="radio" id="g-0" name="g"></span><label for="g-0">Continue</label></fieldset>
      <fieldset><legend>How do you want to apply?</legend><label><input type="radio" name="h" id="h1"> Continue as guest</label><label><input type="radio" name="h" id="h2"> Sign in</label></fieldset>
      <label><input type="checkbox" id="c1"> Continue</label>`);
    expect(await write("g-0", "Continue")).toMatchObject({ outcome: "filled" });
    expect(byId("g-0").checked).toBe(true);
    expect(await write("h1", "Continue as guest")).toMatchObject({ outcome: "failed", reason: "unsafe-click" });
    expect(safeToggleClick(byId("c1"), byId("c1"))).toBe("unsafe-click");
    expect(byId("c1").checked).toBe(false);
  });

  test("A box named only by the button its statement quotes is an agreement (Aledade, Lever); without that statement, or beside another control, it is refused", async () => {
    const card = (id: string, statement: string, extra = "") => `<li class="application-question"><div><div class="application-label"><div class="text">${statement}</div></div>
      <div class="application-field"><ul data-qa="checkboxes"><li><label><input type="checkbox" id="${id}" name="${id}" value="Submit Application"><span>Submit Application</span></label></li></ul>${extra}</div></div></li>`;
    mount(`<form><ul>${card("q1", 'By clicking "Submit Application" I agree to the Aledade Applicant Privacy Policy &amp; Terms of Service')}
      ${card("q2", "Please confirm")}
      ${card("q3", 'By clicking "Submit Application" I certify that all statements made in this application are true', '<button type="submit" id="b">Submit Application</button>')}</ul></form>`);
    expect(safeToggleClick(byId("q1"), byId("q1"))).toBe("ok");
    expect(byId("q1").checked).toBe(true);
    expect(safeToggleClick(byId("q2"), byId("q2"))).toBe("unsafe-click");
    expect(byId("q2").checked).toBe(false);
    expect(safeToggleClick(byId("q3"), byId("q3"))).toBe("unsafe-click");
    expect(byId("q3").checked).toBe(false);
  });
});

describe("Option scoring and polarity", () => {
  test("A bare No picks the option that opens with No and rejects the opposite-polarity option", async () => {
    mount(`<label for="s">Will you require sponsorship?</label><select id="s"><option value="">Select</option>
      <option>Yes, I will require sponsorship</option><option>No, I will not require sponsorship</option></select>
      <fieldset><legend>Authorized to work?</legend><label><input type="radio" name="w" id="w1"> Yes, I am authorized</label><label><input type="radio" name="w" id="w2"> No, I am not authorized</label></fieldset>`);
    expect(await write("s", "No")).toMatchObject({ outcome: "filled", optionLabel: "No, I will not require sponsorship" });
    expect(await write("w1", "No")).toMatchObject({ outcome: "filled", optionLabel: "No, I am not authorized" });
    expect(byId("w2").checked).toBe(true);
    expect(scoreOption("Yes, I will require sponsorship", "No")).toBe(0);
    expect(scoreOption("No, I will not require sponsorship", "No, I will require sponsorship")).toBe(0);
  });

  test("Two options opening with the same polarity are not guessed between", async () => {
    mount(`<label for="s">Visa?</label><select id="s"><option value="">Select</option><option>No, citizen</option><option>No, permanent resident</option><option>Yes, on a visa</option></select>`);
    expect(await write("s", "No")).toMatchObject({ outcome: "failed", reason: "option-not-found" });
    expect(await write("s", "Yes")).toMatchObject({ outcome: "filled", optionLabel: "Yes, on a visa" });
  });

  test("A country select takes UAE as the United Arab Emirates, however long the list", async () => {
    const options = Array.from({ length: 148 }, (_, i) => `<option value="C${i}">Country ${i}</option>`).join("");
    mount(`<label for="c">Country</label><select id="c"><option value="">Select</option>${options}<option value="784">United Arab Emirates</option><option value="826">United Kingdom</option></select>`);
    expect(await write("c", "UAE")).toMatchObject({ outcome: "filled", optionLabel: "United Arab Emirates" });
    expect(byId<HTMLSelectElement>("c").value).toBe("784");
  });

  test("A 150-option country select still matches by exact value", async () => {
    const options = Array.from({ length: 148 }, (_, i) => `<option value="C${i}">Country ${i}</option>`).join("");
    mount(`<label for="c">Country</label><select id="c"><option value="">Select</option>${options}<option value="US">United States of America</option><option value="UM">US Minor Outlying Islands</option></select>`);
    expect(await write("c", "US")).toMatchObject({ outcome: "filled", optionLabel: "United States of America" });
    expect(byId<HTMLSelectElement>("c").value).toBe("US");
    expect(await write("c", "America", {}, "replace", new Map())).toMatchObject({ outcome: "prefilled", reason: "existing-value" });
  });

  test("A long list is not matched by containment", async () => {
    const options = Array.from({ length: 150 }, (_, i) => `<option value="C${i}">Country ${i}</option>`).join("");
    mount(`<label for="c">Country</label><select id="c"><option value="">Select</option>${options}<option value="X">Wonderland Kingdom</option></select>`);
    expect(await write("c", "Wonderland")).toMatchObject({ outcome: "failed", reason: "option-not-found" });
  });
});

describe("Placeholders and overwrite decisions", () => {
  test("A select whose selected option is a placeholder counts as unanswered; a genuine choice is kept", async () => {
    mount(`<label for="c">Level</label><select id="c"><option value="">Please select...</option><option>Junior</option><option>Senior</option></select>
      <label for="d">Team</label><select id="d"><option value="">Select one</option><option selected>Platform</option><option>Data</option></select>`);
    expect(infoOf("c").answered).toBe(false);
    expect(infoOf("d").answered).toBe(true);
    expect((await write("c", "Senior")).outcome).toBe("filled");
    expect(await write("d", "Data")).toMatchObject({ outcome: "prefilled", reason: "existing-value" });
    expect(byId("d").value).toBe("Platform");
    expect((await write("d", "Data", { replaceDefault: true })).outcome).toBe("filled");
    expect(byId("d").value).toBe("Data");
  });

  test("A value another lane wrote in this run is not fought", async () => {
    mount(`<label for="e">Nickname</label><input id="e">`);
    const written = new Map<string, string>();
    expect((await write("e", "Ada", {}, "replace", written)).outcome).toBe("filled");
    expect(await write("e", "Grace", {}, "replace", written)).toMatchObject({ outcome: "prefilled", reason: "existing-value" });
    expect(byId("e").value).toBe("Ada");
  });

  test("A value that no lane wrote is corrected", async () => {
    mount(`<label for="e">Nickname</label><input id="e" value="stale">`);
    expect((await write("e", "Ada")).outcome).toBe("filled");
    expect(byId("e").value).toBe("Ada");
  });
});

describe("Live markup: Jobvite State select and Rippling phone", () => {
  /** An AngularJS ng-options select: values carry a type prefix and the text may sit only in the label attribute. */
  const angularState = (text: boolean) => `<label for="jv-field-yK5jZfwR">State<span>*</span></label>
    <select id="jv-field-yK5jZfwR" name="input-yK5jZfwR" autocomplete="address-level1" ng-model="m" required="required"><option value="" selected="selected">Select an option...</option>
    ${[["Not Applicable", "Not Applicable"], ["Alaska", "AK"], ["North Carolina", "NC"], ["South Carolina", "SC"]]
      .map(([n, v]) => `<option label="${n}" value="string:${v}">${text ? n : ""}</option>`)
      .join("")}</select>`;

  test("A state given as a code picks the option whose label names it, through a type-prefixed value", async () => {
    mount(angularState(true));
    expect(await write("jv-field-yK5jZfwR", "NC")).toMatchObject({ outcome: "filled", optionLabel: "North Carolina" });
    expect(byId<HTMLSelectElement>("jv-field-yK5jZfwR").value).toBe("string:NC");
  });

  test("A state given as a name picks the option that carries only the code in its value, and one with no text reads from its label attribute", async () => {
    mount(`<label for="s">State</label><select id="s"><option value="">Select</option><option value="NC">N. Carolina</option><option value="SC">S. Carolina</option></select>`);
    expect(await write("s", "North Carolina")).toMatchObject({ outcome: "filled" });
    expect(byId<HTMLSelectElement>("s").value).toBe("NC");
    mount(angularState(false));
    expect(await write("jv-field-yK5jZfwR", "North Carolina")).toMatchObject({ outcome: "filled", optionLabel: "North Carolina" });
    expect(byId<HTMLSelectElement>("jv-field-yK5jZfwR").value).toBe("string:NC");
  });

  test("A code that names no state in the list commits nothing", async () => {
    mount(angularState(true));
    expect(await write("jv-field-yK5jZfwR", "TX")).toMatchObject({ outcome: "failed", reason: "option-not-found" });
  });

  /** Rippling: a country-code combobox ("+1 US") beside a plain text box that keeps only digits. */
  const split = (picker: boolean) => `<span id="field-28-label">Phone number</span>
    ${picker ? `<input id="field-31" placeholder="Search" aria-label="Search" role="combobox" aria-autocomplete="list" aria-haspopup="listbox" aria-expanded="false" value="+1 US">` : ""}
    <input id="field-28" inputmode="tel" placeholder="Phone number" aria-labelledby="field-28-label">`;
  const digitsOnly = () => {
    const el = byId("field-28");
    const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set as (v: string) => void;
    el.addEventListener("input", () => set.call(el, el.value.replace(/\D/g, "")));
  };

  test("A phone box beside a country-code picker takes the national digits, so the page keeps what was written", async () => {
    mount(split(true));
    digitsOnly();
    const r = await write("field-28", "+1 919 555 0142", { phone: true });
    expect(r).toMatchObject({ outcome: "filled" });
    expect(byId("field-28").value).toBe("9195550142");
  });

  test("A box that reformats to digits holds the number: inputmode tel counts as a phone box in the read-back", async () => {
    mount(split(false));
    digitsOnly();
    const r = await write("field-28", "+1 919 555 0142", { phone: true });
    expect(r).toMatchObject({ outcome: "filled" });
    expect(byId("field-28").value).toBe("19195550142");
  });
});
