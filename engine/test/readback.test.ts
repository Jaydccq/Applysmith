// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Committed value read-back; Sensitive controls and redacted fragments (diagnostic control snapshot).
import { describe, expect, test } from "vitest";
import { committedDisplay, committedTokens } from "../src/committed";
import { controlSnapshot, MAX_FRAGMENT_BYTES, redactedFragment } from "../src/fragment";
import { isPlaceholderText } from "../src/placeholder";
import { readInfo } from "../src/scan";
import { infoOf, mount } from "./helpers";

describe("Committed value read-back", () => {
  test("Prefix-independent read: a control classed select-customfield_1__control with a __single-value node returns its committed label, and a form-control text input is not mistaken for it", () => {
    mount(`<label for="r">Level</label><div class="select-customfield_1__control"><div class="select-customfield_1__value-container"><div class="select-customfield_1__single-value">Senior</div><input id="r" role="combobox" aria-haspopup="true"></div></div>
      <label for="t">Nickname</label><input id="t" class="form-control" value="Ada">`);
    expect(readInfo(infoOf("r"))).toBe("Senior");
    expect(readInfo(infoOf("t"))).toBe("Ada");
    expect(committedDisplay(document.getElementById("t") as Element)).toBeNull();
  });

  test("A react-select search input that is empty reads the sibling label and a non-empty raw value wins", () => {
    mount(`<div class="x__control"><div class="x__single-value">Beta</div><input id="i" role="combobox"></div>`);
    const input = document.getElementById("i") as HTMLInputElement;
    expect(committedDisplay(input)).toBe("Beta");
    input.value = "typing";
    expect(committedDisplay(input)).toBe("typing");
  });

  test("Ant Design reads .ant-select-selection-item because rc-select clears the input", () => {
    mount(`<label for="a">Role</label><div class="ant-select"><div class="ant-select-selector"><span class="ant-select-selection-item" title="Engineer">Engineer</span><input id="a" role="combobox" value=""></div></div>`);
    expect(readInfo(infoOf("a"))).toBe("Engineer");
    mount(`<label for="a">Role</label><div class="ant-select"><div class="ant-select-selector"><span class="ant-select-selection-placeholder">Please select</span><input id="a" role="combobox" value=""></div></div>`);
    expect(readInfo(infoOf("a"))).toBe("");
  });

  test("A slotted or shadow-tag value is read from the nested tags and the overflow tag is excluded", () => {
    mount(`<label for="h">Skills</label><div id="h" role="combobox" aria-haspopup="listbox"></div>`);
    const host = document.createElement("spl-tags");
    document.getElementById("h")?.appendChild(host);
    const root = host.attachShadow({ mode: "open" });
    root.innerHTML = `<spl-tag>Go</spl-tag><spl-tag>Rust</spl-tag><spl-tag>+2 more</spl-tag>`;
    expect(committedTokens(host)).toEqual(["Go", "Rust"]);
  });

  test("A Select2 chip reads its label whole: the remove glyph goes, a label's own leading x stays", () => {
    const chip = (label: string, title: boolean) => `<li class="select2-selection__choice"${title ? ` title="${label}"` : ""}><span class="select2-selection__choice__remove">×</span>${label}</li>`;
    mount(`<span class="select2-container"><ul id="s" class="select2-selection__rendered">${chip("xUnit", true)}${chip("x86 assembly", true)}${chip("Go", false)}</ul></span>`);
    expect(committedTokens(document.getElementById("s") as Element)).toEqual(["xUnit", "x86 assembly", "Go"]);
  });

  test("An Oracle cx-select input reads its value unless expanded or invalid", () => {
    mount(`<label for="c">Country</label><input id="c" class="cx-select-input" role="combobox" aria-haspopup="grid" value="France">`);
    const input = document.getElementById("c") as HTMLInputElement;
    expect(committedDisplay(input)).toBe("France");
    input.setAttribute("aria-expanded", "true");
    expect(committedDisplay(input)).toBe("");
    input.setAttribute("aria-expanded", "false");
    input.setAttribute("aria-invalid", "true");
    expect(committedDisplay(input)).toBe("");
  });

  test("A Workday-style button reads its own text minus placeholders", () => {
    mount(`<label for="w">Source</label><button id="w" aria-haspopup="listbox" type="button">Select One</button>`);
    expect(readInfo(infoOf("w"))).toBe("");
    (document.getElementById("w") as HTMLElement).textContent = "Job board";
    expect(readInfo(infoOf("w"))).toBe("Job board");
  });

  test("Every ticked box of a checkbox group reads in page order", () => {
    mount(`<fieldset><legend>Languages</legend><label><input type="checkbox" id="a" name="l"> English</label><label><input type="checkbox" id="b" name="l"> French</label><label><input type="checkbox" id="c" name="l"> German</label></fieldset>`);
    (document.getElementById("c") as HTMLInputElement).checked = true;
    (document.getElementById("a") as HTMLInputElement).checked = true;
    expect(readInfo(infoOf("a"))).toBe("English, German");
  });

  test("The placeholder vocabulary reads as empty", () => {
    for (const p of ["Select one", "Select an option", "Select...", "Choose...", "Please select", "No selection", "None selected", "-- Choose --", "— Please Specify —", "Choose an option", "Select a value", "No answer", "-- No answer --", " "]) {
      expect(isPlaceholderText(p), p).toBe(true);
    }
    expect(isPlaceholderText("Selection committee")).toBe(false);
  });

  test("A select whose unchosen option reads No answer (JazzHR, value 0) is unanswered, so a required one is filled", () => {
    mount(`<label for="r">Are you willing to relocate?</label><select id="r"><option value="0" selected>No answer</option><option value="1">Yes</option><option value="2">No</option></select>`);
    const info = infoOf("r");
    expect(info.answered).toBe(false);
    expect(readInfo(info)).toBe("");
  });
});

