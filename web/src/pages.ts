import { Activity, BookMarked, BookOpen, Compass, FileText, Gauge, Globe, House, LibraryBig, ListChecks, Mail, MessageSquareText, MonitorCog, Radar, Rocket, Send, Settings, UserRound, type LucideIcon } from "lucide-react";

export type Group = "Today" | "Find" | "Prepare" | "Assist" | "System";

export interface PageDef {
  title: string;
  path: string;
  group: Group;
  /** Pages the bottom tab bar shows on a phone; the rest sit under More.
   * Four and More fit a phone with whole labels; Runs is one of them because
   * its badge counts what waits for you. */
  primary: boolean;
  icon: LucideIcon;
  /** One line under the page title saying what the page is for. */
  blurb: string;
  /** What a person may call the things the page holds, for the palette. */
  terms?: readonly string[];
}

// The eighteen stable routes.
export const PAGES: readonly PageDef[] = [
  { title: "Home", path: "/", group: "Today", primary: true, icon: House, blurb: "Today at a glance: what is ready, what needs you, and how the system is doing." },
  { title: "Apply Next", path: "/apply-next", group: "Today", primary: true, icon: Send, blurb: "Postings that are ready, in the order to apply to them." },
  { title: "Tracker", path: "/tracker", group: "Today", primary: true, icon: ListChecks, blurb: "Every application, its status and its history.", terms: ["add application", "applied outside", "follow-up", "interview prep", "cover letter", "résumé PDF", "export CSV"] },
  { title: "Reports", path: "/reports", group: "Today", primary: false, icon: FileText, blurb: "Evaluation reports for the postings you considered." },
  { title: "Discovery", path: "/discovery", group: "Find", primary: false, icon: Compass, blurb: "Scan job sources, watch scans run and schedule them.", terms: ["LinkedIn", "Indeed", "Built In", "job boards", "tracked companies", "saved searches"] },
  { title: "Leads and Signals", path: "/leads", group: "Find", primary: false, icon: Radar, blurb: "Leads from feeds and hiring signals, with their evidence." },
  { title: "Answer Bank", path: "/answer-bank", group: "Prepare", primary: false, icon: BookMarked, blurb: "The answers application forms are filled from.", terms: ["screening questions", "salary", "sponsorship", "work authorization", "EEO", "self-identification", "veteran", "disability", "transcript"] },
  { title: "Profile", path: "/profile", group: "Prepare", primary: false, icon: UserRound, blurb: "Your CV, narrative, voice, résumés and personal facts.", terms: ["phone", "email", "address", "LinkedIn", "GitHub", "salary", "pay", "sponsorship", "visa", "work history", "education", "skills"] },
  { title: "Wiki", path: "/wiki", group: "Prepare", primary: false, icon: LibraryBig, blurb: "The pages autofill and Ask draw on, company briefs and your story bank." },
  { title: "Ask Agent", path: "/ask", group: "Assist", primary: false, icon: MessageSquareText, blurb: "Chat with the agent, steer its work and review what it remembers." },
  { title: "Agent control", path: "/agent-control", group: "Assist", primary: false, icon: MonitorCog, blurb: "What the agent looks at and does in your apps through PersonalCU, with Pause and Stop." },
  { title: "Runs", path: "/runs", group: "Assist", primary: true, icon: Activity, blurb: "Everything running, and what is waiting for you." },
  { title: "Browser", path: "/browser", group: "Assist", primary: false, icon: Globe, blurb: "The apply and scan windows, their screenshots and sign-ins." },
  { title: "Gmail", path: "/gmail", group: "Assist", primary: false, icon: Mail, blurb: "Job email briefs, actions to take and the connection." },
  { title: "Usage", path: "/usage", group: "System", primary: false, icon: Gauge, blurb: "Model calls, cost and run traces." },
  { title: "Settings", path: "/settings", group: "System", primary: false, icon: Settings, blurb: "Models, credentials, integrations and the runtime." },
  { title: "Setup", path: "/setup", group: "System", primary: false, icon: Rocket, blurb: "Get Applysmith ready to apply on your behalf." },
  { title: "User guide", path: "/guide", group: "System", primary: false, icon: BookOpen, blurb: "How a day with Applysmith goes, and a walkthrough with the demo." },
];

export const GROUPS: readonly Group[] = ["Today", "Find", "Prepare", "Assist", "System"];

export function pageFor(pathname: string): PageDef | undefined {
  const clean = pathname.length > 1 ? pathname.replace(/\/+$/, "") : pathname;
  // A run has a link of its own, /runs/{id}, which opens it on the Runs page.
  const path = clean.startsWith("/runs/") ? "/runs" : clean;
  return PAGES.find((p) => p.path === path);
}
