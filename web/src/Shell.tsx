import { Bell, Ellipsis, Flower2, Monitor, Moon, PanelLeftClose, PanelLeftOpen, PanelRight, Search, Sun, TriangleAlert, Wifi } from "lucide-react";
import { Component, Suspense, use, useCallback, useContext, useEffect, useRef, useState, type CSSProperties, type ComponentType, type FocusEvent, type ReactNode } from "react";
import { get, post } from "./api/client";
import { applyList } from "./api/decoders_apply";
import { gmailInboxPage } from "./api/decoders_gmail";
import { remoteStatus } from "./api/decoders_remote";
import { boardSummary, noticeList } from "./api/decoders_runs";
import { BoardContext, useBoardState, useNarrow, useResource } from "./hooks";
import { GROUPS, PAGES, pageFor, type PageDef } from "./pages";
import { Link, navigate, useLocation } from "./router";
import { hasShell } from "./desktop";
import Home, { loadNeedsYou, needsYouItems } from "./pages/Home";
import { Empty, ErrorNote, Loading, formatDateTime } from "./ui/parts";
import { EmptyState } from "./ui/EmptyState";
import { DemoBanner } from "./ui/DemoBanner";
import { NotFound } from "./ui/Placeholder";
import { Palette, isAt } from "./ui/Palette";
import { Settle } from "./ui/Settle";
import { Shortcuts, ariaKeys, commands, keyLabel, matches, type Command } from "./ui/Shortcuts";
import { Button } from "./ui/Button";
import { Modal } from "./ui/Modal";
import { focusMain, keepFocus } from "./ui/focus";
import { BrowserPanel, togglePanel, usePanel } from "./pages/browser/Lanes";
import type { BoardSummary, NoticeList } from "./wire";
import careerCat from "./assets/career-cat.webp";

const loaders: Record<string, () => Promise<{ default: ComponentType }>> = {
  "/apply-next": () => import("./pages/ApplyNext"),
  "/discovery": () => import("./pages/Discovery"),
  "/leads": () => import("./pages/Leads"),
  "/tracker": () => import("./pages/Tracker"),
  "/reports": () => import("./pages/Reports"),
  "/answer-bank": () => import("./pages/AnswerBank"),
  "/profile": () => import("./pages/Profile"),
  "/wiki": () => import("./pages/Wiki"),
  "/ask": () => import("./pages/AskAgent"),
  "/agent-control": () => import("./pages/AgentControl"),
  "/gmail": () => import("./pages/Gmail"),
  "/runs": () => import("./pages/Runs"),
  "/browser": () => import("./pages/Browser"),
  "/usage": () => import("./pages/Usage"),
  "/settings": () => import("./pages/Settings"),
  "/setup": () => import("./pages/Setup"),
  "/guide": () => import("./pages/Guide"),
};

// Pages are loaded by hand rather than with React.lazy. A boundary that
// suspends is revealed no sooner than 300 ms after its fallback was shown, even
// when the chunk arrives at once, and lazy suspends on its first render even
// for a chunk that is already in memory. Here a loaded page renders without
// suspending, the current one is loaded before the first render, and the rest
// are fetched once it is up, so no page waits behind a fallback.
const loaded = new Map<string, ComponentType>();
const loading = new Map<string, Promise<ComponentType>>();

function loadPage(path: string): Promise<ComponentType> {
  let p = loading.get(path);
  if (!p) {
    p = loaders[path]().then((m) => {
      loaded.set(path, m.default);
      return m.default;
    });
    p.catch(() => loading.delete(path));
    loading.set(path, p);
  }
  return p;
}

function Page({ path }: { path: string }) {
  const Component = loaded.get(path) ?? use(loadPage(path));
  return <Component />;
}

const built: Record<string, ComponentType> = { "/": Home };
for (const path of Object.keys(loaders)) built[path] = () => <Page path={path} />;

