import { Activity, CircleAlert, ClipboardPaste, CornerDownLeft, FileText, ListChecks, MessageSquareText, Search, Settings, Zap, type LucideIcon } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import { ApiError, get } from "../api/client";
import { candidacyPage, reportDetail, reportPage } from "../api/decoders";
import { chatSessionPage } from "../api/decoders_agent";
import { boardEntryList } from "../api/decoders_runs";
import { useResource } from "../hooks";
import { PAGES } from "../pages";
import { loadNeedsYou, needsYouItems } from "../pages/Home";
import { navigate } from "../router";
import { Modal } from "./Modal";
import { formatScore, humanize, safeHref } from "./parts";
import { keyLabel, type Command } from "./Shortcuts";

/** isAt says whether the address shows a link: the same path and every query value the link names. */
export function isAt(link: string, loc: { pathname: string; search: string }): boolean {
  const u = new URL(link, "http://local");
  const here = new URLSearchParams(loc.search);
  return u.pathname === loc.pathname && [...u.searchParams].every(([k, v]) => here.get(k) === v);
}

// fold lowers a text and drops its accents, so "resume" finds "Résumés".
const fold = (s: string) => s.normalize("NFD").replace(/[\u0300-\u036f]/g, "").toLowerCase();

// slip says whether a and b differ by at most one letter added, dropped,
// changed or swapped with the next.
function slip(a: string, b: string): boolean {
  let i = 0;
  while (i < a.length && a[i] === b[i]) i++;
  const [x, y] = [a.slice(i), b.slice(i)];
  return x === y || x.slice(1) === y.slice(1) || x.slice(1) === y || x === y.slice(1) || (x[0] === y[1] && x[1] === y[0] && x.slice(2) === y.slice(2));
}

// finds says whether every word typed is in the text, or, from four letters
// on, is one slip from the start of one of its words ("trakcer", "setings").
export function finds(query: string, text: string): boolean {
  const hay = fold(text);
  const words = hay.split(/[^a-z0-9]+/);
  return fold(query)
    .split(/\s+/)
    .every((w) => hay.includes(w) || (w.length >= 4 && words.some((x) => [-1, 0, 1].some((d) => slip(w, x.slice(0, w.length + d))))));
}

interface Row {
  key: string;
  group: string;
  title: string;
  icon: LucideIcon;
  side?: string;
  keys?: string;
  run: () => void;
}

// SEARCH_DELAY_MS waits for typing to pause before applications, reports and
// conversations are searched on the server; everything else is matched here.
const SEARCH_DELAY_MS = 150;
const GROUP_CAP = 5;

