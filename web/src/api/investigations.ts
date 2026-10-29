import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useApi } from "./ApiContext";
import { ApiError, detailOf } from "./client";
import type { components } from "./schema";

export type Investigation = components["schemas"]["Investigation"];
export type InvestigationSummary = components["schemas"]["InvestigationSummary"];
export type Step = components["schemas"]["Step"];
export type StepPart = components["schemas"]["StepPart"];
export type Watcher = components["schemas"]["Watcher"];
export type Report = components["schemas"]["Report"];
export type Finding = components["schemas"]["Finding"];
export type Evidence = components["schemas"]["Evidence"];
export type EventInfo = components["schemas"]["Event"];
export type Verdict = components["schemas"]["Verdict"];
export type InvestigationStatus = components["schemas"]["InvestigationStatus"];
export type StreamStatus = components["schemas"]["StreamStatus"];

/** The filters of the list, as the URL keeps them. */
export interface ListFilters {
  q?: string;
  verdict?: Verdict;
  status?: InvestigationStatus;
  route?: string;
  service?: string;
  model?: string;
  /** How far back to look: "1h", "24h", "7d" or "30d". All time when absent. */
  range?: string;
}

/** The ranges the list offers, in milliseconds. */
export const RANGES: Record<string, number> = {
  "1h": 3_600_000,
  "24h": 86_400_000,
  "7d": 604_800_000,
  "30d": 2_592_000_000,
};

/** The steps a page asks for at a time. */
export const STEPS_PAGE = 200;

const WATCHERS_REFETCH_MS = 60_000;
const IDLE_REFETCH_MS = 30_000;

/** The keys of the queries about one investigation, so a change can refresh them. */
export const keys = {
  list: (filters: ListFilters) => ["investigations", filters] as const,
  investigation: (id: string) => ["investigation", id] as const,
  steps: (id: string) => ["steps", id] as const,
  watchers: (id: string) => ["watchers", id] as const,
  feedback: (id: string) => ["feedback", id] as const,
};

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

/** Pages of the list that match filters, newest first; the next page starts at the cursor. */
export function useInvestigationList(filters: ListFilters) {
  const api = useApi();

  return useInfiniteQuery({
    queryKey: keys.list(filters),
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const span = filters.range ? RANGES[filters.range] : undefined;
      const from = span === undefined ? undefined : new Date(Date.now() - span).toISOString();

      return unwrap(
        await api.client.GET("/investigations", {
          params: {
            query: {
              from,
              q: filters.q,
              verdict: filters.verdict,
              status: filters.status,
              route: filters.route,
              service: filters.service,
              model: filters.model,
              cursor: pageParam,
            },
          },
        }),
      );
    },
    getNextPageParam: (last) => last.nextCursor,
  });
}

/**
 * One investigation. While nothing runs, it is read again every 30 seconds,
 * so a follow-up that someone asks in Slack shows up here; while it runs,
 * the stream tells the page when to read it again.
 */
export function useInvestigation(id: string) {
  const api = useApi();

  return useQuery({
    queryKey: keys.investigation(id),
    queryFn: async () => unwrap(await api.client.GET("/investigations/{id}", { params: { path: { id } } })),
    refetchInterval: (query) => (isRunning(query.state.data) ? false : IDLE_REFETCH_MS),
  });
}

/** The steps of the investigation that were saved when the page loaded, a page at a time. */
export function useSteps(id: string) {
  const api = useApi();

  return useInfiniteQuery({
    queryKey: keys.steps(id),
    initialPageParam: undefined as number | undefined,
    queryFn: async ({ pageParam }) =>
      unwrap(await api.client.GET("/investigations/{id}/steps", { params: { path: { id }, query: { after: pageParam, limit: STEPS_PAGE } } })),
    getNextPageParam: (last) => last.nextAfter,
  });
}

/** The watchers of the investigation's group with their last checks. */
export function useWatchers(id: string) {
  const api = useApi();

  return useQuery({
    queryKey: keys.watchers(id),
    queryFn: async () => unwrap(await api.client.GET("/investigations/{id}/watchers", { params: { path: { id } } })).watchers,
    refetchInterval: WATCHERS_REFETCH_MS,
  });
}

/** Whether a run of the investigation is queued or running. */
export function isRunning(inv: Pick<Investigation, "status"> | undefined): boolean {
  return inv?.status === "queued" || inv?.status === "running";
}

/** Asks a follow-up question. The investigation and its steps are read again afterwards. */
export function useAskFollowUp(id: string) {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (text: string) => unwrap(await api.client.POST("/investigations/{id}/follow-ups", { params: { path: { id } }, body: { text } })),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: keys.investigation(id) }),
  });
}

/** Asks the run to stop. */
export function useStopRun(id: string) {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async () => unwrap(await api.client.POST("/investigations/{id}/cancel", { params: { path: { id } } })),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: keys.investigation(id) }),
  });
}

/** Marks the investigation's group as resolved. */
export function useResolve(id: string) {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async () => unwrap(await api.client.POST("/investigations/{id}/resolve", { params: { path: { id } } })),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: keys.investigation(id) }),
        queryClient.invalidateQueries({ queryKey: keys.watchers(id) }),
      ]);
    },
  });
}

/** Stops a running watcher of the investigation. */
export function useStopWatcher(investigationId: string) {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (watcherId: string) => unwrap(await api.client.POST("/watchers/{id}/stop", { params: { path: { id: watcherId } } })),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: keys.watchers(investigationId) }),
  });
}