// PageBoundary keeps a page that throws, or whose chunk will not load, to the
// page; before it the whole window went blank.
export class PageBoundary extends Component<{ path?: string; children: ReactNode }, { error: unknown }> {
  state: { error: unknown } = { error: null };
  static getDerivedStateFromError(error: unknown) {
    return { error };
  }
  componentDidUpdate(prev: { path?: string }) {
    if (prev.path !== this.props.path && this.state.error != null) this.setState({ error: null });
  }
  render() {
    const { error } = this.state;
    if (error == null) return this.props.children;
    const why = error instanceof Error ? error.message : String(error);
    // A chunk that failed to load stays failed in this document (the browser
    // keeps the failure), so only a reload brings it.
    const chunk = /dynamically imported module|importing a module script/i.test(why);
    return (
      <div role="alert">
        <EmptyState
          icon={TriangleAlert}
          title="This page could not be shown."
          action={
            <>
              {!chunk && <Button onClick={() => this.setState({ error: null })}>Try again</Button>} <Button variant={chunk ? "primary" : undefined} onClick={() => window.location.reload()}>Reload the app</Button>
            </>
          }
        >
          {chunk ? "Its code did not arrive: the server was busy, or it now runs a newer version. Reload the app to fetch it again." : why}
        </EmptyState>
      </div>
    );
  }
}

export async function preloadPage(pathname: string): Promise<void> {
  const path = pageFor(pathname)?.path;
  if (path && loaders[path]) await loadPage(path);
}

function preloadOtherPages(): void {
  for (const path of Object.keys(loaders)) loadPage(path).catch(() => {});
}

// The pages lay out by the room they get (container queries on main), so the
// browser panel docks beside them at the width the user chose, narrowed so the
// page keeps PAGE_MIN beside the sidebar. A window that leaves the panel less
// than DOCK_MIN that way gets it over the page instead, at its own width.
const SIDEBAR_W = 236;
const RAIL_W = 60;
const PAGE_MIN = 560;
const DOCK_MIN = 440;

// useDockedWidth is the width the panel docks at, or 0 when it lies over the page.
function useDockedWidth(panelWidth: number, sidebar: number): number {
  const [width, setWidth] = useState(() => window.innerWidth);
  useEffect(() => {
    const on = () => setWidth(window.innerWidth);
    window.addEventListener("resize", on);
    return () => window.removeEventListener("resize", on);
  }, []);
  const room = width - sidebar - PAGE_MIN;
  return room >= DOCK_MIN ? Math.min(panelWidth, room) : 0;
}

type Theme = "auto" | "light" | "dark";

function useTheme(): [Theme, () => void] {
  const [theme, setTheme] = useState<Theme>(() => {
    try {
      const saved = window.localStorage.getItem("applysmith-theme");
      return saved === "light" || saved === "dark" ? saved : "auto";
    } catch {
      return "auto";
    }
  });
  useEffect(() => {
    if (theme === "auto") document.documentElement.removeAttribute("data-theme");
    else document.documentElement.setAttribute("data-theme", theme);
    try {
      if (theme === "auto") window.localStorage.removeItem("applysmith-theme");
      else window.localStorage.setItem("applysmith-theme", theme);
    } catch {
      // The preference is a convenience; a blocked store only loses it.
    }
  }, [theme]);
  return [theme, () => setTheme((t) => (t === "auto" ? "light" : t === "light" ? "dark" : "auto"))];
}

// useRail keeps the sidebar narrowed to its icons, a preference of this
// browser like the theme. The names stay for screen readers and as tooltips.
function useRail(): [boolean, () => void] {
  const [rail, setRail] = useState(() => {
    try {
      return window.localStorage.getItem("applysmith-sidebar") === "rail";
    } catch {
      return false;
    }
  });
  useEffect(() => {
    try {
      if (rail) window.localStorage.setItem("applysmith-sidebar", "rail");
      else window.localStorage.removeItem("applysmith-sidebar");
    } catch {
      // A blocked store only loses the preference.
    }
  }, [rail]);
  return [rail, () => setRail((r) => !r)];
}

// typing says whether a key press belongs to a field, where ? is a character.
function typing(target: EventTarget | null): boolean {
  return target instanceof HTMLElement && (target.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName));
}

interface Badge {
  count: number | undefined;
  /** What the count is, read after the link's name. */
  label: string;
  /** The whole state, as the link's tooltip. */
  title?: string;
  warn?: boolean;
}

function NavLink({ page, badge, current, rail }: { page: PageDef; badge?: Badge; current: boolean; rail?: boolean }) {
  const Icon = page.icon;
  const tip = rail ? [page.title, badge?.title ?? (badge?.count ? badge.label : "")].filter(Boolean).join(": ") : badge?.title;
  return (
    <Link to={page.path} aria-current={current ? "page" : undefined} title={tip}>
      <Icon />
      <span className="nav-label">{page.title}</span>
      {badge?.count !== undefined && badge.count > 0 && (
        <span className={badge.warn ? "badge warn" : "badge"} aria-label={badge.label}>
          {badge.count}
        </span>
      )}
    </Link>
  );
}

