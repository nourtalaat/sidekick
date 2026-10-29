import { json, problem, type Handler } from "./helpers";

/** An EventSource that the test drives: it records the addresses it was opened with and emits what the test says. */
export class FakeEventSource {
  static instances: FakeEventSource[] = [];

  static reset(): void {
    FakeEventSource.instances = [];
  }

  /** The newest source opened. */
  static last(): FakeEventSource {
    const source = FakeEventSource.instances.at(-1);
    if (!source) {
      throw new Error("no EventSource was opened");
    }

    return source;
  }

  readyState = 0;
  closed = false;
  private readonly listeners = new Map<string, Set<(event: Event) => void>>();

  constructor(readonly url: string) {
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, listener: (event: Event) => void): void {
    const set = this.listeners.get(type) ?? new Set();
    set.add(listener);
    this.listeners.set(type, set);
  }

  close(): void {
    this.readyState = 2;
    this.closed = true;
  }

  /** The connection is up. */
  open(): void {
    this.readyState = 1;
    this.dispatch("open", new Event("open"));
  }

  /** The server sends an event with data as JSON. */
  emit(type: string, data: unknown, lastEventId = ""): void {
    this.dispatch(type, new MessageEvent(type, { data: JSON.stringify(data), lastEventId }));
  }

  /** The connection failed. When givenUp is true the browser has closed it and will not try again. */
  fail(givenUp: boolean): void {
    this.readyState = givenUp ? 2 : 0;
    this.dispatch("error", new Event("error"));
  }

  private dispatch(type: string, event: Event): void {
    this.listeners.get(type)?.forEach((listener) => listener(event));
  }
}

/** A handler that answers each "METHOD /path" with its function, and everything else with 404. */
export function routes(table: Record<string, (request: Request) => Response | Promise<Response>>): Handler {
  return (request) => {
    const key = `${request.method} ${new URL(request.url).pathname}`;
    const handle = table[key];

    return handle ? handle(request) : problem(404, `No route for ${key}.`);
  };
}

export { json, problem };
