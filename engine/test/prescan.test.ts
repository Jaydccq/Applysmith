// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Question pass (custom listbox prescan: read without side effects, cap on opens, keyboard
// fallback, placeholder wait); Placeholders and overwrite decisions.
import { describe, expect, test } from "vitest";
import { PAD, byId, fakeHost, makeEngine, mount, rowFor, runFill, summaryOf } from "./helpers";
import { mountMenu } from "./widgets-fixtures";

/**
 * A referral-source menu of categories: a category row opens its sub-menu in
 * the same popup (one by aria-haspopup, one by a trailing chevron), a Back
 * button returns to the top, and any other row commits its choice on a click.
 */
function mountHearAbout(o: { back?: boolean } = {}) {
  const wrap = document.createElement("div");
  wrap.innerHTML = `<label id="src-lbl" for="src">How did you hear about us?</label>
    <button id="src" type="button" role="combobox" aria-haspopup="listbox" aria-expanded="false" aria-controls="src-list" aria-labelledby="src-lbl" aria-required="true">Select one</button>
    <div id="src-list" role="listbox" hidden></div>`;
  document.body.append(wrap);
  const trigger = wrap.querySelector<HTMLButtonElement>("#src")!;
  const list = wrap.querySelector<HTMLElement>("#src-list")!;
  const committed: string[] = [];
  const subs: Record<string, string[]> = { "Job Sites": ["LinkedIn", "Indeed"], "Social Media": ["Twitter", "Facebook"] };
  const close = () => {
    list.hidden = true;
    trigger.setAttribute("aria-expanded", "false");
  };
  const row = (label: string, onClick: () => void, attrs = "") => {
    const d = document.createElement("div");
    d.setAttribute("role", "option");
    if (attrs) d.setAttribute(attrs, "true");
    d.textContent = label;
    d.addEventListener("click", onClick);
    return d;
  };
  const commit = (label: string) => () => {
    committed.push(label);
    trigger.textContent = label;
    close();
  };
  const showTop = () => {
    list.replaceChildren(
      row("Job Sites", () => showSub("Job Sites"), "aria-haspopup"),
      row("Social Media ›", () => showSub("Social Media")),
      row("Employee Referral", commit("Employee Referral")),
      row("Other", commit("Other")),
    );
  };
  const showSub = (name: string) => {
    const back = document.createElement("button");
    back.type = "button";
    back.textContent = "Back";
    back.addEventListener("click", () => (o.back === false ? undefined : showTop()));
    list.replaceChildren(back, ...(subs[name] ?? []).map((l) => row(l, commit(l))));
  };
  trigger.addEventListener("click", () => {
    if (!list.hidden) return close();
    showTop();
    list.hidden = false;
    trigger.setAttribute("aria-expanded", "true");
  });
  trigger.addEventListener("keydown", (e) => {
    if ((e as KeyboardEvent).key === "Escape") close();
  });
  return { trigger, list, committed };
}

/**
 * A SuccessFactors paginated picklist: a click fetches a server page of at
 * most 100 rows, each a menuitem link inside an option, shown a moment later
 * in a list the box owns; a click on a row or Escape closes it.
 */
function mountPicklist(id: string, label: string, labels: string[]) {
  const wrap = document.createElement("div");
  wrap.innerHTML = `<label for="${id}">${label}</label><div class="paginatedPicklistContainer"><input id="${id}" type="text" role="combobox" autocomplete="off" placeholder="No Selection" aria-expanded="false" aria-owns="${id}-l:_listSelect" class="rcmpaginatedselectinput"></div>`;
  document.body.append(wrap);
  const input = wrap.querySelector<HTMLInputElement>("input")!;
  const listId = `${id}-l:_listSelect`;
  let opens = 0;
  const close = () => {
    document.getElementById(listId)?.remove();
    input.setAttribute("aria-expanded", "false");
  };
  input.addEventListener("click", () => {
    if (document.getElementById(listId)) return close();
    opens++;
    setTimeout(() => {
      const ul = document.createElement("ul");
      ul.id = listId;
      ul.setAttribute("role", "listbox");
      labels.slice(0, 100).forEach((text, i) => {
        const li = document.createElement("li");
        li.setAttribute("role", "option");
        li.id = `${id}-l:item${i}`;
        const a = document.createElement("a");
        a.setAttribute("role", "menuitem");
        a.textContent = text;
        a.addEventListener("click", () => {
          input.value = text;
          input.dispatchEvent(new Event("change", { bubbles: true }));
          close();
        });
        li.append(a);
        ul.append(li);
      });
      document.body.append(ul);
      input.setAttribute("aria-expanded", "true");
    }, 20);
  });
  input.addEventListener("keydown", (e) => {
    if ((e as KeyboardEvent).key === "Escape") close();
  });
  return { input, opens: () => opens, open: () => document.getElementById(listId) !== null };
}

