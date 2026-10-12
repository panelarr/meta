// SPDX-License-Identifier: GPL-3.0-or-later

import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { type FormEvent, useMemo, useState } from "react";
import { api, unwrap } from "../api/client";
import type {
  Assignment,
  NetworkProfile,
  ProfileOrigin,
  SearchEngineSettings,
  Solver,
  SolverType,
  UserAgentMode,
} from "../api/types";
import { ConfirmDialog, Dialog } from "../components/Dialog";
import { Badge, Empty, ErrorBox, Field, Loading, Mono, PageHeader, type Tone } from "../components/ui";
import { durationInput, formatDuration, secondsToDuration } from "../lib/duration";
import { enumLabel, formatDate } from "../lib/format";
import { formToProfile, idPattern, type ProfileForm, profileToForm } from "../lib/profileForm";

const solversQuery = () =>
  queryOptions({
    queryKey: ["solvers"],
    queryFn: async () => (await unwrap(api.GET("/v1/admin/solvers"))).solvers ?? [],
  });

const profilesQuery = () =>
  queryOptions({
    queryKey: ["profiles"],
    queryFn: async () => {
      const response = await unwrap(api.GET("/v1/admin/profiles"));
      return { profiles: response.profiles ?? [], defaultProfileId: response.defaultProfileId ?? "" };
    },
  });

const assignmentsQuery = () =>
  queryOptions({
    queryKey: ["assignments"],
    queryFn: async () => {
      const response = await unwrap(api.GET("/v1/admin/assignments"));
      return { assignments: response.assignments ?? [], defaultProfileId: response.defaultProfileId ?? "" };
    },
  });

/** Profiles, the default and assignments all feed the effective settings of each source. */
function useInvalidateNetwork() {
  const queryClient = useQueryClient();
  return () =>
    Promise.all(
      [["solvers"], ["profiles"], ["assignments"]].map((queryKey) => queryClient.invalidateQueries({ queryKey })),
    );
}

export function IntegrationsPage() {
  return (
    <>
      <PageHeader title="Integrations" />
      <p className="muted">
        Plugins that support network profiles send their requests through one. A provider uses, in order: a profile
        pushed by a host, the profile assigned to it here, the default profile, or a direct connection. Below them, the
        search engine meta answers searches with.
      </p>
      <SolversSection />
      <ProfilesSection />
      <AssignmentsSection />
      <SearchEngineSection />
    </>
  );
}

// ---------------------------------------------------------------------------
// Solvers
// ---------------------------------------------------------------------------

const solverTypes: SolverType[] = ["SOLVER_TYPE_FLARESOLVERR", "SOLVER_TYPE_BYPARR"];

