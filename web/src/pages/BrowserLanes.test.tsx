import { act } from "react";
import { describe, expect, it, vi } from "vitest";
import { fail, fakeApi, ok, sseResponse, type Handler } from "../testing/api";
import { button, byLabel, click, mount, press, q, text, type, waitFor } from "../testing/dom";
import Browser from "./Browser";
import { BrowserPanel, setPanel, shortAddress, toAddress } from "./browser/Lanes";

const tab = (over: object = {}) => ({ id: "tab-1", title: "Apply to Example", url: "https://jobs.example.test/apply", loading: false, active: true, kept: true, ...over });

const status = (over: object = {}) => ({
  state: "running",
  chromeVersion: "Google Chrome 150.0.0.0",
  apply: { window: true, tabCap: 24, tabs: [tab()] },
  scan: { window: true, tabCap: 32, tabs: [tab({ id: "tab-9", title: "Scanning", url: "https://boards.example.test/jobs", active: true, kept: false, owner: "run_scan" })] },
  events: [],
  ...over,
});

const sessions = (over: object = {}) => ({
  sites: [
    { domain: "linkedin.com", label: "LinkedIn", cookies: 4, signIn: "signed_in", signInCheckedAt: "2027-03-10T09:00:00Z", signInUrl: "https://www.linkedin.com/login", sourceCookies: 12 },
    { domain: "indeed.com", label: "Indeed", cookies: 0, signIn: "signed_out", signInCheckedAt: null, signInUrl: "https://secure.indeed.com/auth", sourceCookies: null },
    { domain: "jobright.ai", label: "Jobright", cookies: 0, signIn: "unknown", signInCheckedAt: null, signInUrl: "https://jobright.ai/", sourceCookies: 3 },
    { domain: "ziprecruiter.com", label: "ZipRecruiter", cookies: 0, signIn: "unknown", signInCheckedAt: null, signInUrl: "https://www.ziprecruiter.com/authn/login", sourceCookies: null },
  ],
  lastCheckin: "2027-03-10T09:05:00Z",
  pending: null,
  lastOutcome: null,
  lastImport: null,
  undoAvailable: false,
  ...over,
});

const still = () => new Response(new Blob([new Uint8Array([0xff, 0xd8, 0xff])], { type: "image/jpeg" }), { status: 200 });

function stubBlobUrls() {
  const make = vi.fn(() => "blob:test/" + Math.random());
  const revoke = vi.fn();
  vi.stubGlobal("URL", Object.assign(class extends URL {}, { createObjectURL: make, revokeObjectURL: revoke }));
  return { make, revoke };
}

function baseApi(extra: Record<string, Handler> = {}) {
  return fakeApi({
    "GET /api/browser/status": () => ok(status()),
    "GET /api/browser/sessions": () => ok(sessions()),
    "GET /api/browser/lanes/apply/tabs/tab-1/screenshot": () => still(),
    "GET /api/browser/lanes/scan/tabs/tab-9/screenshot": () => still(),
    ...extra,
  });
}

describe("Browser windows", () => {
  it("shows the apply window through the server and reloads the status", async () => {
    stubBlobUrls();
    const api = baseApi({ "POST /api/browser/apply-window/show": () => ok({ shown: true }) });
    await mount(<Browser />);
    await waitFor(() => button("Show apply window"));
    const before = api.callsTo("GET /api/browser/status").length;
    await click(button("Show apply window"));
    expect(api.callsTo("POST /api/browser/apply-window/show")).toHaveLength(1);
    expect(api.callsTo("POST /api/browser/apply-window/show")[0].body).toEqual({});
    await waitFor(() => api.callsTo("GET /api/browser/status").length > before);
  });

  it("stops the browser only after confirmation, and offers to stop anyway when a run holds a tab", async () => {
    stubBlobUrls();
    let force = false;
    const api = baseApi({
      "POST /api/browser/stop": (c) => {
        force = !!(c.body as { force?: boolean }).force;
        return force ? ok({ outcome: "closed" }) : fail("conflict", "a run holds a browser tab", { details: { reason: "lane-leased" } });
      },
    });
    await mount(<Browser />);
    await click(await waitFor(() => button("Stop browser")));
    expect(api.callsTo("POST /api/browser/stop")).toHaveLength(0);
    expect(document.querySelector("dialog")?.textContent).toContain("Chrome closes with every tab");
    await click(document.querySelector<HTMLButtonElement>("dialog button.danger"));
    await waitFor(() => text().includes("a run holds a browser tab"));
    expect(api.callsTo("POST /api/browser/stop")[0].body).toEqual({});
    await click(button("Stop anyway"));
    expect(api.callsTo("POST /api/browser/stop")[1].body).toEqual({ force: true });
    await waitFor(() => text().includes("The browser stopped (closed)."));
  });
});

