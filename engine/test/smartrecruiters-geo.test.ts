// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Drivers for application boards (SmartRecruiters geo autocomplete).
import { describe, expect, test } from "vitest";
import { mount, write } from "./helpers";

type RowStyle = "role-option" | "host-inner" | "host-inner-with-role" | "plain-child" | "slotted";

interface Geo {
  input: HTMLInputElement;
  committed: () => string;
  enters: () => number;
  typed: string[];
}

/**
 * A geo autocomplete: suggestions mount after text is typed. `swallow` makes
 * a click on a row do nothing, so only the keyboard (or the inner element)
 * commits; `wipeOnce` clears the box and the list on the first ArrowDown, as a
 * re-render does.
 */
function mountGeo(o: { places: string[]; style: RowStyle; swallow?: boolean; wipeOnce?: boolean; minQuery?: number }): Geo {
  mount(`<label for="geo">City</label><input id="geo" role="combobox" aria-autocomplete="list" aria-controls="geo-list" aria-expanded="false" autocomplete="off">`);
  const input = document.getElementById("geo") as HTMLInputElement;
  let list: HTMLElement;
  if (o.style === "slotted") {
    list = document.createElement("div");
    list.id = "geo-list";
    list.setAttribute("role", "listbox");
    list.attachShadow({ mode: "open" }).innerHTML = "<div><slot></slot></div>";
  } else {
    list = document.createElement("div");
    list.id = "geo-list";
    list.setAttribute("role", "listbox");
  }
  document.body.append(list);
  if (o.minQuery) {
    const shell = document.createElement("div");
    shell.setAttribute("minquerylength", String(o.minQuery));
    input.replaceWith(shell);
    shell.attachShadow({ mode: "open" }).append(input);
  }
  let value = "";
  let enters = 0;
  let wiped = false;
  let active = -1;
  const typed: string[] = [];
  const commit = (place: string) => {
    value = place;
    input.value = place;
    list.innerHTML = "";
    input.setAttribute("aria-expanded", "false");
  };
  const render = () => {
    list.innerHTML = "";
    active = -1;
    const q = input.value.toLowerCase();
    if (!q) return;
    for (const place of o.places.filter((p) => p.toLowerCase().includes(q))) {
      const click = () => {
        if (!o.swallow) commit(place);
      };
      let row: HTMLElement;
      if (o.style === "host-inner" || o.style === "host-inner-with-role") {
        row = document.createElement("spl-geo-option");
        if (o.style === "host-inner-with-role") row.setAttribute("role", "option");
        const inner = row.attachShadow({ mode: "open" });
        inner.innerHTML = `<div role="option">${place}</div>`;
        // The host itself ignores clicks; the inner element is what the page listens on.
        (inner.firstElementChild as HTMLElement).addEventListener("click", () => commit(place));
      } else {
        row = document.createElement("div");
        if (o.style === "role-option") row.setAttribute("role", "option");
        row.textContent = place;
        row.addEventListener("click", click);
      }
      list.append(row);
    }
    input.setAttribute("aria-expanded", String(list.children.length > 0));
  };
  input.addEventListener("input", () => {
    typed.push(input.value);
    render();
  });
  input.addEventListener("keydown", (e) => {
    const key = (e as KeyboardEvent).key;
    if (key === "ArrowDown") {
      if (o.wipeOnce && !wiped) {
        wiped = true;
        input.value = "";
        list.innerHTML = "";
        return;
      }
      active = Math.min(list.children.length - 1, active + 1);
    }
    if (key === "Enter" && active >= 0) {
      enters++;
      commit(((list.children[active] as HTMLElement).shadowRoot?.textContent ?? list.children[active]?.textContent ?? "").trim());
    }
  });
  return { input, committed: () => value, enters: () => enters, typed };
}

const PLACES = ["Durham, NC, USA", "Durham, England, UK", "Durham Bridge, VA, USA"];

