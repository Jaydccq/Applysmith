// Clean-room: docs/provenance.md#CR-ENGINE
// The wire between the engine core (isolated world) and the main-world helper.
// Both sides share the document, not the JavaScript heap, so a message is a
// string carried by a DOM event whose name holds a nonce the page cannot
// guess. Nothing but strings crosses.

export interface HelperRequest {
  id: number;
  op: string;
  args?: unknown;
}

export type HelperReply = { id: number; ok: true; result?: unknown } | { id: number; ok: false; error: string };

export const reqEvent = (nonce: string): string => `aj:${nonce}:req`;
export const resEvent = (nonce: string): string => `aj:${nonce}:res`;
/**
 * The session-storage key of the submit observer's entries. Storage is the only
 * place both worlds, and a following document of the same origin, can read, and
 * the page can read it too, so the key carries nothing of the request nonce and
 * the entries hold only a time, a method, a status and an origin and path.
 */
export const OBS_KEY = "aj.obs";

export const OBS_MAX = 50;
export const OBS_TTL_MS = 10 * 60 * 1000;
