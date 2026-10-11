// SPDX-License-Identifier: GPL-3.0-or-later

import { QueryCache, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { type RouterHistory, RouterProvider } from "@tanstack/react-router";
import { ApiError } from "./api/errors";
import { session } from "./auth/session";
import { type AppRouter, createAppRouter } from "./router";

function shouldRetry(failureCount: number, error: unknown): boolean {
  // An error meta answered with (it carries a gRPC code) is its verdict: show it at once.
  // Retry only network failures and proxy errors.
  if (error instanceof ApiError && (error.grpcCode || (error.status >= 400 && error.status < 500))) return false;
  return failureCount < 2;
}

export type App = { router: AppRouter; queryClient: QueryClient; dispose: () => void };

/** Creates the query client and router and signs out to the login page when the token stops working. */
export function createApp(history?: RouterHistory): App {
  const queryClient = new QueryClient({
    queryCache: new QueryCache(),
    defaultOptions: {
      queries: { retry: shouldRetry, refetchOnWindowFocus: false, staleTime: 15_000 },
      mutations: { retry: false },
    },
  });
  const router = createAppRouter(queryClient, history);
  const unsubscribe = session.subscribe((reason) => {
    if (!reason) return;
    queryClient.clear();
    const here = router.state.location.href;
    void router.navigate({
      to: "/login",
      search: reason === "expired" ? { expired: true, redirect: here } : {},
    });
  });
  return { router, queryClient, dispose: unsubscribe };
}

export function AppRoot({ app }: { app: App }) {
  return (
    <QueryClientProvider client={app.queryClient}>
      <RouterProvider router={app.router} />
    </QueryClientProvider>
  );
}
