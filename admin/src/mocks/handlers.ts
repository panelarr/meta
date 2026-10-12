// SPDX-License-Identifier: GPL-3.0-or-later

// MSW handlers that answer every API endpoint from in-memory fake data, with the same
// JSON shapes and error bodies as meta. Used by mock mode and the tests.
import { delay, HttpResponse, http, type JsonBodyType } from "msw";
import type {
  Assignment,
  Client,
  DecisionJudgeSettings,
  DuplicatePair,
  Entry,
  EntrySummary,
  Facet,
  Job,
  JobRun,
  NetworkProfile,
  Provider,
  SearchEngineSettings,
  SearchEngineStatus,
  Solver,
  SyncRun,
} from "../api/types";
import {
  CLIENT_KEY_PREFIX,
  MERGED_ENTRY_ID,
  MOCK_ADMIN_TOKEN,
  MOCK_CONFIG_KEY,
  mockClients,
  mockEntries,
  mockGenres,
  mockJobRuns,
  mockJobs,
  mockProfiles,
  mockProviders,
  mockRuns,
  mockSolvers,
  mockTags,
} from "./data";

type Db = {
  entries: Entry[];
  providers: Provider[];
  runs: SyncRun[];
  jobs: Job[];
  /** Job runs, newest first. */
  jobRuns: JobRun[];
  search: { settings: SearchEngineSettings; apiKey: string };
  /** Duplicate pairs by index into entries (made up). */
  pairs: MockPair[];
  judge: { settings: DecisionJudgeSettings; apiKey: string };
  clients: Client[];
  solvers: Solver[];
  profiles: NetworkProfile[];
  /** Stored proxy passwords by profile id (never returned). */
  passwords: Map<string, string>;
  defaultProfileId: string;
  assignments: Map<string, string>;
  /** Profiles pushed by a host, by provider: as stored (no proxy password). */
  pushed: Map<string, Pushed>;
  nextId: number;
};

export type Pushed = { profile: NonNullable<Assignment["pushedProfile"]>; passwordSet: boolean; at: string };

