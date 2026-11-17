import { beforeEach, describe, expect, it, vi } from "vitest";
import { mountResume } from "../src/panel/resume";

const send = vi.fn();
vi.mock("../src/shared/rpc", () => ({
  send: (r: unknown) => send(r),
  failureOf: (e: { failure?: unknown }) => e.failure ?? { code: "internal", message: "Something went wrong." },
}));

const variants = [
  { id: "frontend", label: "Frontend", tags: ["react"], default: true },
  { id: "backend", label: "Backend", tags: ["go", "backend"], default: false },
];
const ranking = { chosen: "backend", ranking: [{ id: "backend", score: 2, reason: "backend" }, { id: "frontend", score: 0, reason: "default" }] };

let el: HTMLElement;
let state: { resumes: unknown[]; pinned?: string };
const click = (label: string) => [...el.querySelectorAll("button")].find((b) => b.textContent === label)!.click();
const chosen = () => el.querySelector("[data-chosen] strong")?.textContent;
const kinds = () => send.mock.calls.map((c) => (c[0] as { kind: string }).kind);

async function open() {
  const tab = mountResume(el);
  tab.setConnection("connected");
  await vi.waitFor(() => expect(el.querySelector("ul, [role=alert], [role=status]")).not.toBeNull());
  await vi.waitFor(() => expect(el.textContent).not.toMatch(/Loading/));
  return tab;
}

beforeEach(() => {
  send.mockReset();
  (window as unknown as { happyDOM: { setURL(u: string): void } }).happyDOM.setURL("https://example.com/careers/backend");
  document.title = "Backend Engineer - Example Corp";
  document.body.innerHTML = `<div id="t"></div><main>Backend Engineer. ${"Build services in Go. ".repeat(30)}</main>`;
  el = document.getElementById("t")!;
  state = { resumes: variants };
  send.mockImplementation(async (req: { kind: string; id?: string | null }) => {
    if (req.kind === "resume.list") return structuredClone(state);
    if (req.kind === "resume.rank") return ranking;
    if (req.kind === "resume.pin") {
      state.pinned = req.id ?? undefined;
      return undefined;
    }
    throw new Error(`unexpected ${req.kind}`);
  });
});

describe("Résumé tab", () => {
  it("a manual pin overrides the choice until Use auto-match", async () => {
    await open();
    click("Use this one");
    await vi.waitFor(() => expect(el.textContent).toContain("Use auto-match"));
    expect(send.mock.calls.find((c) => (c[0] as { kind: string }).kind === "resume.pin")![0]).toMatchObject({ id: expect.any(String) });
    const pinned = state.pinned!;
    expect(chosen()).toBe(variants.find((v) => v.id === pinned)!.label);
    expect(el.querySelector("[data-chosen]")!.textContent).toContain("pinned by you");
    click("Use auto-match");
    await vi.waitFor(() => expect(chosen()).toBe("Backend"));
  });
});

describe("Documents section", () => {
  const withDocs = (view: Record<string, unknown>) =>
    send.mockImplementation(async (req: { kind: string }) => {
      if (req.kind === "resume.list") return structuredClone(state);
      if (req.kind === "resume.rank") return ranking;
      if (req.kind === "docs.get") return view;
      if (req.kind === "docs.start" || req.kind === "docs.dismiss") return { runId: "run_x" };
      throw new Error(`unexpected ${req.kind}`);
    });
  const section = () => el.querySelector("section.docs");
  const form = `<form><label for="cv">Upload résumé</label><input id="cv" type="file" accept=".pdf,.docx"><label for="em">Email</label><input id="em" type="email"></form>`;

  it("hop page without upload and identity hides documents", async () => {
    withDocs({ show: true, reportNumber: 42, candidacyId: 7, inherited: true });
    // A vendor's contact form: an identity field, no résumé upload.
    document.body.insertAdjacentHTML("beforeend", `<form><label for="em">Email</label><input id="em" type="email"><label for="msg">Message</label><textarea id="msg"></textarea></form>`);
    await open();
    expect(kinds()).toContain("docs.get");
    expect(section()).toBeNull();
  });

  it("a hop page with a résumé upload and an identity field shows the documents and offers to dismiss them", async () => {
    withDocs({ show: true, reportNumber: 42, candidacyId: 7, inherited: true });
    document.body.insertAdjacentHTML("beforeend", form);
    await open();
    await vi.waitFor(() => expect(section()).not.toBeNull());
    expect(section()!.textContent).toContain("#042");
    click("Write tailored résumé");
    await vi.waitFor(() => expect(section()!.textContent).toContain("being written"));
    expect(send.mock.calls.find((c) => (c[0] as { kind: string }).kind === "docs.start")![0]).toMatchObject({ candidacyId: 7, document: "resume" });
    click("Not this job? Dismiss");
    await vi.waitFor(() => expect(section()).toBeNull());
    expect(kinds()).toContain("docs.dismiss");
  });

  it("holds both buttons while the document run works, as the application runs one at a time per job, and frees them when it ends", async () => {
    let status = "running";
    send.mockImplementation(async (req: { kind: string }) => {
      if (req.kind === "resume.list") return structuredClone(state);
      if (req.kind === "resume.rank") return ranking;
      if (req.kind === "docs.get") return { show: true, reportNumber: 42, candidacyId: 7, inherited: false };
      if (req.kind === "docs.start") return { runId: "run_d" };
      if (req.kind === "docs.run") return { status };
      throw new Error(`unexpected ${req.kind}`);
    });
    await open();
    await vi.waitFor(() => expect(section()).not.toBeNull());
    vi.useFakeTimers();
    try {
      const btn = (label: string) => [...section()!.querySelectorAll("button")].find((b) => b.textContent === label)!;
      btn("Write tailored résumé").click();
      btn("Write cover letter").click();
      await vi.advanceTimersByTimeAsync(0);
      expect(section()!.textContent).toContain("being written");
      await vi.advanceTimersByTimeAsync(12_000);
      btn("Write cover letter").click();
      expect(kinds().filter((k) => k === "docs.start")).toHaveLength(1);
      expect(section()!.textContent).toContain("being written");
      status = "succeeded";
      await vi.advanceTimersByTimeAsync(6_000);
      btn("Write cover letter").click();
      await vi.advanceTimersByTimeAsync(0);
      expect(kinds().filter((k) => k === "docs.start")).toHaveLength(2);
    } finally {
      vi.useRealTimers();
    }
  });
});
