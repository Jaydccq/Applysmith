// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Widget drivers for custom controls (typeahead, geo typeahead, address autocomplete, places
// autocomplete, phone country-code picker, cascades); Listbox open, search, score and commit ladder.
import { describe, expect, test } from "vitest";
import { countryChooser, geoChooser, phoneCountryChooser, searchSeeds, seedKind } from "../src/choosers";
import type { OptionRow } from "../src/popup";
import { byId, mount, write } from "./helpers";
import { mountReactSelect } from "./library-fixtures";
import { mountCombobox } from "./widgets-fixtures";

const rows = (...labels: string[]): OptionRow[] => labels.map((label) => ({ el: document.createElement("li"), label, value: "", selected: false, disabled: false }));
const label = (r: OptionRow | null) => r?.label ?? null;

describe("Widget drivers for custom controls: typeahead", () => {
  test("A typeahead is typed one key at a time with key events as well as input events, and the matching suggestion is chosen", async () => {
    mount("");
    const c = mountCombobox({ id: "school", label: "School", suggest: (q) => (q.length >= 4 ? ["Example University", "Example College"] : []) });
    const inputs: string[] = [];
    c.input.addEventListener("input", () => inputs.push(c.input.value));
    const r = await write("school", "Example University");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Example University" });
    expect(c.chosen()).toBe("Example University");
    expect(c.keys.filter((k) => k.startsWith("keydown:")).slice(0, 3)).toEqual(["keydown:E", "keydown:x", "keydown:a"]);
    expect(c.keys).toContain("keyup:E");
    expect(inputs[0]).toBe("E");
    expect(c.clicked).toEqual(["Example University"]);
  });

  test("A search that highlights its first suggestion with aria-selected commits that row with Enter, never the row below it", async () => {
    mount("");
    const c = mountCombobox({
      id: "loc",
      label: "Current location",
      highlightFirst: true,
      suggest: () => ["Sampleton, Example State, Exampleland", "North Sampleton, Example State, Exampleland"],
    });
    const r = await write("loc", "Sampleton, Example State, Exampleland");
    expect(r).toMatchObject({ outcome: "filled" });
    expect(c.chosen()).toBe("Sampleton, Example State, Exampleland");
  });

  test("A widget that searches on key down is served by key events", async () => {
    mount("");
    const c = mountCombobox({ id: "loc", label: "Office", searchOn: "keydown", suggest: (q) => (q.length >= 3 ? ["Durham Office", "Raleigh Office"] : []) });
    const r = await write("loc", "Durham Office");
    expect(r.outcome).toBe("filled");
    expect(c.chosen()).toBe("Durham Office");
  });

  test("Typeahead seed shortened: no suggestion for the full text, the shorter seed finds the suggestion that holds the city and state", async () => {
    mount("");
    const c = mountCombobox({
      id: "office",
      label: "Office",
      suggest: (q) => (q === "Durham" ? ["Durham, England, United Kingdom", "Durham, NC, USA"] : []),
    });
    const r = await write("office", "Durham, NC, United States");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Durham, NC, USA" });
    expect(c.queries).toContain("Durham, NC, United States");
    expect(c.queries.at(-1)).toBe("Durham");
  });

  test("A typeahead abstains rather than pick a non-matching first option", async () => {
    mount("");
    const c = mountCombobox({ id: "school", label: "School", suggest: () => ["Sample Institute", "Another Institute"] });
    const r = await write("school", "Example University");
    expect(r).toMatchObject({ outcome: "abstained", reason: "option-not-found" });
    expect(c.clicked).toEqual([]);
    expect(c.chosen()).toBe("");
  });

  test("A search box that lists only what contains the typed words finds the option that words a decline differently", async () => {
    mount("");
    const options = ["Asian", "White", "Choose not to disclose"];
    const c = mountCombobox({ id: "race", label: "Please identify your race", suggest: (q) => options.filter((o) => o.toLowerCase().includes(q.toLowerCase())) });
    const r = await write("race", "Decline to self-identify");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Choose not to disclose" });
    expect(c.chosen()).toBe("Choose not to disclose");
    expect(c.queries).toContain("Decline to self-identify");
  });

  test("A search box that lists nothing for a decline leaves the box blank and abstains", async () => {
    mount("");
    const options = ["Asian", "White", "Two or more races"];
    const c = mountCombobox({ id: "race", label: "Please identify your race", suggest: (q) => options.filter((o) => o.toLowerCase().includes(q.toLowerCase())) });
    const r = await write("race", "Decline to self-identify");
    expect(r).toMatchObject({ outcome: "abstained", reason: "listbox-not-opened" });
    expect(c.clicked).toEqual([]);
    expect(c.chosen()).toBe("");
  });

  describe("A list asked without its options is answered with the first of a ranking (Applied Intuition's Ashby language search)", () => {
    const languages = ["Bash", "C", "C++", "Golang", "Java", "Not applicable", "Other", "Python", "React", "Typescript/Javascript"];
    const mountLanguages = () => {
      mount("");
      return mountCombobox({
        id: "lang",
        label: "What is your preferred programming language for interviews?",
        suggest: (q) => {
          const hits = languages.filter((o) => o.toLowerCase().includes(q.toLowerCase()));
          return hits.length ? hits : ["No results"];
        },
      });
    };

    test("A search that holds none of the answer is searched for the next of the ranking", async () => {
      const c = mountLanguages();
      const r = await write("lang", "Python 3", { optionLabel: "Python 3", alternatives: ["Python", "Python 2", "Java"] });
      expect(r).toMatchObject({ outcome: "filled", optionLabel: "Python" });
      expect(c.chosen()).toBe("Python");
      expect(c.queries).toContain("Python 3");
      expect(c.queries).not.toContain("Python 2");
    });

    test("Without a ranking, or when the search holds none of it, the box stays blank", async () => {
      let c = mountLanguages();
      expect(await write("lang", "Python 3", { optionLabel: "Python 3" })).toMatchObject({ outcome: "abstained", reason: "option-not-found" });
      expect(c.chosen()).toBe("");
      c = mountLanguages();
      expect(await write("lang", "Rust", { optionLabel: "Rust", alternatives: ["Haskell"] })).toMatchObject({ outcome: "abstained", reason: "option-not-found" });
      expect(c.clicked).toEqual([]);
      expect(c.chosen()).toBe("");
    });
  });

  test("A seed shorter than the widget's minquerylength is refused", async () => {
    mount("");
    const c = mountCombobox({ id: "pc", label: "Postcode", minQuery: 6, suggest: () => ["AB1 2CD"] });
    const r = await write("pc", "AB12");
    expect(r.outcome).not.toBe("filled");
    expect(c.queries).toEqual([]);
  });

  test("A search that never lists anything gets its box back as it was, with a minimum query length or on a paginated picklist: the seed typed is no answer", async () => {
    mount("");
    const c = mountCombobox({ id: "pc", label: "Postcode", minQuery: 3, suggest: () => [] });
    expect(await write("pc", "AB1 2CD")).toMatchObject({ outcome: "failed", reason: "listbox-not-opened" });
    expect(c.queries).toContain("AB1 2CD");
    expect(c.chosen()).toBe("");
    mount("");
    const holder = document.createElement("div");
    holder.className = "paginatedPicklistContainer";
    document.body.appendChild(holder);
    const p = mountCombobox({ id: "country", label: "Country of residence", parent: holder, suggest: () => [] });
    p.input.value = "Canada";
    expect(await write("country", "United States")).toMatchObject({ outcome: "failed", reason: "listbox-not-opened" });
    expect(p.queries).toContain("United States");
    expect(p.chosen()).toBe("Canada");
  });

  test("Suggestions arrive late: the driver waits up to its limit and takes a match the moment it appears", async () => {
    mount("");
    const c = mountCombobox({ id: "sch", label: "School", delayMs: 25, suggest: () => ["Example University"] });
    const r = await write("sch", "Example University");
    expect(r.outcome).toBe("filled");
    expect(c.chosen()).toBe("Example University");
  });

  test("An ARIA 1.1 wrapper's inner input is cleared, typed into and chosen from; the wrapper itself is never clicked", async () => {
    mount("");
    const c = mountCombobox({ id: "w", label: "Skill", wrapper: true, suggest: (q) => (q.length ? ["TypeScript", "Typewriting"] : []) });
    c.input.value = "stale";
    const wrapper = c.input.parentElement as HTMLElement;
    let wrapperClicks = 0;
    wrapper.addEventListener("click", (e) => {
      if (e.target === wrapper) wrapperClicks++;
    });
    const r = await write("w", "TypeScript");
    expect(r.outcome).toBe("filled");
    expect(c.chosen()).toBe("TypeScript");
    expect(c.queries.filter(Boolean)[0]).toBe("T");
    expect(wrapperClicks).toBe(0);
  });
});

