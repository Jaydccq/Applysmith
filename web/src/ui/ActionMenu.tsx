import { Ellipsis, type LucideIcon } from "lucide-react";
import { useEffect, useId, useLayoutEffect, useRef, useState, type CSSProperties, type KeyboardEvent } from "react";

interface MenuItem {
  label: string;
  // ariaLabel names the item for a screen reader when the label alone would
  // not say which row it acts on.
  ariaLabel?: string;
  icon?: LucideIcon;
  disabled?: boolean;
  // title says why an item is disabled.
  title?: string;
  onSelect: () => void;
}

// ActionMenu holds a row's secondary actions behind one "More" button, so the
// row keeps its width for its data. The button opens a menu (aria-haspopup),
// arrow keys, Home and End move between the enabled items, Escape closes it and
// returns focus to the button, and Tab or a press outside closes it. The menu is
// fixed to the viewport at the button, so a card's edge never clips it.
//
//   <ActionMenu label={`More actions: ${who}`} items={[{ label: "Documents", onSelect: open }]} />
export function ActionMenu({ label, items, disabled = false }: { label: string; items: MenuItem[]; disabled?: boolean }) {
  const [open, setOpen] = useState(false);
  const [place, setPlace] = useState<CSSProperties>({});
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const id = useId();

  const enabled = () => Array.from(menu.current?.querySelectorAll<HTMLButtonElement>('button[role="menuitem"]:not(:disabled)') ?? []);

  useLayoutEffect(() => {
    if (!open || !trigger.current || !menu.current) return;
    const at = trigger.current.getBoundingClientRect();
    const height = menu.current.offsetHeight;
    const below = window.innerHeight - at.bottom;
    const flip = below < height + 8 && at.top > below;
    setPlace({ top: flip ? Math.max(8, at.top - height - 4) : at.bottom + 4, right: Math.max(8, window.innerWidth - at.right) });
  }, [open]);

  useEffect(() => {
    if (!open) return;
    enabled()[0]?.focus();
    const away = (e: Event) => {
      if (!menu.current?.contains(e.target as Node) && !trigger.current?.contains(e.target as Node)) setOpen(false);
    };
    const close = () => setOpen(false);
    document.addEventListener("pointerdown", away);
    window.addEventListener("resize", close);
    window.addEventListener("scroll", close, true);
    return () => {
      document.removeEventListener("pointerdown", away);
      window.removeEventListener("resize", close);
      window.removeEventListener("scroll", close, true);
    };
  }, [open]);

  const onKey = (e: KeyboardEvent) => {
    const list = enabled();
    const at = list.indexOf(document.activeElement as HTMLButtonElement);
    const next = e.key === "ArrowDown" ? (at + 1) % list.length : e.key === "ArrowUp" ? (at <= 0 ? list.length - 1 : at - 1) : e.key === "Home" ? 0 : e.key === "End" ? list.length - 1 : -1;
    if (next >= 0) {
      e.preventDefault();
      list[next]?.focus();
    } else if (e.key === "Escape") {
      e.preventDefault();
      setOpen(false);
      trigger.current?.focus();
    } else if (e.key === "Tab") {
      setOpen(false);
    }
  };

  return (
    <>
      <button
        ref={trigger}
        type="button"
        className="small"
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? id : undefined}
        disabled={disabled}
        onClick={() => setOpen((o) => !o)}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown" && !open) {
            e.preventDefault();
            setOpen(true);
          }
        }}
      >
        <Ellipsis />
        More
      </button>
      {open && (
        <div ref={menu} id={id} role="menu" aria-label={label} className="menu" style={place} onKeyDown={onKey}>
          {items.map((item) => (
            <button
              key={item.label}
              type="button"
              role="menuitem"
              className="ghost"
              disabled={item.disabled}
              title={item.title}
              aria-label={item.ariaLabel}
              onClick={() => {
                setOpen(false);
                item.onSelect();
                trigger.current?.focus();
              }}
            >
              {item.icon && <item.icon />}
              {item.label}
            </button>
          ))}
        </div>
      )}
    </>
  );
}
