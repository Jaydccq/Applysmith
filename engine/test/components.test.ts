// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Drivers for widget libraries (Dojo, Stencil, typeahead.js, intl-tel-input, switches, tag
// inputs, paginated picklists, input-backed Enter commit).
import { afterEach, describe, expect, test } from "vitest";
import { applyValue } from "../src/apply";
import type { Win } from "../src/dom";
import { writeItiCountry } from "../src/libraries";
import { MainWorld } from "../src/mainworld-client";
import { installHelper } from "../src/mainworld/helper";
import { UserEdits } from "../src/restore";
import { byId, ctx, fakeHost, infoOf, makeEngine, mount, runFill, write, uninstall } from "./helpers";
import { mountCombobox } from "./widgets-fixtures";

/** Dojo dijit.form.Select: a table trigger, a lazy popup named by widget id, a hidden input holding the code. */
function mountDijit(options: [string, string][], opts: { initial?: string; dead?: boolean } = {}) {
  mount(`<label id="st-lbl">State</label><div><table id="st" role="listbox" widgetid="dijit_form_Select_0" aria-haspopup="true" aria-labelledby="st-lbl" class="dijit dijitSelect"><tbody><tr><td class="dijitButtonText">— Please Specify —</td></tr></tbody></table><input type="hidden" name="state" value="${opts.initial ?? " "}"></div>`);
  const trigger = byId("st");
  const hidden = document.querySelector('input[type="hidden"]') as HTMLInputElement;
  const state = { gestures: [] as string[], clicked: [] as string[] };
  for (const t of ["mouseover", "mousedown", "mouseup", "click"]) trigger.addEventListener(t, () => state.gestures.push(t));
  trigger.addEventListener("click", () => {
    if (document.getElementById("dijit_form_Select_0_dropdown")) return;
    const pop = document.createElement("div");
    pop.id = "dijit_form_Select_0_dropdown";
    pop.innerHTML = options.map(([label]) => `<div class="dijitMenuItem" role="option">​${label}</div>`).join("");
    document.body.appendChild(pop);
    Array.from(pop.children).forEach((row, i) =>
      row.addEventListener("click", () => {
        state.clicked.push(options[i]?.[0] ?? "");
        if (opts.dead) return;
        hidden.value = options[i]?.[1] ?? "";
        (trigger.querySelector(".dijitButtonText") as HTMLElement).textContent = options[i]?.[0] ?? "";
        pop.remove();
      }),
    );
  });
  return { trigger, hidden, state };
}

