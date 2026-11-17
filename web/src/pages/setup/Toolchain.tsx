import { get, request } from "../../api/client";
import { documentToolchain } from "../../api/decoders_setup";
import { useResource } from "../../hooks";
import { Chip, ErrorNote, Panel } from "../../ui/parts";
import { useAction } from "../action";
import type { Tone } from "./model";
import { withCode } from "./Reasons";

const STATE_TONE: Record<string, Tone> = { ready: "good", "verification-needed": "warn", unavailable: "bad" };

function componentTone(status: string): Tone {
  if (/^(ok|ready|present|found|pass|installed)/i.test(status)) return "good";
  if (/missing|absent|fail|not/i.test(status)) return "bad";
  return "warn";
}

const mebibytes = (bytes: number) => `${(bytes / 1048576).toFixed(bytes < 10485760 ? 1 : 0)} MiB`;

// RenderTest runs the render test a changed tool or template needs once every
// tool is installed (about 20 seconds, so it waits longer than a plain
// request). The Documents drawer offers it too, where a refused PDF says so.
export function RenderTest({ onDone }: { onDone: () => void }) {
  const smoke = useAction();
  const test = async () => {
    if (await smoke.run(() => request("POST", "/api/documents/toolchain/smoke", documentToolchain, { timeoutMs: 300_000 }))) onDone();
  };
  return (
    <>
      <p className="actions">
        <button type="button" disabled={smoke.busy} onClick={() => void test()}>
          {smoke.busy ? "Running the render test..." : "Run the render test"}
        </button>
        <span className="muted">Compiles two sample documents with made-up details; nothing of yours is read.</span>
      </p>
      {smoke.error && <ErrorNote error={smoke.error} />}
    </>
  );
}

// Toolchain is the rendering subcheck of the résumé step. It answers a
// different question from the roster: whether new PDFs can be made here. It
// changes nothing on the machine, except the render test it runs on request.
export function Toolchain() {
  const [res, reload] = useResource(() => get("/api/documents/toolchain", documentToolchain), [], ["setup"]);
  return (
    <Panel title="Rendering new PDFs" id="setup-toolchain" res={res} retry={reload}>
      {(t) => (
        <div>
          <p>
            <Chip tone={STATE_TONE[t.state] ?? "neutral"}>{t.state}</Chip>
          </p>
          <p className="muted">Uploaded résumés work while rendering is unavailable. This check only says whether the app can make new PDFs, and it is shown apart from the roster above.</p>
          {t.code && <p className="muted">Code: {t.code}</p>}
          {t.smoke && <p className="muted">Render test: {t.smoke}</p>}
          <h3>Components</h3>
          <ul className="plain stack">
            {t.components.map((c) => (
              <li key={c.name}>
                <strong>{c.name}</strong> <Chip tone={componentTone(c.status)}>{c.status}</Chip>
                {c.version && <span className="muted"> {c.version}</span>}
                {c.detail && <p className="muted">{c.detail}</p>}
                {c.fix && (
                  <p>
                    Install: {withCode(c.fix)}
                  </p>
                )}
              </li>
            ))}
          </ul>
          {/* With nothing missing, the fix is the render test a changed tool or
              template needs: the button runs it, so the command is not repeated. */}
          {(t.missing.length > 0 || t.fix) && (
            <div role="status">
              {t.missing.length > 0 && (
                <>
                  <h3>Missing</h3>
                  <p>{t.missing.join(", ")}</p>
                </>
              )}
              {t.state === "verification-needed" ? <RenderTest onDone={reload} /> : t.fix && <p>To fix it: {withCode(t.fix)}</p>}
            </div>
          )}
          {/* Once rendering is ready, what it needs and how to install it is only reference. */}
          <details open={t.state !== "ready"}>
            <summary>What rendering needs</summary>
            <h3>Prepared by the app</h3>
            {t.budget.downloads.length === 0 ? (
              <p className="muted">The app downloads nothing for this.</p>
            ) : (
              <ul>
                {t.budget.downloads.map((d) => (
                  <li key={d}>{d}</li>
                ))}
              </ul>
            )}
            <p className="muted">Disk used by what the app prepares: {mebibytes(t.budget.diskBytes)}.</p>
            <h3>You install these yourself, outside the app</h3>
            <ul className="plain stack">
              {t.budget.external.map((x) => (
                <li key={x.name}>
                  <strong>{x.name}</strong>: {x.size}
                  <p>
                    Install: {withCode(x.install)}
                  </p>
                </li>
              ))}
            </ul>
          </details>
        </div>
      )}
    </Panel>
  );
}
