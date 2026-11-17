import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPicker, reportFor, type Picker } from "../src/widgets/picker";
import type { FieldReport } from "../src/shared/messages";

const roots: ShadowRoot[] = [];
const realAttach = Element.prototype.attachShadow;
let picker: Picker;
let sent: FieldReport[];
let pageClicks: number;
let countClicks: () => void;
let sendImpl: (r: FieldReport) => Promise<void>;

const bar = () => roots.map((r) => r.querySelector(".bar")).filter(Boolean).pop() as HTMLElement | null;
const btn = (label: string) => [...bar()!.querySelectorAll("button")].find((b) => b.textContent === label)!;

function click(el: Element): MouseEvent {
  const e = new MouseEvent("click", { bubbles: true, cancelable: true, composed: true });
  el.dispatchEvent(e);
  return e;
}

beforeEach(() => {
  vi.useFakeTimers();
  roots.length = 0;
  Element.prototype.attachShadow = function (init: ShadowRootInit) {
    const r = realAttach.call(this, { ...init, mode: "open" });
    roots.push(r);
    return r;
  };
  (window as unknown as { happyDOM: { setURL(u: string): void } }).happyDOM.setURL("https://example.com/careers/apply?token=abc");
  document.body.innerHTML = `<form>
    <label for="city">City</label><input id="city" type="text" value="Springfield">
    <label for="pw">Password</label><input id="pw" type="password" value="hunter2">
    <div id="wrap"><label>Country<select id="cc"><option>Canada</option><option>Mexico</option></select></label></div>
    <a id="link" href="/elsewhere">Elsewhere</a><p id="blank">just text</p></form>`;
  sent = [];
  pageClicks = 0;
  countClicks = () => pageClicks++;
  document.addEventListener("click", countClicks);
  sendImpl = async (r) => void sent.push(r);
  picker = createPicker({ doc: document, win: window, send: (r) => sendImpl(r) });
});
afterEach(() => {
  document.removeEventListener("click", countClicks);
  picker.cancel();
  Element.prototype.attachShadow = realAttach;
  vi.useRealTimers();
});

describe("Point at a field", () => {
  it("consumes the click so the page does not see it, and uses it to pick", async () => {
    picker.arm();
    const e = click(document.getElementById("link")!);
    expect(e.defaultPrevented).toBe(true);
    expect(pageClicks).toBe(0);
    expect(bar()!.textContent).toMatch(/No field there/);
  });

  it("reports a missed field with its question and the note, with no value, and changes nothing on the page", async () => {
    picker.arm();
    click(document.getElementById("city")!);
    expect(pageClicks).toBe(0);
    const note = bar()!.querySelector("textarea")!;
    note.value = "It was left empty.";
    pageClicks = 0;
    btn("Send report").click();
    await vi.advanceTimersByTimeAsync(0);
    expect(sent).toHaveLength(1);
    expect(sent[0]).toMatchObject({ question: "City", controlKind: "text", note: "It was left empty.", tag: "input", url: "https://example.com/careers/apply", frameUrl: "https://example.com/careers/apply" });
    expect(JSON.stringify(sent[0])).not.toContain("Springfield");
    expect(JSON.stringify(sent[0])).not.toMatch(/token=abc/);
    expect((document.getElementById("city") as HTMLInputElement).value).toBe("Springfield");
  });

  it("never sends what was typed in an editable region or a text box, nor the options of a withheld field, as nearby text", () => {
    document.body.innerHTML = `<div id="q"><span>Tell us about yourself</span><div id="ed" contenteditable="true">My private answer</div></div>
      <div><span>Cover note</span><textarea id="ta">Typed cover note</textarea></div>`;
    const ed = reportFor(document.getElementById("ed") as HTMLElement, "", window);
    expect(ed.nearbyText).toBe("Tell us about yourself");
    const ta = reportFor(document.getElementById("ta") as HTMLElement, "", window);
    expect(ta.nearbyText).toBe("Cover note");
    document.body.innerHTML = `<div><span>Gender</span><select id="g"><option>Woman</option><option>Man</option></select></div>`;
    expect(reportFor(document.getElementById("g") as HTMLElement, "", window)).toMatchObject({ options: [], nearbyText: "" });
  });

  it("sends up to 25 sample options of a select and never its chosen value", () => {
    document.getElementById("cc")!.innerHTML = Array.from({ length: 40 }, (_, i) => `<option>Country ${i}</option>`).join("");
    const r = reportFor(document.getElementById("cc") as HTMLElement, "x".repeat(900), window);
    expect(r.options).toHaveLength(25);
    expect(r.note).toHaveLength(500);
    expect(r.controlKind).toBe("select");
  });

  it("refuses a password field and sends nothing", () => {
    picker.arm();
    click(document.getElementById("pw")!);
    expect(bar()!.textContent).toMatch(/password field/);
    expect(bar()!.querySelector("textarea")).toBeNull();
    expect(sent).toHaveLength(0);
  });
});
