import { act } from "react";
import { describe, expect, it } from "vitest";
import { fail, ok } from "../../testing/api";
import { button, byText, click, mount, q, qa, text, type, waitFor } from "../../testing/dom";
import { profileDoc, setupApi, setupState, setupStep } from "../../testing/fixtures_setup";
import type { SetupReason } from "../../wire";
import Setup from "../Setup";

async function openStep(id: string, patch: Parameters<typeof setupState>[0] = {}, routes: Parameters<typeof setupApi>[1] = {}) {
  const api = setupApi(setupState(patch), routes);
  window.history.replaceState(null, "", `/setup?step=${id}`);
  await mount(<Setup />);
  await waitFor(() => q(`article[data-step="${id}"]`));
  return api;
}

const blocking = (group: string, n: number, from = 0): SetupReason[] => Array.from({ length: n }, (_, i) => ({ field: group, text: `${group} question ${from + i + 1} (${group.replace(/\W+/g, "-")}.q${from + i + 1})` }));

describe("Setup: answer bank", () => {
  const reasons = [...blocking("work authorization", 3), ...blocking("compensation", 2), ...blocking("address", 2), { field: "self-identification", text: "the self-identification defaults need to be confirmed" }];

  it("sends the typed answers and the ask-me marker, keyed by entry id", async () => {
    const api = await openStep("answer-bank", { "answer-bank": { status: "partial", reasons } });
    const row = (id: string) => q(`[data-entry="${id}"]`)!;
    await type(await waitFor(() => row("compensation.q1").querySelector<HTMLInputElement>("input[type=text]")), " 150000 ");
    await click(row("address.q2").querySelector("input[type=checkbox]"));
    expect(row("address.q2").querySelector<HTMLInputElement>("input[type=text]")!.disabled).toBe(true);
    await click(button("Save answers"));
    await waitFor(() => api.callsTo("POST /api/setup/steps/answer-bank/apply").length === 1);
    expect(api.callsTo("POST /api/setup/steps/answer-bank/apply")[0].body).toEqual({ input: { answers: { "compensation.q1": "150000", "address.q2": "@ask-me" } } });
  });

  it("confirms the reviewed defaults with one button and links to the full bank", async () => {
    const api = await openStep("answer-bank", { "answer-bank": { status: "partial", reasons } });
    await click(await waitFor(() => button("Confirm the reviewed defaults")));
    await waitFor(() => api.callsTo("POST /api/setup/steps/answer-bank/apply").length === 1);
    expect(api.callsTo("POST /api/setup/steps/answer-bank/apply")[0].body).toEqual({ input: { confirm_review: true } });
    expect(byText("a", "Open the full answer bank")?.getAttribute("href")).toBe("/answer-bank");
  });

  it("keeps a typed answer when the rest are left to the person", async () => {
    const api = await openStep("answer-bank", { "answer-bank": { status: "partial", reasons: blocking("address", 3) } });
    await type(await waitFor(() => q('[data-entry="address.q2"] input[type=text]')), "Apt 4");
    await click(button("Save these and leave the rest to me"));
    await waitFor(() => api.callsTo("POST /api/setup/steps/answer-bank/apply").length === 1);
    expect(api.callsTo("POST /api/setup/steps/answer-bank/apply")[0].body).toEqual({ input: { answers: { "address.q1": "@ask-me", "address.q2": "Apt 4", "address.q3": "@ask-me" } } });
  });
});

