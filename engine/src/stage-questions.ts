// Clean-room: docs/provenance.md#CR-ENGINE
// Question pass: scan the page into questions, ask the matcher, apply what
// comes back, and look again for controls the answers revealed.

import { answerControl, makeRow, releasesClaim, REOPEN } from "./answers";
import { dependencyOrder } from "./cascade";
import { flatText, normalize } from "./dom";
import { StopSignal, timerOn } from "./env";
import { blurKind, releaseFocus, tap } from "./gestures";
import type { Host } from "./host";
import {
  MAX_QUESTIONS_PER_REQUEST,
  MAX_REQUEST_BYTES,
  type MatchRequest,
  type MatchResponse,
  type Resolution,
  type WireQuestion,
} from "./protocol";
import type { QuietWatcher } from "./quiet";
import type { Run } from "./run";
import { newPrescan, prescanListboxes } from "./prescan";
import { recognise } from "./profile";
import { isParserHost, rowCaptions, rowControlSelectors, standaloneEducationSelectors } from "./row-plan";
import { scanControls, toQuestion, type ControlInfo } from "./scan";

const MAX_PASSES = 12;
/** The most controls the log names when the matcher left some of them out of its reply. */
const MAX_UNMATCHED_LOGGED = 12;
/** A control asked in this many passes without taking an answer is left alone. */
const MAX_UNFILLED_PASSES = 2;
/** Links the profile owns: where a page's parser mangles one, the applicant's value replaces it. */
const PROFILE_LINKS: ReadonlySet<string> = new Set(["linkedin", "github", "portfolio"]);

export interface QuestionDeps {
  host: Host;
  written: Map<string, string>;
  quiet: QuietWatcher;
}

interface Group {
  rep: ControlInfo;
  question: WireQuestion;
  members: ControlInfo[];
}

function shapeKey(q: WireQuestion): string {
  return `${q.selector}|${q.kind}|${normalize(q.text)}|${q.options.join("|")}`;
}

/** What a question asks, whichever control carries it. */
function contentKey(q: WireQuestion): string {
  return `${q.kind}|${normalize(q.text)}|${q.options.join("|")}`;
}

/**
 * Questions with the same normalized label, kind and options are asked once.
 * A row control is asked on its own: each row holds its own entry's value.
 */
function collapseQuestions(infos: ControlInfo[], inRows: Set<string>, captions: Map<string, string>): Group[] {
  const groups = new Map<string, Group>();
  for (const info of infos) {
    const q = toQuestion(info);
    if (!q) continue;
    const caption = captions.get(info.selector);
    if (caption && !q.section?.includes(caption)) q.section = [...(q.section ?? []), caption];
    const key = inRows.has(info.selector) ? `row|${info.selector}` : contentKey(q);
    const g = groups.get(key);
    if (g) g.members.push(info);
    else groups.set(key, { rep: info, question: q, members: [info] });
  }
  return Array.from(groups.values());
}

/** Batches of at most 300 questions, split further until each body fits the size bound. */
export function batchQuestions(questions: WireQuestion[], base: Omit<MatchRequest, "questions">): WireQuestion[][] {
  const out: WireQuestion[][] = [];
  const fits = (qs: WireQuestion[]) => JSON.stringify({ ...base, questions: qs }).length <= MAX_REQUEST_BYTES;
  const push = (qs: WireQuestion[]) => {
    if (qs.length > 1 && !fits(qs)) {
      const mid = Math.ceil(qs.length / 2);
      push(qs.slice(0, mid));
      push(qs.slice(mid));
    } else out.push(qs);
  };
  for (let i = 0; i < questions.length; i += MAX_QUESTIONS_PER_REQUEST) push(questions.slice(i, i + MAX_QUESTIONS_PER_REQUEST));
  return out;
}

type Raced<T> = { value: T } | { timedOut: true };

async function raceDeadline<T>(run: Run, work: Promise<T>, deadline: number): Promise<Raced<T>> {
  let cancel = () => {};
  const timeout = new Promise<Raced<T>>((resolve) => {
    cancel = timerOn(() => run.active(), deadline, () => resolve({ timedOut: true }));
  });
  try {
    return await Promise.race([work.then((value) => ({ value })), timeout]);
  } finally {
    cancel();
  }
}

/** Ashby ships native radio groups with a default checked: the page's own state is told about the answer even when it equals the default. */
function reopen(info: ControlInfo, reopened: Set<string>, site: string | null): boolean {
  if (site === "ashby" && info.kind === "radio-group" && info.toggles.length === 0 && info.answered && !reopened.has(info.selector)) return true;
  if ((info.kind !== "text" && info.kind !== "textarea" && info.kind !== "select" && info.kind !== "custom-listbox") || !info.answered || reopened.has(info.selector)) return false;
  return REOPEN.test(info.label.text);
}

const LEGEND_POLLS = 10;

