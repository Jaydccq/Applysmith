// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Pipeline stages and their order (and the question pass).
import { describe, expect, test } from "vitest";
import { ADA, PAD, byId, fakeHost, makeEngine, mount, rowFor, runFill, summaryOf, tick, until } from "./helpers";

describe("Pipeline stages and their order", () => {

  test("Employer row city: a section-policy abstain is final for the question pass and the profile pass still has its own rule", async () => {
    // The release of a section-policy claim inside a repeated entity row is a later piece (3.8); here the claim stays.
    mount(`<fieldset><legend>Employment History</legend><label for="c">City</label><input id="c"></fieldset>${PAD}`);
    const engine = makeEngine(fakeHost({ city: { outcome: "abstained", reason: "section-policy" } }));
    const s = summaryOf(await runFill(engine));
    expect(byId("c").value).toBe("");
    expect(rowFor(s, "city").outcome).toBe("abstained");
  });

  test("Terminal abstain is final", async () => {
    mount(`<label for="q">Preferred contact email</label><input id="q" autocomplete="email" required>${PAD}`);
    const engine = makeEngine(fakeHost({ "preferred contact email": { outcome: "abstained", reason: "ask-me", terminal: true } }));
    const s = summaryOf(await runFill(engine));
    expect(byId("q").value).toBe("");
    expect(s.rows[0]).toMatchObject({ reason: "terminal-abstain", matcherReason: "ask-me" });
    // The matcher's ask-me still counts as a needs-user leftover.
    expect(s.counts.requiredUnfilled).toBe(1);
  });

  test("Postal text abstain: an overwritable abstain on a text control goes back to the profile pass", async () => {
    mount(`<label for="z">Home ZIP</label><input id="z">${PAD}`);
    const engine = makeEngine(fakeHost({ "home zip": { outcome: "abstained", reason: "no-entry", clientMayOverwrite: true } }));
    const s = summaryOf(await runFill(engine));
    // "Home ZIP" is matcher-owned (not a whole identity label), so only the release rule can hand it to the profile pass;
    // its label is not a profile label either, so it stays empty and says why.
    expect(byId("z").value).toBe("");
    expect(rowFor(s, "home zip").reason).toBe("no-match");
  });

  test("An autocomplete postal-code box is profile-owned: the matcher is never asked and the profile pass fills it", async () => {
    mount(`<label for="z">Postal code</label><input id="z" autocomplete="postal-code">${PAD}`);
    const host = fakeHost();
    const s = summaryOf(await runFill(makeEngine(host)));
    expect(byId("z").value).toBe("27701");
    expect(host.matchRequests).toHaveLength(0);
    expect(rowFor(s, "postal").lane).toBe("profile");
  });

  test("Inserted block: no later write starts before the DOM is quiet", async () => {
    mount(`<label for="a">Do you need sponsorship?</label>
      <select id="a"><option value="">Select</option><option>Yes</option><option>No</option></select>
      <div id="slot"></div>
      <label for="z">Anything else we should know?</label><input id="z">`);
    const a = byId("a") as unknown as HTMLSelectElement;
    a.addEventListener("change", () => {
      queueMicrotask(() => {
        byId("slot").innerHTML = `<label for="mid">Additional detail</label><input id="mid">`;
      });
    });
    const order: string[] = [];
    let blockPresentAtZ = false;
    document.addEventListener("change", (e) => {
      const id = (e.target as HTMLElement).id;
      order.push(id);
      if (id === "z") blockPresentAtZ = !!byId("mid");
    });
    const engine = makeEngine(
      fakeHost({ "do you need sponsorship?": { outcome: "filled", value: "No" }, "anything else we should know?": { outcome: "filled", value: "Nothing" } }),
      { quietMs: 40, quietCapMs: 400 },
    );
    await runFill(engine);
    expect(byId("z").value).toBe("Nothing");
    expect(byId("mid").value).toBe("");
    expect(order.indexOf("a")).toBeLessThan(order.indexOf("z"));
    expect(blockPresentAtZ).toBe(true);
  });

  test("Wedged pass: the pass stops writing, the run is not stopped and the profile pass runs", async () => {
    mount(`<label for="fn">First name</label><input id="fn"><label for="q">Tell us about a hard problem you solved</label><input id="q">`);
    const host = fakeHost();
    host.match = () => new Promise(() => {});
    const engine = makeEngine(host);
    const status = await runFill(engine, { prepassDeadlineMs: 30 });
    expect(status.state).toBe("done");
    expect(byId("fn").value).toBe("Ada");
    expect(byId("q").value).toBe("");
    expect(summaryOf(status).matchUnavailable).toBeUndefined();
  });

  test("Unclaimed prefilled control", async () => {
    mount(`<label for="x">Reference code</label><input id="x" value="AB-1"><label for="fn">First name</label><input id="fn">`);
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    const row = rowFor(s, "reference code");
    expect(row.outcome).toBe("prefilled");
    expect(row.reason).toBe("no-lane-claimed");
  });

  test("Cancellation: stop during the apply loop ends the loop and later stages do not run", async () => {
    mount(`<label for="fn">First name</label><input id="fn"><label for="ln">Last name</label><input id="ln"><label for="em">Email</label><input id="em">`);
    const engine = makeEngine(fakeHost(), { settleMs: 30 });
    const r = engine.start({ applicant: ADA });
    await until(() => engine.log().some((l) => l.includes("apply-loop start")));
    expect(engine.stop(r.runId as string, "cancelled")).toBe(true);
    await tick(80);
    expect(engine.log().some((l) => l.includes("blur start"))).toBe(false);
    expect(engine.status().state).toBe("stopped");
  });
});

describe("Question pass", () => {
  test("Review flag: a filled-review result is written and flagged", async () => {
    mount(`<label for="q">How did you hear about us?</label><select id="q"><option value="">Select</option><option>Referral</option><option>Job board</option></select>${PAD}`);
    const s = summaryOf(await runFill(makeEngine(fakeHost({ "how did you hear about us?": { outcome: "filled-review", value: "Job board" } }))));
    const row = rowFor(s, "hear about");
    expect(byId("q").value).toBe("Job board");
    expect(row.outcome).toBe("filled");
    expect(row.review).toBe(true);
  });

  test("Revealed controls: the next pass sends the new questions and no earlier question is resent", async () => {
    mount(`<label for="c">Do you live in the US?</label><select id="c"><option value="">Select</option><option>Yes</option><option>No</option></select><div id="slot"></div>${PAD}`);
    (byId("c") as unknown as HTMLSelectElement).addEventListener("change", () => {
      byId("slot").innerHTML = `<label for="st">Which US state?</label><input id="st">`;
    });
    const host = fakeHost({ "do you live in the us?": { outcome: "filled", value: "Yes" }, "which us state?": { outcome: "filled", value: "North Carolina" } });
    await runFill(makeEngine(host));
    expect(byId("st").value).toBe("North Carolina");
    const asked = host.matchRequests.flatMap((r) => r.questions.map((q) => q.text));
    expect(asked).toEqual(["Do you live in the US?", "Which US state?"]);
  });

  test("A custom listbox that will not open is reported listbox-not-opened and never sent with empty options", async () => {
    mount(`<label for="lb">Degree</label><input id="lb" role="combobox" aria-haspopup="listbox">${PAD}`);
    const host = fakeHost();
    const s = summaryOf(await runFill(makeEngine(host)));
    expect(host.matchRequests.flatMap((r) => r.questions).filter((q) => q.kind === "custom-listbox")).toHaveLength(0);
    expect(rowFor(s, "degree").reason).toBe("listbox-not-opened");
  });
});
