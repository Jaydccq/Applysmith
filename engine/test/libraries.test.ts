// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Drivers for widget libraries; Widget drivers for custom controls (calendar picker);
// Committed value read-back.
import { describe, expect, test } from "vitest";
import { PAD, ctx, fakeHost, infoOf, makeEngine, mount, noEdits, rowFor, runFill, summaryOf, write, uninstall } from "./helpers";
import { mountDayPicker, mountReactSelect, mountSelect2, mountTinymce } from "./library-fixtures";
import { applyValue } from "../src/apply";
import { writeDateSegments } from "../src/calendar";
import type { Win } from "../src/dom";
import { MainWorld } from "../src/mainworld-client";
import { installHelper } from "../src/mainworld/helper";

describe("Select2", () => {
  test("The trigger is opened with the full pointer sequence, a typed search is applied to the search box, a result is activated and the native select holds the chosen option", async () => {
    mount("");
    const s = mountSelect2({ id: "school", label: "School", catalog: ["Oakmont University", "Example University", "Example College"] });
    const seen: string[] = [];
    for (const t of ["pointerdown", "mousedown", "pointerup", "mouseup", "click"]) s.selection.addEventListener(t, () => seen.push(t));
    const r = await write("school", "Example University");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Example University" });
    expect(s.select.value).toBe("Example University");
    expect(s.searches.at(-1)).toBe("Example University");
    expect(seen).toEqual(["pointerdown", "mousedown", "pointerup", "mouseup", "click"]);
    expect(s.isOpen()).toBe(false);
  });

  test("Select2 with several widgets: only that widget's menu is read and the other widget is unchanged", async () => {
    mount("");
    const skills = mountSelect2({ id: "skills", label: "Skills", catalog: ["Example University", "Go", "Rust"], initial: ["Go", "Rust"] });
    const school = mountSelect2({ id: "school", label: "School", catalog: ["Example University", "Oakmont University"] });
    skills.selection.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    expect(skills.isOpen()).toBe(true);
    const r = await write("school", "Example University");
    expect(r.outcome).toBe("filled");
    expect(school.select.value).toBe("Example University");
    expect(skills.select.value).toBe("");
  });

  test("A multi-select behind a header the page marks as a dialog is searched in its inline box and the pick becomes a chip (Two Sigma on Avature)", async () => {
    mount(`<div class="globalHeader__menu" role="dialog" aria-modal="true">Menu</div>`);
    const s = mountSelect2({ id: "race", label: "Race/Ethnicity", catalog: ["Black", "East Asian", "White", "Prefer not to say"], initial: ["Black", "East Asian", "White"], multiple: true });
    const r = await write("race", "Prefer not to say");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Prefer not to say" });
    expect(Array.from(s.select.selectedOptions).map((o) => o.text)).toEqual(["Prefer not to say"]);
    expect(s.searches.at(-1)).toBe("Prefer not to say");
    expect(s.isOpen()).toBe(false);
  });

  test("A multi-select that already holds a choice takes another one, and several answers each become a chip", async () => {
    mount("");
    const chosen = (sel: HTMLSelectElement) => Array.from(sel.selectedOptions).map((o) => o.text);
    const s = mountSelect2({ id: "race", label: "Race/Ethnicity", catalog: ["Black", "East Asian", "White"], initial: ["Black", "East Asian", "White"], multiple: true });
    expect((await write("race", "Black")).outcome).toBe("filled");
    expect(await write("race", "White", { replaceDefault: true })).toMatchObject({ outcome: "filled", optionLabel: "White" });
    expect(chosen(s.select)).toEqual(["Black", "White"]);
    mount("");
    const m = mountSelect2({ id: "skills", label: "Skills", catalog: ["Go", "Rust", "Zig"], initial: ["Go", "Rust", "Zig"], multiple: true });
    expect(await write("skills", "Rust", { optionLabels: ["Rust", "Go", "Cobol"] })).toMatchObject({ outcome: "filled", optionLabel: "Rust, Go" });
    expect(chosen(m.select)).toEqual(["Rust", "Go"]);
    expect(m.isOpen()).toBe(false);
  });

  test("An empty search-backed Select2 is asked with the rows it opens with, and its inline search box is not a control of its own", async () => {
    mount(`<div class="globalHeader__menu" role="dialog" aria-modal="true">Menu</div>${PAD}`);
    // Avature's list ends its first page in an infinite-scroll row, which is no search in flight.
    const s = mountSelect2({ id: "race", label: "Race/Ethnicity", catalog: ["Black", "East Asian", "White", "Prefer not to say"], initial: ["Black", "East Asian", "White"], multiple: true, paged: true });
    const host = fakeHost({ "race/ethnicity": { outcome: "filled", value: "East Asian" } });
    const sum = summaryOf(await runFill(makeEngine(host)));
    const asked = host.matchRequests.flatMap((r) => r.questions).find((q) => q.text === "Race/Ethnicity");
    expect(asked?.options).toEqual(["Black", "East Asian", "White"]);
    expect(Array.from(s.select.selectedOptions).map((o) => o.text)).toEqual(["East Asian"]);
    expect(sum.rows.filter((r) => r.label === "Race/Ethnicity").map((r) => r.outcome)).toEqual(["filled"]);
    expect(s.isOpen()).toBe(false);
  });

  test("Exact normalized text, else the single prefix match, else nothing: never the first row", async () => {
    mount("");
    mountSelect2({ id: "school", label: "School", catalog: ["Oakmont Law", "Oakmont University", "Oakmont"] });
    expect((await write("school", "Oakmont")).optionLabel).toBe("Oakmont");
    mount("");
    const t = mountSelect2({ id: "school", label: "School", catalog: ["Oakmont Law", "Oakmont University"] });
    const r = await write("school", "Oakmont");
    expect(r).toMatchObject({ outcome: "failed", reason: "option-not-found" });
    expect(t.select.value).toBe("");
  });

  // Lenovo's Avature education row: the profile's degree and area of study are not catalog rows.
  const DEGREES = ["Associate's Degree", "Bachelor's Degree", "Master's Degree", "Doctorate", "High School"];
  const MAJORS = ["Accounting", "Applied Physics", "Computer Science", "Mathematics", "Mathematics Education"];

  test("A degree the catalog spells differently is picked from the rows the menu opens with", async () => {
    mount("");
    const s = mountSelect2({ id: "deg", label: "Degree", catalog: DEGREES, initial: DEGREES, delayMs: 5 });
    expect(await write("deg", "Bachelor of Science (B.S)")).toMatchObject({ outcome: "filled", optionLabel: "Bachelor's Degree" });
    expect(s.select.value).toBe("Bachelor's Degree");
    expect(s.searches).toEqual([]);
  });

  test("A catalog asked for the level of education is a degree catalog, as a listbox with that label is, and a certificate or a high school is searched by its level word", async () => {
    mount("");
    const s = mountSelect2({ id: "edu", label: "Highest level of education", catalog: DEGREES, initial: DEGREES, delayMs: 5 });
    expect(await write("edu", "Bachelor of Science (B.S)")).toMatchObject({ outcome: "filled", optionLabel: "Bachelor's Degree" });
    expect(s.select.value).toBe("Bachelor's Degree");
    const { select2Terms } = await import("../src/select2");
    expect(select2Terms("Education level", "Master of Science")).toEqual(["Master of Science", "Master"]);
    expect(select2Terms("Degree", "Certificate in Data Science")).toEqual(["Certificate in Data Science", "Certificate"]);
    expect(select2Terms("Degree", "High School Diploma")).toEqual(["High School Diploma", "High School"]);
  });

  test("An area of study is searched by its words and commits the broader field it names, and nothing when no row is a truthful match", async () => {
    mount("");
    const s = mountSelect2({ id: "major", label: "Area of Study", catalog: MAJORS, initial: ["Accounting", "Agriculture"], delayMs: 5 });
    expect(await write("major", "Applied Mathematics")).toMatchObject({ outcome: "filled", optionLabel: "Mathematics" });
    expect(s.select.value).toBe("Mathematics");
    mount("");
    const t = mountSelect2({ id: "major", label: "Area of Study", catalog: ["Applied Physics", "Physics"], initial: ["Accounting"], delayMs: 20 });
    expect(await write("major", "Applied Mathematics")).toMatchObject({ outcome: "failed", reason: "option-not-found" });
    expect(t.select.value).toBe("");
  });

  test("An ajax catalog that keeps the old rows up through its debounce is waited for, and its rows are read only after they change", async () => {
    mount("");
    const s = mountSelect2({
      id: "school",
      label: "School",
      catalog: ["Carver University", "University of Westbridge", "University of Westbridge Harbor Campus"],
      initial: ["(School not listed)", "Aachen Fachhochschule", "Aachen RWTH"],
      debounceMs: 30,
      delayMs: 10,
    });
    expect(await write("school", "University of Westbridge")).toMatchObject({ outcome: "filled", optionLabel: "University of Westbridge" });
    expect(s.select.value).toBe("University of Westbridge");
  });

  test("A row the click guard refuses is not clicked and the menu is closed", async () => {
    mount("");
    const s = mountSelect2({ id: "next", label: "Next step", catalog: ["Submit application"], initial: ["Submit application"] });
    expect(await write("next", "Submit application")).toMatchObject({ outcome: "failed", reason: "unsafe-click" });
    expect(s.select.value).toBe("");
    expect(s.isOpen()).toBe(false);
  });

  test("An already committed widget is not reopened", async () => {
    mount("");
    const s = mountSelect2({ id: "school", label: "School", catalog: ["Example University"] });
    await write("school", "Example University");
    let opened = 0;
    s.selection.addEventListener("mousedown", () => opened++);
    const r = await write("school", "Example University");
    expect(r.outcome).toBe("prefilled");
    expect(opened).toBe(0);
  });
});

