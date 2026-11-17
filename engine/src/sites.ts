// Clean-room: docs/provenance.md#CR-ENGINE
// The site table: which job source or application system a URL belongs to.
// Every pattern accepts http and https only, ends its host at a boundary (the
// host is the domain or a subdomain of it, so `smartrecruiters.com.example.test`
// is nobody) and states a path anchor where the host alone is ambiguous. A
// system that serves each tenant on its own subdomain matches no bare or www
// host: that is the vendor's marketing site.

import { hostIs, SUCCESSFACTORS_DOMAINS } from "./frames";

type SiteKind = "source" | "ats";

export interface Site {
  id: string;
  kind: SiteKind;
  match: (u: URL) => boolean;
}

const sub = (u: URL, domain: string): boolean => hostIs(u.hostname, domain);
/** A tenant's own host under the domain: the bare domain and its www host are the vendor's marketing site. */
const tenant = (u: URL, domain: string): boolean => sub(u, domain) && u.hostname !== domain && u.hostname !== `www.${domain}`;
const exact = (u: URL, host: string): boolean => u.hostname === host;
const path = (u: URL, re: RegExp): boolean => re.test(u.pathname);

export const SITES: readonly Site[] = [
  // Job sources.
  { id: "linkedin", kind: "source", match: (u) => sub(u, "linkedin.com") && path(u, /^\/jobs(\/|$)/) },
  { id: "builtin", kind: "source", match: (u) => sub(u, "builtin.com") && path(u, /^\/job(s)?(\/|$)/) },
  { id: "indeed-apply", kind: "source", match: (u) => exact(u, "smartapply.indeed.com") },
  { id: "indeed", kind: "source", match: (u) => sub(u, "indeed.com") && !exact(u, "smartapply.indeed.com") },
  { id: "jobright", kind: "source", match: (u) => sub(u, "jobright.ai") },
  { id: "newgrad", kind: "source", match: (u) => sub(u, "newgrad-jobs.com") },
  { id: "wellfound", kind: "source", match: (u) => sub(u, "wellfound.com") && path(u, /\/jobs?(\/|$)/) },
  // Application systems. Greenhouse needs the numeric job id or the embed route: a bare board is a listing.
  { id: "greenhouse", kind: "ats", match: (u) => /^(job-)?boards\.greenhouse\.io$/.test(u.hostname) && path(u, /\/jobs\/\d+/) },
  { id: "greenhouse-eu", kind: "ats", match: (u) => /^(job-)?boards\.eu\.greenhouse\.io$/.test(u.hostname) && path(u, /\/jobs\/\d+/) },
  { id: "greenhouse-embed", kind: "ats", match: (u) => sub(u, "greenhouse.io") && path(u, /^\/embed\/job_app$/) && u.searchParams.has("token") },
  { id: "lever", kind: "ats", match: (u) => exact(u, "jobs.lever.co") || exact(u, "jobs.eu.lever.co") },
  { id: "ashby", kind: "ats", match: (u) => exact(u, "jobs.ashbyhq.com") },
  { id: "workday", kind: "ats", match: (u) => sub(u, "myworkdayjobs.com") },
  { id: "workday-site", kind: "ats", match: (u) => sub(u, "myworkdaysite.com") },
  { id: "workable", kind: "ats", match: (u) => exact(u, "apply.workable.com") || exact(u, "jobs.workable.com") },
  { id: "rippling", kind: "ats", match: (u) => exact(u, "ats.rippling.com") },
  { id: "hirebridge", kind: "ats", match: (u) => sub(u, "hirebridge.com") },
  { id: "successfactors", kind: "ats", match: (u) => SUCCESSFACTORS_DOMAINS.some((d) => sub(u, d)) },
  { id: "icims", kind: "ats", match: (u) => tenant(u, "icims.com") },
  // The one-click apply form lives on the jobs. subdomain; the bare domain is the marketing site.
  { id: "smartrecruiters", kind: "ats", match: (u) => /^(jobs|careers)\.smartrecruiters\.com$/.test(u.hostname) },
  { id: "taleo", kind: "ats", match: (u) => tenant(u, "taleo.net") },
  { id: "bamboohr", kind: "ats", match: (u) => tenant(u, "bamboohr.com") },
  { id: "jobvite", kind: "ats", match: (u) => exact(u, "jobs.jobvite.com") },
  { id: "personio", kind: "ats", match: (u) => /\.jobs\.personio\.(de|com)$/.test(u.hostname) && path(u, /^\/jobs?(\/|$)/) },
  { id: "teamtailor", kind: "ats", match: (u) => tenant(u, "teamtailor.com") },
  { id: "recruitee", kind: "ats", match: (u) => sub(u, "recruitee.com") && path(u, /^\/o\//) },
  { id: "pinpoint", kind: "ats", match: (u) => sub(u, "pinpointhq.com") && path(u, /^(\/[a-z]{2}(-[A-Za-z]{2})?)?\/postings\//) },
  { id: "eightfold", kind: "ats", match: (u) => sub(u, "eightfold.ai") && path(u, /^\/careers\/apply/) },
  { id: "jazzhr", kind: "ats", match: (u) => (sub(u, "applytojob.com") || sub(u, "resumator.com")) && path(u, /^\/apply(\/|$)/) },
  { id: "breezy", kind: "ats", match: (u) => sub(u, "breezy.hr") && path(u, /^\/p\//) },
  { id: "oracle", kind: "ats", match: (u) => sub(u, "oraclecloud.com") && /\/hcmUI\/CandidateExperience/i.test(u.pathname) },
  { id: "adp-workforcenow", kind: "ats", match: (u) => exact(u, "workforcenow.adp.com") && path(u, /\/recruitment\//) },
  { id: "adp-recruiting", kind: "ats", match: (u) => exact(u, "recruiting.adp.com") || exact(u, "myjobs.adp.com") },
  { id: "dayforce", kind: "ats", match: (u) => exact(u, "jobs.dayforcehcm.com") },
  { id: "ukg", kind: "ats", match: (u) => /^recruiting2?\.ultipro\.com$/.test(u.hostname) || tenant(u, "ukg.com") },
  { id: "paylocity", kind: "ats", match: (u) => exact(u, "recruiting.paylocity.com") },
  { id: "paycom", kind: "ats", match: (u) => /^(www\.)?paycomonline\.net$/.test(u.hostname) && path(u, /\/ats\//) },
];

function parseHttp(url: string): URL | null {
  try {
    const u = new URL(url);
    return u.protocol === "http:" || u.protocol === "https:" ? u : null;
  } catch {
    return null;
  }
}

/** The site id a URL belongs to, or null. Only http and https are considered. */
export function siteOf(url: string): string | null {
  const u = parseHttp(url);
  if (!u) return null;
  return SITES.find((s) => s.match(u))?.id ?? null;
}

const JOB_TOKENS = ["apply", "applications?", "careers?", "jobs?", "openings", "gh_jid", "greenhouse", "lever", "ashby", "workday", "smartrecruiters", "icims", "taleo", "jobvite", "recruit[a-z]*", "hiring", "join-us", "positions?", "requisition", "vacanc(?:y|ies)"];
const JOB_PAGE = new RegExp(`(?:^|[^a-z0-9])(?:${JOB_TOKENS.join("|")})(?:[^a-z0-9]|$)`);

/**
 * A permissive guess for hosts outside the table: does the path or the query
 * read like a job or application page? The host is not looked at, so a
 * `jobs.` label alone proves nothing.
 */
export function looksLikeJobPage(url: string): boolean {
  const u = parseHttp(url);
  if (!u) return false;
  const text = `${u.pathname}?${u.search.replace(/^\?/, "")}`.toLowerCase();
  return JOB_PAGE.test(text);
}
