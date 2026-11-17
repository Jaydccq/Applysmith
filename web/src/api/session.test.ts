import { beforeEach, describe, expect, it } from "vitest";
import { SESSION_KEY_HEADER, adoptSessionKey, forgetSession, isSignedOut, markSignedOut, sessionKey } from "./session";

const KEY = "A".repeat(43);

beforeEach(() => forgetSession());

describe("session key", () => {
  it("is taken from the fragment of the page address, kept, and removed from the address", () => {
    window.history.replaceState(null, "", `/#k=${KEY}`);
    adoptSessionKey();
    expect(sessionKey()).toBe(KEY);
    expect(window.location.hash).toBe("");
    expect(window.localStorage.getItem("applysmith.sessionKey")).toBe(KEY);
  });

  it("survives a reload, which has no fragment", () => {
    window.localStorage.setItem("applysmith.sessionKey", KEY);
    adoptSessionKey();
    expect(sessionKey()).toBe(KEY);
  });

  it("ignores a fragment that is not a key and leaves it alone", () => {
    window.history.replaceState(null, "", "/#section");
    adoptSessionKey();
    expect(sessionKey()).toBeNull();
    expect(window.location.hash).toBe("#section");
  });

  it("is dropped, with the stored copy, once the server refuses it", () => {
    window.localStorage.setItem("applysmith.sessionKey", KEY);
    adoptSessionKey();
    markSignedOut();
    expect(isSignedOut()).toBe(true);
    expect(sessionKey()).toBeNull();
    expect(window.localStorage.getItem("applysmith.sessionKey")).toBeNull();
  });

  it("names the header the server reads", () => {
    expect(SESSION_KEY_HEADER).toBe("X-Applysmith-Session-Key");
  });
});
