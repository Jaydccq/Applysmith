// Clean-room: docs/provenance.md#CR-ENGINE
// Frame fan-out decisions. The host walks the page's sub-frames; this module
// holds the pure rules it applies and the side-effect-free probe it runs inside
// a frame.

import type { FrameDecision, LearningEvent, ProbeResult } from "./protocol";

export type FrameClass = "registry" | "denylist" | "probe" | "not-http";

export function hostIs(host: string, domain: string): boolean {
  return host === domain || host.endsWith(`.${domain}`);
}

function parse(url: string): URL | null {
  try {
    return new URL(url);
  } catch {
    return null;
  }
}

/** Origin and path only: a query can carry applicant data. Opaque URLs pass through. */
export function reduceUrl(url: string): string {
  const u = parse(url);
  if (!u || !/^https?:$/.test(u.protocol)) return url;
  return `${u.origin}${u.pathname}`;
}

/** The SuccessFactors domains, one list for the frame registry and the site table. */
export const SUCCESSFACTORS_DOMAINS = ["successfactors.com", "successfactors.eu", "sapsf.com", "sapsf.eu", "ns2cloud.com"];

const REGISTRY: ((u: URL) => boolean)[] = [
  // Greenhouse: job-boards on any path, boards and every other greenhouse host on the embed path.
  (u) => /^job-boards(\.eu)?\.greenhouse\.io$/.test(u.hostname),
  (u) => hostIs(u.hostname, "greenhouse.io") && u.pathname.startsWith("/embed/"),
  // Lever: the whole host family.
  (u) => hostIs(u.hostname, "lever.co"),
  // Ashby starts on the job-view route and navigates in the document, so no path anchor.
  (u) => u.hostname === "jobs.ashbyhq.com",
  (u) => hostIs(u.hostname, "ashbyhq.com") && u.pathname.startsWith("/embed/"),
  (u) => hostIs(u.hostname, "icims.com") && u.searchParams.get("in_iframe") === "1",
  (u) => /^absteraim-[a-z0-9-]+\.dispatcher\.hana\.ondemand\.com$/.test(u.hostname),
  (u) => (hostIs(u.hostname, "paycor.com") || hostIs(u.hostname, "recruitingbypaycor.com")) && u.pathname.startsWith("/career/"),
  (u) => hostIs(u.hostname, "linkedin.com") && u.pathname.startsWith("/preload"),
  (u) => SUCCESSFACTORS_DOMAINS.some((d) => hostIs(u.hostname, d)),
  // Form builders: respondent routes only, never an editor or author surface.
  (u) => u.hostname === "tally.so" && /^\/(r|embed)\//.test(u.pathname),
  (u) => hostIs(u.hostname, "typeform.com") && u.pathname.startsWith("/to/"),
  (u) => hostIs(u.hostname, "jotform.com") && /^\/\d+/.test(u.pathname),
  (u) => u.hostname === "docs.google.com" && /^\/forms\/.*\/viewform$/.test(u.pathname),
  (u) => hostIs(u.hostname, "fillout.com") && u.pathname.startsWith("/t/"),
  (u) => hostIs(u.hostname, "airtable.com") && /^\/(embed\/)?shr[A-Za-z0-9]+/.test(u.pathname),
  (u) => /^(?!www\.|help\.|blog\.|support\.|app\.)[a-z0-9-]+\.paperform\.co$/.test(u.hostname),
  (u) => hostIs(u.hostname, "cognitoforms.com") && u.pathname.startsWith("/f/"),
];

// A probe-cost filter, not a security boundary.
const DENYLIST: ((u: URL) => boolean)[] = [
  (u) => ["doubleclick.net", "googletagmanager.com", "google-analytics.com", "googlesyndication.com", "hotjar.com", "segment.com", "segment.io", "intercom.io", "intercom.com", "recaptcha.net", "hcaptcha.com"].some((d) => hostIs(u.hostname, d)),
  (u) => hostIs(u.hostname, "facebook.com") && /^\/(tr|plugins)(\/|$)/.test(u.pathname),
  (u) => (hostIs(u.hostname, "youtube.com") || hostIs(u.hostname, "youtube-nocookie.com")) && u.pathname.startsWith("/embed"),
  (u) => hostIs(u.hostname, "google.com") && u.pathname.startsWith("/recaptcha"),
  (u) => u.hostname === "challenges.cloudflare.com",
];

export function classifyFrame(url: string): FrameClass {
  if (url === "about:blank" || url === "about:srcdoc") return "probe";
  const u = parse(url);
  if (!u || !/^https?:$/.test(u.protocol)) return "not-http";
  if (REGISTRY.some((t) => t(u))) return "registry";
  if (DENYLIST.some((t) => t(u))) return "denylist";
  return "probe";
}

/** Decision for a frame that needs no probe, or null when the host must probe it. */
export function staticDecision(frameId: string, url: string): FrameDecision | null {
  const cls = classifyFrame(url);
  const base = { frameId, url: reduceUrl(url) };
  if (cls === "registry") return { ...base, outcome: "admitted-registry" };
  if (cls === "denylist") return { ...base, outcome: "skipped-denylist" };
  if (cls === "not-http") return { ...base, outcome: "skipped-denylist", reason: "not-http" };
  return null;
}

export function probeDecision(frameId: string, url: string, probe: ProbeResult): FrameDecision {
  const admit = probe.controls >= 2 || probe.required >= 1;
  return { frameId, url: reduceUrl(url), outcome: admit ? "admitted-probe" : "skipped-zero-controls", controls: probe.controls };
}

export function failedDecision(
  frameId: string,
  url: string,
  outcome: "failed-probe" | "failed-inject" | "failed-no-listener",
  reason: string,
): FrameDecision {
  return { frameId, url: reduceUrl(url), outcome, reason };
}

/** The page a fill ran on, as an unreachable frame's event names it: host, and origin and path only. */
export interface FramePage {
  host: string;
  url: string;
  site?: string | null;
}

/** A frame's address as an event may carry it: origin and path of a web page, the bare scheme of anything else. */
function frameLabel(url: string): string {
  const u = parse(url);
  if (u && /^https?:$/.test(u.protocol)) return `${u.origin}${u.pathname}`;
  return u?.protocol === "about:" ? `about:${u.pathname}` : "(opaque frame)";
}

/**
 * One learning event for each frame the fill FAILED to enter (never a frame
 * skipped on purpose), so a provider that keeps being unreachable surfaces and
 * can be added to the registry. It is tagged `iframe` and carries the frame's
 * origin and path, the failure outcome as its reason, and nothing else: no
 * value, no option, no query string.
 */
export function unreachableFrameEvents(decisions: FrameDecision[], page: FramePage, newId: () => string, now: number): LearningEvent[] {
  return decisions
    .filter((d) => d.outcome.startsWith("failed-"))
    .map((d) => {
      const ev: LearningEvent = {
        eventId: newId(),
        kind: "frame-unreachable",
        host: page.host,
        url: reduceUrl(page.url),
        question: `Application form frame could not be reached: ${frameLabel(d.url)}`,
        controlKind: "iframe",
        time: now,
        reason: d.outcome,
      };
      if (page.site) ev.site = page.site;
      return ev;
    });
}

/** The roll-up line of a walk over the page's frames. */
export function frameRollup(decisions: FrameDecision[]): Record<string, number> {
  const n = (p: string) => decisions.filter((d) => d.outcome.startsWith(p)).length;
  return { frames: decisions.length, admitted: n("admitted-"), skipped: n("skipped-"), unreached: n("failed-") };
}
