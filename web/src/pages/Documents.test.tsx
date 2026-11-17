import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok } from "../testing/api";
import { button, byText, click, mount, q, qa, text, waitFor, press } from "../testing/dom";
import { candidacy, detail, docCheck, documentList, documentView, page, rows, summary, transitions, verification } from "../testing/fixtures";
import { DocumentsDrawer } from "./Documents";
import Tracker from "./Tracker";

function server(...docs: ReturnType<typeof documentView>[]) {
  return fakeApi({ "GET /api/documents": () => ok(documentList(...docs)) });
}

async function open() {
  await mount(<DocumentsDrawer candidacyId={1} label="Northwind Example, Staff Engineer" onClose={() => {}} />);
  await waitFor(() => !text().includes("Loading"));
}

describe("DocumentsDrawer", () => {
  it("asks for the documents of the application", async () => {
    const api = server(documentView());
    await open();
    expect(api.callsTo("GET /api/documents")[0].query.get("candidacy")).toBe("1");
    expect(text()).toContain("Documents: Northwind Example, Staff Engineer");
  });

  it("lists each mechanical check with its own result and never turns an unverified one into a pass", async () => {
    server(
      documentView({
        pdfState: "unverified",
        ready: false,
        verification: verification({
          state: "unverified",
          checks: [
            docCheck("verify_pdf", "text-layer", "pass"),
            docCheck("verify_layout", "clipping", "fail", "p1 text extends outside the page"),
            docCheck("final", "contact", "unverified", "the text layer could not be extracted"),
          ],
        }),
      }),
    );
    await open();
    const rowOf = (name: string) => qa("tbody tr").find((r) => r.textContent?.includes(name))!;
    expect(rowOf("Layout: clipping").textContent).toContain("Fail");
    expect(rowOf("Layout: clipping").textContent).toContain("extends outside the page");
    expect(rowOf("Content: contact").textContent).toContain("Unverified");
    expect(rowOf("Text layer: text-layer").textContent).toContain("Pass");
    expect(text()).toContain("1 passed, 1 failed, 1 unverified of 3 checks.");
    expect(text()).toContain("An unverified check could not run. It is not a pass.");
    expect(text()).not.toContain("Checks passed");
  });

  it("does not show a passed badge when the stored verdict no longer matches the file", async () => {
    server(documentView({ verification: verification({ state: "unverified", checks: [], stale: "the PDF is not the one the verdict describes" }) }));
    await open();
    expect(text()).toContain("Not verified");
    expect(text()).not.toContain("Checks passed");
    expect(text()).toContain("The stored checks no longer describe this file (the PDF is not the one the verdict describes)");
  });

  it("marks a failed PDF as failed and offers to make it again, naming the checks that did not pass", async () => {
    const api = server(documentView({ pdfState: "failed", ready: false, verification: verification({ state: "failed", checks: [docCheck("verify_pdf", "text-layer", "pass"), docCheck("final", "contact", "fail", "not in the text layer: the email address")] }) }));
    api.on("POST /api/documents/11/pdf", () => ({ data: { runId: "run-9" }, status: 202 }));
    await open();
    expect(text()).toContain("Checks failed");
    expect(button("Make PDF")).toBeNull();
    const again = button("Make the PDF again")!;
    const why = document.getElementById(again.getAttribute("aria-describedby")!)!.textContent!;
    expect(why).toContain("Not passed: Content contact (not in the text layer: the email address).");
    expect(why).not.toContain("text-layer");
    expect(why).toContain("edit the text under Show text");
    await click(again);
    await waitFor(() => api.callsTo("POST /api/documents/11/pdf").length === 1);
  });

  it("offers to allow a PDF refused below the score floor, records it, then asks for the PDF again", async () => {
    let refused = true;
    const api = fakeApi({
      "GET /api/documents": () => ok(documentList(documentView({ pdfState: "none", pdfPath: undefined, verification: undefined }))),
      "POST /api/documents/11/pdf": () =>
        refused ? fail("validation_failed", "score 3.9 is below the 4.5 floor", { details: { reason: "below-score-floor" } }) : { data: { runId: "run_pdf" }, status: 202 },
      "POST /api/candidacies/1/documents-override": () => ((refused = false), ok(candidacy({ reportNumber: 7 }))),
    });
    await open();
    expect(button("Allow a PDF anyway")).toBeNull();
    await click(button("Make PDF"));
    await waitFor(() => button("Allow a PDF anyway"));
    await click(button("Allow a PDF anyway"));
    await waitFor(() => api.callsTo("POST /api/documents/11/pdf").length === 2);
    expect(api.callsTo("POST /api/candidacies/1/documents-override")).toHaveLength(1);
    await waitFor(() => button("Allow a PDF anyway") === null);
  });

  it("shows a failed request with its id", async () => {
    fakeApi({ "GET /api/documents": () => fail("unavailable", "The database is busy.", { retryable: true }) });
    await mount(<DocumentsDrawer candidacyId={1} label="x" onClose={() => {}} />);
    await waitFor(() => text().includes("The database is busy."));
    expect(text()).toContain("Request ");
  });
});

describe("Tracker document drawer", () => {
  function withRow() {
    const api = fakeApi({
      "GET /api/candidacies": () => ok(page(rows)),
      "GET /api/tracker/summary": () => ok(summary),
      "GET /api/candidacies/1": () => ok(detail(rows[0])),
      "GET /api/candidacies/1/transitions": () => ok(transitions("evaluated", ["applied"])),
      "GET /api/documents": () => ok(documentList(documentView())),
    });
    return api;
  }

  it("opens from the row drawer, keeps its state in the URL and returns to the row on Escape", async () => {
    const api = withRow();
    window.history.replaceState(null, "", "/tracker?row=1");
    await mount(<Tracker />);
    await waitFor(() => button("Documents"));
    button("Documents")!.focus();
    await click(button("Documents"));
    await waitFor(() => text().includes("Documents: Northwind Example, Staff Engineer"));
    expect(window.location.search).toBe("?row=1&documents=1");
    expect(api.callsTo("GET /api/documents")[0].query.get("candidacy")).toBe("1");
    await waitFor(() => text().includes("Checks passed"));
    await press("Escape", {}, q("dialog")!);
    await waitFor(() => byText("button", "Documents"));
    expect(window.location.search).toBe("?row=1");
    await waitFor(() => document.activeElement === button("Documents"), "focus on the Documents button");
  });
});
