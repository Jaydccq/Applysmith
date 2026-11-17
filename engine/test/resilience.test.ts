// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Resilience to re-renders and user edits (restore pass, user-edit tracker, widget parts);
// Committed value read-back (restore re-drives a listbox with the recorded option label).
import { describe, expect, test } from "vitest";
import { PAD, byId, fakeHost, makeEngine, mount, runFill, summaryOf, tick, until, fireTrusted } from "./helpers";
import { mountMenu } from "./widgets-fixtures";
import { mountReactSelect } from "./library-fixtures";

const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set as (v: string) => void;


function fiveFields() {
  mount(Array.from({ length: 5 }, (_, i) => `<label for="f${i}">Favourite thing number ${i}</label><input id="f${i}">`).join(""));
  const table: Record<string, { outcome: "filled"; value: string }> = {};
  for (let i = 0; i < 5; i++) table[`favourite thing number ${i}`] = { outcome: "filled", value: `Value ${i}` };
  return fakeHost(table);
}

describe("Wipe restore", () => {
  test("Framework wipe repaired: a page that clears five fields shortly after the engine wrote them gets the same five values back and the summary lists them as restored", async () => {
    const host = fiveFields();
    const wiped = new Set<string>();
    const engine = makeEngine(host, { restoreCapMs: 5000, restoreIdleMs: 1000 });
    // A page that blanks a field while the writer is still verifying it is repaired by the writer's own retry, which
    // is not a restore. The wipe therefore waits for the question pass to end, whatever the machine's load.
    const writesDone = () => engine.log().some((line) => line.includes("question-pass end"));
    const wipeWhenWritten = (id: string) => {
      if (!writesDone()) return void setTimeout(() => wipeWhenWritten(id), 1);
      setter.call(byId(id), "");
    };
    for (let i = 0; i < 5; i++) {
      byId(`f${i}`).addEventListener("input", () => {
        if (wiped.has(`f${i}`)) return;
        wiped.add(`f${i}`);
        setTimeout(() => wipeWhenWritten(`f${i}`), 5);
      });
    }
    // The wait outlasts the pass's own cap, so the pass ending first is the only way it can fail; it waits on the
    // restored list, which is updated after the value lands, not on the values alone.
    const s = summaryOf(await runFill(engine));
    await until(() => (s.restored ?? []).length === 5, 6000);
    expect([0, 1, 2, 3, 4].map((i) => byId(`f${i}`).value)).toEqual(["Value 0", "Value 1", "Value 2", "Value 3", "Value 4"]);
  });

  test("User deletes an answer: a field the user cleared with trusted events is not written again, empty or not", async () => {
    const host = fiveFields();
    let writes = 0;
    byId("f0").addEventListener("input", (e) => {
      if (!e.isTrusted) writes++;
    });
    const engine = makeEngine(host);
    const started = engine.start({ applicant: { candidate: {} } });
    expect(started.started).toBe(true);
    await until(() => engine.status().state === "done");
    const afterFill = writes;
    // The user clears the field; a re-render later reports it empty.
    setter.call(byId("f0"), "");
    fireTrusted(byId("f0"), "input");
    await tick(120);
    expect(byId("f0").value).toBe("");
    expect(writes).toBe(afterFill);
  });

  test("Private store carrier: a hidden id-less text input holding an opaque id is neither snapshotted nor written", async () => {
    const host = fiveFields();
    const carrier = document.createElement("input");
    carrier.style.display = "none";
    carrier.value = "opaque-id-123";
    document.body.appendChild(carrier);
    const s = summaryOf(await runFill(makeEngine(host)));
    setter.call(carrier, "");
    await tick(100);
    expect(carrier.value).toBe("");
    expect(s.restored ?? []).toEqual([]);
  });

  test("A wiped control whose node was replaced is dropped: an entry lives only while its selector resolves to the same node", async () => {
    const host = fiveFields();
    const engine = makeEngine(host);
    await runFill(engine);
    const old = byId("f1");
    const fresh = old.cloneNode(false) as HTMLInputElement;
    fresh.value = "";
    old.replaceWith(fresh);
    await tick(120);
    expect(byId("f1").value).toBe("");
  });

  test("At most three writes per control: a page that wipes every write is written back three times and then abandoned", async () => {
    mount(`<label for="f0">Favourite thing number 0</label><input id="f0">${PAD}`);
    const host = fakeHost({ "favourite thing number 0": { outcome: "filled", value: "Value 0" } });
    byId("f0").addEventListener("input", () => setTimeout(() => setter.call(byId("f0"), ""), 3));
    // The pass outlasts the slowest machine; the wait below is on the pass giving up, not on a clock.
    const engine = makeEngine(host, { restoreCapMs: 20_000, restoreIdleMs: 20_000, restorePollMs: 20 });
    await runFill(engine);
    await until(() => engine.log().some((l) => l.includes("restore abandoned")));
    const restored = engine.log().filter((l) => l.includes("restore restored"));
    expect(restored.length).toBeLessThanOrEqual(3);
    expect(restored.length).toBeGreaterThan(0);
    expect(engine.log().some((l) => l.includes("restore abandoned"))).toBe(true);
  });
});

