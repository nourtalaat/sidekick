import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useApi } from "./ApiContext";
import { ApiError, detailOf } from "./client";
import { keys } from "./investigations";

/** Marks the fix of a false positive as applied, or takes the mark back. */
export function useSetFixApplied(investigationId: string) {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (applied: boolean) => {
      const params = { params: { path: { id: investigationId } } };
      const result = applied ? await api.client.PUT("/investigations/{id}/fix-applied", params) : await api.client.DELETE("/investigations/{id}/fix-applied", params);

      if (result.data === undefined) {
        throw new ApiError(result.response.status, detailOf(result.error, result.response.statusText));
      }

      return result.data;
    },
    onSuccess: async () => {
      // The page shows the mark, and the dashboard counts it under noisy alerts fixed.
      await Promise.all([queryClient.invalidateQueries({ queryKey: keys.investigation(investigationId) }), queryClient.invalidateQueries({ queryKey: ["stats"] })]);
    },
  });
}
