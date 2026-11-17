// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Injection into an isolated world and re-injection (the engine's half).
import { describe, expect, test } from "vitest";
import type { Win } from "../src/dom";
import { GLOBAL_NAME, install } from "../src/entry";
import { ADA, byId, mount, tick } from "./helpers";

const win = window as unknown as Win;

describe("Engine installation", () => {
  test("Stale engine replaced: the older instance's listeners are removed, the new one reports its stamp", async () => {
    mount(`<form id="f"><label for="a">First name</label><input id="a"><button id="go">Submit</button></form><label for="b">Last</label><input id="b">`);
    const old = install(win);
    old.setStamp("old-stamp");
    old.start({ applicant: ADA });
    byId("go").click();
    expect(old.blockedSubmits()).toHaveLength(1);

    const fresh = install(win);
    fresh.setStamp("new-stamp");
    expect((win as unknown as Record<string, unknown>)[GLOBAL_NAME]).toBe(fresh);
    expect(fresh.status()).toMatchObject({ state: "idle", stamp: "new-stamp" });
    expect(old.status().state).toBe("idle");

    let reached = 0;
    byId("f").addEventListener("submit", (e) => {
      reached++;
      e.preventDefault();
    });
    byId("go").click();
    await tick(20);
    expect(reached).toBe(1);
  });

  test("The engine object is not an enumerable global", () => {
    install(win);
    expect(Object.keys(window)).not.toContain(GLOBAL_NAME);
    for (const key in window) expect(key).not.toBe(GLOBAL_NAME);
  });
});
