// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: File upload and résumé-first ordering (uploader quirks of the enterprise suites).
import { describe, expect, test } from "vitest";
import { byId, fakeHost, makeEngine, mount, runFill } from "./helpers";

describe("Uploader quirks", () => {
  test("An uploader that replaces the input with a clone under the same id is not given the résumé again, and a new slot id still is", async () => {
    mount(`<form><label for="cv">Resume</label><input id="cv" type="file"><label for="fn">First name</label><input id="fn"></form>`);
    const host = fakeHost();
    const engine = makeEngine(host);
    await runFill(engine);
    expect(host.resumeRequests).toHaveLength(1);
    const old = byId("cv");
    const clone = old.cloneNode(true) as HTMLInputElement;
    old.replaceWith(clone);
    await runFill(engine);
    expect(host.resumeRequests).toHaveLength(1);
    byId("cv").parentElement!.innerHTML = `<label for="cv2">Resume</label><input id="cv2" type="file"><label for="fn2">First name</label><input id="fn2">`;
    await runFill(engine);
    expect(host.resumeRequests).toHaveLength(2);
  });
});
