import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useApi } from "./ApiContext";
import { ApiError, detailOf } from "./client";
import { saveErrorOf } from "./rules";
import type { components } from "./schema";

export type ServiceNotes = components["schemas"]["ServiceNotes"];
export type ServiceNotesSummary = components["schemas"]["ServiceNotesSummary"];

/** The longest notes the API takes, in characters. */
export const MAX_NOTES_CHARS = 4000;

/** The key of every query of service notes, so a change can read them all again. */
export const NOTES_KEY = ["service-notes"] as const;

/** The most services the Notes tab reads at a time. */
const LIST_PAGE_SIZE = 50;

/** Pages of the services that have notes, by name; the next page starts after the last service. */
export function useServiceNotesList() {
  const api = useApi();

  return useInfiniteQuery({
    queryKey: [...NOTES_KEY, "list"],
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const { data, error, response } = await api.client.GET("/services/notes", { params: { query: { after: pageParam, limit: LIST_PAGE_SIZE } } });
      if (data === undefined) {
        throw new ApiError(response.status, detailOf(error, response.statusText));
      }

      return data;
    },
    getNextPageParam: (last) => last.nextAfter,
  });
}

/** The notes about a service. A service without notes has empty text. */
export function useServiceNotes(service: string) {
  const api = useApi();

  return useQuery({
    queryKey: [...NOTES_KEY, "one", service],
    queryFn: async () => {
      const { data, error, response } = await api.client.GET("/services/{name}/notes", { params: { path: { name: service } } });
      if (data === undefined) {
        throw new ApiError(response.status, detailOf(error, response.statusText));
      }

      return data;
    },
  });
}

/** Replaces the notes about a service. Empty text deletes them. The last write wins. */
export function useSaveServiceNotes() {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async ({ service, text }: { service: string; text: string }) => {
      const { data, error, response } = await api.client.PUT("/services/{name}/notes", { params: { path: { name: service } }, body: { text } });
      if (data === undefined) {
        throw saveErrorOf(error, response);
      }

      return data;
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: NOTES_KEY });
    },
  });
}
