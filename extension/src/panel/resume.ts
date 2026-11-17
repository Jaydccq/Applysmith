// The Résumé tab: the variants the application holds, which one fits this
// posting, and a pin that overrides the choice for this page. The Autofill tab
// uploads whatever this tab shows as chosen. Below them, the Documents section
// offers a tailored résumé or cover letter for the report this tab works on.
import { h, redraw } from "../shared/dom";
import { pageJob, postingOf } from "../shared/capture";
import type { Connection, DocsStarted, DocsView, PostingHint, ResumeRankView, ResumeState, StateView } from "../shared/messages";
import { failureOf, send } from "../shared/rpc";
import { notConnected, SHOWN } from "./shell";
import { surfaceDocuments } from "../shared/site";

const DOCS_POLL_MS = 5_000;

export interface ResumeTab {
  setConnection(c: Connection, checked?: boolean): void;
  dispose(): void;
}

export function mountResume(el: HTMLElement, doc: Document = document): ResumeTab {
  let connection: Connection = "offline";
  let checked = false;
  let state: ResumeState | undefined;
  let ranking: ResumeRankView | undefined;
  let rankNote = "";
  let error = "";
  let loading = false;
  let epoch = 0;
  // A request that answers after dispose re-arms nothing.
  let disposed = false;
  let docs: DocsView | undefined;
  let docsNote = "";
  // The dashboard's page for a run that was already writing a document for this job.
  let docsLink = "";
  // A document run started here: both buttons hold from the press until it ends, as the
  // application runs one document at a time for a job ("" while the start is on its way).
  let docsRun: string | undefined;
  let docsTimer: ReturnType<typeof setTimeout> | undefined;

  async function load(): Promise<void> {
    const mine = ++epoch;
    loading = true;
    error = rankNote = "";
    ranking = undefined;
    draw();
    try {
      state = await send<ResumeState>({ kind: "resume.list" });
      if (state.resumes.length > 0) {
        const posting: PostingHint = postingOf(await pageJob(doc));
        try {
          ranking = await send<ResumeRankView>({ kind: "resume.rank", posting });
        } catch (e) {
          rankNote = `Ranking is unavailable (${failureOf(e).message}); the default variant is used.`;
        }
      }
    } catch (e) {
      state = undefined;
      error = failureOf(e).message;
    }
    if (mine !== epoch) return;
    loading = false;
    await loadDocs(mine);
    draw();
  }

  /** The tab's report, when this page may show documents for it. */
  async function loadDocs(mine: number): Promise<void> {
    docsNote = "";
    let view: DocsView | undefined;
    try {
      view = await send<DocsView>({ kind: "docs.get", url: doc.location.href });
    } catch {
      view = undefined;
    }
    if (mine !== epoch) return;
    docs = view?.show && surfaceDocuments(view.inherited === true, doc) ? view : undefined;
  }

  async function generate(kind: "resume" | "cover-letter"): Promise<void> {
    if (!docs?.candidacyId || docsRun !== undefined) return;
    const what = kind === "resume" ? "The tailored résumé" : "The cover letter";
    docsRun = "";
    docsLink = "";
    draw();
    try {
      const run = await send<DocsStarted>({ kind: "docs.start", candidacyId: docs.candidacyId, document: kind });
      docsRun = run.runId;
      if (run.already) {
        const address = await send<StateView>({ kind: "state" }).then((v) => v.address, () => "");
        docsLink = address && `${address}/runs/${encodeURIComponent(run.runId)}`;
        docsNote = "A document for this job is already being written, started elsewhere. Wait for it to finish, then try again.";
      } else docsNote = `${what} is being written. Follow it in the Applysmith application.`;
      follow(docsRun, run.already ? "That document" : what);
    } catch (e) {
      docsRun = undefined;
      docsNote = `Could not start it: ${failureOf(e).message}`;
    }
    draw();
  }

  /** Reads the run every few seconds while it is queued or running; an unreadable run frees the buttons. */
  function follow(runId: string, what: string): void {
    if (disposed) return;
    docsTimer = setTimeout(async () => {
      const status = await send<{ status: string }>({ kind: "docs.run", runId }).then((r) => r.status, () => "");
      if (docsRun !== runId || disposed) return;
      if (status === "queued" || status === "running") return follow(runId, what);
      docsRun = undefined;
      if (status === "succeeded") docsNote = `${what} is written. Open it in the Applysmith application.`;
      else if (status) docsNote = `${what} was not written (${status.replace("_", " ")}). See why in the Applysmith application.`;
      draw();
    }, DOCS_POLL_MS);
  }

  async function dismissDocs(): Promise<void> {
    await send({ kind: "docs.dismiss" }).catch(() => {});
    docs = undefined;
    draw();
  }

  function docsSection(): Node[] {
    if (!docs?.reportNumber) return [];
    const out: Node[] = [
      h("h3", {}, "Documents"),
      h("p", {}, `For evaluated job #${String(docs.reportNumber).padStart(3, "0")}.`),
      h("button", { class: "act", type: "button", disabled: docsRun !== undefined, onclick: () => void generate("resume") }, "Write tailored résumé"),
      h("button", { class: "act", type: "button", disabled: docsRun !== undefined, onclick: () => void generate("cover-letter") }, "Write cover letter"),
    ];
    // A record carried here from the posting can be wrong: the person can let it go.
    if (docs.inherited) out.push(h("button", { class: "act", type: "button", onclick: () => void dismissDocs() }, "Not this job? Dismiss"));
    if (docsNote) out.push(h("p", { role: "status" }, docsNote, docsLink ? h("span", {}, " ", h("a", { href: docsLink, target: "_blank", rel: "noopener" }, "Open the run")) : false));
    return [h("section", { class: "docs", "aria-label": "Documents" }, ...out)];
  }

  async function pin(id: string | null): Promise<void> {
    try {
      await send({ kind: "resume.pin", page: doc.location.href, id });
      if (state) state.pinned = id ?? undefined;
    } catch (e) {
      error = failureOf(e).message;
    }
    draw();
  }

  const chosenId = (): string | undefined => {
    if (!state) return undefined;
    if (state.pinned && state.resumes.some((r) => r.id === state!.pinned)) return state.pinned;
    return ranking?.chosen ?? state.resumes.find((r) => r.default)?.id ?? state.resumes[0]?.id;
  };

  function draw(): void {
    const body: Node[] = [];
    if (connection !== "connected") {
      body.push(notConnected(connection, checked));
    } else if (loading) body.push(h("p", { role: "status" }, "Loading your résumé variants."));
    else if (error) body.push(h("p", { class: "bad", role: "alert" }, `Résumé variants are unavailable: ${error}`), h("button", { class: "act", type: "button", onclick: () => void load() }, "Try again"));
    else if (state && state.resumes.length === 0) body.push(h("p", { role: "status" }, "No résumé variant yet. Add one in the Applysmith application, then reopen this tab."));
    else if (state) {
      const chosen = chosenId();
      const why = (id: string) => (id === state!.pinned ? "pinned by you" : ranking?.ranking.find((r) => r.id === id)?.reason);
      body.push(h("p", { class: "muted" }, "Autofill uploads the chosen variant. The file is fetched when the form asks for it."));
      if (rankNote) body.push(h("p", { class: "bad", role: "status" }, rankNote));
      const items = state.resumes.map((r) =>
        h(
          "li",
          { "data-chosen": r.id === chosen },
          h("strong", {}, r.label),
          r.default ? " (default)" : "",
          r.tags.length ? h("span", { class: "muted" }, ` ${r.tags.join(", ")}`) : false,
          r.id === chosen ? h("span", { class: "ok" }, ` chosen${why(r.id) ? `: ${why(r.id)}` : ""}`) : false,
          " ",
          r.id === state!.pinned ? false : h("button", { class: "act", type: "button", onclick: () => void pin(r.id) }, "Use this one"),
        ),
      );
      body.push(h("ul", { class: "list" }, ...items));
      if (state.pinned) body.push(h("button", { class: "act", type: "button", onclick: () => void pin(null) }, "Use auto-match"));
    }
    if (connection === "connected" && !loading) body.push(...docsSection());
    redraw(el, body);
  }

  draw();
  // A variant added in the application since an empty or failed reading shows when the tab is opened again.
  el.addEventListener(SHOWN, () => {
    if (connection === "connected" && !loading && (error || state?.resumes.length === 0)) void load();
  });
  return {
    setConnection(c, now = false) {
      checked = now;
      const first = connection !== "connected" && c === "connected";
      connection = c;
      if (first) void load();
      else draw();
    },
    dispose() {
      disposed = true;
      epoch++;
      clearTimeout(docsTimer);
    },
  };
}
