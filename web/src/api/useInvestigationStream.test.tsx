import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { INVESTIGATION_ID, step } from "../test/fixtures";
import { ORIGIN } from "../test/helpers";
import { FakeEventSource } from "../test/streams";
import { ApiProvider } from "./ApiContext";
import { createApi } from "./client";
import { keys } from "./investigations";
import { FLUSH_MS, RECONNECT_BASE_MS, RECONNECT_MAX_MS, mergeBySeq, useInvestigationStream } from "./useInvestigationStream";

const STREAM = `${ORIGIN}/api/v1/investigations/${INVESTIGATION_ID}/stream`;

function setup(initial: { enabled: boolean; after?: number } = { enabled: true }) {
  const queryClient = new QueryClient();
  const invalidate = vi.spyOn(queryClient, "invalidateQueries");
  const api = createApi({ origin: ORIGIN, fetch: async () => new Response(null, { status: 404 }) });

  const wrapper = ({ children }: { children: ReactNode }) => (
    <ApiProvider api={api}>
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    </ApiProvider>
  );

  const hook = renderHook((props: { enabled: boolean; after?: number }) => useInvestigationStream({ id: INVESTIGATION_ID, ...props }), {
    wrapper,
    initialProps: initial,
  });

  return { ...hook, invalidate };
}

/** Lets the timers of the hook run for ms. */
function elapse(ms: number): void {
  act(() => {
    vi.advanceTimersByTime(ms);
  });
}

