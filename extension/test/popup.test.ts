import { beforeEach, describe, expect, it, vi } from "vitest";
import { DEFAULT_PREFS, type StateView } from "../src/shared/messages";
import { renderPopup, type PageInfo, type PopupActions } from "../src/popup/view";

const base: StateView = {
  connection: "connected",
  address: "http://127.0.0.1:47319",
  extension: { stamp: "3f9a0c27d41e8b65", version: "0.1.0" },
  prefs: { ...DEFAULT_PREFS },
  managed: false,
};
const page: PageInfo = { diskStamp: "3f9a0c27d41e8b65", host: "jobs.example.com", supported: true, off: false };

function actions(): PopupActions {
  return {
    claim: vi.fn(async () => {}),
    token: vi.fn(async () => {}),
    forget: vi.fn(async () => {}),
    address: vi.fn(async () => {}),
    prefs: vi.fn(async () => {}),
    togglePanel: vi.fn(async () => {}),
    setOff: vi.fn(async () => {}),
    syncSite: vi.fn(async () => "Imported 2, failed 0."),
    recheck: vi.fn(async () => {}),
    reload: vi.fn(),
  };
}

let root: HTMLElement;
beforeEach(() => {
  document.body.innerHTML = '<div id="app"></div>';
  root = document.getElementById("app")!;
});

describe("popup states", () => {
  it("asks before it forgets the pairing, and Keep pairing changes nothing", async () => {
    const act = actions();
    renderPopup(root, base, page, act);
    const find = (label: string) => [...root.querySelectorAll("button")].find((b) => b.textContent === label);
    find("Forget pairing")!.click();
    expect(act.forget).not.toHaveBeenCalled();
    expect(root.textContent).toContain("Forget this pairing?");
    expect(document.activeElement).toBe(find("Keep pairing"));
    find("Keep pairing")!.click();
    expect(root.textContent).not.toContain("Forget this pairing?");
    expect(document.activeElement).toBe(find("Forget pairing"));
    find("Forget pairing")!.click();
    find("Forget")!.click();
    await new Promise((r) => setTimeout(r, 0));
    expect(act.forget).toHaveBeenCalledTimes(1);
  });

  it("submitting a code calls the worker and shows the server's refusal plainly", async () => {
    const act = actions();
    act.claim = vi.fn(async () => {
      throw { failure: { code: "unauthenticated", message: "x" } };
    });
    renderPopup(root, { ...base, connection: "unpaired" }, page, act);
    (root.querySelector("#code") as HTMLInputElement).value = "ABCDEFGHJK";
    [...root.querySelectorAll("button")].find((b) => b.textContent === "Pair")!.click();
    await new Promise((r) => setTimeout(r, 0));
    expect(act.claim).toHaveBeenCalledWith("ABCDEFGHJK");
    expect(root.querySelector(".notice")!.textContent).toMatch(/wrong, expired or already used/);
  });

  it("a pairing on its way is claimed once however often Pair is pressed, and a refusal frees Pair", async () => {
    const act = actions();
    let refuse!: (e: unknown) => void;
    act.claim = vi.fn(() => new Promise<void>((_, no) => (refuse = no)));
    renderPopup(root, { ...base, connection: "unpaired" }, page, act);
    (root.querySelector("#code") as HTMLInputElement).value = "ABCDEFGHJK";
    const f = root.querySelector("#code")!.closest("form")!;
    const pair = f.querySelector<HTMLButtonElement>("button[type=submit]")!;
    // A double click, or Enter after the click: the code works once.
    f.dispatchEvent(new Event("submit", { cancelable: true }));
    f.dispatchEvent(new Event("submit", { cancelable: true }));
    expect(act.claim).toHaveBeenCalledTimes(1);
    expect(pair.disabled).toBe(true);
    refuse({ failure: { code: "unauthenticated", message: "x" } });
    await vi.waitFor(() => expect(pair.disabled).toBe(false));
    expect(root.querySelector(".notice")!.textContent).toMatch(/wrong, expired or already used/);
  });

  // The app writes each new build into the folder it keeps, so after an update
  // the files on disk are the build it expects and Reload is the whole fix.
  // Loaded from elsewhere, the build is where Settings, Browser extension says
  // (the kept folder in the app, the export command in a browser): the popup
  // cannot tell those readers apart, so it names no command itself.
});
