import { useEffect, useState } from "react";
import { openRunStream } from "../../api/runstream";
import type { RunFinal } from "../../wire";

const LOG_LINES = 200;

// useRunLines follows the progress of one run and keeps its last 200 lines.
export function useRunLines(runId: string | undefined, onFinal: () => void): { lines: string[]; final?: RunFinal } {
  const [lines, setLines] = useState<string[]>([]);
  const [final, setFinal] = useState<RunFinal>();
  useEffect(() => {
    setLines([]);
    setFinal(undefined);
    if (!runId) return;
    const stream = openRunStream(
      runId,
      (ev) => {
        if (ev.type === "progress") setLines((l) => [...l, ev.line].slice(-LOG_LINES));
        else {
          setFinal(ev.final);
          onFinal();
        }
      },
      () => {},
    );
    return () => stream.close();
  }, [runId]);
  return { lines, final };
}
