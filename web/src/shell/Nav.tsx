import { BookOpen, Compass, Gauge, Inbox, LayoutDashboard, ListChecks, Radio, Route, ScrollText, Settings, type LucideIcon } from "lucide-react";
import { NavLink } from "react-router";
import { hasRole, type Role } from "../roles";
import { useCurrentUser } from "../session";

export const MAIN_NAV_ID = "main-nav";

export interface NavItem {
  to: string;
  label: string;
  Icon: LucideIcon;
  /** The lowest role that sees this item in the menu. */
  minRole: Role;
}

export const NAV_ITEMS: readonly NavItem[] = [
  { to: "/investigations", label: "Investigations", Icon: ListChecks, minRole: "viewer" },
  { to: "/dashboard", label: "Dashboard", Icon: LayoutDashboard, minRole: "viewer" },
  { to: "/review", label: "Review queue", Icon: Inbox, minRole: "viewer" },
  { to: "/lessons", label: "Lessons", Icon: BookOpen, minRole: "viewer" },
  { to: "/routes", label: "Routes", Icon: Route, minRole: "viewer" },
  { to: "/strategies", label: "Strategies", Icon: Compass, minRole: "viewer" },
  { to: "/listeners", label: "Listeners", Icon: Radio, minRole: "viewer" },
  { to: "/benchmarks", label: "Benchmarks", Icon: Gauge, minRole: "viewer" },
  { to: "/audit", label: "Audit log", Icon: ScrollText, minRole: "admin" },
  { to: "/settings", label: "Settings", Icon: Settings, minRole: "admin" },
];

/** The left navigation, with the items the signed-in role can use. Below 768px it is a menu that the header's Menu button opens. */
export function Nav({ open }: { open: boolean }) {
  const me = useCurrentUser();
  const items = NAV_ITEMS.filter(({ minRole }) => hasRole(me.role, minRole));

  return (
    <nav id={MAIN_NAV_ID} aria-label="Main" className={open ? "sk-nav sk-nav--open" : "sk-nav"}>
      <ul className="sk-nav__list">
        {items.map(({ to, label, Icon }) => (
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
