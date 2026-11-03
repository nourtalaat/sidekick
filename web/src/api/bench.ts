import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useApi } from "./ApiContext";
import { STALE_MS, unwrap } from "./query";
import type { components } from "./schema";

export type BenchRunSummary = components["schemas"]["BenchRunSummary"];
export type BenchComparison = components["schemas"]["BenchComparison"];

/** How many benchmark runs the dashboard lists. */
export const BENCH_RUNS_SHOWN = 10;

/** How many runs the Benchmarks page asks for at a time. */
export const BENCH_PAGE_SIZE = 25;

/** The newest benchmark runs. */
export function useBenchRuns() {
  const api = useApi();

  return useQuery({
    queryKey: ["bench", "runs"],
    queryFn: async () => unwrap(await api.client.GET("/bench/runs", { params: { query: { limit: BENCH_RUNS_SHOWN } } })),
    staleTime: STALE_MS,
  });
}

/** Two benchmark runs side by side; waits until both are chosen. */
export function useBenchComparison(a: string | undefined, b: string | undefined) {
  const api = useApi();

  return useQuery({
    queryKey: ["bench", "compare", a, b],
    enabled: a !== undefined && b !== undefined,
    queryFn: async () => unwrap(await api.client.GET("/bench/compare", { params: { query: { a: a as string, b: b as string } } })),
    staleTime: STALE_MS,
  });
}

/**
 * Every benchmark run, newest first, a page at a time: the next page is
 * fetched with the nextCursor of the last. It has a key of its own, apart from
 * the dashboard's newest runs, because the two have different shapes.
 */
export function useBenchRunPages() {
  const api = useApi();

  return useInfiniteQuery({
    queryKey: ["bench", "run-pages"],
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => unwrap(await api.client.GET("/bench/runs", { params: { query: { cursor: pageParam, limit: BENCH_PAGE_SIZE } } })),
    getNextPageParam: (last) => last.nextCursor,
    staleTime: STALE_MS,
  });
}
