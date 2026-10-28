import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";
import { bundledPackages, cleanOutDir } from "./build/plugins.ts";

const outDir = fileURLToPath(new URL("../internal/web/dist", import.meta.url));
const packagesFile = fileURLToPath(new URL("./.build/bundled-packages.json", import.meta.url));

// The Go server of `sidekick serve`, which the dev server forwards API and
// login requests to.
const backend = process.env.SIDEKICK_URL ?? "http://localhost:8080";

export default defineConfig({
  plugins: [react(), cleanOutDir(outDir), bundledPackages(packagesFile)],
  // The favicon files come from the shared brand assets, not a copy.
  publicDir: "../assets/favicon",
  build: {
    outDir,
    emptyOutDir: false,
    // A data: URI for a font would break font-src 'self' in the CSP.
    assetsInlineLimit: 0,
  },
  server: {
    proxy: {
      "/api": backend,
      "/auth": backend,
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test-setup.ts"],
  },
});
