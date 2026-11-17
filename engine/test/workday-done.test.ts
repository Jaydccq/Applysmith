// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Drivers for enterprise application suites (the responsive dialog is closed with Done);
// Controls the engine never writes or presses (that press passes the guard).
import { describe, expect, test } from "vitest";
import { mountWorkdayPrompt } from "./ats-fixtures";
import { mount, write } from "./helpers";

/** Renames the dialog's Done button, its only button, the moment the dialog mounts. */
function reword(label: string): void {
  const obs = new MutationObserver(() => {
    const done = document.querySelector<HTMLElement>("[data-automation-mode=reactresponsive] button");
    if (!done) return;
    done.textContent = label;
    obs.disconnect();
  });
  obs.observe(document.body, { childList: true, subtree: true });
}

async function run(label: string) {
  mount("");
  reword(label);
  const p = mountWorkdayPrompt({ id: "school", label: "School or University", catalog: ["Example University"], responsive: true });
  await write("school", "Example University");
  return p.doneClicks();
}

describe("Workday responsive dialog button", () => {
  test("Done and OK close the dialog", async () => {
    expect(await run("Done")).toBe(1);
    expect(await run("OK")).toBe(1);
  });

  test("a lone Next, Continue, Save or Sign in button in the dialog is not pressed", async () => {
    for (const label of ["Next", "Continue", "Save", "Sign in"]) expect(await run(label), label).toBe(0);
  });

});
