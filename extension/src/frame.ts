// Injected by the worker into an application iframe (an ATS form embedded in a
// careers page) once the top frame's widgets are running: the submission
// recorder, in this frame. A second injection into the same document installs
// nothing, so one click is never recorded twice.
import { send } from "./shared/rpc";
import { isApplicationPage } from "./shared/site";
import { startRecorder } from "./widgets/recorder";

const KEY = "__applysmithFrameRecorder";
const g = globalThis as unknown as Record<string, unknown>;
if (!g[KEY]) {
  Object.defineProperty(globalThis, KEY, { value: true, configurable: false, enumerable: false });
  startRecorder({
    doc: document,
    win: window,
    record: (entry) => send({ kind: "submission.record", entry }).then(() => {}),
    applicationPage: () => isApplicationPage(document, window),
  });
}