/**
 * Nebius's Gravity UI city lookup (careers.nebius.com, live): a button
 * combobox whose click mounts a portal popup that holds its own search box
 * (aria-controls naming the listbox, focused) above a listbox showing only a
 * label-less loading row. With nothing typed the loading row soon gives way to
 * a "Start typing your city" hint and the listbox is unmounted (`idle:
 * "unmount"`), or it stays (`idle: "loading"`); cities are listed a moment
 * after one is typed. A click on the button toggles the popup, Escape in the
 * search box closes it, and a click on a city commits it to the button and
 * closes the popup.
 */
function mountCityLookup(cities: string[], idle: "unmount" | "loading") {
  const wrap = document.createElement("div");
  wrap.innerHTML = `<label for="apply-location">Location<span aria-hidden="true"> *</span></label>
    <div class="g-select"><button id="apply-location" type="button" role="combobox" aria-haspopup="listbox" aria-expanded="false"><span class="g-select-control__placeholder">Select your city</span></button></div>`;
  document.body.append(wrap);
  const trigger = wrap.querySelector<HTMLButtonElement>("button")!;
  const listId = "select-popup-apply-location";
  const typed: string[] = [];
  let popup: HTMLElement | null = null;
  const close = () => {
    popup?.remove();
    popup = null;
    trigger.setAttribute("aria-expanded", "false");
  };
  const row = (label: string, i: number, onClick?: () => void) => {
    const d = document.createElement("div");
    d.setAttribute("role", "option");
    d.id = `${listId}-item-${i}`;
    if (onClick) {
      d.innerHTML = `<div class="g-select-list__option"><span>${label}</span></div>`;
      d.addEventListener("click", onClick);
    } else {
      d.setAttribute("aria-disabled", "true");
      d.innerHTML = `<div class="g-select-list__loading-indicator"><div class="g-loader"></div></div>`;
    }
    return d;
  };
  const list = (rows: HTMLElement[]) => {
    const g = document.createElement("div");
    g.className = "g-list";
    g.innerHTML = `<div id="${listId}" class="g-list__items" role="listbox"></div>`;
    g.firstElementChild!.append(...rows);
    return g;
  };
  const hint = () => {
    const d = document.createElement("div");
    d.className = "g-select-empty-placeholder";
    d.textContent = "Start typing your city";
    return d;
  };
  const open = () => {
    const p = document.createElement("div");
    popup = p;
    p.className = "g-popup g-popup_open";
    p.innerHTML = `<div class="g-select-filter"><input class="g-select-filter__input" aria-label="Filter" aria-controls="${listId}" placeholder="Start typing your city" value=""></div><div class="slot"></div>`;
    document.body.append(p);
    const slot = p.querySelector<HTMLElement>(".slot")!;
    slot.append(list([row("", 0)]));
    const box = p.querySelector<HTMLInputElement>("input")!;
    if (idle === "unmount") setTimeout(() => box.value === "" && slot.replaceChildren(hint()), 10);
    box.addEventListener("input", () => {
      const q = box.value;
      typed.push(q);
      setTimeout(() => {
        if (box.value !== q) return;
        const hits = cities.filter((c) => c.toLowerCase().includes(q.toLowerCase()));
        const pick = (c: string) => () => {
          trigger.textContent = c;
          close();
        };
        slot.replaceChildren(hits.length ? list(hits.map((c, i) => row(c, i, pick(c)))) : hint());
      }, 20);
    });
    box.addEventListener("keydown", (e) => {
      if ((e as KeyboardEvent).key === "Escape") close();
    });
    trigger.setAttribute("aria-controls", listId);
    trigger.setAttribute("aria-expanded", "true");
    box.focus();
  };
  trigger.addEventListener("click", () => (popup ? close() : open()));
  return { trigger, typed, open: () => popup !== null };
}

