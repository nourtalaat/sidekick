import { useQuery } from "@tanstack/react-query";
import { useApi } from "./ApiContext";
import { STALE_MS, unwrap } from "./query";
import type { components } from "./schema";

export type QualityStats = components["schemas"]["QualityStats"];
export type CostStats = components["schemas"]["CostStats"];
export type OperationsStats = components["schemas"]["OperationsStats"];
export type OperationsTotals = components["schemas"]["OperationsTotals"];
export type StatsFilters = components["schemas"]["StatsFilters"];
export type Verdict = components["schemas"]["Verdict"];

export { BENCH_RUNS_SHOWN, useBenchComparison, useBenchRuns, type BenchComparison, type BenchRunSummary } from "./bench";

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
