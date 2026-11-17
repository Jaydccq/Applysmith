// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Page API (summary rows and counts), Free-text answers require review, Uncertainty events and the user's own edits,
// Question pass (watch for controls shown after the run, re-answering a declined control).
import { describe, expect, test } from "vitest";
import { PAD, byId, fakeHost, makeEngine, mount, rowFor, runFill, summaryOf, tick, until } from "./helpers";
import vocabulary from "./matcher-reasons.json";
import { DELIBERATE_MATCHER, KNOWN_ABSTAIN } from "../src/answers";
import type { LearningEvent } from "../src/protocol";

describe("Summary rows", () => {
  test("filled outcome with applied=false is a failed row", async () => {
    mount(`<label for="n">Nickname</label><input id="n"><label for="y">Years of experience</label><input id="y" value="6">${PAD}`);
    // The page throws away what is typed into the nickname box.
    byId("n").addEventListener("input", () => {
      (Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set as (v: string) => void).call(byId("n"), "");
    });
    const host = fakeHost({ nickname: { outcome: "filled", value: "Ada" }, "years of experience": { outcome: "filled", value: "6" } });
    const s = summaryOf(await runFill(makeEngine(host)));
    // The matcher decided "filled" and the driver could not commit it: the row says failed, with the reason, and the box is a leftover.
    expect(rowFor(s, "Nickname")).toMatchObject({ outcome: "failed", reason: "write-not-committed", required: false });
    expect(s.counts).toMatchObject({ failed: 1, filled: 0 });
    // A control that already held the desired value is satisfied, not failed, and raises no failure event.
    expect(rowFor(s, "Years of experience").outcome).toBe("prefilled");
    expect((s.events ?? []).filter((e) => e.reason === "failed").map((e) => e.question)).toEqual(["Nickname"]);
  });

  test("placeholder-only abstain is not counted as required-unfilled", async () => {
    mount(`<label for="a">Pick a team *</label><input id="a" required><label for="b">Describe your setup *</label><input id="b" required><label for="c">Sensitive thing *</label><input id="c" required>${PAD}`);
    const host = fakeHost({
      "pick a team": { outcome: "abstained", reason: "placeholder-empty" },
      "describe your setup": { outcome: "abstained", reason: "low-confidence" },
      "sensitive thing": { outcome: "abstained", reason: "sensitive-category" },
    });
    const s = summaryOf(await runFill(makeEngine(host)));
    // The matcher's own reason is kept on the row; a deliberate blank is not work for the person.
    expect(rowFor(s, "Pick a team")).toMatchObject({ outcome: "abstained", matcherReason: "placeholder-empty" });
    expect(rowFor(s, "Describe your setup")).toMatchObject({ outcome: "abstained", matcherReason: "low-confidence" });
    expect(s.counts.abstained).toBe(3);
    expect(s.counts.requiredUnfilled).toBe(1);
  });

  test("Every matcher reason the engine names is one the matcher sends (the vocabulary shared with internal/answerbank/match)", () => {
    const sent = new Set(vocabulary.reasons);
    for (const reason of [...DELIBERATE_MATCHER, ...KNOWN_ABSTAIN]) expect(sent.has(reason), reason).toBe(true);
    expect(DELIBERATE_MATCHER.has("placeholder-empty")).toBe(true);
  });
});

describe("Free-text drafts", () => {
  test("criminal-history explanation box is not offered for a draft", async () => {
    mount(`<label for="c">If you have been convicted of an offense, please explain</label><textarea id="c"></textarea>
      <label for="v">Do you need a visa? If yes, please explain</label><textarea id="v"></textarea>
      <label for="w">Why do you want this role?</label><textarea id="w"></textarea>
      <div contenteditable="true" id="doc1">A stray paragraph of a document</div>
      <form><label for="ed">Cover note</label><div id="ed" role="textbox" aria-multiline="true" contenteditable="true"></div></form>${PAD}`);
    const host = fakeHost({
      "if you have been convicted of an offense, please explain": { outcome: "filled-model", value: "I was convicted of a degree.", reviewed: true },
      "do you need a visa? if yes, please explain": { outcome: "filled-model", value: "Citizen" },
      "why do you want this role?": { outcome: "filled-model", value: "Because of the mission." },
    });
    const s = summaryOf(await runFill(makeEngine(host)));
    // A model-composed text never reaches the offense or the "if yes" box, not as a draft and not as a reviewed answer.
    expect(byId("c").value).toBe("");
    expect(byId("v").value).toBe("");
    expect(s.drafts.map((d) => d.label)).toEqual(["Why do you want this role?"]);
    expect(rowFor(s, "convicted")).toMatchObject({ outcome: "abstained", reason: "no-answer" });
    // Only a region bound to a form or named by a label is a field at all.
    const labels = s.rows.map((r) => r.label);
    expect(labels).toContain("Cover note");
    expect(labels.some((l) => l.includes("stray paragraph"))).toBe(false);
    expect(s.rows.some((r) => r.selector.includes("doc1"))).toBe(false);
  });
});

describe("Watch after the run", () => {
  test("required control revealed after the run is reported once", async () => {
    mount(`<label for="g">Where do you live?</label><input id="g"><div id="later" hidden><label for="wa">Are you authorized to work there?</label><input id="wa"><label for="opt">Notes (optional)</label><input id="opt" aria-required="false"><label for="have">Seen before</label><input id="have" value="x"></div>${PAD}`);
    const engine = makeEngine(fakeHost({ "where do you live?": { outcome: "abstained", reason: "ask-me", terminal: true } }));
    await runFill(engine);
    expect(engine.drainEvents()).toEqual([]);
    // The person answers the gate and the page shows the follow-ups.
    byId("later").hidden = false;
    const seen: LearningEvent[] = [];
    await until(() => {
      seen.push(...engine.drainEvents());
      return seen.length > 0;
    });
    // The page keeps changing; the same controls are not reported again.
    byId("later").setAttribute("class", "shown");
    byId("later").style.display = "block";
    await tick(700);
    seen.push(...engine.drainEvents());
    expect(seen.map((e) => [e.kind, e.reason, e.question])).toEqual([["uncertainty", "revealed-after-run", "Are you authorized to work there?"]]);
    expect(seen[0]?.required).toBe(true);
  });
});

describe("Answering a control by selector", () => {
  test("a control the scan declined still resolves to a question for a selector-addressed answer", async () => {
    mount(`<label><input type="radio" id="lone"> A lone choice the page does not insist on</label>
      <label for="held">Current employer</label><input id="held" value="Wrong Parse Inc">${PAD}`);
    const engine = makeEngine(fakeHost());
    await runFill(engine);
    // The question pass declined both: the lone optional radio is not a question, and the filled box is not asked again.
    expect(byId("lone").checked).toBe(false);
    const reply = await engine.answer([
      { selector: '[id="lone"]', outcome: "filled", value: "A lone choice the page does not insist on" },
      { selector: '[id="held"]', outcome: "filled-review", value: "Example Corp" },
      { selector: '[id="nothing"]', outcome: "filled", value: "x" },
    ]);
    expect(reply.handled).toBe(3);
    expect(byId("lone").checked).toBe(true);
    expect(byId("held").value).toBe("Example Corp");
    // A selector that resolves to nothing is reported, not guessed.
    expect(reply.results[2]).toMatchObject({ outcome: "failed", reason: "control-detached" });
  });
});
