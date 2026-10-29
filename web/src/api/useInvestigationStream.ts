import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { useApi } from "./ApiContext";
import { keys, type Step } from "./investigations";

/** How often the steps that arrived are added to the page, so a screen reader announces at most one batch each time. */
export const FLUSH_MS = 3000;
/** The wait before the first new connection after the browser gave up, doubled for each failure. */
export const RECONNECT_BASE_MS = 1000;
/** The longest wait between two connections. */
export const RECONNECT_MAX_MS = 30_000;

const EVENT_SOURCE_CLOSED = 2;
const NO_STEP = -1;

interface StreamOptions {
  id: string;
  /** Whether the stream is open. It closes when this turns false. */
  enabled: boolean;
  /** The seq of the last step the page has from the API, so the stream sends only newer ones. */
  after?: number | undefined;
}

/** What the page gets from the stream. */
interface StreamState {
  /** The steps that arrived through the stream, in seq order. */
  steps: Step[];
  connected: boolean;
}

/**
 * Follows an investigation while it runs. The steps that arrive are collected
 * and added to steps at most every FLUSH_MS; a status or watcher change makes
 * the page read the investigation or the watchers again.
 *
 * The browser reconnects by itself, and sends Last-Event-ID, while its
 * connection is only interrupted, such as at the end of the server's 30
 * minutes. When it gives up (the server answered 429 or 503, or the network
 * went away for good), the hook opens a new connection after a wait that
 * grows to RECONNECT_MAX_MS, asking for the steps after the last one it has
 * with the after parameter, since EventSource cannot set a header.
 */
export function useInvestigationStream({ id, enabled, after }: StreamOptions): StreamState {
  const api = useApi();
  const queryClient = useQueryClient();
  const [state, setState] = useState<StreamState>({ steps: [], connected: false });
  const lastSeq = useRef(NO_STEP);
  const pending = useRef<Step[]>([]);

  useEffect(() => {
    lastSeq.current = Math.max(lastSeq.current, after ?? NO_STEP);
  }, [after]);

  useEffect(() => {
    if (!enabled || typeof EventSource === "undefined") {
      return undefined;
    }

    let source: EventSource | undefined;
    let retry: ReturnType<typeof setTimeout> | undefined;
    let failures = 0;
    let stopped = false;

    const flush = () => {
      const arrived = pending.current;
      if (arrived.length === 0) {
        return;
      }

      pending.current = [];
      setState((previous) => ({ ...previous, steps: mergeBySeq(previous.steps, arrived) }));
    };

    const connect = () => {
      const query = lastSeq.current > NO_STEP ? `?after=${lastSeq.current}` : "";
      const opened = new EventSource(api.url(`/investigations/${id}/stream${query}`));
      source = opened;

      opened.addEventListener("open", () => {
        failures = 0;
        setState((previous) => ({ ...previous, connected: true }));
      });

      opened.addEventListener("step", (event) => {
        const step = parseStep((event as MessageEvent<string>).data);
        if (step) {
          lastSeq.current = Math.max(lastSeq.current, step.seq);
          pending.current.push(step);
        }
      });

      opened.addEventListener("status", () => {
        flush();
        void queryClient.invalidateQueries({ queryKey: keys.investigation(id) });
        void queryClient.invalidateQueries({ queryKey: keys.feedback(id) });
      });

      opened.addEventListener("watchers", () => {
        void queryClient.invalidateQueries({ queryKey: keys.watchers(id) });
      });

      opened.addEventListener("error", () => {
        setState((previous) => ({ ...previous, connected: false }));

        if (stopped || opened.readyState !== EVENT_SOURCE_CLOSED) {
          return;
        }

        opened.close();

        const wait = Math.min(RECONNECT_BASE_MS * 2 ** failures, RECONNECT_MAX_MS);
        failures += 1;
        retry = setTimeout(connect, wait);
      });
    };

    const timer = setInterval(flush, FLUSH_MS);
    connect();

    return () => {
      stopped = true;
      clearInterval(timer);
      clearTimeout(retry);
      source?.close();
      flush();
      setState((previous) => (previous.connected ? { ...previous, connected: false } : previous));
    };
  }, [api, id, enabled, queryClient]);

  return state;
}

/** The steps of a and b by seq, in order, with b's version of a step both have. */
export function mergeBySeq(a: readonly Step[], b: readonly Step[]): Step[] {
  const bySeq = new Map<number, Step>();

  for (const step of [...a, ...b]) {
    bySeq.set(step.seq, step);
  }

  return [...bySeq.values()].sort((x, y) => x.seq - y.seq);
}

function parseStep(data: string): Step | undefined {
  try {
    const step: unknown = JSON.parse(data);

    return typeof step === "object" && step !== null && "seq" in step && typeof step.seq === "number" ? (step as Step) : undefined;
  } catch {
    return undefined;
  }
}