// needingYou counts the runs that wait for the user: needs-you and blocked,
// neither acknowledged.
function needingYou(s: BoardSummary | undefined): number | undefined {
  return s && s.needsYou + s.blocked;
}

function runsState(s: BoardSummary): string {
  return `${s.working} running, ${s.needsYou + s.blocked} needs you, ${s.doneUnacknowledged} finished unseen`;
}

function Navigation({ pathname, runs, narrow, rail }: { pathname: string; runs: BoardSummary | undefined; narrow: boolean; rail: boolean }) {
  const [more, setMore] = useState(false);
  const moreButton = useRef<HTMLButtonElement>(null);
  // Escape closes the sheet wherever focus is: WebKit does not focus a button
  // that is clicked, so after a click focus is still on the page body. A press
  // anywhere but the sheet and its button closes it too, as a sheet does.
  useEffect(() => {
    if (!more) return;
    const close = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      setMore(false);
      moreButton.current?.focus();
    };
    const away = (e: Event) => {
      if (e.target instanceof Element && !e.target.closest(".sheet, .tabbar button")) setMore(false);
    };
    window.addEventListener("keydown", close);
    document.addEventListener("pointerdown", away);
    return () => {
      window.removeEventListener("keydown", close);
      document.removeEventListener("pointerdown", away);
    };
  }, [more]);
  // The badge counts one lane, so it asks for that lane alone and the server
  // places no other. Home's Ready to apply card asks for all four, so on Home
  // the badge asks the same as the card and the two share one request.
  const onHome = pageFor(pathname)?.path === "/";
  const [lane] = useResource(() => get("/api/apply/list", applyList, onHome ? { pageSize: 1 } : { pageSize: 1, lane: "apply-now" }), [onHome], ["tracker"]);
  const ready = lane.data?.lanes.find((l) => l.lane === "apply-now")?.total;
  // The Gmail page's do-now lane, which Home's job-email line counts as well.
  const [mail] = useResource(() => get("/api/gmail/inbox", gmailInboxPage, { lane: "do_now", limit: 1 }), [], ["gmail"]);
  const current = pageFor(pathname)?.path;
  const badges: Record<string, Badge> = {
    "/apply-next": { count: ready, label: `${ready} ready` },
    "/gmail": { count: mail.data?.total, label: `${mail.data?.total} need you now`, warn: true },
    "/runs": { count: needingYou(runs), label: runs ? runsState(runs) : "", title: runs && runsState(runs), warn: true },
  };
  // Narrowed to icons, a link reached by keyboard shows its name beside it. The
  // sidebar scrolls, which would clip a label inside it, so the label is fixed
  // at the link's own position.
  const [tip, setTip] = useState<{ text: string; top: number } | null>(null);
  const showTip = (e: FocusEvent<HTMLElement>) => {
    const a = e.target instanceof HTMLAnchorElement && e.target.matches(":focus-visible") ? e.target : null;
    const box = a?.getBoundingClientRect();
    setTip(a && box ? { text: a.title || a.textContent || "", top: box.top + box.height / 2 } : null);
  };
  const item = (p: PageDef) => (
    <li key={p.path}>
      <NavLink page={p} current={current === p.path} badge={badges[p.path]} rail={rail} />
    </li>
  );

  if (narrow) {
    // A count on a page under More shows on More, so a phone does not hide it.
    const under = PAGES.filter((p) => !p.primary && (badges[p.path]?.count ?? 0) > 0).map((p) => [p, badges[p.path]] as const);
    const hidden = under.reduce((n, [, b]) => n + (b.count ?? 0), 0);
    return (
      <nav aria-label="Main" className="nav tabbar">
        <ul className="plain">
          {PAGES.filter((p) => p.primary).map(item)}
          <li>
            <button type="button" ref={moreButton} aria-expanded={more} aria-controls="more-pages" onClick={() => setMore((m) => !m)}>
              <Ellipsis />
              More
              {hidden > 0 && (
                <span className={under.some(([, b]) => b.warn) ? "badge warn" : "badge"} aria-label={under.map(([p, b]) => `${p.title}: ${b.label}`).join(", ")}>
                  {hidden}
                </span>
              )}
            </button>
          </li>
        </ul>
        {more && (
          <ul id="more-pages" className="plain sheet" onClick={() => setMore(false)}>
            {PAGES.filter((p) => !p.primary).map(item)}
          </ul>
        )}
      </nav>
    );
  }
  return (
    <nav aria-label="Main" className="nav sidebar" onFocus={rail ? showTip : undefined} onBlur={rail ? () => setTip(null) : undefined}>
      {rail && tip && (
        <span className="rail-tip" aria-hidden="true" style={{ top: tip.top }}>
          {tip.text}
        </span>
      )}
      <div className="brand">
        <span className="brand-mark" aria-hidden="true">
          <img src={careerCat} alt="" width="40" height="40" />
        </span>
        <span className="brand-name">Applysmith<small>Your career companion</small></span>
      </div>
      {GROUPS.map((g) => (
        <section key={g} role="group" aria-label={g}>
          <h2 className="group">{g}</h2>
          <ul className="plain">{PAGES.filter((p) => p.group === g).map(item)}</ul>
        </section>
      ))}
      {!rail && <div className="sidebar-note"><Flower2 aria-hidden="true" /><p>Small steps.<br /><strong>Big possibilities.</strong></p></div>}
    </nav>
  );
}