function SolversSection() {
  const solvers = useQuery(solversQuery());
  const profiles = useQuery(profilesQuery());
  const [editing, setEditing] = useState<Solver | "new" | null>(null);
  const [deleting, setDeleting] = useState<Solver | null>(null);
  // meta reports which profiles use each solver.
  const unused = (solver: Solver) => (solver.usedBy ?? []).length === 0;

  return (
    <section className="section">
      <header className="section-header">
        <h2>Challenge solvers</h2>
        <button type="button" onClick={() => setEditing("new")}>
          Add solver
        </button>
      </header>
      <p className="muted">
        A solver is only used through a network profile: choose it in a profile, then make that profile the default or
        assign it to providers. Adding the first solver while there are no profiles creates a default profile that uses
        it.
      </p>
      {solvers.isPending ? <Loading /> : null}
      <ErrorBox error={solvers.error} onRetry={() => solvers.refetch()} />
      {solvers.data?.length === 0 ? <Empty>No solvers.</Empty> : null}
      {solvers.data && solvers.data.length > 0 ? (
        <table className="table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Type</th>
              <th>URL</th>
              <th>Session TTL</th>
              <th>Max timeout</th>
              <th>Response fallback</th>
              <th aria-label="Actions" />
            </tr>
          </thead>
          <tbody>
            {solvers.data.map((solver) => (
              <tr key={solver.id}>
                <td>
                  {solver.name || solver.id}
                  <div>
                    <Mono>{solver.id}</Mono>
                  </div>
                  {unused(solver) ? (
                    <div>
                      <Badge tone="warn" title="No network profile uses this solver, so no provider uses it.">
                        Not used by any profile
                      </Badge>
                    </div>
                  ) : null}
                </td>
                <td>{enumLabel(solver.type, "SOLVER_TYPE_")}</td>
                <td className="wrap">{solver.url}</td>
                <td>{formatDuration(solver.sessionTtl)}</td>
                <td>{formatDuration(solver.maxTimeout)}</td>
                <td>{solver.asResponseFallback ? "Yes" : "No"}</td>
                <td className="row-actions">
                  {profiles.data && unused(solver) ? (
                    <UseForAllProvidersButton
                      solver={solver}
                      profiles={profiles.data.profiles}
                      defaultProfileId={profiles.data.defaultProfileId}
                    />
                  ) : null}
                  <button type="button" className="small" onClick={() => setEditing(solver)}>
                    Edit
                  </button>
                  <button type="button" className="small danger" onClick={() => setDeleting(solver)}>
                    Delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : null}
      {editing ? (
        <SolverDialog solver={editing === "new" ? undefined : editing} onClose={() => setEditing(null)} />
      ) : null}
      {deleting ? <DeleteSolverDialog solver={deleting} onClose={() => setDeleting(null)} /> : null}
    </section>
  );
}

/**
 * Makes every provider without an assignment use [solver]: the default profile gets the
 * solver, or a default profile is created. Plain API calls, like everything else here.
 */
function UseForAllProvidersButton({
  solver,
  profiles,
  defaultProfileId,
}: {
  solver: Solver;
  profiles: NetworkProfile[];
  defaultProfileId: string;
}) {
  const invalidate = useInvalidateNetwork();
  const use = useMutation({
    mutationFn: async () => {
      const existing = profiles.find((profile) => profile.id === defaultProfileId);
      if (existing?.id) {
        await unwrap(
          api.PUT("/v1/admin/profiles/{profile.id}", {
            params: { path: { "profile.id": existing.id } },
            body: { ...existing, solverId: solver.id },
          }),
        );
        return;
      }
      const taken = new Set(profiles.map((profile) => profile.id));
      let id = "default";
      for (let n = 2; taken.has(id); n++) id = `default-${n}`;
      await unwrap(
        api.PUT("/v1/admin/profiles/{profile.id}", {
          params: { path: { "profile.id": id } },
          body: { id, name: "Default", solverId: solver.id, userAgentMode: "USER_AGENT_MODE_FROM_SOLVER" },
        }),
      );
      await unwrap(api.PUT("/v1/admin/default-profile", { body: { profileId: id } }));
    },
    onSettled: invalidate,
  });
  return (
    <>
      <button
        type="button"
        className="small"
        disabled={use.isPending}
        title={
          defaultProfileId
            ? "Set this solver on the default profile"
            : "Create a default profile with this solver, used by every provider without an assignment"
        }
        onClick={() => use.mutate()}
      >
        Use for all providers
      </button>
      <ErrorBox error={use.error} />
    </>
  );
}

function SolverDialog({ solver, onClose }: { solver?: Solver; onClose: () => void }) {
  const invalidate = useInvalidateNetwork();
  const [id, setId] = useState(solver?.id ?? "");
  const [name, setName] = useState(solver?.name ?? "");
  const [type, setType] = useState<SolverType>(
    solver?.type && solver.type !== "SOLVER_TYPE_UNSPECIFIED" ? solver.type : "SOLVER_TYPE_FLARESOLVERR",
  );
  const [url, setUrl] = useState(solver?.url ?? "");
  const [sessionTtl, setSessionTtl] = useState(durationInput(solver?.sessionTtl));
  const [maxTimeout, setMaxTimeout] = useState(durationInput(solver?.maxTimeout));
  const [fallback, setFallback] = useState(solver?.asResponseFallback ?? false);

  const save = useMutation({
    mutationFn: () => {
      const solverId = id.trim();
      if (!idPattern.test(solverId)) throw new Error("The ID may only use lowercase letters, digits and dashes.");
      const body: Solver = { id: solverId, name: name.trim(), type, url: url.trim(), asResponseFallback: fallback };
      const ttl = secondsToDuration(sessionTtl, "Session TTL");
      const max = secondsToDuration(maxTimeout, "Max timeout");
      if (ttl) body.sessionTtl = ttl;
      if (max) body.maxTimeout = max;
      return unwrap(api.PUT("/v1/admin/solvers/{solver.id}", { params: { path: { "solver.id": solverId } }, body }));
    },
    onSuccess: async () => {
      await invalidate();
      onClose();
    },
  });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    save.mutate();
  };

  return (
    <Dialog title={solver ? `Edit solver ${solver.name || solver.id}` : "Add solver"} onClose={onClose}>
      <form className="form-grid two" onSubmit={submit}>
        <Field label="ID" hint="Lowercase letters, digits and dashes. Can't be changed later.">
          <input type="text" required value={id} readOnly={!!solver} onChange={(event) => setId(event.target.value)} />
        </Field>
        <Field label="Name">
          <input type="text" value={name} onChange={(event) => setName(event.target.value)} />
        </Field>
        <Field label="Type">
          <select value={type} onChange={(event) => setType(event.target.value as SolverType)}>
            {solverTypes.map((option) => (
              <option key={option} value={option}>
                {enumLabel(option, "SOLVER_TYPE_")}
              </option>
            ))}
          </select>
        </Field>
        <Field label="URL" hint="Base URL of the solver.">
          <input
            type="url"
            required
            value={url}
            placeholder="http://solver.example:8191"
            onChange={(event) => setUrl(event.target.value)}
          />
        </Field>
        <Field label="Session TTL (seconds)" hint="How long clearance cookies from one solve are reused.">
          <input type="number" min="0" step="any" value={sessionTtl} onChange={(e) => setSessionTtl(e.target.value)} />
        </Field>
        <Field label="Max timeout (seconds)" hint="Upper bound for one solve.">
          <input type="number" min="0" step="any" value={maxTimeout} onChange={(e) => setMaxTimeout(e.target.value)} />
        </Field>
        <label className="filter-check span-2">
          <input type="checkbox" checked={fallback} onChange={(event) => setFallback(event.target.checked)} />
          <span>Use the solver's own response when a fresh request still fails</span>
        </label>
        <div className="span-2">
          <ErrorBox error={save.error} />
        </div>
        <div className="actions span-2">
          <button type="submit" className="primary" disabled={save.isPending}>
            {save.isPending ? "Saving…" : "Save"}
          </button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </Dialog>
  );
}

function DeleteSolverDialog({ solver, onClose }: { solver: Solver; onClose: () => void }) {
  const invalidate = useInvalidateNetwork();
  const remove = useMutation({
    mutationFn: () =>
      unwrap(api.DELETE("/v1/admin/solvers/{solverId}", { params: { path: { solverId: solver.id ?? "" } } })),
    onSuccess: async () => {
      await invalidate();
      onClose();
    },
  });
  return (
    <ConfirmDialog
      title={`Delete solver ${solver.name || solver.id}?`}
      confirmLabel="Delete solver"
      pending={remove.isPending}
      error={remove.error}
      onConfirm={() => remove.mutate()}
      onClose={onClose}
    >
      <p>This fails while a network profile still uses it.</p>
    </ConfirmDialog>
  );
}

// ---------------------------------------------------------------------------
// Network profiles
// ---------------------------------------------------------------------------

function ProfilesSection() {
  const profiles = useQuery(profilesQuery());
  const solvers = useQuery(solversQuery());
  const [editing, setEditing] = useState<NetworkProfile | "new" | null>(null);
  const [deleting, setDeleting] = useState<NetworkProfile | null>(null);

  return (
    <section className="section">
      <header className="section-header">
        <h2>Network profiles</h2>
        <button type="button" onClick={() => setEditing("new")}>
          Add profile
        </button>
      </header>
      {profiles.isPending ? <Loading /> : null}
      <ErrorBox error={profiles.error} onRetry={() => profiles.refetch()} />
      {profiles.data ? (
        <DefaultProfile profiles={profiles.data.profiles} current={profiles.data.defaultProfileId} />
      ) : null}
      {profiles.data?.profiles.length === 0 ? <Empty>No profiles: every provider connects directly.</Empty> : null}
      {profiles.data && profiles.data.profiles.length > 0 ? (
        <table className="table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Proxy</th>
              <th>Solver</th>
              <th>Rate limit</th>
              <th>Concurrency</th>
              <th>User agent</th>
              <th aria-label="Actions" />
            </tr>
          </thead>
          <tbody>
            {profiles.data.profiles.map((profile) => (
              <tr key={profile.id}>
                <td>
                  {profile.name || profile.id}{" "}
                  {profile.id === profiles.data.defaultProfileId ? <Badge tone="info">Default</Badge> : null}
                  <div>
                    <Mono>{profile.id}</Mono>
                  </div>
                </td>
                <td className="wrap">
                  {profile.proxy?.url ? (
                    <>
                      {profile.proxy.url}
                      {profile.proxy.username ? <div className="muted small">user {profile.proxy.username}</div> : null}
                      {profile.proxy.hasPassword ? <div className="muted small">password stored</div> : null}
                    </>
                  ) : (
                    <span className="muted">direct</span>
                  )}
                </td>
                <td>{profile.solverId || <span className="muted">none</span>}</td>
                <td>
                  {profile.rateLimit?.requests ? (
                    <>
                      {profile.rateLimit.requests} / {formatDuration(profile.rateLimit.period)}
                      {profile.rateLimit.perHost ? <span className="muted small"> per host</span> : null}
                    </>
                  ) : (
                    <span className="muted">none</span>
                  )}
                </td>
                <td>{profile.concurrency || <span className="muted">no limit</span>}</td>
                <td>
                  {profile.userAgentMode === "USER_AGENT_MODE_FIXED"
                    ? profile.userAgent
                    : enumLabel(profile.userAgentMode ?? "USER_AGENT_MODE_UNSPECIFIED", "USER_AGENT_MODE_").replace(
                        "Unspecified",
                        "Plugin default",
                      )}
                </td>
                <td className="row-actions">
                  <button type="button" className="small" onClick={() => setEditing(profile)}>
                    Edit
                  </button>
                  <button type="button" className="small danger" onClick={() => setDeleting(profile)}>
                    Delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : null}
      {editing ? (
        <ProfileDialog
          profile={editing === "new" ? undefined : editing}
          solvers={solvers.data ?? []}
          onClose={() => setEditing(null)}
        />
      ) : null}
      {deleting ? <DeleteProfileDialog profile={deleting} onClose={() => setDeleting(null)} /> : null}
    </section>
  );
}

function DefaultProfile({ profiles, current }: { profiles: NetworkProfile[]; current: string }) {
  const invalidate = useInvalidateNetwork();
  const setDefault = useMutation({
    mutationFn: (profileId: string) => unwrap(api.PUT("/v1/admin/default-profile", { body: { profileId } })),
    onSuccess: invalidate,
  });
  return (
    <div className="toolbar">
      <Field label="Default profile" hint="Used by every provider without an assignment.">
        <select
          value={current}
          disabled={setDefault.isPending}
          onChange={(event) => setDefault.mutate(event.target.value)}
        >
          <option value="">None (direct connection)</option>
          {profiles.map((profile) => (
            <option key={profile.id} value={profile.id}>
              {profile.name || profile.id}
            </option>
          ))}
        </select>
      </Field>
      <ErrorBox error={setDefault.error} title="Default profile" />
    </div>
  );
}

const userAgentModes: { mode: UserAgentMode; label: string }[] = [
  { mode: "USER_AGENT_MODE_UNSPECIFIED", label: "Plugin default" },
  { mode: "USER_AGENT_MODE_FIXED", label: "Fixed value" },
  { mode: "USER_AGENT_MODE_RANDOM_DESKTOP", label: "Random desktop browser" },
  { mode: "USER_AGENT_MODE_FROM_SOLVER", label: "From the solver" },
];

function ProfileDialog({
  profile,
  solvers,
  onClose,
}: {
  profile?: NetworkProfile;
  solvers: Solver[];
  onClose: () => void;
}) {
  const invalidate = useInvalidateNetwork();
  const [form, setForm] = useState<ProfileForm>(() => profileToForm(profile));
  const set = <K extends keyof ProfileForm>(key: K, value: ProfileForm[K]) =>
    setForm((prev) => ({ ...prev, [key]: value }));
  const text = (key: keyof ProfileForm) => ({
    value: String(form[key]),
    onChange: (event: { target: { value: string } }) => set(key, event.target.value as never),
  });

  const save = useMutation({
    mutationFn: () => {
      const body = formToProfile(form);
      return unwrap(
        api.PUT("/v1/admin/profiles/{profile.id}", { params: { path: { "profile.id": body.id ?? "" } }, body }),
      );
    },
    onSuccess: async () => {
      await invalidate();
      onClose();
    },
  });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    save.mutate();
  };

  return (
    <Dialog
      title={profile ? `Edit profile ${profile.name || profile.id}` : "Add network profile"}
      onClose={onClose}
      wide
    >
      <form className="form-grid two" onSubmit={submit}>
        <Field label="ID" hint="Lowercase letters, digits and dashes. Can't be changed later.">
          <input type="text" required readOnly={!!profile} {...text("id")} />
        </Field>
        <Field label="Name">
          <input type="text" {...text("name")} />
        </Field>

        <h3 className="span-2 small-heading">Proxy</h3>
        <Field label="Proxy URL" hint="http://, https:// or socks5://, without credentials. Empty means direct.">
          <input type="text" placeholder="socks5://proxy.example:1080" {...text("proxyUrl")} />
        </Field>
        <Field label="Username">
          <input type="text" autoComplete="off" {...text("proxyUsername")} />
        </Field>
        <Field
          label="Password"
          hint={form.hasPassword ? "A password is stored. Leave empty to keep it." : "Write-only: never shown again."}
        >
          <input
            type="password"
            autoComplete="new-password"
            placeholder={form.hasPassword ? "Leave empty to keep" : ""}
            {...text("proxyPassword")}
          />
        </Field>
        <Field label="Solver">
          <select value={form.solverId} onChange={(event) => set("solverId", event.target.value)}>
            <option value="">None</option>
            {solvers.map((solver) => (
              <option key={solver.id} value={solver.id}>
                {solver.name || solver.id}
              </option>
            ))}
          </select>
        </Field>

        <h3 className="span-2 small-heading">Limits</h3>
        <Field label="Rate limit: requests" hint="Empty means no rate limit.">
          <input type="number" min="0" step="1" {...text("rateRequests")} />
        </Field>
        <Field label="per period (seconds)">
          <input type="number" min="0" step="any" {...text("ratePeriod")} />
        </Field>
        <label className="filter-check">
          <input
            type="checkbox"
            checked={form.ratePerHost}
            onChange={(event) => set("ratePerHost", event.target.checked)}
          />
          <span>Count per target host instead of per provider</span>
        </label>
        <Field label="Concurrency" hint="Parallel requests per provider. Empty means no limit.">
          <input type="number" min="0" step="1" {...text("concurrency")} />
        </Field>

        <h3 className="span-2 small-heading">Requests</h3>
        <Field label="User agent">
          <select
            value={form.userAgentMode}
            onChange={(event) => set("userAgentMode", event.target.value as UserAgentMode)}
          >
            {userAgentModes.map(({ mode, label }) => (
              <option key={mode} value={mode}>
                {label}
              </option>
            ))}
          </select>
        </Field>
        {form.userAgentMode === "USER_AGENT_MODE_FIXED" ? (
          <Field label="User agent value">
            <input type="text" {...text("userAgent")} />
          </Field>
        ) : (
          <span />
        )}
        <Field label="Extra headers" hint='One per line: "Name: value".'>
          <textarea className="mono" rows={3} {...text("headers")} />
        </Field>
        <Field
          label="DNS over HTTPS"
          hint="An https:// resolver, e.g. https://cloudflare-dns.com/dns-query. Empty: the system resolver. Not used with a proxy, which resolves names itself."
        >
          <input type="text" placeholder="https://cloudflare-dns.com/dns-query" {...text("dohUrl")} />
        </Field>

        <h3 className="span-2 small-heading">Timeouts (seconds)</h3>
        <Field label="Connect">
          <input type="number" min="0" step="any" {...text("connectTimeout")} />
        </Field>
        <Field label="Read">
          <input type="number" min="0" step="any" {...text("readTimeout")} />
        </Field>
        <Field label="Whole call" hint="Including redirects and retries.">
          <input type="number" min="0" step="any" {...text("callTimeout")} />
        </Field>

        <div className="span-2">
          <ErrorBox error={save.error} />
        </div>
        <div className="actions span-2">
          <button type="submit" className="primary" disabled={save.isPending}>
            {save.isPending ? "Saving…" : "Save"}
          </button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </Dialog>
  );
}

function DeleteProfileDialog({ profile, onClose }: { profile: NetworkProfile; onClose: () => void }) {
  const invalidate = useInvalidateNetwork();
  const remove = useMutation({
    mutationFn: () =>
      unwrap(api.DELETE("/v1/admin/profiles/{profileId}", { params: { path: { profileId: profile.id ?? "" } } })),
    onSuccess: async () => {
      await invalidate();
      onClose();
    },
  });
  return (
    <ConfirmDialog
      title={`Delete profile ${profile.name || profile.id}?`}
      confirmLabel="Delete profile"
      pending={remove.isPending}
      error={remove.error}
      onConfirm={() => remove.mutate()}
      onClose={onClose}
    >
      <p>Its assignments are removed too; those providers fall back to the default profile.</p>
    </ConfirmDialog>
  );
}

// ---------------------------------------------------------------------------
// Assignments
// ---------------------------------------------------------------------------

const originTones: Record<ProfileOrigin, [string, Tone]> = {
  PROFILE_ORIGIN_UNSPECIFIED: ["Unknown", "neutral"],
  PROFILE_ORIGIN_NONE: ["None (direct)", "neutral"],
  PROFILE_ORIGIN_DEFAULT: ["Default", "info"],
  PROFILE_ORIGIN_ASSIGNED: ["Assigned", "good"],
  PROFILE_ORIGIN_PUSHED: ["Pushed by host", "warn"],
};

function AssignmentsSection() {
  const assignments = useQuery(assignmentsQuery());
  const profiles = useQuery(profilesQuery());
  const profileNames = useMemo(
    () =>
      new Map((profiles.data?.profiles ?? []).map((profile) => [profile.id ?? "", profile.name || profile.id || ""])),
    [profiles.data],
  );
  const rows = [...(assignments.data?.assignments ?? [])].sort((a, b) =>
    (a.providerId ?? "").localeCompare(b.providerId ?? ""),
  );

  return (
    <section className="section">
      <header className="section-header">
        <h2>Provider assignments</h2>
      </header>
      {assignments.isPending ? <Loading /> : null}
      <ErrorBox error={assignments.error} onRetry={() => assignments.refetch()} />
      {assignments.data?.assignments.length === 0 ? <Empty>No providers configured.</Empty> : null}
      {rows.length > 0 ? (
        <table className="table">
          <thead>
            <tr>
              <th>Provider</th>
              <th>Assigned profile</th>
              <th>In effect</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((assignment) => (
              <AssignmentRow
                key={assignment.providerId}
                assignment={assignment}
                profiles={profiles.data?.profiles ?? []}
                profileNames={profileNames}
              />
            ))}
          </tbody>
        </table>
      ) : null}
    </section>
  );
}

function AssignmentRow({
  assignment,
  profiles,
  profileNames,
}: {
  assignment: Assignment;
  profiles: NetworkProfile[];
  profileNames: Map<string, string>;
}) {
  const invalidate = useInvalidateNetwork();
  const providerId = assignment.providerId ?? "";
  const assign = useMutation({
    mutationFn: (profileId: string) =>
      unwrap(api.PUT("/v1/admin/assignments/{providerId}", { params: { path: { providerId } }, body: { profileId } })),
    onSuccess: invalidate,
  });
  const origin = assignment.effective ?? "PROFILE_ORIGIN_UNSPECIFIED";
  const [label, tone] = originTones[origin];
  const effectiveName =
    origin === "PROFILE_ORIGIN_PUSHED"
      ? assignment.pushedProfileName
      : assignment.effectiveProfileId
        ? (profileNames.get(assignment.effectiveProfileId) ?? assignment.effectiveProfileId)
        : undefined;

  return (
    <tr>
      <td>
        <Mono>{providerId}</Mono>
        {!assignment.supported ? (
          <div className="muted small">This plugin doesn't use network profiles; a profile has no effect.</div>
        ) : null}
      </td>
      <td>
        <select
          aria-label={`Profile for ${providerId}`}
          value={assignment.profileId ?? ""}
          disabled={assign.isPending}
          onChange={(event) => assign.mutate(event.target.value)}
        >
          <option value="">Default</option>
          {profiles.map((profile) => (
            <option key={profile.id} value={profile.id}>
              {profile.name || profile.id}
            </option>
          ))}
        </select>
        <ErrorBox error={assign.error} />
      </td>
      <td>
        <Badge tone={tone}>{label}</Badge> {effectiveName ? <span>{effectiveName}</span> : null}
        {origin === "PROFILE_ORIGIN_PUSHED" ? <PushedProfile assignment={assignment} /> : null}
      </td>
    </tr>
  );
}

const solverTypeNames: Record<string, string> = {
  SOLVER_TYPE_FLARESOLVERR: "FlareSolverr",
  SOLVER_TYPE_BYPARR: "Byparr",
};

/** What a host pushed for a provider, and a way to drop it when meta runs on its own again. */
function PushedProfile({ assignment }: { assignment: Assignment }) {
  const [removing, setRemoving] = useState(false);
  const profile = assignment.pushedProfile;
  const proxy = profile?.proxy;
  const solver = profile?.solver;
  return (
    <div className="muted small">
      <div>A host pushed this profile; it wins over the assignment. Pushed {formatDate(assignment.pushedAt)}.</div>
      <ul className="plain-list">
        <li>
          Proxy:{" "}
          {proxy?.url ? (
            <>
              <Mono>{proxy.url}</Mono>
              {proxy.username ? ` as ${proxy.username}` : ""}
              {assignment.pushedProxyPasswordSet ? ", password stored" : ""}
            </>
          ) : (
            "none (direct)"
          )}
        </li>
        <li>
          Solver:{" "}
          {solver?.url ? (
            <>
              {solverTypeNames[solver.type ?? ""] ?? "Solver"} at <Mono>{solver.url}</Mono>
            </>
          ) : (
            "none"
          )}
        </li>
        {profile?.dohUrl ? (
          <li>
            DNS over HTTPS: <Mono>{profile.dohUrl}</Mono>
          </li>
        ) : null}
      </ul>
      <button type="button" className="small" onClick={() => setRemoving(true)}>
        Remove pushed profile
      </button>
      {removing ? <RemovePushedDialog assignment={assignment} onClose={() => setRemoving(false)} /> : null}
    </div>
  );
}

function RemovePushedDialog({ assignment, onClose }: { assignment: Assignment; onClose: () => void }) {
  const invalidate = useInvalidateNetwork();
  const providerId = assignment.providerId ?? "";
  const remove = useMutation({
    mutationFn: () =>
      unwrap(api.PUT("/v1/admin/pushed-profiles/{providerId}", { params: { path: { providerId } }, body: {} })),
    onSuccess: async () => {
      await invalidate();
      onClose();
    },
  });
  return (
    <ConfirmDialog
      title={`Remove the pushed profile of ${providerId}?`}
      confirmLabel="Remove pushed profile"
      pending={remove.isPending}
      error={remove.error}
      onConfirm={() => remove.mutate()}
      onClose={onClose}
    >
      <p>
        {providerId} then uses the profile assigned here, the default profile, or a direct connection. Do this when meta
        is no longer run by the host that pushed it: a host that still manages meta pushes it again on its next change.
      </p>
    </ConfirmDialog>
  );
}

// ---------------------------------------------------------------------------
// Search engine
// ---------------------------------------------------------------------------

const searchEngineQuery = () =>
  queryOptions({
    queryKey: ["search-engine"],
    queryFn: () => unwrap(api.GET("/v1/admin/search-engine")),
    // Follow a build or a backlog; otherwise check now and then.
    refetchInterval: (query) => {
      const st = query.state.data?.status;
      return st && st.backend !== "postgres" && (!st.ready || Number(st.pending ?? 0) > 0) ? 2000 : 15_000;
    },
  });

const backendNames: Record<string, string> = {
  postgres: "PostgreSQL",
  typesense: "Typesense",
  meilisearch: "Meilisearch",
};

const browseLabels: Record<string, string> = {
  facets: "the engine when facets are asked for",
  engine: "the engine",
  postgres: "PostgreSQL",
};

function SearchEngineSection() {
  const engine = useQuery(searchEngineQuery());
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [editing, setEditing] = useState(false);
  const rebuild = useMutation({
    mutationFn: () =>
      unwrap(api.POST("/v1/admin/jobs/{jobId}:run", { params: { path: { jobId: "search-rebuild" } }, body: {} })),
    onSuccess: (res) => void navigate({ to: "/jobs", search: { job: "search-rebuild", run: res.run?.id } }),
  });
  const reset = useMutation({
    mutationFn: () => unwrap(api.POST("/v1/admin/search-engine:reset", { body: {} })),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["search-engine"] }),
  });
  const set = engine.data?.settings;
  const st = engine.data?.status;
  const isEngine = set?.backend !== undefined && set.backend !== "postgres";
  return (
    <section className="section" aria-label="Search engine">
      <header className="section-header">
        <h2>Search engine</h2>
        <div className="actions">
          <button type="button" onClick={() => setEditing(true)} disabled={!engine.data}>
            Change
          </button>
        </div>
      </header>
      <p className="muted">
        PostgreSQL searches the catalogue with nothing to set up. A Typesense or Meilisearch server adds typo-tolerant,
        multilingual search with facet counts. meta talks to it; browsers never do. PostgreSQL stays the source of truth
        and answers whenever the engine can't.
      </p>
      {engine.isPending ? <Loading /> : null}
      <ErrorBox error={engine.error} onRetry={() => engine.refetch()} />
      {set && st ? (
        <article className="card" aria-label={`Search backend ${backendNames[set.backend ?? ""] ?? set.backend}`}>
          <header className="card-header">
            <div>
              <h3>{backendNames[set.backend ?? ""] ?? set.backend}</h3>
              <span className="muted small">{set.origin === "admin" ? "Set here" : "From the configuration file"}</span>
            </div>
            <div className="badges">
              {isEngine ? (
                <>
                  <Badge tone={st.reachable ? "good" : "bad"}>{st.reachable ? "Reachable" : "Unreachable"}</Badge>
                  <Badge tone={st.ready ? "good" : "warn"}>{st.ready ? "Index ready" : "No index yet"}</Badge>
                  {st.degraded ? <Badge tone="warn">Falling back</Badge> : null}
                </>
              ) : (
                <Badge tone="good">Built in</Badge>
              )}
            </div>
          </header>
          <dl className="details">
            <dt>Can do</dt>
            <dd>
              <div className="badges">
                {st.typoTolerance ? <Badge>typos</Badge> : null}
                {st.substring ? <Badge>substrings</Badge> : null}
                {st.facets ? <Badge tone="info">facet counts</Badge> : <Badge>no facet counts</Badge>}
              </div>
            </dd>
            {isEngine ? (
              <>
                <dt>URL</dt>
                <dd>
                  <Mono>{set.url}</Mono> {set.hasApiKey ? <span className="muted small">key stored</span> : null}
                </dd>
                <dt>Index</dt>
                <dd>
                  {st.index ? <Mono>{st.index}</Mono> : <span className="muted">not built</span>}{" "}
                  <span className="muted small">
                    {Number(st.documents ?? 0).toLocaleString()} documents
                    {st.builtAt ? ` · built ${formatDate(st.builtAt)}` : ""}
                  </span>
                </dd>
                <dt>Changes waiting</dt>
                <dd>
                  {Number(st.pending ?? 0).toLocaleString()}
                  {Number(st.pending ?? 0) > 0 ? (
                    <span className="muted small"> · oldest {formatDuration(st.lag)} ago</span>
                  ) : null}
                  <span className="muted small"> · {Number(st.applied ?? 0).toLocaleString()} applied since start</span>
                </dd>
                <dt>Browsing</dt>
                <dd>{browseLabels[set.browse ?? "facets"]}</dd>
              </>
            ) : null}
          </dl>
          {st.error ? (
            <div className="error-box" role="alert">
              {st.error}
            </div>
          ) : null}
          {st.indexerError ? (
            <div className="error-box" role="alert">
              Indexer: {st.indexerError}
            </div>
          ) : null}
          <ErrorBox error={rebuild.error ?? reset.error} />
          <div className="actions">
            {isEngine ? (
              <button type="button" disabled={rebuild.isPending} onClick={() => rebuild.mutate()}>
                {rebuild.isPending ? "Starting…" : "Rebuild index"}
              </button>
            ) : null}
            {set.origin === "admin" ? (
              <button type="button" disabled={reset.isPending} onClick={() => reset.mutate()}>
                Use the configuration file
              </button>
            ) : null}
          </div>
        </article>
      ) : null}
      {editing && set ? <SearchEngineDialog current={set} onClose={() => setEditing(false)} /> : null}
    </section>
  );
}

const defaultURLs: Record<string, string> = {
  typesense: "http://typesense:8108",
  meilisearch: "http://meilisearch:7700",
};

function SearchEngineDialog({ current, onClose }: { current: SearchEngineSettings; onClose: () => void }) {
  const queryClient = useQueryClient();
  const [backend, setBackend] = useState(current.backend ?? "postgres");
  const [url, setUrl] = useState(current.url ?? "");
  const [apiKey, setApiKey] = useState("");
  const [prefix, setPrefix] = useState(current.indexPrefix || "meta");
  const [browse, setBrowse] = useState(current.browse || "facets");
  const [tested, setTested] = useState<{ ok: boolean; error?: string } | null>(null);
  const settings = (): SearchEngineSettings => ({
    backend,
    url: url.trim(),
    apiKey,
    indexPrefix: prefix.trim(),
    browse,
  });
  const sameEngine = backend === current.backend && url.trim() === current.url;
  const test = useMutation({
    mutationFn: () => unwrap(api.POST("/v1/admin/search-engine:test", { body: { settings: settings() } })),
    onSuccess: (res) => setTested({ ok: !!res.ok, error: res.error }),
  });
  const save = useMutation({
    mutationFn: () => unwrap(api.PUT("/v1/admin/search-engine", { body: { settings: settings() } })),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["search-engine"] });
      onClose();
    },
  });
  const submit = (event: FormEvent) => {
    event.preventDefault();
    save.mutate();
  };
  const isEngine = backend !== "postgres";
  return (
    <Dialog title="Search engine" onClose={onClose}>
      <form className="form-grid two" onSubmit={submit}>
        <Field label="Backend" className="span-2">
          <select
            value={backend}
            onChange={(e) => {
              setBackend(e.target.value);
              setTested(null);
              if (!url || Object.values(defaultURLs).includes(url)) setUrl(defaultURLs[e.target.value] ?? "");
            }}
          >
            {Object.entries(backendNames).map(([value, label]) => (
              <option key={value} value={value}>
                {label}
              </option>
            ))}
          </select>
        </Field>
        {isEngine ? (
          <>
            <Field label="URL" hint="Where meta reaches the engine, e.g. its service in your compose file.">
              <input type="url" required value={url} onChange={(e) => setUrl(e.target.value)} />
            </Field>
            <Field
              label="API key"
              hint={
                current.hasApiKey && sameEngine
                  ? "Leave empty to keep the stored key. Stored encrypted; never shown again."
                  : "A key that may manage indexes. Stored encrypted; never shown again."
              }
            >
              <input
                type="password"
                autoComplete="new-password"
                value={apiKey}
                placeholder={current.hasApiKey && sameEngine ? "(stored)" : ""}
                onChange={(e) => setApiKey(e.target.value)}
              />
            </Field>
            <Field label="Index prefix" hint="meta's index names start with it.">
              <input
                type="text"
                required
                pattern="[a-z0-9][a-z0-9_\-]{0,30}"
                value={prefix}
                onChange={(e) => setPrefix(e.target.value)}
              />
            </Field>
            <Field label="Browsing" hint="Who lists entries without search text.">
              <select value={browse} onChange={(e) => setBrowse(e.target.value)}>
                {Object.entries(browseLabels).map(([value, label]) => (
                  <option key={value} value={value}>
                    {label.charAt(0).toUpperCase() + label.slice(1)}
                  </option>
                ))}
              </select>
            </Field>
            <p className="muted small span-2">
              Saving switches at once. A new index is built in the background (see Jobs); PostgreSQL answers until it is
              ready.
            </p>
          </>
        ) : (
          <p className="muted span-2">PostgreSQL needs no settings. Saving stops feeding any engine.</p>
        )}
        <div className="span-2">
          {tested ? (
            tested.ok ? (
              <p className="notice-box" role="status">
                Connected.
              </p>
            ) : (
              <div className="error-box" role="alert">
                {tested.error}
              </div>
            )
          ) : null}
          <ErrorBox error={test.error ?? save.error} />
        </div>
        <div className="actions span-2">
          <button type="submit" className="primary" disabled={save.isPending}>
            {save.isPending ? "Saving…" : "Save"}
          </button>
          <button type="button" disabled={test.isPending} onClick={() => test.mutate()}>
            {test.isPending ? "Testing…" : "Test connection"}
          </button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </Dialog>
  );
}
