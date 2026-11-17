// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Question text, required flag and selector resolution; Option and label normalisation;
// Widget drivers for custom controls (geo typeahead, phone country-code picker).
// Synthetic copies of the markup three hosted boards rendered in the live dry
// run with the demo identity: Lever's wrapping labels with status lines and its
// location search, Greenhouse's asynchronous city search and its phone fieldset
// whose dial-code picker is labelled only "Country".
import { describe, expect, test } from "vitest";
import { phoneCountryChooser } from "../src/choosers";
import { showsDialCode } from "../src/phone-country";
import type { OptionRow } from "../src/popup";
import { scanControls } from "../src/scan";
import { byId, fakeHost, makeEngine, mount, rowFor, runFill, summaryOf, write } from "./helpers";

const rows = (...labels: string[]): OptionRow[] => labels.map((label) => ({ el: document.createElement("div"), label, value: "", selected: false, disabled: false }));

/** Lever's application rows: the label wraps the title, the field, and lines a stylesheet keeps hidden until they apply. */
function leverForm(places: string[]): { input: HTMLInputElement; hidden: HTMLInputElement; clicks: string[] } {
  mount(`<form id="application-form"><ul>
<li class="application-question resume"><label><div class="application-label">Resume/CV <span class="required">✱</span></div>
 <div class="application-field"><a href="#" class="postings-btn template-btn-utility"><span class="filename"></span><span class="default-label">ATTACH RESUME/CV</span>
 <input class="application-file-input" id="resume-upload-input" name="resume" type="file"></a>
 <span class="resume-upload-failure" style="display:none"><div>Couldn't auto-read resume.</div></span>
 <span class="resume-upload-working" style="display:none"><div>Analyzing resume...</div></span>
 <span class="resume-upload-success" style="display:none"><div>Success!</div></span></div></label></li>
<li class="application-question"><label><div class="application-label">Full name<span class="required">✱</span></div><div class="application-field"><input type="text" name="name" required></div></label></li>
<li class="application-question"><label><div class="application-label">Current location <span class="required">✱</span></div>
 <div class="application-field"><input class="location-input" id="location-input" type="text" maxlength="100" name="location" required>
 <input id="selected-location" type="hidden" name="selectedLocation">
 <div class="dropdown-container"><div class="dropdown-results"></div>
 <div class="dropdown-no-results" style="display:none">No location found. Try entering a different location</div>
 <div class="dropdown-loading-results" style="display:none"><span>Loading</span></div></div></div></label></li>
</ul></form>`);
  const input = byId("location-input");
  const hidden = byId("selected-location");
  const results = document.querySelector(".dropdown-results") as HTMLElement;
  const clicks: string[] = [];
  input.addEventListener("keydown", () =>
    setTimeout(() => {
      const q = input.value.toLowerCase();
      results.innerHTML = "";
      if (!q) return;
      for (const place of places.filter((p) => p.toLowerCase().startsWith(q))) {
        const row = document.createElement("div");
        row.className = "dropdown-location";
        row.textContent = place;
        row.addEventListener("click", () => {
          clicks.push(place);
          input.value = place;
          hidden.value = JSON.stringify({ name: place });
          results.innerHTML = "";
        });
        results.append(row);
      }
    }, 5),
  );
  return { input, hidden, clicks };
}

/**
 * Greenhouse's city search: a react-select whose menu is empty until something
 * is typed, then shows a loading notice and the suggestions of a remote lookup.
 */