describe("Dojo dijit select", () => {
  test("The popup mounts lazily, the option is chosen by a unique exact label and success is the hidden input taking the code", async () => {
    const d = mountDijit([["Alabama", "AL"], ["North Carolina", "NC"], ["South Carolina", "SC"]]);
    const r = await write("st", "North Carolina");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "North Carolina" });
    expect(d.hidden.value).toBe("NC");
    expect(d.state.gestures.slice(0, 4)).toEqual(["mouseover", "mousedown", "mouseup", "click"]);
  });

  test("A two-letter state code maps to the state name", async () => {
    const d = mountDijit([["North Carolina", "NC"], ["South Carolina", "SC"]]);
    expect((await write("st", "SC")).outcome).toBe("filled");
    expect(d.hidden.value).toBe("SC");
  });

  test("Ambiguity abstains and leaves the popup closed", async () => {
    const d = mountDijit([["North Carolina", "NC"], ["North Carolina (East)", "NE"]]);
    const r = await write("st", "Carolina");
    expect(r).toMatchObject({ outcome: "abstained", reason: "option-not-found" });
    expect(d.hidden.value).toBe(" ");
    expect(d.state.clicked).toEqual([]);
  });

  test("A genuine different selection is never overwritten", async () => {
    const d = mountDijit([["North Carolina", "NC"], ["South Carolina", "SC"]], { initial: "SC" });
    const r = await write("st", "North Carolina");
    expect(r).toMatchObject({ outcome: "prefilled", reason: "existing-value" });
    expect(d.hidden.value).toBe("SC");
  });

  test("A click that commits nothing on a select that already held a choice is not a write: the hidden input must change", async () => {
    const d = mountDijit([["North Carolina", "NC"], ["South Carolina", "SC"]], { initial: "SC", dead: true });
    const r = await write("st", "North Carolina", { replaceDefault: true });
    expect(d.state.clicked).toEqual(["North Carolina"]);
    expect(r).toMatchObject({ outcome: "failed", reason: "write-not-committed" });
    expect(d.hidden.value).toBe("SC");
    // The same select, when its click does commit, is filled.
    const live = mountDijit([["North Carolina", "NC"], ["South Carolina", "SC"]], { initial: "SC" });
    expect(await write("st", "North Carolina", { replaceDefault: true })).toMatchObject({ outcome: "filled", optionLabel: "North Carolina" });
    expect(live.hidden.value).toBe("NC");
  });

  test("A loose match is a whole-word one: UK is not Ukraine and Man is not Woman, so a lone near miss is not committed", async () => {
    const d = mountDijit([["Ukraine", "UA"], ["France", "FR"]]);
    expect(await write("st", "UK")).toMatchObject({ outcome: "abstained", reason: "option-not-found" });
    const g = mountDijit([["Woman", "W"], ["Prefer not to say", "X"]]);
    expect(await write("st", "Man")).toMatchObject({ outcome: "abstained", reason: "option-not-found" });
    const n = mountDijit([["None", "0"], ["One or more", "1"]]);
    expect(await write("st", "No")).toMatchObject({ outcome: "abstained", reason: "option-not-found" });
    expect([d, g, n].flatMap((x) => x.state.clicked)).toEqual([]);
    // The one option that holds the answer as whole words is still taken.
    const c = mountDijit([["US - North Carolina", "NC"], ["US - Texas", "TX"]]);
    expect(await write("st", "North Carolina")).toMatchObject({ outcome: "filled", optionLabel: "US - North Carolina" });
    expect(c.hidden.value).toBe("NC");
  });
});

describe("Stencil sdf-select-simple", () => {
  function mountSdf(labels: string[]) {
    mount(`<label for="sdf">Pronouns</label><sdf-select-simple id="sdf"></sdf-select-simple>`);
    const host = byId("sdf") as HTMLElement & { value?: string };
    const root = host.attachShadow({ mode: "open" });
    root.innerHTML = `<button class="trigger-button" type="button">Choose an option</button><div class="pane"></div>`;
    const trigger = root.querySelector(".trigger-button") as HTMLElement;
    trigger.addEventListener("click", () => {
      const pane = root.querySelector(".pane") as HTMLElement;
      pane.innerHTML = labels.map((l) => `<sdf-select-item role="option">${l}</sdf-select-item>`).join("");
      pane.querySelectorAll("sdf-select-item").forEach((item) =>
        item.addEventListener("click", () => {
          host.value = item.textContent ?? "";
          trigger.textContent = item.textContent ?? "";
          pane.innerHTML = "";
        }),
      );
    });
    return host;
  }

  test("Exact, then a unique word-boundary prefix, then a unique containment: He/Him resolves to He/Him/His", async () => {
    const host = mountSdf(["She/Her/Hers", "He/Him/His", "They/Them/Theirs"]);
    const r = await write("sdf", "He/Him");
    expect(r.outcome).toBe("filled");
    expect(host.value).toBe("He/Him/His");
  });

  test("An ambiguous containment abstains and No never matches inside Latino", async () => {
    mountSdf(["Latino", "Hispanic or Latino"]);
    const r = await write("sdf", "No");
    expect(r.outcome).toBe("abstained");
    mount("");
    mountSdf(["Group A one", "Group B one"]);
    expect((await write("sdf", "one")).outcome).toBe("abstained");
  });
});

