// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Page API; Run lifecycle, polling and deadlines; File upload and résumé-first ordering (postback pages).
import { describe, expect, test, vi } from "vitest";
import { ADA, PAD, byId, fakeHost, makeEngine, mount, runFill, summaryOf, tick } from "./helpers";

const FORM = `<label for="fn">First name</label><input id="fn"><label for="ln">Last name</label><input id="ln">`;

function slowMatchHost() {
  const host = fakeHost();
  let release: () => void = () => {};
  const gate = new Promise<void>((r) => (release = r));
  const match = host.match.bind(host);
  const state = { asked: 0 };
  host.match = async (req) => {
    state.asked++;
    await gate;
    return match(req);
  };
  return { host, release, state };
}

const QUESTION = `<label for="q">Anything else</label><input id="q">`;

describe("Run control", () => {
  test("a real stop publishes its reason, releases the submit hold and cancels the deadline timer", async () => {
    vi.useFakeTimers();
    try {
      mount(`<form id="f">${FORM}${QUESTION}<button id="go">Next</button></form>${PAD}`);
      const { host, release, state } = slowMatchHost();
      const engine = makeEngine(host);
      const stops: string[] = [];
      const stop = engine.stop.bind(engine);
      engine.stop = (id, reason) => (stops.push(reason ?? ""), stop(id, reason));
      const run = engine.start({ applicant: ADA, runId: "run-9", pageDeadlineMs: 5_000 });
      for (let i = 0; i < 400 && state.asked === 0; i++) await vi.advanceTimersByTimeAsync(5);
      expect(engine.stop("run-9", "cancelled")).toBe(true);
      expect(engine.stop("run-9", "cancelled")).toBe(false);
      const st = engine.status();
      expect(st).toMatchObject({ state: "stopped", reason: "cancelled", runId: "run-9" });
      // The hold is gone: a native submission now reaches the page.
      let reached = 0;
      byId("f").addEventListener("submit", (e) => {
        reached++;
        e.preventDefault();
      });
      byId("go").click();
      expect(reached).toBe(1);
      expect(engine.blockedSubmits()).toEqual([]);
      release();
      // Past the deadline: a timer still armed would have called stop with reason deadline.
      await vi.advanceTimersByTimeAsync(10_000);
      expect(stops).toEqual(["cancelled", "cancelled"]);
      expect(engine.status()).toMatchObject({ state: "stopped", reason: "cancelled" });
      expect(run.started).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  test("an answer call holds native submissions only while it runs and leaves nothing behind", async () => {
    // A finished run answers; the call holds submissions only while it runs and leaves nothing behind.
    mount(`<form id="g"><label for="a">Favourite colour</label><input id="a"><label for="b">Favourite animal</label><input id="b"><button id="go2">Submit</button></form>${PAD}`);
    const engine = makeEngine(fakeHost());
    await runFill(engine);
    const inflight = engine.answer([{ selector: '[id="a"]', outcome: "filled", value: "green" }]);
    byId("go2").click();
    expect(engine.blockedSubmits()).toHaveLength(1);
    const reply = await inflight;
    expect(reply.results[0]).toMatchObject({ outcome: "filled", value: "green" });
    expect(byId("a").value).toBe("green");
    let reached = 0;
    byId("g").addEventListener("submit", (e) => {
      reached++;
      e.preventDefault();
    });
    byId("go2").click();
    expect(reached).toBe(1);
    // The next call is not refused as concurrent: the previous one released its place.
    const again = await engine.answer([{ selector: '[id="b"]', outcome: "filled", value: "otter" }]);
    expect(again.results[0]?.outcome).toBe("filled");
    expect(byId("b").value).toBe("otter");
  });
});

describe("Inspection and answers", () => {
  test("inspect never writes and lists group once", () => {
    mount(`<fieldset><legend>Preferred contact</legend>
      <label><input type="radio" name="c" value="e"> Email</label><label><input type="radio" name="c" value="p"> Phone</label></fieldset>
      <fieldset><legend>Languages (select all that apply)</legend>
      <label><input type="checkbox" name="l" value="en"> English</label><label><input type="checkbox" name="l" value="fr" checked> French</label></fieldset>
      <label for="nick">Nickname</label><input id="nick" value="Ace">
      <label for="note">Optional note</label><input id="note">
      <label for="pw">Password</label><input id="pw" type="password" value="swordfish-1">
      <label for="cv">Resume</label><input id="cv" type="file">
      <input id="applysmith-panel-input" aria-label="Extension note">`);
    const before = document.body.innerHTML;
    const events: string[] = [];
    for (const type of ["input", "change", "click", "focus"]) document.addEventListener(type, (e) => events.push(`${type}:${(e.target as HTMLElement).id || e.type}`), true);
    const engine = makeEngine(fakeHost());
    const rows = engine.inspect();
    expect(rows.map((r) => r.label)).toEqual(["Preferred contact", "Languages (select all that apply)", "Nickname", "Optional note", "Password", "Resume"]);
    expect(rows.find((r) => r.label === "Languages (select all that apply)")?.value).toBe("French");
    // A filled optional control reports its value, an empty one reports none.
    expect(rows.find((r) => r.label === "Nickname")?.value).toBe("Ace");
    expect(rows.find((r) => r.label === "Optional note")?.value).toBeUndefined();
    expect(rows.filter((r) => r.inspectionOnly).map((r) => r.label)).toEqual(["Password", "Resume"]);
    expect(JSON.stringify(rows)).not.toContain("swordfish");
    expect(JSON.stringify(rows)).not.toContain("applysmith-panel");
    expect(document.body.innerHTML).toBe(before);
    expect(events).toEqual([]);
  });

  test("duplicate id resolves to the visible copy", async () => {
    mount(`<div id="stale" style="display:none"><label for="dup">Favourite thing</label><input id="dup"></div>
      <div id="shown"><label for="dup">Favourite thing</label><input id="dup"></div>
      <div id="host"></div>${PAD}`);
    const [hiddenCopy, shownCopy] = Array.from(document.querySelectorAll<HTMLInputElement>("input[id=dup]"));
    const root = (byId("host") as HTMLElement).attachShadow({ mode: "open" });
    root.innerHTML = `<label for="sx">Shadow thing</label><input id="sx">`;
    const engine = makeEngine(fakeHost());
    const reply = await engine.answer([
      { selector: '[id="dup"]', outcome: "filled", value: "Tea" },
      { selector: '[id="host"] >>> [id="sx"]', outcome: "filled", value: "Coffee" },
      { selector: '[id="gone"]', outcome: "filled", value: "x" },
    ]);
    expect(hiddenCopy?.value).toBe("");
    expect(shownCopy?.value).toBe("Tea");
    expect((root.getElementById("sx") as HTMLInputElement).value).toBe("Coffee");
    expect(reply.results.map((r) => r.outcome)).toEqual(["filled", "filled", "failed"]);
    expect(reply.results[2]?.reason).toBe("control-detached");
  });

  test("a selector naming a control the engine never fills is an explicit failure", async () => {
    mount(`<label for="pw">Password</label><input id="pw" type="password"><input id="tok" type="hidden" value="t">${PAD}`);
    const engine = makeEngine(fakeHost());
    const reply = await engine.answer([
      { selector: '[id="pw"]', outcome: "filled", value: "hunter2" },
      { selector: '[id="tok"]', outcome: "filled", value: "x" },
    ]);
    expect(reply.results.map((r) => [r.outcome, r.reason])).toEqual([
      ["failed", "control-detached"],
      ["failed", "control-detached"],
    ]);
    expect(byId("pw").value).toBe("");
    expect(byId("tok").value).toBe("t");
  });
});

describe("Pages that reload on a change", () => {
  /** A server-rendered page: every change posts back and the page is drawn again from the server's state. */
  function serverPage() {
    const server = { saved: "", country: "", countryChanges: 0, uploads: 0 };
    const render = () => {
      mount(`<form id="wf">
        <label for="cv">Resume</label><input id="cv" type="file">${server.saved ? `<input type="hidden" id="ctl00_Resume_FileName" value="${server.saved}">` : ""}
        <label for="co">Country</label><select id="co">${["", "United States", "Canada"].map((c) => `<option${c === server.country ? " selected" : ""}>${c}</option>`).join("")}</select>
        <label for="fn">First name</label><input id="fn"></form>${PAD}`);
    };
    document.addEventListener(
      "change",
      (e) => {
        const t = e.target as HTMLElement;
        if (t.id === "co") {
          server.countryChanges++;
          server.country = (t as unknown as HTMLSelectElement).value;
        } else if (t.id === "cv") {
          server.uploads++;
          server.saved = (t as HTMLInputElement).files?.[0]?.name ?? "";
        } else return;
        setTimeout(render, 10);
      },
      true,
    );
    render();
    return server;
  }

  test("unchanged autopostback select is not re-fired", async () => {
    const server = serverPage();
    const host = fakeHost();
    const engine = makeEngine(host, { parseNoChangeMs: 80, parseQuietMs: 20 });
    await runFill(engine);
    await tick(30);
    expect(server.countryChanges).toBe(1);
    expect(server.uploads).toBe(1);
    expect((byId("co") as unknown as HTMLSelectElement).value).toBe("United States");
    expect(host.resumeRequests).toHaveLength(1);

    // The fill runs again on the page the server drew: the held country is left alone and nothing is attached again.
    const s = summaryOf(await runFill(engine));
    await tick(30);
    expect(server.countryChanges).toBe(1);
    expect(server.uploads).toBe(1);
    expect(host.resumeRequests).toHaveLength(1);
    expect(s.rows.find((r) => r.label === "Country")).toMatchObject({ outcome: "prefilled", value: "United States" });
    expect((byId("cv") as HTMLInputElement).files?.length ?? 0).toBe(0);
  });
});
