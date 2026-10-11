// SPDX-License-Identifier: GPL-3.0-or-later

import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import react from "@vitejs/plugin-react";
import { loadEnv, type Plugin } from "vite";
import { defineConfig } from "vitest/config";

const require = createRequire(import.meta.url);

/**
 * Serves MSW's service worker from node_modules in mock mode, so the worker file is
 * never copied into public/ and can't end up in a production build.
 */
function mockServiceWorker(): Plugin {
  return {
    name: "meta-mock-service-worker",
    apply: "serve",
    configureServer(server) {
      const file = require.resolve("msw/mockServiceWorker.js");
      server.middlewares.use("/mockServiceWorker.js", (_req, res) => {
        res.setHeader("Content-Type", "text/javascript");
        res.end(readFileSync(file));
      });
    },
  };
}

export default defineConfig(({ command, mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const mock = env.VITE_MOCK === "true";
  if (command === "build" && mock) {
    throw new Error("Mock mode is for the dev server only; don't build with VITE_MOCK=true.");
  }

  // The meta the dev server proxies to. Ignored in mock mode.
  const backend = env.META_BACKEND || "http://localhost:8901";

  return {
    plugins: [react(), ...(mock ? [mockServiceWorker()] : [])],
    server: {
      port: Number(env.ADMIN_PORT || 5173),
      proxy: mock
        ? undefined
        : {
            "/v1": backend,
            "/openapi.yaml": backend,
          },
    },
    build: {
      outDir: "dist",
      emptyOutDir: true,
      target: "es2022",
      // Plain files only: meta serves them under a strict CSP.
      assetsInlineLimit: 0,
    },
    test: {
      environment: "jsdom",
      environmentOptions: { jsdom: { url: "http://localhost:3000/" } },
      setupFiles: ["src/test/setup.ts"],
      include: ["src/**/*.test.{ts,tsx}"],
      restoreMocks: true,
    },
  };
});
