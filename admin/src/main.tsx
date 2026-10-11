// SPDX-License-Identifier: GPL-3.0-or-later

import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { AppRoot, createApp } from "./app";
import "./styles.css";

async function start() {
  // Mock mode (`npm run dev:mock`) answers the API from fake data in the browser. The
  // condition is a build-time constant, so production builds don't contain the mocks.
  if (import.meta.env.VITE_MOCK === "true") {
    const { startMockWorker } = await import("./mocks/browser");
    await startMockWorker();
  }
  const root = document.getElementById("root");
  if (!root) throw new Error("#root is missing from index.html");
  createRoot(root).render(
    <StrictMode>
      <AppRoot app={createApp()} />
    </StrictMode>,
  );
}

void start();
