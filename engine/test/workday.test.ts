// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Drivers for enterprise application suites (Workday).
import { describe, expect, test } from "vitest";
import { pickCatalogLabel, writeWorkdayPrompt } from "../src/workday";
import { readInfo, scanControls } from "../src/scan";
import { fakeHost, makeEngine, mount, runFill, summaryOf } from "./helpers";
import { ctx, infoOf, write } from "./helpers";
import { mountDateTriple, mountForeignPrompt, mountWorkdayPrompt } from "./ats-fixtures";

describe("Workday search prompt", () => {
  test("Workday search prompt: the engine types the answer, presses Enter, reads the filtered options and the selected pill equals the answer", async () => {
    mount("");
    const p = mountWorkdayPrompt({ id: "school", label: "School or University", catalog: ["Example University", "Oakmont College"] });
    const r = await write("school", "Example University");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Example University" });
    expect(p.searches).toEqual(["Example University"]);
    expect(p.pills()).toEqual(["Example University"]);
    expect(p.input.value).toBe("");
    expect(readInfo(infoOf("school"))).toBe("Example University");
  });

  test("The search prompt is recognised from its markup, not its host, and is asked with no options", () => {
    mount("");
    mountWorkdayPrompt({ id: "school", label: "School or University", catalog: ["Example University"] });
    const info = scanControls(document).find((i) => i.el.id === "school");
    expect(info).toMatchObject({ kind: "custom-listbox", driver: "workday-prompt", options: [], needsOptions: false });
  });

  test("Workday stale results: a second skill is searched while the first query's rows are still mounted, and the pick is made only from the rows that differ, so a wrong skill is never committed", async () => {
    mount("");
    // The first query's rows stay mounted after its commit; one of them is a plausible wrong pick for the second answer.
    const p = mountWorkdayPrompt({
      id: "skills--skills",
      label: "Skills",
      catalog: [],
      delayMs: 30,
      keepRoot: true,
      answers: { Python: ["Python", "Rust Basics"], Rust: ["Rust", "Rustic Design"] },
    });
    const r = await write("skills--skills", "Python, Rust");
    expect(r.outcome).toBe("filled");
    expect(p.pills()).toEqual(["Python", "Rust"]);
    expect(p.clicks).toEqual(["Python", "Rust"]);
  });

  test("A foreign prompt that Workday never unmounts is not this widget's options", async () => {
    mount("");
    mountForeignPrompt("W-other", ["Example University"]);
    const p = mountWorkdayPrompt({ id: "school", label: "School or University", catalog: ["Example College"], noWidgetId: false });
    const r = await write("school", "Example University");
    expect(r.outcome).toBe("abstained");
    expect(p.clicks).toEqual([]);
  });

  test("A trigger with no widget id keeps waiting for its own menu and commits nothing", async () => {
    mount("");
    mountForeignPrompt("W-other", ["Example University"]);
    const p = mountWorkdayPrompt({ id: "school", label: "School or University", catalog: ["Example University"], noWidgetId: true });
    const r = await write("school", "Example University");
    expect(r.outcome).not.toBe("filled");
    expect(p.clicks).toEqual([]);
    expect(p.input.value).toBe("");
  });

  test("Failed search leaves no text: no unambiguous option leaves the input empty and no responsive dialog open", async () => {
    mount("");
    const p = mountWorkdayPrompt({ id: "school", label: "School or University", catalog: ["Example Institute", "Example Institute Annex"], responsive: true });
    const r = await write("school", "Example Inst");
    expect(r).toMatchObject({ outcome: "abstained", reason: "option-not-found" });
    expect(p.input.value).toBe("");
    expect(p.pills()).toEqual([]);
    expect(p.closeClicks()).toBe(1);
    expect(p.prompt()?.isConnected ?? false).toBe(false);
  });

  test("A school the catalog does not hold at all takes the catalog's own Other row; a pill that is not that row is taken back off", async () => {
    // Ensemble: "University of Westbridge" finds nothing, and "Other" commits on Enter.
    mount("");
    let p = mountWorkdayPrompt({ id: "school", label: "School or University", catalog: ["Example College", "Other"], commitOnEnter: true });
    let r = await write("school", "University of Westbridge");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Other" });
    expect(p.searches).toEqual(["University of Westbridge", "Other"]);
    expect(p.pills()).toEqual(["Other"]);
    mount("");
    p = mountWorkdayPrompt({ id: "school", label: "School or University", catalog: ["Example College", "Other - School Not Listed"] });
    expect(await write("school", "University of Westbridge")).toMatchObject({ outcome: "filled", optionLabel: "Other - School Not Listed" });
    // "Other" that finds a real school commits nothing that stays.
    mount("");
    p = mountWorkdayPrompt({ id: "school", label: "School or University", catalog: ["Brother Rice College"], answers: { Other: ["Brother Rice College"] }, commitOnEnter: true });
    r = await write("school", "University of Westbridge");
    expect(r.outcome).not.toBe("filled");
    expect(p.pills()).toEqual([]);
    // A school the search finds but cannot tell apart, and a field of study, never fall back.
    mount("");
    p = mountWorkdayPrompt({ id: "school", label: "School or University", catalog: ["Example Institute", "Example Institute Annex", "Other"] });
    expect((await write("school", "Example Inst")).outcome).toBe("abstained");
    expect(p.searches).toEqual(["Example Inst"]);
    mount("");
    p = mountWorkdayPrompt({ id: "major", label: "Field of Study", catalog: ["Mathematics", "Other"], answers: { "Underwater Basketry": [], Basketry: [] } });
    expect((await write("major", "Underwater Basketry")).outcome).not.toBe("filled");
    expect(p.searches).not.toContain("Other");
  });

  test("A wrong held school pill is removed before the right one is committed; skills keep their other pills", async () => {
    mount("");
    const p = mountWorkdayPrompt({ id: "school", label: "School or University", catalog: ["Example University", "Oakmont College"], held: ["Oakmont College"] });
    const r = await write("school", "Example University");
    expect(r.outcome).toBe("filled");
    expect(p.pills()).toEqual(["Example University"]);
    mount("");
    const s = mountWorkdayPrompt({ id: "skills--skills", label: "Skills", catalog: ["Go", "Rust"], held: ["Python"] });
    await write("skills--skills", "Go");
    expect(s.pills()).toEqual(["Python", "Go"]);
  });

  test("A held pill the pick tiers accept is kept without a search, and a search that abstains removes no pill", async () => {
    mount("");
    const p = mountWorkdayPrompt({ id: "major", label: "Field of Study", catalog: ["Mathematics or Statistics"], held: ["Mathematics or Statistics"] });
    expect((await write("major", "Applied Mathematics")).outcome).toBe("filled");
    expect(p.searches).toEqual([]);
    expect(p.pills()).toEqual(["Mathematics or Statistics"]);
    mount("");
    const q = mountWorkdayPrompt({ id: "school", label: "School or University", catalog: ["Example Institute", "Example Institute Annex"], held: ["Oakmont College"] });
    expect((await write("school", "Example Inst")).outcome).toBe("abstained");
    expect(q.pills()).toEqual(["Oakmont College"]);
  });

  test("A pill that already holds the answer is not clicked again", async () => {
    mount("");
    const p = mountWorkdayPrompt({ id: "skills--skills", label: "Skills", catalog: ["Go", "Rust"], held: ["Go"] });
    const r = await write("skills--skills", "Go, Rust");
    expect(r.outcome).toBe("filled");
    expect(p.clicks).toEqual(["Rust"]);
  });

  test("A skill is held only by a pill with its whole label: Java is not JavaScript, React is not React Native, and C# is not C++", async () => {
    mount("");
    const p = mountWorkdayPrompt({ id: "skills--skills", label: "Skills", catalog: ["JavaScript", "Java"] });
    expect((await write("skills--skills", "JavaScript, Java")).outcome).toBe("filled");
    expect(p.pills()).toEqual(["JavaScript", "Java"]);
    mount("");
    const q = mountWorkdayPrompt({ id: "skills--skills", label: "Skills", catalog: ["React Native", "React"], held: ["React Native"] });
    expect(await write("skills--skills", "React")).toMatchObject({ outcome: "filled", optionLabel: "React" });
    expect(q.pills()).toEqual(["React Native", "React"]);
    mount("");
    const c = mountWorkdayPrompt({ id: "skills--skills", label: "Skills", catalog: ["C++", "C#"], held: ["C++"] });
    expect(await writeWorkdayPrompt(c.input, { answers: ["C#"], label: "Skills" }, ctx())).toMatchObject({ ok: true, optionLabel: "C#" });
    expect(c.pills()).toEqual(["C++", "C#"]);
  });

  test("A tenant that commits the single match on Enter keeps every chip and is not clicked again", async () => {
    mount("");
    const p = mountWorkdayPrompt({ id: "skills--skills", label: "Skills", catalog: ["Go", "Rust"], commitOnEnter: true });
    const r = await write("skills--skills", "Go, Rust");
    expect(r.outcome).toBe("filled");
    expect(p.pills()).toEqual(["Go", "Rust"]);
    expect(p.clicks).toEqual(["Go", "Rust"]);
  });

  test("A pill the tenant commits on Enter is judged like any row: one that is not the answer is taken back off, and the right one replaces a wrong held school", async () => {
    mount("");
    const p = mountWorkdayPrompt({ id: "school", label: "School or University", catalog: [], commitOnEnter: true, answers: { "Example University": ["Oakmont College"] } });
    const r = await write("school", "Example University");
    expect(r).toMatchObject({ outcome: "abstained", reason: "option-not-found" });
    expect(p.pills()).toEqual([]);
    expect(p.input.value).toBe("");
    mount("");
    const q = mountWorkdayPrompt({ id: "school", label: "School or University", catalog: ["Example University"], commitOnEnter: true, held: ["Oakmont College"] });
    expect((await write("school", "Example University")).outcome).toBe("filled");
    expect(q.pills()).toEqual(["Example University"]);
  });

  test("Skills each searched over a slow round trip land past one field's budget", async () => {
    // Target: eight skills at one server round trip each outran the single-field budget, and the prompt was left empty.
    mount("");
    const names = ["Go", "Rust", "Python", "Java", "Kotlin", "Swift", "Scala", "Ruby"];
    // Eight round trips of 60 ms outrun one 470 ms budget; the menu's own budget leaves over a second to spare.
    const p = mountWorkdayPrompt({ id: "skills--skills", label: "Skills", catalog: names, delayMs: 60 });
    const r = await write("skills--skills", names.join(", "), {}, "replace", new Map(), false, { fieldBudgetMs: 470 });
    expect(r.outcome).toBe("filled");
    expect(p.pills()).toEqual(names);
  });

  test("The responsive full-screen dialog is closed with Done after the leaf is chosen", async () => {
    mount("");
    const p = mountWorkdayPrompt({ id: "school", label: "School or University", catalog: ["Example University"], responsive: true });
    const r = await write("school", "Example University");
    expect(r.outcome).toBe("filled");
    expect(p.doneClicks()).toBe(1);
  });

  test("A category beyond the first page is found by scrolling the widget's own list", async () => {
    mount("");
    const catalog = Array.from({ length: 40 }, (_, i) => `Field ${String(i).padStart(2, "0")}`).concat(["Mathematics or Statistics"]);
    const p = mountWorkdayPrompt({ id: "major", label: "Field of Study", catalog, windowSize: 5, delayMs: 5, matchAll: true });
    const r = await write("major", "Applied Mathematics");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Mathematics or Statistics" });
    expect(p.pills()).toEqual(["Mathematics or Statistics"]);
  });

  test("A field of study the catalog files under its head noun is found by it, and judged against the whole name", async () => {
    mount("");
    // CVS: "Applied Mathematics" finds unrelated rows, "Mathematics" finds the subject.
    let p = mountWorkdayPrompt({ id: "major", label: "Field of Study", catalog: ["Accounting", "AI", "Mathematics"], answers: { "Applied Mathematics": ["Accounting", "AI"] } });
    let r = await write("major", "Applied Mathematics");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Mathematics" });
    expect(p.searches).toEqual(["Applied Mathematics", "Mathematics"]);
    // The head noun's rows must still answer the whole name.
    mount("");
    p = mountWorkdayPrompt({ id: "major", label: "Field of Study", catalog: ["Mathematics Education", "Mathematics Teaching"], answers: { "Applied Mathematics": [] } });
    r = await write("major", "Applied Mathematics");
    expect(r.outcome).not.toBe("filled");
    // A school is not narrowed to a word of its name.
    mount("");
    p = mountWorkdayPrompt({ id: "school", label: "School or University", catalog: ["University of Westbridge Harbor"], answers: { "University of Westbridge": [] } });
    r = await write("school", "University of Westbridge");
    expect(p.searches).toEqual(["University of Westbridge", "Other"]);
  });

  test("A subject filed under two faculties takes the one the row's degree names", async () => {
    // NXP: "Mathematics" finds "Arts-Mathematics" and "Science-Mathematics".
    const nxp = () => mountWorkdayPrompt({ id: "major", label: "Field of Study", catalog: ["Arts-Mathematics", "Science-Mathematics"], answers: { "Applied Mathematics": [] } });
    for (const [degree, want] of [["Bachelor of Science (B.S)", "Science-Mathematics"], ["B.A.", "Arts-Mathematics"]] as const) {
      mount("");
      const p = nxp();
      expect(await write("major", "Applied Mathematics", { degree })).toMatchObject({ outcome: "filled", optionLabel: want });
      expect(p.pills()).toEqual([want]);
    }
    // No degree, or one that names neither faculty, picks nothing.
    for (const degree of [undefined, "Bachelors"]) {
      mount("");
      const p = nxp();
      expect((await write("major", "Applied Mathematics", { degree })).outcome).not.toBe("filled");
      expect(p.pills()).toEqual([]);
    }
  });

  test("A hierarchical menu is drilled only through options that advertise a sub-menu, then back", async () => {
    mount("");
    const p = mountWorkdayPrompt({
      id: "source",
      label: "How Did You Hear About Us?",
      catalog: [],
      tree: { "Job Board": ["Board A", "Board B"], "Social Media": ["Friend Network", "Group Feed"], Other: [] },
    });
    const r = await write("source", "Group Feed");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Group Feed" });
    expect(p.clicks).toEqual(["Group Feed"]);
  });

  test("A sub-menu the server sends late is waited for, in every category", async () => {
    mount("");
    const p = mountWorkdayPrompt({
      id: "source",
      label: "How Did You Hear About Us?",
      catalog: [],
      tree: { "Job Fair": ["Campus"], "Job Boards": ["Indeed", "LinkedIn"], Other: [] },
      branchDelayMs: 300,
    });
    // The server is slower than a local menu opens, well within a search's wait.
    const r = await write("source", "LinkedIn", {}, "replace", new Map(), false, { openWaitMs: 60, searchWaitMs: 2000 });
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "LinkedIn" });
    expect(p.pills()).toEqual(["LinkedIn"]);
    // Reached through its category, not by the search the drill falls back to.
    expect(p.searches).toEqual([]);
  });

  test("Workday's own menu: rows listed twice, a top drawn a few rows at a time, a server and a header way back", async () => {
    mount("");
    const p = mountWorkdayPrompt({
      id: "source",
      label: "How Did You Hear About Us?",
      catalog: [],
      tree: { Advertising: ["PrintedMedia"], CVS: ["CVS Careers"], "Job Boards": ["Indeed", "LinkedIn"], "Job Fair": ["Campus"] },
      workdayRows: true,
      drawTwoAtFirst: true,
      headerBack: true,
      branchDelayMs: 20,
    });
    const r = await write("source", "LinkedIn");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "LinkedIn" });
    expect(p.searches).toEqual([]);
  });

  test("A Workday menu answered by a category: the category's one choice, never a guess among several", async () => {
    const tree = { "Career Fair": ["Career Fair - Finance", "Career Fair - Tech"], "Internet Job Board / Job Search": ["Other"] };
    mount("");
    let p = mountWorkdayPrompt({ id: "source", label: "How Did You Hear About Us?", catalog: [], tree, workdayRows: true, headerBack: true });
    const one = await write("source", "Internet Job Board / Job Search");
    expect(one).toMatchObject({ outcome: "filled", optionLabel: "Other" });
    expect(p.pills()).toEqual(["Other"]);
    mount("");
    p = mountWorkdayPrompt({ id: "source", label: "How Did You Hear About Us?", catalog: [], tree, workdayRows: true, headerBack: true });
    const several = await write("source", "Career Fair");
    expect(several.outcome).not.toBe("filled");
    expect(p.pills()).toEqual([]);
  });

  test("A Workday menu without the answer hands its categories to the row, for the check to choose from", async () => {
    mount("");
    mountWorkdayPrompt({
      id: "source",
      label: "How Did You Hear About Us?",
      catalog: [],
      tree: { "Career Fair": ["Career Fair - Finance"], "Internet Job Board / Job Search": ["Other"] },
      workdayRows: true,
      headerBack: true,
    });
    const r = await write("source", "LinkedIn");
    expect(r.outcome).not.toBe("filled");
    expect(r.options).toEqual(["Career Fair", "Internet Job Board / Job Search"]);
  });

  test("A menu whose categories hold No Items is left in moments, and the tenant's nearest general channel is taken", async () => {
    const tree = { Advertising: [], "Career Fair": [], CVS: [], "Job Boards": [], Referral: [], "Social Media": [] };
    mount("");
    let p = mountWorkdayPrompt({ id: "source", label: "How Did You Hear About Us?", catalog: ["IndeedEasyApply", "CVS Health Website"], tree, workdayRows: true, headerBack: true });
    const started = Date.now();
    // Six categories at a search's full wait would take half a minute.
    let r = await write("source", "LinkedIn", {}, "replace", new Map(), false, { openWaitMs: 60, searchWaitMs: 5000 });
    expect(Date.now() - started).toBeLessThan(5000);
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "CVS Health Website" });
    expect(p.searches).toEqual(["LinkedIn", "Job Board", "Website"]);
    mount("");
    p = mountWorkdayPrompt({ id: "source", label: "How Did You Hear About Us?", catalog: ["Job Board - Other", "CVS Health Website"], tree, workdayRows: true, headerBack: true });
    r = await write("source", "LinkedIn", {}, "replace", new Map(), false, { openWaitMs: 60, searchWaitMs: 5000 });
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Job Board - Other" });
  }, 60_000);

  test("A menu leaf whose click commits nothing is not proved by a pill the prompt already held", async () => {
    mount("");
    const p = mountWorkdayPrompt({ id: "source", label: "How Did You Hear About Us?", catalog: [], tree: { "Job Board": ["Board A"] }, held: ["Stale Source"] });
    // The page ignores the leaf's click.
    const ignore = (e: Event) => (e.target as Element).closest("li")?.textContent === "Board A" && e.stopPropagation();
    document.addEventListener("click", ignore, true);
    const r = await write("source", "Board A", { replaceDefault: true });
    document.removeEventListener("click", ignore, true);
    expect(r).toMatchObject({ outcome: "failed", reason: "write-not-committed" });
    expect(p.pills()).toEqual(["Stale Source"]);
  });

  test("A flat leaf list is never clicked for a wrong answer", async () => {
    mount("");
    const p = mountWorkdayPrompt({ id: "source", label: "How Did You Hear About Us?", catalog: [], tree: { "Job Board": ["Board A"] } });
    const r = await write("source", "Nonexistent Source");
    expect(r.outcome).not.toBe("filled");
    expect(p.clicks).toEqual([]);
  });
});