describe("SmartRecruiters geo autocomplete", () => {
  test("A suggestion is committed through the inner role-option when the host element ignores clicks", async () => {
    const g = mountGeo({ places: PLACES, style: "host-inner" });
    const r = await write("geo", "Durham, England, UK");
    expect(r.outcome).toBe("filled");
    expect(g.committed()).toBe("Durham, England, UK");
  });

  test("A host that also carries the option role does not hide its inner role-option", async () => {
    const g = mountGeo({ places: PLACES, style: "host-inner-with-role" });
    const r = await write("geo", "Durham, England, UK");
    expect(r.outcome).toBe("filled");
    expect(g.committed()).toBe("Durham, England, UK");
  });

  test("Role-less direct-child suggestions are found and the child is what commits", async () => {
    const g = mountGeo({ places: PLACES, style: "plain-child" });
    const r = await write("geo", "Durham, NC, USA");
    expect(r.outcome).toBe("filled");
    expect(g.committed()).toBe("Durham, NC, USA");
  });

  test("Suggestions slotted into the popup's shadow root are found", async () => {
    const g = mountGeo({ places: PLACES, style: "slotted" });
    const r = await write("geo", "Durham, NC, USA");
    expect(r.outcome).toBe("filled");
    expect(g.committed()).toBe("Durham, NC, USA");
  });

  test("Enter is pressed only when the matching suggestion is the first option", async () => {
    // Clicks do nothing here, so the keyboard is the only way in.
    const first = mountGeo({ places: PLACES, style: "role-option", swallow: true });
    expect((await write("geo", "Durham, NC, USA")).outcome).toBe("filled");
    expect(first.committed()).toBe("Durham, NC, USA");
    expect(first.enters()).toBe(1);
    const second = mountGeo({ places: PLACES, style: "role-option", swallow: true });
    const r = await write("geo", "Durham, England, UK");
    expect(r.outcome).not.toBe("filled");
    expect(second.committed()).toBe("");
    expect(second.enters()).toBe(0);
  });

  test("A re-render that wipes the typed seed is retyped once and the first suggestion is committed", async () => {
    const g = mountGeo({ places: PLACES, style: "role-option", swallow: true, wipeOnce: true });
    const r = await write("geo", "Durham, NC, USA");
    expect(r.outcome).toBe("filled");
    expect(g.committed()).toBe("Durham, NC, USA");
    expect(g.typed.filter((t) => t === "Durham").length).toBeGreaterThanOrEqual(2);
  });

  test("A minimum query length declared on a shadow host is honoured: a shorter seed is never typed", async () => {
    const g = mountGeo({ places: PLACES, style: "role-option", minQuery: 20 });
    const r = await write("geo", "Durham, NC, USA");
    expect(r.outcome).not.toBe("filled");
    expect(g.typed).toEqual([]);
  });
});

describe("SmartRecruiters multiselect tags", () => {
  /** The search input sits in the tags host; a pick clears it and draws a tag in the host's own shadow root. */
  function mountTags(held: string[]): { clicks: string[]; tags: () => string[] } {
    mount(`<label for="skills">Skills</label><spl-tags id="host"><input id="skills" role="combobox" aria-autocomplete="list" aria-controls="sk-list" autocomplete="off"></spl-tags><div id="sk-list" role="listbox"></div>`);
    const host = document.getElementById("host") as HTMLElement;
    const shadow = host.attachShadow({ mode: "open" });
    shadow.innerHTML = '<div class="tags"></div><slot></slot>';
    const box = shadow.querySelector(".tags") as HTMLElement;
    const addTag = (label: string) => box.insertAdjacentHTML("beforeend", `<spl-tag><span class="c-spl-tag-label">${label}</span></spl-tag>`);
    for (const h of held) addTag(h);
    box.insertAdjacentHTML("beforeend", "<spl-tag><span>+2 more</span></spl-tag>");
    const input = document.getElementById("skills") as HTMLInputElement;
    const list = document.getElementById("sk-list") as HTMLElement;
    const clicks: string[] = [];
    input.addEventListener("input", () => {
      list.innerHTML = "";
      const q = input.value.toLowerCase();
      for (const label of ["Python", "Go", "Rust"].filter((l) => q && l.toLowerCase().includes(q))) {
        const row = document.createElement("div");
        row.setAttribute("role", "option");
        row.textContent = label;
        row.addEventListener("click", () => {
          clicks.push(label);
          addTag(label);
          input.value = "";
          list.innerHTML = "";
        });
        list.append(row);
      }
    });
    return { clicks, tags: () => Array.from(box.querySelectorAll(".c-spl-tag-label")).map((e) => e.textContent ?? "") };
  }

  test("SmartRecruiters tags: the committed value is read from the nested tag labels with the overflow tag left out, so a pick verifies", async () => {
    const t = mountTags([]);
    const r = await write("skills", "Go");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Go" });
    expect(t.tags()).toEqual(["Go"]);
    expect(t.clicks).toEqual(["Go"]);
  });

  test("SmartRecruiters tags: a skill that is already tagged is not clicked again", async () => {
    const t = mountTags(["Go"]);
    const r = await write("skills", "Go");
    expect(r.outcome).toBe("prefilled");
    expect(t.clicks).toEqual([]);
    expect(t.tags()).toEqual(["Go"]);
  });
});
