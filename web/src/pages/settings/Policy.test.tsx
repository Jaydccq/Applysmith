import { act, type ReactNode } from "react";
import { describe, expect, it } from "vitest";
import { BoardContext } from "../../hooks";
import { fail, fakeApi, manualBoard, ok } from "../../testing/api";
import { button, click, mount, qa, settle, submit, text, type, waitFor, q } from "../../testing/dom";
import type { ConfigDocument, RiskView } from "../../wire";
import { DocumentsSection, GateSection, PolicySection, ScanSection } from "./Policy";

const cooldowns = { captcha: 168, rate_limited: 168, account_locked: 720, verification_required: 72, login_redirect: 168, silent_degradation: 24 };

function policy(revision: string, captcha = 168): ConfigDocument {
  return { file: "policy", exists: true, template: false, content: "", revision, issues: [], values: { cooldowns: { ...cooldowns, captcha } } };
}

const noRisk: RiskView = { days: 30, baseline: [], signals: [], cooldowns: [] };

function server(doc = policy("p1"), risk: RiskView = noRisk) {
  return fakeApi({
    "GET /api/policy/risk": () => ok(risk),
    "GET /api/config/policy": () => ok(doc),
    "PATCH /api/config/policy": () => ok({ revision: "p2", changed: true }),
  });
}

// changedElsewhere mounts the section, lets the test edit, then serves doc as
// the policy another writer left and announces it.
async function changedElsewhere(api: ReturnType<typeof server>, doc: ConfigDocument, edit: () => Promise<void>) {
  const board = manualBoard();
  await mount(
    <BoardContext.Provider value={board}>
      <PolicySection />
    </BoardContext.Provider>,
  );
  await waitFor(() => field("A captcha stopped a run"));
  await act(async () => board.set({ configuration: 1 }));
  await settle();
  await edit();
  api.on("GET /api/config/policy", () => ok(doc));
  await act(async () => board.set({ configuration: 2 }));
  await waitFor(() => api.callsTo("GET /api/config/policy").length === 2);
  await settle();
}

const field = (label: string) => qa<HTMLInputElement>("input").find((i) => i.labels?.[0]?.textContent?.startsWith(label))!;
const box = (label: string) => qa<HTMLInputElement>("input[type=checkbox]").find((i) => i.labels?.[0]?.textContent?.trim() === label)!;

describe("Settings: applications policy", () => {
  it("saves only what changed, quoting the revision it loaded", async () => {
    const api = server();
    // The file read again after the save is the next revision, as the server answers.
    let written = false;
    api.on("PATCH /api/config/policy", () => ((written = true), ok({ revision: "p2", changed: true })));
    api.on("GET /api/config/policy", () => ok(written ? policy("p2", 24) : policy("p1")));
    await mount(<PolicySection />);
    await type(await waitFor(() => field("A captcha stopped a run")), "24");
    await submit(q("form.form-grid"));
    await waitFor(() => api.callsTo("PATCH /api/config/policy").length === 1);
    const body = api.callsTo("PATCH /api/config/policy")[0].body as { expectedRevision: string; ops: { path: string; value: string }[] };
    expect(body.expectedRevision).toBe("p1");
    expect(body.ops).toEqual([{ op: "set", path: "cooldowns.captcha", value: "24" }]);
    await waitFor(() => text().includes("Saved at"));
    await waitFor(() => button("Save policy")!.disabled); // nothing left to save, so the button cannot hold focus
    expect(document.activeElement).toBe(q(".save-state"));
  });

  it("offers the edit again on top of the current file after a revision conflict", async () => {
    const api = server();
    api.on("PATCH /api/config/policy", (c) => ((c.body as { expectedRevision: string }).expectedRevision === "p1" ? fail("revision_conflict", "policy changed on disk") : ok({ revision: "p3", changed: true })));
    await mount(<PolicySection />);
    await type(await waitFor(() => field("A captcha stopped a run")), "4");
    await submit(q("form.form-grid"));
    await waitFor(() => text().includes("The policy changed since this page loaded"));
    api.on("GET /api/config/policy", () => ok(policy("p2")));
    await click(button("Apply my edit again"));
    await waitFor(() => api.callsTo("PATCH /api/config/policy").length === 2);
    expect((api.callsTo("PATCH /api/config/policy")[1].body as { expectedRevision: string }).expectedRevision).toBe("p2");
  });

  it("keeps an edit over a change made elsewhere to the same values, refuses its save as a conflict, and applies only the edit again", async () => {
    const api = server();
    api.on("PATCH /api/config/policy", (c) => ((c.body as { expectedRevision: string }).expectedRevision === "p1" ? fail("revision_conflict", "policy changed on disk") : ok({ revision: "p3", changed: true })));
    await changedElsewhere(api, policy("p2", 24), () => type(field("The site rate-limited the run"), "5"));
    expect(field("The site rate-limited the run").value).toBe("5");
    expect(field("A captcha stopped a run").value).toBe("168");
    await submit(q("form.form-grid"));
    await waitFor(() => text().includes("The policy changed since this page loaded"));
    expect((api.callsTo("PATCH /api/config/policy")[0].body as { expectedRevision: string }).expectedRevision).toBe("p1");
    await click(button("Apply my edit again"));
    await waitFor(() => api.callsTo("PATCH /api/config/policy").length === 2);
    expect(api.callsTo("PATCH /api/config/policy")[1].body).toEqual({ expectedRevision: "p2", ops: [{ op: "set", path: "cooldowns.rate_limited", value: "5" }] });
  });
});

