// The posting facts widgets send along with a request, read from the page once
// and kept for a short while.
import { pageJob } from "../shared/capture";
import type { ComposePosting } from "../shared/messages";

const TTL_MS = 60_000;
const MAX_DESCRIPTION = 20_000;

// Kept per document and address: a single-page site that moves to another job reads that job.
let held: { at: number; doc: Document; href: string; value: Promise<ComposePosting> } | undefined;

export function postingContext(doc: Document = document, now: number = Date.now()): Promise<ComposePosting> {
  const href = doc.location.href;
  if (held && held.doc === doc && held.href === href && now - held.at < TTL_MS) return held.value;
  const value = pageJob(doc).then((c) => ({ company: c.company, role: c.title, description: c.jdText?.slice(0, MAX_DESCRIPTION), applicationUrl: href }));
  held = { at: now, doc, href, value };
  return value;
}