describe("Sign-ins", () => {
  it("shows every site the check reads as signed in, signed out or unknown, and opens its sign-in page in the browser panel", async () => {
    stubBlobUrls();
    const api = baseApi({ "POST /api/browser/lanes/apply/tabs": () => ok({ tabId: "tab-7" }) });
    await mount(<Browser />);
    await waitFor(() => byLabel(/Open LinkedIn sign-in/));
    const sec = document.querySelector("section[aria-labelledby='browser-signins']")!;
    expect(sec.textContent).toContain("LinkedIn");
    expect(sec.textContent).toContain("signed in");
    expect(sec.textContent).toContain("Indeed");
    expect(sec.textContent).toContain("signed out");
    expect(sec.textContent).toContain("Jobright");
    expect(sec.textContent).toContain("never checked");
    expect(sec.textContent).toContain("ZipRecruiter");
    await click(byLabel(/Open LinkedIn sign-in/));
    expect(api.callsTo("POST /api/browser/lanes/apply/tabs")[0].body).toEqual({ url: "https://www.linkedin.com/login" });
    expect(JSON.parse(window.localStorage.getItem("applysmith-browser-panel") ?? "{}").open).toBe(true);
  });

  it("refuses to open a site's page while a run drives the window and says so", async () => {
    stubBlobUrls();
    baseApi({ "POST /api/browser/lanes/apply/tabs": () => fail("conflict", "an apply run is driving the apply window; try again when it ends", { details: { reason: "apply-run-live" } }) });
    await mount(<Browser />);
    await click(await waitFor(() => byLabel(/Open Indeed sign-in/)));
    await waitFor(() => text().includes("an apply run is driving the apply window"));
  });
});

describe("Session sync", () => {
  it("asks for the sync of one site, waits while the request is pending and then shows the outcome and counts", async () => {
    stubBlobUrls();
    let state: "idle" | "pending" | "done" = "idle";
    const api = baseApi({
      "GET /api/browser/sessions": () =>
        ok(
          state === "pending"
            ? sessions({ pending: { requestId: "req-1", domains: ["linkedin.com"], expiresAt: "2027-03-10T09:07:00Z" } })
            : state === "done"
              ? sessions({
                  lastOutcome: { state: "succeeded", message: "The sign-in was copied", at: "2027-03-10T09:06:00Z" },
                  lastImport: { at: "2027-03-10T09:06:00Z", domains: ["linkedin.com"], result: { imported: 12, failed: 0, replaced: 4, notInSource: 0, truncated: false } },
                  undoAvailable: true,
                })
              : sessions(),
        ),
      "POST /api/browser/sessions/sync-requests": () => {
        state = "pending";
        return { data: { requestId: "req-1", domains: ["linkedin.com"], expiresAt: "2027-03-10T09:07:00Z" }, status: 202 };
      },
    });
    await mount(<Browser />);
    const sync = await waitFor(() => byLabel(/Sync LinkedIn/));
    // One status region is in the page before anything is said in it, so a screen reader announces what appears.
    const region = sync.closest("section")!.querySelector("[role=status]")!;
    await click(sync);
    expect(api.callsTo("POST /api/browser/sessions/sync-requests")[0].body).toEqual({ sites: ["linkedin.com"] });
    await waitFor(() => region.textContent?.includes("Waiting for Chrome to answer for linkedin.com"));
    expect(byLabel(/Sync LinkedIn/)!.disabled).toBe(true);
    state = "done";
    await waitFor(() => region.textContent?.includes("The sign-in was copied"), "the outcome");
    expect(region.isConnected).toBe(true);
    expect(text()).toContain("Imported 12, replaced 4, failed 0, not in your Chrome 0.");
    expect(button("Undo last sync (linkedin.com)")).not.toBeNull();
  });

  it("undoes the last sync only after confirmation", async () => {
    stubBlobUrls();
    const api = baseApi({
      "GET /api/browser/sessions": () => ok(sessions({ undoAvailable: true })),
      "POST /api/browser/sessions/undo": () => ok({ restored: 7, failed: 0 }),
    });
    await mount(<Browser />);
    await click(await waitFor(() => button("Undo last sync")));
    expect(api.callsTo("POST /api/browser/sessions/undo")).toHaveLength(0);
    await click(document.querySelector<HTMLButtonElement>("dialog button.danger"));
    await waitFor(() => text().includes("Restored 7 cookies."));
    expect(api.callsTo("POST /api/browser/sessions/undo")).toHaveLength(1);
  });

  it("shows the refusal of a sync while an apply run drives the window", async () => {
    stubBlobUrls();
    baseApi({ "POST /api/browser/sessions/sync-requests": () => fail("conflict", "an apply run is driving the apply window; try again when it ends", { details: { reason: "apply-run-live" } }) });
    await mount(<Browser />);
    await click(await waitFor(() => byLabel(/Sync LinkedIn/)));
    await waitFor(() => text().includes("an apply run is driving the apply window"));
  });
});

