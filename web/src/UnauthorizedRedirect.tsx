import { useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { useLocation, useNavigate } from "react-router";
import { useApi } from "./api/ApiContext";
import { LOGIN_ROUTE, loginRouteFor } from "./routes";

/** Sends the browser to the login page whenever the API answers 401. Renders nothing. */
export function UnauthorizedRedirect() {
  const api = useApi();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const location = useLocation();

  useEffect(
    () =>
      api.onUnauthorized(() => {
        if (location.pathname === LOGIN_ROUTE) {
          return;
        }

        queryClient.clear();
        void navigate(loginRouteFor(`${location.pathname}${location.search}`), { replace: true });
      }),
    [api, location, navigate, queryClient],
  );

  return null;
}
