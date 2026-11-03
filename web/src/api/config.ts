import { useQuery } from "@tanstack/react-query";
import { useApi } from "./ApiContext";
import { ApiError, detailOf } from "./client";
import type { components } from "./schema";

export type EffectiveConfig = components["schemas"]["EffectiveConfig"];
export type RuntimeRule = components["schemas"]["RuntimeRule"];

/** The key of the query of the effective config. */
export const CONFIG_KEY = ["config"] as const;

/** The config Sidekick runs with: the file with its defaults, and the listeners and routes added at runtime. */
export function useConfig() {
  const api = useApi();

  return useQuery({
    queryKey: CONFIG_KEY,
    queryFn: async () => {
      const { data, error, response } = await api.client.GET("/config");
      if (data === undefined) {
        throw new ApiError(response.status, detailOf(error, response.statusText));
      }

      return data;
    },
  });
}
