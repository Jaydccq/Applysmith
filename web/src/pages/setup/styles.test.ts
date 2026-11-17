// @vitest-environment node
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const css = readFileSync(new URL("../../styles.css", import.meta.url), "utf8");
const setupRules = css.split("\n").filter((l) => /\.setup-nav|\.field\b|code\.pre/.test(l));

describe("setup stylesheet", () => {
  it("gives no setup rule a fixed width", () => {
    expect(setupRules.length).toBeGreaterThan(0);
    expect(setupRules.filter((l) => /(^|[\s{;])width:\s*\d+(px|rem)/.test(l))).toEqual([]);
  });

  it("wraps long command lines instead of widening the page", () => {
    expect(css).toMatch(/code\.pre \{[^}]*overflow-wrap: anywhere/);
  });
});
