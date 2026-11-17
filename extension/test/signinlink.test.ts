import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { WINDOW_MS, openAuthorizationWindow } from "../src/widgets/otp";
import { linkSentPage, startSigninLink, type LinkMail, type SigninLink } from "../src/widgets/signinlink";
import { watchPage } from "../src/widgets/main";

const roots: ShadowRoot[] = [];
const realAttach = Element.prototype.attachShadow;
let widget: SigninLink | undefined;
let latest: ReturnType<typeof vi.fn>;
let open: ReturnType<typeof vi.fn>;

const layers = () => roots.filter((r) => r.host.isConnected);
const card = () => layers().map((r) => r.querySelector<HTMLElement>(".card")).find(Boolean);
const buttonLabelled = (text: string) => [...(card()?.querySelectorAll("button") ?? [])].find((b) => b.textContent === text);
const SECRET_URL = "https://auth.example.com/magic?token=FAKE_Zx9Yw8Vv7Uu6Tt5Ss4Rr3";
const mail = (): LinkMail => ({ url: SECRET_URL, host: "auth.example.com", receivedAt: "2026-10-11T09:30:00.000Z", from: "no-reply@auth.example.com" });

function start() {
  const win = Object.assign(Object.create(window), { open }) as Window;
  widget = startSigninLink({ doc: document, win, changes: watchPage(document), latest: latest as never, now: () => 1_000_000 });
}

beforeEach(() => {
  roots.length = 0;
  Element.prototype.attachShadow = function (init: ShadowRootInit) {
    const r = realAttach.call(this, { ...init, mode: "open" });
    roots.push(r);
    return r;
  };
  latest = vi.fn(async () => mail());
  open = vi.fn();
  document.body.innerHTML = `<p>We have sent a sign-in link to your email address.</p>`;
});
afterEach(() => {
  widget?.destroy();
  widget = undefined;
  Element.prototype.attachShadow = realAttach;
  openAuthorizationWindow(-WINDOW_MS - 1);
});

describe("sign-in link card", () => {
  it("reads mail only after a click and never opens the link on its own", async () => {
    start();
    await new Promise((r) => setTimeout(r, 30));
    expect(card()!.textContent).toMatch(/link was emailed/);
    expect(latest).not.toHaveBeenCalled();
    buttonLabelled("Show the link")!.click();
    await vi.waitFor(() => expect(buttonLabelled("Open in a new tab")).toBeDefined());
    expect(open).not.toHaveBeenCalled();
  });

  it("shows the host and never the whole address, and opens a new tab only on the person's second click", async () => {
    start();
    buttonLabelled("Show the link")!.click();
    await vi.waitFor(() => expect(buttonLabelled("Open in a new tab")).toBeDefined());
    expect(card()!.textContent).toContain("auth.example.com");
    expect(layers().map((r) => r.innerHTML).join("")).not.toContain("Zx9Yw8Vv7Uu6Tt5Ss4Rr3");
    buttonLabelled("Open in a new tab")!.click();
    expect(open).toHaveBeenCalledWith(SECRET_URL, "_blank", "noopener,noreferrer");
  });

  it("reads the link without a second click inside the window a sign-in run opened, and still does not open it", async () => {
    openAuthorizationWindow(1_000_000);
    start();
    await vi.waitFor(() => expect(buttonLabelled("Open in a new tab")).toBeDefined());
    expect(latest).toHaveBeenCalledTimes(1);
    expect(open).not.toHaveBeenCalled();
  });

  it("reads nothing on its own once the window has closed", async () => {
    openAuthorizationWindow(1_000_000 - WINDOW_MS - 1);
    start();
    await new Promise((r) => setTimeout(r, 30));
    expect(latest).not.toHaveBeenCalled();
  });

  it("shows nothing on a page that asks for a password or does not mention a sent link", () => {
    document.body.innerHTML = `<p>We have sent a sign-in link.</p><input type="password">`;
    expect(linkSentPage(document)).toBe(false);
    document.body.innerHTML = `<p>Welcome back</p>`;
    expect(linkSentPage(document)).toBe(false);
  });
});