describe("ARIA 1.1 wrapper without aria-autocomplete", () => {
  test("A wrapper around a text box is still typed into: suggestions mount only once the box has text", async () => {
    mount("");
    const c = mountCombobox({ id: "w", label: "Skill", wrapper: true, suggest: (q) => (q.length ? ["TypeScript"] : []) });
    c.input.removeAttribute("aria-autocomplete");
    const r = await write("w", "TypeScript");
    expect(r.outcome).toBe("filled");
    expect(c.keys).toContain("keydown:T");
  });
});

describe("ARIA 1.1 wrapper commit recognition", () => {
  test("A typed seed that survives while the wrapper stays expanded is not a commit", async () => {
    mount("");
    const c = mountCombobox({ id: "w", label: "Skill", wrapper: true, suggest: () => ["TypeScript"] });
    // Clicking the suggestion does nothing on this widget: only the typed seed is left in the box.
    c.list.addEventListener("click", (e) => e.stopImmediatePropagation(), true);
    const r = await write("w", "TypeScript");
    expect(r.outcome).toBe("failed");
    expect(r.reason).toBe("write-not-committed");
  });
});

describe("Geo typeahead option choice", () => {
  const pick = (target: string, ...labels: string[]) => label(geoChooser(target)(rows(...labels)));

  test("Abbreviations expand only when they are the whole comma segment", () => {
    expect(pick("Raleigh, NC, USA", "Raleigh, North Carolina, United States", "Raleigh, England, United Kingdom")).toBe("Raleigh, North Carolina, United States");
    // NCSU is not a whole segment that names a state, so it expands to nothing.
    expect(pick("Raleigh, NCSU", "Raleigh, North Carolina State University", "Raleigh, England")).toBeNull();
  });
  test("One table names a country's other names for every chooser: UAE, Great Britain and the long form of the United States", () => {
    expect(pick("Dubai, UAE", "Dubai, Saudi Arabia", "Dubai, United Arab Emirates")).toBe("Dubai, United Arab Emirates");
    expect(pick("London, Great Britain", "London, Ontario, Canada", "London, United Kingdom")).toBe("London, United Kingdom");
    expect(pick("Durham, NC, United States", "Durham, England, United Kingdom", "Durham, NC, United States of America")).toBe("Durham, NC, United States of America");
    expect(label(countryChooser("UAE", true)(rows("United Kingdom", "United Arab Emirates")))).toBe("United Arab Emirates");
    expect(label(phoneCountryChooser("UAE")(rows("United Kingdom +44", "United Arab Emirates +971")))).toBe("United Arab Emirates +971");
  });
  test("A bare city with several hits abstains; a single hit is taken", () => {
    expect(pick("Exampleville", "Exampleville, NC", "Exampleville, England")).toBeNull();
    expect(pick("Exampleville", "Exampleville, NC")).toBe("Exampleville, NC");
  });
  test("No qualifier match among several city hits abstains; ties go to the shortest label", () => {
    expect(pick("Exampleville, Oregon", "Exampleville, NC", "Exampleville, England")).toBeNull();
    expect(pick("Exampleville, NC", "Exampleville, North Carolina, United States", "Exampleville, North Carolina")).toBe("Exampleville, North Carolina");
  });
  test("An exact full-string match wins", () => {
    expect(pick("Exampleville, NC", "Exampleville, NC, USA", "Exampleville, NC")).toBe("Exampleville, NC");
  });
  test("Country-only target: US cities commit nothing, an exact country row is allowed", () => {
    expect(pick("United States", "Raleigh, NC, United States", "Durham, NC, United States")).toBeNull();
    expect(pick("United States", "Raleigh, NC, United States", "United States")).toBe("United States");
  });
  test("GitLab's country of residence list spells the country another way: the row that is another of its names", () => {
    const countries = ["United Arab Emirates", "United Kingdom", "United States Minor Outlying Islands", "United States of America", "Uruguay"];
    expect(pick("United States", ...countries)).toBe("United States of America");
    expect(pick("Charleston, SC, United States", ...countries)).toBe("United States of America");
    expect(pick("United States", "United States Minor Outlying Islands", "Uruguay")).toBeNull();
  });
  test("A menu of countries takes the target's country row, never another city's country", () => {
    expect(pick("Charleston, South Carolina, United States", "United States", "Palestine")).toBe("United States");
    expect(pick("Charleston, SC, USA", "United States", "Canada")).toBe("United States");
    expect(pick("Charleston, South Carolina, United States", "Columbia, SC, United States", "Greenville, SC, United States")).toBeNull();
    expect(pick("Charleston, South Carolina", "United States")).toBeNull();
  });
  test("A menu of states takes the one row naming the target's state and nothing besides its country, never another city's row", () => {
    expect(pick("Charleston, South Carolina, United States", "Prefer Not to Disclose", "US - North Carolina", "US - South Carolina")).toBe("US - South Carolina");
    expect(pick("Charleston, SC, USA", "US - South Carolina", "US - Texas")).toBe("US - South Carolina");
    expect(pick("Charleston, South Carolina, United States", "Columbia, SC, United States")).toBeNull();
    expect(pick("Atlanta, Georgia, United States", "US - Georgia", "Georgia")).toBeNull();
  });
  test("Optiver's Greenhouse Current Location lists states: the state row is chosen", async () => {
    mount("");
    const states = ["Prefer Not to Disclose", ...Array.from({ length: 120 }, (_, i) => `Region ${String(i).padStart(3, "0")}`), "US - North Carolina", "US - South Carolina", "US - Texas"];
    const rs = mountReactSelect({ inputId: "question_39092885002", label: "Current Location", options: states });
    const r = await write("question_39092885002", "Charleston, South Carolina, United States");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "US - South Carolina" });
    expect(rs.value()).toEqual(["US - South Carolina"]);
  });
  test("Runway's Ashby location menu lists countries: the city finds nothing, the country is chosen", async () => {
    mount("");
    const countries = ["United States", "United Kingdom", "Palestine", "Canada"];
    const c = mountCombobox({ id: "loc", label: "Location", suggest: (q) => countries.filter((x) => x.toLowerCase().includes(q.toLowerCase())) });
    const r = await write("loc", "Charleston, South Carolina, United States");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "United States" });
    expect(c.chosen()).toBe("United States");
  });
  test("n8n's Ashby location search lists regions: the city finds nothing, the state row is chosen before the country", async () => {
    mount("");
    const regions = ["South Carolina, United States", "North Carolina, United States", "United States", "United States Minor Outlying Islands", "South, Cameroon"];
    const c = mountCombobox({ id: "loc", label: "Where are you currently based?", suggest: (q) => regions.filter((x) => x.toLowerCase().startsWith(q.toLowerCase())) });
    const r = await write("loc", "Charleston, South Carolina, United States");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "South Carolina, United States" });
    expect(c.chosen()).toBe("South Carolina, United States");
  });
  test("A committed value that matches neither the label nor the city seed is cleared", async () => {
    mount("");
    const c = mountCombobox({ id: "city", label: "City", suggest: () => ["Exampleville, NC"] });
    // The widget writes something unrelated to the box when a suggestion is clicked.
    c.list.addEventListener("click", (e) => {
      e.stopImmediatePropagation();
      c.input.value = "Somewhere Else";
    }, true);
    const r = await write("city", "Exampleville, NC");
    expect(r.outcome).toBe("failed");
    expect(c.input.value).toBe("");
  });
  test("The search seed is the level word for degrees, the city, region and country for locations and the country name for phone-country labels", () => {
    expect(searchSeeds(seedKind("Highest degree"), "Bachelor of Science")).toEqual(["Bachelor"]);
    expect(searchSeeds(seedKind("Current location"), "Durham, NC, United States")).toEqual(["Durham", "North Carolina", "United States"]);
    expect(searchSeeds(seedKind("Current location"), "Durham, NC")).toEqual(["Durham", "North Carolina"]);
    expect(searchSeeds(seedKind("Current location"), "Durham, United States")).toEqual(["Durham", "United States"]);
    expect(searchSeeds(seedKind("Phone country code"), "United States (+1)")).toEqual(["United States"]);
    expect(searchSeeds(seedKind("Discipline"), "Applied Mathematics")).toEqual(["Applied Mathematics", "Mathematics"]);
    expect(searchSeeds(seedKind("Major"), "Electrical and Computer Engineering")).toEqual(["Electrical and Computer Engineering", "Computer Engineering", "Engineering"]);
    expect(searchSeeds(seedKind("Office"), "Durham, NC, United States")).toEqual(["Durham, NC, United States", "Durham, NC", "Durham"]);
  });
});