describe("Settings: document defaults", () => {
  const prefs = { language: "en", paper: "a4", resumePages: 2, coverWhenAsked: "always", revision: 3 };

  it("saves the paper, page limit and language against the revision it was read at, and reloads on a conflict", async () => {
    let conflict = false;
    const api = fakeApi({
      "GET /api/documents/preferences": () => ok(prefs),
      "PATCH /api/documents/preferences": () => (conflict ? fail("conflict", "the preferences are at revision 4") : ok({ ...prefs, paper: "letter", resumePages: 1, revision: 4 })),
    });
    await mount(<DocumentsSection />);
    await waitFor(() => qa("select").length === 4);
    const pick = (label: string) => qa<HTMLSelectElement>("select").find((s) => s.labels?.[0]?.textContent === label)!;
    expect(button("Save document defaults")!.disabled).toBe(true);
    await type(pick("Paper size"), "letter");
    await type(pick("Résumé length"), "1");
    await submit(q("form")!);
    await waitFor(() => api.callsTo("PATCH /api/documents/preferences").length === 1);
    expect(api.callsTo("PATCH /api/documents/preferences")[0].body).toEqual({ revision: 3, paper: "letter", resumePages: 1, language: "en", coverWhenAsked: "always" });

    conflict = true;
    await type(pick("Language"), "de");
    await submit(q("form")!);
    await waitFor(() => text().includes("The defaults changed since this page loaded"));
    expect(api.callsTo("PATCH /api/documents/preferences")).toHaveLength(2);
  });
});

describe("Settings: scan defaults and the eligibility gate", () => {
  const pick = (label: string) => qa<HTMLSelectElement>("select").find((s) => s.labels?.[0]?.textContent?.startsWith(label))!;
  const saveWith = async (values: ConfigDocument["values"], ui: ReactNode, edit: () => Promise<void>) => {
    const api = fakeApi({
      "GET /api/config/profile": () => ok({ file: "profile", exists: true, template: false, content: "", revision: "r1", issues: [], values }),
      "PATCH /api/config/profile": () => ok({ revision: "r2", changed: true }),
      "GET /api/scan-sources": () => ok({ sources: ["linkedin", "linkedin-feed"].map((name) => ({ name, title: name, kind: "browser", readiness: "ready", running: false })) }),
    });
    await mount(ui);
    await waitFor(() => qa("select").length > 0);
    await edit();
    await submit(q("form.form-grid"));
    await waitFor(() => api.callsTo("PATCH /api/config/profile").length === 1);
    return (api.callsTo("PATCH /api/config/profile")[0].body as { ops: { op: string; path: string; value?: string }[] }).ops;
  };

  it("shows the feed's evaluate_limit, which wins over evaluate_top_n, and a save replaces it; a blank window leaves the key out", async () => {
    const ops = await saveWith({ scan: { posted_within: "24h", defaults: { "linkedin-feed": { evaluate_limit: 7, blocked_posters: ["Acme"] } } } }, <ScanSection />, async () => {
      await type(pick("Source"), "linkedin-feed");
      expect(field("Send the top N").value).toBe("7");
      await type(field("Send the top N"), "12");
      await type(pick("Postings no older than"), "");
    });
    expect(ops.map((o) => [o.op, o.path, o.value])).toEqual([["unset", "scan.posted_within", undefined], ["set", "scan.defaults.linkedin-feed", 'blocked_posters:\n  - "Acme"\nevaluate_top_n: 12']]);
  });

  it("removes a gate switch set back to its default instead of writing yes or no, and gives a lowered score its points", async () => {
    const ops = await saveWith({ eligibility_gate: { allow_remote: false, employer_tiers: { staffing: { policy: "block" } } } }, <GateSection />, async () => {
      expect(pick("Keep remote postings").value).toBe("false");
      await type(pick("Keep remote postings"), "");
      await type(pick("Staffing agencies"), "penalize");
      await type(field("Score taken off staffing"), "1.5");
    });
    expect(ops.map((o) => [o.op, o.path, o.value])).toEqual([["unset", "eligibility_gate.allow_remote", undefined], ["set", "eligibility_gate.employer_tiers", 'staffing:\n  policy: "penalize"\n  delta: 1.5']]);
  });

  it("saves scan defaults after the gate on the same page: the user's own save is not someone else's change", async () => {
    // The server refuses a save quoting any revision but the file's current one.
    let rev = 1;
    const api = fakeApi({
      "GET /api/config/profile": () => ok({ file: "profile", exists: true, template: false, content: "", revision: `r${rev}`, issues: [], values: {} }),
      "PATCH /api/config/profile": (c) => ((c.body as { expectedRevision: string }).expectedRevision === `r${rev}` ? ok({ revision: `r${++rev}`, changed: true }) : fail("revision_conflict", "profile.yml changed on disk")),
      "GET /api/scan-sources": () => ok({ sources: [{ name: "linkedin", title: "linkedin", kind: "browser", readiness: "ready", running: false }] }),
    });
    await mount(<><GateSection /><ScanSection /></>);
    await waitFor(() => qa("form.form-grid").length === 2 && qa("select").length > 0);
    await click(box("Intern"));
    await submit(qa("form.form-grid")[0]);
    await waitFor(() => api.callsTo("GET /api/config/profile").length === 2 && /Saved at /.test(qa(".save-bar")[0].textContent ?? ""));
    await type(field("Send the top N"), "12");
    await submit(qa("form.form-grid")[1]);
    await waitFor(() => api.callsTo("PATCH /api/config/profile").length === 2 && /Saved at /.test(qa(".save-bar")[1].textContent ?? ""));
    expect(text()).not.toContain("changed this since you opened it");
  });
});
