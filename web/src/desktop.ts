// The desktop shell's native bindings, as the dashboard sees them. The shell
// injects `window.mygo` only into pages of the server's own origin; in a plain
// browser, or on a paired remote device, it does not exist and every control
// that needs it stays hidden.
import { bool, num, object, str, type Decoder } from "./api/decode";

interface Bridge {
  call(method: string, ...args: unknown[]): Promise<unknown>;
  // The desktop's accent and accessibility settings, current from document
  // start, and the event that tells their changes.
  preferences: DesktopPreferences;
  on(event: string, listener: (payload: DesktopPreferences) => void): () => void;
}

interface DesktopPreferences {
  reduceTransparency?: boolean;
}

// ShellInfo mirrors app/bindings.go; app/bindings_test.go checks the field names.
interface ShellInfo {
  version: string;
  buildId: string;
  mode: string;
  pid: number;
  packaged: boolean;
  notificationsPermitted: boolean;
  logDir: string;
  startAtLogin: boolean;
  // The unpacked extension the app keeps for Chrome; "" while it could not
  // be written.
  extensionDir: string;
}

export const shellInfoDecoder: Decoder<ShellInfo> = object<ShellInfo>({
  version: str,
  buildId: str,
  mode: str,
  pid: num,
  packaged: bool,
  notificationsPermitted: bool,
  logDir: str,
  startAtLogin: bool,
  extensionDir: str,
});

function bridge(): Bridge | null {
  const b = (window as { mygo?: Bridge }).mygo;
  return b && typeof b.call === "function" ? b : null;
}

// hasShell says whether the page runs inside the desktop app.
export function hasShell(): boolean {
  return bridge() !== null;
}

function call(method: string, ...args: unknown[]): Promise<unknown> {
  const b = bridge();
  return b ? b.call(method, ...args) : Promise.reject(new Error("no_shell"));
}

// getShellInfo asks the shell, which reads the start-at-login state from the
// operating system on every call.
export async function getShellInfo(): Promise<ShellInfo> {
  return shellInfoDecoder(await call("Shell.GetShellInfo"), "shell");
}

export async function revealInFinder(path: string): Promise<void> {
  await call("Shell.RevealInFinder", path);
}

// reopenDashboard has the shell load the dashboard again in this window under
// a new session. The page never sees the launch code the shell uses.
export async function reopenDashboard(): Promise<void> {
  await call("Shell.ReopenDashboard");
}

// switchWorkspace has the app show the demo workspace or the user's own: it
// restarts its server on the other folder and loads the dashboard there. "started"
// means the page is about to be replaced; "cancelled" means nothing changed
// (the user kept running work).
export async function switchWorkspace(target: "demo" | "home"): Promise<"started" | "cancelled"> {
  return (await call("Shell.SwitchWorkspace", target)) === "cancelled" ? "cancelled" : "started";
}

export async function setStartAtLogin(enabled: boolean): Promise<void> {
  await call("Shell.SetStartAtLogin", enabled);
}

// followDesktopPreferences marks the root element data-opaque while the
// desktop asks for opaque surfaces (macOS Reduce Transparency). WebKit gives
// a page no media query for it; the shell does. Contrast and motion need
// nothing here: prefers-contrast and prefers-reduced-motion follow the
// desktop on their own.
export function followDesktopPreferences(): void {
  const b = bridge();
  if (!b) return;
  const apply = (p: DesktopPreferences) => document.documentElement.toggleAttribute("data-opaque", p.reduceTransparency === true);
  apply(b.preferences);
  b.on("mygo:preferences", apply);
}

const MESSAGES: Record<string, string> = {
  path_outside_workspace: "That file is outside the workspace folder, so it was not opened.",
  path_not_found: "That file no longer exists.",
  demo_not_found: "The demo workspace folder is gone or is not a demo. Create it again from Setup.",
  switch_busy: "The server is starting or restarting. Try again in a moment.",
  server_not_ready: "The server is not running yet. Try again in a moment.",
  fixed_port: "APPLYSMITH_PORT fixes one address for every workspace, so the app cannot switch. Unset it and restart the app.",
};

// bindingMessage turns a rejected native call into a sentence. The shell's
// errors are short codes; anything else is shown as it came.
export function bindingMessage(e: unknown): string {
  const m = e instanceof Error ? e.message : String(e);
  return MESSAGES[m] ?? m;
}