/** Workday draws each question's legend lazily: the fields are brought into view and given a moment before any text is read. */
async function hydrateLegends(run: Run): Promise<void> {
  if (!run.site?.startsWith("workday")) return;
  const blank = (): Element[] =>
    Array.from(run.doc.querySelectorAll('[data-automation-id^="formField"]')).filter((f) => {
      const legend = f.querySelector("legend");
      return !!legend && flatText(legend).trim() === "" && f.querySelector("input, select, textarea, button") !== null;
    });
  for (let i = 0; i < LEGEND_POLLS; i++) {
    const waiting = blank();
    if (!waiting.length) return;
    for (const f of waiting) (f as HTMLElement).scrollIntoView?.({ block: "center" });
    await run.sleep(run.timing.legendPollMs);
    await run.checkpoint();
  }
}

export async function questionPass(run: Run, d: QuestionDeps): Promise<void> {
  const doc = run.doc;
  const host = doc.defaultView?.location?.hostname ?? "";
  /** Sticky questions: an abstained or review-flagged shape is not sent again unchanged. */
  const sent = new Set<string>();
  /** Passes in which a selector was asked and still holds no answer; two make it stuck. */
  const unfilled = new Map<string, number>();
  const stuck = new Set<string>();
  /** Answers a real `filled` result gave this run, by question content, for another control that asks the same thing. */
  const answered = new Map<string, Resolution>();
  const reopened = new Set<string>();
  /** Claims given back to the profile pass stay given back on every later pass. */
  const released = new Set<string>();
  const prescan = newPrescan();
  let passDeadline = run.active() + run.prepassDeadlineMs;
  // The prescan may use half the pass budget, at least 20 s, and gives back what it read when it runs out.
  const prescanBudget = Math.max(20_000, run.prepassDeadlineMs / 2);
  let lastWrote = -1;
  let lastCount = -1;
  let deferred = 0;
  /** Text boxes written and not yet left: a page may draw the next question only once one is (ADP's dijit textarea). */
  let uncommitted: HTMLInputElement[] = [];

  for (let pass = 1; pass <= MAX_PASSES; pass++) {
    await run.checkpoint();
    if (run.active() > passDeadline) {
      run.log.add("question-pass", "deadline", { pass });
      break;
    }
    await hydrateLegends(run);
    const infos = scanControls(doc);
    // A static form: nothing written and nothing new after the settle (listboxes still waiting to be read are new work).
    if (pass > 1 && lastWrote === 0 && infos.length <= lastCount && !deferred) break;
    lastCount = infos.length;

    const inRows = rowControlSelectors(infos, doc);
    // A standalone education box the matcher misses goes back to the profile pass like a row control.
    const rowOwned = new Set([...inRows, ...standaloneEducationSelectors(infos, inRows)]);
    for (const info of infos) if (info.owner === "matcher" && !released.has(info.selector)) run.claimed.add(info.selector);
    deferred = await prescanListboxes(run, infos, prescan, run.active() + Math.min(prescanBudget, Math.max(0, passDeadline - run.active())));
    for (const info of infos) {
      if (info.owner === "matcher" && prescan.failed.has(info.selector) && !run.rows.has(info.selector)) {
        run.setRow(makeRow(info, "bank", "skipped", { reason: "listbox-not-opened" }));
      }
    }
    const order = new Map(infos.map((info, i) => [info.selector, i]));
    const fresh = infos.filter((info) => {
      if (info.owner !== "matcher" || !info.admitted || info.needsOptions) return false;
      if (info.answered && !reopen(info, reopened, run.site)) {
        // A row control the page already filled was never asked: the row's entry may still replace a parser's guess,
        // and so may the applicant's own profile link where the page's parser mangles it.
        if (inRows.has(info.selector) || (isParserHost(run.site) && PROFILE_LINKS.has(recognise(info) ?? ""))) {
          run.claimed.delete(info.selector);
          released.add(info.selector);
        }
        return false;
      }
      const q = toQuestion(info);
      return !!q && !stuck.has(info.selector) && !sent.has(shapeKey(q));
    });
    const groups = collapseQuestions(fresh, inRows, rowCaptions(infos, doc));
    if (!groups.length && uncommitted.length) {
      for (const el of uncommitted) {
        await releaseFocus(el, run.ctx());
        await tap(el, run.ctx());
        run.committed.set(el, el.value);
      }
      uncommitted = [];
      await run.sleep(run.timing.passSettleMs);
      continue;
    }
    if (!groups.length) {
      if (!deferred) break;
      await run.sleep(run.timing.passSettleMs);
      continue;
    }
    for (const g of groups) for (const m of g.members) {
      sent.add(shapeKey(toQuestion(m) as WireQuestion));
      reopened.add(m.selector);
    }
    // A question another control already got a real answer for is answered from that, with no request.
    const results = new Map<string, Resolution>();
    const remote: Group[] = [];
    for (const g of groups) {
      const prior = inRows.has(g.rep.selector) ? undefined : answered.get(contentKey(g.question));
      if (prior) results.set(g.rep.selector, { ...prior, selector: g.rep.selector });
      else remote.push(g);
    }

    const base: Omit<MatchRequest, "questions"> = {
      runId: run.id,
      host,
      posting: run.posting,
      ...(run.coverLetterAttached ? { coverLetterAttached: true as const } : {}),
      ...(run.resumeAttached ? { resumeAttached: true as const } : {}),
    };
    let failed: string | null = null;
    const returned = new Set<string>();
    for (const batch of batchQuestions(remote.map((g) => g.question), base)) {
      await run.checkpoint();
      let r: Raced<MatchResponse> | { error: unknown };
      try {
        r = await raceDeadline<MatchResponse>(run, run.until(d.host.match({ ...base, questions: batch })), passDeadline);
      } catch (e) {
        if (e instanceof StopSignal) throw e;
        r = { error: e };
      }
      if ("error" in r) failed = r.error instanceof Error ? r.error.message : String(r.error);
      else if ("timedOut" in r) failed = "pass deadline";
      else for (const res of r.value.results ?? []) results.set(res.selector, res);
      if (failed) break;
      for (const q of batch) returned.add(q.selector);
      // The pass deadline is rebased onto the apply phase once a response has arrived.
      passDeadline = run.active() + run.prepassDeadlineMs;
    }
    // Questions whose batch never came back: the matcher gave no verdict on them.
    const unasked = new Set<Group>();
    if (failed) {
      run.log.add("question-pass", "match-failed", { reason: failed, pass });
      const none = !remote.some((g) => results.has(g.rep.selector));
      // A wedged pass is slow, not absent: only an error from the binding marks the matcher unavailable.
      const timedOut = failed === "pass deadline";
      if (none && !timedOut) run.matchUnavailable = true;
      const reason = timedOut ? "timeout" : "binding-unavailable";
      for (const g of remote) {
        if (!none && returned.has(g.rep.selector)) continue;
        unasked.add(g);
        for (const m of g.members) run.setRow(makeRow(m, "bank", "skipped", { reason }));
      }
      if (none) break;
    }

    d.quiet.start();
    let wrote = 0;
    const unmatched: string[] = [];
    let exhausted = false;
    for (const g of dependencyOrder(
      groups.sort((a, b) => (order.get(a.rep.selector) ?? 0) - (order.get(b.rep.selector) ?? 0)),
      (x) => x.rep.label.text,
    )) {
      if (unasked.has(g)) continue;
      const res = results.get(g.rep.selector);
      for (const info of g.members) {
        await run.checkpoint();
        if (run.active() > passDeadline) {
          // The budget for the apply phase is spent: what is left is marked, not silently dropped.
          exhausted = true;
          run.setRow(makeRow(info, "bank", "failed", { reason: "timeout" }));
          continue;
        }
        if (!res) {
          if (releasesClaim(info, undefined, rowOwned.has(info.selector))) {
            run.claimed.delete(info.selector);
            released.add(info.selector);
          }
          unmatched.push(info.label.text.slice(0, 80));
          run.setRow(makeRow(info, "bank", "abstained", { reason: "no-match" }));
          continue;
        }
        const row = await answerControl(run, info, { ...res, selector: info.selector }, d.written, passDeadline);
        if (releasesClaim(info, res, rowOwned.has(info.selector))) {
          run.claimed.delete(info.selector);
          released.add(info.selector);
        }
        run.setRow(row);
        if (row.outcome === "filled") {
          wrote++;
          if (blurKind(row.kind, info.el as HTMLInputElement) === "text") uncommitted.push(info.el as HTMLInputElement);
          unfilled.delete(info.selector);
          if ((res.outcome === "filled" || res.outcome === "filled-model") && !inRows.has(g.rep.selector)) answered.set(contentKey(g.question), res);
        } else {
          unfilled.set(info.selector, (unfilled.get(info.selector) ?? 0) + 1);
          if ((unfilled.get(info.selector) ?? 0) >= MAX_UNFILLED_PASSES) stuck.add(info.selector);
        }
        // Only an abstain or a review-flagged answer is remembered; any other outcome may be asked again if the control still needs it.
        if (row.outcome !== "abstained" && !row.review) sent.delete(shapeKey(toQuestion(info) as WireQuestion));
        await d.quiet.settle(run);
      }
    }
    lastWrote = wrote;
    // The one way to see afterwards which controls the matcher dropped: labels only, never a value.
    if (unmatched.length) run.log.add("question-pass", "unmatched", { pass, count: unmatched.length, controls: unmatched.slice(0, MAX_UNMATCHED_LOGGED) });
    run.log.add("question-pass", "pass", { pass, asked: groups.length, wrote });
    if (exhausted) {
      run.log.add("question-pass", "apply-budget-exhausted", { pass });
      break;
    }
    if (failed) break;
    await run.sleep(run.timing.passSettleMs);
  }
  // A row control the matcher never answered (a row's "I currently work here" box) belongs to its row's entry.
  for (const sel of rowControlSelectors(scanControls(doc), doc)) {
    if (run.claimed.has(sel) && !run.rows.has(sel)) run.claimed.delete(sel);
  }
}
