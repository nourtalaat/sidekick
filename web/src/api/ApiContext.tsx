import { createContext, useContext, type ReactNode } from "react";
import type { Api } from "./client";

const ApiContext = createContext<Api | undefined>(undefined);

export function ApiProvider({ api, children }: { api: Api; children: ReactNode }) {
  return <ApiContext value={api}>{children}</ApiContext>;
}

export function useApi(): Api {
  const api = useContext(ApiContext);
  if (!api) {
    throw new Error("useApi needs an ApiProvider");
  }

  return api;
}
