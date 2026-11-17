// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Widget drivers for custom controls; Placeholders and overwrite decisions;
// Listbox open, search, score and commit ladder; Committed value read-back (driver half).
import { describe, expect, test } from "vitest";
import { traitsOf } from "../src/listbox";
import { PAD, byId, ctx, fakeHost, infoOf, makeEngine, mount, noEdits, runFill, write } from "./helpers";
import { mountMenu } from "./widgets-fixtures";

/** An open list whose options ignore every gesture. */
function stuckMenu(id: string) {
  const m = mountMenu({ id, label: "Pet", options: ["Cat", "Dog"], clickable: false });
  m.list.hidden = false;
  const clone = m.trigger.cloneNode(true) as HTMLElement;
  m.trigger.replaceWith(clone);
  return clone;
}

const COUNTRIES = ["Canada", "United States of America", "United States Minor Outlying Islands", "Mexico"];

describe("Widget drivers for custom controls", () => {
  test("Full pointer sequence: a trigger that opens only on a complete pointer sequence is opened, the option chosen and the trigger shows its label", async () => {
    mount("");
    const m = mountMenu({ id: "country", label: "Country", options: ["Canada", "United States", "Mexico"], openOn: "sequence" });
    const r = await write("country", "United States");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "United States" });
    expect(m.shown()).toBe("United States");
    expect(m.list.hidden).toBe(true);
    expect(m.trigger.getAttribute("aria-expanded")).toBe("false");
  });

  test("Already selected: a listbox that already displays the answer is not activated and the row is prefilled", async () => {
    mount("");
    const m = mountMenu({ id: "c", label: "Country", options: ["Canada", "Mexico"] });
    m.trigger.textContent = "Canada";
    m.list.querySelector("li")?.setAttribute("aria-selected", "true");
    // Activating the selected option would deselect it on this widget.
    m.list.querySelector("li")?.addEventListener("click", () => (m.trigger.textContent = "Select one"));
    const r = await write("c", "Canada");
    expect(r).toMatchObject({ outcome: "prefilled" });
    expect(m.shown()).toBe("Canada");
  });

  test("A decline is committed on the option that words it differently, and on no other option", async () => {
    mount("");
    const m = mountMenu({ id: "race", label: "Race", options: ["Asian", "White", "Choose not to disclose"] });
    const r = await write("race", "Decline to self-identify");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Choose not to disclose" });
    expect(m.shown()).toBe("Choose not to disclose");
    mount("");
    const n = mountMenu({ id: "race2", label: "Race", options: ["Asian", "White", "Two or more races"] });
    expect(await write("race2", "Decline to self-identify")).toMatchObject({ outcome: "abstained", reason: "option-not-found" });
    expect(n.shown()).not.toBe("Asian");
  });

  test("A whole-word phrase of at least three letters counts as already showing the answer", async () => {
    mount("");
    const m = mountMenu({ id: "c", label: "Country", options: ["United States of America"] });
    m.trigger.textContent = "United States of America";
    expect((await write("c", "United States")).outcome).toBe("prefilled");
    mount("");
    const n = mountMenu({ id: "d", label: "State", options: ["South Carolina", "North Carolina"] });
    n.trigger.textContent = "North Carolina";
    const r = await write("d", "South Carolina", {}, "replace");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "South Carolina" });
    mount("");
    const k = mountMenu({ id: "k", label: "Language", options: ["C++", "C#"] });
    k.trigger.textContent = "C++";
    expect(await write("k", "C#", {}, "replace")).toMatchObject({ outcome: "filled", optionLabel: "C#" });
  });

  test("Stale page default corrected: a value no lane wrote is replaced by the answer", async () => {
    mount("");
    const m = mountMenu({ id: "st", label: "State", options: ["North Carolina", "South Carolina"] });
    m.trigger.textContent = "North Carolina";
    const r = await write("st", "South Carolina");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "South Carolina" });
    expect(m.shown()).toBe("South Carolina");
  });

  test("A value another lane wrote in this run is not fought", async () => {
    mount("");
    const m = mountMenu({ id: "st", label: "State", options: ["North Carolina", "South Carolina"] });
    m.trigger.textContent = "North Carolina";
    const infoSel = infoOf("st").selector;
    const r = await write("st", "South Carolina", {}, "replace", new Map([[infoSel, "North Carolina"]]));
    expect(r).toMatchObject({ outcome: "prefilled", reason: "existing-value" });
    expect(m.shown()).toBe("North Carolina");
  });

  test("Equal score prefers the shorter label: United States of America over United States Minor Outlying Islands", async () => {
    mount("");
    const m = mountMenu({ id: "c", label: "Country", options: COUNTRIES });
    const r = await write("c", "United States");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "United States of America" });
    expect(m.shown()).toBe("United States of America");
  });

  test("A degree spelled out takes the one option of its level (Workday's Masters (+2 or +3 yrs)), and no option on a tie", async () => {
    mount("");
    const m = mountMenu({ id: "deg", label: "Degree", options: ["Bachelors (+3 to +4 yrs)", "Masters (+2 or +3 yrs)", "Doctorate (+3 to +5 yrs)"] });
    expect(await write("deg", "Master of Science (M.S)")).toMatchObject({ outcome: "filled", optionLabel: "Masters" });
    expect(m.shown()).toBe("Masters (+2 or +3 yrs)");
    mount("");
    mountMenu({ id: "deg2", label: "Degree", options: ["Master of Arts", "Master of Engineering", "Bachelor of Science"] });
    expect((await write("deg2", "Master of Science (M.S)")).outcome).not.toBe("filled");
  });

  test("A list of abbreviations takes the degree's own (The Clearing House's Workday: BS, MS), and nothing when none is the answer's", async () => {
    const tch = ["GED", "High School", "AA", "AS", "BA", "BS", "MA", "MS", "MBA", "PhD", "Other"];
    mount("");
    const m = mountMenu({ id: "tch1", label: "Degree", options: tch });
    expect((await write("tch1", "Master of Science (M.S)")).outcome).toBe("filled");
    expect(m.shown()).toBe("MS");
    mount("");
    const b = mountMenu({ id: "tch2", label: "Degree", options: tch });
    expect((await write("tch2", "Bachelor of Science")).outcome).toBe("filled");
    expect(b.shown()).toBe("BS");
    mount("");
    mountMenu({ id: "tch3", label: "Degree", options: tch.filter((o) => o !== "MS") });
    expect((await write("tch3", "Master of Science (M.S)")).outcome).not.toBe("filled");
  });

  test("Among several options of the degree's level, the one naming the level alone is taken (Ensemble's Workday), never another field's", async () => {
    const ensemble = ["Select One", "Master of Science Information Technology", "Bachelor of Science Information Technology", "High School/GED", "Associates", "Bachelor of Science Nursing", "Bachelors", "Master of Science Nursing", "Masters", "Doctorate"];
    mount("");
    const m = mountMenu({ id: "deg3", label: "Degree", options: ensemble });
    expect(await write("deg3", "Bachelor of Science in Mathematics")).toMatchObject({ outcome: "filled", optionLabel: "Bachelors" });
    expect(m.shown()).toBe("Bachelors");
    mount("");
    mountMenu({ id: "deg4", label: "Degree", options: ensemble.filter((o) => o !== "Masters") });
    expect((await write("deg4", "Master of Science in Mathematics")).outcome).not.toBe("filled");
  });

  test("ASM's Greenhouse list spells each level twice: the first row naming the level alone with no gloss is taken", async () => {
    const asm = ["Accelerated Master's", "Associates", "Associate's Degree", "Bachelors", "Bachelor's Degree", "Doctorate", "Master of Business Administration (M.B.A.)", "Masters", "Master's Degree", "Master's of Business Administration", "Other"];
    mount("");
    const m = mountMenu({ id: "asm1", label: "Degree", options: asm });
    expect((await write("asm1", "Master of Science (M.S)")).outcome).toBe("filled");
    expect(m.shown()).toBe("Masters");
    mount("");
    const b = mountMenu({ id: "asm2", label: "Degree", options: asm });
    expect((await write("asm2", "Bachelor of Science (B.S)")).outcome).toBe("filled");
    expect(b.shown()).toBe("Bachelors");
    mount("");
    mountMenu({ id: "asm3", label: "Degree", options: ["Masters (in progress)", "Masters (completed)"] });
    expect((await write("asm3", "Master of Science (M.S)")).outcome).not.toBe("filled");
  });

  test("Visa's ISCED list: a master's degree takes the generic master's row, not the MBA", async () => {
    const visa = [
      "Select One",
      "High School or Upper-Secondary Completion (ISCED 3: High-School Diploma, A-Levels, IB Diploma, Abitur, Baccalauréat, Matura, GED)",
      "Other/Not Listed",
      "Bachelor’s or Equivalent First-Degree (ISCED 6: BA, BSc, BEng, LLB, etc.)",
      "Master of Business Administration - MBA ( (ISCED 7)",
      "Master’s or Equivalent Second-Degree (ISCED 7: MA, MSc, MEng, MPhil, professional licentiate)",
      "Doctoral or Equivalent Research Degree (ISCED 8: PhD, DPhil, EdD, MD-Res, professional doctorates)",
    ];
    mount("");
    const m = mountMenu({ id: "deg5", label: "Degree", options: visa });
    expect((await write("deg5", "Master of Science (M.S)")).outcome).toBe("filled");
    expect(m.shown()).toBe(visa[5]);
    mount("");
    const mba = mountMenu({ id: "deg6", label: "Degree", options: visa });
    expect((await write("deg6", "Master of Business Administration")).outcome).toBe("filled");
    expect(mba.shown()).toBe(visa[4]);
  });

  test("KLA: a master's degree takes the one master's row naming only degree types, its own among them", async () => {
    const kla = ["Select One", "High School", "Bachelors of Arts or Science (Bachelors)", "Masters of Technology (Masters)", "Masters of Arts or Science or Business Administration (Masters)", "Doctor of Philosophy or Juris Doctor (Doctorate)"];
    mount("");
    const m = mountMenu({ id: "deg7", label: "Degree", options: kla });
    expect((await write("deg7", "Master of Science (M.S)")).outcome).toBe("filled");
    expect(m.shown()).toBe(kla[4]);
    // The field after "in" is not a degree type, and a row naming a field is never taken for it.
    mount("");
    const n = mountMenu({ id: "deg8", label: "Degree", options: kla });
    expect((await write("deg8", "Master of Science in Technology Management")).outcome).toBe("filled");
    expect(n.shown()).toBe(kla[4]);
    mount("");
    mountMenu({ id: "deg9", label: "Degree", options: ["Master of Science Nursing", "Master of Arts"] });
    expect((await write("deg9", "Master of Science (M.S)")).outcome).not.toBe("filled");
  });

  test("An exact label beats a containing one", async () => {
    mount("");
    const m = mountMenu({ id: "y", label: "Eligible?", options: ["Yes, with conditions", "Yes"] });
    await write("y", "Yes");
    expect(m.shown()).toBe("Yes");
  });

  test("Neighbour menu never borrowed: a trigger whose aria-controls names an id not yet in the DOM waits and never clicks the neighbour's option", async () => {
    mount("");
    const a = mountMenu({ id: "a", label: "Authorized?", options: ["Yes", "No"], openOn: "click" });
    a.open();
    const b = mountMenu({ id: "b", label: "Sponsorship?", options: ["Yes", "No"], openOn: "arrow" });
    b.trigger.setAttribute("aria-controls", "b-popup-never-mounted");
    const clicks: string[] = [];
    a.list.addEventListener("click", (e) => clicks.push((e.target as HTMLElement).textContent ?? ""), true);
    const r = await write("b", "Yes");
    expect(r.outcome).toBe("failed");
    expect(r.reason).toBe("listbox-not-opened");
    expect(clicks).toEqual([]);
    expect(a.clicked).toEqual([]);
  });

  test("Neighbour popup still draining: two buttons share one id-less popup and the neighbour's Yes is selected while the second trigger still shows its placeholder", async () => {
    mount("");
    const a = mountMenu({ id: "a", label: "Authorized?", options: ["Yes", "No"], ref: false });
    const b = mountMenu({ id: "b", label: "Sponsorship?", options: ["Yes", "No"], ref: false });
    // One shared id-less popup node that belongs to the first button's wrapper.
    b.list.remove();
    const shared = a.list;
    shared.querySelector("li")?.setAttribute("aria-selected", "true");
    b.trigger.addEventListener("click", () => {
      shared.hidden = false;
    });
    const r = await write("b", "Yes");
    expect(r).toMatchObject({ outcome: "failed", reason: "verify-mismatch" });
    expect(b.shown()).toBe("Select one");
  });

  test("A selected option counts as success only when the control itself shows a non-placeholder value", async () => {
    mount("");
    const m = mountMenu({ id: "y", label: "Eligible?", options: ["Yes", "No"] });
    m.trigger.addEventListener("click", () => m.list.querySelector("li")?.setAttribute("aria-selected", "true"));
    const r = await write("y", "Yes");
    expect(r).toMatchObject({ outcome: "failed", reason: "verify-mismatch" });
  });

  test("Keyboard-only combobox: options that ignore clicks are committed with arrow keys and Enter", async () => {
    mount("");
    const m = mountMenu({ id: "k", label: "Level", options: ["Junior", "Senior", "Staff"], clickable: false });
    const r = await write("k", "Staff");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Staff" });
    expect(m.shown()).toBe("Staff");
  });

  test("A virtualised option list is searched by scrolling until the row mounts", async () => {
    mount("");
    const all = Array.from({ length: 120 }, (_, i) => `Skill ${i}`);
    const m = mountMenu({ id: "v", label: "Skill", options: all.slice(0, 5), clickable: true });
    m.list.style.overflow = "auto";
    const render = (from: number) => {
      m.list.innerHTML = "";
      for (const text of all.slice(from, from + 5)) {
        const li = document.createElement("li");
        li.setAttribute("role", "option");
        li.textContent = text;
        li.addEventListener("click", () => {
          m.trigger.textContent = text;
          m.close();
        });
        m.list.appendChild(li);
      }
    };
    render(0);
    m.list.addEventListener("scroll", () => render(Math.min(115, Math.floor(m.list.scrollTop / 160))));
    const r = await write("v", "Skill 40");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Skill 40" });
  });

  test("A search for a row the list lacks never scrolls the page, and a scrolling box around the list is put back where it was", async () => {
    mount("");
    const page = document.documentElement;
    // A page taller than its window, as a browser reports it.
    Object.defineProperty(page, "scrollHeight", { configurable: true, get: () => 5000 });
    Object.defineProperty(page, "clientHeight", { configurable: true, get: () => 800 });
    const modal = document.createElement("div");
    modal.style.overflowY = "auto";
    document.body.appendChild(modal);
    mountMenu({ id: "top", label: "Skill", options: ["Skill 1", "Skill 2"] });
    mountMenu({ id: "boxed", label: "Tool", options: ["Tool 1", "Tool 2"], parent: modal });
    page.scrollTop = 300;
    modal.scrollTop = 120;
    const scrolled: Element[] = [];
    for (const el of [page, document.body, modal]) el.addEventListener("scroll", (e) => e.target === el && scrolled.push(el));
    try {
      expect(await write("top", "Skill 40")).toMatchObject({ outcome: "failed", reason: "option-not-found" });
      expect(scrolled).toEqual([]);
      expect(page.scrollTop).toBe(300);
      expect(await write("boxed", "Tool 40")).toMatchObject({ outcome: "failed", reason: "option-not-found" });
      expect(scrolled).toContain(modal);
      expect(scrolled).not.toContain(page);
      expect(modal.scrollTop).toBe(120);
    } finally {
      Reflect.deleteProperty(page, "scrollHeight");
      Reflect.deleteProperty(page, "clientHeight");
      page.scrollTop = 0;
    }
  });

  test("A row below the fold of a scroller inside the popup is brought into view by moving that scroller, not the popup around it", async () => {
    mount(`<label id="n-l">Office</label><button type="button" id="n" role="combobox" aria-haspopup="listbox" aria-expanded="false" aria-controls="n-list" aria-labelledby="n-l">Select one</button>
      <div id="n-list" role="listbox" hidden><div id="n-scroll">${Array.from({ length: 40 }, (_, i) => `<div role="option">Office ${i}</div>`).join("")}</div></div>`);
    const trigger = byId<HTMLElement>("n") as unknown as HTMLElement;
    const list = byId<HTMLElement>("n-list") as unknown as HTMLElement;
    const scroller = byId<HTMLElement>("n-scroll") as unknown as HTMLElement;
    trigger.addEventListener("click", () => {
      list.hidden = !list.hidden;
      trigger.setAttribute("aria-expanded", String(!list.hidden));
    });
    const rows = Array.from(scroller.children) as HTMLElement[];
    // The layout a browser reports: a 200px window on 40 rows of 20px, each row placed by how far the window has scrolled.
    Object.defineProperty(scroller, "scrollHeight", { get: () => 800 });
    Object.defineProperty(scroller, "clientHeight", { get: () => 200 });
    const rect = (top: number, height: number) => ({ top, bottom: top + height, left: 0, right: 100, width: 100, height, x: 0, y: top, toJSON: () => ({}) });
    scroller.getBoundingClientRect = () => rect(0, 200);
    rows.forEach((row, i) => {
      row.getBoundingClientRect = () => rect(i * 20 - scroller.scrollTop, 20);
      row.addEventListener("click", () => {
        trigger.textContent = row.textContent;
        list.hidden = true;
        trigger.setAttribute("aria-expanded", "false");
      });
    });
    expect(await write("n", "Office 30")).toMatchObject({ outcome: "filled", optionLabel: "Office 30" });
    // Row 30 spans 600 to 620: its bottom is brought 8px inside the window's lower edge.
    expect(scroller.scrollTop).toBe(428);
    expect(list.scrollTop).toBe(0);
  });

  test("A list that mounts a new listbox node while it is searched is read from the new node", async () => {
    mount("");
    const m = mountMenu({ id: "v", label: "Skill", options: ["Skill 0", "Skill 1"] });
    m.list.addEventListener("scroll", () => {
      const next = document.createElement("ul");
      next.id = m.list.id;
      next.setAttribute("role", "listbox");
      next.innerHTML = `<li role="option">Skill 40</li>`;
      next.querySelector("li")?.addEventListener("click", () => {
        m.trigger.textContent = "Skill 40";
        next.remove();
        m.trigger.setAttribute("aria-expanded", "false");
      });
      m.list.replaceWith(next);
    });
    expect(await write("v", "Skill 40")).toMatchObject({ outcome: "filled", optionLabel: "Skill 40" });
  });

  test("Stuck field: a custom listbox that never commits fails alone at the field budget and the next field still runs", async () => {
    mount(`<label for="t">Nickname</label><input id="t">`);
    stuckMenu("s");
    const { applyValue } = await import("../src/apply");
    const t0 = Date.now();
    const slow = ctx({ fieldBudgetMs: 150, activateMs: 100 });
    const r = await applyValue(infoOf("s"), { value: "Dog", replaceDefault: false }, { ctx: slow, written: new Map(), mode: "replace", acceptLegalDeclarations: false, edits: noEdits() });
    expect(r).toMatchObject({ outcome: "failed", reason: "timeout" });
    // The field budget is 150 ms; the bound only tells it from a hang.
    expect(Date.now() - t0).toBeLessThan(10_000);
    expect((await write("t", "Ada")).outcome).toBe("filled");
  });

  test("The write is clamped to the stage deadline", async () => {
    mount("");
    stuckMenu("s");
    const { applyValue } = await import("../src/apply");
    const t0 = Date.now();
    const slow = ctx({ fieldBudgetMs: 15_000, activateMs: 100 });
    const r = await applyValue(infoOf("s"), { value: "Dog", replaceDefault: false }, { ctx: slow, written: new Map(), mode: "replace", acceptLegalDeclarations: false, edits: noEdits(), deadline: Date.now() + 120 });
    expect(r).toMatchObject({ outcome: "failed", reason: "timeout" });
    expect(Date.now() - t0).toBeLessThan(10_000);
  });
});

