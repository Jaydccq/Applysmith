import { Chip } from "./parts";
import { useEffect, useId, useRef, type KeyboardEvent, type ReactNode } from "react";

interface TabDef {
  id: string;
  label: string;
  /** A count of what waits in the tab, drawn as a chip; zero is left out. */
  count?: number;
}

// Tabs is the tab pattern of the dashboard: one tab stop, arrow keys move
// between tabs, and the panel is labelled by its tab. The active tab lives in
// the caller's state so a page can keep it in the address.
export function Tabs({ label, tabs, active, onSelect, children }: { label: string; tabs: readonly TabDef[]; active: string; onSelect: (id: string) => void; children: ReactNode }) {
  const base = useId();
  const refs = useRef<Record<string, HTMLButtonElement | null>>({});
  // On a phone the strip scrolls sideways; the open tab, such as one named by
  // the address, is brought into view so the page never shows no selection.
  useEffect(() => {
    refs.current[active]?.scrollIntoView?.({ inline: "nearest", block: "nearest" });
  }, [active]);
  const move = (e: KeyboardEvent, index: number) => {
    const last = tabs.length - 1;
    const next = e.key === "ArrowRight" ? (index === last ? 0 : index + 1) : e.key === "ArrowLeft" ? (index === 0 ? last : index - 1) : e.key === "Home" ? 0 : e.key === "End" ? last : -1;
    if (next < 0) return;
    e.preventDefault();
    onSelect(tabs[next].id);
    refs.current[tabs[next].id]?.focus();
  };
  return (
    <>
      <div role="tablist" aria-label={label} className="tabs">
        {tabs.map((t, i) => (
          <button
            key={t.id}
            ref={(el) => void (refs.current[t.id] = el)}
            type="button"
            role="tab"
            id={`${base}-tab-${t.id}`}
            aria-selected={t.id === active}
            aria-controls={`${base}-panel-${t.id}`}
            tabIndex={t.id === active ? 0 : -1}
            onClick={() => onSelect(t.id)}
            onKeyDown={(e) => move(e, i)}
          >
            {t.label}
            {t.count ? <Chip>{t.count}</Chip> : null}
          </button>
        ))}
      </div>
      <div role="tabpanel" id={`${base}-panel-${active}`} aria-labelledby={`${base}-tab-${active}`} className="tab-panel">
        {children}
      </div>
    </>
  );
}
