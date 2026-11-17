// A small Markdown renderer that builds React elements and never sets inner
// HTML. Raw HTML in a report therefore appears as literal text, links carry
// noopener noreferrer, and an image loads only from this server. Reports can
// come from hostile posting text, so every scan here is a single pass with
// memoised failures: no input shape makes it super-linear.
import { Fragment, memo, type ReactNode } from "react";

export interface Heading {
  level: number;
  text: string;
  id: string;
}

// Slugs is the set of heading ids handed out so far, with the next counter to
// try per base, so a thousand headings of the same text cost a thousand steps.
class Slugs {
  private used = new Set<string>();
  private next = new Map<string, number>();

  take(text: string): string {
    const base = text.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "") || "section";
    let n = this.next.get(base) ?? 1;
    let id = n === 1 ? base : `${base}-${n}`;
    while (this.used.has(id)) id = `${base}-${++n}`;
    this.next.set(base, n + 1);
    this.used.add(id);
    return id;
  }
}

const NAMED: Record<string, string> = {
  amp: "&", lt: "<", gt: ">", quot: '"', apos: "'", nbsp: "\u00a0", copy: "\u00a9", reg: "\u00ae", trade: "\u2122",
  ndash: "\u2013", mdash: "\u2014", hellip: "\u2026", lsquo: "\u2018", rsquo: "\u2019", ldquo: "\u201c", rdquo: "\u201d",
  bull: "\u2022", middot: "\u00b7", times: "\u00d7", euro: "\u20ac",
};

