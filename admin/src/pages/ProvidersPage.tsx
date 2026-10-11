// SPDX-License-Identifier: GPL-3.0-or-later

import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { api, unwrap } from "../api/client";
import { ApiError } from "../api/errors";
import type { Provider, SyncRun, SyncRunStatus } from "../api/types";
import { Dialog } from "../components/Dialog";
import { TabList, tabProps } from "../components/Tabs";
import { Badge, Empty, ErrorBox, Loading, Mono, PageHeader, type Tone } from "../components/ui";
import { enumLabel, formatDate } from "../lib/format";

const route = getRouteApi("/_app/providers");

const syncKinds = ["dump", "delta"];

const healthTones: Record<string, [string, Tone]> = {
  HEALTH_STATUS_OK: ["Healthy", "good"],
  HEALTH_STATUS_DEGRADED: ["Degraded", "warn"],
  HEALTH_STATUS_DOWN: ["Down", "bad"],
  HEALTH_STATUS_UNSPECIFIED: ["Unknown", "neutral"],
};

export const runTones: Record<SyncRunStatus, [string, Tone]> = {
  SYNC_RUN_STATUS_UNSPECIFIED: ["Unknown", "neutral"],
  SYNC_RUN_STATUS_RUNNING: ["Running", "info"],
  SYNC_RUN_STATUS_SUCCEEDED: ["Succeeded", "good"],
  SYNC_RUN_STATUS_FAILED: ["Failed", "bad"],
  SYNC_RUN_STATUS_CANCELLED: ["Cancelled", "warn"],
};

function RunStatus({ run }: { run: SyncRun }) {
  const [label, tone] = runTones[run.status ?? "SYNC_RUN_STATUS_UNSPECIFIED"];
  return <Badge tone={tone}>{label}</Badge>;
}

