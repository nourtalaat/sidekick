import { BookOpen, Gauge, Inbox, LayoutDashboard, ListChecks, Route, ScrollText, Settings, type LucideIcon } from "lucide-react";
import { NavLink } from "react-router";

export const MAIN_NAV_ID = "main-nav";

export interface NavItem {
  to: string;
  label: string;
  Icon: LucideIcon;
}

export const NAV_ITEMS: readonly NavItem[] = [
  { to: "/investigations", label: "Investigations", Icon: ListChecks },
  { to: "/dashboard", label: "Dashboard", Icon: LayoutDashboard },
  { to: "/review", label: "Review queue", Icon: Inbox },
  { to: "/lessons", label: "Lessons", Icon: BookOpen },
  { to: "/routes", label: "Routes", Icon: Route },
  { to: "/benchmarks", label: "Benchmarks", Icon: Gauge },
  { to: "/audit", label: "Audit log", Icon: ScrollText },
  { to: "/settings", label: "Settings", Icon: Settings },
];

/** The left navigation. Below 768px it is a menu that the header's Menu button opens. */
export function Nav({ open }: { open: boolean }) {
  return (
    <nav id={MAIN_NAV_ID} aria-label="Main" className={open ? "sk-nav sk-nav--open" : "sk-nav"}>
      <ul className="sk-nav__list">
        {NAV_ITEMS.map(({ to, label, Icon }) => (
          <li key={to}>
            <NavLink to={to} className="sk-nav__link">
              <Icon size={20} aria-hidden="true" />
              {label}
            </NavLink>
          </li>
        ))}
      </ul>
    </nav>
  );
}
