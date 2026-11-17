// The ARIA tabs pattern with automatic activation: arrow keys move focus and
// selection together and wrap, Home and End jump, and only the selected tab is
// in the tab order.

export interface TabSpec {
  id: string;
  label: string;
}

export interface TabsHandle {
  panels: Map<string, HTMLElement>;
  focus(): void;
  selected(): string;
}

export function mountTabs(container: HTMLElement, specs: TabSpec[], initial: string, onSelect: (id: string) => void): TabsHandle {
  const doc = container.ownerDocument;
  const list = doc.createElement("div");
  list.setAttribute("role", "tablist");
  list.setAttribute("aria-label", "Applysmith");
  const buttons = new Map<string, HTMLButtonElement>();
  const panels = new Map<string, HTMLElement>();
  let current = specs.some((s) => s.id === initial) ? initial : (specs[0] as TabSpec).id;

  const apply = (id: string, focus = false) => {
    current = id;
    for (const [tabId, btn] of buttons) {
      const on = tabId === id;
      btn.setAttribute("aria-selected", String(on));
      btn.tabIndex = on ? 0 : -1;
      panels.get(tabId)!.hidden = !on;
    }
    if (focus) buttons.get(id)!.focus();
  };
  const select = (id: string, focus = false) => {
    apply(id, focus);
    onSelect(id);
  };

  specs.forEach((spec, i) => {
    const btn = doc.createElement("button");
    btn.type = "button";
    btn.id = `tab-${spec.id}`;
    btn.setAttribute("role", "tab");
    btn.setAttribute("aria-controls", `panel-${spec.id}`);
    btn.textContent = spec.label;
    btn.addEventListener("click", () => select(spec.id));
    btn.addEventListener("keydown", (e) => {
      const last = specs.length - 1;
      const move: Record<string, number> = { ArrowRight: (i + 1) % specs.length, ArrowLeft: (i - 1 + specs.length) % specs.length, Home: 0, End: last };
      const to = move[e.key];
      if (to === undefined) return;
      e.preventDefault();
      select((specs[to] as TabSpec).id, true);
    });
    buttons.set(spec.id, btn);
    list.append(btn);
    const panel = doc.createElement("div");
    panel.id = `panel-${spec.id}`;
    panel.setAttribute("role", "tabpanel");
    panel.setAttribute("aria-labelledby", btn.id);
    panels.set(spec.id, panel);
  });
  container.append(list, ...panels.values());
  apply(current);
  return { panels, selected: () => current, focus: () => buttons.get(current)!.focus() };
}
