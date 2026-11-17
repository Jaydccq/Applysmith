// The Answer tab: the questions found on the page and, for each free-text one,
// a card that drafts an answer, shows it, and writes it only when the person
// presses Fill.
import { scanControls, type ControlInfo } from "../../../engine/src/scan";
import { h, redraw } from "../shared/dom";
import { pageJob } from "../shared/capture";
import type { ComposePosting, ComposeView, Connection, FillView, StateView } from "../shared/messages";
import { failureOf, send } from "../shared/rpc";
import { showButton } from "./autofill";
import { notConnected, SHOWN } from "./shell";
import { markPending, onPendingChange, onPendingCleared, pendingCount, settle } from "../widgets/drafts";
import { isFreeTextControl, lengthLimit } from "../widgets/freetext";
import { limitAtSentence, writeControl } from "../widgets/write";

const MAX_DESCRIPTION = 20_000;

type CardStatus = "idle" | "requesting" | "preview" | "filled" | "skipped" | "refused" | "error";

interface Card {
  key: string;
  el: HTMLElement;
  question: string;
  limit?: number;
  status: CardStatus;
  draft?: string;
  message?: string;
  open: boolean;
  instruction: string;
}

export interface AnswerTab {
  setConnection(c: Connection, checked?: boolean): void;
  dispose(): void;
}

const wordCount = (text: string): number => (text.trim() ? text.trim().split(/\s+/).length : 0);

