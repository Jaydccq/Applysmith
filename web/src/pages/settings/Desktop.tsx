import { useEffect, useId, useState } from "react";
import { bindingMessage, getShellInfo, setStartAtLogin } from "../../desktop";
import { RevealButton } from "../../ui/Reveal";

// DesktopRows are the Files controls that need the desktop app: the folder in
// Finder, and whether the app starts when the user logs in. The login state is
// asked of the operating system each time the section is shown, never kept.
export function DesktopRows({ workspacePath }: { workspacePath: string }) {
  const [on, setOn] = useState<boolean | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const id = useId();
  const read = async () => {
    try {
      setOn((await getShellInfo()).startAtLogin);
    } catch (e) {
      setError(bindingMessage(e));
    }
  };
  useEffect(() => {
    void read();
  }, []);
  const toggle = async (next: boolean) => {
    setBusy(true);
    setError("");
    try {
      await setStartAtLogin(next);
    } catch (e) {
      setError(bindingMessage(e));
    }
    await read();
    setBusy(false);
  };
  return (
    <div className="actions">
      <RevealButton path={workspacePath} label="Reveal the workspace folder in Finder" />
      <label htmlFor={id}>
        <input id={id} type="checkbox" checked={on === true} disabled={on === null || busy} onChange={(e) => void toggle(e.target.checked)} /> Start Applysmith when I log in
      </label>
      <span role="alert" className="muted">
        {error}
      </span>
    </div>
  );
}
