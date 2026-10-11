// SPDX-License-Identifier: GPL-3.0-or-later

import type { QueryClient } from "@tanstack/react-query";
import {
  createRootRouteWithContext,
  createRoute,
  createRouter,
  type RouterHistory,
  redirect,
} from "@tanstack/react-router";
import { session } from "./auth/session";
import { type CatalogueSearch, parseCatalogueSearch } from "./lib/catalogue";
import { AppLayout, NotFound, RootLayout } from "./pages/AppLayout";
import { CataloguePage } from "./pages/CataloguePage";
import { ClientsPage } from "./pages/ClientsPage";
import { EntryPage } from "./pages/EntryPage";
import { IntegrationsPage } from "./pages/IntegrationsPage";
import { LoginPage } from "./pages/LoginPage";
import { ProvidersPage } from "./pages/ProvidersPage";

export type RouterContext = { queryClient: QueryClient };

type Search = Record<string, unknown>;

function text(value: unknown): string | undefined {
  if (typeof value === "string" && value !== "") return value;
  if (typeof value === "number") return String(value);
  return undefined;
}

const rootRoute = createRootRouteWithContext<RouterContext>()({
  component: RootLayout,
  notFoundComponent: NotFound,
});

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  validateSearch: (search: Search): { redirect?: string; expired?: boolean } => ({
    redirect: text(search.redirect),
    expired: search.expired === true || search.expired === "true" ? true : undefined,
  }),
  beforeLoad: () => {
    if (session.signedIn()) throw redirect({ to: "/catalogue" });
  },
  component: LoginPage,
});

// Every page but the login needs the token.
const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "_app",
  beforeLoad: ({ location }) => {
    if (!session.signedIn()) throw redirect({ to: "/login", search: { redirect: location.href } });
  },
  component: AppLayout,
});

const indexRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/catalogue" });
  },
});

const catalogueRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/catalogue",
  validateSearch: (search: Search): CatalogueSearch => parseCatalogueSearch(search),
  component: CataloguePage,
});

const entryRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/entries/$entryId",
  component: EntryPage,
});

const providersRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/providers",
  validateSearch: (search: Search): { provider?: string; run?: string } => ({
    provider: text(search.provider),
    run: text(search.run),
  }),
  component: ProvidersPage,
});

const clientsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/clients",
  component: ClientsPage,
});

const integrationsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/integrations",
  component: IntegrationsPage,
});

const routeTree = rootRoute.addChildren([
  loginRoute,
  appRoute.addChildren([indexRoute, catalogueRoute, entryRoute, providersRoute, clientsRoute, integrationsRoute]),
]);

export function createAppRouter(queryClient: QueryClient, history?: RouterHistory) {
  return createRouter({
    routeTree,
    context: { queryClient },
    defaultPreload: false,
    scrollRestoration: true,
    ...(history ? { history } : {}),
  });
}

export type AppRouter = ReturnType<typeof createAppRouter>;

declare module "@tanstack/react-router" {
  interface Register {
    router: AppRouter;
  }
}
