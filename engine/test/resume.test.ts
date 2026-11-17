// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: File upload and résumé-first ordering.
import { describe, expect, test } from "vitest";
import { ADA, PDF, byId, fakeHost, makeEngine, mount, rowFor, runFill, summaryOf, tick, until } from "./helpers";

const BASE = `<form id="f"><label for="cv">Resume</label><input id="cv" type="file"><div id="block"><label for="fn">First name</label><input id="fn"><label for="em">Email</label><input id="em"></div></form>`;

describe("File upload and résumé-first ordering", () => {
  test("Upload first: a page that re-renders its form after an upload gets the name and email writes after the re-render, and they survive it", async () => {
    mount(BASE);
    const writesBefore: string[] = [];
    byId("cv").addEventListener("change", () => {
      setTimeout(() => {
        byId("block").innerHTML = `<label for="fn">First name</label><input id="fn"><label for="em">Email</label><input id="em">`;
      }, 30);
    });
    for (const id of ["fn", "em"]) byId(id).addEventListener("input", () => writesBefore.push(id));
    const engine = makeEngine(fakeHost(), { parseNoChangeMs: 150, parseQuietMs: 25, parsePollMs: 5 });
    const s = summaryOf(await runFill(engine));
    expect(writesBefore).toEqual([]);
    expect(byId("fn").value).toBe("Ada");
    expect(byId("em").value).toBe("ada@example.com");
    expect(s.rows.find((r) => r.kind === "file")?.fileName).toBe("ada-example.pdf");
  });

  test("Native upload needed: an input that ignores the assignment gets a row with needs-native-upload and its selector", async () => {
    mount(BASE);
    Object.defineProperty(byId("cv"), "files", { configurable: true, get: () => ({ length: 0 }), set: () => {} });
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    const row = rowFor(s, "resume");
    expect(row).toMatchObject({ outcome: "skipped", reason: "needs-native-upload", selector: '[id="cv"]' });
    expect(row?.fileName).toBeTruthy();
  });

  test("Already uploaded: a slot that shows a Replace file control and a chip with a file name gets no second upload", async () => {
    mount(`<div class="slot"><label for="cv">Resume</label><input id="cv" type="file"><span class="chip">ada-example.pdf</span><button type="button">Replace file</button></div><label for="fn">First name</label><input id="fn">`);
    const host = fakeHost();
    await runFill(makeEngine(host));
    expect(host.resumeRequests).toHaveLength(0);
  });

  test("Workday's upload already holding the same document, by name and size, gets no second copy; another size does", async () => {
    const workday = (size: string) =>
      `<p><label>Upload a file (5MB max)</label><div data-automation-id="attachments-FileUpload"><div><div>Drop files here</div><button type="button" data-automation-id="select-files">Select files</button><input id="cv" data-automation-id="file-upload-input-ref" type="file"></div>` +
      `<div data-automation-id="file-upload-item"><div data-automation-id="file-upload-item-name">ada-example.pdf</div><div>${size}</div><div data-automation-id="file-upload-successful">Successfully Uploaded!</div></div></div></p><label for="fn">First name</label><input id="fn">`;
    // PDF is 16 bytes: Workday shows 0.02 KB.
    mount(workday("0.02 KB"));
    let s = summaryOf(await runFill(makeEngine(fakeHost())));
    expect(s.rows.find((r) => r.kind === "file")?.outcome).toBe("prefilled");
    expect(byId("cv").files?.length ?? 0).toBe(0);
    mount(workday("34.58 KB"));
    s = summaryOf(await runFill(makeEngine(fakeHost())));
    expect(s.rows.find((r) => r.kind === "file")?.outcome).toBe("filled");
  });

  test("Identical inputs: only sibling text says Cover Letter for the second, so the résumé is attached once, to the first", async () => {
    mount(`<form><div><input id="a" type="file" accept=".pdf"></div><div><span>Cover Letter</span><input id="b" type="file" accept=".pdf"></div><label for="fn">First name</label><input id="fn"></form>`);
    const host = fakeHost();
    const letter = { fileName: "letter.pdf", mediaType: "application/pdf", contentBase64: btoa("%PDF-1.4 l") };
    host.resume = async (req) => {
      host.resumeRequests.push(req);
      return req.document === "resume" ? PDF : letter;
    };
    const engine = makeEngine(host);
    await runFill(engine);
    expect(host.resumeRequests.map((r) => r.document)).toEqual(["resume", "cover-letter"]);
    expect((byId("a") as HTMLInputElement).files?.[0]?.name).toBe("ada-example.pdf");
    expect((byId("b") as HTMLInputElement).files?.[0]?.name).toBe("letter.pdf");
  });

  test("Cover-letter slot is filled only when the host supplies a cover-letter document", async () => {
    mount(`<label for="cl">Cover letter</label><input id="cl" type="file"><label for="fn">First name</label><input id="fn">`);
    const host = fakeHost();
    await runFill(makeEngine(host));
    expect((byId("cl") as HTMLInputElement).files?.length ?? 0).toBe(0);
    expect(host.resumeRequests.map((r) => r.document)).toEqual(["cover-letter"]);
  });

  test("Letter box: a box labelled Cover letter gets the letter, paragraphs kept, Why-us none; no letter or too long leaves it", async () => {
    const letter = "Dear team,\n\nI build platforms.";
    for (const [max, give, typed] of [["", true, letter], ["", false, ""], [' maxlength="9"', true, ""]] as const) {
      mount(`<label for="cl">Cover letter<sup>*</sup></label><textarea id="cl"${max}></textarea><label for="why">Why do you want to work here?</label><textarea id="why"></textarea><label for="q">Did you submit a cover letter?</label><input id="q">`);
      const host = fakeHost();
      host.resume = async (req) => (host.resumeRequests.push(req), give ? { fileName: "c.txt", mediaType: "text/plain", contentBase64: btoa(letter) } : null);
      await runFill(makeEngine(host));
      expect([byId("cl").value, byId("why").value]).toEqual([typed, ""]);
      expect(host.resumeRequests).toEqual([{ runId: expect.any(String), document: "cover-letter", format: "text", ...(max ? { maxLength: 9 } : {}) }]);
      expect(host.matchRequests.length > 0 && host.matchRequests.every((m) => m.coverLetterAttached === (typed ? true : undefined))).toBe(true);
    }
  });

  test("A slot whose own label names another document is vetoed", async () => {
    mount(`<label for="t">Portfolio</label><input id="t" type="file"><label for="fn">First name</label><input id="fn">`);
    const host = fakeHost();
    await runFill(makeEngine(host));
    expect(host.resumeRequests).toHaveLength(0);
  });

  test("A transcript slot asks for the transcript, never the résumé: the host's transcript is attached, and with none the slot stays empty", async () => {
    const transcript = { fileName: "unofficial-transcript.pdf", mediaType: "application/pdf", contentBase64: btoa("%PDF-1.4 t") };
    for (const stored of [true, false]) {
      mount(`<label for="t">Unofficial transcript</label><input id="t" type="file"><label for="q">Why do you want to work here?</label><input id="q">`);
      const host = fakeHost();
      host.resume = async (req) => (host.resumeRequests.push(req), req.document === "resume" ? PDF : req.document === "transcript" && stored ? transcript : null);
      const s = summaryOf(await runFill(makeEngine(host)));
      expect(host.resumeRequests.map((r) => r.document)).toEqual(["transcript"]);
      expect((byId("t") as HTMLInputElement).files?.[0]?.name, `stored ${stored}`).toBe(stored ? "unofficial-transcript.pdf" : undefined);
      expect(s.rows.some((r) => r.kind === "file" && r.outcome === "filled"), `stored ${stored}`).toBe(stored);
      // The transcript is not the résumé: the questions are not told one was attached.
      expect(host.matchRequests.length > 0 && host.matchRequests.every((m) => m.resumeAttached === undefined)).toBe(true);
    }
  });

  test("Accept list: a slot that accepts only .doc and .docx is skipped with format-not-accepted for a PDF", async () => {
    mount(`<label for="cv">Resume</label><input id="cv" type="file" accept=".doc,.docx"><label for="fn">First name</label><input id="fn">`);
    const s = summaryOf(await runFill(makeEngine(fakeHost())));
    expect(rowFor(s, "resume")).toMatchObject({ outcome: "skipped", reason: "format-not-accepted" });
    expect((byId("cv") as HTMLInputElement).files?.length ?? 0).toBe(0);
  });

  test("The same input is never attached twice within a document across fills", async () => {
    mount(BASE);
    const host = fakeHost();
    const engine = makeEngine(host);
    await runFill(engine);
    Object.defineProperty(byId("cv"), "files", { configurable: true, get: () => ({ length: 0 }) });
    await runFill(engine);
    expect(host.resumeRequests).toHaveLength(1);
  });

  test("A stop during the binding await attaches nothing", async () => {
    mount(BASE);
    const host = fakeHost();
    let release: (p: typeof PDF) => void = () => {};
    host.resume = (req) => {
      host.resumeRequests.push(req);
      return new Promise((r) => (release = r));
    };
    const engine = makeEngine(host);
    const r = engine.start({ applicant: ADA });
    await until(() => host.resumeRequests.length === 1);
    engine.stop(r.runId as string);
    release(PDF);
    await tick(30);
    expect((byId("cv") as HTMLInputElement).files?.length ?? 0).toBe(0);
    expect(engine.status().state).toBe("stopped");
  });
});
