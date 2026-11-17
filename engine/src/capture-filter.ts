// Clean-room: docs/provenance.md#CR-ENGINE
// Which captures are dropped instead of stored. The same rules run in Go when
// an upload is ingested (internal/answerbank/learn, CaptureDrop); both read
// engine/test/capture-vectors.json, so they cannot drift. "unchanged" and
// "equals-label" read what an upload does not carry and run here only (their
// vectors are marked engineOnly).

export interface CaptureCandidate {
  question: string;
  value: string;
  /** The control's own label. */
  label: string;
  controlKind: string;
  /** The value the control held when the watch attached. */
  valueAtAttach: string;
}

const MAX_QUESTION = 2000;
const DUPLICATE_SHARE = 0.35;
const OPAQUE_ID = /\b(?:question|field|input|q|id)[_-]?\d{5,}\b|\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b|\b[a-z]+_[0-9a-f]{10,}\b|\b\d{9,}\b/;
const SEAM = /[a-z][A-Z]/g;

const placeholderSpace = (c: string): boolean => /[\p{White_Space}\uFEFF]/u.test(c);
const placeholderDash = (c: string): boolean => c === "-" || c === "\u2013" || c === "\u2014";
const trimPlaceholderSpace = (s: string): string => s.replace(/^[\p{White_Space}\uFEFF]+|[\p{White_Space}\uFEFF]+$/gu, "");

/** Parse the capture contract's prompts; count wrapped bodies before case conversion. */
export function isNothingChosen(label: string): boolean {
  const text = trimPlaceholderSpace(label);
  const chars = Array.from(text);
  if (chars.every((c) => placeholderSpace(c) || placeholderDash(c) || c === "." || c === "\u2026")) return true;

  let left = 0;
  let right = chars.length;
  while (left < right && placeholderDash(chars[left] as string)) left++;
  while (right > left && placeholderDash(chars[right - 1] as string)) right--;
  if (left >= 2 && chars.length - right >= 2) {
    const body = Array.from(trimPlaceholderSpace(chars.slice(left, right).join("")));
    return body.length >= 1 && body.length <= 40 && !body.some(placeholderDash);
  }

  const prompt = text.replace(/[A-Z]/g, (c) => c.toLowerCase()).replace(/[.\u2026\p{White_Space}\uFEFF]+$/u, "");
  if (prompt === "") return false;
  let words = prompt.split(/[\p{White_Space}\uFEFF]+/u);
  if (words[0] === "please") {
    if (words[1] !== "choose" && words[1] !== "select") return false;
    words = words.slice(1);
  }
  const tail = words.slice(1).join(" ");
  switch (words[0]) {
    case "choose":
    case "select":
      return tail === "" || tail === "one" || tail === "an option";
    case "make":
      return tail === "a selection" || tail === "a selection here";
    case "none":
      return tail === "" || tail === "selected";
    case "no":
      return tail === "selection";
    case "pick":
      return tail === "one";
    case "na":
    case "n/a":
      return tail === "";
    default:
      return false;
  }
}

const fold = (s: string): string => s.toLowerCase().replace(/\s+/g, " ").trim();

/** Sentences, lines and bars, as the page glues them together. */
function segmentsOf(text: string): string[] {
  const out: string[] = [];
  let cur = "";
  const chars = Array.from(text);
  for (let i = 0; i < chars.length; i++) {
    const c = chars[i] as string;
    if (c === "\n" || c === "|") {
      out.push(cur);
      cur = "";
    } else {
      cur += c;
      if ((c === "." || c === "?" || c === "!") && /\s/.test(chars[i + 1] ?? "x")) {
        out.push(cur);
        cur = "";
      }
    }
  }
  out.push(cur);
  return out.map(fold).filter(Boolean);
}

/** The text opens with a string and, after at most a few separator characters, the same string again ("Male--MaleFemale"). */
function repeatsPrefix(t: string): boolean {
  const chars = Array.from(t);
  for (let k = 3; k * 2 <= chars.length; k++) {
    const head = chars.slice(0, k).join("");
    let rest = chars.slice(k);
    let sep = 0;
    while (rest.length && !/[\p{L}\p{N}]/u.test(rest[0] as string) && sep < 4) {
      rest = rest.slice(1);
      sep++;
    }
    if (rest.join("").startsWith(head)) return true;
  }
  return false;
}

/** The text is two copies of one string. */
function doubled(text: string, prefix = false): boolean {
  const t = fold(text);
  if (prefix && repeatsPrefix(t)) return true;
  if (t.length >= 2 && t.length % 2 === 0 && t.slice(0, t.length / 2) === t.slice(t.length / 2)) return true;
  const words = t.split(" ");
  if (words.length >= 2 && words.length % 2 === 0) {
    const half = words.length / 2;
    return words.slice(0, half).join(" ") === words.slice(half).join(" ");
  }
  return false;
}

const SCRIPTS: [string, RegExp][] = [
  ["latin", /\p{Script=Latin}/u],
  ["cyrillic", /\p{Script=Cyrillic}/u],
  ["greek", /\p{Script=Greek}/u],
  ["arabic", /\p{Script=Arabic}/u],
  ["hebrew", /\p{Script=Hebrew}/u],
  ["han", /\p{Script=Han}/u],
  ["kana", /[\p{Script=Hiragana}\p{Script=Katakana}]/u],
  ["hangul", /\p{Script=Hangul}/u],
  ["devanagari", /\p{Script=Devanagari}/u],
  ["thai", /\p{Script=Thai}/u],
];

/** The one script a text's letters are written in, "" when it has no letters or mixes scripts. */
function scriptOf(text: string): string {
  const seen = new Set<string>();
  for (const ch of text) {
    if (!/\p{L}/u.test(ch)) continue;
    seen.add(SCRIPTS.find(([, re]) => re.test(ch))?.[0] ?? "other");
  }
  return seen.size === 1 ? (Array.from(seen)[0] as string) : "";
}

/** Why a capture is dropped, or null when it is kept. */
export function captureDrop(c: CaptureCandidate): string | null {
  const q = c.question;
  if (Array.from(q).length > MAX_QUESTION) return "question-too-long";
  const segs = segmentsOf(q);
  if ((segs.length >= 2 && (segs.length - new Set(segs).size) / segs.length >= DUPLICATE_SHARE) || doubled(q, false)) return "repeated-segment";
  if (OPAQUE_ID.test(q)) return "opaque-id";
  if ((q.match(SEAM) ?? []).length >= 2) return "seams";
  if (q.trim() !== "" && isNothingChosen(q)) return "placeholder-question";
  const v = c.value.trim();
  if (v === "") return "empty-value";
  if (doubled(v, true)) return "doubled-value";
  if (isNothingChosen(v)) return "placeholder";
  const qs = scriptOf(q);
  const vs = scriptOf(v);
  if (qs !== "" && vs !== "" && qs !== vs) return "script-mismatch";
  if (c.valueAtAttach !== "" && fold(c.valueAtAttach) === fold(v)) return "unchanged";
  if (fold(c.label) === fold(v)) return "equals-label";
  if (fold(q) === fold(v)) return "equals-question";
  if (c.controlKind === "custom-listbox" && /^[a-z]{1,3}$/.test(v)) return "listbox-fragment";
  return null;
}