// RemoteIndicator is the persistent notice that the dashboard can be reached
// from outside. It stays out of the way while remote access is off, and for a
// remote device, whose credential the status route refuses.
function RemoteIndicator() {
  const [status] = useResource(() => get("/api/remote/status", remoteStatus), [], ["notifications"]);
  const s = status.data;
  if (!s || s.mode === "off") return null;
  return (
    <span role="status">
      <Link to="/settings#settings-remote" className="chip warn remote-chip">
        <Wifi />
        Remote access: {s.mode}, {s.connected} connected
      </Link>
    </span>
  );
}

// Notices is the passive list of what no desktop notification showed: an
// announcement quiet hours hold, one made while the app was closed, a pairing.
// It never interrupts; the unread count is read out politely when it changes.
// A remote device, whose credential the route refuses, does not get it. The
// list is read once the first page has settled, so it does not share the
// opening's connections with the reads that decide the page.
// The palette's "Show notices" opens it too, reading it first if need be.
function Notices({ ready, open, setOpen }: { ready: boolean; open: boolean; setOpen: (open: boolean) => void }) {
  const [res, reload] = useResource(() => (ready || open ? get("/api/notices", noticeList) : new Promise<NoticeList>(() => {})), [ready || open], ["notifications"]);
  const [error, setError] = useState<unknown>(null);
  const data = res.data;
  if (!data) return null;
  const read = (ids?: number[]) => {
    setError(null);
    post("/api/notices/read", noticeList, ids ? { ids } : {}).then(reload, setError);
  };
  const unread = data.unread > 0 ? `${data.unread} unread ${data.unread === 1 ? "notice" : "notices"}` : "";
  return (
    <>
      <Button variant="ghost" icon={Bell} className="icon-only notices-button" onClick={() => setOpen(true)} aria-label={unread ? `Notices: ${unread}` : "Notices"} title="Notices">
        {data.unread > 0 && (
          <span className="badge" aria-hidden="true">
            {data.unread > 99 ? "99+" : data.unread}
          </span>
        )}
      </Button>
      <span role="status" className="sr-only">
        {unread}
      </span>
      {open && (
        <Modal title="Notices" variant="drawer" onClose={() => setOpen(false)}>
          <p className="muted">What no desktop notification showed: updates held during quiet hours, ones made while the app was closed, and pairings.</p>
          {data.unread > 0 && (
            <Button size="sm" onClick={() => read()}>
              Mark all as read
            </Button>
          )}
          {error != null && <ErrorNote error={error} what="the notices" />}
          {data.notices.length === 0 && <Empty>No notices.</Empty>}
          <ul className="plain notices">
            {data.notices.map((n) => (
              <li key={n.id} className={n.read ? undefined : "unread"}>
                <p className="notice-title">
                  {!n.read && <span className="sr-only">Unread: </span>}
                  {n.title}
                </p>
                {n.body && <p>{n.body}</p>}
                <p className="muted">
                  <time dateTime={n.createdAt}>{formatDateTime(n.createdAt)}</time>
                  {n.link?.startsWith("/") && (
                    <Link
                      to={n.link}
                      onClick={() => {
                        if (!n.read) read([n.id]);
                        setOpen(false);
                      }}
                    >
                      Open
                    </Link>
                  )}
                  {!n.read && (
                    <Button variant="ghost" size="sm" onClick={() => read([n.id])} aria-label={`Mark read: ${n.title}`}>
                      Mark read
                    </Button>
                  )}
                </p>
              </li>
            ))}
          </ul>
        </Modal>
      )}
    </>
  );
}

