import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useApi } from "./ApiContext";
import { ApiError, detailOf } from "./client";
import { saveErrorOf } from "./rules";
import { STRATEGIES_KEY } from "./strategies";
import type { components } from "./schema";

export type StrategyProposalSummary = components["schemas"]["StrategyProposalSummary"];
export type StrategyProposal = components["schemas"]["StrategyProposal"];
export type StrategyProposalStatus = components["schemas"]["StrategyProposalStatus"];
export type StrategyProposalApproval = components["schemas"]["StrategyProposalApproval"];
export type StrategyGate = components["schemas"]["StrategyGate"];
export type StrategyGateResult = components["schemas"]["StrategyGateResult"];

/** The code of the problem of an approval of a proposal that has not passed its gate. */
export const CODE_NOT_PASSED = "not_passed";

/** The proposals of every status but the drafts, or those of one status. */
export type ProposalFilter = StrategyProposalStatus | "all";

/** How many proposals one request asks for. */
const PAGE = 50;

export const PROPOSALS_KEY = ["strategy-proposals"] as const;

/** The query keys of the proposals. */
export const proposalKeys = {
  list: (filter: ProposalFilter) => [...PROPOSALS_KEY, "list", filter] as const,
  one: (id: string) => [...PROPOSALS_KEY, "one", id] as const,
};

/** The newest proposals, with a status or of any status but the drafts. */
export function useStrategyProposals(filter: ProposalFilter) {
  const api = useApi();

  return useQuery({
    queryKey: proposalKeys.list(filter),
    queryFn: async () => {
      const { data, error, response } = await api.client.GET("/strategy-proposals", {
        params: { query: { status: filter === "all" ? undefined : filter, limit: PAGE } },
      });
      if (data === undefined) {
        throw new ApiError(response.status, detailOf(error, response.statusText));
      }

      return data;
    },
  });
}

/** One proposal with its YAML, its feedback and its gate result. Nothing is asked for while id is undefined. */
export function useStrategyProposal(id: string | undefined) {
  const api = useApi();

  return useQuery({
    queryKey: proposalKeys.one(id ?? ""),
    enabled: id !== undefined,
    queryFn: async () => {
      const { data, error, response } = await api.client.GET("/strategy-proposals/{id}", { params: { path: { id: id ?? "" } } });
      if (data === undefined) {
        throw new ApiError(response.status, detailOf(error, response.statusText));
      }

      return data;
    },
  });
}

/** Refreshes the proposals, and the strategies when a decision may have changed them. */
function useRefresh() {
  const queryClient = useQueryClient();

  return () => Promise.all([queryClient.invalidateQueries({ queryKey: PROPOSALS_KEY }), queryClient.invalidateQueries({ queryKey: STRATEGIES_KEY })]);
}

/** Approves a proposal that passed its gate. A refusal throws a SaveError with the code of its problem. */
export function useApproveProposal(id: string) {
  const api = useApi();
  const refresh = useRefresh();

  return useMutation({
    mutationFn: async () => {
      const { data, error, response } = await api.client.POST("/strategy-proposals/{id}/approve", { params: { path: { id } } });
      if (data === undefined) {
        throw saveErrorOf(error, response);
      }

      return data;
    },
    onSuccess: refresh,
  });
}

/** Sets a proposal aside, with a reason when there is one. */
export function useRejectProposal(id: string) {
  const api = useApi();
  const refresh = useRefresh();

  return useMutation({
    mutationFn: async (reason: string) => {
      const { data, error, response } = await api.client.POST("/strategy-proposals/{id}/reject", {
        params: { path: { id } },
        body: reason.trim() === "" ? undefined : { reason },
      });
      if (data === undefined) {
        throw saveErrorOf(error, response);
      }

      return data;
    },
    onSuccess: refresh,
  });
}

/** Replaces the YAML of a proposal and sends it through the gate again. */
export function useEditProposal(id: string) {
  const api = useApi();
  const refresh = useRefresh();

  return useMutation({
    mutationFn: async (body: string) => {
      const { data, error, response } = await api.client.PUT("/strategy-proposals/{id}", { params: { path: { id } }, body: { body } });
      if (data === undefined) {
        throw saveErrorOf(error, response);
      }

      return data;
    },
    onSuccess: refresh,
  });
}
