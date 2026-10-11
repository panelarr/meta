// SPDX-License-Identifier: GPL-3.0-or-later

import { useQuery } from "@tanstack/react-query";
import { Link, Outlet } from "@tanstack/react-router";
import icon from "../assets/panelarr-icon.svg";
import { session } from "../auth/session";
import { healthQuery } from "./HealthPage";

export function RootLayout() {
  return <Outlet />;
}

const nav = [
  { to: "/catalogue", label: "Catalogue" },
  { to: "/duplicates", label: "Duplicates" },
  { to: "/providers", label: "Providers" },
  { to: "/jobs", label: "Jobs" },
  { to: "/clients", label: "Clients" },
  { to: "/integrations", label: "Integrations" },
  { to: "/health", label: "Health" },
] as const;

export function AppLayout() {
  // A badge when meta isn't fully healthy, from the detailed health view.
  const health = useQuery(healthQuery);
  const status = health.data?.status;
  const unhealthy = (health.data?.components ?? []).filter((c) => c.status !== "HEALTH_STATUS_OK");

  return (
    <div className="shell">
      <header className="topbar">
        <Link to="/catalogue" className="brand" aria-label="Panelarr Meta">
          <img src={icon} alt="" width={26} height={26} className="brand-icon" />
          <span className="brand-name">Panelarr</span>
          <span className="brand-product">Meta</span>
        </Link>
        <nav className="nav" aria-label="Main">
          {nav.map((item) => (
            <Link key={item.to} to={item.to} activeProps={{ className: "active" }}>
              {item.label}
            </Link>
          ))}
        </nav>
        <div className="topbar-end">
          {status && status !== "HEALTH_STATUS_OK" ? (
            <Link
              to="/health"
              className={`badge ${status === "HEALTH_STATUS_DOWN" ? "badge-bad" : "badge-warn"}`}
              title={unhealthy.map((c) => c.name).join(", ")}
            >
              {status === "HEALTH_STATUS_DOWN" ? "Down" : "Degraded"}: {unhealthy.length}{" "}
              {unhealthy.length === 1 ? "part" : "parts"}
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