// Banner is the one notice of a server that is not answering. The stream
// reconnects with backoff (up to 30 s apart); Try now does not wait it out.
// In the app, Restart Server also starts the app's own server when the one it
// attached to stopped (docs/architecture/app.md).
function Banner() {
  const board = useContext(BoardContext);
  const { reconnecting, stale } = useBoardState();
  if (!reconnecting) return null;
  return (
    <div role="status" className="banner">
      The server is not answering. The dashboard reconnects by itself and reloads what it could not load, and it keeps what you typed.{stale ? " What you see may be out of date." : ""} If it does
      not come back, {hasShell() ? <>choose Restart Server in the Applysmith menu-bar menu.</> : <>start the Applysmith app or run <code>applysmith serve</code>.</>}{" "}
      <Button size="sm" onClick={() => board.retryNow()}>
        Try now
      </Button>
    </div>
  );
}

// nextNeedingYou opens the item after the one the address shows, in the order
// of Home's Needs you (each run and conversation named), wrapping round; it
// answers what to say when there is none to move to. Two items on one page
// are one stop. A list it cannot read leaves its items out.
async function nextNeedingYou(): Promise<string> {
  const data = await loadNeedsYou();
  const items = needsYouItems(data, true).map((i) => ({ link: i.to })).filter((w, i, all) => all.findIndex((o) => o.link === w.link) === i);
  const unread = data.unread.length > 0 ? " Part of the list could not be read." : "";
  const at = items.findIndex((w) => isAt(w.link, window.location));
  if (items.length === 0) return `Nothing needs you.${unread}`;
  if (items.length === 1 && at === 0) return `Nothing else needs you.${unread}`;
  navigate(items[(at + 1) % items.length].link);
  return unread.trim();
}

// shortcutAllowed keeps a shortcut away from keys something else owns: a
// field being typed in or composed (an input method), a key a widget already
// handled, and any open dialog. A drawer a page opened (a run, an interview's
// prep, a job email) is the one dialog the next-item command may leave, so a
// repeated key walks every item; leaving still asks over unsaved edits.
function shortcutAllowed(e: KeyboardEvent, c: Command): boolean {
  if (e.defaultPrevented || e.isComposing || e.keyCode === 229 || e.repeat) return false;
  if (typing(e.target) && !c.inFields) return false;
  const open = Array.from(document.querySelectorAll("dialog[open]"));
  if (open.length === 0) return true;
  const main = document.getElementById("main");
  return !!c.overDrawer && open.every((d) => d.classList.contains("drawer") && !!main?.contains(d));
}

