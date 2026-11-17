import { Check } from "lucide-react";
import { useEffect, useState } from "react";

// COPIED_MS is how long the button shows that the copy worked.
const COPIED_MS = 2000;

// CopyButton puts text on the clipboard and says so: the button itself turns
// to a check for a moment, and a status line tells assistive technology. A
// blocked clipboard is reported rather than ignored, because the text is then
// still on the page to select by hand.
export function CopyButton({ text, label }: { text: string; label: string }) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
  useEffect(() => {
    if (state !== "copied") return;
    const timer = setTimeout(() => setState("idle"), COPIED_MS);
    return () => clearTimeout(timer);
  }, [state]);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setState("copied");
    } catch {
      setState("failed");
    }
  };
  return (
    <>
      <button type="button" className={state === "copied" ? "copied" : undefined} onClick={() => void copy()} aria-label={label}>
        {state === "copied" ? (
          <>
            <Check aria-hidden="true" />
            Copied
          </>
        ) : (
          "Copy"
        )}
      </button>
      <span role="status" className={state === "failed" ? "muted" : "sr-only"}>
        {state === "copied" ? "Copied" : state === "failed" ? "Could not copy; select the text instead." : ""}
      </span>
    </>
  );
}
