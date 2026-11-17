import { act } from "react";
import { describe, expect, it } from "vitest";
import { navigate } from "../router";
import { fail } from "../testing/api";
import { button, click, mount, q, qa, text, waitFor } from "../testing/dom";
import { setupApi, setupState } from "../testing/fixtures_setup";
import Setup from "./Setup";

const navLinks = () => qa<HTMLAnchorElement>("nav[aria-label='Setup steps'] a");

async function open(id: string) {
  await act(async () => navigate(`/setup?step=${id}`));
  await waitFor(() => q(`article[data-step="${id}"]`));
}

describe("Setup wizard", () => {
  it("skips and unskips an optional step through the decision routes", async () => {
    const api = setupApi(setupState());
    await mount(<Setup />);
    await waitFor(() => navLinks().length === 18);
    await open("captcha");
    await click(button("Skip this step"));
    await waitFor(() => api.callsTo("POST /api/setup/steps/captcha/skip").length === 1);
    await waitFor(() => api.callsTo("GET /api/setup/state").length === 2);
  });

  it("offers acknowledge only for a step with an ackable reason", async () => {
    const api = setupApi(
      setupState({
        identity: { status: "template", reasons: [{ field: "candidate.full_name", text: "the name is the example persona", ackable: true }] },
        cv: { status: "partial", reasons: [{ text: "cv.md is shorter than 300 characters" }] },
        orientation: { status: "partial", reasons: [{ field: "orientation", text: "Read the daily loop and the steps you skipped, then acknowledge", ackable: true }] },
      }),
    );
    await mount(<Setup />);
    await waitFor(() => navLinks().length === 18);
    await open("cv");
    expect(button("Acknowledge")).toBeNull();
    // Orientation's own "I have read this" is its one acknowledgement.
    await open("orientation");
    expect(button("Acknowledge")).toBeNull();
    await open("identity");
    expect(text()).toContain("match the shipped example");
    await click(button("Acknowledge"));
    await waitFor(() => api.callsTo("POST /api/setup/steps/identity/ack").length === 1);
  });

  it("explains a failed load and offers a retry", async () => {
    setupApi(setupState(), { "GET /api/setup/state": () => fail("unavailable", "Setup is not available in this build.") });
    await mount(<Setup />);
    await waitFor(() => text().includes("Setup is not available in this build."));
    expect(button("Try again")).not.toBeNull();
  });
});
