// Clean-room: docs/provenance.md#CR-ENGINE
// Entry of the main-world bundle. The host replaces the two placeholders with
// the tab's random nonce and the bundle's content hash before it evaluates it.

import { installHelper } from "./helper";

installHelper(globalThis as unknown as Window & typeof globalThis, "__AJ_NONCE__", "__AJ_STAMP__");
