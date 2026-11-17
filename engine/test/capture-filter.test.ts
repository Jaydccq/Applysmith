// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Sensitive controls and redacted fragments (manual-fill capture rules).
import { describe, expect, test } from "vitest";
import { captureDrop } from "../src/capture-filter";
import vectors from "./capture-vectors.json";

describe("Manual-fill capture rules", () => {
  test.each(vectors)("$name", (v) => {
    const q = v.input.question.startsWith("REPEAT:") ? (() => { const [, s, n] = v.input.question.split(":"); return (s as string).repeat(Number(n)); })() : v.input.question;
    expect(captureDrop({ ...v.input, question: q }) ?? "").toBe(v.want);
  });
});
