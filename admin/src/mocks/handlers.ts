// SPDX-License-Identifier: GPL-3.0-or-later

// MSW handlers that answer every API endpoint from in-memory fake data, with the same
// JSON shapes and error bodies as meta. Used by mock mode and the tests.
import { delay, HttpResponse, http, type JsonBodyType } from "msw";
import type { Assignment, Client, Entry, EntrySummary, NetworkProfile, Provider, Solver, SyncRun } from "../api/types";
import {
  CLIENT_KEY_PREFIX,
  MERGED_ENTRY_ID,
  MOCK_ADMIN_TOKEN,
  MOCK_CONFIG_KEY,
  mockClients,
  mockEntries,
  mockGenres,
  mockProfiles,
  mockProviders,
  mockRuns,
  mockSolvers,
} from "./data";

type Db = {
  entries: Entry[];
  providers: Provider[];
  runs: SyncRun[];
  clients: Client[];
  solvers: Solver[];
  profiles: NetworkProfile[];
  /** Stored proxy passwords by profile id (never returned). */
  passwords: Map<string, string>;
  defaultProfileId: string;
  assignments: Map<string, string>;
  pushed: Map<string, string>;
  nextId: number;
};

export function createDb(): Db {
  return {
    entries: mockEntries(),
    providers: mockProviders(),
    runs: mockRuns(),
    clients: mockClients(),
    solvers: mockSolvers(),
    profiles: mockProfiles(),
    passwords: new Map([["via-proxy", "fake-password"]]),
    defaultProfileId: "polite",
    assignments: new Map([["example", "via-proxy"]]),
    pushed: new Map(),
    nextId: 1,
  };
}

const grpcCodes = {
  INVALID_ARGUMENT: [3, 400],
  NOT_FOUND: [5, 404],
  ALREADY_EXISTS: [6, 409],
  PERMISSION_DENIED: [7, 403],
  FAILED_PRECONDITION: [9, 400],
  UNAVAILABLE: [14, 503],
} as const;

/** meta's REST errors: a google.rpc.Status with the matching HTTP status. */
export function grpcError(code: keyof typeof grpcCodes, message: string) {
  const [number, status] = grpcCodes[code];
  return HttpResponse.json({ code: number, message, details: [] }, { status });
}

/** meta's answer to a missing or wrong token. */
function unauthenticated() {
  return HttpResponse.json({ code: "unauthenticated", message: "missing or wrong API key" }, { status: 401 });
}

const ok = (body: JsonBodyType) => HttpResponse.json(body);

type Who = "admin" | "client" | "config" | null;

/** Mirrors meta's auth: the admin token, client keys ("mtk_…"), a config key, or nothing. */
function principal(request: Request): Who {
  const header = request.headers.get("Authorization") ?? "";
  const token = header.startsWith("Bearer ") ? header.slice(7) : "";
  if (!token || token === "bad") return null;
  if (token === MOCK_CONFIG_KEY) return "config";
  return token.startsWith(CLIENT_KEY_PREFIX) ? "client" : "admin";
}

function guard(request: Request, admin: boolean) {
  const who = principal(request);
  if (!who) return unauthenticated();
  if (admin && who === "client") return grpcError("PERMISSION_DENIED", "this call needs the 'admin' scope");
  return null;
}

function newKey(): string {
  const chars = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789";
  let key = CLIENT_KEY_PREFIX;
  for (let i = 0; i < 43; i++) key += chars[Math.floor(Math.random() * chars.length)];
  return key;
}

const now = () => new Date().toISOString();

function summary(e: Entry): EntrySummary {
  return {
    id: e.id,
    title: e.title,
    type: e.type,
    status: e.status,
    contentRating: e.contentRating,
    originalLanguage: e.originalLanguage,
    year: e.dates?.start?.year,
    cover: e.images?.[0],
    genres: (e.tags ?? []).filter((t) => t.tag?.isGenre).map((t) => t.tag?.name ?? ""),
    rating: e.stats?.[0]?.stats?.rating,
    attributions: (e.sources ?? []).flatMap((s) => (s.attribution ? [s.attribution] : [])),
  };
}