describe("Address autocomplete", () => {
  const hints = { city: "Durham", state: "NC", postalCode: "27701" };
  test("Address decoys: suggestions in other cities and states are never committed and the control abstains", async () => {
    mount("");
    const c = mountCombobox({
      id: "street",
      label: "Street address",
      suggest: () => ["1 Example Street, Raleigh, NC 27601", "1 Example Street, Durham, SC 29501", "1 Example Street, Austin, TX 78701"],
    });
    const r = await write("street", "1 Example Street", { address: hints });
    expect(r).toMatchObject({ outcome: "abstained", reason: "option-not-found" });
    expect(c.clicked).toEqual([]);
    expect(c.chosen()).toBe("");
  });

  test("A suggestion is committed only when it holds the city and the state, preferring the one with the postal code", async () => {
    mount("");
    const c = mountCombobox({
      id: "street",
      label: "Street address",
      delayMs: 5,
      suggest: () => ["1 Example Street, Raleigh, NC 27601", "1 Example Street, Durham, NC 27705", "1 Example Street, Durham, NC 27701"],
    });
    const r = await write("street", "1 Example Street", { address: hints });
    expect(r.outcome).toBe("filled");
    expect(c.chosen()).toBe("1 Example Street, Durham, NC 27701");
  });

  test("In a full fill the profile lane types the street and commits only the suggestion in the applicant's city and state", async () => {
    mount("");
    const c = mountCombobox({
      id: "street",
      label: "Address",
      suggest: () => ["1 Example Street, Durham, SC 29501", "1 Example Street, Durham, NC 27701"],
    });
    const { fakeHost, makeEngine, runFill, summaryOf, rowFor } = await import("./helpers");
    document.body.insertAdjacentHTML("beforeend", `<label for="x">Referral code</label><input id="x">`);
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    expect(c.chosen()).toBe("1 Example Street, Durham, NC 27701");
    expect(rowFor(s, "address")).toMatchObject({ lane: "profile", outcome: "filled" });
  });
});

