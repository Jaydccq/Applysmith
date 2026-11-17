import type { LucideIcon } from "lucide-react";
import { createContext, useContext, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { flushSync } from "react-dom";
import { ApiError, fetchBlob, get } from "../api/client";
import { llmRouting } from "../api/decoders";
import { ReasonNoModelRoute, ReasonServerStarting } from "../wire";
import { Link } from "../router";
import { asApiError, useBoardState, useNarrow, useResource, type Resource } from "../hooks";
import { barRoom, focusables } from "./focus";

// SelectionBar holds the actions on the rows a person ticked. It sits after
// the list and sticks to the bottom of the view (above the phone tab bar), so
// the actions stay beside the rows ticked far down a long list. Its count is a
// polite live region that is on the page before the first tick, so the first
// count is announced too; the bar itself shows only while a row is ticked.
//
//   <SelectionBar count={chosen.size}><Button>Evaluate</Button></SelectionBar>
export function SelectionBar({ count, children }: { count: number; children: ReactNode }) {
  const bar = useRef<HTMLDivElement>(null);
  const on = count > 0;
  useEffect(() => (on ? barRoom(bar.current!) : undefined), [on]);
  return (
    <div ref={bar} className={on ? "save-bar" : "sr-only"} role={on ? "region" : undefined} aria-label={on ? "Selected rows" : undefined}>
      <span className="save-state" aria-live="polite">
        {on && `${count} ${count === 1 ? "row" : "rows"} chosen.`}
      </span>
      {on && <span className="save-actions">{children}</span>}
    </div>
  );
}

const MODELS = "Settings, Models";

// ModelsLinked says text with the words "Settings, Models" as the link there,
// so a reason that names the place is one press from it.
export function ModelsLinked({ text }: { text: string }) {
  const at = text.indexOf(MODELS);
  return at < 0 ? <>{text}</> : <>{text.slice(0, at)}<Link to="/settings#settings-models">{MODELS}</Link>{text.slice(at + MODELS.length)}</>;
}

// OfflineScoresNote stands over the scores a person decides from while the
// workspace runs offline: no model read those postings, so a score there is
// simulated, which the score itself does not say.
export function OfflineScoresNote() {
  const [routing] = useResource(() => get("/api/llm/routing", llmRouting), [], ["runs"]);
  if (!routing.data?.offline) return null;
  return (
    <p className="note" role="note">
      <ModelsLinked text="Offline: no model reads the postings, so the scores here are simulated. Choose a model under Settings, Models before you apply on one." />
    </p>
  );
}

// FileLink saves an answer of the API as a file. A plain link cannot send the
// session key (fetchBlob), so a press fetches the file and saves it from a
// blob address, under the name the server gives it.
export function FileLink({ path, name, children }: { path: string; name: string; children: ReactNode }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      const url = URL.createObjectURL(await fetchBlob(path));
      const a = document.createElement("a");
      a.href = url;
      a.download = name;
      a.click();
      setTimeout(() => URL.revokeObjectURL(url), 0);
    } catch (err) {
      setError(asApiError(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <button type="button" className="small" disabled={busy} onClick={() => void save()}>
        {children}
      </button>
      {error && <ErrorNote error={error} />}
    </>
  );
}

// ErrorNote shows a failure with its request id and a "Try again" control
// where retrying is safe. A failed load names what it could not load first
// ("Couldn't load the leads."), then the server's reason; a failed action
// passes no `what` and shows the reason alone. The message is assertive
// because it replaces content. A load the server never saw (it is not
// answering) is a calm status instead: the shell's banner tells the story once
// and the load runs again when the server is back (useResource), so the cards
// of a page do not each raise an alarm or offer a retry beside the banner's.
// While the banner shows, the note is a quiet line and no live region, so a
// page reads one outage, not one per card (Settings had over twenty).
// A server that answers "starting" is the same story, told as "starting".
// A task with no model routed names Settings, Models in its reason; that name
// is the link there, so the place is said once.
export function ErrorNote({ error, onRetry, what }: { error: unknown; onRetry?: () => void; what?: string }) {
  const e: ApiError = asApiError(error);
  const { reconnecting } = useBoardState();
  // The note stays while a retry or a poll runs (useResource), with its button
  // disabled after a press. An attempt that fails the same way keeps the first
  // request id, so nothing in the alert changes and it is not announced again.
  const [trying, setTrying] = useState(false);
  useEffect(() => setTrying(false), [error]);
  const first = useRef(e);
  if (first.current.message !== e.message) first.current = e;
  const retry = onRetry && (e.retryable || e.kind === "network" || e.kind === "timeout" || e.kind === "internal" || e.kind === "unavailable") && (
    <button type="button" disabled={trying} onClick={() => (setTrying(true), onRetry())}>
      Try again
    </button>
  );
  const said = what ? e.message.charAt(0).toUpperCase() + e.message.slice(1) : e.message;
  const away = e.kind === "network" ? "not answering. This loads again once it is back." : e.details?.reason === ReasonServerStarting ? "starting. This loads again once it is ready." : "";
  if (away && onRetry && reconnecting) return <p className="empty">Waiting for the server to load {what ?? "this"}.</p>;
  if (away && onRetry) {
    return (
      <div role="status" className="note">
        <p>{what ? `Couldn't load ${what}: the` : "The"} server is {away}</p>
        {retry}
      </div>
    );
  }
  return (
    <div role="alert" className="note error">
      <p className="sentence">
        {what && <strong>Couldn't load {what}. </strong>}
        {/* After the lead the reason is a sentence of its own. */}
        {e.details?.reason === ReasonNoModelRoute ? <ModelsLinked text={said} /> : said}
      </p>
      {first.current.requestId && <p className="muted">Request {first.current.requestId}</p>}
      {retry}
    </div>
  );
}

// Stale is true inside a Panel that shows its last answer under a failed load.
const Stale = createContext(false);

// Empty is the quiet one-line empty state inside a panel. A page's main list
// uses EmptyState instead. Under a failed load it says nothing: "Nothing
// moved" from an earlier answer is no fact while the server cannot be read.
export function Empty({ children }: { children: ReactNode }) {
  return useContext(Stale) ? null : <p className="empty">{children}</p>;
}

// Skeleton is the placeholder drawn while a section loads. It is decoration:
// the status line beside it is what assistive technology reads.
function Skeleton({ lines = 3 }: { lines?: number }) {
  return (
    <div className="skeleton" aria-hidden="true">
      {Array.from({ length: lines }, (_, i) => (
        <span key={i} />
      ))}
    </div>
  );
}

// Loading is the loading state of a section: a polite status that names what
// is loading, read by assistive technology, and the skeleton drawn in its place.
//
//   {res.status === "loading" && <Loading label="Loading the batch" lines={2} />}
export function Loading({ lines = 3, label = "Loading" }: { lines?: number; label?: string }) {
  return (
    <div className="panel-loading">
      <p role="status" className="sr-only">
        {label}
      </p>
      <Skeleton lines={lines} />
    </div>
  );
}

// Panel is one section of a page. It owns its own loading and error state so
// that one failed request does not blank the rest of the page. `what` names
// the data in the error lead ("the pipeline inbox"); without it the lead says
// "this section", because a title such as "Running now" does not read as the
// object of "Couldn't load". A load that fails after one that answered keeps
// that answer, greyed under "Last read at 14:05", with its empty lines left
// out; an answer that was all empty lines leaves the error alone.
export function Panel<T>({ title, id, res, retry, children, actions, icon: Icon, className, what = "this section" }: {
  title: string;
  what?: string;
  id: string;
  res: Resource<T>;
  retry: () => void;
  children: (data: T) => ReactNode;
  actions?: ReactNode;
  icon?: LucideIcon;
  className?: string;
}) {
  const stale = res.status === "error";
  const box = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    if (box.current) box.current.hidden = stale && box.current.childElementCount <= 1;
  });
  return (
    <section aria-labelledby={id} className={className ? `panel ${className}` : "panel"}>
      <header className="panel-head">
        <h2 id={id}>
          {Icon && <Icon className="panel-icon" />}
          {title}
        </h2>
        {actions}
      </header>
      {res.status === "loading" && <Loading />}
      {res.status === "error" && <ErrorNote error={res.error} onRetry={retry} what={what} />}
      {res.data !== undefined && res.status !== "loading" && (
        <div ref={box} className={stale ? "stale-data" : undefined}>
          {stale && <p className="muted">{lastRead(res.at)}</p>}
          <Stale.Provider value={stale}>{children(res.data)}</Stale.Provider>
        </div>
      )}
    </section>
  );
}

// lastRead says when an answer kept under a failed load was read: "Last read
// at 14:05" today, with the date on another day.
export function lastRead(at?: number): string {
  const read = at === undefined ? "" : formatDateTime(new Date(at).toISOString());
  return `Last read ${read.startsWith(today()) ? `at ${read.slice(11)}` : read ? `on ${read}` : "earlier"}; it may have changed since.`;
}

export type Tone = "good" | "warn" | "bad" | "info" | "neutral";

// Chip is a short status or label. Its first letter is drawn in capitals, so
// every chip reads in sentence case whatever word the server sent; a chip that
// shows an identifier, such as a source or tool name, passes raw to keep it as
// it is.
export function Chip({ children, tone, raw, title }: { children: ReactNode; tone?: Tone; raw?: boolean; title?: string }) {
  return (
    <span className={`chip ${tone ?? "neutral"}${raw ? " raw" : ""}`} title={title}>
      {children}
    </span>
  );
}

// TRACKER_STATUSES is the tracker's status table in its own order.
export const TRACKER_STATUSES = ["evaluated", "skipped", "applied", "responded", "interview", "offer", "rejected", "discarded"] as const;

// StatusPill draws a tracker status in that status's colour. The word is
// always shown, so colour is never the only carrier.
export function StatusPill({ status }: { status: string }) {
  return (
    <span className="chip status" data-status={status.toLowerCase()}>
      {status}
    </span>
  );
}

export function formatScore(score: number | undefined): string {
  return score === undefined ? "n/a" : score.toFixed(1);
}

// The score bands follow the evaluation rules: below 3.5 is a skip, and 4.5
// and above earns a tailored PDF.
function scoreBand(score: number | undefined): string {
  if (score === undefined) return "none";
  return score >= 4.5 ? "high" : score >= 3.5 ? "mid" : "low";
}

export function ScoreBadge({ score }: { score: number | undefined }) {
  return (
    <span className="score" data-band={scoreBand(score)}>
      {formatScore(score)}
    </span>
  );
}

// ProgressBar is a determinate bar with its value exposed to assistive
// technology. The width is the one inline style the pages set.
export function ProgressBar({ percent, label, tone }: { percent: number; label: string; tone?: Tone }) {
  const value = Math.max(0, Math.min(100, Math.round(percent)));
  return (
    <span className={tone ? `bar ${tone}` : "bar"} role="progressbar" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={value}>
      <span style={{ width: `${value}%` }} />
    </span>
  );
}

const pad = (n: number) => String(n).padStart(2, "0");

// moment reads a full timestamp; a plain date or an unreadable value is null,
// so it is shown as written.
function moment(iso: string): Date | null {
  if (iso.length <= 10) return null;
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? null : d;
}

// formatDate shows the calendar day. A full timestamp is shown on this
// computer's clock, so "today" on every page is the user's today.
export function formatDate(iso: string): string {
  const d = moment(iso);
  return d ? `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}` : iso.slice(0, 10);
}

// usd shows dollars to the cent, in one form on every page; a cost under a
// cent is not shown as nothing.
export const usd = (n: number) => (n > 0 && n < 0.005 ? "<$0.01" : `$${n.toFixed(2)}`);

// formatDateTime shows a timestamp to the minute on this computer's clock, in
// one form on every page; to the second where two in a list share the minute.
export function formatDateTime(iso: string, seconds = false): string {
  const d = moment(iso);
  return d ? `${formatDate(iso)} ${pad(d.getHours())}:${pad(d.getMinutes())}${seconds ? `:${pad(d.getSeconds())}` : ""}` : iso;
}

const ACRONYMS = new Set(["ats", "dm", "api", "url", "cv", "llm", "pdf", "eeo", "otp", "ok"]);

// humanize turns a machine token such as "never-run" into words for a chip;
// a sentence already in words keeps its own capitals, and an acronym such as
// "ats" is written in capitals.
export function humanize(token: string): string {
  const words = token
    .replace(/[-_]+/g, " ")
    .trim()
    .split(" ")
    .map((w) => (ACRONYMS.has(w) ? w.toUpperCase() : w))
    .join(" ");
  return words ? words[0].toUpperCase() + words.slice(1) : words;
}

// Truncate keeps a long value, a role, to two lines in a table cell, so two
// openings of one title ("Staff Software Engineer, Payments ...") show the
// words that tell them apart; the whole value is in its title.
//
//   <td data-label="Role"><Truncate text={row.role} /></td>
export function Truncate({ text }: { text: string }) {
  if (!text) return null;
  return (
    <span className="clamp-2" title={text}>
      {text}
    </span>
  );
}

// useMoreFilters folds a page's secondary filters on a phone, where they would
// fill the first screen: the search shows with a More filters button, and the
// rest shows once it is pressed, or while any of them is set, so a narrowed
// list says why.
export function useMoreFilters(set: number): { hidden: boolean; toggle: ReactNode } {
  const phone = useNarrow();
  const [open, setOpen] = useState(false);
  const hidden = phone && !open && set === 0;
  const toggle = hidden ? (
    <button type="button" className="more-filters" aria-expanded={false} onClick={() => setOpen(true)}>
      More filters
    </button>
  ) : null;
  return { hidden, toggle };
}

// useCapped shows the first `cap` items of a list and `more()` reveals the
// rest, so a long list does not make the page long. Nothing it reveals is
// state-changing.
export function useCapped<T>(items: T[], cap: number): { shown: T[]; hidden: number; more: () => void } {
  const [revealed, setRevealed] = useState(false);
  const shown = revealed ? items : items.slice(0, cap);
  return { shown, hidden: items.length - shown.length, more: () => setRevealed(true) };
}

// ShowMore goes once the rows are revealed; the keyboard moves to the first
// control they bring, not to the top of the list.
export function ShowMore({ hidden, onMore, what = "" }: { hidden: number; onMore: () => void; what?: string }) {
  if (hidden <= 0) return null;
  const reveal = (box: HTMLElement | null | undefined) => {
    const before = new Set(box ? focusables(box) : []);
    flushSync(onMore);
    if (box) focusables(box).find((e) => !before.has(e))?.focus();
  };
  return (
    <p>
      <button type="button" onClick={(e) => reveal(e.currentTarget.parentElement?.parentElement)}>
        Show {hidden} more{what ? ` ${what}` : ""}
      </button>
    </p>
  );
}

// today is this computer's calendar day as an ISO date, for a date field's
// default and its upper bound.
export const today = () => formatDate(new Date().toISOString());

// UndoNote says what a move did and takes it back. Undo takes focus when the
// note appears, because the row the press came from may be gone, and the note
// takes it when the move is undone, so focus never falls to the top of the
// page. onUndo answers whether the move was undone; a refusal is the caller's
// to show. Mount it in a live region, keyed by the move. keepFocus leaves focus
// where the caller put it (a form ready for the next entry); the live region
// still announces the note.
//
//   <UndoNote key={n} done="Skipped Acme." undone="Acme is back." onUndo={back} />
export function UndoNote({ done, undone, onUndo, keepFocus }: { done: ReactNode; undone: ReactNode; onUndo: () => Promise<boolean>; keepFocus?: boolean }) {
  const [state, setState] = useState<"done" | "busy" | "undone">("done");
  const note = useRef<HTMLParagraphElement>(null);
  const undo = async () => {
    if (state === "busy") return;
    setState("busy");
    const ok = await onUndo();
    setState(ok ? "undone" : "done");
    if (ok) note.current?.focus();
  };
  return (
    <p ref={note} tabIndex={-1} className="note ok">
      {state === "undone" ? undone : done}{" "}
      {state !== "undone" && (
        <button type="button" autoFocus={!keepFocus} aria-disabled={state === "busy"} onClick={() => void undo()}>
          Undo
        </button>
      )}
    </p>
  );
}

export function Pager({ page, pageSize, total, onPage }: { page: number; pageSize: number; total: number; onPage: (page: number) => void }) {
  const pages = Math.max(1, Math.ceil(total / pageSize));
  return (
    <nav aria-label="Pages" className="pager">
      <button type="button" onClick={() => onPage(page - 1)} disabled={page <= 1}>
        Previous
      </button>
      <span aria-live="polite">
        Page {page} of {pages}
      </span>
      <button type="button" onClick={() => onPage(page + 1)} disabled={page >= pages}>
        Next
      </button>
    </nav>
  );
}

// safeHref admits only web links, so a posting URL from a report can never be
// a script URL.
export function safeHref(raw: string): string | undefined {
  try {
    const u = new URL(raw);
    return u.protocol === "https:" || u.protocol === "http:" ? u.href : undefined;
  } catch {
    return undefined;
  }
}

export function ExternalLink({ href, children }: { href: string; children: ReactNode }) {
  const safe = safeHref(href);
  if (!safe) return <>{children}</>;
  return (
    <a href={safe} target="_blank" rel="noopener noreferrer">
      {children}
    </a>
  );
}

// QrCode draws the modules the server encoded; nothing is fetched. The quiet
// zone and the light background are part of the picture so that it scans on a
// dark page too.
export function QrCode({ rows, size, label }: { rows: string[]; size: number; label: string }) {
  const quiet = 4;
  let path = "";
  rows.forEach((row, y) => {
    for (let x = 0; x < row.length; x++) if (row[x] === "1") path += `M${x + quiet} ${y + quiet}h1v1h-1z`;
  });
  const side = size + 2 * quiet;
  return (
    <svg viewBox={`0 0 ${side} ${side}`} width={224} height={224} role="img" aria-label={label} shapeRendering="crispEdges">
      <rect width={side} height={side} fill="#fff" />
      <path d={path} fill="#000" />
    </svg>
  );
}

const DAY_MS = 86_400_000;

// ageLabel turns a date or a timestamp into days ago, counted in this
// computer's calendar days; a date that cannot be read is shown as it came.
export function ageLabel(date: string, now: number = Date.now()): string {
  const [y, m, d] = formatDate(date).split("-").map(Number);
  const then = new Date(y, m - 1, d).getTime();
  if (Number.isNaN(then)) return date;
  // Rounded, so a day across a daylight-saving change counts once.
  const days = Math.round((new Date(now).setHours(0, 0, 0, 0) - then) / DAY_MS);
  if (days <= 0) return "today";
  return days === 1 ? "1 day ago" : `${days} days ago`;
}
