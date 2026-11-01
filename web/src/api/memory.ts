import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useApi } from "./ApiContext";
import { ApiError, detailOf } from "./client";
import { saveErrorOf } from "./rules";
import type { components } from "./schema";

export type MemoryEntry = components["schemas"]["MemoryEntry"];
export type MemoryEntryList = components["schemas"]["MemoryEntryList"];
export type MemoryEntryUpdate = components["schemas"]["MemoryEntryUpdate"];
export type MemoryVerdict = components["schemas"]["Verdict"];

/** The longest summary or cause the API takes, in characters. */
export const MAX_MEMORY_CHARS = 2000;

/** The key of every query of memory entries, so a change can read them all again. */
export const MEMORY_KEY = ["memory"] as const;

/** Whether a list shows disabled entries, enabled ones, or both. */
export type DisabledFilter = "" | "disabled" | "enabled";

/** The filters of the list of memory entries. An empty one is not used. */
export interface MemoryFilters {
  service: string;
  verdict: MemoryVerdict | "";
  q: string;
  disabled: DisabledFilter;
}

/** The most entries the Memory tab reads at a time. */
const LIST_PAGE_SIZE = 25;

/** Pages of the memory entries that match filters, newest confirmation first; the next page starts at the cursor. */
export function useMemoryEntries(filters: MemoryFilters) {
  const api = useApi();

  return useInfiniteQuery({
    queryKey: [...MEMORY_KEY, "list", filters],
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const { data, error, response } = await api.client.GET("/memory/entries", {
        params: {
          query: {
            service: filters.service.trim() || undefined,
            verdict: filters.verdict || undefined,
            q: filters.q.trim() || undefined,
            disabled: filters.disabled === "" ? undefined : filters.disabled === "disabled",
            cursor: pageParam,
            limit: LIST_PAGE_SIZE,
          },
        },
      });
      if (data === undefined) {
        throw new ApiError(response.status, detailOf(error, response.statusText));
      }

      return data;
    },
    getNextPageParam: (last) => last.nextCursor,
  });
}

/** Replaces the summary, cause and disabled flag of a memory entry. The last write wins. */
export function useUpdateMemoryEntry() {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async ({ id, body }: { id: string; body: MemoryEntryUpdate }) => {
      const { data, error, response } = await api.client.PUT("/memory/entries/{id}", { params: { path: { id } }, body });
      if (data === undefined) {
        throw saveErrorOf(error, response);
      }

      return data;
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: MEMORY_KEY });
    },
  });
}

/** Deletes a memory entry. A later change of the investigation's confirmation writes a new one. */
export function useDeleteMemoryEntry() {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (id: string) => {
      const { error, response } = await api.client.DELETE("/memory/entries/{id}", { params: { path: { id } } });
      if (!response.ok) {
        throw saveErrorOf(error, response);
      }
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: MEMORY_KEY });
    },
  });
}