/** "1m 23s" between two timestamps, or since the start for running runs. */
export function runDuration(run: SyncRun): string {
  if (!run.startedAt) return "—";
  const start = new Date(run.startedAt).getTime();
  const end = run.finishedAt ? new Date(run.finishedAt).getTime() : Date.now();
  const seconds = Math.max(0, Math.round((end - start) / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

function count(value: string | undefined): string {
  return Number(value ?? 0).toLocaleString();
}

export function ProvidersPage() {
  const providers = useQuery({
    queryKey: ["providers"],
    queryFn: async () => (await unwrap(api.GET("/v1/admin/providers"))).providers ?? [],
    refetchInterval: 60_000,
  });
  const [running, setRunning] = useState<{ provider: Provider; kind: string } | null>(null);

  return (
    <>
      <PageHeader title="Providers" />
      <p className="muted">
        Metadata plugins configured in meta. Each syncs on its schedule; "Run now" starts a sync at once.
      </p>
      {providers.isPending ? <Loading /> : null}
      <ErrorBox error={providers.error} onRetry={() => providers.refetch()} />
      {providers.data?.length === 0 ? (
        <Empty>
          No providers configured: the catalogue stays empty. Add a metadata plugin under <Mono>providers</Mono> in
          meta's configuration and restart meta.
        </Empty>
      ) : null}
      {providers.data && providers.data.length > 0 ? (
        <div className="cards">
          {providers.data.map((p) => (
            <ProviderCard key={p.id} provider={p} onRun={(kind) => setRunning({ provider: p, kind })} />
          ))}
        </div>
      ) : null}
      <RunsSection providers={providers.data ?? []} />
      {running ? <RunDialog provider={running.provider} kind={running.kind} onClose={() => setRunning(null)} /> : null}
    </>
  );
}

function ProviderCard({ provider: p, onRun }: { provider: Provider; onRun: (kind: string) => void }) {
  const [healthLabel, healthTone] = healthTones[p.health ?? "HEALTH_STATUS_UNSPECIFIED"] ?? ["Unknown", "neutral"];
  const ingestions = p.info?.ingestions ?? [];
  const kinds = ingestions.map((i) => i.kind ?? "").filter((k) => syncKinds.includes(k));
  return (
    <article className="card" aria-label={`Provider ${p.id}`}>
      <header className="card-header">
        <div>
          <h2>{p.info?.name || p.id}</h2>
          <Mono>{p.id}</Mono>
        </div>
        <div className="badges">
          <Badge tone={healthTone} title={p.healthMessage}>
            {healthLabel}
          </Badge>
          <Badge>{p.mode}</Badge>
        </div>
      </header>
      {p.healthMessage ? <p className="muted small">{p.healthMessage}</p> : null}
      <dl className="details">
        <dt>Plugin</dt>
        <dd>
          {p.info?.version ? (
            `${p.info.name || p.info.id} ${p.info.version}`
          ) : (
            <span className="muted">not connected</span>
          )}
        </dd>
        <dt>Ingestion</dt>
        <dd>
          <div className="badges">
            {ingestions.length === 0 ? <span className="muted">—</span> : null}
            {ingestions.map((i) => (
              <Badge key={i.kind} tone="info" title={i.description}>
                {i.kind}
              </Badge>
            ))}
          </div>
        </dd>
        <dt>Schedules</dt>
        <dd>
          {Object.keys(p.schedules ?? {}).length === 0 ? <span className="muted">manual only</span> : null}
          {Object.entries(p.schedules ?? {}).map(([kind, cron]) => (
            <div key={kind}>
              {kind}: <Mono>{cron}</Mono> <span className="muted small">UTC</span>
            </div>
          ))}
        </dd>
        <dt>Records</dt>
        <dd>{count(p.recordCount)}</dd>
        <dt>Last runs</dt>
        <dd>
          {(p.lastRuns ?? []).length === 0 ? <span className="muted">never synced</span> : null}
          {(p.lastRuns ?? []).map((r) => (
            <div key={r.id}>
              {r.kind} <RunStatus run={r} /> <span className="muted small">{formatDate(r.startedAt)}</span>
            </div>
          ))}
        </dd>
      </dl>
      {kinds.length > 0 ? (
        <div className="actions">
          {kinds.map((kind) => (
            <button key={kind} type="button" onClick={() => onRun(kind)}>
              Run {kind} now
            </button>
          ))}
        </div>
      ) : null}
    </article>
  );
}

function RunDialog({ provider, kind, onClose }: { provider: Provider; kind: string; onClose: () => void }) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [force, setForce] = useState(false);
  const run = useMutation({
    mutationFn: async () => {
      try {
        return await unwrap(
          api.POST("/v1/admin/providers/{provider}/sync", {
            params: { path: { provider: provider.id ?? "" } },
            body: { kind, force },
          }),
        );
      } catch (error) {
        if (error instanceof ApiError && (error.status === 409 || error.grpcCode === "ALREADY_EXISTS")) {
          throw new Error(`A sync of ${provider.id} is already running. Wait for it to finish.`);
        }
        throw error;
      }
    },
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({ queryKey: ["sync-runs"] });
      await queryClient.invalidateQueries({ queryKey: ["providers"] });
      onClose();
      void navigate({ to: "/providers", search: { provider: provider.id, run: res.run?.id } });
    },
  });
  return (
    <Dialog title={`Run a ${kind} sync of ${provider.info?.name || provider.id}?`} onClose={onClose}>
      <p>The sync starts at once and runs in the background. Its progress shows in the runs below.</p>
      <label className="filter-check">
        <input type="checkbox" checked={force} onChange={(e) => setForce(e.target.checked)} />
        <span>Force: send every record even if the provider reports no changes</span>
      </label>
      <ErrorBox error={run.error} />
      <div className="actions">
        <button type="button" className="primary" disabled={run.isPending} onClick={() => run.mutate()}>
          {run.isPending ? "Starting…" : "Run now"}
        </button>
        <button type="button" onClick={onClose}>
          Cancel
        </button>
      </div>
    </Dialog>
  );
}

function RunsSection({ providers }: { providers: Provider[] }) {
  const search = route.useSearch();
  const navigate = useNavigate();
  const provider = search.provider ?? "";
  const runs = useInfiniteQuery({
    queryKey: ["sync-runs", provider],
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET("/v1/admin/sync-runs", {
          params: { query: { provider: provider || undefined, pageSize: 20, pageToken: pageParam } },
        }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextPageToken || undefined,
    // Follow running syncs live.
    refetchInterval: (query) =>
      query.state.data?.pages.some((p) => (p.runs ?? []).some((r) => r.status === "SYNC_RUN_STATUS_RUNNING"))
        ? 2000
        : false,
  });
  const list = (runs.data?.pages ?? []).flatMap((p) => p.runs ?? []);
  const open = list.find((r) => r.id === search.run);

  return (
    <section className="section">
      <header className="section-header">
        <h2>Sync runs</h2>
      </header>
      <TabList label="Runs of">
        <Link to="/providers" search={{}} {...tabProps(provider === "")}>
          All
        </Link>
        {providers.map((p) => (
          <Link key={p.id} to="/providers" search={{ provider: p.id }} {...tabProps(provider === p.id)}>
            {p.id}
          </Link>
        ))}
      </TabList>
      {runs.isPending ? <Loading /> : null}
      <ErrorBox error={runs.error} onRetry={() => runs.refetch()} />
      {runs.data && list.length === 0 ? <Empty>No sync runs yet.</Empty> : null}
      {list.length > 0 ? (
        <table className="table">
          <thead>
            <tr>
              <th>Started</th>
              <th>Provider</th>
              <th>Status</th>
              <th>Records</th>
              <th>Changed</th>
              <th>Merges</th>
              <th>Removed</th>
              <th>Duration</th>
              <th aria-label="Actions" />
            </tr>
          </thead>
          <tbody>
            {list.map((r) => (
              <tr key={r.id}>
                <td>
                  {formatDate(r.startedAt)}
                  <div className="muted small">{r.trigger}</div>
                </td>
                <td>
                  <Mono>{r.provider}</Mono> <span className="muted small">{r.kind}</span>
                </td>
                <td>
                  <RunStatus run={r} />
                  {r.status === "SYNC_RUN_STATUS_RUNNING" ? <Progress run={r} /> : null}
                </td>
                <td>{count(r.upserts)}</td>
                <td>{count(r.changed)}</td>
                <td>{count(r.merges)}</td>
                <td>{count(String(Number(r.removals ?? 0) + Number(r.markedRemoved ?? 0)))}</td>
                <td>{runDuration(r)}</td>
                <td className="row-actions">
                  <button
                    type="button"
                    className="small"
                    onClick={() =>
                      void navigate({ to: "/providers", search: { provider: search.provider, run: r.id } })
                    }
                  >
                    Details
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : null}
      {runs.hasNextPage ? (
        <div className="pager">
          <button type="button" disabled={runs.isFetchingNextPage} onClick={() => runs.fetchNextPage()}>
            {runs.isFetchingNextPage ? "Loading…" : "Load more"}
          </button>
        </div>
      ) : null}
      {search.run ? (
        <RunDetails
          id={search.run}
          fromList={open}
          onClose={() => void navigate({ to: "/providers", search: { provider: search.provider } })}
        />
      ) : null}
    </section>
  );
}

function Progress({ run }: { run: SyncRun }) {
  const p = (run.progress ?? {}) as { phase?: string; done?: number; total?: number; unit?: string };
  if (!p.phase) return null;
  const pct = p.total ? Math.min(100, Math.round(((p.done ?? 0) / p.total) * 100)) : undefined;
  return (
    <div className="muted small">
      {p.phase}
      {pct !== undefined ? ` ${pct}%` : ""} {p.unit === "bytes" && p.done ? `(${Math.round(p.done / 1e6)} MB)` : ""}
    </div>
  );
}

function RunDetails({ id, fromList, onClose }: { id: string; fromList?: SyncRun; onClose: () => void }) {
  const detail = useQuery({
    queryKey: ["sync-run", id],
    queryFn: async () => (await unwrap(api.GET("/v1/admin/sync-runs/{id}", { params: { path: { id } } }))).run,
    initialData: fromList,
    refetchInterval: (query) => (query.state.data?.status === "SYNC_RUN_STATUS_RUNNING" ? 2000 : false),
  });
  const r = detail.data;
  return (
    <Dialog title="Sync run" onClose={onClose} wide>
      <ErrorBox error={detail.error} />
      {!r ? <Loading /> : null}
      {r ? (
        <>
          <dl className="details">
            <dt>Run</dt>
            <dd>
              <Mono>{r.id}</Mono>
            </dd>
            <dt>Provider</dt>
            <dd>
              <Mono>{r.provider}</Mono> {r.kind} · {r.trigger}
            </dd>
            <dt>Status</dt>
            <dd>
              <RunStatus run={r} /> <Progress run={r} />
            </dd>
            <dt>Started</dt>
            <dd>{formatDate(r.startedAt)}</dd>
            <dt>Finished</dt>
            <dd>
              {formatDate(r.finishedAt)} <span className="muted small">({runDuration(r)})</span>
            </dd>
            <dt>Snapshot</dt>
            <dd>{r.snapshotId ? <Mono>{r.snapshotId}</Mono> : "—"}</dd>
            <dt>Cursor</dt>
            <dd>
              <Mono>{r.cursorStart || "(start)"}</Mono> → <Mono>{r.cursorEnd || "—"}</Mono>
            </dd>
            <dt>Counts</dt>
            <dd>
              {count(r.upserts)} records, {count(r.changed)} changed, {count(r.merges)} merges, {count(r.removals)}{" "}
              removals, {count(r.skipped)} skipped, {count(r.markedRemoved)} marked removed
            </dd>
          </dl>
          {r.error ? (
            <div className="error-box" role="alert">
              {r.error}
            </div>
          ) : null}
          <h3>Notices</h3>
          {(r.notices ?? []).length === 0 ? <Empty>No notices.</Empty> : null}
          <ul className="plain-list">
            {(r.notices ?? []).map((n, i) => (
              <li key={i}>
                <Badge
                  tone={
                    n.level === "NOTICE_LEVEL_ERROR" ? "bad" : n.level === "NOTICE_LEVEL_WARNING" ? "warn" : "neutral"
                  }
                >
                  {enumLabel(n.level, "NOTICE_LEVEL_")}
                </Badge>{" "}
                {n.message} {n.ref ? <Mono>{n.ref}</Mono> : null}{" "}
                <span className="muted small">{formatDate(n.at)}</span>
              </li>
            ))}
          </ul>
        </>
      ) : null}
    </Dialog>
  );
}