function greenhouseCitySearch(places: string[]): { input: HTMLInputElement; chosen: () => string } {
  mount(`<div class="field-wrapper"><div class="select"><div class="select__container">
<label id="candidate-location-label" for="candidate-location" class="label select__label">Location (City)<span aria-hidden="true">*</span></label>
<div class="select-shell"><span id="react-select-candidate-location-live-region"></span><span aria-live="polite" role="log"></span>
<div class="select__control"><div class="select__value-container"><div class="select__placeholder"></div>
<div class="select__input-container"><input class="select__input" id="candidate-location" type="text" role="combobox" aria-autocomplete="list" aria-expanded="false" aria-haspopup="true" aria-labelledby="candidate-location-label" aria-required="true" value=""></div></div>
<div class="select__indicators"></div></div></div></div></div></div>
<div class="field-wrapper"><label for="pad">Referral code</label><input id="pad" value="REF-1"></div>`);
  const input = byId("candidate-location");
  const control = document.querySelector(".select__control") as HTMLElement;
  const valueBox = document.querySelector(".select__value-container") as HTMLElement;
  let chosen = "";
  let menu: HTMLElement | null = null;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const close = () => {
    menu?.remove();
    menu = null;
    input.setAttribute("aria-expanded", "false");
    input.removeAttribute("aria-controls");
  };
  const render = () => {
    close();
    menu = document.createElement("div");
    menu.className = "select__menu";
    const list = document.createElement("div");
    list.className = "select__menu-list";
    list.id = "react-select-candidate-location-listbox";
    list.setAttribute("role", "listbox");
    menu.append(list);
    control.after(menu);
    input.setAttribute("aria-expanded", "true");
    input.setAttribute("aria-controls", list.id);
    if (!input.value) return;
    list.innerHTML = `<div class="select__menu-notice select__menu-notice--loading">Loading...</div>`;
    const q = input.value.toLowerCase();
    clearTimeout(timer);
    timer = setTimeout(() => {
      list.innerHTML = "";
      places.filter((p) => p.toLowerCase().includes(q)).forEach((place, i) => {
        const opt = document.createElement("div");
        opt.className = "select__option";
        opt.id = `react-select-candidate-location-option-${i}`;
        opt.setAttribute("role", "option");
        opt.textContent = place;
        opt.addEventListener("click", () => {
          chosen = place;
          input.value = "";
          valueBox.querySelector(".select__single-value")?.remove();
          const single = document.createElement("div");
          single.className = "select__single-value";
          single.textContent = place;
          valueBox.prepend(single);
          close();
        });
        list.append(opt);
      });
    }, 15);
  };
  input.addEventListener("input", render);
  control.addEventListener("mousedown", () => {
    if (!menu) render();
  });
  input.addEventListener("keydown", (e) => {
    if ((e as KeyboardEvent).key === "Escape") close();
  });
  return { input, chosen: () => chosen };
}

/** Greenhouse's phone fieldset: a react-select dial-code picker labelled "Country" beside the tel input. */
function greenhousePhone(): { chosen: () => string; display: () => string } {
  mount(`<fieldset class="phone-input"><legend class="visually-hidden">Phone</legend>
<div class="phone-input__country"><div class="select"><div class="select__container">
<label id="country-label" for="country" class="label select__label">Country<span aria-hidden="true">*</span></label>
<div class="select-shell"><div class="select__control"><div class="select__value-container"><div class="select__placeholder"></div>
<div class="select__input-container"><input class="select__input" id="country" type="text" role="combobox" aria-autocomplete="list" aria-expanded="false" aria-haspopup="true" aria-labelledby="country-label" aria-required="true" value=""></div></div></div></div></div></div></div>
<div class="phone-input__phone"><label id="phone-label" for="phone" class="label">Phone<span aria-hidden="true">*</span></label><input id="phone" type="tel" aria-label="Phone"></div></fieldset>
<div class="field-wrapper"><label for="pad">Referral code</label><input id="pad" value="REF-1"></div>`);
  const options: [string, string][] = [
    ["ca", "Canada +1"],
    ["um", "United States Minor Outlying Islands +1"],
    ["gb", "United Kingdom +44"],
    ["us", "United States +1"],
  ];
  const input = byId("country");
  const control = document.querySelector(".select__control") as HTMLElement;
  const valueBox = document.querySelector(".select__value-container") as HTMLElement;
  let chosen = "";
  let menu: HTMLElement | null = null;
  const close = () => {
    menu?.remove();
    menu = null;
    input.setAttribute("aria-expanded", "false");
  };
  const open = () => {
    if (menu) return;
    menu = document.createElement("div");
    menu.className = "select__menu";
    const list = document.createElement("div");
    list.className = "select__menu-list";
    list.id = "react-select-country-listbox";
    list.setAttribute("role", "listbox");
    options.forEach(([code, text], i) => {
      const opt = document.createElement("div");
      opt.className = "select__option";
      opt.id = `react-select-country-option-${i}`;
      opt.setAttribute("role", "option");
      opt.innerHTML = `<div class="iti__flag iti__${code}"></div>${text}`;
      opt.addEventListener("click", () => {
        chosen = text;
        valueBox.querySelector(".select__single-value")?.remove();
        const single = document.createElement("div");
        single.className = "select__single-value";
        // The committed value shows the flag and the dial code only, the plus sign and the digits as separate text nodes.
        single.innerHTML = `<div class="iti__flag iti__${code}"></div><span></span>`;
        (single.querySelector("span") as HTMLElement).append("+", (/\+(\d+)/.exec(text)?.[1] ?? ""));
        valueBox.prepend(single);
        close();
      });
      list.append(opt);
    });
    menu.append(list);
    control.after(menu);
    input.setAttribute("aria-expanded", "true");
  };
  control.addEventListener("mousedown", open);
  input.addEventListener("keydown", (e) => {
    if ((e as KeyboardEvent).key === "ArrowDown") open();
    if ((e as KeyboardEvent).key === "Escape") close();
  });
  return { chosen: () => chosen, display: () => (document.querySelector(".select__single-value")?.textContent ?? "").trim() };
}

