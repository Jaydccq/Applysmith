import type { Failure, Reply, Request } from "./messages";

export class RpcError extends Error {
  constructor(readonly failure: Failure) {
    super(failure.message);
  }
}

export class WorkerDisconnected extends RpcError {
  constructor(failure: Failure, readonly invalidated: boolean) {
    super(failure);
  }
}

/** Asks the service worker; rejects with the worker's failure. */
export async function send<T = unknown>(req: Request): Promise<T> {
  let reply: Reply<T> | undefined;
  try {
    reply = (await chrome.runtime.sendMessage(req)) as Reply<T> | undefined;
  } catch (e) {
    const invalidated = e instanceof Error && /extension context invalidated/i.test(e.message);
    throw new WorkerDisconnected({ code: "unavailable", message: invalidated ? "The extension was reloaded. Reload this page." : "The extension worker disconnected." }, invalidated);
  }
  if (!reply) throw new RpcError({ code: "unavailable", message: "The extension did not answer." });
  if (!reply.ok) throw new RpcError(reply.error);
  return reply.data;
}

export function failureOf(e: unknown): Failure {
  return e instanceof RpcError ? e.failure : { code: "internal", message: "Something went wrong." };
}