describe("Phone country chooser", () => {
  test("An option scores 2 when its country part equals the country and 1 when it merely contains it; an exact match stops the search", () => {
    const choose = phoneCountryChooser("United States");
    expect(label(choose(rows("United States Minor Outlying Islands +1", "United States of America +1")))).toBe("United States of America +1");
    expect(label(choose(rows("United States Minor Outlying Islands +1", "Canada +1")))).toBe("United States Minor Outlying Islands +1");
    expect(label(choose(rows("United States")))).toBeNull();
    expect(label(phoneCountryChooser("United States")(rows("USA +1")))).toBe("USA +1");
  });
});

describe("Places autocomplete", () => {
  /** A widget that reads the legacy keyCode, fills a dependent box on Enter and breaks when a suggestion is clicked. */
  function places(opts: { withContainer?: boolean } = {}) {
    mount(`<label for="addr">Street address</label><input id="addr" class="pac-target-input" autocomplete="off"><label for="city">City</label><input id="city">`);
    const input = byId("addr");
    const state = { crashed: false, keys: [] as number[], committed: "" };
    if (opts.withContainer === false) return state;
    const box = document.createElement("div");
    box.className = "pac-container";
    document.body.appendChild(box);
    let active = -1;
    input.addEventListener("input", () => {
      box.innerHTML = input.value.length > 2 ? `<div class="pac-item">1 Example Street, Durham, NC</div><div class="pac-item">2 Example Street, Durham, NC</div>` : "";
      active = -1;
    });
    box.addEventListener("click", () => (state.crashed = true));
    input.addEventListener("keydown", (e) => {
      const code = (e as KeyboardEvent).keyCode;
      state.keys.push(code);
      if (code === 40) active = Math.min(1, active + 1);
      if (code === 13 && active >= 0) {
        state.committed = box.children[active]?.textContent ?? "";
        input.value = state.committed;
        byId("city").value = "Durham";
        box.innerHTML = "";
      }
    });
    return state;
  }

  test("Typed with legacy keyCode, the top suggestion is committed with ArrowDown and Enter and a suggestion is never clicked", async () => {
    const state = places();
    const r = await write("addr", "1 Example Street", { address: { city: "Durham", state: "NC" } });
    expect(r.outcome).toBe("filled");
    expect(state.crashed).toBe(false);
    expect(state.keys.filter((k) => k === 40 || k === 13)).toEqual([40, 13]);
    expect(state.keys.slice(0, 3)).toEqual([49, 32, 69]);
    expect(byId("addr").value).toBe("1 Example Street, Durham, NC");
    expect(byId("city").value).toBe("Durham");
  });

  test("Without the widget's container the typed value stands", async () => {
    places({ withContainer: false });
    const r = await write("addr", "1 Example Street", { address: {} });
    expect(r).toMatchObject({ outcome: "filled", value: "1 Example Street" });
  });
});

