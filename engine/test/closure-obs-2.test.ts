// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Uncertainty events and the user's own edits (handoff recorder); Sensitive controls and redacted fragments;
// Pipeline stages and their order; Page API.
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { beforeAll, beforeEach, describe, expect, test } from "vitest";
import { redactedFragment } from "../src/fragment";
import type { LearningEvent } from "../src/protocol";
import { PAD, byId, fakeHost, makeEngine, mount, runFill, summaryOf, tick, until, trusted } from "./helpers";

describe("Handoff recorder of the user's own edits", () => {
  // The recorder is the function the apply chain arms a kept tab with, called with the session's key; it runs here
  // against a synthetic page. Each buffered entry is its sealed text.
  const source = readFileSync(resolve(import.meta.dirname, "../../internal/apply/recorder.js"), "utf8");
  const KEY = "__ajhr";
  const entries = (): Record<string, unknown>[] =>
    ((JSON.parse(sessionStorage.getItem(KEY) ?? "{}") as { e?: { d: string }[] }).e ?? []).map((x) => JSON.parse(x.d) as Record<string, unknown>);

  beforeAll(() => {
    expect((0, eval)(source)("00".repeat(32))).toBe("armed");
  });
  beforeEach(() => {
    sessionStorage.setItem(KEY, JSON.stringify({ epoch: "t1", n: 0, e: [] }));
  });

  const change = (el: Element, isTrusted = true) => el.dispatchEvent(isTrusted ? trusted(new Event("change", { bubbles: true })) : new Event("change", { bubbles: true }));

  test("a radio change is recorded under its fieldset legend with the option label as value", () => {
    mount(`<fieldset><legend>Are you authorised to work here? *</legend>
      <label><input type="radio" name="auth1" value="1" id="ay"> Yes</label><label><input type="radio" name="auth1" value="2" id="an"> No</label></fieldset>
      <label for="team">Team</label><select id="team"><option value="">Select</option><option value="t9">Platform team</option></select>
      <label for="note">Anything else</label><input id="note" value="start">
      <label for="pw">Account password</label><input id="pw" type="password"><label for="up">Upload</label><input id="up" type="file"><input id="hid" type="hidden">`);
    const no = byId<HTMLInputElement>("an");
    no.checked = true;
    change(no);
    // The chosen option's text, not the page's code for it, is what the answer is.
    expect(entries()).toHaveLength(1);
    expect(entries()[0]).toMatchObject({ question: "Are you authorised to work here?", kind: "radio-group", optionLabel: "No", skipped: false, url: "http://localhost:3000/" });

    const team = byId<HTMLSelectElement>("team");
    team.value = "t9";
    change(team);
    expect(entries()[1]).toMatchObject({ question: "Team", kind: "select", value: "Platform team", skipped: false });

    // A script's change, a cleared box, a password, a file and a hidden field record nothing.
    const note = byId<HTMLInputElement>("note");
    note.value = "";
    change(note);
    byId<HTMLInputElement>("pw").value = "not-a-real-password";
    change(byId("pw"));
    change(byId("up"));
    change(byId("hid"));
    const before = entries().length;
    team.value = "";
    change(team, false);
    expect(entries()).toHaveLength(before);
    expect(JSON.stringify(entries())).not.toContain("not-a-real-password");
  });

  test("a Yes click then a No click on a pressed-state group leaves one entry with No", async () => {
    mount(`<div role="group" aria-label="Do you need sponsorship? *"><button type="button" id="yes" aria-pressed="false">Yes</button><button type="button" id="no" aria-pressed="false">No</button></div>`);
    const press = (id: string) => {
      const group = [byId("yes"), byId("no")];
      const el = byId(id);
      // The page flips the state after the event, as a framework does.
      el.addEventListener("click", () => setTimeout(() => group.forEach((b) => b.setAttribute("aria-pressed", String(b === el))), 5), { once: true });
      el.dispatchEvent(trusted(new MouseEvent("click", { bubbles: true })));
    };
    press("yes");
    await tick(200);
    expect(entries().map((e) => e.optionLabel)).toEqual(["Yes"]);
    press("no");
    await tick(200);
    expect(entries()).toHaveLength(1);
    expect(entries()[0]).toMatchObject({ question: "Do you need sponsorship?", kind: "radio-group", value: "No", optionLabel: "No", prior: "Yes", skipped: false });
  });

  test("an own-email edit is noted as valueSkipped and carries no value", () => {
    mount(`<label for="em">Email address</label><input id="em" type="email"><label for="fn">First name</label><input id="fn"><label for="ph">Mobile</label><input id="ph" type="tel">`);
    for (const [id, text] of [["em", "alex@example.com"], ["fn", "Alex"], ["ph", "+1 555 0100"]] as const) {
      byId<HTMLInputElement>(id).value = text;
      change(byId(id));
    }
    expect(entries().map((e) => [e.question, e.skipped, e.value, e.optionLabel, e.prior])).toEqual([
      ["Email address", true, "", "", ""],
      ["First name", true, "", "", ""],
      ["Mobile", true, "", "", ""],
    ]);
    expect(JSON.stringify(entries())).not.toMatch(/alex@example|Alex|555/);
  });

  test("a hand-typed gender select is noted as skipped with no value by the handoff recorder", () => {
    mount(`<label for="g">Gender</label><select id="g"><option value="">Select</option><option>Female</option><option>Male</option></select>`);
    byId<HTMLSelectElement>("g").value = "Female";
    change(byId("g"));
    expect(entries()).toHaveLength(1);
    expect(entries()[0]).toMatchObject({ question: "Gender", skipped: true, value: "", optionLabel: "" });
    expect(JSON.stringify(entries())).not.toContain("Female");
  });

  test("an entry from a same-origin iframe carries the top URL and a path-only frame URL", async () => {
    // The frame's page is served by an interceptor: the test has no server.
    const settings = (window as unknown as { happyDOM: { settings: { fetch: { interceptor: unknown } } } }).happyDOM.settings;
    settings.fetch.interceptor = {
      beforeAsyncRequest: async ({ window: w }: { window: Window & typeof globalThis }) =>
        new w.Response(`<html><body><label for="city">City of residence</label><input id="city"><label for="pw">Account password</label><input id="pw" type="password">
          <label for="ssn">Social security number</label><input id="ssn"></body></html>`, { headers: { "content-type": "text/html" } }),
    };
    try {
      mount(`<label for="top">Team</label><input id="top"><iframe id="apply" src="http://localhost:3000/apply/frame?token=abc#step2"></iframe>`);
      const frame = byId<HTMLIFrameElement>("apply");
      await until(() => frame.contentDocument?.getElementById("city") != null);
      await tick(20);
      const doc = frame.contentDocument as Document;
      const city = doc.getElementById("city") as HTMLInputElement;
      city.value = "Lyon";
      change(city);
      (doc.getElementById("pw") as HTMLInputElement).value = "not-a-real-password";
      change(doc.getElementById("pw") as Element);
      (doc.getElementById("ssn") as HTMLInputElement).value = "078-05-1120";
      change(doc.getElementById("ssn") as Element);
      byId<HTMLInputElement>("top").value = "Platform";
      change(byId("top"));
      // One entry per commit: the frame's change never reaches the top document's listener as well.
      expect(entries().map((e) => [e.question, e.value, e.skipped])).toEqual([
        ["City of residence", "Lyon", false],
        ["Account password", "", true],
        ["Social security number", "", true],
        ["Team", "Platform", false],
      ]);
      expect(entries()[0]).toMatchObject({ url: "http://localhost:3000/", frameUrl: "http://localhost:3000/apply/frame" });
      expect(entries()[3]?.frameUrl).toBeUndefined();
      expect(JSON.stringify(entries())).not.toMatch(/token|step2|not-a-real-password|078-05/);
    } finally {
      settings.fetch.interceptor = null;
    }
  });
});

