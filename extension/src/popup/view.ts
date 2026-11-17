// The popup, drawn from the worker's state. Pure DOM so it can be tested.
import { h } from "../shared/dom";
import type { Failure, StateView } from "../shared/messages";

export interface PageInfo {
  /** The build stamp of the files on disk, which the popup was loaded from. */
  diskStamp: string;
  host: string;
  supported: boolean;
  off: boolean;
}

export interface PopupActions {
  claim(code: string): Promise<void>;
  token(token: string): Promise<void>;
  forget(): Promise<void>;
  address(value: string): Promise<void>;
  prefs(patch: Partial<StateView["prefs"]>): Promise<void>;
  togglePanel(): Promise<void>;
  setOff(off: boolean): Promise<void>;
  syncSite(): Promise<string>;
  /** Reads the connection again and redraws. */
  recheck(): Promise<void>;
  reload(): void;
}

/** What the user reads for a failure; the server's own text is used when it explains. */
function explain(f: Failure, context: "claim" | "other" = "other"): string {
  if (f.code === "rate_limited") return `Too many tries. Try again in ${f.retryAfter ?? 60} s.`;
  if (context === "claim" && f.code === "unauthenticated") return "That code is wrong, expired or already used. Ask the dashboard for a new one.";
  if (f.code === "offline") return f.message;
  return f.message;
}

type Child = Node | string | null | false | undefined;
/** Runs an action and shows its failure; the result says whether it succeeded. */
type Run = (fn: () => Promise<unknown>, context?: "claim" | "other") => () => Promise<boolean>;

function row(label: string, value: string): HTMLElement {
  return h("div", { class: "row" }, h("span", { class: "k" }, label), h("span", { class: "v" }, value));
}

/** A switch whose save failed goes back, so it never shows a setting that is not in force. */
function switchRow(label: string, checked: boolean, save: (v: boolean) => Promise<boolean>): HTMLElement {
  const input = h("input", { type: "checkbox", checked, onchange: async () => {
    if (!(await save(input.checked))) input.checked = !input.checked;
  } });
  input.checked = checked;
  return h("label", { class: "switch" }, input, h("span", {}, label));
}

function button(label: string, onClick: () => void, attrs: Record<string, unknown> = {}): HTMLButtonElement {
  return h("button", { type: "button", onclick: onClick, ...attrs }, label);
}

/**
 * A form whose submit (the button or Enter in a field) runs `fn`; the page never navigates. While
 * `fn` runs its button is held and says so, and a second submit sends nothing (a pairing code works once).
 */
function form(fn: () => Promise<unknown>, ...kids: Child[]): HTMLFormElement {
  let busy = false;
  const el = h("form", { onsubmit: async (e: Event) => {
    e.preventDefault();
    if (busy) return;
    busy = true;
    const btn = el.querySelector<HTMLButtonElement>("button[type=submit]");
    const [label, disabled] = [btn?.textContent ?? "", btn?.disabled ?? false];
    if (btn) [btn.disabled, btn.textContent] = [true, btn.dataset.busy ?? label];
    try {
      await fn();
    } finally {
      busy = false;
      if (btn) [btn.disabled, btn.textContent] = [disabled, label];
    }
  } }, ...kids);
  return el;
}

function submitButton(label: string, attrs: Record<string, unknown> = {}): HTMLButtonElement {
  return h("button", { type: "submit", ...attrs }, label);
}

const PILL = { connected: "Connected", unpaired: "Not paired", offline: "Offline" } as const;

export function renderPopup(root: HTMLElement, state: StateView, page: PageInfo, act: PopupActions, notice = ""): void {
  const say = h("p", { class: "notice", role: "status" }, notice);
  const run: Run = (fn, context = "other") => async () => {
    try {
      await fn();
      return true;
    } catch (e) {
      const f = (e as { failure?: Failure }).failure ?? { code: "internal", message: "Something went wrong." };
      say.textContent = explain(f, context);
      return false;
    }
  };
  const nodes: Child[] = [
    h("header", {}, h("h1", {}, "Applysmith"), h("span", { class: `pill ${state.connection}` }, PILL[state.connection])),
    say,
  ];

  if (state.managed) {
    nodes.push(h("p", { class: "notice" }, "This is the Applysmith browser. The extension does not sync sign-ins from here."));
  }

  if (state.connection === "offline") {
    nodes.push(
      h("p", {}, `Cannot reach the Applysmith application at ${state.address}. Open the Applysmith application, then press Check again.`),
      button("Check again", run(act.recheck)),
      h("p", {}, "If it is open, it moved to another address: pair again with a new code, which carries the new address."),
      pairForm(state, act, run),
      addressForm(state, act, run),
    );
  } else if (state.connection === "unpaired") {
    nodes.push(pairForm(state, act, run), addressForm(state, act, run));
  } else {
    nodes.push(connected(state, page, act, run));
  }
  root.replaceChildren(...nodes.filter((n): n is Node | string => Boolean(n)));
}

