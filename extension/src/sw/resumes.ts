// Résumé variants: the list, the ranking against a posting, the page's pin and
// the document the Autofill tab uploads. Bytes are fetched when the engine asks
// for them and handed straight over; nothing about a document is stored.
import { reduceUrl } from "../../../engine/src/frames";
import type { ResumePayload } from "../../../engine/src/protocol";
import { ApiError } from "./api";
import { on } from "./router";
import { api } from "./state";
import * as store from "./store";
import type { ResumeList, ResumeRankRequest, RunAccepted } from "../../../web/src/wire";
import type { PostingHint, ResumeRankView, ResumeState, ResumeVariant, DocsStarted, DocsView } from "../shared/messages";
import { lookup } from "./analyze";

interface Pin {
  page: string;
  id: string;
  /** The pinned variant's file, so a pin outlives a renamed id. */
  fileName?: string;
}

const pinKey = (tabId: number) => `pin:${tabId}`;

async function list(): Promise<ResumeVariant[]> {
  return (await api.call<ResumeList>("/api/resumes")).resumes;
}

async function rank(posting: PostingHint): Promise<ResumeRankView> {
  const body: ResumeRankRequest = { title: (posting.title ?? "").slice(0, 500), company: (posting.company ?? "").slice(0, 500), description: (posting.jdText ?? "").slice(0, 20_000) };
  return api.call<ResumeRankView>("/api/resumes/rank", { method: "POST", body });
}

async function pinOf(tabId: number, page: string): Promise<Pin | undefined> {
  const pin = await store.sessionGet<Pin>(pinKey(tabId));
  return pin && pin.page === page ? pin : undefined;
}

/** The roster variant a pin names: by id, then by file name. A pin that names neither is stale and chooses nothing. */
export function pinnedVariant(pin: Pin | undefined, resumes: ResumeVariant[]): ResumeVariant | undefined {
  if (!pin) return undefined;
  return resumes.find((r) => r.id === pin.id) ?? (pin.fileName ? resumes.find((r) => r.fileName === pin.fileName) : undefined);
}

/** The variant the upload will use: the page's pin, else the API's ranking, else the default. */
export async function chosenFor(tabId: number, page: string, posting: PostingHint, resumes: ResumeVariant[]): Promise<ResumeVariant | undefined> {
  const byPin = pinnedVariant(await pinOf(tabId, page), resumes);
  if (byPin) return byPin;
  try {
    const ranked = await rank(posting);
    const byRank = resumes.find((r) => r.id === ranked.chosen);
    if (byRank) return byRank;
  } catch (e) {
    if (!(e instanceof ApiError)) throw e;
  }
  return resumes.find((r) => r.default) ?? resumes[0];
}

/** The document for the engine's résumé binding, or null when there is none to give. */
export async function payloadFor(tabId: number, page: string, posting: PostingHint): Promise<ResumePayload | null> {
  const resumes = await list();
  const pick = await chosenFor(tabId, page, posting, resumes);
  if (!pick) return null;
  // The page's origin goes with the request: a demo workspace audits each upload of its persona's document.
  const path = `/api/resumes/${encodeURIComponent(pick.id)}/file?for=${encodeURIComponent(originOf(page))}`;
  return { fileName: pick.fileName ?? `${pick.id}.pdf`, mediaType: "application/pdf", contentBase64: await api.bytes(path, "application/pdf") };
}

function originOf(page: string): string {
  try {
    return new URL(page).origin;
  } catch {
    return "";
  }
}

export function registerResumes(): void {
  on("resume.list", async (_m, s): Promise<ResumeState> => {
    const tabId = s.tab?.id;
    const page = reduceUrl(s.tab?.url ?? "");
    const resumes = await list();
    return { resumes, pinned: tabId === undefined ? undefined : pinnedVariant(await pinOf(tabId, page), resumes)?.id };
  });
  on("resume.rank", (m) => rank(m.posting));
  on("resume.pin", async (m, s) => {
    if (s.tab?.id === undefined) throw new ApiError("bad_request", "That needs a page.");
    if (m.id === null) await store.sessionRemove(pinKey(s.tab.id));
    else {
      const fileName = (await list().catch(() => [] as ResumeVariant[])).find((r) => r.id === m.id)?.fileName;
      await store.sessionPut(pinKey(s.tab.id), { page: reduceUrl(m.page), id: m.id, ...(fileName ? { fileName } : {}) } satisfies Pin);
    }
  });
}

// The report a tab is working on, for the Documents section of the Résumé tab.
// A page whose own lookup finds an evaluated report binds that report to its
// tab; a tab opened from it (the posting's Apply button), or the same tab once
// it has moved to another page, inherits the record. An inherited record is
// shown only on a page that reads as an application form (the panel decides).
/** A tab's record: the report and tracker row, and the page that showed them. */
interface TabReport {
  reportNumber: number;
  candidacyId: number;
  page: string;
}

const key = (tabId: number) => `docs:${tabId}`;

/** The report for a page in a tab: its own lookup's, else the one the tab carried here. */
export async function docsFor(tabId: number, url: string): Promise<DocsView> {
  const page = reduceUrl(url);
  const found = await lookup(url, false);
  if (found.found && found.reportNumber && found.candidacyId) {
    const own: TabReport = { reportNumber: found.reportNumber, candidacyId: found.candidacyId, page };
    await store.sessionPut(key(tabId), own);
    return { show: true, reportNumber: own.reportNumber, candidacyId: own.candidacyId, inherited: false };
  }
  const held = await store.sessionGet<TabReport>(key(tabId));
  if (!held) return { show: false };
  return { show: true, reportNumber: held.reportNumber, candidacyId: held.candidacyId, inherited: held.page !== page };
}

export function registerDocs(): void {
  on("docs.get", async (m, s) => {
    if (s.tab?.id === undefined) throw new ApiError("bad_request", "That needs a page.");
    return docsFor(s.tab.id, m.url);
  });
  // "Not this job": the tab forgets the report it carried.
  on("docs.dismiss", async (_m, s) => {
    if (s.tab?.id !== undefined) await store.sessionRemove(key(s.tab.id));
  });
  // A run already writing a document for the job (started in the dashboard) is
  // handed back to follow, marked as not this press's. The reason is
  // wire.ReasonDocumentRunActive (the bundle takes no values from the wire).
  on("docs.start", async (m): Promise<DocsStarted> => {
    const path = m.document === "cover-letter" ? "/api/documents/cover-letter" : "/api/documents/resume";
    try {
      return await api.call<RunAccepted>(path, { method: "POST", body: { candidacyId: m.candidacyId }, idempotencyKey: `ext_${crypto.randomUUID()}` });
    } catch (e) {
      if (e instanceof ApiError && e.reason === "document-run-active" && e.runId) return { runId: e.runId, already: true };
      throw e;
    }
  });
  // Whether a document run the panel started still works; only its status leaves the worker.
  on("docs.run", async (m) => ({ status: (await api.call<{ status: string }>(`/api/runs/${encodeURIComponent(m.runId)}`)).status }));
  // The posting's Apply button opens the form in a new tab, which inherits the posting's report.
  chrome.webNavigation.onCreatedNavigationTarget.addListener((d) => {
    void (async () => {
      const held = await store.sessionGet<TabReport>(key(d.sourceTabId));
      if (held) await store.sessionPut(key(d.tabId), held);
    })().catch(() => {});
  });
  chrome.tabs.onRemoved.addListener((tabId) => void store.sessionRemove(key(tabId)).catch(() => {}));
}