describe("Listbox open, search, score and commit ladder", () => {
  test("Open retry: a trigger that mounts nothing on the first pointer open is retried with blur, refocus and ArrowDown", async () => {
    mount("");
    const m = mountMenu({ id: "r", label: "Level", options: ["Junior", "Senior"], openOn: "arrow" });
    const types: string[] = [];
    for (const t of ["blur", "focus", "keydown"]) m.trigger.addEventListener(t, (e) => types.push(`${t}${t === "keydown" ? `:${(e as KeyboardEvent).key}` : ""}`));
    const r = await write("r", "Senior");
    expect(r.outcome).toBe("filled");
    expect(types).toContain("blur");
    expect(types).toContain("keydown:ArrowDown");
  });

  test("Neighbour menu hidden by a flag: lists with iti__hide, chip strips and lists owned by another control are never the popup", async () => {
    mount("");
    const own = mountMenu({ id: "o", label: "Level", options: ["Junior", "Senior"], ref: false, openOn: "click" });
    const hiddenList = document.createElement("ul");
    hiddenList.setAttribute("role", "listbox");
    hiddenList.className = "iti__hide";
    hiddenList.innerHTML = `<li role="option">Senior</li>`;
    const strip = document.createElement("ul");
    strip.setAttribute("role", "listbox");
    strip.setAttribute("data-automation-id", "selectedItemList");
    strip.innerHTML = `<li>Senior</li>`;
    document.body.append(hiddenList, strip);
    const other = mountMenu({ id: "p", label: "Other", options: ["Senior"], ref: true });
    other.open();
    other.list.setAttribute("aria-labelledby", "p");
    const r = await write("o", "Senior");
    expect(r.outcome).toBe("filled");
    expect(own.clicked).toEqual(["Senior"]);
    expect(other.clicked).toEqual([]);
  });

  test("An id-less popup is bound to its trigger through its aria-labelledby", async () => {
    mount("");
    const a = mountMenu({ id: "a", label: "A", options: ["Yes", "No"], ref: false });
    const b = mountMenu({ id: "b", label: "B", options: ["Yes", "No"], ref: false });
    a.list.setAttribute("aria-labelledby", "a");
    b.list.setAttribute("aria-labelledby", "b");
    a.open();
    b.trigger.addEventListener("click", () => (b.list.hidden = false));
    const r = await write("b", "No");
    expect(r.outcome).toBe("filled");
    expect(b.shown()).toBe("No");
    expect(a.clicked).toEqual([]);
  });

  test("A trigger without aria-controls waits for a stale populated popup to drain before it opens", async () => {
    mount("");
    const stale = mountMenu({ id: "z", label: "Stale", options: ["Yes"], ref: true });
    stale.open();
    const m = mountMenu({ id: "m", label: "Fresh", options: ["Yes", "No"], ref: false });
    setTimeout(() => stale.close(), 8);
    const t0 = Date.now();
    const r = await write("m", "No");
    expect(r.outcome).toBe("filled");
    expect(m.shown()).toBe("No");
    expect(Date.now() - t0).toBeLessThan(10_000);
  });

  test("Rippling-style comboboxes are opened without the trailing click, which would toggle the menu shut", async () => {
    mount("");
    const m = mountMenu({ id: "rp", label: "Level", options: ["Junior", "Senior"], openOn: "click" });
    m.trigger.parentElement?.classList.add("rpd-theme");
    let clicks = 0;
    m.trigger.addEventListener("click", () => clicks++);
    // This widget opens on mouse down and a click on the trigger toggles it shut again.
    m.trigger.addEventListener("mousedown", () => m.open());
    const r = await write("rp", "Senior");
    expect(r.outcome).toBe("filled");
    expect(m.shown()).toBe("Senior");
    expect(clicks).toBe(0);
  });

  test("Traits: data-menu-id and the Fabric toggle class name their popup by that id", async () => {
    mount("");
    const m = mountMenu({ id: "fb", label: "Level", options: ["Junior", "Senior"], ref: false, openOn: "click" });
    m.trigger.classList.add("fab-SelectToggle");
    m.trigger.setAttribute("data-menu-id", "fb-list");
    expect(traitsOf(m.trigger).fabric).toBe(true);
    const r = await write("fb", "Senior");
    expect(r.outcome).toBe("filled");
  });
});