/** Filters and sorts entries like Browse; a query narrows to title and people matches. */
function matching(db: Db, params: URLSearchParams, query?: string): { entries: Entry[]; matched: Map<string, string> } {
  const all = (key: string) => params.getAll(key);
  const matched = new Map<string, string>();
  let list = db.entries.filter((e) => {
    if (all("filter.types").length && !all("filter.types").includes(e.type ?? "")) return false;
    if (all("filter.statuses").length && !all("filter.statuses").includes(e.status ?? "")) return false;
    if (all("filter.contentRatings").length && !all("filter.contentRatings").includes(e.contentRating ?? ""))
      return false;
    if (all("filter.languages").length && !all("filter.languages").includes(e.originalLanguage ?? "")) return false;
    const genres = (e.tags ?? []).filter((t) => t.tag?.isGenre).map((t) => (t.tag?.name ?? "").toLowerCase());
    if (all("filter.genres").length && !all("filter.genres").some((g) => genres.includes(g.toLowerCase())))
      return false;
    const year = e.dates?.start?.year ?? 0;
    if (params.get("filter.yearFrom") && year < Number(params.get("filter.yearFrom"))) return false;
    if (params.get("filter.yearTo") && year > Number(params.get("filter.yearTo"))) return false;
    if (query) {
      const q = query.toLowerCase();
      const title = (e.titles ?? []).find((t) => (t.text ?? "").toLowerCase().includes(q));
      const person = (e.credits ?? []).find((c) => (c.person?.name ?? "").toLowerCase().includes(q));
      if (!title && !person) return false;
      matched.set(e.id ?? "", title?.text ?? person?.person?.name ?? "");
    }
    return true;
  });
  const sort = params.get("sort") ?? "";
  const key = (e: Entry): number | string => {
    switch (sort) {
      case "SORT_FIELD_RATING":
        return -(e.stats?.[0]?.stats?.rating ?? 0);
      case "SORT_FIELD_YEAR":
        return -(e.dates?.start?.year ?? 0);
      case "SORT_FIELD_TITLE":
        return (e.title ?? "").toLowerCase();
      default:
        return Number(e.stats?.[0]?.stats?.popularityRank ?? 1e9);
    }
  };
  list = [...list].sort((a, b) => {
    const ka = key(a);
    const kb = key(b);
    return ka < kb ? -1 : ka > kb ? 1 : 0;
  });
  if (params.get("reverse") === "true") list.reverse();
  return { entries: list, matched };
}

function page<T>(list: T[], params: URLSearchParams): { items: T[]; next: string } {
  const size = Number(params.get("pageSize") ?? 24) || 24;
  const offset = Number(params.get("pageToken") ?? 0) || 0;
  return { items: list.slice(offset, offset + size), next: offset + size < list.length ? String(offset + size) : "" };
}

function assignmentFor(db: Db, providerId: string): Assignment {
  const supported = providerId === "example";
  const pushed = db.pushed.get(providerId);
  const assigned = db.assignments.get(providerId) ?? "";
  if (pushed)
    return {
      providerId,
      profileId: assigned,
      pushedProfileName: pushed,
      effective: "PROFILE_ORIGIN_PUSHED",
      supported,
    };
  if (assigned)
    return {
      providerId,
      profileId: assigned,
      effective: "PROFILE_ORIGIN_ASSIGNED",
      effectiveProfileId: assigned,
      supported,
    };
  if (db.defaultProfileId)
    return { providerId, effective: "PROFILE_ORIGIN_DEFAULT", effectiveProfileId: db.defaultProfileId, supported };
  return { providerId, effective: "PROFILE_ORIGIN_NONE", supported };
}

function publicProfile(db: Db, profile: NetworkProfile): NetworkProfile {
  if (!profile.proxy) return profile;
  const { password: _password, ...proxy } = profile.proxy;
  return { ...profile, proxy: { ...proxy, hasPassword: db.passwords.has(profile.id ?? "") } };
}

