import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createLayer } from "../src/widgets/layer";
import { startWidgets, type Widgets } from "../src/widgets/main";

const send = vi.fn();
vi.mock("../src/shared/rpc", () => ({ send: (r: unknown) => send(r), failureOf: () => ({ code: "internal", message: "x" }) }));

const roots: ShadowRoot[] = [];
const realAttach = Element.prototype.attachShadow;
let widgets: Widgets | undefined;
let flags: { off: boolean; editWithAI: boolean };

const sparks = () => roots.filter((r) => r.host.isConnected).flatMap((r) => [...r.querySelectorAll(".spark")]);
const kinds = () => send.mock.calls.map((c) => (c[0] as { kind: string }).kind);

function page(url: string) {
  (window as unknown as { happyDOM: { setURL(u: string): void } }).happyDOM.setURL(url);
  document.body.innerHTML = `<form><label for="why">Why us?</label><textarea id="why" rows="6"></textarea></form>`;
}

beforeEach(() => {
  roots.length = 0;
  Element.prototype.attachShadow = function (init: ShadowRootInit) {
    const r = realAttach.call(this, { ...init, mode: "open" });
    roots.push(r);
    return r;
  };
  flags = { off: false, editWithAI: true };
  send.mockReset();
  send.mockImplementation(async (req: { kind: string }) => {
    if (req.kind === "flags.get") return flags;
    if (req.kind === "banner.get") return { show: false };
    return undefined;
  });
});
afterEach(() => {
  widgets?.destroy();
  widgets = undefined;
  Element.prototype.attachShadow = realAttach;
});

describe("page widgets", () => {
  it("shows no Edit with AI button on a page the application heuristic does not accept", async () => {
    page("https://docs.example.org/editor/notes");
    widgets = await startWidgets();
    await new Promise((r) => setTimeout(r, 40));
    expect(sparks()).toHaveLength(0);
  });

  it("starts nothing and sends nothing but the switch question on a host that is switched off", async () => {
    flags = { off: true, editWithAI: true };
    page("https://job-boards.greenhouse.io/example/jobs/1");
    widgets = await startWidgets();
    await new Promise((r) => setTimeout(r, 40));
    expect(sparks()).toHaveLength(0);
    expect(kinds()).toEqual(["flags.get"]);
  });

  it("composes nothing and asks for no credential, code or submission on load", async () => {
    page("https://job-boards.greenhouse.io/example/jobs/1");
    widgets = await startWidgets();
    await new Promise((r) => setTimeout(r, 40));
    for (const k of ["compose.run", "signin.resolve", "signin.settings", "otp.latest", "signin.link", "submission.record", "report.field"]) expect(kinds()).not.toContain(k);
  });
});

describe("page surfaces", () => {
  it("live in a closed shadow root the page cannot reach", () => {
    Element.prototype.attachShadow = realAttach;
    document.body.innerHTML = "<p>page</p>";
    const layer = createLayer(document);
    const host = document.querySelector("[data-applysmith]")!;
    expect(host.shadowRoot).toBeNull();
    expect(layer.box.className).toBe("surface");
    expect((layer.box.getRootNode() as ShadowRoot).host).toBe(host);
    expect(document.querySelector(".surface")).toBeNull();
    layer.destroy();
    expect(layer.alive()).toBe(false);
  });
});
