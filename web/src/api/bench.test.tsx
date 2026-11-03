import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it } from "vitest";
import { benchRunRange } from "../test/bench";
import { fakeServer, json, problem } from "../test/helpers";
import { recording } from "../test/rules";
import { ApiProvider } from "./ApiContext";
import { BENCH_PAGE_SIZE, useBenchRunPages } from "./bench";

function setup(answer: Parameters<typeof recording>[0]) {
  const { handler, seen } = recording(answer);
  const { api } = fakeServer(undefined, handler);
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });

  const wrapper = ({ children }: { children: ReactNode }) => (
    <ApiProvider api={api}>
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    </ApiProvider>
  );

  return { ...renderHook(() => useBenchRunPages(), { wrapper }), seen };
}

describe("useBenchRunPages", () => {
  it("asks for a page of 25 without a cursor, then for the next with the cursor of the last", async () => {
    const { result, seen } = setup((request) =>
      request.search.get("cursor") === "next" ? json(200, { runs: benchRunRange(26, 27) }) : json(200, { runs: benchRunRange(1, 25), nextCursor: "next" }),
    );

    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(BENCH_PAGE_SIZE).toBe(25);
    expect(seen[0]?.search.get("limit")).toBe("25");
    expect(seen[0]?.search.get("cursor")).toBeNull();
    expect(result.current.hasNextPage).toBe(true);

    await act(async () => {
      await result.current.fetchNextPage();
    });

    await waitFor(() => expect(result.current.data?.pages).toHaveLength(2));

    expect(seen.at(-1)?.search.get("cursor")).toBe("next");
    expect(seen.at(-1)?.search.get("limit")).toBe("25");
    expect(result.current.hasNextPage).toBe(false);
  });

  it("fails with the detail of the problem", async () => {
    const { result } = setup(() => problem(500, "The database is down."));

    await waitFor(() => expect(result.current.isError).toBe(true));

    expect(result.current.error?.message).toBe("The database is down.");
  });
});
