// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Pipeline stages and their order (question pass loop, claims, lanes).
import { describe, expect, test } from "vitest";
import type { MatchRequest, Resolution } from "../src/protocol";
import { releasesClaim } from "../src/answers";
import { scanControls, type ControlInfo } from "../src/scan";
import { byId, fakeHost, makeEngine, mount, PAD, rowFor, runFill, summaryOf } from "./helpers";

/** A matcher that answers every question with the same text. */
function answerAll(value = "x") {
  const host = fakeHost();
  host.match = async (req: MatchRequest) => {
    host.matchRequests.push(req);
    return { results: req.questions.map((q) => ({ selector: q.selector, outcome: "filled", value }) as Resolution) };
  };
  return host;
}

const asked = (host: { matchRequests: MatchRequest[] }) => host.matchRequests.flatMap((r) => r.questions.map((q) => q.text));

describe("Question pass loop", () => {
  test("stuck selector retired after two attempts", async () => {
    mount(`<label for="s">Preferred shift</label>
      <select id="s"><option value="">Select</option><option>Day</option><option>Night</option></select>
      <label for="r">Reveal the next question</label><input id="r"><div id="slot"></div>${PAD}`);
    // The page puts the select back to its placeholder whenever it is set.
    byId("s").addEventListener("change", () => setTimeout(() => ((byId("s") as unknown as HTMLSelectElement).value = ""), 0));
    byId("r").addEventListener("input", () => {
      if (!document.getElementById("t")) byId("slot").innerHTML = `<label for="t">Preferred team</label><input id="t">`;
    });
    const host = fakeHost({
      "preferred shift": { outcome: "filled", value: "Day" },
      "reveal the next question": { outcome: "filled", value: "go" },
      "preferred team": { outcome: "filled", value: "Platform" },
    });
    const s = summaryOf(await runFill(makeEngine(host)));
    const shifts = asked(host).filter((t) => t === "Preferred shift");
    expect(shifts.length).toBeGreaterThanOrEqual(1);
    expect(shifts.length).toBeLessThanOrEqual(2);
    expect(host.matchRequests.length).toBeLessThan(12);
    expect(rowFor(s, "preferred shift").outcome).toBe("failed");
    // Progressive disclosure of a different field is not affected by the retired one.
    expect(byId("t").value).toBe("Platform");
  });

  test("claim from pass one excludes the profile lane after pass three", async () => {
    mount(`<label for="e1">Preferred contact email</label><input id="e1" autocomplete="email" type="email">
      <div id="chain"></div>${PAD}`);
    const add = (n: number) => {
      byId("chain").insertAdjacentHTML("beforeend", `<label for="c${n}">Chain question ${n}</label><input id="c${n}">`);
      byId(`c${n}`).addEventListener("input", () => {
        if (n < 3) add(n + 1);
      });
    };
    add(1);
    const writes: string[] = [];
    document.addEventListener("input", (e) => writes.push((e.target as HTMLElement).id));
    const host = answerAll("bank@example.org");
    const s = summaryOf(await runFill(makeEngine(host)));
    // Three question passes ran, the first one claimed the email box, and the profile pass never wrote over it.
    expect(host.matchRequests.length).toBeGreaterThanOrEqual(3);
    expect(byId("e1").value).toBe("bank@example.org");
    expect(writes.filter((id) => id === "e1")).toHaveLength(1);
    expect(rowFor(s, "preferred contact email").lane).toBe("bank");
    // A control asked on any pass is not reported as unclaimed afterwards, so it raises no unrecognised event.
    expect((s.events ?? []).filter((ev) => ev.reason === "unrecognised")).toEqual([]);
    expect(s.rows.filter((r) => r.reason === "no-lane-claimed")).toHaveLength(1);
  });
});

describe("Claims given back to the profile pass", () => {
  test("a no-match on a listbox, a radio group or a checkbox keeps its claim", () => {
    mount(`<label for="tx">Postal note</label><input id="tx">
      <label for="se">Region note</label><select id="se"><option value="">Select</option><option>North</option></select>
      <label for="lb">Pick one</label><input id="lb" role="combobox" aria-haspopup="listbox">
      <fieldset><legend>Preferred state of work</legend>
      <label><input type="radio" name="st" value="nc"> North Carolina</label><label><input type="radio" name="st" value="oh"> Ohio</label></fieldset>
      <label><input type="checkbox" id="cb" required> I have read the notes</label>`);
    const by = (kind: string) => scanControls(document).find((i) => i.kind === kind) as ControlInfo;
    const miss: Resolution = { selector: "", outcome: "abstained", reason: "no-match", clientMayOverwrite: true };
    expect(["text", "select"].map((k) => releasesClaim(by(k), miss))).toEqual([true, true]);
    expect(["custom-listbox", "radio-group", "checkbox"].map((k) => releasesClaim(by(k), miss))).toEqual([false, false, false]);
    // The matcher's own decisions stay final for every kind.
    for (const reason of ["context-policy", "duplicate-link", "self-id-policy", "ask-me"] as const) {
      expect(releasesClaim(by("text"), { ...miss, reason, clientMayOverwrite: false })).toBe(false);
    }
  });
});

