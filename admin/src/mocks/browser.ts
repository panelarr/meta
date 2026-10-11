// SPDX-License-Identifier: GPL-3.0-or-later

import { setupWorker } from "msw/browser";
import { createDb, createHandlers } from "./handlers";

/** Starts mock mode: a service worker answers /v1 from fake data. */
export async function startMockWorker(): Promise<void> {
  const worker = setupWorker(...createHandlers(createDb(), 250));
  await worker.start({
    // Let the page's own files (scripts, styles, fonts) through untouched.
    onUnhandledRequest: "bypass",
    quiet: false,
  });
  console.info(
    'Panelarr Meta admin: mock mode. Sign in with any token ("bad" is rejected, "mtk_…" is a client key, "config-key" an api_keys key).',
  );
}
