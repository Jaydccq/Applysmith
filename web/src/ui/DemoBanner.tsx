import { useState } from "react";
import { get } from "../api/client";
import { setupStatus } from "../api/decoders_setup";
import { bindingMessage, hasShell, switchWorkspace } from "../desktop";
import { useResource } from "../hooks";
import { leave, Link } from "../router";
import { CopyButton } from "./CopyButton";

// DEMO_NEVER is what a demo workspace never does; the server's gates enforce
// each item, so this text only describes them.
export const DEMO_NEVER =
  "It never presses Submit, creates an account, resets a password, solves a captcha, opens an activation link, sends mail, or sends a Telegram message. It reads mail only to type a site's emailed verification code, and only when the persona's address is a plus alias of your own connected Gmail account. The only files it uploads are the persona's own fictional documents.";

// useDemo says whether this dashboard runs the demo workspace.
export function useDemo(): boolean {
  const [status] = useResource(() => get("/api/setup/status", setupStatus), [], ["setup"]);
  return status.data?.demo === true;
}

// SwitchButton asks the desktop app to show the demo workspace or the user's
// own. The app restarts its server on the other folder and replaces this page,
// so it asks first while a form holds unsaved text, and stays busy once the
// app took the request; it frees again when the user kept running work or the
// app refused.
export function SwitchButton({ target, children }: { target: "demo" | "home"; children: string }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const go = () => {
    setBusy(true);
    setError("");
    switchWorkspace(target).then(
      (r) => r === "cancelled" && setBusy(false),
      (e) => {
        setError(bindingMessage(e));
        setBusy(false);
      },
    );
  };
  return (
    <>
      <button type="button" disabled={busy} onClick={() => leave(go)}>
        {busy ? "Switching…" : children}
      </button>
      {error && <span role="alert"> {error}</span>}
    </>
  );
}

// Without the app there is no process to restart, so the way back is the
// command that starts the user's own workspace, ready to copy.
const HOME_COMMAND = "applysmith serve";

// BackToWorkspace is the way out of the demo: one button inside the app, a
// copyable command in a plain browser.
export function BackToWorkspace({ inline }: { inline?: boolean }) {
  if (hasShell()) return <SwitchButton target="home">Back to my workspace</SwitchButton>;
  return (
    <>
      {inline ? "stop" : "To return to your own workspace, stop"} this server and run <code>{HOME_COMMAND}</code> <CopyButton text={HOME_COMMAND} label="Copy the command for your own workspace" />
    </>
  );
}

// DemoBanner is the persistent notice that this dashboard runs the demo
// workspace. It shows on every page and has no dismiss control. What the demo
// never does sits under a disclosure so the banner stays short on a phone.
export function DemoBanner() {
  if (!useDemo()) return null;
  return (
    <div role="note" aria-label="Demo workspace" className="banner">
      <strong>Demo workspace.</strong> Everything here belongs to the fictional persona Alex Example.{" "}
      <Link to="/setup?step=orientation#demo-tutorial">Tutorial</Link>. <BackToWorkspace />
      <details>
        <summary>What the demo never does</summary>
        {DEMO_NEVER}
      </details>
    </div>
  );
}
