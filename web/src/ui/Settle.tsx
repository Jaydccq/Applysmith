import { createContext, useEffect, useState, type ReactNode } from "react";
import { onReadsChange, readsInFlight } from "../api/client";

// QUIET_MS is how long no read may be in flight before the page counts as
// arrived: a section that asks for more once its first answer is drawn starts
// that request in the same tick, well inside this.
export const QUIET_MS = 60;
// SETTLE_MAX_MS bounds the wait, so a slow server shows its skeletons instead
// of a blank page.
export const SETTLE_MAX_MS = 1500;

// Settled is false while the page is held. Work the opening page does not
// draw from (the Home setup check, the other pages' code) waits for it rather
// than share the browser's six connections to the server with the reads that
// decide the page. Outside a Settle the page counts as settled.
export const Settled = createContext(true);

// Settle keeps a page invisible, not absent, until the reads it started on
// opening have arrived. Its sections still draw their skeletons, but out of
// sight, so the first thing a viewer sees is the page as it will stay and
// nothing moves under their eyes. Opacity rather than visibility, so a drawer
// or dialog that opens with the page can take focus at once. Mount it with a
// key per page: it settles once, when its page opens.
export function Settle({ children, onSettled }: { children: ReactNode; onSettled?: () => void }) {
  const [settled, setSettled] = useState(false);
  useEffect(() => {
    if (settled) onSettled?.();
  }, [settled, onSettled]);
  useEffect(() => {
    let quiet: ReturnType<typeof setTimeout> | undefined;
    const open = () => setSettled(true);
    const check = () => {
      clearTimeout(quiet);
      if (readsInFlight() === 0) quiet = setTimeout(open, QUIET_MS);
    };
    const cap = setTimeout(open, SETTLE_MAX_MS);
    const off = onReadsChange(check);
    check();
    return () => {
      clearTimeout(quiet);
      clearTimeout(cap);
      off();
    };
  }, []);
  return (
    <div className="settle" data-settled={settled} aria-busy={!settled}>
      <Settled.Provider value={settled}>{children}</Settled.Provider>
    </div>
  );
}
