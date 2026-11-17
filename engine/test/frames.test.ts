// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Frame fan-out decisions (the pure rules and the in-frame probe).
import { describe, expect, test } from "vitest";
import { probeControls } from "../src/detect";
import { classifyFrame, failedDecision, probeDecision, staticDecision, unreachableFrameEvents } from "../src/frames";
import type { FrameDecision } from "../src/protocol";
import { mount } from "./helpers";

function decide(frameId: string, url: string, probe = { controls: 0, required: 0 }): FrameDecision {
  return staticDecision(frameId, url) ?? probeDecision(frameId, url, probe);
}

/** The events that tell the user a frame of the form could not be reached. */
const unreachable = (decisions: FrameDecision[]) => unreachableFrameEvents(decisions, { host: "x.example.test", url: "https://x.example.test/apply" }, () => "id", 0);

describe("Frame fan-out decisions", () => {
  test("One decision per frame: a cross-origin application iframe, an analytics iframe and a blank iframe", () => {
    const decisions = [
      decide("1", "https://boards.greenhouse.io/embed/job_app?for=example&token=1"),
      decide("2", "https://www.googletagmanager.com/ns.html?id=GTM-1"),
      decide("3", "about:blank"),
    ];
    expect(decisions.map((d) => d.outcome)).toEqual(["admitted-registry", "skipped-denylist", "skipped-zero-controls"]);
    expect(decisions[0]?.url).toBe("https://boards.greenhouse.io/embed/job_app");
    expect(decisions[2]?.url).toBe("about:blank");
  });

  test("Last required field in a frame: a frame with a single required input is admitted by probe", () => {
    mount(`<label for="a">Phone *</label><input id="a" required>`);
    const probe = probeControls(document);
    expect(probe).toEqual({ controls: 1, required: 1 });
    expect(probeDecision("9", "https://forms.example.test/x", probe).outcome).toBe("admitted-probe");
    // One optional field left (a later step of a multi-step form) is not enough.
    expect(probeDecision("10", "https://forms.example.test/x", { controls: 1, required: 0 }).outcome).toBe("skipped-zero-controls");
  });

  test("Probe: counts only visible enabled data-entry controls, never buttons, and pierces open shadow roots", () => {
    mount(`<button>Accept cookies</button><button aria-haspopup="listbox">Language</button>
      <input type="hidden" name="t"><input disabled aria-label="Disabled"><input style="display:none" aria-label="Hidden">
      <div id="host"></div>`);
    const host = document.getElementById("host") as HTMLElement;
    const root = host.attachShadow({ mode: "open" });
    root.innerHTML = `<label for="s">Email</label><input id="s"><label for="t2">Name</label><input id="t2">`;
    expect(probeControls(document)).toEqual({ controls: 2, required: 0 });
  });

  test("Unreachable frame: a failure is recorded with its reason and the others still count", () => {
    const decisions = [failedDecision("2", "https://x.example.test/a?q=1", "failed-inject", "context destroyed"), decide("3", "https://jobs.lever.co/example/1")];
    expect(decisions[0]).toMatchObject({ outcome: "failed-inject", reason: "context destroyed", url: "https://x.example.test/a" });
    expect(decisions[1]?.outcome).toBe("admitted-registry");
    expect(unreachable(decisions)).toHaveLength(1);
  });

  test("Script-built blank frame: about:blank and about:srcdoc are probe candidates admitted by their controls", () => {
    expect(classifyFrame("about:blank")).toBe("probe");
    expect(classifyFrame("about:srcdoc")).toBe("probe");
    expect(probeDecision("4", "about:srcdoc", { controls: 14, required: 0 }).outcome).toBe("admitted-probe");
  });

  test("Only skips: no frame failed to be reached, so no unreachable message", () => {
    const decisions = [decide("1", "https://www.youtube.com/embed/x"), decide("2", "about:blank"), decide("3", "chrome-extension://abc/x.html")];
    expect(decisions.map((d) => d.outcome)).toEqual(["skipped-denylist", "skipped-zero-controls", "skipped-denylist"]);
    expect(unreachable(decisions)).toEqual([]);
  });

});

describe("Frame registry table", () => {
  const accept = [
    "https://boards.greenhouse.io/embed/job_app?for=example",
    "https://job-boards.greenhouse.io/example/jobs/1",
    "https://job-boards.eu.greenhouse.io/example/jobs/1",
    "https://jobs.lever.co/example/abc/apply",
    "https://jobs.ashbyhq.com/example/abc/application",
    "https://example.icims.com/jobs/1/job?in_iframe=1",
    "https://absteraim-abc123.dispatcher.hana.ondemand.com/x",
    "https://example.paycor.com/career/apply",
    "https://career5.successfactors.com/career?company=example",
    "https://career41.sapsf.eu/career?company=example",
    "https://tally.so/r/abc123",
    "https://form.typeform.com/to/abc123",
    "https://form.jotform.com/231234567890",
    "https://docs.google.com/forms/d/e/1FAIpQLSexample/viewform",
    "https://example.paperform.co/",
    "https://example.cognitoforms.com/f/abc/1",
  ];
  const reject = [
    "https://jobs.ashbyhq.com.example.test/x",
    "https://example.icims.com/jobs/1/job",
    "https://example.paycor.com/about",
    "https://tally.so/forms/abc/edit",
    "https://docs.google.com/forms/d/e/abc/edit",
    "https://airtable.com/appXYZ/tblABC",
    "https://www.paperform.co/",
    "ftp://jobs.lever.co/x",
  ];
  test.each(accept)("registry accepts %s", (url) => {
    expect(classifyFrame(url)).toBe("registry");
  });
  test.each(reject)("registry rejects look-alike %s", (url) => {
    expect(classifyFrame(url)).not.toBe("registry");
  });
  test("Deny-listed frames are never probed", () => {
    for (const url of ["https://googleads.g.doubleclick.net/x", "https://www.google.com/recaptcha/api2/anchor", "https://widget.intercom.io/x", "https://www.facebook.com/tr?id=1"]) {
      expect(classifyFrame(url)).toBe("denylist");
    }
  });
});
