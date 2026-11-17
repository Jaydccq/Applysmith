type Child = Node | string | null | false | undefined;

/** A small element builder. Text goes in as text, never as markup. */
export function h<K extends keyof HTMLElementTagNameMap>(tag: K, props: Record<string, unknown> = {}, ...kids: Child[]): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === "class") el.className = String(v);
    else if (k.startsWith("on") && typeof v === "function") el.addEventListener(k.slice(2), v as EventListener);
    else if (v === true) el.setAttribute(k, "");
    else if (v !== false && v !== undefined && v !== null) el.setAttribute(k, String(v));
  }
  for (const kid of kids) if (kid) el.append(kid);
  return el;
}

/** Which control a focused element is: its own key or label, inside the nearest keyed ancestor. */
function focusKey(n: HTMLElement): string {
  const scope = n.parentElement?.closest<HTMLElement>("[data-key]")?.dataset.key ?? "";
  return `${scope}|${n.tagName}|${n.dataset.key ?? n.getAttribute("aria-label") ?? n.textContent}`;
}

/**
 * Replaces el's children and keeps keyboard focus: on the same control of the new content (with a
 * text field's caret) while it is enabled, else on el itself, never lost to the page.
 */
export function redraw(el: HTMLElement, kids: (Node | string)[]): void {
  const was = (el.getRootNode() as Document | ShadowRoot).activeElement as HTMLElement | null;
  if (!was || was === el || !el.contains(was)) return el.replaceChildren(...kids);
  const key = focusKey(was);
  const peers = (): HTMLElement[] => [...el.querySelectorAll<HTMLElement>(was.tagName)].filter((n) => focusKey(n) === key);
  const nth = peers().indexOf(was);
  const field = was as HTMLInputElement;
  const caret = [field.selectionStart, field.selectionEnd];
  el.replaceChildren(...kids);
  const same = peers()[nth] as HTMLButtonElement | undefined;
  const next = same && !same.disabled ? same : el;
  if (next === el) el.tabIndex = -1;
  next.focus({ preventScroll: true });
  try {
    if (next !== el && caret[0] != null) (next as HTMLInputElement).setSelectionRange(caret[0], caret[1]);
  } catch {
    /* a field type with no caret */
  }
}
