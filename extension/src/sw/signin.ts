// Credentials and codes for the two page widgets that fill them. Each is asked
// for by the person's click. The password and the code pass through here to the
// page that asked and are neither stored nor logged.
import type { AnswerBankSettings } from "../../../web/src/wire";
import { ApiError } from "./api";
import { on } from "./router";
import { api } from "./state";

export interface SignInCredential {
  email: string;
  password: string;
  source: string;
}

export interface LinkMail {
  url: string;
  host: string;
  receivedAt: string;
  from: string;
}

export interface CodeMail {
  code: string;
  receivedAt: string;
  from: string;
}

/**
 * The mail domain a site's verification codes come from, as the apply chain
 * knows them (internal/apply codeSenders): a host matches by equality or as a
 * subdomain. A site not listed takes any sender.
 */
const CODE_SENDERS: [host: string, sender: string][] = [
  ["adp.com", "adp.com"],
  ["greenhouse.io", "greenhouse-mail.io"],
  ["myworkdayjobs.com", "otp.workday.com"],
  ["myworkdaysite.com", "otp.workday.com"],
  ["myworkday.com", "otp.workday.com"],
];

function codeSenderFor(page: string | undefined): string | undefined {
  let host = "";
  try {
    host = new URL(page ?? "").hostname.toLowerCase();
  } catch {
    return undefined;
  }
  return CODE_SENDERS.find(([h]) => host === h || host.endsWith(`.${h}`))?.[1];
}

function pageOf(s: chrome.runtime.MessageSender): string {
  if (!s.tab?.url) throw new ApiError("bad_request", "That needs a page.");
  return s.tab.url;
}

export function registerSignIn(): void {
  on("signin.resolve", (_m, s) => api.call<SignInCredential>("/api/signin/resolve", { method: "POST", body: { url: pageOf(s) } }));
  on("signin.settings", async () => ({ acceptLegalDeclarations: (await api.call<AnswerBankSettings>("/api/answer-bank/settings")).autoAcceptLegalDeclarations === true }));
  // No mail yet is an ordinary answer while the person waits for it.
  on("otp.latest", async (m, s) => {
    const fromDomain = codeSenderFor(s.url ?? s.tab?.url);
    try {
      return await api.call<CodeMail>("/api/gmail/otp/latest", { query: { since: m.since, ...(fromDomain ? { fromDomain } : {}) } });
    } catch (e) {
      if (e instanceof ApiError && e.code === "not_found") return null;
      throw e;
    }
  });
  // The link is only passed on for display; the product never requests it.
  on("signin.link", async (m) => {
    try {
      return await api.call<LinkMail>("/api/gmail/signin-link/latest", { query: { since: m.since } });
    } catch (e) {
      if (e instanceof ApiError && e.code === "not_found") return null;
      throw e;
    }
  });
}
