import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((n) => {
    const p = join(dir, n);
    return statSync(p).isDirectory() ? sources(p) : p.endsWith(".ts") ? [p] : [];
  });
}

describe("token custody", () => {
  it("only the service worker touches extension storage, cookies or the token", () => {
    for (const file of sources("src")) {
      if (file.startsWith(join("src", "sw"))) continue;
      const text = readFileSync(file, "utf8");
      expect(text, file).not.toMatch(/chrome\.storage|chrome\.cookies|\bAuthorization\b|\bBearer\b/);
    }
  });

  it("no source logs a token, code or password", () => {
    for (const file of sources("src")) {
      const text = readFileSync(file, "utf8");
      expect(text, file).not.toMatch(/console\.\w+\([^)]*(token|password|passcode|cookie)/i);
    }
  });
});