// entityAt decodes the character reference that starts at text[i] (an
// ampersand). The lookahead is bounded, so a run of ampersands stays linear.
function entityAt(text: string, i: number): { value: string; end: number } | undefined {
  const near = text.slice(i + 1, i + 13).indexOf(";");
  if (near < 0) return undefined;
  const semi = i + 1 + near;
  const body = text.slice(i + 1, semi);
  let value: string | undefined;
  if (/^#[0-9]{1,7}$/.test(body)) value = codePoint(Number(body.slice(1)));
  else if (/^#[xX][0-9a-fA-F]{1,6}$/.test(body)) value = codePoint(parseInt(body.slice(2), 16));
  else value = NAMED[body];
  return value === undefined ? undefined : { value, end: semi + 1 };
}

function codePoint(n: number): string | undefined {
  if (n === 0 || n > 0x10ffff || (n >= 0xd800 && n <= 0xdfff)) return undefined;
  return String.fromCodePoint(n);
}

function decodeEntities(text: string): string {
  if (!text.includes("&")) return text;
  let out = "";
  let from = 0;
  for (let i = text.indexOf("&"); i >= 0; i = text.indexOf("&", i + 1)) {
    const e = entityAt(text, i);
    if (!e) continue;
    out += text.slice(from, i) + e.value;
    from = e.end;
    i = e.end - 1;
  }
  return out + text.slice(from);
}

// safeLink answers the address a link may carry. The scheme check runs on the
// decoded text with the same parser a browser uses, so an entity, a tab or a
// case change cannot hide a script scheme.
function safeLink(raw: string): string | undefined {
  const url = decodeEntities(raw);
  try {
    const u = new URL(url, "https://placeholder.invalid");
    return u.protocol === "https:" || u.protocol === "http:" || u.protocol === "mailto:" ? url : undefined;
  } catch {
    return undefined;
  }
}

// localImage admits an address only when a browser resolves it to this
// server's own origin; a backslash or an entity-decoded tab cannot change that.
function localImage(raw: string): string | undefined {
  const src = decodeEntities(raw);
  if (!src.startsWith("/") || /[\\\u0000-\u0020]/.test(src)) return undefined;
  const origin = globalThis.location?.origin ?? "http://localhost";
  try {
    return new URL(src, origin).origin === origin ? src : undefined;
  } catch {
    return undefined;
  }
}

const isSpace = (c: string) => c === " " || c === "\t" || c === "\n" || c === "\r" || c === "\f" || c === "\v";

// indexFinder answers indexOf(ch, from) for a from that never decreases by
// much, remembering the last answer so a failed search is paid for once.
function indexFinder(text: string, ch: string) {
  let lastFrom = -1;
  let lastAt = -2;
  return (from: number): number => {
    if (lastAt !== -2 && from >= lastFrom && (lastAt === -1 || lastAt >= from)) return lastAt;
    lastFrom = from;
    lastAt = text.indexOf(ch, from);
    return lastAt;
  };
}

export function inline(text: string, key = "i"): ReactNode[] {
  const out: ReactNode[] = [];
  const nextTick = indexFinder(text, "`");
  const nextClose = indexFinder(text, "]");
  const nextStar = indexFinder(text, "*");
  let urlFailUntil = -1;
  let n = 0;
  let plain = 0;
  let i = 0;

  const flush = (to: number) => {
    if (to > plain) out.push(decodeEntities(text.slice(plain, to)));
  };
  const take = (to: number, node: ReactNode) => {
    flush(to);
    out.push(node);
  };

  // linkAt parses [text](url) with its opener at text[open]; the url must hold
  // no whitespace. A failed scan is remembered up to where it stopped.
  const linkAt = (open: number): { label: string; url: string; end: number } | undefined => {
    const close = nextClose(open + 1);
    if (close <= open + 1 || text[close + 1] !== "(") return undefined;
    const start = close + 2;
    if (start < urlFailUntil) return undefined;
    let j = start;
    while (j < text.length && text[j] !== ")" && !isSpace(text[j])) j++;
    if (j >= text.length || text[j] !== ")") {
      urlFailUntil = j;
      return undefined;
    }
    if (j === start) return undefined;
    return { label: text.slice(open + 1, close), url: text.slice(start, j), end: j + 1 };
  };

  while (i < text.length) {
    const c = text[i];
    const k = `${key}-${n}`;
    if (c === "`") {
      const close = nextTick(i + 1);
      if (close > i + 1) {
        take(i, <code key={k}>{text.slice(i + 1, close)}</code>);
        n++;
        i = plain = close + 1;
        continue;
      }
    } else if (c === "!" && text[i + 1] === "[") {
      const l = linkAt(i + 1);
      if (l) {
        const src = localImage(l.url);
        // The image links to itself: a guide screenshot is wider than a phone, and the guide opens it in a dialog.
        take(i, src ? <a key={k} href={src} className="md-shot" title="Open full size"><img src={src} alt={decodeEntities(l.label)} loading="lazy" decoding="async" /></a> : <span key={k}>{decodeEntities(l.label) || "image"}</span>);
        n++;
        i = plain = l.end;
        continue;
      }
    } else if (c === "[") {
      const l = linkAt(i);
      if (l) {
        const href = safeLink(l.url);
        take(i, href ? <a key={k} href={href} target="_blank" rel="noopener noreferrer">{inline(l.label, k)}</a> : <span key={k}>{decodeEntities(l.label)}</span>);
        n++;
        i = plain = l.end;
        continue;
      }
    } else if (c === "*") {
      if (text[i + 1] === "*") {
        const close = nextStar(i + 2);
        if (close > i + 2 && text[close + 1] === "*") {
          take(i, <strong key={k}>{inline(text.slice(i + 2, close), k)}</strong>);
          n++;
          i = plain = close + 2;
          continue;
        }
      } else {
        const close = nextStar(i + 1);
        if (close > i + 1) {
          take(i, <em key={k}>{inline(text.slice(i + 1, close), k)}</em>);
          n++;
          i = plain = close + 1;
          continue;
        }
      }
    } else if (c === "<") {
      const end = autolinkEnd(text, i);
      if (end > 0) {
        const target = text.slice(i + 1, end);
        const href = safeLink(target);
        if (href) {
          take(i, <a key={k} href={href} target="_blank" rel="noopener noreferrer">{decodeEntities(target)}</a>);
          n++;
          i = plain = end + 1;
          continue;
        }
      }
    }
    i++;
  }
  flush(text.length);
  return out;
}

// autolinkEnd returns the index of the closing > of <scheme:address> that
// opens at text[open], or -1. The scan stops at whitespace or another <, so
// each character is read by at most one opener.
function autolinkEnd(text: string, open: number): number {
  let j = open + 1;
  if (!/[A-Za-z]/.test(text[j] ?? "")) return -1;
  while (j < text.length && /[A-Za-z0-9+.-]/.test(text[j])) j++;
  if (text[j] !== ":") return -1;
  j++;
  const start = j;
  while (j < text.length && text[j] !== ">" && text[j] !== "<" && !isSpace(text[j])) j++;
  return text[j] === ">" && j > start ? j : -1;
}

// parseHeading reads an ATX heading without a backtracking pattern: one to six
// hashes, a space, the text, and an optional closing run of hashes after a space.
export function parseHeading(line: string): { level: number; text: string } | undefined {
  let level = 0;
  while (level < 7 && line[level] === "#") level++;
  if (level === 0 || level > 6 || !isSpace(line[level] ?? "x")) return undefined;
  let text = line.slice(level).trim();
  let end = text.length;
  while (end > 0 && text[end - 1] === "#") end--;
  if (end === 0 || isSpace(text[end - 1])) text = text.slice(0, end).trimEnd();
  return text === "" ? undefined : { level, text };
}

const isRule = (l: string) => /^ {0,3}([-*_])( ?\1){2,}\s*$/.test(l);
const SEP_CELL = /^:?-{3,}:?$/;
const isTableSep = (l: string) => {
  const cs = l.trim().replace(/^\|/, "").replace(/\|$/, "").split("|");
  return cs.length > 0 && cs.every((c) => SEP_CELL.test(c.trim()));
};
const cells = (l: string) => l.trim().replace(/^\||\|$/g, "").split("|").map((c) => c.trim());

export function headingsOf(source: string): Heading[] {
  const slugs = new Slugs();
  const out: Heading[] = [];
  let fence = false;
  for (const line of source.split(/\r?\n/)) {
    if (/^\s*```/.test(line)) fence = !fence;
    const h = fence ? undefined : parseHeading(line);
    if (h) out.push({ level: h.level, text: h.text, id: slugs.take(h.text) });
  }
  return out;
}

// Markdown renders again only when its text changes: a streaming answer or a
// polled page renders its parent often, and every earlier message and report
// would otherwise be parsed again each time.
export const Markdown = memo(function Markdown({ source }: { source: string }) {
  const lines = source.split(/\r?\n/);
  const slugs = new Slugs();
  const blocks: ReactNode[] = [];
  let i = 0;
  let k = 0;
  while (i < lines.length) {
    const line = lines[i];
    const key = `b${k++}`;
    if (line.trim() === "") {
      i++;
    } else if (/^\s*```/.test(line)) {
      const info = line.trim().slice(3).trim();
      const body: string[] = [];
      i++;
      while (i < lines.length && !/^\s*```/.test(lines[i])) body.push(lines[i++]);
      i++;
      // Focusable, so the keyboard scrolls a line wider than the page (WebKit does not focus a scroller itself).
      const pre = <pre tabIndex={0}><code>{body.join("\n")}</code></pre>;
      // An evaluation report embeds its score inputs as JSON; it is data for
      // tools, so it stays out of the way until opened.
      blocks.push(info === "scorecard" ? <details key={key} className="fold"><summary>Scorecard data</summary>{pre}</details> : <Fragment key={key}>{pre}</Fragment>);
    } else if (parseHeading(line)) {
      const h = parseHeading(line)!;
      const Tag = `h${Math.min(6, h.level + 1)}` as "h2";
      blocks.push(<Tag key={key} id={slugs.take(h.text)}>{inline(h.text, key)}</Tag>);
      i++;
    } else if (isRule(line)) {
      blocks.push(<hr key={key} />);
      i++;
    } else if (/^\s*>/.test(line)) {
      const body: string[] = [];
      while (i < lines.length && /^\s*>/.test(lines[i])) body.push(lines[i++].replace(/^\s*>\s?/, ""));
      blocks.push(<blockquote key={key}>{inline(body.join(" "), key)}</blockquote>);
    } else if (/^\s*([-*+]|\d+\.)\s+/.test(line)) {
      const ordered = /^\s*\d+\./.test(line);
      const items: string[] = [];
      while (i < lines.length && /^\s*([-*+]|\d+\.)\s+/.test(lines[i])) {
        let item = lines[i++].replace(/^\s*([-*+]|\d+\.)\s+/, "");
        // A hard-wrapped item goes on over the lines that follow it, indented
        // or not, until a blank line or the start of another block.
        while (i < lines.length && lines[i].trim() !== "" && !parseHeading(lines[i]) && !/^(\s*```|\s*>|\s*([-*+]|\d+\.)\s+)/.test(lines[i]) && !isRule(lines[i])) item += " " + lines[i++].trim();
        items.push(item);
      }
      const Tag = ordered ? "ol" : "ul";
      blocks.push(<Tag key={key}>{items.map((t, j) => <li key={j}>{inline(t, `${key}-${j}`)}</li>)}</Tag>);
    } else if (line.includes("|") && i + 1 < lines.length && isTableSep(lines[i + 1])) {
      const head = cells(line);
      i += 2;
      const rows: string[][] = [];
      while (i < lines.length && lines[i].includes("|") && lines[i].trim() !== "") rows.push(cells(lines[i++]));
      blocks.push(
        <table key={key} className="md-table">
          <thead><tr>{head.map((c, j) => <th scope="col" key={j}>{inline(c, `${key}h${j}`)}</th>)}</tr></thead>
          <tbody>{rows.map((r, j) => <tr key={j}>{r.map((c, x) => <td key={x}>{inline(c, `${key}-${j}-${x}`)}</td>)}</tr>)}</tbody>
        </table>,
      );
    } else {
      const body: string[] = [];
      while (i < lines.length && lines[i].trim() !== "" && !parseHeading(lines[i]) && !/^(\s*```|\s*>|\s*([-*+]|\d+\.)\s+)/.test(lines[i]) && !isRule(lines[i])) body.push(lines[i++]);
      blocks.push(<p key={key}>{inline(body.join(" "), key)}</p>);
    }
  }
  return <div className="markdown">{blocks}</div>;
});

// isData says whether text is a JSON object or array, which reads best as
// it came.
function isData(text: string): boolean {
  if (!/^\s*[[{]/.test(text)) return false;
  try {
    JSON.parse(text);
    return true;
  } catch {
    return false;
  }
}

// ModelText shows what a model or a tool returned: JSON stays preformatted,
// anything else is prose and renders as Markdown.
export function ModelText({ source }: { source: string }) {
  return isData(source) ? <pre className="pre">{source}</pre> : <Markdown source={source} />;
}
