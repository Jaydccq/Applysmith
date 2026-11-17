// Clean-room: docs/provenance.md#CR-ENGINE
// Résumé-first: attach the applicant's document before anything else, because
// many application systems parse the upload and re-render the form.

import { makeRow } from "./answers";
import { applyValue } from "./apply";
import { collapse, flatText, normalize, walkElements, type Win } from "./dom";
import { StopSignal, waitFor } from "./env";
import { pointerSequence } from "./gestures";
import type { Host } from "./host";
import { isControlEl } from "./labels";
import { MAX_RESUME_BYTES, type ResumePayload, type Row } from "./protocol";
import type { Run } from "./run";
import { readInfo, scanControls, type ControlInfo } from "./scan";
import { attachFile } from "./writers";
import { attachSfResume, isSfUploader } from "./successfactors";
import { historyCardCount } from "./srcards";
import { ariaHiddenBlocks, isRendered } from "./visibility";

type SlotKind = "resume" | "cover-letter" | "transcript" | "other";

const COVER = /cover letter|covering letter|motivation(al)? letter|letter of (interest|motivation)/;
/** A text box whose label is the letter itself, never a question about one; Go's coverSlot holds the same rule. */
const LETTER_BOX =
  /^(?:(?:paste|write|add|enter) (?:your |a )?)?(?:your )?(?:cover(?:ing)? letter|motivation(?:al)? letter|letter of (?:interest|motivation))(?: here)?(?: (?:required|optional))*$/;
