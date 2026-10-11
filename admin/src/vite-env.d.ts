// SPDX-License-Identifier: GPL-3.0-or-later

/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** "true" in mock mode (`npm run dev:mock`). */
  readonly VITE_MOCK?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