describe("Stencil sdf-select-simple commit through the component", () => {
  const win = window as unknown as Win;
  const NONCE = "sdf-nonce-7d21c0";
  afterEach(() => uninstall());

  type Item = { value: string; label: string };
  const ITEMS: Item[] = [
    { value: "she", label: "She/Her/Hers" },
    { value: "he", label: "He/Him/His" },
  ];

  /** A host whose pane options ignore clicks; `honours` says whether its own change handler takes an item and renders it. */
  function mountClickless(honours: boolean, listed: Item[] = ITEMS) {
    mount(`<label for="sdf">Pronouns</label><sdf-select-simple id="sdf"></sdf-select-simple>`);
    const host = byId("sdf") as unknown as HTMLElement & { value?: unknown; items?: Item[] };
    host.items = listed;
    const root = host.attachShadow({ mode: "open" });
    root.innerHTML = `<button class="trigger-button" type="button">Choose an option</button><div class="pane"></div>`;
    const trigger = root.querySelector(".trigger-button") as HTMLElement;
    const events: Item[] = [];
    host.addEventListener("sdfChange", (ev) => {
      const item = (ev as CustomEvent<Item>).detail;
      events.push(item);
      if (honours) trigger.textContent = item.label;
    });
    trigger.addEventListener("click", () => {
      (root.querySelector(".pane") as HTMLElement).innerHTML = ITEMS.map((i) => `<sdf-select-item role="option" value="${i.value}">${i.label}</sdf-select-item>`).join("");
    });
    return { host, trigger, events };
  }

  async function writeWithHelper(value: string) {
    installHelper(win, NONCE, "h1");
    const main = new MainWorld(win, NONCE);
    const r = await applyValue(infoOf("sdf"), { value, replaceDefault: false }, { ctx: { ...ctx(), main }, written: new Map(), mode: "replace", acceptLegalDeclarations: false, edits: new UserEdits(document) });
    main.dispose();
    return r;
  }

  test("An option that takes no click is committed through the component's own value and sdfChange, and the display is re-read", async () => {
    const { host, trigger, events } = mountClickless(true);
    expect(await writeWithHelper("He/Him")).toMatchObject({ outcome: "filled", optionLabel: "He/Him/His" });
    expect(events).toEqual([ITEMS[1]]);
    expect(host.value).toBe(ITEMS[1]);
    expect(trigger.textContent).toBe("He/Him/His");
  });

  test("A component that ignores both the click and the change stays write-not-committed, and without the helper nothing is tried", async () => {
    const { events } = mountClickless(false);
    expect(await writeWithHelper("He/Him")).toMatchObject({ outcome: "failed", reason: "write-not-committed" });
    expect(events).toHaveLength(1);
    mountClickless(true);
    expect(await write("sdf", "He/Him")).toMatchObject({ outcome: "failed", reason: "write-not-committed" });
  });

  test("A value that is not one of the component's own items is never written", async () => {
    const { host, events } = mountClickless(true, [{ value: "she", label: "She/Her/Hers" }, { value: "they", label: "They/Them/Theirs" }]);
    expect(await writeWithHelper("He/Him")).toMatchObject({ outcome: "failed", reason: "write-not-committed" });
    expect(events).toEqual([]);
    expect(host.value).toBeUndefined();
  });
});

describe("typeahead.js", () => {
  function mountTt(suggestions: string[]) {
    mount(`<label for="school">School name</label><span class="twitter-typeahead"><input id="school" class="tt-input"><div class="tt-menu"></div></span>`);
    const input = byId("school");
    const menu = document.querySelector(".tt-menu") as HTMLElement;
    input.addEventListener("input", () => {
      menu.innerHTML = suggestions.filter((s) => s.toLowerCase().includes(input.value.toLowerCase())).map((s) => `<div class="tt-suggestion" role="option">${s}</div>`).join("");
      menu.querySelectorAll(".tt-suggestion").forEach((n) => n.addEventListener("click", () => (input.value = n.textContent ?? "")));
    });
    return input;
  }

  test("A box with no suggestions commits the typed text and verifies the box kept it", async () => {
    const input = mountTt([]);
    const r = await write("school", "Example University");
    expect(r).toMatchObject({ outcome: "filled", value: "Example University" });
    expect(input.value).toBe("Example University");
  });

  test("A box that discards the typed text fails verification", async () => {
    const input = mountTt([]);
    input.addEventListener("change", () => ((Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set as (v: string) => void).call(input, "")));
    const r = await write("school", "Example University");
    expect(r.outcome).toBe("failed");
  });
});

