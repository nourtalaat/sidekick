import { QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import axe from "axe-core";
import type { ReactElement } from "react";
import { MemoryRouter } from "react-router";
import { App } from "../App";
import { ApiProvider } from "../api/ApiContext";
import { createApi, type Api } from "../api/client";
import { createQueryClient } from "../api/queryClient";
import type { Me } from "../session";
import { ThemeProvider } from "../theme";

export const ORIGIN = "http://sidekick.test";

export const VIEWER: Me = { kind: "user", id: "u1", name: "Ada Lovelace", email: "ada@example.com", role: "viewer", csrfToken: "csrf-ada" };

/** One request of the fake server, with its path and the headers it came with. */
export interface Seen {
  method: string;
  path: string;
  headers: Headers;
}

export type Handler = (request: Request) => Response | Promise<Response>;

export function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

export function problem(status: number, detail: string): Response {
  return new Response(JSON.stringify({ type: "about:blank", title: "Problem", status, detail }), {
    status,
    headers: { "Content-Type": "application/problem+json" },
  });
}

/** A fake Sidekick server: GET /me answers with me (401 when null), a sign-out with 204, and everything else with handler. */
export function fakeServer(me: Me | null = VIEWER, handler?: Handler) {
  const seen: Seen[] = [];

  const fetch = async (request: Request) => {
    const path = new URL(request.url).pathname;
    seen.push({ method: request.method, path, headers: request.headers });

    if (path === "/api/v1/me") {
      return me ? json(200, me) : problem(401, "Sign in to continue.");
    }

    if (path === "/auth/logout") {
      return new Response(null, { status: 204 });
    }

    return handler ? handler(request) : problem(404, "Not found.");
  };

  return { seen, api: createApi({ origin: ORIGIN, fetch }) };
}

/** Renders ui inside the providers of the app, at route. */
export function renderWithProviders(ui: ReactElement, options: { api?: Api; route?: string } = {}) {
  const api = options.api ?? fakeServer().api;

  return {
    api,
    ...render(
      <ApiProvider api={api}>
        <QueryClientProvider client={createQueryClient()}>
          <ThemeProvider>
            <MemoryRouter initialEntries={[options.route ?? "/"]}>{ui}</MemoryRouter>
          </ThemeProvider>
        </QueryClientProvider>
      </ApiProvider>,
    ),
  };
}

/** Renders the whole app at route, with a fake server that knows me (no session when null). */
export function renderApp(options: { me?: Me | null; route?: string; handler?: Handler } = {}) {
  const server = fakeServer(options.me, options.handler);

  return { ...server, ...renderWithProviders(<App />, { api: server.api, route: options.route }) };
}

/**
 * Runs axe on container for WCAG 2.0 to 2.2 A and AA, and returns the
 * violations. jsdom has no layout, so colour contrast cannot be measured
 * here; the contrast figures that come with the design tokens cover it.
 */
export async function wcagViolations(container: Element): Promise<axe.Result[]> {
  const results = await axe.run(container, {
    runOnly: { type: "tag", values: ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"] },
    rules: { "color-contrast": { enabled: false } },
  });

  return results.violations;
}

/** Makes window.matchMedia answer for the dark colour scheme and forced colors. */
export function stubMedia(options: { dark: boolean; forcedColors: boolean }): void {
  window.matchMedia = (query: string) =>
    ({
      matches: query.includes("forced-colors: active") ? options.forcedColors : query.includes("prefers-color-scheme: dark") ? options.dark : false,
      media: query,
      onchange: null,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
      addListener: () => undefined,
      removeListener: () => undefined,
      dispatchEvent: () => false,
    }) as MediaQueryList;
}