describe("Popup ownership and option enumeration", () => {
  test("A role-less popup is accepted once it holds options, with a search through shadow roots above the trigger", async () => {
    mount(`<div id="host"></div><ul id="menu-1" hidden></ul>`);
    const root = (document.getElementById("host") as HTMLElement).attachShadow({ mode: "open" });
    root.innerHTML = `<label id="l" for="t">Level</label><button id="t" type="button" role="combobox" aria-haspopup="listbox" aria-labelledby="l" aria-controls="menu-1">Select one</button>`;
    const trigger = root.getElementById("t") as HTMLElement;
    const menu = document.getElementById("menu-1") as HTMLElement;
    menu.innerHTML = `<li class="x__option">Junior</li><li class="x__option">Senior</li><li class="x__option">Cannot find your level, fill in manually</li>`;
    trigger.addEventListener("click", () => (menu.hidden = false));
    menu.querySelectorAll("li").forEach((li) =>
      li.addEventListener("click", () => {
        trigger.textContent = li.textContent;
        menu.hidden = true;
      }),
    );
    const { writeListbox } = await import("../src/listbox");
    const out = await writeListbox(trigger, { answers: ["Senior"], label: "Level" }, ctx());
    expect(out).toMatchObject({ ok: true, optionLabel: "Senior" });
    expect(trigger.textContent).toBe("Senior");
  });

  test("A combobox wrapper that holds a hidden input before its text box is bound to the popup the text box names, never to a neighbour's open list", async () => {
    const { popupFor } = await import("../src/popup");
    mount(`<label id="t-l">Team</label><div id="t" role="combobox" aria-haspopup="listbox" aria-expanded="true" aria-labelledby="t-l"><input type="hidden" name="team" value=""><input id="t-in" aria-autocomplete="list" aria-controls="t-list"></div>
      <ul id="t-list" role="listbox"><li role="option">Platform</li><li role="option">Payments</li></ul>
      <ul id="other" role="listbox"><li role="option">Platform (legacy)</li></ul>`);
    const found = popupFor(byId("t"), document);
    expect(found.state === "found" ? found.popup.id : found.state).toBe("t-list");
    byId("t-list").innerHTML = "";
    // Its own popup is named and empty: the answer is to wait, not to use the list that is open.
    expect(popupFor(byId("t"), document).state).toBe("waiting");
  });

  test("State suffixes and opaque value ids are stripped from an option label", async () => {
    const { cleanOptionLabel } = await import("../src/popup");
    expect(cleanOptionLabel("Texas, not checked")).toBe("Texas");
    expect(cleanOptionLabel("Texas currently selected")).toBe("Texas");
    expect(cleanOptionLabel("Skill, press delete to clear value")).toBe("Skill");
    expect(cleanOptionLabel("Master's Degree 6b1f2c3d-1111-2222-3333-444455556666")).toBe("Master's Degree");
  });

  test("A state suffix is a whole trailing phrase after the label: a label that is itself such a phrase, or only ends in its letters, is kept whole", async () => {
    const { cleanOptionLabel, optionsOf } = await import("../src/popup");
    expect(cleanOptionLabel("Not Selected")).toBe("Not Selected");
    expect(cleanOptionLabel("Unselected")).toBe("Unselected");
    expect(cleanOptionLabel("Selected")).toBe("Selected");
    expect(cleanOptionLabel("Not checked")).toBe("Not checked");
    expect(cleanOptionLabel("Texas, not selected")).toBe("Texas");
    expect(cleanOptionLabel("Texas selected")).toBe("Texas");
    expect(cleanOptionLabel("Not Selected, currently selected")).toBe("Not Selected");
    mount(`<ul id="l" role="listbox"><li role="option">Selected</li><li role="option">Not Selected</li></ul>`);
    expect(optionsOf(byId("l")).map((r) => r.label)).toEqual(["Selected", "Not Selected"]);
  });

  test("Only a row that tells the person to enter something by hand, or says it cannot be found, is the escape row: an option that merely holds the word manual is an option", async () => {
    const { isEscapeRow, optionsOf } = await import("../src/popup");
    for (const escape of ["Enter manually", "Fill in manually", "Add it manually", "Type address manually", "Enter manual address", "Manually enter your school", "Can't find your school?", "Couldn’t find it"]) expect(isEscapeRow(escape), escape).toBe(true);
    for (const option of ["Manual", "Manual Testing", "Manual QA", "Manual Data Entry", "Automated and manual testing", "Manually operated machinery"]) expect(isEscapeRow(option), option).toBe(false);
    mount(`<ul id="l" role="listbox"><li role="option">Automation</li><li role="option">Manual Testing</li><li role="option">Can't find it? Enter manually</li></ul>`);
    expect(optionsOf(byId("l")).map((r) => r.label)).toEqual(["Automation", "Manual Testing"]);
  });

  test("An option that only responds to focus with Enter is committed by the last rung of the ladder", async () => {
    mount("");
    const m = mountMenu({ id: "f", label: "Level", options: ["Junior", "Senior"], clickable: false, keyboard: false });
    m.list.querySelectorAll("li").forEach((li) =>
      li.addEventListener("keydown", (e) => {
        if ((e as KeyboardEvent).key === "Enter") {
          m.trigger.textContent = li.textContent;
          m.close();
        }
      }),
    );
    const r = await write("f", "Senior");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Senior" });
  });
});