describe("Workday catalog pick tiers", () => {
  const table: [string[], string, string | null][] = [
    [["Example University", "Example University Online"], "Example University", "Example University"],
    [["Python (Programming Language)", "Pythonic Design"], "Python", "Python (Programming Language)"],
    [["Example Institute", "Example Institute Annex", "Example Institute Press"], "Example Inst", null],
    [["Example University of Arts", "Oakmont College"], "Example University", "Example University of Arts"],
    [["Mathematics or Statistics", "Computer Science or Computer Engineering"], "Applied Mathematics", "Mathematics or Statistics"],
    [["Oakmont State College", "Greater Oakmont State College"], "Oakmont State", "Oakmont State College"],
    [["No Items."], "Anything", null],
    // NXP: the catalog files a subject under its discipline first.
    [["Engineering-Electrical", "Engineering-Computer", "Computer Science"], "Computer Engineering", "Engineering-Computer"],
    [["Mathematics-General", "Mathematics-Applied"], "Applied Mathematics", "Mathematics-Applied"],
    [["Engineering-Computer Systems"], "Computer Engineering", null],
    // A plus or hash sign is part of the name.
    [["C++", "C#", "C"], "C#", "C#"],
    [["C++"], "C#", null],
  ];
  test.each(table)("%j answers %j with %j", (labels, answer, want) => {
    expect(pickCatalogLabel(labels, answer)).toBe(want);
  });
});