export function Shell() {
  const { pathname } = useLocation();
  const [palette, setPalette] = useState(false);
  const [shortcuts, setShortcuts] = useState(false);
  const [notices, setNotices] = useState(false);
  const [note, setNote] = useState("");
  const [theme, cycleTheme] = useTheme();
  const [rail, toggleRail] = useRail();
  const narrow = useNarrow();
  const main = useRef<HTMLElement>(null);
  const first = useRef(true);
  const page = pageFor(pathname);
  const [runs] = useResource(() => get("/api/runs/summary", boardSummary), [], ["runs"], { whileHidden: true });
  const waiting = needingYou(runs.data) ?? 0;
  const browser = usePanel();
  const [settled, setSettled] = useState(false);
  const onSettled = useCallback(() => {
    setSettled(true);
    preloadOtherPages();
  }, []);

  const list = commands({
    openPalette: () => setPalette(true),
    openShortcuts: () => setShortcuts(true),
    nextNeedingYou: () => void nextNeedingYou().then(setNote, () => setNote("The waiting items could not be read.")),
    cycleTheme,
    toggleSidebar: toggleRail,
    toggleBrowser: togglePanel,
    showNotices: () => setNotices(true),
    go: (path) => navigate(path),
  });
  const registry = useRef(list);
  registry.current = list;

  useEffect(() => {
    document.title = `${waiting > 0 ? `(${waiting}) ` : ""}${page?.title ?? "Not found"} - Applysmith`;
  }, [page, waiting]);

  // A note from the next-item command clears itself.
  useEffect(() => {
    if (!note) return;
    const timer = setTimeout(() => setNote(""), 4000);
    return () => clearTimeout(timer);
  }, [note]);

  useEffect(() => {
    if (first.current) first.current = false;
    else {
      // A new page opens at its top; focus moves to it without the browser
      // scrolling the content under the sticky top bar.
      main.current?.focus({ preventScroll: true });
      window.scrollTo?.(0, 0);
    }
  }, [page, pathname]);

  useEffect(() => (main.current ? keepFocus(main.current) : undefined), []);

  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      const c = registry.current.find((c) => c.keys.some((k) => matches(e, k)));
      if (!c || !shortcutAllowed(e, c)) return;
      e.preventDefault();
      c.run();
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  }, []);

  const Body = page ? built[page.path] : undefined;
  const ThemeIcon = theme === "light" ? Sun : theme === "dark" ? Moon : Monitor;
  const docked = useDockedWidth(browser.width, rail ? RAIL_W : SIDEBAR_W);
  const shellClass = `shell${rail ? " slim" : ""}${browser.open ? ` with-browser ${docked ? "browser-docked" : "browser-over"}` : ""}${browser.open && browser.full ? " browser-full" : ""}`;
  return (
    <div className={shellClass} style={browser.open ? ({ "--browser-pref": `${docked || browser.width}px` } as CSSProperties) : undefined}>
      <a
        className="skip"
        href="#main"
        onClick={(e) => {
          // The fragment is a page's own address on Settings, so the skip
          // moves focus without changing it.
          e.preventDefault();
          focusMain();
        }}
      >
        Skip to content
      </a>
      <header className="topbar">
        {!narrow && (
          <Button
            variant="ghost"
            icon={rail ? PanelLeftOpen : PanelLeftClose}
            className="icon-only"
            onClick={toggleRail}
            aria-pressed={rail}
            aria-label="Narrow sidebar"
            title={rail ? "Show the page names" : "Narrow the sidebar to icons"}
          />
        )}
        <p className="crumbs">
          {page && <span className="crumb-group">{page.group}</span>}
          <span className="crumb-page">{page?.title ?? "Not found"}</span>
        </p>
        {/* The spacer is also where the next-item command answers. */}
        <span role="status" className="spacer topbar-note">
          {note}
        </span>
        <RemoteIndicator />
        <button type="button" className="palette-button" onClick={() => setPalette(true)} aria-label="Go to page" aria-keyshortcuts={ariaKeys("Mod+K")}>
          <Search />
          <span className="palette-label">Go to page</span>
          <kbd aria-hidden="true" data-keys={keyLabel("Mod+K").replace("Cmd+", "⌘")} />
        </button>
        <Notices ready={settled || !Body} open={notices} setOpen={setNotices} />
        <Button
          variant="ghost"
          icon={PanelRight}
          className="icon-only"
          onClick={togglePanel}
          aria-pressed={browser.open}
          aria-label="Browser panel"
          aria-keyshortcuts={ariaKeys("Mod+Shift+B")}
          title="Browser panel"
        />
        <Button variant="ghost" icon={ThemeIcon} className="icon-only" onClick={cycleTheme} aria-label={`Theme: ${theme}. Change theme`} title={`Theme: ${theme}`}>
          <span className="sr-only">Theme: {theme}</span>
        </Button>
      </header>
      <Navigation pathname={pathname} runs={runs.data} narrow={narrow} rail={rail} />
      {/* Named for the page, so moving focus here on a new page says which. */}
      <main id="main" ref={main} tabIndex={-1} aria-label={page?.title ?? "Not found"}>
        <DemoBanner />
        <Banner />
        {!Body && <NotFound />}
        {Body && (
          <PageBoundary path={page?.path}>
            <Suspense fallback={<Loading lines={6} />}>
              <Settle key={page?.path} onSettled={onSettled}>
                <Body />
              </Settle>
            </Suspense>
          </PageBoundary>
        )}
      </main>
      <BrowserPanel />
      {palette && <Palette actions={list} onClose={() => setPalette(false)} />}
      {shortcuts && <Shortcuts list={list} onClose={() => setShortcuts(false)} />}
    </div>
  );
}
