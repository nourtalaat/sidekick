import type { components } from "../api/schema";
import type { Me } from "../session";
import { VIEWER, json, type Handler } from "./helpers";

type Listener = components["schemas"]["Listener"];
type Route = components["schemas"]["Route"];
type FieldError = components["schemas"]["FieldError"];

export const RESPONDER: Me = { ...VIEWER, role: "responder" };
export const ADMIN: Me = { ...VIEWER, role: "admin" };

export function listener(over: Partial<Listener> = {}): Listener {
  return {
    name: "ops",
    layer: "runtime",
    status: "active",
    readOnly: false,
    enabled: true,
    trusted: false,
    channel: "#ops",
    version: 2,
    updatedBy: "U01SEED",
    updatedAt: "2026-10-20T10:00:00Z",
    match: { sender: { kind: "bot", names: ["Grafana"] }, text: "(?i)firing" },
    ...over,
  };
}

export function route(over: Partial<Route> = {}): Route {
  return {
    name: "checkout",
    layer: "runtime",
    status: "active",
    readOnly: false,
    enabled: true,
    version: 1,
    updatedBy: "U01SEED",
    updatedAt: "2026-10-20T10:00:00Z",
    priority: 20,
    position: 2,
    strategy: "generic-alert",
    mode: "read-only",
    match: { service: "checkout-*" },
    outputs: ["slack-thread"],
    ...over,
  };
}

/** The listeners of the list tests: one of the config file, one at runtime, and a runtime one the file shadows. */
export function sampleListeners(): Listener[] {
  return [
    listener({ name: "alerts", layer: "file", readOnly: true, channel: "#alerts", version: undefined, updatedBy: undefined, updatedAt: undefined }),
    listener(),
    listener({
      name: "alerts",
      status: "shadowed",
      statusReason: "a rule in the config file has the same name",
      channel: "#old-alerts",
      version: 1,
    }),
  ];
}

/** The routes of the list tests, in the order the API gives them. */
export function sampleRoutes(): Route[] {
  return [
    route({ name: "payments", layer: "file", readOnly: true, priority: 50, position: 1, match: { service: "payments-*" }, version: undefined, updatedBy: undefined, updatedAt: undefined }),
    route({ name: "default", layer: "file", readOnly: true, priority: 100, position: 2, match: undefined, version: undefined, updatedBy: undefined, updatedAt: undefined }),
    route({ name: "checkout", enabled: false, status: "disabled", statusReason: "disabled", position: undefined }),
  ];
}

/** An answer of 422 for a change that fails the config checks. */
export function validationProblem(errors: FieldError[]): Response {
  return json(422, { type: "about:blank", title: "Unprocessable Entity", status: 422, detail: "The rule does not pass the config checks.", code: "validation", errors });
}

/** An answer with a code, such as 409 stale. */
export function codedProblem(status: number, code: string, detail: string): Response {
  return json(status, { type: "about:blank", title: "Problem", status, detail, code });
}

/** One request the fake server saw, with its query and body. */
export interface Recorded {
  method: string;
  path: string;
  search: URLSearchParams;
  body: unknown;
  csrf: string | null;
}

/**
 * A handler that records every request it is given, with its JSON body, and
 * answers with what answer returns. The records are in seen.
 */
export function recording(answer: (request: Recorded) => Response | Promise<Response>): { handler: Handler; seen: Recorded[] } {
  const seen: Recorded[] = [];

  const handler: Handler = async (request) => {
    const url = new URL(request.url);
    const text = await request.clone().text();
    const recorded: Recorded = {
      method: request.method,
      path: url.pathname,
      search: url.searchParams,
      body: text === "" ? undefined : (JSON.parse(text) as unknown),
      csrf: request.headers.get("X-CSRF-Token"),
    };

    seen.push(recorded);

    return answer(recorded);
  };

  return { handler, seen };
}

/** The row at index i of a table's rows; the test fails if there is none. */
export function rowAt(rows: HTMLElement[], i: number): HTMLElement {
  const row = rows[i];
  if (!row) {
    throw new Error(`the table has no row ${i}`);
  }

  return row;
}