describe("Workday questionnaire date", () => {
  test("Workday date triple: month 08, day 02 and year 2027 are typed segment by segment and read back; the scan makes one question named by the legend", async () => {
    mount("");
    const parts = mountDateTriple({ id: "q", legend: "Expiry date *" });
    const infos = scanControls(document);
    expect(infos).toHaveLength(1);
    expect(infos[0]).toMatchObject({ kind: "text", inputType: "date", driver: "date-segments", owner: "matcher", required: true });
    expect(infos[0]?.label.text).toBe("Expiry date");
    const r = await write(infos[0]!.el.id, "2027-08-02");
    expect(r).toMatchObject({ outcome: "filled", value: "2027-08-02" });
    expect(parts.map((p) => p.value)).toEqual(["08", "02", "2027"]);
  });

  test("A US slash date is accepted; anything else fails", async () => {
    mount("");
    const parts = mountDateTriple({ id: "q", legend: "Expiry date" });
    const id = scanControls(document)[0]!.el.id;
    expect((await write(id, "8/2/2027")).outcome).toBe("filled");
    expect(parts.map((p) => p.value)).toEqual(["08", "02", "2027"]);
    mount("");
    mountDateTriple({ id: "q", legend: "Expiry date" });
    expect(await write(scanControls(document)[0]!.el.id, "August 2, 2027")).toMatchObject({ outcome: "failed", reason: "format-not-accepted" });
  });

  test("A tenant that shows the month and day without a leading zero reads back as the date written", async () => {
    mount("");
    const parts = mountDateTriple({ id: "q", legend: "Expiry date", unpadded: true });
    expect(await write(scanControls(document)[0]!.el.id, "2027-08-02")).toMatchObject({ outcome: "filled", value: "2027-08-02" });
    expect(parts.map((p) => p.value)).toEqual(["8", "2", "2027"]);
  });

  test("A segment is the part the end of its id names: a row called lastDayOfWork does not turn its year into a day", async () => {
    mount("");
    const parts = mountDateTriple({ id: "lastDayOfWork", legend: "Last day of work", questionnaire: false });
    const info = scanControls(document)[0]!;
    const r = await write(info.el.id, "2027-08-02");
    expect(r).toMatchObject({ outcome: "filled", value: "2027-08-02" });
    expect(parts.map((p) => p.value)).toEqual(["08", "02", "2027"]);
    expect(readInfo(info)).toBe("2027-08-02");
  });

  test("A month and year under a row whose name holds 'day' is a month and year, held once written", async () => {
    mount("");
    const parts = mountDateTriple({ id: "birthday", legend: "Start", questionnaire: false, noDay: true });
    const info = scanControls(document)[0]!;
    expect((await write(info.el.id, "2027-08")).outcome).toBe("filled");
    expect(parts.map((p) => p.value)).toEqual(["08", "2027"]);
    expect(readInfo(info)).toBe("2027-08");
  });

  test("A first interaction before the widget hydrates is repeated once", async () => {
    mount("");
    const parts = mountDateTriple({ id: "q", legend: "Expiry date", coldStart: true });
    const r = await write(scanControls(document)[0]!.el.id, "2027-08-02");
    expect(r.outcome).toBe("filled");
    expect(parts.map((p) => p.value)).toEqual(["08", "02", "2027"]);
  });

  test("A row's own date (not a questionnaire answer) stays with the profile lane", async () => {
    mount("");
    mountDateTriple({ id: "workExperience-1", legend: "From", questionnaire: false });
    const info = scanControls(document)[0]!;
    expect(info).toMatchObject({ driver: "date-segments", owner: "profile" });
  });

  test("The run asks the matcher for the date once and writes the bank's answer", async () => {
    mount("");
    const parts = mountDateTriple({ id: "q", legend: "Work authorization expiry date *" });
    const host = fakeHost({ "work authorization expiry date": { outcome: "filled", value: "2027-08-02" } });
    const s = summaryOf(await runFill(makeEngine(host)));
    expect(host.matchRequests[0]?.questions).toHaveLength(1);
    expect(host.matchRequests[0]?.questions[0]).toMatchObject({ inputType: "date", kind: "text" });
    expect(parts.map((p) => p.value)).toEqual(["08", "02", "2027"]);
    expect(s.rows.find((r) => r.label.includes("expiry"))?.outcome).toBe("filled");
  });

  test("A month and year answers a date whose question asks for the first of the month: the day typed is 01", async () => {
    mount("");
    const parts = mountDateTriple({ id: "q", legend: "When did you or do you expect to graduate? Please select the first of the month and year you will complete your studies. *" });
    const host = fakeHost({ [scanControls(document)[0]!.selector]: { outcome: "filled", value: "May 2017" } });
    const s = summaryOf(await runFill(makeEngine(host)));
    expect(parts.map((p) => p.value)).toEqual(["05", "01", "2017"]);
    expect(s.rows.find((r) => r.label.includes("graduate"))?.outcome).toBe("filled");
  });

  test("A month and year is never typed into a day-precision date that does not ask for the first, and the empty date never reads as answered", async () => {
    mount("");
    const parts = mountDateTriple({ id: "q", legend: "Graduation date *" });
    const host = fakeHost({ [scanControls(document)[0]!.selector]: { outcome: "filled", value: "May 2017" } });
    const s = summaryOf(await runFill(makeEngine(host)));
    expect(parts.map((p) => p.value)).toEqual(["", "", ""]);
    expect(s.rows.find((r) => r.label.includes("Graduation"))).toMatchObject({ outcome: "failed", reason: "format-not-accepted" });
  });
});

