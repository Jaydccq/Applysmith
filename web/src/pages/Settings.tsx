import { Bell, CalendarClock, Cpu, FileCheck2, FileText, FolderOpen, Funnel, KeyRound, Mail, MessageSquareText, MonitorCog, MoonStar, Puzzle, Radar, Search, Send, ShieldAlert, ShieldCheck, SlidersHorizontal, Wifi, Wrench, type LucideIcon } from "lucide-react";
import { createContext, useCallback, useContext, useEffect, useState, type ComponentType } from "react";
import { get } from "../api/client";
import { llmCost, llmRouting } from "../api/decoders";
import { useResource } from "../hooks";
import { useWhere } from "../identity";
import { Link, navigate, useHash } from "../router";
import { PageHeader } from "../ui/PageHeader";
import { Empty, Loading, Panel } from "../ui/parts";
import { AskAgentSection } from "./settings/AskAgent";
import { ComputerUseSection } from "./settings/ComputerUse";
import { GmailSection } from "./settings/Gmail";
import { AdvancedSection } from "./settings/Advanced";
import { ExtensionSection } from "./settings/Extension";
import { FilesSection } from "./settings/Files";
import { NotificationsSection } from "./settings/Notifications";
import { DocumentsSection, GateSection, PolicySection, ScanSection } from "./settings/Policy";
import { COST_DAYS } from "./settings/modelsShared";
import { ModelsBody } from "./settings/Models";
import { RemoteSection } from "./settings/Remote";
import { RiskSection } from "./settings/Risk";
import { RuntimeSection } from "./settings/Runtime";
import { CAPTCHA_TERMS, CaptchaSection, SignInSection } from "./settings/SignIn";
import { TelegramSection } from "./settings/Telegram";

function Models() {
  const [routing, reload] = useResource(() => get("/api/llm/routing", llmRouting), [], ["runs"], { keep: "settings.routing" });
  const [month] = useResource(() => get("/api/llm/cost", llmCost, { days: COST_DAYS }), [], ["runs"], { keep: "settings.cost-month" });
  const index = useContext(LiveTerms);
  const onNames = useCallback((names: string[]) => index("models", names), [index]);
  return (
    <Panel title="Models and providers" id="settings-models" res={routing} retry={reload} icon={Cpu} what="the model routing" className="models-section">
      {(r) => <ModelsBody routing={r} byTask={month.data?.byTask} reload={reload} onNames={onNames} />}
    </Panel>
  );
}

// The groups of the section rail, in order.
const GROUPS = ["Applying", "Finding jobs", "Assistant", "Alerts", "System"] as const;

