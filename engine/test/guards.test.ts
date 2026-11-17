// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Value-shape guards for typed controls; Controls the engine never writes or presses.
import { describe, expect, test } from "vitest";
import { isUnsafeLabel, safeClick } from "../src/click-guard";
import { foldLabel, hasMixedScriptWord } from "../src/guard-text";
import { parseDate } from "../src/dates";
import { lowerBound, plainNumber } from "../src/guards";
import { phoneHint } from "../src/phone";
import { ADA, PAD, byId, fakeHost, makeEngine, mount, rowFor, runFill, write, summaryOf, tick } from "./helpers";

describe("Value-shape guards for typed controls", () => {
  test("Email box refuses a URL: a mis-bound answer for an autocomplete=email input is not written and the row is failed", async () => {
    mount(`<label for="e">Contact</label><input id="e" autocomplete="email">`);
    expect(await write("e", "https://www.linkedin.com/in/ada-example")).toMatchObject({ outcome: "failed", reason: "format-not-accepted" });
    expect(byId("e").value).toBe("");
    expect((await write("e", "ada@example.com")).outcome).toBe("filled");
  });

  test("A control naming exactly one social network refuses a URL on another host, unless it invites any site", async () => {
    mount(`<label for="a">LinkedIn profile</label><input id="a"><label for="b">GitHub or personal website</label><input id="b"><label for="c">GitHub</label><input id="c">`);
    expect(await write("a", "https://github.com/ada-example")).toMatchObject({ outcome: "failed", reason: "format-not-accepted" });
    expect((await write("a", "https://www.linkedin.com/in/ada-example")).outcome).toBe("filled");
    expect((await write("b", "https://ada.example.com")).outcome).toBe("filled");
    expect((await write("c", "ada-example")).outcome).toBe("filled");
  });

  test("Range on a digits-only box: $100K-250K writes the lower bound with scaling", async () => {
    mount(`<label for="s">Desired salary</label><input id="s" inputmode="numeric"><label for="n">Expected pay</label><input id="n" type="number" min="0" max="500000" step="1000">`);
    expect((await write("s", "$100K-250K")).outcome).toBe("filled");
    expect(byId("s").value).toBe("100000");
    expect((await write("n", "$100K-250K")).outcome).toBe("filled");
    expect(byId("n").value).toBe("100000");
    mount(`<label for="s">Desired salary</label><input id="s" inputmode="numeric">`);
    expect(await write("s", "negotiable")).toMatchObject({ outcome: "failed", reason: "format-not-accepted" });
  });

  test("A salary question rendered as a plain text box takes the range's lower bound, never the digits of the whole range", async () => {
    // Workday MSK "Desired Annual Pay": type text, no inputmode, no pattern; the page keeps only the digits typed.
    const id = "primaryQuestionnaire--602e5b1032dd1000b49c5baf54150001";
    mount(`<label for="${id}">Desired Annual Pay:*</label><input id="${id}" type="text">`);
    byId(id).addEventListener("input", () => {
      byId(id).value = byId(id).value.replace(/\D/g, "");
    });
    expect((await write(id, "$100K-250K")).outcome).toBe("filled");
    expect(byId(id).value).toBe("100000");
  });

  test("A digits-only box keeps the answer as given: a ZIP code its leading zero, a GPA its decimals, an extension its zeros", async () => {
    mount(`<label for="z">ZIP code</label><input id="z" inputmode="numeric">
      <label for="z4">Postal code</label><input id="z4" pattern="[0-9]*">
      <label for="g">GPA</label><input id="g" inputmode="decimal">
      <label for="x">Phone extension</label><input id="x" inputmode="numeric" pattern="\\d*">
      <label for="d">Date available</label><input id="d" inputmode="numeric" placeholder="MM/DD/YYYY">`);
    for (const [id, value] of [["z", "02139"], ["z4", "02139"], ["g", "3.85"], ["x", "0042"], ["d", "11/10/2026"]] as const) {
      expect((await write(id, value)).outcome, id).toBe("filled");
      expect(byId(id).value, id).toBe(value);
    }
    expect(await write("z", "not applicable")).toMatchObject({ outcome: "failed", reason: "format-not-accepted" });
  });

  test("A digits-only box is reduced to one whole number only where the wording asks for an amount or a count", async () => {
    mount(`<label for="s">Desired salary</label><input id="s" inputmode="numeric">
      <label for="y">Years of experience</label><input id="y" inputmode="numeric">
      <label for="d">Years of experience with Go</label><input id="d" inputmode="decimal">`);
    expect((await write("s", "$85,000 - $95,000")).outcome).toBe("filled");
    expect(byId("s").value).toBe("85000");
    expect((await write("y", "3-5 years")).outcome).toBe("filled");
    expect(byId("y").value).toBe("3");
    expect((await write("d", "2.5")).outcome).toBe("filled");
    expect(byId("d").value).toBe("2.5");
  });

  test("A range whose last number alone carries the k or m gives every number that scale", () => {
    expect(lowerBound("$120-150k")).toBe("120000");
    expect(lowerBound("120 to 150K")).toBe("120000");
    expect(lowerBound("1.5 - 2M")).toBe("1500000");
    expect(lowerBound("$100K-250K")).toBe("100000");
    expect(lowerBound("5 years, 150k")).toBe("5");
  });

  test("A numeric input honours min, max and step; an identifier-marked box takes exactly one plain number", async () => {
    mount(`<label for="y">Years</label><input id="y" type="number" min="0" max="40" step="1"><label for="skill-numeric">Years with Go</label><input id="skill-numeric" name="skill">`);
    expect((await write("y", "75")).outcome).toBe("filled");
    expect(byId("y").value).toBe("40");
    expect((await write("skill-numeric", "2-3 years")).outcome).toBe("failed");
    expect((await write("skill-numeric", "1,200")).outcome).toBe("filled");
    expect(byId("skill-numeric").value).toBe("1200");
    expect(plainNumber("Yes")).toBeNull();
    expect(plainNumber("120k")).toBeNull();
    expect(lowerBound("up to 2M")).toBe("2000000");
  });

  test("A phone box the page makes type=number takes the number's digits, a North American number its ten; another number box refuses a phone", async () => {
    // Freed (Ashby): "Phone Number" is a Number field, so the page renders <input type="number">, which drops "+1 843-555-0142".
    mount(`<label for="p">Phone Number</label><input id="p" type="number"><label for="q">Mobile phone</label><input id="q" type="number"><label for="y">Years</label><input id="y" type="number">`);
    expect((await write("p", "+1 843-555-0142")).outcome).toBe("filled");
    expect(byId("p").value).toBe("8435550142");
    expect((await write("q", "(843) 555-0142")).outcome).toBe("filled");
    expect(byId("q").value).toBe("8435550142");
    expect(await write("y", "+1 843-555-0142")).toMatchObject({ outcome: "failed", reason: "format-not-accepted" });
  });

  test("type=month takes YYYY-MM, sliced from a range by the control's wording; a bare month name is refused", async () => {
    mount(`<label for="s">Start date</label><input id="s" type="month"><label for="e">End date</label><input id="e" type="month"><label for="b">Graduation</label><input id="b" type="month">`);
    expect((await write("s", "Aug. 2024 - May 2026")).outcome).toBe("filled");
    expect(byId("s").value).toBe("2024-08");
    expect((await write("e", "Aug. 2024 - May 2026")).outcome).toBe("filled");
    expect(byId("e").value).toBe("2026-05");
    expect(await write("b", "August")).toMatchObject({ outcome: "failed", reason: "format-not-accepted" });
  });

  test("A native date input receives ISO converted from the US form, or stays unwritten", async () => {
    mount(`<label for="d">Available from</label><input id="d" type="date"><label for="o">Other</label><input id="o" type="date">`);
    expect((await write("d", "08/02/2027")).outcome).toBe("filled");
    expect(byId("d").value).toBe("2027-08-02");
    expect(await write("o", "next month")).toMatchObject({ outcome: "failed", reason: "format-not-accepted" });
  });

  test("A month and year written to a native date input is its first day, a range's edge by the wording (Bitdeer's graduation date)", async () => {
    mount(`<label for="g">When did you graduate/When are you graduating?</label><input id="g" type="date"><label for="s">Start date</label><input id="s" type="date"><label for="y">Year</label><input id="y" type="date">`);
    expect((await write("g", "May 2026")).outcome).toBe("filled");
    expect(byId("g").value).toBe("2026-05-01");
    expect((await write("s", "Aug 2024 - May 2026")).outcome).toBe("filled");
    expect(byId("s").value).toBe("2024-08-01");
    expect(await write("y", "2026")).toMatchObject({ outcome: "failed", reason: "format-not-accepted" });
  });

  test("A native date outside the box's own min or max is never written, and the bound is told (Citadel)", async () => {
    mount(`<label for="g">Graduation Date</label><input id="g" type="date" min="2026-11-02"><label for="h">Start Date</label><input id="h" type="date" max="2026-10-31">`);
    expect(await write("g", "05/01/2026")).toMatchObject({ outcome: "failed", reason: "out-of-range", detail: "the page accepts dates from 2026-11-02 on" });
    expect(byId("g").value).toBe("");
    expect(await write("h", "11/01/2026")).toMatchObject({ outcome: "failed", reason: "out-of-range", detail: "the page accepts dates up to 2026-10-31" });
    expect((await write("g", "12/15/2026")).outcome).toBe("filled");
    expect(byId("g").value).toBe("2026-12-15");
  });

  test("A text date follows the layout its placeholder declares", async () => {
    mount(`<label for="a">Start</label><input id="a" placeholder="MM/YYYY"><label for="b">Start</label><input id="b" placeholder="DD/MM/YYYY"><label for="c">Start</label><input id="c" placeholder="MM/DD/YYYY">`);
    await write("a", "2024-08-15");
    await write("b", "2024-08-15");
    await write("c", "2024-08-15");
    expect([byId("a").value, byId("b").value, byId("c").value]).toEqual(["08/2024", "15/08/2024", "08/15/2024"]);
  });

  test("A text date takes its separator from between the date parts, not from a dash or dot elsewhere in the hint", async () => {
    mount(`<label for="a">Start</label><input id="a" placeholder="MM/DD/YYYY" pattern="[0-9]{2}/[0-9]{2}/[0-9]{4}">
      <label for="b">Start (e.g. 31.12.2024)</label><input id="b" placeholder="MM/DD/YYYY">
      <label for="c">Start</label><input id="c" placeholder="DD.MM.YYYY">
      <label for="d">Start</label><input id="d" placeholder="D-M-YY">`);
    for (const id of ["a", "b", "c", "d"]) await write(id, "2026-11-09");
    expect(["a", "b", "c", "d"].map((id) => byId(id).value)).toEqual(["11/09/2026", "11/09/2026", "09.11.2026", "09-11-2026"]);
  });

  test("A two-digit year more than ten years ahead belongs to the last century", () => {
    const now = new Date().getFullYear();
    const yy = (y: number) => String(y % 100).padStart(2, "0");
    expect(parseDate(`06/15/${yy(now + 5)}`)?.y).toBe(now + 5);
    expect(parseDate(`06/15/${yy(now - 28)}`)?.y).toBe(now - 28);
    expect(parseDate(`06/15/${yy(now + 20)}`)?.y).toBe(now + 20 - 100);
  });

  test("A text state input receives the two-letter code when maxlength is 2 or the wording names a code, else the full name", async () => {
    mount(`<label for="a">State</label><input id="a" maxlength="2"><label for="b">State</label><input id="b"><label for="c">State (abbreviation)</label><input id="c">`);
    await write("a", "North Carolina");
    await write("b", "NC");
    await write("c", "North Carolina");
    expect([byId("a").value, byId("b").value, byId("c").value]).toEqual(["NC", "North Carolina", "NC"]);
  });

  test("The current date is derived at write time from the local clock, in the control's format", async () => {
    mount(`<label for="a">Date signed</label><input id="a" type="date"><label for="b">Date signed</label><input id="b" placeholder="DD/MM/YYYY"><label for="c">Date signed</label><input id="c">`);
    for (const id of ["a", "b", "c"]) await write(id, "", { currentDate: true });
    expect([byId("a").value, byId("b").value, byId("c").value]).toEqual(["2026-10-11", "11/10/2026", "10/11/2026"]);
  });

  test("A box the page marks tel-national takes the national digits, whatever its hint says (Oracle's phone beside its +1 country-code picker, Stanford)", async () => {
    mount(`<span id="cc-label">Country code</span><div><input id="cc" class="cx-select-input" role="combobox" aria-haspopup="grid" aria-labelledby="cc-label" value="+1">
      <input id="p" type="tel" autocomplete="tel-national" aria-label="Phone Number" maxlength="30"></div>`);
    expect((await write("p", "+1 202-555-0142", { phone: true })).outcome).toBe("filled");
    expect(byId("p").value).toBe("2025550142");
  });

  test("Non-NANP number untouched: the same label for a profile phone whose country code is not +1 writes it unchanged", async () => {
    mount(`<label for="p">Phone (Enter with dashes. Ex: 202-555-0100)</label><input id="p" type="tel">`);
    // Built at run time: the repository guard only allows the fictional +1 range as a literal.
    const uk = ["+44", "20", "7946", "0123"].join(" ");
    expect((await write("p", uk, { phone: true })).outcome).toBe("filled");
    expect(byId("p").value).toBe(uk);
  });

  test("A long country-code select names each country with its dial code: an answer naming the country, with or without its code, picks it (Capgemini SuccessFactors)", async () => {
    // Over 100 options the matcher gets none, so the bank's own wording arrives and only an exact country is taken.
    const filler = Array.from({ length: 120 }, (_, i) => `<option>Exampleland ${String.fromCharCode(97 + (i % 26))}${i} (+${300 + i})</option>`).join("");
    mount(`<label for="cc">Country/Region Code:</label><select id="cc" aria-required="true"><option value="">- Select -</option><option>American Samoa (+1)</option><option>Canada (+1)</option>${filler}<option>United Kingdom (+44)</option><option>United States (+1)</option><option>United States Minor Outlying Islands (+340)</option></select>${PAD}`);
    const sel = byId("cc") as unknown as HTMLSelectElement;
    for (const answer of ["United States", "US", "United States (+1)", "+1 United States", "USA +1", "United States of America (+1)"]) {
      sel.selectedIndex = 0;
      expect((await write("cc", answer)).outcome, answer).toBe("filled");
      expect(sel.options[sel.selectedIndex]?.text, answer).toBe("United States (+1)");
    }
    sel.selectedIndex = 0;
    expect((await write("cc", "+1")).outcome).not.toBe("filled");
  });

  test("A phone label cue order: E.164 cues first, then examples, then prose", async () => {
    mount(`<label for="a">Phone (include country code, e.g. +1)</label><input id="a" type="tel"><label for="b">Phone</label><input id="b" type="tel" placeholder="(555) 555-0100">
      <label for="c">Phone (digits only)</label><input id="c" type="tel"><label for="d">Phone</label><input id="d" type="tel">`);
    for (const id of ["a", "b", "c", "d"]) await write(id, "+12025550142", { phone: true });
    expect([byId("a").value, byId("b").value, byId("c").value, byId("d").value]).toEqual(["+12025550142", "(202) 555-0142", "2025550142", "+12025550142"]);
  });

  test("A phone input is typed one character at a time and a country-detecting tel input receives E.164", async () => {
    mount(`<label for="p">Mobile</label><input id="p" type="tel"><label for="q">Phone</label><input id="q" type="tel" role="combobox" aria-haspopup="listbox">`);
    const keys: string[] = [];
    byId("p").addEventListener("keydown", (e) => keys.push((e as KeyboardEvent).key));
    await write("p", "202-555-0142", { phone: true });
    expect(keys.join("")).toBe("202-555-0142");
    expect(byId("p").value).toBe("202-555-0142");
    await write("q", "+12025550142", { phone: true });
    expect(byId("q").value).toBe("+12025550142");
  });
});