function pairForm(state: StateView, act: PopupActions, run: Run): HTMLElement {
  const code = h("input", { id: "code", autocomplete: "off", spellcheck: "false", maxlength: "24", placeholder: "Pairing code", "aria-label": "Pairing code" });
  const token = h("input", { id: "token", type: "password", autocomplete: "off", placeholder: "Token from applysmith pair", "aria-label": "Token" });
  const wait = state.claimWait ? h("p", { class: "notice" }, `Too many tries. Try again in ${state.claimWait} s.`) : null;
  return h(
    "section",
    {},
    h("h2", {}, "Pair with the application"),
    h("p", {}, "In the Applysmith dashboard, open Settings, Browser extension and press Pair the extension. Type the code it shows here; it also tells the extension where the application is, works once and lasts two minutes."),
    form(run(() => act.claim(code.value), "claim"), code, submitButton("Pair", { disabled: Boolean(state.claimWait), "data-busy": "Pairing..." })),
    wait,
    h(
      "details",
      {},
      h("summary", {}, "Use a token instead"),
      h("p", {}, "Run applysmith pair in a terminal and paste what it prints."),
      form(run(() => act.token(token.value)), token, submitButton("Use token")),
    ),
  );
}

function addressForm(state: StateView, act: PopupActions, run: Run, open = false): HTMLElement {
  const input = h("input", { id: "address", value: state.address, "aria-label": "Application address", spellcheck: "false" });
  input.value = state.address;
  return h("details", { open }, h("summary", {}, "Application address"), form(run(() => act.address(input.value)), input, submitButton("Save")));
}

function connected(state: StateView, page: PageInfo, act: PopupActions, run: Run): HTMLElement {
  const out: Child[] = [];
  out.push(row("Extension build", `${state.extension.version} ${state.extension.stamp}`));
  if (state.application) {
    out.push(row("Application expects", `${state.application.version} ${state.application.stamp}`));
    if (state.application.stamp !== state.extension.stamp) {
      // The app writes each new build into the folder it keeps, so once the
      // files on disk are the expected build a reload is the whole fix. Else
      // the dashboard's section says where that build is (the kept folder, or
      // in a browser the export command); the popup cannot tell which reader
      // it has, so it names no command itself.
      const next =
        page.diskStamp === state.application.stamp
          ? "The files on disk are already that build: press Reload extension."
          : "In the Applysmith dashboard, Settings, Browser extension says where that build is and how to load it.";
      out.push(
        h("p", { class: "warn", role: "alert" }, `This extension is build ${state.extension.stamp} but the application expects build ${state.application.stamp}. ${next}`),
        button("Reload extension", act.reload),
      );
    }
  }
  if (page.diskStamp !== state.extension.stamp) {
    out.push(
      h("p", { class: "warn", role: "alert" }, `The running extension is build ${state.extension.stamp} but the files on disk are build ${page.diskStamp}.`),
      button("Reload extension", act.reload),
    );
  }
  out.push(row("This page", page.supported ? `Supported (${page.host})` : "Not supported (not a web page)"));
  out.push(
    button("Open or close panel", run(act.togglePanel), { disabled: !page.supported }),
    page.supported && button(page.off ? `Turn on for ${page.host}` : `Turn off on ${page.host}`, run(() => act.setOff(!page.off))),
    switchRow("Edit with AI", state.prefs.editWithAI, (v) => run(() => act.prefs({ editWithAI: v }))()),
    switchRow("Open the panel by itself on supported sites", state.prefs.autoOpen, (v) => run(() => act.prefs({ autoOpen: v }))()),
    switchRow("Answer sign-in sync requests", state.prefs.answerSync, (v) => run(() => act.prefs({ answerSync: v }))()),
  );
  const syncNote = h("p", { class: "notice", role: "status" });
  out.push(
    page.supported && button("Sync this site's sign-in to the apply window", run(async () => { syncNote.textContent = await act.syncSite(); }), { disabled: state.managed }),
    syncNote,
    h("a", { href: `${state.address}/`, target: "_blank", rel: "noopener" }, "Open Applysmith"),
    forgetPairing(act, run),
    addressForm(state, act, run),
  );
  return h("section", {}, ...out);
}

/** Forget pairing asks first: undoing it takes a new code from the dashboard. */
function forgetPairing(act: PopupActions, run: Run): HTMLElement {
  const box = h("div", {});
  const ask = button("Forget pairing", () => {
    const yes = button("Forget", run(act.forget));
    const no = button("Keep pairing", () => (box.replaceChildren(ask), ask.focus()));
    box.replaceChildren(h("p", { class: "warn", role: "alert" }, "Forget this pairing? The extension stops filling until you pair it again with a new code from the dashboard."), yes, no);
    no.focus();
  });
  box.append(ask);
  return box;
}
