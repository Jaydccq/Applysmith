import { post } from "../../api/client";
import { applyTabShown } from "../../api/decoders_apply";

// The server brings the tab a run kept for the user to the front. It answers
// only for an open kept tab, so the pages enable the control from keptTab.
export const showTab = (runId: string) => post(`/api/apply/runs/${encodeURIComponent(runId)}/show-tab`, applyTabShown);
