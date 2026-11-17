// Clean-room: docs/provenance.md#CR-ENGINE
// DOM helpers shared by every module: text normalisation, composed-tree walks
// that cross open shadow roots, and event construction.

export type Win = Window & typeof globalThis;

export function winOf(node: Node): Win {
  const doc = node.ownerDocument ?? (node as Document);
  return doc.defaultView as Win;
}

/** Lower-case, accent-free, punctuation-free form used for every label comparison. */
export function normalize(text: string): string {
  return text
    .normalize("NFKD")
    .replace(/\p{M}+/gu, "")
    .toLowerCase()
    .replace(/[^\p{L}\p{N}]+/gu, " ")
    .trim();
}

const signedWords = (s: string): string[] => s.toLowerCase().match(/[\p{L}\p{N}]+[+#]*/gu) ?? [];
const unsigned = (w: string): string => w.replace(/[+#]+$/, "");

/** Whether no word reads as another name in the other text by its trailing signs, which `normalize` drops: "C++", "C#" and "C" are three skills. */
export function signsAgree(a: string, b: string): boolean {
  const x = signedWords(a);
  const y = signedWords(b);
  return !x.some((w) => y.some((v) => v !== w && unsigned(v) === unsigned(w)));
}

/** Whether a normalised text holds the needle as whole words. */
export function whole(haystack: string, needle: string): boolean {
  return needle !== "" && ` ${haystack} `.includes(` ${needle} `);
}

const GLOSS_NUMBER = "(?:\\d+|one|two|three|four|five|six|seven|eight|nine|ten)";
const LENGTH_GLOSS = new RegExp(`\\s*\\([^()]*?\\b${GLOSS_NUMBER}(?:\\s*(?:-|to)\\s*${GLOSS_NUMBER})?[\\s-]*(?:years?|yrs?)\\b[^()]*\\)`, "gi");
const DEGREE_WORD = /\b(associate|bachelor|master|doctor|doctorate|ph ?d|diploma|certificate|degree|undergraduate|graduate|ged|high school)/i;

/**
 * A degree option's schooling-length gloss ("Bachelor's Degree (4 years)") is
 * not part of what it is called. A parenthesis on anything else, such as an
 * experience bucket "Intermediate (3-5 years)", is left alone.
 */
export function stripSchoolingGloss(text: string): string {
  const bare = text.replace(LENGTH_GLOSS, "").trim();
  return bare !== "" && bare !== text.trim() && DEGREE_WORD.test(bare) ? bare : text;
}

export function collapse(text: string): string {
  return text.replace(/\s+/g, " ").trim();
}

/** Parent in the composed tree: a shadow root's parent is its host. */
export function composedParent(node: Node): Element | null {
  const parent = node.parentNode;
  if (!parent) return null;
  if (parent.nodeType === 11) return (parent as ShadowRoot).host ?? null;
  return parent.nodeType === 1 ? (parent as Element) : null;
}

export function* composedAncestors(el: Element): Generator<Element> {
  let cur = composedParent(el);
  while (cur) {
    yield cur;
    cur = composedParent(cur);
  }
}

/** Depth-first walk in document order that descends into open shadow roots. */
export function walkElements(root: ParentNode, visit: (el: Element) => void): void {
  const stack: Node[] = [];
  const push = (parent: ParentNode) => {
    const kids = parent.childNodes;
    for (let i = kids.length - 1; i >= 0; i--) stack.push(kids[i] as Node);
  };
  push(root);
  while (stack.length) {
    const node = stack.pop() as Node;
    if (node.nodeType !== 1) continue;
    const el = node as Element;
    visit(el);
    const shadow = (el as HTMLElement).shadowRoot;
    // Shadow content is pushed last so it is visited first among the host's children.
    push(el);
    if (shadow) push(shadow);
  }
}

/** Text of a node with slots resolved and shadow content included; skips script, style and svg. */
export function flatText(node: Node, skip?: (el: Element) => boolean): string {
  const parts: string[] = [];
  const visit = (n: Node) => {
    if (n.nodeType === 3) {
      parts.push(n.nodeValue ?? "");
      return;
    }
    if (n.nodeType !== 1 && n.nodeType !== 11) return;
    if (n.nodeType === 1) {
      const el = n as Element;
      const tag = el.localName;
      if (tag === "script" || tag === "style" || tag === "svg" || tag === "template" || tag === "noscript") return;
      if (skip?.(el)) return;
      if (tag === "slot") {
        const assigned = (el as HTMLSlotElement).assignedNodes?.({ flatten: true }) ?? [];
        if (assigned.length) assigned.forEach(visit);
        else el.childNodes.forEach(visit);
        return;
      }
      const shadow = (el as HTMLElement).shadowRoot;
      if (shadow) {
        shadow.childNodes.forEach(visit);
        return;
      }
    }
    n.childNodes.forEach(visit);
  };
  visit(node);
  return collapse(parts.join(" "));
}

export function hasLayout(doc: Document): boolean {
  const root = doc.documentElement;
  if (!root) return false;
  const rect = root.getBoundingClientRect();
  return rect.width > 0 && rect.height > 0;
}

export function nativeValueSetter(el: HTMLElement): ((v: string) => void) | null {
  const win = winOf(el);
  const proto =
    el.localName === "textarea"
      ? win.HTMLTextAreaElement.prototype
      : el.localName === "select"
        ? win.HTMLSelectElement.prototype
        : win.HTMLInputElement.prototype;
  const desc = Object.getOwnPropertyDescriptor(proto, "value");
  return desc?.set ? (v: string) => desc.set!.call(el, v) : null;
}

export function nativeValueGetter(el: HTMLElement): () => string {
  const win = winOf(el);
  const proto =
    el.localName === "textarea"
      ? win.HTMLTextAreaElement.prototype
      : el.localName === "select"
        ? win.HTMLSelectElement.prototype
        : win.HTMLInputElement.prototype;
  const desc = Object.getOwnPropertyDescriptor(proto, "value");
  return () => (desc?.get ? String(desc.get.call(el)) : String((el as HTMLInputElement).value ?? ""));
}

export function fireInput(el: Element, data: string | null, inputType: string): void {
  const win = winOf(el);
  let ev: Event;
  try {
    ev = new win.InputEvent("input", { bubbles: true, composed: true, data, inputType });
  } catch {
    ev = new win.Event("input", { bubbles: true, composed: true });
  }
  el.dispatchEvent(ev);
}

export function fire(el: Element, type: string, init: EventInit = {}): void {
  const win = winOf(el);
  el.dispatchEvent(new win.Event(type, { bubbles: true, composed: true, ...init }));
}

const NAMED_KEYS: Record<string, { code: string; keyCode: number }> = {
  ArrowDown: { code: "ArrowDown", keyCode: 40 },
  ArrowUp: { code: "ArrowUp", keyCode: 38 },
  ArrowLeft: { code: "ArrowLeft", keyCode: 37 },
  ArrowRight: { code: "ArrowRight", keyCode: 39 },
  Enter: { code: "Enter", keyCode: 13 },
  Escape: { code: "Escape", keyCode: 27 },
  Tab: { code: "Tab", keyCode: 9 },
  Backspace: { code: "Backspace", keyCode: 8 },
  " ": { code: "Space", keyCode: 32 },
};

/** A key event with the legacy keyCode and which set, because some widgets still read them. */
export function fireKey(el: Element, type: "keydown" | "keypress" | "keyup", key: string): boolean {
  const win = winOf(el);
  const named = NAMED_KEYS[key];
  const code = named?.code ?? (/^\d$/.test(key) ? `Digit${key}` : /^[a-z]$/i.test(key) ? `Key${key.toUpperCase()}` : key);
  const keyCode = named?.keyCode ?? (key.length === 1 ? key.toUpperCase().charCodeAt(0) : 0);
  const init = { key, code, keyCode, which: keyCode, bubbles: true, cancelable: true, composed: true };
  return el.dispatchEvent(new win.KeyboardEvent(type, init as KeyboardEventInit));
}