describe("Browser panel helpers", () => {
  it("reads an address or a host name and shortens it at rest", () => {
    expect(toAddress("jobs.example.com/apply?x=1")).toBe("https://jobs.example.com/apply?x=1");
    expect(toAddress(" http://localhost.test:8080 ")).toBe("http://localhost.test:8080");
    expect(toAddress("javascript:alert(1)")).toBeNull();
    expect(toAddress("find me a job")).toBeNull();
    expect(shortAddress("https://www.jobs.example.com/")).toBe("jobs.example.com");
    expect(shortAddress("about:blank")).toBe("");
    expect(shortAddress("https://jobs.example.com/apply/?token=abc#step")).toBe("jobs.example.com/apply");
  });
});

// sse builds a live stream body that stays open after its events.
const sse = (events: [string, unknown][]) => sseResponse(events.map(([name, data]) => `event: ${name}\ndata: ${JSON.stringify(data)}\n\n`));

const frame = { jpeg: "/9j/AA==", width: 1280, height: 800, offsetTop: 0, scale: 1 };
const liveTab = (over: object = {}) => ({ url: "https://jobs.example.test/apply?step=2", title: "Apply to Example", loading: false, canGoBack: true, canGoForward: false, driven: false, ...over });

function panelApi(tabs: object[], live: () => Response | Promise<Response>, extra: Record<string, Handler> = {}) {
  return fakeApi({
    "GET /api/browser/status": () => ok(status({ apply: { window: true, tabCap: 24, tabs } })),
    "GET /api/browser/lanes/apply/tabs/tab-1/live": live,
    "POST /api/browser/lanes/apply/tabs/tab-1/input": () => ok({ sent: true }),
    "POST /api/browser/lanes/apply/tabs/tab-1/navigate": () => ok({ sent: true }),
    "POST /api/browser/apply-window/show": () => ok({ shown: true }),
    ...extra,
  });
}

async function openPanel() {
  setPanel({ open: true, tabId: null });
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({ left: 0, top: 0, width: 640, height: 400, right: 640, bottom: 400, x: 0, y: 0, toJSON: () => ({}) });
  await mount(<BrowserPanel />);
}

const mouseAt = (type: string, x: number, y: number, init: MouseEventInit = {}) =>
  act(async () => {
    q(".bp-view")!.dispatchEvent(new MouseEvent(type, { bubbles: true, cancelable: true, clientX: x, clientY: y, button: 0, buttons: type === "mouseup" ? 0 : 1, detail: 1, ...init }));
  });

const sentEvents = (api: ReturnType<typeof fakeApi>) => api.callsTo("POST /api/browser/lanes/apply/tabs/tab-1/input").flatMap((c) => (c.body as { events: Record<string, unknown>[] }).events).filter((e) => e.kind !== "fit");

