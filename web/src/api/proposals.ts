import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useApi } from "./ApiContext";
import { ApiError, detailOf } from "./client";
import { keys } from "./investigations";
import type { components } from "./schema";

export type Proposal = components["schemas"]["Proposal"];
export type ProposalStatus = components["schemas"]["ProposalStatus"];
export type ProposalChange = components["schemas"]["ProposalChange"];

/** How many proposals one request asks for. */
export const PROPOSALS_PAGE_SIZE = 25;

/** The key of the review queue's queries, so a decision can refresh them. */
export const PROPOSALS_KEY = ["proposals"] as const;

/** The key of the query for the names of the actions that can be proposed. */
const PROPOSAL_ACTIONS_KEY = ["proposal-actions"] as const;

/**
 * The review queue, newest first, a page at a time: the next page is fetched
 * with the cursor of the last. An empty action lists every action.
 */
export function useProposals(statuses: readonly ProposalStatus[], action: string) {
  const api = useApi();

  return useInfiniteQuery({
    queryKey: [...PROPOSALS_KEY, statuses, action],
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const { data, error, response } = await api.client.GET("/proposals", {
        params: { query: { status: [...statuses], action: action === "" ? undefined : action, cursor: pageParam, limit: PROPOSALS_PAGE_SIZE } },
      });
      if (data === undefined) {
        throw new ApiError(response.status, detailOf(error, response.statusText));
      }

      return data;
    },
    getNextPageParam: (last) => last.nextCursor,
  });
}

/** The names of the actions in the config, sorted: the values the action filter of the queue takes. */
export function useProposalActions() {
  const api = useApi();

  return useQuery({
    queryKey: PROPOSAL_ACTIONS_KEY,
    queryFn: async () => {
      const { data, error, response } = await api.client.GET("/proposals/actions");
      if (data === undefined) {
        throw new ApiError(response.status, detailOf(error, response.statusText));
      }

      return data.actions;
    },
  });
}

/** What a person decides about a proposal. */
export type Decision = { kind: "approve" } | { kind: "reject"; note: string };

/**
 * Approves or rejects the proposal with id. The answer can be 409 when the
 * proposal expired, someone else decided first, or the config no longer
 * allows it; its detail says which, and who, so it is shown as it is.
 */
export function useDecideProposal(id: string, investigationId: string) {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (decision: Decision) => {
      const params = { params: { path: { id } } };
      const result =
        decision.kind === "approve"
          ? await api.client.POST("/proposals/{id}/approve", params)
          : await api.client.POST("/proposals/{id}/reject", { ...params, body: decision.note.trim() === "" ? undefined : { note: decision.note.trim() } });

      if (result.data === undefined) {
        throw new ApiError(result.response.status, detailOf(result.error, result.response.statusText));
      }

      return result.data;
    },
    // A decision that was refused, because someone else decided first or
    // the proposal expired, also means the lists are out of date.
    onSettled: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: PROPOSALS_KEY }),
        queryClient.invalidateQueries({ queryKey: keys.investigation(investigationId) }),
      ]);
    },
  });
}

/**
 * The answer to an undo when someone changed a field since the action
 * changed it, and force was not set: nothing was restored. `changes` is what
 * a restore would overwrite, field by field; asking again with force set
 * restores it.
 */
export class UndoConflictError extends ApiError {
  readonly changes: ProposalChange[];

  constructor(message: string, changes: ProposalChange[]) {
    super(409, message);
    this.name = "UndoConflictError";
    this.changes = changes;
  }
}

/**
 * Undoes the succeeded proposal with id. The first request has force false;
 * a UndoConflictError carries the difference for the person to read, and
 * the request is sent again with force true if they confirm. Other refusals
 * are ApiErrors whose message is the detail the server gave.
 */
export function useUndoProposal(id: string, investigationId: string) {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (force: boolean) => {
      const result = await api.client.POST("/proposals/{id}/undo", { params: { path: { id } }, body: { force } });

      if (result.data === undefined) {
        const detail = detailOf(result.error, result.response.statusText);

        if (result.response.status === 409 && result.error?.code === "conflict") {
          throw new UndoConflictError(detail, result.error.changes ?? []);
        }

        throw new ApiError(result.response.status, detail);
      }

      return result.data;
    },
    onSettled: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: PROPOSALS_KEY }),
        queryClient.invalidateQueries({ queryKey: keys.investigation(investigationId) }),
      ]);
    },
  });
}
