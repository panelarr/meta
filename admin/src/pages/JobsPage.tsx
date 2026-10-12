// SPDX-License-Identifier: GPL-3.0-or-later

import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { getRouteApi, useNavigate } from "@tanstack/react-router";
import { type FormEvent, useState } from "react";
import { api, unwrap } from "../api/client";
import { ApiError } from "../api/errors";
import type { Job, JobParam, JobProgress, JobRun, JobRunStatus, JobRunSummary } from "../api/types";
import { Dialog } from "../components/Dialog";
import { Badge, Empty, ErrorBox, Field, Loading, Mono, PageHeader, type Tone } from "../components/ui";
import { formatDuration } from "../lib/duration";
import { formatDate } from "../lib/format";

const route = getRouteApi("/_app/jobs");

export const jobRunTones: Record<JobRunStatus, [string, Tone]> = {
  JOB_RUN_STATUS_UNSPECIFIED: ["Unknown", "neutral"],
  JOB_RUN_STATUS_RUNNING: ["Running", "info"],
  JOB_RUN_STATUS_SUCCEEDED: ["Succeeded", "good"],
  JOB_RUN_STATUS_FAILED: ["Failed", "bad"],
  JOB_RUN_STATUS_CANCELLED: ["Cancelled", "warn"],
  JOB_RUN_STATUS_SKIPPED: ["Skipped", "neutral"],
};

const triggerLabels: Record<string, string> = {
  JOB_TRIGGER_SCHEDULE: "schedule",
  JOB_TRIGGER_MANUAL: "manual",
  JOB_TRIGGER_STARTUP: "startup",
};

const statusFilters: JobRunStatus[] = [
  "JOB_RUN_STATUS_RUNNING",
  "JOB_RUN_STATUS_SUCCEEDED",
  "JOB_RUN_STATUS_FAILED",
  "JOB_RUN_STATUS_CANCELLED",
  "JOB_RUN_STATUS_SKIPPED",
];

const running = (s: JobRunSummary | undefined) => s?.status === "JOB_RUN_STATUS_RUNNING";

function RunStatus({ run }: { run: JobRunSummary | undefined }) {
  const [label, tone] = jobRunTones[run?.status ?? "JOB_RUN_STATUS_UNSPECIFIED"];
  return <Badge tone={tone}>{label}</Badge>;
}

