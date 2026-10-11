// SPDX-License-Identifier: GPL-3.0-or-later

import { createMemoryHistory } from "@tanstack/react-router";
import { render } from "@testing-library/react";
import { type App, AppRoot, createApp } from "../app";
import { session } from "../auth/session";
import { MOCK_ADMIN_TOKEN } from "../mocks/data";

/** Renders the whole app at `path`, signed in unless `token` is null. */
export function renderApp(path: string, token: string | null = MOCK_ADMIN_TOKEN): App & ReturnType<typeof render> {
  if (token) session.signIn(token);
  const app = createApp(createMemoryHistory({ initialEntries: [path] }));
  const result = render(<AppRoot app={app} />);
  return { ...app, ...result };
}
