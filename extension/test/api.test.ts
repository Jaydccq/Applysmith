import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Api, OFFLINE, parseAddress, type Connection } from "../src/sw/api";
import { fail, installChrome, installFetch, ok } from "./chrome-mock";

function conn(over: Partial<Connection> = {}): Connection & { dropped: number } {
  const c = {
    dropped: 0,
    address: async () => "http://127.0.0.1:47319",
    token: async () => "t".repeat(43),
    rejected: async () => {
      c.dropped++;
    },
    ...over,
  };
  return c;
}

beforeEach(() => installChrome());
afterEach(() => vi.unstubAllGlobals());

describe("API address", () => {
  it("accepts only loopback addresses over http", () => {
    for (const a of ["127.0.0.1:47319", "http://localhost:9000", "http://[::1]:47319", "http://127.0.0.1"]) {
      expect(parseAddress(a).ok, a).toBe(true);
    }
    const bad = parseAddress("https://example.test:47319");
    expect(bad.ok).toBe(false);
    expect(!bad.ok && bad.message).toMatch(/only loopback/i);
    for (const a of ["http://0.0.0.0:1", "http://192.168.1.4:1", "http://127.0.0.1.example.com:1", "not an address"]) {
      expect(parseAddress(a).ok, a).toBe(false);
    }
  });

  it("refuses to call a stored address that is not loopback", async () => {
    const calls = installFetch(() => ok({}));
    const api = new Api(conn({ address: async () => "http://example.test:47319" }));
    await expect(api.call("/api/health")).rejects.toMatchObject({ code: "forbidden" });
    expect(calls).toHaveLength(0);
  });
});

describe("API client", () => {
  it("sends the bearer token and a request id and reads the envelope", async () => {
    const calls = installFetch(() => ok({ hello: "world" }));
    const api = new Api(conn({ client: async () => "id=install-1" }));
    await expect(api.call("/api/x", { method: "POST", body: { a: 1 } })).resolves.toEqual({ hello: "world" });
    expect(calls[0]!.headers.Authorization).toBe(`Bearer ${"t".repeat(43)}`);
    // The dashboard's list of extensions rests on this line (TestExtensionClientsAndUnpair).
    expect(calls[0]!.headers["X-Applysmith-Extension"]).toBe("id=install-1");
    expect(calls[0]!.headers["X-Request-Id"]).toMatch(/^req_[0-9a-f]{16}$/);
    expect(calls[0]!.body).toEqual({ a: 1 });
    expect(calls[0]!.url).toBe("http://127.0.0.1:47319/api/x");
  });

  it("retries a read once when the server cannot be reached, never a write", async () => {
    let n = 0;
    const calls = installFetch(() => {
      n++;
      if (n === 1) throw new TypeError("network");
      return ok({ fine: true });
    });
    const api = new Api(conn());
    await expect(api.call("/api/r")).resolves.toEqual({ fine: true });
    expect(calls).toHaveLength(2);

    const writes = installFetch(() => {
      throw new TypeError("network");
    });
    await expect(api.call("/api/w", { method: "POST", body: {} })).rejects.toMatchObject({ code: OFFLINE });
    expect(writes).toHaveLength(1);
  });

  it("drops the token when the server rejects it", async () => {
    installFetch(() => fail("unauthenticated", "no", 401));
    const c = conn();
    await expect(new Api(c).call("/api/r")).rejects.toMatchObject({ code: "unauthenticated" });
    expect(c.dropped).toBe(1);
  });
});