describe("Restore pass bounds", () => {
  const wipe = () => {
    for (let i = 0; i < 5; i++) setter.call(byId(`f${i}`), "");
  };
  const ended = (engine: { log(): string[] }, reason: string) => engine.log().some((l) => l.includes("restore end") && l.includes(`reason=${reason}`));

  const LONG = { restoreIdleMs: 20_000, restoreCapMs: 20_000, restorePollMs: 10, tailCapMs: 10 };

  test("stop() on the finished run ends the pass and nothing is restored afterwards", async () => {
    const engine = makeEngine(fiveFields(), LONG);
    const s = await runFill(engine);
    expect(engine.stop((s as { runId: string }).runId)).toBe(false);
    await until(() => ended(engine, "stopped"), 3000);
    wipe();
    await tick(100);
    expect(byId("f0").value).toBe("");
  });

  test("A new run ends the previous run's pass: no restore of run A writes during run B", async () => {
    const host = fiveFields();
    const engine = makeEngine(host, LONG);
    await runFill(engine);
    // Run B is held in its question pass, so it never reaches a restore pass of its own.
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => (release = resolve));
    const match = host.match.bind(host);
    host.match = async (req) => {
      await gate;
      return match(req);
    };
    document.body.insertAdjacentHTML("beforeend", `<label for="g">Another question</label><input id="g">`);
    const second = engine.start({ applicant: { candidate: {} } });
    expect(second.started).toBe(true);
    const writes: string[] = [];
    document.addEventListener("input", (e) => (e.isTrusted ? undefined : writes.push((e.target as HTMLElement).id)), true);
    const before = writes.length;
    wipe();
    await tick(120);
    expect(writes.length).toBe(before);
    expect(byId("f0").value).toBe("");
    release();
    engine.stop((second as { runId: string }).runId);
  });
});