describe("TinyMCE", () => {
  test("A required textarea the editor hides is one long-text question reached through the editor; its menu bar and toolbar are no controls (Two Sigma on Avature)", async () => {
    // Avature's header menu is an always-shown modal dialog, and the editor marks its textarea aria-hidden.
    mount(`<div class="globalHeader__menu" role="dialog" aria-modal="true">Menu</div>${PAD}`);
    mountTinymce({ id: "5502-11", label: "Please list any awards or honors you have received." });
    const host = fakeHost();
    const sum = summaryOf(await runFill(makeEngine(host)));
    const asked = host.matchRequests.flatMap((r) => r.questions).filter((q) => q.selector !== '[id="pad"]');
    expect(asked.map((q) => [q.text, q.inputType, q.required])).toEqual([["Please list any awards or honors you have received.", "long_text", true]]);
    expect(sum.rows.filter((r) => r.selector !== '[id="pad"]').map((r) => [r.kind, r.outcome])).toEqual([["textarea", "abstained"]]);
    expect(sum.counts.requiredUnfilled).toBe(1);
  });

  test("Without the main-world helper, or with no editor object behind the textarea, nothing is written: the hidden textarea alone is never filled", async () => {
    mount(PAD);
    const area = mountTinymce({ id: "awards", label: "Awards" });
    expect(await write("awards", "Dean's list")).toMatchObject({ outcome: "failed", reason: "binding-unavailable" });
    const win = window as unknown as Win;
    installHelper(win, "tinymce-nonce", "h1");
    const main = new MainWorld(win, "tinymce-nonce");
    const r = await applyValue(infoOf("awards"), { value: "Dean's list", replaceDefault: false }, { ctx: { ...ctx(), main }, written: new Map(), mode: "replace", acceptLegalDeclarations: false, edits: noEdits() });
    main.dispose();
    uninstall();
    expect(r).toMatchObject({ outcome: "failed", reason: "driver-unavailable" });
    expect(area.value).toBe("");
  });

  test("A textarea the editor saved an answer into reads as the answer's text and is held; one it saved only empty paragraphs into is still a gap", async () => {
    mount(PAD);
    mountTinymce({ id: "awards", label: "Awards", saved: "<p>Dean&#39;s list &amp; prize</p><p>Chess club</p>" });
    mountTinymce({ id: "groups", label: "Student groups", saved: "<p>&nbsp;</p><p>&nbsp;</p>" });
    const sum = summaryOf(await runFill(makeEngine(fakeHost())));
    expect(rowFor(sum, "Awards")).toMatchObject({ kind: "textarea", outcome: "prefilled", value: "Dean's list & prize Chess club" });
    expect(rowFor(sum, "Student groups")).toMatchObject({ kind: "textarea", outcome: "abstained" });
    expect(sum.counts.requiredUnfilled).toBe(1);
  });
});