describe("Phone country-code picker", () => {
  function picker(options: string[], opts: { keyboardOnly?: boolean; shown?: string } = {}) {
    mount(`<div id="row"><input id="cc" placeholder="Search" role="combobox" aria-controls="cc-list" value="${opts.shown ?? ""}"><ul id="cc-list" role="listbox" hidden></ul><label for="ph">Phone number</label><input id="ph" type="tel"></div>`);
    const cc = byId("cc");
    const list = byId<HTMLElement>("cc-list") as unknown as HTMLElement;
    const state = { chosen: "", escapes: 0, opens: 0 };
    const render = () => {
      list.innerHTML = "";
      for (const o of options.filter((x) => x.toLowerCase().includes(cc.value.toLowerCase()) || cc.value === "")) {
        const li = document.createElement("li");
        li.setAttribute("role", "option");
        li.textContent = o;
        li.addEventListener("click", () => {
          state.chosen = o;
          cc.value = /\+\d+/.exec(o)?.[0] ?? o;
          list.hidden = true;
        });
        list.appendChild(li);
      }
      list.hidden = false;
    };
    if (!opts.keyboardOnly) cc.addEventListener("mousedown", () => (state.opens++, render()));
    cc.addEventListener("input", render);
    cc.addEventListener("keydown", (e) => {
      if ((e as KeyboardEvent).key === "Escape") (state.escapes++, (list.hidden = true));
      if ((e as KeyboardEvent).key === "ArrowDown" && opts.keyboardOnly && list.hidden) (state.opens++, render());
    });
    return state;
  }

  test("A searchable control beside a phone input is recognised structurally and the exact country wins over a substring hit", async () => {
    const state = picker(["United States Minor Outlying Islands +1", "United States of America +1", "Canada +1"]);
    const { findPhoneCountryPicker, commitPhoneCountry, countryOfPhone } = await import("../src/phone-country");
    const found = findPhoneCountryPicker(byId("ph"));
    expect(found?.id).toBe("cc");
    const { ctx } = await import("./helpers");
    expect(countryOfPhone("+1")).toBe("United States");
    const out = await commitPhoneCountry(found as HTMLInputElement, "United States", ctx());
    expect(out.ok).toBe(true);
    expect(state.chosen).toBe("United States of America +1");
  });

  test("The picker's country follows the phone's own calling code, and the address country only where the code allows it", async () => {
    const { countryOfPhone } = await import("../src/phone-country");
    expect(countryOfPhone("+1", "China")).toBe("United States");
    expect(countryOfPhone("+1", "Canada")).toBe("Canada");
    expect(countryOfPhone("+86", "China")).toBe("China");
    expect(countryOfPhone("", "Germany")).toBe("Germany");
    expect(countryOfPhone("")).toBeUndefined();
  });

  test("A row whose dial code is not the phone's is never committed: a +86 number beside a United States address leaves the picker alone", async () => {
    const state = picker(["United States +1", "China +86", "Canada +1"]);
    const { commitPhoneCountry } = await import("../src/phone-country");
    const { ctx } = await import("./helpers");
    const out = await commitPhoneCountry(byId("cc"), "United States", ctx(), "+86");
    expect(out.ok).toBe(false);
    expect(state.chosen).toBe("");
    expect((await commitPhoneCountry(byId("cc"), "China", ctx(), "+86")).ok).toBe(true);
    expect(state.chosen).toBe("China +86");
  });

  test("Wrong phone picker: options without a plus-and-digits shape commit nothing and the control is closed with Escape", async () => {
    const state = picker(["United States", "Canada"]);
    const { commitPhoneCountry } = await import("../src/phone-country");
    const { ctx } = await import("./helpers");
    const out = await commitPhoneCountry(byId("cc"), "United States", ctx());
    expect(out.ok).toBe(false);
    expect(state.chosen).toBe("");
    expect(state.escapes).toBeGreaterThan(0);
  });

  test("A picker that already shows a plus and digits is skipped", async () => {
    const state = picker(["United States +1"], { shown: "+44" });
    const { commitPhoneCountry } = await import("../src/phone-country");
    const { ctx } = await import("./helpers");
    expect((await commitPhoneCountry(byId("cc"), "United States", ctx())).ok).toBe(true);
    expect(state.opens).toBe(0);
  });

  test("A keyboard-only picker is opened once with ArrowDown", async () => {
    const state = picker(["United States of America +1", "Canada +1"], { keyboardOnly: true });
    const { commitPhoneCountry } = await import("../src/phone-country");
    const { ctx } = await import("./helpers");
    const out = await commitPhoneCountry(byId("cc"), "United States", ctx());
    expect(out.ok).toBe(true);
    expect(state.chosen).toBe("United States of America +1");
    expect(state.opens).toBe(1);
  });

  test("A control with its own phone label or an aria-labelledby is not a country picker", async () => {
    mount(`<div><label for="a">Phone country</label><input id="a" placeholder="Search"><input id="ph" type="tel" aria-label="Phone number"></div>`);
    const { findPhoneCountryPicker } = await import("../src/phone-country");
    expect(findPhoneCountryPicker(byId("ph"))).toBeNull();
  });

  // Ashby (Apex, Lime, OpenAI, live): the location autocomplete is the next
  // field's, in its own data-field-path container beside the phone's.
  test("A combobox in another field's Ashby container is not the phone's country picker", async () => {
    mount(`<form><div data-field-path="phone-q"><label for="ph">Phone</label><input id="ph" type="tel"></div>` +
      `<div data-field-path="_systemfield_location"><div>Location</div><div><input id="loc" role="combobox" placeholder="Start typing..."></div></div></form>`);
    const { findPhoneCountryPicker } = await import("../src/phone-country");
    expect(findPhoneCountryPicker(byId("ph"))).toBeNull();
  });
});