// The sections of the page, in rail order, each with its group and the anchor
// the page and the setup catalog link to. Quiet hours is drawn by the
// notifications section. `terms` is the search index: the headings, control
// labels and descriptions the section shows, written as the page writes them
// (a test holds every term to the text the page draws). A setting kept on
// another page is an entry with `to`, its address, listed only as a search
// result.
export const SECTIONS: { id: string; anchor: string; label: string; icon: LucideIcon; group: (typeof GROUPS)[number]; terms: string[]; Body?: ComponentType<{ shown: boolean }>; to?: string }[] = [
  { id: "extension", anchor: "settings-extension", label: "Browser extension", icon: Puzzle, group: "Applying", Body: ExtensionSection, terms: ["Pair the extension", "Extension build this application ships", "pairing code", "developer mode"] },
  { id: "applications", anchor: "settings-applications", label: "Applications", icon: FileCheck2, group: "Applying", Body: PolicySection, terms: ["Account creation", "Cooldown after a risk signal", "A captcha stopped a run", "Save policy"] },
  { id: "documents", anchor: "settings-documents", label: "Documents", icon: FileText, group: "Applying", Body: DocumentsSection, terms: ["Paper size", "US Letter", "A4", "Résumé length", "Language", "Cover letter", "Save document defaults"] },
  { id: "risk", anchor: "settings-risk", label: "Detection and cooldowns", icon: ShieldAlert, group: "Applying", Body: RiskSection, terms: ["Cooling down now", "Challenge rate", "Recent detection signals"] },
  { id: "signin", anchor: "settings-signin", label: "Sign-in credentials", icon: KeyRound, group: "Applying", Body: SignInSection, terms: ["Passwords for application sites", "Registration email", "Shared default", "Site overrides", "Alternate passwords"] },
  { id: "captcha", anchor: "settings-captcha", label: "Captcha solver", icon: ShieldCheck, group: "Applying", Body: CaptchaSection, terms: CAPTCHA_TERMS },
  { id: "scan", anchor: "settings-scan", label: "Scan defaults", icon: Radar, group: "Finding jobs", Body: ScanSection, terms: ["Postings no older than", "Send the top N to evaluation", "Evaluation mode", "Cooldown after a blocked run", "Location scope", "Scan source API keys"] },
  { id: "gate", anchor: "settings-gate", label: "Eligibility gate", icon: Funnel, group: "Finding jobs", Body: GateSection, terms: ["Reject titles that name", "Other title words", "Allowed countries", "Allowed pay currencies", "Most years of experience", "Keep remote postings", "Security clearance", "US persons", "Employer types", "Staffing agencies"] },
  { id: "schedules", anchor: "schedules", to: "/discovery?tab=schedules", label: "Schedules", icon: CalendarClock, group: "Finding jobs", terms: ["New schedule", "Pause all", "Resume all", "Next 7 days", "Built-in timers"] },
  { id: "models", anchor: "settings-models", label: "Models and providers", icon: Cpu, group: "Assistant", Body: Models, terms: ["Accounts", "Subscriptions", "Pay-per-use API keys", "What each job uses", "Change default", "Daily cap", "Spent today", "Model pools", "ChatGPT plan", "Claude"] },
  { id: "askagent", anchor: "settings-askagent", label: "Ask Agent", icon: MessageSquareText, group: "Assistant", Body: AskAgentSection, terms: ["Prompt budget (tokens)", "Summarise long conversations", "Nightly memory review", "Improve from conversations", "Run a round now"] },
  { id: "computeruse", anchor: "settings-computeruse", label: "Computer use", icon: MonitorCog, group: "Assistant", Body: ComputerUseSection, terms: ["PersonalCU", "Accessibility", "Screen Recording", "Open Agent control", "PersonalCU keeps Accessibility and Screen Recording across rebuilds"] },
  { id: "notifications", anchor: "settings-notifications", label: "Notifications", icon: Bell, group: "Alerts", Body: NotificationsSection, terms: ["Desktop notifications", "Telegram notifications", "Announce failed scheduled scans", "Send a test notification"] },
  { id: "notifications", anchor: "settings-quiet", label: "Quiet hours", icon: MoonStar, group: "Alerts", terms: ["Hold normal notifications overnight", "Time zone", "What is held and what is not"] },
  { id: "gmail", anchor: "settings-gmail", label: "Gmail", icon: Mail, group: "Alerts", Body: GmailSection, terms: ["Unattended work", "08:00 and 18:00 briefs", "Send an alert when an important email arrives", "reminder before its deadline"] },
  { id: "telegram", anchor: "settings-telegram", label: "Telegram", icon: Send, group: "Alerts", Body: TelegramSection, terms: ["Bot token", "Pairing", "Get a pairing code"] },
  { id: "runtime", anchor: "settings-runtime", label: "Runtime settings", icon: SlidersHorizontal, group: "System", Body: RuntimeSection, terms: ["Automatically sign in, authorize and register with Google", "Model fallback", "Grounding mode", "Tick required legal declarations", "Semantic knowledge search", "Watch scans in the browser window"] },
  { id: "remote", anchor: "settings-remote", label: "Remote access", icon: Wifi, group: "System", Body: RemoteSection, terms: ["Turn remote access on", "Only this computer can reach the dashboard", "Paired devices"] },
  { id: "files", anchor: "settings-files", label: "Files", icon: FolderOpen, group: "System", Body: FilesSection, terms: ["Workspace folder", "profile.yml", "portals.yml", "policy.yml", "cv.md"] },
  { id: "advanced", anchor: "settings-advanced", label: "Advanced", icon: Wrench, group: "System", Body: AdvancedSection, terms: ["Versions", "Run doctor", "Log", "Export diagnostics", "Command line"] },
];

type Match = { section: (typeof SECTIONS)[number]; hint?: string };

// LiveTerms adds to the index the names a section knows once loaded (the model
// presets and providers), by section id.
const LiveTerms = createContext<(id: string, terms: string[]) => void>(() => {});

// found says whether a word typed is in a text: as written, run together
// ("timezone" finds "Time zone"), or as another form of a word that differs
// only in its last letter ("spend" finds "Spent").
function found(word: string, text: string): boolean {
  const t = text.toLowerCase();
  if (t.includes(word) || t.replace(/[^a-z0-9]+/g, "").includes(word)) return true;
  return word.length >= 4 && t.split(/[^a-z0-9]+/).some((x) => x.length === word.length && x.slice(0, -1) === word.slice(0, -1));
}

// search finds the sections whose name or indexed terms hold every word of the
// query. A hit inside the index is returned as the hint, so the rail can say
// which setting matched; a section that matches only across several terms has
// none. Without a query it lists the sections of this page.
export function search(query: string, live: Record<string, string[]> = {}): Match[] {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return SECTIONS.filter((s) => !s.to).map((section) => ({ section }));
  const has = (t: string) => words.every((w) => found(w, t));
  const out: Match[] = [];
  for (const section of SECTIONS) {
    const terms = [...section.terms, ...(live[section.id] ?? [])];
    if (has(section.label)) out.push({ section });
    else if (has([section.label, ...terms].join(" "))) out.push({ section, hint: terms.find(has) });
  }
  return out;
}