describe("React-Select", () => {
  test("The menu is opened by the control, found by the id derived from the input and an option is resolved strictly by exact normalized label", async () => {
    mount("");
    const rs = mountReactSelect({ inputId: "react-select-2-input", label: "Eligible?", options: ["Yes, with conditions", "Yes", "No"] });
    const r = await write("react-select-2-input", "Yes");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Yes" });
    expect(rs.value()).toEqual(["Yes"]);
    expect(rs.clicked).toEqual(["Yes"]);
  });

  test("A country a long list spells another way is the row that is exactly another of its names (Scopely's Greenhouse list), and no near miss", async () => {
    mount("");
    const catalog = [...Array.from({ length: 120 }, (_, i) => `Country ${String(i).padStart(3, "0")}`), "United Kingdom", "United States of America", "United States Minor Outlying Islands"];
    const rs = mountReactSelect({ inputId: "question_1", label: "Country", options: catalog, firstPage: 25 });
    expect(await write("question_1", "United States")).toMatchObject({ outcome: "filled", optionLabel: "United States of America" });
    expect(rs.value()).toEqual(["United States of America"]);
    mount("");
    const none = mountReactSelect({ inputId: "question_2", label: "Country", options: ["United States Minor Outlying Islands", "Canada"], firstPage: 25 });
    expect((await write("question_2", "United States")).outcome).not.toBe("filled");
    expect(none.value()).toEqual([]);
  });

  test("A school catalog that lists only its first page is searched by typing, and its list is never walked", async () => {
    mount("");
    const catalog = [...Array.from({ length: 60 }, (_, i) => `Aalto Institute ${String(i).padStart(3, "0")}`), "Carver University"];
    const rs = mountReactSelect({ inputId: "school--0", label: "School", options: catalog, firstPage: 25 });
    const r = await write("school--0", "Carver University");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Carver University" });
    expect(rs.value()).toEqual(["Carver University"]);
    expect(rs.keys.filter((k) => k === "ArrowDown").length).toBeLessThanOrEqual(1);
  });

  test("A discipline the list lacks is searched by its endings and takes the one row naming the broader field (ASM's Greenhouse list), and nothing when no row is a truthful match", async () => {
    mount("");
    const catalog = [...Array.from({ length: 30 }, (_, i) => `Accounting ${String(i).padStart(2, "0")}`), "Computer Engineering", "Mathematics", "Mathematics Education"];
    const rs = mountReactSelect({ inputId: "discipline--1", label: "Discipline", options: catalog, firstPage: 25 });
    expect(await write("discipline--1", "Applied Mathematics")).toMatchObject({ outcome: "filled", optionLabel: "Mathematics" });
    expect(rs.value()).toEqual(["Mathematics"]);
    mount("");
    const none = mountReactSelect({ inputId: "discipline--2", label: "Discipline", options: ["Applied Physics", "Physics"], firstPage: 25 });
    expect((await write("discipline--2", "Applied Mathematics")).outcome).toBe("failed");
    expect(none.value()).toEqual([]);
  });

  test("A write that runs out of its budget stops: it never picks its option after the run has moved on, and a pick that landed in time is the row's", async () => {
    mount("");
    const catalog = [...Array.from({ length: 60 }, (_, i) => `Aalto Institute ${String(i).padStart(3, "0")}`), "Carver University"];
    const rs = mountReactSelect({ inputId: "school--0", label: "School", options: catalog, firstPage: 25 });
    const r = await write("school--0", "Carver University", {}, "replace", new Map(), false, { fieldBudgetMs: 4 });
    const picked = rs.clicked.length;
    await new Promise((done) => setTimeout(done, 300));
    expect(rs.clicked.length).toBe(picked);
    expect(r.outcome).toBe(rs.value().length ? "filled" : "failed");
  });

  test("A search text is no answer: a list whose option does not take the pick is a failed row, and the box gets back what it held", async () => {
    mount("");
    // A catalog lists its first page only, so the answer is searched for by typing it.
    const catalog = [...Array.from({ length: 60 }, (_, i) => `Aalto Institute ${String(i).padStart(3, "0")}`), "Carver University"];
    const rs = mountReactSelect({ inputId: "school--0", label: "School", options: catalog, firstPage: 25, clickable: false });
    const r = await write("school--0", "Carver University");
    expect(rs.value()).toEqual([]);
    expect(r.outcome).toBe("failed");
    expect(rs.input.value).toBe("");
  });

  test("Greenhouse strict option: a menu with Yes and Yes with conditions and the answer Yes chooses exactly Yes, and an answer with no exact label commits nothing", async () => {
    mount("");
    const rs = mountReactSelect({ inputId: "question_123", label: "Authorized?", options: ["Yes, with conditions", "No"] });
    const r = await write("question_123", "Yes");
    expect(r.outcome).toBe("failed");
    expect(rs.value()).toEqual([]);
  });

  test("A portalled menu is located by the id derived from the trigger and a neighbour's open menu is never used", async () => {
    mount("");
    const other = mountReactSelect({ inputId: "react-select-1-input", label: "Other", options: ["Yes", "No"], portal: true });
    other.control.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    const rs = mountReactSelect({ inputId: "react-select-2-input", label: "Mine", options: ["Yes", "No"], portal: true });
    const r = await write("react-select-2-input", "No");
    expect(r.outcome).toBe("filled");
    expect(rs.value()).toEqual(["No"]);
    expect(other.value()).toEqual([]);
  });

  test("React-Select limit: a multi-value control that says select up to 2 gets exactly the first two chips of four labels", async () => {
    mount("");
    const rs = mountReactSelect({ inputId: "react-select-4-input", label: "Skills", multi: true, hint: "Select up to 2", options: ["Go", "Rust", "C", "Zig"] });
    const r = await write("react-select-4-input", "Go", { optionLabels: ["Go", "Rust", "C", "Zig"] });
    expect(r.outcome).toBe("filled");
    expect(rs.value()).toEqual(["Go", "Rust"]);
  });

  test("Chips whose labels hold a comma are read as held: a second write of the same answers picks nothing again", async () => {
    mount("");
    const rs = mountReactSelect({ inputId: "react-select-6-input", label: "Preferred offices", multi: true, options: ["Atlanta, GA", "Chicago, IL", "Dallas, TX"] });
    const answers = { optionLabels: ["Atlanta, GA", "Chicago, IL"] };
    expect((await write("react-select-6-input", "Atlanta, GA", answers)).outcome).toBe("filled");
    const picks = rs.clicked.length;
    await write("react-select-6-input", "Atlanta, GA", answers);
    expect(rs.clicked).toHaveLength(picks);
    expect(rs.value()).toEqual(["Atlanta, GA", "Chicago, IL"]);
  });
});