describe("Redacted fragments", () => {
  test("a fragment with a typed value, a script and an email-like label drops the value and script and masks the email", () => {
    mount(`<fieldset id="blk"><legend>Reach me at alex@example.com or +1 415 555 0100 <b>now</b></legend>
      <input id="who" type="text" value="Typed Name" onclick="steal()" data-secret="s1" aria-label="Contact alex@example.com">
      <script>steal()</script><style>.a{}</style><svg><circle></circle></svg><template><p>tpl</p></template><noscript>ns</noscript>
      <input type="hidden" name="tok" value="hidden-token"><input type="password" value="pw-secret"><div contenteditable="true">draft prose</div>
      <textarea>typed prose</textarea><select><option value="o1">One</option></select>
      <input type="radio" name="r" value="r1"><input type="checkbox" value="c1">
      <a href="https://example.org/next?token=abc">link text &lt;b&gt;</a></fieldset>`);
    const html = redactedFragment(byId("blk")).html;
    expect(html).not.toMatch(/Typed Name|steal|onclick|data-secret|hidden-token|pw-secret|draft prose|typed prose|href|tpl|circle|ns<|abc/);
    expect(html).not.toContain("alex@example.com");
    expect(html).not.toContain("415 555 0100");
    expect(html).toContain("[email]");
    expect(html).toContain("[phone]");
    expect(html).toContain("<textarea></textarea>");
    for (const kept of ['value="o1"', 'value="r1"', 'value="c1"']) expect(html).toContain(kept);
    expect(html).toContain("link text &lt;b&gt;");
    expect(html).not.toMatch(/<input type="text"[^>]*value=/);
  });

});

describe("Telemetry", () => {
  test("one fill of a fixture yields events, summary counts and a bounded log and nothing else", async () => {
    mount(`<label for="c">Favourite colour</label><input id="c"><label for="d">Describe a hard bug</label><input id="d"><input id="blind" required>${PAD}`);
    const engine = makeEngine(fakeHost({ "favourite colour": { outcome: "filled", value: "Quokka-7731" }, "describe a hard bug": { outcome: "abstained", reason: "low-confidence", terminal: true } }));
    const s = summaryOf(await runFill(engine));
    const allowed: (keyof LearningEvent)[] = ["eventId", "kind", "host", "site", "url", "selector", "question", "controlKind", "inputType", "autocomplete", "options", "time", "reason", "labelSource", "required", "current", "before", "after", "optionLabel", "priorValue", "fillOutcome", "trustedInput", "edited", "incomplete", "fragment"];
    expect(Object.keys(s).every((k) => ["host", "url", "site", "rows", "counts", "drafts", "events", "partial", "restored", "matchUnavailable", "blockedByDialog", "notAnApplication"].includes(k))).toBe(true);
    expect(s.counts).toMatchObject({ filled: 1, abstained: 1 });
    expect(s.events?.length).toBeGreaterThan(0);
    for (const ev of s.events ?? []) expect(Object.keys(ev).every((k) => allowed.includes(k as keyof LearningEvent))).toBe(true);
    // The value the fill wrote belongs to its row; no event and no log line repeats it.
    expect(JSON.stringify(s.events)).not.toContain("Quokka-7731");
    expect(engine.log().join("\n")).not.toContain("Quokka-7731");
    expect(engine.log().length).toBeLessThanOrEqual(2000);
    expect(engine.submitObservations()).toEqual([]);
    expect(engine.drainEvents().every((e) => e.kind !== "uncertainty")).toBe(true);
  });
});