describe("Geo typeahead commit", () => {
  test("Poll for the final-query wave: stale prefix waves are ignored and the match from the final wave is committed", async () => {
    mount("");
    const c = mountCombobox({
      id: "loc",
      label: "Current location",
      delayMs: 12,
      suggest: (q) => (q === "Durham" ? ["Durham, NC, USA", "Durham, England, UK"] : q.length < 6 ? [`${q}ndee, Scotland, UK`] : []),
    });
    const r = await write("loc", "Durham, NC, United States");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Durham, NC, USA" });
    expect(c.chosen()).toBe("Durham, NC, USA");
  });

  test("Keyboard commit is tried only when the matched row is the first option", async () => {
    mount("");
    const onlyKeyboard = (c: ReturnType<typeof mountCombobox>) => {
      // Clicking a suggestion does nothing; ArrowDown then Enter commits the first one.
      c.list.addEventListener("click", (e) => e.stopImmediatePropagation(), true);
      let active = -1;
      c.input.addEventListener("keydown", (e) => {
        const key = (e as KeyboardEvent).key;
        if (key === "ArrowDown") active = 0;
        if (key === "Enter" && active === 0) c.input.value = c.list.children[0]?.textContent ?? "";
      });
    };
    const first = mountCombobox({ id: "first", label: "City", suggest: () => ["Exampleville, NC", "Exampleville, England"] });
    onlyKeyboard(first);
    expect((await write("first", "Exampleville, NC")).outcome).toBe("filled");
    expect(first.chosen()).toBe("Exampleville, NC");
    mount("");
    const second = mountCombobox({ id: "second", label: "City", suggest: () => ["Exampleville, England", "Exampleville, NC"] });
    onlyKeyboard(second);
    expect((await write("second", "Exampleville, NC")).outcome).toBe("failed");
    expect(second.chosen()).not.toBe("Exampleville, NC");
  });
});

