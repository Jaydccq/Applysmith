import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { appliedText, startBanner } from "../src/widgets/banner";
import { watchPage } from "../src/widgets/main";

const roots: ShadowRoot[] = [];
const realAttach = Element.prototype.attachShadow;
let stop: { destroy(): void } | undefined;
const banner = () => roots.filter((r) => r.host.isConnected).map((r) => r.querySelector(".banner")).find(Boolean) as HTMLElement | undefined;

beforeEach(() => {
  roots.length = 0;
  Element.prototype.attachShadow = function (init: ShadowRootInit) {
    const r = realAttach.call(this, { ...init, mode: "open" });
    roots.push(r);
    return r;
  };
  document.body.innerHTML = "<main>posting</main>";
});
afterEach(() => {
  stop?.destroy();
  stop = undefined;
  Element.prototype.attachShadow = realAttach;
});

const deps = (over: Partial<Parameters<typeof startBanner>[0]> = {}) => ({
  doc: document,
  changes: watchPage(document),
  win: window,
  flags: async () => ({ off: false, editWithAI: true }),
  get: vi.fn(async () => ({ show: true, key: "k1", date: "2026-09-30" })),
  dismiss: vi.fn(async () => {}),
  ...over,
});

describe("applied banner", () => {
  it("an unconfirmed record never reads as a confirmed application", async () => {
    const at = new Date(2026, 3, 2, 10).toISOString();
    const view = (submission: { confirmed: boolean; agent: boolean; fromLedger: boolean }) => ({ show: true, key: "k1", date: at, submission });
    expect(appliedText(view({ confirmed: false, agent: false, fromLedger: false }))).toBe("You applied to this job on 4/2/26. (unconfirmed)");
    expect(appliedText(view({ confirmed: true, agent: false, fromLedger: false }))).toBe("You applied to this job on 4/2/26.");
    expect(appliedText(view({ confirmed: true, agent: true, fromLedger: true }))).toBe("Applied by Applysmith agent on 4/2/26. (from your application ledger)");
    expect(appliedText(view({ confirmed: false, agent: true, fromLedger: true }))).toBe("Applied by Applysmith agent on 4/2/26. (unconfirmed) (from your application ledger)");
    // The tracker's own status, with no submission to quote, is the person's statement.
    expect(appliedText({ show: true, key: "k1", date: "2026-04-02" })).toBe("You applied to this job on 4/2/26.");
    stop = startBanner(deps({ get: vi.fn(async () => view({ confirmed: false, agent: false, fromLedger: false })) }));
    await vi.waitFor(() => expect(banner()).toBeDefined());
    expect(banner()!.textContent).toContain("(unconfirmed)");
  });

  it("does not appear when the tracker does not show it, when the host is off, or when the application cannot be reached", async () => {
    stop = startBanner(deps({ get: vi.fn(async () => ({ show: false })) }));
    await new Promise((r) => setTimeout(r, 20));
    expect(banner()).toBeUndefined();
    stop.destroy();
    stop = startBanner(deps({ flags: async () => ({ off: true, editWithAI: true }) }));
    await new Promise((r) => setTimeout(r, 20));
    expect(banner()).toBeUndefined();
    stop.destroy();
    stop = startBanner(deps({ get: vi.fn(async () => { throw new Error("offline"); }) }));
    await new Promise((r) => setTimeout(r, 20));
    expect(banner()).toBeUndefined();
  });
});
