import { useQuery } from "@tanstack/react-query";
import { useApi } from "./ApiContext";
import { ApiError, detailOf } from "./client";
import type { components } from "./schema";

export type QualityStats = components["schemas"]["QualityStats"];
export type CostStats = components["schemas"]["CostStats"];
export type OperationsStats = components["schemas"]["OperationsStats"];
export type OperationsTotals = components["schemas"]["OperationsTotals"];
export type StatsFilters = components["schemas"]["StatsFilters"];
export type BenchRunSummary = components["schemas"]["BenchRunSummary"];
export type BenchComparison = components["schemas"]["BenchComparison"];
export type Verdict = components["schemas"]["Verdict"];

/** The filters and the range of the dashboard, as the API takes them. A filter that is absent does not filter. */
export interface StatsQuery {
  /** The first and last day of the range, as YYYY-MM-DD in UTC. */
  from: string;
  to: string;
  route?: string;
  service?: string;
  model?: string;
  verdict?: Verdict;
}

/** How long the numbers stay fresh. The rollup behind them is recounted once a minute. */
const STALE_MS = 30_000;

/** How many benchmark runs the dashboard lists. */
export const BENCH_RUNS_SHOWN = 10;

interface Result<T> {
  data?: T;
  error?: unknown;
  response: Response;
}

/** The data of an answer of the API, or an ApiError with the problem's detail. */
function unwrap<T>(result: Result<T>): T {
  if (result.data === undefined) {
    throw new ApiError(result.response.status, detailOf(result.error, result.response.statusText));
  }

  return result.data;
}

/** The quality numbers of the range: accuracy, the confusion matrix, precision and recall. */
export function useQualityStats(query: StatsQuery) {
  const api = useApi();

  return useQuery({
    queryKey: ["stats", "quality", query],
    queryFn: async () => unwrap(await api.client.GET("/stats/quality", { params: { query } })),
    staleTime: STALE_MS,
  });
}

/** What model calls cost over the range, and today's spend against the caps. */
export function useCostStats(query: StatsQuery) {
  const api = useApi();

  return useQuery({
    queryKey: ["stats", "cost", query],
    queryFn: async () => unwrap(await api.client.GET("/stats/cost", { params: { query } })),
    staleTime: STALE_MS,
  });
}

/** Volume, speed, tool calls, false positives, fixes, actions and downtime over the range. */
export function useOperationsStats(query: StatsQuery) {
  const api = useApi();

  return useQuery({
    queryKey: ["stats", "operations", query],
    queryFn: async () => unwrap(await api.client.GET("/stats/operations", { params: { query } })),
    staleTime: STALE_MS,
  });
}

/** The routes, services and models the filter menus offer. */
export function useStatsFilters() {
  const api = useApi();

  return useQuery({
    queryKey: ["stats", "filters"],
    queryFn: async () => unwrap(await api.client.GET("/stats/filters")),
    staleTime: STALE_MS,
  });
}

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
