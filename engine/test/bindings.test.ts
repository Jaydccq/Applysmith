// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Bindings to Go (the engine's half), and the Fake host scenario.
import { describe, expect, test } from "vitest";
import { bindingHost } from "../src/host";
import { BINDINGS, MAX_REQUEST_BYTES, type WireQuestion } from "../src/protocol";
import { batchQuestions } from "../src/stage-questions";
import { ADA, PAD, byId, fakeHost, makeEngine, mount, rowFor, runFill, summaryOf, until } from "./helpers";

describe("Bindings to Go", () => {
  test("Fake host: a complete fill of a synthetic page runs without the Go server or a browser", async () => {
    mount(`<label for="fn">First name</label><input id="fn"><label for="q">Are you over 18?</label>
      <select id="q"><option value="">Select</option><option>Yes</option><option>No</option></select>`);
    const host = fakeHost({ "are you over 18?": { outcome: "filled", value: "Yes" } });
    const s = summaryOf(await runFill(makeEngine(host)));
    expect(byId("fn").value).toBe("Ada");
    expect(byId("q").value).toBe("Yes");
    expect(s.counts.filled).toBe(2);
  });

  test("The match request carries the host, the posting context and each question's shape", async () => {
    mount(`<fieldset><legend>Are you legally able to work in the US?</legend>
        <label><input type="radio" name="auth" value="y"> Yes</label><label><input type="radio" name="auth" value="n"> No</label></fieldset>
      <label for="t">Tell us more</label><textarea id="t" maxlength="500"></textarea>
      <label for="pn">Preferred Name</label><input id="pn" type="text" maxlength="255">${PAD}`);
    const host = fakeHost();
    await runFill(makeEngine(host), { posting: { company: "Example Corp", title: "Engineer", url: "https://jobs.example.test/1" } });
    const req = host.matchRequests[0];
    expect(req?.posting).toEqual({ company: "Example Corp", title: "Engineer", url: "https://jobs.example.test/1" });
    expect(req?.host).toBeTypeOf("string");
    const radio = req?.questions.find((q) => q.kind === "radio-group");
    expect(radio).toMatchObject({ text: "Are you legally able to work in the US?", options: ["Yes", "No"], inputType: "yes_no", required: true });
    const area = req?.questions.find((q) => q.kind === "text");
    expect(area).toMatchObject({ inputType: "long_text", maxLength: 500 });
    expect(area?.selector).toBe('[id="t"]');
    // A single-line box with a long length limit is still a short answer.
    expect(req?.questions.find((q) => q.text === "Preferred Name")).toMatchObject({ inputType: "text", maxLength: 255 });
  });

  test("Cover letter attached: the request carries coverLetterAttached true", async () => {
    mount(`<label for="cl">Cover letter</label><input id="cl" type="file"><label for="q">Anything else</label><input id="q">`);
    const host = fakeHost();
    host.resume = async () => ({ fileName: "letter.pdf", mediaType: "application/pdf", contentBase64: btoa("%PDF-1.4 letter") });
    await runFill(makeEngine(host));
    expect(host.matchRequests[0]?.coverLetterAttached).toBe(true);
  });

  test("Resume observation: a slot showing a file, or one the run attaches, sets resumeAttached true", async () => {
    mount(`<label for="cv">Resume</label><input id="cv" type="file"><label for="q">Anything else</label><input id="q">`);
    const host = fakeHost();
    await runFill(makeEngine(host));
    expect(host.matchRequests[0]?.resumeAttached).toBe(true);
    expect("coverLetterAttached" in (host.matchRequests[0] as object)).toBe(false);
  });

  test("Resume observation: no document, no resumeAttached", async () => {
    mount(`<label for="cv">Resume</label><input id="cv" type="file"><label for="q">Anything else</label><input id="q">`);
    const host = fakeHost();
    host.resume = async () => null;
    await runFill(makeEngine(host));
    expect("resumeAttached" in (host.matchRequests[0] as object)).toBe(false);
  });

  test("Large country list: sent without options and bound by exact normalized label, else option-not-found", async () => {
    const options = Array.from({ length: 250 }, (_, i) => `<option>Country ${i}</option>`).join("");
    mount(`<label for="c">Which country do you live in?</label><select id="c"><option value="">Select</option>${options}<option>United States</option></select>${PAD}`);
    const host = fakeHost({ "which country do you live in?": { outcome: "filled", value: "united states" } });
    const s = summaryOf(await runFill(makeEngine(host)));
    expect(host.matchRequests[0]?.questions[0]?.options).toEqual([]);
    expect(byId("c").value).toBe("United States");
    expect(rowFor(s, "which country").outcome).toBe("filled");

    // Containment is never tried on a long list; a country's own aliases ("US", "USA") are names, not containment.
    mount(`<label for="c">Which country do you live in?</label><select id="c"><option value="">Select</option>${options}<option>Korea, Republic of</option></select>${PAD}`);
    const s2 = summaryOf(await runFill(makeEngine(fakeHost({ "which country do you live in?": { outcome: "filled", value: "Korea" } }))));
    expect(rowFor(s2, "which country").reason).toBe("option-not-found");
    expect(byId("c").value).toBe("");
  });

  test("A batch that fails after an earlier one came back: its questions are skipped for the failure, not recorded as the matcher's no-match", async () => {
    mount(Array.from({ length: 301 }, (_, i) => `<label for="t${i}">Custom question number ${i}</label><input id="t${i}">`).join(""));
    const host = fakeHost();
    const match = host.match.bind(host);
    host.match = async (req) => {
      const r = await match(req);
      host.failMatch = new Error("matcher down");
      return r;
    };
    const s = summaryOf(await runFill(makeEngine(host), {}, 40_000));
    expect(host.matchRequests.map((r) => r.questions.length)).toEqual([300, 1]);
    expect(rowFor(s, "question number 0").outcome).toBe("abstained");
    expect(rowFor(s, "question number 300")).toMatchObject({ outcome: "skipped", reason: "binding-unavailable" });
  }, 60_000);

  test("A request body is bounded to 256 KiB by splitting the batch", () => {
    const big = (i: number): WireQuestion => ({ selector: `#q${i}`, text: "x".repeat(40_000), kind: "text", inputType: "text", options: [], required: false });
    const batches = batchQuestions(Array.from({ length: 20 }, (_, i) => big(i)), { runId: "r", host: "h", posting: {} });
    expect(batches.flat()).toHaveLength(20);
    for (const b of batches) expect(JSON.stringify({ runId: "r", host: "h", posting: {}, questions: b }).length).toBeLessThanOrEqual(MAX_REQUEST_BYTES);
  });

  test("Questions with an identical normalized label and kind are collapsed at intake", async () => {
    mount(`<label for="a">Favourite colour?</label><input id="a"><label for="b">favourite colour</label><input id="b">`);
    const host = fakeHost({ "favourite colour?": { outcome: "filled", value: "Green" }, "favourite colour": { outcome: "filled", value: "Green" } });
    await runFill(makeEngine(host));
    expect(host.matchRequests.flatMap((r) => r.questions)).toHaveLength(1);
    expect(byId("a").value).toBe("Green");
    expect(byId("b").value).toBe("Green");
  });

  test("Matcher down: the profile pass still fills identity and contact fields, the question pass is skipped and the summary says matchUnavailable", async () => {
    mount(`<label for="fn">First name</label><input id="fn"><label for="em">Email</label><input id="em"><label for="q">Why us?</label><textarea id="q"></textarea>`);
    const host = fakeHost();
    host.failMatch = new Error("matcher down");
    const s = summaryOf(await runFill(makeEngine(host)));
    expect(byId("fn").value).toBe("Ada");
    expect(byId("em").value).toBe("ada@example.com");
    expect(byId("q").value).toBe("");
    expect(s.matchUnavailable).toBe(true);
    expect(rowFor(s, "why us").reason).toBe("binding-unavailable");
  });

  test("Binding host: three named bindings carry one JSON string each way, and a failure reply rejects", async () => {
    const calls: string[] = [];
    const g: Record<string, unknown> = {
      [BINDINGS.match]: async (p: string) => {
        calls.push(p);
        return JSON.stringify({ ok: true, result: { results: [] } });
      },
      [BINDINGS.resume]: async () => JSON.stringify({ ok: true, result: null }),
      [BINDINGS.terminal]: async () => JSON.stringify({ ok: false, error: "boom" }),
    };
    const host = bindingHost(g);
    expect(await host.match({ runId: "r1", host: "h", posting: {}, questions: [] })).toEqual({ results: [] });
    expect(JSON.parse(calls[0] as string).runId).toBe("r1");
    expect(await host.resume({ runId: "r1", document: "resume" })).toBeNull();
    await expect(host.terminal({ runId: "r1", state: "done" })).rejects.toThrow("boom");
    await expect(bindingHost({}).match({ runId: "r", host: "h", posting: {}, questions: [] })).rejects.toThrow(/not available/);
  });

  test("Every binding call carries the run id", async () => {
    mount(`<label for="cl">Resume</label><input id="cl" type="file"><label for="q">Anything else</label><input id="q">`);
    const host = fakeHost();
    const engine = makeEngine(host);
    await runFill(engine, { applicant: ADA, runId: "run-77" });
    await until(() => host.terminals.length === 1);
    expect(host.matchRequests).toHaveLength(1);
    expect(host.resumeRequests).toHaveLength(1);
    expect(host.matchRequests.every((m) => m.runId === "run-77")).toBe(true);
    expect(host.resumeRequests.every((m) => m.runId === "run-77")).toBe(true);
    expect(host.terminals.every((m) => m.runId === "run-77")).toBe(true);
  });
});
