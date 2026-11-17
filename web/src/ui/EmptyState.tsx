import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";

// EmptyState is what a page's main list shows when it has nothing: an icon,
// one sentence that says why, a line of help, and the action that fills it.
//
//   <EmptyState icon={ListChecks} title="No applications are tracked yet." action={<Link to="/apply-next">Open Apply Next</Link>}>
//     Evaluate a posting to add the first row.
//   </EmptyState>
export function EmptyState({ icon: Icon, title, children, action }: { icon: LucideIcon; title: ReactNode; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className="empty-state">
      <span className="empty-icon" aria-hidden="true">
        <Icon />
      </span>
      <p className="empty-title">{title}</p>
      {children && <p className="muted">{children}</p>}
      {action && <div className="empty-action">{action}</div>}
    </div>
  );
}