describe("Question text, required flag and selector resolution: live dry run boards", () => {
  test("A wrapping label reads only the question before its control: no button text, status lines or required glyph", () => {
    leverForm([]);
    const infos = scanControls(document);
    const resume = infos.find((i) => i.el.id === "resume-upload-input");
    const location = infos.find((i) => i.el.id === "location-input");
    expect(resume?.label.text).toBe("Resume/CV");
    expect(location?.label.text).toBe("Current location");
    expect(location?.label.marked).toBe(true);
  });

  test("Live regions, status lines and hidden notices inside a label are never part of the question", () => {
    mount(`<label for="city">City <span aria-live="polite">Searching</span><span role="status">3 results</span><span hidden>No match</span><span style="display:none">Loading</span> ✱ (as on your lease)</label><input id="city" name="city">
<label for="pad">Referral code</label><input id="pad" value="REF-1">`);
    const info = scanControls(document).find((i) => i.el.id === "city");
    expect(info?.label.text).toBe("City (as on your lease)");
  });

  test("A wrapping label with nothing before its control still reads whole", () => {
    mount(`<label><input id="other" name="other"> Other (please specify)</label><label for="pad">Referral code</label><input id="pad" value="REF-1">`);
    expect(scanControls(document).find((i) => i.el.id === "other")?.label.text).toBe("Other (please specify)");
  });
});

describe("Widget drivers for custom controls: location searches", () => {
  test("Lever's Current location is asked by its clean label, the city is typed and the suggestion with its region and country is committed", async () => {
    const lever = leverForm(["Durham, England, GBR", "Durham, NC, USA", "Durham, NH, USA"]);
    const host = fakeHost({ "current location": { outcome: "filled", value: "Durham, North Carolina, United States" } });
    const s = summaryOf(await runFill(makeEngine(host)));
    expect(host.matchRequests.flatMap((r) => r.questions.map((q) => q.text))).toContain("Current location");
    expect(rowFor(s, "Current location")).toMatchObject({ outcome: "filled", optionLabel: "Durham, NC, USA" });
    expect(lever.clicks).toEqual(["Durham, NC, USA"]);
    expect(JSON.parse(lever.hidden.value)).toEqual({ name: "Durham, NC, USA" });
  });

  test("Lever's location search with no suggestion for the region is left to the person and nothing is clicked", async () => {
    const lever = leverForm(["Durham, England, GBR", "Durham, NH, USA"]);
    const r = await write("location-input", "Durham, North Carolina, United States");
    expect(r).toMatchObject({ outcome: "abstained", reason: "option-not-found" });
    expect(lever.clicks).toEqual([]);
    expect(lever.hidden.value).toBe("");
  });

  test("Greenhouse's asynchronous city search is asked without options, typed, and the matching suggestion committed", async () => {
    const gh = greenhouseCitySearch(["Durham, North Carolina, United States", "Durham, England, United Kingdom", "Durham, New Hampshire, United States"]);
    const host = fakeHost({ "location (city)": { outcome: "filled", value: "Durham, North Carolina, United States" } });
    const s = summaryOf(await runFill(makeEngine(host)));
    const asked = host.matchRequests.flatMap((r) => r.questions).find((q) => q.text === "Location (City)");
    expect(asked).toBeDefined();
    expect(asked?.options ?? []).toEqual([]);
    expect(rowFor(s, "Location (City)")).toMatchObject({ outcome: "filled", optionLabel: "Durham, North Carolina, United States" });
    expect(gh.chosen()).toBe("Durham, North Carolina, United States");
  });

  test("Greenhouse's city search without a suggestion that matches the region abstains and never takes the first one", async () => {
    const gh = greenhouseCitySearch(["Durham, England, United Kingdom", "Durham, New Hampshire, United States"]);
    const r = await write("candidate-location", "Durham, North Carolina, United States");
    expect(r).toMatchObject({ outcome: "abstained", reason: "option-not-found" });
    expect(gh.chosen()).toBe("");
    expect(gh.input.value).toBe("");
  });
});

describe("Option and label normalisation: dial codes", () => {

  test("Greenhouse's dial-code picker labelled Country inside the phone fieldset commits the country's own row", async () => {
    const gh = greenhousePhone();
    const r = await write("country", "United States");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "United States +1" });
    expect(gh.chosen()).toBe("United States +1");
    expect(gh.display().replace(/\s/g, "")).toBe("+1");
    expect(phoneCountryChooser("United States")(rows("Canada +1", "United States +1"))?.label).toBe("United States +1");
    expect(showsDialCode("+ 1", "United States +1")).toBe(true);
    expect(showsDialCode("+1242", "United States +1")).toBe(false);
  });
});
