// Injected into one sub-frame by the worker when the panel's Next step button
// is pressed and the top frame has no progress control of its own. It presses
// this frame's progress control at most once per call.
import { advanceThisFrame } from "./widgets/nextstep";

Object.defineProperty(globalThis, "__applysmithAdvance", { value: () => advanceThisFrame(document), configurable: true, enumerable: false });