describe("Phone country detection by typed digits", () => {
  test("A tel input that is a country combobox receives E.164 once its own dial-code seed has settled", async () => {
    mount(`<label for="ph">Mobile phone</label><input id="ph" type="tel" role="combobox" aria-haspopup="listbox" autocomplete="tel">`);
    const input = byId("ph");
    let seeded = false;
    input.addEventListener("focus", () => {
      if (seeded) return;
      seeded = true;
      setTimeout(() => (Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set as (v: string) => void).call(input, "+1"), 2);
    });
    const r = await write("ph", "+12025550142", { phone: true });
    expect(r.outcome).toBe("filled");
    expect(input.value).toBe("+12025550142");
  });
});

describe("Phone country picker in a full fill", () => {
  test("The unlabeled picker beside the phone is set to the applicant's country before the number is typed", async () => {
    mount(`<div><input id="cc" placeholder="Search" role="combobox" aria-controls="cc-list"><ul id="cc-list" role="listbox" hidden></ul><label for="ph">Phone</label><input id="ph" type="tel" autocomplete="tel"></div><label for="x">Referral code</label><input id="x">`);
    const cc = byId("cc");
    const list = document.getElementById("cc-list") as HTMLElement;
    const order: string[] = [];
    cc.addEventListener("mousedown", () => {
      list.hidden = false;
      list.innerHTML = ["Canada +1", "United States +1"].map((t) => `<li role="option">${t}</li>`).join("");
      list.querySelectorAll("li").forEach((li) => li.addEventListener("click", () => (order.push(`pick:${li.textContent}`), ((cc as HTMLInputElement).value = "+1"), (list.hidden = true))));
    });
    byId("ph").addEventListener("input", () => order.push("phone"));
    const { fakeHost, makeEngine, runFill } = await import("./helpers");
    await runFill(makeEngine(fakeHost()));
    expect(cc.value).toBe("+1");
    expect(byId("ph").value.replace(/\D/g, "")).toBe("15550100");
    // The applicant's country, not the first +1 row, and before the first keystroke of the number.
    expect(order.filter((o) => o.startsWith("pick:"))).toEqual(["pick:United States +1"]);
    expect(order.indexOf("pick:United States +1")).toBeLessThan(order.indexOf("phone"));
  });

  test("A picker the user set before the phone is written is not driven again", async () => {
    mount(`<div><input id="cc" placeholder="Search" role="combobox" aria-controls="cc-list"><ul id="cc-list" role="listbox" hidden></ul><label for="ph">Phone</label><input id="ph" type="tel" autocomplete="tel"></div><label for="x">Referral code</label><input id="x">`);
    const cc = byId("cc") as HTMLInputElement;
    let opened = 0;
    cc.addEventListener("mousedown", () => opened++);
    const { fakeHost, makeEngine, runFill } = await import("./helpers");
    const host = fakeHost();
    const match = host.match.bind(host);
    // The user picks a country while the run waits for its answers.
    host.match = async (req) => {
      const ev = new Event("input", { bubbles: true, composed: true });
      Object.defineProperty(ev, "isTrusted", { value: true });
      cc.dispatchEvent(ev);
      return match(req);
    };
    await runFill(makeEngine(host));
    expect(opened).toBe(0);
    expect(byId("ph").value.replace(/\D/g, "")).toBe("15550100");
  });
});