describe("Custom listbox prescan", () => {
  test.each(["unmount", "loading"] as const)("A popup that opens empty on its own search box (idle list: %s) is asked with no options, and the city is typed into that box and chosen", async (idle) => {
    mount(PAD);
    const c = mountCityLookup(["Charleston, South Carolina, United States", "North Charleston, South Carolina, United States", "Charleston, West Virginia, United States"], idle);
    const host = fakeHost({ location: { outcome: "filled", value: "Charleston, SC" } });
    const s = summaryOf(await runFill(makeEngine(host)));
    expect(host.matchRequests.flatMap((r) => r.questions).find((q) => q.kind === "custom-listbox")?.options).toEqual([]);
    expect(c.typed).toContain("Charleston");
    expect(c.trigger.textContent).toBe("Charleston, South Carolina, United States");
    expect(rowFor(s, "location").outcome).toBe("filled");
    expect(c.open()).toBe(false);
  });

  test("Listbox read without side effects: the prescan opens a closed listbox to read its options, closes it again, selects nothing and leaves the committed value unchanged", async () => {
    mount(PAD);
    const m = mountMenu({ id: "deg", label: "Highest degree", options: ["Bachelor's", "Master's"] });
    const host = fakeHost();
    await runFill(makeEngine(host));
    const q = host.matchRequests.flatMap((r) => r.questions).find((x) => x.kind === "custom-listbox");
    expect(q?.options).toEqual(["Bachelor's", "Master's"]);
    expect(m.clicked).toEqual([]);
    expect(m.shown()).toBe("Select one");
    expect(m.list.hidden).toBe(true);
    expect(m.trigger.getAttribute("aria-expanded")).toBe("false");
  });

  test("Cap on listbox opens: with 40 closed custom listboxes 25 are opened and read in the first pass, none of the other 15 is sent, and they are read in the next pass", async () => {
    mount(PAD);
    const opens = new Map<string, number>();
    for (let i = 0; i < 40; i++) {
      const m = mountMenu({ id: `lb${i}`, label: `Question number ${i}`, options: ["Yes", "No"] });
      m.trigger.addEventListener("pointerdown", () => opens.set(`lb${i}`, (opens.get(`lb${i}`) ?? 0) + 1));
    }
    const host = fakeHost();
    await runFill(makeEngine(host));
    const sizes = host.matchRequests.map((r) => r.questions.filter((q) => q.kind === "custom-listbox").length);
    expect(sizes.slice(0, 2)).toEqual([25, 15]);
    expect(Array.from(opens.values()).every((n) => n === 1)).toBe(true);
    expect(opens.size).toBe(40);
  });

  test("The open falls back to ArrowDown when the pointer open mounts nothing, and a popup that holds only a placeholder is waited on for real options", async () => {
    mount(PAD);
    const m = mountMenu({ id: "deg", label: "Highest degree", options: ["Select an option"], openOn: "arrow" });
    setTimeout(() => {
      m.list.innerHTML = `<li role="option">Select an option</li><li role="option">Bachelor's</li>`;
    }, 10);
    const host = fakeHost();
    await runFill(makeEngine(host));
    const q = host.matchRequests.flatMap((r) => r.questions).find((x) => x.kind === "custom-listbox");
    expect(q?.options).toEqual(["Bachelor's"]);
  });

  test("A paginated picklist whose first page is not full is read whole, closed again, and its answer is clicked in the list", async () => {
    mount(PAD);
    const p = mountPicklist("auth", "Are you legally entitled to work in the country you are applying for?", ["No Selection", "No", "Yes"]);
    const host = fakeHost({ "are you legally entitled to work in the country you are applying for?": { outcome: "filled", value: "Yes" } });
    await runFill(makeEngine(host));
    const q = host.matchRequests.flatMap((r) => r.questions).find((x) => x.kind === "custom-listbox");
    expect(q?.options).toEqual(["No", "Yes"]);
    expect(p.input.value).toBe("Yes");
    expect(p.open()).toBe(false);
  });

  test("A paginated picklist whose first page is full is asked with no options, because more pages may follow", async () => {
    mount(PAD);
    const countries = Array.from({ length: 250 }, (_, i) => `Country ${String(i).padStart(3, "0")}`);
    const p = mountPicklist("res", "Country of Residence", countries);
    const host = fakeHost();
    await runFill(makeEngine(host));
    const q = host.matchRequests.flatMap((r) => r.questions).find((x) => x.kind === "custom-listbox");
    expect(q?.options).toEqual([]);
    expect(p.opens()).toBeGreaterThan(0);
    expect(p.open()).toBe(false);
  });

  test("A search widget whose chevron shows its whole list (Ashby's value select) is read through the chevron, closed again, and its answer typed and chosen", async () => {
    mount(PAD);
    const { mountCombobox } = await import("./widgets-fixtures");
    const states = ["Ontario, Canada", "Arizona (AZ)", "South Carolina (SC)", "Location not listed"];
    const c = mountCombobox({ id: "loc", label: "Please select the location you intend to work from.", suggest: (q) => states.filter((l) => l.toLowerCase().includes(q.toLowerCase())), toggle: states });
    const host = fakeHost({ "please select the location you intend to work from.": { outcome: "filled", value: "South Carolina (SC)" } });
    const s = summaryOf(await runFill(makeEngine(host)));
    expect(host.matchRequests.flatMap((r) => r.questions).find((q) => q.kind === "custom-listbox")?.options).toEqual(states);
    expect(c.clicked).toEqual(["South Carolina (SC)"]);
    expect(c.chosen()).toBe("South Carolina (SC)");
    expect(rowFor(s, "loc").outcome).toBe("filled");
  });

  test("A search widget whose chevron shows only No results (Ashby's place search) is asked with no options, and its answer is typed and chosen", async () => {
    mount(PAD);
    const { mountCombobox } = await import("./widgets-fixtures");
    const c = mountCombobox({ id: "sch", label: "School", suggest: (q) => (q ? ["Example University"] : []), toggleEmpty: "No results" });
    const host = fakeHost({ school: { outcome: "filled", value: "Example University" } });
    const s = summaryOf(await runFill(makeEngine(host)));
    expect(host.matchRequests.flatMap((r) => r.questions).find((q) => q.kind === "custom-listbox")?.options).toEqual([]);
    expect(c.chosen()).toBe("Example University");
    expect(rowFor(s, "school").outcome).toBe("filled");
  });

  test("A listbox that already shows a value is not opened, and one whose question names the current employer or a location is asked over a parser's guess", async () => {
    mount(PAD);
    const shown = mountMenu({ id: "s1", label: "Pronouns", options: ["He", "She"] });
    shown.trigger.textContent = "She";
    const loc = mountMenu({ id: "s2", label: "Current location", options: ["Durham", "Raleigh"] });
    loc.trigger.textContent = "Raleigh";
    let opened = 0;
    shown.trigger.addEventListener("pointerdown", () => opened++);
    const host = fakeHost({ "current location": { outcome: "filled", value: "Durham" } });
    await runFill(makeEngine(host));
    expect(opened).toBe(0);
    expect(loc.shown()).toBe("Durham");
  });

  test("A select that names the current city is answered over a document parser's guess", async () => {
    mount(`${PAD}<label for="c1">Current city</label><select id="c1"><option value="">Select</option><option>Raleigh</option><option>Durham</option></select>`);
    (byId<HTMLSelectElement>("c1").options[1] as HTMLOptionElement).selected = true;
    const host = fakeHost({ "current city": { outcome: "filled", value: "Durham" } });
    await runFill(makeEngine(host));
    expect(byId("c1").value).toBe("Durham");
    // An unrelated select that holds a genuine choice is kept.
    mount(`${PAD}<label for="l1">Level</label><select id="l1"><option value="">Select</option><option>Junior</option><option>Senior</option></select>`);
    (byId<HTMLSelectElement>("l1").options[1] as HTMLOptionElement).selected = true;
    await runFill(makeEngine(fakeHost({ level: { outcome: "filled", value: "Senior" } })));
    expect(byId("l1").value).toBe("Junior");
  });

  test("hierarchical hear-about listbox is flattened and no leaf is committed", async () => {
    mount(PAD);
    const m = mountHearAbout();
    const host = fakeHost();
    await runFill(makeEngine(host));
    const q = host.matchRequests.flatMap((r) => r.questions).find((x) => x.text === "How did you hear about us?");
    expect(q?.options).toEqual(["Job Sites > LinkedIn", "Job Sites > Indeed", "Social Media > Twitter", "Social Media > Facebook", "Employee Referral", "Other"]);
    expect(m.committed).toEqual([]);
    expect(m.trigger.textContent).toBe("Select one");
    expect(m.list.hidden).toBe(true);
  });

  test("A category menu whose Back does not return stops after the first category, and a flattened answer is committed through its category", async () => {
    mount(PAD);
    const stuck = mountHearAbout({ back: false });
    const host = fakeHost();
    await runFill(makeEngine(host));
    const q = host.matchRequests.flatMap((r) => r.questions).find((x) => x.text === "How did you hear about us?");
    expect(q?.options).toEqual(["Job Sites > LinkedIn", "Job Sites > Indeed", "Social Media ›", "Employee Referral", "Other"]);
    expect(stuck.committed).toEqual([]);

    mount(PAD);
    const m = mountHearAbout();
    await runFill(makeEngine(fakeHost({ "how did you hear about us?": { outcome: "filled", value: "Job Sites > Indeed", optionLabel: "Job Sites > Indeed" } })));
    expect(m.committed).toEqual(["Indeed"]);
    expect(m.trigger.textContent).toBe("Indeed");
  });

  // Sourcegraph's Greenhouse race list: "(mark all that apply)" is the list's
  // instruction, not an Apply button.
  test("A label that says mark all that apply is a question, not a submit-shaped button", async () => {
    mount(PAD);
    const label = "Race/ethnicity (mark all that apply):";
    mountMenu({ id: "race", label, options: ["Asian", "White"] });
    const host = fakeHost();
    await runFill(makeEngine(host));
    const q = host.matchRequests.flatMap((r) => r.questions).find((x) => x.text.startsWith("Race/ethnicity"));
    expect(q?.options).toEqual(["Asian", "White"]);
  });

  // Alnylam's Greenhouse acknowledgement: "review" inside a request is a
  // question; a label that leads with it ("Review and submit") is a button.
  test("A label that asks to review something is a question; one that leads with Review is submit-shaped", async () => {
    mount(PAD);
    const label = "Please review and acknowledge the following:";
    mountMenu({ id: "ack", label, options: ["I acknowledge", "I do not acknowledge"] });
    const button = mountMenu({ id: "rev", label: "Review and submit", options: ["A", "B"] });
    let opened = 0;
    button.trigger.addEventListener("pointerdown", () => opened++);
    const host = fakeHost();
    await runFill(makeEngine(host));
    const q = host.matchRequests.flatMap((r) => r.questions).find((x) => x.text === label);
    expect(q?.options).toEqual(["I acknowledge", "I do not acknowledge"]);
    expect(opened).toBe(0);
  });

  test("A listbox that is a submit-shaped button in page chrome is not opened", async () => {
    mount(`<nav><button aria-haspopup="listbox" id="nav">Menu</button></nav>${PAD}`);
    let opened = 0;
    byId("nav").addEventListener("pointerdown", () => opened++);
    await runFill(makeEngine(fakeHost()));
    expect(opened).toBe(0);
  });
});
