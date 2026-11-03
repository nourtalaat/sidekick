import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useApi } from "./ApiContext";
import { ApiError, detailOf } from "./client";
import { saveErrorOf } from "./rules";
import type { components } from "./schema";

export type Token = components["schemas"]["Token"];
export type NewToken = components["schemas"]["NewToken"];

/** The longest token name the API takes, in characters. */
export const MAX_TOKEN_NAME_CHARS = 100;

const TOKENS_KEY = ["tokens"] as const;

/** Every API token, revoked and expired ones included. The tokens themselves are never in the answer. */
export function useTokens() {
  const api = useApi();

  return useQuery({
    queryKey: TOKENS_KEY,
    queryFn: async () => {
      const { data, error, response } = await api.client.GET("/tokens");
      if (data === undefined) {
        throw new ApiError(response.status, detailOf(error, response.statusText));
      }

      return data.tokens;
    },
  });
}

/** What the create call needs: the new token's settings, and where to hand the secret. */
interface CreateTokenInput {
  body: NewToken;
  /** Called once with the secret of the new token. */
  onSecret(secret: string): void;
}

/**
 * Creates a token. The secret goes only to onSecret and is not returned, so
 * the mutation cache, which keeps a result for some minutes, never holds it.
 */
export function useCreateToken() {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async ({ body, onSecret }: CreateTokenInput) => {
      const { data, error, response } = await api.client.POST("/tokens", { body });
      if (data === undefined) {
        throw saveErrorOf(error, response);
      }

      onSecret(data.secret);

      return data.token;
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: TOKENS_KEY });
    },
  });
}

/** Revokes a token by its ID. A token that is already revoked answers 404. */
export function useRevokeToken() {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (id: string) => {
      const { error, response } = await api.client.DELETE("/tokens/{id}", { params: { path: { id } } });
      if (!response.ok) {
        throw saveErrorOf(error, response);
      }
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: TOKENS_KEY });
    },
  });
}
