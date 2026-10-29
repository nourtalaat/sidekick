import createClient from "openapi-fetch";
import type { paths } from "./schema";

/** Where the JSON API is served; the paths of the generated types are relative to it. */
export const API_BASE = "/api/v1";

/** Where the browser sends a sign-out, which is not part of the JSON API. */
export const LOGOUT_PATH = "/auth/logout";

/** The header that a request which changes something sends the session's CSRF token in. */
export const CSRF_HEADER = "X-CSRF-Token";

const SAFE_METHODS = new Set(["GET", "HEAD", "OPTIONS"]);

export type ApiClient = ReturnType<typeof createClient<paths>>;

export interface ApiOptions {
  /** The scheme and host that requests go to. Empty means the page's own origin. */
  origin?: string;
  fetch?: (request: Request) => Promise<Response>;
}

/** The JSON API client, with the session's CSRF token and the sign-out call. */
export interface Api {
  client: ApiClient;
  /** Keeps the token that GET /me returns, for the requests that change something. */
  setCsrfToken(token: string | undefined): void;
  /** Ends the session. */
  signOut(): Promise<void>;
  /** The address of an API path such as "/investigations/{id}/stream", for an EventSource. */
  url(path: string): string;
  /** Calls listener whenever the API answers 401. Returns the function that stops it. */
  onUnauthorized(listener: () => void): () => void;
}

/** An error answer of the API, with the detail of its problem document. */
export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

/** Makes a client of the JSON API. The session cookie goes along with every request. */
export function createApi(options: ApiOptions = {}): Api {
  const origin = options.origin ?? "";
  const send = options.fetch ?? ((request: Request) => fetch(request));
  const listeners = new Set<() => void>();
  let csrfToken: string | undefined;

  const client = createClient<paths>({
    baseUrl: `${origin}${API_BASE}`,
    credentials: "same-origin",
    fetch: send,
  });

  client.use({
    onRequest({ request }) {
      if (csrfToken && !SAFE_METHODS.has(request.method)) {
        request.headers.set(CSRF_HEADER, csrfToken);
      }

      return request;
    },
    onResponse({ response }) {
      if (response.status === 401) {
        listeners.forEach((listener) => listener());
      }
    },
  });

  return {
    client,
    setCsrfToken(token) {
      csrfToken = token;
    },
    url(path) {
      return `${origin}${API_BASE}${path}`;
    },
    async signOut() {
      const headers = new Headers();
      if (csrfToken) {
        headers.set(CSRF_HEADER, csrfToken);
      }

      const response = await send(new Request(`${origin}${LOGOUT_PATH}`, { method: "POST", headers, credentials: "same-origin" }));
      if (!response.ok) {
        throw new ApiError(response.status, await problemDetail(response));
      }
    },
    onUnauthorized(listener) {
      listeners.add(listener);

      return () => {
        listeners.delete(listener);
      };
    },
  };
}

/** The detail of a problem document in a response body, or the status text. */
export async function problemDetail(response: Response): Promise<string> {
  try {
    const body: unknown = await response.clone().json();
    if (typeof body === "object" && body !== null && "detail" in body && typeof body.detail === "string") {
      return body.detail;
    }
  } catch {
    // The body is not JSON; the status text says what is known.
  }

  return response.statusText;
}

/** The detail of an error that openapi-fetch parsed from a problem document. */
export function detailOf(error: unknown, fallback: string): string {
  if (typeof error === "object" && error !== null && "detail" in error && typeof error.detail === "string") {
    return error.detail;
  }

  return fallback;
}
