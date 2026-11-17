// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Drivers for enterprise application suites (Workday virtualized popups and scroll-only prompts).
import { describe, expect, test } from "vitest";
import { readInfo } from "../src/scan";
import { mountWorkdayPrompt } from "./ats-fixtures";
import { infoOf, mount, write } from "./helpers";

const DEGREES = [
  "Associate Degree",
  "Bachelor's Degree",
  "Certificate",
  "Diploma",
  "Doctorate",
  "Executive MBA",
  "High School",
  "Juris Doctor",
  "Master of Arts",
  "Master of Science",
  "Postdoctoral",
  "Professional Degree",
];

interface Popup {
  trigger: HTMLButtonElement;
  keys: string[];
  clicks: string[];
}

/**
 * A questionnaire dropdown whose popup is fixed-position and virtualized: only
 * the rows around the highlighted one are mounted, its scroller discards
 * scrollTop writes, and a printable key jumps to the next label that starts
 * with it.
 */
function mountVirtualDropdown(options: string[], window = 3): Popup {
  document.body.innerHTML = `<label id="l1">Highest level of education *</label><button id="deg" type="button" aria-haspopup="listbox" aria-labelledby="l1">Select One</button>`;
  const trigger = document.getElementById("deg") as HTMLButtonElement;
  const popup = document.createElement("ul");
  popup.id = "deg-popup";
  popup.setAttribute("role", "listbox");
  popup.setAttribute("aria-labelledby", "deg");
  popup.style.position = "fixed";
  popup.hidden = true;
  Object.defineProperty(popup, "scrollTop", { get: () => 0, set: () => undefined });
  document.body.append(popup);
  const keys: string[] = [];
  const clicks: string[] = [];
  let active = -1;
  const paint = () => {
    popup.innerHTML = "";
    const from = Math.max(0, active - 1);
    for (let i = from; i < Math.min(options.length, from + window); i++) {
      const li = document.createElement("li");
      li.id = `deg-opt-${i}`;
      li.setAttribute("role", "option");
      li.textContent = options[i] as string;
      li.addEventListener("click", () => {
        clicks.push(options[i] as string);
        trigger.textContent = options[i] as string;
        popup.hidden = true;
      });
      popup.append(li);
    }
    if (active >= 0) trigger.setAttribute("aria-activedescendant", `deg-opt-${active}`);
  };
  trigger.addEventListener("mousedown", () => {
    popup.hidden = false;
    active = -1;
    paint();
  });
  trigger.addEventListener("keydown", (e) => {
    const key = (e as KeyboardEvent).key;
    keys.push(key);
    if (popup.hidden) return;
    if (key === "ArrowDown") active = Math.min(options.length - 1, active + 1);
    else if (key === "Enter" && active >= 0) {
      trigger.textContent = options[active] as string;
      popup.hidden = true;
      return;
    } else if (key.length === 1) {
      const at = options.findIndex((o, i) => i > active && o.toLowerCase().startsWith(key.toLowerCase()));
      if (at >= 0) active = at;
    }
    paint();
  });
  return { trigger, keys, clicks };
}

describe("Workday popup below the fold", () => {
  test("A virtualized popup whose scroller swallows scrollTop writes surfaces a lower option by keyboard type-ahead", async () => {
    const m = mountVirtualDropdown(DEGREES);
    const r = await write("deg", "Postdoctoral");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Postdoctoral" });
    expect(m.trigger.textContent).toBe("Postdoctoral");
    expect(readInfo(infoOf("deg"))).toBe("Postdoctoral");
    expect(m.keys.some((k) => k === "P" || k === "p")).toBe(true);
  });

  test("A lower option with no type-ahead help is reached by stepping ArrowDown until it mounts, then committed", async () => {
    // Every label starts with the same letter, so only the arrow steps can reach the end.
    const flat = Array.from({ length: 30 }, (_, i) => `Level ${String(i).padStart(2, "0")}`);
    const m = mountVirtualDropdown(flat);
    const r = await write("deg", "Level 22");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Level 22" });
    expect(m.trigger.textContent).toBe("Level 22");
  });

  test("An option past the keyboard budget is not committed and nothing else is chosen", async () => {
    const flat = Array.from({ length: 200 }, (_, i) => `Level ${String(i).padStart(3, "0")}`);
    const m = mountVirtualDropdown(flat);
    const r = await write("deg", "Level 190");
    expect(r.outcome).not.toBe("filled");
    expect(m.clicks).toEqual([]);
    expect(m.trigger.textContent).toBe("Select One");
  });
});