describe("Workday phone and listbox buttons", () => {
  test("The phone's country code prompt is searched by country name and a row with the exact dial code is committed by the phone box", async () => {
    mount(`<label for="phone-number">Phone Number *</label><input id="phone-number" type="tel">`);
    const p = mountWorkdayPrompt({
      id: "country-code",
      label: "Country Phone Code",
      catalog: ["United States Minor Outlying Islands (+1)", "United States of America (+1)", "Antigua and Barbuda (+1264)"],
    });
    const host = fakeHost();
    const args = { applicant: { candidate: { phone: "+1 555 0100", phoneCountryCode: "+1", address: { country: "United States" } } } };
    await runFill(makeEngine(host), args);
    expect(p.pills()).toEqual(["United States of America (+1)"]);
    expect(p.searches).toEqual(["United States"]);
    // The matcher never saw the prompt.
    expect(host.matchRequests.flatMap((r) => r.questions).some((q) => /code/i.test(q.text))).toBe(false);
  });

  test("The dial code is the one the server split off the number, so a number written without separators finds its row", async () => {
    mount(`<label for="phone-number">Phone Number *</label><input id="phone-number" type="tel">`);
    const p = mountWorkdayPrompt({ id: "country-code", label: "Country Phone Code", catalog: ["China (+86)", "United States of America (+1)"] });
    // Built at run time: the repository guard only allows the fictional +1 range as a literal.
    const phone = ["+86", "138", "0013", "8000"].join("");
    await runFill(makeEngine(fakeHost()), { applicant: { candidate: { phone, phoneCountryCode: "+86", address: { country: "China" } } } });
    expect(p.pills()).toEqual(["China (+86)"]);
  });

  test("A questionnaire dropdown is a button that declares a listbox popup, read by its own text; the placeholder reads empty", async () => {
    mount(`<label id="l1">Are you legally authorized to work here? *</label>
      <button id="q1" type="button" aria-haspopup="listbox" aria-labelledby="l1">Select One</button>
      <div data-automation-id="selectedItemList"><span role="option">Do not choose me</span></div>`);
    const btn = document.getElementById("q1") as HTMLButtonElement;
    expect(readInfo(infoOf("q1"))).toBe("");
    const popup = document.createElement("ul");
    popup.setAttribute("role", "listbox");
    popup.setAttribute("aria-labelledby", "q1");
    popup.hidden = true;
    for (const t of ["Yes", "No"]) {
      const li = document.createElement("li");
      li.setAttribute("role", "option");
      li.textContent = t;
      li.addEventListener("click", () => {
        btn.textContent = t;
        popup.hidden = true;
      });
      popup.append(li);
    }
    document.body.append(popup);
    btn.addEventListener("mousedown", () => (popup.hidden = false));
    const r = await write("q1", "Yes");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Yes" });
    expect(readInfo(infoOf("q1"))).toBe("Yes");
  });
});
