// Drafts and rewrites of free-text answers. A draft is only ever returned to
// the page; nothing here writes to a form.
import type { AnswerComposeRequest, AnswerComposeResult } from "../../../web/src/wire";
import { ApiError } from "./api";
import { on } from "./router";
import { api } from "./state";
import type { ComposeView, Request } from "../shared/messages";

type ComposeAsk = Extract<Request, { kind: "compose.run" }>;

const inFlight = new Map<string, AbortController>();

const flightKey = (tabId: number, key: string) => `${tabId}:${key}`;

/** One request per card or control at a time; a second while one runs is refused. */
export async function compose(tabId: number, pageUrl: string, ask: ComposeAsk): Promise<ComposeView> {
  const id = flightKey(tabId, ask.key);
  if (inFlight.has(id)) throw new ApiError("conflict", "A request for this field is already running.", 409);
  const ctl = new AbortController();
  inFlight.set(id, ctl);
  const body: AnswerComposeRequest = {
    question: ask.question,
    host: new URL(pageUrl).hostname,
    posting: { company: ask.posting.company, role: ask.posting.role, description: ask.posting.description, applicationUrl: ask.posting.applicationUrl },
  };
  if (ask.maxLength) body.maxLength = ask.maxLength;
  if (ask.current) body.current = ask.current;
  if (ask.instruction) body.instruction = ask.instruction;
  try {
    const out = await api.call<AnswerComposeResult>("/api/answers/compose", { method: "POST", body, signal: ctl.signal });
    return { outcome: out.outcome === "filled-model" ? "filled-model" : "abstained", answer: out.answer, reason: out.reason };
  } catch (e) {
    if (ctl.signal.aborted) throw new ApiError("cancelled", "The request was cancelled.");
    throw e;
  } finally {
    inFlight.delete(id);
  }
}

export function registerAnswers(): void {
  on("compose.run", (m, s) => {
    if (s.tab?.id === undefined || !s.tab.url) throw new ApiError("bad_request", "That needs a page.");
    return compose(s.tab.id, s.tab.url, m);
  });
  on("compose.cancel", async (m, s) => {
    inFlight.get(flightKey(s.tab?.id ?? -1, m.key))?.abort();
  });
}