describe("Workday popup with its own scroller", () => {
  test("An option below the popup's fold is nudged into the popup's own scroller before it is clicked", async () => {
    document.body.innerHTML = `<label id="l1">Highest level of education *</label><button id="deg" type="button" aria-haspopup="listbox" aria-labelledby="l1">Select One</button>`;
    const trigger = document.getElementById("deg") as HTMLButtonElement;
    const popup = document.createElement("ul");
    popup.setAttribute("role", "listbox");
    popup.setAttribute("aria-labelledby", "deg");
    popup.style.cssText = "position:fixed;overflow-y:auto";
    popup.hidden = true;
    document.body.append(popup);
    let top = 0;
    const ROW = 30;
    const VIEW = 120;
    Object.defineProperty(popup, "scrollTop", { get: () => top, set: (v: number) => void (top = Math.max(0, Math.min(v, (DEGREES.length - 1) * ROW - VIEW))) });
    popup.getBoundingClientRect = () => ({ top: 0, bottom: VIEW, left: 0, right: 200, width: 200, height: VIEW, x: 0, y: 0, toJSON: () => ({}) });
    // The sizes a browser reports for a box with more rows than height.
    Object.defineProperty(popup, "scrollHeight", { get: () => DEGREES.length * ROW });
    Object.defineProperty(popup, "clientHeight", { get: () => VIEW });
    const clicked: string[] = [];
    DEGREES.forEach((label, i) => {
      const li = document.createElement("li");
      li.setAttribute("role", "option");
      li.textContent = label;
      li.getBoundingClientRect = () => ({ top: i * ROW - top, bottom: (i + 1) * ROW - top, left: 0, right: 200, width: 200, height: ROW, x: 0, y: 0, toJSON: () => ({}) });
      // A row outside the popup's frame is covered, so a click on it does nothing.
      li.addEventListener("click", () => {
        const r = li.getBoundingClientRect();
        if (r.top < 0 || r.bottom > VIEW) return;
        clicked.push(label);
        trigger.textContent = label;
        popup.hidden = true;
      });
      popup.append(li);
    });
    trigger.addEventListener("mousedown", () => (popup.hidden = false));
    const r = await write("deg", "Postdoctoral");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Postdoctoral" });
    expect(clicked).toEqual(["Postdoctoral"]);
  });
});

describe("Workday scroll-only prompt", () => {
  test("A prompt that ignores the typed query is read as its whole catalog by scrolling, and an exact label is committed", async () => {
    mount("");
    const catalog = Array.from({ length: 30 }, (_, i) => `Degree ${String(i).padStart(2, "0")}`).concat(["Bachelor's Degree"]);
    const p = mountWorkdayPrompt({ id: "degree", label: "Degree", catalog, windowSize: 5, scrollOnly: true });
    const r = await write("degree", "Bachelor's Degree");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Bachelor's Degree" });
    expect(p.pills()).toEqual(["Bachelor's Degree"]);
    expect(p.input.value).toBe("");
  });

  test("A scroll-only prompt commits only an exact label: a near name is abstained, never guessed", async () => {
    mount("");
    const p = mountWorkdayPrompt({ id: "degree", label: "Degree", catalog: ["Bachelor of Science", "Bachelor of Arts", "Master of Science"], scrollOnly: true });
    const r = await write("degree", "Bachelor");
    expect(r.outcome).toBe("abstained");
    expect(p.clicks).toEqual([]);
    expect(p.input.value).toBe("");
  });
});
