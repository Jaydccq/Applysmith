import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { claimCode, claimWait, pasteToken } from "../src/sw/pairing";
import { api, connection } from "../src/sw/state";
import * as store from "../src/sw/store";
import { fail, installChrome, installFetch, ok, type Call } from "./chrome-mock";

const TOKEN = "A".repeat(43);

let env: ReturnType<typeof installChrome>;
beforeEach(() => {
  env = installChrome();
});
afterEach(() => vi.unstubAllGlobals());

function server(over: (c: Call) => Response | undefined = () => undefined) {
  return installFetch((c) => {
    const custom = over(c);
    if (custom) return custom;
    const path = new URL(c.url).pathname;
    if (path === "/api/pairing/claim") return c.body && (c.body as { code: string }).code === "ABCDEFGHJK" ? ok({ token: TOKEN }) : fail("unauthenticated", "bad code", 401);
    if (path === "/api/answer-bank/settings") return c.headers.Authorization === `Bearer ${TOKEN}` ? ok({}) : fail("unauthenticated", "no", 401);
    if (path === "/api/health") return ok({ status: "ok" });
    return fail("not_found", "no", 404);
  });
}

describe("pairing code", () => {
  it("is redeemed, verified with one authenticated request, and the token is stored", async () => {
    const calls = server();
    await claimCode(api, "abcde-fghjk");
    expect(await store.get("token")).toBe(TOKEN);
    expect(calls.map((c) => new URL(c.url).pathname)).toEqual(["/api/pairing/claim", "/api/answer-bank/settings"]);
    expect(calls[0]!.headers.Authorization).toBeUndefined();
    expect(calls[1]!.headers.Authorization).toBe(`Bearer ${TOKEN}`);
    for (const c of calls) expect(c.url).not.toContain(TOKEN);
    expect(await connection()).toBe("connected");
  });

  it("with a wrong or old code stores no token", async () => {
    server();
    await expect(claimCode(api, "WRONGWRONG")).rejects.toMatchObject({ code: "unauthenticated" });
    expect(await store.get("token")).toBeUndefined();
    expect(await connection()).toBe("unpaired");
  });

  it("is throttled: after rate_limited no further claim is sent until the wait passes", async () => {
    const calls = server((c) => (new URL(c.url).pathname === "/api/pairing/claim" ? fail("rate_limited", "slow", 429, { details: { retryAfterSeconds: 45 } }) : undefined));
    await expect(claimCode(api, "ABCDEFGHJK")).rejects.toMatchObject({ code: "rate_limited", retryAfter: 45 });
    expect(calls).toHaveLength(1);
    await expect(claimCode(api, "ABCDEFGHJK")).rejects.toMatchObject({ code: "rate_limited" });
    expect(calls).toHaveLength(1);
    const wait = await claimWait();
    expect(wait).toBeGreaterThan(40);
    expect(wait).toBeLessThanOrEqual(45);
    expect(await claimWait(Date.now() + 46_000)).toBe(0);
  });

  it("carrying a port is claimed there, and that address is saved only once paired", async () => {
    const calls = server((c) => (new URL(c.url).port === "54712" ? undefined : fail("unauthenticated", "elsewhere", 401)));
    await expect(claimCode(api, "54712-WRONGWRONG")).rejects.toMatchObject({ code: "unauthenticated" });
    expect(await store.get("address")).toBe("http://127.0.0.1:47319");
    await claimCode(api, " 54712 - abcde fghjk ");
    expect(calls.map((c) => c.url)).toContain("http://127.0.0.1:54712/api/answer-bank/settings");
    expect(await store.get("address")).toBe("http://127.0.0.1:54712");
    expect(await connection()).toBe("connected");
  });

  it("never travels in a URL", async () => {
    const calls = server();
    await claimCode(api, "ABCDEFGHJK");
    for (const c of calls) {
      const u = new URL(c.url);
      expect(u.search).toBe("");
      expect(c.url).not.toContain("ABCDEFGHJK");
    }
  });
});

describe("token pasted by hand", () => {
  it("is verified with one request and then connected", async () => {
    const calls = server();
    await pasteToken(api, ` ${TOKEN}\n`);
    expect(calls).toHaveLength(1);
    expect(await store.get("token")).toBe(TOKEN);
  });

  it("is refused when it does not look like a token or the server rejects it", async () => {
    server();
    await expect(pasteToken(api, "short")).rejects.toMatchObject({ code: "validation_failed" });
    await expect(pasteToken(api, "B".repeat(43))).rejects.toMatchObject({ code: "unauthenticated" });
    expect(await store.get("token")).toBeUndefined();
  });
});

describe("rotated token", () => {
  it("drops the token, shows unpaired and starts no pairing by itself", async () => {
    const calls = server();
    await pasteToken(api, TOKEN);
    calls.length = 0;
    installFetch((c) => {
      calls.push(c);
      const path = new URL(c.url).pathname;
      return path === "/api/health" ? ok({}) : fail("unauthenticated", "rotated", 401);
    });
    expect(await connection()).toBe("unpaired");
    expect(await store.get("token")).toBeUndefined();
    expect(calls.some((c) => new URL(c.url).pathname === "/api/pairing/claim")).toBe(false);
  });

  it("reports offline when the application is not running", async () => {
    installFetch(() => {
      throw new TypeError("down");
    });
    expect(await connection()).toBe("offline");
  });
});

describe("token custody", () => {
  it("restricts both storage areas to trusted contexts", async () => {
    // The restriction runs once per worker: a fresh module, so an earlier session write in this file cannot have used it up.
    vi.resetModules();
    const fresh = await import("../src/sw/store");
    await fresh.restrictToTrusted();
    expect(env.levels.sort()).toEqual(["local:TRUSTED_CONTEXTS", "session:TRUSTED_CONTEXTS"]);
  });
});
