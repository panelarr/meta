// SPDX-License-Identifier: GPL-3.0-or-later

import { useQuery } from "@tanstack/react-query";
import { Link, Outlet } from "@tanstack/react-router";
import { api, unwrap } from "../api/client";
import { session } from "../auth/session";

export function RootLayout() {
  return <Outlet />;
}

const nav = [
  { to: "/catalogue", label: "Catalogue" },
  { to: "/providers", label: "Providers" },
  { to: "/clients", label: "Clients" },
  { to: "/integrations", label: "Integrations" },
] as const;

/** A badge when any provider is unhealthy, from the provider list the Providers page uses too. */
function useProviderHealth() {
  return useQuery({
    queryKey: ["providers"],
    queryFn: async () => (await unwrap(api.GET("/v1/admin/providers"))).providers ?? [],
    refetchInterval: 60_000,
  });
}

export function AppLayout() {
  const providers = useProviderHealth();
  const unhealthy = (providers.data ?? []).filter(
    (p) => p.health === "HEALTH_STATUS_DOWN" || p.health === "HEALTH_STATUS_DEGRADED",
  );

  return (
    <div className="shell">
      <header className="topbar">
        <Link to="/catalogue" className="brand">
          meta <span className="muted">admin</span>
        </Link>
        <nav className="nav" aria-label="Main">
          {nav.map((item) => (
            <Link key={item.to} to={item.to} activeProps={{ className: "active" }}>
              {item.label}
            </Link>
          ))}
        </nav>
        <div className="topbar-end">
          {unhealthy.length > 0 ? (
            <Link to="/providers" className="badge badge-warn" title={unhealthy.map((p) => p.id).join(", ")}>
              {unhealthy.length === 1 ? "1 provider unhealthy" : `${unhealthy.length} providers unhealthy`}
            </Link>
          ) : null}
          <button type="button" className="small" onClick={() => session.signOut("logout")}>
            Log out
          </button>
        </div>
      </header>
      <main className="content">
        <Outlet />
      </main>
    </div>
  );
}

export function NotFound() {
  return (
    <main className="content">
      <h1>Not found</h1>
      <p>
        There's no page here. <Link to="/catalogue">Go to the catalogue</Link>.
      </p>
    </main>
  );
}
