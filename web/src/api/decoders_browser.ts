// Decoders for the Browser page: the lane status, the window controls and the
// sign-in sync view. Each mirrors a struct of internal/api/wire.
import type {
  BrowserEvent, BrowserLane, BrowserLiveCursor, BrowserLiveEnd, BrowserLiveFrame, BrowserLiveTab, BrowserNewTab, BrowserShown, BrowserStatus,
  BrowserStopped, BrowserTab, BrowserTabSent, ForgetResult, ImportedSite, ImportedView, ImportSite, ImportSites, ImportSource, ImportSources,
  SessionImportResult, SessionImportSummary, SessionOutcome, SessionPending, SessionSiteView, SessionsView, SessionUndoResult,
} from "../wire";
import { bool, num, object, orEmpty, safeInt, str, time, type Decoder } from "./decode";

const tab: Decoder<BrowserTab> = object<BrowserTab>(
  { id: str, title: str, url: str, loading: bool, active: bool, kept: bool },
  { owner: str },
);

const lane: Decoder<BrowserLane> = object<BrowserLane>({ window: bool, tabCap: safeInt, tabs: orEmpty(tab) });

const event: Decoder<BrowserEvent> = object<BrowserEvent>({ at: time, kind: str }, { lane: str, detail: str });

export const browserStatus: Decoder<BrowserStatus> = object<BrowserStatus>(
  { state: str, apply: lane, scan: lane, events: orEmpty(event) },
  { chromeVersion: str, error: str },
);

export const browserShown: Decoder<BrowserShown> = object<BrowserShown>({ shown: bool });

export const browserStopped: Decoder<BrowserStopped> = object<BrowserStopped>({ outcome: str });

// The browser panel: the user's input and navigation, a new tab, and the
// events of the live view stream.
export const browserTabSent: Decoder<BrowserTabSent> = object<BrowserTabSent>({ sent: bool });

export const browserNewTab: Decoder<BrowserNewTab> = object<BrowserNewTab>({ tabId: str });

export const browserLiveFrame: Decoder<BrowserLiveFrame> = object<BrowserLiveFrame>({ jpeg: str, width: num, height: num, offsetTop: num, scale: num });

export const browserLiveTab: Decoder<BrowserLiveTab> = object<BrowserLiveTab>(
  { url: str, title: str, loading: bool, canGoBack: bool, canGoForward: bool, driven: bool },
  { runId: str },
);

export const browserLiveCursor: Decoder<BrowserLiveCursor> = object<BrowserLiveCursor>({ x: num, y: num });

export const browserLiveEnd: Decoder<BrowserLiveEnd> = object<BrowserLiveEnd>({ reason: str });

const siteView: Decoder<SessionSiteView> = object<SessionSiteView>(
  { domain: str, label: str, cookies: safeInt, signIn: str },
  { signInCheckedAt: time, signInUrl: str, sourceCookies: safeInt },
  ["signInCheckedAt", "sourceCookies"],
);

const pending: Decoder<SessionPending> = object<SessionPending>({ requestId: str, domains: orEmpty(str), expiresAt: time });

const outcome: Decoder<SessionOutcome> = object<SessionOutcome>({ state: str, message: str, at: time });

export const importResult: Decoder<SessionImportResult> = object<SessionImportResult>({
  imported: safeInt, failed: safeInt, replaced: safeInt, notInSource: safeInt, truncated: bool,
});

const importSummary: Decoder<SessionImportSummary> = object<SessionImportSummary>({ at: time, domains: orEmpty(str), result: importResult });

export const sessionsView: Decoder<SessionsView> = object<SessionsView>(
  { sites: orEmpty(siteView), undoAvailable: bool },
  { lastCheckin: time, pending, lastOutcome: outcome, lastImport: importSummary },
  ["lastCheckin", "pending", "lastOutcome", "lastImport"],
);

export const sessionPending: Decoder<SessionPending> = pending;

export const sessionUndoResult: Decoder<SessionUndoResult> = object<SessionUndoResult>({ restored: safeInt, failed: safeInt });

// Direct import of the user's own sign-in cookies from an installed browser.
const importSource: Decoder<ImportSource> = object<ImportSource>({ id: str, browserId: str, browserLabel: str, profile: str });

export const importSources: Decoder<ImportSources> = object<ImportSources>({ sources: orEmpty(importSource), supported: bool }, { reason: str });

const importSite: Decoder<ImportSite> = object<ImportSite>({ host: str, cookies: safeInt });

export const importSites: Decoder<ImportSites> = object<ImportSites>({ sites: orEmpty(importSite) });

const importedSite: Decoder<ImportedSite> = object<ImportedSite>({ host: str, at: time, cookies: safeInt });

export const importedView: Decoder<ImportedView> = object<ImportedView>({ sites: orEmpty(importedSite) });

export const forgetResult: Decoder<ForgetResult> = object<ForgetResult>({ cleared: safeInt });

export { importResult as sessionImportResult };