describe("Keyboard commit bounds", () => {
  /** ArrowDown presses that reached an open list (a press that only opens it is not part of the commit). */
  const arrowDowns = (m: ReturnType<typeof mountMenu>) => {
    const seen: string[] = [];
    m.trigger.addEventListener("keydown", (e) => (e as KeyboardEvent).key === "ArrowDown" && !m.list.hidden && seen.push("down"));
    return seen;
  };

  test("It gives up after 20 ArrowDown presses when the option is further down the list", async () => {
    mount("");
    const options = Array.from({ length: 30 }, (_, i) => `Choice ${String(i).padStart(2, "0")}`);
    const m = mountMenu({ id: "kb", label: "Pick", options, openOn: "click", clickable: false });
    const downs = arrowDowns(m);
    const r = await write("kb", "Choice 25");
    expect(r.outcome).toBe("failed");
    expect(m.shown()).toBe("Select one");
    expect(downs).toHaveLength(20);
  });

  test("It bails after two presses that did not move the active option", async () => {
    mount("");
    const m = mountMenu({ id: "kb", label: "Pick", options: ["Alpha", "Beta", "Gamma"], openOn: "click", clickable: false, keyboard: false });
    const downs = arrowDowns(m);
    expect((await write("kb", "Gamma")).outcome).toBe("failed");
    expect(downs).toHaveLength(2);
  });
});

