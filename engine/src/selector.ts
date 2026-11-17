// Clean-room: docs/provenance.md#CR-ENGINE
// Stable selectors for controls, including controls inside open shadow roots.
// A selector survives a re-render: the engine re-resolves it instead of
// holding a node.

import { isRendered } from "./visibility";

export const SHADOW_SEP = " >>> ";

type Root = Document | ShadowRoot;

function quote(value: string): string {
  return `"${value.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
}

function idSelector(id: string): string {
  return `[id=${quote(id)}]`;
}

/** Whether exactly one element of the root carries `attr` equal to `value`, and it is `el`: within a look, read from its index. */
function uniqueBy(root: Root, attr: string, value: string, el: Element): boolean {
  const found = byAttribute(root, "", attr, value);
  if (!found) return isUnique(root, `[${attr}=${quote(value)}]`, el);
  return found.length === 1 && found[0] === el;
}

function isUnique(root: Root, sel: string, el: Element): boolean {
  try {
    const found = root.querySelectorAll(sel);
    return found.length === 1 && found[0] === el;
  } catch {
    return false;
  }
}

/** While one look at an unchanging page runs: each element's anchor, worked out once. */
let anchors: WeakMap<Element, string | null> | null = null;
/** While one look runs: per root and `tag[attr]`, the elements carrying each value of the attribute, in tree order. */
let indexes: WeakMap<Root, Map<string, Map<string, Element[]>>> | null = null;

/**
 * Runs one synchronous look at the page, in which the many controls under one ancestor ask for its anchor once, and an
 * attribute lookup reads the root once instead of once per control.
 */
export function sharingAnchors<T>(look: () => T): T {
  if (anchors) return look();
  anchors = new WeakMap();
  indexes = new WeakMap();
  try {
    return look();
  } finally {
    anchors = null;
    indexes = null;
  }
}

/**
 * Within a look, what `root.querySelectorAll('tag[attr="value"]')` finds (`tag` empty for any element), from one read of
 * the root; null outside a look, or for a value a quoted selector cannot hold as it is.
 */
export function byAttribute(root: Root, tag: string, attr: string, value: string): Element[] | null {
  if (!indexes || /["\\\n\r\f\0]/.test(value)) return null;
  let perRoot = indexes.get(root);
  if (!perRoot) indexes.set(root, (perRoot = new Map()));
  const key = `${tag}[${attr}]`;
  let index = perRoot.get(key);
  if (!index) {
    index = new Map();
    for (const e of Array.from(root.querySelectorAll(key))) {
      const v = e.getAttribute(attr) as string;
      const list = index.get(v);
      if (list) list.push(e);
      else index.set(v, [e]);
    }
    perRoot.set(key, index);
  }
  return index.get(value) ?? [];
}

function anchorOf(el: Element, root: Root): string | null {
  const known = anchors?.get(el);
  if (known !== undefined) return known;
  const found = findAnchor(el, root);
  anchors?.set(el, found);
  return found;
}

function findAnchor(el: Element, root: Root): string | null {
  const id = el.getAttribute("id");
  if (id && uniqueBy(root, "id", id, el)) return idSelector(id);
  // Workday names its controls by automation id (the résumé box is
  // file-upload-input-ref); a path through its siblings breaks when a toast or
  // an error line is drawn beside it.
  for (const a of ["data-field-path", "data-automation-id"]) {
    const v = el.getAttribute(a);
    if (!v) continue;
    if (uniqueBy(root, a, v, el)) return `[${a}=${quote(v)}]`;
  }
  return null;
}

function step(el: Element): string {
  const parent = el.parentNode as ParentNode | null;
  const index = parent ? Array.from(parent.children).indexOf(el) + 1 : 1;
  return `${el.localName}:nth-child(${index})`;
}

function localSelector(el: Element, root: Root): string {
  const anchor = anchorOf(el, root);
  if (anchor) return anchor;
  const name = el.getAttribute("name");
  if (name) {
    const type = el.getAttribute("type");
    const sel = `${el.localName}[name=${quote(name)}]${type ? `[type=${quote(type)}]` : ""}`;
    if (isUnique(root, sel, el)) return sel;
  }
  const parts = [step(el)];
  let cur: Element | null = el.parentElement;
  for (let depth = 0; cur && depth < 24; depth++) {
    const a = anchorOf(cur, root);
    if (a) {
      const sel = `${a} > ${parts.join(" > ")}`;
      if (isUnique(root, sel, el)) return sel;
    }
    parts.unshift(step(cur));
    if (depth >= 5 && isUnique(root, parts.join(" > "), el)) return parts.join(" > ");
    cur = cur.parentElement;
  }
  return parts.join(" > ");
}

export function selectorFor(el: Element): string {
  const parts: string[] = [];
  let node: Element = el;
  for (;;) {
    const root = node.getRootNode() as Root;
    parts.unshift(localSelector(node, root));
    if (root.nodeType === 11) node = (root as ShadowRoot).host;
    else break;
  }
  return parts.join(SHADOW_SEP);
}

/** Selector that matches every member of a named radio or checkbox group. */
export function groupSelector(first: Element): string {
  const name = first.getAttribute("name");
  const type = first.getAttribute("type");
  if (!name || !type) return selectorFor(first);
  const base = selectorFor(first);
  const sepAt = base.lastIndexOf(SHADOW_SEP);
  const prefix = sepAt >= 0 ? base.slice(0, sepAt + SHADOW_SEP.length) : "";
  return `${prefix}input[type=${quote(type)}][name=${quote(name)}]`;
}

/** Every element a selector matches, in document order per shadow level. */
export function resolveAll(doc: Document, selector: string): Element[] {
  let roots: Root[] = [doc];
  const parts = selector.split(SHADOW_SEP);
  for (let i = 0; i < parts.length; i++) {
    const part = parts[i] as string;
    const found: Element[] = [];
    for (const r of roots) {
      try {
        found.push(...Array.from(r.querySelectorAll(part)));
      } catch {
        return [];
      }
    }
    if (i === parts.length - 1) return found;
    roots = found.map((f) => f.shadowRoot).filter((s): s is ShadowRoot => !!s);
    if (!roots.length) return [];
  }
  return [];
}

/** The shown copy when several elements match, else the first. */
export function resolveSelector(doc: Document, selector: string): Element | null {
  const found = resolveAll(doc, selector);
  if (found.length <= 1) return found[0] ?? null;
  return found.find((f) => isRendered(f)) ?? found[0] ?? null;
}
