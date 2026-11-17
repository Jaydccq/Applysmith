import { useState } from "react";
import { bindingMessage, hasShell, revealInFinder } from "../desktop";

// RevealButton shows a file of the workspace in Finder. It exists only inside
// the desktop app; a browser has no way to open the user's file manager.
export function RevealButton({ path, label }: { path: string; label: string }) {
  const [error, setError] = useState("");
  if (!hasShell()) return null;
  const press = async () => {
    setError("");
    try {
      await revealInFinder(path);
    } catch (e) {
      setError(bindingMessage(e));
    }
  };
  return (
    <>
      <button type="button" aria-label={label} onClick={() => void press()}>
        Reveal in Finder
      </button>
      <span role="alert" className="muted">
        {error}
      </span>
    </>
  );
}
