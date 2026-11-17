// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Page API (the read-only inventory the apply chain's walker reads).
import { describe, expect, test } from "vitest";
import { byId, fakeHost, makeEngine, mount } from "./helpers";

describe("Inspect inventory for the walker", () => {
  test("a password box is listed for its structure, with no value, and is marked inspection-only", () => {
    mount(`<label for="em">Email</label><input id="em" type="email" value="ada@example.org">
      <label for="pw">Password</label><input id="pw" type="password" required value="hunter2">`);
    const before = document.body.innerHTML;
    const rows = makeEngine(fakeHost()).inspect();
    const pw = rows.find((r) => r.label === "Password");
    expect(pw).toMatchObject({ kind: "text", required: true, inspectionOnly: true });
    expect(pw?.value).toBeUndefined();
    expect(JSON.stringify(rows)).not.toContain("hunter2");
    expect(rows.find((r) => r.label === "Email")?.inspectionOnly).toBeUndefined();
    expect(document.body.innerHTML).toBe(before);
  });

  test("a one-time-code box is listed inspection-only; a hidden password is not listed", () => {
    mount(`<label for="otp">Verification code</label><input id="otp" autocomplete="one-time-code">
      <div style="display:none"><label for="h">Hidden password</label><input id="h" type="password"></div>`);
    const rows = makeEngine(fakeHost()).inspect();
    expect(rows.map((r) => r.label)).toEqual(["Verification code"]);
    expect(rows[0]).toMatchObject({ inspectionOnly: true });
    expect(rows[0]?.value).toBeUndefined();
  });

  test("an attachment slot is inspection-only, and a parser-only upload is told apart from the application resume", () => {
    mount(`<div><label for="r">Resume</label><input id="r" type="file"></div>
      <div><label for="a">Autofill your application</label><input id="a" type="file"></div>`);
    const rows = makeEngine(fakeHost()).inspect();
    const resume = rows.find((r) => r.label === "Resume");
    const parser = rows.find((r) => r.label.startsWith("Autofill"));
    expect(resume).toMatchObject({ kind: "file", inspectionOnly: true });
    expect(resume?.parserOnly).toBeUndefined();
    expect(parser).toMatchObject({ kind: "file", inspectionOnly: true, parserOnly: true });
  });

  test("a composite lookup (a combobox wrapper around its input) is one row, not two", () => {
    mount(`<div><label id="cl">City</label><div role="combobox" aria-labelledby="cl"><input id="city" aria-autocomplete="list" value="Springfield"></div></div>`);
    const rows = makeEngine(fakeHost()).inspect();
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ label: "City", value: "Springfield" });
  });

  test("an answered button group stays in the inventory with its answer", () => {
    mount(`<div role="group" aria-labelledby="q"><span id="q">Are you 18 or older?</span>
      <button type="button" id="yes" aria-pressed="false">Yes</button><button type="button" id="no" aria-pressed="false">No</button></div>`);
    const engine = makeEngine(fakeHost());
    expect(engine.inspect()).toHaveLength(1);
    expect(engine.inspect()[0]).toMatchObject({ kind: "radio-group", label: "Are you 18 or older?" });
    expect(engine.inspect()[0]?.value).toBeUndefined();
    byId("yes").setAttribute("aria-pressed", "true");
    const rows = engine.inspect();
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ kind: "radio-group", value: "Yes" });
  });
});

describe("Inspect options", () => {
  // The extension turns a row into the event of what the person picked; the server refuses a whole event with more than 25 options or an option over 200 characters.
  test("a row names its control's options as an event carries them, and a withheld control names none", () => {
    const long = `Lisbon, ${"Avenida da Liberdade, ground floor, ".repeat(7).trim()}`;
    const offices = ["Berlin", long, ...Array.from({ length: 28 }, (_, i) => `Office ${i}`)];
    mount(`<label for="office">Preferred office</label><select id="office"><option value="">Select...</option>${offices.map((o) => `<option>${o}</option>`).join("")}</select>
      <fieldset><legend>Do you hold a work authorization for the US?</legend><label><input type="radio" name="auth" value="y">Yes</label><label><input type="radio" name="auth" value="n">No</label></fieldset>
      <label for="g">Gender</label><select id="g"><option value="">Select...</option><option>Female</option><option>Male</option></select>
      <label for="n">Preferred name</label><input id="n">`);
    const options = Object.fromEntries(makeEngine(fakeHost()).inspect().map((r) => [r.label, r.options]));
    expect(options).toEqual({
      "Preferred office": ["Berlin", Array.from(long).slice(0, 200).join(""), ...offices.slice(2, 25)],
      "Do you hold a work authorization for the US?": ["Yes", "No"],
      Gender: undefined,
      "Preferred name": undefined,
    });
  });
});

describe("Inspect labels", () => {
  test("a questionnaire's listbox trigger is named by its fieldset legend and marked required", () => {
    mount(`<fieldset><legend>Are you at least 18 years old?<abbr title="required" class="requiredAsterisk">*</abbr></legend><button aria-haspopup="listbox">Select One</button><input type="text" style="width:0;height:0;border:0;padding:0"></fieldset>`);
    const rows = makeEngine(fakeHost()).inspect();
    const trigger = rows.find((r) => r.kind === "custom-listbox");
    expect(trigger).toMatchObject({ label: expect.stringContaining("Are you at least 18"), required: true });
  });
});
