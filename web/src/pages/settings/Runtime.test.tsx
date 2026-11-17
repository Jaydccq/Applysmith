import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok } from "../../testing/api";
import { click, mount, q, text, type, waitFor } from "../../testing/dom";
import { RuntimeSection, LEGAL_TEXT } from "./Runtime";

function server(initial = { llmFallbackEnabled: false, groundingMode: "strict", autoAcceptLegalDeclarations: false, allowGoogleSignIn: false, watchScans: false, knowledgeSemanticRetrieval: false }) {
  let cur = initial;
  const api = fakeApi({
    "GET /api/answer-bank/settings": () => ok(cur),
    "PATCH /api/answer-bank/settings": (c) => {
      cur = { ...cur, ...(c.body as object) };
      return ok(cur);
    },
  });
  return api;
}

const box = (label: RegExp) => Array.from(document.querySelectorAll<HTMLInputElement>("input[type=checkbox]")).find((i) => label.test(i.closest("label")?.textContent ?? ""))!;
const dialog = () => q("dialog");

describe("Settings: runtime settings", () => {
  it("turns the legal declarations on only after a dialog that repeats the text, and turns them off at once", async () => {
    const api = server();
    await mount(<RuntimeSection />);
    await click(await waitFor(() => box(/legal declarations/)));
    expect(api.callsTo("PATCH /api/answer-bank/settings")).toHaveLength(0);
    const d = dialog()!;
    for (const line of LEGAL_TEXT.slice(0, 4)) expect(d.textContent).toContain(line.slice(0, 40));
    expect(d.textContent).toContain("certifying that the information is true");
    expect(d.textContent).toContain("typed signatures");
    await click(Array.from(d.querySelectorAll("button")).find((b) => b.textContent === "Cancel")!);
    expect(api.callsTo("PATCH /api/answer-bank/settings")).toHaveLength(0);
    expect(box(/legal declarations/).checked).toBe(false);

    await click(box(/legal declarations/));
    await click(Array.from(dialog()!.querySelectorAll("button")).find((b) => b.textContent === "Turn on")!);
    expect(api.callsTo("PATCH /api/answer-bank/settings")[0].body).toEqual({ autoAcceptLegalDeclarations: true });
    await waitFor(() => box(/legal declarations/).checked);
    expect(text()).toContain("written to the audit log");

    await click(box(/legal declarations/));
    expect(api.callsTo("PATCH /api/answer-bank/settings")[1].body).toEqual({ autoAcceptLegalDeclarations: false });
    expect(dialog()).toBeNull();
  });

  it("asks before the least strict grounding mode and writes the other two at once", async () => {
    const api = server();
    await mount(<RuntimeSection />);
    const select = await waitFor(() => q<HTMLSelectElement>("select"));
    await type(select, "flex");
    expect(api.callsTo("PATCH /api/answer-bank/settings")[0].body).toEqual({ groundingMode: "flex" });
    await waitFor(() => q<HTMLSelectElement>("select")!.value === "flex");
    await type(q<HTMLSelectElement>("select"), "dangerous");
    expect(api.callsTo("PATCH /api/answer-bank/settings")).toHaveLength(1);
    expect(dialog()!.textContent).toContain("treats it as flexible");
    await click(Array.from(dialog()!.querySelectorAll("button")).find((b) => b.textContent === "Use least strict")!);
    expect(api.callsTo("PATCH /api/answer-bank/settings")[1].body).toEqual({ groundingMode: "dangerous" });
  });

  it("shows the refusal and leaves the setting unchanged", async () => {
    const api = server();
    api.on("PATCH /api/answer-bank/settings", () => fail("validation_failed", "groundingMode must be strict, flex or dangerous"));
    await mount(<RuntimeSection />);
    await type(await waitFor(() => q<HTMLSelectElement>("select")), "flex");
    await waitFor(() => text().includes("groundingMode must be strict, flex or dangerous"));
    expect(q<HTMLSelectElement>("select")!.value).toBe("strict");
  });
});
