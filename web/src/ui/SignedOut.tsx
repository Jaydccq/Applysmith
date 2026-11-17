import { useState } from "react";
import { bindingMessage, hasShell, reopenDashboard } from "../desktop";

// Shown instead of the dashboard once the server stops accepting this page's
// session. Sessions outlive a server restart, so this means the session
// ended; a page cannot create one for itself. Inside the desktop app the app
// can: it mints the new session and loads it into this window.
export function SignedOut() {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const reopen = () => {
    setBusy(true);
    setError("");
    reopenDashboard().catch((e) => {
      setError(bindingMessage(e));
      setBusy(false);
    });
  };
  return (
    <main className="signed-out" id="main">
      <h1>Open the dashboard from the Applysmith app</h1>
      <p>
        This page's session has ended: it went unused for 30 days, a newer one replaced it, or a backup was restored. Open the dashboard again from the Applysmith app
        (menu bar item, Open Dashboard), or run <code>applysmith open</code> in a terminal for a new link.
      </p>
      {hasShell() && (
        <p>
          <button type="button" disabled={busy} onClick={reopen}>
            {busy ? "Opening…" : "Open the dashboard again"}
          </button>
        </p>
      )}
      {error && <p role="alert">{error}</p>}
      <p>A link works once and expires after two minutes. Once it opens in this browser, this page comes back by itself.</p>
    </main>
  );
}
