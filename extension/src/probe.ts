// Injected into an unknown sub-frame before the engine: it only counts the
// frame's controls, with the engine's own counting rule, and changes nothing in
// the page. The worker injects the engine afterwards only if the frame qualifies.
import { probeControls } from "../../engine/src/detect";

Object.defineProperty(globalThis, "__applysmithProbe", { value: () => probeControls(document), configurable: true, enumerable: false });