describe("intl-tel-input country", () => {
  const TWO: [string, string, string][] = [["gb", "United Kingdom", "+44"], ["us", "United States", "+1"]];
  const NEAR: [string, string, string][] = [["as", "American Samoa", "+1684"], ["ua", "Ukraine (\u0423\u043a\u0440\u0430\u0457\u043d\u0430)", "+380"], ["um", "U.S. Outlying Islands", "+1"], ...TWO, ["de", "Germany (Deutschland)", "+49"]];

  /** `stock` is the library as shipped: a pick marks its own row and leaves the hidden input (filled only on submit) alone. */
  function mountIti(countries = TWO, dead = false, stock = false) {
    const items = countries.map(([code, name, dial]) => `<li class="iti__country" data-country-code="${code}"><span class="iti__country-name">${name}</span><span class="iti__dial-code">${dial}</span></li>`).join("");
    mount(`<div class="iti"><div class="iti__flag-container"><div class="iti__selected-flag" role="combobox" aria-haspopup="listbox"></div><ul class="iti__country-list iti__hide">${items}</ul></div><input type="tel" id="phone" aria-label="Phone"><input type="hidden" id="country" name="country"></div>`);
    const root = document.querySelector(".iti") as HTMLElement;
    const list = root.querySelector(".iti__country-list") as HTMLElement;
    root.querySelector(".iti__selected-flag")?.addEventListener("click", () => list.classList.toggle("iti__hide"));
    root.querySelector(".iti__selected-flag")?.addEventListener("keydown", (e) => (e as KeyboardEvent).key === "Escape" && list.classList.add("iti__hide"));
    list.querySelectorAll("li").forEach((li) =>
      li.addEventListener("click", () => {
        if (dead) return;
        if (stock) {
          list.querySelectorAll("li").forEach((other) => {
            other.classList.toggle("iti__active", other === li);
            other.setAttribute("aria-selected", String(other === li));
          });
        } else (byId("country") as HTMLInputElement).value = li.getAttribute("data-country-code") ?? "";
        list.classList.add("iti__hide");
      }),
    );
    return root;
  }

  test("A country whose row carries another dial code than the phone's is not picked", async () => {
    expect(await writeItiCountry(mountIti(), "United States", ctx(), "+44")).toMatchObject({ ok: false, reason: "option-not-found" });
    expect(byId("country").value).toBe("");
    expect((await writeItiCountry(mountIti(), "United Kingdom", ctx(), "+44")).ok).toBe(true);
    expect(byId("country").value).toBe("gb");
  });

  test("A country is picked by its two-letter code and verified on the hidden input", async () => {
    const root = mountIti();
    const out = await writeItiCountry(root, "us", ctx());
    expect(out.ok).toBe(true);
    expect(byId("country").value).toBe("us");
  });

  test("In a full fill the country beside the phone is set from the applicant's country before the number is typed", async () => {
    mountIti();
    document.body.insertAdjacentHTML("beforeend", `<label for="x">Referral code</label><input id="x">`);
    await runFill(makeEngine(fakeHost()));
    expect(byId("country").value).toBe("us");
    expect(byId("phone").value.replace(/\D/g, "")).toBe("15550100");
  });

  test("A country is found under any of its names and only as whole words: UK is the United Kingdom and not Ukraine, America is not American Samoa", async () => {
    expect((await writeItiCountry(mountIti(NEAR), "UK", ctx())).ok).toBe(true);
    expect(byId("country").value).toBe("gb");
    expect((await writeItiCountry(mountIti(NEAR), "USA", ctx())).ok).toBe(true);
    expect(byId("country").value).toBe("us");
    expect((await writeItiCountry(mountIti(NEAR), "United States of America", ctx())).ok).toBe(true);
    expect(byId("country").value).toBe("us");
    expect(await writeItiCountry(mountIti(NEAR), "America", ctx())).toMatchObject({ ok: false, reason: "option-not-found" });
    expect(byId("country").value).toBe("");
    // A name the list writes with its own gloss is the one row that holds it.
    expect((await writeItiCountry(mountIti(NEAR), "Germany", ctx())).ok).toBe(true);
    expect(byId("country").value).toBe("de");
  });

  test("intl-tel-input 20 and later: the country button is iti__selected-country and the list sits in a dropdown that hides as a whole", async () => {
    const items = TWO.map(([code, name, dial]) => `<li class="iti__country" data-country-code="${code}"><span class="iti__country-name">${name}</span><span class="iti__dial-code">${dial}</span></li>`).join("");
    mount(`<div class="iti iti--allow-dropdown"><div class="iti__country-container"><button type="button" class="iti__selected-country" aria-haspopup="dialog"></button><div class="iti__dropdown-content iti__hide"><ul class="iti__country-list">${items}</ul></div></div><input type="tel" id="phone" aria-label="Phone"><input type="hidden" id="country"></div>`);
    const root = document.querySelector(".iti") as HTMLElement;
    const drop = root.querySelector(".iti__dropdown-content") as HTMLElement;
    root.querySelector(".iti__selected-country")?.addEventListener("click", () => drop.classList.toggle("iti__hide"));
    root.querySelectorAll("li").forEach((li) =>
      li.addEventListener("click", () => {
        (byId("country") as HTMLInputElement).value = li.getAttribute("data-country-code") ?? "";
        drop.classList.add("iti__hide");
      }),
    );
    expect((await writeItiCountry(root, "United Kingdom", ctx())).ok).toBe(true);
    expect(byId("country").value).toBe("gb");
  });

  test("A list whose country takes no click is closed again and the write is not committed", async () => {
    const root = mountIti(TWO, true);
    expect(await writeItiCountry(root, "United States", ctx())).toMatchObject({ ok: false, reason: "write-not-committed" });
    expect(root.querySelector(".iti__country-list")?.classList.contains("iti__hide")).toBe(true);
  });

  test("The library as shipped fills its hidden input only on submit: the picked row's own selected state verifies the pick, and a country already selected is not clicked again", async () => {
    const root = mountIti(TWO, false, true);
    const us = root.querySelector('[data-country-code="us"]') as HTMLElement;
    let clicks = 0;
    us.addEventListener("click", () => clicks++);
    expect(await writeItiCountry(root, "United States", ctx())).toMatchObject({ ok: true, optionLabel: "united states" });
    expect(us.classList.contains("iti__active")).toBe(true);
    expect(byId("country").value).toBe("");
    expect(await writeItiCountry(root, "United States", ctx())).toMatchObject({ ok: true });
    expect(clicks).toBe(1);
    expect(root.querySelector(".iti__country-list")?.classList.contains("iti__hide")).toBe(true);
  });
});

