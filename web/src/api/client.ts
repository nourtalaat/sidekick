import createClient, { type Middleware } from "openapi-fetch";
import type { paths } from "./schema";

/** Where the JSON API is served; the paths of the generated types are relative to it. */
export const API_BASE = "/api/v1";

/** The header that a request which changes something sends the session's CSRF token in. */
export const CSRF_HEADER = "X-CSRF-Token";

const SAFE_METHODS = new Set(["GET", "HEAD", "OPTIONS"]);

let csrfToken: string | undefined;

/** Keeps the token that GET /me returns, for the requests that change something. */
export function setCsrfToken(token: string | undefined): void {
  csrfToken = token;
}

const addCsrfToken: Middleware = {
  onRequest({ request }) {
    if (csrfToken && !SAFE_METHODS.has(request.method)) {
      request.headers.set(CSRF_HEADER, csrfToken);
    }

    return request;
  },
};

export interface ApiClientOptions {
  baseUrl?: string;
  fetch?: (request: Request) => Promise<Response>;
}

/** Makes a client of the JSON API. The session cookie goes along with every request. */
export function createApiClient(options: ApiClientOptions = {}) {
  const client = createClient<paths>({
    baseUrl: options.baseUrl ?? API_BASE,
    credentials: "same-origin",
    fetch: options.fetch,
  });

  client.use(addCsrfToken);

  return client;
}

export const api = createApiClient();