describe("Diagnostic control snapshot", () => {
  test("Contact strings become tokens, input values, scripts and hidden tokens are omitted", () => {
    mount(`<fieldset><legend>Contact alex@example.com or +1 555 0100</legend><label for="e">Email</label><input id="e" value="alex@example.com" placeholder="you@example.com"><script>var token="abc"</script><input type="hidden" name="csrf" value="tok"></fieldset>`);
    const snap = controlSnapshot(document.getElementById("e") as Element);
    expect(snap?.html).toContain("[email]");
    expect(snap?.html).toContain("[phone]");
    expect(snap?.html).not.toContain("alex@example.com");
    expect(snap?.html).not.toContain("555 0100");
    expect(snap?.html).not.toContain("token");
    expect(snap?.html).not.toContain("csrf");
    expect(snap?.html).not.toMatch(/value="/);
  });

  test("Options, radios and checkboxes keep their value attribute; nested editable contents are dropped", () => {
    mount(`<fieldset><legend>Pick</legend><select id="s"><option value="a">A</option></select><input type="radio" value="r1"><div contenteditable="true">typed secret</div></fieldset>`);
    const snap = controlSnapshot(document.getElementById("s") as Element);
    expect(snap?.html).toContain('value="a"');
    expect(snap?.html).toContain('value="r1"');
    expect(snap?.html).not.toContain("typed secret");
  });

  test("A node-limited snapshot is flagged incomplete and the copy is bounded in bytes", () => {
    mount(`<fieldset><legend>Many</legend>${Array.from({ length: 200 }, (_, i) => `<div><span>row ${i}</span></div>`).join("")}<input id="e"></fieldset>`);
    const snap = controlSnapshot(document.getElementById("e") as Element);
    expect(snap?.incomplete).toBe(true);
    expect(new TextEncoder().encode(snap?.html ?? "").length).toBeLessThanOrEqual(MAX_FRAGMENT_BYTES);
    const multibyte = redactedFragment(Object.assign(document.createElement("div"), { textContent: "é".repeat(5000) }));
    expect(new TextEncoder().encode(multibyte.html).length).toBeLessThanOrEqual(MAX_FRAGMENT_BYTES);
    expect(multibyte.incomplete).toBe(true);
  });

  test("There is no snapshot for a detached, password or one-time-code control", () => {
    mount(`<input id="p" type="password"><input id="o" autocomplete="one-time-code"><input id="d">`);
    expect(controlSnapshot(document.getElementById("p") as Element)).toBeNull();
    expect(controlSnapshot(document.getElementById("o") as Element)).toBeNull();
    const detached = document.getElementById("d") as Element;
    detached.remove();
    expect(controlSnapshot(detached)).toBeNull();
  });
});
