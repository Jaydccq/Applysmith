// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Resilience to re-renders and user edits (identity refill, tail guard); Pipeline stages and their
// order (replaced form).
import { describe, expect, test } from "vitest";
import { IDENTITY_KEYS } from "../src/refill";
import { PAD, byId, fakeHost, makeEngine, mount, runFill, summaryOf, until, fireTrusted } from "./helpers";

const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set as (v: string) => void;


const CONTACT: [string, string][] = [
  ["First name", "fn"],
  ["Last name", "ln"],
  ["Email", "em"],
  ["Phone", "ph"],
  ["City", "ct"],
];

/** The contact block of one generation: the same labels under new ids, as a re-rendered form has. */
const block = (gen: number) => CONTACT.map(([label, id]) => `<label for="${id}${gen}">${label}</label><input id="${id}${gen}">`).join("");

const logCount = (engine: { log(): string[] }, text: string) => engine.log().filter((l) => l.includes(text)).length;

/** Calls `fn` each time the engine's log shows another fill pass has finished its cascade stage. */
function afterEachPass(engine: { log(): string[] }, fn: (pass: number) => void): () => void {
  let seen = 0;
  const timer = setInterval(() => {
    const n = engine.log().filter((l) => l.includes("cascade end")).length;
    while (seen < n) fn(++seen);
  }, 1);
  return () => clearInterval(timer);
}

describe("Replaced form", () => {
  const RESUME = `<label for="up">Resume</label><input type="file" id="up">`;
  /** The refill stage settles long enough for the page to swap its block between the pass and the check. */
  const SLOW = { refillSettleMs: 150 };

  test("A parse swaps the contact block after the first pass wrote five fields: one extra pass fills the new block and the document is not attached again", async () => {
    mount(`${RESUME}<div id="blk">${block(0)}</div>${PAD}`);
    const host = fakeHost();
    const engine = makeEngine(host, SLOW);
    const stop = afterEachPass(engine, (pass) => {
      if (pass === 1) (byId("blk") as HTMLElement).innerHTML = block(1);
    });
    const s = summaryOf(await runFill(engine));
    stop();
    expect(["fn1", "ln1", "em1", "ph1", "ct1"].map((id) => byId(id).value)).toEqual(["Ada", "Example", "ada@example.com", "+1 555 0100", "Durham"]);
    expect(host.resumeRequests).toHaveLength(1);
    expect(logCount(engine, "refill form-replaced")).toBe(1);
    expect(s.rows.some((r) => r.selector.includes('"fn0"'))).toBe(false);
  });

  test("A page that swaps the block after every pass gets at most two extra passes", async () => {
    mount(`<div id="blk">${block(0)}</div>${PAD}`);
    const engine = makeEngine(fakeHost(), SLOW);
    let gen = 0;
    const stop = afterEachPass(engine, () => {
      (byId("blk") as HTMLElement).innerHTML = block(++gen);
    });
    await runFill(engine);
    stop();
    expect(logCount(engine, "refill form-replaced")).toBe(2);
    expect(gen).toBe(3);
    expect(byId("fn3").value).toBe("");
  });

  test("Two lost writes are not a replaced form: no extra pass, the identity refill puts them back", async () => {
    mount(`<div id="blk">${block(0)}</div>${PAD}`);
    byId("ct0").addEventListener("input", () =>
      setTimeout(() => {
        setter.call(byId("fn0"), "");
        setter.call(byId("em0"), "");
      }, 0),
    );
    const engine = makeEngine(fakeHost());
    await runFill(engine);
    expect(logCount(engine, "refill form-replaced")).toBe(0);
    expect(logCount(engine, "refill identity")).toBeGreaterThan(0);
    expect([byId("fn0").value, byId("em0").value]).toEqual(["Ada", "ada@example.com"]);
  });
});

describe("User edits during a replaced form", () => {
  const ASK = `<label for="fav">Favourite thing</label><input id="fav">`;
  const page = () => `${block(0)}${ASK}`;

  test("User deletes an answer: a box the user cleared mid-run stays empty through the extra pass, refill, restore and tail guard when the form is then replaced", async () => {
    mount(`<div id="blk">${page()}</div>${PAD}`);
    const host = fakeHost({ "favourite thing": { outcome: "filled", value: "Tea" } });
    const engine = makeEngine(host, { refillSettleMs: 150, restorePollMs: 20, restoreCapMs: 600, restoreIdleMs: 300, tailCapMs: 600 });
    const stop = afterEachPass(engine, (pass) => {
      if (pass !== 1) return;
      // The user clears two boxes with real key events, then the page re-renders the whole block.
      for (const id of ["em0", "fav"]) {
        setter.call(byId(id), "");
        fireTrusted(byId(id), "input");
      }
      (byId("blk") as HTMLElement).innerHTML = page();
    });
    const s = summaryOf(await runFill(engine));
    await until(() => logCount(engine, "tail end") > 0, 3000);
    stop();
    expect(logCount(engine, "refill form-replaced")).toBe(1);
    // The rest of the block was written again, so the extra pass really ran; the two the user owns were not.
    expect(["fn0", "ln0", "ph0", "ct0"].map((id) => byId(id).value)).toEqual(["Ada", "Example", "+1 555 0100", "Durham"]);
    expect([byId("em0").value, byId("fav").value]).toEqual(["", ""]);
    expect(s.rows.find((r) => r.label === "Email")?.outcome).not.toBe("filled");
  });
});