// Sections shows one section at a time, chosen by the address fragment, so a
// link such as /settings#settings-gmail opens that section. Every section is
// mounted from the start and the others are hidden, so each one's data is
// asked for at once and switching sections never waits. The rail groups the
// sections and its search filters them; Enter opens the first match.
function Sections() {
  const hash = useHash();
  const link = SECTIONS.find((s) => s.anchor === hash && !s.to);
  // The section last shown stays open while the fragment names no section.
  const [owner, setOwner] = useState(link?.id ?? SECTIONS[0].id);
  const [query, setQuery] = useState("");
  const [live, setLive] = useState<Record<string, string[]>>({});
  const index = useCallback((id: string, terms: string[]) => setLive((l) => ({ ...l, [id]: terms })), []);
  const active = link?.id ?? owner;
  const current = link?.anchor ?? SECTIONS.find((s) => s.id === active)?.anchor;
  const matches = search(query, live);
  const searching = query.trim() !== "";
  useEffect(() => {
    if (link) setOwner(link.id);
  }, [link]);

  // A section's own anchor shows that section from its top. A fragment that
  // names something inside a section, such as a heading, opens the section
  // that holds it and scrolls it into view.
  useEffect(() => {
    if (!hash) return;
    if (SECTIONS.some((s) => s.anchor === hash && s.Body)) {
      window.scrollTo?.({ top: 0 });
      return;
    }
    const el = document.getElementById(hash);
    const section = el?.closest<HTMLElement>("[data-section]")?.dataset.section;
    if (section) setOwner(section);
    requestAnimationFrame(() => el?.scrollIntoView?.({ block: "start" }));
  }, [hash]);

  return (
    <div className="with-rail">
      <div className="settings-rail">
        <label className="field">
          <span className="sr-only">Search settings</span>
          <span className="search-box">
            <Search aria-hidden="true" />
            <input
              type="search"
              data-page-search
              value={query}
              placeholder="Search settings"
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && matches.length > 0) {
                  e.preventDefault();
                  navigate(matches[0].section.to ?? `/settings#${matches[0].section.anchor}`);
                } else if (e.key === "Escape" && query) {
                  e.preventDefault();
                  setQuery("");
                }
              }}
            />
          </span>
        </label>
        <p role="status" className="sr-only">
          {searching ? `${matches.length} ${matches.length === 1 ? "section matches" : "sections match"}` : ""}
        </p>
        <nav aria-label="Sections on this page" className="subnav">
          {GROUPS.map((g) => {
            const shown = matches.filter((m) => m.section.group === g);
            if (shown.length === 0) return null;
            return (
              <div key={g} role="group" aria-labelledby={`settings-group-${g}`} className="subnav-group">
                <p id={`settings-group-${g}`} className="subnav-heading">
                  {g}
                </p>
                {shown.map(({ section: s, hint }) => {
                  const Icon = s.icon;
                  const body = (
                    <>
                      <Icon />
                      <span>
                        {s.label}
                        {hint && <span className="sub">{hint}</span>}
                      </span>
                    </>
                  );
                  return s.to ? (
                    <Link key={s.anchor} to={s.to}>
                      {body}
                    </Link>
                  ) : (
                    <a key={s.anchor} href={`#${s.anchor}`} aria-current={s.anchor === current ? "true" : undefined}>
                      {body}
                    </a>
                  );
                })}
              </div>
            );
          })}
          {matches.length === 0 && (
            <Empty>
              No setting matches &ldquo;{query.trim()}&rdquo;.{" "}
              <button type="button" className="small" onClick={() => setQuery("")}>
                Clear search
              </button>
            </Empty>
          )}
        </nav>
      </div>
      <LiveTerms.Provider value={index}>
        <div>
          {SECTIONS.map(
            ({ id, Body }) =>
              Body && (
                <div key={id} data-section={id} hidden={id !== active}>
                  <Body shown={id === active} />
                </div>
              ),
          )}
        </div>
      </LiveTerms.Provider>
    </div>
  );
}

export default function Settings() {
  const where = useWhere();
  if (where === "unknown") {
    return (
      <>
        <PageHeader path="/settings" />
        <Loading lines={6} />
      </>
    );
  }
  // Every section reads or writes configuration, secrets, policy or the answer
  // bank, and the server refuses those routes to a paired device, so none is
  // drawn for it.
  if (where === "remote") {
    return (
      <>
        <PageHeader path="/settings" />
        <p role="note" className="note">
          Settings are changed on the computer that runs Applysmith. This device can follow runs, read reports and chat in read and draft mode.
        </p>
      </>
    );
  }
  return (
    <>
      <PageHeader path="/settings" />
      <Sections />
    </>
  );
}
