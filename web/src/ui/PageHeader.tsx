import type { ReactNode } from "react";
import { PAGES } from "../pages";

// PageHeader opens every page on one line: its title as the page's one h1,
// the line that says what the page is for beside it, and the page-level
// actions. The page's icon is in the sidebar, not repeated here.
//
//   <PageHeader path="/tracker" actions={<Button variant="primary">Add</Button>} />
export function PageHeader({ path, actions, children }: { path: string; actions?: ReactNode; children?: ReactNode }) {
  const page = PAGES.find((p) => p.path === path);
  if (!page) return null;
  return (
    <header className="page-head">
      <div className="page-title">
        <h1>{page.title}</h1>
        <p className="muted">{children ?? page.blurb}</p>
      </div>
      {actions && <div className="page-actions">{actions}</div>}
    </header>
  );
}
