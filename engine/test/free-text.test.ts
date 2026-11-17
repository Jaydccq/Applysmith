// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Free-text answers require review.
import { describe, expect, test } from "vitest";
import { PAD, byId, fakeHost, makeEngine, mount, rowFor, runFill, summaryOf } from "./helpers";

const FORM = `<label for="t">Why do you want to work here?</label><textarea id="t"></textarea>${PAD}`;
const Q = "why do you want to work here?";

describe("Free-text answers require review", () => {
  test("Unreviewed draft held: a composed paragraph without reviewed true leaves the textarea empty, the row skipped awaiting-review and the draft in the summary", async () => {
    mount(FORM.replace("<textarea", "<textarea required"));
    const s = summaryOf(await runFill(makeEngine(fakeHost({ [Q]: { outcome: "filled-model", value: "I enjoy building reliable software." } }))));
    expect(byId("t").value).toBe("");
    expect(rowFor(s, "why do you").reason).toBe("awaiting-review");
    expect(rowFor(s, "why do you").outcome).toBe("skipped");
    expect(s.drafts).toEqual([{ selector: '[id="t"]', label: "Why do you want to work here?", text: "I enjoy building reliable software." }]);
    expect(s.counts.requiredUnfilled).toBe(1);
  });

  test("Reviewed draft written: the host resubmits the same draft with reviewed true through answer", async () => {
    mount(FORM);
    const engine = makeEngine(fakeHost({ [Q]: { outcome: "filled-model", value: "I enjoy building reliable software." } }));
    await runFill(engine);
    const reply = await engine.answer([{ selector: '[id="t"]', outcome: "filled-model", value: "I enjoy building reliable software.", reviewed: true }]);
    expect(reply.results[0]).toMatchObject({ outcome: "filled", value: "I enjoy building reliable software." });
    expect(byId("t").value).toBe("I enjoy building reliable software.");
  });

  test("Refusal token: a resolution whose text is NO_ANSWER, or empty, is never written and the row is abstained", async () => {
    mount(FORM);
    for (const value of ["NO_ANSWER", "   "]) {
      const s = summaryOf(await runFill(makeEngine(fakeHost({ [Q]: { outcome: "filled", value } }))));
      expect(byId("t").value).toBe("");
      expect(rowFor(s, "why do you").outcome).toBe("abstained");
    }
  });

  test("Bank-authored text: a user-written bank entry supplies a long answer and is written as filled without a review requirement", async () => {
    mount(FORM);
    const s = summaryOf(await runFill(makeEngine(fakeHost({ [Q]: { outcome: "filled", value: "Because I like the mission." } }))));
    expect(byId("t").value).toBe("Because I like the mission.");
    expect(rowFor(s, "why do you")).toMatchObject({ outcome: "filled", review: false });
  });

  test("A choice control answered by the grounded fallback is written with the review flag and is not a draft", async () => {
    mount(`<label for="c">How did you hear about us?</label><select id="c"><option value="">Select</option><option>Referral</option></select>${PAD}`);
    const s = summaryOf(await runFill(makeEngine(fakeHost({ "how did you hear about us?": { outcome: "filled-model", value: "Referral" } }))));
    expect(byId("c").value).toBe("Referral");
    expect(rowFor(s, "hear about")).toMatchObject({ outcome: "filled", review: true });
    expect(s.drafts).toHaveLength(0);
  });
});
