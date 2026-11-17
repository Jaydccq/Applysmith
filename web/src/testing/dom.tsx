// Minimal rendering helpers over react-dom and happy-dom. They exist so the
// component tests need no extra dependency.
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let current: { root: Root; host: HTMLElement } | null = null;

export async function mount(ui: ReactNode): Promise<HTMLElement> {
  unmount();
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  current = { root, host };
  await act(async () => root.render(ui));
  await settle();
  return host;
}

export function unmount(): void {
  if (!current) return;
  const { root, host } = current;
  act(() => root.unmount());
  host.remove();
  current = null;
  document.body.innerHTML = "";
}

// settle lets pending promises and effects finish.
export async function settle(times = 3): Promise<void> {
  for (let i = 0; i < times; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
}

// realNow is the clock as loaded, so a test that fakes Date or performance
// cannot stop waitFor's deadline from coming.
const realNow = performance.now.bind(performance);

// waitFor polls for at least 60 turns and up to 3 seconds, so a lazily loaded
// page that takes longer under a loaded test run still counts.
export async function waitFor<T>(check: () => T | null | undefined | false, what = "condition"): Promise<T> {
  const deadline = realNow() + 3000;
  for (let i = 0; i < 60 || realNow() < deadline; i++) {
    const v = check();
    if (v) return v;
    await settle(1);
  }
  throw new Error(`timed out waiting for ${what}\n${document.body.innerHTML.slice(0, 1500)}`);
}

export const q = <T extends Element = HTMLElement>(sel: string): T | null => document.querySelector<T>(sel);
export const qa = <T extends Element = HTMLElement>(sel: string): T[] => Array.from(document.querySelectorAll<T>(sel));

export function byText<T extends HTMLElement = HTMLElement>(sel: string, text: string | RegExp): T | null {
  return qa<T>(sel).find((e) => (typeof text === "string" ? e.textContent?.trim() === text : text.test(e.textContent ?? ""))) ?? null;
}

export const button = (text: string | RegExp) => byText<HTMLButtonElement>("button", text);

export async function click(el: Element | null): Promise<void> {
  if (!el) throw new Error("click target is missing");
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 }));
  });
  await settle();
}

// type sets a controlled input or select the way a user's edit does.
export async function type(el: HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement | null, value: string): Promise<void> {
  if (!el) throw new Error("input is missing");
  const proto = el instanceof HTMLSelectElement ? HTMLSelectElement.prototype : el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  await act(async () => {
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event(el instanceof HTMLSelectElement ? "change" : "input", { bubbles: true }));
  });
}

export async function submit(form: Element | null): Promise<void> {
  if (!form) throw new Error("form is missing");
  await act(async () => {
    form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
  });
  await settle();
}

export async function press(key: string, init: KeyboardEventInit = {}, target: EventTarget = window): Promise<void> {
  await act(async () => {
    target.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init }));
  });
  await settle();
}

export function text(): string {
  return document.body.textContent ?? "";
}

export const byLabel = (label: RegExp) => qa<HTMLButtonElement>("button").find((b) => label.test(b.getAttribute("aria-label") ?? "")) ?? null;

// openMore opens the "More" menu of the row whose company name starts with who.
export async function openMore(who: string): Promise<void> {
  const trigger = await waitFor(() => qa<HTMLButtonElement>('button[aria-haspopup="menu"]').find((b) => b.getAttribute("aria-label")?.startsWith(`More actions: ${who}`)), `the More menu of ${who}`);
  await click(trigger);
}