function withUsage(db: Db, solver: Solver): Solver {
  return { ...solver, usedBy: db.profiles.filter((p) => p.solverId === solver.id).map((p) => p.id ?? "") };
}

/** Handlers over `db`. `latency` adds a delay in milliseconds to feel like a network. */
export function createHandlers(db: Db = createDb(), latency = 0) {
  const wait = () => (latency > 0 ? delay(latency) : Promise.resolve());
  const base = "*/v1";

  return [
    // ----- MetaClientsService.WhoAmI --------------------------------------------
    http.get(`${base}/whoami`, ({ request }) => {
      const who = principal(request);
      if (!who) return unauthenticated();
      if (who === "client")
        return ok({
          kind: "PRINCIPAL_KIND_CLIENT",
          clientId: "a1b2c3d4",
          name: "library-server",
          scopes: ["catalog:read"],
        });
      if (who === "config")
        return ok({ kind: "PRINCIPAL_KIND_CONFIG_KEY", name: "api_keys setting", scopes: ["admin"] });
      return ok({ kind: "PRINCIPAL_KIND_ADMIN", name: "admin", scopes: ["admin"] });
    }),

    // ----- MetaService -------------------------------------------------------------
    http.get(`${base}/entries`, async ({ request }) => {
      await wait();
      const denied = guard(request, false);
      if (denied) return denied;
      const params = new URL(request.url).searchParams;
      const { entries } = matching(db, params);
      const { items, next } = page(entries, params);
      return ok({
        entries: items.map(summary),
        nextPageToken: next,
        totalSize: String(entries.length),
        noProviders: db.providers.length === 0,
      });
    }),

    http.get(`${base}/search`, async ({ request }) => {
      await wait();
      const denied = guard(request, false);
      if (denied) return denied;
      const params = new URL(request.url).searchParams;
      const query = params.get("query") ?? "";
      if (!query.trim()) return grpcError("INVALID_ARGUMENT", "query is empty; use Browse to list entries");
      const { entries, matched } = matching(db, params, query);
      const { items, next } = page(entries, params);
      return ok({
        hits: items.map((e) => ({
          entry: summary(e),
          score: 0.9,
          matched: matched.get(e.id ?? ""),
          matchedField: "title",
        })),
        nextPageToken: next,
        totalSize: String(entries.length),
        noProviders: db.providers.length === 0,
      });
    }),

    http.get(`${base}/entries/:id`, async ({ request, params }) => {
      await wait();
      const denied = guard(request, false);
      if (denied) return denied;
      const requested = String(params.id);
      const id = requested === MERGED_ENTRY_ID ? db.entries[0]?.id : requested;
      const entry = db.entries.find((e) => e.id === id);
      if (!entry) return grpcError("NOT_FOUND", `no entry ${requested}`);
      return ok({ entry, redirectedFrom: id !== requested ? requested : undefined });
    }),

    http.get(`${base}/tags`, async ({ request }) => {
      await wait();
      return guard(request, false) ?? ok({ tags: mockGenres() });
    }),

    // ----- MetaAdminService --------------------------------------------------------
    http.get(`${base}/admin/providers`, async ({ request }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      return ok({
        providers: db.providers.map((p) => ({
          ...p,
          lastRuns: db.runs.filter((r) => r.provider === p.id).slice(0, 1),
        })),
      });
    }),

    http.post(`${base}/admin/providers/:provider/sync`, async ({ request, params }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      const provider = String(params.provider);
      if (!db.providers.some((p) => p.id === provider))
        return grpcError("NOT_FOUND", `no provider "${provider}" is configured`);
      if (db.runs.some((r) => r.provider === provider && r.status === "SYNC_RUN_STATUS_RUNNING"))
        return grpcError("ALREADY_EXISTS", "a sync is already running for this provider");
      const body = (await request.json()) as { kind?: string; force?: boolean };
      const run: SyncRun = {
        id: `0192cc00-0000-7000-8000-${String(db.nextId++).padStart(12, "0")}`,
        provider,
        kind: body.kind || "dump",
        status: "SYNC_RUN_STATUS_RUNNING",
        trigger: "manual",
        startedAt: now(),
        upserts: "0",
        changed: "0",
        progress: { phase: "download", done: 1_000_000, total: 4_000_000, unit: "bytes" },
        notices: [],
      };
      db.runs = [run, ...db.runs];
      return ok({ run });
    }),

    http.get(`${base}/admin/sync-runs`, async ({ request }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      const params = new URL(request.url).searchParams;
      const provider = params.get("provider");
      const runs = db.runs.filter((r) => !provider || r.provider === provider);
      const { items, next } = page(runs, params);
      return ok({ runs: items, nextPageToken: next });
    }),

    http.get(`${base}/admin/sync-runs/:id`, async ({ request, params }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      const run = db.runs.find((r) => r.id === params.id);
      return run ? ok({ run }) : grpcError("NOT_FOUND", `no sync run ${params.id}`);
    }),

    http.post(`${base}/admin/entries/:id/refresh`, async ({ request, params }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      if (!db.entries.some((e) => e.id === params.id)) return grpcError("NOT_FOUND", `no entry ${params.id}`);
      return ok({ refreshed: ["example"], failed: {} });
    }),

    // ----- MetaClientsService ------------------------------------------------------
    http.get(`${base}/admin/clients`, async ({ request }) => {
      await wait();
      return guard(request, true) ?? ok({ clients: db.clients });
    }),

    http.post(`${base}/admin/clients`, async ({ request }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      const body = (await request.json()) as { name?: string; scopes?: string[] };
      if (!body.name?.trim()) return grpcError("INVALID_ARGUMENT", "name is required");
      if (!body.scopes?.length) return grpcError("INVALID_ARGUMENT", "at least one scope is required");
      const apiKey = newKey();
      const client: Client = {
        id: `mock${db.nextId++}`,
        name: body.name.trim(),
        scopes: body.scopes,
        keyPrefix: apiKey.slice(0, 10),
        createdAt: now(),
      };
      db.clients = [...db.clients, client];
      return ok({ client, apiKey });
    }),

    http.patch(`${base}/admin/clients/:clientId`, async ({ request, params }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      const body = (await request.json()) as { name?: string; scopes?: string[] };
      const client = db.clients.find((c) => c.id === params.clientId);
      if (!client) return grpcError("NOT_FOUND", `no client "${params.clientId}"`);
      if (body.name) client.name = body.name;
      if (body.scopes?.length) client.scopes = body.scopes;
      return ok({ client });
    }),

    http.post(`${base}/admin/clients/:clientId/rotate`, async ({ request, params }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      const client = db.clients.find((c) => c.id === params.clientId);
      if (!client) return grpcError("NOT_FOUND", `no client "${params.clientId}"`);
      const apiKey = newKey();
      client.keyPrefix = apiKey.slice(0, 10);
      client.rotatedAt = now();
      return ok({ client, apiKey });
    }),

    http.delete(`${base}/admin/clients/:clientId`, async ({ request, params }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      if (!db.clients.some((c) => c.id === params.clientId))
        return grpcError("NOT_FOUND", `no client "${params.clientId}"`);
      db.clients = db.clients.filter((c) => c.id !== params.clientId);
      return ok({});
    }),

    // ----- MetaIntegrationsService -------------------------------------------------
    http.get(`${base}/admin/solvers`, async ({ request }) => {
      await wait();
      return guard(request, true) ?? ok({ solvers: db.solvers.map((s) => withUsage(db, s)) });
    }),

    http.put(`${base}/admin/solvers/:id`, async ({ request, params }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      const solver = (await request.json()) as Solver;
      if (!solver.url) return grpcError("INVALID_ARGUMENT", "url must be an absolute http(s) URL");
      const saved = { ...solver, id: String(params.id) };
      const isNew = !db.solvers.some((s) => s.id === saved.id);
      db.solvers = [...db.solvers.filter((s) => s.id !== saved.id), saved];
      let created = "";
      // Like meta: the first solver, with no profiles at all, becomes the default profile's.
      if (isNew && db.profiles.length === 0 && !db.defaultProfileId) {
        db.profiles = [
          { id: "default", name: "Default", solverId: saved.id, userAgentMode: "USER_AGENT_MODE_FROM_SOLVER" },
        ];
        db.defaultProfileId = "default";
        created = "default";
      }
      return ok({ solver: withUsage(db, saved), createdDefaultProfileId: created });
    }),

    http.delete(`${base}/admin/solvers/:id`, async ({ request, params }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      const users = db.profiles.filter((p) => p.solverId === params.id).map((p) => p.id);
      if (users.length > 0)
        return grpcError(
          "FAILED_PRECONDITION",
          `in use: solver "${params.id}" is used by profiles: ${users.join(", ")}`,
        );
      if (!db.solvers.some((s) => s.id === params.id))
        return grpcError("NOT_FOUND", `not found: no solver "${params.id}"`);
      db.solvers = db.solvers.filter((s) => s.id !== params.id);
      return ok({});
    }),

    http.get(`${base}/admin/profiles`, async ({ request }) => {
      await wait();
      return (
        guard(request, true) ??
        ok({ profiles: db.profiles.map((p) => publicProfile(db, p)), defaultProfileId: db.defaultProfileId })
      );
    }),

    http.put(`${base}/admin/profiles/:id`, async ({ request, params }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      const profile = (await request.json()) as NetworkProfile;
      const id = String(params.id);
      if (profile.solverId && !db.solvers.some((s) => s.id === profile.solverId)) {
        return grpcError("INVALID_ARGUMENT", `invalid: no solver "${profile.solverId}"`);
      }
      if (profile.proxy?.password) db.passwords.set(id, profile.proxy.password);
      if (!profile.proxy?.username) db.passwords.delete(id);
      const saved: NetworkProfile = { ...profile, id };
      if (saved.proxy) saved.proxy = { url: saved.proxy.url ?? "", username: saved.proxy.username ?? "" };
      db.profiles = [...db.profiles.filter((p) => p.id !== id), saved];
      return ok({ profile: publicProfile(db, saved) });
    }),

    http.delete(`${base}/admin/profiles/:id`, async ({ request, params }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      const id = String(params.id);
      db.profiles = db.profiles.filter((p) => p.id !== id);
      db.passwords.delete(id);
      for (const [providerId, profileId] of db.assignments) if (profileId === id) db.assignments.delete(providerId);
      if (db.defaultProfileId === id) db.defaultProfileId = "";
      return ok({});
    }),

    http.get(`${base}/admin/assignments`, async ({ request }) => {
      await wait();
      return (
        guard(request, true) ??
        ok({
          assignments: db.providers.map((p) => assignmentFor(db, p.id ?? "")),
          defaultProfileId: db.defaultProfileId,
        })
      );
    }),

    http.put(`${base}/admin/assignments/:providerId`, async ({ request, params }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      const providerId = String(params.providerId);
      const body = (await request.json()) as { profileId?: string };
      if (body.profileId && !db.profiles.some((p) => p.id === body.profileId))
        return grpcError("NOT_FOUND", `not found: no profile "${body.profileId}"`);
      if (body.profileId) db.assignments.set(providerId, body.profileId);
      else db.assignments.delete(providerId);
      return ok({ assignment: assignmentFor(db, providerId) });
    }),

    http.put(`${base}/admin/default-profile`, async ({ request }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      const body = (await request.json()) as { profileId?: string };
      if (body.profileId && !db.profiles.some((p) => p.id === body.profileId))
        return grpcError("NOT_FOUND", `not found: no profile "${body.profileId}"`);
      db.defaultProfileId = body.profileId ?? "";
      return ok({});
    }),
  ];
}

export { MOCK_ADMIN_TOKEN };
