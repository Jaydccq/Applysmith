// @vitest-environment node
import { describe, expect, it } from "vitest";
import { WireError } from "./decode";
import { applyBatchView, applyList, applyRunView, applySkipList, applySkipStatus, applyTabShown } from "./decoders_apply";

const stamp = "2026-10-12T09:00:00.000Z";
const item = { id: 1, company: "Northwind Example", role: "Staff Engineer", lane: "apply-now", date: "2026-09-28", version: 1, state: "idle" };

describe("apply decoders", () => {
  it("reads a list row with only its required fields and treats null maps and slices as empty", () => {
    const out = applyList({ lanes: [{ lane: "apply-now", total: 1, items: [item] }, { lane: "selective", total: 0, items: null }], page: 1, pageSize: 10, counts: { states: null, lanes: { "apply-now": 1 } } }, "$");
    expect(out.lanes[1].items).toEqual([]);
    expect(out.counts.states).toEqual({});
    expect(out.lanes[0].items[0].attempt).toBeUndefined();
  });

  it("reads an attempt, a kept tab and a run id", () => {
    const attempt = { runId: "r1", at: stamp, mode: "submit", outcome: { kind: "stopped", stage: "fill", class: "defect", reason: "x", owner: "defect" } };
    const out = applyList({ lanes: [{ lane: "apply-now", total: 1, items: [{ ...item, state: "failed", attempt, runId: "r1", keptTab: true }] }], page: 1, pageSize: 10, counts: { states: { failed: 1 }, lanes: {} } }, "$");
    expect(out.lanes[0].items[0].attempt?.outcome.owner).toBe("defect");
    expect(out.lanes[0].items[0].keptTab).toBe(true);
  });

  it("rejects a row without a state, an unknown field and a form value", () => {
    const { state: _state, ...bare } = item;
    expect(() => applyList({ lanes: [{ lane: "x", total: 1, items: [bare] }], page: 1, pageSize: 10, counts: { states: {}, lanes: {} } }, "$")).toThrow(WireError);
    expect(() => applyList({ lanes: [{ lane: "x", total: 1, items: [{ ...item, answers: { name: "x" } }] }], page: 1, pageSize: 10, counts: { states: {}, lanes: {} } }, "$")).toThrow(/unknown field/);
  });

  it("reads a run record, its stages and the optional links", () => {
    const run = { runId: "r1", mode: "dry-run", caller: "cli", host: "jobs.example.com", startedAt: stamp, outcome: { kind: "filled" }, stages: null, pages: 0, keptTab: false };
    expect(applyRunView(run, "$").stages).toEqual([]);
    const full = applyRunView({ ...run, batchId: "b1", company: "Northwind Example", role: "Staff Engineer", ats: "lever", finishedAt: stamp, reportNumber: 7, candidacyId: 3, stages: [{ stage: "fill", status: "ok", detail: "", startedAt: stamp, ms: 5 }] }, "$");
    expect(full.candidacyId).toBe(3);
    expect(full.stages[0].ms).toBe(5);
    expect(() => applyRunView({ ...run, stages: [{ stage: "fill", status: "ok", detail: "", startedAt: stamp, ms: 1.5 }] }, "$")).toThrow(WireError);
  });

  it("reads a batch with nil slices", () => {
    const out = applyBatchView({ batchId: "b1", state: "open", mode: "submit", caller: "agent", createdAt: stamp, rows: null, owners: null, defects: null }, "$");
    expect(out.rows).toEqual([]);
    const row = { index: 1, company: "A", role: "B", host: "h.example.com", state: "pending", note: "skip list" };
    expect(applyBatchView({ batchId: "b1", state: "open", mode: "submit", caller: "agent", createdAt: stamp, rows: [row], owners: [{ owner: "user", companies: null }], defects: [{ stage: "fill", class: "defect", reason: "r", companies: ["A"] }] }, "$").owners[0].companies).toEqual([]);
  });

  it("reads the skip list, the skip status and the tab reply", () => {
    expect(applySkipList({ entries: null, defaulted: true, differs: false, defaults: ["Apple"], revision: "r1" }, "$").entries).toEqual([]);
    expect(applySkipStatus({ company: "Apple", listed: true, entry: "Apple" }, "$").entry).toBe("Apple");
    expect(applyTabShown({ runId: "r1" }, "$").runId).toBe("r1");
    expect(() => applySkipList({ entries: [], defaulted: true, differs: false, defaults: [] }, "$")).toThrow(/missing field/);
  });
});