describe("Late answers and inspect", () => {
  test("answer() writes a custom listbox the host resolved after the fill, and inspect() reads it back without changing the page", async () => {
    mount(PAD);
    const m = mountMenu({ id: "m", label: "Preference", options: ["Alpha", "Beta"] });
    const engine = makeEngine(fakeHost());
    await runFill(engine);
    expect(m.shown()).toBe("Select one");
    const before = document.body.innerHTML;
    const rows = engine.inspect();
    expect(document.body.innerHTML).toBe(before);
    expect(rows.find((r) => r.label === "Preference")).toMatchObject({ kind: "custom-listbox" });
    const selector = infoOf("m").selector;
    const reply = await engine.answer([{ selector, outcome: "filled", value: "Beta" }]);
    expect(reply.results[0]).toMatchObject({ outcome: "filled", optionLabel: "Beta" });
    expect(m.shown()).toBe("Beta");
    expect(engine.inspect().find((r) => r.label === "Preference")?.value).toBe("Beta");
  });
});

describe("Address search that finds no street", () => {
  /** Teamtailor's location box: a combobox input whose suggestions come from a places search. */
  function mountPlaceSearch(label: string): { queries: string[] } {
    mount(`${PAD}<div id="id_1"><label for="candidate_location">${label}<sup aria-hidden="true">*</sup><span class="sr-only">Required</span></label>
      <input name="candidate[location][query]" autocomplete="street-address" role="combobox" aria-autocomplete="list" aria-expanded="false" aria-controls="id_1-location-listbox" aria-haspopup="listbox" value="" maxlength="200" placeholder="Start typing your address" type="text" id="candidate_location">
      <div role="listbox" id="id_1-location-listbox" aria-hidden="true" style="display: none;"></div></div>`);
    const input = byId("candidate_location");
    const list = byId<HTMLElement>("id_1-location-listbox");
    const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set as (v: string) => void;
    const queries: string[] = [];
    input.addEventListener("input", () => {
      queries.push(input.value);
      // The fictional street matches places elsewhere; only the city search finds the applicant's.
      const found = input.value.toLowerCase().startsWith("durham") ? ["Durham, NC, USA", "Durham, UK"] : ["Example Tree, Al Kharj 16256, Saudi Arabia", "Poor Example, Escalon CA 95320-1751, United States"];
      list.style.display = "flex";
      list.innerHTML = found.map((d, i) => `<button type="button" role="option" id="id_1-location-listbox-option-${i}" aria-selected="false"><span>${d}</span></button>`).join("");
      list.querySelectorAll("button").forEach((b) =>
        b.addEventListener("click", () => {
          set.call(input, b.textContent ?? "");
          list.style.display = "none";
          list.innerHTML = "";
        }),
      );
    });
    return { queries };
  }
  const where = { address: { city: "Durham", state: "NC", postalCode: "27701" } };

  test("A box that is the whole address falls back to the applicant's city and state, and commits only a suggestion that names both", async () => {
    const p = mountPlaceSearch("Address");
    const r = await write("candidate_location", "1 Example Street", where);
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Durham, NC, USA" });
    expect(byId("candidate_location").value).toBe("Durham, NC, USA");
    expect(p.queries.at(-1)).toBe("Durham, NC");
  });

  test("An Address Line 1 box is a street only: no city search stands in for it and nothing is committed", async () => {
    const p = mountPlaceSearch("Address Line 1");
    const r = await write("candidate_location", "1 Example Street", where);
    expect(r).toMatchObject({ outcome: "abstained", reason: "option-not-found" });
    expect(p.queries.some((q) => q.startsWith("Durham"))).toBe(false);
  });

  /** Paylocity's street box: a combobox input whose list mounts only when the search finds something. */
  const street = `<div data-automation-id="public-site-address-address-1-wrapper"><label for="public-site-address-address-1"><span>Address Line 1</span>
    <div><div style="border:0;clip:rect(0,0,0,0);height:1px;overflow:hidden;position:absolute;width:1px"><div aria-atomic="true" aria-live="polite" id="public-site-address-address-1-status-a" role="status"></div></div>
    <input aria-autocomplete="list" aria-controls="public-site-address-address-1-autocomplete-list" aria-expanded="false" autocomplete="off" role="combobox" id="public-site-address-address-1" maxlength="50" placeholder="" required type="text" value="" aria-describedby="public-site-address-address-1-error-message" aria-invalid="true"></div></label>
    <span id="public-site-address-address-1-error-message">Address is required</span></div>${PAD}`;

  test("A street box whose search lists nothing for the street keeps the street typed", async () => {
    mount(street);
    const r = await write("public-site-address-address-1", "1 Example Street", where);
    expect(r).toMatchObject({ outcome: "filled", value: "1 Example Street" });
    expect(byId("public-site-address-address-1").value).toBe("1 Example Street");
  });

  test("A street the page throws away when the box loses focus is not a held answer", async () => {
    mount(street);
    byId("public-site-address-address-1").addEventListener("focusout", (e) => {
      (e.target as HTMLInputElement).value = "";
    });
    const r = await write("public-site-address-address-1", "1 Example Street", where);
    expect(r.outcome).not.toBe("filled");
    expect(byId("public-site-address-address-1").value).toBe("");
  });
});

describe("Reading a long option list", () => {
  test("Squarespace's school list: 2,557 options are read once each, with a row inside another dropped, in well under a second", async () => {
    const { optionsOf } = await import("../src/popup");
    mount("");
    const list = document.createElement("div");
    list.setAttribute("role", "listbox");
    const names = Array.from({ length: 2557 }, (_, i) => `School ${i}`);
    list.innerHTML = names.map((n) => `<div role="option">${n}</div>`).join("") + `<div role="option" id="wrap"><span role="option">Carver University</span></div>`;
    document.body.append(list);
    // Processor time, not the wall clock: a loaded machine stretches the clock but not the work.
    const t0 = process.cpuUsage();
    const rows = optionsOf(list);
    const used = process.cpuUsage(t0);
    expect(rows.map((r) => r.label)).toEqual([...names, "Carver University"]);
    expect(rows.some((r) => r.el.id === "wrap")).toBe(false);
    expect((used.user + used.system) / 1000).toBeLessThan(1000);
  });
});