/** "1m 23s" between two timestamps, or since the start while running. */
export function jobDuration(run: JobRunSummary | undefined): string {
  if (!run?.startedAt) return "—";
  const start = new Date(run.startedAt).getTime();
  const end = run.finishedAt ? new Date(run.finishedAt).getTime() : Date.now();
  const seconds = Math.max(0, Math.round((end - start) / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

/** The schedule's fields; the generated type splits its oneof into a union. */
function schedule(job: Job): { cron?: string; interval?: string; enabled?: boolean; source?: string } {
  return (job.schedule ?? {}) as { cron?: string; interval?: string; enabled?: boolean; source?: string };
}

/** The schedule in words: the cron expression, "every 1h" or "manual only". */
export function scheduleLabel(job: Job): string {
  const s = schedule(job);
  if (s.cron) return s.enabled === false ? `${s.cron} (off)` : `${s.cron} UTC`;
  if (s.interval) return `every ${formatDuration(s.interval)}`;
  return "manual only";
}

/** "1,234 / 5,000 entries (25%)" or "download 12 MB". */
export function progressLabel(p: JobProgress | undefined): string {
  if (!p) return "";
  const done = Number(p.done ?? 0);
  const total = Number(p.total ?? 0);
  const unit = p.unit ?? "";
  const amount =
    unit === "bytes"
      ? `${Math.round(done / 1e6).toLocaleString()} MB`
      : `${done.toLocaleString()}${total ? ` / ${total.toLocaleString()}` : ""} ${unit}`.trim();
  const pct = total ? ` (${Math.min(100, Math.round((done / total) * 100))}%)` : "";
  return [p.phase, amount + pct].filter(Boolean).join(" · ");
}

function countsLabel(counts: Record<string, string> | undefined): string {
  const entries = Object.entries(counts ?? {}).filter(([, v]) => Number(v) !== 0);
  if (entries.length === 0) return "";
  return entries
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([k, v]) => `${Number(v).toLocaleString()} ${k.replaceAll("_", " ")}`)
    .join(", ");
}

export function JobsPage() {
  const search = route.useSearch();
  const navigate = useNavigate();
  const jobs = useQuery({
    queryKey: ["jobs"],
    queryFn: async () => (await unwrap(api.GET("/v1/admin/jobs"))).jobs ?? [],
    // Follow running jobs; otherwise refresh now and then for the next runs.
    refetchInterval: (query) => ((query.state.data ?? []).some((j) => j.running) ? 2000 : 30_000),
  });
  const [toRun, setToRun] = useState<Job | null>(null);
  const groups = new Map<string, Job[]>();
  for (const j of jobs.data ?? []) {
    const g = j.group || "Other";
    groups.set(g, [...(groups.get(g) ?? []), j]);
  }
  const selected = jobs.data?.find((j) => j.id === search.job);
  const openRun = (id: string | undefined, job?: string) =>
    void navigate({ to: "/jobs", search: { job: job ?? search.job, run: id } });

  return (
    <>
      <PageHeader title="Jobs" />
      <p className="muted">
        Background work meta does: provider syncs, rebuilds and refreshes. Schedules come from meta's configuration;
        "Run now" starts a run at once.
      </p>
      {jobs.isPending ? <Loading /> : null}
      <ErrorBox error={jobs.error} onRetry={() => jobs.refetch()} />
      {jobs.data?.length === 0 ? <Empty>No jobs.</Empty> : null}
      {[...groups.entries()].map(([group, list]) => (
        <section key={group} className="section" aria-label={group}>
          <header className="section-header">
            <h2>{group}</h2>
          </header>
          <table className="table jobs-table">
            <thead>
              <tr>
                <th>Job</th>
                <th>Schedule</th>
                <th>Last run</th>
                <th>Next run</th>
                <th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {list.map((j) => (
                <JobRow
                  key={j.id}
                  job={j}
                  onRun={() => setToRun(j)}
                  onHistory={() => void navigate({ to: "/jobs", search: { job: j.id } })}
                  onOpenRun={(id) => openRun(id, j.id)}
                />
              ))}
            </tbody>
          </table>
        </section>
      ))}
      {selected ? <RunsSection job={selected} onOpen={(id) => openRun(id)} /> : null}
      {toRun ? (
        <RunJobDialog job={toRun} onClose={() => setToRun(null)} onStarted={(id) => openRun(id, toRun.id)} />
      ) : null}
      {search.run ? (
        <RunDetails
          id={search.run}
          job={jobs.data?.find((j) => j.id === search.job)}
          onClose={() => void navigate({ to: "/jobs", search: { job: search.job } })}
        />
      ) : null}
    </>
  );
}

function JobRow({
  job: j,
  onRun,
  onHistory,
  onOpenRun,
}: {
  job: Job;
  onRun: () => void;
  onHistory: () => void;
  onOpenRun: (id: string) => void;
}) {
  const last = j.lastRun;
  return (
    <tr aria-label={`Job ${j.name}`}>
      <td>
        <strong>{j.name}</strong> <Mono>{j.id}</Mono>
        {j.description ? <div className="muted small">{j.description}</div> : null}
      </td>
      <td>
        <span className={scheduleLabel(j) === "manual only" ? "muted" : undefined}>{scheduleLabel(j)}</span>
        {schedule(j).source ? <div className="muted small">{schedule(j).source}</div> : null}
      </td>
      <td>
        {j.running ? (
          <button type="button" className="link-button" onClick={() => onOpenRun(j.currentRunId ?? "")}>
            <Badge tone="info">Running</Badge>
          </button>
        ) : last ? (
          <button type="button" className="link-button" onClick={() => onOpenRun(last.id ?? "")}>
            <RunStatus run={last} />
          </button>
        ) : (
          <span className="muted">never</span>
        )}
        {last && !j.running ? (
          <div className="muted small">
            {formatDate(last.startedAt)} · {jobDuration(last)}
            {last.message ? <div className="job-message">{last.message}</div> : null}
          </div>
        ) : null}
      </td>
      <td>{j.nextRunAt ? formatDate(j.nextRunAt) : <span className="muted">—</span>}</td>
      <td>
        <div className="job-actions">
          <button
            type="button"
            className="small primary"
            disabled={j.running || !!j.disabledReason}
            title={j.disabledReason || undefined}
            onClick={onRun}
          >
            Run now
          </button>
          <button type="button" className="small" onClick={onHistory}>
            History
          </button>
        </div>
        {j.disabledReason ? <div className="muted small">{j.disabledReason}</div> : null}
      </td>
    </tr>
  );
}

/** Asks for the job's params, if any, and starts a run. */
function RunJobDialog({ job, onClose, onStarted }: { job: Job; onClose: () => void; onStarted: (id: string) => void }) {
  const queryClient = useQueryClient();
  const params = job.params ?? [];
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(params.map((p) => [p.name ?? "", p.default ?? ""])),
  );
  const [busyRun, setBusyRun] = useState<string | undefined>();
  const run = useMutation({
    mutationFn: async () => {
      const sent = Object.fromEntries(Object.entries(values).filter(([, v]) => v !== ""));
      try {
        return await unwrap(
          api.POST("/v1/admin/jobs/{jobId}:run", {
            params: { path: { jobId: job.id ?? "" } },
            body: { params: sent },
          }),
        );
      } catch (error) {
        if (error instanceof ApiError && (error.status === 409 || error.grpcCode === "ALREADY_EXISTS")) {
          const fresh = await unwrap(api.GET("/v1/admin/jobs/{jobId}", { params: { path: { jobId: job.id ?? "" } } }));
          setBusyRun(fresh.job?.currentRunId);
          throw new Error(`${job.name} is already running. Wait for it to finish, or cancel it.`);
        }
        throw error;
      }
    },
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({ queryKey: ["jobs"] });
      await queryClient.invalidateQueries({ queryKey: ["job-runs", job.id] });
      onClose();
      onStarted(res.run?.id ?? "");
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    run.mutate();
  };
  return (
    <Dialog title={`Run ${job.name}?`} onClose={onClose}>
      <form onSubmit={submit}>
        <p>The run starts at once and goes on in the background.</p>
        {params.map((p) => (
          <ParamField
            key={p.name}
            param={p}
            value={values[p.name ?? ""] ?? ""}
            onChange={(v) => setValues((old) => ({ ...old, [p.name ?? ""]: v }))}
          />
        ))}
        <ErrorBox error={run.error} />
        {busyRun ? (
          <p>
            <button
              type="button"
              className="link-button"
              onClick={() => {
                onClose();
                onStarted(busyRun);
              }}
            >
              Open the current run
            </button>
          </p>
        ) : null}
        <div className="actions">
          <button type="submit" className="primary" disabled={run.isPending}>
            {run.isPending ? "Starting…" : "Run now"}
          </button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </Dialog>
  );
}

function ParamField({ param: p, value, onChange }: { param: JobParam; value: string; onChange: (v: string) => void }) {
  const choices = p.choices ?? [];
  if (choices.length > 0 && p.type === "string_list") {
    const picked = value.split(",").filter(Boolean);
    return (
      <fieldset className="choices">
        <legend>
          <Mono>{p.name}</Mono> <span className="muted small">{p.description}</span>
        </legend>
        {choices.map((c) => (
          <label key={c} className="filter-check">
            <input
              type="checkbox"
              checked={picked.includes(c)}
              onChange={(e) => onChange((e.target.checked ? [...picked, c] : picked.filter((x) => x !== c)).join(","))}
            />
            <span>{c}</span>
          </label>
        ))}
      </fieldset>
    );
  }
  if (choices.length > 0) {
    return (
      <Field label={p.name} hint={p.description}>
        <select value={value} required={p.required} onChange={(e) => onChange(e.target.value)}>
          {p.required ? null : <option value="">—</option>}
          {choices.map((c) => (
            <option key={c} value={c}>
              {c}
            </option>
          ))}
        </select>
      </Field>
    );
  }
  if (p.type === "bool") {
    return (
      <label className="filter-check">
        <input type="checkbox" checked={value === "true"} onChange={(e) => onChange(String(e.target.checked))} />
        <span>
          <Mono>{p.name}</Mono> {p.description}
        </span>
      </label>
    );
  }
  return (
    <Field
      label={p.name}
      hint={[p.description, p.type === "string_list" ? "Separate values with commas." : ""].filter(Boolean).join(" ")}
    >
      <input
        type={p.type === "int" ? "number" : "text"}
        required={p.required}
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
    </Field>
  );
}

function RunsSection({ job, onOpen }: { job: Job; onOpen: (id: string) => void }) {
  const [status, setStatus] = useState<JobRunStatus | "">("");
  const runs = useInfiniteQuery({
    queryKey: ["job-runs", job.id, status],
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET("/v1/admin/jobs/{jobId}/runs", {
          params: {
            path: { jobId: job.id ?? "" },
            query: { pageSize: 20, pageToken: pageParam, status: status || undefined },
          },
        }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextPageToken || undefined,
    refetchInterval: (query) =>
      job.running || query.state.data?.pages.some((p) => (p.runs ?? []).some(running)) ? 2000 : false,
  });
  const list = (runs.data?.pages ?? []).flatMap((p) => p.runs ?? []);
  return (
    <section className="section" aria-label={`Runs of ${job.name}`}>
      <header className="section-header">
        <h2>Runs of {job.name}</h2>
        <label className="inline-field">
          <span>Status</span>
          <select value={status} onChange={(e) => setStatus(e.target.value as JobRunStatus | "")}>
            <option value="">Any</option>
            {statusFilters.map((s) => (
              <option key={s} value={s}>
                {jobRunTones[s][0]}
              </option>
            ))}
          </select>
        </label>
      </header>
      {runs.isPending ? <Loading /> : null}
      <ErrorBox error={runs.error} onRetry={() => runs.refetch()} />
      {runs.data && list.length === 0 ? <Empty>No runs.</Empty> : null}
      {list.length > 0 ? (
        <table className="table">
          <thead>
            <tr>
              <th>Started</th>
              <th>Status</th>
              <th>Outcome</th>
              <th>Duration</th>
              <th aria-label="Actions" />
            </tr>
          </thead>
          <tbody>
            {list.map((r) => (
              <tr key={r.id}>
                <td>
                  {formatDate(r.startedAt)}
                  <div className="muted small">
                    {triggerLabels[r.trigger ?? ""] ?? "—"}
                    {r.triggeredBy ? ` by ${r.triggeredBy}` : ""}
                  </div>
                </td>
                <td>
                  <RunStatus run={r} />
                </td>
                <td>
                  <div className="job-message">{r.message || <span className="muted">—</span>}</div>
                  {countsLabel(r.counts) ? <div className="muted small">{countsLabel(r.counts)}</div> : null}
                </td>
                <td>{jobDuration(r)}</td>
                <td className="row-actions">
                  <button type="button" className="small" onClick={() => onOpen(r.id ?? "")}>
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
    </section>
  );
}

function RunDetails({ id, job, onClose }: { id: string; job: Job | undefined; onClose: () => void }) {
  const queryClient = useQueryClient();
  const detail = useQuery({
    queryKey: ["job-run", id],
    queryFn: async () =>
      (await unwrap(api.GET("/v1/admin/job-runs/{runId}", { params: { path: { runId: id } } }))).run as JobRun,
    refetchInterval: (query) => (running(query.state.data?.summary) ? 2000 : false),
  });
  const cancel = useMutation({
    mutationFn: () => unwrap(api.POST("/v1/admin/job-runs/{runId}:cancel", { params: { path: { runId: id } } })),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["job-run", id] });
      await queryClient.invalidateQueries({ queryKey: ["jobs"] });
    },
  });
  const r = detail.data;
  const s = r?.summary;
  const params = Object.entries(r?.params ?? {});
  return (
    <Dialog title={job ? `Run of ${job.name}` : "Job run"} onClose={onClose} wide>
      <ErrorBox error={detail.error} />
      {detail.isPending ? <Loading /> : null}
      {s ? (
        <>
          <dl className="details">
            <dt>Run</dt>
            <dd>
              <Mono>{s.id}</Mono>
            </dd>
            <dt>Job</dt>
            <dd>
              <Mono>{s.jobId}</Mono>
            </dd>
            <dt>Status</dt>
            <dd>
              <RunStatus run={s} />{" "}
              {r?.progress ? <span className="muted small">{progressLabel(r.progress)}</span> : null}
            </dd>
            <dt>Trigger</dt>
            <dd>
              {triggerLabels[s.trigger ?? ""] ?? "—"}
              {s.triggeredBy ? ` by ${s.triggeredBy}` : ""}
            </dd>
            <dt>Started</dt>
            <dd>{formatDate(s.startedAt)}</dd>
            <dt>Finished</dt>
            <dd>
              {formatDate(s.finishedAt)} <span className="muted small">({jobDuration(s)})</span>
            </dd>
            <dt>Parameters</dt>
            <dd>
              {params.length === 0 ? <span className="muted">none</span> : null}
              {params.map(([k, v]) => (
                <div key={k}>
                  <Mono>{k}</Mono> = <Mono>{v}</Mono>
                </div>
              ))}
            </dd>
            <dt>Counts</dt>
            <dd>{countsLabel(s.counts) || <span className="muted">—</span>}</dd>
          </dl>
          {s.message ? (
            <div
              className={s.status === "JOB_RUN_STATUS_FAILED" ? "error-box" : "notice-box"}
              role={s.status === "JOB_RUN_STATUS_FAILED" ? "alert" : undefined}
            >
              {s.message}
            </div>
          ) : null}
          {running(s) && job?.cancellable ? (
            <div className="actions">
              <button type="button" className="danger" disabled={cancel.isPending} onClick={() => cancel.mutate()}>
                {cancel.isPending ? "Cancelling…" : "Cancel run"}
              </button>
            </div>
          ) : null}
          <ErrorBox error={cancel.error} />
          <h3>Notes</h3>
          {(r?.notes ?? []).length === 0 ? <Empty>No notes.</Empty> : null}
          <ul className="plain-list">
            {(r?.notes ?? []).map((n, i) => (
              <li key={i}>
                <Badge tone={n.level === "error" ? "bad" : n.level === "warning" ? "warn" : "neutral"}>
                  {n.level ?? "info"}
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
