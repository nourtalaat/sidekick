import { useInfiniteQuery } from "@tanstack/react-query";
import { useApi } from "./ApiContext";
import { ApiError, detailOf } from "./client";
import type { components } from "./schema";

export type AuditEntry = components["schemas"]["AuditEntry"];

/** The filters of the audit log. A filter that is empty is not sent. */
export interface AuditFilters {
  /** A local date and time as a datetime-local input writes it, such as 2026-10-20T10:00. */
  from: string;
  to: string;
  actor: string;
  action: string;
  investigation: string;
}

/** How many entries one request asks for. */
export const AUDIT_PAGE_SIZE = 50;

/** The audit log, newest first, a page at a time: the next page is fetched with the cursor of the last. */
export function useAudit(filters: AuditFilters) {
  const api = useApi();

  return useInfiniteQuery({
    queryKey: ["audit", filters],
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const query = {
        from: isoTime(filters.from),
        to: isoTime(filters.to),
        actor: blankToUndefined(filters.actor),
        action: blankToUndefined(filters.action),
        investigation: blankToUndefined(filters.investigation),
        cursor: pageParam,
        limit: AUDIT_PAGE_SIZE,
      };

      const { data, error, response } = await api.client.GET("/audit", { params: { query } });
      if (data === undefined) {
        throw new ApiError(response.status, detailOf(error, response.statusText));
      }

      return data;
    },
    getNextPageParam: (last) => last.nextCursor,
  });
}

/** A local date and time from a datetime-local input as the UTC time the API takes, or undefined when it is empty or not a date. */
export function isoTime(local: string): string | undefined {
  if (local.trim() === "") {
    return undefined;
  }

  const date = new Date(local);

  return Number.isNaN(date.getTime()) ? undefined : date.toISOString();
}

function blankToUndefined(text: string): string | undefined {
  const trimmed = text.trim();

  return trimmed === "" ? undefined : trimmed;
}
