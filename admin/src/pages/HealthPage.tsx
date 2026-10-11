// SPDX-License-Identifier: GPL-3.0-or-later

import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { api, unwrap } from "../api/client";
import type { ComponentHealth, HealthStatus } from "../api/types";
import { Badge, ErrorBox, Loading, Mono, PageHeader, type Tone } from "../components/ui";
import { formatDate } from "../lib/format";

export const healthLabels: Record<HealthStatus, [string, Tone]> = {
  HEALTH_STATUS_UNSPECIFIED: ["Unknown", "neutral"],
  HEALTH_STATUS_OK: ["Healthy", "good"],
  HEALTH_STATUS_DEGRADED: ["Degraded", "warn"],
  HEALTH_STATUS_DOWN: ["Down", "bad"],
};

export function HealthBadge({ status }: { status?: HealthStatus }) {
  const [label, tone] = healthLabels[status ?? "HEALTH_STATUS_UNSPECIFIED"];
  return <Badge tone={tone}>{label}</Badge>;
}

export const healthQuery = {
  queryKey: ["health"],
  queryFn: async () => unwrap(api.GET("/v1/admin/health")),
  refetchInterval: 60_000,
} as const;

/** "12 ms" from a protobuf duration ("0.012s"). */
function latency(d?: string): string {
  if (!d) return "—";
  const seconds = Number.parseFloat(d);
  if (Number.isNaN(seconds)) return d;
  return seconds < 1 ? `${Math.round(seconds * 1000)} ms` : `${seconds.toFixed(1)} s`;
}

function componentName(c: ComponentHealth) {
  if (c.kind === "provider") {
    const id = (c.name ?? "").replace(/^provider:/, "");
    return (
      <>
        <Link to="/providers" search={{ provider: id }}>
          {id}
        </Link>
        <div className="muted small">plugin</div>
      </>
    );
  }
  return c.kind === "database" ? "Database" : c.kind === "search" ? "Search" : c.name;
}

export function HealthPage() {
  const health = useQuery({ ...healthQuery, refetchInterval: 15_000 });
  const data = health.data;
  return (
    <>
      <PageHeader title="Health">
        <button type="button" className="small" onClick={() => health.refetch()} disabled={health.isFetching}>
          Check now
        </button>
      </PageHeader>
      <p className="muted">
        Each part of meta, checked now: the database, the search backend and every provider's plugin. The same data is
        served at <Mono>GET /v1/admin/health</Mono>; Prometheus metrics are described in the docs.
      </p>
      <ErrorBox error={health.error} onRetry={() => health.refetch()} />
      {health.isPending ? (
        <Loading />
      ) : data ? (
        <>
          <p>
            Overall: <HealthBadge status={data.status} /> <span className="muted small">meta {data.version}</span>
          </p>
          <table className="table">
            <thead>
              <tr>
                <th>Part</th>
                <th>Status</th>
                <th>Message</th>
                <th>Details</th>
                <th>Latency</th>
                <th>Checked</th>
              </tr>
            </thead>
            <tbody>
              {(data.components ?? []).map((c) => (
                <tr key={c.name}>
                  <td>{componentName(c)}</td>
                  <td>
                    <HealthBadge status={c.status} />
                  </td>
                  <td>{c.message ? c.message : <span className="muted">—</span>}</td>
                  <td>
                    <div className="details">
                      {Object.entries(c.details ?? {})
                        .sort(([a], [b]) => a.localeCompare(b))
                        .map(([k, v]) => (
                          <div key={k} className="small">
                            <span className="muted">{k.replaceAll("_", " ")}:</span> <Mono>{v}</Mono>
                          </div>
                        ))}
                    </div>
                  </td>
                  <td>{latency(c.latency)}</td>
                  <td className="small">{formatDate(c.checkedAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      ) : null}
    </>
  );
}
