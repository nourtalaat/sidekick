import { useQuery } from "@tanstack/react-query";
import { createContext, useContext } from "react";
import { Outlet } from "react-router";
import { useApi } from "./api/ApiContext";
import { ApiError, detailOf } from "./api/client";
import type { components } from "./api/schema";
import { QueryError } from "./components/QueryError";

export type Me = components["schemas"]["Me"];

const MeContext = createContext<Me | undefined>(undefined);

/** The signed-in user. Only pages under RequireSession can call it. */
export function useCurrentUser(): Me {
  const me = useContext(MeContext);
  if (!me) {
    throw new Error("useCurrentUser needs a signed-in session");
  }

  return me;
}

/**
 * Loads the signed-in user and shows its pages, or sends the browser to the
 * login page when there is no session (see UnauthorizedRedirect).
 */
export function RequireSession() {
  const api = useApi();

  const me = useQuery({
    queryKey: ["me"],
    queryFn: async () => {
      const { data, error, response } = await api.client.GET("/me");
      if (data === undefined) {
        throw new ApiError(response.status, detailOf(error, response.statusText));
      }

      api.setCsrfToken(data.csrfToken);

      return data;
    },
  });

  if (me.isPending) {
    return (
      <p role="status" className="sk-loading">
        Loading
      </p>
    );
  }

  if (me.isError) {
    if (me.error instanceof ApiError && me.error.status === 401) {
      return null;
    }

    return (
      <main className="sk-page">
        <QueryError error={me.error} onRetry={() => void me.refetch()} />
      </main>
    );
  }

  return (
    <MeContext value={me.data}>
      <Outlet />
    </MeContext>
  );
}
