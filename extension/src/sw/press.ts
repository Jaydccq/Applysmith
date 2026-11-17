// The press claim for the step walker's final submit. The server runs the
// duplicate guard and the claim; this file only asks and passes the answer on.
// Anything but an explicit grant is a refusal, an unreachable server included,
// so a failure here can never become a press.
import type { PressClaimGranted } from "../../../web/src/wire";
import { reduceUrl } from "../../../engine/src/frames";
import { ApiError, OFFLINE } from "./api";
import { on } from "./router";
import { api } from "./state";
import { record } from "./submissions";
import type { PressDecision } from "../shared/messages";

const REFUSED_TEXT = "Submit was not pressed.";

function refusal(e: unknown): PressDecision {
  if (e instanceof ApiError && e.code === OFFLINE) return { granted: false, message: `The Applysmith application did not answer, so Submit was not pressed.` };
  if (e instanceof ApiError) return { granted: false, message: `${e.message} ${REFUSED_TEXT}` };
  return { granted: false, message: `The press could not be cleared. ${REFUSED_TEXT}` };
}

export function registerPress(): void {
  on("press.claim", async (m, s): Promise<PressDecision> => {
    const page = s.tab?.url;
    if (!page) return { granted: false, message: `There is no page to clear. ${REFUSED_TEXT}` };
    try {
      const url = reduceUrl(page);
      const body = { url, host: new URL(url).hostname, ...(m.company ? { company: m.company } : {}), ...(m.role ? { role: m.role } : {}) };
      const granted = await api.call<PressClaimGranted | undefined>("/api/submissions/claims", { method: "POST", body });
      return typeof granted?.claimId === "string" && granted.claimId !== "" ? { granted: true, claimId: granted.claimId } : { granted: false, message: `The application did not grant the press. ${REFUSED_TEXT}` };
    } catch (e) {
      return refusal(e);
    }
  });
  // The press was made: record it with its claim. A failed send waits in the
  // same queue as an observed submission.
  on("press.report", (m, s) => record({ ...m.entry, claimId: m.claimId }, s.tab?.id));
}