describe("Switches and role widgets", () => {
  function radixSwitch(opts: { moves?: boolean; on?: boolean } = {}) {
    mount(`<label for="sw">Willing to relocate</label><button type="button" role="switch" id="sw" aria-required="true" aria-checked="${opts.on ? "true" : "false"}" data-state="${opts.on ? "checked" : "unchecked"}"></button><input type="checkbox" aria-hidden="true" tabindex="-1" style="position:absolute;opacity:0;pointer-events:none"><div>Notes</div><label for="pad">Referral code</label><input id="pad">`);
    const sw = byId<HTMLElement>("sw") as unknown as HTMLElement;
    const clicks = { n: 0 };
    sw.addEventListener("click", () => {
      clicks.n++;
      if (opts.moves === false) return;
      const on = sw.getAttribute("aria-checked") === "true";
      sw.setAttribute("aria-checked", String(!on));
      sw.setAttribute("data-state", on ? "unchecked" : "checked");
    });
    return { sw, clicks };
  }

  test("Radix switch: the proxy button is clicked and its aria-checked read back; the hidden bubble input is never used", async () => {
    const { sw, clicks } = radixSwitch();
    const r = await write("sw", "Yes");
    expect(r.outcome).toBe("filled");
    expect(sw.getAttribute("aria-checked")).toBe("true");
    expect(clicks.n).toBe(1);
  });

  test("A switch whose aria-checked never moves is clicked up to five times, 50 ms apart, and reported failed", async () => {
    const { clicks } = radixSwitch({ moves: false });
    const r = await write("sw", "Yes");
    expect(r).toMatchObject({ outcome: "failed", reason: "write-not-committed" });
    expect(clicks.n).toBe(5);
  });

  test("A switch is never turned off: No on an on switch is refused and on an off switch is a no-op", async () => {
    const on = radixSwitch({ on: true });
    expect((await write("sw", "No")).outcome).toBe("failed");
    expect(on.sw.getAttribute("aria-checked")).toBe("true");
    expect(on.clicks.n).toBe(0);
    const off = radixSwitch();
    expect((await write("sw", "No")).outcome).toBe("prefilled");
    expect(off.clicks.n).toBe(0);
  });

  test("A lone switch takes every word a native tick box takes: I agree turns it on, I do not agree leaves it off", async () => {
    const agree = radixSwitch();
    expect((await write("sw", "I agree")).outcome).toBe("filled");
    expect(agree.sw.getAttribute("aria-checked")).toBe("true");
    const refuse = radixSwitch();
    expect((await write("sw", "I do not agree")).outcome).toBe("prefilled");
    expect(refuse.clicks.n).toBe(0);
    const on = radixSwitch({ on: true });
    expect(await write("sw", "Decline")).toMatchObject({ outcome: "failed", reason: "format-not-accepted" });
    expect(on.clicks.n).toBe(0);
  });

  test("A switch already on is prefilled and not clicked", async () => {
    const { clicks } = radixSwitch({ on: true });
    expect((await write("sw", "Yes")).outcome).toBe("prefilled");
    expect(clicks.n).toBe(0);
  });
});

