// SPDX-License-Identifier: GPL-3.0-or-later

import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { setupServer } from "msw/node";
import { afterAll, afterEach, beforeAll } from "vitest";
import { createDb, createHandlers } from "../mocks/handlers";

// jsdom has no object URLs; AuthImage only needs a string back.
if (typeof URL.createObjectURL !== "function") {
  URL.createObjectURL = () => "blob:test";
  URL.revokeObjectURL = () => {};
}

// jsdom doesn't scroll; the router restores scroll on every navigation.
window.scrollTo = () => {};

/** The same fake API as mock mode, fresh for every test. */
export const server = setupServer(...createHandlers(createDb()));

beforeAll(() => server.listen({ onUnhandledRequest: "error" }));
afterEach(() => {
  cleanup();
  server.resetHandlers(...createHandlers(createDb()));
  if (typeof sessionStorage !== "undefined") sessionStorage.clear();
});
afterAll(() => server.close());