const RESUME = /resume|cv|curriculum vitae/;
/** A slot for the academic transcript ("Unofficial transcript"); one that also names the résumé or the letter is theirs. */
const TRANSCRIPT = /transcript/;
const OTHER_DOC = /portfolio|writing sample|certificate|certification|reference|work sample|id card|passport|photo|picture|headshot|license|licence/;
// A file name ends in an extension glued to its stem; a bare ".pdf" in a list of accepted formats is an instruction, not an upload.
const UPLOADED = /replace file|remove file|delete file|change file|uploaded|[^\s.,/(]\.(pdf|docx?|odt|txt|rtf)\b/i;

function classifyText(text: string): SlotKind | null {
  const t = normalize(text);
  if (!t) return null;
  // A heading that names both documents ("Resume/Cover Letter/CV") stands for the only upload; one that names only a cover letter vetoes.
  if (COVER.test(t) && !RESUME.test(t)) return "cover-letter";
  if (RESUME.test(t)) return "resume";
  if (TRANSCRIPT.test(t)) return "transcript";
  if (OTHER_DOC.test(t)) return "other";
  return null;
}

/** Text of the nearest ancestor scope that holds this file input and no other. */
function scopedText(input: Element): string {
  let node = input.parentElement;
  for (let depth = 0; node && depth < 8; depth++, node = node.parentElement) {
    if (node.querySelectorAll("input[type=file]").length > 1) return "";
    const text = flatText(node, isControlEl);
    if (collapse(text) && text.length <= 300) return text;
  }
  return "";
}

/** What the slot's surroundings say it is: the first scope up to eight levels out that names a document, never past a second upload. */
function scopedKind(input: Element): SlotKind | null {
  let node = input.parentElement;
  for (let depth = 0; node && depth < 8; depth++, node = node.parentElement) {
    if (node.querySelectorAll("input[type=file]").length > 1) return null;
    const text = flatText(node, isControlEl);
    if (collapse(text) && text.length <= 300) {
      const kind = classifyText(text);
      if (kind) return kind;
    }
  }
  return null;
}

/** SmartRecruiters' "autocomplete your application" dropzone only posts a parse; it is no attachment. */
const PARSE_ONLY = /auto-?(complete|fill)( (your|the))? application|pre-?fill( (your|the))? application/;

export function isParseOnly(info: ControlInfo): boolean {
  const input = info.el as HTMLInputElement;
  return PARSE_ONLY.test(normalize(`${info.label.text} ${scopedText(input)}`));
}

function acceptsAll(input: HTMLInputElement): string[] {
  return (input.getAttribute("accept") ?? "").split(",").map((s) => s.trim().toLowerCase()).filter(Boolean);
}

/** Own label first, then the slot's scoped text, and only then what the input accepts. */
export function classifySlot(info: ControlInfo): SlotKind {
  const input = info.el as HTMLInputElement;
  const own = classifyText(info.label.direct || info.label.text);
  if (own) return own;
  const scoped = scopedKind(input);
  if (scoped) return scoped;
  const accept = acceptsAll(input);
  if (!accept.length || accept.some((a) => /pdf|doc|word|\.txt|\.rtf|application\//.test(a))) return "resume";
  return "other";
}

function accepted(input: HTMLInputElement, payload: ResumePayload): boolean {
  const accept = acceptsAll(input);
  if (!accept.length) return true;
  const ext = `.${payload.fileName.split(".").pop()?.toLowerCase() ?? ""}`;
  const type = payload.mediaType.toLowerCase();
  return accept.some((a) => a === ext || a === type || (a.endsWith("/*") && type.startsWith(a.slice(0, -1))));
}

/** iCIMS keeps the uploaded file in its own state: the saved-name field is what says it took the upload. */
function savedNameField(doc: Document): HTMLInputElement | null {
  return doc.querySelector<HTMLInputElement>('input[type="hidden"][id$="Resume_FileName"], input[type="hidden"][name$="Resume_FileName"]');
}

/** Dayforce's records: editors plus committed cards. */
function dayforceRecords(doc: Document): number {
  const editors = doc.querySelectorAll('form[id^="workHistory-"], form[id^="educationHistory-"]').length;
  const cards = Array.from(doc.querySelectorAll("button, [role=button]")).filter((b) => /^delete record$/i.test(collapse(b.textContent ?? ""))).length;
  return editors + cards;
}

const takenByDropzone = (input: HTMLInputElement): boolean => (input.getRootNode() as ShadowRoot).host?.localName === "spl-dropzone";

/** The document's exact size: the base64 length less its padding. */
function exactBytes(payload: ResumePayload): number {
  const b64 = payload.contentBase64;
  return Math.floor((b64.length * 3) / 4) - (b64.endsWith("==") ? 2 : b64.endsWith("=") ? 1 : 0);
}

/**
 * Workday lists the files an upload already holds, carried over from the
 * account's last application ("jordan-avery.pdf", "34.58 KB"): the same
 * document, by name and size, is held, and another copy is never added.
 */
function holdsSameFile(input: HTMLInputElement, payload: ResumePayload): boolean {
  const area = input.closest('[data-automation-id="attachments-FileUpload"]');
  if (!area) return false;
  const kb = exactBytes(payload) / 1024;
  return Array.from(area.querySelectorAll('[data-automation-id="file-upload-item"]')).some((item) => {
    const name = collapse(flatText(item.querySelector('[data-automation-id="file-upload-item-name"]') ?? item));
    const size = /([\d.]+)\s*KB\b/i.exec(flatText(item));
    return name === payload.fileName && !!size && Math.abs(Number(size[1]) - kb) <= 0.006;
  });
}

function showsUpload(input: HTMLInputElement): boolean {
  if ((input.files?.length ?? 0) > 0) return true;
  if ((savedNameField(input.ownerDocument)?.value ?? "") !== "") return true;
  return UPLOADED.test(scopedText(input));
}

export interface ResumeDeps {
  host: Host;
  win: Win;
  /** Inputs this document already received a file in, keyed by node and by id. */
  attached: { nodes: WeakSet<Element>; ids: Set<string> };
  /** What this run wrote, by selector. */
  written: Map<string, string>;
}

const FIELDS = "input, select, textarea";

/** An element that is or holds a field, shadow roots included. */
function holdsField(el: Element): boolean {
  let found = el.matches(FIELDS);
  if (!found) walkElements(el, (e) => (found ||= e.matches(FIELDS)));
  return found;
}

/** Wait for the page to finish parsing an upload: quiet after a structural change, or no change at all. */
async function waitForParse(run: Run, win: Win): Promise<string> {
  const t = run.timing;
  const began = Date.now();
  // The page must have taken the upload before its quiet is measured: iCIMS says so in its saved-name field.
  const marker = savedNameField(run.doc);
  if (marker && marker.value === "") {
    while (marker.value === "" && Date.now() - began < t.parseCapMs) {
      await run.checkpoint();
      await run.sleep(t.parsePollMs);
    }
  }
  // Dayforce creates its records long after its own settle signal: wait for the record count to stop changing.
  if (run.site === "dayforce") {
    let count = dayforceRecords(run.doc);
    let since = Date.now();
    while (Date.now() - began < t.dayforceParseCapMs) {
      await run.checkpoint();
      await run.sleep(t.parsePollMs);
      const now = dayforceRecords(run.doc);
      if (now !== count) {
        count = now;
        since = Date.now();
      } else if (count > 0 && Date.now() - since >= t.dayforceQuietMs) {
        run.parseSettled = true;
        return "records-stable";
      }
    }
    return "cap";
  }
  let structural = false;
  // The no-change window opens when the page took the upload, however long that took.
  const taken = Date.now();
  let last = taken;
  // Added nodes are read at the next poll, once their components drew their shadow roots; a parse's
  // history cards (SmartRecruiters') hold no field but change the card count.
  const seen: Element[] = [];
  let cards = historyCardCount(run.doc);
  const obs = new win.MutationObserver((records) => {
    for (const r of records) for (const n of [...Array.from(r.addedNodes), ...Array.from(r.removedNodes)]) if (n.nodeType === 1) seen.push(n as Element);
  });
  obs.observe(run.doc, { childList: true, subtree: true });
  try {
    for (;;) {
      await run.checkpoint();
      const count = historyCardCount(run.doc);
      if (seen.splice(0).some(holdsField) || count !== cards) {
        cards = count;
        structural = true;
        last = Date.now();
      }
      const now = Date.now();
      if (structural && now - last >= t.parseQuietMs) {
        run.parseSettled = true;
        return "quiet";
      }
      if (!structural && now - taken >= t.parseNoChangeMs) return "no-parse";
      if (now - began >= t.parseCapMs) return "cap";
      await run.sleep(t.parsePollMs);
    }
  } finally {
    obs.disconnect();
  }
}

/** An empty résumé slot the run has a document for is a required row, whatever the page marks: the next step refuses a missing upload. */
function requiredSlot(row: Row, kind: SlotKind): Row {
  return kind === "resume" ? { ...row, required: true } : row;
}

/** An upload input inside a menu or dialog the page keeps closed until its trigger is pressed. */
function closedSlot(input: HTMLInputElement): boolean {
  const holder = input.parentElement;
  return ariaHiddenBlocks(input) || (holder !== null && !isRendered(holder) && !isSfUploader(input));
}

function triggerName(trigger: Element): string {
  const named = (trigger.getAttribute("aria-labelledby") ?? "").split(/\s+/).filter(Boolean).map((id) => trigger.ownerDocument.getElementById(id)?.textContent ?? "");
  return collapse(named.join(" ") || (trigger.getAttribute("aria-label") ?? "") || (trigger.textContent ?? ""));
}

/** A visible menu button that names the résumé (Jobvite's "Add Resume" Select): the page's own way to its upload. */
function resumeMenuButton(doc: Document): boolean {
  return Array.from(doc.querySelectorAll("button, [role=button]")).some((b) => {
    const popup = b.getAttribute("aria-haspopup");
    return popup !== null && popup !== "false" && isRendered(b) && classifyText(triggerName(b)) === "resume";
  });
}

/** A visible link or button that opens the page's own résumé upload ("Attach resume"): pressed before the file goes in. */
function resumeOpener(doc: Document): HTMLElement | null {
  const named = /^(attach|upload|add|choose|select)( a| your| my| the)?( new)? (resume|cv|curriculum vitae)( file)?$/i;
  return Array.from(doc.querySelectorAll<HTMLElement>("a, button, [role=button]")).find((e) => isRendered(e) && named.test(triggerName(e))) ?? null;
}

/** A step whose only field is the résumé upload behind the page's own "Upload Resume" button (evlo): an application step. */
export function resumeUploadStep(doc: Document): boolean {
  return resumeOpener(doc) !== null && scanControls(doc, { hiddenUploads: true }).some((i) => i.kind === "file" && !isParseOnly(i) && classifySlot(i) === "resume");
}

/**
 * The slots to fill. A closed upload is one only when the page shows no résumé slot of its own, a résumé menu
 * button or an "Attach resume" opener stands for it, and its own words (never the accepted-formats default) say
 * it takes the résumé.
 */
function usableSlots(run: Run): ControlInfo[] {
  const all = scanControls(run.doc, { hiddenUploads: true }).filter((i) => i.kind === "file");
  const open = all.filter((i) => !closedSlot(i.el as HTMLInputElement));
  if (open.some((i) => !isParseOnly(i) && classifySlot(i) === "resume") || !(resumeMenuButton(run.doc) || resumeOpener(run.doc))) return open;
  const closed = all.filter((i) => closedSlot(i.el as HTMLInputElement) && (classifyText(i.label.direct || i.label.text) ?? scopedKind(i.el)) === "resume");
  return [...open, ...closed];
}

export async function resumeStage(run: Run, d: ResumeDeps): Promise<void> {
  const slots = usableSlots(run);
  const hasAttachmentSlot = slots.some((i) => !isParseOnly(i));
  for (const info of slots) {
    await run.checkpoint();
    // The parse-only dropzone adds entries of its own to the form: it is used only when it is the one slot there is.
    if (isParseOnly(info) && hasAttachmentSlot) continue;
    const input = info.el as HTMLInputElement;
    const kind = classifySlot(info);
    const idKey = input.getAttribute("id") ?? "";
    const received = d.attached.nodes.has(input) || (idKey && d.attached.ids.has(idKey));
    const shown = showsUpload(input);
    // A slot the page shows a document in is answered, though the input itself reads empty (the page keeps the file in its own state).
    if (shown && !run.rows.has(info.selector)) run.setRow(makeRow(info, "profile", "prefilled", { reason: "no-lane-claimed", value: input.files?.[0]?.name }));
    if (kind === "other" || run.edits.edited(info)) continue;
    if (shown) {
      if (kind === "cover-letter") run.coverLetterAttached = true;
      else if (kind === "resume") {
        run.resumeAttached = true;
        run.hasDocument = true;
      }
      continue;
    }
    if (received) continue;

    let payload: ResumePayload | null = null;
    try {
      payload = await run.until(d.host.resume({ runId: run.id, document: kind }));
    } catch (e) {
      if (e instanceof StopSignal) throw e;
      run.log.add("resume", "binding-failed", { reason: e instanceof Error ? e.message : String(e) });
    }
    await run.checkpoint();
    if (!payload || exactBytes(payload) > MAX_RESUME_BYTES) continue;
    if (kind === "resume") run.hasDocument = true;
    if (holdsSameFile(input, payload)) {
      run.setRow(makeRow(info, "profile", "prefilled", { reason: "no-lane-claimed", value: payload.fileName }));
      if (kind === "cover-letter") run.coverLetterAttached = true;
      else if (kind === "resume") run.resumeAttached = true;
      continue;
    }
    if (!accepted(input, payload)) {
      run.setRow(requiredSlot(makeRow(info, "profile", "skipped", { reason: "format-not-accepted" }), kind));
      continue;
    }
    // A page that keeps its upload closed until its opener is pressed gets that press once, as a person would, before the file goes in.
    if (closedSlot(input) && !resumeMenuButton(run.doc)) {
      const opener = resumeOpener(run.doc);
      if (opener && pointerSequence(opener) === "ok") await waitFor(run.ctx(), () => !closedSlot(input), run.timing.openWaitMs);
    }
    if (isSfUploader(input)) {
      // The page's own uploader overwrites an assigned file: the helper in the page's world does it.
      const sf = await attachSfResume(run.ctx(), input, payload);
      // Only a slot that holds a file is marked: an upload that did not land is tried again by the next run.
      if (sf === "attached" || sf === "already") {
        d.attached.nodes.add(input);
        if (idKey) d.attached.ids.add(idKey);
      }
      if (sf === "already") {
        if (kind === "cover-letter") run.coverLetterAttached = true;
        else if (kind === "resume") run.resumeAttached = true;
        continue;
      }
      if (sf !== "attached") {
        run.setRow(requiredSlot(makeRow(info, "profile", "skipped", { reason: sf === "unavailable" ? "binding-unavailable" : "write-not-committed", value: payload.fileName }), kind));
        continue;
      }
      if (kind === "cover-letter") run.coverLetterAttached = true;
      else if (kind === "resume") run.resumeAttached = true;
      run.setRow(makeRow(info, "profile", "filled", { value: payload.fileName }));
      run.parseTriggered = true;
      run.log.add("resume", "parse-settled", { how: run.parseSettled ? "settled-before" : await waitForParse(run, d.win) });
      continue;
    }
    // SmartRecruiters' dropzone reads the file and empties its input (its parse dropzone lists nothing): a native upload would hand it over twice.
    const result = attachFile(input, payload) === "attached" || takenByDropzone(input) ? "attached" : "needs-native-upload";
    d.attached.nodes.add(input);
    if (idKey) d.attached.ids.add(idKey);
    if (result === "needs-native-upload") {
      run.setRow(requiredSlot(makeRow(info, "profile", "skipped", { reason: "needs-native-upload", value: payload.fileName }), kind));
      continue;
    }
    if (kind === "cover-letter") run.coverLetterAttached = true;
    else if (kind === "resume") run.resumeAttached = true;
    run.setRow(makeRow(info, "profile", "filled", { value: payload.fileName }));
    run.parseTriggered = true;
    // A parse this run already saw land is the page's one parse: the next upload is only an attachment.
    const how = run.parseSettled ? "settled-before" : await waitForParse(run, d.win);
    run.log.add("resume", "parse-settled", { how });
  }
  for (const info of scanControls(run.doc)) if (info.kind === "textarea" && LETTER_BOX.test(normalize(info.label.direct || info.label.text))) await letterBox(run, d, info);
}

function utf8(base64: string): string {
  return new TextDecoder().decode(Uint8Array.from(atob(base64), (c) => c.charCodeAt(0)));
}

/** A letter box gets the text of the letter an upload slot would get, or stays a leftover; a box with text is an observed letter. */
async function letterBox(run: Run, d: ResumeDeps, info: ControlInfo): Promise<void> {
  if (run.rows.has(info.selector) || run.edits.edited(info)) return;
  if (readInfo(info) !== "") {
    run.coverLetterAttached = true;
    return;
  }
  let payload: ResumePayload | null = null;
  try {
    payload = await run.until(d.host.resume({ runId: run.id, document: "cover-letter", format: "text", ...(info.maxLength ? { maxLength: info.maxLength } : {}) }));
  } catch (e) {
    if (e instanceof StopSignal) throw e;
    run.log.add("resume", "binding-failed", { reason: e instanceof Error ? e.message : String(e) });
  }
  await run.checkpoint();
  const text = payload?.mediaType === "text/plain" ? utf8(payload.contentBase64) : "";
  if (!text.trim() || text.length > (info.maxLength ?? Infinity)) return;
  const r = await applyValue(info, { value: text, replaceDefault: false }, { ctx: run.ctx(), written: d.written, mode: "keep", acceptLegalDeclarations: run.acceptLegalDeclarations, edits: run.edits });
  run.setRow(makeRow(info, "profile", r.outcome, { reason: r.reason, value: r.value }));
  if (r.outcome === "filled") run.coverLetterAttached = true;
}