describe("Tag and chip inputs", () => {
  function mountTags() {
    mount(`<label for="tags">Skills</label><div class="tag-input"><span class="chip">Existing</span><input id="tags" role="combobox" aria-multiselectable="true" aria-autocomplete="list" aria-controls="tags-list"></div><ul id="tags-list" role="listbox" hidden></ul>`);
    const input = byId("tags");
    const box = document.querySelector(".tag-input") as HTMLElement;
    const list = byId<HTMLElement>("tags-list") as unknown as HTMLElement;
    const add = (text: string) => {
      const chip = document.createElement("span");
      chip.className = "chip";
      chip.textContent = text;
      box.insertBefore(chip, input);
      input.value = "";
      list.hidden = true;
    };
    input.addEventListener("input", () => {
      list.innerHTML = input.value ? `<li role="option">${input.value} (new)</li>` : "";
      list.hidden = !input.value;
      list.querySelectorAll("li").forEach((li) => li.addEventListener("click", () => add(input.value)));
    });
    return { box, chips: () => Array.from(box.querySelectorAll(".chip")).map((c) => c.textContent) };
  }

  test("One chip is added per answer in the answer's order", async () => {
    const t = mountTags();
    const r = await write("tags", "Golang", { optionLabels: ["Golang", "Rust"] });
    expect(r.outcome).toBe("filled");
    expect(t.chips()).toEqual(["Existing", "Golang", "Rust"]);
  });

  test("An answer the box already holds as a chip is left alone, and the other answers are added", async () => {
    const t = mountTags();
    const r = await write("tags", "Existing", { optionLabels: ["Existing", "Rust"] });
    expect(r.outcome).toBe("filled");
    expect(t.chips()).toEqual(["Existing", "Rust"]);
  });

  /** A button that opens a list of several choices: each option toggles on a click and keeps its own aria-selected, the list stays open, and the button shows only a count. */
  function mountChecklist(options: string[], held: string[] = []) {
    mount(`<label id="ms-l">Languages</label><button type="button" id="ms" role="combobox" aria-haspopup="listbox" aria-expanded="false" aria-multiselectable="true" aria-controls="ms-list" aria-labelledby="ms-l">Select</button><ul id="ms-list" role="listbox" aria-multiselectable="true" hidden></ul>`);
    const trigger = byId<HTMLElement>("ms") as unknown as HTMLElement;
    const list = byId<HTMLElement>("ms-list") as unknown as HTMLElement;
    const clicks: string[] = [];
    const show = () => {
      const n = list.querySelectorAll('[aria-selected="true"]').length;
      trigger.textContent = n ? `${n} selected` : "Select";
    };
    for (const text of options) {
      const li = document.createElement("li");
      li.setAttribute("role", "option");
      li.setAttribute("aria-selected", String(held.includes(text)));
      li.innerHTML = `<span>${text}</span>`;
      li.addEventListener("click", () => {
        clicks.push(text);
        li.setAttribute("aria-selected", String(li.getAttribute("aria-selected") !== "true"));
        show();
      });
      list.appendChild(li);
    }
    show();
    const toggle = (open: boolean) => {
      list.hidden = !open;
      trigger.setAttribute("aria-expanded", String(open));
    };
    trigger.addEventListener("click", () => toggle(list.hidden === true));
    trigger.addEventListener("keydown", (e) => (e as KeyboardEvent).key === "Escape" && toggle(false));
    return { clicks, selected: () => Array.from(list.querySelectorAll('[aria-selected="true"]')).map((o) => o.textContent) };
  }

  test("A list of several choices with no chips is verified on the option's own selected state: each answer is clicked once", async () => {
    const m = mountChecklist(["Go", "Rust", "Zig"]);
    const r = await write("ms", "Go", { optionLabels: ["Go", "Zig"] });
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Go, Zig" });
    expect(m.clicks).toEqual(["Go", "Zig"]);
    expect(m.selected()).toEqual(["Go", "Zig"]);
  });

  test("In that list a choice already selected is not clicked again, which would clear it", async () => {
    const m = mountChecklist(["Go", "Rust", "Zig"], ["Rust"]);
    const r = await write("ms", "Rust", { optionLabels: ["Rust", "Go"] });
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Rust, Go" });
    expect(m.clicks).toEqual(["Go"]);
    expect(m.selected()).toEqual(["Go", "Rust"]);
  });

  /** A box with no suggestions at all: Enter turns its text into a chip (or into `chipAs`, when the widget rewrites it). */
  function mountFreeEntry(label: string, chipAs?: string) {
    mount(`<label for="tags">${label}</label><div class="tag-input"><input id="tags" role="combobox" aria-autocomplete="list"></div>`);
    const input = byId("tags");
    const box = document.querySelector(".tag-input") as HTMLElement;
    input.addEventListener("keydown", (e) => {
      if ((e as KeyboardEvent).key !== "Enter" || !input.value) return;
      const chip = document.createElement("span");
      chip.className = "chip";
      chip.textContent = chipAs ?? input.value;
      box.insertBefore(chip, input);
      input.value = "";
    });
    return { input, chips: () => Array.from(box.querySelectorAll(".chip")).map((c) => c.textContent) };
  }

  test("Free-entry commits the answer itself, never the last search seed: a place is not its country and a degree is not its level word", async () => {
    const place = mountFreeEntry("Location");
    expect((await write("tags", "Durham, NC, United States")).outcome).toBe("filled");
    expect(place.chips()).toEqual(["Durham, NC, United States"]);
    const degree = mountFreeEntry("Degree");
    expect((await write("tags", "Bachelor of Science")).outcome).toBe("filled");
    expect(degree.chips()).toEqual(["Bachelor of Science"]);
  });

  test("Free-entry never invents a decline, and a chip that is not the answer is not a commit", async () => {
    const decline = mountFreeEntry("Gender");
    expect((await write("tags", "Prefer not to say")).outcome).toBe("abstained");
    expect(decline.chips()).toEqual([]);
    expect(decline.input.value).toBe("");
    const other = mountFreeEntry("Skills", "Other");
    expect((await write("tags", "Golang")).outcome).not.toBe("filled");
    expect(other.chips()).toEqual(["Other"]);
  });

  test("A select that lists nothing and takes no Enter is not filled by the text typed into its search box, and the box is emptied", async () => {
    // A react-select under generated class names: what is being typed reads as its value until something is chosen.
    mount(`<div><label for="team">Team</label><div class="css-b62m3t-container"><div class="css-13cymwt-control"><div class="css-hlgwow"><div class="css-1jqq78o-placeholder">Select...</div><div class="css-19bb58m"><input id="team" role="combobox" aria-autocomplete="list" aria-expanded="false" value=""></div></div></div></div></div>`);
    expect(await write("team", "Platform")).toMatchObject({ outcome: "failed", reason: "listbox-not-opened" });
    expect(byId("team").value).toBe("");
  });

  test("Free-entry: with no suggestions Enter accepts the typed text only when a new chip appears; otherwise the box gets its text back", async () => {
    mount(`<label for="tags">Skills</label><div class="tag-input"><input id="tags" role="combobox" aria-multiselectable="true" aria-autocomplete="list"></div>`);
    const input = byId("tags");
    const box = document.querySelector(".tag-input") as HTMLElement;
    input.addEventListener("keydown", (e) => {
      if ((e as KeyboardEvent).key === "Enter" && input.value) {
        const chip = document.createElement("span");
        chip.className = "chip";
        chip.textContent = input.value;
        box.insertBefore(chip, input);
        input.value = "";
      }
    });
    const r = await write("tags", "Go");
    expect(r.outcome).toBe("filled");
    expect(box.querySelector(".chip")?.textContent).toBe("Go");
    // A box that ignores Enter is restored.
    mount(`<label for="tags">Skills</label><div class="tag-input"><input id="tags" role="combobox" aria-autocomplete="list" value="keep"></div>`);
    const r2 = await write("tags", "Go");
    expect(r2.outcome).not.toBe("filled");
    expect(byId("tags").value).toBe("keep");
  });
});

describe("SuccessFactors-style paginated picklist", () => {
  test("A picklist whose server filter does not surface the exact row stays empty and Enter is never pressed", async () => {
    mount("");
    const holder = document.createElement("div");
    holder.className = "paginatedPicklistContainer";
    document.body.appendChild(holder);
    const c = mountCombobox({ id: "country", label: "Country of residence", parent: holder, suggest: () => ["United Arab Emirates", "United Kingdom"] });
    const r = await write("country", "United States");
    expect(r).toMatchObject({ outcome: "abstained" });
    expect(c.keys.filter((k) => k === "keydown:Enter")).toEqual([]);
    expect(c.chosen()).toBe("");
  });

  test("The exact row is typed per character and chosen by click, not by Enter", async () => {
    mount("");
    const holder = document.createElement("div");
    holder.className = "paginatedPicklistContainer";
    document.body.appendChild(holder);
    const c = mountCombobox({ id: "country", label: "Country of residence", parent: holder, suggest: () => ["United Kingdom", "United States"] });
    const r = await write("country", "United States");
    expect(r.outcome).toBe("filled");
    expect(c.clicked).toEqual(["United States"]);
    expect(c.keys.filter((k) => k === "keydown:Enter")).toEqual([]);
  });
});
