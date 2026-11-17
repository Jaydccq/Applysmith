// Pairing: a one-time code typed into the popup, or the token pasted by hand.
// Either ends in the same bearer token, kept only in worker storage. The code
// the dashboard shows carries its port, so pairing also finds the application.
import { Api, ApiError } from "./api";
import * as store from "./store";

const CLAIM_BLOCK = "claimBlockedUntil";
/** A standard route, used to prove a token is accepted. */
const VERIFY_PATH = "/api/answer-bank/settings";

interface Claimed {
  token: string;
}

/** Seconds a throttled pairing must still wait; zero when it may try. */
export async function claimWait(now = Date.now()): Promise<number> {
  const got = await chrome.storage.session.get(CLAIM_BLOCK);
  const until = Number(got[CLAIM_BLOCK] ?? 0);
  return until > now ? Math.ceil((until - now) / 1000) : 0;
}

async function verified(api: Api, token: string): Promise<void> {
  await store.put("token", token);
  try {
    await api.call(VERIFY_PATH);
  } catch (e) {
    await store.put("token", undefined);
    throw e;
  }
}

export function normalizeCode(raw: string): string {
  return raw.toUpperCase().replace(/[\s-]/g, "");
}

/** `54712-ABCDEFGHJK` is the code for the application on loopback port 54712; a bare code keeps the saved address. */
export function parseCode(raw: string): { code: string; origin?: string } {
  const m = /^\s*(\d{1,5})\s*-(.*)$/s.exec(raw);
  const port = Number(m?.[1]);
  if (!m || port < 1 || port > 65535) return { code: normalizeCode(raw) };
  return { code: normalizeCode(m[2]!), origin: `http://127.0.0.1:${port}` };
}

/** Redeems a pairing code at the address it carries, else at the saved one; the address is saved only once paired. */
export async function claimCode(api: Api, raw: string): Promise<void> {
  const wait = await claimWait();
  if (wait > 0) throw new ApiError("rate_limited", `Too many tries. Wait ${wait} s.`, 429, wait);
  const { code, origin } = parseCode(raw);
  if (code.length === 0) throw new ApiError("validation_failed", "Type the code the dashboard shows.", 422);
  const at = origin ? new Api({ address: async () => origin, token: () => store.get("token"), rejected: async () => {} }) : api;
  try {
    const got = await at.call<Claimed>("/api/pairing/claim", { method: "POST", body: { code }, anonymous: true });
    await verified(at, got.token);
    if (origin) await store.put("address", origin);
  } catch (e) {
    if (e instanceof ApiError && e.code === "rate_limited") {
      await chrome.storage.session.set({ [CLAIM_BLOCK]: Date.now() + (e.retryAfter ?? 60) * 1000 });
    }
    throw e;
  }
}

/** Accepts the token that `applysmith pair` printed. */
export async function pasteToken(api: Api, raw: string): Promise<void> {
  const token = raw.trim();
  if (!/^[A-Za-z0-9_-]{43,}$/.test(token)) {
    throw new ApiError("validation_failed", "That does not look like the token `applysmith pair` prints.", 422);
  }
  await verified(api, token);
}

export async function forget(): Promise<void> {
  await store.put("token", undefined);
}