describe("Calendar picker", () => {
  test("A button that opens a day grid is driven through its month and day controls and the trigger text reads the ISO date", async () => {
    mount("");
    const dp = mountDayPicker({ id: "avail", label: "Available from", start: { y: 2026, m: 10 } });
    const r = await write("avail", "2027-02-14");
    expect(r).toMatchObject({ outcome: "filled", value: "2027-02-14" });
    expect(dp.trigger.textContent).toBe("2027-02-14");
    expect(dp.stats.navClicks).toBe(4);
    expect(dp.isOpen()).toBe(false);
  });

  test("Walking back to an earlier month uses the previous-month control", async () => {
    mount("");
    const dp = mountDayPicker({ id: "avail", label: "Available from", start: { y: 2026, m: 10 } });
    const r = await write("avail", "2026-08-03");
    expect(r.outcome).toBe("filled");
    expect(dp.stats.navClicks).toBe(2);
  });

  test("A grid whose caption does not read as the wanted month and year gets no day clicked: the date stays unset and the panel is closed", async () => {
    mount("");
    const numeric = mountDayPicker({ id: "avail", label: "Available from", start: { y: 2026, m: 10 }, caption: (y, m) => `${m}/${y}` });
    expect(await write("avail", "2027-02-14")).toMatchObject({ outcome: "failed", reason: "write-not-committed" });
    expect(numeric.trigger.textContent).toBe("Select a date");
    expect(numeric.isOpen()).toBe(false);
    // A month further away than the walk goes is never reached, so no day of the month it stopped on is taken.
    mount("");
    const far = mountDayPicker({ id: "avail", label: "Available from", start: { y: 2026, m: 10 } });
    expect(await write("avail", "2035-02-14")).toMatchObject({ outcome: "failed", reason: "write-not-committed" });
    expect(far.trigger.textContent).toBe("Select a date");
    expect(far.isOpen()).toBe(false);
  });

  test("A caption that abbreviates the month is read as that month", async () => {
    mount("");
    const short = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
    const dp = mountDayPicker({ id: "avail", label: "Available from", start: { y: 2026, m: 10 }, caption: (y, m) => `${short[m - 1]} ${y}` });
    expect(await write("avail", "2027-02-14")).toMatchObject({ outcome: "filled", value: "2027-02-14" });
    expect(dp.stats.navClicks).toBe(4);
  });

  test("A grid that offers no way to the month, or whose day is disabled, is closed again with no date picked", async () => {
    mount("");
    const stuck = mountDayPicker({ id: "avail", label: "Available from", start: { y: 2026, m: 10 }, noNext: true });
    expect(await write("avail", "2027-02-14")).toMatchObject({ outcome: "failed", reason: "write-not-committed" });
    expect(stuck.isOpen()).toBe(false);
    mount("");
    const taken = mountDayPicker({ id: "avail", label: "Available from", start: { y: 2026, m: 10 }, disabledDay: 14 });
    expect(await write("avail", "2026-10-14")).toMatchObject({ outcome: "failed", reason: "option-not-found" });
    expect(taken.isOpen()).toBe(false);
    expect(taken.trigger.textContent).toBe("Select a date");
  });

  test("A picker whose panel never opens fails safe", async () => {
    mount("");
    const dp = mountDayPicker({ id: "avail", label: "Available from", start: { y: 2026, m: 10 } });
    dp.trigger.replaceWith(dp.trigger.cloneNode(true));
    const r = await write("avail", "2026-11-02");
    expect(r).toMatchObject({ outcome: "failed", reason: "listbox-not-opened" });
  });

  test("A value that is not a date is refused", async () => {
    mount("");
    mountDayPicker({ id: "avail", label: "Available from", start: { y: 2026, m: 10 } });
    expect((await write("avail", "next spring")).reason).toBe("format-not-accepted");
  });

  test("A date shown as separate month, day and year spinbuttons is typed segment by segment", async () => {
    mount(`<div role="group">${["Month", "Day", "Year"].map((part) => `<input id="q--dateSection${part}-input" role="spinbutton" aria-label="${part}">`).join("")}</div>`);
    const parts = Array.from(document.querySelectorAll<HTMLInputElement>("input"));
    const keys: string[] = [];
    parts[0]?.addEventListener("keydown", (e) => keys.push((e as KeyboardEvent).key));
    const out = await writeDateSegments(parts, "2027-08-02", ctx());
    expect(out.ok).toBe(true);
    expect(parts.map((p) => p.value)).toEqual(["08", "02", "2027"]);
    expect(keys).toEqual(["0", "8"]);
  });
});
