import { QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router";
import "../../assets/tokens/tokens.css";
import { App } from "./App";
import { ApiProvider } from "./api/ApiContext";
import { createApi } from "./api/client";
import { createQueryClient } from "./api/queryClient";
import "./fonts";
import "./styles.css";
import { applyStoredTheme, ThemeProvider } from "./theme";

const root = document.getElementById("root");
if (!root) {
  throw new Error("index.html has no #root element");
}

applyStoredTheme();

const api = createApi();
const queryClient = createQueryClient();

createRoot(root).render(
  <StrictMode>
    <ApiProvider api={api}>
      <QueryClientProvider client={queryClient}>
        <ThemeProvider>
          <BrowserRouter>
            <App />
          </BrowserRouter>
        </ThemeProvider>
      </QueryClientProvider>
    </ApiProvider>
  </StrictMode>,
);
