import { useState } from "react";
import { del, get, request } from "../../api/client";
import { cliInstall } from "../../api/decoders_system";
import { useResource } from "../../hooks";
import { Chip, ErrorNote, Loading } from "../../ui/parts";
import type { CLIInstall } from "../../wire";
import { useAction } from "../action";

// CommandLine makes or removes the ~/.local/bin/applysmith link to the program
// inside the installed app. It shows the shell-profile line to add when that
// folder is not on PATH and never edits the profile itself.
export function CommandLine() {
  const [res, reload] = useResource(() => get("/api/system/cli", cliInstall), [], [], { keep: "settings.cli" });
  const act = useAction();
  // The answer of the last change; it replaces the loaded state until a reload.
  const [changed, setChanged] = useState<CLIInstall | null>(null);
  const change = async (go: () => Promise<CLIInstall>) => {
    const done = await act.run(go);
    if (done) setChanged(done);
  };
  const cli = changed ?? res.data;
  return (
    <section aria-labelledby="settings-cli">
      <h3 id="settings-cli">Command line</h3>
      <p className="muted">
        Makes <code>applysmith</code> work in Terminal by linking <code>~/.local/bin/applysmith</code> to the program inside this app. Nothing else on your computer is changed.
      </p>
      {res.status === "loading" && <Loading />}
      {res.status === "error" && <ErrorNote error={res.error} onRetry={reload} what="the command line state" />}
      {cli && !cli.available && <p className="muted">This copy of the server does not run from the installed app, so the link cannot be made from here.</p>}
      {cli && cli.available && (
        <>
          <p>
            <Chip tone={cli.state === "installed" ? "good" : cli.state === "foreign" ? "bad" : "warn"}>{STATE_LABEL[cli.state] ?? cli.state}</Chip>
          </p>
          {cli.state === "foreign" && (
            <p className="note">
              A different file is at <code>{cli.link}</code>. It was left as it is; move it away if you want this link.
            </p>
          )}
          {cli.state === "stale" && <p className="note">The link points at a copy of the app that is somewhere else. Make the link again to use this copy.</p>}
          <div className="actions">
            {cli.state !== "installed" && cli.state !== "foreign" && (
              <button type="button" disabled={act.busy} onClick={() => void change(() => request("PUT", "/api/system/cli", cliInstall))}>
                {cli.state === "stale" ? "Update the command link" : "Install the command"}
              </button>
            )}
            {(cli.state === "installed" || cli.state === "stale") && (
              <button type="button" disabled={act.busy} onClick={() => void change(() => del("/api/system/cli", cliInstall))}>
                Remove the command
              </button>
            )}
          </div>
          {cli.state === "installed" &&
            (cli.onPath ? (
              <p role="status" className="note ok">
                <code>~/.local/bin</code> is on your PATH. Open a new Terminal window and run <code>applysmith version</code>.
              </p>
            ) : (
              <div role="status">
                <p className="note">
                  <code>~/.local/bin</code> is not on your PATH. Add this line to <code>{cli.profileFile}</code>, then open a new Terminal window. The app does not edit your shell files.
                </p>
                <pre className="log-lines" tabIndex={0} aria-label="Line to add to the shell profile">{cli.profileLine}</pre>
              </div>
            ))}
        </>
      )}
      {act.error && <ErrorNote error={act.error} />}
    </section>
  );
}

const STATE_LABEL: Record<string, string> = {
  "not-installed": "not installed",
  installed: "installed",
  stale: "points at another copy of the app",
  foreign: "another file is there",
};