export function mountAnswer(el: HTMLElement, doc: Document = document): AnswerTab {
  let connection: Connection = "offline";
  let checked = false;
  const cards = new Map<string, Card>();
  let infos: ControlInfo[] = [];
  let lanes = new Map<string, string>();
  let context: Promise<ComposePosting> | undefined;
  let scanned = false;
  // The application's address, for a question to add to the answer bank in the dashboard.
  let address = "";

  const posting = (): Promise<ComposePosting> =>
    (context ??= pageJob(doc).then((c) => ({ company: c.company, role: c.title, description: c.jdText?.slice(0, MAX_DESCRIPTION), applicationUrl: doc.location.href })));

  const stopWatching = [
    onPendingChange(() => draw()),
    // "Skip the pending fields" in the submit guard skips every draft that is waiting.
    onPendingCleared(() => {
      for (const card of cards.values()) {
        if (card.status === "preview" || card.status === "requesting") {
          if (card.status === "requesting") void send({ kind: "compose.cancel", key: card.key }).catch(() => {});
          card.status = "skipped";
        }
      }
    }),
  ];

  /** Takes a card out of the tab: its request is cancelled and its draft no longer holds a submit. */
  function drop(card: Card): void {
    if (card.status === "requesting") void send({ kind: "compose.cancel", key: card.key }).catch(() => {});
    card.status = "skipped";
    settle(card.key);
    cards.delete(card.key);
  }

  async function scan(): Promise<void> {
    infos = scanControls(doc).filter((i) => i.sensitivity === null && i.kind !== "file" && i.label.text !== "");
    scanned = true;
    const seen = new Set<string>();
    for (const info of infos) {
      if (!isFreeTextControl(info.el, true)) continue;
      seen.add(info.selector);
      const known = cards.get(info.selector);
      // A control that now asks another question gets a new card.
      if (known && known.question === info.label.text) {
        known.el = info.el;
        known.limit = lengthLimit(info.el);
        continue;
      }
      if (known) drop(known);
      cards.set(info.selector, { key: info.selector, el: info.el, question: info.label.text, limit: lengthLimit(info.el), status: "idle", open: true, instruction: "" });
    }
    for (const card of [...cards.values()]) if (!seen.has(card.key)) drop(card);
    address ||= await send<StateView>({ kind: "state" }).then((v) => v.address).catch(() => "");
    try {
      const view = await send<FillView>({ kind: "fill.status" });
      lanes = new Map((view.rows ?? []).filter((r) => r.lane && ["filled", "prefilled"].includes(r.outcome)).map((r) => [r.label, r.lane!]));
    } catch {
      lanes = new Map();
    }
  }

  function stateOf(info: ControlInfo): string {
    const card = cards.get(info.selector);
    if (card?.status === "skipped") return "skipped";
    if (card?.status === "filled") return "answered";
    if (card && (card.status === "preview" || card.status === "requesting")) return "draft";
    if (lanes.get(info.label.text) === "bank") return "answered from the bank";
    if (info.answered) return "answered";
    return "needs you";
  }

  async function ask(card: Card, rewrite: boolean): Promise<void> {
    if (card.status === "requesting") return;
    const instruction = card.instruction.trim();
    card.status = "requesting";
    card.message = undefined;
    markPending(card.key);
    draw();
    try {
      const out = await send<ComposeView>({
        kind: "compose.run",
        key: card.key,
        question: card.question,
        maxLength: card.limit,
        posting: await posting(),
        ...(rewrite ? { current: card.draft, instruction } : {}),
      });
      if (card.status !== "requesting") return;
      if (out.outcome === "filled-model" && out.answer) {
        card.draft = out.answer;
        card.status = "preview";
        card.open = true;
        card.instruction = "";
      } else {
        card.status = rewrite && card.draft ? "preview" : "refused";
        card.message = "No grounded answer: nothing in your profile or answer bank supports one. Nothing was written.";
        if (card.status === "refused") settle(card.key);
      }
    } catch (e) {
      if (card.status !== "requesting") return;
      card.status = card.draft ? "preview" : "error";
      card.message = failureOf(e).message;
      if (card.status === "error") settle(card.key);
    }
    draw();
  }

  async function cancel(card: Card): Promise<void> {
    await send({ kind: "compose.cancel", key: card.key }).catch(() => {});
    card.status = card.draft ? "preview" : "idle";
    if (!card.draft) settle(card.key);
    draw();
  }

  async function fill(card: Card): Promise<void> {
    if (!card.draft) return;
    const text = card.limit ? limitAtSentence(card.draft, card.limit) : card.draft;
    const res = await writeControl(card.el, text);
    if (res.ok) {
      card.status = "filled";
      card.message = `Written to the page (${text.length} characters).`;
      settle(card.key);
    } else {
      card.message = "The page did not keep the text. The draft is still here; copy it into the field.";
    }
    draw();
  }

  function skip(card: Card): void {
    card.status = "skipped";
    card.message = undefined;
    settle(card.key);
    draw();
  }

  function cardView(card: Card): Node {
    const busy = card.status === "requesting";
    const body: (Node | false)[] = [];
    const btn = (label: string, run: () => void, disabled = false) => h("button", { class: "act", type: "button", disabled, onclick: run }, label);
    if (card.status === "filled" || card.status === "skipped") {
      body.push(h("p", { class: card.status === "filled" ? "ok" : "muted" }, card.status === "filled" ? "Filled." : "Skipped."));
      if (card.message) body.push(h("p", { class: "muted", role: "status" }, card.message));
      body.push(
        btn("Draft again", () => {
          card.status = "idle";
          void ask(card, false);
        }),
      );
      return h("div", { class: "card", "data-status": card.status, "data-key": card.key }, h("strong", {}, card.question), ...body);
    }
    const fillBtn = btn("Fill", () => void fill(card), !card.draft || busy);
    body.push(
      btn(card.draft ? "Draft again" : "Draft", () => void ask(card, false), busy),
      btn("Preview", () => {
        card.open = !card.open;
        draw();
      }, !card.draft),
      fillBtn,
      btn("Skip", () => skip(card)),
    );
    if (busy) body.push(btn("Cancel", () => void cancel(card)));
    if (busy) body.push(h("p", { class: "muted", role: "status" }, "Drafting, nothing is written."));
    if (card.draft && card.open) {
      // The draft is the person's to edit before Fill; Fill and Rewrite take the edited text.
      const editor = h("textarea", { class: "preview", rows: 6, "aria-label": "Draft answer" });
      editor.value = card.draft;
      const count = h("p", {});
      const measure = () => {
        const text = card.draft ?? "";
        const over = card.limit && text.length > card.limit ? text.length - card.limit : 0;
        count.className = over ? "bad" : "muted";
        count.textContent = `${wordCount(text)} words, ${text.length}${card.limit ? ` of ${card.limit}` : ""} characters.${over ? ` Over the limit by ${over} characters. Fill writes up to the last full sentence that fits.` : ""}`;
        fillBtn.disabled = busy || text.trim() === "";
      };
      editor.addEventListener("input", () => {
        card.draft = editor.value;
        measure();
      });
      measure();
      body.push(editor, count);
      const field = h("input", { type: "text", placeholder: "How should it change? (shorter, more specific)", "aria-label": "Rewrite instruction", value: card.instruction, maxlength: 500 });
      const rewrite = btn("Rewrite", () => void ask(card, true), busy || card.instruction.trim() === "");
      field.addEventListener("input", () => {
        card.instruction = field.value;
        rewrite.disabled = busy || field.value.trim() === "";
      });
      body.push(field, rewrite);
    }
    if (card.message) body.push(h("p", { class: card.status === "refused" || card.status === "error" ? "bad" : "muted", role: "status" }, card.message));
    return h("div", { class: "card", "data-status": card.status, "data-key": card.key }, h("strong", {}, card.question), ...body);
  }

  function draw(): void {
    if (connection !== "connected") {
      redraw(el, [notConnected(connection, checked)]);
      return;
    }
    const nodes: Node[] = [
      h("p", { class: "muted" }, "Drafts are written only when you press Fill."),
      h("button", { class: "act", type: "button", onclick: () => void refresh() }, "Read the page again"),
      h("p", { role: "status", "data-unreviewed": pendingCount() }, `${pendingCount()} unreviewed ${pendingCount() === 1 ? "draft" : "drafts"}`),
    ];
    if (scanned && infos.length === 0) nodes.push(h("p", { class: "muted" }, "No questions were found on this page."));
    for (const info of infos) {
      const card = cards.get(info.selector);
      const state = stateOf(info);
      const row = h("div", { class: "q" }, h("span", {}, info.label.text), " ", h("span", { class: "muted", "data-state": state }, `(${state})`));
      nodes.push(row);
      if (card && isFreeTextControl(card.el, true)) nodes.push(cardView(card));
      else if (state === "needs you") {
        // A question with no card is answered on the page, or once for every form in the bank.
        row.append(" ", showButton(doc, info.selector, info.label.text));
        if (address) row.append(" ", h("a", { href: `${address}/answer-bank`, target: "_blank", rel: "noopener" }, "Add to answer bank"));
      }
    }
    redraw(el, nodes);
  }

  async function refresh(): Promise<void> {
    context = undefined;
    await scan();
    draw();
  }

  draw();
  // A fill may have answered questions since the last reading.
  el.addEventListener(SHOWN, () => {
    if (connection === "connected") void refresh();
  });
  return {
    setConnection(c, now = false) {
      checked = now;
      const first = connection !== "connected" && c === "connected";
      connection = c;
      if (first) void refresh();
      else draw();
    },
    dispose() {
      for (const stop of stopWatching) stop();
      for (const card of [...cards.values()]) drop(card);
    },
  };
}

