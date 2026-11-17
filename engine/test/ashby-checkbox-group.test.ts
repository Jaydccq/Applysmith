// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Drivers for application boards (Ashby); a checkbox group is one question.
import { describe, expect, test } from "vitest";
import { scanControls } from "../src/scan";
import { mount } from "./helpers";

/** Ashby's checkbox group: every box is named after its own option, the title's label points at an id nothing carries, and "required" is only a class on the title. */
const GROUP = `<form><label for="fn">First Name *</label><input id="fn" required><div class="ashby-application-form-field-entry">
  <fieldset class="ashby-application-form-input-checkbox-group">
    <label for="missing-id" class="ashby-application-form-question-title _required_a1b2c">Office Location</label>
    <div class="ashby-application-form-question-description">Please identify your preferred office location(s)</div>
    <div><input type="checkbox" id="o1" name="Austin, Texas"><label for="o1">Austin, Texas</label></div>
    <div><input type="checkbox" id="o2" name="Remote"><label for="o2">Remote</label></div>
    <div><input type="checkbox" id="o3" name="London"><label for="o3">London</label></div>
  </fieldset></div></form>`;

describe("Ashby checkbox groups", () => {
  test("a group whose boxes are named after their options is one question", () => {
    mount(GROUP);
    const infos = scanControls(document).filter((i) => i.kind === "checkbox");
    expect(infos).toHaveLength(1);
    const q = infos[0]!;
    expect(q.members.map((m) => m.id)).toEqual(["o1", "o2", "o3"]);
    expect(q.options.map((o) => o.label)).toEqual(["Austin, Texas", "Remote", "London"]);
    expect(q.label.text).toContain("Office Location");
    expect(q.required).toBe(true);
    expect(q.inputType).toBe("multi_select");
  });




  test("titles that point at nothing, empty titles, self-named boxes", () => {
    const ask = (kind: string, title: string, desc: string, names: string[]) => {
      const box = (n: string, i: number) => `<input type="${kind}" id="f${i}" name="${kind === "radio" ? "r" : n}"><label for="f${i}">${n}</label>`;
      const a = "ashby-application-form-";
      mount(`<fieldset class="${a}input-${kind}-group"><label class="${a}question-title" for="x">${title}</label><p class="${a}question-description">${desc}</p>${names.map(box).join("")}</fieldset>`);
      return scanControls(document)[0]!.label.text;
    };
    expect(ask("radio", "Served?", "Any forces.", ["No", "Yes"])).toBe("Served? Any forces.");
    expect(ask("checkbox", "", "Contact you?", ["I agree"])).toBe("Contact you?");
    expect(ask("checkbox", "I understand it.", "", ["I understand"])).toBe("I understand it.");
  });
});