beforeEach(() => {
  FakeEventSource.reset();
  vi.stubGlobal("EventSource", FakeEventSource);
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("the investigation stream", () => {
  it("opens the stream of the investigation, from the start when the page has no step", () => {
    setup();

    expect(FakeEventSource.instances.map((s) => s.url)).toEqual([STREAM]);
  });

  it("asks only for the steps after the last one the page has", () => {
    setup({ enabled: true, after: 7 });

    expect(FakeEventSource.last().url).toBe(`${STREAM}?after=7`);
  });

  it("does not open while disabled, opens when enabled, and closes when disabled again or unmounted", () => {
    const { rerender, unmount } = setup({ enabled: false });
    expect(FakeEventSource.instances).toHaveLength(0);

    rerender({ enabled: true });
    expect(FakeEventSource.instances).toHaveLength(1);
    expect(FakeEventSource.last().closed).toBe(false);

    rerender({ enabled: false });
    expect(FakeEventSource.last().closed).toBe(true);

    rerender({ enabled: true, after: 3 });
    expect(FakeEventSource.instances).toHaveLength(2);
    expect(FakeEventSource.last().url).toBe(`${STREAM}?after=3`);

    unmount();
    expect(FakeEventSource.last().closed).toBe(true);
  });

  it("adds the steps that arrive in batches, so a screen reader hears one announcement at a time", () => {
    const { result } = setup();
    const source = FakeEventSource.last();

    act(() => {
      source.open();
      source.emit("step", step(1), "1");
      source.emit("step", step(2), "2");
    });

    elapse(FLUSH_MS - 1);
    expect(result.current.steps).toEqual([]);
    expect(result.current.connected).toBe(true);

    elapse(1);
    expect(result.current.steps.map((s) => s.seq)).toEqual([1, 2]);

    act(() => source.emit("step", step(3), "3"));
    expect(result.current.steps.map((s) => s.seq)).toEqual([1, 2]);

    elapse(FLUSH_MS);
    expect(result.current.steps.map((s) => s.seq)).toEqual([1, 2, 3]);
  });

  it("ignores a step it cannot read and a step it has already", () => {
    const { result } = setup();
    const source = FakeEventSource.last();

    act(() => {
      source.emit("step", { not: "a step" });
      source.emit("step", step(1));
      source.emit("step", step(1, { durationMs: 99 }));
    });
    elapse(FLUSH_MS);

    expect(result.current.steps).toHaveLength(1);
    expect(result.current.steps[0]?.durationMs).toBe(99);
  });

  it("reads the investigation again on a status event, and shows the steps that came before it at once", () => {
    const { result, invalidate } = setup();
    const source = FakeEventSource.last();

    act(() => {
      source.emit("step", step(1));
      source.emit("status", { status: "final" });
    });

    expect(invalidate).toHaveBeenCalledWith({ queryKey: keys.investigation(INVESTIGATION_ID) });
    expect(result.current.steps.map((s) => s.seq)).toEqual([1]);
  });

  it("reads the watchers again on a watchers event", () => {
    const { invalidate } = setup();

    act(() => FakeEventSource.last().emit("watchers", {}));

    expect(invalidate).toHaveBeenCalledWith({ queryKey: keys.watchers(INVESTIGATION_ID) });
  });

  describe("when the connection fails", () => {
    it("leaves the reconnection to the browser while it is only interrupted", () => {
      const { result } = setup();
      const source = FakeEventSource.last();

      act(() => {
        source.open();
        source.fail(false);
      });
      elapse(RECONNECT_MAX_MS);

      expect(FakeEventSource.instances).toHaveLength(1);
      expect(source.closed).toBe(false);
      expect(result.current.connected).toBe(false);
    });

    it("connects again with the last step it has after the browser gave up, waiting longer each time", () => {
      setup();

      act(() => {
        FakeEventSource.last().open();
        FakeEventSource.last().emit("step", step(4));
        FakeEventSource.last().fail(true);
      });
      expect(FakeEventSource.last().closed).toBe(true);

      elapse(RECONNECT_BASE_MS - 1);
      expect(FakeEventSource.instances).toHaveLength(1);

      elapse(1);
      expect(FakeEventSource.instances).toHaveLength(2);
      expect(FakeEventSource.last().url).toBe(`${STREAM}?after=4`);

      act(() => FakeEventSource.last().fail(true));
      elapse(2 * RECONNECT_BASE_MS - 1);
      expect(FakeEventSource.instances).toHaveLength(2);
      elapse(1);
      expect(FakeEventSource.instances).toHaveLength(3);
      expect(FakeEventSource.last().url).toBe(`${STREAM}?after=4`);
    });

    it("starts the waits again after a connection that worked", () => {
      setup();

      act(() => FakeEventSource.last().fail(true));
      elapse(RECONNECT_BASE_MS);
      act(() => FakeEventSource.last().fail(true));
      elapse(2 * RECONNECT_BASE_MS);
      expect(FakeEventSource.instances).toHaveLength(3);

      act(() => {
        FakeEventSource.last().open();
        FakeEventSource.last().fail(true);
      });
      elapse(RECONNECT_BASE_MS);

      expect(FakeEventSource.instances).toHaveLength(4);
    });

    it("never waits longer than the longest wait", () => {
      setup();

      for (let i = 0; i < 8; i++) {
        act(() => FakeEventSource.last().fail(true));
        elapse(RECONNECT_MAX_MS);
      }

      const before = FakeEventSource.instances.length;
      act(() => FakeEventSource.last().fail(true));
      elapse(RECONNECT_MAX_MS);

      expect(FakeEventSource.instances).toHaveLength(before + 1);
    });

    it("does not connect again after it was disabled", () => {
      const { rerender } = setup();

      act(() => FakeEventSource.last().fail(true));
      rerender({ enabled: false });
      elapse(RECONNECT_MAX_MS);

      expect(FakeEventSource.instances).toHaveLength(1);
    });
  });
});

describe("mergeBySeq", () => {
  it("orders by seq and keeps the newer version of a step both lists have", () => {
    const merged = mergeBySeq([step(3), step(1)], [step(2), step(3, { durationMs: 5 })]);

    expect(merged.map((s) => s.seq)).toEqual([1, 2, 3]);
    expect(merged[2]?.durationMs).toBe(5);
  });
});
