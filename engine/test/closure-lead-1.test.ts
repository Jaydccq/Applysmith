// Clean-room: docs/provenance.md#CR-ENGINE
// Parity rows closed by the lead: the native-submission hold's record and its guarantees.
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, test } from "vitest";
import { ADA, PAD, byId, fakeHost, makeEngine, mount, tick } from "./helpers";

function sourceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const p = join(dir, name);
    return statSync(p).isDirectory() ? sourceFiles(p) : p.endsWith(".ts") ? [p] : [];
  });
}

describe("Native submission hold", () => {
  test("a submission with no submitter is recorded as such and a recording failure still cancels it", async () => {
    mount(`<form id="apply"><input id="a"></form>${PAD}`);
    const engine = makeEngine(fakeHost(), { settleMs: 20 });
    engine.start({ applicant: ADA });
    const bare = new Event("submit", { cancelable: true, bubbles: true });
    byId("apply").dispatchEvent(bare);
    expect(bare.defaultPrevented).toBe(true);
    expect(engine.blockedSubmits()).toEqual([{ form: "apply", submitter: "no submitter" }]);
    const hostile = new Event("submit", { cancelable: true, bubbles: true });
    Object.defineProperty(hostile, "target", {
      get() {
        throw new Error("hostile target");
      },
    });
    window.dispatchEvent(hostile);
    expect(hostile.defaultPrevented).toBe(true);
    expect(engine.blockedSubmits().at(-1)).toEqual({ form: "form", submitter: "no submitter" });
    while (engine.status().state === "running") await tick(5);
  });

  test("the hold is armed only while a call is in flight and the engine never submits a form itself", async () => {
    mount(`<form id="apply"><input id="a"></form>${PAD}`);
    const engine = makeEngine(fakeHost());
    const event = () => {
      const e = new Event("submit", { cancelable: true, bubbles: true });
      byId("apply").dispatchEvent(e);
      return e.defaultPrevented;
    };
    expect(event()).toBe(false);
    engine.start({ applicant: ADA });
    expect(event()).toBe(true);
    while (engine.status().state === "running") await tick(5);
    expect(event()).toBe(false);
    // Programmatic form.submit() fires no event and cannot be held, so no engine source may call it.
    const root = join(__dirname, "..", "src");
    const offenders = sourceFiles(root).filter((f) => /\.submit\s*\(/.test(readFileSync(f, "utf8")));
    expect(offenders).toEqual([]);
  });
});

