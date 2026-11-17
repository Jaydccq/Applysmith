// Clean-room: docs/provenance.md#CR-ENGINE
// Bundle entry: installs one engine per document, replacing an older instance
// first so its listeners, observers and timers are released.

import type { Win } from "./dom";
import { AutofillEngine } from "./engine";
import { bindingHost } from "./host";

export const GLOBAL_NAME = "__applysmithEngine";

export function install(win: Win): AutofillEngine {
  const holder = win as unknown as Record<string, AutofillEngine | undefined>;
  holder[GLOBAL_NAME]?.dispose();
  const engine = new AutofillEngine({ host: bindingHost(win as unknown as Record<string, unknown>), win });
  Object.defineProperty(win, GLOBAL_NAME, { value: engine, configurable: true, writable: true, enumerable: false });
  return engine;
}

install(globalThis as unknown as Win);
