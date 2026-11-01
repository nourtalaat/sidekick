import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useApi } from "./ApiContext";
import { ApiError, detailOf } from "./client";
import type { components } from "./schema";

export type Scenario = components["schemas"]["Scenario"];
export type ScenarioDetail = components["schemas"]["ScenarioDetail"];
export type ScenarioPair = components["schemas"]["ScenarioPair"];
export type ScenarioCandidate = components["schemas"]["ScenarioCandidate"];
export type ScenarioStatus = components["schemas"]["ScenarioStatus"];

/** How many scenarios one request asks for. */
export const SCENARIOS_PAGE = 50;

const keys = {
  list: (status: ScenarioStatus | undefined) => ["scenarios", { status }] as const,
  one: (id: string) => ["scenario", id] as const,
};

interface Result<T> {
  data?: T;
  error?: unknown;
  response: Response;
}

/** The data of an answer, or an ApiError that says what the problem document says, with the fields it lists. */
function unwrap<T>(result: Result<T>): T {
  if (result.data === undefined) {
    throw new ApiError(result.response.status, problemText(result.error, result.response.statusText));
  }

  return result.data;
}

/** The detail of a problem document and, one to a line, the fields it refused. */
export function problemText(error: unknown, fallback: string): string {
  const detail = detailOf(error, fallback);

  if (typeof error === "object" && error !== null && "errors" in error && Array.isArray(error.errors)) {
    const lines = error.errors
      .map((e: unknown) => (typeof e === "object" && e !== null && "message" in e ? `${"field" in e && e.field ? `${String(e.field)}: ` : ""}${String(e.message)}` : ""))
      .filter((line: string) => line !== "");

    return [detail, ...lines].join("\n");
  }

  return detail;
}

/** Pages of scenarios, most recently changed first, of one status or all. */
export function useScenarioList(status: ScenarioStatus | undefined) {
  const api = useApi();

  return useInfiniteQuery({
    queryKey: keys.list(status),
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) =>
      unwrap(await api.client.GET("/scenarios", { params: { query: { status, cursor: pageParam, limit: SCENARIOS_PAGE } } })),
    getNextPageParam: (last) => last.nextCursor,
  });
}

/** A scenario with its notes, the places to check, and its parts next to their originals. */
export function useScenario(id: string) {
  const api = useApi();

  return useQuery({
    queryKey: keys.one(id),
    queryFn: async () => unwrap(await api.client.GET("/scenarios/{id}", { params: { path: { id } } })),
  });
}

/** Exports a confirmed investigation; the answer says whether the draft is new. */
export function useExportScenario(investigationId: string) {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async () => {
      const result = await api.client.POST("/investigations/{id}/scenario-draft", { params: { path: { id: investigationId } } });

      return { scenario: unwrap(result), created: result.response.status === 201 };
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["scenarios"] });
    },
  });
}

/** Replaces the file of a scenario, which makes it a draft again. */
export function useUpdateScenario(id: string) {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (change: { revision: number; body: string }) =>
      unwrap(await api.client.PUT("/scenarios/{id}", { params: { path: { id } }, body: change })),
    onSuccess: async () => {
      await Promise.all([queryClient.invalidateQueries({ queryKey: keys.one(id) }), queryClient.invalidateQueries({ queryKey: ["scenarios"] })]);
    },
  });
}

/** Marks a draft as reviewed, with the tick that the reviewer checked it for sensitive data. */
export function useReviewScenario(id: string) {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (review: { revision: number; sensitiveDataChecked: boolean }) =>
      unwrap(await api.client.POST("/scenarios/{id}/review", { params: { path: { id } }, body: review })),
    onSuccess: async () => {
      await Promise.all([queryClient.invalidateQueries({ queryKey: keys.one(id) }), queryClient.invalidateQueries({ queryKey: ["scenarios"] })]);
    },
  });
}

/** Where the browser downloads a reviewed scenario from. */
export function scenarioFileUrl(id: string): string {
  return `/api/v1/scenarios/${encodeURIComponent(id)}/file`;
}
