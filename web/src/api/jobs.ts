import { useInfiniteQuery, useMutation, useQueryClient, type InfiniteData } from "@tanstack/react-query";
import { useApi } from "./ApiContext";
import { ApiError, detailOf, type Api } from "./client";
import type { components } from "./schema";

export type Job = components["schemas"]["Job"];
export type JobList = components["schemas"]["JobList"];

/** The key of the queries of dead jobs. */
export const JOBS_KEY = ["jobs", "dead"] as const;

/** How many jobs one request asks for. */
export const JOBS_PAGE_SIZE = 50;

const NOT_FOUND = 404;
const CONFLICT = 409;

/** The dead jobs, newest first, a page at a time: the next page is fetched with the cursor of the last. */
export function useDeadJobs() {
  const api = useApi();

  return useInfiniteQuery({
    queryKey: JOBS_KEY,
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const { data, error, response } = await api.client.GET("/jobs", {
        params: { query: { status: "dead", cursor: pageParam, limit: JOBS_PAGE_SIZE } },
      });
      if (data === undefined) {
        throw new ApiError(response.status, detailOf(error, response.statusText));
      }

      return data;
    },
    getNextPageParam: (last) => last.nextCursor,
  });
}

/** Sets a dead job back to pending. The job leaves the list. */
export function useRetryJob() {
  return useJobAction((api, id) => api.client.POST("/jobs/{id}/retry", { params: { path: { id } } }));
}

/** Deletes a dead job, so it cannot run again. The job leaves the list. */
export function useDiscardJob() {
  return useJobAction((api, id) => api.client.POST("/jobs/{id}/discard", { params: { path: { id } } }));
}

/**
 * The pages without the job, or the same pages when the job is not in them.
 * Cursors name a place in the list by time and ID, so the pages after a
 * removed job stay valid.
 */
export function withoutJob(data: InfiniteData<JobList, string | undefined>, id: number): InfiniteData<JobList, string | undefined> {
  return { ...data, pages: data.pages.map((page) => ({ ...page, jobs: page.jobs.filter((job) => job.id !== id) })) };
}

/**
 * A change to one job. On success the job is taken out of the pages already
 * read, without reading them again. When the server says the job is not
 * dead any more (409) or is gone (404), the list is read again, because it
 * is out of date.
 */
function useJobAction(send: (api: Api, id: number) => Promise<{ error?: unknown; response: Response }>) {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (id: number) => {
      const { error, response } = await send(api, id);
      if (!response.ok) {
        throw new ApiError(response.status, detailOf(error, response.statusText));
      }
    },
    onSuccess: (_, id) => {
      queryClient.setQueryData<InfiniteData<JobList, string | undefined>>(JOBS_KEY, (data) => data && withoutJob(data, id));
    },
    onError: (error) => {
      if (error instanceof ApiError && (error.status === CONFLICT || error.status === NOT_FOUND)) {
        void queryClient.invalidateQueries({ queryKey: JOBS_KEY });
      }
    },
  });
}
