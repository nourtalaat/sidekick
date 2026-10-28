export const LOGIN_ROUTE = "/login";

/** The login page for a user who wants to come back to path afterwards. */
export function loginRouteFor(path: string): string {
  return `${LOGIN_ROUTE}?return=${encodeURIComponent(path)}`;
}

/** Where the login endpoint of the server starts the sign-in, and then sends the browser to path. */
export function serverLoginUrl(path: string): string {
  return `/auth/login?return=${encodeURIComponent(path)}`;
}

/** path when it is a path of this site that the app can show; otherwise "/". Stops a login link from leaving the site. */
export function safeReturnPath(path: string | null): string {
  if (!path || !path.startsWith("/") || path.startsWith("//") || path.includes("\\") || path.startsWith(`${LOGIN_ROUTE}`)) {
    return "/";
  }

  return path;
}