export function createDb(): Db {
  return {
    entries: mockEntries(),
    providers: mockProviders(),
    runs: mockRuns(),
    jobs: mockJobs(),
    jobRuns: mockJobRuns(),
    search: { settings: { backend: "postgres", indexPrefix: "meta", browse: "facets", origin: "config" }, apiKey: "" },
    pairs: mockPairs(),
    judge: { settings: { enabled: false, origin: "config" }, apiKey: "" },
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
    // Tags by key ("tag/settings/fantasy") or by name.
    const has = (ref: string) =>
      (e.tags ?? []).some((t) => t.tag?.key === ref || (t.tag?.name ?? "").toLowerCase() === ref.toLowerCase());
    if (!all("filter.tags").every(has)) return false;
    if (all("filter.excludeTags").some(has)) return false;
    if (
      all("filter.titleLanguages").length &&
      !(e.titles ?? []).some((t) => all("filter.titleLanguages").includes(t.language ?? ""))
    )
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
      pushedProfileName: pushed.profile.name,
      pushedProfile: pushed.profile,
      pushedProxyPasswordSet: pushed.passwordSet,
      pushedAt: pushed.at,
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
/** A URL pattern from the handlers' base (a glob such as `*` + "/v1", or a full URL) and a regular expression tail. */
function pattern(base: string, tail: string): RegExp {
  const escaped = base.replace(/[.+?^${}()|[\]\\]/g, "\\$&").replace(/\*/g, ".*");
  return new RegExp(`^${escaped}${tail}`);
}

function lastSegment(url: string): string {
  return decodeURIComponent(new URL(url).pathname.split("/").pop() ?? "");
}

/** How long a mock run runs before it succeeds on its own. */
const MOCK_RUN_MS = 8000;

/** Finishes a mock run once it has run long enough. */
function settle(run: JobRun): JobRun {
  const s = run.summary;
  if (s?.status === "JOB_RUN_STATUS_RUNNING" && Date.now() - new Date(s.startedAt ?? 0).getTime() > MOCK_RUN_MS) {
    s.status = "JOB_RUN_STATUS_SUCCEEDED";
    s.finishedAt = now();
    s.message = "done";
    s.counts = { items: "1000" };
    run.progress = undefined;
  }
  return run;
}

type MockPair = Omit<DuplicatePair, "entryA" | "entryB"> & { a: number; b: number };

function mockPairs(): MockPair[] {
  const signals = (title: number, people: number, shared: string[] = []) => ({
    title,
    titleA: "lantern keeper",
    titleB: "the lantern keeper",
    people,
    sharedPeople: people > 0 ? ["hana imaginary"] : [],
    yearA: 2019,
    yearB: 2019,
    typeA: "manhwa",
    typeB: "manhwa",
    languageA: "ko",
    languageB: "ko",
    sharedIds: shared,
    sameProvider: true,
  });
  return [
    {
      id: "1",
      a: 0,
      b: 1,
      status: "DUPLICATE_STATUS_OPEN",
      score: 0.92,
      signals: signals(0.79, 1, ["anilist:4242"]),
      verdict: {
        judge: "openai",
        model: "gpt-6-luna",
        same: 0.94,
        choice: "same_work",
        probabilities: { same_work: 0.94, related_work: 0.04, different: 0.02 },
        confidence: 0.9,
        tokens: "410",
      },
      createdAt: "2026-10-11T03:10:00Z",
    },
    {
      id: "2",
      a: 2,
      b: 3,
      status: "DUPLICATE_STATUS_OPEN",
      score: 0.68,
      signals: signals(0.64, -1),
      createdAt: "2026-10-11T03:10:00Z",
    },
    {
      id: "3",
      a: 1,
      b: 3,
      status: "DUPLICATE_STATUS_DISMISSED",
      score: 0.62,
      signals: signals(0.6, 0),
      decidedAt: "2026-10-10T12:00:00Z",
      decidedBy: "admin",
      note: "a sequel",
    },
  ];
}

function pairOut(db: Db, p: MockPair): DuplicatePair {
  const { a, b, ...rest } = p;
  const ea = db.entries[a];
  const eb = db.entries[b];
  return { ...rest, entryA: ea ? summary(ea) : undefined, entryB: eb ? summary(eb) : undefined };
}

/** Facet counts like an engine's, when an engine is set and they're asked for. */
function facetsFor(db: Db, entries: Entry[], params: URLSearchParams): Facet[] | undefined {
  if (params.get("facets") !== "true" || db.search.settings.backend === "postgres") return undefined;
  const count = (field: string, values: (e: Entry) => string[]): Facet => {
    const n = new Map<string, number>();
    for (const e of entries) for (const v of values(e)) n.set(v, (n.get(v) ?? 0) + 1);
    return {
      field,
      values: [...n.entries()].sort((a, b) => b[1] - a[1]).map(([value, c]) => ({ value, count: String(c) })),
    };
  };
  const short = (v: string | undefined, prefix: string) => (v ? [v.replace(prefix, "").toLowerCase()] : []);
  return [
    count("genres", (e) => (e.tags ?? []).filter((t) => t.tag?.isGenre).map((t) => t.tag?.key ?? "")),
    count("tags", (e) => (e.tags ?? []).filter((t) => !t.tag?.isGenre).map((t) => t.tag?.key ?? "")),
    count("type", (e) => short(e.type, "ENTRY_TYPE_")),
    count("status", (e) => short(e.status, "ENTRY_STATUS_")),
    count("content_rating", (e) => short(e.contentRating, "CONTENT_RATING_")),
    count("original_language", (e) => (e.originalLanguage ? [e.originalLanguage] : [])),
  ];
}

/** The search engine's settings (never the key) and a made-up status. */
function searchState(db: Db): { settings: SearchEngineSettings; status: SearchEngineStatus } {
  const set = db.search.settings;
  const settings = { ...set, hasApiKey: db.search.apiKey !== "" };
  if (set.backend === "postgres")
    return {
      settings,
      status: { backend: "postgres", typoTolerance: true, substring: true, facets: false, reachable: true },
    };
  return {
    settings,
    status: {
      backend: set.backend,
      typoTolerance: true,
      substring: true,
      facets: true,
      reachable: true,
      ready: true,
      index: `${set.indexPrefix}_entries_v1_1791700000000`,
      documents: String(db.entries.length),
      builtAt: "2026-10-11T08:00:00Z",
      pending: "2",
      lag: "3s",
      applied: "120",
    },
  };
}

/** A job with its run state: running, current run and last run. */
function jobState(db: Db, job: Job): Job {
  const runs = db.jobRuns.map(settle).filter((r) => r.summary?.jobId === job.id);
  const current = runs.find((r) => r.summary?.status === "JOB_RUN_STATUS_RUNNING");
  return { ...job, running: !!current, currentRunId: current?.summary?.id, lastRun: runs[0]?.summary };
}

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
        facets: facetsFor(db, entries, params),
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
        facets: facetsFor(db, entries, params),
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
      const denied = guard(request, false);
      if (denied) return denied;
      const params = new URL(request.url).searchParams;
      const q = (params.get("query") ?? "").toLowerCase();
      let tags = params.get("genresOnly") === "true" ? mockGenres() : [...mockGenres(), ...mockTags()];
      if (params.get("tagsOnly") === "true") tags = mockTags();
      if (q) tags = tags.filter((t) => [...(t.path ?? []), t.name ?? ""].join(" ").toLowerCase().includes(q));
      return ok({ tags: tags.slice(0, Number(params.get("pageSize") ?? 200)) });
    }),

    http.get(`${base}/languages`, async ({ request }) => {
      await wait();
      const denied = guard(request, false);
      if (denied) return denied;
      const count = (codes: string[]) =>
        [...codes.reduce((m, c) => m.set(c, (m.get(c) ?? 0) + 1), new Map<string, number>())]
          .sort((a, b) => b[1] - a[1])
          .map(([code, n]) => ({ code, entries: String(n) }));
      return ok({
        original: count(db.entries.map((e) => e.originalLanguage ?? "").filter(Boolean)),
        titles: count(
          db.entries.flatMap((e) => [...new Set((e.titles ?? []).map((t) => t.language ?? ""))]).filter(Boolean),
        ),
      });
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

    http.get(`${base}/admin/health`, async ({ request }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      const checkedAt = now();
      const components = [
        {
          name: "database",
          kind: "database",
          status: "HEALTH_STATUS_OK",
          latency: "0.002s",
          checkedAt,
          details: { connections: "4", idle: "3" },
        },
        {
          name: "search",
          kind: "search",
          status: "HEALTH_STATUS_OK",
          latency: "0.006s",
          checkedAt,
          details: { backend: "postgres", typo_tolerance: "true", substring: "true", facets: "false" },
        },
        ...db.providers.map((p) => ({
          name: `provider:${p.id}`,
          kind: "provider",
          status: p.health,
          message: p.healthMessage ?? "",
          latency: "0.004s",
          checkedAt,
          details: {
            mode: p.mode ?? "",
            ...(p.info ? { plugin: p.info.id ?? "", version: p.info.version ?? "" } : {}),
          },
        })),
      ];
      const status = components.every((c) => c.status === "HEALTH_STATUS_OK")
        ? "HEALTH_STATUS_OK"
        : "HEALTH_STATUS_DEGRADED";
      return ok({ status, components, version: "dev" });
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

    // ----- Search engine (MetaIntegrationsService) ----------------------------------
    http.get(`${base}/admin/search-engine`, async ({ request }) => {
      await wait();
      return guard(request, true) ?? ok(searchState(db));
    }),

    http.put(`${base}/admin/search-engine`, async ({ request }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      const body = (await request.json()) as { settings?: SearchEngineSettings };
      const s = body.settings ?? {};
      if (!["postgres", "typesense", "meilisearch"].includes(s.backend ?? ""))
        return grpcError("INVALID_ARGUMENT", `invalid search settings: unknown search backend "${s.backend}"`);
      if (s.backend !== "postgres" && !/^https?:\/\/./.test(s.url ?? ""))
        return grpcError("INVALID_ARGUMENT", `invalid search settings: the ${s.backend} URL must be an http(s) URL`);
      const same = s.backend === db.search.settings.backend && s.url === db.search.settings.url;
      db.search = {
        settings: {
          backend: s.backend,
          url: s.url,
          indexPrefix: s.indexPrefix || "meta",
          browse: s.browse || "facets",
          origin: "admin",
        },
        apiKey: s.apiKey || (same ? db.search.apiKey : ""),
      };
      return ok(searchState(db));
    }),

    http.post(pattern(base, "/admin/search-engine:test$"), async ({ request }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      const body = (await request.json()) as { settings?: SearchEngineSettings };
      const u = body.settings?.url ?? "";
      if (body.settings?.backend === "postgres" || u.includes("typesense") || u.includes("meilisearch"))
        return ok({ ok: true });
      return ok({ error: `Get "${u}/health": dial tcp: connection refused` });
    }),

    http.post(pattern(base, "/admin/search-engine:reset$"), async ({ request }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      db.search = {
        settings: { backend: "postgres", indexPrefix: "meta", browse: "facets", origin: "config" },
        apiKey: "",
      };
      return ok(searchState(db));
    }),

    // ----- MetaDuplicatesService ---------------------------------------------------
    http.get(`${base}/admin/duplicates`, async ({ request }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      const params = new URL(request.url).searchParams;
      const status = params.get("status") || "DUPLICATE_STATUS_OPEN";
      const min = Number(params.get("minScore") ?? 0);
      const judged = params.get("judgedOnly") === "true";
      const list = db.pairs.filter((p) => p.status === status && (p.score ?? 0) >= min && (!judged || p.verdict));
      const { items, next } = page(list, params);
      return ok({ pairs: items.map((p) => pairOut(db, p)), nextPageToken: next, totalSize: String(list.length) });
    }),

    http.post(pattern(base, "/admin/duplicates/([^/:]+):(confirm|dismiss|reopen)$"), async ({ request }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      const [, id, action] = /\/admin\/duplicates\/([^/:]+):(\w+)$/.exec(new URL(request.url).pathname) ?? [];
      const p = db.pairs.find((x) => x.id === id);
      if (!p) return grpcError("NOT_FOUND", `not found: no duplicate pair ${id}`);
      const body = (await request.json().catch(() => ({}))) as { keep?: string; note?: string };
      if (action === "reopen") {
        if (p.status !== "DUPLICATE_STATUS_DISMISSED")
          return grpcError("FAILED_PRECONDITION", "only dismissed pairs can be reopened");
        Object.assign(p, { status: "DUPLICATE_STATUS_OPEN", decidedAt: undefined, decidedBy: "", note: "" });
        return ok({ pair: pairOut(db, p) });
      }
      if (p.status !== "DUPLICATE_STATUS_OPEN") return grpcError("FAILED_PRECONDITION", "the pair was already decided");
      if (action === "confirm" && body.keep && !["a", "b"].includes(body.keep))
        return grpcError("INVALID_ARGUMENT", "invalid: keep must be a or b");
      Object.assign(p, {
        status: action === "confirm" ? "DUPLICATE_STATUS_CONFIRMED" : "DUPLICATE_STATUS_DISMISSED",
        decidedAt: now(),
        decidedBy: "admin",
        note: body.note ?? "",
      });
      const kept = body.keep === "b" ? db.entries[p.b] : db.entries[p.a];
      return ok({ pair: pairOut(db, p), entryId: action === "confirm" ? kept?.id : undefined });
    }),

    http.get(`${base}/admin/decision-judge`, async ({ request }) => {
      await wait();
      return guard(request, true) ?? ok({ settings: { ...db.judge.settings, hasApiKey: db.judge.apiKey !== "" } });
    }),

    http.put(`${base}/admin/decision-judge`, async ({ request }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      const s = ((await request.json()) as { settings?: DecisionJudgeSettings }).settings ?? {};
      const same = s.kind === db.judge.settings.kind && s.url === db.judge.settings.url;
      const key = s.apiKey || (same ? db.judge.apiKey : "");
      if (s.enabled && !key) return grpcError("INVALID_ARGUMENT", "invalid: an API key is needed to turn the model on");
      db.judge = {
        settings: { enabled: s.enabled, kind: s.kind, url: s.url, model: s.model, covers: s.covers, origin: "admin" },
        apiKey: key,
      };
      return ok({ settings: { ...db.judge.settings, hasApiKey: key !== "" } });
    }),

    http.post(pattern(base, "/admin/decision-judge:test$"), async ({ request }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      const s = ((await request.json()) as { settings?: DecisionJudgeSettings }).settings ?? {};
      if (!s.apiKey && !db.judge.apiKey) return ok({ error: "decision model: HTTP 401: missing API key" });
      return ok({ ok: true, verdict: { judge: s.kind, same: 0.96, choice: "same_work" } });
    }),

    http.post(pattern(base, "/admin/decision-judge:reset$"), async ({ request }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      db.judge = { settings: { enabled: false, origin: "config" }, apiKey: "" };
      return ok({ settings: db.judge.settings });
    }),

    // ----- MetaJobsService --------------------------------------------------------
    http.get(`${base}/admin/jobs`, async ({ request }) => {
      await wait();
      return guard(request, true) ?? ok({ jobs: db.jobs.map((j) => jobState(db, j)) });
    }),

    // "/jobs/{id}", "/jobs/{id}/runs" and "/jobs/{id}:run": the ":run" verb needs a pattern.
    http.get(pattern(base, "/admin/jobs/([^/:]+)$"), async ({ request }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      const job = db.jobs.find((j) => j.id === lastSegment(request.url));
      return job ? ok({ job: jobState(db, job) }) : grpcError("NOT_FOUND", `no job ${lastSegment(request.url)}`);
    }),

    http.get(`${base}/admin/jobs/:jobId/runs`, async ({ request, params }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      if (!db.jobs.some((j) => j.id === params.jobId)) return grpcError("NOT_FOUND", `no job ${params.jobId}`);
      const query = new URL(request.url).searchParams;
      const status = query.get("status");
      const runs = db.jobRuns
        .map((r) => settle(r))
        .filter((r) => r.summary?.jobId === params.jobId && (!status || r.summary?.status === status))
        .map((r) => r.summary);
      const { items, next } = page(runs, query);
      return ok({ runs: items, nextPageToken: next });
    }),

    http.post(pattern(base, "/admin/jobs/([^/:]+):run$"), async ({ request }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      const id = decodeURIComponent(new URL(request.url).pathname.split("/").pop()?.replace(/:run$/, "") ?? "");
      const job = db.jobs.find((j) => j.id === id);
      if (!job) return grpcError("NOT_FOUND", `not found: no job "${id}"`);
      const current = db.jobRuns
        .map(settle)
        .find((r) => r.summary?.jobId === id && r.summary.status === "JOB_RUN_STATUS_RUNNING");
      if (current) return grpcError("ALREADY_EXISTS", `job ${id} is already running (run ${current.summary?.id})`);
      const body = (await request.json().catch(() => ({}))) as { params?: Record<string, string> };
      const params: Record<string, string> = {};
      for (const p of job.params ?? []) {
        const v = body.params?.[p.name ?? ""] ?? p.default;
        if (v) params[p.name ?? ""] = v;
      }
      for (const k of Object.keys(body.params ?? {}))
        if (!(job.params ?? []).some((p) => p.name === k))
          return grpcError("INVALID_ARGUMENT", `invalid parameter: job ${id} takes no "${k}"`);
      const run: JobRun = {
        summary: {
          id: `0192dd00-0000-7000-8000-${String(1000 + db.nextId++).padStart(12, "0")}`,
          jobId: id,
          status: "JOB_RUN_STATUS_RUNNING",
          trigger: "JOB_TRIGGER_MANUAL",
          triggeredBy: principal(request) === "config" ? "api_keys setting" : "admin",
          startedAt: now(),
          counts: {},
        },
        params,
        notes: [],
        progress: { phase: "work", done: "250", total: "1000", unit: "items" },
      };
      db.jobRuns = [run, ...db.jobRuns];
      return ok({ run: run.summary });
    }),

    http.get(`${base}/admin/job-runs/:runId`, async ({ request, params }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      const run = db.jobRuns.find((r) => r.summary?.id === params.runId);
      return run ? ok({ run: settle(run) }) : grpcError("NOT_FOUND", `not found: no run ${params.runId}`);
    }),

    http.post(pattern(base, "/admin/job-runs/([^/:]+):cancel$"), async ({ request }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      const id = new URL(request.url).pathname
        .split("/")
        .pop()
        ?.replace(/:cancel$/, "");
      const run = db.jobRuns.find((r) => r.summary?.id === id);
      if (!run?.summary) return grpcError("NOT_FOUND", `not found: no run ${id}`);
      if (run.summary.status !== "JOB_RUN_STATUS_RUNNING")
        return grpcError("FAILED_PRECONDITION", "the run isn't running in this meta process");
      run.summary.status = "JOB_RUN_STATUS_CANCELLED";
      run.summary.message = "cancelled";
      run.summary.finishedAt = now();
      run.progress = undefined;
      return ok({ run: run.summary });
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

    http.put(`${base}/admin/pushed-profiles/:providerId`, async ({ request, params }) => {
      await wait();
      const denied = guard(request, true);
      if (denied) return denied;
      const providerId = String(params.providerId);
      if (!db.providers.some((p) => p.id === providerId))
        return grpcError("NOT_FOUND", `not found: no provider "${providerId}" is configured`);
      const body = (await request.json()) as { profile?: Pushed["profile"] };
      if (!body.profile) db.pushed.delete(providerId);
      else {
        const { password, ...proxy } = body.profile.proxy ?? {};
        db.pushed.set(providerId, {
          profile: { ...body.profile, ...(body.profile.proxy ? { proxy } : {}) },
          passwordSet: !!password,
          at: new Date().toISOString(),
        });
      }
      return ok({});
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