// The palette opens a page, an item of Home's Needs you (the same list, each
// run and conversation named), a recent run, an application (by
// company or role), a report (by company) or a conversation, or runs an
// action from the command registry. It is a combobox: the
// keyboard stays in the field, Up and Down move the active row over every
// group, Enter opens it. The active row is kept by key, so results that
// arrive later do not move it.
export function Palette({ onClose, actions }: { onClose: () => void; actions: readonly Command[] }) {
  const [text, setText] = useState("");
  const [activeKey, setActiveKey] = useState<string | null>(null);
  const input = useRef<HTMLInputElement>(null);
  const list = useId();
  const t = text.trim().toLowerCase();
  const [search, setSearch] = useState("");
  useEffect(() => {
    const timer = setTimeout(() => setSearch(t.slice(0, 200)), SEARCH_DELAY_MS);
    return () => clearTimeout(timer);
  }, [t]);
  const [needs] = useResource(loadNeedsYou, []);
  const [runs] = useResource(() => get("/api/runs", boardEntryList, { limit: 100 }), []);
  const [talks] = useResource(() => (search ? get("/api/chat/sessions", chatSessionPage, { limit: GROUP_CAP, q: search }) : Promise.resolve(null)), [search]);
  const [apps] = useResource(() => (search ? get("/api/candidacies", candidacyPage, { query: search, pageSize: GROUP_CAP }) : Promise.resolve(null)), [search]);
  const [reports] = useResource(() => (search ? get("/api/reports", reportPage, { company: search, pageSize: GROUP_CAP }) : Promise.resolve(null)), [search]);

  // A number (such as 12) names one report; no such report is no row, not a failure.
  const number = /^#?0*(\d{1,9})$/.exec(search)?.[1];
  const [byNumber] = useResource(
    () => (number ? get(`/api/reports/${number}`, reportDetail).then((d) => d.report, (e) => (e instanceof ApiError && e.status === 404 ? null : Promise.reject(e))) : Promise.resolve(null)),
    [number],
  );

  // Settings' sections are found by their names and the settings they hold;
  // the page's code is fetched once the first page has settled anyway.
  const [settings] = useResource(() => import("../pages/Settings"), []);
  const hit = (s: string) => t === "" || finds(t, s);
  // A page named by the query comes before one it only describes.
  const pages = PAGES.map((p) => ({ p, rank: hit(p.title) ? 0 : hit([p.group, p.blurb, ...(p.terms ?? [])].join(" ")) ? 1 : 2 }))
    .filter((x) => x.rank < 2)
    .sort((a, b) => a.rank - b.rank)
    .map((x) => x.p);
  // Text that only looks like an address is not offered: safeHref parses it.
  const url = /^https?:\/\/\S+$/i.test(text.trim()) ? text.trim() : "";
  const href = url ? safeHref(url) : undefined;
  const open = (link: string) => () => {
    onClose();
    navigate(link);
  };
  const waiting = (needs.data ? needsYouItems(needs.data, true) : []).filter((i) => hit(i.text));
  const keyFor = new Map(actions.flatMap((c) => (c.id.startsWith("go:") && c.keys[0] ? [[c.id.slice(3), keyLabel(c.keys[0])]] : [])));
  const rows: Row[] = [
    // A busy day's waiting items would push the pages out of sight; the rest are one row.
    ...waiting.slice(0, GROUP_CAP).map((i) => ({ key: `need:${i.key}`, group: "Needs you", title: i.text, icon: i.icon, side: i.detail, run: open(i.to) })),
    ...(waiting.length > GROUP_CAP ? [{ key: "waiting:more", group: "Needs you", title: `${waiting.length - GROUP_CAP} more need you`, icon: CircleAlert, run: open("/") }] : []),
    ...pages.map((p) => ({ key: `page:${p.path}`, group: "Pages", title: p.title, icon: p.icon, side: p.group, keys: keyFor.get(p.path), run: open(p.path) })),
    ...(t === "" || !settings.data ? [] : settings.data.SECTIONS.filter((s) => hit([s.label, ...s.terms].join(" "))).map((s) => ({ key: `set:${s.anchor}`, group: "Settings", title: s.label, icon: Settings, side: s.group, run: open(s.to ?? `/settings#${s.anchor}`) }))),
    ...(t === ""
      ? []
      : (runs.data?.entries ?? [])
          .filter((e) => hit(e.title) && !waiting.some((i) => i.runs?.includes(e.id)))
          .slice(0, GROUP_CAP)
          .map((e) => ({ key: `run:${e.id}`, group: "Recent runs", title: e.title, icon: Activity, side: humanize(e.state), run: open(e.link) }))),
    ...(t === "" || !apps.data ? [] : apps.data.entries.map((e) => ({ key: `app:${e.id}`, group: "Applications", title: `${e.company}: ${e.role}`, icon: ListChecks, side: humanize(e.status), run: open(`/tracker?row=${e.id}`) }))),
    ...(t === "" || !reports.data
      ? []
      : [...(number && byNumber.data ? [byNumber.data] : []), ...reports.data.reports.filter((r) => r.number !== byNumber.data?.number)].map((r) => ({ key: `report:${r.number}`, group: "Reports", title: `#${r.number} ${r.company}: ${r.role}`, icon: FileText, side: formatScore(r.score), run: open(`/reports?id=${r.number}`) }))),
    ...(t === "" || !talks.data ? [] : talks.data.sessions.map((s) => ({ key: `chat:${s.id}`, group: "Conversations", title: s.title, icon: MessageSquareText, run: open(`/ask?conversation=${encodeURIComponent(s.id)}`) }))),
    // A pasted posting address is one press from its evaluation.
    ...(href ? [{ key: "do:post-url", group: "Actions", title: "Evaluate this posting", icon: ClipboardPaste, side: new URL(href).host, run: open(`/?post=${encodeURIComponent(url)}`) }] : []),
    ...actions
      .filter((c) => c.palette && hit(c.title))
      .map((c) => ({
        key: `do:${c.id}`, group: "Actions", title: c.title, icon: Zap, keys: c.keys[0] && keyLabel(c.keys[0]),
        run: () => {
          onClose();
          c.run();
        },
      })),
  ];
  const groups = rows.reduce<{ name: string; rows: Row[] }[]>((all, r) => {
    const last = all[all.length - 1];
    if (last?.name === r.group) last.rows.push(r);
    else all.push({ name: r.group, rows: [r] });
    return all;
  }, []);
  const current = Math.max(0, rows.findIndex((r) => r.key === activeKey));
  const optionId = (i: number) => `${list}-${i}`;
  useEffect(() => {
    document.getElementById(optionId(current))?.scrollIntoView?.({ block: "nearest" });
  }, [current]);
  const move = (by: number) => {
    if (rows.length > 0) setActiveKey(rows[(current + by + rows.length) % rows.length].key);
  };
  const searching = needs.status === "loading" || (t !== "" && (t.slice(0, 200) !== search || [runs, talks, apps, reports, byNumber].some((r) => r.status === "loading")));
  const reads: [string, { status: string }][] = [["what needs you", needs]];
  if (t !== "") reads.push(["recent runs", runs], ["applications", apps], ["reports", reports], ["report by number", byNumber], ["conversations", talks]);
  const failed = [...reads.filter(([, r]) => r.status === "error").map(([what]) => what), ...(needs.data?.unread ?? [])];
  const status = [rows.length > 0 ? "" : searching ? "Searching." : "Nothing matches.", failed.length > 0 ? `Could not read ${failed.join(", ")}.` : ""].filter(Boolean).join(" ");
  let index = 0;
  return (
    <Modal title="Go to page" onClose={onClose} initialFocus={input} variant="palette">
      <div className="palette-search">
        <Search />
        <label>
          <span className="sr-only">Search pages, settings, applications, reports, runs, conversations and actions</span>
          <input
            ref={input}
            type="search"
            role="combobox"
            aria-expanded={rows.length > 0}
            aria-controls={list}
            aria-autocomplete="list"
            aria-activedescendant={rows.length > 0 ? optionId(current) : undefined}
            placeholder="Search or run a command"
            value={text}
            onChange={(e) => {
              setText(e.target.value);
              setActiveKey(null);
            }}
            onKeyDown={(e) => {
              if (e.key === "ArrowDown" || e.key === "ArrowUp") {
                e.preventDefault();
                move(e.key === "ArrowDown" ? 1 : -1);
              } else if (e.key === "Enter" && rows[current]) {
                e.preventDefault();
                rows[current].run();
              }
            }}
          />
        </label>
      </div>
      <div className="palette-list" role="listbox" id={list} aria-label="Matches">
        {groups.map((g) => (
          <div key={g.name} role="group" aria-label={g.name}>
            <div className="palette-heading" aria-hidden="true">
              {g.name}
            </div>
            {g.rows.map((r) => {
              const i = index++;
              const Icon = r.icon;
              return (
                <div key={r.key} id={optionId(i)} role="option" aria-selected={i === current} onClick={r.run} onMouseMove={() => i !== current && setActiveKey(r.key)}>
                  <Icon />
                  <span className="palette-title">{r.title}</span>
                  {(r.side || r.keys) && (
                    <span className="palette-group" aria-hidden="true">
                      {r.side}
                      {r.keys && <kbd>{r.keys}</kbd>}
                    </span>
                  )}
                </div>
              );
            })}
          </div>
        ))}
      </div>
      <p className="empty palette-status" role="status">
        {status}
      </p>
      <p className="palette-foot" aria-hidden="true">
        <span>
          <kbd>↑</kbd> <kbd>↓</kbd> move
        </span>
        <span>
          <kbd>
            <CornerDownLeft />
          </kbd>{" "}
          opens
        </span>
        <span>
          <kbd>esc</kbd> closes
        </span>
      </p>
    </Modal>
  );
}
