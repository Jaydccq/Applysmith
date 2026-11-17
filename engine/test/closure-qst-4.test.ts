// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Question pass (loop, repeat rules, budgets, apply order, outcomes) and Page API (summary rows).
import { describe, expect, test } from "vitest";
import { PAD, byId, fakeHost, makeEngine, mount, rowFor, runFill, summaryOf } from "./helpers";

const asked = (host: ReturnType<typeof fakeHost>) => host.matchRequests.flatMap((r) => r.questions.map((q) => q.text));

describe("Question loop", () => {
  test("second control with identical question and options is answered locally", async () => {
    mount(`<label for="a1">Preferred office</label><select id="a1"><option value="">Select</option><option>Austin</option><option>Denver</option></select><div id="slot"></div>
      <label for="b1">Pet name?</label><input id="b1">${PAD}`);
    byId("a1").addEventListener("change", () => {
      byId("slot").innerHTML = `<label for="a2">Preferred office</label><select id="a2"><option value="">Select</option><option>Austin</option><option>Denver</option></select>
        <label for="b2">Pet name?</label><input id="b2">`;
    });
    const host = fakeHost({
      "preferred office": { outcome: "filled", value: "Austin" },
      "pet name?": { outcome: "abstained", reason: "no-entry" },
    });
    // Were the second office asked, the matcher would send it somewhere else.
    const base = host.match.bind(host);
    host.match = async (req) => {
      const res = await base(req);
      if (host.matchRequests.length > 1) for (const r of res.results) if (/a2/.test(r.selector)) Object.assign(r, { outcome: "filled", value: "Denver" });
      return res;
    };
    const s = summaryOf(await runFill(makeEngine(host)));
    expect((byId("a2") as unknown as HTMLSelectElement).value).toBe("Austin");
    expect(asked(host).filter((t) => t === "Preferred office")).toHaveLength(1);
    expect(rowFor(s, "Preferred office").outcome).toBe("filled");
    // Only a real answer is reused: the abstained question is put to the matcher again for the other control.
    expect(asked(host).filter((t) => t === "Pet name?")).toHaveLength(2);
  });
});

describe("Question apply", () => {
  test("replaced select node is re-resolved and written once", async () => {
    mount(`<div id="w"><label for="s">Preferred language</label><select id="s"><option value="">Select</option><option>English</option><option>French</option></select></div>${PAD}`);
    let writes = 0;
    const host = fakeHost({ "preferred language": { outcome: "filled", value: "French" } });
    // The page swaps the select for a fresh node while the answer is on its way.
    const base = host.match.bind(host);
    host.match = async (req) => {
      const res = await base(req);
      byId("w").innerHTML = `<label for="s">Preferred language</label><select id="s"><option value="">Select</option><option>English</option><option>French</option></select>`;
      byId("s").addEventListener("change", () => writes++);
      return res;
    };
    const s = summaryOf(await runFill(makeEngine(host)));
    expect((byId("s") as unknown as HTMLSelectElement).value).toBe("French");
    expect(writes).toBe(1);
    expect(rowFor(s, "Preferred language").outcome).toBe("filled");
  });
});
