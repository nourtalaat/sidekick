import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useApi } from "./ApiContext";
import { STALE_MS, unwrap } from "./query";
import type { components } from "./schema";

export type BenchRunSummary = components["schemas"]["BenchRunSummary"];
export type BenchComparison = components["schemas"]["BenchComparison"];
export type BenchRun = components["schemas"]["BenchRun"];
export type BenchResult = components["schemas"]["BenchResult"];
export type BenchThreshold = components["schemas"]["BenchThreshold"];
export type BenchInterval = components["schemas"]["BenchInterval"];

/** How many benchmark runs the dashboard lists. */
export const BENCH_RUNS_SHOWN = 10;

/** How many runs the Benchmarks page asks for at a time. */
export const BENCH_PAGE_SIZE = 25;

/** How many runs the compare page offers in its menus. */
export const BENCH_COMPARE_CHOICES = 50;

/** The newest benchmark runs, the dashboard's number of them unless limit says otherwise. */
export function useBenchRuns(limit = BENCH_RUNS_SHOWN) {
  const api = useApi();

  return useQuery({
    queryKey: ["bench", "runs", limit],
    queryFn: async () => unwrap(await api.client.GET("/bench/runs", { params: { query: { limit } } })),
    staleTime: STALE_MS,
  });
}

/** The query key of one benchmark run. */
export function benchRunKey(id: string) {
  return ["bench", "run", id] as const;
}

/** One benchmark run with its intervals, thresholds and results; waits until there is an id. */
export function useBenchRun(id: string | undefined) {
  const api = useApi();

  return useQuery({
    queryKey: benchRunKey(id ?? ""),
    enabled: id !== undefined,
    queryFn: async () => unwrap(await api.client.GET("/bench/runs/{id}", { params: { path: { id: id as string } } })),
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
