// Clean-room: docs/provenance.md#CR-ENGINE
// The engine reaches Go (or the extension) only through this interface.

import {
  BINDINGS,
  type BindingReply,
  type MatchRequest,
  type MatchResponse,
  type ResumePayload,
  type ResumeRequest,
  type TerminalNotice,
} from "./protocol";

export interface Host {
  match(req: MatchRequest): Promise<MatchResponse>;
  resume(req: ResumeRequest): Promise<ResumePayload | null>;
  terminal(notice: TerminalNotice): Promise<void>;
}

type Binding = (payload: string) => Promise<string>;

async function call<T>(g: Record<string, unknown>, name: string, req: unknown): Promise<T> {
  const fn = g[name] as Binding | undefined;
  if (typeof fn !== "function") throw new Error(`binding ${name} is not available`);
  const reply = JSON.parse(await fn(JSON.stringify(req))) as BindingReply<T>;
  if (!reply.ok) throw new Error(reply.error);
  return reply.result;
}

/** The host for a page the Go side drives: three named bindings in the isolated world. */
export function bindingHost(g: Record<string, unknown>): Host {
  return {
    match: (req) => call<MatchResponse>(g, BINDINGS.match, req),
    resume: (req) => call<ResumePayload | null>(g, BINDINGS.resume, req),
    terminal: (n) => call<void>(g, BINDINGS.terminal, n),
  };
}
