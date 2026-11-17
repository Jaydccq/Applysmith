import { describe, expect, it } from "vitest";
import { intakePreview, intakeResult, setupApplied, setupState } from "./decoders_setup";
import { WireError } from "./decode";
import { setupState as fixture, setupStep } from "../testing/fixtures_setup";

describe("setup decoders", () => {
  it("reads the state the server sends", () => {
    const s = fixture();
    expect(setupState(JSON.parse(JSON.stringify(s)), "$")).toEqual(s);
  });

  it("reads null lists as empty, which is how Go encodes a nil slice", () => {
    const step = { ...setupStep("cv"), reasons: null, warnings: null, commands: null, fields: null };
    const out = setupState({ progress: 0, next: "cv", steps: [step], secrets: null }, "$");
    expect(out.steps[0].reasons).toEqual([]);
    expect(out.secrets).toEqual([]);
  });

  it("rejects a field it does not know and a status that is not text", () => {
    expect(() => setupState({ ...fixture(), extra: 1 }, "$")).toThrow(WireError);
    expect(() => setupState({ ...fixture(), steps: [{ ...setupStep("cv"), status: 3 }] }, "$")).toThrow(WireError);
  });

  it("reads an apply answer with its draft", () => {
    const out = setupApplied({ step: setupStep("cv"), wrote: true, notes: null, untouched: [], draft: { employment: [{ employer: "Northwind Example" }], education: null, skills: ["Go"] } }, "$");
    expect(out.notes).toEqual([]);
    expect(out.draft?.education).toEqual([]);
  });

  it("reads an evidence preview, with Go's null lists as empty and no unknown field", () => {
    const item = { id: "a1", type: "signal", kind: "skill", summary: "Django", entry: { name: "Django" }, sources: [{ label: "outline.txt", hash: "h", locator: "line 3", origin: "inferred" }], fields: null, needsMonth: null, question: "Do you have it?" };
    const out = intakePreview({ revision: "r", documents: null, items: [item], notes: null }, "$");
    expect(out.documents).toEqual([]);
    expect(out.items[0].fields).toEqual([]);
    expect(out.items[0].entry).toEqual({ name: "Django" });
    expect(() => intakePreview({ revision: "r", documents: [], items: [{ ...item, extra: 1 }], notes: [] }, "$")).toThrow(WireError);
    expect(intakeResult({ wrote: false, revision: "r", applied: null, notes: [] }, "$").applied).toEqual([]);
  });
});
