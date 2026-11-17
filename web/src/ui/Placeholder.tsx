import { MapPinOff } from "lucide-react";
import { PAGES } from "../pages";
import { Link } from "../router";
import { EmptyState } from "./EmptyState";

export function NotFound() {
  return (
    <>
      <h1>Page not found</h1>
      <EmptyState icon={MapPinOff} title="No page lives at this address.">
        Pick one of the pages below.
      </EmptyState>
      <ul className="plain tiles page-links">
        {PAGES.map((p) => {
          const Icon = p.icon;
          return (
            <li key={p.path}>
              <Link to={p.path}>
                <Icon />
                {p.title}
              </Link>
            </li>
          );
        })}
      </ul>
    </>
  );
}