describe("Setup: résumés", () => {
  it("uploads a PDF and registers it on the roster with exactly one default", async () => {
    const api = await openStep(
      "resumes",
      { resumes: { status: "missing" } },
      {
        "GET /api/config/profile": () => ok(profileDoc({ resumes: [] })),
        "POST /api/resumes": () => ok({ file: "resumes/riley-sample.pdf" }),
      },
    );
    await waitFor(() => text().includes("Your résumés"));
    await waitFor(() => !button("Upload a résumé PDF")!.disabled);
    const picker = q<HTMLInputElement>("input[type=file]")!;
    const file = new File([new Uint8Array(2048)], "Riley Sample.pdf", { type: "application/pdf" });
    Object.defineProperty(picker, "files", { value: [file] });
    await act(async () => void picker.dispatchEvent(new Event("change", { bubbles: true })));
    await waitFor(() => api.callsTo("POST /api/setup/steps/resumes/apply").length === 1);
    expect(api.callsTo("POST /api/resumes")[0].query.get("name")).toBe("riley-sample");
    expect(api.callsTo("POST /api/setup/steps/resumes/apply")[0].body).toEqual({ input: { roster: [{ id: "riley-sample", label: "Riley Sample", file: "resumes/riley-sample.pdf", default: true }] } });
  });

  it("adds a second résumé without a second default, and shows the writer's roster error", async () => {
    const api = await openStep(
      "resumes",
      {},
      {
        "GET /api/config/profile": () => ok(profileDoc({ resumes: [{ id: "general", label: "General", file: "resumes/general.pdf", default: true }] })),
        "POST /api/resumes": () => ok({ file: "resumes/second.pdf" }),
        "POST /api/setup/steps/resumes/apply": () => fail("validation_failed", "rejected", { details: { fields: { roster: "exactly one default is allowed" } } }),
      },
    );
    await waitFor(() => button("Upload a résumé PDF") && !button("Upload a résumé PDF")!.disabled);
    const picker = q<HTMLInputElement>("input[type=file]")!;
    Object.defineProperty(picker, "files", { value: [new File([new Uint8Array(100)], "second.pdf", { type: "application/pdf" })] });
    await act(async () => void picker.dispatchEvent(new Event("change", { bubbles: true })));
    await waitFor(() => api.callsTo("POST /api/setup/steps/resumes/apply").length === 1);
    expect((api.callsTo("POST /api/setup/steps/resumes/apply")[0].body as { input: { roster: { default: boolean }[] } }).input.roster.map((r) => r.default)).toEqual([true, false]);
    await waitFor(() => text().includes("exactly one default is allowed"));
  });

  it("makes another résumé the default through the roster", async () => {
    const api = await openStep(
      "resumes",
      {},
      {
        "GET /api/config/profile": () => ok(profileDoc({ resumes: [{ id: "general", file: "resumes/general.pdf", default: true, instructions: "Lead with platform work." }, { id: "ml", file: "resumes/ml.pdf", default: false }] })),
        "GET /api/resumes": () => ok({ resumes: [{ id: "general", label: "General", tags: [], default: true, description: "", fileName: "general.pdf", size: 1000 }, { id: "ml", label: "ML", tags: [], default: false, description: "", fileName: "ml.pdf", size: 1000 }] }),
      },
    );
    await click(await waitFor(() => qa<HTMLButtonElement>("button").find((b) => b.getAttribute("aria-label") === "Make ML the default")!));
    await waitFor(() => api.callsTo("POST /api/setup/steps/resumes/apply").length === 1);
    expect(api.callsTo("POST /api/setup/steps/resumes/apply")[0].body).toEqual({
      input: { roster: [{ id: "general", file: "resumes/general.pdf", default: false, instructions: "Lead with platform work." }, { id: "ml", file: "resumes/ml.pdf", default: true }] },
    });
  });
});

describe("Setup: story", () => {
  it("shows the five guiding prompts and sends the narrative", async () => {
    const api = await openStep("story", { story: { status: "missing" } });
    await waitFor(() => qa("ol li").length === 5);
    expect(qa("ol li").map((l) => l.textContent?.split(":")[0])[0]).toBe("A one-line headline");
    await type(q<HTMLTextAreaElement>('[data-field="narrative"] textarea'), "I build reliable data systems.");
    await click(button("Save story"));
    await waitFor(() => api.callsTo("POST /api/setup/steps/story/apply").length === 1);
    expect(api.callsTo("POST /api/setup/steps/story/apply")[0].body).toEqual({ input: { narrative: "I build reliable data systems." } });
  });

  it("skips and restores the voice part through its own routes", async () => {
    const api = await openStep("story", {}, {
      "POST /api/setup/steps/story.voice/skip": () => ok(setupStep("story")),
      "POST /api/setup/steps/story.voice/unskip": () => ok(setupStep("story")),
    });
    await click(await waitFor(() => button("Skip voice")));
    await waitFor(() => api.callsTo("POST /api/setup/steps/story.voice/skip").length === 1);
    await waitFor(() => button("Use voice samples again"));
    expect(q<HTMLTextAreaElement>('[data-field="voice"] textarea')!.disabled).toBe(true);
    await click(button("Use voice samples again"));
    await waitFor(() => api.callsTo("POST /api/setup/steps/story.voice/unskip").length === 1);
    expect(button("Skip voice")).not.toBeNull();
  });
});