describe("Browser panel", () => {
  it("streams the tab: connecting, then the frame; a click activates it and only then input reaches the page", async () => {
    let answer: (r: Response) => void = () => {};
    const api = panelApi([tab()], () => new Promise<Response>((r) => (answer = r)));
    await openPanel();
    await waitFor(() => text().includes("Connecting to the tab"));
    answer(sse([["tab", liveTab()], ["frame", frame]]));
    await waitFor(() => q<HTMLImageElement>("img.bp-frame")?.src.startsWith("data:image/jpeg;base64,"));
    expect(q(".bp-view")!.getAttribute("aria-label")).toBe("Live browser: Apply to Example");
    await mouseAt("mousedown", 320, 100);
    expect(sentEvents(api)).toHaveLength(0);
    await click(button("Click anywhere to use the browser"));
    const ime = await waitFor(() => q<HTMLTextAreaElement>("textarea.bp-ime"));
    expect(document.activeElement).toBe(ime);
    // 640 by 400 shows the 1280 by 800 page at half size.
    await mouseAt("mousedown", 320, 100);
    await mouseAt("mouseup", 320, 100);
    await press("Enter", { code: "Enter", keyCode: 13 }, ime);
    await waitFor(() => sentEvents(api).some((e) => e.kind === "key"));
    const evs = sentEvents(api);
    expect(evs[0]).toMatchObject({ kind: "mouse", type: "down", x: 640, y: 200, button: "left", clickCount: 1 });
    expect(evs.find((e) => e.kind === "key")).toMatchObject({ type: "down", key: "Enter", text: "\r" });
    // The user's tab is fitted to the panel's 640 by 400.
    await waitFor(() => api.callsTo("POST /api/browser/lanes/apply/tabs/tab-1/input").some((c) => JSON.stringify(c.body).includes('{"kind":"fit","x":640,"y":400}')));
    // A dashboard shortcut typed into the active viewport is the page's key.
    const heard = vi.fn();
    window.addEventListener("keydown", heard);
    await press("B", { metaKey: true, shiftKey: true, code: "KeyB", keyCode: 66 }, ime);
    window.removeEventListener("keydown", heard);
    expect(heard).not.toHaveBeenCalled();
    await waitFor(() => sentEvents(api).some((e) => e.key === "B" && e.modifiers === 12));
    await press("Escape", {}, ime);
    expect(q("textarea.bp-ime")).toBeNull();
    expect(button("Click anywhere to use the browser")).not.toBeNull();
  });

  it("sends one commit exactly once in the orders WebKit and Chrome fire, and a lost compositionend holds nothing back", async () => {
    const api = panelApi([tab()], () => sse([["tab", liveTab()], ["frame", frame]]));
    await openPanel();
    await click(await waitFor(() => button("Click anywhere to use the browser")));
    const ime = await waitFor(() => q<HTMLTextAreaElement>("textarea.bp-ime"));
    const fire = (ev: Event) => act(async () => void ime.dispatchEvent(ev));
    const start = () => fire(new CompositionEvent("compositionstart", { bubbles: true }));
    // happy-dom drops a CompositionEvent's data, so it is set on the event.
    const end = (data = "") => fire(Object.defineProperty(new CompositionEvent("compositionend", { bubbles: true }), "data", { value: data }));
    const tick = () => act(() => new Promise((r) => setTimeout(r, 5)));
    const up = (isComposing: boolean, keyCode = 78) => fire(new KeyboardEvent("keyup", { bubbles: true, key: "n", keyCode, isComposing }));
    // A browser sets the field between beforeinput and input, in one task.
    const edit = (value: string, inputType: string, isComposing: boolean) =>
      act(async () => {
        ime.dispatchEvent(new InputEvent("beforeinput", { bubbles: true, cancelable: true, inputType, isComposing }));
        ime.value = value;
        ime.dispatchEvent(new InputEvent("input", { bubbles: true, inputType, isComposing }));
      });
    const orders: Record<string, () => Promise<void>> = {
      // Chrome: the committed text is the last composition update.
      chrome: async () => (await start(), await edit("ni", "insertCompositionText", true), await up(true), await edit("\u4f60", "insertCompositionText", true), await end("\u4f60"), await up(false)),
      // WebKit: compositionend while the field holds the marked text, then the
      // commit, which may come a task later.
      webkit: async () => (await start(), await edit("ni", "insertCompositionText", true), await up(true), await end("\u4f60"), await tick(), await edit("\u4f60", "insertFromComposition", false), await up(false)),
      // WKWebView committing with Return: the commit is still composing, and
      // compositionend carries no data.
      return: async () => (await start(), await edit("\u4f60", "insertCompositionText", true), await edit("\u4f60", "insertFromComposition", true), await end(), await up(false, 13)),
      // compositionend never comes; the next input or key up ends it.
      lostEnd: async () => (await start(), await edit("ni", "insertCompositionText", true), await edit("\u4f60", "insertText", false), await up(false)),
      lostEndKeyUp: async () => (await start(), await edit("\u4f60", "insertCompositionText", true), await up(false)),
      // A digit typed through the input method: its key down is 229.
      typed: async () => (await fire(new KeyboardEvent("keydown", { bubbles: true, key: "Process", keyCode: 229 })), await edit("\u4f60", "insertText", false), await up(false)),
      pasted: async () => edit("\u4f60", "insertFromPaste", false),
    };
    for (const [name, run] of Object.entries(orders)) {
      const before = sentEvents(api).length;
      await run();
      await act(() => new Promise((r) => setTimeout(r, 20)));
      expect([name, sentEvents(api).slice(before)]).toEqual([name, [{ kind: "text", text: "\u4f60" }]]);
    }
    // A composition the field lost with its blur does not stop the next one.
    await start();
    await fire(new FocusEvent("blur"));
    await edit("1", "insertText", false);
    await waitFor(() => sentEvents(api).at(-1)?.text === "1");
  });

  it("only watches a tab a run drives: a glowing view, the run's island and link, no input and no toolbar actions", async () => {
    const api = panelApi([tab({ owner: "run_b", kept: false })], () => sse([["tab", liveTab({ driven: true, runId: "run_b" })], ["frame", frame], ["cursor", { x: 640, y: 400 }]]));
    await openPanel();
    const island = await waitFor(() => q(".bp-island"));
    expect(island.textContent).toContain("Applysmith is using this tab");
    expect(island.querySelector("a")!.getAttribute("href")).toBe("/runs/run_b");
    expect(q(".bp-view")!.className).toContain("driven");
    expect(button("Click anywhere to use the browser")).toBeNull();
    await mouseAt("mousedown", 100, 100);
    await waitFor(() => q<HTMLElement>(".bp-cursor"));
    expect(q<HTMLElement>(".bp-cursor")!.style.transform).toBe("translate(320px, 200px)");
    expect(byLabel(/^Back$/)!.disabled).toBe(true);
    expect(byLabel(/^Reload page$/)!.disabled).toBe(true);
    expect(q<HTMLInputElement>("input[aria-label=Address]")!.disabled).toBe(true);
    await new Promise((r) => setTimeout(r, 350));
    expect(api.callsTo("POST /api/browser/lanes/apply/tabs/tab-1/input")).toHaveLength(0);
  });

  it("edits the address: the full address while editing, Enter goes, Escape puts it back", async () => {
    const api = panelApi([tab()], () => sse([["tab", liveTab()], ["frame", frame]]));
    await openPanel();
    const field = await waitFor(() => (q<HTMLInputElement>("input[aria-label=Address]")?.value === "jobs.example.test/apply" ? q<HTMLInputElement>("input[aria-label=Address]") : null));
    await act(async () => field.focus());
    expect(field.value).toBe("https://jobs.example.test/apply?step=2");
    await type(field, "careers.example.org/jobs");
    await act(async () => void field.form!.requestSubmit());
    await waitFor(() => api.callsTo("POST /api/browser/lanes/apply/tabs/tab-1/navigate").length === 1);
    expect(api.callsTo("POST /api/browser/lanes/apply/tabs/tab-1/navigate")[0].body).toEqual({ action: "go", url: "https://careers.example.org/jobs" });
    await act(async () => field.focus());
    await type(field, "something else");
    await press("Escape", {}, field);
    expect(field.value).toBe("jobs.example.test/apply");
    await click(byLabel(/^Back$/));
    expect(api.callsTo("POST /api/browser/lanes/apply/tabs/tab-1/navigate")[1].body).toEqual({ action: "back" });
  });
});