describe("Identity refill", () => {

  test("State and country are never refilled, and neither is a box the user touched", async () => {
    expect(IDENTITY_KEYS.has("state")).toBe(false);
    expect(IDENTITY_KEYS.has("country")).toBe(false);
    mount(`<label for="co">Country</label><input id="co"><div id="blk">${block(0)}</div>${PAD}`);
    byId("ct0").addEventListener("input", () =>
      setTimeout(() => {
        setter.call(byId("co"), "");
        fireTrusted(byId("fn0"), "input");
        setter.call(byId("fn0"), "");
      }, 0),
    );
    const engine = makeEngine(fakeHost());
    await runFill(engine);
    expect(logCount(engine, "refill identity")).toBe(0);
    expect(byId("fn0").value).toBe("");
  });
});

describe("State and country refill", () => {
  test("A blanked state in a plain box is refilled; a blanked country is not while the city beside it holds a value", async () => {
    mount(`<label for="st">State</label><input id="st"><label for="co">Country</label><input id="co"><div id="blk">${block(0)}</div>${PAD}`);
    byId("ct0").addEventListener("input", () =>
      setTimeout(() => {
        setter.call(byId("st"), "");
        setter.call(byId("co"), "");
      }, 0),
    );
    const engine = makeEngine(fakeHost());
    await runFill(engine);
    // One box, the state: the country is left to the restore pass, which drives it as its lane did.
    expect(engine.log().filter((l) => l.includes("refill identity")).map((l) => /boxes=\d+/.exec(l)?.[0])).toEqual(["boxes=1"]);
    expect(byId("st").value).toBe("North Carolina");
  });

  test("A blanked country is refilled where no state, city or postal code would be wiped by it", async () => {
    mount(`<label for="co">Country</label><input id="co"><label for="fn0">First name</label><input id="fn0"><label for="ln0">Last name</label><input id="ln0">${PAD}`);
    byId("ln0").addEventListener("input", () => setTimeout(() => setter.call(byId("co"), ""), 0));
    const engine = makeEngine(fakeHost());
    await runFill(engine);
    expect(logCount(engine, "refill identity")).toBe(1);
    expect(byId("co").value).toBe("United States");
  });
});

describe("Tail guard", () => {
  const LONG = { restoreCapMs: 5000, restoreIdleMs: 200, tailCapMs: 3000 };

  /** The input node replaced by an empty one with the same id, as a late remount does. */
  function remount(id: string): HTMLInputElement {
    const old = byId(id);
    const fresh = document.createElement("input");
    fresh.id = id;
    old.replaceWith(fresh);
    return fresh;
  }

  test("A phone box remounted after the summary went out is filled again, and the guard stops after three quiet looks", async () => {
    mount(`<div id="blk">${block(0)}</div>${PAD}`);
    const engine = makeEngine(fakeHost(), LONG);
    await runFill(engine);
    // The summary exists and the guard is still watching.
    expect(logCount(engine, "tail end")).toBe(0);
    const fresh = remount("ph0");
    expect(fresh.value).toBe("");
    await until(() => byId("ph0").value !== "", 3000);
    await until(() => engine.log().some((l) => l.includes("tail end") && l.includes("reason=stable")), 3000);
  });

  test("A box the user touched is left alone", async () => {
    mount(`<div id="blk">${block(0)}</div>${PAD}`);
    const engine = makeEngine(fakeHost(), { restoreCapMs: 5000, restoreIdleMs: 5000, tailCapMs: 120 });
    await runFill(engine);
    fireTrusted(byId("ph0"), "input");
    const fresh = remount("ph0");
    await until(() => engine.log().some((l) => l.includes("tail end")), 3000);
    expect(fresh.value).toBe("");
  });

  test("It lets go of the applicant data when the run is stopped", async () => {
    mount(`<div id="blk">${block(0)}</div>${PAD}`);
    const engine = makeEngine(fakeHost(), LONG);
    const s = await runFill(engine);
    expect(engine.stop((s as { runId: string }).runId)).toBe(false);
    await until(() => engine.log().some((l) => l.includes("tail end") && l.includes("reason=stopped")), 3000);
    remount("ph0");
    await new Promise((r) => setTimeout(r, 120));
    expect(byId("ph0").value).toBe("");
  });
});
