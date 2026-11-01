import { keepPreviousData, useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useApi } from "./ApiContext";
import { ApiError, detailOf } from "./client";
import { saveErrorOf } from "./rules";
import type { components } from "./schema";

export type Strategy = components["schemas"]["Strategy"];
export type StrategyDetail = components["schemas"]["StrategyDetail"];
export type StrategyVersion = components["schemas"]["StrategyVersion"];
export type StrategyVersionDetail = components["schemas"]["StrategyVersionDetail"];
export type StrategyVersionPage = components["schemas"]["StrategyVersionList"];
export type NewStrategyVersion = components["schemas"]["NewStrategyVersion"];

/** Where a strategy is defined, as the API names the layers. */
export type StrategyLayer = "built-in" | "runtime" | "file";

/** Whether a strategy is in effect. */
export type StrategyStatus = "active" | "shadowed" | "invalid";

/** The code of the problem of a save whose body is the same as the current version. */
export const CODE_UNCHANGED = "unchanged";

/** The most versions one request for the history asks for. */
const VERSION_PAGE = 50;

export const STRATEGIES_KEY = ["strategies"] as const;

/** The query keys of one strategy, which a save refreshes. */
export const strategyKeys = {
  root: (name: string) => ["strategy", name] as const,
  one: (name: string) => ["strategy", name] as const,
  versions: (name: string) => ["strategy", name, "versions"] as const,
  version: (name: string, version: number) => ["strategy", name, "version", version] as const,
};

/** Every strategy: those in effect first, then the runtime ones that are not. */
export function useStrategies() {
  const api = useApi();

  return useQuery({
    queryKey: STRATEGIES_KEY,
    queryFn: async () => {
      const { data, error, response } = await api.client.GET("/strategies");
      if (data === undefined) {
        throw new ApiError(response.status, detailOf(error, response.statusText));
      }

      return data.strategies;
    },
  });
}

/** The strategy in effect with a name, with its YAML. */
export function useStrategy(name: string) {
  const api = useApi();

  return useQuery({
    queryKey: strategyKeys.one(name),
    queryFn: async () => {
      const { data, error, response } = await api.client.GET("/strategies/{name}", { params: { path: { name } } });
      if (data === undefined) {
        throw new ApiError(response.status, detailOf(error, response.statusText));
      }

      return data;
    },
  });
}

/** The saved versions of a runtime strategy, newest first, a page at a time. */
export function useStrategyVersions(name: string) {
  const api = useApi();

  return useInfiniteQuery({
    queryKey: strategyKeys.versions(name),
    initialPageParam: undefined as number | undefined,
    getNextPageParam: (last: StrategyVersionPage) => last.next,
    queryFn: async ({ pageParam }) => {
      const { data, error, response } = await api.client.GET("/strategies/{name}/versions", {
        params: { path: { name }, query: { before: pageParam, limit: VERSION_PAGE } },
      });
      if (data === undefined) {
        throw new ApiError(response.status, detailOf(error, response.statusText));
      }

      return data;
    },
  });
}

/**
 * One saved version with its YAML. Nothing is asked for while version is
 * undefined. With keepPrevious, the version asked for before stays in data
 * while a new one loads, so an editor does not vanish after a save.
 */
export function useStrategyVersion(name: string, version: number | undefined, keepPrevious = false) {
  const api = useApi();

  return useQuery({
    queryKey: strategyKeys.version(name, version ?? 0),
    enabled: version !== undefined,
    placeholderData: keepPrevious ? keepPreviousData : undefined,
    queryFn: async () => {
      const { data, error, response } = await api.client.GET("/strategies/{name}/versions/{version}", {
        params: { path: { name, version: version ?? 0 } },
      });
      if (data === undefined) {
        throw new ApiError(response.status, detailOf(error, response.statusText));
      }

      return data;
    },
  });
}

/** Saves the YAML as the next version of the strategy name. A refused save throws a SaveError. */
export function useSaveStrategyVersion(name: string) {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (body: NewStrategyVersion) => {
      const { data, error, response } = await api.client.POST("/strategies/{name}/versions", { params: { path: { name } }, body });
      if (data === undefined) {
        throw saveErrorOf(error, response);
      }

      return data;
    },
    onSuccess: () => Promise.all([queryClient.invalidateQueries({ queryKey: STRATEGIES_KEY }), queryClient.invalidateQueries({ queryKey: strategyKeys.root(name) })]),
  });
}
