import type { ReactNode } from "react";
import { ApiError } from "../api/client";
import { hasRole, type Role } from "../roles";
import { useCurrentUser } from "../session";
import { QueryError } from "./QueryError";

const FORBIDDEN = 403;

interface RequireRoleProps {
  /** The lowest role that may open the page. */
  role: Role;
  /** The title of the page, shown above the message when the role is too low. */
  title: string;
  children: ReactNode;
}

/**
 * Shows its page to a signed-in user whose role is at least role. Any other
 * user sees the title and a message that names the role, and the page is not
 * mounted, so it sends no request.
 */
export function RequireRole({ role, title, children }: RequireRoleProps) {
  const me = useCurrentUser();

  if (hasRole(me.role, role)) {
    return children;
  }

  return (
    <div className="sk-page">
      <h1 className="sk-page__title">{title}</h1>
      <QueryError error={new ApiError(FORBIDDEN, `This needs the ${role} role.`)} />
    </div>
  );
}