describe("Controls the engine never writes or presses", () => {
  test("Guard table: Submit, Submit application, Withdraw application, Apply and save and Discard draft are refused, Add another is allowed", () => {
    for (const label of ["Submit", "Submit application", "Withdraw application", "Apply and save", "Discard draft", "Delete application", "Apply without saving"]) {
      expect(isUnsafeLabel(label), label).toBe(true);
    }
    for (const label of ["Add another", "Add education", "Yes", "Done", "OK", "Previous month", "Go to next month", "Next year"]) expect(isUnsafeLabel(label), label).toBe(false);
  });

  test("Guard table: page advances, sign-in and sign-up, every Save and Apply control are refused", () => {
    const refused = [
      "Next", "Continue", "Review", "Review application", "Continue to next step",
      "Sign in", "Log in", "Login", "Sign up", "Create account", "Create an account", "Register",
      "Save and continue", "Save draft", "Save application", "Save for later", "Save", "Save & continue",
      "Apply", "Apply now", "Easy Apply", "Apply for this job",
    ];
    for (const label of refused) expect(isUnsafeLabel(label), label).toBe(true);
  });

  test("Guard table: labels are read folded, so accents, width forms, invisible characters and lookalikes do not dodge a word", () => {
    const folded = [
      "Sub\u00admit", "Sub\u200bmit", "\uff33\uff55\uff42\uff4d\uff49\uff54", "W\u0131thdraw", "Withdr\u0561w", "Contin\u0575e",
      "\u0405ubmit", "Cont\u0131nue", "N\u00e9xt", "Re\u0301view", "\u0391pply", "Dele\u0442e application",
    ];
    for (const label of folded) expect(isUnsafeLabel(label), JSON.stringify(label)).toBe(true);
    expect(foldLabel("  Save\u00a0 and\tcontinue ")).toBe("save and continue");
    expect(foldLabel("Sub\u200bmit")).toBe("submit");
    expect(foldLabel("\u0405ubmit")).toBe("submit");
    expect(foldLabel("Withdr\u0561w")).toBe("withdraw");
    expect(foldLabel("\u13aa\u13e2\u13e2\u13de" + "y")).toBe("apply");
    expect(foldLabel("caf" + "e\u0301")).toBe("cafe");
  });

  test("Guard table: a word spelled from two alphabets is refused even when the fold does not know the letter", () => {
    expect(hasMixedScriptWord("Contin\u13a1e")).toBe(true);
    expect(hasMixedScriptWord("C\u043entinue")).toBe(true);
    expect(hasMixedScriptWord("Next step and \u0e44\u0e17\u0e22")).toBe(false);
    for (const label of ["Add anot\u13a1er", "Cont\u0131nu\u03b5 here", "Yes\u0561"]) expect(isUnsafeLabel(label), JSON.stringify(label)).toBe(true);
  });

  test("a control named through aria-labelledby or a nested aria-label is read by that name", () => {
    mount(`<span id="n">Submit application</span><button id="a" aria-labelledby="n">Go</button><button id="b"><span aria-label="Withdraw application">x</span></button><button id="c">Add another</button>`);
    let clicks = 0;
    for (const id of ["a", "b", "c"]) byId(id).addEventListener("click", () => clicks++);
    expect(["a", "b", "c"].map((id) => safeClick(byId(id)))).toEqual(["unsafe-click", "unsafe-click", "ok"]);
    expect(clicks).toBe(1);
  });

  test("safeClick refuses a labelled control without clicking it, reading the aria-label, text, title and value", () => {
    mount(`<button id="a" aria-label="Submit application">Go</button><button id="b" title="Discard draft">x</button><input id="c" type="button" value="Withdraw"><button id="d">Add another</button>`);
    let clicks = 0;
    for (const id of ["a", "b", "c", "d"]) byId(id).addEventListener("click", () => clicks++);
    expect(["a", "b", "c", "d"].map((id) => safeClick(byId(id)))).toEqual(["unsafe-click", "unsafe-click", "unsafe-click", "ok"]);
    expect(clicks).toBe(1);
  });

  test("Untyped button in a form: the submission is cancelled, appears in blockedSubmits, and no request reaches the server", async () => {
    mount(`<form id="apply" action="/submit"><label for="a">First name</label><input id="a"><button id="go">Submit your application</button></form>${PAD}`);
    const engine = makeEngine(fakeHost(), { settleMs: 30 });
    let reached = 0;
    byId("apply").addEventListener("submit", () => reached++);
    const r = engine.start({ applicant: ADA });
    expect(r.started).toBe(true);
    byId("go").click();
    expect(reached).toBe(0);
    expect(engine.blockedSubmits()).toEqual([{ form: "apply", submitter: "Submit your application" }]);
    while (engine.status().state === "running") await tick(5);
  });

  test("Hold released: a later native submission is not held by the engine", async () => {
    mount(`<form id="apply"><label for="a">First name</label><input id="a"><button id="go">Next</button></form>${PAD}`);
    const engine = makeEngine(fakeHost());
    await runFill(engine);
    let reached = 0;
    byId("apply").addEventListener("submit", (e) => {
      reached++;
      e.preventDefault();
    });
    byId("go").click();
    expect(reached).toBe(1);
    expect(engine.blockedSubmits()).toEqual([]);
  });

  test("Marketing box: an optional opt-in is not ticked even when legal declarations are accepted, and the matcher is not asked", async () => {
    mount(`<label><input type="checkbox" id="m"> Keep me informed about future roles</label>${PAD}`);
    const host = fakeHost();
    const s = summaryOf(await runFill(makeEngine(host), { acceptLegalDeclarations: true }));
    expect(byId("m").checked).toBe(false);
    expect(host.matchRequests).toHaveLength(0);
    expect(s.rows.find((r) => r.kind === "checkbox")?.outcome).toBe("skipped");
  });

  test("Payment field: a control with a card-number autocomplete token is neither read nor written", async () => {
    mount(`<label for="n">First name</label><input id="n"><label for="cc">Card</label><input id="cc" autocomplete="cc-number" value="4111 1111 1111 1111">
      <label for="id">Social security number</label><input id="id" value="000-00-0000">${PAD}`);
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    expect(JSON.stringify(s)).not.toContain("4111");
    expect(JSON.stringify(s)).not.toContain("000-00");
    expect(s.rows.map((r) => r.label)).toEqual(["First name", "Referral code"]);
  });

  test("Years of experience with Swift is read and written while a SWIFT code or BIC stays sensitive", async () => {
    mount(`<label for="y">Years of experience with Swift</label><input id="y" type="number">
      <label for="s">SWIFT code</label><input id="s" value="EXAMPLEXXX"><label for="b">BIC</label><input id="b" value="EXAMPLEBIC">${PAD}`);
    const s = summaryOf(await runFill(makeEngine(fakeHost({ "years of experience with swift": { outcome: "filled", value: "4" } }))));
    expect(byId("y").value).toBe("4");
    expect(rowFor(s, "Years of experience").outcome).toBe("filled");
    expect(s.rows.map((r) => r.label)).toEqual(["Years of experience with Swift", "Referral code"]);
    expect(JSON.stringify(s)).not.toContain("EXAMPLE");
  });

  test("Date of birth and self-identification controls may be answered but their values never leave the page", async () => {
    mount(`<label for="g">Gender</label><select id="g"><option value="">Select</option><option>Woman</option><option>Man</option></select>
      <label for="d">Date of birth</label><input id="d" value="1990-01-01">${PAD}`);
    const s = summaryOf(await runFill(makeEngine(fakeHost({ gender: { outcome: "filled", value: "Woman" } }))));
    expect(byId("g").value).toBe("Woman");
    const row = s.rows.find((r) => r.label === "Gender");
    expect(row?.outcome).toBe("filled");
    expect(row?.value).toBeUndefined();
    expect(JSON.stringify(s)).not.toContain("1990");
  });
});

describe("Phone label lookup", () => {
  test("The climb stops at a container that holds a second control", () => {
    mount(`<div id="outer"><p>Format 202-555-0142</p><div id="inner"><input id="p"><input id="other"></div></div>`);
    expect(phoneHint(byId("p"), "Phone")).not.toContain("202-555-0142");
  });
});