describe("Restore of widgets", () => {
  function blankAll(menus: ReturnType<typeof mountMenu>[]) {
    for (const m of menus) m.reset();
  }

  test("Late wipe: the page blanks four comboboxes after the fill and one has since landed a different option; three are restored and the one holding a different value is left alone", async () => {
    mount(PAD);
    const ids = ["a", "b", "c", "d"];
    const menus = ids.map((id, i) => mountMenu({ id, label: `Preference ${i}`, options: ["Alpha", "Beta", "Gamma"] }));
    const table = Object.fromEntries(ids.map((_, i) => [`preference ${i}`, { outcome: "filled" as const, value: "Beta" }]));
    const engine = makeEngine(fakeHost(table), { restoreCapMs: 1500, restoreIdleMs: 300, restorePollMs: 25 });
    await runFill(engine);
    expect(menus.map((m) => m.shown())).toEqual(["Beta", "Beta", "Beta", "Beta"]);
    blankAll(menus);
    // The user (or the page) chose another option in the last one before the next look.
    (menus[3] as ReturnType<typeof mountMenu>).trigger.textContent = "Gamma";
    await until(() => menus.slice(0, 3).every((m) => m.shown() === "Beta"), 2000);
    await tick(80);
    expect((menus[3] as ReturnType<typeof mountMenu>).shown()).toBe("Gamma");
  });

  test("A listbox is re-driven with the option label the writing lane committed, not its display text", async () => {
    mount(PAD);
    // The display is a short code shared by two options; only the recorded label picks the right one.
    const m = mountMenu({ id: "cc", label: "Country calling code", options: ["Antigua +1", "United States +1"], keyboard: false });
    m.list.querySelectorAll("li").forEach((li) => li.addEventListener("click", () => (m.trigger.textContent = "+1")));
    const engine = makeEngine(fakeHost({ "country calling code": { outcome: "filled", optionLabel: "United States +1", value: "United States +1" } }), { restoreCapMs: 600, restoreIdleMs: 200, restorePollMs: 25 });
    await runFill(engine);
    expect(m.shown()).toBe("+1");
    m.reset();
    await until(() => m.clicked.length >= 2, 2000);
    expect(m.clicked.slice(-2)).toEqual(["United States +1", "United States +1"]);
  });

  test("A restore that lands a different value stops: the control is left alone from then on", async () => {
    mount(PAD);
    const m = mountMenu({ id: "cc", label: "Preference", options: ["Alpha", "Beta"], keyboard: false });
    const engine = makeEngine(fakeHost({ preference: { outcome: "filled", value: "Beta" } }), { restoreCapMs: 600, restoreIdleMs: 300, restorePollMs: 25 });
    await runFill(engine);
    // From now on the widget lands "Alpha" whatever is chosen, and then loses its value.
    m.list.querySelectorAll("li").forEach((li) => li.addEventListener("click", () => (m.trigger.textContent = "Alpha")));
    m.reset();
    await tick(250);
    const clicks = m.clicked.length;
    m.reset();
    await tick(150);
    expect(m.clicked.length).toBe(clicks);
  });

  test("Tag widget left alone: a multi-value control that loses its chips is never reopened to add them back", async () => {
    mount(PAD);
    const rs = mountReactSelect({ inputId: "react-select-9-input", label: "Skills", multi: true, options: ["Rust", "Zig"] });
    const engine = makeEngine(fakeHost({ skills: { outcome: "filled", optionLabels: ["Rust", "Zig"] } }), { restoreCapMs: 400, restoreIdleMs: 200, restorePollMs: 25 });
    await runFill(engine);
    expect(rs.value()).toEqual(["Rust", "Zig"]);
    let opened = 0;
    rs.control.addEventListener("mousedown", () => opened++);
    document.querySelectorAll(".select__multi-value").forEach((n) => n.remove());
    await tick(150);
    expect(opened).toBe(0);
  });

  test("A disabled cascade child waits for its parent: the country is restored first and the state follows once it is enabled", async () => {
    mount(`<label for="country">Country</label><select id="country"><option value="">Select</option><option value="US">United States</option></select><label for="state">State</label><select id="state"><option value="">Select</option><option value="NC">North Carolina</option></select>${PAD}`);
    const country = byId<HTMLSelectElement>("country") as unknown as HTMLSelectElement;
    const state = byId<HTMLSelectElement>("state") as unknown as HTMLSelectElement;
    country.addEventListener("change", () => (state.disabled = country.value === ""));
    const engine = makeEngine(fakeHost(), { restoreCapMs: 800, restoreIdleMs: 300, restorePollMs: 25 });
    await runFill(engine);
    expect([country.value, state.value]).toEqual(["US", "NC"]);
    country.value = "";
    state.value = "";
    state.disabled = true;
    await until(() => country.value === "US" && state.value === "NC", 2000);
  });
});
