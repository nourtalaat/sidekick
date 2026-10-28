import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useApi } from "./ApiContext";
import { ApiError, detailOf } from "./client";
import type { components } from "./schema";

export type Listener = components["schemas"]["Listener"];
export type Route = components["schemas"]["Route"];
export type NewListener = components["schemas"]["NewListener"];
export type ListenerUpdate = components["schemas"]["ListenerUpdate"];
export type NewRoute = components["schemas"]["NewRoute"];
export type RouteUpdate = components["schemas"]["RouteUpdate"];
export type FieldError = components["schemas"]["FieldError"];
export type RuleStatus = components["schemas"]["RuleStatus"];

export const LISTENERS_KEY = ["listeners"] as const;
export const ROUTES_KEY = ["routes"] as const;

/** The codes the API gives to the problems of a listener or route change. */
export const CODE_VALIDATION = "validation";
export const CODE_STALE = "stale";
export const CODE_FILE_RULE = "file_rule";
export const CODE_EXISTS = "exists";

/** A refused change of a listener or route, with the code and the fields of its problem document. */
export class SaveError extends ApiError {
  readonly code: string | undefined;
  readonly fields: readonly FieldError[];

  constructor(status: number, message: string, code: string | undefined, fields: readonly FieldError[]) {
    super(status, message);
    this.name = "SaveError";
    this.code = code;
    this.fields = fields;
  }
}

/** Reads a problem document that openapi-fetch parsed, or the response, into a SaveError. */
export function saveErrorOf(error: unknown, response: Response): SaveError {
  const problem = (typeof error === "object" && error !== null ? error : {}) as Partial<components["schemas"]["Problem"]>;

  return new SaveError(response.status, detailOf(error, response.statusText), problem.code, problem.errors ?? []);
}

/** Every listener: those in effect first, then the runtime ones that are not. */
export function useListeners() {
  const api = useApi();

  return useQuery({
    queryKey: LISTENERS_KEY,
    queryFn: async () => {
      const { data, error, response } = await api.client.GET("/listeners");
      if (data === undefined) {
        throw new ApiError(response.status, detailOf(error, response.statusText));
      }

      return data.listeners;
    },
  });
}

/** Every route: those in effect in the order they are tried, then the runtime ones that are not. */
export function useRoutes() {
  const api = useApi();

  return useQuery({
    queryKey: ROUTES_KEY,
    queryFn: async () => {
      const { data, error, response } = await api.client.GET("/routes");
      if (data === undefined) {
        throw new ApiError(response.status, detailOf(error, response.statusText));
      }

      return data.routes;
    },
  });
}

export function useCreateListener() {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (body: NewListener) => {
      const { data, error, response } = await api.client.POST("/listeners", { body });
      if (data === undefined) {
        throw saveErrorOf(error, response);
      }

      return data;
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: LISTENERS_KEY }),
  });
}

export function useUpdateListener(name: string) {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (body: ListenerUpdate) => {
      const { data, error, response } = await api.client.PUT("/listeners/{name}", { params: { path: { name } }, body });
      if (data === undefined) {
        throw saveErrorOf(error, response);
      }

      return data;
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: LISTENERS_KEY }),
  });
}

export function useDeleteListener() {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (name: string) => {
      const { error, response } = await api.client.DELETE("/listeners/{name}", { params: { path: { name } } });
      if (!response.ok) {
        throw saveErrorOf(error, response);
      }
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: LISTENERS_KEY }),
  });
}

export function useCreateRoute() {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (body: NewRoute) => {
      const { data, error, response } = await api.client.POST("/routes", { body });
      if (data === undefined) {
        throw saveErrorOf(error, response);
      }

      return data;
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ROUTES_KEY }),
  });
}

export function useUpdateRoute(name: string) {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (body: RouteUpdate) => {
      const { data, error, response } = await api.client.PUT("/routes/{name}", { params: { path: { name } }, body });
      if (data === undefined) {
        throw saveErrorOf(error, response);
      }

      return data;
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ROUTES_KEY }),
  });
}

export function useDeleteRoute() {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (name: string) => {
      const { error, response } = await api.client.DELETE("/routes/{name}", { params: { path: { name } } });
      if (!response.ok) {
        throw saveErrorOf(error, response);
      }
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ROUTES_KEY }),
  });
}
